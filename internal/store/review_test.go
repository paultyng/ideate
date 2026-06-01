package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/paultyng/ideate/internal/review"
)

func newReviewStore(t *testing.T) (*FSStore, string) {
	t.Helper()
	dir := t.TempDir()
	return NewFSStore(filepath.Join(dir, "ideas"), filepath.Join(dir, "reviews"), "", ""), dir
}

func TestCreateReadDiffReview(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	r, err := s.CreateDiffReview(review.CreateOpts{
		BaseCommit: "aaa1111",
		HeadCommit: "bbb2222",
		HeadRef:    "feat/test",
		SessionID:  "ses-1",
		IdeaSlug:   "idea-x",
	})
	if err != nil {
		t.Fatalf("CreateDiffReview: %v", err)
	}

	if r.Status != review.ReviewPending {
		t.Errorf("status = %q, want %q", r.Status, review.ReviewPending)
	}
	if r.Kind != review.KindDiff {
		t.Errorf("kind = %q, want %q", r.Kind, review.KindDiff)
	}
	if r.Session != "ses-1" {
		t.Errorf("session = %q", r.Session)
	}
	if r.IdeaSlug != "idea-x" {
		t.Errorf("idea_slug = %q", r.IdeaSlug)
	}

	// File should exist at the central reviews dir.
	path := filepath.Join(s.ReviewsDir(), r.ID+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("review file not found: %v", err)
	}

	// Read it back.
	r2, err := s.ReadReview(r.ID)
	if err != nil {
		t.Fatalf("ReadReview: %v", err)
	}
	if r2.BaseCommit != "aaa1111" || r2.HeadCommit != "bbb2222" {
		t.Errorf("commits = %q..%q", r2.BaseCommit, r2.HeadCommit)
	}
}

func TestSubmitDiffReview(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	r, err := s.CreateDiffReview(review.CreateOpts{BaseCommit: "aaa1111", HeadCommit: "bbb2222", HeadRef: "main"})
	if err != nil {
		t.Fatalf("CreateDiffReview: %v", err)
	}

	comments := []review.ReviewComment{
		{Path: "foo.go", Line: 10, Side: "RIGHT", Body: "Fix this"},
		{Path: "bar.go", Line: 5, StartLine: intPtr(3), Side: "RIGHT", StartSide: "RIGHT", Body: "Refactor"},
	}

	r, err = s.SubmitDiffReview(r.ID, "REQUEST_CHANGES", "Needs work", comments)
	if err != nil {
		t.Fatalf("SubmitDiffReview: %v", err)
	}

	if r.Status != review.ReviewComplete {
		t.Errorf("status = %q, want %q", r.Status, review.ReviewComplete)
	}
	if r.Completed == nil {
		t.Error("completed is nil")
	}
	if r.Event != "REQUEST_CHANGES" {
		t.Errorf("event = %q", r.Event)
	}
	if len(r.Comments) != 2 {
		t.Fatalf("comments count = %d", len(r.Comments))
	}
	if r.Comments[0].Path != "foo.go" || r.Comments[0].Line != 10 {
		t.Errorf("comment[0] = %+v", r.Comments[0])
	}

	// Re-read from disk to verify atomic write.
	r2, err := s.ReadReview(r.ID)
	if err != nil {
		t.Fatalf("ReadReview after submit: %v", err)
	}
	if r2.Status != review.ReviewComplete {
		t.Errorf("re-read status = %q", r2.Status)
	}
	if len(r2.Comments) != 2 {
		t.Errorf("re-read comments = %d", len(r2.Comments))
	}
}

func TestSubmitDiffReviewNotPending(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	r, _ := s.CreateDiffReview(review.CreateOpts{BaseCommit: "aaa", HeadCommit: "bbb"})
	_, _ = s.SubmitDiffReview(r.ID, "APPROVE", "", nil)

	// Try to submit again — should fail.
	_, err := s.SubmitDiffReview(r.ID, "APPROVE", "", nil)
	if err == nil {
		t.Error("expected error submitting non-pending review")
	}
}

func TestCancelReview(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	r, _ := s.CreateDiffReview(review.CreateOpts{BaseCommit: "aaa", HeadCommit: "bbb"})
	r, err := s.CancelReview(r.ID)
	if err != nil {
		t.Fatalf("CancelReview: %v", err)
	}
	if r.Status != review.ReviewCancelled {
		t.Errorf("status = %q, want %q", r.Status, review.ReviewCancelled)
	}
	if r.Completed == nil {
		t.Error("completed is nil")
	}
}

