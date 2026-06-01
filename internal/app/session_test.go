package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/paultyng/ideate/internal/agent"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/service"
	"github.com/paultyng/ideate/internal/store"
)

// newSessionTestApp builds an App wired only with the store and coordinator —
// enough to exercise StartIdeaSession's reuse branch and GetRunningIdeaSession
// without spinning up the MCP/hooks HTTP server. Real testagent runner so the
// spawn path can be exercised by other tests if needed.
func newSessionTestApp(t *testing.T) (*App, string) {
	t.Helper()
	ideasDir := t.TempDir()
	reviewsDir := filepath.Join(ideasDir, ".reviews")
	s := store.NewFSStore(ideasDir, reviewsDir, "pt/", "")

	coord := agent.NewCoordinator(t.TempDir())

	a := &App{
		ctx:         context.Background(),
		store:       s,
		svc:         service.New(s, nil),
		coordinator: coord,
		ideasDir:    ideasDir,
	}
	return a, ideasDir
}

func TestGetRunningIdeaSession_NoRecord(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)

	idea := &model.Idea{Name: "Empty", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := a.GetRunningIdeaSession(idea.Slug, "claude-code")
	if got.UUID != "" {
		t.Errorf("expected empty result, got %+v", got)
	}
}

func TestGetRunningIdeaSession_StaleRecord(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)

	idea := &model.Idea{Name: "Stale", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Persisted running session, but coordinator never started it (e.g. crash).
	sess := model.AgentSession{
		UUID: "uuid-stale", Agent: "claude-code",
		Status: model.SessionStatusRunning, Started: time.Now(),
	}
	if err := a.store.WriteSession(a.ctx, idea.Slug, sess.UUID, sess); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	got := a.GetRunningIdeaSession(idea.Slug, "claude-code")
	if got.UUID != "uuid-stale" {
		t.Errorf("UUID = %q, want uuid-stale", got.UUID)
	}
}

func TestGetRunningIdeaSession_OtherAgentType(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)

	idea := &model.Idea{Name: "Mixed", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Running session for testagent — shouldn't show up under claude-code.
	sess := model.AgentSession{
		UUID: "uuid-test", Agent: "testagent",
		Status: model.SessionStatusRunning, Started: time.Now(),
	}
	if err := a.store.WriteSession(a.ctx, idea.Slug, sess.UUID, sess); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	got := a.GetRunningIdeaSession(idea.Slug, "claude-code")
	if got.UUID != "" {
		t.Errorf("expected no claude-code session; got %+v", got)
	}

	got = a.GetRunningIdeaSession(idea.Slug, "testagent")
	if got.UUID != "uuid-test" {
		t.Errorf("UUID = %q, want uuid-test", got.UUID)
	}
}

// StartIdeaSession refuses to spawn when a record claims a running session
// for (slug, agentType) but the coordinator has lost it (post-crash,
// pre-auto-resume) — returns ErrSessionStaleRunning so the UI can either
// wait for reconciliation or open the existing session via
// GetRunningIdeaSession.
//
// The live ErrSessionAlreadyRunning branch (coordinator owns the session)
// is exercised end-to-end by Playwright via testagent — see
// frontend/playwright/idea-session.spec.ts. It can't be unit-tested here
// without seeding coordinator-internal state.
func TestStartIdeaSession_ErrorsOnStaleRunning(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)

	idea := &model.Idea{Name: "Stale", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	seeded := model.AgentSession{
		UUID: "uuid-existing", Agent: "claude-code",
		Status: model.SessionStatusRunning, Started: time.Now(),
	}
	if err := a.store.WriteSession(a.ctx, idea.Slug, seeded.UUID, seeded); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	result, err := a.StartIdeaSession(idea.Slug, "claude-code", false)
	if err == nil {
		t.Fatalf("expected error; got result %+v", result)
	}
	if result != nil {
		t.Errorf("expected nil result on error; got %+v", result)
	}
	if !errors.Is(err, ErrSessionStaleRunning) {
		t.Errorf("err = %v, want wrapping ErrSessionStaleRunning", err)
	}
	var inUse *SessionInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("err = %T, want *SessionInUseError", err)
	}
	if inUse.UUID != "uuid-existing" {
		t.Errorf("inUse.UUID = %q, want uuid-existing", inUse.UUID)
	}

	// No new record should have been written.
	sessions, err := a.store.ListSessions(a.ctx, idea.Slug)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("got %d sessions after refused start, want 1", len(sessions))
	}
}

