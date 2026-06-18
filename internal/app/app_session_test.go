package app

import (
	"testing"
	"time"

	"github.com/paultyng/ideate/internal/agent"
	"github.com/paultyng/ideate/internal/model"
)

func TestResolveActiveSession_Running(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)

	idea := &model.Idea{Name: "Running", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	sess := model.AgentSession{
		UUID: "uuid-running", Agent: "claude-code",
		Status: model.SessionStatusRunning, Started: time.Now(),
	}
	if err := a.store.WriteSession(a.ctx, idea.Slug, sess.UUID, sess); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	got := a.ResolveActiveSession(idea.Slug)
	if !got.OK {
		t.Fatal("OK = false, want true")
	}
	if got.Resumed {
		t.Error("Resumed = true, want false")
	}
	if got.UUID != "uuid-running" {
		t.Errorf("UUID = %q, want uuid-running", got.UUID)
	}
}

// TestResolveActiveSession_DormantRunnerFails asserts the
// continue-on-error contract in the dormant loop: when StartIdeaSession
// can't spawn a runner (here, /dev/null binary), the method falls
// through cleanly rather than crashing or returning a partial result.
// The happy-path dormant resume (real binary spawns the agent) is
// exercised by integration tests with testagent.
func TestResolveActiveSession_DormantRunnerFails(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)
	a.coordinator.RegisterRunner("testagent", &agent.TestAgentRunner{BinaryPath: "/dev/null"})

	idea := &model.Idea{Name: "Dormant Fail", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	dormant := model.AgentSession{
		UUID: "uuid-dormant-fail", Agent: "testagent",
		Status: model.SessionStatusDormant, Started: time.Now().Add(-1 * time.Hour),
	}
	if err := a.store.WriteSession(a.ctx, idea.Slug, dormant.UUID, dormant); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	got := a.ResolveActiveSession(idea.Slug)
	if got.OK {
		t.Errorf("OK = true, want false (StartIdeaSession should fail with /dev/null binary)")
	}
	if got.UUID != "" {
		t.Errorf("UUID = %q, want empty", got.UUID)
	}
	if got.Resumed {
		t.Errorf("Resumed = true, want false")
	}
}

// TestResolveActiveSession_InvalidSlug guards the path-traversal +
// charset gate. Without IsValidSlug, an attacker-controlled `..` slug
// (e.g. from an agent-emitted ideate://ideas/../active-session link)
// would reach ListSessions → filepath.Join and traverse outside the
// ideas root.
func TestResolveActiveSession_InvalidSlug(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)

	for _, bad := range []string{"..", "../etc", "foo/bar", "Foo", "", "-leading"} {
		bad := bad
		t.Run("slug_"+bad, func(t *testing.T) {
			t.Parallel()
			got := a.ResolveActiveSession(bad)
			if got.OK || got.UUID != "" {
				t.Errorf("ResolveActiveSession(%q) = %+v, want empty result", bad, got)
			}
		})
	}
}

func TestResolveActiveSession_NoSession(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)

	idea := &model.Idea{Name: "Empty", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := a.ResolveActiveSession(idea.Slug)
	if got.OK {
		t.Errorf("OK = true, want false")
	}
	if got.Resumed {
		t.Errorf("Resumed = true, want false")
	}
	if got.UUID != "" {
		t.Errorf("UUID = %q, want empty", got.UUID)
	}
}