func TestSaveReviewDraft_RoundTrip(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	r, err := s.CreateDiffReview(review.CreateOpts{BaseCommit: "aaa", HeadCommit: "bbb"})
	if err != nil {
		t.Fatalf("CreateDiffReview: %v", err)
	}

	drafted, err := s.SaveReviewDraft(r.ID, "in progress…", []review.ReviewComment{
		{Path: "foo.go", Line: 10, Side: "RIGHT", Body: "WIP comment"},
	})
	if err != nil {
		t.Fatalf("SaveReviewDraft: %v", err)
	}
	if drafted.Status != review.ReviewPending {
		t.Errorf("status drifted: %q", drafted.Status)
	}
	if drafted.DraftBody != "in progress…" {
		t.Errorf("draft_body = %q", drafted.DraftBody)
	}
	if len(drafted.DraftComments) != 1 || drafted.DraftComments[0].Body != "WIP comment" {
		t.Errorf("draft_comments = %+v", drafted.DraftComments)
	}

	// Re-read to confirm the draft survived to disk.
	reread, err := s.ReadReview(r.ID)
	if err != nil {
		t.Fatalf("ReadReview: %v", err)
	}
	if reread.DraftBody != "in progress…" || len(reread.DraftComments) != 1 {
		t.Errorf("draft fields not persisted: body=%q comments=%d", reread.DraftBody, len(reread.DraftComments))
	}
	// Authoritative fields must remain empty until submit.
	if reread.Body != "" || len(reread.Comments) != 0 {
		t.Errorf("non-draft fields leaked: body=%q comments=%d", reread.Body, len(reread.Comments))
	}
}

func TestSaveReviewDraft_NoOpWhenNotPending(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	r, _ := s.CreateDiffReview(review.CreateOpts{BaseCommit: "aaa", HeadCommit: "bbb"})
	if _, err := s.CancelReview(r.ID); err != nil {
		t.Fatalf("CancelReview: %v", err)
	}

	got, err := s.SaveReviewDraft(r.ID, "should not stick", nil)
	if err != nil {
		t.Fatalf("SaveReviewDraft: %v", err)
	}
	if got.DraftBody != "" {
		t.Errorf("draft_body should be empty on cancelled review, got %q", got.DraftBody)
	}
}

func TestSubmitDiffReview_ClearsDrafts(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	r, _ := s.CreateDiffReview(review.CreateOpts{BaseCommit: "aaa", HeadCommit: "bbb"})
	if _, err := s.SaveReviewDraft(r.ID, "WIP body", []review.ReviewComment{
		{Path: "foo.go", Line: 5, Side: "RIGHT", Body: "WIP"},
	}); err != nil {
		t.Fatalf("SaveReviewDraft: %v", err)
	}

	final, err := s.SubmitDiffReview(r.ID, "REQUEST_CHANGES", "Final body", []review.ReviewComment{
		{Path: "foo.go", Line: 5, Side: "RIGHT", Body: "Real comment"},
	})
	if err != nil {
		t.Fatalf("SubmitDiffReview: %v", err)
	}
	if final.DraftBody != "" || len(final.DraftComments) != 0 {
		t.Errorf("drafts not cleared on submit: body=%q comments=%d", final.DraftBody, len(final.DraftComments))
	}
	if final.Body != "Final body" || len(final.Comments) != 1 {
		t.Errorf("submitted fields wrong: body=%q comments=%d", final.Body, len(final.Comments))
	}
}

func TestSaveMarkdownReviewDraft_RoundTrip(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	r, err := s.CreateMarkdownReview(review.MarkdownCreateOpts{
		Path:     "/tmp/notes.md",
		Original: "# Hello\n",
	})
	if err != nil {
		t.Fatalf("CreateMarkdownReview: %v", err)
	}

	drafted, err := s.SaveMarkdownReviewDraft(r.ID, "in progress…", "# Hello\n{++added++}\n")
	if err != nil {
		t.Fatalf("SaveMarkdownReviewDraft: %v", err)
	}
	if drafted.Status != review.ReviewPending {
		t.Errorf("status drifted: %q", drafted.Status)
	}
	if drafted.Markdown == nil || drafted.Markdown.DraftMarkedUp != "# Hello\n{++added++}\n" {
		t.Errorf("draft markdown = %+v", drafted.Markdown)
	}
	if drafted.DraftBody != "in progress…" {
		t.Errorf("draft_body = %q", drafted.DraftBody)
	}

	// Re-read.
	reread, err := s.ReadReview(r.ID)
	if err != nil {
		t.Fatalf("ReadReview: %v", err)
	}
	if reread.Markdown.DraftMarkedUp != "# Hello\n{++added++}\n" {
		t.Errorf("draft_marked_up not persisted: %q", reread.Markdown.DraftMarkedUp)
	}
	if reread.Markdown.MarkedUp != "" {
		t.Errorf("authoritative marked_up should be empty pre-submit, got %q", reread.Markdown.MarkedUp)
	}
}