func TestMarkRunningSessionsStopped(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)

	idea := &model.Idea{Name: "MarkStopped", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Two running, one already-stopped — only the running ones should flip.
	for _, sess := range []model.AgentSession{
		{UUID: "running-1", Agent: "claude-code", Status: model.SessionStatusRunning, Activity: model.SessionActivityActive, Started: time.Now()},
		{UUID: "running-2", Agent: "testagent", Status: model.SessionStatusRunning, Started: time.Now()},
		{UUID: "stopped-old", Agent: "claude-code", Status: model.SessionStatusStopped, StopReason: model.SessionStopReasonUser, Started: time.Now().Add(-time.Hour)},
	} {
		if err := a.store.WriteSession(a.ctx, idea.Slug, sess.UUID, sess); err != nil {
			t.Fatalf("WriteSession %s: %v", sess.UUID, err)
		}
	}

	candidates := a.markRunningSessionsStopped(model.SessionStopReasonShutdown)
	if len(candidates) != 2 {
		t.Errorf("got %d candidates, want 2 (one per running session)", len(candidates))
	}

	// Verify on disk: the two running became stopped+shutdown; the old
	// stopped one is untouched.
	r1, _ := a.store.ReadSession(a.ctx, idea.Slug, "running-1")
	if r1.Status != model.SessionStatusStopped {
		t.Errorf("running-1 Status = %q, want stopped", r1.Status)
	}
	if r1.StopReason != model.SessionStopReasonShutdown {
		t.Errorf("running-1 StopReason = %q, want shutdown", r1.StopReason)
	}
	if r1.Activity != "" {
		t.Errorf("running-1 Activity = %q, want cleared", r1.Activity)
	}
	if r1.Ended == nil {
		t.Error("running-1 Ended is nil")
	}

	old, _ := a.store.ReadSession(a.ctx, idea.Slug, "stopped-old")
	if old.StopReason != model.SessionStopReasonUser {
		t.Errorf("stopped-old StopReason = %q, want user (must not be overwritten)", old.StopReason)
	}
}

func TestScanResumeCandidates_OnlyMostRecentPerAgent(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)
	// Register testagent so AgentSupportsResume returns true.
	a.coordinator.RegisterRunner("testagent", &agent.TestAgentRunner{BinaryPath: "/dev/null"})

	idea := &model.Idea{Name: "ScanCandidates", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	now := time.Now()
	for _, sess := range []model.AgentSession{
		// Older shutdown — should NOT be picked (newer wins).
		{UUID: "older", Agent: "testagent", Status: model.SessionStatusStopped, StopReason: model.SessionStopReasonShutdown, Started: now.Add(-2 * time.Hour)},
		// Newer shutdown — should be picked.
		{UUID: "newer", Agent: "testagent", Status: model.SessionStatusStopped, StopReason: model.SessionStopReasonShutdown, Started: now.Add(-1 * time.Hour)},
		// User-stopped — must NOT be picked.
		{UUID: "user-stop", Agent: "testagent", Status: model.SessionStatusStopped, StopReason: model.SessionStopReasonUser, Started: now},
	} {
		if err := a.store.WriteSession(a.ctx, idea.Slug, sess.UUID, sess); err != nil {
			t.Fatalf("WriteSession %s: %v", sess.UUID, err)
		}
	}

	candidates := a.scanResumeCandidates(model.SessionStopReasonShutdown)
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1 (only most-recent shutdown)", len(candidates))
	}
	// scanResumeCandidates iterates in ListSessions order (Started-desc).
	// Most recent SHUTDOWN is "newer" — but "user-stop" is more recent and
	// occupies the per-agent seen slot first if our filter is wrong.
	// The picked candidate must be "newer".
	if candidates[0].UUID != "newer" {
		t.Errorf("got UUID %q, want %q", candidates[0].UUID, "newer")
	}
}

// Orchestrator sessions live under the synthetic OrchestratorSlug and must be
// picked up by the same auto-resume sweep as idea sessions.
func TestScanResumeCandidates_IncludesOrchestrator(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)
	a.coordinator.RegisterRunner("testagent", &agent.TestAgentRunner{BinaryPath: "/dev/null"})

	// No idea record — just a orchestrator shutdown candidate.
	scratch := model.AgentSession{
		UUID:       "scratch-uuid",
		Agent:      "testagent",
		Status:     model.SessionStatusStopped,
		StopReason: model.SessionStopReasonShutdown,
		Started:    time.Now().Add(-1 * time.Hour),
	}
	if err := a.store.WriteSession(a.ctx, model.OrchestratorSlug, scratch.UUID, scratch); err != nil {
		t.Fatalf("WriteSession orchestrator: %v", err)
	}

	candidates := a.scanResumeCandidates(model.SessionStopReasonShutdown)
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1 (the orchestrator)", len(candidates))
	}
	if candidates[0].Slug != model.OrchestratorSlug {
		t.Errorf("Slug = %q, want %q", candidates[0].Slug, model.OrchestratorSlug)
	}
	if candidates[0].UUID != scratch.UUID {
		t.Errorf("UUID = %q, want %q", candidates[0].UUID, scratch.UUID)
	}
}

