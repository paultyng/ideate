package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/paultyng/ideate/internal/review"
)

// initTestRepo creates a temp git repo with two commits and returns the repo path
// and the SHAs of both commits.
func initTestRepo(t *testing.T) (repoPath, baseSHA, headSHA string) {
	t.Helper()
	dir := t.TempDir()

	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)

	cmds := [][]string{
		{"init"},
		{"commit", "--allow-empty", "-m", "base"},
	}
	for _, args := range cmds {
		full := append([]string{"-c", "commit.gpgsign=false"}, args...)
		cmd := exec.Command("git", full...)
		cmd.Dir = dir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}

	baseSHA, _ = review.ResolveRef(context.Background(), dir, "HEAD")

	// Add a file and commit.
	if err := os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "hello.go"},
		{"commit", "-m", "head"},
	} {
		full := append([]string{"-c", "commit.gpgsign=false"}, args...)
		cmd := exec.Command("git", full...)
		cmd.Dir = dir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}

	headSHA, _ = review.ResolveRef(context.Background(), dir, "HEAD")
	return dir, baseSHA, headSHA
}

func TestRequestDiffReview(t *testing.T) {
	t.Parallel()

	repoPath, _, _ := initTestRepo(t)

	m, store := setupManager(t)
	drain := captureEvents(t, m)

	handler := m.handleRequestDiffReview("ses-1-test")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo": repoPath,
		"base": "HEAD~1",
		"head": "HEAD",
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %v", result.Content)
	}

	// Parse response.
	text := result.Content[0].(mcp.TextContent).Text
	var resp map[string]any
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parsing response: %v", err)
	}
	if resp["status"] != "pending" {
		t.Errorf("status = %q, want %q", resp["status"], "pending")
	}
	reviewID, ok := resp["review_id"].(string)
	if !ok || reviewID == "" {
		t.Fatal("missing review_id in response")
	}

	// Verify both events fired: review:created (drives nav-to-review)
	// and review:changed (drives the topbar bar refetch).
	gotEvents := drain()
	if !slices.Contains(gotEvents, "review:created") {
		t.Errorf("events = %v, missing review:created", gotEvents)
	}
	if !slices.Contains(gotEvents, "review:changed") {
		t.Errorf("events = %v, missing review:changed", gotEvents)
	}

	// Verify review is in store.
	r, err := store.ReadReview(reviewID)
	if err != nil {
		t.Fatalf("ReadReview: %v", err)
	}
	if r.Status != review.ReviewPending {
		t.Errorf("review status = %q", r.Status)
	}
	if r.Kind != review.KindDiff {
		t.Errorf("review kind = %q, want %q", r.Kind, review.KindDiff)
	}
}

func TestRequestDiffReview_RejectsUnconfiguredRepo(t *testing.T) {
	t.Parallel()

	m, store := setupManager(t)
	store.repos = map[string]map[string]bool{
		"test-idea": {"/allowed/repo": true},
	}

	handler := m.handleRequestDiffReview("ses-1-test")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo": "/etc",
		"base": "HEAD~1",
		"head": "HEAD",
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected error result for unconfigured repo, got: %v", result.Content)
	}
}

func TestGetDiffReviewResult_RejectsBadReviewID(t *testing.T) {
	t.Parallel()

	m, _ := setupManager(t)
	handler := m.handleGetDiffReviewResult("ses-1-test")

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"review_id": "../../../etc/passwd",
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected error for traversal review_id, got: %v", result.Content)
	}
}

func TestGetDiffReviewResultPending(t *testing.T) {
	t.Parallel()

	m, store := setupManager(t)
	r, _, err := store.CreateOrReopenDiffReview(review.CreateOpts{
		BaseCommit: "aaa1111", HeadCommit: "bbb2222", HeadRef: "main",
	})
	if err != nil {
		t.Fatalf("CreateOrReopenDiffReview: %v", err)
	}

	handler := m.handleGetDiffReviewResult("ses-1-test")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"review_id": r.ID,
	}

	// Should return quickly with pending (60s timeout, but review not complete).
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := handler(ctx, req)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	// Should have returned due to context timeout, not 60s timer.
	if elapsed > 5*time.Second {
		t.Errorf("took too long: %v", elapsed)
	}

	text := result.Content[0].(mcp.TextContent).Text
	var resp review.Review
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parsing response: %v", err)
	}
	if resp.Status != review.ReviewPending {
		t.Errorf("status = %q, want %q", resp.Status, review.ReviewPending)
	}
}