func TestSubmitMarkdownReview_ClearsDrafts(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	r, _ := s.CreateMarkdownReview(review.MarkdownCreateOpts{
		Path:     "/tmp/notes.md",
		Original: "# Hello\n",
	})
	if _, err := s.SaveMarkdownReviewDraft(r.ID, "WIP", "# Hello\n{++WIP++}\n"); err != nil {
		t.Fatalf("SaveMarkdownReviewDraft: %v", err)
	}

	final, err := s.SubmitMarkdownReview(r.ID, "COMMENT", "Final", "# Hello\n{++final++}\n")
	if err != nil {
		t.Fatalf("SubmitMarkdownReview: %v", err)
	}
	if final.DraftBody != "" || final.Markdown.DraftMarkedUp != "" {
		t.Errorf("drafts not cleared: body=%q draft_marked_up=%q",
			final.DraftBody, final.Markdown.DraftMarkedUp)
	}
}

func TestCancelReview_ClearsDrafts(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	r, _ := s.CreateDiffReview(review.CreateOpts{BaseCommit: "aaa", HeadCommit: "bbb"})
	if _, err := s.SaveReviewDraft(r.ID, "WIP", []review.ReviewComment{
		{Path: "foo.go", Line: 1, Side: "RIGHT", Body: "WIP"},
	}); err != nil {
		t.Fatalf("SaveReviewDraft: %v", err)
	}
	cancelled, err := s.CancelReview(r.ID)
	if err != nil {
		t.Fatalf("CancelReview: %v", err)
	}
	if cancelled.DraftBody != "" || len(cancelled.DraftComments) != 0 {
		t.Errorf("drafts not cleared on cancel: body=%q comments=%d",
			cancelled.DraftBody, len(cancelled.DraftComments))
	}
}

func TestCancelReviewNotPending(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	r, _ := s.CreateDiffReview(review.CreateOpts{BaseCommit: "aaa", HeadCommit: "bbb"})
	_, _ = s.CancelReview(r.ID)

	_, err := s.CancelReview(r.ID)
	if err == nil {
		t.Error("expected error cancelling non-pending review")
	}
}

func TestCreateOrReopenDiffReview_ErrorsIfPending(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	opts := review.CreateOpts{BaseCommit: "aaa1111", HeadCommit: "bbb2222", HeadRef: "main"}
	_, reopened, err := s.CreateOrReopenDiffReview(opts)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if reopened {
		t.Error("first call should not be a reopen")
	}

	// Second call while the first is still pending should error — caller
	// orchestrates (poll the existing review or cancel it first).
	_, _, err = s.CreateOrReopenDiffReview(opts)
	if !errors.Is(err, ErrReviewInProgress) {
		t.Errorf("second call err = %v, want ErrReviewInProgress", err)
	}
}

func TestCreateOrReopenDiffReview_ReopensAfterCancel(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	opts := review.CreateOpts{BaseCommit: "aaa1111", HeadCommit: "bbb2222", HeadRef: "main"}
	r1, _, err := s.CreateOrReopenDiffReview(opts)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := s.CancelReview(r1.ID); err != nil {
		t.Fatalf("CancelReview: %v", err)
	}

	r2, reopened, err := s.CreateOrReopenDiffReview(opts)
	if err != nil {
		t.Fatalf("reopen call: %v", err)
	}
	if !reopened {
		t.Error("expected reopen after cancel")
	}
	if r1.ID != r2.ID {
		t.Errorf("IDs differ: %q vs %q", r1.ID, r2.ID)
	}
	if r2.Status != review.ReviewPending {
		t.Errorf("reopen status = %q, want pending", r2.Status)
	}
}

