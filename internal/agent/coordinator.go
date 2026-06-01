package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// SessionMeta tracks the idea context for a running session.
type SessionMeta struct {
	IdeaSlug  string
	AgentType string
}

// AgentCoordinator manages all active agent sessions.
type AgentCoordinator struct {
	mu             sync.RWMutex
	sessions       map[string]*Session
	runners        map[string]AgentRunner
	sessionMetas   map[string]SessionMeta // uuid → meta
	stopReasons    map[string]StopReason  // uuid → reason stashed before process exit
	configDir      string
	onOutput       func(uuid string, data []byte)
	onStatusChange func(uuid string, meta SessionMeta, status string, exitCode int)
}

// StopReason categorises why a session was stopped. Stashed before exit so
// the finalize callback can branch on it. User-initiated stops are the only
// triggered reason today; finalize otherwise falls through to "stopped".
type StopReason string

const (
	// StopReasonUser is an explicit user-initiated stop from the UI.
	StopReasonUser StopReason = "user"
)

// NewCoordinator creates a coordinator. configDir is used for session manifests.
func NewCoordinator(configDir string) *AgentCoordinator {
	return &AgentCoordinator{
		sessions:     make(map[string]*Session),
		runners:      make(map[string]AgentRunner),
		sessionMetas: make(map[string]SessionMeta),
		stopReasons:  make(map[string]StopReason),
		configDir:    configDir,
	}
}

// RegisterRunner associates an agent type string with a runner implementation.
func (c *AgentCoordinator) RegisterRunner(agentType string, runner AgentRunner) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runners[agentType] = runner
}

// ListRunnerTypes returns the names of all registered agent runners.
// Sorted so the primary runner (claude-code) comes first.
func (c *AgentCoordinator) ListRunnerTypes() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	types := make([]string, 0, len(c.runners))
	for t := range c.runners {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

// GetRunner returns the registered runner for an agent type.
func (c *AgentCoordinator) GetRunner(agentType string) AgentRunner {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.runners[agentType]
}

// SetOutputHandler sets the callback for PTY output data.
func (c *AgentCoordinator) SetOutputHandler(fn func(uuid string, data []byte)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onOutput = fn
}

// SetStatusHandler sets the callback for session status changes.
func (c *AgentCoordinator) SetStatusHandler(fn func(uuid string, meta SessionMeta, status string, exitCode int)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onStatusChange = fn
}

// Start creates and starts a new agent session, returning the session UUID.
// Either config.AgentUUID (new) or config.ResumeUUID (existing) must be set;
// the coordinator keys all live state on this UUID.
func (c *AgentCoordinator) Start(ctx context.Context, config SessionConfig) (string, error) {
	c.mu.RLock()
	runner, ok := c.runners[config.AgentType]
	onOutput := c.onOutput
	c.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("unknown agent type %q", config.AgentType)
	}

	uuid := config.AgentUUID
	if uuid == "" {
		uuid = config.ResumeUUID
	}
	if uuid == "" {
		return "", fmt.Errorf("Start requires AgentUUID or ResumeUUID")
	}
	config.SessionID = uuid

	outputFunc := func(data []byte) {
		if onOutput != nil {
			onOutput(uuid, data)
		}
	}

	session, err := runner.Run(ctx, config, outputFunc)
	if err != nil {
		return "", fmt.Errorf("starting agent session: %w", err)
	}

	c.mu.Lock()
	c.sessions[uuid] = session
	c.sessionMetas[uuid] = SessionMeta{
		IdeaSlug:  config.IdeaSlug,
		AgentType: config.AgentType,
	}
	c.mu.Unlock()

	slog.Info("coordinator: session started",
		slog.String("uuid", uuid),
		slog.String("idea_slug", config.IdeaSlug),
		slog.String("agent_type", config.AgentType),
		slog.Int("pid", session.cmd.Process.Pid))

	if c.configDir != "" {
		m := SessionManifest{
			ID:         uuid,
			Name:       config.Name,
			PID:        session.cmd.Process.Pid,
			AgentType:  config.AgentType,
			WorkingDir: config.WorkingDir,
			StartedAt:  session.startedAt,
			IdeaSlug:   config.IdeaSlug,
		}
		if err := writeManifest(c.configDir, m); err != nil {
			slog.Warn("coordinator: writing manifest",
				slog.String("uuid", uuid), slog.Any("err", err))
		}
	}

	go func() {
		<-session.Wait()
		c.mu.Lock()
		meta := c.sessionMetas[uuid]
		delete(c.sessions, uuid)
		delete(c.sessionMetas, uuid)
		onStatusChange := c.onStatusChange
		c.mu.Unlock()

		if c.configDir != "" {
			if err := removeManifest(c.configDir, uuid); err != nil {
				slog.Warn("coordinator: removing manifest on exit",
					slog.String("uuid", uuid), slog.Any("err", err))
			}
		}

		session.mu.Lock()
		status := session.status
		exitCode := session.exitCode
		session.mu.Unlock()
		slog.Info("coordinator: session exited",
			slog.String("uuid", uuid),
			slog.String("idea_slug", meta.IdeaSlug),
			slog.String("agent_type", meta.AgentType),
			slog.String("status", string(status)),
			slog.Int("exit_code", exitCode))

		if onStatusChange != nil {
			onStatusChange(uuid, meta, status, exitCode)
		}

		// Backstop: ConsumeStopReason (called from finalize via the callback
		// above) is the primary cleanup path; this delete handles orchestrator
		// sessions and any error path that bypasses finalize.
		c.mu.Lock()
		delete(c.stopReasons, uuid)
		c.mu.Unlock()
	}()

	return uuid, nil
}

