package app

import (
	"context"
	"log/slog"
	"path/filepath"
	"sort"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	ideateMCP "github.com/paultyng/ideate/internal/mcp"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/review"
)

func (a *App) GetLocalDiff(repoPath, base, head string) (*review.DiffResult, error) {
	src := &review.LocalSource{RepoPath: repoPath, Base: base, Head: head}
	return src.GetDiff(a.ctx)
}

// GetReview reads a review record from the central reviews store by ID.

func (a *App) GetReview(reviewID string) (*review.Review, error) {
	return a.svc.ReadReview(reviewID)
}

// SubmitDiffReview marks a diff review as complete with comments and summary.

func (a *App) SubmitDiffReview(reviewID, event, body string, comments []review.ReviewComment) (*review.Review, error) {
	r, err := a.svc.SubmitDiffReview(reviewID, event, body, comments)
	if err != nil {
		return nil, err
	}
	a.clearReviewingState(r)
	if a.mcpManager != nil {
		a.mcpManager.NotifyReviewComplete(reviewID, r.Status)
	}
	a.emitReviewChanged(r)
	return r, nil
}

// SubmitMarkdownReview marks a markdown review as complete with the human's
// CriticMarkup-encoded edited content and an optional summary.

func (a *App) SubmitMarkdownReview(reviewID, event, body, markedUp string) (*review.Review, error) {
	r, err := a.svc.SubmitMarkdownReview(reviewID, event, body, markedUp)
	if err != nil {
		return nil, err
	}
	a.clearReviewingState(r)
	if a.mcpManager != nil {
		a.mcpManager.NotifyReviewComplete(reviewID, r.Status)
	}
	a.emitReviewChanged(r)
	return r, nil
}

