package summarizer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/paultyng/ideate/internal/model"
)

// StaleStore is the slice of store ops needed by EnqueueStale +
// NeedsRegeneration. A superset of Store: also lists ideas. Kept
// narrow so tests can substitute fakes.
type StaleStore interface {
	Store
	List(ctx context.Context) ([]model.Idea, error)
}

// Reason explains why an idea's summary needs regeneration. The
// strings are stable so callers can log + assert on them.
type Reason string

const (
	ReasonFresh        Reason = "no sessions yet"
	ReasonMissing      Reason = "missing"
	ReasonNewerSession Reason = "newer session available"
	ReasonForce        Reason = "force"
)

// NeedsRegeneration is the gate logic used by both the CLI backfill
// and the App's periodic sweep. Returns (reason, true) when slug
// should be re-summarized, ("", false) when it's up to date.
//
// The one-line summary now lives on the idea's Description; the
// regenerate path bumps idea.Updated when it writes, so Updated
// doubles as the last-generation timestamp for staleness.
//
// Stale conditions, in order:
//
//  1. force is true — always regen.
//  2. No description yet — regen (use ReasonFresh when no sessions
//     exist either, ReasonMissing otherwise).
//  3. The most recent ended session ended after the last generation
//     (idea.Updated) — a newer session has run.
//
// External body edits are caught by the idea:changed debouncer, not
// this gate — without a separate generated-at timestamp the sweep
// can't distinguish a body edit from its own write.
//
// Errors propagate as-is for the caller to surface.
func NeedsRegeneration(ctx context.Context, store StaleStore, idea model.Idea, force bool) (Reason, bool, error) {
	if force {
		return ReasonForce, true, nil
	}
	sessions, err := store.ListSessions(ctx, idea.Slug)
	if err != nil {
		return "", false, fmt.Errorf("listing sessions for %s: %w", idea.Slug, err)
	}
	var latestEnded *time.Time
	for _, s := range sessions {
		if s.Ended == nil {
			continue
		}
		// ListSessions returns by Started DESC; first ended record is
		// the most recent terminated session.
		latestEnded = s.Ended
		break
	}
	if idea.Description == "" {
		if latestEnded == nil {
			return ReasonFresh, true, nil
		}
		return ReasonMissing, true, nil
	}
	if latestEnded != nil && latestEnded.After(idea.Updated) {
		return ReasonNewerSession, true, nil
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
