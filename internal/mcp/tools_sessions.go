package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/paultyng/ideate/internal/model"
)

// Tools in this file form the read-only orchestration surface exposed
// only to the orchestrator MCP server (see addRootTools). Idea-bound
// sessions never see them — the orchestrator framing is intentionally
// orchestrator-only so the privilege boundary is obvious.

// sessionView is the shared shape for `list_sessions` entries and the
// base of `get_session` output. Field names mirror the planned tool
// payloads in orchestrator-orchestrator-plan.md.
type sessionView struct {
	UUID       string `json:"uuid"`
	IdeaSlug   string `json:"idea_slug"`
	IdeaName   string `json:"idea_name"`
	AgentType  string `json:"agent_type"`
	Status     string `json:"status"`
	Activity   string `json:"activity"`
	State      string `json:"state"` // classifySessionState(Activity)
	Started    string `json:"started"`
	WorkingDir string `json:"working_dir,omitempty"`
	// IdeaSummary is the one-line headless summary of the idea, lifted
	// from the persisted sidecar. Empty when no summary has been
	// generated yet. Gives summarize-ideas / work-idea the same historical
	// context for dormant entries as for running ones.
	IdeaSummary string `json:"idea_summary,omitempty"`
	// SessionURL is the ideate:// deep-link for this session. Lets
	// skills emit a clickable permalink without reconstructing the
	// URL from the slug + uuid pair.
	SessionURL string `json:"session_url"`
	// IdeaURL routes to the idea's detail page; IdeaActiveSessionURL
	// is the synthetic "open the live session for this idea, resume
	// if dormant" deep-link. Both are stable per-idea so a skill can
	// emit them regardless of whether the user clicks while the
	// session is still live.
	IdeaURL              string `json:"idea_url"`
	IdeaActiveSessionURL string `json:"idea_active_session_url"`
}

// sessionDetail extends sessionView with derived activity timing and
// classification — returned by `get_session` AND by `list_sessions`
// (which inlines the same fields per entry to cut the orchestrator's
// 1+N call pattern down to a single round-trip).
type sessionDetail struct {
	sessionView
	LastActivityAt string `json:"last_activity_at,omitempty"`
	IdleSeconds    int64  `json:"idle_seconds"`
	State          string `json:"state"`       // active | awaiting | idle
	IdleBucket     string `json:"idle_bucket"` // <1m | Nm | Nh | Nd

	// RecentOutput is the tail of the session's vscreen snapshot,
	// populated when list_sessions is called with `include_output_lines > 0`.
	// Empty when the caller didn't ask for it. Same semantics as a
	// get_session_output call with `lines=include_output_lines,
	// strip_prompt_placeholder=true, raw=false`.
	RecentOutput string `json:"recent_output,omitempty"`
}

// classifySessionState maps the persisted Activity to the orchestrator
// state vocabulary the skills + filter DSL share. Activity "reviewing"
// folds into "active" — both mean the agent is mid-turn from the
// user's perspective.
func classifySessionState(activity string) string {
	switch activity {
	case string(model.SessionActivityActive), string(model.SessionActivityReviewing):
		return "active"
	case string(model.SessionActivityWaiting):
		return "awaiting"
	default:
		return "idle"
	}
}

// idleBucket compresses idleSeconds to one of {<1m, Nm, Nh, Nd} so
// skill output stays terse. Bucket boundaries match the canonical
// summarize-ideas / work-idea output template.
func idleBucket(idleSeconds int64) string {
	switch {
	case idleSeconds < 60:
		return "<1m"
	case idleSeconds < 60*60:
		return fmt.Sprintf("%dm", idleSeconds/60)
	case idleSeconds < 24*60*60:
		return fmt.Sprintf("%dh", idleSeconds/(60*60))
	default:
		return fmt.Sprintf("%dd", idleSeconds/(24*60*60))
	}
}

// SessionStarter spins up a new agent session in an idea (or resumes
// the most recent one). Defined at the consumer site so the mcp
// package doesn't need to import internal/app; the App provides an
// adapter forwarding to App.StartIdeaSession.
//
// Returns the stable session UUID. The internal coordinator session
// ID is deliberately not surfaced — orchestrator tools all key on
// UUID (send_session_input, get_session, goto_session, etc.), so
// leaking the coordinator concept onto the MCP wire adds nothing.
type SessionStarter interface {
	StartIdeaSession(slug, agentType string, resume bool) (uuid string, err error)
}

// SetSessionStarter wires the start-session adapter onto the manager.
// Called once on App.Startup. Empty = the start_idea_session tool
// returns an error rather than silently no-op'ing.
func (m *Manager) SetSessionStarter(s SessionStarter) {
	m.mu.Lock()
	m.starter = s
	m.mu.Unlock()
}

