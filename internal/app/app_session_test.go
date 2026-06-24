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

// TestResolveActiveSession_TerminatedNewerThanDormant guards the
// "user said stop" contract: a freshly user-terminated session
// (completed/stopped/failed) outranks an older dormant. The resolver
// must return the terminated session's UUID without auto-resuming so
// the frontend lands on the session-detail page rather than reviving
// state the user just chose to exit.
func TestResolveActiveSession_TerminatedNewerThanDormant(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)

	idea := &model.Idea{Name: "Term Wins", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	old := model.AgentSession{
		UUID: "uuid-dormant-old", Agent: "claude-code",
		Status: model.SessionStatusDormant, Started: time.Now().Add(-2 * time.Hour),
	}
	if err := a.store.WriteSession(a.ctx, idea.Slug, old.UUID, old); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	terminated := model.AgentSession{
		UUID: "uuid-stopped-new", Agent: "claude-code",
		Status: model.SessionStatusStopped, Started: time.Now().Add(-5 * time.Minute),
	}
	if err := a.store.WriteSession(a.ctx, idea.Slug, terminated.UUID, terminated); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	got := a.ResolveActiveSession(idea.Slug)
	if !got.OK {
		t.Fatal("OK = false, want true")
	}
	if got.UUID != "uuid-stopped-new" {
		t.Errorf("UUID = %q, want uuid-stopped-new (terminated newer than dormant)", got.UUID)
	}
	if got.Resumed {
		t.Errorf("Resumed = true, want false (terminated path must not auto-resume)")
	}
}

// TestResolveActiveSession_DormantNewerThanTerminated covers the
// complement: when the dormant is the newest non-running entry, it
// wins and resumes per usual. The presence of an older terminated
// shouldn't perturb the resume path.
func TestResolveActiveSession_DormantNewerThanTerminated(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)
	a.coordinator.RegisterRunner("testagent", &agent.TestAgentRunner{BinaryPath: "/dev/null"})

	idea := &model.Idea{Name: "Dormant Wins", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	old := model.AgentSession{
		UUID: "uuid-completed-old", Agent: "testagent",
		Status: model.SessionStatusCompleted, Started: time.Now().Add(-3 * time.Hour),
	}
	if err := a.store.WriteSession(a.ctx, idea.Slug, old.UUID, old); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	dormant := model.AgentSession{
		UUID: "uuid-dormant-new", Agent: "testagent",
		Status: model.SessionStatusDormant, Started: time.Now().Add(-10 * time.Minute),
	}
	if err := a.store.WriteSession(a.ctx, idea.Slug, dormant.UUID, dormant); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	got := a.ResolveActiveSession(idea.Slug)
	// /dev/null runner fails the dormant resume; the step-4 fallback
	// then returns the old terminated session. Either way the
	// dormant-wins rule has been exercised (the resume was attempted
	// before falling through). Assert we did NOT return the dormant's
	// UUID with Resumed=true (success path), AND that the terminated
	// path's fallback engaged.
	if got.UUID == "uuid-dormant-new" && got.Resumed {
		t.Fatalf("dormant resume should have failed with /dev/null runner; got resumed=true")
	}
	// Fallback to the older terminated entry is acceptable.
	if got.UUID != "uuid-completed-old" {
		t.Errorf("UUID = %q, want uuid-completed-old (terminated fallback after dormant-resume failure)", got.UUID)
	}
	if got.Resumed {
		t.Errorf("Resumed = true, want false on the terminated fallback path")
	}
}

// TestResolveActiveSession_OnlyTerminated handles the case where only
// terminated entries exist — no dormant to resume. Returns the
// newest terminated UUID with resumed=false; frontend lands on
// session-detail and can offer a "new session" / "resume" affordance.
func TestResolveActiveSession_OnlyTerminated(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)

	idea := &model.Idea{Name: "Only Term", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i, status := range []model.SessionStatus{
		model.SessionStatusStopped,
		model.SessionStatusCompleted,
	} {
		s := model.AgentSession{
			UUID: "uuid-term-" + string(status), Agent: "claude-code",
			Status:  status,
			Started: time.Now().Add(-time.Duration(i+1) * time.Hour),
		}
		if err := a.store.WriteSession(a.ctx, idea.Slug, s.UUID, s); err != nil {
			t.Fatalf("WriteSession: %v", err)
		}
	}

	got := a.ResolveActiveSession(idea.Slug)
	if !got.OK {
		t.Fatal("OK = false, want true")
	}
	if got.UUID != "uuid-term-stopped" {
		t.Errorf("UUID = %q, want newest terminated (uuid-term-stopped)", got.UUID)
	}
	if got.Resumed {
		t.Errorf("Resumed = true, want false")
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
