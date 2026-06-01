package agent

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestSession_WriteAndRead(t *testing.T) {
	t.Parallel()
	requirePTY(t)

	cmd := exec.Command("cat")
	cmd.SysProcAttr = newSysProcAttr()

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("starting cat: %v", err)
	}

	var mu sync.Mutex
	var buf bytes.Buffer
	outputFunc := func(data []byte) {
		mu.Lock()
		buf.Write(data)
		mu.Unlock()
	}

	s := newSession("test-1", "test", "cat", ptmx, cmd, outputFunc, sessionInit{})
	t.Cleanup(func() { _ = s.Stop() })

	if err := s.Write([]byte("hello\n")); err != nil {
		t.Fatalf("writing to session: %v", err)
	}

	// Wait for the echo to come back.
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		got := buf.String()
		mu.Unlock()
		if strings.Contains(got, "hello") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for echo; got %q", got)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	info := s.Info()
	if info.Status != StatusRunning {
		t.Errorf("expected status %q, got %q", StatusRunning, info.Status)
	}
}

func TestSession_Resize(t *testing.T) {
	t.Parallel()
	requirePTY(t)

	cmd := exec.Command("cat")
	cmd.SysProcAttr = newSysProcAttr()

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("starting cat: %v", err)
	}

	s := newSession("test-resize", "resize-test", "cat", ptmx, cmd, nil, sessionInit{})
	t.Cleanup(func() { _ = s.Stop() })

	if err := s.Resize(40, 120); err != nil {
		t.Errorf("resize returned error: %v", err)
	}
}

func TestSession_Stop(t *testing.T) {
	t.Parallel()
	requirePTY(t)

	cmd := exec.Command("cat")
	cmd.SysProcAttr = newSysProcAttr()

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("starting cat: %v", err)
	}

	s := newSession("test-stop", "stop-test", "cat", ptmx, cmd, nil, sessionInit{})

	if err := s.Stop(); err != nil {
		t.Fatalf("stop returned error: %v", err)
	}

	select {
	case <-s.Wait():
		// OK
	case <-time.After(10 * time.Second):
		t.Fatal("done channel not closed after stop")
	}

	info := s.Info()
	if info.Status != StatusStopped {
		t.Errorf("expected status %q, got %q", StatusStopped, info.Status)
	}
}

// Regression for a deadlock in the drain pipeline: vt.Emulator's response
// handlers (DA1, cursor-position-report, etc.) push bytes into an
// unbuffered internal io.Pipe. Without a reader, the first such handler
// blocks the drainLoop under the screen mutex and the outputFunc callback
// never fires — frontend renders nothing. The vscreen package now spawns
// a drain goroutine; this test guards the integration: feed query escapes
// through a real PTY and assert the post-query bytes still reach the
// callback.
func TestSession_OutputFlowsWhenInputContainsResponseQueries(t *testing.T) {
	t.Parallel()
	requirePTY(t)

	cmd := exec.Command("cat")
	cmd.SysProcAttr = newSysProcAttr()

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("starting cat: %v", err)
	}

	var mu sync.Mutex
	var buf bytes.Buffer
	outputFunc := func(data []byte) {
		mu.Lock()
		buf.Write(data)
		mu.Unlock()
	}

	s := newSession("test-queries", "test", "cat", ptmx, cmd, outputFunc, sessionInit{})
	t.Cleanup(func() { _ = s.Stop() })

	// DA1 + CPR queries followed by a marker. cat echoes everything back,
	// so the drainLoop sees these bytes and feeds them to vscreen.
	if err := s.Write([]byte("\x1b[c\x1b[6nMARKER\n")); err != nil {
		t.Fatalf("writing to session: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		got := buf.String()
		mu.Unlock()
		if strings.Contains(got, "MARKER") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("post-query marker never delivered to outputFunc — drain deadlock regression. got=%q", got)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestSession_DoneOnExit(t *testing.T) {
	t.Parallel()
	requirePTY(t)

	cmd := exec.Command("echo", "bye")
	cmd.SysProcAttr = newSysProcAttr()

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("starting echo: %v", err)
	}

	s := newSession("test-done", "done-test", "echo", ptmx, cmd, nil, sessionInit{})

	select {
	case <-s.Wait():
		// OK
	case <-time.After(10 * time.Second):
		t.Fatal("done channel not closed after process exit")
	}

	info := s.Info()
	if info.Status != StatusExited {
		t.Errorf("expected status %q, got %q", StatusExited, info.Status)
	}
}

// TestSession_NaturalExitWritesSnapshot guards that the on-stop snapshot is
// written when the process exits on its own (PTY EOF path), without an explicit
// Stop() call. Previously readLoop returned without calling screen.Close(), so
// the on-disk snapshot was silently skipped for the most common exit path.
func TestSession_NaturalExitWritesSnapshot(t *testing.T) {
	t.Parallel()
	requirePTY(t)

	dir := t.TempDir()
	snapPath := filepath.Join(dir, "test.snapshot.ans")

	// printf writes output then exits — natural process exit, no Stop().
	cmd := exec.Command("printf", "natural-exit-marker\r\n")
	cmd.SysProcAttr = newSysProcAttr()

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("starting printf: %v", err)
	}

	s := newSession("test-natural-snap", "snap-test", "printf", ptmx, cmd, func([]byte) {}, sessionInit{
		snapshotPath: snapPath,
	})

	select {
	case <-s.Wait():
		// OK — process exited naturally
	case <-time.After(10 * time.Second):
		t.Fatal("session did not exit within timeout")
	}

	// readLoop must have called screen.Close() before closing done, which
	// triggers the final snapshot write. Assert the file exists and is non-empty.
	data, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("snapshot not written after natural exit: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("snapshot file is empty after natural exit")
	}
	if !bytes.Contains(data, []byte("natural-exit-marker")) {
		t.Errorf("snapshot missing expected output; got %q", data)
	}
}
