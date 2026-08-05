package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/paultyng/ideate/internal/agent"
	ideatecfg "github.com/paultyng/ideate/internal/config"
	ideateMCP "github.com/paultyng/ideate/internal/mcp"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/pubsub"
	"github.com/paultyng/ideate/internal/skills"
	"github.com/paultyng/ideate/internal/store"
)

func (a *App) ListActiveSessions() []ActiveSession {
	ctx := context.Background()
	ideas, err := a.svc.List(ctx)
	if err != nil {
		return nil
	}
	var out []ActiveSession
	for _, idea := range ideas {
		sessions, err := a.svc.ListSessions(ctx, idea.Slug)
		if err != nil {
			continue
		}
		updated := idea.Updated.Format(time.RFC3339Nano)
		if idea.Updated.IsZero() {
			updated = ""
		}
		for _, s := range sessions {
			if s.Status != model.SessionStatusRunning && s.Status != model.SessionStatusDormant {
				continue
			}
			activity := string(s.Activity)
			if activity == "" {
				activity = string(model.SessionActivityIdle)
			}
			out = append(out, ActiveSession{
				Slug:        idea.Slug,
				IdeaName:    idea.Name,
				IdeaStatus:  string(idea.Status),
				UUID:        s.UUID,
				AgentType:   s.Agent,
				Activity:    activity,
				Started:     s.Started.Format(time.RFC3339Nano),
				Updated:     updated,
				IdeaSummary: idea.Description,
			})
		}
	}
	return out
}

// BusyRunningSessions returns every running session with Activity set to
// active or waiting. Idle (or empty) running sessions are not busy — the
// user can leave them running across app restarts and auto-resume picks
// them up. Surfaced as a Wails binding so the frontend can preflight a
// "Quit?" prompt before the OS actually fires the close event.

func (a *App) BusyRunningSessions() []BusySession {
	ctx := context.Background()
	ideas, err := a.svc.List(ctx)
	if err != nil {
		return nil
	}
	var busy []BusySession
	for _, idea := range ideas {
		sessions, err := a.svc.ListSessions(ctx, idea.Slug)
		if err != nil {
			continue
		}
		for _, s := range sessions {
			if s.Status != model.SessionStatusRunning {
				continue
			}
			if s.Activity != model.SessionActivityActive && s.Activity != model.SessionActivityWaiting {
				continue
			}
			busy = append(busy, BusySession{
				Slug:      idea.Slug,
				IdeaName:  idea.Name,
				UUID:      s.UUID,
				AgentType: s.Agent,
				Activity:  string(s.Activity),
			})
		}
	}
	return busy
}

// GetLaunchConfig returns the initial launch config.

func (a *App) RegisterSessionViewer(sessionID string) {
	if sessionID == "" {
		return
	}
	a.viewerMu.Lock()
	a.sessionViewer[sessionID]++
	a.viewerMu.Unlock()
}

// UnregisterSessionViewer is called by TerminalPanel on unmount. The
// vscreen.Feed path keeps running, so the next mount's GetSessionReplay
// reproduces everything that arrived while no one was looking.

func (a *App) UnregisterSessionViewer(sessionID string) {
	if sessionID == "" {
		return
	}
	a.viewerMu.Lock()
	if n := a.sessionViewer[sessionID]; n > 1 {
		a.sessionViewer[sessionID] = n - 1
	} else {
		delete(a.sessionViewer, sessionID)
	}
	a.viewerMu.Unlock()
}

func (a *App) hasSessionViewer(sessionID string) bool {
	a.viewerMu.Lock()
	defer a.viewerMu.Unlock()
	return a.sessionViewer[sessionID] > 0
}

// IsTermDebugEnabled reports whether the Go process was started with
// IDEATE_TERM_DEBUG=1. The frontend uses this to gate its termDebug
// calls so the env var is the single source of truth across both
// layers (no localStorage / URL drift).

func (a *App) IsTermDebugEnabled() bool {
	return os.Getenv("IDEATE_TERM_DEBUG") == "1"
}

// GetAppStatus returns version and uptime for the frontend status footer.

