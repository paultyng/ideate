package app

import (
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"github.com/paultyng/ideate/frontend"
	ideatecfg "github.com/paultyng/ideate/internal/config"
	"github.com/paultyng/ideate/internal/version"
)

// Launch starts the Wails application with the given launch config.
func Launch(config LaunchConfig) error {
	// File logging is set up before App.New() so any slog calls during
	// store load / coordinator init also land on disk.
	closeLog := setupFileLogging(ideatecfg.LogsDir(ideatecfg.DefaultConfigDir()))
	defer closeLog()

	// Harvest the user's interactive shell env BEFORE anything else so
	// every downstream subprocess (claude, testagent, MCP servers,
	// summarizer) inherits the same PATH / locale / user-configured
	// vars the user gets in their terminal. No-op when terminal-launched
	// (the env is already correct) or when IDEATE_NO_SHELL_ENV=1.
	// Closes the per-var Dock-launch patches (#16 TERM/COLORTERM,
	// #22 testagent TERM, #26 FORCE_HYPERLINK) by giving subprocesses
	// the correct env baseline.
	ResolveShellEnv()

	a := New(config)

	width, height := 1200, 800
	ws := loadWindowState()
	if ws != nil {
		width = ws.Width
		height = ws.Height
		a.savedWindowPos = ws
	}

	opts := &options.App{
		Title:            "Ideate",
		StartHidden:      true,
		Width:            width,
		Height:           height,
		MinWidth:         800,
		MinHeight:        600,
		BackgroundColour: &options.RGBA{R: 26, G: 26, B: 26, A: 1},
		AssetServer: &assetserver.Options{
			Assets: frontend.Assets,
		},
		OnStartup:     a.Startup,
		OnDomReady:    a.DomReady,
		OnShutdown:    a.Shutdown,
		OnBeforeClose: a.BeforeClose,
		Bind: []interface{}{
			a,
		},
		// Wails-side log level. Frontend console.* calls are mirrored
		// to the Wails logger via consoleBridge.ts; the logger
		// filters by these thresholds before writing to stderr (which
		// `task dev`'s tee captures). DEBUG only when explicitly
		// asked for via IDEATE_TERM_DEBUG=1; otherwise the standard
		// INFO/ERROR pair so production stays clean.
		LogLevel:           logLevelFromEnv(),
		LogLevelProduction: logger.ERROR,
		Mac: &mac.Options{
			// TitleBarHidden gives us:
			// - traffic lights stay (top-left)
			// - window title text hidden (no "Ideate" duplicating the in-app
			//   topbar title)
			// - title bar transparent + FullSizeContent so the topbar
			//   renders flush with the traffic-lights row instead of
			//   stacking under a separate macOS chrome strip
			// CSS pads the topbar's left side to clear the lights and sets
			// `-webkit-app-region: drag` on the topbar so window dragging
			// still works.
			TitleBar:   mac.TitleBarHidden(),
			Appearance: mac.NSAppearanceNameDarkAqua,
			About: &mac.AboutInfo{
				Title:   "Ideate",
				Message: "Idea lifecycle tracker\nVersion " + version.Version,
			},
			// macOS LaunchServices routes `open ideate://...` (and clicks
			// on ideate://... links from other apps) here. The handler
			// forwards the URL to the frontend deeplink dispatcher and
			// brings the window forward. Pre-Startup URLs (cold-start
			// race window) get buffered on the App and drained when
			// Startup wires up the Wails context.
			OnUrlOpen: a.HandleOpenURL,
		},
	}

	if ws != nil {
		opts.WindowStartState = options.Normal
	}

	return wails.Run(opts)
}

// logLevelFromEnv returns DEBUG when IDEATE_TERM_DEBUG=1 so console.debug
// calls forwarded from the frontend land in the dev log. Otherwise the
// Wails default INFO is fine — errors and warnings still surface, but
// the chatty per-byte termDebug output stays out.
func logLevelFromEnv() logger.LogLevel {
	if os.Getenv("IDEATE_TERM_DEBUG") == "1" {
		return logger.DEBUG
	}
	return logger.INFO
}
