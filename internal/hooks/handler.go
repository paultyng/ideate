package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/paultyng/ideate/internal/claudecode"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/pubsub"
)

// SessionStore is the subset of store operations needed by the hooks handler.
//
// Hook routing is slug-based: every hook arrives with X-Ideate-Idea-Slug.
// We resolve via ListSessions(slug) and pick the running record (or, for
// the SessionStart sibling-creation path, the most-recently-terminated
// record matching the source's stop reason). M14 enforces one running
// session per (slug, agent) so a slug-keyed lookup is unambiguous in
// practice; the new-record's Agent comes from the predecessor, not from
// the handler.
type SessionStore interface {
	ListSessions(ctx context.Context, slug string) ([]model.AgentSession, error)
	WriteSession(ctx context.Context, slug, key string, session model.AgentSession) error
	UpdateSession(ctx context.Context, slug, key string, session model.AgentSession) error
	AppendHistory(ctx context.Context, slug string, event model.HistoryEvent) error
	TouchIdea(ctx context.Context, slug string) (time.Time, error)
	// AddResource dedupes (canonical URL) and persists a resource on
	// the idea. Used by the WebFetch PostToolUse branch to auto-track
	// fetched URLs as `type="web"` resources.
	AddResource(ctx context.Context, slug string, res model.Resource) error
}

// SessionEndedNotifier receives a slug each time a session finishes
// (including via /clear or /compact restarts). The hooks handler
// calls Enqueue from HandleEnd so the summarizer pipeline regenerates
// the idea's summary sidecar against the just-ended session's
// transcript. Nil is permitted — tests that don't care about
// summarization pass nil and the Enqueue call is skipped.
type SessionEndedNotifier interface {
	Enqueue(slug string) bool
}

// Handler processes typed hook events from Claude Code.
// It implements claudecode.HookHandler.
type Handler struct {
	store SessionStore
	// events is the app-wide broker for frontend-bound events. Nil
	// is permitted for tests that don't care about emit traffic.
	events *pubsub.Broker[pubsub.Event]
	// summarizer, when set, is notified of every SessionEnd so the
	// idea's summary sidecar gets regenerated. Optional.
	summarizer SessionEndedNotifier
}

// NewHandler creates a new hooks handler. events may be nil — emits
// turn into no-ops in that case. summarizer may be nil — SessionEnd
// fires without enqueueing for regeneration.
func NewHandler(store SessionStore, events *pubsub.Broker[pubsub.Event], summarizer SessionEndedNotifier) *Handler {
	return &Handler{store: store, events: events, summarizer: summarizer}
}

// HandleStop transitions Activity to idle (Claude finished a turn) and emits
// a frontend event. Also clears ActiveReviewID — Stop fires for both
// natural turn-ends and user Esc-interrupts; in the interrupt case the
// agent has abandoned its review-polling loop, so dropping the
// reviewing-attribution leaves the session record honest. The review
// record itself stays pending — the user can still submit/cancel from
// the /review route.
func (h *Handler) HandleStop(ctx context.Context, event claudecode.StopEvent) error {
	h.setActivityAndClearReview(ctx, event.IdeaSlug, model.SessionActivityIdle, "stop")
	h.emit(event.IdeaSlug, "stop", parseRawSafe(event.Raw))
	return nil
}

// HandlePreToolUse transitions Activity to active before the tool
// runs so the chip's activity indicator reflects "agent is doing
// something" without waiting for PostToolUse to fire after the tool
// completes (intra-tool latency was otherwise dead time in the dot).
// Also records a `tool_call_started` history event with the tool
// name + input so the timeline shows what the agent attempted, not
// just what completed.
func (h *Handler) HandlePreToolUse(ctx context.Context, event claudecode.PreToolUseEvent) error {
	h.setActivity(ctx, event.IdeaSlug, model.SessionActivityActive, "pre-tool-use")
	h.emit(event.IdeaSlug, "pre-tool-use", parseRawSafe(event.Raw))
	if err := h.appendToolHistory(ctx, event.IdeaSlug, "tool_call_started", event.ToolName, event.ToolInput, nil, false); err != nil {
		slog.Warn("appending pre-tool-use history",
			slog.String("slug", event.IdeaSlug), slog.Any("err", err))
	}
	return nil
}