func startIdeaSessionTool() mcp.Tool {
	return mcp.Tool{
		Name:        "start_idea_session",
		Description: desc("start_idea_session"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug": map[string]any{
					"type":        "string",
					"description": "Idea slug as returned by list_ideas or create_idea.",
				},
				"agent_type": map[string]any{
					"type":        "string",
					"description": "Registered runner name. Defaults to `claude-code` when omitted.",
				},
				"resume": map[string]any{
					"type":        "boolean",
					"description": "If true, resume the most recent terminated session for this (slug, agent_type). Default false.",
				},
				"initial_prompt": map[string]any{
					"type":        "string",
					"description": "Optional. If non-empty, typed into the new session's prompt buffer once the agent is ready (boot + TUI raw-mode set up). Use for fire-and-forget orchestrator briefings.",
				},
				"initial_prompt_submit": map[string]any{
					"type":        "boolean",
					"description": "If true (default), submit the initial_prompt as the first turn. If false, leave it in the prompt buffer for the user to review and submit manually. Ignored when initial_prompt is empty.",
				},
			},
			Required: []string{"slug"},
		},
	}
}

// sessionID is the caller's session UUID, recorded as source_session in
// the session_initial_prompt history event. Empty for non-session callers.
func (m *Manager) handleStartIdeaSession(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		m.mu.RLock()
		starter := m.starter
		m.mu.RUnlock()
		if starter == nil {
			return mcp.NewToolResultError("session starter is not wired"), nil
		}
		slug := request.GetString("slug", "")
		if slug == "" {
			return mcp.NewToolResultError("slug is required"), nil
		}
		agentType := request.GetString("agent_type", "claude-code")
		resume := request.GetBool("resume", false)
		initialPrompt := request.GetString("initial_prompt", "")
		initialPromptSubmit := request.GetBool("initial_prompt_submit", true)

		uuid, err := starter.StartIdeaSession(slug, agentType, resume)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("start_idea_session: %v", err)), nil
		}

		// If the caller supplied an initial prompt, wait for the
		// agent's TUI to come up before typing — fresh sessions race
		// the same boot window dormant-resume hits in
		// send_session_input. Writes before the agent claims stdin go
		// to the kernel's line discipline and are silently dropped.
		// Empty prompt skips the wait entirely (no write needed; saves
		// up to defaultAgentReadyTimeout on fast-path callers).
		initialPromptDelivered := false
		if initialPrompt != "" {
			if err := waitForAgentReady(ctx, m.resolver, uuid, resolvedAgentReadyTimeout()); err != nil {
				// Session is up but the prompt didn't land. Return the
				// uuid so the caller can decide to retry via
				// send_session_input or surface to the user.
				body, mErr := json.Marshal(map[string]any{
					"uuid":                     uuid,
					"initial_prompt_delivered": false,
					"initial_prompt_submitted": false,
					"initial_prompt_error":     err.Error(),
				})
				if mErr != nil {
					return mcp.NewToolResultError(fmt.Sprintf("marshaling: %v", mErr)), nil
				}
				return mcp.NewToolResultText(string(body)), nil
			}
			// Empty prefix: the initial prompt is the user's first
			// turn, not an orchestrator-routed reply. The
			// send_session_input wrapper "orchestrator: ..." is for
			// cross-session attribution and is wrong here.
			//
			// Initial prompts are fire-and-forget by design — the
			// orchestrator briefs the session; the session does the
			// work. Block reply_to_orchestrator until the orchestrator
			// explicitly opts in via send_session_input(include_reply_hint=true).
			m.setReplyAllowed(uuid, false)
			if err := m.writeBufferedInput(uuid, "", initialPrompt, initialPromptSubmit); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("writing initial_prompt: %v", err)), nil
			}
			initialPromptDelivered = true

			// Best-effort: prompt already landed; don't fail the call on a history write.
			if err := m.store.AppendHistory(ctx, slug, model.HistoryEvent{
				Timestamp: time.Now().UTC(),
				Event:     "session_initial_prompt",
				Session:   uuid,
				Fields: map[string]any{
					"source_session": sessionID,
					"text":           initialPrompt,
					"submitted":      initialPromptSubmit,
				},
			}); err != nil {
				slog.Warn("appending session_initial_prompt history event",
					slog.String("slug", slug),
					slog.String("uuid", uuid),
					slog.Any("err", err))
			}
		}

		response := map[string]any{
			"uuid": uuid,
		}
		if initialPrompt != "" {
			response["initial_prompt_delivered"] = initialPromptDelivered
			response["initial_prompt_submitted"] = initialPromptDelivered && initialPromptSubmit
		}
		body, err := json.Marshal(response)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("encoding result: %v", err)), nil
		}
		return mcp.NewToolResultText(string(body)), nil
	}
}

