package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/paultyng/ideate/internal/model"
)

func setupOrchestrator(t *testing.T) (*Manager, *fakeStore, *fakeResolver) {
	t.Helper()
	store := newFakeStore()
	now := time.Now().UTC()

	store.ideas["alpha"] = &model.Idea{
		Slug: "alpha", Name: "Alpha Idea", Status: model.StatusActive,
		Updated: now.Add(-30 * time.Second),
	}
	store.ideas["beta"] = &model.Idea{
		Slug: "beta", Name: "Beta Idea", Status: model.StatusActive,
	}
	store.sessions = map[string][]model.AgentSession{
		"alpha": {
			{
				UUID: "alpha-running", Agent: "claude-code",
				Status:     model.SessionStatusRunning,
				Activity:   model.SessionActivityIdle,
				Started:    now.Add(-2 * time.Hour),
				WorkingDir: "/tmp/alpha",
			},
			{
				UUID: "alpha-dormant", Agent: "claude-code",
				Status:  model.SessionStatusDormant,
				Started: now.Add(-90 * time.Minute),
			},
			{
				UUID: "alpha-stopped", Agent: "claude-code",
				Status:  model.SessionStatusStopped,
				Started: now.Add(-3 * time.Hour),
			},
		},
		"beta": {
			{
				UUID: "beta-running", Agent: "testagent",
				Status:   model.SessionStatusRunning,
				Activity: model.SessionActivityActive,
				Started:  now.Add(-10 * time.Minute),
			},
		},
		// Orchestrator pseudo-idea. Persisted under model.OrchestratorSlug
		// but intentionally NEVER surfaced by listRunningSessions —
		// orchestration tools refuse self-targeting.
		model.OrchestratorSlug: {
			{
				UUID: "scratch-running", Agent: "claude-code",
				Status:   model.SessionStatusRunning,
				Activity: model.SessionActivityIdle,
				Started:  now.Add(-5 * time.Minute),
			},
		},
	}

	resolver := &fakeResolver{
		mapping: map[string]string{},
		running: map[string]bool{
			"alpha-running":   true,
			"beta-running":    true,
			"scratch-running": true,
		},
		replays: map[string][]byte{
			"alpha-running":   []byte("\x1b[1mhello\x1b[0m\r\nworld\r\nplain text line\r\n"),
			"beta-running":    []byte("line1\r\nline2\r\nline3\r\nline4\r\n"),
			"scratch-running": []byte("scratch output\r\n"),
		},
	}
	return NewManager(store, resolver, nil), store, resolver
}

