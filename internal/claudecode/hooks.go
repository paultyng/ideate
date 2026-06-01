package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// HookEvent is the common envelope for all hook callbacks. IdeaSlug is the
// stable identifier across `/clear` and `/compact`; SessionID is Claude
// Code's per-conversation session id (from the JSON body), included for
// observability but not used for routing.
type HookEvent struct {
	IdeaSlug  string // from X-Ideate-Idea-Slug header
	SessionID string // from JSON body's session_id field; changes on /clear
}

// StopEvent is the payload for the Stop hook.
type StopEvent struct {
	HookEvent
	Raw json.RawMessage `json:"-"` // full original body
}

// ToolUseEvent is the payload for the PostToolUse hook.
type ToolUseEvent struct {
	HookEvent
	ToolName     string          `json:"tool_name,omitempty"`
	ToolInput    map[string]any  `json:"tool_input,omitempty"`
	ToolResponse json.RawMessage `json:"tool_response,omitempty"`
	Raw          json.RawMessage `json:"-"`
}

// IsError reports whether the tool response indicates failure. Claude
// Code's PostToolUse fires for both success and failure; the
// distinguishing signal is `tool_response.is_error: true` (the most
// stable shape across tools — error-string tools are tool-specific
// and don't surface a uniform failure flag). Returns false for any
// missing / unparseable / non-object response.
func (e *ToolUseEvent) IsError() bool {
	if len(e.ToolResponse) == 0 {
		return false
	}
	var probe struct {
		IsError bool `json:"is_error"`
	}
	if err := json.Unmarshal(e.ToolResponse, &probe); err != nil {
		return false
	}
	return probe.IsError
}

// PreToolUseEvent is the payload for the PreToolUse hook — fires
// BEFORE a tool runs. Used to bump Activity=active earlier than the
// PostToolUse signal alone (intra-tool latency is otherwise dead time
// in the chip's activity dot) and to give future pre-flight gating a
// natural hook point.
type PreToolUseEvent struct {
	HookEvent
	ToolName  string          `json:"tool_name,omitempty"`
	ToolInput map[string]any  `json:"tool_input,omitempty"`
	Raw       json.RawMessage `json:"-"`
}

// EndEvent is the payload for the SessionEnd hook. Reason describes how
// the conversation ended: "exit" (process winding down), "logout",
// "clear" (in-process /clear), "compact" (in-process /compact), or
// "prompt_input_exit" per Claude Code's hooks reference.
type EndEvent struct {
	HookEvent
	Reason string          `json:"reason,omitempty"`
	Raw    json.RawMessage `json:"-"`
}

// PromptEvent is the payload for the UserPromptSubmit hook —
// fires when the user submits a prompt. Drives Activity=active.
type PromptEvent struct {
	HookEvent
	Prompt string          `json:"prompt,omitempty"`
	Raw    json.RawMessage `json:"-"`
}

// NotificationEvent is the payload for the Notification hook — fires
// when Claude needs user attention (permission prompts, idle waits).
// Drives Activity=waiting.
type NotificationEvent struct {
	HookEvent
	Message string          `json:"message,omitempty"`
	Raw     json.RawMessage `json:"-"`
}

// SessionStartEvent is the payload for the SessionStart hook. Fires when
// Claude Code initializes a new internal session: process startup,
// `--resume`, `/clear`, or `/compact`. Source distinguishes them.
type SessionStartEvent struct {
	HookEvent
	// Source is "startup" | "resume" | "clear" | "compact" (per Claude
	// Code's hooks reference). Empty if Claude omits the field.
	Source string          `json:"source,omitempty"`
	Raw    json.RawMessage `json:"-"`
}

// PreCompactEvent is the payload for the PreCompact hook — fires before
// /compact summarizes the conversation. Drives Activity=active so the UI
// reflects that the agent is doing real work during the compaction window
// (SessionEnd reason=compact only fires after the summary completes).
//
// Trigger is "manual" (user `/compact`) or "auto" (automatic compaction);
// either way the activity transition is the same.
type PreCompactEvent struct {
	HookEvent
	Trigger string          `json:"trigger,omitempty"`
	Raw     json.RawMessage `json:"-"`
}

// HookHandler processes typed Claude Code hook events.
// Implementations contain only business logic — no HTTP awareness.
type HookHandler interface {
	HandleStop(ctx context.Context, event StopEvent) error
	HandlePreToolUse(ctx context.Context, event PreToolUseEvent) error
	HandleToolUse(ctx context.Context, event ToolUseEvent) error
	HandleEnd(ctx context.Context, event EndEvent) error
	HandlePrompt(ctx context.Context, event PromptEvent) error
	HandleNotification(ctx context.Context, event NotificationEvent) error
	HandleSessionStart(ctx context.Context, event SessionStartEvent) error
	HandlePreCompact(ctx context.Context, event PreCompactEvent) error
}

// HookError can be returned by HookHandler methods to control the HTTP response.
type HookError struct {
	Code    int // HTTP status code (0 defaults to 500)
	Message string
}

