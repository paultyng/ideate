package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/repo"
)

// ErrIdeaNotFound is returned by RenameIdea when the source slug doesn't
// resolve to an existing idea directory.
var ErrIdeaNotFound = errors.New("idea not found")

// ErrSlugTaken is returned by RenameIdea when the target slug already
// names an existing directory under <ideasDir>.
var ErrSlugTaken = errors.New("target slug already exists")

// ErrIdeaBusy is returned by RenameIdea when the source idea has at
// least one running session. Rename is forbidden mid-session because
// the agent's PTY cwd, Claude transcript path, and worktree admin
// links would all need atomic surgery while live; the caller is
// expected to stop or wait for the session before retrying.
var ErrIdeaBusy = errors.New("idea has running sessions; stop them before renaming")

// WorkingDirMove records a single (oldDir → newDir) translation that
// RenameIdea applied to a session's WorkingDir. The MCP tool layer
// uses these to migrate Claude's per-cwd transcript directories under
// ~/.claude/projects/ — store deliberately doesn't take a dependency
// on Claude's encoding scheme, that lives in agent/transcript/claudefmt.
type WorkingDirMove struct {
	OldDir string
	NewDir string
}

// RenameResult is what RenameIdea returns on success.
type RenameResult struct {
	OldSlug         string
	NewSlug         string
	SessionsRewired int
	// WorktreesRebuilt is the count of linked worktrees that were
	// removed before the directory move and re-added at the new path
	// (on the same branch). The rebuild trades file-tree state for
	// simplicity: any uncommitted/untracked content in a worktree is
	// discarded and a fresh checkout is staged at the new location.
	// DirtyWorktrees lists the worktrees that had uncommitted or
	// untracked content at rebuild time so the caller can surface a
	// warning to the user.
	WorktreesRebuilt int
	DirtyWorktrees   []string
	WorkingDirMoves  []WorkingDirMove
}

