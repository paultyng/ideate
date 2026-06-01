package headless

import (
	"context"
	"io"
	"time"
)

// Runner spawns a non-interactive agent invocation. The returned
// [io.ReadCloser] yields newline-delimited JSON: one [Event] per line.
//
// The runner does NOT own a background goroutine reading the pipe —
// the caller's read loop drives consumption. Callers that block while
// reading also block the subprocess (via the OS pipe buffer); this is
// the intended backpressure path. To isolate slow consumers from
// request handlers, wrap calls in a bounded worker pool.
//
// Closing the [io.ReadCloser] before EOF kills the subprocess (via
// the context wired in by adapters using exec.CommandContext).
type Runner interface {
	Run(ctx context.Context, prompt string, opts Opts) (io.ReadCloser, error)
}

// Opts configures a single [Runner.Run] invocation. All fields are
// optional; the zero value defers to upstream defaults.
type Opts struct {
	// Model is the agent's model selector ("haiku", "sonnet", "gpt-5",
	// etc.). Adapters map this onto their CLI's --model flag. Empty
	// means upstream default.
	Model string

	// MaxTokens caps the response length. Zero means upstream default.
	MaxTokens int

	// Timeout, if non-zero, applies a deadline to the run via a
	// context.WithTimeout wrapper. Hitting it kills the subprocess.
	// Prefer threading deadlines through the caller's ctx; this is a
	// belt-and-suspenders option for callers that don't.
	Timeout time.Duration

	// SystemPrompt, if non-empty, is supplied to the agent as the
	// system role. Adapters map this onto their CLI's --system or
	// equivalent flag.
	SystemPrompt string

	// WorkingDir, if non-empty, sets the spawned subprocess's cwd
	// (`exec.Cmd.Dir`). Lets the agent's tool calls (`git log`,
	// relative `Read`) resolve against a domain-specific root —
	// e.g. an Ideate idea root for summary research.
	WorkingDir string
}
