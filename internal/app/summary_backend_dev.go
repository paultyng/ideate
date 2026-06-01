//go:build dev

package app

import "github.com/paultyng/ideate/internal/agent/headless"

// pickHeadlessTestAgent returns a ClaudeRunner pointed at the
// testagent binary's `claude` subcommand. testagent v0.3+ mirrors
// real Claude's --print --output-format stream-json contract, so the
// existing ClaudeRunner decoder works against it unchanged.
//
// Dev builds only — release builds get a nil runner via
// summary_backend_release.go and fall back to the snippet generator.
func pickHeadlessTestAgent() headless.Runner {
	return &headless.ClaudeRunner{
		Binary:     "testagent",
		Subcommand: "claude",
	}
}
