package agent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func buildTestAgent(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "testagent")
	// testagent is a go-tool dependency (see go.mod's `tool
	// github.com/paultyng/testagent` directive). `go build` against the
	// module path resolves through the module cache without needing an
	// in-tree main package.
	cmd := exec.Command("go", "build", "-o", bin, "github.com/paultyng/testagent")
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building testagent: %s\n%s", err, out)
	}
	return bin
}

func TestCoordinator_StartListStop(t *testing.T) {
	t.Parallel()
	requirePTY(t)

	bin := buildTestAgent(t)
	configDir := t.TempDir()
	coord := NewCoordinator(configDir)
	coord.RegisterRunner("testagent", &TestAgentRunner{BinaryPath: bin})

	ctx := context.Background()

	id, err := coord.Start(ctx, SessionConfig{
		Name:       "test session",
		WorkingDir: t.TempDir(),
		AgentUUID:  "uuid-" + t.Name(),
		AgentType:  "testagent",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if id == "" {
		t.Errorf("expected non-empty session ID")
	}

	sessions := coord.List()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Status != StatusRunning {
		t.Errorf("expected status %q, got %q", StatusRunning, sessions[0].Status)
	}

	// Verify manifest was written.
	manifests, err := scanManifests(configDir)
	if err != nil {
		t.Fatalf("scanManifests: %v", err)
	}
	if len(manifests) != 1 {
		t.Fatalf("expected 1 manifest, got %d", len(manifests))
	}

	if err := coord.Stop(ctx, id); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Wait for exit watcher to clean up.
	deadline := time.After(10 * time.Second)
	for len(coord.List()) > 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for session removal")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Manifest is cleaned up asynchronously by the exit watcher goroutine.
	manifestDeadline := time.After(5 * time.Second)
	for {
		manifests, err = scanManifests(configDir)
		if err != nil {
			t.Fatalf("scanManifests: %v", err)
		}
		if len(manifests) == 0 {
			break
		}
		select {
		case <-manifestDeadline:
			t.Fatalf("expected 0 manifests after stop, got %d", len(manifests))
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestCoordinator_Write(t *testing.T) {
	t.Parallel()
	requirePTY(t)

	bin := buildTestAgent(t)
	configDir := t.TempDir()

	var mu sync.Mutex
	var outputBuf bytes.Buffer

	coord := NewCoordinator(configDir)
	coord.RegisterRunner("testagent", &TestAgentRunner{BinaryPath: bin})
	coord.SetOutputHandler(func(_ string, data []byte) {
		mu.Lock()
		outputBuf.Write(data)
		mu.Unlock()
	})

	ctx := context.Background()
	id, err := coord.Start(ctx, SessionConfig{
		Name:       "write-test",
		WorkingDir: t.TempDir(),
		AgentUUID:  "uuid-" + t.Name(),
		AgentType:  "testagent",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { coord.Shutdown(ctx) })

	// Wait for the upstream testagent banner to appear before writing.
	// "/help for commands" is the last banner line; once it's present
	// the bubbletea TUI is ready for input.
	deadline := time.After(10 * time.Second)
	for {
		mu.Lock()
		got := outputBuf.String()
		mu.Unlock()
		if strings.Contains(got, "/help for commands") {
			break
		}
		select {
		case <-deadline:
			mu.Lock()
			t.Fatalf("timed out waiting for banner; got %q", outputBuf.String())
			mu.Unlock()
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Bubbletea reads stdin in raw mode — Enter is CR (\r), not LF.
	if err := coord.Write(id, []byte("ping\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Wait for the echoed prompt. The TUI echoes user input back into
	// history as "> ping" (see upstream internal/engine/tui.go).
	deadline = time.After(10 * time.Second)
	for {
		mu.Lock()
		got := outputBuf.String()
		mu.Unlock()
		if strings.Contains(got, "ping") {
			break
		}
		select {
		case <-deadline:
			mu.Lock()
			t.Fatalf("timed out waiting for echo; got %q", outputBuf.String())
			mu.Unlock()
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestCoordinator_StatusHandler(t *testing.T) {
	t.Parallel()
	requirePTY(t)

	bin := buildTestAgent(t)
	configDir := t.TempDir()

	var statusMu sync.Mutex
	var gotID, gotStatus string

	var outMu sync.Mutex
	var outBuf bytes.Buffer

	coord := NewCoordinator(configDir)
	coord.RegisterRunner("testagent", &TestAgentRunner{BinaryPath: bin})
	coord.SetOutputHandler(func(_ string, data []byte) {
		outMu.Lock()
		outBuf.Write(data)
		outMu.Unlock()
	})
	coord.SetStatusHandler(func(sessionID string, _ SessionMeta, status string, _ int) {
		statusMu.Lock()
		gotID = sessionID
		gotStatus = status
		statusMu.Unlock()
	})

	ctx := context.Background()
	id, err := coord.Start(ctx, SessionConfig{
		Name:       "status-test",
		WorkingDir: t.TempDir(),
		AgentUUID:  "uuid-" + t.Name(),
		AgentType:  "testagent",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for upstream's bubbletea TUI to finish initialization
	// before sending /exit — input written before raw mode is set up
	// goes to the PTY's line discipline and the slash command is
	// silently dropped.
	bannerDeadline := time.After(10 * time.Second)
	for {
		outMu.Lock()
		ready := strings.Contains(outBuf.String(), "/help for commands")
		outMu.Unlock()
		if ready {
			break
		}
		select {
		case <-bannerDeadline:
			t.Fatal("timed out waiting for banner before /exit")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Tell testagent to exit. Upstream's slash command is /exit;
	// bubbletea reads stdin in raw mode so Enter is CR.
	if err := coord.Write(id, []byte("/exit\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Wait for the status handler to fire.
	deadline := time.After(10 * time.Second)
	for {
		statusMu.Lock()
		sid := gotID
		st := gotStatus
		statusMu.Unlock()
		if sid != "" {
			if sid != id {
				t.Errorf("expected status for %q, got %q", id, sid)
			}
			if st != StatusExited {
				t.Errorf("expected status %q, got %q", StatusExited, st)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for status handler")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestCoordinator_UnknownAgentType(t *testing.T) {
	t.Parallel()

	coord := NewCoordinator(t.TempDir())
	_, err := coord.Start(context.Background(), SessionConfig{
		Name:      "bad",
		AgentType: "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for unknown agent type")
	}
}

func TestCoordinator_Shutdown(t *testing.T) {
	t.Parallel()
	requirePTY(t)

	bin := buildTestAgent(t)
	configDir := t.TempDir()

	coord := NewCoordinator(configDir)
	coord.RegisterRunner("testagent", &TestAgentRunner{BinaryPath: bin})

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, err := coord.Start(ctx, SessionConfig{
			Name:       "shutdown-test",
			WorkingDir: t.TempDir(),
			AgentUUID:  fmt.Sprintf("uuid-%s-%d", t.Name(), i),
			AgentType:  "testagent",
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	}

	if n := len(coord.List()); n != 3 {
		t.Fatalf("expected 3 sessions, got %d", n)
	}

	coord.Shutdown(ctx)

	// Wait for exit watchers to clean up.
	deadline := time.After(10 * time.Second)
	for len(coord.List()) > 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for shutdown cleanup")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
