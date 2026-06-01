// Package transcript abstracts per-agent on-disk transcript formats.
//
// Different agents (Claude Code today, others in the future) write their
// own session transcripts to the filesystem. The Writer interface lets
// testagent simulate these formats during testing — and is also where
// future agent integrations plug in.
//
// Format-specific implementations live in subpackages (e.g. claudefmt).
package transcript

import "context"

// Writer emits a session transcript in some agent-specific format.
// Implementations decide where on disk the transcript lives based on
// constructor params (e.g. a projects directory + cwd + session ID for
// Claude). The contract is start-once → append-many → end-once. End is
// safe to call from a deferred close path even if Start failed.
type Writer interface {
	Start(ctx context.Context, sessionID, cwd string) error
	Append(ctx context.Context, role, content string) error
	End(ctx context.Context, exitCode int) error
}

// Noop returns a Writer that does nothing — useful as a default when no
// transcript output is requested.
func Noop() Writer { return noopWriter{} }

type noopWriter struct{}

func (noopWriter) Start(context.Context, string, string) error  { return nil }
func (noopWriter) Append(context.Context, string, string) error { return nil }
func (noopWriter) End(context.Context, int) error               { return nil }
