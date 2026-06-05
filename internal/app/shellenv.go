package app

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// harvestShellEnv runs the user's interactive login shell and captures
// its env into a KEY=VALUE slice. Used at app boot to give Dock-launched
// Ideate the same PATH / locale / user-configured vars the user gets in
// their terminal — without this, claude / testagent / spawned tools
// see only the launchd-minimal env (PATH=/usr/bin:/bin:..., no
// TERM_PROGRAM, no brew shims, no user exports from .zprofile).
//
// Pattern mirrors VSCode / Cursor / Zed's resolveShellEnv: spawn
// `$SHELL -ilc 'env -0'` (NUL-separated so multi-line values survive),
// parse, return. 10s timeout per JetBrains' documented hang mode where
// rc files that probe for a TTY can block indefinitely.
//
// Returns nil and logs on any failure — callers should fall back to
// launch env. Set IDEATE_NO_SHELL_ENV=1 to skip the harvest entirely
// (testing / when the user prefers explicit env).
//
// Output is the same shape as os.Environ() (KEY=VALUE slice) so it
// merges cleanly with parent env via append.
func harvestShellEnv() []string {
	if os.Getenv("IDEATE_NO_SHELL_ENV") == "1" {
		slog.Debug("shellenv: skipped (IDEATE_NO_SHELL_ENV=1)")
		return nil
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		// Fallback chain matches what launchd-launched apps actually see —
		// $SHELL unset is common in that env. Pick the user's likely shell
		// without invoking system calls; bash exists on every macOS install.
		for _, candidate := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
			if _, err := os.Stat(candidate); err == nil {
				shell = candidate
				break
			}
		}
		if shell == "" {
			slog.Warn("shellenv: no shell found in $SHELL or fallback chain")
			return nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// -i interactive (sources .zshrc / .bashrc), -l login (sources
	// .zprofile / .bash_profile), -c runs the command and exits.
	// `env -0` writes NUL-separated KEY=VAL so a multi-line value (e.g.
	// PROMPT_COMMAND with embedded newlines) doesn't split.
	cmd := exec.CommandContext(ctx, shell, "-ilc", "env -0")
	// Strip the parent's env so the user's shell init isn't influenced by
	// the minimal launchd env we're inheriting. The shell will populate
	// its own env from rc files. SHELL passed back through so the shell
	// can identify itself in scripts that look at it.
	cmd.Env = []string{"SHELL=" + shell}
	out, err := cmd.Output()
	if err != nil {
		slog.Warn("shellenv: harvest failed; falling back to launch env",
			slog.String("shell", shell),
			slog.Any("err", err))
		return nil
	}

	entries := parseNullSeparatedEnv(out)
	slog.Info("shellenv: harvested",
		slog.String("shell", shell),
		slog.Int("entries", len(entries)))
	return entries
}

// parseNullSeparatedEnv splits the output of `env -0` into KEY=VAL
// entries. Skips empty entries and entries without `=` (defensive —
// `env -0` should never produce these).
func parseNullSeparatedEnv(raw []byte) []string {
	parts := strings.Split(string(raw), "\x00")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" || !strings.Contains(p, "=") {
			continue
		}
		out = append(out, p)
	}
	return out
}

// looksDockLaunched returns true when the inherited env shape matches
// what a macOS Dock launch produces — empty TERM_PROGRAM AND a minimal
// PATH that doesn't include /opt/homebrew/bin or /usr/local/bin. Used
// to skip the harvest on terminal launches where the shell-env is
// already inherited correctly (no need to pay the spawn cost).
//
// Conservative: returns false when uncertain. False positives just
// mean we pay the harvest cost on a terminal launch (cheap); false
// negatives mean we MISS harvesting on a Dock launch (expensive — the
// bug stays unfixed). Bias toward false positives.
func looksDockLaunched() bool {
	// Terminal.app sets TERM_PROGRAM=Apple_Terminal, iTerm sets
	// TERM_PROGRAM=iTerm.app, vscode sets TERM_PROGRAM=vscode. If any
	// of these is set, we're terminal-launched.
	if os.Getenv("TERM_PROGRAM") != "" {
		return false
	}
	// VTE-based terminals (GNOME Terminal, Tilix) set VTE_VERSION.
	if os.Getenv("VTE_VERSION") != "" {
		return false
	}
	// Windows Terminal.
	if os.Getenv("WT_SESSION") != "" {
		return false
	}
	// Dock launches inherit a minimal PATH. If PATH already contains
	// brew, we're probably terminal-launched even if TERM_PROGRAM is
	// somehow unset.
	path := os.Getenv("PATH")
	if strings.Contains(path, "/opt/homebrew/bin") ||
		strings.Contains(path, "/usr/local/bin") {
		return false
	}
	return true
}

// applyShellEnv writes harvested entries into the current process env.
// Existing values are overridden so the user's shell config wins over
// launchd defaults. After this, every subprocess inherits via
// os.Environ() — claude, testagent, MCP servers, summarizer.
//
// Returns the count of vars actually applied (vs. parsed) for the
// slog line.
func applyShellEnv(entries []string) int {
	applied := 0
	for _, kv := range entries {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		key, val := kv[:eq], kv[eq+1:]
		// Don't overwrite our own runtime-meaningful vars. The launch
		// env has these set correctly; the shell's idea would be wrong.
		switch key {
		case "PWD", "_", "SHLVL", "OLDPWD":
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			slog.Debug("shellenv: setenv failed",
				slog.String("key", key), slog.Any("err", err))
			continue
		}
		applied++
	}
	return applied
}

// ResolveShellEnv runs the harvest + apply sequence. Called once from
// app.Launch before App.New so every downstream subprocess inherits.
// Best-effort — silent fall through to launch env when harvest fails
// or skip conditions trigger.
func ResolveShellEnv() {
	if !looksDockLaunched() {
		slog.Debug("shellenv: terminal-launched, skipping harvest")
		return
	}
	entries := harvestShellEnv()
	if entries == nil {
		return
	}
	applied := applyShellEnv(entries)
	slog.Info("shellenv: applied",
		slog.Int("vars", applied),
		slog.String("path", os.Getenv("PATH")))
}