func listSessionsTool() mcp.Tool {
	return mcp.Tool{
		Name:        "list_sessions",
		Description: desc("list_sessions"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"exclude_archived": map[string]any{
					"type":        "boolean",
					"description": "If true (default), drop sessions whose parent idea has status=archived. Pass false to surface them too.",
				},
				"filter": map[string]any{
					"type":        "string",
					"description": "Optional filter DSL. Whitespace-separated tokens AND together; same-key tokens within OR. Supported tokens: `s:<state>` (active|awaiting|idle), `a:<agent_type>` (claude-code|testagent|…), `#<pr_number>` (idea has a github_pr resource with that PR number). Example: `s:awaiting a:claude-code #142`.",
				},
				"include_output_lines": map[string]any{
					"type":        "integer",
					"description": "If > 0, each entry's `recent_output` field is populated with the last N lines of the session's screen (same as get_session_output with strip_prompt_placeholder=true). Defaults to 0 (no inline output). Narrower than list_ideas's session-state envelope — use this when you need session-level granularity that list_ideas doesn't expose.",
				},
			},
		},
	}
}

// sessionFilter is the parsed form of the list_sessions `filter` DSL.
// Each non-empty slice is an OR set; non-empty slices AND across keys.
// prNumber=0 means "no PR-number predicate".
type sessionFilter struct {
	states   []string
	agents   []string
	prNumber int64
}

func parseSessionFilter(s string) (sessionFilter, error) {
	var f sessionFilter
	for _, tok := range strings.Fields(s) {
		switch {
		case strings.HasPrefix(tok, "s:"):
			v := strings.TrimPrefix(tok, "s:")
			if v == "" {
				return f, fmt.Errorf("empty state in token %q", tok)
			}
			f.states = append(f.states, v)
		case strings.HasPrefix(tok, "a:"):
			v := strings.TrimPrefix(tok, "a:")
			if v == "" {
				return f, fmt.Errorf("empty agent in token %q", tok)
			}
			f.agents = append(f.agents, v)
		case strings.HasPrefix(tok, "#"):
			n, err := strconv.ParseInt(strings.TrimPrefix(tok, "#"), 10, 64)
			if err != nil || n <= 0 {
				return f, fmt.Errorf("bad PR number in token %q", tok)
			}
			if f.prNumber != 0 {
				return f, fmt.Errorf("multiple #<pr_number> tokens not supported (use one)")
			}
			f.prNumber = n
		default:
			return f, fmt.Errorf("unknown filter token %q (expected s:<state>, a:<agent>, or #<pr>)", tok)
		}
	}
	return f, nil
}

func (f sessionFilter) matches(view sessionView, idea model.Idea) bool {
	if len(f.states) > 0 && !stringIn(f.states, view.State) {
		return false
	}
	if len(f.agents) > 0 && !stringIn(f.agents, view.AgentType) {
		return false
	}
	if f.prNumber > 0 {
		hit := false
		for _, r := range idea.Resources {
			if n, ok := githubPRNumberFromURL(r); ok && n == f.prNumber {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// githubPRNumberFromURL pulls the PR number out of a github_pr resource's
// URL (e.g. https://github.com/owner/repo/pull/89 → 89). The Resource
// model doesn't carry the structured ref shape from CLAUDE.md yet, so
// parsing the URL is the only path. Returns (0, false) for non-PR
// resources or URLs that don't match the /pull/<n> pattern.
func githubPRNumberFromURL(r model.Resource) (int64, bool) {
	if r.Type != "github_pr" || r.URL == "" {
		return 0, false
	}
	parts := strings.Split(r.URL, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] != "pull" {
			continue
		}
		// Trim trailing query/fragment if any.
		num := parts[i+1]
		if idx := strings.IndexAny(num, "?#"); idx >= 0 {
			num = num[:idx]
		}
		n, err := strconv.ParseInt(num, 10, 64)
		if err == nil && n > 0 {
			return n, true
		}
	}
	return 0, false
}

func stringIn(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

func getSessionTool() mcp.Tool {
	return mcp.Tool{
		Name:        "get_session",
		Description: desc("get_session"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"uuid": map[string]any{
					"type":        "string",
					"description": "Session UUID as returned by list_sessions.",
				},
			},
			Required: []string{"uuid"},
		},
	}
}

func sendSessionInputTool() mcp.Tool {
	return mcp.Tool{
		Name:        "send_session_input",
		Description: desc("send_session_input"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"uuid": map[string]any{
					"type":        "string",
					"description": "Target session UUID (from list_sessions).",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "Text to type. Embedded newlines stay in the target's prompt buffer (sent as soft-newlines) until the final submit.",
				},
				"submit": map[string]any{
					"type":        "boolean",
					"description": "If true (default), submit the input as a turn (Enter). If false, leave it in the prompt buffer for the human to edit/submit.",
				},
				"include_reply_hint": map[string]any{
					"type":        "boolean",
					"description": "If true, the prefix advertises the `reply_to_orchestrator` tool so the receiver knows it can route a structured reply back. Default `false` — sends are fire-and-forget; set true only for interactive orchestration where you actually want a reply.",
				},
			},
			Required: []string{"uuid", "text"},
		},
	}
}

func replyToOrchestratorTool() mcp.Tool {
	return mcp.Tool{
		Name:        "reply_to_orchestrator",
		Description: desc("reply_to_orchestrator"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "Text to type into the orchestrator's prompt. Embedded newlines stay buffered (sent as soft-newlines) until the final submit.",
				},
				"submit": map[string]any{
					"type":        "boolean",
					"description": "If true (default), submit the input to the orchestrator as a turn. If false, leave it in the orchestrator's prompt buffer for the human to edit/submit.",
				},
			},
			Required: []string{"text"},
		},
	}
}