// HandleToolUse keeps Activity at active (PreToolUse already bumped
// it) and records either a `tool_call_completed` or a `tool_failure`
// history event depending on the tool_response payload's is_error
// flag. Frontend gets a `tool-use` event in the success case and a
// `tool-failure` event when IsError() reports true.
func (h *Handler) HandleToolUse(ctx context.Context, event claudecode.ToolUseEvent) error {
	h.setActivity(ctx, event.IdeaSlug, model.SessionActivityActive, "tool-use")
	if event.IsError() {
		h.emit(event.IdeaSlug, "tool-failure", parseRawSafe(event.Raw))
		if err := h.appendToolHistory(ctx, event.IdeaSlug, "tool_failure", event.ToolName, event.ToolInput, event.ToolResponse, true); err != nil {
			slog.Warn("appending tool-failure history",
				slog.String("slug", event.IdeaSlug), slog.Any("err", err))
		}
		return nil
	}
	h.emit(event.IdeaSlug, "tool-use", parseRawSafe(event.Raw))
	if err := h.appendToolHistory(ctx, event.IdeaSlug, "tool_call_completed", event.ToolName, event.ToolInput, event.ToolResponse, false); err != nil {
		slog.Warn("appending tool-use history",
			slog.String("slug", event.IdeaSlug), slog.Any("err", err))
	}
	h.maybeTrackWebFetch(ctx, event)
	return nil
}

// maybeTrackWebFetch auto-adds a `web` resource when the tool was
// WebFetch. Best-effort: any extraction or AddResource failure logs
// warn and does not propagate. Other tool names are no-ops; the auto-
// tracking surface is intentionally narrow (the prompt + summarizer
// channels carry coverage for tools like gh / notion / etc.).
func (h *Handler) maybeTrackWebFetch(ctx context.Context, event claudecode.ToolUseEvent) {
	if event.ToolName != "WebFetch" || event.IdeaSlug == "" {
		return
	}
	rawURL, _ := event.ToolInput["url"].(string)
	if rawURL == "" {
		return
	}
	label := webFetchLabel(rawURL, event.ToolResponse)
	res := model.Resource{Type: "web", URL: rawURL, Label: label}
	if err := h.store.AddResource(ctx, event.IdeaSlug, res); err != nil {
		slog.Warn("auto-tracking webfetch resource",
			slog.String("slug", event.IdeaSlug),
			slog.String("url", rawURL),
			slog.Any("err", err))
	}
}

// webFetchLabel derives a label for the resource. Preference order:
// (1) page title from the WebFetch response (if surfaced), (2) URL
// host fallback. Empty when neither is available — UpsertResource is
// tolerant of empty labels.
func webFetchLabel(rawURL string, toolResponse json.RawMessage) string {
	// Title from response — Claude's WebFetch surfaces a parsed title in
	// some shapes but not all; best-effort extract.
	if len(toolResponse) > 0 {
		var probe struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(toolResponse, &probe); err == nil && probe.Title != "" {
			return probe.Title
		}
	}
	// Host fallback.
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return ""
}

// HandlePrompt transitions Activity to active (user just submitted a prompt)
// and emits a frontend event.
func (h *Handler) HandlePrompt(ctx context.Context, event claudecode.PromptEvent) error {
	h.setActivity(ctx, event.IdeaSlug, model.SessionActivityActive, "prompt")
	h.emit(event.IdeaSlug, "prompt", parseRawSafe(event.Raw))
	return nil
}

// HandlePreCompact transitions Activity to active. /compact summarizes the
// conversation in a Claude sub-call that doesn't fire any other hook, and
// SessionEnd reason=compact only arrives after the summary completes — so
// PreCompact is the only signal during the compaction window.
func (h *Handler) HandlePreCompact(ctx context.Context, event claudecode.PreCompactEvent) error {
	h.setActivity(ctx, event.IdeaSlug, model.SessionActivityActive, "pre-compact")
	h.emit(event.IdeaSlug, "pre-compact", parseRawSafe(event.Raw))
	return nil
}

