package agent

import (
	"io"
	"log/slog"
	"os"
)

// IDEATE_TERM_DEBUG=1 enables PTY/vscreen/replay diagnostic logging.
// Off by default so production builds stay quiet. Every line emitted
// through termLog carries session_id (and idea_slug when known) so
// the user can grep a noisy log down to a single session under
// investigation.
const termDebugEnvVar = "IDEATE_TERM_DEBUG"

// termLog routes terminal-pipeline diagnostics to stderr when the env
// var is set, or discards them otherwise. Cheap on the hot path: when
// disabled the underlying handler short-circuits before format args
// are evaluated.
var termLog = func() *slog.Logger {
	if os.Getenv(termDebugEnvVar) != "1" {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}()