func TestListSessions_RunningAndDormant(t *testing.T) {
	t.Parallel()
	m, _, _ := setupOrchestrator(t)

	handler := m.handleListSessions("orchestrator-ses")
	res, err := handler(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}

	var entries []sessionDetail
	text := res.Content[0].(mcp.TextContent).Text
	if err := json.Unmarshal([]byte(text), &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Running and dormant entries surface; stopped and the orchestrator
	// pseudo-idea entries do not. The orchestrator exclusion is
	// documented on listLiveSessions.
	gotUUIDs := map[string]sessionDetail{}
	for _, e := range entries {
		gotUUIDs[e.UUID] = e
	}
	for _, want := range []string{"alpha-running", "alpha-dormant", "beta-running"} {
		if _, ok := gotUUIDs[want]; !ok {
			t.Errorf("missing UUID %q in output: %s", want, text)
		}
	}
	for _, off := range []string{"alpha-stopped", "scratch-running"} {
		if _, ok := gotUUIDs[off]; ok {
			t.Errorf("UUID %q should be excluded from list_sessions output", off)
		}
	}
	if entry := gotUUIDs["alpha-running"]; entry.IdeaSlug != "alpha" || entry.IdeaName != "Alpha Idea" {
		t.Errorf("alpha entry idea = %+v", entry)
	}
	if entry := gotUUIDs["alpha-dormant"]; entry.State != "dormant" {
		t.Errorf("alpha-dormant.state = %q, want \"dormant\"", entry.State)
	}
}

// s:dormant filters down to dormant entries; the other states are
// dropped. Verifies the parseSessionFilter / state-classification
// path round-trips for the dormant value.
func TestListSessions_FilterDormant(t *testing.T) {
	t.Parallel()
	m, _, _ := setupOrchestrator(t)

	handler := m.handleListSessions("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"filter": "s:dormant"}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	var entries []sessionDetail
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(entries) != 1 || entries[0].UUID != "alpha-dormant" {
		t.Errorf("s:dormant returned %d entries, want exactly alpha-dormant: %+v", len(entries), entries)
	}
}

// list_sessions returns sessionDetail-shape entries (idle bucket,
// last_activity_at, state) inline. summarize-ideas previously
// needed one get_session call per UUID for these fields.
func TestListSessions_InlinesActivityTiming(t *testing.T) {
	t.Parallel()
	m, _, _ := setupOrchestrator(t)

	handler := m.handleListSessions("orchestrator-ses")
	res, err := handler(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var entries []sessionDetail
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, e := range entries {
		if e.LastActivityAt == "" {
			t.Errorf("%s: LastActivityAt empty inline", e.UUID)
		}
		if e.IdleBucket == "" {
			t.Errorf("%s: IdleBucket empty inline", e.UUID)
		}
		if e.State == "" {
			t.Errorf("%s: State empty inline", e.UUID)
		}
		if e.RecentOutput != "" {
			t.Errorf("%s: RecentOutput populated without include_output_lines: %q", e.UUID, e.RecentOutput)
		}
	}
}

// include_output_lines populates recent_output inline so the
// orchestrator can recap N sessions in one round-trip.
func TestListSessions_IncludeOutputLines(t *testing.T) {
	t.Parallel()
	m, _, _ := setupOrchestrator(t)

	handler := m.handleListSessions("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"include_output_lines": 2}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var entries []sessionDetail
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	by := map[string]sessionDetail{}
	for _, e := range entries {
		by[e.UUID] = e
	}
	alpha := by["alpha-running"]
	if alpha.RecentOutput == "" {
		t.Fatalf("alpha-running.RecentOutput should be populated")
	}
	if strings.Contains(alpha.RecentOutput, "\x1b[") {
		t.Errorf("recent_output still has ANSI: %q", alpha.RecentOutput)
	}
	if !strings.Contains(alpha.RecentOutput, "plain text line") {
		t.Errorf("recent_output missing last line: %q", alpha.RecentOutput)
	}
}

func TestGetSession_PopulatesActivityTiming(t *testing.T) {
	t.Parallel()
	m, _, _ := setupOrchestrator(t)

	handler := m.handleGetSession("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"uuid": "alpha-running"}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}

	var detail sessionDetail
	text := res.Content[0].(mcp.TextContent).Text
	if err := json.Unmarshal([]byte(text), &detail); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if detail.UUID != "alpha-running" {
		t.Errorf("UUID = %q", detail.UUID)
	}
	if detail.LastActivityAt == "" {
		t.Errorf("LastActivityAt empty — should fall back to idea.Updated")
	}
	// Idea.Updated was set to now-30s; idle should be ≥0 and well under
	// the running session's Started-of-2-hours-ago.
	if detail.IdleSeconds < 0 || detail.IdleSeconds > 120 {
		t.Errorf("IdleSeconds = %d, expected ~30s", detail.IdleSeconds)
	}
}

func TestGetSession_UnknownUUID(t *testing.T) {
	t.Parallel()
	m, _, _ := setupOrchestrator(t)

	handler := m.handleGetSession("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"uuid": "nonexistent"}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected error result for unknown UUID, got %v", res.Content)
	}
}

func TestGetSessionOutput_StripsANSIByDefault(t *testing.T) {
	t.Parallel()
	m, _, _ := setupOrchestrator(t)

	handler := m.handleGetSessionOutput("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"uuid": "alpha-running"}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if strings.Contains(text, "\x1b[") {
		t.Errorf("output still contains ANSI escapes: %q", text)
	}
	if !strings.Contains(text, "hello") || !strings.Contains(text, "world") {
		t.Errorf("output missing expected text: %q", text)
	}
}

func TestGetSessionOutput_RawPreservesANSI(t *testing.T) {
	t.Parallel()
	m, _, _ := setupOrchestrator(t)

	handler := m.handleGetSessionOutput("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"uuid": "alpha-running",
		"raw":  true,
	}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "\x1b[1m") {
		t.Errorf("raw output missing original ANSI: %q", text)
	}
}

func TestGetSessionOutput_LinesTrim(t *testing.T) {
	t.Parallel()
	m, _, _ := setupOrchestrator(t)

	handler := m.handleGetSessionOutput("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"uuid":  "beta-running",
		"lines": 2,
	}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	text := res.Content[0].(mcp.TextContent).Text
	// Replay was 4 lines (line1..line4 + trailing CRLF). Last 2 = line3, line4.
	if !strings.Contains(text, "line3") || !strings.Contains(text, "line4") {
		t.Errorf("expected last 2 lines, got %q", text)
	}
	if strings.Contains(text, "line1") || strings.Contains(text, "line2") {
		t.Errorf("earlier lines leaked through trim: %q", text)
	}
}

// Dormant sessions serve their persisted snapshot — summarize-style
// callers must not trigger a resume just to read the last on-screen
// state.
func TestGetSessionOutput_DormantServesSnapshot(t *testing.T) {
	t.Parallel()
	m, _, resolver := setupOrchestrator(t)
	resolver.snapshots = map[string][]byte{
		"alpha/alpha-dormant": []byte("snapshot line one\nsnapshot line two\n"),
	}

	handler := m.handleGetSessionOutput("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"uuid": "alpha-dormant"}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "snapshot line one") || !strings.Contains(text, "snapshot line two") {
		t.Errorf("output missing snapshot text: %q", text)
	}
}

// Dormant session with no persisted snapshot returns an empty string
// rather than erroring — summarize-ideas iterates the full set and
// shouldn't break on a session that crashed before its first flush.
func TestGetSessionOutput_DormantMissingSnapshot(t *testing.T) {
	t.Parallel()
	m, _, _ := setupOrchestrator(t)

	handler := m.handleGetSessionOutput("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"uuid": "alpha-dormant"}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	if text := res.Content[0].(mcp.TextContent).Text; text != "" {
		t.Errorf("output = %q, want empty", text)
	}
}

// send_session_input on a dormant target resumes it before writing
// and surfaces resumed=true so the caller knows the cost was paid.
func TestSendSessionInput_AutoResumesDormant(t *testing.T) {
	t.Parallel()
	m, _, resolver := setupOrchestrator(t)
	starter := &fakeSessionStarter{uuid: "alpha-dormant"}
	m.SetSessionStarter(starter)
	// Simulate the resume making the session live so the post-resume
	// IsRunning gate and Write hit the same path the production
	// coordinator would after StartIdeaSession returns.
	starter.uuid = "alpha-dormant"
	resolver.running["alpha-dormant"] = false
	originalStart := starter
	_ = originalStart

	// Wrap the starter so the resume flips IsRunning to true mid-call.
	resumeStarter := &resumingStarter{inner: starter, resolver: resolver, uuid: "alpha-dormant"}
	m.SetSessionStarter(resumeStarter)

	handler := m.handleSendSessionInput("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"uuid": "alpha-dormant",
		"text": "hello dormant",
	}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, `"resumed":true`) {
		t.Errorf("response missing resumed:true: %q", text)
	}
	if len(resumeStarter.inner.calls) != 1 {
		t.Fatalf("expected 1 resume call, got %d", len(resumeStarter.inner.calls))
	}
	call := resumeStarter.inner.calls[0]
	if call.slug != "alpha" || call.agentType != "claude-code" || !call.resume {
		t.Errorf("resume call = %+v, want {alpha, claude-code, resume=true}", call)
	}
	resolver.writesMu.Lock()
	writes := resolver.writes["alpha-dormant"]
	resolver.writesMu.Unlock()
	if len(writes) < 1 {
		t.Errorf("expected at least one write to resumed session, got 0")
	}
}

// Dormant target without a starter wired returns an error rather
// than silently dropping the write.
func TestSendSessionInput_DormantWithoutStarter(t *testing.T) {
	t.Parallel()
	m, _, _ := setupOrchestrator(t)
	// Intentionally skip SetSessionStarter.

	handler := m.handleSendSessionInput("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"uuid": "alpha-dormant",
		"text": "hi",
	}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected error when dormant target needs resume but starter is nil, got %v", res.Content)
	}
}

// resumingStarter wraps fakeSessionStarter so the resume call also
// flips the resolver's IsRunning bit for the target uuid — mirrors
// what the live coordinator does after StartIdeaSession returns.
type resumingStarter struct {
	inner    *fakeSessionStarter
	resolver *fakeResolver
	uuid     string
}

func (r *resumingStarter) StartIdeaSession(slug, agentType string, resume bool) (string, error) {
	uuid, err := r.inner.StartIdeaSession(slug, agentType, resume)
	if err == nil {
		r.resolver.running[r.uuid] = true
	}
	return uuid, err
}

func TestGetSessionOutput_UnknownUUID(t *testing.T) {
	t.Parallel()
	m, _, _ := setupOrchestrator(t)

	handler := m.handleGetSessionOutput("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"uuid": "nonexistent"}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected error for unknown UUID, got %v", res.Content)
	}
}

func TestSendSessionInput_WrapsAndAuditLogs(t *testing.T) {
	t.Parallel()
	m, store, resolver := setupOrchestrator(t)

	handler := m.handleSendSessionInput("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"uuid": "alpha-running",
		"text": "hello agent",
	}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}

	resolver.writesMu.Lock()
	writes := resolver.writes["alpha-running"]
	resolver.writesMu.Unlock()
	// Body and submit-CR ship as separate Write() calls so the target
	// TUI drains the body through its input handler before the \r
	// arrives — packing them into one chunk causes some TUIs (Claude
	// Code) to treat the trailing \r as paste content rather than
	// submit. Default submit=true → 2 writes; submit=false → 1.
	if len(writes) != 2 {
		t.Fatalf("expected 2 writes (body + submit) to alpha-running, got %d", len(writes))
	}
	body := string(writes[0])
	// Default include_reply_hint=true → prefix advertises the reply
	// tool. Prefix lands on its own prompt-buffer line via the soft
	// newline (\x1b\r — same byte sequence Shift+Enter emits in
	// TerminalPanel).
	wantPrefix := "[Input from Orchestrating Agent — reply via the `reply_to_orchestrator` MCP tool]\x1b\r"
	if !strings.HasPrefix(body, wantPrefix) {
		t.Errorf("body missing orchestrator prefix+soft-newline: %q", body)
	}
	if !strings.Contains(body, "hello agent") {
		t.Errorf("body missing payload text: %q", body)
	}
	if strings.HasSuffix(body, "\r") {
		t.Errorf("body should NOT end with \\r — submit goes in a separate write: %q", body)
	}
	// Submit Write: a single \r byte.
	if string(writes[1]) != "\r" {
		t.Errorf("submit write = %q, want %q", string(writes[1]), "\r")
	}

	// Audit event landed on the target idea.
	if len(store.history) != 1 {
		t.Fatalf("expected 1 history event, got %d", len(store.history))
	}
	ev := store.history[0]
	if ev.Event != "orchestrator_input" {
		t.Errorf("event = %q, want orchestrator_input", ev.Event)
	}
	if ev.Session != "alpha-running" {
		t.Errorf("session = %q, want alpha-running", ev.Session)
	}
	if ev.Fields["text"] != "hello agent" {
		t.Errorf("fields.text = %v", ev.Fields["text"])
	}
}

