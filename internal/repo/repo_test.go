package repo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeriveName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		origin    string
		canonical string
		want      string
	}{
		{"ssh github", "git@github.com:paultyng/ideate.git", "/tmp/x", "ideate"},
		{"https github", "https://github.com/paultyng/ideate.git", "/tmp/x", "ideate"},
		{"https no .git", "https://github.com/paultyng/ideate", "/tmp/x", "ideate"},
		{"ssh proto", "ssh://git@github.com/paultyng/ideate.git", "/tmp/x", "ideate"},
		{"trailing slash", "https://github.com/paultyng/ideate/", "/tmp/x", "ideate"},
		{"local path origin", "/srv/git/foo.git", "/tmp/x", "foo"},
		{"empty origin falls back", "", "/Users/me/src/github.com/paultyng/ideate", "ideate"},
		{"weird chars get slugified", "https://example.com/My_Project.v2.git", "/tmp/x", "my-project-v2"},
		{"no path origin falls back", "", "/repo-with-CAPS_AND-stuff", "repo-with-caps-and-stuff"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DeriveName(tc.origin, tc.canonical)
			if got != tc.want {
				t.Errorf("DeriveName(%q, %q) = %q, want %q", tc.origin, tc.canonical, got, tc.want)
			}
		})
	}
}

func TestAddRemoveWorktree(t *testing.T) {
	t.Parallel()
	requireGit(t)

	ctx := context.Background()
	canonical := setupGitRepo(t)
	worktree := filepath.Join(t.TempDir(), "wt")

	if err := AddWorktree(ctx, canonical, worktree, "feature-1"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if fi, err := os.Stat(worktree); err != nil || !fi.IsDir() {
		t.Fatalf("worktree dir missing: %v", err)
	}

	branch, err := branchName(ctx, worktree)
	if err != nil {
		t.Fatalf("branchName: %v", err)
	}
	if branch != "feature-1" {
		t.Errorf("branch = %q, want %q", branch, "feature-1")
	}

	if err := RemoveWorktree(ctx, worktree, false); err != nil {
		t.Fatalf("RemoveWorktree clean: %v", err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("worktree should be removed; stat err = %v", err)
	}
}

func TestAddWorktreeExistingBranch(t *testing.T) {
	t.Parallel()
	requireGit(t)

	ctx := context.Background()
	canonical := setupGitRepo(t)

	// Create a branch in canonical first.
	mustGit(t, canonical, "branch", "preexisting")

	worktree := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktree(ctx, canonical, worktree, "preexisting"); err != nil {
		t.Fatalf("AddWorktree on existing branch: %v", err)
	}

	branch, err := branchName(ctx, worktree)
	if err != nil {
		t.Fatalf("branchName: %v", err)
	}
	if branch != "preexisting" {
		t.Errorf("branch = %q, want %q", branch, "preexisting")
	}
}

func TestRemoveWorktreeDirty(t *testing.T) {
	t.Parallel()
	requireGit(t)

	ctx := context.Background()
	canonical := setupGitRepo(t)
	worktree := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktree(ctx, canonical, worktree, "dirty-test"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	// Make the worktree dirty.
	if err := os.WriteFile(filepath.Join(worktree, "extra.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, worktree, "add", "extra.txt")

	// Without force: should fail.
	if err := RemoveWorktree(ctx, worktree, false); err == nil {
		t.Errorf("RemoveWorktree without force on dirty worktree should fail")
	}

	// With force: should succeed.
	if err := RemoveWorktree(ctx, worktree, true); err != nil {
		t.Errorf("RemoveWorktree with force on dirty worktree: %v", err)
	}
}

func TestStatus(t *testing.T) {
	t.Parallel()
	requireGit(t)

	ctx := context.Background()
	canonical := setupGitRepo(t)
	worktree := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktree(ctx, canonical, worktree, "status-test"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	// Clean.
	s, err := ReadStatus(ctx, worktree)
	if err != nil {
		t.Fatalf("Status clean: %v", err)
	}
	if s.Branch != "status-test" {
		t.Errorf("clean: branch = %q, want %q", s.Branch, "status-test")
	}
	if s.Dirty {
		t.Error("clean: Dirty should be false")
	}
	if s.Ahead != 0 || s.Behind != 0 {
		t.Errorf("clean (no upstream): ahead/behind = %d/%d, want 0/0", s.Ahead, s.Behind)
	}

	// Dirty (untracked).
	if err := os.WriteFile(filepath.Join(worktree, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err = ReadStatus(ctx, worktree)
	if err != nil {
		t.Fatalf("ReadStatus dirty: %v", err)
	}
	if !s.Dirty {
		t.Error("dirty: Dirty should be true after writing untracked file")
	}
}

func TestCanonical(t *testing.T) {
	t.Parallel()
	requireGit(t)

	ctx := context.Background()
	canonical := setupGitRepo(t)
	worktree := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktree(ctx, canonical, worktree, "canonical-test"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	got, err := Canonical(ctx, worktree)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	wantAbs, _ := filepath.Abs(canonical)
	wantAbs = filepath.Clean(wantAbs)
	if real, err := filepath.EvalSymlinks(got); err == nil {
		got = real
	}
	if real, err := filepath.EvalSymlinks(wantAbs); err == nil {
		wantAbs = real
	}
	if got != wantAbs {
		t.Errorf("Canonical = %q, want %q", got, wantAbs)
	}
}

func TestOriginURL(t *testing.T) {
	t.Parallel()
	requireGit(t)

	ctx := context.Background()
	canonical := setupGitRepo(t)

	// No origin yet.
	url, err := OriginURL(ctx, canonical)
	if err != nil {
		t.Fatalf("OriginURL no origin: %v", err)
	}
	if url != "" {
		t.Errorf("OriginURL with no origin = %q, want empty", url)
	}

	// Add an origin.
	mustGit(t, canonical, "remote", "add", "origin", "https://github.com/example/repo.git")
	url, err = OriginURL(ctx, canonical)
	if err != nil {
		t.Fatalf("OriginURL: %v", err)
	}
	if url != "https://github.com/example/repo.git" {
		t.Errorf("OriginURL = %q, want the configured URL", url)
	}
}

func TestWorktreeAdminDir(t *testing.T) {
	t.Parallel()
	got := WorktreeAdminDir("/srv/canonical", "leaf")
	want := filepath.Join("/srv/canonical", ".git", "worktrees", "leaf")
	if got != want {
		t.Errorf("WorktreeAdminDir = %q, want %q", got, want)
	}
}

// setupGitRepo initializes a git repo in a fresh temp dir with one commit on
// `main`. Returns the absolute repo path.
func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		mustGit(t, dir, args...)
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "initial")
	return dir
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), out, err)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}
