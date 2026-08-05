package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/paultyng/ideate/internal/agent"
	"github.com/paultyng/ideate/internal/agent/summarizer"
	"github.com/paultyng/ideate/internal/capture"
	"github.com/paultyng/ideate/internal/claudecode"
	ideatecfg "github.com/paultyng/ideate/internal/config"
	"github.com/paultyng/ideate/internal/hooks"
	"github.com/paultyng/ideate/internal/ipc"
	ideateMCP "github.com/paultyng/ideate/internal/mcp"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/pubsub"
	"github.com/paultyng/ideate/internal/skills"
	"github.com/paultyng/ideate/internal/sleep"
)

// HandleOpenURL is the Mac.OnUrlOpen callback wired in launch.go.
// macOS LaunchServices routes `open ideate://...` (and clicks on
// ideate://... links from other apps) here for both cold-start and
// hot-launch. The URL is forwarded to the frontend's "deeplink"
// EventsOn listener, which calls handleLink → HashRouter navigation.
//
// Cold-start race: kAEGetURL can fire before Startup wires a.ctx.
// We buffer such URLs and drain them in Startup once ctx is non-nil.
// Post-Startup invocations dispatch immediately.
func (a *App) HandleOpenURL(url string) {
	a.deeplinkMu.Lock()
	if a.ctx == nil {
		a.pendingDeeplinks = append(a.pendingDeeplinks, url)
		a.deeplinkMu.Unlock()
		return
	}
	a.deeplinkMu.Unlock()
	a.dispatchDeeplink(url)
}

// dispatchDeeplink emits the URL to the frontend and brings the window
// forward. macOS doesn't auto-focus the app on URL receipt, so the
// WindowShow + WindowUnminimise calls are necessary for hot launch.
// Drops the URL with a warn log if a.ctx isn't set yet — the defensive
// guard protects callers that reach the dispatch before Startup wires
// the context (which would otherwise panic inside Wails' EventsEmit).
func (a *App) dispatchDeeplink(url string) {
	if a.ctx == nil {
		slog.Warn("dispatchDeeplink called before Startup wired ctx; dropping",
			slog.String("url", url))
		return
	}
	wailsRuntime.EventsEmit(a.ctx, "deeplink", url)
	wailsRuntime.WindowShow(a.ctx)
	wailsRuntime.WindowUnminimise(a.ctx)
}

// drainPendingDeeplinks flushes URLs that arrived before the webview
// was ready. Called once from DomReady after a.ctx is set; after that
// HandleOpenURL dispatches immediately and the buffer stays empty.
// Repeat calls are safe (no-ops on empty buffer).
func (a *App) drainPendingDeeplinks() {
	a.deeplinkMu.Lock()
	pending := a.pendingDeeplinks
	a.pendingDeeplinks = nil
	a.deeplinkMu.Unlock()
	for _, url := range pending {
		a.dispatchDeeplink(url)
	}
}

// startEventBridge spawns the goroutine that reads from the app-
// wide event broker and forwards each event to wailsRuntime.EventsEmit.
// Single subscriber, runs for the app's lifetime; exits when the
// broker is closed in Shutdown.
func (a *App) startEventBridge() {
	ch, _ := a.events.Subscribe()
	a.eventBridge = make(chan struct{})
	go func() {
		defer close(a.eventBridge)
		for ev := range ch {
			wailsRuntime.EventsEmit(a.ctx, ev.Name, ev.Data)
		}
	}()
}

