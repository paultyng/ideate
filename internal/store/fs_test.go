package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paultyng/ideate/internal/model"
)

func newTestStore(t *testing.T) (*FSStore, string) {
	t.Helper()
	dir := t.TempDir()
	return NewFSStore(dir, filepath.Join(dir, ".reviews"), "pt/", ""), dir
}

func TestCreateAndGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, dir := newTestStore(t)

	idea := &model.Idea{
		Name:    "Batch Processing",
		Status:  model.StatusPaused,
		Summary: "An idea about batch processing.\n",
	}

	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if idea.Slug == "" {
		t.Fatal("Slug not set after Create")
	}

	// Verify directory structure.
	ideaDir := filepath.Join(dir, idea.Slug)
	for _, sub := range []string{"repos", "sessions"} {
		p := filepath.Join(ideaDir, sub)
		fi, err := os.Stat(p)
		if err != nil {
			t.Errorf("expected subdir %q: %v", sub, err)
		} else if !fi.IsDir() {
			t.Errorf("%q is not a directory", sub)
		}
	}

	// Verify idea.md exists.
	if _, err := os.Stat(filepath.Join(ideaDir, "idea.md")); err != nil {
		t.Fatalf("idea.md not found: %v", err)
	}

	// Get it back.
	got, err := s.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "Batch Processing" {
		t.Errorf("Name = %q, want %q", got.Name, "Batch Processing")
	}
	if got.Status != model.StatusPaused {
		t.Errorf("Status = %q, want %q", got.Status, model.StatusPaused)
	}
	if got.Summary != "An idea about batch processing.\n" {
		t.Errorf("Summary = %q, want %q", got.Summary, "An idea about batch processing.\n")
	}
	if got.Created.IsZero() {
		t.Error("Created is zero")
	}
}

func TestList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	// Create two ideas.
	idea1 := &model.Idea{Name: "First Idea", Status: model.StatusActive}
	if err := s.Create(ctx, idea1); err != nil {
		t.Fatalf("Create idea1: %v", err)
	}

	// Ensure second idea gets a different Updated time.
	idea2 := &model.Idea{
		Name:    "Second Idea",
		Status:  model.StatusPaused,
		Updated: time.Now().Add(time.Second),
	}
	if err := s.Create(ctx, idea2); err != nil {
		t.Fatalf("Create idea2: %v", err)
	}

	ideas, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ideas) != 2 {
		t.Fatalf("List len = %d, want 2", len(ideas))
	}

	// Most recent first.
	if ideas[0].Name != "Second Idea" {
		t.Errorf("first listed = %q, want %q", ideas[0].Name, "Second Idea")
	}
}

// Phase A: a bare-slug directory (no date prefix) created with an
// idea.md is discovered by List. Created falls back to the parsed
// frontmatter when present, the slug-derived date when not, and zero
// otherwise. This test covers the legacy-style date-prefixed case
// where frontmatter has no Created.
func TestList_LegacyDatePrefixedSlugSansFrontmatterCreated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	// Hand-write a date-prefixed dir whose idea.md has no `created`
	// in frontmatter — the shape of records that pre-date Phase A.
	slug := "2026-01-15-legacy-idea"
	dir := filepath.Join(s.ideasDir, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: Legacy Idea\nstatus: thinking\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "idea.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	ideas, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ideas) != 1 || ideas[0].Slug != slug {
		t.Fatalf("List = %+v, want [%s]", ideas, slug)
	}
	want := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	if !ideas[0].Created.Equal(want) {
		t.Errorf("Created = %v, want %v (slug-derived fallback)", ideas[0].Created, want)
	}
}

// Phase A: a bare-slug directory with `created` in frontmatter is
// listed and the frontmatter time wins.
func TestList_BareSlugFromFrontmatter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	slug := "my-bare-idea"
	dir := filepath.Join(s.ideasDir, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: My Bare Idea\nstatus: active\ncreated: 2026-03-04T12:00:00Z\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "idea.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	ideas, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ideas) != 1 || ideas[0].Slug != slug {
		t.Fatalf("List = %+v, want [%s]", ideas, slug)
	}
	want := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	if !ideas[0].Created.Equal(want) {
		t.Errorf("Created = %v, want %v (frontmatter)", ideas[0].Created, want)
	}
}

func TestUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	idea := &model.Idea{Name: "Updatable", Status: model.StatusPaused, Summary: "Original body.\n"}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	idea.Status = model.StatusActive
	if err := s.Update(ctx, idea); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.Status != model.StatusActive {
		t.Errorf("Status = %q, want %q", got.Status, model.StatusActive)
	}
	// Body preserved.
	if got.Summary != "Original body.\n" {
		t.Errorf("Summary = %q, want %q", got.Summary, "Original body.\n")
	}
	if got.Updated.IsZero() {
		t.Error("Updated is zero after update")
	}
}

