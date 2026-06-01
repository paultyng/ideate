package agent

import (
	"context"
	"log/slog"
	"syscall"

	"github.com/paultyng/ideate/internal/model"
)

// AdoptBucket categorises an on-disk manifest during Adopt().
type AdoptBucket int

const (
	// AdoptBucketOrchestratorLive is a live-PID orchestrator session.
	AdoptBucketOrchestratorLive AdoptBucket = iota
	// AdoptBucketIdeaLiveActive is an idea session with a live PID.
	// Treated as still running; the caller resumes it.
	AdoptBucketIdeaLiveActive
	// AdoptBucketIdeaDead is an idea session whose PID is gone. The
	// caller should mark the session dormant without restarting.
	AdoptBucketIdeaDead
)

// AdoptResult holds the classification for one on-disk manifest.
type AdoptResult struct {
	Manifest SessionManifest
	Bucket   AdoptBucket
}

// Adopt scans on-disk manifests and classifies each session into one of
// three buckets (see AdoptBucket). Idle time is no longer a signal —
// sessions whose PID is still alive are always treated as resumable.
func (c *AgentCoordinator) Adopt(ctx context.Context) []AdoptResult {
	_ = ctx
	manifests, err := scanManifests(c.configDir)
	if err != nil {
		slog.Warn("adopt: scanning manifests", slog.Any("err", err))
		return nil
	}

	results := make([]AdoptResult, 0, len(manifests))
	for _, m := range manifests {
		results = append(results, classifyManifest(m))
	}
	return results
}

// classifyManifest applies the alive/dead bucket logic to a single manifest.
func classifyManifest(m SessionManifest) AdoptResult {
	if m.IdeaSlug == model.OrchestratorSlug {
		return AdoptResult{Manifest: m, Bucket: AdoptBucketOrchestratorLive}
	}
	if !pidAlive(m.PID) {
		slog.Info("adopt: idea session PID dead — marking dormant",
			slog.String("uuid", m.ID),
			slog.String("idea_slug", m.IdeaSlug),
			slog.Int("pid", m.PID),
		)
		return AdoptResult{Manifest: m, Bucket: AdoptBucketIdeaDead}
	}
	slog.Info("adopt: idea session PID alive — keeping running",
		slog.String("uuid", m.ID),
		slog.String("idea_slug", m.IdeaSlug),
	)
	return AdoptResult{Manifest: m, Bucket: AdoptBucketIdeaLiveActive}
}

// pidAlive returns true if the process with the given PID exists and is
// reachable. Uses kill(pid, 0) — sends no signal; just probes existence.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