func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx

	// Deeplink drain is deferred to DomReady — Startup is too early.
	// EventsEmit from here would fire before React mounts and wires
	// its EventsOn('deeplink') listener, and Wails events don't buffer
	// for late subscribers. See DomReady's call to drainPendingDeeplinks.

	// App-wide event broker. Both the MCP manager and the hooks
	// handler publish to it; a single bridge goroutine fans events
	// to wailsRuntime.EventsEmit. Closed in Shutdown so the bridge
	// goroutine exits cleanly.
	a.events = pubsub.New[pubsub.Event]()
	a.startEventBridge()

	// Sleep inhibitor: in-memory toggle, defaults disabled. Watcher
	// recomputes the assertion on every broker event, plus on
	// SetSleepEnabled flips. Released in Shutdown.
	a.sleepInhibitor = sleep.New()
	a.startSleepWatcher()
	if a.launchConfig.PreventSleep {
		a.SetSleepEnabled(true)
	}

	emitEvent := func(event string, data any) {
		a.events.Publish(pubsub.Event{Name: event, Data: data})
	}
	a.emitFn = emitEvent

	// Wire agent coordinator output to Wails events. Skip the
	// base64-encode + EventsEmit round-trip when no TerminalPanel is
	// mounted for this session — viewers register on mount and
	// unregister on unmount. The vscreen.Feed path on the agent side
	// runs unconditionally, so on a future re-mount the replay
	// snapshot still reproduces everything that was missed.
	a.coordinator.SetOutputHandler(func(uuid string, data []byte) {
		if !a.hasSessionViewer(uuid) {
			return
		}
		encoded := base64.StdEncoding.EncodeToString(data)
		wailsRuntime.EventsEmit(a.ctx, "session:"+uuid+":output", encoded)
	})
	a.coordinator.SetStatusHandler(func(uuid string, meta agent.SessionMeta, status string, exitCode int) {
		if os.Getenv("IDEATE_TERM_DEBUG") == "1" {
			slog.Info("term: bridge status -> wails",
				slog.String("uuid", uuid),
				slog.String("status", status),
				slog.Int("exit_code", exitCode))
		}
		wailsRuntime.EventsEmit(a.ctx, "session:"+uuid+":status", map[string]any{
			"status":   status,
			"exitCode": exitCode,
		})

		// Idea-bound sessions finalize the per-idea session record. Root
		// orchestrator sessions skip — they have no idea record to update.
		if meta.IdeaSlug != "" {
			a.finalizeSession(meta.IdeaSlug, uuid, status)
		}

		if a.mcpManager != nil {
			a.mcpManager.UnregisterSession(uuid)
		}
	})

	// Startup adoption: classify on-disk manifests by PID liveness before
	// marking anything stopped. lazyAdoptIdeaSessions splits "PID alive"
	// (keep running) from "PID dead" (mark dormant). Idle time is not a
	// signal — there is no runtime idle-stop watcher and no idle-on-adopt
	// branch; sessions live as long as their PTY does.

	// Crash detection: any session record still flagged Status=running has
	// outlived the process that wrote it (we don't re-adopt PTYs across app
	// restarts). Mark them stopped+crash so the lazy-adopt sweep can pick
	// them up. The frontend gets a one-shot crash-recovery toast for the
	// ones that end up resuming. Defer the actual scheduleAutoResume call
	// until after the hooks port is set on the runner — otherwise resumed
	// sessions get spawned with HooksURL="" (no --settings flag), and Claude
	// Code runs hookless for the entire lifetime of those processes.
	crashed := a.markRunningSessionsStopped(model.SessionStopReasonCrash)

	// Prune pending reviews whose linked session won't return (clean exit,
	// /clear, /compact, user-stopped, orphaned transcript) or that are older
	// than the staleness threshold. Reviews linked to sessions that will
	// auto-resume (StopReason=shutdown|crash) stay pending so the human's
	// in-progress edits survive the restart.
	if cancelled := a.cancelStaleReviews(); len(cancelled) > 0 {
		slog.Info("cancelled stale pending reviews", slog.Int("count", len(cancelled)))
	}

	// Watch ideas dir for external edits (editor saves, agent writes via MCP)
	// and push idea:changed events to the frontend so views stay live.
	watcherCtx, cancel := context.WithCancel(ctx)
	a.watcherCancel = cancel
	if w, err := startWatcher(watcherCtx, a.ideasDir, emitEvent, a.store.ReloadConfig); err != nil {
		slog.Warn("starting ideas watcher; live reload disabled", slog.Any("err", err))
	} else {
		a.watcher = w
	}

	// Start MCP + hooks HTTP server on a random port.
	a.mcpManager = ideateMCP.NewManager(a.svc, appSessionResolver{AgentCoordinator: a.coordinator, ideasDir: a.ideasDir}, a.events)
	// rename_idea uses this to migrate Claude transcript dirs whose
	// per-cwd path key changes when the idea's working dir moves.
	a.mcpManager.SetClaudeProjectsDir(ideatecfg.DefaultClaudeProjectsDir())
	// set_sleep_enabled (orchestrator-only MCP tool) needs to flip
	// the App's in-memory sleep toggle. Adapter keeps the mcp
	// package free of an internal/app dep.
	a.mcpManager.SetSleepController(appSleepController{a: a})
	// start_idea_session (orchestrator-only) calls back into
	// App.StartIdeaSession. Adapter keeps the mcp package free of an
	// internal/app dep.
	a.mcpManager.SetSessionStarter(appSessionStarter{a: a})
	// list_default_skills / reset_default_skill (orchestrator-only).
	a.mcpManager.SetSkillsManager(appSkillsManager{ideasDir: a.ideasDir})
	// Auto-install missing default skills into the ideas root so the
	// orchestrator session picks them up on parent-dir skill discovery.
	// Best-effort: any install error is logged, not fatal.
	if installed, err := skills.InstallMissing(a.ideasDir); err != nil {
		slog.Warn("auto-installing default skills",
			slog.String("ideasDir", a.ideasDir), slog.Any("err", err))
	} else if len(installed) > 0 {
		slog.Info("installed default skills",
			slog.Any("names", installed))
	}
	// Summarizer: regenerates idea summary sidecars after each
	// SessionEnd and on the periodic sweep below. Backend is selected
	// from <ideas-dir>/config.json's summary.backend field — defaults
	// to deterministic snippet synthesis (no subprocess cost). Users
	// opt up to a headless LLM (claude / codex / dev-only testagent)
	// for richer output.
	gen := pickSummaryGenerator(a.store.ConfigSummaryBackend())
	a.summarizer = summarizer.New(
		gen,
		a.svc,
		summarizer.WithProjectsDir(ideatecfg.DefaultClaudeProjectsDir()),
		summarizer.WithIdeasDir(a.ideasDir),
	)
	a.summarizer.Start(a.ctx, 2)
	// Startup auto-backfill + periodic staleness sweep. Catches:
	//   - Ideas that never had a session run inside Ideate (e.g. seeded
	//     externally or that ran via claudesync) and so never had their
	//     summary generated.
	//   - Ideas whose summary lags because Ideate was offline during
	//     the SessionEnd hook (latest ended session is newer than
	//     idea.Generated, the last content-change time — see NeedsRegeneration).
	// Sweep runs every 3 hours while the app is up; cheap on the
	// up-to-date case (one summary read + session list per idea, no
	// runner spawn). Subprocess fan-out is bounded by the summarizer
	// pool's 2 workers.
	go a.runSummarizerSweep()
	// Per-slug debounced summarizer trigger for external idea body
	// edits. Coarse 3h sweep already eventually catches these, but a
	// foreground edit shouldn't sit ~3h before the dashboard line
	// reflects it.
	go a.runIdeaChangedDebouncer()

	// RSS memory watcher: polls per-session ps RSS every
	// IDEATE_RSS_LOG_INTERVAL_SEC seconds (default 60). Set to 0 to disable.
	rssIntervalSec := 60
	if v := os.Getenv("IDEATE_RSS_LOG_INTERVAL_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			rssIntervalSec = n
		}
	}
	if rssIntervalSec > 0 {
		rssWatch := agent.NewRSSWatch(a.coordinator, time.Duration(rssIntervalSec)*time.Second)
		go rssWatch.Start(a.ctx)
	}

	a.hooksHandler = hooks.NewHandler(a.svc, a.events, a.summarizer)

	mux := http.NewServeMux()

	var mcpHandler http.Handler = a.mcpManager
	var hookHandler http.Handler = claudecode.NewHookServer(a.hooksHandler, a.mcpManager.ValidateSession)

	// Opt-in capture mode: records hook POSTs and MCP JSON-RPC frames to disk
	// for fixture generation and debugging. See internal/capture.
	if dir := os.Getenv(capture.EnvVar); dir != "" {
		rec, err := capture.New(dir)
		if err != nil {
			slog.Warn("capture mode disabled", slog.Any("err", err))
		} else {
			slog.Info("capture mode: " + rec.Dir())
			mcpHandler = rec.WrapMCP(mcpHandler)
			hookHandler = rec.WrapHooks(hookHandler)
		}
	}

	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/hooks/", hookHandler)

	// Hooks port: fixed by default so that Claude Code sessions started
	// against a previous Ideate run can keep firing hooks at the same
	// endpoint after Ideate restarts. Override with IDEATE_HOOKS_PORT for
	// alternate dev instances (so dev/test:ui don't collide with
	// dev:user). If the chosen port is taken (likely another Ideate
	// running), fall back to ephemeral and warn — auto-resume will still
	// regenerate fresh per-session --settings against the live port.
	hooksAddr := "localhost:34117"
	if v := os.Getenv("IDEATE_HOOKS_PORT"); v != "" {
		hooksAddr = "localhost:" + v
	}
	listener, err := net.Listen("tcp", hooksAddr)
	if err != nil {
		slog.Warn("hooks port unavailable; falling back to ephemeral",
			slog.String("addr", hooksAddr), slog.Any("err", err))
		listener, err = net.Listen("tcp", "localhost:0")
	}
	if err != nil {
		slog.Error("starting MCP/hooks listener", slog.Any("err", err))
	} else {
		a.httpPort = listener.Addr().(*net.TCPAddr).Port
		a.httpServer = &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			if err := a.httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
				slog.Error("MCP/hooks server", slog.Any("err", err))
			}
		}()
		slog.Info("MCP/hooks server started", "port", a.httpPort)

		// Set port on every ClaudeCodeRunner so it can generate temp configs.
		// Both "claude-code" and "claude-code-debug" share the runner type.
		for _, name := range []string{"claude-code", "claude-code-debug"} {
			if r, ok := a.coordinator.GetRunner(name).(*agent.ClaudeCodeRunner); ok {
				r.Port = a.httpPort
			}
		}
		// Wire testagent to the same hooks endpoint so integration tests
		// can drive hook events via the well-known commands in testagent's
		// input loop. No-op when the testagent runner isn't registered
		// (release builds).
		if testRunner, ok := a.coordinator.GetRunner("testagent").(*agent.TestAgentRunner); ok {
			testRunner.HooksURL = fmt.Sprintf("http://localhost:%d/hooks", a.httpPort)
			testRunner.MCPURL = fmt.Sprintf("http://localhost:%d/mcp", a.httpPort)
			// The previous in-tree testagent's --transcript flag wrote
			// Claude-format jsonl fixtures into IDEATE_CLAUDE_PROJECTS_DIR
			// so the Claude sync flow could be exercised end-to-end.
			// The upstream paultyng/testagent has no equivalent; the
			// claudesync_test fixture is now generated directly in Go
			// via writeJSONL (see internal/agent/claudesync_test.go).
		}
	}

	// Now that the runner has the hooks port, kick off lazy adoption for
	// idea sessions and eager resume for the orchestrator. Any claude
	// processes spawned here will receive a fresh --settings file pointing
	// at the live hooks endpoint.
	//
	// lazyAdoptIdeaSessions filters crashed and shutdown candidates:
	//   - idea session, PID alive + recently active → resume (returned)
	//   - idea session, PID alive but idle > threshold → kill + dormant
	//   - idea session, PID dead → dormant
	//   - orchestrator → always returned for eager resume
	// It also handles shutdown-stopped sessions internally (replacing the
	// old resumeShutdownSessions call).
	go func() {
		toResume := a.lazyAdoptIdeaSessions(context.Background(), crashed)
		orchCandidates := make([]ResumeCandidate, 0)
		ideaCandidates := make([]ResumeCandidate, 0)
		for _, c := range toResume {
			if c.Slug == model.OrchestratorSlug {
				orchCandidates = append(orchCandidates, c)
			} else {
				ideaCandidates = append(ideaCandidates, c)
			}
		}
		// Orchestrator uses crash-recovery toast; idea sessions do not
		// (they were alive and active, so "recovery" is seamless).
		if len(orchCandidates) > 0 {
			a.scheduleAutoResume(orchCandidates, true)
		}
		if len(ideaCandidates) > 0 {
			a.scheduleAutoResume(ideaCandidates, false)
		}
	}()

	// Reconcile our session history with Claude's on-disk transcripts:
	// ingest unknown sessions, orphan stale records. Best-effort —
	// runs concurrently with auto-resume since they touch disjoint
	// records (auto-resume only revives StopReason=shutdown|crash; sync
	// only writes new UUIDs or flips StopReason=orphaned).
	go func() {
		projectsDir := ideatecfg.DefaultClaudeProjectsDir()
		if err := agent.SyncClaudeSessions(a.ctx, a.store, a.ideasDir, projectsDir); err != nil {
			slog.Warn("syncing claude sessions", slog.Any("err", err))
		}
	}()

	// Start IPC server.
	srv, err := ipc.NewServer(a.navigate)
	if err != nil {
		slog.Error("starting IPC server", slog.Any("err", err))
		return
	}
	srv.SleepStateFunc = func() (bool, bool) {
		s := a.GetSleepState()
		return s.Enabled, s.Held
	}
	a.ipcServer = srv

	if err := a.ipcServer.Start(); err != nil {
		slog.Error("starting IPC server", slog.Any("err", err))
		a.ipcServer = nil
	}
}

