package app

import (
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/paultyng/ideate/internal/version"
)

func (a *App) GetLaunchConfig() LaunchConfig {
	return a.launchConfig
}

// RegisterSessionViewer is called by TerminalPanel on mount. Bumps a
// per-session refcount; while >0, session-output EventsEmit fires
// (otherwise we'd pay a base64 + JS-bridge cost per PTY chunk for an
// invisible terminal). Refcount handles the rare case where the same
// session is rendered in two places concurrently.

func (a *App) GetAppStatus() StatusInfo {
	return StatusInfo{
		Version: version.Version,
		Uptime:  time.Since(a.startTime).Round(time.Second).String(),
	}
}

// GetLocalDiff returns the diff between two refs in a local git repository.

func (a *App) navigate(view string, params map[string]string) {
	wailsRuntime.EventsEmit(a.ctx, "navigate", map[string]any{
		"view":   view,
		"params": params,
	})

	wailsRuntime.WindowUnminimise(a.ctx)
	wailsRuntime.WindowShow(a.ctx)
	wailsRuntime.WindowSetAlwaysOnTop(a.ctx, true)
	wailsRuntime.WindowSetAlwaysOnTop(a.ctx, false)
}

// ListIdeas returns all ideas sorted by most recently updated.
