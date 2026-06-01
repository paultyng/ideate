package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/paultyng/ideate/internal/claudecode"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/pubsub"
)

// fakeSessionStore implements SessionStore for testing. Sessions live under
// (slug, uuid). FindRunningSession scans for the first running record
// matching (slug, agentType) — same shape as the real FSStore.
type fakeSessionStore struct {
	sessions map[string]map[string]model.AgentSession // slug → uuid → session
	history  []model.HistoryEvent
	touched  []string // slug per TouchIdea call
	added    []addedResourceCall
}

type addedResourceCall struct {
	slug string
	res  model.Resource
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{
		sessions: make(map[string]map[string]model.AgentSession),
	}
}

func (s *fakeSessionStore) FindRunningSession(_ context.Context, slug, agentType string) (*model.AgentSession, error) {
	for _, session := range s.sessions[slug] {
		if session.Status == model.SessionStatusRunning && session.Agent == agentType {
			return &session, nil
		}
	}
	return nil, nil
}

func (s *fakeSessionStore) ListSessions(_ context.Context, slug string) ([]model.AgentSession, error) {
	out := make([]model.AgentSession, 0, len(s.sessions[slug]))
	for _, session := range s.sessions[slug] {
		out = append(out, session)
	}
	return out, nil
}

func (s *fakeSessionStore) WriteSession(ctx context.Context, slug, key string, session model.AgentSession) error {
	return s.UpdateSession(ctx, slug, key, session)
}

func (s *fakeSessionStore) UpdateSession(ctx context.Context, slug, key string, session model.AgentSession) error {
	if s.sessions[slug] == nil {
		s.sessions[slug] = make(map[string]model.AgentSession)
	}
	s.sessions[slug][key] = session
	// Real FSStore.WriteSession funnels through TouchIdea — mirror that.
	_, _ = s.TouchIdea(ctx, slug)
	return nil
}

func (s *fakeSessionStore) AppendHistory(_ context.Context, _ string, event model.HistoryEvent) error {
	s.history = append(s.history, event)
	return nil
}

func (s *fakeSessionStore) TouchIdea(_ context.Context, slug string) (time.Time, error) {
	s.touched = append(s.touched, slug)
	return time.Now(), nil
}

func (s *fakeSessionStore) AddResource(_ context.Context, slug string, res model.Resource) error {
	s.added = append(s.added, addedResourceCall{slug: slug, res: res})
	return nil
}

// runningStore returns a fakeSessionStore preloaded with one running
// claude-code session under slug "test-idea" / uuid "uuid-abc".
func runningStore() *fakeSessionStore {
	store := newFakeSessionStore()
	store.sessions["test-idea"] = map[string]model.AgentSession{
		"uuid-abc": {
			UUID:    "uuid-abc",
			Agent:   "claude-code",
			Status:  model.SessionStatusRunning,
			Started: time.Now(),
		},
	}
	return store
}

// newHandler builds a Handler whose emit traffic lands on a buffered
// channel the test can synchronously receive from. Pass nil to skip
// emit instrumentation (the handler is constructed with a nil broker
// — emits become no-ops).
// fakeNotifier records every slug Enqueue is called with. Used by
// TestHandleEnd_EnqueuesSummarizer to assert the SessionEnd hook
// drives summary regeneration.
type fakeNotifier struct {
	mu       sync.Mutex
	enqueued []string
}

func (f *fakeNotifier) Enqueue(slug string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enqueued = append(f.enqueued, slug)
	return true
}

func newHandler(store SessionStore, events chan<- string) *Handler {
	if events == nil {
		return NewHandler(store, nil, nil)
	}
	br := pubsub.New[pubsub.Event]()
	ch, _ := br.Subscribe()
	go func() {
		for ev := range ch {
			events <- ev.Name
		}
	}()
	return NewHandler(store, br, nil)
}

// nextEvent blocks the test on the next emit. Fails the test if no
// event arrives within a generous deadline so a buggy handler can't
// hang the suite.
func nextEvent(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for emit")
		return ""
	}
}