// Shutdown is called when the Wails app is closing.

func (a *App) Shutdown(_ context.Context) {
	// Pending reviews are intentionally NOT cancelled on shutdown — they
	// survive across restarts so the human's in-progress work isn't lost.
	// On next startup, cancelStaleReviews() prunes only reviews whose linked
	// session won't be auto-resumed (or that have aged past the staleness
	// threshold).

	// Stop the ideas watcher before tearing down Wails runtime — pending
	// debounce timers would otherwise fire emit() into a dead context.
	if a.watcherCancel != nil {
		a.watcherCancel()
	}

	// Persist vscreen snapshots BEFORE stopping sessions so the
	// emulator state is captured while still live. Auto-resume on
	// next launch preloads them so re-mounted xterms show continuity
	// instead of blanking. Runners that replay their own state on
	// --resume (Claude Code) are skipped inside the helper.
	a.persistVscreenSnapshots()

	// Persist shutdown intent BEFORE stopping sessions: once the OS reaps
	// the process we won't get back to write the record. On next launch
	// these become auto-resume candidates (StopReason=shutdown).
	a.markRunningSessionsStopped(model.SessionStopReasonShutdown)

	// Release the sleep assertion (if held) and unsubscribe the
	// watcher before tearing down the broker. caffeinate's -w
	// fallback covers SIGKILL but clean exits should release
	// promptly so the OS can sleep again.
	a.stopSleepWatcher(context.Background())

	// Stop all agent sessions first.
	a.coordinator.Shutdown(context.Background())

	// Drain the headless summarizer pool. Any in-flight Haiku call
	// gets ctx-cancelled — partial summaries are dropped, the next
	// startup's staleness sweep (Phase 3) will fill them in.
	if a.summarizer != nil {
		a.summarizer.Stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if a.httpServer != nil {
		if err := a.httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("shutting down MCP/hooks server", slog.Any("err", err))
		}
	}

	if a.ipcServer != nil {
		if err := a.ipcServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("shutting down IPC server", slog.Any("err", err))
		}
	}

	// Close the event broker last so any final emits from the
	// shutdown sequence above (e.g. session-finalize hooks) had a
	// chance to land. Closing fans channel-close to the bridge
	// goroutine, which exits cleanly; we wait for that signal so
	// shutdown is deterministic.
	if a.events != nil {
		a.events.Close()
		if a.eventBridge != nil {
			<-a.eventBridge
		}
	}
}