func getSessionOutputTool() mcp.Tool {
	return mcp.Tool{
		Name:        "get_session_output",
		Description: desc("get_session_output"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"uuid": map[string]any{
					"type":        "string",
					"description": "Session UUID as returned by list_sessions.",
				},
				"lines": map[string]any{
					"type":        "integer",
					"description": "Number of trailing lines to return. Defaults to 50; pass 0 for the full snapshot.",
				},
				"raw": map[string]any{
					"type":        "boolean",
					"description": "If true, return raw ANSI bytes instead of stripped text. Default false. Implies `strip_prompt_placeholder=false`.",
				},
				"strip_prompt_placeholder": map[string]any{
					"type":        "boolean",
					"description": "If true (default), drop the empty-prompt placeholder lines Claude Code renders (`❯ Try \"…\"`). They are hints to the human, not buffered input, and they confuse summarizers. Ignored when `raw=true`.",
				},
			},
			Required: []string{"uuid"},
		},
	}
}

// listLiveSessions walks every idea + collects running and dormant
// sessions into flat sessionView entries. The store is the source of
// truth for session records (the coordinator's in-memory list omits
// sessions adopted via auto-resume until they re-emit a hook).
// Dormant entries surface so summarize-ideas and work-idea see
// the full set of resumable sessions; the live-resolver guard on
// send_session_input / get_session_output keeps callers from poking
// them as if they were live.
//
// The synthetic orchestrator pseudo-idea is intentionally excluded:
// these orchestration tools are only registered on the root
// orchestrator's MCP server (see addRootTools), so any orchestrator
// entry would be the caller's own session — typing into it or
// reading its replay would echo into the same agent's input/output
// stream. Self-reference there is always a bug, so we hide it from
// every introspection path rather than relying on per-tool guards.
func (m *Manager) listLiveSessions(ctx context.Context) ([]sessionView, []model.Idea, error) {
	ideas, err := m.store.List(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("listing ideas: %w", err)
	}

	var out []sessionView
	for _, idea := range ideas {
		sessions, err := m.store.ListSessions(ctx, idea.Slug)
		if err != nil {
			continue
		}
		// Cache the per-idea summary read so two sessions on the same
		// idea (e.g. orchestrator-driven + interactive) don't do
		// duplicate disk reads.
		var summaryLine string
		var summaryRead bool
		for _, s := range sessions {
			if s.Status != model.SessionStatusRunning && s.Status != model.SessionStatusDormant {
				continue
			}
			if !summaryRead {
				if sum, err := m.store.ReadSummary(ctx, idea.Slug); err == nil && sum != nil {
					summaryLine = sum.Line
				}
				summaryRead = true
			}
			activity := string(s.Activity)
			if activity == "" {
				activity = string(model.SessionActivityIdle)
			}
			state := classifySessionState(activity)
			if s.Status == model.SessionStatusDormant {
				state = "dormant"
			}
			out = append(out, sessionView{
				UUID:                 s.UUID,
				IdeaSlug:             idea.Slug,
				IdeaName:             idea.Name,
				AgentType:            s.Agent,
				Status:               string(s.Status),
				Activity:             activity,
				State:                state,
				Started:              s.Started.Format(time.RFC3339Nano),
				WorkingDir:           s.WorkingDir,
				IdeaSummary:          summaryLine,
				SessionURL:           model.SessionURL(idea.Slug, s.UUID),
				IdeaURL:              model.IdeaURL(idea.Slug),
				IdeaActiveSessionURL: model.IdeaActiveSessionURL(idea.Slug),
			})
		}
	}
	return out, ideas, nil
}