func (e *HookError) Error() string { return e.Message }

// SessionValidator reports whether a session UUID is currently registered
// (i.e. corresponds to a live agent session that Ideate spawned). Used by
// the hook server to authenticate incoming POSTs, matching the gate the
// MCP server already enforces.
type SessionValidator func(uuid string) bool

// AllowAnySession is a permissive validator used by tests; production
// callers always pass a real validator.
func AllowAnySession(string) bool { return true }

// HookServer is an http.Handler that routes Claude Code hook webhooks
// to a typed HookHandler. It handles method enforcement, session header
// extraction, body reading, JSON parsing, and path-based event dispatch.
//
// Mount at "/hooks/" — it strips that prefix to determine the event type.
type HookServer struct {
	handler  HookHandler
	validate SessionValidator
}

// NewHookServer creates a new HookServer that delegates to the given handler.
// validate authenticates the X-Ideate-Session-Id header on every POST; pass
// AllowAnySession only in tests.
func NewHookServer(handler HookHandler, validate SessionValidator) *HookServer {
	if validate == nil {
		validate = AllowAnySession
	}
	return &HookServer{handler: handler, validate: validate}
}

func (s *HookServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionUUID := r.Header.Get(SessionHeader)
	if sessionUUID == "" {
		slog.Warn("hooks: missing session header",
			slog.String("path", r.URL.Path), slog.String("remote", r.RemoteAddr))
		http.Error(w, "missing "+SessionHeader+" header", http.StatusBadRequest)
		return
	}
	if !s.validate(sessionUUID) {
		slog.Warn("hooks: unknown session",
			slog.String("session_uuid", sessionUUID),
			slog.String("path", r.URL.Path),
			slog.String("remote", r.RemoteAddr))
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}

	ideaSlug := r.Header.Get(IdeaSlugHeader)
	if ideaSlug == "" {
		http.Error(w, "missing "+IdeaSlugHeader+" header", http.StatusBadRequest)
		return
	}

	event := strings.TrimPrefix(r.URL.Path, "/hooks/")
	if event == r.URL.Path || event == "" {
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		http.Error(w, "reading body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Capture Claude's per-conversation session id from the JSON body when
	// present (best-effort). Not used for routing — the slug header is —
	// but useful for /clear-vs-/compact disambiguation later.
	var envelope struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(body, &envelope)
	base := HookEvent{IdeaSlug: ideaSlug, SessionID: envelope.SessionID}
	raw := json.RawMessage(body)

	var handleErr error
	switch event {
	case "stop":
		var ev StopEvent
		_ = json.Unmarshal(body, &ev) // best-effort
		ev.HookEvent = base
		ev.Raw = raw
		handleErr = s.handler.HandleStop(r.Context(), ev)
	case "pre-tool-use":
		var ev PreToolUseEvent
		_ = json.Unmarshal(body, &ev)
		ev.HookEvent = base
		ev.Raw = raw
		handleErr = s.handler.HandlePreToolUse(r.Context(), ev)
	case "tool-use":
		var ev ToolUseEvent
		_ = json.Unmarshal(body, &ev)
		ev.HookEvent = base
		ev.Raw = raw
		handleErr = s.handler.HandleToolUse(r.Context(), ev)
	case "end":
		var ev EndEvent
		_ = json.Unmarshal(body, &ev)
		ev.HookEvent = base
		ev.Raw = raw
		handleErr = s.handler.HandleEnd(r.Context(), ev)
	case "prompt":
		var ev PromptEvent
		_ = json.Unmarshal(body, &ev)
		ev.HookEvent = base
		ev.Raw = raw
		handleErr = s.handler.HandlePrompt(r.Context(), ev)
	case "notification":
		var ev NotificationEvent
		_ = json.Unmarshal(body, &ev)
		ev.HookEvent = base
		ev.Raw = raw
		handleErr = s.handler.HandleNotification(r.Context(), ev)
	case "session-start":
		var ev SessionStartEvent
		_ = json.Unmarshal(body, &ev)
		ev.HookEvent = base
		ev.Raw = raw
		handleErr = s.handler.HandleSessionStart(r.Context(), ev)
	case "pre-compact":
		var ev PreCompactEvent
		_ = json.Unmarshal(body, &ev)
		ev.HookEvent = base
		ev.Raw = raw
		handleErr = s.handler.HandlePreCompact(r.Context(), ev)
	default:
		http.NotFound(w, r)
		return
	}

	if handleErr != nil {
		slog.Warn("hook handler returned error",
			slog.String("event", event),
			slog.String("idea_slug", ideaSlug),
			slog.String("session_id", envelope.SessionID),
			slog.Any("err", handleErr))
		var he *HookError
		if errors.As(handleErr, &he) && he.Code != 0 {
			http.Error(w, he.Message, he.Code)
		} else {
			http.Error(w, handleErr.Error(), http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusOK)
}