// HandleNotification transitions Activity to waiting (Claude is blocked
// on user input mid-turn, e.g. a permission prompt) and emits a frontend
// event. Claude Code also fires Notification as an idle-reminder when the
// user has been idle at an idle prompt — in that case the agent itself
// is idle, not blocked, so we keep Activity=idle. The guard: only
// transition to waiting from active. From idle, the notification is by
// definition the idle-reminder kind.
func (h *Handler) HandleNotification(ctx context.Context, event claudecode.NotificationEvent) error {
	h.setActivityIfFrom(ctx, event.IdeaSlug, model.SessionActivityWaiting, "notification",
		model.SessionActivityActive, model.SessionActivityWaiting)
	h.emit(event.IdeaSlug, "notification", parseRawSafe(event.Raw))
	return nil
}

// HandleSessionStart is intentionally a no-op. Claude Code v2.x doesn't
// support HTTP hooks for SessionStart (logs "HTTP hooks are not supported
// for SessionStart" and silently skips), so we don't subscribe it from
// the settings file at all. The handler exists only to satisfy the
// claudecode.HookHandler interface and to absorb a request if a future
// Claude version starts firing it. Sibling-record creation on /clear or
// /compact happens in HandleEnd via the SessionEnd reason field.
func (h *Handler) HandleSessionStart(_ context.Context, event claudecode.SessionStartEvent) error {
	slog.Info("session_start hook (no-op)",
		slog.String("slug", event.IdeaSlug),
		slog.String("source", event.Source))
	h.emit(event.IdeaSlug, "session-start", parseRawSafe(event.Raw))
	return nil
}

// HandleEnd finalizes the running session (Status=completed, Activity
// cleared). For reason="clear" or reason="compact" — Claude's in-process
// conversation resets — it ALSO creates a sibling successor session
// linked back to the predecessor via PreviousUUID. The successor takes a
// generated UUID; we don't have Claude's new internal session id (that
// would have come via SessionStart, which Claude Code doesn't fire over
// HTTP), so the manifest key is locally minted. Resume across an Ideate
// restart that lands on a generated UUID will fail to attach to Claude
// — but /clear has already discarded the conversation, so a fresh
// session is the correct semantic anyway.
func (h *Handler) HandleEnd(ctx context.Context, event claudecode.EndEvent) error {
	slog.Info("session_end hook",
		slog.String("slug", event.IdeaSlug),
		slog.String("reason", event.Reason),
	)
	predecessor, ok := h.findRunning(ctx, event.IdeaSlug, "end")
	var predecessorUUID string
	if ok {
		predecessorUUID = predecessor.UUID
		now := time.Now()
		predecessor.Status = model.SessionStatusCompleted
		predecessor.StopReason = stopReasonFromEnd(event.Reason)
		predecessor.Activity = ""
		predecessor.ActiveReviewID = ""
		predecessor.Ended = &now
		if err := h.store.UpdateSession(ctx, event.IdeaSlug, predecessorUUID, *predecessor); err != nil {
			slog.Warn("finalizing session in end hook",
				slog.String("slug", event.IdeaSlug), slog.String("session", predecessorUUID), slog.Any("err", err))
		}
	}

	if err := h.store.AppendHistory(ctx, event.IdeaSlug, model.HistoryEvent{
		Timestamp: time.Now(),
		Event:     "session_ended",
		Session:   predecessorUUID,
		Fields:    map[string]any{"body": parseRawSafe(event.Raw)},
	}); err != nil {
		slog.Warn("appending session_ended history",
			slog.String("slug", event.IdeaSlug), slog.Any("err", err))
	}

	// In-process conversation reset: spawn a sibling successor record
	// immediately so the global session bar transitions from
	// predecessor → successor without a gap.
	if ok && (event.Reason == "clear" || event.Reason == "compact") {
		h.createSuccessor(ctx, event.IdeaSlug, predecessor, event.Reason)
	}

	h.emit(event.IdeaSlug, "end", map[string]any{
		"slug":        event.IdeaSlug,
		"sessionUUID": predecessorUUID,
	})

	// Trigger the headless summarizer to regenerate the idea's summary
	// sidecar against the just-ended session's transcript. Best-effort:
	// a full queue just logs at the summarizer level and the next
	// SessionEnd (or a future staleness sweep) will catch up.
	if h.summarizer != nil {
		h.summarizer.Enqueue(event.IdeaSlug)
	}

	return nil
}