func TestRename(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, dir := newTestStore(t)

	idea := &model.Idea{Name: "Rename Me", Status: model.StatusPaused}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	oldSlug := idea.Slug

	newSlug := model.GenerateSlug("Renamed Idea", time.Now(), false)
	if err := s.Rename(ctx, oldSlug, newSlug); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	// Old dir should not exist.
	if _, err := os.Stat(filepath.Join(dir, oldSlug)); !os.IsNotExist(err) {
		t.Error("old directory still exists")
	}

	// New dir should exist.
	if _, err := os.Stat(filepath.Join(dir, newSlug)); err != nil {
		t.Errorf("new directory not found: %v", err)
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, dir := newTestStore(t)

	idea := &model.Idea{Name: "Delete Me", Status: model.StatusPaused}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.Delete(ctx, idea.Slug, false); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, idea.Slug)); !os.IsNotExist(err) {
		t.Error("directory still exists after delete")
	}
}

func TestHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	idea := &model.Idea{Name: "History Test", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Create already appends a "created" event.
	ev := model.HistoryEvent{
		Timestamp: time.Now(),
		Event:     "status_change",
		Fields:    map[string]any{"from": "thinking", "to": "active"},
	}
	if err := s.AppendHistory(ctx, idea.Slug, ev); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}

	events, err := s.ReadHistory(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	// "created" from Create + our appended event.
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].Event != "created" {
		t.Errorf("first event = %q, want %q", events[0].Event, "created")
	}
	if events[1].Event != "status_change" {
		t.Errorf("second event = %q, want %q", events[1].Event, "status_change")
	}
}

func TestFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	idea := &model.Idea{Name: "Files Test", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Write an additional file.
	content := "---\nresources:\n  - type: doc\n    url: https://example.com\n---\n# Notes\n"
	if err := s.WriteFile(ctx, idea.Slug, "notes.md", content); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// List files (should not include idea.md).
	files, err := s.ListFiles(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 1 || files[0] != "notes.md" {
		t.Errorf("ListFiles = %v, want [notes.md]", files)
	}

	// Read it back.
	got, err := s.ReadFile(ctx, idea.Slug, "notes.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got != content {
		t.Errorf("ReadFile content mismatch")
	}

	// Get should aggregate resources from notes.md.
	gotIdea, err := s.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	found := false
	for _, r := range gotIdea.Resources {
		if r.Type == "doc" && r.URL == "https://example.com" {
			found = true
			break
		}
	}
	if !found {
		t.Error("aggregated resource from notes.md not found in Get result")
	}
}

func TestSlugCollision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	// Phase A: two ideas with different names on the same day both
	// get bare slugs — no date prefix unless the directory name
	// itself collides.
	idea1 := &model.Idea{Name: "First Today", Status: model.StatusPaused}
	if err := s.Create(ctx, idea1); err != nil {
		t.Fatalf("Create idea1: %v", err)
	}
	idea2 := &model.Idea{Name: "Second Today", Status: model.StatusPaused}
	if err := s.Create(ctx, idea2); err != nil {
		t.Fatalf("Create idea2: %v", err)
	}

	if idea1.Slug != "first-today" {
		t.Errorf("idea1 slug = %q, want %q", idea1.Slug, "first-today")
	}
	if idea2.Slug != "second-today" {
		t.Errorf("idea2 slug = %q, want %q", idea2.Slug, "second-today")
	}
}

// Identical names trigger the disambiguation cascade: bare → date →
// date+time. The third copy lands on date+time even when the second
// already used the date-prefixed form.
func TestSlugCollision_SameName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	first := &model.Idea{Name: "Same Name", Status: model.StatusPaused}
	if err := s.Create(ctx, first); err != nil {
		t.Fatalf("Create first: %v", err)
	}
	if first.Slug != "same-name" {
		t.Errorf("first slug = %q, want bare %q", first.Slug, "same-name")
	}

	second := &model.Idea{Name: "Same Name", Status: model.StatusPaused}
	if err := s.Create(ctx, second); err != nil {
		t.Fatalf("Create second: %v", err)
	}
	if !strings.HasSuffix(second.Slug, "-same-name") {
		t.Errorf("second slug = %q, want date-prefixed -same-name", second.Slug)
	}
	if second.Slug == first.Slug {
		t.Errorf("second slug %q must differ from first", second.Slug)
	}
	// Date-only form is YYYY-MM-DD-same-name (20 chars).
	if len(second.Slug) != len("YYYY-MM-DD-same-name") {
		t.Errorf("second slug %q expected date-only form, got len %d", second.Slug, len(second.Slug))
	}

	third := &model.Idea{Name: "Same Name", Status: model.StatusPaused}
	if err := s.Create(ctx, third); err != nil {
		t.Fatalf("Create third: %v", err)
	}
	// Third copy escalates to date+time.
	if len(third.Slug) <= len(second.Slug) {
		t.Errorf("third slug %q should escalate to date+time form, got len %d (second: %d)",
			third.Slug, len(third.Slug), len(second.Slug))
	}
}

