package bindisco

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeStubBin writes a 0o755 executable shell script at path. Used by
// each tier's test to plant a discoverable "binary" without touching the
// real toolchain.
func writeStubBin(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestResolve_OverrideWins(t *testing.T) {
	t.Parallel()
	// Override is honored even when the path doesn't exist — the override
	// tier is "trust the caller verbatim". Existence checking is the
	// caller's job; conflating the two would defeat the override.
	got, err := Resolve("claude", Locations{Override: "/nope/claude"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "/nope/claude" {
		t.Errorf("got %q, want %q", got, "/nope/claude")
	}
}

func TestResolve_LookPathHit(t *testing.T) {
	// No t.Parallel(): t.Setenv (below) is incompatible with parallel tests
	// (Go 1.26+ panics; older versions silently leaked env state).
	dir := t.TempDir()
	bin := filepath.Join(dir, "stubcli")
	writeStubBin(t, bin)
	t.Setenv("PATH", dir)

	got, err := Resolve("stubcli", Locations{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != bin {
		t.Errorf("got %q, want %q", got, bin)
	}
}

func TestResolve_ExtraCommonPathsHit(t *testing.T) {
	// No t.Parallel(): t.Setenv (below) is incompatible with parallel tests
	// (Go 1.26+ panics; older versions silently leaked env state).
	dir := t.TempDir()
	bin := filepath.Join(dir, "stubcli")
	writeStubBin(t, bin)
	// Empty PATH forces LookPath to miss.
	t.Setenv("PATH", "")

	got, err := Resolve("stubcli", Locations{ExtraCommonPaths: []string{bin}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != bin {
		t.Errorf("got %q, want %q", got, bin)
	}
}

func TestResolve_NotFound(t *testing.T) {
	// No t.Parallel(): uses t.Setenv (Go 1.26+ panic incompatibility).
	t.Setenv("PATH", "")
	t.Setenv("HOME", t.TempDir()) // empty home, no curated hits

	_, err := Resolve("definitely-not-here", Locations{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want wrap of ErrNotFound", err)
	}
	// Error message should name what was tried so users see where to look.
	if !strings.Contains(err.Error(), "$PATH") {
		t.Errorf("error %q should mention $PATH", err.Error())
	}
}

func TestResolve_SkipsNonExecutableFile(t *testing.T) {
	// No t.Parallel(): t.Setenv (below) is incompatible with parallel tests
	// (Go 1.26+ panics; older versions silently leaked env state).
	dir := t.TempDir()
	bin := filepath.Join(dir, "stubcli")
	// Mode 0o644 — readable but not executable; should not be selected.
	if err := os.WriteFile(bin, []byte("not a real exe\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("PATH", "")

	_, err := Resolve("stubcli", Locations{ExtraCommonPaths: []string{bin}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-exec file should not be picked up; err = %v", err)
	}
}

func TestResolve_SkipsDirectoryAtBinaryPath(t *testing.T) {
	// No t.Parallel(): t.Setenv (below) is incompatible with parallel tests
	// (Go 1.26+ panics; older versions silently leaked env state).
	dir := t.TempDir()
	dirAsBin := filepath.Join(dir, "stubcli")
	// A directory at the expected binary path should not satisfy lookup
	// (some package managers leave behind empty dir stubs).
	if err := os.Mkdir(dirAsBin, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("PATH", "")

	_, err := Resolve("stubcli", Locations{ExtraCommonPaths: []string{dirAsBin}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("dir should not be picked up; err = %v", err)
	}
}

func TestExpandNvmGlob_PicksNewestMtime(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("nvm layout is unix-specific")
	}
	t.Parallel()
	home := t.TempDir()
	older := filepath.Join(home, ".nvm", "versions", "node", "v18.0.0", "bin", "stubcli")
	newer := filepath.Join(home, ".nvm", "versions", "node", "v20.0.0", "bin", "stubcli")
	writeStubBin(t, older)
	writeStubBin(t, newer)
	// Force older to actually be older by setting its mtime backwards.
	if err := os.Chtimes(older, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got := expandNvmGlob(home, "stubcli")
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2 (%v)", len(got), got)
	}
	if got[0] != newer {
		t.Errorf("got newest %q, want %q", got[0], newer)
	}
}

func TestExpandNvmGlob_EmptyWhenNoNvm(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	got := expandNvmGlob(home, "stubcli")
	if got != nil {
		t.Errorf("got %v, want nil for missing nvm dir", got)
	}
}