func TestGetDiffReviewResultComplete(t *testing.T) {
	t.Parallel()

	m, store := setupManager(t)
	r, _, err := store.CreateOrReopenDiffReview(review.CreateOpts{
		BaseCommit: "aaa1111", HeadCommit: "bbb2222", HeadRef: "main",
	})
	if err != nil {
		t.Fatalf("CreateOrReopenDiffReview: %v", err)
	}

	// Submit the review after a short delay.
	go func() {
		time.Sleep(100 * time.Millisecond)
		store.submitReview(r.ID, "APPROVE", "LGTM", []review.ReviewComment{
			{Path: "foo.go", Line: 10, Side: "RIGHT", Body: "Nice"},
		})
		m.NotifyReviewComplete(r.ID, review.ReviewComplete)
	}()

	handler := m.handleGetDiffReviewResult("ses-1-test")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"review_id": r.ID,
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	var resp review.Review
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parsing response: %v", err)
	}
	if resp.Status != review.ReviewComplete {
		t.Errorf("status = %q, want %q", resp.Status, review.ReviewComplete)
	}
	if len(resp.Comments) != 1 {
		t.Fatalf("comments = %d, want 1", len(resp.Comments))
	}
	if resp.Comments[0].Body != "Nice" {
		t.Errorf("comment body = %q", resp.Comments[0].Body)
	}
}

func TestGetDiffReviewResultAlreadyComplete(t *testing.T) {
	t.Parallel()

	m, store := setupManager(t)
	r, _, err := store.CreateOrReopenDiffReview(review.CreateOpts{
		BaseCommit: "aaa", HeadCommit: "bbb",
	})
	if err != nil {
		t.Fatalf("CreateOrReopenDiffReview: %v", err)
	}
	store.submitReview(r.ID, "APPROVE", "", nil)

	handler := m.handleGetDiffReviewResult("ses-1-test")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"review_id": r.ID,
	}

	// Should return immediately since already complete.
	start := time.Now()
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("should have returned immediately for completed review")
	}

	text := result.Content[0].(mcp.TextContent).Text
	var resp review.Review
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parsing response: %v", err)
	}
	if resp.Status != review.ReviewComplete {
		t.Errorf("status = %q", resp.Status)
	}
}

func TestRequestMarkdownReview(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mdPath := filepath.Join(dir, "context.md")
	contents := "# Notes\n\nFirst draft.\n"
	if err := os.WriteFile(mdPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	m, store := setupManager(t)
	drain := captureEvents(t, m)

	handler := m.handleRequestMarkdownReview("ses-1-test")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"path": mdPath}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %v", result.Content)
	}

	text := result.Content[0].(mcp.TextContent).Text
	var resp map[string]any
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parsing response: %v", err)
	}
	reviewID, _ := resp["review_id"].(string)
	if reviewID == "" || !strings.HasPrefix(reviewID, "md-") {
		t.Errorf("review_id = %q, want md-* prefix", reviewID)
	}

	gotEvents := drain()
	if !slices.Contains(gotEvents, "review:created") {
		t.Errorf("events = %v, missing review:created", gotEvents)
	}
	if !slices.Contains(gotEvents, "review:changed") {
		t.Errorf("events = %v, missing review:changed", gotEvents)
	}

	r, err := store.ReadReview(reviewID)
	if err != nil {
		t.Fatalf("ReadReview: %v", err)
	}
	if r.Kind != review.KindMarkdown {
		t.Errorf("kind = %q", r.Kind)
	}
	if r.Markdown == nil || r.Markdown.Path != mdPath {
		t.Errorf("markdown payload = %+v", r.Markdown)
	}
	if r.Markdown.Original != contents {
		t.Errorf("original snapshot mismatch:\n got %q\nwant %q", r.Markdown.Original, contents)
	}
}

func TestRequestMarkdownReview_RejectsNonMarkdown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	m, _ := setupManager(t)
	handler := m.handleRequestMarkdownReview("ses-1-test")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"path": other}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected error for .txt file, got: %v", result.Content)
	}
}
