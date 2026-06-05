package app

import (
	"os"
	"slices"
	"testing"
)

func TestParseNullSeparatedEnv(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "single entry",
			raw:  "PATH=/usr/bin\x00",
			want: []string{"PATH=/usr/bin"},
		},
		{
			name: "multiple entries",
			raw:  "PATH=/usr/bin\x00HOME=/Users/me\x00SHELL=/bin/zsh\x00",
			want: []string{"PATH=/usr/bin", "HOME=/Users/me", "SHELL=/bin/zsh"},
		},
		{
			name: "value with embedded newlines survives NUL split",
			raw:  "PROMPT=line1\nline2\nline3\x00PATH=/bin\x00",
			want: []string{"PROMPT=line1\nline2\nline3", "PATH=/bin"},
		},
		{
			name: "skips empty + malformed entries",
			raw:  "PATH=/bin\x00\x00BARE_WORD\x00=NOPE\x00HOME=/h\x00",
			want: []string{"PATH=/bin", "=NOPE", "HOME=/h"},
		},
		{
			name: "no trailing NUL",
			raw:  "A=1\x00B=2",
			want: []string{"A=1", "B=2"},
		},
		{
			name: "empty input",
			raw:  "",
			want: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseNullSeparatedEnv([]byte(tc.raw))
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("parseNullSeparatedEnv = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLooksDockLaunched(t *testing.T) {
	cases := []struct {
		name        string
		termProgram string
		vteVersion  string
		wtSession   string
		path        string
		want        bool
	}{
		{
			name:        "Dock launch: minimal env, no TERM_PROGRAM, minimal PATH",
			termProgram: "",
			path:        "/usr/bin:/bin:/usr/sbin:/sbin",
			want:        true,
		},
		{
			name:        "Terminal.app: TERM_PROGRAM set",
			termProgram: "Apple_Terminal",
			path:        "/usr/bin:/bin",
			want:        false,
		},
		{
			name:        "iTerm: TERM_PROGRAM set",
			termProgram: "iTerm.app",
			path:        "/usr/bin:/bin",
			want:        false,
		},
		{
			name:       "VTE-based terminal (GNOME Terminal): VTE_VERSION set",
			vteVersion: "6800",
			path:       "/usr/bin:/bin",
			want:       false,
		},
		{
			name:      "Windows Terminal: WT_SESSION set",
			wtSession: "0d92cb02-...",
			path:      "/usr/bin:/bin",
			want:      false,
		},
		{
			name:        "Dock launch with empty PATH (defensive)",
			termProgram: "",
			path:        "",
			want:        true,
		},
		{
			name:        "Terminal-launched even without TERM_PROGRAM if PATH has brew",
			termProgram: "",
			path:        "/opt/homebrew/bin:/usr/bin:/bin",
			want:        false,
		},
		{
			name:        "Terminal-launched with /usr/local/bin in PATH",
			termProgram: "",
			path:        "/usr/local/bin:/usr/bin:/bin",
			want:        false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TERM_PROGRAM", tc.termProgram)
			t.Setenv("VTE_VERSION", tc.vteVersion)
			t.Setenv("WT_SESSION", tc.wtSession)
			t.Setenv("PATH", tc.path)
			got := looksDockLaunched()
			if got != tc.want {
				t.Errorf("looksDockLaunched = %v, want %v (TERM_PROGRAM=%q VTE_VERSION=%q WT_SESSION=%q PATH=%q)",
					got, tc.want, tc.termProgram, tc.vteVersion, tc.wtSession, tc.path)
			}
		})
	}
}

func TestApplyShellEnv_OverwritesAndPreserves(t *testing.T) {
	t.Setenv("EXISTING_LAUNCH_VAR", "launch-value")
	t.Setenv("SHELL_HARVESTED_VAR", "")
	// PWD/_/SHLVL/OLDPWD must be preserved at their launch-time values
	// even if the harvest sets them — those are runtime-meaningful.
	t.Setenv("PWD", "/launch/pwd")

	entries := []string{
		"SHELL_HARVESTED_VAR=harvested",
		"EXISTING_LAUNCH_VAR=overridden-by-harvest",
		"PWD=/shell/pwd",  // should be skipped
		"_=/usr/bin/zsh",  // should be skipped
		"SHLVL=2",         // should be skipped
		"OLDPWD=/old/pwd", // should be skipped
		"=NO_KEY",         // should be skipped (no key)
	}
	applied := applyShellEnv(entries)

	if applied != 2 {
		t.Errorf("applied count = %d, want 2 (only SHELL_HARVESTED_VAR + EXISTING_LAUNCH_VAR)", applied)
	}
	if v := os.Getenv("SHELL_HARVESTED_VAR"); v != "harvested" {
		t.Errorf("SHELL_HARVESTED_VAR = %q, want harvested", v)
	}
	if v := os.Getenv("EXISTING_LAUNCH_VAR"); v != "overridden-by-harvest" {
		t.Errorf("EXISTING_LAUNCH_VAR = %q, want overridden-by-harvest", v)
	}
	if v := os.Getenv("PWD"); v != "/launch/pwd" {
		t.Errorf("PWD = %q, want /launch/pwd (must NOT be overridden by harvest)", v)
	}
}
