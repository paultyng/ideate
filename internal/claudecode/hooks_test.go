package claudecode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordingHandler captures calls to HookHandler methods for assertions.
type recordingHandler struct {
	stops         []StopEvent
	preToolUses   []PreToolUseEvent
	toolUses      []ToolUseEvent
	ends          []EndEvent
	prompts       []PromptEvent
	notifications []NotificationEvent
	sessionStarts []SessionStartEvent
	preCompacts   []PreCompactEvent
	err           error // returned by all methods when non-nil
}

func (h *recordingHandler) HandleStop(_ context.Context, event StopEvent) error {
	h.stops = append(h.stops, event)
	return h.err
}

func (h *recordingHandler) HandlePreToolUse(_ context.Context, event PreToolUseEvent) error {
	h.preToolUses = append(h.preToolUses, event)
	return h.err
}

func (h *recordingHandler) HandleToolUse(_ context.Context, event ToolUseEvent) error {
	h.toolUses = append(h.toolUses, event)
	return h.err
}

func (h *recordingHandler) HandleEnd(_ context.Context, event EndEvent) error {
	h.ends = append(h.ends, event)
	return h.err
}

func (h *recordingHandler) HandlePrompt(_ context.Context, event PromptEvent) error {
	h.prompts = append(h.prompts, event)
	return h.err
}

func (h *recordingHandler) HandleNotification(_ context.Context, event NotificationEvent) error {
	h.notifications = append(h.notifications, event)
	return h.err
}

func (h *recordingHandler) HandleSessionStart(_ context.Context, event SessionStartEvent) error {
	h.sessionStarts = append(h.sessionStarts, event)
	return h.err
}

func (h *recordingHandler) HandlePreCompact(_ context.Context, event PreCompactEvent) error {
	h.preCompacts = append(h.preCompacts, event)
	return h.err
}

var _ HookHandler = (*recordingHandler)(nil)

// hookReq builds a hook POST. The third arg is the idea slug sent in the
// X-Ideate-Idea-Slug header (the value the hook server resolves on); kept
// as the parameter name `sessionID` for backwards-compat with existing
// test call sites that pass values like "ses-1".
func hookReq(path, sessionID, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if sessionID != "" {
		req.Header.Set(IdeaSlugHeader, sessionID)
		// The hook server also requires X-Ideate-Session-Id; tests using
		// AllowAnySession just need a non-empty value to pass the gate.
		req.Header.Set(SessionHeader, "test-session-"+sessionID)
	}
	return req
}

func TestHookServerStop(t *testing.T) {
	t.Parallel()
	rec := &recordingHandler{}
	srv := NewHookServer(rec, AllowAnySession)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hookReq("/hooks/stop", "ses-1", `{"message":"done"}`))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if len(rec.stops) != 1 {
		t.Fatalf("expected 1 stop event, got %d", len(rec.stops))
	}
	if rec.stops[0].IdeaSlug != "ses-1" {
		t.Errorf("sessionID = %q, want %q", rec.stops[0].IdeaSlug, "ses-1")
	}
	if string(rec.stops[0].Raw) != `{"message":"done"}` {
		t.Errorf("raw = %s", rec.stops[0].Raw)
	}
}

func TestHookServerToolUse(t *testing.T) {
	t.Parallel()
	rec := &recordingHandler{}
	srv := NewHookServer(rec, AllowAnySession)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hookReq("/hooks/tool-use", "ses-1", `{"tool_name":"Edit","tool_input":{"file":"main.go"}}`))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if len(rec.toolUses) != 1 {
		t.Fatalf("expected 1 tool-use event, got %d", len(rec.toolUses))
	}
	ev := rec.toolUses[0]
	if ev.IdeaSlug != "ses-1" {
		t.Errorf("sessionID = %q", ev.IdeaSlug)
	}
	if ev.ToolName != "Edit" {
		t.Errorf("toolName = %q, want %q", ev.ToolName, "Edit")
	}
	if ev.ToolInput["file"] != "main.go" {
		t.Errorf("toolInput = %v", ev.ToolInput)
	}
}

func TestHookServerPreToolUse(t *testing.T) {
	t.Parallel()
	rec := &recordingHandler{}
	srv := NewHookServer(rec, AllowAnySession)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hookReq("/hooks/pre-tool-use", "ses-1", `{"tool_name":"Bash","tool_input":{"cmd":"ls"}}`))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if len(rec.preToolUses) != 1 {
		t.Fatalf("expected 1 pre-tool-use event, got %d", len(rec.preToolUses))
	}
	ev := rec.preToolUses[0]
	if ev.ToolName != "Bash" {
		t.Errorf("toolName = %q", ev.ToolName)
	}
	if ev.ToolInput["cmd"] != "ls" {
		t.Errorf("toolInput = %v", ev.ToolInput)
	}
}