// DomReady is called after the frontend DOM is ready.

func (a *App) DomReady(_ context.Context) {
	if a.savedWindowPos != nil {
		wailsRuntime.WindowSetPosition(a.ctx, a.savedWindowPos.X, a.savedWindowPos.Y)
	}
	wailsRuntime.WindowShow(a.ctx)

	// Drain deeplinks buffered before the webview was ready (cold-start
	// race: Mac.OnUrlOpen fires during NSApplication willFinishLaunching,
	// well before React mounts). DomReady runs after the DOM is parsed;
	// React's useEffect (where useOSDeeplinkBridge subscribes) runs on
	// the next render-commit, typically within a tick. Residual race:
	// a URL drained here just before useEffect runs would still be
	// missed. Acceptable for v0.1 (cold-start is rare); a frontend-
	// triggered "ready" handshake binding is the future-proof fix.
	a.drainPendingDeeplinks()
}

// BeforeClose is called when the window is about to close. Returns true to
// block the close. We block when any session is non-idle (active or
// waiting); the frontend renders a confirm dialog and either calls
// ForceQuit to bypass the guard or Cancel to leave the app open.

func (a *App) BeforeClose(_ context.Context) bool {
	w, h := wailsRuntime.WindowGetSize(a.ctx)
	x, y := wailsRuntime.WindowGetPosition(a.ctx)
	saveWindowState(windowState{X: x, Y: y, Width: w, Height: h})

	if a.forceClosing {
		return false
	}
	busy := a.BusyRunningSessions()
	if len(busy) == 0 {
		return false
	}
	wailsRuntime.EventsEmit(a.ctx, "app:close-blocked", busy)
	return true
}