func (a *App) StartSession(config agent.SessionConfig) (string, error) {
	if config.AgentUUID == "" && config.ResumeUUID == "" {
		config.AgentUUID = uuid.New().String()
	}
	if config.IdeaSlug != "" {
		// State gate: refuse on archived ideas (new starts AND resumes).
		if err := a.svc.EnsureStartable(a.ctx, config.IdeaSlug); err != nil {
			return "", err
		}
		if err := a.svc.MarkSessionActive(a.ctx, config.IdeaSlug); err != nil {
			slog.Warn("marking idea active on session start",
				slog.String("slug", config.IdeaSlug),
				slog.Any("err", err))
		}
	}
	// Inject the snapshot path so the vscreen Buffer can persist
	// its state on Close and periodically during the session.
	// The UUID is already resolved above (AgentUUID for new, ResumeUUID for resume).
	sessionUUID := config.AgentUUID
	if sessionUUID == "" {
		sessionUUID = config.ResumeUUID
	}
	config.SnapshotPath = sessionSnapshotPath(a.ideasDir, config.IdeaSlug, sessionUUID)
	return a.coordinator.Start(a.ctx, config)
}

// StopSession terminates a running session. Marks StopReason=user on
// the persisted record before killing the process so the next launch
// knows this stop was deliberate (and skips auto-resume).

func (a *App) StopSession(id string) error {
	if meta, err := a.coordinator.GetSessionMeta(id); err == nil && meta.IdeaSlug != "" {
		if sess, err := a.svc.ReadSession(a.ctx, meta.IdeaSlug, id); err == nil && sess.Status == model.SessionStatusRunning {
			sess.StopReason = model.SessionStopReasonUser
			if err := a.svc.UpdateSession(a.ctx, meta.IdeaSlug, id, *sess); err != nil {
				slog.Warn("marking session as user-stopped",
					slog.String("slug", meta.IdeaSlug), slog.String("uuid", id), slog.Any("err", err))
			}
		}
	}
	return a.coordinator.Stop(a.ctx, id)
}

// WriteToSession sends input to a session's PTY.

func (a *App) WriteToSession(id string, input string) error {
	return a.coordinator.Write(id, []byte(input))
}

// appSkillsManager adapts internal/skills to the mcp.SkillsManager
// interface so the mcp package doesn't import internal/skills directly.
type appSkillsManager struct{ ideasDir string }

func (a appSkillsManager) List() []ideateMCP.SkillStatus {
	entries := skills.List(a.ideasDir)
	out := make([]ideateMCP.SkillStatus, 0, len(entries))
	for _, e := range entries {
		out = append(out, ideateMCP.SkillStatus{
			Name:         e.Name,
			Status:       string(e.Status),
			Path:         e.Path,
			CanonicalSHA: e.CanonicalSHA,
			OnDiskSHA:    e.OnDiskSHA,
		})
	}
	return out
}

func (a appSkillsManager) Reset(name string) ([]string, error) {
	return skills.Reset(a.ideasDir, name)
}

// appSessionResolver layers ReadSessionSnapshot onto AgentCoordinator
// so the mcp package can serve persisted snapshots for dormant
// sessions without learning the on-disk path layout.
type appSessionResolver struct {
	*agent.AgentCoordinator
	ideasDir string
}

func (r appSessionResolver) ReadSessionSnapshot(slug, uuid string) ([]byte, error) {
	path := sessionSnapshotPath(r.ideasDir, slug, uuid)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading session snapshot: %w", err)
	}
	return data, nil
}

// appSessionStarter adapts App to the mcp.SessionStarter interface
// so the orchestrator's start_idea_session tool can spin up a
// subagent without internal/mcp depending on internal/app.
type appSessionStarter struct{ a *App }

func (s appSessionStarter) StartIdeaSession(slug, agentType string, resume bool) (uuid string, err error) {
	res, err := s.a.StartIdeaSession(slug, agentType, resume)
	if err != nil {
		return "", err
	}
	return res.UUID, nil
}

// SignalSessionCancel is the frontend-driven "the user just pressed
// Esc" hint. It flips Activity to idle and clears any pending review
// attribution immediately, without waiting for Claude's Stop hook to
// catch up (which lags ~30s after a tool-use cancel while the agent
// wraps up its current turn). PTY bytes — including the Esc byte
// that triggered this — flow through their own path; this is purely
// the session-record side-channel so the chip stops claiming the
// agent is doing work.
//
// No-op when the coordinator has no meta for the UUID (e.g. the
// session already exited), or when the persisted record isn't
// currently running (deduped against late Stop hooks).

