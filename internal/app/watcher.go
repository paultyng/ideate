package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/paultyng/ideate/internal/repo"
)

// EventIdeaChanged is emitted when a file under <ideasDir>/<slug>/ is
// created/modified/removed. Frontend listeners refetch the affected idea.
const EventIdeaChanged = "idea:changed"

// EventRepoChanged is emitted when a worktree's git admin dir changes —
// chiefly when HEAD is rewritten on `git checkout` / `git switch`. The
// payload is {slug, name}; the frontend refetches ListRepos.
const EventRepoChanged = "repo:changed"

// IdeaChangedPayload describes the affected idea.
type IdeaChangedPayload struct {
	Slug string `json:"slug"`
}

// RepoChangedPayload describes the affected worktree.
type RepoChangedPayload struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// debounceWindow coalesces multiple events for the same key into a single
// emission. Editor saves and atomic renames typically fire several events
// within milliseconds; 500ms is well above that without feeling laggy.
const debounceWindow = 500 * time.Millisecond

// configFilename is the ideas-directory-level config file. Writes to this
// file (from the user's editor or from an MCP tool that mutates config)
// trigger a hot-reload of the store's branch_prefix / tracking_branch.
const configFilename = "config.json"

// ideaWatcher watches the ideas directory and emits idea:changed events to
// the frontend when files inside an idea subdirectory change. It also
// watches each linked worktree's git admin dir
// (<canonical>/.git/worktrees/<name>/) for HEAD changes — branch switches —
// and emits repo:changed events.
//
// The store itself is stateless except for the cached config.json values
// (branch_prefix, tracking_branch). Writes to <ideasDir>/config.json
// invoke reloadConfig so external edits take effect without a restart.
type ideaWatcher struct {
	rootDir      string
	emit         func(event string, data any)
	reloadConfig func() error
	w            *fsnotify.Watcher

	mu            sync.Mutex
	debouncer     map[string]*time.Timer // key → pending emit timer (slug for ideas, "slug/name" for repos)
	worktreeAdmin map[string]worktreeKey // admin dir abs path → owner
}

type worktreeKey struct {
	Slug, Name string
}

// startWatcher spawns a watcher goroutine that runs until ctx is cancelled.
// If fsnotify init fails (rare; unsupported OS), returns an error and the
// caller should continue without live reload. reloadConfig is called when
// <rootDir>/config.json is written; pass nil to disable that branch.
func startWatcher(ctx context.Context, rootDir string, emit func(string, any), reloadConfig func() error) (*ideaWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("creating fsnotify watcher: %w", err)
	}

	iw := &ideaWatcher{
		rootDir:       rootDir,
		emit:          emit,
		reloadConfig:  reloadConfig,
		w:             w,
		debouncer:     make(map[string]*time.Timer),
		worktreeAdmin: make(map[string]worktreeKey),
	}

	if err := iw.addInitialPaths(); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("seeding watcher paths: %w", err)
	}

	go iw.run(ctx)
	return iw, nil
}

// addInitialPaths watches the ideas root + each existing idea subdirectory.
// fsnotify is non-recursive on Linux/macOS, so each directory is added
// explicitly. Subdirs created later are picked up via Create events. Linked
// worktrees discovered under <idea>/repos/ also get an admin-dir watch.
func (iw *ideaWatcher) addInitialPaths() error {
	if err := iw.w.Add(iw.rootDir); err != nil {
		return fmt.Errorf("watching root: %w", err)
	}
	entries, err := os.ReadDir(iw.rootDir)
	if err != nil {
		return fmt.Errorf("reading root: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		slug := e.Name()
		if err := iw.w.Add(filepath.Join(iw.rootDir, slug)); err != nil {
			slog.Warn("watcher: failed to add idea dir",
				slog.String("slug", slug), slog.Any("err", err))
		}
		iw.seedWorktreeWatches(slug)
	}
	return nil
}

// seedWorktreeWatches scans <idea>/repos/ for existing worktrees and adds an
// admin-dir watch for each.
func (iw *ideaWatcher) seedWorktreeWatches(slug string) {
	reposPath := filepath.Join(iw.rootDir, slug, "repos")
	entries, err := os.ReadDir(reposPath)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if err := iw.AddWorktree(slug, e.Name()); err != nil {
			slog.Warn("watcher: failed to add worktree admin watch",
				slog.String("slug", slug), slog.String("name", e.Name()), slog.Any("err", err))
		}
	}
}