// createSuccessor writes a new running session record linked back to the
// predecessor via PreviousUUID. Best-effort: errors are logged, not
// returned (the SessionEnd hook still completes the predecessor cleanly).
func (h *Handler) createSuccessor(ctx context.Context, slug string, predecessor *model.AgentSession, reason string) {
	now := time.Now()
	successor := model.AgentSession{
		UUID:         uuid.New().String(),
		Agent:        predecessor.Agent,
		Status:       model.SessionStatusRunning,
		Activity:     model.SessionActivityIdle,
		PreviousUUID: predecessor.UUID,
		Started:      now,
		WorkingDir:   predecessor.WorkingDir,
		RepoName:     predecessor.RepoName,
	}
	if err := h.store.WriteSession(ctx, slug, successor.UUID, successor); err != nil {
		slog.Warn("writing successor session after /clear or /compact",
			slog.String("slug", slug),
			slog.String("uuid", successor.UUID),
			slog.Any("err", err))
		return
	}
	if err := h.store.AppendHistory(ctx, slug, model.HistoryEvent{
		Timestamp: now,
		Event:     "session_" + reason, // session_clear / session_compact
		Session:   successor.UUID,
		Fields: map[string]any{
			"previous_uuid": predecessor.UUID,
			"reason":        reason,
		},
	}); err != nil {
		slog.Warn("appending session_clear/compact history",
			slog.String("slug", slug), slog.Any("err", err))
	}
}

// setActivity is the common path for hooks that change a running session's
// Activity. It is best-effort: no running session for the slug is a no-op.
// On a successful update, UpdateSession funnels through TouchIdea so the
// idea's Updated timestamp is bumped — driving the dashboard MRU sort.
// `event` is the hook event name (stop, tool-use, prompt, notification);
// included in not-found log lines for observability.
func (h *Handler) setActivity(ctx context.Context, slug string, activity model.SessionActivity, event string) {
	h.setActivityIfFrom(ctx, slug, activity, event)
}

// setActivityAndClearReview sets Activity (no `from` guard) AND clears
// ActiveReviewID in a single write. Used by Stop where the agent has
// stopped its turn — including via user Esc-interrupt — so the
// reviewing-pending attribution is no longer valid.
func (h *Handler) setActivityAndClearReview(ctx context.Context, slug string, activity model.SessionActivity, event string) {
	session, ok := h.findRunning(ctx, slug, event)
	if !ok {
		return
	}
	if session.Activity == activity && session.ActiveReviewID == "" {
		if _, err := h.store.TouchIdea(ctx, slug); err != nil {
			slog.Warn("touching idea on idempotent stop hook",
				slog.String("slug", slug), slog.Any("err", err))
		}
		return
	}
	session.Activity = activity
	session.ActiveReviewID = ""
	if err := h.store.UpdateSession(ctx, slug, session.UUID, *session); err != nil {
		slog.Warn("persisting stop activity transition",
			slog.String("slug", slug), slog.String("session", session.UUID),
			slog.Any("err", err))
	}
}

