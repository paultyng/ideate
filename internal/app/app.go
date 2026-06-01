package app

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/paultyng/ideate/internal/agent"
	"github.com/paultyng/ideate/internal/agent/summarizer"
	"github.com/paultyng/ideate/internal/claudecode"
	ideatecfg "github.com/paultyng/ideate/internal/config"
	"github.com/paultyng/ideate/internal/ipc"
	ideateMCP "github.com/paultyng/ideate/internal/mcp"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/pubsub"
	"github.com/paultyng/ideate/internal/service"
	"github.com/paultyng/ideate/internal/sleep"
	"github.com/paultyng/ideate/internal/store"
)

// LaunchConfig describes the initial view to open when the app starts.
// Populated from CLI flags when a subcommand is used to launch the app directly.
type LaunchConfig struct {
	View   string            `json:"view"`
	Params map[string]string `json:"params,omitempty"`
	// Standalone is true when the app was launched for a single-purpose flow
	// (e.g. `ideate review diff start` with no running daemon). Single-purpose
	// flows can quit the app on completion; long-running daemon flows must not.
	Standalone bool `json:"standalone,omitempty"`
	// PreventSleep, when true, flips the in-app sleep-inhibitor toggle to ON
	// immediately at Startup. Driven by the CLI --prevent-sleep flag for users
	// who want long-running sessions to keep the Mac awake without first
	// reaching for the footer toggle. The toggle remains user-controllable from
	// the footer either way.
	PreventSleep bool `json:"prevent_sleep,omitempty"`
}

// StatusInfo is the response from GetAppStatus, displayed in the frontend footer.
type StatusInfo struct {
	Version string `json:"version"`
	Uptime  string `json:"uptime"`
}

// IdeaDetail wraps Idea with computed fields for the frontend.
type IdeaDetail struct {
	model.Idea
	Files []string `json:"files"`
}

// App is the Wails application struct. It holds the IPC server, agent coordinator,
// store, and provides lifecycle callbacks and bindings for the frontend.
type App struct {
	ctx            context.Context
	ipcServer      *ipc.Server
	coordinator    *agent.AgentCoordinator
	store          *store.FSStore
	svc            *service.IdeaService
	mcpManager     *ideateMCP.Manager
	hooksHandler   claudecode.HookHandler
	events         *pubsub.Broker[pubsub.Event]
	eventBridge    chan struct{} // closed by startEventBridge to signal exit
	httpServer     *http.Server
	httpPort       int
	launchConfig   LaunchConfig
	startTime      time.Time
	savedWindowPos *windowState
	configDir      string
	ideasDir       string
	watcherCancel  context.CancelFunc
	watcher        *ideaWatcher
	forceClosing   bool // bypass BeforeClose guard after user confirms Stop & Quit
	// emitFn publishes a named event to the Wails frontend via the pubsub
	// bridge. Nil in tests (no Wails runtime); callers must guard.
	emitFn func(string, any)

	// Sleep inhibitor: opt-in toggle that holds an OS sleep assertion
	// while at least one running session has Activity in {active,
	// waiting}. State is in-memory only — every app start defaults to
	// disabled.
	sleepMu          sync.Mutex
	sleepEnabled     bool
	sleepInhibitor   sleep.Inhibitor
	sleepCancel      func()
	sleepStop        chan struct{}
	sleepDone        chan struct{}
	sleepLastEmitted SleepState

	// Per-session viewer refcount. Drives whether session-output
	// EventsEmit fires for a given session — when no TerminalPanel
	// is mounted for it, we'd otherwise pay a base64 + JS-bridge
	// round-trip per PTY chunk for nothing. Status emits stay
	// unconditional so off-screen lifecycle changes still update
	// the global session bar.
	viewerMu      sync.Mutex
	sessionViewer map[string]int

	// summarizer regenerates the idea-level summary sidecar after
	// each SessionEnd hook. Started in startup, stopped in Shutdown.
	summarizer *summarizer.Summarizer
}

func New(config LaunchConfig) *App {
	configDir := ideatecfg.DefaultConfigDir()
	coord := agent.NewCoordinator(configDir)

	ideasDir := ideatecfg.DefaultIdeasDir()
	_ = os.MkdirAll(ideasDir, 0o755)
	cfg, _ := store.LoadConfig(ideasDir)
	st := store.NewFSStore(ideasDir, ideatecfg.ReviewsDir(configDir), cfg.BranchPrefix, cfg.TrackingBranch)
	st.SetSummaryBackend(cfg.Summary.Backend)

	a := &App{
		coordinator:   coord,
		store:         st,
		svc:           service.New(st, coord),
		launchConfig:  config,
		startTime:     time.Now(),
		configDir:     configDir,
		ideasDir:      ideasDir,
		sessionViewer: make(map[string]int),
	}

	// Register runners. ClaudeCodeRunner gets idea context via callback.
	ideaGetter := newIdeaGetter(st, ideasDir)
	// ConfigGetter re-reads config.json on every Run() so user edits
	// (e.g. extra --add-dir paths) land on the next session start or
	// resume without an app restart.
	claudeConfigGetter := func() store.ClaudeAgent {
		c, _ := store.LoadConfig(ideasDir)
		if c == nil {
			return store.ClaudeAgent{}
		}
		return c.Agents.Claude
	}
	claudeRunner := &agent.ClaudeCodeRunner{
		IdeaGetter:   ideaGetter,
		ConfigGetter: claudeConfigGetter,
	}
	coord.RegisterRunner("claude-code", claudeRunner)
	// Claude (Debug): same runner config plus --debug, with PTY output
	// tee'd to a per-session file. IDEATE_LOG_DIR points it at
	// <repo>/logs in dev (the dir cleaned by `task clean:logs`); falls
	// back to <configDir>/logs in production.
	debugLogDir := os.Getenv("IDEATE_LOG_DIR")
	if debugLogDir == "" {
		debugLogDir = filepath.Join(configDir, "logs")
	}
	claudeDebugRunner := &agent.ClaudeCodeRunner{
		IdeaGetter:   ideaGetter,
		ConfigGetter: claudeConfigGetter,
		Debug:        true,
		DebugLogDir:  debugLogDir,
	}
	coord.RegisterRunner("claude-code-debug", claudeDebugRunner)
	registerTestAgentRunner(coord)

	return a
}

// newIdeaGetter builds the IdeaGetter callback registered with the
// Claude runner. AddDirs include the idea root plus every linked
// repo's absolute path so Claude's per-dir skill discovery picks up
// `.claude/skills/` directories committed inside each repo. A failure
// to list repos must not block session start — log + fall back to
// `[ideaDir]` only.
func newIdeaGetter(st store.Store, ideasDir string) func(slug string) (*agent.IdeaContext, error) {
	return func(slug string) (*agent.IdeaContext, error) {
		ctx := context.Background()
		idea, err := st.Get(ctx, slug)
		if err != nil {
			return nil, err
		}
		ideaDir := filepath.Join(ideasDir, slug)
		addDirs := []string{ideaDir}
		repos, err := st.ListRepos(ctx, slug)
		if err != nil {
			slog.Warn("idea getter: listing repos failed; falling back to idea-root only",
				slog.String("slug", slug),
				slog.Any("err", err))
		} else {
			for _, r := range repos {
				addDirs = append(addDirs, filepath.Join(ideaDir, r.Path))
			}
		}
		return &agent.IdeaContext{
			Idea:    *idea,
			AddDirs: addDirs,
		}, nil
	}
}

// Startup is called when the Wails app starts. It initializes the IPC server,
// MCP/hooks HTTP server, and sets up the agent coordinator's event handlers.
