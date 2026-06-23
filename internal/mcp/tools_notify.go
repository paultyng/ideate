package mcp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// notifyMinInterval is the per-session minimum gap between OS notifications.
// Loose enough to be useful (a blocked-agent ping isn't a once-an-hour event)
// but tight enough to prevent spam from a stuck loop.
const notifyMinInterval = 5 * time.Second

// errNotifyRateLimited is returned when the per-session rate limit kicked in.
var errNotifyRateLimited = errors.New("notify_user: rate-limited (one notification per 5s per session)")

func notifyUserTool() mcp.Tool {
	return mcp.Tool{
		Name:        "notify_user",
		Description: desc("notify_user"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"title": map[string]any{
					"type":        "string",
					"description": "One-line headline (≤80 chars).",
				},
				"body": map[string]any{
					"type":        "string",
					"description": "One or two sentences, plain text.",
				},
			},
			Required: []string{"title", "body"},
		},
	}
}

func (m *Manager) handleNotifyUser(sessionID string) server.ToolHandlerFunc {
	return func(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		title := strings.TrimSpace(request.GetString("title", ""))
		body := strings.TrimSpace(request.GetString("body", ""))
		if title == "" || body == "" {
			return mcp.NewToolResultError("title and body are required"), nil
		}
		if !m.checkNotifyRate(sessionID) {
			return mcp.NewToolResultError(errNotifyRateLimited.Error()), nil
		}
		if err := m.notify(title, body); err != nil {
			// Roll back the rate-limit slot — a failed osascript shouldn't
			// burn the user's quota for 5s when no notification was delivered.
			m.clearNotifyRate(sessionID)
			return mcp.NewToolResultError(fmt.Sprintf("notify_user: %v", err)), nil
		}
		return mcp.NewToolResultText("ok"), nil
	}
}

// checkNotifyRate enforces the per-session rate limit. Returns true and
// records the timestamp on allow; returns false on deny without updating (so
// the next allowed call's window starts from the last successful emit, not
// from the last attempt — a hammering agent doesn't push its own quota
// forward).
func (m *Manager) checkNotifyRate(sessionID string) bool {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastNotify == nil {
		m.lastNotify = map[string]time.Time{}
	}
	if last, ok := m.lastNotify[sessionID]; ok && now.Sub(last) < notifyMinInterval {
		return false
	}
	m.lastNotify[sessionID] = now
	return true
}

// clearNotifyRate rolls back a session's rate-limit slot. Used when the
// notify dispatch failed (e.g. osascript missing) so the user isn't held
// to a 5s cooldown for a delivery that never landed.
func (m *Manager) clearNotifyRate(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.lastNotify, sessionID)
}

// notify dispatches the notification to the OS. Indirected through a field
// on Manager so tests can stub without spawning subprocesses or popping a
// real notification at every test run.
func (m *Manager) notify(title, body string) error {
	m.mu.RLock()
	fn := m.notifier
	m.mu.RUnlock()
	if fn == nil {
		fn = defaultNotifier
	}
	return fn(title, body)
}

// SetNotifier overrides the default OS notifier — exposed for tests and for
// future hosts that want to route notifications through their own channel.
func (m *Manager) SetNotifier(fn func(title, body string) error) {
	m.mu.Lock()
	m.notifier = fn
	m.mu.Unlock()
}

// defaultNotifier shells out to `osascript` on macOS. Other platforms log
// only — implementing freedesktop notify-send / Windows toast is a follow-up
// when Ideate ships beyond macOS. Returns nil on the non-macOS path so a
// session running on Linux dev environment doesn't fail every call.
func defaultNotifier(title, body string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	// AppleScript quoting. In an osascript -e statement, an unescaped
	// newline ends the current statement and starts a new one — a payload
	// like `body\ndisplay dialog "x"` would execute a second statement. So
	// scrub newlines first, then backslash-escape backslashes and double
	// quotes. Carriage returns get the same treatment for the same reason.
	esc := func(s string) string {
		s = strings.ReplaceAll(s, "\r", " ")
		s = strings.ReplaceAll(s, "\n", " ")
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		return s
	}
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, esc(body), esc(title))
	cmd := exec.Command("osascript", "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("osascript: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