// AddWorktree begins watching the canonical-side admin dir for the named
// worktree so HEAD changes (branch switches, ref updates) emit repo:changed.
// Safe to call from outside the watcher goroutine.
func (iw *ideaWatcher) AddWorktree(slug, name string) error {
	wt := filepath.Join(iw.rootDir, slug, "repos", name)
	canonical, err := repo.Canonical(context.Background(), wt)
	if err != nil {
		return fmt.Errorf("resolving canonical: %w", err)
	}
	adminDir := repo.WorktreeAdminDir(canonical, name)
	abs, err := filepath.Abs(adminDir)
	if err != nil {
		return fmt.Errorf("absolute admin dir: %w", err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}

	iw.mu.Lock()
	iw.worktreeAdmin[abs] = worktreeKey{Slug: slug, Name: name}
	iw.mu.Unlock()

	if err := iw.w.Add(abs); err != nil {
		return fmt.Errorf("watching admin dir: %w", err)
	}
	return nil
}

// RemoveWorktree stops watching the admin dir for the given worktree.
func (iw *ideaWatcher) RemoveWorktree(slug, name string) {
	iw.mu.Lock()
	var target string
	for path, key := range iw.worktreeAdmin {
		if key.Slug == slug && key.Name == name {
			target = path
			break
		}
	}
	if target != "" {
		delete(iw.worktreeAdmin, target)
	}
	iw.mu.Unlock()

	if target != "" {
		_ = iw.w.Remove(target)
	}
}

// run consumes fsnotify events until ctx is done.
func (iw *ideaWatcher) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			_ = iw.w.Close()
			iw.flushTimers()
			return
		case ev, ok := <-iw.w.Events:
			if !ok {
				iw.flushTimers()
				return
			}
			iw.handleEvent(ev)
		case err, ok := <-iw.w.Errors:
			if !ok {
				return
			}
			slog.Warn("watcher error", slog.Any("err", err))
		}
	}
}

// handleEvent classifies an event and schedules a debounced emit. Worktree
// admin-dir events route to repo:changed; other events route to idea:changed
// based on the event path's slug parent.
func (iw *ideaWatcher) handleEvent(ev fsnotify.Event) {
	if key, ok := iw.matchWorktree(ev.Name); ok {
		iw.scheduleRepoEmit(key)
		return
	}

	rel, err := filepath.Rel(iw.rootDir, ev.Name)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return
	}

	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	slug := parts[0]
	if slug == "" || strings.HasPrefix(slug, ".") {
		return
	}

	// Top-level config.json: hot-swap the store's cached values rather
	// than treating it as an idea-changed event. Skips when reloadConfig
	// is nil (test wiring or platforms that opt out).
	if len(parts) == 1 && slug == configFilename {
		if iw.reloadConfig != nil && (ev.Has(fsnotify.Write) || ev.Has(fsnotify.Create)) {
			if err := iw.reloadConfig(); err != nil {
				slog.Warn("watcher: reloading config.json", slog.Any("err", err))
			} else {
				slog.Info("watcher: reloaded config.json")
			}
		}
		return
	}

	// Top-level event: idea dir created or removed.
	if len(parts) == 1 {
		if ev.Has(fsnotify.Create) {
			full := filepath.Join(iw.rootDir, slug)
			info, err := os.Stat(full)
			if err == nil && info.IsDir() {
				if addErr := iw.w.Add(full); addErr != nil {
					slog.Warn("watcher: failed to watch new idea dir",
						slog.String("slug", slug), slog.Any("err", addErr))
				}
			}
		}
		// Remove/Rename: fsnotify auto-cleans the watch; nothing to do.
		iw.scheduleEmit(slug)
		return
	}

	// Path inside an idea dir.
	filename := filepath.Base(rel)
	if shouldIgnore(filename) {
		return
	}

	// Skip changes deep inside repos/ — those are agent edits to worktree
	// contents and don't reflect user-visible idea state. Branch switches
	// inside repos/ are handled via the worktree admin watch above.
	// sessions/ is intentionally NOT skipped: session record writes drive
	// the sidebar status icons, and the existing 500ms debounce keeps a
	// burst of hook-driven writes from amplifying.
	sub := parts[1]
	if strings.HasPrefix(sub, "repos"+string(filepath.Separator)) {
		// A new worktree directory created directly under repos/ (e.g. via
		// `ideate repo link` from the CLI while the app is running) needs an
		// admin watch added. Detect new top-level repos/<name> directories
		// only — deeper changes are agent edit noise and stay skipped.
		repoParts := strings.Split(sub, string(filepath.Separator))
		if len(repoParts) == 2 && ev.Has(fsnotify.Create) && repoParts[1] != "" && !strings.HasPrefix(repoParts[1], ".") {
			full := filepath.Join(iw.rootDir, slug, "repos", repoParts[1])
			if info, err := os.Stat(full); err == nil && info.IsDir() {
				if addErr := iw.AddWorktree(slug, repoParts[1]); addErr != nil {
					slog.Warn("watcher: add worktree (auto)",
						slog.String("slug", slug), slog.String("name", repoParts[1]), slog.Any("err", addErr))
				}
				iw.scheduleRepoEmit(worktreeKey{Slug: slug, Name: repoParts[1]})
			}
		}
		return
	}

	iw.scheduleEmit(slug)
}

