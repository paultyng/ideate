package app

import (
	"log/slog"
	"sync"
	"time"

	"github.com/paultyng/ideate/internal/pubsub"
)

func (a *App) runSummarizerSweep() {
	a.summarizerSweepOnce("startup")
	ticker := time.NewTicker(summarySweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.summarizerSweepOnce("periodic")
		}
	}
}

// ideaChangedDebounce is the per-slug window for coalescing
// idea:changed events into a single summarizer re-run. Sized at one
// minute: long enough that a flurry of editor saves don't fan out to
// N Haiku calls, short enough that the dashboard line reflects body
// edits within a coffee-break.
const ideaChangedDebounce = 60 * time.Second

// runIdeaChangedDebouncer subscribes to idea:changed events from the
// pubsub broker and enqueues the affected slug onto the summarizer
// after a debounce. Owned by a goroutine spawned from startup; ctx
// cancellation drains and exits.
//
// Recursion guard lives in the watcher: shouldIgnore filters
// summary.json writes so the summarizer's own sidecar writes don't
// re-trigger this loop.

func (a *App) runIdeaChangedDebouncer() {
	if a.summarizer == nil || a.events == nil {
		return
	}
	ch, cancel := pubsub.Filter(a.events, func(ev pubsub.Event) bool {
		return ev.Name == EventIdeaChanged
	})
	defer cancel()

	var mu sync.Mutex
	timers := map[string]*time.Timer{}
	stopAll := func() {
		mu.Lock()
		for _, t := range timers {
			t.Stop()
		}
		timers = nil
		mu.Unlock()
	}

	for {
		select {
		case <-a.ctx.Done():
			stopAll()
			return
		case ev, ok := <-ch:
			if !ok {
				stopAll()
				return
			}
			payload, _ := ev.Data.(IdeaChangedPayload)
			slug := payload.Slug
			if slug == "" {
				continue
			}
			mu.Lock()
			if t, exists := timers[slug]; exists {
				t.Stop()
			}
			timers[slug] = time.AfterFunc(ideaChangedDebounce, func() {
				mu.Lock()
				delete(timers, slug)
				mu.Unlock()
				a.summarizer.Enqueue(slug)
			})
			mu.Unlock()
		}
	}
}

func (a *App) summarizerSweepOnce(kind string) {
	if a.summarizer == nil {
		return
	}
	enqueued, errs := a.summarizer.EnqueueStale(a.ctx, a.store, false)
	if enqueued > 0 || errs > 0 {
		slog.Info("summarizer sweep",
			slog.String("kind", kind),
			slog.Int("enqueued", enqueued),
			slog.Int("errs", errs))
	}
}