func TestScanResumeCandidates_SkipsNonResumableAgents(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)
	// No runner registered — AgentSupportsResume returns false for everything.

	idea := &model.Idea{Name: "Skip", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	sess := model.AgentSession{
		UUID: "shutdown-claude", Agent: "claude-code",
		Status: model.SessionStatusStopped, StopReason: model.SessionStopReasonShutdown,
		Started: time.Now(),
	}
	if err := a.store.WriteSession(a.ctx, idea.Slug, sess.UUID, sess); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	candidates := a.scanResumeCandidates(model.SessionStopReasonShutdown)
	if len(candidates) != 0 {
		t.Errorf("got %d candidates, want 0 (agent not registered, can't resume)", len(candidates))
	}
}

func TestBusyRunningSessions(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)

	idea := &model.Idea{Name: "Busy", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// One of each: active (busy), idle (not busy), waiting (busy), empty (not busy),
	// completed (irrelevant).
	for _, sess := range []model.AgentSession{
		{UUID: "active", Agent: "claude-code", Status: model.SessionStatusRunning, Activity: model.SessionActivityActive, Started: time.Now()},
		{UUID: "idle", Agent: "testagent", Status: model.SessionStatusRunning, Activity: model.SessionActivityIdle, Started: time.Now()},
		{UUID: "waiting", Agent: "claude-code", Status: model.SessionStatusRunning, Activity: model.SessionActivityWaiting, Started: time.Now()},
		// Empty Activity on a running session — treat as idle for the close
		// guard so a freshly-spawned session doesn't block quit.
		{UUID: "fresh", Agent: "testagent", Status: model.SessionStatusRunning, Started: time.Now()},
		{UUID: "completed", Agent: "claude-code", Status: model.SessionStatusCompleted, Started: time.Now()},
	} {
		if err := a.store.WriteSession(a.ctx, idea.Slug, sess.UUID, sess); err != nil {
			t.Fatalf("WriteSession %s: %v", sess.UUID, err)
		}
	}

	busy := a.BusyRunningSessions()
	if len(busy) != 2 {
		t.Fatalf("got %d busy sessions, want 2 (active + waiting)", len(busy))
	}
	uuids := map[string]bool{}
	for _, b := range busy {
		uuids[b.UUID] = true
	}
	if !uuids["active"] {
		t.Error("expected active session in busy list")
	}
	if !uuids["waiting"] {
		t.Error("expected waiting session in busy list")
	}
	if uuids["idle"] || uuids["fresh"] || uuids["completed"] {
		t.Errorf("idle/fresh/completed should not be busy: %v", uuids)
	}
}

func TestMarkSessionDormant_EmitsStatusEvent(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)

	var emitted []string
	a.emitFn = func(name string, _ any) { emitted = append(emitted, name) }

	idea := &model.Idea{Name: "DormantEmit", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	sess := model.AgentSession{
		UUID: "uuid-dormant", Agent: "claude-code",
		Status: model.SessionStatusRunning, Started: time.Now(),
	}
	if err := a.store.WriteSession(a.ctx, idea.Slug, sess.UUID, sess); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	a.markSessionDormant(a.ctx, ResumeCandidate{Slug: idea.Slug, UUID: sess.UUID}, "test")

	want := "session:" + sess.UUID + ":status"
	if len(emitted) != 1 || emitted[0] != want {
		t.Errorf("emitted = %v, want [%s]", emitted, want)
	}
	// Verify on-disk status flipped to dormant.
	got, _ := a.store.ReadSession(a.ctx, idea.Slug, sess.UUID)
	if got.Status != model.SessionStatusDormant {
		t.Errorf("Status = %q, want dormant", got.Status)
	}
}

// When the running record belongs to a *different* agent type, the start path
// should not reuse it — but spawning a new claude session requires a real
// runner, so we just verify the lookup doesn't short-circuit.
// (The actual spawn path is covered by integration tests that use testagent.)
func TestStartIdeaSession_DoesNotReuseDifferentAgentType(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)

	idea := &model.Idea{Name: "Cross", Status: model.StatusActive}
	if err := a.store.Create(a.ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Seed a running TESTAGENT record.
	seeded := model.AgentSession{
		UUID: "uuid-testagent", Agent: "testagent",
		Status: model.SessionStatusRunning, Started: time.Now(),
	}
	if err := a.store.WriteSession(a.ctx, idea.Slug, seeded.UUID, seeded); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	// StartIdeaSession for CLAUDE-CODE — must not return uuid-testagent.
	// It will fall through to the spawn path, which will fail because no
	// claude runner is registered. That's the signal the lookup didn't match.
	result, err := a.StartIdeaSession(idea.Slug, "claude-code", false)
	if err == nil {
		t.Fatalf("expected spawn error (no claude runner registered); got reuse %+v", result)
	}
	if result != nil {
		t.Errorf("expected nil result when spawn fails; got %+v", result)
	}
}
