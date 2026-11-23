package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

type gatingRunner struct {
	inner AgentRunner
	gate  chan struct{}
	calls atomic.Int32
}

func (g *gatingRunner) Run(ctx context.Context, cfg SessionConfig, out OutputFunc) (*Session, error) {
	g.calls.Add(1)
	if g.gate != nil {
		<-g.gate
	}
	return g.inner.Run(ctx, cfg, out)
}

type errorRunner struct{}

func (errorRunner) Run(context.Context, SessionConfig, OutputFunc) (*Session, error) {
	return nil, errors.New("boom")
}

func TestCoordinator_StartConcurrentSameUUID(t *testing.T) {
	t.Parallel()
	requirePTY(t)

	bin := buildTestAgent(t)
	coord := NewCoordinator(t.TempDir())
	gr := &gatingRunner{inner: &TestAgentRunner{BinaryPath: bin}, gate: make(chan struct{})}
	coord.RegisterRunner("testagent", gr)
	t.Cleanup(func() { _ = coord.Stop(context.Background(), "dup-uuid") })

	cfg := SessionConfig{Name: "dup", WorkingDir: t.TempDir(), AgentUUID: "dup-uuid", AgentType: "testagent"}
	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := coord.Start(context.Background(), cfg)
			errs <- err
		}()
	}
	// Losers return immediately; the winner is blocked on the gate.
	for i := 0; i < n-1; i++ {
		select {
		case err := <-errs:
			if !errors.Is(err, ErrSessionStarting) {
				t.Fatalf("loser err = %v, want ErrSessionStarting", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for rejected starts")
		}
	}
	close(gr.gate)
	wg.Wait()
	if err := <-errs; err != nil {
		t.Fatalf("winner Start: %v", err)
	}
	if got := gr.calls.Load(); got != 1 {
		t.Errorf("Run calls = %d, want 1", got)
	}
	if !coord.IsRunning("dup-uuid") {
		t.Error("expected session running")
	}
	// A start while the session is live is also rejected.
	if _, err := coord.Start(context.Background(), cfg); !errors.Is(err, ErrSessionStarting) {
		t.Errorf("start of running uuid err = %v, want ErrSessionStarting", err)
	}
}

func TestCoordinator_StartFailedRunClearsInFlight(t *testing.T) {
	t.Parallel()
	requirePTY(t)

	bin := buildTestAgent(t)
	coord := NewCoordinator(t.TempDir())
	cfg := SessionConfig{Name: "retry", WorkingDir: t.TempDir(), AgentUUID: "retry-uuid", AgentType: "testagent"}

	coord.RegisterRunner("testagent", errorRunner{})
	if _, err := coord.Start(context.Background(), cfg); err == nil || errors.Is(err, ErrSessionStarting) {
		t.Fatalf("first Start err = %v, want run failure", err)
	}
	if coord.IsRunning("retry-uuid") {
		t.Fatal("failed start left a session")
	}

	coord.RegisterRunner("testagent", &TestAgentRunner{BinaryPath: bin})
	if _, err := coord.Start(context.Background(), cfg); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	t.Cleanup(func() { _ = coord.Stop(context.Background(), "retry-uuid") })
}

func TestCoordinator_StartExitThenRestart(t *testing.T) {
	t.Parallel()
	requirePTY(t)

	bin := buildTestAgent(t)
	configDir := t.TempDir()
	coord := NewCoordinator(configDir)
	coord.RegisterRunner("testagent", &TestAgentRunner{BinaryPath: bin})
	ctx := context.Background()
	cfg := SessionConfig{Name: "again", WorkingDir: t.TempDir(), AgentUUID: "again-uuid", AgentType: "testagent"}

	if _, err := coord.Start(ctx, cfg); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := coord.Stop(ctx, "again-uuid"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for coord.IsRunning("again-uuid") {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for exit")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := coord.Start(ctx, cfg); err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Cleanup(func() { _ = coord.Stop(ctx, "again-uuid") })

	// The first session's exit goroutine must not have removed the new manifest.
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(manifestPath(configDir, "again-uuid")); err != nil {
		t.Errorf("restarted session manifest missing: %v", err)
	}
	if !coord.IsRunning("again-uuid") {
		t.Error("restarted session not running")
	}
}

func TestCoordinator_StartAfterExitBeforeCleanup(t *testing.T) {
	requirePTY(t) // sets a package-global hook; not parallel

	const id = "exit-lag-uuid"
	ctx := context.Background()
	configDir := t.TempDir()
	coord := NewCoordinator(configDir)
	coord.RegisterRunner("testagent", &TestAgentRunner{BinaryPath: buildTestAgent(t)})
	var callbacks atomic.Int32
	coord.SetStatusHandler(func(string, SessionMeta, string, int) { callbacks.Add(1) })

	// Block only the first exit goroutine for id, before it takes c.mu.
	reached := make(chan struct{})
	release := make(chan struct{})
	var first atomic.Bool
	hook := func(u string) {
		if u == id && first.CompareAndSwap(false, true) {
			close(reached)
			<-release
		}
	}
	exitGoroutineHook.Store(&hook)
	t.Cleanup(func() { exitGoroutineHook.Store(nil) })

	cfg := SessionConfig{Name: "t", WorkingDir: t.TempDir(), AgentUUID: id, AgentType: "testagent"}
	if _, err := coord.Start(ctx, cfg); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	old := coord.GetSession(id)
	if err := coord.Stop(ctx, id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("exit goroutine never reached hook")
	}
	if coord.GetSession(id) != old {
		t.Fatal("old entry cleaned before hook released; window not open")
	}

	// Window is open: process exited, entry still in the map.
	if _, err := coord.Start(ctx, cfg); err != nil {
		t.Fatalf("restart inside exit-lag window: %v", err)
	}
	successor := coord.GetSession(id)
	if successor == nil || successor == old {
		t.Fatal("successor not registered")
	}
	coord.StashStopReason(id, StopReasonUser)
	close(release)

	// Old goroutine must leave the successor's state alone.
	time.Sleep(200 * time.Millisecond)
	if coord.GetSession(id) != successor {
		t.Error("old goroutine removed successor's session")
	}
	if _, err := os.Stat(manifestPath(configDir, id)); err != nil {
		t.Errorf("successor manifest removed: %v", err)
	}
	coord.mu.RLock()
	_, hasMeta := coord.sessionMetas[id]
	reason := coord.stopReasons[id]
	coord.mu.RUnlock()
	if !hasMeta {
		t.Error("successor meta removed")
	}
	if reason != StopReasonUser {
		t.Errorf("successor stop reason = %q, want %q", reason, StopReasonUser)
	}
	if n := callbacks.Load(); n != 0 {
		t.Errorf("status callbacks after old exit = %d, want 0", n)
	}

	// Successor exits normally and fires exactly one callback.
	if err := coord.Stop(ctx, id); err != nil {
		t.Fatalf("Stop successor: %v", err)
	}
	for deadline := time.Now().Add(30 * time.Second); callbacks.Load() == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("successor callback never fired")
		}
	}
	if n := callbacks.Load(); n != 1 {
		t.Errorf("callbacks = %d, want 1", n)
	}
}
