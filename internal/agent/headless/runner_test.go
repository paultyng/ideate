package headless

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFakeClaude installs a shell script at <dir>/claude that mimics
// the wire format we care about. The script reads its prompt from
// stdin, then prints a canned stream-json transcript that ends with a
// text_delta of the prompt itself plus a `result` frame. Tests use
// this to exercise the full Runner pipeline (spawn → translator
// goroutine → reader → decoder) without needing the real claude CLI.
func writeFakeClaude(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncat >/dev/null\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return bin
}

func TestClaudeRunner_HappyPath(t *testing.T) {
	t.Parallel()
	bin := writeFakeClaude(t, `cat <<'EOF'
{"type":"system","subtype":"init","cwd":"/tmp"}
{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_1"}}}
{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hey "}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Paul."}}}
{"type":"result","subtype":"success","is_error":false,"result":"Hey Paul.","session_id":"s1"}
EOF`)

	r := &ClaudeRunner{Binary: bin}
	rc, err := r.Run(context.Background(), "say hi", Opts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := DrainText(context.Background(), rc)
	if err != nil {
		t.Fatalf("DrainText: %v", err)
	}
	if got != "Hey Paul." {
		t.Errorf("got %q, want %q", got, "Hey Paul.")
	}
}

func TestClaudeRunner_PropagatesError(t *testing.T) {
	t.Parallel()
	bin := writeFakeClaude(t, `cat <<'EOF'
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}}
{"type":"result","subtype":"error_max_turns","is_error":true,"result":"max_turns_exceeded"}
EOF`)
	r := &ClaudeRunner{Binary: bin}
	rc, err := r.Run(context.Background(), "p", Opts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := DrainText(context.Background(), rc)
	if err == nil || !strings.Contains(err.Error(), "max_turns_exceeded") {
		t.Errorf("err = %v, want one containing max_turns_exceeded", err)
	}
	if got != "partial" {
		t.Errorf("got = %q, want %q", got, "partial")
	}
}

func TestClaudeRunner_NonZeroExitSurfacesAsError(t *testing.T) {
	t.Parallel()
	// Script writes no JSON and exits non-zero with stderr text.
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncat >/dev/null\necho 'auth required' >&2\nexit 2\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := &ClaudeRunner{Binary: bin}
	rc, err := r.Run(context.Background(), "p", Opts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer func() { _ = rc.Close() }()
	d := NewDecoder(rc)
	var sawError bool
	for {
		ev, err := d.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if ev.Kind == EventError {
			sawError = true
			if !strings.Contains(ev.Err, "auth required") {
				t.Errorf("error %q missing stderr tail", ev.Err)
			}
		}
	}
	if !sawError {
		t.Errorf("expected an EventError, got none")
	}
}

func TestClaudeRunner_CtxCancellationKillsSubprocess(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	// Script sleeps forever — only a kill ends it.
	script := "#!/bin/sh\ncat >/dev/null\nsleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &ClaudeRunner{Binary: bin}
	rc, err := r.Run(ctx, "p", Opts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Cancel after a beat — exec.CommandContext should kill the child.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	// Reader returns EOF (or error event) once the subprocess dies.
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 1024)
	for time.Now().Before(deadline) {
		n, err := rc.Read(buf)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			return
		}
		if n == 0 {
			break
		}
	}
	t.Errorf("subprocess did not exit within 5s after ctx cancel")
}

func TestClaudeRunner_BinaryNotFound(t *testing.T) {
	t.Parallel()
	r := &ClaudeRunner{Binary: filepath.Join(t.TempDir(), "definitely-not-here")}
	_, err := r.Run(context.Background(), "p", Opts{})
	if err == nil {
		t.Errorf("expected error for missing binary, got nil")
	}
}

// Smoke test that the translator goroutine doesn't deadlock when the
// consumer closes the reader before the subprocess finishes writing.
func TestClaudeRunner_ConsumerCloseStopsRunner(t *testing.T) {
	t.Parallel()
	bin := writeFakeClaude(t, `cat <<'EOF'
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a"}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"b"}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"c"}}}
{"type":"result","subtype":"success","is_error":false,"result":"abc"}
EOF`)
	r := &ClaudeRunner{Binary: bin}
	rc, err := r.Run(context.Background(), "p", Opts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Read a single byte then close — the translator goroutine should
	// notice the closed pipe and exit without leaking.
	one := make([]byte, 1)
	_, _ = rc.Read(one)
	if err := rc.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// Sanity check on the runReadCloser wiring: a clean drain via the
// real Decoder pipeline (mimics the production summarizer call site).
func TestClaudeRunner_DecoderPipeline(t *testing.T) {
	t.Parallel()
	bin := writeFakeClaude(t, `cat <<'EOF'
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"line one\n"}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"line two"}}}
{"type":"result","subtype":"success","is_error":false,"result":"line one\nline two"}
EOF`)
	r := &ClaudeRunner{Binary: bin}
	rc, err := r.Run(context.Background(), "p", Opts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got bytes.Buffer
	d := NewDecoder(rc)
	for {
		ev, err := d.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if ev.Kind == EventTextDelta {
			got.WriteString(ev.Delta)
		}
	}
	_ = rc.Close()
	if got.String() != "line one\nline two" {
		t.Errorf("got %q", got.String())
	}
}

// Opts.WorkingDir must propagate to the subprocess's cwd. The fake
// claude prints its own $PWD as a text_delta; the runner under test
// is expected to set cmd.Dir to opts.WorkingDir.
func TestClaudeRunner_WorkingDirSetsCwd(t *testing.T) {
	t.Parallel()

	// Script emits one text_delta containing $PWD (escaped so the
	// JSON parses), then a success result.
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"%s\"}}}\\n' \"$PWD\"\nprintf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"\",\"session_id\":\"s\"}\\n'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	workdir := t.TempDir()
	r := &ClaudeRunner{Binary: bin}
	rc, err := r.Run(context.Background(), "p", Opts{WorkingDir: workdir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := DrainText(context.Background(), rc)
	if err != nil {
		t.Fatalf("DrainText: %v", err)
	}
	// macOS tempdirs resolve through /private/var/folders/... so do a
	// suffix match against the trailing path segment rather than an
	// equality check.
	if !strings.HasSuffix(got, filepath.Base(workdir)) {
		t.Errorf("subprocess cwd = %q, want suffix %q", got, filepath.Base(workdir))
	}
}

// Empty Opts.WorkingDir must leave cmd.Dir alone (subprocess
// inherits the caller's cwd). Asserts the field's optionality.
func TestClaudeRunner_EmptyWorkingDirInherits(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"%s\"}}}\\n' \"$PWD\"\nprintf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"\",\"session_id\":\"s\"}\\n'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	parentCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	r := &ClaudeRunner{Binary: bin}
	rc, err := r.Run(context.Background(), "p", Opts{}) // no WorkingDir
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := DrainText(context.Background(), rc)
	if err != nil {
		t.Fatalf("DrainText: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Base(parentCwd)) {
		t.Errorf("subprocess cwd = %q, want suffix %q (inherited from parent)", got, filepath.Base(parentCwd))
	}
}
