package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/paultyng/ideate/internal/model"
)

func mkRepoEntry(t *testing.T, reposDir, name, gitContent string) {
	t.Helper()
	dir := filepath.Join(reposDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if gitContent != "" {
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(gitContent), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func primaries(t *testing.T, reposDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(reposDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range primaryRepoDirs(reposDir, entries) {
		out = append(out, filepath.Base(d))
	}
	return out
}

func wt(common, name string) string {
	return "gitdir: " + filepath.Join(common, ".git", "worktrees", name) + "\n"
}

func TestIdeaGetter_OnePrimaryPerRepo(t *testing.T) {
	repos := t.TempDir()
	a, b := "/src/alpha", "/src/beta"
	mkRepoEntry(t, repos, "alpha-feat", wt(a, "alpha-feat"))
	mkRepoEntry(t, repos, "alpha", wt(a, "alpha"))
	mkRepoEntry(t, repos, "alpha-fix", wt(a, "alpha-fix"))
	mkRepoEntry(t, repos, "beta-x", wt(b, "beta-x"))
	if got, want := primaries(t, repos), []string{"alpha", "beta-x"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestIdeaGetter_LexicalWhenNoNameMatch(t *testing.T) {
	repos := t.TempDir()
	mkRepoEntry(t, repos, "z-wt", wt("/src/alpha", "z-wt"))
	mkRepoEntry(t, repos, "b-wt", wt("/src/alpha", "b-wt"))
	if got, want := primaries(t, repos), []string{"b-wt"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestIdeaGetter_CloneIsOwnGroup(t *testing.T) {
	repos := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repos, "clone", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkRepoEntry(t, repos, "wt", wt("/src/alpha", "wt"))
	if got, want := primaries(t, repos), []string{"clone", "wt"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestIdeaGetter_UnreadableGitFallsBackToOwnGroup(t *testing.T) {
	repos := t.TempDir()
	mkRepoEntry(t, repos, "garbled", "not a gitdir line\n")
	mkRepoEntry(t, repos, "nogit", "")
	mkRepoEntry(t, repos, "relative", "gitdir: ../elsewhere\n")
	mkRepoEntry(t, repos, "wt", wt("/src/alpha", "wt"))
	if got, want := primaries(t, repos), []string{"garbled", "nogit", "relative", "wt"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestIdeaGetter_IdeaRootFirst(t *testing.T) {
	a, ideasDir := newSessionTestApp(t)
	st := a.store
	idea := &model.Idea{Name: "Getter", Status: model.StatusActive}
	if err := st.Create(a.ctx, idea); err != nil {
		t.Fatal(err)
	}
	slug := idea.Slug
	repos := filepath.Join(ideasDir, slug, "repos")
	mkRepoEntry(t, repos, "a1", wt("/src/alpha", "a1"))
	mkRepoEntry(t, repos, "a2", wt("/src/alpha", "a2"))
	ic, err := newIdeaGetter(st, ideasDir)(slug)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(ideasDir, slug), filepath.Join(repos, "a1")}
	if !slices.Equal(ic.AddDirs, want) {
		t.Fatalf("got %v, want %v", ic.AddDirs, want)
	}
}
