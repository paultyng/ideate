package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/paultyng/ideate/internal/model"
)

// loadAndConsume reads the persisted snapshot bytes AND deletes the
// file — a snapshot is meant to be replayed exactly once. Verify both
// halves: returns the bytes on first call, returns nil on the
// second.
func TestLoadAndConsumeVscreenSnapshot_OneShot(t *testing.T) {
	t.Parallel()
	a, ideasDir := newSessionTestApp(t)

	slug := "alpha"
	uuid := "test-uuid"
	if err := os.MkdirAll(filepath.Join(ideasDir, slug, "sessions"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	wanted := []byte("\x1b[1msaved screen\x1b[0m\r\n")
	path := vscreenSnapshotPath(ideasDir, slug, uuid)
	if err := os.WriteFile(path, wanted, 0o644); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	got, _, _ := a.loadAndConsumeVscreenSnapshot(slug, uuid)
	if string(got) != string(wanted) {
		t.Errorf("first read = %q, want %q", got, wanted)
	}
	// File must be gone — second resume of the same UUID should not
	// double-preload.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected snapshot file removed; stat err = %v", err)
	}
	if got2, _, _ := a.loadAndConsumeVscreenSnapshot(slug, uuid); len(got2) != 0 {
		t.Errorf("second read returned bytes %q, expected nil", got2)
	}
}

// Missing file is a normal not-yet-persisted state; helper returns
// nil without erroring.
func TestLoadAndConsumeVscreenSnapshot_MissingIsNil(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)
	if got, _, _ := a.loadAndConsumeVscreenSnapshot("nonexistent-slug", "no-uuid"); got != nil {
		t.Errorf("expected nil for missing file, got %q", got)
	}
}

// Orchestrator sessions persist under the synthetic OrchestratorSlug; the
// path helper must rewrite empty slug to that constant so the resume
// path can find the file (resume passes IdeaSlug="" via SessionConfig
// for orchestrator sessions, but the on-disk record lives under
// OrchestratorSlug).
func TestVscreenSnapshotPath_OrchestratorFallback(t *testing.T) {
	t.Parallel()
	got := vscreenSnapshotPath("/ideas", "", "uuid-x")
	want := filepath.Join("/ideas", model.OrchestratorSlug, "sessions", "uuid-x.vscreen.ansi")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