func TestListAndCancelPendingReviews(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	r1, _ := s.CreateDiffReview(review.CreateOpts{BaseCommit: "aaa", HeadCommit: "bbb"})
	r2, _ := s.CreateDiffReview(review.CreateOpts{BaseCommit: "ccc", HeadCommit: "ddd"})
	// Submit r1 so only r2 is pending.
	_, _ = s.SubmitDiffReview(r1.ID, "APPROVE", "", nil)

	pending, err := s.ListPendingReviews()
	if err != nil {
		t.Fatalf("ListPendingReviews: %v", err)
	}
	if len(pending) != 1 || pending[0] != r2.ID {
		t.Errorf("pending = %v, want [%s]", pending, r2.ID)
	}

	cancelled := s.CancelPendingReviews()
	if len(cancelled) != 1 || cancelled[0] != r2.ID {
		t.Errorf("cancelled = %v, want [%s]", cancelled, r2.ID)
	}

	// r2 should now be cancelled.
	r2reread, _ := s.ReadReview(r2.ID)
	if r2reread.Status != review.ReviewCancelled {
		t.Errorf("r2 status = %q, want cancelled", r2reread.Status)
	}
}

func TestCreateReadMarkdownReview(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	r, err := s.CreateMarkdownReview(review.MarkdownCreateOpts{
		Path:      "/tmp/notes/foo.md",
		Original:  "# Hello\n",
		SessionID: "ses-md-1",
		IdeaSlug:  "idea-x",
	})
	if err != nil {
		t.Fatalf("CreateMarkdownReview: %v", err)
	}

	if r.Status != review.ReviewPending {
		t.Errorf("status = %q, want %q", r.Status, review.ReviewPending)
	}
	if r.Kind != review.KindMarkdown {
		t.Errorf("kind = %q, want %q", r.Kind, review.KindMarkdown)
	}
	if r.Markdown == nil || r.Markdown.Path != "/tmp/notes/foo.md" {
		t.Errorf("markdown payload = %+v", r.Markdown)
	}
	if r.Markdown.Original != "# Hello\n" {
		t.Errorf("original = %q", r.Markdown.Original)
	}

	r2, err := s.ReadReview(r.ID)
	if err != nil {
		t.Fatalf("ReadReview: %v", err)
	}
	if r2.Markdown == nil || r2.Markdown.Path != "/tmp/notes/foo.md" {
		t.Errorf("re-read markdown = %+v", r2.Markdown)
	}
}

func TestSubmitMarkdownReview(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	r, err := s.CreateMarkdownReview(review.MarkdownCreateOpts{
		Path:     "/tmp/notes/foo.md",
		Original: "# Hello\n",
	})
	if err != nil {
		t.Fatalf("CreateMarkdownReview: %v", err)
	}

	markedUp := "# Hello{++!++}\n{>>nice intro<<}\n"
	r, err = s.SubmitMarkdownReview(r.ID, "REQUEST_CHANGES", "Tweaks needed", markedUp)
	if err != nil {
		t.Fatalf("SubmitMarkdownReview: %v", err)
	}
	if r.Status != review.ReviewComplete {
		t.Errorf("status = %q", r.Status)
	}
	if r.Event != "REQUEST_CHANGES" {
		t.Errorf("event = %q", r.Event)
	}
	if r.Markdown.MarkedUp != markedUp {
		t.Errorf("marked_up round-trip mismatch:\n got %q\nwant %q", r.Markdown.MarkedUp, markedUp)
	}

	// Submit-time mark parsing — the persisted record must carry a parsed
	// view of the human's NEW marks so consumers don't have to scan or
	// filter doc literals themselves.
	if got, want := len(r.Markdown.Marks), 2; got != want {
		t.Fatalf("Marks count = %d, want %d (%+v)", got, want, r.Markdown.Marks)
	}
	if r.Markdown.Marks[0].Type != review.CriticInsertion || r.Markdown.Marks[0].Text != "!" {
		t.Errorf("Marks[0] = %+v, want insertion of %q", r.Markdown.Marks[0], "!")
	}
	if r.Markdown.Marks[1].Type != review.CriticComment || r.Markdown.Marks[1].Text != "nice intro" {
		t.Errorf("Marks[1] = %+v, want comment of %q", r.Markdown.Marks[1], "nice intro")
	}

	// Re-read to verify atomic write.
	r2, _ := s.ReadReview(r.ID)
	if r2.Markdown.MarkedUp != markedUp {
		t.Errorf("re-read marked_up = %q", r2.Markdown.MarkedUp)
	}
	if len(r2.Markdown.Marks) != 2 {
		t.Errorf("re-read Marks count = %d, want 2", len(r2.Markdown.Marks))
	}
}