func (a *App) SignalSessionCancel(uuid string) {
	meta, err := a.coordinator.GetSessionMeta(uuid)
	if err != nil || meta.IdeaSlug == "" {
		return
	}
	sess, err := a.svc.ReadSession(a.ctx, meta.IdeaSlug, uuid)
	if err != nil || sess.Status != model.SessionStatusRunning {
		return
	}
	if sess.Activity == model.SessionActivityIdle && sess.ActiveReviewID == "" {
		return
	}
	sess.Activity = model.SessionActivityIdle
	sess.ActiveReviewID = ""
	if err := a.svc.UpdateSession(a.ctx, meta.IdeaSlug, uuid, *sess); err != nil {
		slog.Warn("session cancel signal: persisting idle transition",
			slog.String("slug", meta.IdeaSlug),
			slog.String("uuid", uuid),
			slog.Any("err", err))
	}
}

// ResizeSession updates the terminal size for a session.

func (a *App) ResizeSession(id string, rows int, cols int) error {
	return a.coordinator.Resize(id, clampUint16(rows), clampUint16(cols))
}

func clampUint16(v int) uint16 {
	if v < 1 {
		return 1
	}
	if v > 0xFFFF {
		return 0xFFFF
	}
	return uint16(v)
}

// ListSessions returns info about all active sessions.

func (a *App) ListSessions() []agent.SessionInfo {
	return a.coordinator.List()
}

// GetSessionReplay returns buffered terminal output for reconnecting to a running session.
// The returned string is base64-encoded.