// Stop stops a session by UUID.
func (c *AgentCoordinator) Stop(_ context.Context, uuid string) error {
	c.mu.RLock()
	session, ok := c.sessions[uuid]
	c.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session %q not found", uuid)
	}

	// Stash before stop so finalize sees user reason even if idle-stop
	// races and stashes idle_timeout concurrently.
	c.StashStopReason(uuid, StopReasonUser)
	return session.Stop()
}

// StashStopReason records the stop reason for a session before the process
// exits. ConsumeStopReason retrieves it inside finalizeSession so the
// correct on-disk status (dormant vs stopped) is written.
// No-op when the session is not live (already exited).
func (c *AgentCoordinator) StashStopReason(uuid string, reason StopReason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.sessions[uuid]; ok {
		c.stopReasons[uuid] = reason
	}
}

// ConsumeStopReason returns and clears the stashed stop reason for uuid.
// Returns the zero value when no reason was stashed (normal exit path).
func (c *AgentCoordinator) ConsumeStopReason(uuid string) StopReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.stopReasons[uuid]
	delete(c.stopReasons, uuid)
	return r
}

// GetSession returns the live Session for uuid, or nil if not found.
// Used by IdleStop to call session helpers directly.
func (c *AgentCoordinator) GetSession(uuid string) *Session {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessions[uuid]
}

// Write sends data to a session's PTY.
func (c *AgentCoordinator) Write(uuid string, data []byte) error {
	c.mu.RLock()
	session, ok := c.sessions[uuid]
	c.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session %q not found", uuid)
	}

	return session.Write(data)
}

// Resize changes the PTY dimensions for a session.
func (c *AgentCoordinator) Resize(uuid string, rows, cols uint16) error {
	c.mu.RLock()
	session, ok := c.sessions[uuid]
	c.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session %q not found", uuid)
	}

	return session.Resize(rows, cols)
}

// List returns info for all active sessions.
func (c *AgentCoordinator) List() []SessionInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	infos := make([]SessionInfo, 0, len(c.sessions))
	for _, s := range c.sessions {
		infos = append(infos, s.Info())
	}
	return infos
}

// RSSSessionInfo holds the fields needed by RSSWatch to poll process memory.
// LastActivity is also included so IdleStop can reuse this same struct
// without a separate coordinator query.
type RSSSessionInfo struct {
	UUID         string
	AgentType    string
	IdeaSlug     string
	PID          int
	Started      time.Time
	LastActivity time.Time
}

// ActiveSessionInfo returns per-session data for RSS polling. Only running
// sessions (those still in the live sessions map) are included.
func (c *AgentCoordinator) ActiveSessionInfo() []RSSSessionInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]RSSSessionInfo, 0, len(c.sessions))
	for uuid, s := range c.sessions {
		s.mu.Lock()
		// cmd.Process is nil before Start() returns or after Wait() reaps.
		if s.cmd == nil || s.cmd.Process == nil {
			s.mu.Unlock()
			continue
		}
		pid := s.cmd.Process.Pid
		started := s.startedAt
		s.mu.Unlock()
		meta := c.sessionMetas[uuid]
		out = append(out, RSSSessionInfo{
			UUID:         uuid,
			AgentType:    meta.AgentType,
			IdeaSlug:     meta.IdeaSlug,
			PID:          pid,
			Started:      started,
			LastActivity: s.LastActivity(),
		})
	}
	return out
}

