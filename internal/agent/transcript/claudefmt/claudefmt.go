// Package claudefmt reads and writes Claude Code session transcripts
// (.jsonl files under ~/.claude/projects/<encoded-cwd>/<session-id>.jsonl).
//
// Only the small subset of fields that the sync code cares about is
// modeled: timestamps, entrypoint, sessionId, cwd, and message role. The
// real Claude format has many more fields — we tolerate them by using
// json.RawMessage for unknown keys at the record level (i.e. we ignore
// them).
package claudefmt

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/paultyng/ideate/internal/agent/transcript"
)

// EncodeProjectDir returns the project directory name Claude uses for the
// given absolute working directory. Encoding is "/" → "-" applied to the
// absolute path, keeping the leading slash as a leading "-".
//
// Example: /Users/paul/ideas/foo → -Users-paul-ideas-foo
//
// Decoding is intentionally unimplemented — it's lossy ("a-b" could mean
// "/a-b" or "/a/b"). Callers that need to compare must encode each known
// cwd and compare against directory entries.
func EncodeProjectDir(absCwd string) string {
	return strings.ReplaceAll(absCwd, string(filepath.Separator), "-")
}

// Record models the small slice of a Claude transcript record that the
// sync code reads. Unknown fields are ignored.
type Record struct {
	Type       string    `json:"type,omitempty"`       // "user" | "assistant" | "summary" | ...
	SessionID  string    `json:"sessionId,omitempty"`  // stable session UUID
	CWD        string    `json:"cwd,omitempty"`        // absolute working directory
	Entrypoint string    `json:"entrypoint,omitempty"` // "cli" | "claude-desktop" | "sdk-cli"
	Timestamp  time.Time `json:"timestamp"`
}

// Meta is the digest that sync extracts from a single jsonl file.
// First/Last timestamps come from the earliest/latest non-zero record;
// Entrypoint is the first non-empty value seen (stable per file).
type Meta struct {
	SessionID  string
	CWD        string
	Entrypoint string
	First      time.Time
	Last       time.Time
	HasContent bool // at least one user/assistant record present
}

// Parse reads a jsonl file and returns its meta. Tolerates malformed lines
// (skipped). Returns (nil, nil) for an empty / unparseable file so the
// caller can decide whether to skip ingest.
func Parse(path string) (*Meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	var meta Meta
	sc := bufio.NewScanner(f)
	// Some Claude transcripts have very long lines (large tool outputs).
	// Bump the buffer so we can still read them.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			continue // tolerate per-line errors
		}
		if r.SessionID != "" && meta.SessionID == "" {
			meta.SessionID = r.SessionID
		}
		if r.CWD != "" && meta.CWD == "" {
			meta.CWD = r.CWD
		}
		if r.Entrypoint != "" && meta.Entrypoint == "" {
			meta.Entrypoint = r.Entrypoint
		}
		if r.Type == "user" || r.Type == "assistant" {
			meta.HasContent = true
		}
		if !r.Timestamp.IsZero() {
			if meta.First.IsZero() || r.Timestamp.Before(meta.First) {
				meta.First = r.Timestamp
			}
			if r.Timestamp.After(meta.Last) {
				meta.Last = r.Timestamp
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scanning transcript: %w", err)
	}
	if meta.SessionID == "" {
		return nil, nil
	}
	return &meta, nil
}

// IsInteractive returns true for entrypoints we treat as interactive
// (and therefore worth ingesting into Ideate's history). Empty entrypoint
// is treated as non-interactive — defensive default for unknown formats.
func IsInteractive(entrypoint string) bool {
	return entrypoint == "cli" || entrypoint == "claude-desktop"
}

// NewWriter returns a transcript.Writer that emits Claude-format jsonl
// records to <projectsDir>/<encoded-cwd>/<sessionID>.jsonl. The projects
// dir is created on Start; the file is opened append-only so concurrent
// writers never overwrite each other's records (none expected today).
func NewWriter(projectsDir string) transcript.Writer {
	return &writer{projectsDir: projectsDir}
}

type writer struct {
	projectsDir string

	mu        sync.Mutex
	f         *os.File
	sessionID string
	cwd       string
	closed    bool
}

func (w *writer) Start(_ context.Context, sessionID, cwd string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f != nil {
		return errors.New("writer already started")
	}
	if sessionID == "" || cwd == "" {
		return errors.New("sessionID and cwd are required")
	}

	dir := filepath.Join(w.projectsDir, EncodeProjectDir(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating project dir: %w", err)
	}

	path := filepath.Join(dir, sessionID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("opening transcript: %w", err)
	}
	w.f = f
	w.sessionID = sessionID
	w.cwd = cwd

	// Emit a header record so Parse picks up entrypoint/cwd/sessionID.
	if err := w.write(Record{
		Type:       "user", // any user/assistant qualifies as "has content"
		SessionID:  sessionID,
		CWD:        cwd,
		Entrypoint: "cli",
		Timestamp:  time.Now(),
	}); err != nil {
		return err
	}
	return nil
}

func (w *writer) Append(_ context.Context, role, _ string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil || w.closed {
		return nil
	}
	t := role
	if t != "user" && t != "assistant" {
		t = "user"
	}
	return w.write(Record{
		Type:      t,
		SessionID: w.sessionID,
		CWD:       w.cwd,
		Timestamp: time.Now(),
	})
}

func (w *writer) End(_ context.Context, _ int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil || w.closed {
		return nil
	}
	w.closed = true
	return w.f.Close()
}

func (w *writer) write(r Record) error {
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now()
	}
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshaling transcript record: %w", err)
	}
	if _, err := io.WriteString(w.f, string(data)+"\n"); err != nil {
		return fmt.Errorf("writing transcript record: %w", err)
	}
	return nil
}