func (a *App) GetSessionReplay(id string) (string, error) {
	data, err := a.coordinator.GetSessionReplay(id)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// navigate is the callback passed to the IPC server.

func (a *App) StartIdeaSession(slug, agentType string, resume bool) (*SessionStartResult, error) {
	idea, err := a.svc.Get(a.ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("loading idea: %w", err)
	}

	if existing, err := a.svc.FindRunningSession(a.ctx, slug, agentType); err == nil && existing != nil {
		if a.coordinator.IsRunning(existing.UUID) {
			return nil, &SessionInUseError{UUID: existing.UUID, Err: ErrSessionAlreadyRunning}
		}
		return nil, &SessionInUseError{UUID: existing.UUID, Err: ErrSessionStaleRunning}
	}

	ideasDir := ideatecfg.DefaultIdeasDir()
	workingDir := filepath.Join(ideasDir, slug)

	event := "session_started"
	var sessionUUID string

	if resume {
		// Implicit-resume path: only consider dormant sessions. A
		// user-terminated session (status=completed/stopped, stop_reason in
		// {exit, user, cleared, compacted, orphaned}) is *not* implicitly
		// resumable — the user said "stop." Callers that want to revive a
		// terminated session must go through ResumeIdeaSession with the
		// explicit UUID. ListSessions is started-DESC so the first dormant
		// match is the most recent.
		sessions, _ := a.svc.ListSessions(a.ctx, slug)
		for _, s := range sessions {
			if s.Agent == agentType && s.Status == model.SessionStatusDormant {
				sessionUUID = s.UUID
				event = "session_resumed"
				break
			}
		}
		if sessionUUID == "" {
			resume = false
		}
	}

	if !resume {
		sessionUUID = uuid.New().String()
	}

	config := agent.SessionConfig{
		Name:       idea.Name,
		WorkingDir: workingDir,
		AgentType:  agentType,
		IdeaSlug:   slug,
	}
	if resume {
		config.ResumeUUID = sessionUUID
	} else {
		config.AgentUUID = sessionUUID
	}

	// Register the MCP server instance for this session BEFORE spawning
	// the agent process. The agent connects to /mcp during boot; if the
	// connect lands before RegisterSession runs, the MCP handler logs
	// `mcp: unknown session` and returns 404, the agent's MCP client
	// gives up, no "mcp connected:" line lands in the agent's output, and
	// downstream tests that poll for boot-readiness time out. Race
	// confirmed in test-ui run 27015848817 against testagent v0.6.3 for
	// the resume-button-test session — the warn line landed 6ms after
	// session-started, well before this point. nil guard matches the
	// adopt path in app_lifecycle.go where unit tests construct App
	// without the manager.
	if a.mcpManager != nil {
		a.mcpManager.RegisterSession(sessionUUID)
	}

	// Start the agent process BEFORE writing to the store. If start fails,
	// no orphaned "running" records are left behind.
	if _, err := a.coordinator.Start(a.ctx, config); err != nil {
		if a.mcpManager != nil {
			a.mcpManager.UnregisterSession(sessionUUID)
		}
		return nil, err
	}

	if resume {
		sessions, _ := a.svc.ListSessions(a.ctx, slug)
		for _, s := range sessions {
			if s.UUID == sessionUUID {
				s.Status = model.SessionStatusRunning
				s.Ended = nil
				if err := a.svc.WriteSession(a.ctx, slug, s.UUID, s); err != nil {
					slog.Warn("writing resumed session record",
						slog.String("slug", slug), slog.String("uuid", s.UUID),
						slog.Any("err", err))
				}
				break
			}
		}
	} else {
		if err := a.svc.WriteSession(a.ctx, slug, sessionUUID, model.AgentSession{
			UUID:       sessionUUID,
			Agent:      agentType,
			Status:     model.SessionStatusRunning,
			Started:    time.Now(),
			WorkingDir: workingDir,
		}); err != nil {
			slog.Warn("writing new session record",
				slog.String("slug", slug), slog.String("uuid", sessionUUID),
				slog.Any("err", err))
		}
	}

	if err := a.svc.AppendHistory(a.ctx, slug, model.HistoryEvent{
		Timestamp: time.Now(),
		Event:     event,
		Session:   sessionUUID,
		Fields:    map[string]any{"agent": agentType},
	}); err != nil {
		slog.Warn("appending session-start history event",
			slog.String("slug", slug), slog.String("uuid", sessionUUID),
			slog.Any("err", err))
	}

	return &SessionStartResult{UUID: sessionUUID}, nil
}

// ResumeIdeaSession resumes a specific session by UUID. Unlike
// StartIdeaSession(resume=true) — which picks the most-recent dormant
// candidate itself — this binding respects the caller's intent: it
// resumes the named session, whatever its stop reason. Used by the
// session-detail Resume button and the quick-switcher auto-resume so
// the frontend's "this UUID" choice survives the round-trip to the
// backend.
//
// Errors when: idea archived, session not found for slug, session is
// still running (no-op-resume guard), agent type doesn't support resume.
func (a *App) ResumeIdeaSession(slug, sessionUUID string) (*SessionStartResult, error) {
	if slug == "" {
		return nil, fmt.Errorf("slug is required")
	}
	if sessionUUID == "" {
		return nil, fmt.Errorf("session uuid is required")
	}

	idea, err := a.svc.Get(a.ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("loading idea: %w", err)
	}
	if err := a.svc.EnsureStartable(a.ctx, slug); err != nil {
		return nil, err
	}

	sess, err := a.svc.ReadSession(a.ctx, slug, sessionUUID)
	if err != nil {
		return nil, fmt.Errorf("loading session: %w", err)
	}
	if sess.Status == model.SessionStatusRunning {
		if a.coordinator.IsRunning(sessionUUID) {
			return nil, &SessionInUseError{UUID: sessionUUID, Err: ErrSessionAlreadyRunning}
		}
		return nil, &SessionInUseError{UUID: sessionUUID, Err: ErrSessionStaleRunning}
	}

	if !a.AgentSupportsResume(sess.Agent) {
		return nil, fmt.Errorf("agent %q does not support resume", sess.Agent)
	}

	workingDir := filepath.Join(a.ideasDir, slug)
	config := agent.SessionConfig{
		Name:       idea.Name,
		WorkingDir: workingDir,
		AgentType:  sess.Agent,
		IdeaSlug:   slug,
		ResumeUUID: sessionUUID,
	}

	if a.mcpManager != nil {
		a.mcpManager.RegisterSession(sessionUUID)
	}
	if _, err := a.coordinator.Start(a.ctx, config); err != nil {
		if a.mcpManager != nil {
			a.mcpManager.UnregisterSession(sessionUUID)
		}
		return nil, fmt.Errorf("starting agent: %w", err)
	}

	sess.Status = model.SessionStatusRunning
	sess.StopReason = ""
	sess.Ended = nil
	if err := a.svc.WriteSession(a.ctx, slug, sessionUUID, *sess); err != nil {
		slog.Warn("writing resumed session record",
			slog.String("slug", slug), slog.String("uuid", sessionUUID),
			slog.Any("err", err))
	}

	if err := a.svc.AppendHistory(a.ctx, slug, model.HistoryEvent{
		Timestamp: time.Now(),
		Event:     "session_resumed",
		Session:   sessionUUID,
		Fields:    map[string]any{"agent": sess.Agent, "by": "explicit_uuid"},
	}); err != nil {
		slog.Warn("appending session-resumed history event",
			slog.String("slug", slug), slog.String("uuid", sessionUUID),
			slog.Any("err", err))
	}

	return &SessionStartResult{UUID: sessionUUID}, nil
}

// StartRootSession launches a "orchestrator" agent session that isn't bound to
// any idea. Working dir is the ideas root, so the agent can browse and edit
// across all ideas. The MCP server registered for this session exposes only
// the cross-idea (slug-based) tools — list_ideas, create_idea, *_by_slug.
//
// Enforces at most one root session at a time via the per-dir lock. A
// session record is persisted under model.OrchestratorSlug so the same
// activity-tracking + crash-recovery + auto-resume path that idea
// sessions use applies (resume on the next launch, etc.). On resume the
// existing record is reused instead of minting a new UUID.

func (a *App) StartRootSession(agentType string) (*SessionStartResult, error) {
	workingDir := ideatecfg.DefaultIdeasDir()
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating ideas dir: %w", err)
	}

	if existingUUID := a.coordinator.FindRunningSessionForDir(workingDir); existingUUID != "" {
		// Caller asked to start; we're handing back an in-flight session
		// instead. Surface this so CI artifacts capture cross-test leaks
		// when subsequent assertions key on fresh-session state (banner
		// scrollback, MCP-connect marker position, etc.).
		slog.Info("StartRootSession: reusing existing running session",
			slog.String("uuid", existingUUID),
			slog.String("agent_type", agentType),
			slog.String("working_dir", workingDir))
		return &SessionStartResult{UUID: existingUUID}, nil
	}

	sessionUUID := uuid.New().String()
	config := agent.SessionConfig{
		Name:       "Orchestrator",
		WorkingDir: workingDir,
		AgentType:  agentType,
		AgentUUID:  sessionUUID,
		// IdeaSlug and RepoName intentionally empty in the agent runner —
		// hooks routing uses the synthetic OrchestratorSlug below.
	}

	// Register the orchestrator MCP server BEFORE spawning the agent.
	// Same race as StartIdeaSession: the agent connects to /mcp during
	// boot and a late-arriving RegisterRootSession leaves the connect
	// returning 404 — no "mcp connected:" lands in the buffer and
	// downstream readiness polling times out.
	if a.mcpManager != nil {
		a.mcpManager.RegisterRootSession(sessionUUID)
	}

	if _, err := a.coordinator.Start(a.ctx, config); err != nil {
		if a.mcpManager != nil {
			a.mcpManager.UnregisterSession(sessionUUID)
		}
		return nil, err
	}

	// Persist a session record under the synthetic orchestrator slug so the
	// resume / activity / crash-detection sweeps include it. Passive write
	// because there's no idea.md to bump for the synthetic slug.
	if err := a.svc.WriteSessionPassive(a.ctx, model.OrchestratorSlug, sessionUUID, model.AgentSession{
		UUID:       sessionUUID,
		Agent:      agentType,
		Status:     model.SessionStatusRunning,
		Started:    time.Now(),
		WorkingDir: workingDir,
	}); err != nil {
		slog.Warn("writing orchestrator session record",
			slog.String("uuid", sessionUUID), slog.Any("err", err))
	}

	// Publish idea:changed for the orchestrator slug so the drawer's
	// probe re-fires immediately. The fsnotify watcher would catch
	// the session-record write too, but its debounce delay (~hundreds
	// of ms) is racy when the start was driven via direct binding —
	// the drawer can be polled by callers before fsnotify catches up.
	if a.events != nil {
		a.events.Publish(pubsub.Event{
			Name: EventIdeaChanged,
			Data: IdeaChangedPayload{Slug: model.OrchestratorSlug},
		})
	}

	return &SessionStartResult{UUID: sessionUUID}, nil
}

