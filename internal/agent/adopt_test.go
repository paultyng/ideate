package agent

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/paultyng/ideate/internal/model"
)

// spawnSleepProc starts a long-lived background process and returns its PID.
// A goroutine calls Wait() so the process doesn't linger as a zombie after
// it is killed — important for pidAlive() to return false promptly.
func spawnSleepProc(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sleep: %v", err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
	})
	return cmd.Process.Pid
}

// waitForDeadPID runs a process, waits for it to exit, and returns its
// former PID — guaranteed not to be reused for long enough for the test.
func waitForDeadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running true: %v", err)
	}
	return cmd.ProcessState.Pid()
}

// TestAdopt_Orchestrator_EagerLiveAttach verifies that an orchestrator manifest
// with a live PID lands in AdoptBucketOrchestratorLive.
func TestAdopt_Orchestrator_EagerLiveAttach(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	pid := spawnSleepProc(t)

	m := SessionManifest{
		ID:        "orch-uuid-1",
		Name:      "orchestrator",
		PID:       pid,
		AgentType: "claude-code",
		StartedAt: time.Now().Add(-3 * time.Hour),
		IdeaSlug:  model.OrchestratorSlug,
	}
	if err := writeManifest(configDir, m); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	coord := NewCoordinator(configDir)
	results := coord.Adopt(context.Background())

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Bucket != AdoptBucketOrchestratorLive {
		t.Errorf("expected AdoptBucketOrchestratorLive, got %v", results[0].Bucket)
	}
}

// TestAdopt_IdeaSession_LivePID_Adopted verifies that an idea session whose
// PID is still alive — regardless of how long it has been idle — lands in
// AdoptBucketIdeaLiveActive.
func TestAdopt_IdeaSession_LivePID_Adopted(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	pid := spawnSleepProc(t)

	m := SessionManifest{
		ID:        "idea-uuid-active",
		Name:      "active idea session",
		PID:       pid,
		AgentType: "claude-code",
		StartedAt: time.Now().Add(-10 * time.Minute),
		IdeaSlug:  "my-idea",
	}
	if err := writeManifest(configDir, m); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	coord := NewCoordinator(configDir)
	results := coord.Adopt(context.Background())

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Bucket != AdoptBucketIdeaLiveActive {
		t.Errorf("expected AdoptBucketIdeaLiveActive, got %v", results[0].Bucket)
	}
}

// TestAdopt_IdeaSession_DeadPID_MarkedDormant verifies that an idea session
// whose PID is already gone lands in AdoptBucketIdeaDead.
func TestAdopt_IdeaSession_DeadPID_MarkedDormant(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	deadPID := waitForDeadPID(t)

	m := SessionManifest{
		ID:        "idea-uuid-dead",
		Name:      "dead idea session",
		PID:       deadPID,
		AgentType: "claude-code",
		StartedAt: time.Now().Add(-30 * time.Minute),
		IdeaSlug:  "my-dead-idea",
	}
	if err := writeManifest(configDir, m); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	coord := NewCoordinator(configDir)
	results := coord.Adopt(context.Background())

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Bucket != AdoptBucketIdeaDead {
		t.Errorf("expected AdoptBucketIdeaDead, got %v", results[0].Bucket)
	}
}

// TestAdopt_Mixed verifies multi-manifest classification in one call:
// orchestrator → orch bucket, alive idea → active, dead idea → dead.
func TestAdopt_Mixed(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()

	orchPID := spawnSleepProc(t)
	activePID := spawnSleepProc(t)
	deadPID := waitForDeadPID(t)

	manifests := []SessionManifest{
		{ID: "uuid-orch", Name: "orchestrator", PID: orchPID, AgentType: "claude-code",
			StartedAt: time.Now().Add(-5 * time.Hour), IdeaSlug: model.OrchestratorSlug},
		{ID: "uuid-active", Name: "active", PID: activePID, AgentType: "claude-code",
			StartedAt: time.Now().Add(-10 * time.Minute), IdeaSlug: "active-idea"},
		{ID: "uuid-dead", Name: "dead", PID: deadPID, AgentType: "claude-code",
			StartedAt: time.Now().Add(-1 * time.Hour), IdeaSlug: "dead-idea"},
	}
	for _, m := range manifests {
		if err := writeManifest(configDir, m); err != nil {
			t.Fatalf("writeManifest %s: %v", m.ID, err)
		}
	}

	coord := NewCoordinator(configDir)
	results := coord.Adopt(context.Background())

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	byUUID := make(map[string]AdoptBucket)
	for _, r := range results {
		byUUID[r.Manifest.ID] = r.Bucket
	}

	want := map[string]AdoptBucket{
		"uuid-orch":   AdoptBucketOrchestratorLive,
		"uuid-active": AdoptBucketIdeaLiveActive,
		"uuid-dead":   AdoptBucketIdeaDead,
	}
	for uuid, wantBucket := range want {
		if got := byUUID[uuid]; got != wantBucket {
			t.Errorf("uuid=%s: got bucket %v, want %v", uuid, got, wantBucket)
		}
	}
}
