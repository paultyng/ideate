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

// failListReposStore proves the getter never calls ListRepos (which runs git).
type failListReposStore struct {
	store.Store
	t *testing.T
}

func (f failListReposStore) ListRepos(context.Context, string) ([]store.RepoLink, error) {
	f.t.Fatal("ListRepos must not be called")
	return nil, nil
}

func newGetterFixture(t *testing.T) (store.Store, string, *model.Idea) {
	t.Helper()
	ideasDir := t.TempDir()
	s := store.NewFSStore(ideasDir, filepath.Join(t.TempDir(), "reviews"), "pt/", "origin/main")
	idea := &model.Idea{Name: "Fixture", Status: model.StatusActive}
	if err := s.Create(context.Background(), idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return s, ideasDir, idea
}

func TestNewIdeaGetter_SkipsGitStatus(t *testing.T) {
	t.Parallel()
	s, ideasDir, idea := newGetterFixture(t)
	ideaDir := filepath.Join(ideasDir, idea.Slug)
	for _, name := range []string{"b", "a"} {
		if err := os.MkdirAll(filepath.Join(ideaDir, "repos", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := newIdeaGetter(failListReposStore{Store: s, t: t}, ideasDir)(idea.Slug)
	if err != nil {
		t.Fatalf("getter: %v", err)
	}
	want := []string{ideaDir, filepath.Join(ideaDir, "repos", "a"), filepath.Join(ideaDir, "repos", "b")}
	if !slices.Equal(got.AddDirs, want) {
		t.Errorf("AddDirs = %v, want %v", got.AddDirs, want)
	}
}

func TestNewIdeaGetter_SkipsNonDirs(t *testing.T) {
	t.Parallel()
	s, ideasDir, idea := newGetterFixture(t)
	ideaDir := filepath.Join(ideasDir, idea.Slug)
	reposDir := filepath.Join(ideaDir, "repos")
	if err := os.MkdirAll(filepath.Join(reposDir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reposDir, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := newIdeaGetter(s, ideasDir)(idea.Slug)
	if err != nil {
		t.Fatalf("getter: %v", err)
	}
	want := []string{ideaDir, filepath.Join(reposDir, "real")}
	if !slices.Equal(got.AddDirs, want) {
		t.Errorf("AddDirs = %v, want %v", got.AddDirs, want)
	}
}

func TestNewIdeaGetter_ReadDirError(t *testing.T) {
	t.Parallel()
	s, ideasDir, idea := newGetterFixture(t)
	ideaDir := filepath.Join(ideasDir, idea.Slug)
	if err := os.RemoveAll(filepath.Join(ideaDir, "repos")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ideaDir, "repos"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := newIdeaGetter(s, ideasDir)(idea.Slug)
	if err != nil {
		t.Fatalf("getter must not error: %v", err)
	}
	if want := []string{ideaDir}; !slices.Equal(got.AddDirs, want) {
		t.Errorf("AddDirs = %v, want %v", got.AddDirs, want)
	}
}

func TestNewIdeaGetter_EmptyReposDir(t *testing.T) {
	t.Parallel()
	s, ideasDir, idea := newGetterFixture(t)
	ideaDir := filepath.Join(ideasDir, idea.Slug)
	if err := os.MkdirAll(filepath.Join(ideaDir, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := newIdeaGetter(s, ideasDir)(idea.Slug)
	if err != nil {
		t.Fatalf("getter: %v", err)
	}
	if want := []string{ideaDir}; !slices.Equal(got.AddDirs, want) {
		t.Errorf("AddDirs = %v, want %v", got.AddDirs, want)
	}
}
