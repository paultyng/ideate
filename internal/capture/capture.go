// Package capture provides opt-in HTTP middleware that records hook POST
// bodies and MCP JSON-RPC frames to disk for fixture generation and debugging.
//
// Activation: when constructed via New with a non-empty directory (typically
// from the IDEATE_CAPTURE_DIR env var), the Recorder's WrapHooks and WrapMCP
// methods produce middleware that tees request/response bodies to disk under
// <dir>/<session-id>/{hooks,mcp}/. When the directory is empty, callers should
// not wrap at all — capture is observability, not a correctness path, and
// errors writing captures are logged but never fail the request.
package capture

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/paultyng/ideate/internal/claudecode"
)

// EnvVar is the environment variable that activates capture mode.
const EnvVar = "IDEATE_CAPTURE_DIR"

const unknownSession = "unknown-session"

// Recorder captures hook POST bodies and MCP JSON-RPC frames to disk.
type Recorder struct {
	dir string

	mu       sync.Mutex
	counters map[string]int // key: "<session>/<subtype>"
}

// New constructs a Recorder rooted at dir. The directory is created with
// 0o755 if it does not exist. dir must be non-empty.
func New(dir string) (*Recorder, error) {
	if dir == "" {
		return nil, fmt.Errorf("capture: empty directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("capture: creating dir %q: %w", dir, err)
	}
	return &Recorder{
		dir:      dir,
		counters: make(map[string]int),
	}, nil
}

// Dir returns the recorder's root directory.
func (r *Recorder) Dir() string { return r.dir }

// nextCounter returns the next 4-digit counter for the given session and
// subtype, monotonically increasing per (session, subtype).
func (r *Recorder) nextCounter(session, subtype string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := session + "/" + subtype
	r.counters[key]++
	return r.counters[key]
}

// sanitizeSessionID makes a session ID safe for use as a directory name.
// Replaces path separators and colons with dashes; preserves the rest.
func sanitizeSessionID(s string) string {
	if s == "" {
		return unknownSession
	}
	r := strings.NewReplacer("/", "-", "\\", "-", ":", "-")
	out := r.Replace(s)
	if out == "" {
		return unknownSession
	}
	return out
}

// filterHeaders returns a copy of h with sensitive entries removed.
// Currently strips Authorization only.
func filterHeaders(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, v := range h {
		if strings.EqualFold(k, "Authorization") {
			continue
		}
		out[k] = append([]string(nil), v...)
	}
	return out
}

// writeJSON marshals v and writes it to path under the recorder's root.
// Errors are logged, never returned, since capture is best-effort.
func (r *Recorder) writeJSON(relPath string, v any) {
	full := filepath.Join(r.dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		slog.Warn("capture: mkdir", slog.String("path", full), slog.Any("err", err))
		return
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		slog.Warn("capture: marshal", slog.String("path", full), slog.Any("err", err))
		return
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		slog.Warn("capture: write", slog.String("path", full), slog.Any("err", err))
	}
}

// WrapHooks wraps a hooks http.Handler with capture middleware. Each POST
// request body is captured to <dir>/<session>/hooks/NNNN-<event>.json.
func (r *Recorder) WrapHooks(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Only capture POSTs (the hook server rejects others anyway).
		if req.Method != http.MethodPost {
			next.ServeHTTP(w, req)
			return
		}

		body, err := io.ReadAll(req.Body)
		if err != nil {
			// Pass through; the downstream handler will report the read error.
			slog.Warn("capture: read hook body", slog.Any("err", err))
			next.ServeHTTP(w, req)
			return
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))

		session := sanitizeSessionID(req.Header.Get(claudecode.SessionHeader))
		event := lastPathSegment(req.URL.Path)
		if event == "" {
			event = "unknown"
		}

		n := r.nextCounter(session, "hooks")
		rel := filepath.Join(session, "hooks", fmt.Sprintf("%04d-%s.json", n, event))

		record := map[string]any{
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"method":    req.Method,
			"path":      req.URL.Path,
			"headers":   filterHeaders(req.Header),
			"body":      decodeBody(req.Header.Get("Content-Type"), body),
		}
		r.writeJSON(rel, record)

		next.ServeHTTP(w, req)
	})
}

