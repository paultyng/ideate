package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/paultyng/ideate/internal/atomicfile"
	"github.com/paultyng/ideate/internal/review"
)

// ErrReviewInProgress is the sentinel returned by CreateOrReopen* when any
// pending review exists (any kind, any path or range — the global "one
// pending review at a time" rule). Use errors.Is to detect; errors.As with
// *ReviewInProgressError to extract the in-progress review's metadata so
// the caller can decide whether the conflict is its own review (poll the
// existing ID) or a different one (wait, retry, or surface to user).
var ErrReviewInProgress = errors.New("review already in progress")

// ReviewInProgressError carries identifying metadata about the pending
// review that blocked a new request. The kind discriminator selects which
// payload fields are meaningful — Path for markdown, Repo/BaseCommit/
// HeadCommit/HeadRef for diff. Other fields are zero values.
type ReviewInProgressError struct {
	ID   string
	Kind review.Kind

	// Markdown-specific
	Path string

	// Diff-specific
	Repo       string
	BaseCommit string
	HeadCommit string
	HeadRef    string
}

func (e *ReviewInProgressError) Error() string {
	return fmt.Sprintf("review already in progress: %s", e.ID)
}

func (e *ReviewInProgressError) Unwrap() error {
	return ErrReviewInProgress
}

func newReviewInProgressError(r *review.Review) *ReviewInProgressError {
	e := &ReviewInProgressError{ID: r.ID, Kind: r.Kind}
	switch r.Kind {
	case review.KindMarkdown:
		if r.Markdown != nil {
			e.Path = r.Markdown.Path
		}
	case review.KindDiff:
		e.Repo = r.Repo
		e.BaseCommit = r.BaseCommit
		e.HeadCommit = r.HeadCommit
		e.HeadRef = r.HeadRef
	}
	return e
}

// CreateDiffReview initializes a new pending diff review.
func (s *FSStore) CreateDiffReview(opts review.CreateOpts) (*review.Review, error) {
	id := review.GenerateReviewID(opts.BaseCommit, opts.HeadCommit, opts.HeadRef)

	r := &review.Review{
		ID:         id,
		Kind:       review.KindDiff,
		Status:     review.ReviewPending,
		Created:    time.Now(),
		Session:    opts.SessionID,
		IdeaSlug:   opts.IdeaSlug,
		Repo:       opts.Repo,
		BaseCommit: opts.BaseCommit,
		HeadCommit: opts.HeadCommit,
		HeadRef:    opts.HeadRef,
	}

	if err := s.writeReview(r); err != nil {
		return nil, err
	}
	return r, nil
}

// ReadReview loads a review record by ID.
func (s *FSStore) ReadReview(id string) (*review.Review, error) {
	if err := review.ValidID(id); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.reviewPath(id))
	if err != nil {
		return nil, fmt.Errorf("reading review: %w", err)
	}
	var r review.Review
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parsing review: %w", err)
	}
	if r.Kind == "" {
		// Records written before the Kind discriminator existed are diff reviews.
		r.Kind = review.KindDiff
	}
	return &r, nil
}

// SubmitDiffReview marks a diff review complete with comments and summary.
// The event must be APPROVE, REQUEST_CHANGES, or COMMENT.
func (s *FSStore) SubmitDiffReview(id, event, body string, comments []review.ReviewComment) (*review.Review, error) {
	if !review.ValidEvent(event) {
		return nil, fmt.Errorf("invalid review event %q (want APPROVE, REQUEST_CHANGES, or COMMENT)", event)
	}
	r, err := s.ReadReview(id)
	if err != nil {
		return nil, err
	}
	if r.Status != review.ReviewPending {
		return nil, fmt.Errorf("review %q is %s, not pending", id, r.Status)
	}

	now := time.Now()
	r.Status = review.ReviewComplete
	r.Completed = &now
	r.Event = event
	r.Body = body
	r.Comments = comments
	// Drafts are superseded by the submitted record.
	r.DraftBody = ""
	r.DraftComments = nil

	if err := s.writeReview(r); err != nil {
		return nil, err
	}
	return r, nil
}

