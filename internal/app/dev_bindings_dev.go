//go:build dev

package app

import (
	"github.com/paultyng/ideate/internal/agent"
	ideatecfg "github.com/paultyng/ideate/internal/config"
)

// RunClaudeSync triggers the Claude transcript sync on demand. Dev/test
// builds only — the production startup goroutine is the only invocation
// path in release builds. Returns the same error shape as the underlying
// SyncClaudeSessions call.
func (a *App) RunClaudeSync() error {
	return agent.SyncClaudeSessions(a.ctx, a.store, a.ideasDir, ideatecfg.DefaultClaudeProjectsDir())
}

// ForceDormantSession flips a session to dormant via the same
// markSessionDormant path that crash-recovery uses, including the
// `session:<uuid>:status` event PendingReviewsBar / sidebar listen on.
// Playwright tests use this instead of patching `status='dormant'`
// directly on the session JSON, which would bypass the event. Dev
// builds only — production has no use case for forcing a session
// dormant without a real adopt trigger. See docs/test-drift-audit.md.
func (a *App) ForceDormantSession(slug, uuid string) {
	a.markSessionDormant(a.ctx, ResumeCandidate{Slug: slug, UUID: uuid}, "playwright_force")
}