func TestSubmitMarkdownReview_FiltersDocLiterals(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	// Original contains CriticMarkup syntax as part of the doc content
	// (e.g. an inline-code documentation example) plus surrounding prose.
	original := "Doc explains the `{++ins++}` syntax.\n"
	r, err := s.CreateMarkdownReview(review.MarkdownCreateOpts{
		Path:     "/tmp/notes/spec.md",
		Original: original,
	})
	if err != nil {
		t.Fatalf("CreateMarkdownReview: %v", err)
	}

	// User's marked-up version preserves the doc literal AND adds a real
	// edit. We expect Marks to include only the user's edit.
	markedUp := original + "\nReal {++addition++} here.\n"
	r, err = s.SubmitMarkdownReview(r.ID, "REQUEST_CHANGES", "", markedUp)
	if err != nil {
		t.Fatalf("SubmitMarkdownReview: %v", err)
	}

	if got, want := len(r.Markdown.Marks), 1; got != want {
		t.Fatalf("Marks count = %d, want %d — doc literal `{++ins++}` should have been filtered. Got: %+v",
			got, want, r.Markdown.Marks)
	}
	if r.Markdown.Marks[0].Text != "addition" {
		t.Errorf("Marks[0].Text = %q, want %q", r.Markdown.Marks[0].Text, "addition")
	}
}

func TestSubmitMarkdownReview_RejectsKindMismatch(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)
	// Diff review.
	r, _ := s.CreateDiffReview(review.CreateOpts{BaseCommit: "aaa", HeadCommit: "bbb"})

	_, err := s.SubmitMarkdownReview(r.ID, "APPROVE", "", "")
	if err == nil {
		t.Error("expected error submitting a markdown event on a diff review")
	}
}

func TestCreateOrReopenMarkdownReview_ErrorsIfPending(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	opts := review.MarkdownCreateOpts{Path: "/tmp/x/foo.md", Original: "v1"}
	_, _, err := s.CreateOrReopenMarkdownReview(opts)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Second call while first is pending should error.
	opts.Original = "v2"
	_, _, err = s.CreateOrReopenMarkdownReview(opts)
	if !errors.Is(err, ErrReviewInProgress) {
		t.Errorf("second call err = %v, want ErrReviewInProgress", err)
	}
}

func TestCreateOrReopenMarkdownReview_ReopensAfterCancel(t *testing.T) {
	t.Parallel()
	s, _ := newReviewStore(t)

	opts := review.MarkdownCreateOpts{Path: "/tmp/x/foo.md", Original: "v1"}
	r1, _, err := s.CreateOrReopenMarkdownReview(opts)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := s.CancelReview(r1.ID); err != nil {
		t.Fatalf("CancelReview: %v", err)
	}

	// Reopen with a fresher snapshot — should reuse the ID and refresh Original.
	opts.Original = "v2"
	r2, reopened, err := s.CreateOrReopenMarkdownReview(opts)
	if err != nil {
		t.Fatalf("reopen call: %v", err)
	}
	if !reopened {
		t.Error("expected reopen after cancel")
	}
	if r1.ID != r2.ID {
		t.Errorf("IDs differ: %q vs %q", r1.ID, r2.ID)
	}
	if r2.Markdown.Original != "v2" {
		t.Errorf("reopen did not refresh Original; got %q", r2.Markdown.Original)
	}
}

func TestGenerateMarkdownReviewID_Deterministic(t *testing.T) {
	t.Parallel()
	a := review.GenerateMarkdownReviewID("/a/b/foo.md")
	b := review.GenerateMarkdownReviewID("/a/b/foo.md")
	if a != b {
		t.Errorf("expected deterministic IDs, got %q vs %q", a, b)
	}
	c := review.GenerateMarkdownReviewID("/a/c/foo.md")
	if a == c {
		t.Error("expected different IDs for different paths")
	}
}

func intPtr(i int) *int { return &i }

