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
