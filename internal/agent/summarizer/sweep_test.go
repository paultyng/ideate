package summarizer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/paultyng/ideate/internal/model"
	ext_store "github.com/paultyng/ideate/internal/store"
)

// staleFakeStore extends fakeStore with the bits NeedsRegeneration +
// EnqueueStale need (List, ReadSummary). Stays in-memory.
type staleFakeStore struct {
	mu        sync.Mutex
	ideas     []model.Idea
	sessions  map[string][]model.AgentSession
	summaries map[string]model.Summary
	written   map[string]model.Summary

	readSummaryErr error
}

func newStaleFakeStore() *staleFakeStore {
	return &staleFakeStore{
		sessions:  map[string][]model.AgentSession{},
		summaries: map[string]model.Summary{},
		written:   map[string]model.Summary{},
	}
}

func (f *staleFakeStore) Get(_ context.Context, slug string) (*model.Idea, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.ideas {
		if f.ideas[i].Slug == slug {
			cp := f.ideas[i]
			return &cp, nil
		}
	}
	return nil, errors.New("not found")
}

func (f *staleFakeStore) List(_ context.Context) ([]model.Idea, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.Idea, len(f.ideas))
	copy(out, f.ideas)
	return out, nil
}

func (f *staleFakeStore) ListSessions(_ context.Context, slug string) ([]model.AgentSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[slug], nil
}

func (f *staleFakeStore) ListRepos(_ context.Context, _ string) ([]ext_store.RepoLink, error) {
	return nil, nil
}

func (f *staleFakeStore) ReadSummary(_ context.Context, slug string) (*model.Summary, error) {
	if f.readSummaryErr != nil {
		return nil, f.readSummaryErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if sum, ok := f.summaries[slug]; ok {
		return &sum, nil
	}
	return nil, nil
}

func (f *staleFakeStore) WriteSummary(_ context.Context, slug string, sum model.Summary) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written[slug] = sum
	return nil
}

func (f *staleFakeStore) AddResource(_ context.Context, _ string, _ model.Resource) error {
	return nil
}

func TestNeedsRegeneration(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	earlier := now.Add(-2 * time.Hour)
	later := now.Add(time.Hour)

	cases := []struct {
		name     string
		setup    func(st *staleFakeStore, idea *model.Idea)
		force    bool
		want     Reason
		wantNeed bool
	}{
		{
			name: "force always regen",
			setup: func(st *staleFakeStore, _ *model.Idea) {
				st.summaries["x"] = model.Summary{
					Line: "fresh", GeneratedAt: now, SourceSessionUUID: "s1",
				}
				st.sessions["x"] = []model.AgentSession{
					{UUID: "s1", Status: model.SessionStatusCompleted, Ended: ptrTime(earlier)},
				}
			},
			force:    true,
			want:     ReasonForce,
			wantNeed: true,
		},
		{
			name:     "no sessions yet",
			setup:    func(_ *staleFakeStore, _ *model.Idea) {},
			want:     ReasonFresh,
			wantNeed: true,
		},
		{
			name: "session exists but no sidecar",
			setup: func(st *staleFakeStore, _ *model.Idea) {
				st.sessions["x"] = []model.AgentSession{
					{UUID: "s1", Status: model.SessionStatusCompleted, Ended: ptrTime(earlier)},
				}
			},
			want:     ReasonMissing,
			wantNeed: true,
		},
		{
			name: "newer session than sidecar",
			setup: func(st *staleFakeStore, _ *model.Idea) {
				st.summaries["x"] = model.Summary{
					GeneratedAt: now, SourceSessionUUID: "s1",
				}
				st.sessions["x"] = []model.AgentSession{
					{UUID: "s2", Status: model.SessionStatusCompleted, Ended: ptrTime(now), Started: now},
					{UUID: "s1", Status: model.SessionStatusCompleted, Ended: ptrTime(earlier), Started: earlier},
				}
			},
			want:     ReasonNewerSession,
			wantNeed: true,
		},
		{
			name: "idea updated after sidecar",
			setup: func(st *staleFakeStore, idea *model.Idea) {
				idea.Updated = later
				st.summaries["x"] = model.Summary{
					GeneratedAt: now, SourceSessionUUID: "s1",
				}
				st.sessions["x"] = []model.AgentSession{
					{UUID: "s1", Status: model.SessionStatusCompleted, Ended: ptrTime(earlier)},
				}
			},
			want:     ReasonNewerIdea,
			wantNeed: true,
		},
		{
			name: "up to date — no regen",
			setup: func(st *staleFakeStore, idea *model.Idea) {
				idea.Updated = earlier
				st.summaries["x"] = model.Summary{
					GeneratedAt: now, SourceSessionUUID: "s1",
				}
				st.sessions["x"] = []model.AgentSession{
					{UUID: "s1", Status: model.SessionStatusCompleted, Ended: ptrTime(earlier)},
				}
			},
			wantNeed: false,
		},
		{
			name: "running session ignored, sidecar still up to date",
			setup: func(st *staleFakeStore, _ *model.Idea) {
				st.summaries["x"] = model.Summary{
					GeneratedAt: now, SourceSessionUUID: "s1",
				}
				st.sessions["x"] = []model.AgentSession{
					{UUID: "s2-running", Status: model.SessionStatusRunning, Started: later},
					{UUID: "s1", Status: model.SessionStatusCompleted, Ended: ptrTime(earlier), Started: earlier},
				}
			},
			wantNeed: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			st := newStaleFakeStore()
			idea := model.Idea{Slug: "x", Name: "X"}
			c.setup(st, &idea)
			st.ideas = []model.Idea{idea}

			reason, needs, err := NeedsRegeneration(context.Background(), st, idea, c.force)
			if err != nil {
				t.Fatalf("NeedsRegeneration: %v", err)
			}
			if needs != c.wantNeed {
				t.Errorf("needs = %v, want %v", needs, c.wantNeed)
			}
			if needs && reason != c.want {
				t.Errorf("reason = %q, want %q", reason, c.want)
			}
		})
	}
}

