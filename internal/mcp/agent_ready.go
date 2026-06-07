package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// ErrAgentNotReady is returned by waitForAgentReady when the polling
// deadline expires without observing any of the known prompt markers
// in the session's vscreen output. Callers should surface this to the
// orchestrator with a retry hint, not paper over it — the underlying
// write would otherwise race the agent's stdin-raw-mode setup and the
// bytes would be silently dropped or echoed.
var ErrAgentNotReady = errors.New("agent not ready: prompt marker not observed within timeout")

// agentReadyMarkers are the byte sequences whose presence in a
// session's vscreen output signals the agent has booted, entered TUI
// mode, and put stdin in raw mode (so it's safe to type into the PTY).
//
// Markers are checked against the raw replay bytes (no ANSI strip
// needed): claude's prompt glyph ❯ is U+276F (E2 9D AF in UTF-8) and
// always renders without an ANSI escape splitting it; testagent's
// "[mcp connected: N tools]" lifecycle marker is emitted contiguously
// by render.Lifecycle.
//
// Order is conservative-first: ❯ is the strongest claude signal;
// "mcp connected:" is the testagent fallback. Adding markers for
// codex / cursor / future agents goes here — TODO before shipping
// those runners.
var agentReadyMarkers = [][]byte{
	[]byte("❯ "), // claude TUI idle prompt (❯ + space)
	[]byte("mcp connected:"),
}

// defaultAgentReadyTimeout is the polling ceiling before we give up.
// Matches the timeout we use elsewhere for boot-stage waits (PR #28's
// shell-env harvest, JetBrains' rc-file hang doc). Overridable via
// IDEATE_AGENT_READY_TIMEOUT_MS for users with slow MCP boots
// (many remote servers) or for tests.
const defaultAgentReadyTimeout = 10 * time.Second

// agentReadyPollInterval is how often we re-fetch the vscreen
// snapshot. 200ms is the same cadence as the playwright sibling
// (frontend/playwright/ptyCapture.ts). Faster polling wastes CPU on a
// snapshot copy + scan; slower polling adds perceived latency to
// orchestrator-driven writes.
const agentReadyPollInterval = 200 * time.Millisecond

// agentReplaySource is the slice of SessionResolver that waitForAgentReady
// depends on. Keeping the local interface small makes the helper
// trivially testable without the full SessionResolver surface.
type agentReplaySource interface {
	GetSessionReplay(uuid string) ([]byte, error)
}

// waitForAgentReady polls the session's vscreen output until one of
// the agent-ready markers appears, the context is cancelled, or the
// timeout fires. Returns nil as soon as a marker is observed. On
// timeout, returns ErrAgentNotReady wrapped with the uuid for log
// context.
//
// Caller responsibility: only invoke after the underlying session has
// been spawned / resumed (IsRunning=true). This helper does not
// verify the session exists — that check lives at the call site so
// the not-found error surfaces with the correct shape.
func waitForAgentReady(ctx context.Context, src agentReplaySource, uuid string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultAgentReadyTimeout
	}
	deadline := time.Now().Add(timeout)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	ticker := time.NewTicker(agentReadyPollInterval)
	defer ticker.Stop()

	// Check once before the first sleep — a session that booted before
	// the caller invokes us returns immediately.
	if marker, ok := checkReplayForMarker(src, uuid); ok {
		slog.Debug("agent ready",
			slog.String("uuid", uuid),
			slog.String("marker", marker))
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			slog.Warn("agent-ready timeout",
				slog.String("uuid", uuid),
				slog.Duration("timeout", timeout))
			return fmt.Errorf("%w: uuid=%s timeout=%s", ErrAgentNotReady, uuid, timeout)
		case <-ticker.C:
			if marker, ok := checkReplayForMarker(src, uuid); ok {
				slog.Debug("agent ready",
					slog.String("uuid", uuid),
					slog.String("marker", marker))
				return nil
			}
		}
	}
}

// checkReplayForMarker fetches the current replay snapshot once and
// reports whether any known marker is present. Returns the matched
// marker text for logging. Replay-fetch errors are not propagated —
// they're transient during boot (the session record may not have a
// vscreen attached yet) and the next tick will retry.
func checkReplayForMarker(src agentReplaySource, uuid string) (string, bool) {
	data, err := src.GetSessionReplay(uuid)
	if err != nil {
		return "", false
	}
	return matchAgentReadyMarker(data)
}

// matchAgentReadyMarker is the pure-function predicate that
// agent-ready detection is built on. Exported-shape (lowercase but
// no closure over a resolver) so unit tests can exercise it
// independently of the polling loop. Returns the matched marker for
// logging, or "" + false if none match.
//
// Pure string predicate: no I/O, no time, no goroutines. Safe to
// unit-test exhaustively (claude prompt present, testagent banner
// present, neither present, both present, escapes around the marker).
func matchAgentReadyMarker(data []byte) (string, bool) {
	for _, marker := range agentReadyMarkers {
		if bytes.Contains(data, marker) {
			return string(marker), true
		}
	}
	return "", false
}

// resolvedAgentReadyTimeout returns the configured timeout, honoring
// IDEATE_AGENT_READY_TIMEOUT_MS for ops overrides. Bad values fall
// back to the default so a typo doesn't disable the protection.
func resolvedAgentReadyTimeout() time.Duration {
	raw := os.Getenv("IDEATE_AGENT_READY_TIMEOUT_MS")
	if raw == "" {
		return defaultAgentReadyTimeout
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		slog.Warn("invalid IDEATE_AGENT_READY_TIMEOUT_MS; using default",
			slog.String("value", raw),
			slog.Duration("default", defaultAgentReadyTimeout))
		return defaultAgentReadyTimeout
	}
	return time.Duration(n) * time.Millisecond
}