// ForceQuit is the frontend's "Stop & Quit" confirmation in the close-blocked
// dialog. Bypasses BeforeClose and calls Wails Quit so OnShutdown runs the
// usual graceful path (which also persists StopReason=shutdown).

func (a *App) ForceQuit() {
	a.forceClosing = true
	wailsRuntime.Quit(a.ctx)
}

// BusySession is a session that's running and not idle — surfaced to the
// frontend so the close-blocked dialog can name what's busy.
type BusySession struct {
	Slug      string `json:"slug"`
	IdeaName  string `json:"ideaName"`
	UUID      string `json:"uuid"`
	AgentType string `json:"agentType"`
	Activity  string `json:"activity"`
}

// ActiveSession is a single running session — surfaced to the global
// session bar so the user can hop between concurrent sessions across
// ideas without going through the dashboard.
//
// Updated is the parent idea's Updated timestamp; every session-activity
// hook (Prompt/Tool/Stop/Notification) funnels through TouchIdea, so this
// field doubles as a "last activity" signal for the bar's recency sort.
//
// IdeaSummary is the parent idea's one-line description (idea.Description)
// when present. Embedded inline so the bar's overflow popover can render
// dashboard-style cards without a second fan-out fetch per session.
type ActiveSession struct {
	Slug        string `json:"slug"`
	IdeaName    string `json:"ideaName"`
	IdeaStatus  string `json:"ideaStatus"` // active|paused|archived; empty for orchestrator
	UUID        string `json:"uuid"`
	AgentType   string `json:"agentType"`
	Activity    string `json:"activity"`
	Started     string `json:"started"` // RFC3339; sortable as a string
	Updated     string `json:"updated"` // RFC3339; parent idea's Updated (== last session-activity bump)
	IdeaSummary string `json:"ideaSummary,omitempty"`
}

