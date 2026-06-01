package app

import (
	"errors"
	"fmt"
	"log/slog"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/store"
)

func (a *App) ListIdeas() ([]model.Idea, error) {
	return a.svc.List(a.ctx)
}

// GetIdea returns a single idea with its files.

func (a *App) GetIdea(slug string) (*IdeaDetail, error) {
	idea, err := a.svc.Get(a.ctx, slug)
	if err != nil {
		return nil, err
	}
	files, _ := a.svc.ListFiles(a.ctx, slug)
	return &IdeaDetail{Idea: *idea, Files: files}, nil
}

// CreateIdea creates a new idea and returns its slug.

func (a *App) CreateIdea(name string, status string, summary string) (string, error) {
	idea := &model.Idea{
		Name:    name,
		Status:  model.Status(status),
		Summary: summary,
	}
	if idea.Status == "" {
		idea.Status = model.StatusPaused
	}
	if err := a.svc.Create(a.ctx, idea); err != nil {
		return "", err
	}
	return idea.Slug, nil
}

// ReadIdeaFile reads a file from an idea's directory. filename may be a
// relative path (e.g. "repos/<name>/README.md") so callers can read files
// inside linked worktrees; path traversal beyond the idea dir is rejected.

func (a *App) ReadIdeaFile(slug, filename string) (string, error) {
	return a.svc.ReadFile(a.ctx, slug, filename)
}

// ListRepoFiles returns top-level markdown files inside a linked worktree.

func (a *App) ListRepoFiles(slug, repoName string) ([]string, error) {
	return a.svc.ListRepoFiles(a.ctx, slug, repoName)
}

// WriteIdeaFile writes a file to an idea's directory.

func (a *App) WriteIdeaFile(slug, filename, content string) error {
	return a.svc.WriteFile(a.ctx, slug, filename, content)
}

// GetHistory returns the event log for an idea.

func (a *App) GetHistory(slug string) ([]model.HistoryEvent, error) {
	return a.svc.ReadHistory(a.ctx, slug)
}

// LinkRepo creates a per-idea worktree of the canonical repo at repoPath.
// branch is the branch name to check out (created from HEAD if it doesn't
// exist locally); empty falls back to the per-idea default. nameOverride
// forces a worktree leaf name; empty derives from origin or basename.
// Returns the resolved name.

func (a *App) LinkRepo(slug, repoPath, branch, nameOverride string) (string, error) {
	name, err := a.svc.LinkRepo(a.ctx, slug, repoPath, branch, nameOverride)
	if err != nil {
		return "", err
	}
	if a.watcher != nil {
		if addErr := a.watcher.AddWorktree(slug, name); addErr != nil {
			slog.Warn("watcher: add worktree",
				slog.String("slug", slug), slog.String("name", name), slog.Any("err", addErr))
		}
	}
	a.emitRepoChanged(slug, name)
	return name, nil
}

// UnlinkRepo removes the worktree for the named repo. With force=false,
// refuses on uncommitted changes; with force=true, removes regardless.

func (a *App) UnlinkRepo(slug, name string, force bool) error {
	if err := a.svc.UnlinkRepo(a.ctx, slug, name, force); err != nil {
		return err
	}
	if a.watcher != nil {
		a.watcher.RemoveWorktree(slug, name)
	}
	a.emitRepoChanged(slug, name)
	return nil
}

// ListRepos returns linked repositories for an idea.

func (a *App) ListRepos(slug string) ([]store.RepoLink, error) {
	return a.svc.ListRepos(a.ctx, slug)
}

func (a *App) emitRepoChanged(slug, name string) {
	if a.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(a.ctx, "repo:changed", map[string]string{
		"slug": slug,
		"name": name,
	})
}

// SessionStartResult is returned by StartIdeaSession.
type SessionStartResult struct {
	UUID string `json:"uuid"`
}

// Sentinel errors returned by StartIdeaSession when the single-session
// invariant (M14: at most one running session per (idea, agent type))
// would be violated. Callers match with errors.Is; the conflicting UUID
// is exposed via SessionInUseError when caught with errors.As.
var (
	ErrSessionAlreadyRunning = errors.New("session already running for this idea")
	ErrSessionStaleRunning   = errors.New("session record stuck in running state; awaiting reconciliation")
)

// SessionInUseError reports that StartIdeaSession refused to spawn a
// duplicate session. Wraps either ErrSessionAlreadyRunning (coordinator
// has the live session) or ErrSessionStaleRunning (record exists but
// the coordinator does not — typically post-crash, pre-auto-resume).
type SessionInUseError struct {
	UUID string
	Err  error
}

func (e *SessionInUseError) Error() string {
	return fmt.Sprintf("%s (uuid=%s)", e.Err.Error(), e.UUID)
}

func (e *SessionInUseError) Unwrap() error { return e.Err }

// StartIdeaSession launches an agent session for an idea. Sessions always
// run at the idea root — the agent uses link_repo / cd to enter linked
// worktrees as needed (see the file-scope guidance in BuildSystemPrompt).
//
// Enforces M14 (at most one running session per (idea, agent type)):
// returns ErrSessionAlreadyRunning when a live session exists, or
// ErrSessionStaleRunning when a record is persisted as running but the
// coordinator has lost it (post-crash, pre-auto-resume). Both are wrapped
// in *SessionInUseError so callers can recover the conflicting UUID.