// TestCreateOrReopen_GlobalPendingBlock — only one pending review at a time
// across all kinds and paths. Each subtest seeds a different pending review
// and confirms a new request of the other kind / different path / different
// range fails with ErrReviewInProgress carrying the in-progress metadata.
func TestCreateOrReopen_GlobalPendingBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		seed     func(s *FSStore) *review.Review
		request  func(s *FSStore) error
		wantKind review.Kind
		wantPath string
		wantRefs [3]string // base, head, ref
	}{
		{
			name: "pending markdown blocks new markdown on different path",
			seed: func(s *FSStore) *review.Review {
				r, _ := s.CreateMarkdownReview(review.MarkdownCreateOpts{Path: "/tmp/a.md", Original: "a"})
				return r
			},
			request: func(s *FSStore) error {
				_, _, err := s.CreateOrReopenMarkdownReview(review.MarkdownCreateOpts{Path: "/tmp/b.md", Original: "b"})
				return err
			},
			wantKind: review.KindMarkdown,
			wantPath: "/tmp/a.md",
		},
		{
			name: "pending markdown blocks new diff",
			seed: func(s *FSStore) *review.Review {
				r, _ := s.CreateMarkdownReview(review.MarkdownCreateOpts{Path: "/tmp/a.md", Original: "a"})
				return r
			},
			request: func(s *FSStore) error {
				_, _, err := s.CreateOrReopenDiffReview(review.CreateOpts{BaseCommit: "aaa1111", HeadCommit: "bbb2222", HeadRef: "main"})
				return err
			},
			wantKind: review.KindMarkdown,
			wantPath: "/tmp/a.md",
		},
		{
			name: "pending diff blocks new markdown",
			seed: func(s *FSStore) *review.Review {
				r, _ := s.CreateDiffReview(review.CreateOpts{Repo: "/tmp/repo", BaseCommit: "aaa1111", HeadCommit: "bbb2222", HeadRef: "main"})
				return r
			},
			request: func(s *FSStore) error {
				_, _, err := s.CreateOrReopenMarkdownReview(review.MarkdownCreateOpts{Path: "/tmp/x.md", Original: "x"})
				return err
			},
			wantKind: review.KindDiff,
			wantRefs: [3]string{"aaa1111", "bbb2222", "main"},
		},
		{
			name: "pending diff blocks new diff on different range",
			seed: func(s *FSStore) *review.Review {
				r, _ := s.CreateDiffReview(review.CreateOpts{Repo: "/tmp/repo", BaseCommit: "aaa1111", HeadCommit: "bbb2222", HeadRef: "main"})
				return r
			},
			request: func(s *FSStore) error {
				_, _, err := s.CreateOrReopenDiffReview(review.CreateOpts{BaseCommit: "ccc3333", HeadCommit: "ddd4444", HeadRef: "feat/x"})
				return err
			},
			wantKind: review.KindDiff,
			wantRefs: [3]string{"aaa1111", "bbb2222", "main"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, _ := newReviewStore(t)
			seeded := tt.seed(s)

			err := tt.request(s)
			if !errors.Is(err, ErrReviewInProgress) {
				t.Fatalf("err = %v, want ErrReviewInProgress", err)
			}

			var ripErr *ReviewInProgressError
			if !errors.As(err, &ripErr) {
				t.Fatalf("err is not *ReviewInProgressError: %v", err)
			}
			if ripErr.ID != seeded.ID {
				t.Errorf("err.ID = %q, want %q", ripErr.ID, seeded.ID)
			}
			if ripErr.Kind != tt.wantKind {
				t.Errorf("err.Kind = %q, want %q", ripErr.Kind, tt.wantKind)
			}
			if tt.wantPath != "" && ripErr.Path != tt.wantPath {
				t.Errorf("err.Path = %q, want %q", ripErr.Path, tt.wantPath)
			}
			if tt.wantRefs != ([3]string{}) {
				if ripErr.BaseCommit != tt.wantRefs[0] || ripErr.HeadCommit != tt.wantRefs[1] || ripErr.HeadRef != tt.wantRefs[2] {
					t.Errorf("err refs = %q/%q/%q, want %q/%q/%q",
						ripErr.BaseCommit, ripErr.HeadCommit, ripErr.HeadRef,
						tt.wantRefs[0], tt.wantRefs[1], tt.wantRefs[2])
				}
			}
		})
	}
}