// PendingReviewSummary is the slim shape rendered as a chip on the topbar.
// Strips the on-disk record down to just what the bar needs (id, kind,
// label, age) so we don't ship full file payloads or comment threads to
// the frontend just to draw a row of chips.
type PendingReviewSummary struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`    // "diff" or "markdown"
	Label    string `json:"label"`   // file basename for markdown; "<repo> <base>..<head>" for diff
	Created  string `json:"created"` // RFC3339; bar uses for age tooltip + sort
	IdeaSlug string `json:"ideaSlug,omitempty"`
	// Path is the absolute filesystem path of the file under review.
	// Set only for markdown reviews; empty for diffs. Idea-detail
	// sidebar uses it to dot the matching file row.
	Path string `json:"path,omitempty"`
}

// ListPendingReviews returns every review currently in pending status,
// most-recent first. The frontend renders these as chips in the topbar so
// the user can see open reviews regardless of whether they're linked to a
// running session.

func (a *App) ListPendingReviews() []PendingReviewSummary {
	reviews, err := a.svc.ListPendingReviewsFull()
	if err != nil {
		slog.Warn("listing pending reviews", slog.Any("err", err))
		return nil
	}
	out := make([]PendingReviewSummary, 0, len(reviews))
	for _, r := range reviews {
		var path string
		if r.Kind == review.KindMarkdown && r.Markdown != nil {
			path = r.Markdown.Path
		}
		out = append(out, PendingReviewSummary{
			ID:       r.ID,
			Kind:     string(r.Kind),
			Label:    pendingReviewLabel(r),
			Created:  r.Created.Format(time.RFC3339Nano),
			IdeaSlug: r.IdeaSlug,
			Path:     path,
		})
	}
	// Sort newest-first so a freshly-requested review jumps to the
	// leftmost slot — matches the session bar's recency-first ordering.
	sort.Slice(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out
}

// pendingReviewLabel renders the chip's primary label. Diff reviews show
// "<repoLeaf> <base7>..<head7>" so the user can disambiguate multiple
// concurrent reviews on different repos / branches; markdown reviews show
// the file basename.
func pendingReviewLabel(r *review.Review) string {
	switch r.Kind {
	case review.KindMarkdown:
		if r.Markdown != nil && r.Markdown.Path != "" {
			return filepath.Base(r.Markdown.Path)
		}
		return "markdown"
	case review.KindDiff:
		base, head := r.BaseCommit, r.HeadCommit
		if len(base) > 7 {
			base = base[:7]
		}
		if len(head) > 7 {
			head = head[:7]
		}
		repoLeaf := filepath.Base(r.Repo)
		if repoLeaf == "" || repoLeaf == "." {
			return base + ".." + head
		}
		return repoLeaf + " " + base + ".." + head
	default:
		return r.ID
	}
}

// SaveReviewDraft persists in-progress diff-review edits (summary text +
// inline comments) without changing review status. Frontend autosaves
// after each edit so the draft survives an app close before submit.

func (a *App) SaveReviewDraft(reviewID, body string, comments []review.ReviewComment) (*review.Review, error) {
	return a.svc.SaveReviewDraft(reviewID, body, comments)
}

// SaveMarkdownReviewDraft persists in-progress markdown-review edits (summary
// text + CriticMarkup-encoded body) without changing review status.

func (a *App) SaveMarkdownReviewDraft(reviewID, body, markedUp string) (*review.Review, error) {
	return a.svc.SaveMarkdownReviewDraft(reviewID, body, markedUp)
}

// CancelReview marks a review as cancelled, regardless of kind.

func (a *App) CancelReview(reviewID string) (*review.Review, error) {
	r, err := a.svc.CancelReview(reviewID)
	if err != nil {
		return nil, err
	}
	a.clearReviewingState(r)
	if a.mcpManager != nil {
		a.mcpManager.NotifyReviewComplete(reviewID, r.Status)
	}
	a.emitReviewChanged(r)
	return r, nil
}

// emitReviewChanged fires the review:changed Wails event so views like
// PendingReviewsBar can refetch in response to a status flip without
// polling on a tight interval. Coarse signal — frontend looks up the
// new state via ListPendingReviews / GetReview rather than trusting the
// payload as the source of truth.

func (a *App) emitReviewChanged(r *review.Review) {
	if a.ctx == nil || r == nil {
		return
	}
	wailsRuntime.EventsEmit(a.ctx, ideateMCP.EventReviewChanged, map[string]any{
		"review_id": r.ID,
		"status":    string(r.Status),
	})
}

// clearReviewingState clears Activity=reviewing + ActiveReviewID on the
// session record that owns this review. Looks up by slug + matching
// ActiveReviewID rather than going through the live coordinator — that
// way submit/cancel still work after the agent has exited (the
// coordinator drops dead sessions from its map). Best-effort.

func (a *App) clearReviewingState(r *review.Review) {
	if r == nil || r.IdeaSlug == "" {
		return
	}
	sessions, err := a.svc.ListSessions(a.ctx, r.IdeaSlug)
	if err != nil {
		slog.Warn("listing sessions for review clear",
			slog.String("review", r.ID), slog.String("slug", r.IdeaSlug),
			slog.Any("err", err))
		return
	}
	for _, s := range sessions {
		if s.ActiveReviewID != r.ID {
			continue
		}
		if err := a.svc.ClearSessionReview(a.ctx, r.IdeaSlug, s.UUID); err != nil {
			slog.Warn("clearing session reviewing state",
				slog.String("review", r.ID), slog.String("slug", r.IdeaSlug),
				slog.String("uuid", s.UUID), slog.Any("err", err))
		}
		return
	}
}

// StartSession spawns a new agent session and returns its ID.

func (a *App) cancelStaleReviews() []string {
	ctx := context.Background()
	pending, err := a.svc.ListPendingReviewsFull()
	if err != nil {
		slog.Warn("listing pending reviews for staleness sweep", slog.Any("err", err))
		return nil
	}
	if len(pending) == 0 {
		return nil
	}

	now := time.Now()
	var cancelled []string
	for _, r := range pending {
		if !a.shouldCancelStaleReview(ctx, r, now) {
			continue
		}
		if _, err := a.svc.CancelReview(r.ID); err != nil {
			slog.Debug("cancelling stale pending review",
				slog.String("id", r.ID), slog.Any("err", err))
			continue
		}
		cancelled = append(cancelled, r.ID)
	}
	return cancelled
}

// shouldCancelStaleReview applies the keep-vs-cancel policy for a single
// pending review. Pulled out for testability.

func (a *App) shouldCancelStaleReview(ctx context.Context, r *review.Review, now time.Time) bool {
	if now.Sub(r.Created) > reviewStalenessAge {
		return true
	}
	// CLI / sessionless reviews have no agent watching them; keep pending
	// so the human can still submit. The 30-day cap above bounds growth.
	if r.Session == "" {
		return false
	}
	// Look up the linked session. Orchestrator reviews have IdeaSlug="" but
	// their session lives under the synthetic orchestrator slug.
	slug := r.IdeaSlug
	if slug == "" {
		slug = model.OrchestratorSlug
	}
	sess, err := a.svc.ReadSession(ctx, slug, r.Session)
	if err != nil {
		// Session record is gone — the agent isn't coming back to consume
		// the result. Cancel.
		return true
	}
	// Session will auto-resume → keep the review pending so the human's
	// edits survive the restart.
	if sess.StopReason == model.SessionStopReasonShutdown ||
		sess.StopReason == model.SessionStopReasonCrash {
		return false
	}
	// Dormant sessions are resumable on demand (via the quick switcher,
	// send_session_input auto-resume, or the session view). The same
	// UUID will be re-attached, so a pending review still has an agent
	// that may poll for its result.
	if sess.Status == model.SessionStatusDormant {
		return false
	}
	// Anything else (user|exit|cleared|compacted|orphaned, or a fresh
	// running record from concurrent activity) — the original UUID won't
	// be polling for this review again.
	return sess.Status != model.SessionStatusRunning
}

// ResumeCandidate records a session that should be picked up by the
// auto-resume sweep on startup. The frontend gets these via the
// crash-recovery toast event when Reason==crash.
type ResumeCandidate struct {
	Slug      string                  `json:"slug"`
	IdeaName  string                  `json:"ideaName"`
	UUID      string                  `json:"uuid"`
	AgentType string                  `json:"agentType"`
	Reason    model.SessionStopReason `json:"reason"`
}

// finalizeSession marks a session as completed/stopped by its stable UUID.
// Dormancy is no longer a finalize-time outcome — it is set only by the
// adoption sweep on next launch (for sessions whose PID is dead). Drain
// any stashed stop reason so the coordinator's map doesn't leak.
