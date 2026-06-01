package agent

import "time"

// SessionConfig holds parameters for starting a new agent session.
type SessionConfig struct {
	Name       string `json:"name"`
	WorkingDir string `json:"workingDir"`
	AgentType  string `json:"agentType"` // "claude-code", "testagent"

	// Idea-linked session fields (M5).
	IdeaSlug string `json:"ideaSlug,omitempty"`

	// SessionID is set by the coordinator before calling runner.Run().
	SessionID string `json:"-"`

	// Agent session UUID — stable identity passed to the agent CLI.
	// For new sessions: passed as --session-id <uuid>.
	// For resume: empty (use ResumeUUID instead).
	AgentUUID string `json:"-"`

	// ResumeUUID is the UUID of a prior session to resume.
	// Passed as --resume <uuid>. Mutually exclusive with AgentUUID.
	ResumeUUID string `json:"-"`

	// SnapshotPath is the file path for periodic and on-close vscreen
	// snapshot persistence. Set by the App before calling
	// coordinator.Start() so the Buffer can write directly without
	// importing internal/store. Empty disables persistence.
	SnapshotPath string `json:"-"`
}

// SessionInfo is the subset of session state exposed to the frontend.
type SessionInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	AgentType string    `json:"agentType"`
	Status    string    `json:"status"` // running, stopped, exited
	StartedAt time.Time `json:"startedAt" ts_type:"string"`
	IdeaSlug  string    `json:"ideaSlug,omitempty"`
}

const (
	StatusRunning = "running"
	StatusStopped = "stopped"
	StatusExited  = "exited"
)

// SessionManifest is persisted to disk so the coordinator can detect
// sessions that survived a crash and clean them up.
type SessionManifest struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	PID        int       `json:"pid"`
	AgentType  string    `json:"agentType"`
	WorkingDir string    `json:"workingDir"`
	StartedAt  time.Time `json:"startedAt" ts_type:"string"`
	IdeaSlug   string    `json:"ideaSlug,omitempty"`
}