// IsError() reads tool_response.is_error; covers the success and the
// failure path plus the missing-payload case so a future regression
// can't silently flip the failure-detection contract.
func TestToolUseEventIsError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"missing", `{"tool_name":"Edit"}`, false},
		{"success", `{"tool_name":"Edit","tool_response":{"is_error":false,"data":"ok"}}`, false},
		{"failure", `{"tool_name":"Edit","tool_response":{"is_error":true,"error":"boom"}}`, true},
		{"non_object", `{"tool_name":"Edit","tool_response":"plain text"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &recordingHandler{}
			srv := NewHookServer(rec, AllowAnySession)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, hookReq("/hooks/tool-use", "ses-1", tc.body))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			if len(rec.toolUses) != 1 {
				t.Fatalf("expected 1 event")
			}
			if got := rec.toolUses[0].IsError(); got != tc.want {
				t.Errorf("IsError = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHookServerEnd(t *testing.T) {
	t.Parallel()
	rec := &recordingHandler{}
	srv := NewHookServer(rec, AllowAnySession)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hookReq("/hooks/end", "ses-1", `{}`))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if len(rec.ends) != 1 {
		t.Fatalf("expected 1 end event, got %d", len(rec.ends))
	}
	if rec.ends[0].IdeaSlug != "ses-1" {
		t.Errorf("sessionID = %q", rec.ends[0].IdeaSlug)
	}
}

func TestHookServerPrompt(t *testing.T) {
	t.Parallel()
	rec := &recordingHandler{}
	srv := NewHookServer(rec, AllowAnySession)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hookReq("/hooks/prompt", "ses-1", `{"prompt":"go"}`))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if len(rec.prompts) != 1 {
		t.Fatalf("expected 1 prompt event, got %d", len(rec.prompts))
	}
	if rec.prompts[0].IdeaSlug != "ses-1" {
		t.Errorf("sessionID = %q", rec.prompts[0].IdeaSlug)
	}
	if rec.prompts[0].Prompt != "go" {
		t.Errorf("prompt = %q, want %q", rec.prompts[0].Prompt, "go")
	}
}

func TestHookServerNotification(t *testing.T) {
	t.Parallel()
	rec := &recordingHandler{}
	srv := NewHookServer(rec, AllowAnySession)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hookReq("/hooks/notification", "ses-1", `{"message":"approval needed"}`))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if len(rec.notifications) != 1 {
		t.Fatalf("expected 1 notification event, got %d", len(rec.notifications))
	}
	if rec.notifications[0].Message != "approval needed" {
		t.Errorf("message = %q", rec.notifications[0].Message)
	}
}

func TestHookServerSessionStart(t *testing.T) {
	t.Parallel()
	rec := &recordingHandler{}
	srv := NewHookServer(rec, AllowAnySession)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hookReq("/hooks/session-start", "test-idea",
		`{"source":"clear","session_id":"new-id-123"}`))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if len(rec.sessionStarts) != 1 {
		t.Fatalf("expected 1 session-start event, got %d", len(rec.sessionStarts))
	}
	ev := rec.sessionStarts[0]
	if ev.IdeaSlug != "test-idea" {
		t.Errorf("ideaSlug = %q, want test-idea", ev.IdeaSlug)
	}
	if ev.Source != "clear" {
		t.Errorf("source = %q, want clear", ev.Source)
	}
	// The body's session_id field is captured into HookEvent.SessionID
	// (Claude's per-conversation id, distinct from the idea slug).
	if ev.SessionID != "new-id-123" {
		t.Errorf("sessionID = %q, want new-id-123", ev.SessionID)
	}
}

func TestHookServerPreCompact(t *testing.T) {
	t.Parallel()
	rec := &recordingHandler{}
	srv := NewHookServer(rec, AllowAnySession)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hookReq("/hooks/pre-compact", "test-idea", `{"trigger":"manual"}`))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if len(rec.preCompacts) != 1 {
		t.Fatalf("expected 1 pre-compact event, got %d", len(rec.preCompacts))
	}
	if rec.preCompacts[0].Trigger != "manual" {
		t.Errorf("trigger = %q, want manual", rec.preCompacts[0].Trigger)
	}
}

func TestHookServerMethodNotAllowed(t *testing.T) {
	t.Parallel()
	srv := NewHookServer(&recordingHandler{}, AllowAnySession)

	req := httptest.NewRequest(http.MethodGet, "/hooks/stop", nil)
	req.Header.Set(SessionHeader, "ses-1")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHookServerMissingHeader(t *testing.T) {
	t.Parallel()
	srv := NewHookServer(&recordingHandler{}, AllowAnySession)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hookReq("/hooks/stop", "", `{}`))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHookServerUnknownEvent(t *testing.T) {
	t.Parallel()
	srv := NewHookServer(&recordingHandler{}, AllowAnySession)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hookReq("/hooks/unknown", "ses-1", `{}`))

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHookServerBadPrefix(t *testing.T) {
	t.Parallel()
	srv := NewHookServer(&recordingHandler{}, AllowAnySession)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hookReq("/other/path", "ses-1", `{}`))

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHookServerHookError(t *testing.T) {
	t.Parallel()
	rec := &recordingHandler{err: &HookError{Code: http.StatusConflict, Message: "conflict"}}
	srv := NewHookServer(rec, AllowAnySession)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hookReq("/hooks/stop", "ses-1", `{}`))

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", w.Code, http.StatusConflict)
	}
}

func TestHookServerGenericError(t *testing.T) {
	t.Parallel()
	rec := &recordingHandler{err: errors.New("something broke")}
	srv := NewHookServer(rec, AllowAnySession)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hookReq("/hooks/stop", "ses-1", `{}`))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestHookServerMalformedJSON(t *testing.T) {
	t.Parallel()
	rec := &recordingHandler{}
	srv := NewHookServer(rec, AllowAnySession)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hookReq("/hooks/tool-use", "ses-1", `not json`))

	// Should still dispatch — best-effort parsing.
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if len(rec.toolUses) != 1 {
		t.Fatalf("expected 1 tool-use event, got %d", len(rec.toolUses))
	}
	// Typed fields stay zero-valued.
	if rec.toolUses[0].ToolName != "" {
		t.Errorf("toolName = %q, want empty", rec.toolUses[0].ToolName)
	}
	// Raw preserves the original body.
	if string(rec.toolUses[0].Raw) != "not json" {
		t.Errorf("raw = %s", rec.toolUses[0].Raw)
	}
}