func (m *Manager) handleListSessions(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		excludeArchived := request.GetBool("exclude_archived", true)
		filterRaw := request.GetString("filter", "")
		filter, err := parseSessionFilter(filterRaw)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("filter: %v", err)), nil
		}
		outputLines := request.GetInt("include_output_lines", 0)
		if outputLines < 0 {
			outputLines = 0
		}

		entries, ideas, err := m.listLiveSessions(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		ideaBySlug := make(map[string]model.Idea, len(ideas))
		for _, idea := range ideas {
			ideaBySlug[idea.Slug] = idea
		}

		details := make([]sessionDetail, 0, len(entries))
		for _, e := range entries {
			idea := ideaBySlug[e.IdeaSlug]
			if excludeArchived && string(idea.Status) == string(model.StatusArchived) {
				continue
			}
			if !filter.matches(e, idea) {
				continue
			}
			d := buildSessionDetail(e, idea)
			if outputLines > 0 {
				if tail, ok := m.recentOutput(e.UUID, outputLines); ok {
					d.RecentOutput = tail
				}
			}
			details = append(details, d)
		}

		data, err := json.MarshalIndent(details, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshaling: %v", err)), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// buildSessionDetail computes the derived fields (last_activity_at,
// idle_seconds, state, idle_bucket) shared by list_sessions and
// get_session. Pulling this into one function keeps the two tool
// surfaces in lockstep — adding a derived field surfaces it in both
// without a coordination patch.
func buildSessionDetail(view sessionView, idea model.Idea) sessionDetail {
	lastActivity := view.Started
	if idea.Slug == view.IdeaSlug && !idea.Updated.IsZero() {
		lastActivity = idea.Updated.Format(time.RFC3339Nano)
	}
	idle := int64(0)
	if t, perr := time.Parse(time.RFC3339Nano, lastActivity); perr == nil {
		if d := time.Since(t); d > 0 {
			idle = int64(d.Seconds())
		}
	}
	state := classifySessionState(view.Activity)
	if view.Status == string(model.SessionStatusDormant) {
		state = "dormant"
	}
	return sessionDetail{
		sessionView:    view,
		LastActivityAt: lastActivity,
		IdleSeconds:    idle,
		State:          state,
		IdleBucket:     idleBucket(idle),
	}
}

// recentOutput pulls the strip-default tail of the session's vscreen
// snapshot. Returns ("", false) when the session isn't attached to a
// live coordinator or when the snapshot read fails — list_sessions
// keeps emitting the entry; the recent_output field stays empty in
// that case.
func (m *Manager) recentOutput(uuid string, lines int) (string, bool) {
	if !m.resolver.IsRunning(uuid) {
		return "", false
	}
	snapshot, err := m.resolver.GetSessionReplay(uuid)
	if err != nil {
		return "", false
	}
	out := stripPromptPlaceholder(stripANSI(string(snapshot)))
	if lines > 0 {
		out = lastLines(out, lines)
	}
	return out, true
}

func (m *Manager) handleGetSession(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		uuid := request.GetString("uuid", "")
		if uuid == "" {
			return mcp.NewToolResultError("uuid is required"), nil
		}

		entries, ideas, err := m.listLiveSessions(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var match *sessionView
		for i := range entries {
			if entries[i].UUID == uuid {
				match = &entries[i]
				break
			}
		}
		if match == nil {
			return mcp.NewToolResultError(fmt.Sprintf("no running session with uuid %q", uuid)), nil
		}

		// last_activity_at = parent idea's Updated (bumped by every
		// session-activity hook via TouchIdea). For the orchestrator
		// (synthetic idea, no Updated bump path), fall back to Started.
		var parent model.Idea
		for _, idea := range ideas {
			if idea.Slug == match.IdeaSlug {
				parent = idea
				break
			}
		}
		detail := buildSessionDetail(*match, parent)
		data, err := json.MarshalIndent(detail, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshaling: %v", err)), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

func (m *Manager) handleGetSessionOutput(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		uuid := request.GetString("uuid", "")
		if uuid == "" {
			return mcp.NewToolResultError("uuid is required"), nil
		}

		// Refuse self-reference: orchestrator UUIDs are off-limits even
		// when the orchestrator knows them (e.g. left over from a
		// prior turn). Reading the orchestrator's own replay would
		// just feed its history back into its context.
		if isOrchestrator, err := m.isOrchestratorUUID(ctx, uuid); err == nil && isOrchestrator {
			return mcp.NewToolResultError(fmt.Sprintf("uuid %q is the orchestrator's own session and is not introspectable via this tool", uuid)), nil
		}

		entries, _, err := m.listLiveSessions(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var match *sessionView
		for i := range entries {
			if entries[i].UUID == uuid {
				match = &entries[i]
				break
			}
		}
		if match == nil {
			return mcp.NewToolResultError(fmt.Sprintf("no running or dormant session with uuid %q", uuid)), nil
		}

		var snapshot []byte
		if match.Status == string(model.SessionStatusDormant) {
			// Dormant: serve from disk so summarize-ideas can read
			// the last on-screen state without resuming the process.
			snap, err := m.resolver.ReadSessionSnapshot(match.IdeaSlug, uuid)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("reading dormant snapshot: %v", err)), nil
			}
			snapshot = snap
		} else {
			snap, err := m.resolver.GetSessionReplay(uuid)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("snapshot: %v", err)), nil
			}
			snapshot = snap
		}

		raw := request.GetBool("raw", false)
		// Default lines=50; lines=0 means "full snapshot". Negative
		// values are clamped to the default.
		lines := request.GetInt("lines", 50)
		if lines < 0 {
			lines = 50
		}
		stripPlaceholder := request.GetBool("strip_prompt_placeholder", true)

		var output string
		if raw {
			output = string(snapshot)
		} else {
			output = stripANSI(string(snapshot))
			if stripPlaceholder {
				output = stripPromptPlaceholder(output)
			}
		}
		if lines > 0 {
			output = lastLines(output, lines)
		}
		return mcp.NewToolResultText(output), nil
	}
}

// promptPlaceholderPattern matches Claude Code's empty-prompt hint —
// `❯ Try "<anything>"` (optionally with leading whitespace). Hint text
// to the human, not buffered input; summarizers misread it as a
// pending prompt otherwise.
var promptPlaceholderPattern = regexp.MustCompile(`(?m)^\s*❯\s+Try\s+".*"\s*$`)

func stripPromptPlaceholder(s string) string {
	out := promptPlaceholderPattern.ReplaceAllString(s, "")
	// Collapse double blank lines the strip might leave behind.
	out = regexp.MustCompile(`\n{3,}`).ReplaceAllString(out, "\n\n")
	return out
}

// orchestratorInputPrefix is prepended to every byte sequence
// `send_session_input` writes into a target PTY, followed by a soft
// newline so multiline `text` reads naturally below the prefix in
// the target's prompt buffer.
const orchestratorInputPrefix = "[Input from Orchestrating Agent]"

// orchestratorInputPrefixWithReplyHint advertises the reverse channel
// so the receiving idea agent knows it can reply via the
// `reply_to_orchestrator` MCP tool. Used when send_session_input is
// invoked with include_reply_hint=true — an explicit opt-in for
// interactive orchestration where the caller wants a structured reply
// routed back. The default is fire-and-forget (no reply hint).
const orchestratorInputPrefixWithReplyHint = "[Input from Orchestrating Agent — reply via the `reply_to_orchestrator` MCP tool]"

// replyInputPrefix marks input typed BACK to the orchestrator from
// an idea agent. The idea slug is templated in at write time so the
// orchestrator can route the reply back to the right context.
const replyInputPrefixFmt = "[Reply from %s]"

// setReplyAllowed records whether the target session may reply to the
// orchestrator on its next turn. Called from send_session_input + the
// start_idea_session initial_prompt path. A `false` value blocks the
// next reply_to_orchestrator from that session; map miss leaves the
// previous behavior intact (spontaneous replies still allowed).
func (m *Manager) setReplyAllowed(uuid string, allowed bool) {
	if uuid == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.replyAllowed == nil {
		m.replyAllowed = map[string]bool{}
	}
	m.replyAllowed[uuid] = allowed
}

// replyAllowedFor reports whether the session may currently reply to
// the orchestrator. Returns (allowed, explicit) — `explicit` is true
// when send_session_input has set the flag at least once for this
// session; false when the map has no entry yet (the legacy
// no-prior-send case).
func (m *Manager) replyAllowedFor(uuid string) (allowed bool, explicit bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.replyAllowed[uuid]
	return v, ok
}

// softNewline is the byte sequence that puts a newline in a TUI's
// prompt buffer WITHOUT submitting — equivalent to Shift+Enter in
// Claude Code's TUI (and what TerminalPanel's keyboard handler emits
// for that key combo). testagent's read loop also recognizes this as
// a multi-line trigger.
const softNewline = "\x1b\r"

// submitDelay paces the body→submit hand-off (see Write call site).
const submitDelay = 30 * time.Millisecond

// submitCR is the byte that submits the prompt buffer as a turn — a
// bare carriage return is Enter in raw-mode PTY input.
const submitCR = "\r"

// writeBufferedInput types `prefix` followed by a soft newline and
// `text` (with embedded \n converted to soft newlines) into the PTY
// addressed by uuid, then optionally submits as a turn. Body and
// submit-CR ship as separate writes with a brief delay so receivers
// like Claude Code don't treat the trailing \r as paste content (see
// submitDelay docs). Shared by send_session_input and
// reply_to_orchestrator so both directions of the bus speak the
// same wire format.
func (m *Manager) writeBufferedInput(uuid, prefix, text string, submit bool) error {
	safeText := strings.ReplaceAll(text, "\n", softNewline)
	body := prefix + softNewline + safeText
	if err := m.resolver.Write(uuid, []byte(body)); err != nil {
		return fmt.Errorf("writing body: %w", err)
	}
	if submit {
		time.Sleep(submitDelay)
		if err := m.resolver.Write(uuid, []byte(submitCR)); err != nil {
			return fmt.Errorf("writing submit: %w", err)
		}
	}
	return nil
}

func (m *Manager) handleSendSessionInput(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		targetUUID := request.GetString("uuid", "")
		if targetUUID == "" {
			return mcp.NewToolResultError("uuid is required"), nil
		}
		text := request.GetString("text", "")
		if text == "" {
			return mcp.NewToolResultError("text is required"), nil
		}

		// Self-input guard: typing into ANY orchestrator PTY (the
		// caller's own session, or another orchestrator record left
		// over in the store) is a bug — the response echoes back
		// into the orchestrator's input stream. Cover both the
		// "calling session's own UUID" case and the "any orchestrator
		// UUID" case, since the orchestration tools are only exposed
		// to the root orchestrator MCP server.
		sourceUUID := sessionID
		if sourceUUID != "" && sourceUUID == targetUUID {
			return mcp.NewToolResultError("cannot send_session_input to the orchestrator's own session"), nil
		}
		if isOrchestrator, err := m.isOrchestratorUUID(ctx, targetUUID); err == nil && isOrchestrator {
			return mcp.NewToolResultError(fmt.Sprintf("uuid %q is a orchestrator session — orchestration tools cannot target the orchestrator", targetUUID)), nil
		}

		// Resolve target status against the live + dormant set. Dormant
		// targets auto-resume so the write lands on a live PTY; the
		// caller learns via `resumed: true` in the response that the
		// cost was paid on this turn.
		entries, _, listErr := m.listLiveSessions(ctx)
		if listErr != nil {
			return mcp.NewToolResultError(listErr.Error()), nil
		}
		var match *sessionView
		for i := range entries {
			if entries[i].UUID == targetUUID {
				match = &entries[i]
				break
			}
		}
		if match == nil {
			return mcp.NewToolResultError(fmt.Sprintf("no running or dormant session with uuid %q", targetUUID)), nil
		}
		resumed := false
		if match.Status == string(model.SessionStatusDormant) {
			m.mu.RLock()
			starter := m.starter
			m.mu.RUnlock()
			if starter == nil {
				return mcp.NewToolResultError("dormant session cannot be resumed: starter not wired"), nil
			}
			if _, err := starter.StartIdeaSession(match.IdeaSlug, match.AgentType, true); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("auto-resume dormant session: %v", err)), nil
			}
			resumed = true
		}
		if !m.resolver.IsRunning(targetUUID) {
			return mcp.NewToolResultError(fmt.Sprintf("session %q not live after resume attempt", targetUUID)), nil
		}

		// Resumed dormant sessions need a moment for the agent process
		// to boot, enter its TUI, and put stdin in raw mode before the
		// PTY can receive input. Skipping this wait drops bytes through
		// the kernel's line discipline before the agent claims it —
		// same race shape as PR #25's MCP-connect issue, different
		// stage. Live targets were presumed-ready by the caller, so
		// skip the wait there to avoid pointless latency.
		if resumed {
			if err := waitForAgentReady(ctx, m.resolver, targetUUID, resolvedAgentReadyTimeout()); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("session resumed but agent not ready: %v (retry send_session_input or check get_session_output)", err)), nil
			}
		}

		// Pick the prefix variant. include_reply_hint defaults to false
		// — sends are fire-and-forget; the user explicitly opts in to a
		// reverse channel for interactive orchestration. Default-on
		// caused the orchestrator to relay session output and pull the
		// user back into a loop they'd already said they'd drive.
		//
		// The same flag now also gates reply_to_orchestrator: an explicit
		// false records "do not reply" on the target, and the reply
		// handler refuses. Previously the hint was advisory and the agent
		// could still call reply, sending the orchestrator back into a
		// turn the user explicitly opted out of.
		allowReply := request.GetBool("include_reply_hint", false)
		prefix := orchestratorInputPrefix
		if allowReply {
			prefix = orchestratorInputPrefixWithReplyHint
		}
		m.setReplyAllowed(targetUUID, allowReply)
		if err := m.writeBufferedInput(targetUUID, prefix, text, request.GetBool("submit", true)); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("writing to session: %v", err)), nil
		}

		// Audit log on the target idea so the human-facing
		// HistoryPanel and any future audit views can group
		// orchestrator-driven input.
		if err := m.store.AppendHistory(ctx, match.IdeaSlug, model.HistoryEvent{
			Timestamp: time.Now().UTC(),
			Event:     "orchestrator_input",
			Session:   targetUUID,
			Fields: map[string]any{
				"source_session": sourceUUID,
				"text":           text,
				"resumed":        resumed,
			},
		}); err != nil {
			slog.Warn("appending orchestrator_input history event",
				slog.String("slug", match.IdeaSlug),
				slog.String("uuid", targetUUID),
				slog.Any("err", err))
		}

		body, err := json.Marshal(map[string]any{
			"status":  "ok",
			"resumed": resumed,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshaling: %v", err)), nil
		}
		return mcp.NewToolResultText(string(body)), nil
	}
}