// RenameIdea moves <ideasDir>/<oldSlug>/ to <ideasDir>/<newSlug>/ and
// rewires the bookkeeping that pointed at the old path:
//
//   - Every linked worktree under repos/ is removed BEFORE the
//     directory move and re-added at the new path on the same
//     branch AFTER the move. This trades worktree filesystem state
//     (uncommitted edits, untracked files like .env or build
//     artifacts) for simplicity — renames are infrequent enough
//     that the cost is acceptable, and the caller is told which
//     worktrees had dirt at rebuild time so it can warn the user.
//   - Every session JSON's WorkingDir is updated. Paths under the old
//     idea dir keep their relative portion (so .../old/repos/foo →
//     .../new/repos/foo); paths outside the idea dir are rehomed to
//     the new idea root.
//   - A "renamed" history event is appended.
//
// Returns the per-session WorkingDir transitions so the caller can
// migrate any external state keyed on the old path (e.g. Claude
// transcripts under ~/.claude/projects/<encoded-cwd>/).
//
// Refuses on:
//
//   - oldSlug not present (ErrIdeaNotFound)
//   - newSlug directory already exists (ErrSlugTaken)
//   - any session under oldSlug currently in Status=running (ErrIdeaBusy)
func (s *FSStore) RenameIdea(ctx context.Context, oldSlug, newSlug string) (*RenameResult, error) {
	if oldSlug == "" || newSlug == "" {
		return nil, fmt.Errorf("rename: empty slug")
	}
	if oldSlug == newSlug {
		return nil, fmt.Errorf("rename: old and new slugs are identical")
	}
	if cleaned := model.Slugify(newSlug); cleaned != newSlug {
		return nil, fmt.Errorf("rename: %q is not a valid slug; expected %q", newSlug, cleaned)
	}

	oldDir := s.ideaDir(oldSlug)
	newDir := s.ideaDir(newSlug)
	if !s.dirExists(oldSlug) {
		return nil, fmt.Errorf("%w: %s", ErrIdeaNotFound, oldSlug)
	}
	if s.dirExists(newSlug) {
		return nil, fmt.Errorf("%w: %s", ErrSlugTaken, newSlug)
	}

	sessions, err := s.ListSessions(ctx, oldSlug)
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	for _, sess := range sessions {
		if sess.Status == model.SessionStatusRunning {
			return nil, fmt.Errorf("%w: session %s", ErrIdeaBusy, sess.UUID)
		}
	}

	// Plan WorkingDir transitions before the rename so the encoded
	// path computation uses the *originally* persisted cwd. After
	// os.Rename the inside-idea paths still resolve via the prefix
	// translation, but capturing them up front simplifies reasoning.
	var moves []WorkingDirMove
	for _, sess := range sessions {
		if sess.WorkingDir == "" {
			continue
		}
		moves = append(moves, WorkingDirMove{
			OldDir: sess.WorkingDir,
			NewDir: translateWorkingDir(sess.WorkingDir, oldDir, newDir),
		})
	}

	// Capture every linked worktree's canonical + branch + dirty
	// state BEFORE removing them. Recorded fields are everything
	// AddWorktree needs to rebuild the worktree at the new path.
	type wtRebuild struct {
		Name      string
		Canonical string
		Branch    string
	}
	var rebuilds []wtRebuild
	var dirty []string
	if entries, err := os.ReadDir(filepath.Join(oldDir, reposDir)); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			wt := filepath.Join(oldDir, reposDir, e.Name())
			canonical, err := repo.Canonical(ctx, wt)
			if err != nil {
				slog.Warn("rename: skipping worktree (canonical resolve failed)",
					slog.String("worktree", e.Name()),
					slog.String("err", err.Error()))
				continue
			}
			status, err := repo.ReadStatus(ctx, wt)
			if err != nil {
				slog.Warn("rename: skipping worktree (status read failed)",
					slog.String("worktree", e.Name()),
					slog.String("err", err.Error()))
				continue
			}
			if status.Dirty {
				slog.Warn("rename: worktree has uncommitted or untracked content; it will be discarded during rebuild",
					slog.String("worktree", e.Name()))
				dirty = append(dirty, e.Name())
			}
			rebuilds = append(rebuilds, wtRebuild{
				Name:      e.Name(),
				Canonical: canonical,
				Branch:    status.Branch,
			})
			if err := repo.RemoveWorktree(ctx, wt, true); err != nil {
				slog.Warn("rename: removing worktree before move",
					slog.String("worktree", e.Name()),
					slog.String("err", err.Error()))
			}
		}
	}

	// Atomic dir-level move on the same filesystem. After this point
	// any failure leaves a half-migrated state — we log + best-effort
	// continue rather than rolling back, since reverting the os.Rename
	// after sessions and worktrees have been touched would create its
	// own consistency hazard.
	if err := os.Rename(oldDir, newDir); err != nil {
		return nil, fmt.Errorf("renaming idea dir: %w", err)
	}

	// Rewrite WorkingDir on every session that needs it. Read fresh
	// from the new location so we operate on the post-rename layout.
	rewired := 0
	if newSessions, err := s.ListSessions(ctx, newSlug); err == nil {
		for _, sess := range newSessions {
			if sess.WorkingDir == "" {
				continue
			}
			next := translateWorkingDir(sess.WorkingDir, oldDir, newDir)
			if next == sess.WorkingDir {
				continue
			}
			sess.WorkingDir = next
			if err := s.UpdateSession(ctx, newSlug, sess.UUID, sess); err != nil {
				slog.Warn("rename: updating session WorkingDir",
					slog.String("slug", newSlug),
					slog.String("session", sess.UUID),
					slog.String("err", err.Error()))
				continue
			}
			rewired++
		}
	}

	// Re-add every captured worktree at its new path. AddWorktree
	// recreates the worktree dir + checks out the same branch from
	// the canonical, producing a fresh checkout that loses any
	// uncommitted or untracked content (per the contract documented
	// on RenameResult.WorktreesRebuilt / .DirtyWorktrees).
	rebuilt := 0
	for _, rb := range rebuilds {
		newPath := filepath.Join(newDir, reposDir, rb.Name)
		if err := repo.AddWorktree(ctx, rb.Canonical, newPath, rb.Branch); err != nil {
			slog.Warn("rename: re-adding worktree at new path",
				slog.String("slug", newSlug),
				slog.String("worktree", rb.Name),
				slog.String("err", err.Error()))
			continue
		}
		rebuilt++
	}

	if err := s.appendHistory(newSlug, model.HistoryEvent{
		Timestamp: time.Now(),
		Event:     "renamed",
		Fields: map[string]any{
			"old_slug": oldSlug,
			"new_slug": newSlug,
		},
	}); err != nil {
		slog.Warn("rename: appending history",
			slog.String("slug", newSlug), slog.String("err", err.Error()))
	}

	return &RenameResult{
		OldSlug:          oldSlug,
		NewSlug:          newSlug,
		SessionsRewired:  rewired,
		WorktreesRebuilt: rebuilt,
		DirtyWorktrees:   dirty,
		WorkingDirMoves:  moves,
	}, nil
}

// translateWorkingDir returns the WorkingDir's post-rename location.
// If wd lives under oldIdeaDir, the relative portion is preserved
// under newIdeaDir; otherwise wd is rehomed to newIdeaDir (the new
// idea root) — external paths can't be tracked across renames.
func translateWorkingDir(wd, oldIdeaDir, newIdeaDir string) string {
	rel, err := filepath.Rel(oldIdeaDir, wd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return newIdeaDir
	}
	if rel == "." {
		return newIdeaDir
	}
	return filepath.Join(newIdeaDir, rel)
}
