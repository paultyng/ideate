package app

import (
	"context"
	"log/slog"

	"github.com/paultyng/ideate/internal/agent"
	"github.com/paultyng/ideate/internal/model"
)

// lazyAdoptIdeaSessions runs the adoption sweep. It classifies on-disk
// manifests for idea sessions into two outcomes:
//
//   - PID alive → keep in resumeCandidates for the caller
//   - PID dead  → mark session dormant; no restart
//
// The orchestrator (IdeaSlug == model.OrchestratorSlug) is always passed
// through unchanged so the caller's eager-resume path handles it.
//
// resumeCandidates is the slice returned by markRunningSessionsStopped for
// the crash reason; it contains every session that had Status==running at
// startup. This function filters it to only the sessions whose manifests
// indicate a live process. Dead idea sessions are marked dormant on disk
// and removed from the returned slice.
func (a *App) lazyAdoptIdeaSessions(
	ctx context.Context,
	resumeCandidates []ResumeCandidate,
) []ResumeCandidate {
	results := a.coordinator.Adopt(ctx)

	buckets := make(map[string]agent.AdoptBucket, len(results))
	for _, r := range results {
		buckets[r.Manifest.ID] = r.Bucket
	}

	var keep []ResumeCandidate
	for _, c := range resumeCandidates {
		if c.Slug == model.OrchestratorSlug {
			keep = append(keep, c)
			continue
		}

		bucket, hasBucket := buckets[c.UUID]
		if !hasBucket {
			a.markSessionDormant(ctx, c, "no_manifest")
			continue
		}

		switch bucket {
		case agent.AdoptBucketIdeaLiveActive:
			keep = append(keep, c)
		case agent.AdoptBucketIdeaDead:
			a.markSessionDormant(ctx, c, "process_died")
		default:
			a.markSessionDormant(ctx, c, "unknown_bucket")
		}
	}

	a.lazyAdoptShutdownSessions(ctx, buckets)

	return keep
}

// lazyAdoptShutdownSessions handles sessions with StopReason=shutdown.
// Same liveness rule: alive → resume, dead → dormant.
func (a *App) lazyAdoptShutdownSessions(
	ctx context.Context,
	buckets map[string]agent.AdoptBucket,
) {
	candidates := a.scanResumeCandidates(model.SessionStopReasonShutdown)
	if len(candidates) == 0 {
		return
	}

	var toResume []ResumeCandidate
	for _, c := range candidates {
		if c.Slug == model.OrchestratorSlug {
			toResume = append(toResume, c)
			continue
		}

		bucket, hasBucket := buckets[c.UUID]
		if !hasBucket {
			a.markSessionDormant(ctx, c, "no_manifest_shutdown")
			continue
		}
		switch bucket {
		case agent.AdoptBucketIdeaLiveActive:
			toResume = append(toResume, c)
		default:
			a.markSessionDormant(ctx, c, "process_died_shutdown")
		}
	}

	if len(toResume) > 0 {
		a.scheduleAutoResume(toResume, false)
	}
}

// markSessionDormant flips a session record from stopped/running to dormant.
// reason is logged for observability but not stored in the session record.
func (a *App) markSessionDormant(ctx context.Context, c ResumeCandidate, reason string) {
	sess, err := a.svc.ReadSession(ctx, c.Slug, c.UUID)
	if err != nil {
		slog.Warn("adopt: reading session for dormant flip",
			slog.String("slug", c.Slug),
			slog.String("uuid", c.UUID),
			slog.String("reason", reason),
			slog.Any("err", err),
		)
		return
	}
	sess.Status = model.SessionStatusDormant
	sess.StopReason = ""
	if sess.Outcome == "claude transcript deleted" {
		sess.Outcome = ""
	}
	if err := a.svc.WriteSessionPassive(ctx, c.Slug, c.UUID, *sess); err != nil {
		slog.Warn("adopt: writing dormant status",
			slog.String("slug", c.Slug),
			slog.String("uuid", c.UUID),
			slog.String("reason", reason),
			slog.Any("err", err),
		)
		return
	}
	if a.emitFn != nil {
		a.emitFn("session:"+c.UUID+":status", map[string]any{
			"status":   string(model.SessionStatusDormant),
			"exitCode": 0,
		})
	}
	slog.Info("adopt: session marked dormant",
		slog.String("slug", c.Slug),
		slog.String("uuid", c.UUID),
		slog.String("reason", reason),
	)
}