// SaveReviewDraft persists the human's in-progress diff-review edits without
// changing status. No-op if the review isn't pending — submit/cancel races
// shouldn't clobber a terminal record.
func (s *FSStore) SaveReviewDraft(id, body string, comments []review.ReviewComment) (*review.Review, error) {
	r, err := s.ReadReview(id)
	if err != nil {
		return nil, err
	}
	if r.Status != review.ReviewPending {
		return r, nil
	}
	r.DraftBody = body
	r.DraftComments = comments
	if err := s.writeReview(r); err != nil {
		return nil, err
	}
	return r, nil
}

// SaveMarkdownReviewDraft persists the human's in-progress markdown edits
// (CriticMarkup-encoded body + summary). No-op if the review isn't pending
// or isn't a markdown review.
func (s *FSStore) SaveMarkdownReviewDraft(id, body, markedUp string) (*review.Review, error) {
	r, err := s.ReadReview(id)
	if err != nil {
		return nil, err
	}
	if r.Status != review.ReviewPending || r.Kind != review.KindMarkdown {
		return r, nil
	}
	if r.Markdown == nil {
		r.Markdown = &review.MarkdownPayload{}
	}
	r.DraftBody = body
	r.Markdown.DraftMarkedUp = markedUp
	if err := s.writeReview(r); err != nil {
		return nil, err
	}
	return r, nil
}

// ReopenReview resets a cancelled or pending review back to pending. Body,
// Event, and Comments are cleared so a fresh round starts clean. If
// sessionID is non-empty the review's Session is refreshed to attribute the
// new round to the calling agent.
func (s *FSStore) ReopenReview(id, sessionID string) (*review.Review, error) {
	r, err := s.ReadReview(id)
	if err != nil {
		return nil, err
	}
	if r.Status == review.ReviewComplete {
		return nil, fmt.Errorf("review %q is complete, cannot reopen", id)
	}

	r.Status = review.ReviewPending
	r.Completed = nil
	r.Body = ""
	r.Event = ""
	r.Comments = nil
	r.DraftBody = ""
	r.DraftComments = nil
	if sessionID != "" {
		r.Session = sessionID
	}

	if err := s.writeReview(r); err != nil {
		return nil, err
	}
	return r, nil
}

// CreateOrReopenDiffReview returns an existing cancelled diff review for the
// given commits/ref (Reopened — the iterate-after-cancel path) or creates a
// new one. Returns *ReviewInProgressError (matching ErrReviewInProgress) if
// the same scope (per-session, or the CLI/sessionless scope) already has a
// pending review — orchestration (poll the blocking review or cancel it) is
// the caller's job. Used at every entry point (CLI, MCP) so the dedup
// logic lives in one place.
func (s *FSStore) CreateOrReopenDiffReview(opts review.CreateOpts) (*review.Review, bool, error) {
	if pending, err := s.findPendingReviewForSession(opts.SessionID); err == nil && pending != nil {
		return nil, false, newReviewInProgressError(pending)
	}

	id := review.GenerateReviewID(opts.BaseCommit, opts.HeadCommit, opts.HeadRef)
	if existing, err := s.ReadReview(id); err == nil {
		// Pending was already filtered above; same-ID record here is
		// cancelled (reopen) or complete (overwrite).
		if existing.Status == review.ReviewCancelled {
			r, err := s.ReopenReview(id, opts.SessionID)
			if err != nil {
				return nil, false, err
			}
			return r, true, nil
		}
		// Status is Complete: fall through to overwrite. (Tracked separately
		// — completed reviews should arguably be preserved for history.)
	}
	r, err := s.CreateDiffReview(opts)
	if err != nil {
		return nil, false, err
	}
	return r, false, nil
}