// WrapMCP wraps the MCP http.Handler with capture middleware. The request
// body (a JSON-RPC frame) is captured as direction "in", and the response
// body is captured frame-by-frame as direction "out". Both plain
// application/json and text/event-stream (SSE) responses are supported.
func (r *Recorder) WrapMCP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			slog.Warn("capture: read mcp body", slog.Any("err", err))
			next.ServeHTTP(w, req)
			return
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))

		session := sanitizeSessionID(req.Header.Get(claudecode.SessionHeader))

		// Per-exchange meta.json (one per HTTP exchange, written each time —
		// last-write wins is fine; debug feature). Includes remote_addr and
		// the request headers.
		metaRel := filepath.Join(session, "mcp", "meta.json")
		r.writeJSON(metaRel, map[string]any{
			"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
			"headers":     filterHeaders(req.Header),
			"remote_addr": req.RemoteAddr,
		})

		// Capture inbound frame(s). The body may be a single JSON-RPC object
		// or a JSON-RPC batch (array) — handle both.
		r.captureFrames(session, "in", body)

		// Wrap the response writer to tee outbound bytes for parsing once
		// the handler completes (or as data flushes for SSE).
		rec := &mcpRecorder{
			ResponseWriter: w,
			recorder:       r,
			session:        session,
		}
		next.ServeHTTP(rec, req)
		rec.flushFinal()
	})
}

// captureFrames parses one or more JSON-RPC frames out of raw bytes and
// writes each as <session>/mcp/NNNN-<dir>-<method-or-id>.json.
func (r *Recorder) captureFrames(session, direction string, raw []byte) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return
	}
	// JSON-RPC permits batches (arrays). Try array first; fall back to single.
	var batch []json.RawMessage
	if err := json.Unmarshal(raw, &batch); err == nil {
		for _, frame := range batch {
			r.writeFrame(session, direction, frame)
		}
		return
	}
	r.writeFrame(session, direction, raw)
}

// writeFrame writes a single JSON-RPC frame to disk.
func (r *Recorder) writeFrame(session, direction string, frame []byte) {
	frame = bytes.TrimSpace(frame)
	if len(frame) == 0 {
		return
	}
	var obj map[string]any
	label := frameLabel(frame, &obj)

	n := r.nextCounter(session, "mcp")
	rel := filepath.Join(session, "mcp", fmt.Sprintf("%04d-%s-%s.json", n, direction, label))

	var frameValue any = obj
	if obj == nil {
		// Non-JSON or non-object frame — preserve as raw string.
		frameValue = string(frame)
	}

	record := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"direction": direction,
		"frame":     frameValue,
	}
	r.writeJSON(rel, record)
}

// frameLabel decodes frame and returns a filesystem-safe label derived from
// the JSON-RPC method (for requests/notifications) or "result-<id>" / "error-<id>"
// for responses. obj receives the decoded frame on success.
func frameLabel(frame []byte, obj *map[string]any) string {
	if err := json.Unmarshal(frame, obj); err != nil {
		return "raw"
	}
	if m, ok := (*obj)["method"].(string); ok && m != "" {
		return safeLabel(m)
	}
	id := (*obj)["id"]
	idStr := "noid"
	if id != nil {
		idStr = fmt.Sprintf("%v", id)
	}
	if _, hasErr := (*obj)["error"]; hasErr {
		return "error-" + safeLabel(idStr)
	}
	return "result-" + safeLabel(idStr)
}

// safeLabel makes a method/id string safe to embed in a filename.
func safeLabel(s string) string {
	if s == "" {
		return "anon"
	}
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return r.Replace(s)
}