// submit=false leaves the input staged in the target's prompt
// buffer without a terminal CR — useful for handing the human a
// pre-filled draft to edit before they hit Enter themselves.
func TestSendSessionInput_NoSubmit(t *testing.T) {
	t.Parallel()
	m, _, resolver := setupOrchestrator(t)

	handler := m.handleSendSessionInput("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"uuid":   "alpha-running",
		"text":   "draft for human",
		"submit": false,
	}

	res, err := handler(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("handler error: %v %v", err, res.Content)
	}

	resolver.writesMu.Lock()
	writes := resolver.writes["alpha-running"]
	resolver.writesMu.Unlock()
	if len(writes) != 1 {
		t.Fatalf("expected 1 write")
	}
	got := string(writes[0])
	if strings.HasSuffix(got, "\r") {
		t.Errorf("submit=false write should NOT have a trailing CR: %q", got)
	}
	if !strings.Contains(got, "draft for human") {
		t.Errorf("write missing payload text: %q", got)
	}
}

// Multiline `text` keeps newlines in the prompt buffer (soft) until
// the trailing submit CR — so a multi-line message lands as ONE turn,
// not multiple separate submissions.
func TestSendSessionInput_MultilineUsesSoftNewlines(t *testing.T) {
	t.Parallel()
	m, _, resolver := setupOrchestrator(t)

	handler := m.handleSendSessionInput("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"uuid": "alpha-running",
		"text": "line one\nline two",
	}

	res, err := handler(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("handler error: %v %v", err, res.Content)
	}

	resolver.writesMu.Lock()
	writes := resolver.writes["alpha-running"]
	resolver.writesMu.Unlock()
	if len(writes) != 2 {
		t.Fatalf("expected 2 writes (body + submit), got %d", len(writes))
	}
	body := string(writes[0])
	// Embedded \n converted to \x1b\r so the target sees one buffered
	// multiline turn rather than two separate submissions.
	if strings.Contains(body, "line one\nline two") {
		t.Errorf("raw \\n leaked through to PTY (would submit prematurely): %q", body)
	}
	if !strings.Contains(body, "line one\x1b\rline two") {
		t.Errorf("expected soft-newline between lines, got %q", body)
	}
	// Body has no submit CR — submit ships separately.
	if strings.Count(body, "\r")-strings.Count(body, "\x1b\r") != 0 {
		t.Errorf("body should have no bare CR, got %q", body)
	}
	if string(writes[1]) != "\r" {
		t.Errorf("submit write = %q, want \\r", string(writes[1]))
	}
}