func TestEnqueueStale_OnlyStaleIdeasEnqueue(t *testing.T) {
	t.Parallel()
	st := newStaleFakeStore()
	now := time.Now()
	earlier := now.Add(-time.Hour)
	// Three ideas:
	//   stale: missing sidecar
	//   fresh: up to date
	//   newer: newer session than sidecar
	st.ideas = []model.Idea{
		{Slug: "stale", Name: "Stale"},
		{Slug: "fresh", Name: "Fresh"},
		{Slug: "newer", Name: "Newer"},
	}
	st.sessions["stale"] = []model.AgentSession{
		{UUID: "a", Status: model.SessionStatusCompleted, Ended: ptrTime(earlier)},
	}
	st.summaries["fresh"] = model.Summary{
		GeneratedAt: now, SourceSessionUUID: "b",
	}
	st.sessions["fresh"] = []model.AgentSession{
		{UUID: "b", Status: model.SessionStatusCompleted, Ended: ptrTime(earlier)},
	}
	st.summaries["newer"] = model.Summary{
		GeneratedAt: earlier, SourceSessionUUID: "c1",
	}
	st.sessions["newer"] = []model.AgentSession{
		{UUID: "c2", Status: model.SessionStatusCompleted, Ended: ptrTime(now), Started: now},
		{UUID: "c1", Status: model.SessionStatusCompleted, Ended: ptrTime(earlier), Started: earlier},
	}

	gen := &fakeGenerator{line: "ok"}
	s := New(gen, st)
	s.Start(context.Background(), 1)
	defer s.Stop()

	enqueued, errs := s.EnqueueStale(context.Background(), st, false)
	if errs != 0 {
		t.Errorf("errs = %d, want 0", errs)
	}
	if enqueued != 2 {
		t.Errorf("enqueued = %d, want 2 (stale + newer)", enqueued)
	}

	// Two sidecars should land within a beat.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		got := len(st.written)
		st.mu.Unlock()
		if got >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.written["stale"]; !ok {
		t.Errorf("stale never got written")
	}
	if _, ok := st.written["newer"]; !ok {
		t.Errorf("newer never got written")
	}
	if _, ok := st.written["fresh"]; ok {
		t.Errorf("fresh was regenerated but shouldn't have been")
	}
}

func TestEnqueueStale_ForceRegeneratesAll(t *testing.T) {
	t.Parallel()
	st := newStaleFakeStore()
	now := time.Now()
	earlier := now.Add(-time.Hour)
	st.ideas = []model.Idea{{Slug: "x"}, {Slug: "y"}}
	for _, slug := range []string{"x", "y"} {
		st.sessions[slug] = []model.AgentSession{
			{UUID: slug + "-s", Status: model.SessionStatusCompleted, Ended: ptrTime(earlier)},
		}
		st.summaries[slug] = model.Summary{
			GeneratedAt: now, SourceSessionUUID: slug + "-s",
		}
	}

	gen := &fakeGenerator{line: "ok"}
	s := New(gen, st)
	s.Start(context.Background(), 2)
	defer s.Stop()

	enqueued, _ := s.EnqueueStale(context.Background(), st, true)
	if enqueued != 2 {
		t.Errorf("enqueued = %d, want 2 (force)", enqueued)
	}
}

func TestEnqueueStale_ListError(t *testing.T) {
	t.Parallel()
	// staleFakeStore.List doesn't error today; use a wrapper that does.
	st := &listErrStore{newStaleFakeStore()}

	gen := &fakeGenerator{line: "ok"}
	s := New(gen, st)
	s.Start(context.Background(), 1)
	defer s.Stop()

	enqueued, errs := s.EnqueueStale(context.Background(), st, false)
	if enqueued != 0 || errs != 1 {
		t.Errorf("enqueued=%d errs=%d, want 0 / 1", enqueued, errs)
	}
}

type listErrStore struct{ *staleFakeStore }

func (l *listErrStore) List(_ context.Context) ([]model.Idea, error) {
	return nil, errors.New("disk on fire")
}

func ptrTime(t time.Time) *time.Time { return &t }