// matchWorktree returns the worktree owner if eventPath sits inside a
// watched admin dir.
func (iw *ideaWatcher) matchWorktree(eventPath string) (worktreeKey, bool) {
	abs, err := filepath.Abs(eventPath)
	if err != nil {
		return worktreeKey{}, false
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}

	iw.mu.Lock()
	defer iw.mu.Unlock()
	// Direct match (dir-level event) or parent match (file inside admin dir).
	for path, key := range iw.worktreeAdmin {
		if abs == path || strings.HasPrefix(abs, path+string(filepath.Separator)) {
			return key, true
		}
	}
	return worktreeKey{}, false
}

// shouldIgnore returns true for editor noise / atomic-rename temp files.
// atomicfile.Write uses a `.tmp-*` prefix; vim/.swp and .DS_Store also
// caught by the dot-prefix rule.
//
// summary.json is ignored too: the summarizer writes it, and an
// idea:changed emit on its own write would re-trigger the
// summarizer (Phase 3C). The summarizer doesn't need a watcher
// event to know about its own writes.
func shouldIgnore(filename string) bool {
	if filename == "" || filename[0] == '.' {
		return true
	}
	if strings.HasSuffix(filename, "~") {
		return true
	}
	if filename == "summary.json" {
		return true
	}
	return false
}

// scheduleEmit debounces idea-changed emits per slug.
func (iw *ideaWatcher) scheduleEmit(slug string) {
	key := "idea:" + slug
	iw.mu.Lock()
	defer iw.mu.Unlock()

	if t, ok := iw.debouncer[key]; ok {
		t.Stop()
	}
	iw.debouncer[key] = time.AfterFunc(debounceWindow, func() {
		iw.mu.Lock()
		delete(iw.debouncer, key)
		iw.mu.Unlock()
		iw.emit(EventIdeaChanged, IdeaChangedPayload{Slug: slug})
	})
}

// scheduleRepoEmit debounces repo-changed emits per (slug, name).
func (iw *ideaWatcher) scheduleRepoEmit(wk worktreeKey) {
	key := "repo:" + wk.Slug + "/" + wk.Name
	iw.mu.Lock()
	defer iw.mu.Unlock()

	if t, ok := iw.debouncer[key]; ok {
		t.Stop()
	}
	iw.debouncer[key] = time.AfterFunc(debounceWindow, func() {
		iw.mu.Lock()
		delete(iw.debouncer, key)
		iw.mu.Unlock()
		iw.emit(EventRepoChanged, RepoChangedPayload(wk))
	})
}

// flushTimers stops pending debouncer timers (called on shutdown so we
// don't leak emit callbacks after the Wails runtime is gone).
func (iw *ideaWatcher) flushTimers() {
	iw.mu.Lock()
	defer iw.mu.Unlock()
	for key, t := range iw.debouncer {
		t.Stop()
		delete(iw.debouncer, key)
	}
}
