package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/paultyng/ideate/internal/claudecode"
)

func TestNewCreatesDir(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "nested", "captures")
	r, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if r.Dir() != dir {
		t.Errorf("Dir = %q, want %q", r.Dir(), dir)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir not created: %v", err)
	}
}

func TestNewEmptyDirRejected(t *testing.T) {
	t.Parallel()
	if _, err := New(""); err == nil {
		t.Fatal("expected error for empty dir")
	}
}

// TestWrapHooks runs a table of hook events through the middleware and
// asserts each is captured to its own file with the expected payload.
func TestWrapHooks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		path      string
		event     string
		body      string
		ct        string
		sessionID string
		wantBody  any
	}{
		{
			name:      "stop json",
			path:      "/hooks/stop",
			event:     "stop",
			body:      `{"reason":"done"}`,
			ct:        "application/json",
			sessionID: "ses-1",
			wantBody:  map[string]any{"reason": "done"},
		},
		{
			name:      "tool-use json",
			path:      "/hooks/tool-use",
			event:     "tool-use",
			body:      `{"tool_name":"Read","tool_input":{"path":"/x"}}`,
			ct:        "application/json",
			sessionID: "ses-1",
			wantBody: map[string]any{
				"tool_name":  "Read",
				"tool_input": map[string]any{"path": "/x"},
			},
		},
		{
			name:      "end raw text",
			path:      "/hooks/end",
			event:     "end",
			body:      `not-json`,
			ct:        "text/plain",
			sessionID: "ses-2",
			wantBody:  "not-json",
		},
		{
			name:      "missing session header",
			path:      "/hooks/stop",
			event:     "stop",
			body:      `{"x":1}`,
			ct:        "application/json",
			sessionID: "",
			wantBody:  map[string]any{"x": float64(1)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			rec, err := New(dir)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			// Downstream handler verifies body is intact (middleware must replay it).
			var seen []byte
			downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.ct)
			req.Header.Set("Authorization", "Bearer SECRET")
			if tc.sessionID != "" {
				req.Header.Set(claudecode.SessionHeader, tc.sessionID)
			}
			w := httptest.NewRecorder()
			rec.WrapHooks(downstream).ServeHTTP(w, req)

			if string(seen) != tc.body {
				t.Errorf("downstream body = %q, want %q", seen, tc.body)
			}

			wantSession := tc.sessionID
			if wantSession == "" {
				wantSession = unknownSession
			}
			files := listFiles(t, filepath.Join(dir, wantSession, "hooks"))
			if len(files) != 1 {
				t.Fatalf("expected 1 capture file, got %d: %v", len(files), files)
			}
			name := filepath.Base(files[0])
			wantSuffix := "-" + tc.event + ".json"
			if !strings.HasSuffix(name, wantSuffix) {
				t.Errorf("filename %q missing suffix %q", name, wantSuffix)
			}
			if !strings.HasPrefix(name, "0001-") {
				t.Errorf("filename %q missing 4-digit counter prefix", name)
			}

			rec2 := readJSONFile(t, files[0])
			if got := rec2["method"]; got != "POST" {
				t.Errorf("method = %v, want POST", got)
			}
			if got := rec2["path"]; got != tc.path {
				t.Errorf("path = %v, want %v", got, tc.path)
			}
			if _, ok := rec2["timestamp"].(string); !ok {
				t.Errorf("timestamp missing or wrong type: %v", rec2["timestamp"])
			}
			headers, _ := rec2["headers"].(map[string]any)
			if _, hasAuth := headers["Authorization"]; hasAuth {
				t.Errorf("Authorization header should be filtered out: %v", headers)
			}
			if !equalJSON(rec2["body"], tc.wantBody) {
				t.Errorf("body = %v, want %v", rec2["body"], tc.wantBody)
			}
		})
	}
}

// TestWrapHooksCounterMonotonic asserts that the per-session counter
// increments across multiple captures.
func TestWrapHooksCounterMonotonic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rec, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	noop := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := rec.WrapHooks(noop)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/hooks/stop", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(claudecode.SessionHeader, "ses-x")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	files := listFiles(t, filepath.Join(dir, "ses-x", "hooks"))
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d: %v", len(files), files)
	}
	for i, f := range files {
		want := fmt.Sprintf("%04d-stop.json", i+1)
		if filepath.Base(f) != want {
			t.Errorf("file[%d] = %s, want %s", i, filepath.Base(f), want)
		}
	}
}