// GetIdeaSlug returns the idea slug associated with a running session.
func (c *AgentCoordinator) GetIdeaSlug(uuid string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	meta, ok := c.sessionMetas[uuid]
	if !ok {
		return "", fmt.Errorf("session %q has no linked idea", uuid)
	}
	return meta.IdeaSlug, nil
}

// GetSessionMeta returns the full session context for a running session.
func (c *AgentCoordinator) GetSessionMeta(uuid string) (SessionMeta, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	meta, ok := c.sessionMetas[uuid]
	if !ok {
		return SessionMeta{}, fmt.Errorf("session %q has no linked idea", uuid)
	}
	return meta, nil
}

// IsRunning reports whether a session with the given UUID is currently live.
func (c *AgentCoordinator) IsRunning(uuid string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.sessions[uuid]
	return ok
}

// FindRunningSessionForDir returns the UUID of a running session in the
// given working directory, or empty string if none.
func (c *AgentCoordinator) FindRunningSessionForDir(workingDir string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for uuid, s := range c.sessions {
		s.mu.Lock()
		dir := s.cmd.Dir
		status := s.status
		s.mu.Unlock()
		if dir == workingDir && status == StatusRunning {
			return uuid
		}
	}
	return ""
}

// GetSessionReplay returns buffered terminal output for a running session.
// Used by the frontend to restore terminal state when reconnecting.
func (c *AgentCoordinator) GetSessionReplay(uuid string) ([]byte, error) {
	c.mu.RLock()
	session, ok := c.sessions[uuid]
	c.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("session %q not found", uuid)
	}

	return session.Replay(), nil
}

// PreloadSessionSnapshot feeds previously-persisted ANSI bytes into a
// running session's vscreen — used by App.resumeSession to restore
// terminal continuity across app restarts for runners that don't
// regenerate their own state on --resume.
func (c *AgentCoordinator) PreloadSessionSnapshot(uuid string, data []byte) error {
	c.mu.RLock()
	session, ok := c.sessions[uuid]
	c.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session %q not found", uuid)
	}
	session.PreloadSnapshot(data)
	return nil
}

// SnapshotForPersistence returns the per-session info needed to write
// a vscreen snapshot to disk on graceful shutdown. Cols and Rows are
// the dimensions the snapshot bytes were laid out for; restoring them
// before preload on resume keeps cursor + column anchors aligned with
// the historic content.
type SnapshotEntry struct {
	UUID      string
	IdeaSlug  string
	AgentType string
	Snapshot  []byte
	Cols      int
	Rows      int
}

// SnapshotAllRunningForPersistence captures a Snapshot of every
// currently-running session along with enough context for the caller
// to decide whether to persist it (which agent type, which idea
// slug, which UUID).
func (c *AgentCoordinator) SnapshotAllRunningForPersistence() []SnapshotEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]SnapshotEntry, 0, len(c.sessions))
	for uuid, sess := range c.sessions {
		meta := c.sessionMetas[uuid]
		cols, rows := sess.screen.Dimensions()
		out = append(out, SnapshotEntry{
			UUID:      uuid,
			IdeaSlug:  meta.IdeaSlug,
			AgentType: meta.AgentType,
			Snapshot:  sess.Replay(),
			Cols:      cols,
			Rows:      rows,
		})
	}
	return out
}

// CleanStale removes manifests for sessions whose processes are no longer running.
func (c *AgentCoordinator) CleanStale() {
	if c.configDir != "" {
		_ = cleanStaleManifests(c.configDir)
	}
}

// Shutdown stops all active sessions concurrently. Each Stop() can
// block up to 5 seconds (SIGTERM grace period before SIGKILL), so
// serializing N sessions stretched shutdown to N×5s worst case.
func (c *AgentCoordinator) Shutdown(_ context.Context) {
	c.mu.RLock()
	sessions := make([]*Session, 0, len(c.sessions))
	for _, s := range c.sessions {
		sessions = append(sessions, s)
	}
	c.mu.RUnlock()

	var wg sync.WaitGroup
	wg.Add(len(sessions))
	for _, s := range sessions {
		go func(s *Session) {
			defer wg.Done()
			_ = s.Stop()
		}(s)
	}
	wg.Wait()
}