// ActiveSessionResolution is the wire shape ResolveActiveSession returns to
// the frontend. Wails only marshals the first non-error return value, so
// multi-return Go shapes collapse to a single-field Promise<string> on the TS
// side; using a struct preserves the (uuid, resumed, ok) triple end-to-end.
//
// OK=false covers three cases collapsed into a single fall-back: no
// session existed; the dormant-resume attempt failed; or the slug
// failed validation. The caller in all cases falls back to the idea
// detail page. Resumed=true means a dormant session was awakened.
type ActiveSessionResolution struct {
	UUID    string `json:"uuid"`
	Resumed bool   `json:"resumed"`
	OK      bool   `json:"ok"`
}

// ResolveActiveSession implements the ideate://ideas/<slug>/active-session
// resolution chain, mirroring `sessionNav.resolveSessionTarget` on the
// frontend so the deep-link and Cmd+K quick switcher agree on what
// "active session" means:
//
//  1. Reject invalid slugs (path-traversal / wrong charset) — empty result.
//  2. If a running session exists for the idea (any agent_type) →
//     uuid, ok=true.
//  3. Else if the most-recent non-running session is user-terminated
//     (completed/stopped/failed) AND newer than every dormant, return
//     its UUID with resumed=false — the frontend navigates to the
//     session-detail page so the user can explicitly resume. Respects
//     the "user said stop" contract; previously an older dormant would
//     shadow a fresh /exit'd session.
//  4. Else iterate dormant sessions newest-first; resume the first one
//     that starts cleanly. A failing dormant doesn't abort — continue
//     to the next candidate (different agent type, different runner
//     state).
//  5. Else if any terminated session exists (no dormant beat it), return
//     its UUID with resumed=false so the frontend lands on session-detail.
//  6. Else → empty result with ok=false; caller falls back to the idea
//     detail page.
//
// The slug arrives from a URL parser on the frontend (`ideate://ideas/<slug>/active-session`).
// Validate before it reaches `ListSessions` → `filepath.Join`; an
// attacker-controlled markdown body or terminal output could otherwise
// embed `ideate://ideas/../active-session` and traverse outside the
// ideas root. Single-user desktop bounds the blast radius, but the
// guard is cheap.
func (a *App) ResolveActiveSession(slug string) *ActiveSessionResolution {
	if !model.IsValidSlug(slug) {
		slog.Warn("resolve active session: invalid slug",
			slog.String("slug", slug))
		return &ActiveSessionResolution{}
	}

	sessions, err := a.svc.ListSessions(a.ctx, slug)
	if err != nil {
		slog.Warn("resolve active session: listing sessions",
			slog.String("slug", slug), slog.Any("err", err))
		return &ActiveSessionResolution{}
	}

	// Step 1: return first running session (list is started-DESC).
	for _, s := range sessions {
		if s.Status == model.SessionStatusRunning {
			return &ActiveSessionResolution{UUID: s.UUID, OK: true}
		}
	}

	// Partition non-running entries: newest dormant (the resume candidate)
	// and newest user-terminated (completed/stopped/failed). ListSessions
	// returns started-DESC so the first match in either bucket is the
	// newest of its kind.
	var newestDormant, newestTerminated *model.AgentSession
	for i := range sessions {
		s := &sessions[i]
		switch s.Status {
		case model.SessionStatusDormant:
			if newestDormant == nil {
				newestDormant = s
			}
		case model.SessionStatusCompleted,
			model.SessionStatusStopped,
			model.SessionStatusFailed:
			if newestTerminated == nil {
				newestTerminated = s
			}
		}
	}

	// Step 2: a fresh user-terminated session outranks every older
	// dormant. Return without resuming so the frontend lands on the
	// session-detail page; the user already said stop.
	if newestTerminated != nil &&
		(newestDormant == nil || newestTerminated.Started.After(newestDormant.Started)) {
		return &ActiveSessionResolution{UUID: newestTerminated.UUID, OK: true}
	}

	// Step 3: resume the newest dormant that starts cleanly. A single
	// failing candidate (no registered runner for that agent type,
	// missing binary, etc.) shouldn't shadow other dormant entries —
	// fall through and try the next.
	for _, s := range sessions {
		if s.Status != model.SessionStatusDormant {
			continue
		}
		res, err := a.StartIdeaSession(slug, s.Agent, true)
		if err != nil {
			slog.Warn("resolve active session: resuming dormant",
				slog.String("slug", slug),
				slog.String("uuid", s.UUID),
				slog.String("agent", s.Agent),
				slog.Any("err", err))
			continue
		}
		return &ActiveSessionResolution{UUID: res.UUID, Resumed: true, OK: true}
	}

	// Step 4: no dormant either resumed or was newer — fall back to the
	// most recent terminated session if one exists. Same no-auto-resume
	// rule as step 2.
	if newestTerminated != nil {
		return &ActiveSessionResolution{UUID: newestTerminated.UUID, OK: true}
	}

	return &ActiveSessionResolution{}
}

