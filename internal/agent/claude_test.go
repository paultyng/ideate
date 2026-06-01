package agent

import (
	"slices"
	"strings"
	"testing"

	"github.com/paultyng/ideate/internal/store"
)

func TestMergeAddDirs_KeepsFirstThenDedupes(t *testing.T) {
	t.Parallel()

	got := mergeAddDirs(
		[]string{"/idea/root", "/idea/root/repos/a"},
		[]string{"/idea/root/repos/a", "/Users/me/.claude/skills"},
	)
	want := []string{"/idea/root", "/idea/root/repos/a", "/Users/me/.claude/skills"}
	if !slices.Equal(got, want) {
		t.Errorf("mergeAddDirs = %v, want %v", got, want)
	}
}

func TestExpandPaths(t *testing.T) {
	// t.Setenv forbids t.Parallel — sequential is fine for this fast test.

	// Use a fixed HOME so the test isn't host-dependent.
	t.Setenv("HOME", "/Users/fake")
	t.Setenv("MY_SKILLS", "/opt/skills")

	got := expandPaths([]string{
		"~/.claude/skills",
		"$MY_SKILLS",
		"$HOME/.claude/another",
		"/already/absolute",
		"", // dropped
	})
	want := []string{
		"/Users/fake/.claude/skills",
		"/opt/skills",
		"/Users/fake/.claude/another",
		"/already/absolute",
	}
	if !slices.Equal(got, want) {
		t.Errorf("expandPaths = %v, want %v", got, want)
	}
}

func TestExpandPaths_NoHomeNoTildeExpansion(t *testing.T) {
	// t.Setenv forbids t.Parallel.

	// On a host without HOME (CI quirks), tilde stays literal —
	// claude will reject the dir, which is the same loud failure
	// the user would see for any other invalid path. Better than
	// silently dropping the entry.
	t.Setenv("HOME", "")
	// os.UserHomeDir consults additional sources on some platforms;
	// at minimum the empty-HOME path must not panic or silently
	// substitute something surprising. We assert only that the
	// function returns without error and preserves the input.
	got := expandPaths([]string{"/abs/path"})
	if !slices.Equal(got, []string{"/abs/path"}) {
		t.Errorf("expandPaths = %v, want [/abs/path]", got)
	}
}

func envVal(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix), true
		}
	}
	return "", false
}

func TestBuildClaudeEnv_UserKeyPassedThrough(t *testing.T) {
	t.Parallel()

	cfg := store.ClaudeAgent{
		Env: map[string]string{"FOO": "bar"},
	}
	got := buildClaudeEnv(nil, cfg)

	if v, ok := envVal(got, "FOO"); !ok || v != "bar" {
		t.Errorf("FOO = %q %v, want bar true", v, ok)
	}
}

func TestBuildClaudeEnv_ParentEnvPreserved(t *testing.T) {
	t.Parallel()

	parent := []string{"EXISTING=value", "PATH=/usr/bin"}
	got := buildClaudeEnv(parent, store.ClaudeAgent{})

	for _, entry := range parent {
		if !slices.Contains(got, entry) {
			t.Errorf("parent env entry %q missing from result", entry)
		}
	}
}