// initTestRepo initializes a working repo with one commit on a `main` branch.
// If upstreamDir is non-empty, also inits a bare repo there and configures it
// as `origin`, with `main` pushed.
func initTestRepo(t *testing.T, repoDir, upstreamDir string) {
	t.Helper()
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git -C %s %v: %s: %v", dir, args, out, err)
		}
	}

	run(repoDir, "init", "-b", "main")
	run(repoDir, "config", "user.email", "test@test.com")
	run(repoDir, "config", "user.name", "Test")
	run(repoDir, "config", "commit.gpgsign", "false")

	if err := os.WriteFile(filepath.Join(repoDir, ".gitkeep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	run(repoDir, "add", ".")
	run(repoDir, "commit", "-m", "initial")

	if upstreamDir != "" {
		run(upstreamDir, "init", "--bare", "-b", "main")
		run(repoDir, "remote", "add", "origin", upstreamDir)
		run(repoDir, "push", "-u", "origin", "main")
	}
}

func TestLinkRepoAndListRepos(t *testing.T) {
	t.Parallel()

	// Skip if git is not available.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	ctx := context.Background()
	s, _ := newTestStore(t)

	repoDir := t.TempDir()
	upstreamDir := t.TempDir()
	initTestRepo(t, repoDir, upstreamDir)

	idea := &model.Idea{Name: "Repo Test", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	name, err := s.LinkRepo(ctx, idea.Slug, repoDir, "", "myrepo")
	if err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}
	if name != "myrepo" {
		t.Errorf("LinkRepo returned name = %q, want %q", name, "myrepo")
	}

	repos, err := s.ListRepos(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("ListRepos len = %d, want 1", len(repos))
	}
	if repos[0].Name != "myrepo" {
		t.Errorf("repo name = %q, want %q", repos[0].Name, "myrepo")
	}
	wantRel := filepath.Join("repos", "myrepo")
	if repos[0].Path != wantRel {
		t.Errorf("repo path = %q, want %q", repos[0].Path, wantRel)
	}

	// Verify the worktree directory exists.
	wtDir := filepath.Join(s.ideaDir(idea.Slug), "repos", "myrepo")
	fi, err := os.Stat(wtDir)
	if err != nil {
		t.Fatalf("worktree dir not found: %v", err)
	}
	if !fi.IsDir() {
		t.Error("worktree path is not a directory")
	}

	// Verify the slug worktree branch has upstream origin/main.
	branchName := "pt/" + idea.Slug
	out, err := exec.Command("git", "-C", wtDir, "for-each-ref",
		"--format=%(upstream:short)", "refs/heads/"+branchName).CombinedOutput()
	if err != nil {
		t.Fatalf("for-each-ref: %s: %v", out, err)
	}
	if got := strings.TrimSpace(string(out)); got != "origin/main" {
		t.Errorf("upstream = %q, want %q", got, "origin/main")
	}
}

// TestLinkRepoNoTrackingRef verifies that LinkRepo succeeds (with no upstream
// configured) when the configured tracking branch does not exist in the repo.
func TestLinkRepoNoTrackingRef(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	ctx := context.Background()
	s, _ := newTestStore(t)

	repoDir := t.TempDir()
	initTestRepo(t, repoDir, "") // no origin remote

	idea := &model.Idea{Name: "No Origin", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.LinkRepo(ctx, idea.Slug, repoDir, "", "myrepo"); err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}

	// Worktree branch exists but has no upstream.
	wtDir := filepath.Join(s.ideaDir(idea.Slug), "repos", "myrepo")
	branchName := "pt/" + idea.Slug
	out, err := exec.Command("git", "-C", wtDir, "for-each-ref",
		"--format=%(upstream:short)", "refs/heads/"+branchName).CombinedOutput()
	if err != nil {
		t.Fatalf("for-each-ref: %s: %v", out, err)
	}
	if got := strings.TrimSpace(string(out)); got != "" {
		t.Errorf("upstream = %q, want empty (origin/main missing)", got)
	}
}

func TestLinkRepoAutoDeriveName(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	ctx := context.Background()
	s, _ := newTestStore(t)

	repoDir := newTestGitRepo(t)
	mustGitCmd(t, repoDir, "remote", "add", "origin", "https://github.com/example/widget.git")

	idea := &model.Idea{Name: "Auto-name", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	name, err := s.LinkRepo(ctx, idea.Slug, repoDir, "", "")
	if err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}
	if name != "widget" {
		t.Errorf("auto-derived name = %q, want %q", name, "widget")
	}
}

func TestLinkRepoExplicitBranch(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	ctx := context.Background()
	s, _ := newTestStore(t)
	repoDir := newTestGitRepo(t)

	idea := &model.Idea{Name: "Branch Test", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := s.LinkRepo(ctx, idea.Slug, repoDir, "feature-x", "myrepo"); err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}

	repos, err := s.ListRepos(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 || repos[0].Branch != "feature-x" {
		t.Errorf("expected single repo on branch feature-x, got %+v", repos)
	}
}

func TestLinkRepoCollisionRefused(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	ctx := context.Background()
	s, _ := newTestStore(t)
	repoDir := newTestGitRepo(t)

	idea := &model.Idea{Name: "Collision", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := s.LinkRepo(ctx, idea.Slug, repoDir, "", "myrepo"); err != nil {
		t.Fatalf("first LinkRepo: %v", err)
	}
	if _, err := s.LinkRepo(ctx, idea.Slug, repoDir, "", "myrepo"); err == nil {
		t.Errorf("second LinkRepo with same name should fail")
	}
}

func TestUnlinkRepo(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	ctx := context.Background()
	s, _ := newTestStore(t)
	repoDir := newTestGitRepo(t)

	idea := &model.Idea{Name: "Unlink", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.LinkRepo(ctx, idea.Slug, repoDir, "", "myrepo"); err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}

	wt := filepath.Join(s.ideaDir(idea.Slug), "repos", "myrepo")

	// Make worktree dirty.
	if err := os.WriteFile(filepath.Join(wt, "extra.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitCmd(t, wt, "add", "extra.txt")

	// Without force: refused.
	err := s.UnlinkRepo(ctx, idea.Slug, "myrepo", false)
	if err == nil {
		t.Errorf("UnlinkRepo on dirty worktree without force should fail")
	}
	var dirty *ErrDirtyRepos
	if !errors.As(err, &dirty) {
		t.Errorf("expected *ErrDirtyRepos, got %T: %v", err, err)
	}

	// With force: succeeds.
	if err := s.UnlinkRepo(ctx, idea.Slug, "myrepo", true); err != nil {
		t.Errorf("UnlinkRepo force: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree should be gone, stat err = %v", err)
	}
}

func TestDeleteIdeaDirtyRepoRefused(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	ctx := context.Background()
	s, _ := newTestStore(t)
	repoDir := newTestGitRepo(t)

	idea := &model.Idea{Name: "Delete Dirty", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.LinkRepo(ctx, idea.Slug, repoDir, "", "myrepo"); err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}

	wt := filepath.Join(s.ideaDir(idea.Slug), "repos", "myrepo")
	if err := os.WriteFile(filepath.Join(wt, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitCmd(t, wt, "add", "x.txt")

	err := s.Delete(ctx, idea.Slug, false)
	var dirty *ErrDirtyRepos
	if !errors.As(err, &dirty) {
		t.Fatalf("expected *ErrDirtyRepos, got %T: %v", err, err)
	}
	if len(dirty.Repos) != 1 || dirty.Repos[0] != "myrepo" {
		t.Errorf("ErrDirtyRepos repos = %v, want [myrepo]", dirty.Repos)
	}

	// Force succeeds.
	if err := s.Delete(ctx, idea.Slug, true); err != nil {
		t.Errorf("Delete force: %v", err)
	}
}

func newTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		mustGitCmd(t, dir, args...)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitCmd(t, dir, "add", ".")
	mustGitCmd(t, dir, "commit", "-m", "initial")
	return dir
}

func mustGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

func TestSessionCRUD(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	idea := &model.Idea{Name: "Session Test", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	session := model.AgentSession{
		UUID:       "uuid-1111",
		Agent:      "claude-code",
		Status:     model.SessionStatusRunning,
		Started:    time.Now(),
		WorkingDir: "/tmp/repos/pipeline",
		RepoName:   "pipeline",
	}

	// Write using UUID as key.
	if err := s.WriteSession(ctx, idea.Slug, session.UUID, session); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	// Read.
	got, err := s.ReadSession(ctx, idea.Slug, session.UUID)
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if got.UUID != session.UUID {
		t.Errorf("UUID = %q, want %q", got.UUID, session.UUID)
	}
	if got.Agent != "claude-code" {
		t.Errorf("Agent = %q, want %q", got.Agent, "claude-code")
	}

	// Update.
	now := time.Now()
	session.Status = model.SessionStatusCompleted
	session.Ended = &now
	session.Outcome = "Created PR #89"
	if err := s.UpdateSession(ctx, idea.Slug, session.UUID, session); err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}

	got, err = s.ReadSession(ctx, idea.Slug, session.UUID)
	if err != nil {
		t.Fatalf("ReadSession after update: %v", err)
	}
	if got.Status != model.SessionStatusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, model.SessionStatusCompleted)
	}
	if got.Outcome != "Created PR #89" {
		t.Errorf("Outcome = %q, want %q", got.Outcome, "Created PR #89")
	}
	if got.Ended == nil {
		t.Error("Ended is nil after update")
	}

	// List.
	session2 := model.AgentSession{
		UUID:       "uuid-2222",
		Agent:      "testagent",
		Status:     model.SessionStatusRunning,
		Started:    time.Now().Add(time.Second),
		WorkingDir: "/tmp/repos/web-ui",
		RepoName:   "web-ui",
	}
	if err := s.WriteSession(ctx, idea.Slug, session2.UUID, session2); err != nil {
		t.Fatalf("WriteSession second: %v", err)
	}

	sessions, err := s.ListSessions(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("ListSessions len = %d, want 2", len(sessions))
	}
	// Most recent first.
	if sessions[0].UUID != "uuid-2222" {
		t.Errorf("first session = %q, want %q", sessions[0].UUID, "uuid-2222")
	}
}

func TestTouchIdea(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	idea := &model.Idea{Name: "Touchable", Status: model.StatusActive, Summary: "Body.\n"}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	before, err := s.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get before: %v", err)
	}

	// Wait long enough that filesystem timestamps differ.
	time.Sleep(10 * time.Millisecond)

	updated, err := s.TouchIdea(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("TouchIdea: %v", err)
	}
	if !updated.After(before.Updated) {
		t.Errorf("returned Updated %v not after original %v", updated, before.Updated)
	}

	after, err := s.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get after: %v", err)
	}
	if !after.Updated.After(before.Updated) {
		t.Errorf("persisted Updated %v not after original %v", after.Updated, before.Updated)
	}
	if after.Summary != before.Summary {
		t.Errorf("Summary changed: %q -> %q", before.Summary, after.Summary)
	}
	if after.Status != before.Status {
		t.Errorf("Status changed: %q -> %q", before.Status, after.Status)
	}

	// TouchIdea must NOT append a history event (would pollute history with hook noise).
	history, err := s.ReadHistory(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	for _, e := range history {
		if e.Event == "updated" {
			t.Errorf("TouchIdea wrote unwanted history event: %+v", e)
		}
	}
}

func TestTouchIdeaMissing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	// TouchIdea is best-effort: synthetic slugs (e.g. OrchestratorSlug)
	// own a sessions/ subdir but no idea.md, and a session write that
	// routes through WriteSession should not error just because there's
	// no idea.md to bump. Returns zero time + nil error in that case.
	got, err := s.TouchIdea(ctx, "nonexistent-slug")
	if err != nil {
		t.Errorf("TouchIdea on missing slug returned err = %v, want nil", err)
	}
	if !got.IsZero() {
		t.Errorf("TouchIdea returned %v, want zero time", got)
	}
}

func TestWriteSessionTouchesIdea(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	idea := &model.Idea{Name: "Session Touch", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	before, err := s.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get before: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	session := model.AgentSession{
		UUID:    "uuid-touch",
		Agent:   "claude-code",
		Status:  model.SessionStatusRunning,
		Started: time.Now(),
	}
	if err := s.WriteSession(ctx, idea.Slug, session.UUID, session); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	after, err := s.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get after: %v", err)
	}
	if !after.Updated.After(before.Updated) {
		t.Errorf("Idea.Updated not bumped: before=%v after=%v", before.Updated, after.Updated)
	}
}

func TestFindRunningSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	idea := &model.Idea{Name: "Find Running", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// No sessions yet.
	got, err := s.FindRunningSession(ctx, idea.Slug, "claude-code")
	if err != nil {
		t.Fatalf("FindRunningSession (empty): %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for empty idea, got %+v", got)
	}

	// Mix of completed (claude), running (testagent), running (claude).
	now := time.Now()
	completed := model.AgentSession{
		UUID: "completed-claude", Agent: "claude-code",
		Status: model.SessionStatusCompleted, Started: now.Add(-2 * time.Hour),
	}
	runningTest := model.AgentSession{
		UUID: "running-test", Agent: "testagent",
		Status: model.SessionStatusRunning, Started: now.Add(-1 * time.Hour),
	}
	runningClaude := model.AgentSession{
		UUID: "running-claude", Agent: "claude-code",
		Status: model.SessionStatusRunning, Started: now,
	}
	for _, sess := range []model.AgentSession{completed, runningTest, runningClaude} {
		if err := s.WriteSession(ctx, idea.Slug, sess.UUID, sess); err != nil {
			t.Fatalf("WriteSession %s: %v", sess.UUID, err)
		}
	}

	// Should pick the running claude one, not the completed one and not the testagent one.
	got, err = s.FindRunningSession(ctx, idea.Slug, "claude-code")
	if err != nil {
		t.Fatalf("FindRunningSession (claude): %v", err)
	}
	if got == nil {
		t.Fatal("FindRunningSession returned nil for claude-code, expected running-claude")
	}
	if got.UUID != "running-claude" {
		t.Errorf("got UUID %q, want %q", got.UUID, "running-claude")
	}

	// Testagent lookup returns the testagent session.
	got, err = s.FindRunningSession(ctx, idea.Slug, "testagent")
	if err != nil {
		t.Fatalf("FindRunningSession (testagent): %v", err)
	}
	if got == nil || got.UUID != "running-test" {
		t.Errorf("testagent: got %+v, want UUID running-test", got)
	}

	// Unknown agent type returns nil.
	got, err = s.FindRunningSession(ctx, idea.Slug, "codex")
	if err != nil {
		t.Fatalf("FindRunningSession (codex): %v", err)
	}
	if got != nil {
		t.Errorf("codex: got %+v, want nil", got)
	}
}

func TestAgentSessionRoundTripWithNewFields(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	idea := &model.Idea{Name: "RoundTrip", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	cases := []struct {
		name    string
		session model.AgentSession
	}{
		{
			name: "running-active",
			session: model.AgentSession{
				UUID: "ra", Agent: "claude-code",
				Status: model.SessionStatusRunning, Activity: model.SessionActivityActive,
				Started: time.Now(),
			},
		},
		{
			name: "running-waiting",
			session: model.AgentSession{
				UUID: "rw", Agent: "claude-code",
				Status: model.SessionStatusRunning, Activity: model.SessionActivityWaiting,
				Started: time.Now(),
			},
		},
		{
			name: "stopped-shutdown",
			session: model.AgentSession{
				UUID: "ss", Agent: "claude-code",
				Status: model.SessionStatusStopped, StopReason: model.SessionStopReasonShutdown,
				Started: time.Now(),
			},
		},
		{
			name: "stopped-crash",
			session: model.AgentSession{
				UUID: "sc", Agent: "claude-code",
				Status: model.SessionStatusStopped, StopReason: model.SessionStopReasonCrash,
				Started: time.Now(),
			},
		},
		{
			name: "completed-exit",
			session: model.AgentSession{
				UUID: "ce", Agent: "claude-code",
				Status: model.SessionStatusCompleted, StopReason: model.SessionStopReasonExit,
				Started: time.Now(),
			},
		},
		{
			name: "dormant",
			session: model.AgentSession{
				UUID: "dm", Agent: "claude-code",
				Status: model.SessionStatusDormant, StopReason: model.SessionStopReasonUser,
				Started: time.Now(),
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if err := s.WriteSession(ctx, idea.Slug, tc.session.UUID, tc.session); err != nil {
				t.Fatalf("WriteSession: %v", err)
			}
			got, err := s.ReadSession(ctx, idea.Slug, tc.session.UUID)
			if err != nil {
				t.Fatalf("ReadSession: %v", err)
			}
			if got.Activity != tc.session.Activity {
				t.Errorf("Activity = %q, want %q", got.Activity, tc.session.Activity)
			}
			if got.StopReason != tc.session.StopReason {
				t.Errorf("StopReason = %q, want %q", got.StopReason, tc.session.StopReason)
			}
			if got.Status != tc.session.Status {
				t.Errorf("Status = %q, want %q (read-repair changed it)", got.Status, tc.session.Status)
			}
		})
	}
}

func TestReadSession_RepairUnknownStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, ideasDir := newTestStore(t)

	idea := &model.Idea{Name: "Repair", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	sessionsDirPath := filepath.Join(ideasDir, idea.Slug, "sessions")
	if err := os.MkdirAll(sessionsDirPath, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	raw := `{"uuid":"x","agent":"claude-code","status":"bogus","working_dir":"/tmp","started":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(sessionsDirPath, "x.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := s.ReadSession(ctx, idea.Slug, "x")
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if got.Status != model.SessionStatusRunning {
		t.Errorf("Status = %q, want %q (unknown should repair to running)", got.Status, model.SessionStatusRunning)
	}
}

func TestListSessionSummaries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	ideaA := &model.Idea{Name: "Alpha", Status: model.StatusActive}
	if err := s.Create(ctx, ideaA); err != nil {
		t.Fatalf("Create A: %v", err)
	}
	ideaB := &model.Idea{Name: "Bravo", Status: model.StatusActive}
	if err := s.Create(ctx, ideaB); err != nil {
		t.Fatalf("Create B: %v", err)
	}
	ideaC := &model.Idea{Name: "Charlie", Status: model.StatusActive}
	if err := s.Create(ctx, ideaC); err != nil {
		t.Fatalf("Create C: %v", err)
	}

	// A: one running claude (active), one running testagent (idle).
	for _, sess := range []model.AgentSession{
		{UUID: "a-claude", Agent: "claude-code", Status: model.SessionStatusRunning, Activity: model.SessionActivityActive, Started: time.Now()},
		{UUID: "a-test", Agent: "testagent", Status: model.SessionStatusRunning, Started: time.Now().Add(-time.Hour)},
	} {
		if err := s.WriteSession(ctx, ideaA.Slug, sess.UUID, sess); err != nil {
			t.Fatalf("WriteSession A: %v", err)
		}
	}

	// B: only completed sessions.
	for _, sess := range []model.AgentSession{
		{UUID: "b-old", Agent: "claude-code", Status: model.SessionStatusCompleted, Started: time.Now().Add(-2 * time.Hour)},
		{UUID: "b-newer", Agent: "claude-code", Status: model.SessionStatusCompleted, Started: time.Now().Add(-1 * time.Hour)},
	} {
		if err := s.WriteSession(ctx, ideaB.Slug, sess.UUID, sess); err != nil {
			t.Fatalf("WriteSession B: %v", err)
		}
	}

	// C: no sessions.

	summaries, err := s.ListSessionSummaries(ctx)
	if err != nil {
		t.Fatalf("ListSessionSummaries: %v", err)
	}
	bySlug := make(map[string]IdeaSessionSummary, len(summaries))
	for _, sm := range summaries {
		bySlug[sm.Slug] = sm
	}

	a := bySlug[ideaA.Slug]
	if a.RunningCount != 2 {
		t.Errorf("A.RunningCount = %d, want 2", a.RunningCount)
	}
	if len(a.ByAgent) != 2 {
		t.Errorf("A.ByAgent = %v; want both claude + testagent keys", a.ByAgent)
	}
	if a.ByAgent["claude-code"] != model.SessionActivityActive {
		t.Errorf("A claude activity = %q, want active", a.ByAgent["claude-code"])
	}
	if a.ByAgent["testagent"] != model.SessionActivityIdle {
		t.Errorf("A testagent activity = %q, want idle (default for empty)", a.ByAgent["testagent"])
	}

	b := bySlug[ideaB.Slug]
	if b.RunningCount != 0 {
		t.Errorf("B.RunningCount = %d, want 0", b.RunningCount)
	}
	if b.MostRecent == nil || b.MostRecent.UUID != "b-newer" {
		t.Errorf("B.MostRecent = %+v, want b-newer", b.MostRecent)
	}

	c := bySlug[ideaC.Slug]
	if c.RunningCount != 0 || c.MostRecent != nil {
		t.Errorf("C should be empty summary, got %+v", c)
	}
}

func TestListSessionsEmptyIdea(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	idea := &model.Idea{Name: "No Sessions", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	sessions, err := s.ListSessions(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected empty sessions, got %d", len(sessions))
	}
}

func TestConfigLoadSave(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Load from nonexistent file returns defaults.
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.BranchPrefix != "" {
		t.Errorf("default BranchPrefix = %q, want empty", cfg.BranchPrefix)
	}
	if cfg.TrackingBranch != "" {
		t.Errorf("default TrackingBranch = %q, want empty", cfg.TrackingBranch)
	}

	// Save and reload.
	cfg.BranchPrefix = "pt/"
	cfg.TrackingBranch = "upstream/master"
	cfg.Agents.Claude.AddDirs = []string{"~/.claude/skills", "/abs/path"}
	cfg.Agents.Claude.ExtraArgs = []string{"--debug", "--model", "claude-opus-4-7"}
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	cfg2, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig after save: %v", err)
	}
	if cfg2.BranchPrefix != "pt/" {
		t.Errorf("BranchPrefix = %q, want %q", cfg2.BranchPrefix, "pt/")
	}
	if cfg2.TrackingBranch != "upstream/master" {
		t.Errorf("TrackingBranch = %q, want %q", cfg2.TrackingBranch, "upstream/master")
	}
	if got, want := cfg2.Agents.Claude.AddDirs, []string{"~/.claude/skills", "/abs/path"}; !slices.Equal(got, want) {
		t.Errorf("Agents.Claude.AddDirs = %v, want %v", got, want)
	}
	if got, want := cfg2.Agents.Claude.ExtraArgs, []string{"--debug", "--model", "claude-opus-4-7"}; !slices.Equal(got, want) {
		t.Errorf("Agents.Claude.ExtraArgs = %v, want %v", got, want)
	}
}

func TestReloadConfig_HotSwapsBranchPrefixAndTracking(t *testing.T) {
	t.Parallel()

	ideasDir := t.TempDir()
	reviewsDir := filepath.Join(t.TempDir(), "reviews")
	s := NewFSStore(ideasDir, reviewsDir, "old/", "origin/main")

	// Sanity: initial values land.
	if got := s.branchPrefixVal(); got != "old/" {
		t.Fatalf("initial branch prefix = %q, want old/", got)
	}
	if got := s.trackingBranchVal(); got != "origin/main" {
		t.Fatalf("initial tracking branch = %q, want origin/main", got)
	}

	// Write a new config and reload.
	if err := SaveConfig(ideasDir, &Config{BranchPrefix: "new/", TrackingBranch: "upstream/master"}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := s.ReloadConfig(); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	if got := s.branchPrefixVal(); got != "new/" {
		t.Errorf("post-reload branch prefix = %q, want new/", got)
	}
	if got := s.trackingBranchVal(); got != "upstream/master" {
		t.Errorf("post-reload tracking branch = %q, want upstream/master", got)
	}

	// Empty tracking branch → default fills in (matches NewFSStore semantics).
	if err := SaveConfig(ideasDir, &Config{BranchPrefix: ""}); err != nil {
		t.Fatalf("SaveConfig empty: %v", err)
	}
	if err := s.ReloadConfig(); err != nil {
		t.Fatalf("ReloadConfig empty: %v", err)
	}
	if got := s.branchPrefixVal(); got != "" {
		t.Errorf("post-empty-reload branch prefix = %q, want empty", got)
	}
	if got := s.trackingBranchVal(); got != defaultTrackingBranch {
		t.Errorf("post-empty-reload tracking branch = %q, want %q", got, defaultTrackingBranch)
	}
}

func TestSetAndClearSessionReview(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	idea := &model.Idea{Name: "Reviewing", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	sess := model.AgentSession{
		UUID:    "uuid-1",
		Agent:   "claude-code",
		Status:  model.SessionStatusRunning,
		Started: time.Now(),
	}
	if err := s.WriteSession(ctx, idea.Slug, sess.UUID, sess); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	// Setting transitions Activity to reviewing.
	if err := s.SetSessionReviewActive(ctx, idea.Slug, sess.UUID, "rev-abc"); err != nil {
		t.Fatalf("SetSessionReviewActive: %v", err)
	}
	got, _ := s.ReadSession(ctx, idea.Slug, sess.UUID)
	if got.ActiveReviewID != "rev-abc" {
		t.Errorf("ActiveReviewID = %q, want %q", got.ActiveReviewID, "rev-abc")
	}
	if got.Activity != model.SessionActivityReviewing {
		t.Errorf("Activity = %q, want reviewing", got.Activity)
	}

	// Clearing drops Activity from reviewing back to active.
	if err := s.ClearSessionReview(ctx, idea.Slug, sess.UUID); err != nil {
		t.Fatalf("ClearSessionReview: %v", err)
	}
	got, _ = s.ReadSession(ctx, idea.Slug, sess.UUID)
	if got.ActiveReviewID != "" {
		t.Errorf("ActiveReviewID = %q, want empty", got.ActiveReviewID)
	}
	if got.Activity != model.SessionActivityActive {
		t.Errorf("Activity = %q, want active", got.Activity)
	}
}

// SetSessionReviewActive must reject sessions that aren't running — sync /
// auto-resume can't have a review active because the agent isn't bound.
func TestSetSessionReviewActive_RejectsNonRunning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	idea := &model.Idea{Name: "X", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	sess := model.AgentSession{
		UUID:    "uuid-stopped",
		Agent:   "claude-code",
		Status:  model.SessionStatusStopped,
		Started: time.Now(),
	}
	if err := s.WriteSession(ctx, idea.Slug, sess.UUID, sess); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	err := s.SetSessionReviewActive(ctx, idea.Slug, sess.UUID, "rev-stale")
	if err == nil {
		t.Fatal("expected error setting reviewing on stopped session; got nil")
	}
}