// GetRunningIdeaSession returns the running session for (slug, agentType),
// or empty fields if none. The persisted Status=="running" record is the
// source of truth. UUID is empty when no running record exists.

func (a *App) GetRunningIdeaSession(slug, agentType string) *SessionStartResult {
	existing, err := a.svc.FindRunningSession(a.ctx, slug, agentType)
	if err != nil || existing == nil {
		return &SessionStartResult{}
	}
	return &SessionStartResult{UUID: existing.UUID}
}

// GetRunningRootSession returns the UUID of the currently running root
// orchestrator session if one exists, otherwise empty fields. The frontend
// uses this to decide whether the dashboard "Orchestrator" button should
// "Open" or "Start".

func (a *App) GetRunningRootSession() *SessionStartResult {
	workingDir := ideatecfg.DefaultIdeasDir()
	uuid := a.coordinator.FindRunningSessionForDir(workingDir)
	if uuid == "" {
		return &SessionStartResult{}
	}
	return &SessionStartResult{UUID: uuid}
}

// scanResumeCandidates returns at most one resume candidate per
// (idea, agentType) — the most recent record matching the given reason.
// Sessions whose runner doesn't support resume are skipped (e.g. an agent
// that has no --resume flag); the user can manually start a fresh one.

func (a *App) finalizeSession(slug, sessionUUID, status string) {
	session, err := a.svc.ReadSession(a.ctx, slug, sessionUUID)
	if err != nil || session.Status != model.SessionStatusRunning {
		return
	}

	now := time.Now()
	session.Status = model.SessionStatusCompleted
	if status == agent.StatusStopped {
		_ = a.coordinator.ConsumeStopReason(sessionUUID)
		session.Status = model.SessionStatusStopped
	}
	session.Ended = &now
	if err := a.svc.UpdateSession(a.ctx, slug, sessionUUID, *session); err != nil {
		slog.Warn("finalizing session record",
			slog.String("slug", slug), slog.String("uuid", sessionUUID),
			slog.Any("err", err))
	}
}