// TestCreateOrReopen_PerSessionScope — pending block is scoped per session.
// Two sessions can each have a pending review concurrently; the global
// shutdown sweep is unaffected (it cancels regardless of session).
func TestCreateOrReopen_PerSessionScope(t *testing.T) {
	t.Parallel()

	t.Run("same session blocks", func(t *testing.T) {
		t.Parallel()
		s, _ := newReviewStore(t)

		first, _, err := s.CreateOrReopenMarkdownReview(review.MarkdownCreateOpts{
			Path: "/tmp/a.md", Original: "a", SessionID: "ses-1",
		})
		if err != nil {
			t.Fatalf("first: %v", err)
		}
		_, _, err = s.CreateOrReopenMarkdownReview(review.MarkdownCreateOpts{
			Path: "/tmp/b.md", Original: "b", SessionID: "ses-1",
		})
		if !errors.Is(err, ErrReviewInProgress) {
			t.Fatalf("err = %v, want ErrReviewInProgress for same session", err)
		}
		var rip *ReviewInProgressError
		_ = errors.As(err, &rip)
		if rip != nil && rip.ID != first.ID {
			t.Errorf("rip.ID = %q, want %q", rip.ID, first.ID)
		}
	})

	t.Run("different sessions both succeed", func(t *testing.T) {
		t.Parallel()
		s, _ := newReviewStore(t)

		_, _, err := s.CreateOrReopenMarkdownReview(review.MarkdownCreateOpts{
			Path: "/tmp/a.md", Original: "a", SessionID: "ses-1",
		})
		if err != nil {
			t.Fatalf("session 1: %v", err)
		}
		_, _, err = s.CreateOrReopenMarkdownReview(review.MarkdownCreateOpts{
			Path: "/tmp/b.md", Original: "b", SessionID: "ses-2",
		})
		if err != nil {
			t.Errorf("session 2 should not be blocked by session 1; got %v", err)
		}
	})

	t.Run("CLI scope (empty session) is its own bucket", func(t *testing.T) {
		t.Parallel()
		s, _ := newReviewStore(t)

		// Agent session creates a review.
		_, _, err := s.CreateOrReopenMarkdownReview(review.MarkdownCreateOpts{
			Path: "/tmp/a.md", Original: "a", SessionID: "ses-1",
		})
		if err != nil {
			t.Fatalf("agent: %v", err)
		}
		// CLI invocation (Session="") starts cleanly — it's a different scope.
		_, _, err = s.CreateOrReopenMarkdownReview(review.MarkdownCreateOpts{
			Path: "/tmp/b.md", Original: "b",
		})
		if err != nil {
			t.Errorf("CLI scope should not be blocked by agent session; got %v", err)
		}
		// A second CLI invocation IS blocked by the first CLI review.
		_, _, err = s.CreateOrReopenMarkdownReview(review.MarkdownCreateOpts{
			Path: "/tmp/c.md", Original: "c",
		})
		if !errors.Is(err, ErrReviewInProgress) {
			t.Errorf("second CLI invocation: err = %v, want ErrReviewInProgress", err)
		}
	})
}

// TestCreateOrReopen_TerminalAllowsNew — once the on-disk pending review
// reaches a terminal state (cancelled or complete), a new review of any
// kind starts cleanly.
func TestCreateOrReopen_TerminalAllowsNew(t *testing.T) {
	t.Parallel()

	terminate := []struct {
		name string
		end  func(s *FSStore, id string) error
	}{
		{
			name: "after cancel",
			end: func(s *FSStore, id string) error {
				_, err := s.CancelReview(id)
				return err
			},
		},
		{
			name: "after submit",
			end: func(s *FSStore, id string) error {
				_, err := s.SubmitMarkdownReview(id, "APPROVE", "", "")
				return err
			},
		},
	}

	for _, tt := range terminate {
		t.Run(tt.name+" allows new diff", func(t *testing.T) {
			t.Parallel()
			s, _ := newReviewStore(t)

			r, _ := s.CreateMarkdownReview(review.MarkdownCreateOpts{Path: "/tmp/a.md", Original: "a"})
			if err := tt.end(s, r.ID); err != nil {
				t.Fatalf("terminate: %v", err)
			}

			_, _, err := s.CreateOrReopenDiffReview(review.CreateOpts{BaseCommit: "aaa1111", HeadCommit: "bbb2222", HeadRef: "main"})
			if err != nil {
				t.Errorf("new diff after %s: %v", tt.name, err)
			}
		})
	}
}