// ListActiveSessions returns every running or dormant idea-bound session.
// Dormant records are sessions whose Claude process was dropped but whose
// transcript is still resumable — surfacing them keeps users from losing
// track of work that's waiting on auto-resume. The root orchestrator
// session is intentionally omitted — it surfaces in the dedicated topbar
// drawer, not in the global bar's chip row. Sort order is the bar's
// responsibility.

func (a *App) scanResumeCandidates(reason model.SessionStopReason) []ResumeCandidate {
	ctx := context.Background()
	ideas, err := a.svc.List(ctx)
	if err != nil {
		slog.Warn("listing ideas for auto-resume",
			slog.String("reason", string(reason)), slog.Any("err", err))
		return nil
	}
	// Orchestrator rides the same path via a synthetic-slug pseudo-idea.
	ideas = append(ideas, model.Idea{Slug: model.OrchestratorSlug, Name: "Orchestrator"})
	var out []ResumeCandidate
	for _, idea := range ideas {
		sessions, err := a.svc.ListSessions(ctx, idea.Slug)
		if err != nil {
			continue
		}
		// ListSessions is Started-desc — first match per agent wins.
		seen := make(map[string]bool)
		for _, s := range sessions {
			if seen[s.Agent] {
				continue
			}
			if s.Status != model.SessionStatusStopped || s.StopReason != reason {
				continue
			}
			seen[s.Agent] = true
			if !a.AgentSupportsResume(s.Agent) {
				continue
			}
			out = append(out, ResumeCandidate{
				Slug:      idea.Slug,
				IdeaName:  idea.Name,
				UUID:      s.UUID,
				AgentType: s.Agent,
				Reason:    reason,
			})
		}
	}
	return out
}