// isOrchestratorUUID reports whether the given session UUID belongs to
// any persisted orchestrator record (running or otherwise). Used by
// orchestration tools to refuse self-targeting on the root orchestrator
// MCP server, where every UUID the caller could pass that resolves
// under model.OrchestratorSlug is by definition the orchestrator
// itself.
func (m *Manager) isOrchestratorUUID(ctx context.Context, uuid string) (bool, error) {
	if uuid == "" {
		return false, nil
	}
	sessions, err := m.store.ListSessions(ctx, model.OrchestratorSlug)
	if err != nil {
		return false, fmt.Errorf("listing orchestrator sessions: %w", err)
	}
	for _, s := range sessions {
		if s.UUID == uuid {
			return true, nil
		}
	}
	return false, nil
}

// findRunningOrchestrator returns the UUID of the live root orchestrator
// session if any. The orchestrator lives under the synthetic
// model.OrchestratorSlug; an idea agent calling reply_to_orchestrator
// uses this to discover the target without needing the UUID up front.
//
// When more than one record passes both filters (rare but reachable on
// e.g. /clear successor races, or any window where the predecessor's
// disk status hasn't flipped yet), the *newest by Started* wins. The
// older one was almost certainly the predecessor; routing the reply
// to its (dying) PTY would silently drop the message.
func (m *Manager) findRunningOrchestrator(ctx context.Context) (string, error) {
	sessions, err := m.store.ListSessions(ctx, model.OrchestratorSlug)
	if err != nil {
		return "", fmt.Errorf("listing orchestrator sessions: %w", err)
	}
	var best *model.AgentSession
	for i := range sessions {
		s := &sessions[i]
		if s.Status != model.SessionStatusRunning {
			continue
		}
		if !m.resolver.IsRunning(s.UUID) {
			continue
		}
		if best == nil || s.Started.After(best.Started) {
			best = s
		}
	}
	if best == nil {
		return "", nil
	}
	return best.UUID, nil
}