func slugEvent(slug string) claudecode.HookEvent {
	return claudecode.HookEvent{IdeaSlug: slug}
}

// TestHandleStop_ClearsActiveReviewID — when the user Esc-interrupts a
// session that was waiting on a review, the Stop hook fires. Activity
// flips to idle and ActiveReviewID is dropped so the session no longer
// claims to be reviewing — the banner clears even if the agent never
// finished its get_*_review_result polling loop.
func TestHandleStop_ClearsActiveReviewID(t *testing.T) {
	t.Parallel()
	store := runningStore()
	store.sessions["test-idea"]["uuid-abc"] = model.AgentSession{
		UUID:           "uuid-abc",
		Agent:          "claude-code",
		Status:         model.SessionStatusRunning,
		Activity:       model.SessionActivityReviewing,
		ActiveReviewID: "rev-xyz",
		Started:        time.Now(),
	}
	handler := newHandler(store, nil)

	if err := handler.HandleStop(context.Background(), claudecode.StopEvent{
		HookEvent: slugEvent("test-idea"),
		Raw:       json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("HandleStop: %v", err)
	}

	got := store.sessions["test-idea"]["uuid-abc"]
	if got.Activity != model.SessionActivityIdle {
		t.Errorf("Activity = %q, want idle", got.Activity)
	}
	if got.ActiveReviewID != "" {
		t.Errorf("ActiveReviewID = %q, want empty (cleared on Stop)", got.ActiveReviewID)
	}
}

func TestHandleStop_SetsActivityIdle(t *testing.T) {
	t.Parallel()
	store := runningStore()
	events := make(chan string, 4)
	handler := newHandler(store, events)

	if err := handler.HandleStop(context.Background(), claudecode.StopEvent{
		HookEvent: slugEvent("test-idea"),
		Raw:       json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("HandleStop: %v", err)
	}
	if got := nextEvent(t, events); got != "session:test-idea:hook:stop" {
		t.Errorf("event = %q, want session:test-idea:hook:stop", got)
	}

	got := store.sessions["test-idea"]["uuid-abc"]
	if got.Activity != model.SessionActivityIdle {
		t.Errorf("Activity = %q, want idle", got.Activity)
	}
	if got.Status != model.SessionStatusRunning {
		t.Errorf("Stop must not change Status; got %q", got.Status)
	}
	if len(store.touched) == 0 || store.touched[len(store.touched)-1] != "test-idea" {
		t.Errorf("expected TouchIdea(test-idea); got %v", store.touched)
	}
}

func TestHandlePrompt_SetsActivityActive(t *testing.T) {
	t.Parallel()
	store := runningStore()
	sess := store.sessions["test-idea"]["uuid-abc"]
	sess.Activity = model.SessionActivityIdle
	store.sessions["test-idea"]["uuid-abc"] = sess

	events := make(chan string, 4)
	handler := newHandler(store, events)

	if err := handler.HandlePrompt(context.Background(), claudecode.PromptEvent{
		HookEvent: slugEvent("test-idea"),
		Prompt:    "do the thing",
		Raw:       json.RawMessage(`{"prompt":"do the thing"}`),
	}); err != nil {
		t.Fatalf("HandlePrompt: %v", err)
	}
	if got := nextEvent(t, events); got != "session:test-idea:hook:prompt" {
		t.Errorf("event = %q, want session:test-idea:hook:prompt", got)
	}
	if got := store.sessions["test-idea"]["uuid-abc"]; got.Activity != model.SessionActivityActive {
		t.Errorf("Activity = %q, want active", got.Activity)
	}
}

func TestHandlePreCompact_SetsActivityActive(t *testing.T) {
	t.Parallel()
	store := runningStore()
	sess := store.sessions["test-idea"]["uuid-abc"]
	sess.Activity = model.SessionActivityIdle
	store.sessions["test-idea"]["uuid-abc"] = sess

	events := make(chan string, 4)
	handler := newHandler(store, events)

	if err := handler.HandlePreCompact(context.Background(), claudecode.PreCompactEvent{
		HookEvent: slugEvent("test-idea"),
		Trigger:   "manual",
		Raw:       json.RawMessage(`{"trigger":"manual"}`),
	}); err != nil {
		t.Fatalf("HandlePreCompact: %v", err)
	}
	if got := nextEvent(t, events); got != "session:test-idea:hook:pre-compact" {
		t.Errorf("event = %q, want session:test-idea:hook:pre-compact", got)
	}
	if got := store.sessions["test-idea"]["uuid-abc"]; got.Activity != model.SessionActivityActive {
		t.Errorf("Activity = %q, want active", got.Activity)
	}
}

func TestHandleNotification_FromActiveSetsWaiting(t *testing.T) {
	t.Parallel()
	store := runningStore()
	sess := store.sessions["test-idea"]["uuid-abc"]
	sess.Activity = model.SessionActivityActive
	store.sessions["test-idea"]["uuid-abc"] = sess

	events := make(chan string, 4)
	handler := newHandler(store, events)

	if err := handler.HandleNotification(context.Background(), claudecode.NotificationEvent{
		HookEvent: slugEvent("test-idea"),
		Message:   "permission needed",
		Raw:       json.RawMessage(`{"message":"permission needed"}`),
	}); err != nil {
		t.Fatalf("HandleNotification: %v", err)
	}
	if got := nextEvent(t, events); got != "session:test-idea:hook:notification" {
		t.Errorf("event = %q, want session:test-idea:hook:notification", got)
	}
	if got := store.sessions["test-idea"]["uuid-abc"]; got.Activity != model.SessionActivityWaiting {
		t.Errorf("Activity = %q, want waiting", got.Activity)
	}
}

// Idle-reminder Notifications fire when the user has been idle at an idle
// prompt — the agent is also idle, not blocked. Activity must stay idle.
func TestHandleNotification_FromIdleStaysIdle(t *testing.T) {
	t.Parallel()
	store := runningStore()
	sess := store.sessions["test-idea"]["uuid-abc"]
	sess.Activity = model.SessionActivityIdle
	store.sessions["test-idea"]["uuid-abc"] = sess

	handler := newHandler(store, nil)
	if err := handler.HandleNotification(context.Background(), claudecode.NotificationEvent{
		HookEvent: slugEvent("test-idea"),
		Message:   "Claude is waiting for your input",
		Raw:       json.RawMessage(`{"message":"Claude is waiting for your input"}`),
	}); err != nil {
		t.Fatalf("HandleNotification: %v", err)
	}
	if got := store.sessions["test-idea"]["uuid-abc"]; got.Activity != model.SessionActivityIdle {
		t.Errorf("Activity = %q, want idle (notification from idle should not flip to waiting)", got.Activity)
	}
}

func TestHandleToolUse_SetsActivityActive(t *testing.T) {
	t.Parallel()
	store := runningStore()
	sess := store.sessions["test-idea"]["uuid-abc"]
	sess.Activity = model.SessionActivityWaiting
	store.sessions["test-idea"]["uuid-abc"] = sess

	handler := newHandler(store, nil)
	if err := handler.HandleToolUse(context.Background(), claudecode.ToolUseEvent{
		HookEvent: slugEvent("test-idea"),
		ToolName:  "Edit",
		Raw:       json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("HandleToolUse: %v", err)
	}
	if got := store.sessions["test-idea"]["uuid-abc"]; got.Activity != model.SessionActivityActive {
		t.Errorf("Activity = %q, want active (PostToolUse forces back to active)", got.Activity)
	}
}

func TestHandleToolUse_WebFetchAddsWebResource(t *testing.T) {
	t.Parallel()
	store := runningStore()
	handler := newHandler(store, nil)

	err := handler.HandleToolUse(context.Background(), claudecode.ToolUseEvent{
		HookEvent:    slugEvent("test-idea"),
		ToolName:     "WebFetch",
		ToolInput:    map[string]any{"url": "https://example.com/docs/page"},
		ToolResponse: json.RawMessage(`{"title":"Docs Page"}`),
		Raw:          json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("HandleToolUse: %v", err)
	}
	if len(store.added) != 1 {
		t.Fatalf("AddResource called %d times, want 1; got %+v", len(store.added), store.added)
	}
	got := store.added[0]
	if got.slug != "test-idea" {
		t.Errorf("slug = %q, want %q", got.slug, "test-idea")
	}
	if got.res.Type != "web" {
		t.Errorf("type = %q, want %q", got.res.Type, "web")
	}
	if got.res.URL != "https://example.com/docs/page" {
		t.Errorf("url = %q", got.res.URL)
	}
	if got.res.Label != "Docs Page" {
		t.Errorf("label = %q, want %q (from response title)", got.res.Label, "Docs Page")
	}
}

func TestHandleToolUse_WebFetchWithoutTitle_FallsBackToHost(t *testing.T) {
	t.Parallel()
	store := runningStore()
	handler := newHandler(store, nil)

	err := handler.HandleToolUse(context.Background(), claudecode.ToolUseEvent{
		HookEvent: slugEvent("test-idea"),
		ToolName:  "WebFetch",
		ToolInput: map[string]any{"url": "https://example.com/path"},
		// no ToolResponse — label falls back to host
		Raw: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("HandleToolUse: %v", err)
	}
	if len(store.added) != 1 {
		t.Fatalf("AddResource called %d times, want 1", len(store.added))
	}
	if got := store.added[0].res.Label; got != "example.com" {
		t.Errorf("label = %q, want %q (host fallback)", got, "example.com")
	}
}

func TestHandleToolUse_NonWebFetchDoesNotAddResource(t *testing.T) {
	t.Parallel()
	store := runningStore()
	handler := newHandler(store, nil)

	err := handler.HandleToolUse(context.Background(), claudecode.ToolUseEvent{
		HookEvent: slugEvent("test-idea"),
		ToolName:  "Edit",
		ToolInput: map[string]any{"url": "https://example.com/path"}, // Edit doesn't have url, but shouldn't matter
		Raw:       json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("HandleToolUse: %v", err)
	}
	if len(store.added) != 0 {
		t.Errorf("AddResource called %d times for non-WebFetch tool, want 0", len(store.added))
	}
}

func TestHandleEnd(t *testing.T) {
	t.Parallel()
	store := runningStore()
	sess := store.sessions["test-idea"]["uuid-abc"]
	sess.Activity = model.SessionActivityActive
	store.sessions["test-idea"]["uuid-abc"] = sess

	events := make(chan string, 4)
	handler := newHandler(store, events)

	if err := handler.HandleEnd(context.Background(), claudecode.EndEvent{
		HookEvent: slugEvent("test-idea"),
		Raw:       json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("HandleEnd: %v", err)
	}
	if got := nextEvent(t, events); got != "session:test-idea:hook:end" {
		t.Errorf("event = %q, want session:test-idea:hook:end", got)
	}

	got := store.sessions["test-idea"]["uuid-abc"]
	if got.Status != model.SessionStatusCompleted {
		t.Errorf("Status = %q, want completed", got.Status)
	}
	if got.StopReason != model.SessionStopReasonExit {
		t.Errorf("StopReason = %q, want exit", got.StopReason)
	}
	if got.Activity != "" {
		t.Errorf("Activity = %q, want cleared", got.Activity)
	}
	if got.Ended == nil {
		t.Error("Ended is nil after end hook")
	}

	if len(store.history) != 1 || store.history[0].Event != "session_ended" {
		t.Errorf("history = %v", store.history)
	}
}

// HandleEnd enqueues the slug on the summarizer when one is wired.
// Drives idea-summary regeneration off the SessionEnd hook.
func TestHandleEnd_EnqueuesSummarizer(t *testing.T) {
	t.Parallel()
	store := runningStore()
	sess := store.sessions["test-idea"]["uuid-abc"]
	sess.Activity = model.SessionActivityActive
	store.sessions["test-idea"]["uuid-abc"] = sess

	notifier := &fakeNotifier{}
	handler := NewHandler(store, nil, notifier)

	if err := handler.HandleEnd(context.Background(), claudecode.EndEvent{
		HookEvent: slugEvent("test-idea"),
		Raw:       json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("HandleEnd: %v", err)
	}

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.enqueued) != 1 || notifier.enqueued[0] != "test-idea" {
		t.Errorf("notifier got %v, want [test-idea]", notifier.enqueued)
	}
}

// SessionEnd reason=clear marks the session completed with StopReason=cleared
// (vs. the default exit). Lets the UI render predecessor records correctly
// once Model C creates the sibling session on SessionStart source=clear.
func TestHandleEnd_ClearReasonSetsClearedStopReason(t *testing.T) {
	t.Parallel()
	store := runningStore()
	handler := newHandler(store, nil)

	if err := handler.HandleEnd(context.Background(), claudecode.EndEvent{
		HookEvent: slugEvent("test-idea"),
		Reason:    "clear",
		Raw:       json.RawMessage(`{"reason":"clear"}`),
	}); err != nil {
		t.Fatalf("HandleEnd: %v", err)
	}
	got := store.sessions["test-idea"]["uuid-abc"]
	if got.StopReason != model.SessionStopReasonCleared {
		t.Errorf("StopReason = %q, want cleared", got.StopReason)
	}
	if got.Status != model.SessionStatusCompleted {
		t.Errorf("Status = %q, want completed", got.Status)
	}
}

// reason=compact maps to StopReasonCompacted.
func TestHandleEnd_CompactReasonSetsCompactedStopReason(t *testing.T) {
	t.Parallel()
	store := runningStore()
	handler := newHandler(store, nil)

	if err := handler.HandleEnd(context.Background(), claudecode.EndEvent{
		HookEvent: slugEvent("test-idea"),
		Reason:    "compact",
		Raw:       json.RawMessage(`{"reason":"compact"}`),
	}); err != nil {
		t.Fatalf("HandleEnd: %v", err)
	}
	got := store.sessions["test-idea"]["uuid-abc"]
	if got.StopReason != model.SessionStopReasonCompacted {
		t.Errorf("StopReason = %q, want compacted", got.StopReason)
	}
}

func TestHandleEnd_UnknownSlug(t *testing.T) {
	t.Parallel()

	events := make(chan string, 4)
	handler := newHandler(newFakeSessionStore(), events)

	if err := handler.HandleEnd(context.Background(), claudecode.EndEvent{
		HookEvent: slugEvent("does-not-exist"),
		Raw:       json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("HandleEnd: %v", err)
	}
	// The event still emits — the frontend may want to react to any end
	// hook, even for unrecognized slugs.
	if got := nextEvent(t, events); got != "session:does-not-exist:hook:end" {
		t.Errorf("event = %q, want session:does-not-exist:hook:end", got)
	}
}

func TestSetActivity_NoOpForTerminalSession(t *testing.T) {
	t.Parallel()
	store := runningStore()
	// Flip the session to completed; FindRunningSession will return nil
	// for the slug and the activity update should be skipped.
	sess := store.sessions["test-idea"]["uuid-abc"]
	sess.Status = model.SessionStatusCompleted
	store.sessions["test-idea"]["uuid-abc"] = sess

	handler := newHandler(store, nil)
	if err := handler.HandleStop(context.Background(), claudecode.StopEvent{
		HookEvent: slugEvent("test-idea"),
		Raw:       json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("HandleStop: %v", err)
	}
	got := store.sessions["test-idea"]["uuid-abc"]
	if got.Activity != "" {
		t.Errorf("Activity = %q, want empty (terminal session must not be touched)", got.Activity)
	}
	if got.Status != model.SessionStatusCompleted {
		t.Errorf("Status = %q, want completed", got.Status)
	}
}

func TestSetActivity_UnknownSlugIsNoOp(t *testing.T) {
	t.Parallel()
	store := newFakeSessionStore()
	events := make(chan string, 4)
	handler := newHandler(store, events)

	if err := handler.HandlePrompt(context.Background(), claudecode.PromptEvent{
		HookEvent: slugEvent("does-not-exist"),
		Raw:       json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("HandlePrompt: %v", err)
	}
	if got := nextEvent(t, events); got != "session:does-not-exist:hook:prompt" {
		t.Errorf("event = %q, want session:does-not-exist:hook:prompt", got)
	}
	if len(store.touched) != 0 {
		t.Errorf("unknown slug must not call TouchIdea; got %v", store.touched)
	}
}

// FindRunningSession returning an error should be a no-op (not panic).
func TestFindRunningSession_ErrorIsSwallowed(t *testing.T) {
	t.Parallel()
	store := &errStore{fakeSessionStore: newFakeSessionStore(), err: fmt.Errorf("boom")}
	handler := newHandler(store, nil)
	if err := handler.HandleStop(context.Background(), claudecode.StopEvent{
		HookEvent: slugEvent("anything"),
		Raw:       json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("HandleStop: %v", err)
	}
}

// errStore wraps a fakeSessionStore but forces FindRunningSession to error.
type errStore struct {
	*fakeSessionStore
	err error
}

func (s *errStore) FindRunningSession(_ context.Context, _, _ string) (*model.AgentSession, error) {
	return nil, s.err
}

// /clear flow: SessionEnd reason=clear marks the predecessor as
// completed/cleared, then SessionStart source=clear creates a sibling
// session record linked to the predecessor via PreviousUUID.
func TestHandleEnd_ClearCreatesSiblingLinkedToPredecessor(t *testing.T) {
	t.Parallel()
	store := runningStore()
	handler := newHandler(store, nil)

	// /clear arrives as SessionEnd with reason="clear"; HandleEnd marks
	// the predecessor cleared AND creates the sibling successor in one
	// shot (Claude doesn't fire SessionStart over HTTP, so we can't
	// rely on a separate SessionStart hook to do it).
	if err := handler.HandleEnd(context.Background(), claudecode.EndEvent{
		HookEvent: slugEvent("test-idea"),
		Reason:    "clear",
		Raw:       json.RawMessage(`{"reason":"clear"}`),
	}); err != nil {
		t.Fatalf("HandleEnd: %v", err)
	}

	pre := store.sessions["test-idea"]["uuid-abc"]
	if pre.Status != model.SessionStatusCompleted {
		t.Errorf("predecessor.Status = %q, want completed", pre.Status)
	}
	if pre.StopReason != model.SessionStopReasonCleared {
		t.Errorf("predecessor.StopReason = %q, want cleared", pre.StopReason)
	}

	// Find the successor — its UUID is generated locally so we look it
	// up by PreviousUUID linkage.
	var succ *model.AgentSession
	for _, s := range store.sessions["test-idea"] {
		if s.PreviousUUID == "uuid-abc" {
			s := s
			succ = &s
			break
		}
	}
	if succ == nil {
		t.Fatalf("successor session not created (sessions: %v)", store.sessions["test-idea"])
	}
	if succ.Status != model.SessionStatusRunning {
		t.Errorf("successor.Status = %q, want running", succ.Status)
	}
	if succ.Activity != model.SessionActivityIdle {
		t.Errorf("successor.Activity = %q, want idle", succ.Activity)
	}
	if succ.Agent != "claude-code" {
		t.Errorf("successor.Agent = %q, want claude-code", succ.Agent)
	}

	var saw bool
	for _, h := range store.history {
		if h.Event == "session_clear" && h.Session == succ.UUID {
			saw = true
			break
		}
	}
	if !saw {
		t.Errorf("expected session_clear history event for successor; got %v", store.history)
	}
}

// /compact follows the same pattern but with StopReasonCompacted.
func TestHandleEnd_CompactCreatesSibling(t *testing.T) {
	t.Parallel()
	store := runningStore()
	handler := newHandler(store, nil)

	if err := handler.HandleEnd(context.Background(), claudecode.EndEvent{
		HookEvent: slugEvent("test-idea"),
		Reason:    "compact",
		Raw:       json.RawMessage(`{"reason":"compact"}`),
	}); err != nil {
		t.Fatalf("HandleEnd: %v", err)
	}

	if pre := store.sessions["test-idea"]["uuid-abc"]; pre.StopReason != model.SessionStopReasonCompacted {
		t.Errorf("predecessor.StopReason = %q, want compacted", pre.StopReason)
	}
	var succ *model.AgentSession
	for _, s := range store.sessions["test-idea"] {
		if s.PreviousUUID == "uuid-abc" {
			s := s
			succ = &s
			break
		}
	}
	if succ == nil {
		t.Fatalf("compact successor not created")
	}
}

// reason="exit" (or empty / other) must NOT spawn a sibling — the session
// is genuinely over.
func TestHandleEnd_ExitDoesNotCreateSibling(t *testing.T) {
	t.Parallel()
	store := runningStore()
	handler := newHandler(store, nil)

	if err := handler.HandleEnd(context.Background(), claudecode.EndEvent{
		HookEvent: slugEvent("test-idea"),
		Reason:    "exit",
		Raw:       json.RawMessage(`{"reason":"exit"}`),
	}); err != nil {
		t.Fatalf("HandleEnd: %v", err)
	}
	if len(store.sessions["test-idea"]) != 1 {
		t.Errorf("exit should not create sibling; got %d sessions (%v)",
			len(store.sessions["test-idea"]), store.sessions["test-idea"])
	}
}

// SessionStart is no-op since v2.x Claude Code doesn't fire HTTP SessionStart.
func TestHandleSessionStart_IsAlwaysNoOp(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"startup", "resume", "clear", "compact", ""} {
		source := source
		t.Run("source="+source, func(t *testing.T) {
			t.Parallel()
			store := runningStore()
			handler := newHandler(store, nil)
			if err := handler.HandleSessionStart(context.Background(), claudecode.SessionStartEvent{
				HookEvent: claudecode.HookEvent{IdeaSlug: "test-idea", SessionID: "uuid-noop"},
				Source:    source,
				Raw:       json.RawMessage(`{}`),
			}); err != nil {
				t.Fatalf("HandleSessionStart: %v", err)
			}
			if len(store.sessions["test-idea"]) != 1 {
				t.Errorf("HandleSessionStart should never mutate state; got %d sessions",
					len(store.sessions["test-idea"]))
			}
		})
	}
}

func TestActivityTransition_BumpsIdea(t *testing.T) {
	t.Parallel()
	store := runningStore()
	handler := newHandler(store, nil)

	for _, fn := range []func() error{
		func() error {
			return handler.HandlePrompt(context.Background(), claudecode.PromptEvent{
				HookEvent: slugEvent("test-idea"), Raw: json.RawMessage(`{}`),
			})
		},
		func() error {
			return handler.HandleStop(context.Background(), claudecode.StopEvent{
				HookEvent: slugEvent("test-idea"), Raw: json.RawMessage(`{}`),
			})
		},
		func() error {
			return handler.HandlePrompt(context.Background(), claudecode.PromptEvent{
				HookEvent: slugEvent("test-idea"), Raw: json.RawMessage(`{}`),
			})
		},
	} {
		if err := fn(); err != nil {
			t.Fatalf("hook: %v", err)
		}
	}
	if len(store.touched) < 3 {
		t.Errorf("expected at least 3 TouchIdea calls; got %d (%v)", len(store.touched), store.touched)
	}
}

// Compile-time interface check.
var _ SessionStore = (*fakeSessionStore)(nil)
