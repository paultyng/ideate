package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/store"
)

// IdeaGetter must surface the idea root plus every linked repo's
// absolute path so Claude's per-dir skill discovery picks up
// `.claude/skills/` directories committed inside each repo.
func TestNewIdeaGetter_AddDirsIncludeIdeaRootAndLinkedRepos(t *testing.T) {
	t.Parallel()

	ideasDir := t.TempDir()
	reviewsDir := filepath.Join(t.TempDir(), "reviews")
	s := store.NewFSStore(ideasDir, reviewsDir, "pt/", "origin/main")

	ctx := context.Background()
	idea := &model.Idea{Name: "Repo Skills", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Drop the worktree dirs directly — ListRepos enumerates the
	// `repos/` subdir, no git init required for path discovery.
	ideaDir := filepath.Join(ideasDir, idea.Slug)
	for _, name := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(ideaDir, "repos", name), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	getter := newIdeaGetter(s, ideasDir)
	got, err := getter(idea.Slug)
	if err != nil {
		t.Fatalf("getter: %v", err)
	}

	want := []string{
		ideaDir,
		filepath.Join(ideaDir, "repos", "alpha"),
		filepath.Join(ideaDir, "repos", "beta"),
	}
	if !slices.Equal(got.AddDirs, want) {
		t.Errorf("AddDirs = %v, want %v", got.AddDirs, want)
	}
	if got.Idea.Name != "Repo Skills" {
		t.Errorf("Idea.Name = %q, want %q", got.Idea.Name, "Repo Skills")
	}
}

// IdeaGetter without any linked repos still returns the idea root
// alone. ListRepos returns (nil, nil) for a missing repos/ dir.
func TestNewIdeaGetter_NoReposReturnsIdeaRootOnly(t *testing.T) {
	t.Parallel()

	ideasDir := t.TempDir()
	reviewsDir := filepath.Join(t.TempDir(), "reviews")
	s := store.NewFSStore(ideasDir, reviewsDir, "pt/", "origin/main")

	ctx := context.Background()
	idea := &model.Idea{Name: "No Repos", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	getter := newIdeaGetter(s, ideasDir)
	got, err := getter(idea.Slug)
	if err != nil {
		t.Fatalf("getter: %v", err)
	}

	want := []string{filepath.Join(ideasDir, idea.Slug)}
	if !slices.Equal(got.AddDirs, want) {
		t.Errorf("AddDirs = %v, want %v", got.AddDirs, want)
	}
}