func TestSendSessionInput_SelfGuard(t *testing.T) {
	t.Parallel()
	m, store, resolver := setupOrchestrator(t)
	// Mark "orchestrator-ses" as resolving to scratch-running's UUID so the
	// guard fires.
	resolver.mapping = map[string]string{"orchestrator-ses": model.OrchestratorSlug}
	// fakeResolver.GetSessionUUID returns the sessionID itself by design.
	// Override the test target to match that returned UUID.
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"uuid": "orchestrator-ses", // == GetSessionUUID("orchestrator-ses")
		"text": "talking to myself",
	}

	res, err := m.handleSendSessionInput("orchestrator-ses")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected error from self-guard, got %v", res.Content)
	}
	resolver.writesMu.Lock()
	if len(resolver.writes) != 0 {
		t.Errorf("self-guard should have prevented any write, got %v", resolver.writes)
	}
	resolver.writesMu.Unlock()
	if len(store.history) != 0 {
		t.Errorf("self-guard should have prevented audit log, got %v", store.history)
	}
}

func TestSendSessionInput_UnknownUUID(t *testing.T) {
	t.Parallel()
	m, _, _ := setupOrchestrator(t)

	handler := m.handleSendSessionInput("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"uuid": "nonexistent", "text": "hi"}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected error for unknown UUID, got %v", res.Content)
	}
}