// scheduleAutoResume spawns each candidate concurrently (capped at 4) and
// emits a one-shot crash-recovery toast event when isCrashRecovery is true.

func (a *App) scheduleAutoResume(candidates []ResumeCandidate, isCrashRecovery bool) {
	const maxConcurrent = 4
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	resumed := make([]ResumeCandidate, 0, len(candidates))
	var mu sync.Mutex
	for _, c := range candidates {
		wg.Add(1)
		sem <- struct{}{}
		go func(c ResumeCandidate) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := a.resumeSession(c); err != nil {
				slog.Warn("auto-resume",
					slog.String("slug", c.Slug), slog.String("uuid", c.UUID),
					slog.String("reason", string(c.Reason)), slog.Any("err", err))
				return
			}
			mu.Lock()
			resumed = append(resumed, c)
			mu.Unlock()
		}(c)
	}
	wg.Wait()
	if isCrashRecovery && len(resumed) > 0 && a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "session:recovery", resumed)
	}
}

// resumeSession spawns an agent for an existing UUID via --resume and
// flips the persisted record back to Status=running, Activity=idle.
// Per M14 the working directory is the idea root regardless of where
// the previous session ran — the agent cd's itself. Orchestrator sessions
// (slug == OrchestratorSlug) resume at the ideas root with empty IdeaSlug
// so the runner uses the synthetic-slug hook header path and the MCP
// manager registers them as a root orchestrator rather than an idea
// session.

func (a *App) resumeSession(c ResumeCandidate) error {
	isOrchestrator := c.Slug == model.OrchestratorSlug
	workingDir := filepath.Join(a.ideasDir, c.Slug)
	if isOrchestrator {
		workingDir = a.ideasDir
	}

	config := agent.SessionConfig{
		Name:       c.IdeaName,
		WorkingDir: workingDir,
		AgentType:  c.AgentType,
		IdeaSlug:   c.Slug,
		ResumeUUID: c.UUID,
	}
	if isOrchestrator {
		// Hook routing for orchestrator uses the synthetic slug, but the
		// agent runner takes IdeaSlug="" to mean "no idea binding" for
		// MCP/tool wiring.
		config.IdeaSlug = ""
	}

	if _, err := a.coordinator.Start(context.Background(), config); err != nil {
		return fmt.Errorf("starting agent: %w", err)
	}

	// Cross-restart continuity: replay the prior life's vscreen
	// snapshot into the fresh emulator (one-shot, file is consumed)
	// BEFORE the new agent's first byte arrives — buffered output
	// concatenates naturally below the preloaded history. No-op for
	// runners that regenerate their own state on --resume because
	// persistVscreenSnapshots skipped them at shutdown.
	//
	// Resize the PTY + vscreen to the snapshot's saved dimensions
	// before preloading. The PTY starts at 24x80 (pty.StartWithSize)
	// and the snapshot bytes were laid out for whatever size the prior
	// session had. Reflowing them into 24x80 misaligns cursor + column
	// anchors until the user's next fit; restoring the saved size up
	// front keeps the historic content readable immediately.
	if snapshot, cols, rows := a.loadAndConsumeVscreenSnapshot(c.Slug, c.UUID); len(snapshot) > 0 {
		if cols > 0 && rows > 0 {
			if err := a.coordinator.Resize(c.UUID, uint16(rows), uint16(cols)); err != nil {
				slog.Warn("resizing PTY to saved snapshot dims",
					slog.String("slug", c.Slug), slog.String("uuid", c.UUID),
					slog.Int("cols", cols), slog.Int("rows", rows), slog.Any("err", err))
			}
		}
		if err := a.coordinator.PreloadSessionSnapshot(c.UUID, snapshot); err != nil {
			slog.Warn("preloading vscreen snapshot on resume",
				slog.String("slug", c.Slug), slog.String("uuid", c.UUID), slog.Any("err", err))
		}
	}

	if sess, err := a.svc.ReadSession(context.Background(), c.Slug, c.UUID); err == nil {
		sess.Status = model.SessionStatusRunning
		sess.StopReason = ""
		if sess.Outcome == "claude transcript deleted" {
			sess.Outcome = ""
		}
		sess.Activity = model.SessionActivityIdle
		sess.Ended = nil
		// System-driven status flip (auto-resume after crash/shutdown);
		// the user didn't interact, so don't bump idea Updated.
		if err := a.svc.WriteSessionPassive(context.Background(), c.Slug, c.UUID, *sess); err != nil {
			slog.Warn("flipping session back to running on resume",
				slog.String("slug", c.Slug), slog.String("uuid", c.UUID), slog.Any("err", err))
		}
	}

	if a.mcpManager != nil {
		if isOrchestrator {
			a.mcpManager.RegisterRootSession(c.UUID)
		} else {
			a.mcpManager.RegisterSession(c.UUID)
		}
	}

	if err := a.svc.AppendHistory(context.Background(), c.Slug, model.HistoryEvent{
		Timestamp: time.Now(),
		Event:     "session_resumed_auto",
		Session:   c.UUID,
		Fields:    map[string]any{"reason": string(c.Reason)},
	}); err != nil {
		slog.Warn("appending session-resumed history event",
			slog.String("slug", c.Slug), slog.String("uuid", c.UUID),
			slog.Any("err", err))
	}

	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "session:resumed", map[string]any{
			"slug":   c.Slug,
			"uuid":   c.UUID,
			"reason": string(c.Reason),
		})
	}
	return nil
}

