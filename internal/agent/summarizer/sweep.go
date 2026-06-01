package summarizer

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/paultyng/ideate/internal/model"
)

// StaleStore is the slice of store ops needed by EnqueueStale +
// NeedsRegeneration. A superset of Store: also reads sidecars and
// lists ideas. Kept narrow so tests can substitute fakes.
type StaleStore interface {
	Store
	List(ctx context.Context) ([]model.Idea, error)
	ReadSummary(ctx context.Context, slug string) (*model.Summary, error)
}

// Reason explains why an idea's summary needs regeneration. The
// strings are stable so callers can log + assert on them.
type Reason string

const (
	ReasonFresh        Reason = "no sessions yet"
	ReasonMissing      Reason = "missing"
	ReasonNewerSession Reason = "newer session available"
	ReasonNewerIdea    Reason = "idea updated since summary"
	ReasonForce        Reason = "force"
)

// NeedsRegeneration is the gate logic used by both the CLI backfill
// and the App's periodic sweep. Returns (reason, true) when slug
// should be re-summarized, ("", false) when it's up to date.
//
// Stale conditions, in order:
//
//  1. force is true — always regen.
//  2. No sidecar on disk — regen (use ReasonFresh when no sessions
//     exist either, ReasonMissing otherwise).
//  3. The sidecar's SourceSessionUUID is older than the most recent
//     ended session's UUID — a newer session has run.
//  4. The idea's Updated timestamp is newer than the sidecar's
//     GeneratedAt — the body was edited (externally or otherwise).
//
// Errors propagate as-is for the caller to surface.
func NeedsRegeneration(ctx context.Context, store StaleStore, idea model.Idea, force bool) (Reason, bool, error) {
	if force {
		return ReasonForce, true, nil
	}
	cur, err := store.ReadSummary(ctx, idea.Slug)
	if err != nil {
		return "", false, fmt.Errorf("reading summary for %s: %w", idea.Slug, err)
	}
	sessions, err := store.ListSessions(ctx, idea.Slug)
	if err != nil {
		return "", false, fmt.Errorf("listing sessions for %s: %w", idea.Slug, err)
	}
	latestUUID := ""
	for _, s := range sessions {
		if s.Ended == nil {
			continue
		}
		// ListSessions returns by Started DESC; first ended record is
		// the most recent terminated session.
		latestUUID = s.UUID
		break
	}
	if cur == nil {
		if latestUUID == "" {
			return ReasonFresh, true, nil
		}
		return ReasonMissing, true, nil
	}
	if cur.SourceSessionUUID != latestUUID {
		return ReasonNewerSession, true, nil
	}
	if !idea.Updated.IsZero() && idea.Updated.After(cur.GeneratedAt) {
		return ReasonNewerIdea, true, nil
	}
	return "", false, nil
}

// EnqueueStale walks every idea via store.List, evaluates the gate
// for each, and enqueues those needing regeneration onto s. Returns
// the number of slugs enqueued and the number that errored out
// during the gate evaluation (errors are logged via the Summarizer's
// logger; the walk continues past per-slug failures).
//
// Use Enqueue on the returned Summarizer; the workers must already
// be running via Start.
func (s *Summarizer) EnqueueStale(ctx context.Context, store StaleStore, force bool) (enqueued int, errs int) {
	ideas, err := store.List(ctx)
	if err != nil {
		s.logger.Warn("summarizer sweep: listing ideas",
			slog.Any("err", err))
		return 0, 1
	}
	for _, idea := range ideas {
		if ctx.Err() != nil {
			return enqueued, errs
		}
		reason, needs, err := NeedsRegeneration(ctx, store, idea, force)
		if err != nil {
			errs++
			s.logger.Warn("summarizer sweep: gate check failed",
				slog.String("slug", idea.Slug),
				slog.Any("err", err))
			continue
		}
		if !needs {
			continue
		}
		if !s.Enqueue(idea.Slug) {
			s.logger.Warn("summarizer sweep: queue full",
				slog.String("slug", idea.Slug),
				slog.String("reason", string(reason)))
			continue
		}
		enqueued++
	}
	return enqueued, errs
}
