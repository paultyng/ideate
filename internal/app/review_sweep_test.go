package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/review"
	"github.com/paultyng/ideate/internal/service"
	"github.com/paultyng/ideate/internal/store"
)

// newReviewTestApp builds an App with just a store, enough to exercise the
// review sweep policy. No coordinator needed — the sweep only inspects
// persisted records.
func newReviewTestApp(t *testing.T) (*App, *store.FSStore) {
	t.Helper()
	ideasDir := t.TempDir()
	reviewsDir := filepath.Join(t.TempDir(), "reviews")
	s := store.NewFSStore(ideasDir, reviewsDir, "pt/", "")
	return &App{ctx: context.Background(), store: s, svc: service.New(s, nil), ideasDir: ideasDir}, s
}

func TestShouldCancelStaleReview(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)

	type sessionSpec struct {
		slug       string
		uuid       string
		status     model.SessionStatus
		stopReason model.SessionStopReason
	}

	cases := []struct {
		name    string
		setup   *sessionSpec // nil → no session record on disk
		review  *review.Review
		wantCxl bool
	}{
		{
			name:    "stale CLI review past 30 days",
			review:  &review.Review{ID: "r1", Status: review.ReviewPending, Created: now.Add(-31 * 24 * time.Hour)},
			wantCxl: true,
		},
		{
			name:    "fresh CLI review under threshold kept",
			review:  &review.Review{ID: "r2", Status: review.ReviewPending, Created: now.Add(-1 * time.Hour)},
			wantCxl: false,
		},
		{
			name:    "fresh sessionless review at boundary kept",
			review:  &review.Review{ID: "r3", Status: review.ReviewPending, Created: now.Add(-30*24*time.Hour + time.Minute)},
			wantCxl: false,
		},
		{
			name: "session linked, will auto-resume (shutdown) → keep",
			setup: &sessionSpec{
				slug: "alpha", uuid: "ses-alpha-1",
				status: model.SessionStatusStopped, stopReason: model.SessionStopReasonShutdown,
			},
			review:  &review.Review{ID: "r4", Status: review.ReviewPending, Created: now, Session: "ses-alpha-1", IdeaSlug: "alpha"},
			wantCxl: false,
		},
		{
			name: "session linked, will auto-resume (crash) → keep",
			setup: &sessionSpec{
				slug: "alpha", uuid: "ses-alpha-2",
				status: model.SessionStatusStopped, stopReason: model.SessionStopReasonCrash,
			},
			review:  &review.Review{ID: "r5", Status: review.ReviewPending, Created: now, Session: "ses-alpha-2", IdeaSlug: "alpha"},
			wantCxl: false,
		},
		{
			name: "session linked, user-stopped → cancel",
			setup: &sessionSpec{
				slug: "alpha", uuid: "ses-alpha-3",
				status: model.SessionStatusStopped, stopReason: model.SessionStopReasonUser,
			},
			review:  &review.Review{ID: "r6", Status: review.ReviewPending, Created: now, Session: "ses-alpha-3", IdeaSlug: "alpha"},
			wantCxl: true,
		},
		{
			name: "session linked, agent exited cleanly → cancel",
			setup: &sessionSpec{
				slug: "alpha", uuid: "ses-alpha-4",
				status: model.SessionStatusCompleted, stopReason: model.SessionStopReasonExit,
			},
			review:  &review.Review{ID: "r7", Status: review.ReviewPending, Created: now, Session: "ses-alpha-4", IdeaSlug: "alpha"},
			wantCxl: true,
		},
		{
			name: "session linked, /clear ended → cancel (new UUID polls, not this one)",
			setup: &sessionSpec{
				slug: "alpha", uuid: "ses-alpha-5",
				status: model.SessionStatusStopped, stopReason: model.SessionStopReasonCleared,
			},
			review:  &review.Review{ID: "r8", Status: review.ReviewPending, Created: now, Session: "ses-alpha-5", IdeaSlug: "alpha"},
			wantCxl: true,
		},
		{
			name: "session linked, transcript orphaned → cancel",
			setup: &sessionSpec{
				slug: "alpha", uuid: "ses-alpha-6",
				status: model.SessionStatusStopped, stopReason: model.SessionStopReasonOrphaned,
			},
			review:  &review.Review{ID: "r9", Status: review.ReviewPending, Created: now, Session: "ses-alpha-6", IdeaSlug: "alpha"},
			wantCxl: true,
		},
		{
			name:    "session linked but record missing → cancel as orphan",
			review:  &review.Review{ID: "r10", Status: review.ReviewPending, Created: now, Session: "ghost", IdeaSlug: "alpha"},
			wantCxl: true,
		},
		{
			name: "orchestrator session resolved via synthetic slug",
			setup: &sessionSpec{
				slug: model.OrchestratorSlug, uuid: "ses-scratch-1",
				status: model.SessionStatusStopped, stopReason: model.SessionStopReasonShutdown,
			},
			review:  &review.Review{ID: "r11", Status: review.ReviewPending, Created: now, Session: "ses-scratch-1" /* IdeaSlug intentionally empty */},
			wantCxl: false,
		},
		{
			name: "stale beats keep — even resumable session past threshold cancels",
			setup: &sessionSpec{
				slug: "alpha", uuid: "ses-alpha-7",
				status: model.SessionStatusStopped, stopReason: model.SessionStopReasonShutdown,
			},
			review:  &review.Review{ID: "r12", Status: review.ReviewPending, Created: now.Add(-31 * 24 * time.Hour), Session: "ses-alpha-7", IdeaSlug: "alpha"},
			wantCxl: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, s := newReviewTestApp(t)

			if tc.setup != nil {
				// Bare-minimum idea record — sessions live under <ideasDir>/<slug>/sessions/.
				if tc.setup.slug != model.OrchestratorSlug {
					if err := s.Create(a.ctx, &model.Idea{Name: tc.setup.slug, Status: model.StatusActive}); err != nil {
						t.Fatalf("Create idea: %v", err)
					}
				}
				sess := model.AgentSession{
					UUID:       tc.setup.uuid,
					Agent:      "claude-code",
					Status:     tc.setup.status,
					StopReason: tc.setup.stopReason,
					Started:    now.Add(-2 * time.Hour),
				}
				if err := s.WriteSession(a.ctx, tc.setup.slug, tc.setup.uuid, sess); err != nil {
					t.Fatalf("WriteSession: %v", err)
				}
			}

			got := a.shouldCancelStaleReview(a.ctx, tc.review, now)
			if got != tc.wantCxl {
				t.Errorf("shouldCancelStaleReview = %v, want %v", got, tc.wantCxl)
			}
		})
	}
}