// CreateMarkdownReview initializes a new pending markdown review.
func (s *FSStore) CreateMarkdownReview(opts review.MarkdownCreateOpts) (*review.Review, error) {
	id := review.GenerateMarkdownReviewID(opts.Path)
	r := &review.Review{
		ID:       id,
		Kind:     review.KindMarkdown,
		Status:   review.ReviewPending,
		Created:  time.Now(),
		Session:  opts.SessionID,
		IdeaSlug: opts.IdeaSlug,
		Markdown: &review.MarkdownPayload{
			Path:     opts.Path,
			Original: opts.Original,
		},
	}
	if err := s.writeReview(r); err != nil {
		return nil, err
	}
	return r, nil
}

// CreateOrReopenMarkdownReview returns an existing cancelled markdown review
// for the same file path (Reopened — iterate-after-cancel) or creates a new
// one. Returns *ReviewInProgressError (matching ErrReviewInProgress) if the
// same scope (per-session, or the CLI/sessionless scope) already has a
// pending review — caller orchestrates. Reopen refreshes Markdown.Original
// to the latest content snapshot so the new round reflects the file's
// current state.
func (s *FSStore) CreateOrReopenMarkdownReview(opts review.MarkdownCreateOpts) (*review.Review, bool, error) {
	if pending, err := s.findPendingReviewForSession(opts.SessionID); err == nil && pending != nil {
		return nil, false, newReviewInProgressError(pending)
	}

	id := review.GenerateMarkdownReviewID(opts.Path)
	if existing, err := s.ReadReview(id); err == nil {
		// Pending was already filtered above; same-ID record here is
		// cancelled (reopen) or complete (overwrite).
		if existing.Status == review.ReviewCancelled {
			existing.Status = review.ReviewPending
			existing.Completed = nil
			existing.Body = ""
			existing.Event = ""
			existing.DraftBody = ""
			existing.DraftComments = nil
			existing.Markdown = &review.MarkdownPayload{
				Path:     opts.Path,
				Original: opts.Original,
			}
			if opts.SessionID != "" {
				existing.Session = opts.SessionID
			}
			if opts.IdeaSlug != "" {
				existing.IdeaSlug = opts.IdeaSlug
			}
			if err := s.writeReview(existing); err != nil {
				return nil, false, err
			}
			return existing, true, nil
		}
		// Status is Complete: fall through to overwrite. (Tracked separately.)
	}
	r, err := s.CreateMarkdownReview(opts)
	if err != nil {
		return nil, false, err
	}
	return r, false, nil
}

// SubmitMarkdownReview marks a markdown review complete with the human's
// CriticMarkup-encoded edited content and an optional summary. The event
// must be APPROVE, REQUEST_CHANGES, or COMMENT (same vocab as diff reviews).
func (s *FSStore) SubmitMarkdownReview(id, event, body, markedUp string) (*review.Review, error) {
	if !review.ValidEvent(event) {
		return nil, fmt.Errorf("invalid review event %q (want APPROVE, REQUEST_CHANGES, or COMMENT)", event)
	}
	r, err := s.ReadReview(id)
	if err != nil {
		return nil, err
	}
	if r.Status != review.ReviewPending {
		return nil, fmt.Errorf("review %q is %s, not pending", id, r.Status)
	}
	if r.Kind != review.KindMarkdown {
		return nil, fmt.Errorf("review %q is %s, not markdown", id, r.Kind)
	}
	if r.Markdown == nil {
		r.Markdown = &review.MarkdownPayload{}
	}

	now := time.Now()
	r.Status = review.ReviewComplete
	r.Completed = &now
	r.Event = event
	r.Body = body
	r.Markdown.MarkedUp = markedUp
	r.Markdown.Marks = review.NewCriticMarks(r.Markdown.Original, markedUp)
	// Drafts are superseded by the submitted record.
	r.DraftBody = ""
	r.Markdown.DraftMarkedUp = ""

	if err := s.writeReview(r); err != nil {
		return nil, err
	}
	return r, nil
}