// ListIdeaSessions returns session records for an idea.

func (a *App) ListIdeaSessions(slug string) ([]model.AgentSession, error) {
	return a.svc.ListSessions(a.ctx, slug)
}

// ListSessionSummaries returns per-idea session summaries for the dashboard
// (running-count, running sessions, most-recent fallback). Cheaper than
// calling ListIdeaSessions per card. Slice (not map) so Wails generates
// the IdeaSessionSummary type into the TS bindings — see store.ListSessionSummaries.

func (a *App) ListSessionSummaries() ([]store.IdeaSessionSummary, error) {
	return a.svc.ListSessionSummaries(a.ctx)
}

// ListAgentTypes returns the registered agent runner names (for the frontend dropdown).

func (a *App) ListAgentTypes() []string {
	return a.coordinator.ListRunnerTypes()
}

// AgentSupportsResume returns whether the given agent type supports resuming sessions.

func (a *App) AgentSupportsResume(agentType string) bool {
	runner := a.coordinator.GetRunner(agentType)
	if runner == nil {
		return false
	}
	r, ok := runner.(agent.AgentResumer)
	return ok && r.CanResumeSession()
}

// summarySweepInterval is the cadence at which the App walks every
// idea and re-enqueues stale ones. Three hours matches the plan-doc
// staleness window (idea is considered "fresh enough" to skip if its
// Updated is within this window — the gate inside NeedsRegeneration
// is what actually decides per-idea, this only paces the walk).
const summarySweepInterval = 3 * time.Hour

// runSummarizerSweep fires the initial backfill walk then re-walks
// every summarySweepInterval until a.ctx is cancelled. Owned by the
// goroutine spawned from startup. EnqueueStale is cheap on a fully-
// up-to-date workspace (one summary read + session list per idea, no
// runner spawn), so the wall-clock cost of a periodic walk is small.
