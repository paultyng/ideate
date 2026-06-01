package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManifest_WriteAndScan(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	m := SessionManifest{
		ID:         "ses-1-test",
		Name:       "test",
		PID:        os.Getpid(),
		AgentType:  "testagent",
		WorkingDir: "/tmp",
		StartedAt:  time.Now(),
	}

	if err := writeManifest(dir, m); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	// Verify the file exists.
	path := filepath.Join(dir, "sessions", "ses-1-test.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("manifest file not found: %v", err)
	}

	manifests, err := scanManifests(dir)
	if err != nil {
		t.Fatalf("scanManifests: %v", err)
	}
	if len(manifests) != 1 {
		t.Fatalf("expected 1 manifest, got %d", len(manifests))
	}
	if manifests[0].ID != "ses-1-test" {
		t.Errorf("expected ID %q, got %q", "ses-1-test", manifests[0].ID)
	}
}

func TestManifest_Remove(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	m := SessionManifest{
		ID:        "ses-2-remove",
		Name:      "remove",
		PID:       os.Getpid(),
		AgentType: "testagent",
		StartedAt: time.Now(),
	}

	if err := writeManifest(dir, m); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	if err := removeManifest(dir, "ses-2-remove"); err != nil {
		t.Fatalf("removeManifest: %v", err)
	}

	manifests, err := scanManifests(dir)
	if err != nil {
		t.Fatalf("scanManifests: %v", err)
	}
	if len(manifests) != 0 {
		t.Fatalf("expected 0 manifests after remove, got %d", len(manifests))
	}
}

func TestManifest_CleanStale(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Write a manifest with a PID that definitely doesn't exist.
	m := SessionManifest{
		ID:        "ses-3-stale",
		Name:      "stale",
		PID:       999999999, // very unlikely to exist
		AgentType: "testagent",
		StartedAt: time.Now(),
	}

	if err := writeManifest(dir, m); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	if err := cleanStaleManifests(dir); err != nil {
		t.Fatalf("cleanStaleManifests: %v", err)
	}

	manifests, err := scanManifests(dir)
	if err != nil {
		t.Fatalf("scanManifests: %v", err)
	}
	if len(manifests) != 0 {
		t.Fatalf("expected stale manifest to be removed, got %d", len(manifests))
	}
}

func TestManifest_ScanEmpty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	manifests, err := scanManifests(dir)
	if err != nil {
		t.Fatalf("scanManifests on empty dir: %v", err)
	}
	if len(manifests) != 0 {
		t.Fatalf("expected 0 manifests, got %d", len(manifests))
	}
}
