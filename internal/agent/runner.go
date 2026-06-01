package agent

import "context"

// AgentRunner spawns an agent subprocess. Implementations are per-agent-type.
type AgentRunner interface {
	Run(ctx context.Context, config SessionConfig, outputFunc OutputFunc) (*Session, error)
}

// AgentResumer is an optional interface that AgentRunners can implement to indicate
// they support resuming previous sessions (e.g. Claude's -c flag).
type AgentResumer interface {
	CanResumeSession() bool
}

// AgentStateReplayer is an optional interface signaling that the agent
// reconstructs its own visible terminal state on resume (e.g. Claude
// reprints the prior conversation when invoked with --resume). Runners
// that implement this and return true OPT OUT of cross-restart
// vscreen persistence — preloading a persisted snapshot on top of
// agent-regenerated content would render the same history twice.
//
// Runners that do NOT implement this interface (or return false) will
// have their last-seen vscreen snapshot persisted on graceful
// shutdown and replayed into the fresh emulator on resume so the
// re-mounted xterm shows continuity rather than blanking.
type AgentStateReplayer interface {
	ReplaysOwnState() bool
}

// OutputFunc is called with PTY output data. The coordinator wraps this
// to call EventsEmit.
type OutputFunc func(data []byte)