// lastPathSegment returns the final segment of a URL path.
func lastPathSegment(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// decodeBody returns the body parsed as JSON if the Content-Type is JSON,
// otherwise the raw string.
func decodeBody(contentType string, body []byte) any {
	if isJSONContentType(contentType) {
		var v any
		if err := json.Unmarshal(body, &v); err == nil {
			return v
		}
	}
	return string(body)
}

func isJSONContentType(ct string) bool {
	ct = strings.ToLower(ct)
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct == "application/json" || strings.HasSuffix(ct, "+json")
}

// mcpRecorder wraps http.ResponseWriter to tee the response body. It buffers
// non-streaming responses (application/json) and parses SSE responses
// (text/event-stream) on the fly so each `data:` line is captured as it is
// flushed.
type mcpRecorder struct {
	http.ResponseWriter
	recorder *Recorder
	session  string

	wroteHeader bool
	isSSE       bool
	sseBuf      bytes.Buffer // accumulates an in-progress SSE event
	jsonBuf     bytes.Buffer // accumulates non-SSE response body
}

func (m *mcpRecorder) WriteHeader(code int) {
	if !m.wroteHeader {
		ct := m.Header().Get("Content-Type")
		m.isSSE = strings.HasPrefix(strings.ToLower(ct), "text/event-stream")
		m.wroteHeader = true
	}
	m.ResponseWriter.WriteHeader(code)
}

func (m *mcpRecorder) Write(p []byte) (int, error) {
	if !m.wroteHeader {
		// Implicit 200; capture content type from headers now.
		ct := m.Header().Get("Content-Type")
		m.isSSE = strings.HasPrefix(strings.ToLower(ct), "text/event-stream")
		m.wroteHeader = true
	}
	if m.isSSE {
		m.sseBuf.Write(p)
		m.consumeSSE()
	} else {
		m.jsonBuf.Write(p)
	}
	return m.ResponseWriter.Write(p)
}

// Flush implements http.Flusher when the underlying writer supports it,
// which mcp-go's StreamableHTTPServer relies on for SSE.
func (m *mcpRecorder) Flush() {
	if f, ok := m.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// consumeSSE drains complete events from sseBuf. SSE events are delimited
// by a blank line (\n\n). Within an event, lines starting with "data:" hold
// the JSON-RPC frame.
func (m *mcpRecorder) consumeSSE() {
	for {
		raw := m.sseBuf.Bytes()
		idx := bytes.Index(raw, []byte("\n\n"))
		if idx < 0 {
			// Try CRLF too.
			idx = bytes.Index(raw, []byte("\r\n\r\n"))
			if idx < 0 {
				return
			}
		}
		event := raw[:idx]
		// Advance past the delimiter (handle either \n\n or \r\n\r\n).
		skip := 2
		if bytes.HasPrefix(raw[idx:], []byte("\r\n\r\n")) {
			skip = 4
		}
		m.sseBuf.Next(idx + skip)
		m.handleSSEEvent(event)
	}
}

// handleSSEEvent parses one SSE event block and emits its data payload.
func (m *mcpRecorder) handleSSEEvent(event []byte) {
	// Concatenate all data: lines (per SSE spec, multiple data: lines join
	// with newlines). For JSON-RPC over SSE the payload is typically a
	// single line.
	var data bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(event))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimPrefix(line, "data:")
			payload = strings.TrimPrefix(payload, " ")
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(payload)
		}
	}
	if data.Len() > 0 {
		m.recorder.captureFrames(m.session, "out", data.Bytes())
	}
}

// flushFinal handles any pending data when the request completes. For SSE,
// the trailing event may have no terminator — flush whatever's buffered.
// For non-SSE, parse the buffered response body as JSON-RPC.
func (m *mcpRecorder) flushFinal() {
	if m.isSSE {
		// Flush trailing partial event, if any.
		if rem := bytes.TrimSpace(m.sseBuf.Bytes()); len(rem) > 0 {
			m.handleSSEEvent(rem)
		}
		return
	}
	if m.jsonBuf.Len() == 0 {
		return
	}
	m.recorder.captureFrames(m.session, "out", m.jsonBuf.Bytes())
}
