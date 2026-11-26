//go:build dev

package app

import (
	"fmt"

	"github.com/paultyng/ideate/internal/agent"
	ideatecfg "github.com/paultyng/ideate/internal/config"
	"github.com/paultyng/ideate/internal/model"
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

// ForceSessionActivity sets a running session's Activity through the same
// UpdateSession write the activity hooks use (the watcher then emits
// idea:changed). Playwright tests use this instead of rewriting the session
// JSON. Dev builds only. See docs/test-drift-audit.md.
func (a *App) ForceSessionActivity(uuid, activity string) error {
	act := model.SessionActivity(activity)
	switch act {
	case model.SessionActivityActive, model.SessionActivityIdle,
		model.SessionActivityWaiting, model.SessionActivityReviewing:
	default:
		return fmt.Errorf("invalid activity %q", activity)
	}
	summaries, err := a.svc.ListSessionSummaries(a.ctx)
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}
	for _, sum := range summaries {
		for _, s := range sum.Running {
			if s.UUID != uuid {
				continue
			}
			s.Activity = act
			return a.svc.UpdateSession(a.ctx, sum.Slug, uuid, s)
		}
	}
	return fmt.Errorf("no running session %q", uuid)
}