func (m *Manager) handleReplyToOrchestrator(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		text := request.GetString("text", "")
		if text == "" {
			return mcp.NewToolResultError("text is required"), nil
		}

		// Reply gate: if the orchestrator's most recent send to this
		// session explicitly set include_reply_hint=false (or the
		// session was briefed via start_idea_session.initial_prompt),
		// the reply tool refuses. Map miss = no recent orchestrator
		// send = spontaneous reply, which we still allow.
		if allowed, explicit := m.replyAllowedFor(sessionID); explicit && !allowed {
			return mcp.NewToolResultError(
				"reply_to_orchestrator blocked: the orchestrator's last send_session_input set include_reply_hint=false (fire-and-forget). Wait for the user to opt in to a reply channel.",
			), nil
		}

		// Resolve the calling idea so the prefix can identify the
		// source. GetIdeaSlug only succeeds for idea-bound MCP
		// servers — the orchestrator's own session has no slug, and
		// it shouldn't be calling this tool anyway (reply_to_orchestrator
		// is registered only on per-idea MCPs).
		slug, err := m.resolver.GetIdeaSlug(sessionID)
		if err != nil || slug == "" {
			return mcp.NewToolResultError("reply_to_orchestrator must be called from an idea-bound session"), nil
		}

		orchUUID, err := m.findRunningOrchestrator(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if orchUUID == "" {
			return mcp.NewToolResultError("no running orchestrator (root orchestrator) session"), nil
		}

		// Source-name on the prefix is the idea's display name when
		// available; falls back to the slug. Read-only lookup, ignore
		// errors — slug-only is still informative.
		display := slug
		if idea, ierr := m.store.Get(ctx, slug); ierr == nil && idea != nil && idea.Name != "" {
			display = idea.Name
		}
		prefix := fmt.Sprintf(replyInputPrefixFmt, display)

		if err := m.writeBufferedInput(orchUUID, prefix, text, request.GetBool("submit", true)); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("writing to orchestrator: %v", err)), nil
		}

		// Reset the gate to fire-and-forget after a successful reply.
		// The opt-in is a single-shot grant: the orchestrator briefs the
		// session, the session replies once, the channel closes. Without
		// this reset the session stays permanently open for additional
		// unsolicited replies until the orchestrator's next explicit
		// fire-and-forget send — which would defer the original bug by
		// one turn instead of fixing it.
		m.setReplyAllowed(sessionID, false)

		// Audit log on the source idea — the reply is a state change
		// the human may want to see in the timeline of THIS idea, not
		// the orchestrator's history (which has no idea slug).
		if err := m.store.AppendHistory(ctx, slug, model.HistoryEvent{
			Timestamp: time.Now().UTC(),
			Event:     "orchestrator_reply",
			Session:   sessionID,
			Fields: map[string]any{
				"target_session": orchUUID,
				"text":           text,
			},
		}); err != nil {
			slog.Warn("appending orchestrator_reply history event",
				slog.String("slug", slug),
				slog.String("uuid", sessionID),
				slog.Any("err", err))
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// ansiPattern strips the common terminal escape forms emitted by the
// vscreen renderer: CSI (`ESC [ … letter`), OSC (`ESC ] … BEL|ST`),
// charset designation (`ESC ( …`), and the rare single-byte escapes
// (`ESC =`, `ESC >`, etc). Sufficient for stripping rendered output —
// not a general-purpose terminal-control parser.
var ansiPattern = regexp.MustCompile(
	`\x1b\[[0-9;?]*[ -/]*[@-~]` + // CSI
		`|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)` + // OSC (BEL- or ST-terminated)
		`|\x1b[()][AB012]` + // SCS (charset designation)
		`|\x1b[=>NOPM]`, // single-byte escapes
)

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

// lastLines returns the trailing n lines of s. Lines are split on \r\n
// (vscreen.Snapshot emits CRLF; see internal/agent/vscreen for the
// LF→CRLF conversion).
func lastLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	parts := strings.Split(s, "\r\n")
	// Drop a trailing empty line so requesting 1 line returns the last
	// non-empty row rather than the empty tail after the last CRLF.
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) <= n {
		return strings.Join(parts, "\r\n")
	}
	return strings.Join(parts[len(parts)-n:], "\r\n")
}
