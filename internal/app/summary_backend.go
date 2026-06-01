package app

import (
	"log/slog"

	"github.com/paultyng/ideate/internal/agent/headless"
	"github.com/paultyng/ideate/internal/agent/summarizer"
)

// pickSummaryGenerator maps a config backend string to the
// corresponding [summarizer.Generator]. Unknown / "" defaults to the
// deterministic snippet backend so users don't get surprise Haiku
// spend just by leaving config.summary unset. Recognized:
//
//   - ""        | "snippet"   → deterministic local synthesis
//   - "claude"               → headless claude --print
//   - "codex"                → reserved for headless codex exec --json
//     (no concrete runner yet; falls back to snippet with a warning)
//   - "testagent"            → dev-only; see summary_backend_dev.go
//     for the registration. In release builds the case is unknown and
//     the function falls back to snippet.
//
// pickHeadlessTestAgent is overridden by the dev build to return a
// real headless.Runner for testagent; the release build leaves it
// returning nil so the "testagent" case hits the snippet fallback.
func pickSummaryGenerator(backend string) summarizer.Generator {
	switch backend {
	case "", "snippet":
		return summarizer.SnippetGenerator{}
	case "claude":
		return summarizer.HeadlessGenerator{
			Runner: &headless.ClaudeRunner{},
			Model:  "haiku",
		}
	case "codex":
		slog.Warn("summary.backend=codex requested but no codex runner available; using snippet")
		return summarizer.SnippetGenerator{}
	case "testagent":
		if r := pickHeadlessTestAgent(); r != nil {
			return summarizer.HeadlessGenerator{Runner: r}
		}
		slog.Warn("summary.backend=testagent requested but unavailable in this build; using snippet")
		return summarizer.SnippetGenerator{}
	default:
		slog.Warn("summary.backend unrecognized; using snippet",
			slog.String("backend", backend))
		return summarizer.SnippetGenerator{}
	}
}