// markRunningSessionsStopped flips every Status=="running" session record
// across all ideas (and the orchestrator) to Status=="stopped" with the
// given StopReason. Used during Shutdown (StopReason=shutdown) and during
// Startup crash detection (StopReason=crash). Best-effort; one bad idea
// must not abort the sweep.

func (a *App) markRunningSessionsStopped(reason model.SessionStopReason) []ResumeCandidate {
	ctx := context.Background()
	ideas, err := a.svc.List(ctx)
	if err != nil {
		slog.Warn("listing ideas for session reconciliation",
			slog.String("reason", string(reason)), slog.Any("err", err))
		return nil
	}
	// Append the orchestrator as a synthetic idea so its session records
	// flow through the same reconciliation loop.
	ideas = append(ideas, model.Idea{Slug: model.OrchestratorSlug, Name: "Orchestrator"})
	var candidates []ResumeCandidate
	now := time.Now()
	for _, idea := range ideas {
		sessions, err := a.svc.ListSessions(ctx, idea.Slug)
		if err != nil {
			continue
		}
		for _, s := range sessions {
			if s.Status != model.SessionStatusRunning {
				continue
			}
			s.Status = model.SessionStatusStopped
			s.StopReason = reason
			s.Activity = ""
			s.ActiveReviewID = ""
			ended := now
			s.Ended = &ended
			// System-driven cleanup (startup crash detection / shutdown).
			// Skip the idea Updated bump — sessions ending here are not
			// user activity; bumping would drown the actual session the
			// user is interacting with.
			if err := a.svc.WriteSessionPassive(ctx, idea.Slug, s.UUID, s); err != nil {
				slog.Warn("persisting session stop reason",
					slog.String("slug", idea.Slug), slog.String("uuid", s.UUID),
					slog.String("reason", string(reason)), slog.Any("err", err))
				continue
			}
			candidates = append(candidates, ResumeCandidate{
				Slug:      idea.Slug,
				IdeaName:  idea.Name,
				UUID:      s.UUID,
				AgentType: s.Agent,
				Reason:    reason,
			})
		}
	}
	return candidates
}

// reviewStalenessAge is the maximum age a pending review can reach before
// the startup sweep cancels it as a safety valve, even if its linked
// session would otherwise auto-resume. Keeps abandoned drafts from
// accumulating forever.
const reviewStalenessAge = 30 * 24 * time.Hour

// cancelStaleReviews sweeps pending reviews and cancels those that won't
// be resumed: linked session is permanently gone (clean exit, /clear,
// /compact, user-stopped, orphaned transcript), the linked session record
// is missing entirely, or the review has aged past reviewStalenessAge.
// Reviews linked to sessions that will auto-resume (StopReason=shutdown
// or crash) and CLI/sessionless reviews are kept pending. Must run AFTER
// markRunningSessionsStopped so freshly-crashed sessions are visible with
// StopReason=crash. Returns the IDs that were cancelled.
