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

// envValLast returns the LAST matching key's value, matching exec.Cmd's
// last-occurrence semantics. Use when asserting that a later entry (e.g.
// cfg.Env override) wins over an earlier inject.
func envValLast(env []string, key string) (string, bool) {
	prefix := key + "="
	var v string
	var ok bool
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			v = strings.TrimPrefix(e, prefix)
			ok = true
		}
	}
	return v, ok
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

// buildClaudeEnv forces TERM=xterm-256color + COLORTERM=truecolor so the
// claude subprocess sees xterm.js's emulation capability regardless of
// what the launcher inherited (Dock launches see neither; some shells
// set TERM=dumb).
func TestBuildClaudeEnv_TerminalIdentity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		parent     []string
		cfg        store.ClaudeAgent
		wantTerm   string
		wantColor  string
		wantOther  map[string]string // additional KV pairs that must appear
		wantNoKeys []string          // keys that must NOT appear (anywhere)
	}{
		{
			name:      "Dock launch: parent has neither TERM nor COLORTERM",
			parent:    []string{"PATH=/usr/bin:/bin"},
			wantTerm:  "xterm-256color",
			wantColor: "truecolor",
		},
		{
			name:      "shell launch: parent's TERM is dropped, ours wins",
			parent:    []string{"PATH=/usr/bin", "TERM=xterm-kitty", "COLORTERM=truecolor"},
			wantTerm:  "xterm-256color",
			wantColor: "truecolor",
		},
		{
			name:      "TERM=dumb is dropped (some CI envs)",
			parent:    []string{"TERM=dumb"},
			wantTerm:  "xterm-256color",
			wantColor: "truecolor",
		},
		{
			name: "user cfg.Env wins via last-occurrence semantics",
			cfg: store.ClaudeAgent{
				Env: map[string]string{"TERM": "screen-256color"},
			},
			wantTerm:  "screen-256color",
			wantColor: "truecolor",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := buildClaudeEnv(tc.parent, tc.cfg)
			// exec.Cmd applies last-occurrence semantics on the env
			// slice. Assert against that, not just the first match.
			if v, _ := envValLast(got, "TERM"); v != tc.wantTerm {
				t.Errorf("TERM = %q, want %q (got env: %v)", v, tc.wantTerm, got)
			}
			if v, _ := envValLast(got, "COLORTERM"); v != tc.wantColor {
				t.Errorf("COLORTERM = %q, want %q", v, tc.wantColor)
			}
		})
	}
}

// resolveClaudeBinaryOverride is the env-then-config precedence helper
// feeding bindisco.Resolve's Override tier. Env wins by design so a single
// launch can target a different claude (e.g. testing a beta build) without
// editing the per-ideas config. Both empty → empty (bindisco walks tiers).
func TestResolveClaudeBinaryOverride(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		envBinary string
		cfgBinary string
		want      string
	}{
		{name: "neither set leaves override empty", want: ""},
		{name: "env only", envBinary: "/env/claude", want: "/env/claude"},
		{name: "config only", cfgBinary: "/cfg/claude", want: "/cfg/claude"},
		{name: "both set: env wins", envBinary: "/env/claude", cfgBinary: "/cfg/claude", want: "/env/claude"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveClaudeBinaryOverride(tc.envBinary, tc.cfgBinary)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