// setActivityIfFrom is setActivity gated on the current Activity being one
// of `from`. Empty `from` means "always apply". Used by HandleNotification
// to prevent the idle-reminder Notification from flipping a finished-turn
// session to waiting.
func (h *Handler) setActivityIfFrom(ctx context.Context, slug string, activity model.SessionActivity, event string, from ...model.SessionActivity) {
	session, ok := h.findRunning(ctx, slug, event)
	if !ok {
		return
	}
	if len(from) > 0 {
		match := false
		for _, f := range from {
			if session.Activity == f {
				match = true
				break
			}
		}
		if !match {
			// Source state doesn't match a permitted transition; soft no-op,
			// but still bump the idea so MRU stays fresh.
			if _, err := h.store.TouchIdea(ctx, slug); err != nil {
				slog.Warn("touching idea on guarded activity hook",
					slog.String("slug", slug), slog.Any("err", err))
			}
			return
		}
	}
	if session.Activity == activity {
		// Idempotent: same state, still touch the idea so MRU sort moves.
		if _, err := h.store.TouchIdea(ctx, slug); err != nil {
			slog.Warn("touching idea on idempotent activity hook",
				slog.String("slug", slug), slog.Any("err", err))
		}
		return
	}
	session.Activity = activity
	if err := h.store.UpdateSession(ctx, slug, session.UUID, *session); err != nil {
		slog.Warn("persisting activity transition",
			slog.String("slug", slug), slog.String("session", session.UUID),
			slog.String("activity", string(activity)), slog.Any("err", err))
	}
}

// findRunning resolves the slug to its running session record. Returns
// ok=false (with an info log) when no running session exists.
func (h *Handler) findRunning(ctx context.Context, slug, event string) (*model.AgentSession, bool) {
	if slug == "" {
		return nil, false
	}
	sessions, err := h.store.ListSessions(ctx, slug)
	if err != nil {
		slog.Info("hook lookup failed",
			slog.String("event", event),
			slog.String("slug", slug),
			slog.Any("err", err))
		return nil, false
	}
	for i := range sessions {
		if sessions[i].Status == model.SessionStatusRunning {
			s := sessions[i]
			return &s, true
		}
	}
	slog.Info("hook for slug with no running session",
		slog.String("event", event),
		slog.String("slug", slug))
	return nil, false
}

// stopReasonFromEnd maps Claude's SessionEnd `reason` string to our
// SessionStopReason enum. Unknown / empty reasons fall back to Exit.
func stopReasonFromEnd(reason string) model.SessionStopReason {
	switch reason {
	case "clear":
		return model.SessionStopReasonCleared
	case "compact":
		return model.SessionStopReasonCompacted
	default:
		return model.SessionStopReasonExit
	}
}

func (h *Handler) emit(slug, kind string, data any) {
	if h.events == nil {
		return
	}
	h.events.Publish(pubsub.Event{
		Name: fmt.Sprintf("session:%s:hook:%s", slug, kind),
		Data: data,
	})
}

// appendToolHistory writes a tool-call event into the idea's
// history.jsonl. Shared by HandlePreToolUse (event=tool_call_started)
// and HandleToolUse (event=tool_call_completed or tool_failure).
// Tool response is included raw so a future failure-detail UI can
// pull error text without re-fetching from the transcript.
func (h *Handler) appendToolHistory(ctx context.Context, slug, event, toolName string, toolInput map[string]any, toolResponse json.RawMessage, isError bool) error {
	session, ok := h.findRunning(ctx, slug, event)
	if !ok {
		return nil
	}
	fields := map[string]any{"tool_name": toolName}
	if len(toolInput) > 0 {
		fields["tool_input"] = toolInput
	}
	if len(toolResponse) > 0 {
		fields["tool_response"] = parseRawSafe(toolResponse)
	}
	if isError {
		fields["is_error"] = true
	}
	return h.store.AppendHistory(ctx, slug, model.HistoryEvent{
		Timestamp: time.Now().UTC(),
		Event:     event,
		Session:   session.UUID,
		Fields:    fields,
	})
}

// parseRawSafe parses raw JSON defensively, returning a map or the raw string.
func parseRawSafe(raw json.RawMessage) any {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	return data
}

// Compile-time interface check.
var _ claudecode.HookHandler = (*Handler)(nil)