// Targeting any orchestrator UUID — even one the caller looked up from
// list_sessions or list_ideas in a prior turn — must be refused.
// reply_to_orchestrator is the only path that may write to a orchestrator.
func TestSendSessionInput_RefusesOrchestratorTarget(t *testing.T) {
	t.Parallel()
	m, store, resolver := setupOrchestrator(t)

	handler := m.handleSendSessionInput("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"uuid": "scratch-running",
		"text": "echo into myself",
	}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected error when targeting a orchestrator UUID, got %v", res.Content)
	}
	resolver.writesMu.Lock()
	defer resolver.writesMu.Unlock()
	if len(resolver.writes) != 0 {
		t.Errorf("orchestrator guard should have prevented any write, got %v", resolver.writes)
	}
	if len(store.history) != 0 {
		t.Errorf("orchestrator guard should have prevented audit log, got %v", store.history)
	}
}

// get_session_output also refuses orchestrator UUIDs — reading the
// orchestrator's own replay would feed its history back into context.
func TestGetSessionOutput_RefusesOrchestratorTarget(t *testing.T) {
	t.Parallel()
	m, _, _ := setupOrchestrator(t)

	handler := m.handleGetSessionOutput("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"uuid": "scratch-running"}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected error when reading a orchestrator UUID, got %v", res.Content)
	}
}

