package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paultyng/ideate/internal/model"
)

func TestRenameIdea_BasicAndWorkingDirRewrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	idea := &model.Idea{Name: "Old Name", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	oldSlug := idea.Slug

	// Two sessions: one inside the idea tree (under repos/foo) — should
	// have its WorkingDir prefix-translated; one outside — should be
	// rehomed to the new idea root.
	insideWD := filepath.Join(s.ideaDir(oldSlug), "repos", "foo")
	outsideWD := t.TempDir()
	insideUUID := "inside-uuid"
	outsideUUID := "outside-uuid"
	if err := s.WriteSession(ctx, oldSlug, insideUUID, model.AgentSession{
		UUID:       insideUUID,
		Status:     model.SessionStatusCompleted,
		WorkingDir: insideWD,
	}); err != nil {
		t.Fatalf("WriteSession inside: %v", err)
	}
	if err := s.WriteSession(ctx, oldSlug, outsideUUID, model.AgentSession{
		UUID:       outsideUUID,
		Status:     model.SessionStatusCompleted,
		WorkingDir: outsideWD,
	}); err != nil {
		t.Fatalf("WriteSession outside: %v", err)
	}

	res, err := s.RenameIdea(ctx, oldSlug, "renamed-slug")
	if err != nil {
		t.Fatalf("RenameIdea: %v", err)
	}
	if res.NewSlug != "renamed-slug" || res.OldSlug != oldSlug {
		t.Errorf("result slugs = %+v", res)
	}
	if res.SessionsRewired != 2 {
		t.Errorf("SessionsRewired = %d, want 2", res.SessionsRewired)
	}
	if len(res.WorkingDirMoves) != 2 {
		t.Errorf("WorkingDirMoves = %d, want 2", len(res.WorkingDirMoves))
	}

	// Verify dir was renamed and old gone.
	if _, err := os.Stat(s.ideaDir("renamed-slug")); err != nil {
		t.Fatalf("new dir not present: %v", err)
	}
	if _, err := os.Stat(s.ideaDir(oldSlug)); !os.IsNotExist(err) {
		t.Errorf("old dir still present: %v", err)
	}

	// Inside-tree WorkingDir: prefix translated.
	got, err := s.ReadSession(ctx, "renamed-slug", insideUUID)
	if err != nil {
		t.Fatalf("ReadSession inside: %v", err)
	}
	wantInside := filepath.Join(s.ideaDir("renamed-slug"), "repos", "foo")
	if got.WorkingDir != wantInside {
		t.Errorf("inside WorkingDir = %q, want %q", got.WorkingDir, wantInside)
	}

	// Outside-tree WorkingDir: rehomed to new idea root.
	got, err = s.ReadSession(ctx, "renamed-slug", outsideUUID)
	if err != nil {
		t.Fatalf("ReadSession outside: %v", err)
	}
	if got.WorkingDir != s.ideaDir("renamed-slug") {
		t.Errorf("outside WorkingDir = %q, want %q (rehomed)", got.WorkingDir, s.ideaDir("renamed-slug"))
	}

	// History event recorded under the new slug.
	hist, err := s.ReadHistory(ctx, "renamed-slug")
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	hasRenameEvent := false
	for _, h := range hist {
		if h.Event == "renamed" {
			hasRenameEvent = true
			if h.Fields["old_slug"] != oldSlug || h.Fields["new_slug"] != "renamed-slug" {
				t.Errorf("rename history fields = %+v", h.Fields)
			}
		}
	}
	if !hasRenameEvent {
		t.Errorf("no renamed history event under new slug")
	}
}

func TestRenameIdea_RefusesOnRunningSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	idea := &model.Idea{Name: "Busy Idea", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSession(ctx, idea.Slug, "running-uuid", model.AgentSession{
		UUID:   "running-uuid",
		Status: model.SessionStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := s.RenameIdea(ctx, idea.Slug, "new-busy")
	if !errors.Is(err, ErrIdeaBusy) {
		t.Fatalf("got %v, want ErrIdeaBusy", err)
	}

	// Old dir untouched.
	if _, err := os.Stat(s.ideaDir(idea.Slug)); err != nil {
		t.Errorf("old dir gone after refused rename: %v", err)
	}
	if _, err := os.Stat(s.ideaDir("new-busy")); !os.IsNotExist(err) {
		t.Errorf("new dir created despite refused rename")
	}
}

func TestRenameIdea_RefusesOnTargetCollision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	a := &model.Idea{Name: "Source", Status: model.StatusActive}
	if err := s.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &model.Idea{Name: "Target", Status: model.StatusActive}
	if err := s.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	_, err := s.RenameIdea(ctx, a.Slug, b.Slug)
	if !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("got %v, want ErrSlugTaken", err)
	}
}

func TestRenameIdea_RefusesUnknownSource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	_, err := s.RenameIdea(ctx, "no-such-idea", "anywhere")
	if !errors.Is(err, ErrIdeaNotFound) {
		t.Fatalf("got %v, want ErrIdeaNotFound", err)
	}
}

func TestRenameIdea_RebuildsWorktrees(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, dir := newTestStore(t)

	idea := &model.Idea{Name: "Repo Idea", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatal(err)
	}

	// Set up a canonical repo + linked worktree under the idea tree.
	canonical := filepath.Join(dir, "canonical")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	initTestRepo(t, canonical, "")
	if _, err := s.LinkRepo(ctx, idea.Slug, canonical, "feature-branch", ""); err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}
	repos, err := s.ListRepos(ctx, idea.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 {
		t.Fatalf("ListRepos = %d, want 1", len(repos))
	}

	if _, err := s.RenameIdea(ctx, idea.Slug, "new-repo-slug"); err != nil {
		t.Fatalf("RenameIdea: %v", err)
	}

	// After rename, git rev-parse from the worktree should still
	// resolve — proving the worktree was rebuilt at the new path
	// on the same branch.
	wt := filepath.Join(s.ideaDir("new-repo-slug"), reposDir, repos[0].Name)
	out, err := exec.CommandContext(ctx, "git", "-C", wt, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse in repaired worktree: %s: %v", strings.TrimSpace(string(out)), err)
	}
}
