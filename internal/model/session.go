package model

import (
	"time"
)

// OrchestratorSlug is the synthetic idea slug used for the root
// orchestrator session. AgentSession records live at
// <ideasDir>/<OrchestratorSlug>/sessions/ so the same persistence +
// activity-tracking + auto-resume path that idea sessions use applies.
// ListIdeas filters dirs without idea.md so the orchestrator slug never
// appears as a fake idea in the dashboard.
const OrchestratorSlug = "__orchestrator__"

// SessionStatus represents the lifecycle state of an agent session.
//
// Running sessions carry an additional Activity dimension (active/idle/waiting).
// Terminal statuses (completed/stopped/failed) carry a StopReason describing
// how the session ended.
type SessionStatus string

const (
	SessionStatusRunning   SessionStatus = "running"
	SessionStatusCompleted SessionStatus = "completed"
	SessionStatusStopped   SessionStatus = "stopped"
	SessionStatusFailed    SessionStatus = "failed" // failed to start
	// SessionStatusDormant marks an idea session whose Claude process was
	// stopped (idle-timeout / RSS-trigger) but whose record persists. UI
	// renders a read-only last-snapshot view + a Resume button; the next
	// communication MCP tool call auto-resumes. NOT used for orchestrator
	// sessions — those go directly to stopped.
	SessionStatusDormant SessionStatus = "dormant"
)

// SessionActivity is the fine-grained activity state of a running session.
// Only meaningful while Status == SessionStatusRunning.
type SessionActivity string

const (
	SessionActivityActive    SessionActivity = "active"    // agent is processing (between UserPromptSubmit and Stop)
	SessionActivityIdle      SessionActivity = "idle"      // agent finished a turn, waiting for next prompt
	SessionActivityWaiting   SessionActivity = "waiting"   // agent is blocked on user input (Notification hook)
	SessionActivityReviewing SessionActivity = "reviewing" // agent is blocked on a pending review tool result
)

// SessionStopReason explains why a non-running session is no longer running.
// Drives auto-resume: shutdown/crash resume on next launch, user/exit/
// cleared/compacted don't (the working session continues in-process for
// cleared/compacted; a new sibling record is created at SessionStart time).
type SessionStopReason string

const (
	SessionStopReasonUser      SessionStopReason = "user"      // user-initiated stop from UI
	SessionStopReasonShutdown  SessionStopReason = "shutdown"  // app shutdown stopped the session
	SessionStopReasonExit      SessionStopReason = "exit"      // agent exited cleanly (SessionEnd)
	SessionStopReasonCrash     SessionStopReason = "crash"     // process died unexpectedly
	SessionStopReasonCleared   SessionStopReason = "cleared"   // /clear ended the conversation; same process continues
	SessionStopReasonCompacted SessionStopReason = "compacted" // /compact ended the conversation; same process continues
	SessionStopReasonOrphaned  SessionStopReason = "orphaned"  // backing transcript on disk has been deleted
)

// AgentSession records an agent session linked to an idea.
// UUID is the stable session identity — used as the file key and passed to
// the agent for session management. The value is agent-type-specific
// (e.g. a UUID for Claude, an arbitrary string for other agents).
// It never changes across resume cycles.
type AgentSession struct {
	UUID       string            `json:"uuid"`                  // Stable session ID — file key, passed to agent CLI
	Agent      string            `json:"agent"`                 // "claude-code", "testagent"
	Status     SessionStatus     `json:"status"`                // running, completed, stopped, failed, dormant
	Activity   SessionActivity   `json:"activity,omitempty"`    // active|idle|waiting; meaningful only while running
	StopReason SessionStopReason `json:"stop_reason,omitempty"` // why a non-running session ended
	// PreviousUUID is set on a session record that was spawned in-process
	// from a prior session via /clear or /compact, pointing back at the
	// predecessor's UUID. Lets the UI render the chain across conversation
	// resets without losing history.
	PreviousUUID string     `json:"previous_uuid,omitempty"`
	Started      time.Time  `json:"started" ts_type:"string"`
	Ended        *time.Time `json:"ended,omitempty" ts_type:"string"`
	Outcome      string     `json:"outcome,omitempty"`
	WorkingDir   string     `json:"working_dir"`
	RepoName     string     `json:"repo_name,omitempty"`
	// ActiveReviewID is set while a review tool result is pending for this
	// session. Cleared when the review reaches a terminal status (complete /
	// cancelled / orphaned). Drives the per-session at-most-one-active-review
	// invariant and the Activity=reviewing flip.
	ActiveReviewID string `json:"active_review_id,omitempty"`
}
