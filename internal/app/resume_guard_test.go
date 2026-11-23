package app

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/paultyng/ideate/internal/agent"
	ideateMCP "github.com/paultyng/ideate/internal/mcp"
	"github.com/paultyng/ideate/internal/model"
)

func buildGuardTestAgent(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "testagent")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/paultyng/testagent")
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building testagent: %s\n%s", err, out)
	}
	return bin
}

// countingRunner counts Run calls and optionally blocks on gate before
// delegating to the real testagent runner.
type countingRunner struct {
	inner *agent.TestAgentRunner
	gate  chan struct{}
	calls atomic.Int32
}

func (r *countingRunner) CanResumeSession() bool { return true }

func (r *countingRunner) Run(ctx context.Context, cfg agent.SessionConfig, out agent.OutputFunc) (*agent.Session, error) {
	r.calls.Add(1)
	if r.gate != nil {
		<-r.gate
	}
	return r.inner.Run(ctx, cfg, out)
}

// newResumeGuardApp returns an App with an MCP manager, a counting runner
// registered as "testagent", and one dormant session.
func newResumeGuardApp(t *testing.T, gate chan struct{}) (*App, *countingRunner, string, string) {
	t.Helper()
	a, _ := newSessionTestApp(t)
	a.mcpManager = ideateMCP.NewManager(a.store, nil, nil)

	runner := &countingRunner{inner: &agent.TestAgentRunner{BinaryPath: buildGuardTestAgent(t)}, gate: gate}
	a.coordinator.RegisterRunner("testagent", runner)

	idea := &model.Idea{Name: "Guard", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	const uuid = "guard-uuid"
	sess := model.AgentSession{
		UUID: uuid, Agent: "testagent",
		Status: model.SessionStatusDormant, Started: time.Now(),
	}
	if err := a.store.WriteSession(a.ctx, idea.Slug, uuid, sess); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	t.Cleanup(func() { _ = a.coordinator.Stop(context.Background(), uuid) })
	return a, runner, idea.Slug, uuid
}

func requireInUse(t *testing.T, err error, uuid string) {
	t.Helper()
	var inUse *SessionInUseError
	if !errors.As(err, &inUse) || inUse.UUID != uuid || !errors.Is(err, ErrSessionAlreadyRunning) {
		t.Fatalf("err = %v, want SessionInUseError(AlreadyRunning) for %s", err, uuid)
	}
}

func TestResumeIdeaSession_ConcurrentSameUUID(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	a, runner, slug, uuid := newResumeGuardApp(t, gate)

	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.ResumeIdeaSession(slug, uuid)
			errs <- err
		}()
	}

	// Losers return while the winner is blocked in Run.
	for i := 0; i < n-1; i++ {
		select {
		case err := <-errs:
			requireInUse(t, err, uuid)
		case <-time.After(30 * time.Second):
			t.Fatal("timed out waiting for losing resume calls")
		}
	}
	// Winner is registered once it is inside Run (registration precedes Start).
	for deadline := time.Now().Add(30 * time.Second); runner.calls.Load() == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("winner never reached Run")
		}
	}
	if !a.mcpManager.ValidateSession(uuid) {
		t.Fatal("winner's MCP registration lost while duplicates were rejected")
	}
	close(gate)
	wg.Wait()
	if err := <-errs; err != nil {
		t.Fatalf("winner err = %v", err)
	}
	if got := runner.calls.Load(); got != 1 {
		t.Errorf("Run calls = %d, want 1", got)
	}
	if !a.mcpManager.ValidateSession(uuid) {
		t.Error("ValidateSession false after resume")
	}
	if _, held := a.resuming.Load(uuid); held {
		t.Error("resume guard key not released")
	}
}

func TestResumeIdeaSession_SequentialAfterRunning(t *testing.T) {
	t.Parallel()
	a, runner, slug, uuid := newResumeGuardApp(t, nil)

	if _, err := a.ResumeIdeaSession(slug, uuid); err != nil {
		t.Fatalf("first resume: %v", err)
	}
	_, err := a.ResumeIdeaSession(slug, uuid)
	requireInUse(t, err, uuid)
	if got := runner.calls.Load(); got != 1 {
		t.Errorf("Run calls = %d, want 1", got)
	}
	if !a.mcpManager.ValidateSession(uuid) {
		t.Error("ValidateSession false after rejected duplicate")
	}
	if _, held := a.resuming.Load(uuid); held {
		t.Error("resume guard key not released")
	}
}

func TestStartIdeaSession_ResumeConcurrentSameUUID(t *testing.T) {
	gate := make(chan struct{})
	a, runner, slug, uuid := newResumeGuardApp(t, gate)
	t.Setenv("IDEATE_IDEAS_DIR", a.ideasDir)

	const n = 6
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.StartIdeaSession(slug, "testagent", true)
			errs <- err
		}()
	}
	for i := 0; i < n-1; i++ {
		select {
		case err := <-errs:
			var inUse *SessionInUseError
			if !errors.As(err, &inUse) {
				t.Fatalf("loser err = %v, want SessionInUseError", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("timed out waiting for losing calls")
		}
	}
	close(gate)
	wg.Wait()
	if err := <-errs; err != nil {
		t.Fatalf("winner err = %v", err)
	}
	if got := runner.calls.Load(); got != 1 {
		t.Errorf("Run calls = %d, want 1", got)
	}
	if !a.mcpManager.ValidateSession(uuid) {
		t.Error("ValidateSession false after resume")
	}
}