// include_reply_hint=false drops the "reply via reply_to_orchestrator"
// suffix on the prefix so one-way fire-and-forget sends look minimal.
func TestSendSessionInput_NoReplyHint(t *testing.T) {
	t.Parallel()
	m, _, resolver := setupOrchestrator(t)

	handler := m.handleSendSessionInput("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"uuid":               "alpha-running",
		"text":               "fire and forget",
		"include_reply_hint": false,
	}

	res, err := handler(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("handler error: %v %v", err, res.Content)
	}

	resolver.writesMu.Lock()
	body := string(resolver.writes["alpha-running"][0])
	resolver.writesMu.Unlock()
	if !strings.HasPrefix(body, "[Input from Orchestrating Agent]\x1b\r") {
		t.Errorf("body should use bare prefix when include_reply_hint=false: %q", body)
	}
	if strings.Contains(body, "reply_to_orchestrator") {
		t.Errorf("body should NOT mention reply_to_orchestrator when hint disabled: %q", body)
	}
}

// reply_to_orchestrator routes a reply from an idea agent back to the
// running orchestrator with a "[Reply from <idea name>]" prefix.
func TestReplyToOrchestrator_WrapsAndRoutes(t *testing.T) {
	t.Parallel()
	m, store, resolver := setupOrchestrator(t)
	// Map the calling session ID to the alpha idea slug — that's how
	// the per-idea MCP knows which idea is replying.
	resolver.mapping["alpha-ses"] = "alpha"

	handler := m.handleReplyToOrchestrator("alpha-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"text": "Doc draft attached: search-strategy.md"}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}

	resolver.writesMu.Lock()
	writes := resolver.writes["scratch-running"]
	resolver.writesMu.Unlock()
	if len(writes) != 2 {
		t.Fatalf("expected 2 writes (body + submit) to scratch-running, got %d", len(writes))
	}
	body := string(writes[0])
	// Prefix uses idea Display name from the store.
	if !strings.HasPrefix(body, "[Reply from Alpha Idea]\x1b\r") {
		t.Errorf("body missing reply prefix: %q", body)
	}
	if !strings.Contains(body, "search-strategy.md") {
		t.Errorf("body missing payload: %q", body)
	}
	if string(writes[1]) != "\r" {
		t.Errorf("submit write = %q, want \\r", string(writes[1]))
	}

	// Audit event lands on the source idea (not the orchestrator).
	if len(store.history) != 1 {
		t.Fatalf("expected 1 history event, got %d", len(store.history))
	}
	ev := store.history[0]
	if ev.Event != "orchestrator_reply" {
		t.Errorf("event = %q", ev.Event)
	}
	if ev.Fields["target_session"] != "scratch-running" {
		t.Errorf("target_session = %v", ev.Fields["target_session"])
	}
}

// No running orchestrator → clear error rather than silent no-op.
func TestReplyToOrchestrator_NoOrchestrator(t *testing.T) {
	t.Parallel()
	m, _, resolver := setupOrchestrator(t)
	resolver.mapping["alpha-ses"] = "alpha"
	// Drop the running orchestrator.
	resolver.running["scratch-running"] = false
	delete(resolver.replays, "scratch-running")

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"text": "reply"}
	res, err := m.handleReplyToOrchestrator("alpha-ses")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected error when orchestrator not running, got %v", res.Content)
	}
}

