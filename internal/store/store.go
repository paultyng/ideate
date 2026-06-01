package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/paultyng/ideate/internal/model"
)

// RepoLink describes a linked repository worktree. Path is the worktree
// location relative to the idea root (e.g. "repos/ideate"). All remaining
// fields are computed live from the worktree on each ListRepos call.
type RepoLink struct {
	Name            string `json:"name"`
	Path            string `json:"path"`            // worktree path relative to the idea root
	OriginURL       string `json:"originUrl"`       // origin remote URL, if set
	Branch          string `json:"branch"`          // current branch
	IsDefaultBranch bool   `json:"isDefaultBranch"` // branch is the per-idea default (i.e. the worktree hasn't moved to a feature branch yet)
	Dirty           bool   `json:"dirty"`           // any uncommitted changes
	Ahead           int    `json:"ahead"`           // commits ahead of upstream
	Behind          int    `json:"behind"`          // commits behind upstream
}

// ErrDirtyRepos is returned by Delete when linked worktrees have uncommitted
// changes and force was not set. Callers can confirm with the user, then
// re-invoke with force=true.
type ErrDirtyRepos struct {
	Repos []string
}

func (e *ErrDirtyRepos) Error() string {
	return fmt.Sprintf("uncommitted changes in: %s", strings.Join(e.Repos, ", "))
}

// Store is the interface for idea persistence.
type Store interface {
	List(ctx context.Context) ([]model.Idea, error)
	Get(ctx context.Context, slug string) (*model.Idea, error)
	Create(ctx context.Context, idea *model.Idea) error
	Update(ctx context.Context, idea *model.Idea) error
	Rename(ctx context.Context, oldSlug, newSlug string) error
	Delete(ctx context.Context, slug string, force bool) error

	ListFiles(ctx context.Context, slug string) ([]string, error)
	ReadFile(ctx context.Context, slug, filename string) (string, error)
	WriteFile(ctx context.Context, slug, filename, content string) error

	AppendHistory(ctx context.Context, slug string, event model.HistoryEvent) error
	ReadHistory(ctx context.Context, slug string) ([]model.HistoryEvent, error)

	// LinkRepo creates a worktree of the canonical repo at repoPath. branch
	// is checked out (created from HEAD if it doesn't exist); empty branch
	// uses the per-idea default. nameOverride forces a specific worktree
	// name; empty derives from the origin remote or canonical basename.
	// Returns the resolved name.
	LinkRepo(ctx context.Context, slug, repoPath, branch, nameOverride string) (string, error)
	UnlinkRepo(ctx context.Context, slug, name string, force bool) error
	ListRepos(ctx context.Context, slug string) ([]RepoLink, error)

	// Session key is the agent session ID used as the filename ({id}.json).
	WriteSession(ctx context.Context, slug, key string, session model.AgentSession) error
	// WriteSessionPassive persists a session record without bumping the
	// idea's Updated timestamp. Use for system-driven mutations (startup
	// crash cleanup, auto-resume status flips) that do not represent user
	// activity. User-driven and hook-driven mutations should use the
	// regular WriteSession/UpdateSession path.
	WriteSessionPassive(ctx context.Context, slug, key string, session model.AgentSession) error
	ReadSession(ctx context.Context, slug, key string) (*model.AgentSession, error)
	ListSessions(ctx context.Context, slug string) ([]model.AgentSession, error)
	UpdateSession(ctx context.Context, slug, key string, session model.AgentSession) error
	// FindRunningSession returns the running session for (slug, agentType), or nil.
	FindRunningSession(ctx context.Context, slug, agentType string) (*model.AgentSession, error)

	// TouchIdea bumps an idea's Updated timestamp without recording a history
	// event. Used by hook handlers and session-driven flows so MRU sort
	// reflects activity. Returns the new Updated value.
	TouchIdea(ctx context.Context, slug string) (time.Time, error)
}
