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

// staleFakeStore drives NeedsRegeneration + EnqueueStale off idea
// state directly: idea.Description ("" = no summary yet) and
// idea.Updated (bumped at write time, doubling as the last
// generation timestamp) rather than a separate sidecar record.
type staleFakeStore struct {
	mu       sync.Mutex
	ideas    []model.Idea
	sessions map[string][]model.AgentSession
	updated  map[string]*model.Idea
}

func newStaleFakeStore() *staleFakeStore {
	return &staleFakeStore{
		sessions: map[string][]model.AgentSession{},
		updated:  map[string]*model.Idea{},
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

func (f *staleFakeStore) Update(_ context.Context, idea *model.Idea) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *idea
	f.updated[idea.Slug] = &cp
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
			setup: func(st *staleFakeStore, idea *model.Idea) {
				idea.Description = "fresh"
				idea.Updated = now
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
			name: "session exists but no description",
			setup: func(st *staleFakeStore, _ *model.Idea) {
				st.sessions["x"] = []model.AgentSession{
					{UUID: "s1", Status: model.SessionStatusCompleted, Ended: ptrTime(earlier)},
				}
			},
			want:     ReasonMissing,
			wantNeed: true,
		},
		{
			name: "newer session than last generation",
			setup: func(st *staleFakeStore, idea *model.Idea) {
				idea.Description = "stale line"
				idea.Updated = earlier
				st.sessions["x"] = []model.AgentSession{
					{UUID: "s2", Status: model.SessionStatusCompleted, Ended: ptrTime(now), Started: now},
					{UUID: "s1", Status: model.SessionStatusCompleted, Ended: ptrTime(earlier), Started: earlier},
				}
			},
			want:     ReasonNewerSession,
			wantNeed: true,
		},
		{
			name: "up to date — no regen",
			setup: func(st *staleFakeStore, idea *model.Idea) {
				idea.Description = "fresh line"
				idea.Updated = later
				st.sessions["x"] = []model.AgentSession{
					{UUID: "s1", Status: model.SessionStatusCompleted, Ended: ptrTime(earlier)},
				}
			},
			wantNeed: false,
		},
		{
			name: "running session ignored, description still up to date",
			setup: func(st *staleFakeStore, idea *model.Idea) {
				idea.Description = "fresh line"
				idea.Updated = now
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
	//   stale: missing description
	//   fresh: up to date (description newer than the last ended session)
	//   newer: description present, but a newer session has ended since
	st.ideas = []model.Idea{
		{Slug: "stale", Name: "Stale"},
		{Slug: "fresh", Name: "Fresh", Description: "already summarized", Updated: now},
		{Slug: "newer", Name: "Newer", Description: "stale line", Updated: earlier},
	}
	st.sessions["stale"] = []model.AgentSession{
		{UUID: "a", Status: model.SessionStatusCompleted, Ended: ptrTime(earlier)},
	}
	st.sessions["fresh"] = []model.AgentSession{
		{UUID: "b", Status: model.SessionStatusCompleted, Ended: ptrTime(earlier)},
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

	// Two ideas should get their Description updated within a beat.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		got := len(st.updated)
		st.mu.Unlock()
		if got >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.updated["stale"]; !ok {
		t.Errorf("stale never got updated")
	}
	if _, ok := st.updated["newer"]; !ok {
		t.Errorf("newer never got updated")
	}
	if _, ok := st.updated["fresh"]; ok {
		t.Errorf("fresh was regenerated but shouldn't have been")
	}
}

func TestEnqueueStale_ForceRegeneratesAll(t *testing.T) {
	t.Parallel()
	st := newStaleFakeStore()
	now := time.Now()
	earlier := now.Add(-time.Hour)
	st.ideas = []model.Idea{
		{Slug: "x", Description: "fresh line", Updated: now},
		{Slug: "y", Description: "fresh line", Updated: now},
	}
	for _, slug := range []string{"x", "y"} {
		st.sessions[slug] = []model.AgentSession{
			{UUID: slug + "-s", Status: model.SessionStatusCompleted, Ended: ptrTime(earlier)},
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