// reply_to_orchestrator after the original orchestrator terminated and
// a new one started must route to the NEW one. The on-disk record for
// the old orchestrator may still carry Status=running (the App-side
// finalizeSession is gated on idea_slug != "" and so doesn't run for
// the root orchestrator; only the SessionEnd hook flips the predecessor,
// and a missed hook leaves the stale record behind). The picker must
// filter by coordinator liveness (resolver.IsRunning), not just disk
// status.
func TestReplyToOrchestrator_RoutesToLiveOrchestratorAfterRestart(t *testing.T) {
	t.Parallel()
	m, store, resolver := setupOrchestrator(t)
	resolver.mapping["alpha-ses"] = "alpha"

	now := time.Now().UTC()
	// Disk: old orchestrator stale-running (process gone, hook missed),
	// new orchestrator live-running. Started-DESC order = newest first.
	store.sessions[model.OrchestratorSlug] = []model.AgentSession{
		{
			UUID: "orch-new", Agent: "claude-code",
			Status: model.SessionStatusRunning, Started: now,
		},
		{
			UUID: "orch-stale-running", Agent: "claude-code",
			Status: model.SessionStatusRunning, Started: now.Add(-1 * time.Hour),
		},
	}
	// Coordinator: only the new orchestrator is actually live.
	resolver.running["orch-new"] = true
	resolver.running["orch-stale-running"] = false
	// Seed the writes map for the new orchestrator so we can assert
	// later.
	resolver.replays["orch-new"] = []byte("ready")

	handler := m.handleReplyToOrchestrator("alpha-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"text": "from idea"}
	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}

	resolver.writesMu.Lock()
	writesNew := resolver.writes["orch-new"]
	writesStale := resolver.writes["orch-stale-running"]
	resolver.writesMu.Unlock()
	if len(writesNew) != 2 {
		t.Errorf("expected 2 writes to orch-new (body + submit), got %d", len(writesNew))
	}
	if len(writesStale) != 0 {
		t.Errorf("expected 0 writes to orch-stale-running, got %d — picker leaked to a dead PTY", len(writesStale))
	}
}

// When MULTIPLE orchestrator records pass both the disk Status=running
// check AND resolver.IsRunning, the picker must return the newest by
// Started. Today the loop returns the first iteration match, which
// makes the result store-iteration-order dependent. Real store sorts
// started-DESC so the production-path test would pass with the fake
// seeded in the same order — but seeding oldest-first here exposes
// the ordering dependency.
func TestReplyToOrchestrator_PrefersNewestWhenMultipleLive(t *testing.T) {
	t.Parallel()
	m, store, resolver := setupOrchestrator(t)
	resolver.mapping["alpha-ses"] = "alpha"

	now := time.Now().UTC()
	// Seed OLDEST-FIRST to invert the production sort order. A correct
	// picker should still return the newest.
	store.sessions[model.OrchestratorSlug] = []model.AgentSession{
		{
			UUID: "orch-older", Agent: "claude-code",
			Status: model.SessionStatusRunning, Started: now.Add(-2 * time.Hour),
		},
		{
			UUID: "orch-newer", Agent: "claude-code",
			Status: model.SessionStatusRunning, Started: now,
		},
	}
	resolver.running["orch-older"] = true
	resolver.running["orch-newer"] = true

	handler := m.handleReplyToOrchestrator("alpha-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"text": "ambiguous live"}
	if _, err := handler(context.Background(), req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	resolver.writesMu.Lock()
	writesOlder := resolver.writes["orch-older"]
	writesNewer := resolver.writes["orch-newer"]
	resolver.writesMu.Unlock()
	if len(writesNewer) != 2 {
		t.Errorf("expected reply to land on orch-newer (newest by Started), got %d writes there", len(writesNewer))
	}
	if len(writesOlder) != 0 {
		t.Errorf("expected 0 writes to orch-older, got %d — picker used iteration order, not Started", len(writesOlder))
	}
}

// Calling reply_to_orchestrator from a session without an idea slug
// (i.e. the orchestrator itself or a stray) must error — the prefix
// can't be templated and routing back to the orchestrator from the
// orchestrator is meaningless.
func TestReplyToOrchestrator_RequiresIdeaSlug(t *testing.T) {
	t.Parallel()
	m, _, _ := setupOrchestrator(t)
	// No mapping for "stray-ses".

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"text": "reply"}
	res, err := m.handleReplyToOrchestrator("stray-ses")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected error for non-idea-bound caller, got %v", res.Content)
	}
}

func TestStripANSI_HandlesCommonForms(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"\x1b[1mbold\x1b[0m":                        "bold",
		"\x1b[31;1mred bold\x1b[0m end":             "red bold end",
		"\x1b]8;;https://x.com\x07link\x1b]8;;\x07": "link",
		"\x1b[2J\x1b[Hcleared":                      "cleared",
		"plain":                                     "plain",
	}
	for in, want := range cases {
		if got := stripANSI(in); got != want {
			t.Errorf("stripANSI(%q) = %q, want %q", in, got, want)
		}
	}
}