// CancelReview marks a review as cancelled.
func (s *FSStore) CancelReview(id string) (*review.Review, error) {
	r, err := s.ReadReview(id)
	if err != nil {
		return nil, err
	}
	if r.Status != review.ReviewPending {
		return nil, fmt.Errorf("review %q is %s, not pending", id, r.Status)
	}

	now := time.Now()
	r.Status = review.ReviewCancelled
	r.Completed = &now
	// Drafts are no longer meaningful once the review is closed; drop them
	// so a later Reopen starts clean.
	r.DraftBody = ""
	r.DraftComments = nil
	if r.Markdown != nil {
		r.Markdown.DraftMarkedUp = ""
	}

	if err := s.writeReview(r); err != nil {
		return nil, err
	}
	return r, nil
}

// findPendingReviewForSession returns the first pending review owned by the
// given sessionID (empty == CLI/sessionless scope), or nil if none. Used by
// CreateOrReopen* to enforce the at-most-one-pending-per-session invariant.
// Two sessions can each have a pending review concurrently; the CLI-scope
// (Session=="") still acts like a single bucket so back-to-back CLI calls
// still block each other.
func (s *FSStore) findPendingReviewForSession(sessionID string) (*review.Review, error) {
	if s.reviewsDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(s.reviewsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading reviews dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		r, err := s.ReadReview(id)
		if err != nil {
			continue
		}
		if r.Status == review.ReviewPending && r.Session == sessionID {
			return r, nil
		}
	}
	return nil, nil
}

// ListPendingReviews returns IDs of every review currently in pending status.
// Used on shutdown and startup-recovery to cancel orphaned reviews so polling
// agents don't wait forever.
func (s *FSStore) ListPendingReviews() ([]string, error) {
	if s.reviewsDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(s.reviewsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading reviews dir: %w", err)
	}

	var pending []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		r, err := s.ReadReview(id)
		if err != nil {
			continue
		}
		if r.Status == review.ReviewPending {
			pending = append(pending, id)
		}
	}
	return pending, nil
}

// ListPendingReviewsFull returns full review records for every pending review.
// Used by the startup sweep to apply selective cancellation policy without
// re-reading each record.
func (s *FSStore) ListPendingReviewsFull() ([]*review.Review, error) {
	if s.reviewsDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(s.reviewsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading reviews dir: %w", err)
	}
	var pending []*review.Review
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		r, err := s.ReadReview(id)
		if err != nil {
			continue
		}
		if r.Status == review.ReviewPending {
			pending = append(pending, r)
		}
	}
	return pending, nil
}

// CancelPendingReviews sweeps every pending review and marks it cancelled.
// Returns the IDs that were successfully cancelled.
func (s *FSStore) CancelPendingReviews() []string {
	ids, err := s.ListPendingReviews()
	if err != nil {
		slog.Warn("listing pending reviews", slog.Any("err", err))
		return nil
	}
	var cancelled []string
	for _, id := range ids {
		if _, err := s.CancelReview(id); err != nil {
			slog.Debug("cancelling pending review", slog.String("id", id), slog.Any("err", err))
			continue
		}
		cancelled = append(cancelled, id)
	}
	return cancelled
}

func (s *FSStore) reviewPath(id string) string {
	return filepath.Join(s.reviewsDir, id+".json")
}

// writeReview atomically writes a review record.
func (s *FSStore) writeReview(r *review.Review) error {
	if err := review.ValidID(r.ID); err != nil {
		return err
	}
	if err := os.MkdirAll(s.reviewsDir, 0o700); err != nil {
		return fmt.Errorf("creating reviews dir: %w", err)
	}

	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling review: %w", err)
	}
	data = append(data, '\n')

	return atomicfile.Write(s.reviewPath(r.ID), data, 0o600)
}