// TestWrapMCPJSON exercises the non-SSE path: a single JSON-RPC request in,
// a single JSON-RPC response out.
func TestWrapMCPJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rec, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo a JSON-RPC response and verify request body was preserved.
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(`"tools/call"`)) {
			t.Errorf("downstream did not see request body: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`))
	})

	reqBody := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"x"}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(claudecode.SessionHeader, "ses-mcp")
	w := httptest.NewRecorder()
	rec.WrapMCP(downstream).ServeHTTP(w, req)

	mcpDir := filepath.Join(dir, "ses-mcp", "mcp")
	files := listFiles(t, mcpDir)
	// meta.json + 0001-in-tools_call.json + 0002-out-result-7.json.
	if len(files) != 3 {
		t.Fatalf("expected 3 files (meta + in + out), got %d: %v", len(files), files)
	}

	// meta.json
	meta := readJSONFile(t, filepath.Join(mcpDir, "meta.json"))
	if _, ok := meta["timestamp"].(string); !ok {
		t.Errorf("meta missing timestamp: %v", meta)
	}

	in := readJSONFile(t, filepath.Join(mcpDir, "0001-in-tools_call.json"))
	if in["direction"] != "in" {
		t.Errorf("in direction = %v", in["direction"])
	}
	frame, _ := in["frame"].(map[string]any)
	if frame["method"] != "tools/call" {
		t.Errorf("in frame method = %v", frame["method"])
	}

	out := readJSONFile(t, filepath.Join(mcpDir, "0002-out-result-7.json"))
	if out["direction"] != "out" {
		t.Errorf("out direction = %v", out["direction"])
	}
	frame, _ = out["frame"].(map[string]any)
	if result, _ := frame["result"].(map[string]any); result["ok"] != true {
		t.Errorf("out frame result = %v", frame["result"])
	}
}

// TestWrapMCPSSE exercises the streaming path: server emits multiple SSE
// events, each carrying a JSON-RPC frame.
func TestWrapMCPSSE(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rec, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Two frames in two events, plus a notification.
		_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"a\":1}}\n\n")
		_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"p\":50}}\n\n")
		// Trailing event with no terminator — exercise flushFinal.
		_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"b\":2}}\n\n")
	})

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(claudecode.SessionHeader, "ses-sse")
	w := httptest.NewRecorder()
	rec.WrapMCP(downstream).ServeHTTP(w, req)

	mcpDir := filepath.Join(dir, "ses-sse", "mcp")
	files := listFiles(t, mcpDir)
	// Expect: meta.json + 0001-in-initialize + 0002-out-result-1 + 0003-out-notifications_progress + 0004-out-result-2.
	if len(files) != 5 {
		t.Fatalf("expected 5 files, got %d: %v", len(files), files)
	}

	gotNames := make([]string, 0, len(files))
	for _, f := range files {
		gotNames = append(gotNames, filepath.Base(f))
	}
	wantNames := []string{
		"0001-in-initialize.json",
		"0002-out-result-1.json",
		"0003-out-notifications_progress.json",
		"0004-out-result-2.json",
		"meta.json",
	}
	sort.Strings(gotNames)
	sort.Strings(wantNames)
	for i := range wantNames {
		if gotNames[i] != wantNames[i] {
			t.Errorf("file[%d] = %s, want %s", i, gotNames[i], wantNames[i])
		}
	}
}

// TestWrapMCPMissingSessionUsesUnknown ensures captures still land somewhere
// when the X-Ideate-Session-Id header is absent.
func TestWrapMCPMissingSessionUsesUnknown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rec, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	rec.WrapMCP(downstream).ServeHTTP(httptest.NewRecorder(), req)

	if _, err := os.Stat(filepath.Join(dir, unknownSession, "mcp")); err != nil {
		t.Errorf("missing unknown-session capture dir: %v", err)
	}
}

// TestSanitizeSessionID confirms filesystem-unsafe characters are replaced.
func TestSanitizeSessionID(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":              unknownSession,
		"abc":           "abc",
		"a/b":           "a-b",
		"a\\b":          "a-b",
		"a:b":           "a-b",
		"foo/bar:baz/x": "foo-bar-baz-x",
	}
	for in, want := range cases {
		if got := sanitizeSessionID(in); got != want {
			t.Errorf("sanitizeSessionID(%q) = %q, want %q", in, got, want)
		}
	}
}

// listFiles returns absolute paths of files in dir, sorted.
func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out
}

// readJSONFile reads and decodes a JSON file as a map.
func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal %s: %v", path, err)
	}
	return m
}

// equalJSON compares two values via JSON round-trip — handy for comparing
// decoded maps where number types may differ.
func equalJSON(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}
