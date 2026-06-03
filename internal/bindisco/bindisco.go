// Package bindisco resolves binaries by name across the messy reality of
// macOS / Linux install locations. macOS GUI apps inherit launchd's PATH
// (default `/usr/bin:/bin:/usr/sbin:/sbin`), NOT the user's shell PATH, so
// a plain os/exec.LookPath misses binaries installed under ~/.local/bin,
// /opt/homebrew/bin, ~/.npm/bin, etc. This package layers a per-OS curated
// fallback on top of LookPath, plus a caller-supplied override tier for
// env-var / config-file escape hatches.
//
// First caller is the claude CLI (see internal/claudecode); future callers
// (codex, cursor, testagent variants) share the same lookup tiers.
package bindisco

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// ErrNotFound is returned by Resolve when no tier finds the binary.
// Wraps the per-tier attempts so callers can render a "looked here:" list.
var ErrNotFound = errors.New("binary not found")

// Locations parameterises Resolve's search. Empty means "$PATH only,
// no curated fallback" (matches os/exec.LookPath behaviour).
type Locations struct {
	// Override, if non-empty, is honored verbatim. Skips all other tiers.
	// Use for env-var overrides (e.g. IDEATE_CLAUDE_BINARY) and
	// config-file knobs (e.g. agents.claude.binary in config.json).
	// Empty when no override is set.
	//
	// Contract: Resolve returns Override without stat'ing or executable-
	// bit checking it. A non-existent or non-executable Override surfaces
	// only at exec time. Caller owns validation when per-source error
	// messages matter (e.g. "env var points nowhere" vs "config field
	// points nowhere").
	Override string

	// ExtraCommonPaths lands AFTER the curated per-OS entries in the
	// lookup order. Use for binary-specific locations the curated list
	// doesn't carry (e.g. ~/.claude/local/claude for the Anthropic
	// installer). Each entry should be an absolute path that already
	// includes the binary name — Resolve checks it as-is.
	ExtraCommonPaths []string
}

// Resolve returns the absolute path to a binary named `name`, trying in
// order:
//
//  1. locations.Override (verbatim, if non-empty)
//  2. os/exec.LookPath(name) (honors current $PATH)
//  3. Curated per-OS common paths + locations.ExtraCommonPaths
//
// Returns a wrapped ErrNotFound on full miss. The error string lists every
// tier attempted so failure messages stay actionable.
func Resolve(name string, locations Locations) (string, error) {
	if locations.Override != "" {
		// Override is trusted verbatim. Callers that want existence
		// validation can stat it themselves; surfacing a clear error
		// from Resolve here would conflate "you set it wrong" with
		// "we couldn't find it anywhere," which the override exists to
		// disambiguate.
		return locations.Override, nil
	}

	var attempts []string

	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	attempts = append(attempts, "$PATH")

	for _, p := range candidatePaths(name, locations.ExtraCommonPaths) {
		if isExecutable(p) {
			return p, nil
		}
		attempts = append(attempts, p)
	}

	return "", fmt.Errorf("%w: %q; looked in: %s", ErrNotFound, name, strings.Join(attempts, ", "))
}

// candidatePaths returns the absolute paths to check for `name`,
// per-OS curated list first, then ExtraCommonPaths. Home-relative entries
// are expanded; glob entries (e.g. nvm node-versions) are expanded and
// sorted newest-mtime-first.
func candidatePaths(name string, extra []string) []string {
	var roots []string

	home, _ := os.UserHomeDir()

	switch runtime.GOOS {
	case "darwin":
		if home != "" {
			roots = append(roots, filepath.Join(home, ".local", "bin", name))
		}
		roots = append(roots,
			filepath.Join("/opt/homebrew/bin", name),
			filepath.Join("/usr/local/bin", name),
		)
		if home != "" {
			roots = append(roots, filepath.Join(home, ".npm", "bin", name))
			roots = append(roots, expandNvmGlob(home, name)...)
		}
	case "linux":
		if home != "" {
			roots = append(roots, filepath.Join(home, ".local", "bin", name))
		}
		roots = append(roots,
			filepath.Join("/usr/local/bin", name),
			filepath.Join("/usr/bin", name),
		)
	}

	return append(roots, extra...)
}

// expandNvmGlob returns ~/.nvm/versions/node/*/bin/<name> matches sorted
// newest-mtime-first. Empty slice on miss (no nvm installed, or no node
// version with the binary). The newest-first ordering picks whichever node
// the user most recently installed, which is usually the one their shell
// rc points $PATH at.
func expandNvmGlob(home, name string) []string {
	if home == "" {
		return nil
	}
	root := filepath.Join(home, ".nvm", "versions", "node")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	type candidate struct {
		path string
		ts   int64
	}
	var cs []candidate
	for _, e := range entries {
		p := filepath.Join(root, e.Name(), "bin", name)
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		cs = append(cs, candidate{path: p, ts: fi.ModTime().UnixNano()})
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].ts > cs[j].ts })
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.path
	}
	return out
}

// isExecutable reports whether p exists, is a regular file, and has at
// least one execute bit set. Quick-fails on missing files without
// surfacing a stat error to the caller — the path list is speculative by
// nature.
func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	if err != nil {
		return false
	}
	if !fi.Mode().IsRegular() {
		return false
	}
	return fi.Mode().Perm()&0o111 != 0
}
