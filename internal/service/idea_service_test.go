package service_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/paultyng/ideate/internal/agent"
	"github.com/paultyng/ideate/internal/agent/summarizer"
	"github.com/paultyng/ideate/internal/hooks"
	"github.com/paultyng/ideate/internal/mcp"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/review"
	"github.com/paultyng/ideate/internal/service"
	"github.com/paultyng/ideate/internal/store"
)

// Compile-time assertions: *IdeaService must satisfy every consumer interface
// the App wires it into. Placed in the test file to avoid an import cycle once
// mcp / hooks / summarizer import service.
var (
	_ mcp.IdeaStore      = (*service.IdeaService)(nil)
	_ hooks.SessionStore = (*service.IdeaService)(nil)
	_ summarizer.Store   = (*service.IdeaService)(nil)
)

func newTestService(t *testing.T) (*service.IdeaService, string) {
	t.Helper()
	dir := t.TempDir()
	s := store.NewFSStore(dir, filepath.Join(dir, ".reviews"), "pt/", "")
	return service.New(s, nil), dir
}

func TestList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _ := newTestService(t)

	ideas, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ideas) != 0 {
		t.Fatalf("expected empty list, got %d", len(ideas))
	}
}

func TestCreateAndGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "test idea", Status: model.StatusPaused}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if idea.Slug == "" {
		t.Fatal("slug not set after Create")
	}

	got, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != idea.Name {
		t.Errorf("Name: got %q, want %q", got.Name, idea.Name)
	}
}

func TestUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "update me", Status: model.StatusPaused}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	idea.Status = model.StatusActive
	if err := svc.Update(ctx, idea); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != model.StatusActive {
		t.Errorf("Status: got %q, want %q", got.Status, model.StatusActive)
	}
}

func TestAppendHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "history test", Status: model.StatusPaused}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	ev := model.HistoryEvent{Timestamp: time.Now(), Event: "test-event"}
	if err := svc.AppendHistory(ctx, idea.Slug, ev); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
}

func TestListRepos(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "repo test", Status: model.StatusPaused}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	repos, err := svc.ListRepos(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 0 {
		t.Fatalf("expected no repos, got %d", len(repos))
	}
}

func TestListSessions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "sessions test", Status: model.StatusPaused}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	sessions, err := svc.ListSessions(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected no sessions, got %d", len(sessions))
	}
}

func TestWriteAndUpdateSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "session write", Status: model.StatusActive}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	sess := model.AgentSession{
		UUID:    "test-uuid",
		Agent:   "claude-code",
		Status:  model.SessionStatusRunning,
		Started: time.Now(),
	}

	if err := svc.WriteSession(ctx, idea.Slug, sess.UUID, sess); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	sess.Status = model.SessionStatusCompleted
	if err := svc.UpdateSession(ctx, idea.Slug, sess.UUID, sess); err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}

	sessions, err := svc.ListSessions(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Status != model.SessionStatusCompleted {
		t.Errorf("Status: got %q, want %q", sessions[0].Status, model.SessionStatusCompleted)
	}
}

func TestTouchIdea(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "touch test", Status: model.StatusPaused}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	before := idea.Updated
	// Small sleep to ensure Updated advances.
	time.Sleep(5 * time.Millisecond)

	updated, err := svc.TouchIdea(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("TouchIdea: %v", err)
	}
	if !updated.After(before) {
		t.Errorf("TouchIdea: expected updated timestamp to advance; before=%v after=%v", before, updated)
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "delete test", Status: model.StatusPaused}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := svc.Delete(ctx, idea.Slug, false); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := svc.Get(ctx, idea.Slug)
	if err == nil {
		t.Fatal("expected error after Delete, got nil")
	}
}

func TestRenameIdea(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "rename me", Status: model.StatusPaused}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	result, err := svc.RenameIdea(ctx, idea.Slug, "renamed-idea")
	if err != nil {
		t.Fatalf("RenameIdea: %v", err)
	}
	if result.NewSlug != "renamed-idea" {
		t.Errorf("NewSlug: got %q, want %q", result.NewSlug, "renamed-idea")
	}
}

func TestReadAndCancelReview(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	r, _, err := svc.CreateOrReopenDiffReview(review.CreateOpts{
		BaseCommit: "abc123",
		HeadCommit: "def456",
		HeadRef:    "feat/test",
	})
	if err != nil {
		t.Fatalf("CreateOrReopenDiffReview: %v", err)
	}

	got, err := svc.ReadReview(r.ID)
	if err != nil {
		t.Fatalf("ReadReview: %v", err)
	}
	if got.ID != r.ID {
		t.Errorf("ID: got %q, want %q", got.ID, r.ID)
	}

	cancelled, err := svc.CancelReview(r.ID)
	if err != nil {
		t.Fatalf("CancelReview: %v", err)
	}
	if cancelled.Status != review.ReviewCancelled {
		t.Errorf("Status: got %q, want %q", cancelled.Status, review.ReviewCancelled)
	}
}

func TestCreateOrReopenMarkdownReview(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	r, _, err := svc.CreateOrReopenMarkdownReview(review.MarkdownCreateOpts{
		Path:     "context.md",
		Original: "# Hello",
	})
	if err != nil {
		t.Fatalf("CreateOrReopenMarkdownReview: %v", err)
	}
	if r.ID == "" {
		t.Fatal("expected non-empty review ID")
	}
}

func TestAddResource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "resource test", Status: model.StatusActive}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Append on no match.
	res1 := model.Resource{Type: "notion", URL: "https://notion.so/page-abc", Label: "Design doc"}
	if err := svc.AddResource(ctx, idea.Slug, res1); err != nil {
		t.Fatalf("AddResource (first): %v", err)
	}

	got, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get after first add: %v", err)
	}
	if len(got.Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(got.Resources))
	}
	if got.Resources[0].Label != "Design doc" {
		t.Errorf("label = %q, want Design doc", got.Resources[0].Label)
	}

	// Dedupe by URL with type promotion: same URL, richer type overrides "web".
	// First add a "web" resource so the promotion rule can fire.
	res1b := model.Resource{Type: "web", URL: "https://github.com/owner/repo/pull/99", Label: "PR link"}
	if err := svc.AddResource(ctx, idea.Slug, res1b); err != nil {
		t.Fatalf("AddResource (web seed): %v", err)
	}
	res2 := model.Resource{Type: "github_pr", URL: "https://github.com/owner/repo/pull/99", Label: "PR #99"}
	if err := svc.AddResource(ctx, idea.Slug, res2); err != nil {
		t.Fatalf("AddResource (second): %v", err)
	}

	got2, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get after second add: %v", err)
	}
	if len(got2.Resources) != 2 {
		t.Fatalf("expected 2 resources (notion + deduped PR), got %d", len(got2.Resources))
	}
	// Find the PR resource.
	var prRes *model.Resource
	for i := range got2.Resources {
		if got2.Resources[i].URL == "https://github.com/owner/repo/pull/99" {
			prRes = &got2.Resources[i]
			break
		}
	}
	if prRes == nil {
		t.Fatal("PR resource not found after dedupe")
	}
	if prRes.Type != "github_pr" {
		t.Errorf("type = %q, want github_pr (promotion from web)", prRes.Type)
	}
	if prRes.Label != "PR #99" {
		t.Errorf("label = %q, want PR #99", prRes.Label)
	}
}

// initTestRepoForService initializes a git repo with one commit on main and
// sets origin to the provided URL via git config (no actual remote needed).
func initTestRepoForService(t *testing.T, dir, origin string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git -C %s %v: %s: %v", dir, args, out, err)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "initial")
	if origin != "" {
		run("remote", "add", "origin", origin)
	}
}

// stubCoord is a minimal sessionCoordinator for tests.
type stubCoord struct {
	sessions []agent.SessionInfo
	stopped  []string
}

func (c *stubCoord) List() []agent.SessionInfo { return c.sessions }
func (c *stubCoord) Stop(_ context.Context, uuid string) error {
	c.stopped = append(c.stopped, uuid)
	return nil
}

func newTestServiceWithCoord(t *testing.T, coord *stubCoord) (*service.IdeaService, string) {
	t.Helper()
	dir := t.TempDir()
	s := store.NewFSStore(dir, filepath.Join(dir, ".reviews"), "pt/", "")
	return service.New(s, coord), dir
}

func TestService_Archive_HappyPath(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	ctx := context.Background()
	coord := &stubCoord{}
	svc, _ := newTestServiceWithCoord(t, coord)

	idea := &model.Idea{Name: "archive happy", Status: model.StatusActive}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	repoDir := t.TempDir()
	const originURL = "https://github.com/example/archive-test.git"
	initTestRepoForService(t, repoDir, originURL)

	if _, err := svc.LinkRepo(ctx, idea.Slug, repoDir, "", "archrepo"); err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}

	report, err := svc.Archive(ctx, idea.Slug, false)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if len(report.ReleasedRepos) != 1 {
		t.Fatalf("ReleasedRepos: got %d, want 1", len(report.ReleasedRepos))
	}

	got, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get after Archive: %v", err)
	}
	if got.Status != model.StatusArchived {
		t.Errorf("Status = %q, want %q", got.Status, model.StatusArchived)
	}

	// Resources should contain the repo entry.
	var found bool
	for _, r := range got.Resources {
		if r.Type == "repo" && r.URL == originURL {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("repo resource not found in idea.Resources after Archive")
	}

	// Worktree directory should be gone.
	repos, err := svc.ListRepos(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 0 {
		t.Errorf("expected 0 linked repos after Archive, got %d", len(repos))
	}
}

func TestService_Archive_RefusesOnDirtyRepos(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	ctx := context.Background()
	coord := &stubCoord{}
	svc, ideasDir := newTestServiceWithCoord(t, coord)

	idea := &model.Idea{Name: "archive dirty refuse", Status: model.StatusActive}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	repoDir := t.TempDir()
	initTestRepoForService(t, repoDir, "https://github.com/example/dirty.git")

	if _, err := svc.LinkRepo(ctx, idea.Slug, repoDir, "", "dirtyr"); err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}

	// Make the worktree dirty by writing an untracked file.
	worktreePath := filepath.Join(ideasDir, idea.Slug, "repos", "dirtyr")
	if writeErr := os.WriteFile(filepath.Join(worktreePath, "dirty.txt"), []byte("dirty"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	_, archErr := svc.Archive(ctx, idea.Slug, false)
	if archErr == nil {
		t.Fatal("expected error on dirty repo, got nil")
	}
	var dirtyErr *store.ErrDirtyRepos
	if !errors.As(archErr, &dirtyErr) {
		t.Fatalf("expected *store.ErrDirtyRepos, got %T: %v", archErr, archErr)
	}

	got, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status == model.StatusArchived {
		t.Error("idea should not be archived after dirty-repo refusal")
	}
}

func TestService_Archive_ForceOverridesDirty(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	ctx := context.Background()
	coord := &stubCoord{}
	svc, ideasDir := newTestServiceWithCoord(t, coord)

	idea := &model.Idea{Name: "archive force dirty", Status: model.StatusActive}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	repoDir := t.TempDir()
	initTestRepoForService(t, repoDir, "https://github.com/example/forcedirty.git")

	if _, err := svc.LinkRepo(ctx, idea.Slug, repoDir, "", "forcedirty"); err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}

	worktreePath := filepath.Join(ideasDir, idea.Slug, "repos", "forcedirty")
	if writeErr := os.WriteFile(filepath.Join(worktreePath, "dirty.txt"), []byte("dirty"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	if _, archErr := svc.Archive(ctx, idea.Slug, true); archErr != nil {
		t.Fatalf("Archive(force=true): %v", archErr)
	}

	got, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != model.StatusArchived {
		t.Errorf("Status = %q, want %q", got.Status, model.StatusArchived)
	}
}

func TestService_Archive_RefusesOnRunningSession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	coord := &stubCoord{
		sessions: []agent.SessionInfo{
			{ID: "ses-1", Status: agent.StatusRunning},
		},
	}
	svc, _ := newTestServiceWithCoord(t, coord)

	idea := &model.Idea{Name: "archive busy", Status: model.StatusActive}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Set IdeaSlug on the session to match.
	coord.sessions[0].IdeaSlug = idea.Slug

	_, archErr := svc.Archive(ctx, idea.Slug, false)
	if archErr == nil {
		t.Fatal("expected ErrIdeaBusy, got nil")
	}
	if !errors.Is(archErr, store.ErrIdeaBusy) {
		t.Fatalf("expected errors.Is(err, store.ErrIdeaBusy), got %v", archErr)
	}
}

func TestService_Archive_ForceStopsRunningSessions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	coord := &stubCoord{
		sessions: []agent.SessionInfo{
			{ID: "ses-1", Status: agent.StatusRunning},
		},
	}
	svc, _ := newTestServiceWithCoord(t, coord)

	idea := &model.Idea{Name: "archive force stop", Status: model.StatusActive}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	coord.sessions[0].IdeaSlug = idea.Slug

	if _, archErr := svc.Archive(ctx, idea.Slug, true); archErr != nil {
		t.Fatalf("Archive(force=true): %v", archErr)
	}

	if len(coord.stopped) != 1 || coord.stopped[0] != "ses-1" {
		t.Errorf("expected Stop(ses-1) to be called, got stopped=%v", coord.stopped)
	}

	got, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != model.StatusArchived {
		t.Errorf("Status = %q, want %q", got.Status, model.StatusArchived)
	}
}

// TestService_Archive_RefusesOnOpenBacklog covers the backlog gate:
// an open/in-progress item blocks archive without force. The typed
// error carries the count + up to 10 titles so the caller can surface
// what would be buried before retrying.
func TestService_Archive_RefusesOnOpenBacklog(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "archive backlog refuse", Status: model.StatusActive}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.AddBacklogItem(ctx, idea.Slug, model.BacklogItem{
		Title:  "still open",
		Status: model.BacklogStatusOpen,
	}); err != nil {
		t.Fatalf("AddBacklogItem: %v", err)
	}

	_, err := svc.Archive(ctx, idea.Slug, false)
	if err == nil {
		t.Fatal("expected error on open backlog, got nil")
	}
	var openErr *store.ErrOpenBacklogItems
	if !errors.As(err, &openErr) {
		t.Fatalf("expected *store.ErrOpenBacklogItems, got %T: %v", err, err)
	}
	if openErr.Count != 1 {
		t.Errorf("Count = %d, want 1", openErr.Count)
	}
	if len(openErr.Titles) != 1 || openErr.Titles[0] != "still open" {
		t.Errorf("Titles = %v, want [\"still open\"]", openErr.Titles)
	}

	got, _ := svc.Get(ctx, idea.Slug)
	if got.Status == model.StatusArchived {
		t.Error("idea should not be archived after open-backlog refusal")
	}
}

// TestService_Archive_AllowsWhenAllBacklogDone confirms items in
// done/wontfix state don't block archive. Only open/in_progress do.
func TestService_Archive_AllowsWhenAllBacklogDone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "archive backlog closed", Status: model.StatusActive}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, status := range []model.BacklogStatus{model.BacklogStatusDone, model.BacklogStatusWontFix} {
		if _, err := svc.AddBacklogItem(ctx, idea.Slug, model.BacklogItem{
			Title:  "finished item " + string(status),
			Status: status,
		}); err != nil {
			t.Fatalf("AddBacklogItem: %v", err)
		}
	}

	if _, err := svc.Archive(ctx, idea.Slug, false); err != nil {
		t.Fatalf("Archive should have succeeded with only done/wontfix items: %v", err)
	}

	got, _ := svc.Get(ctx, idea.Slug)
	if got.Status != model.StatusArchived {
		t.Errorf("Status = %q, want archived", got.Status)
	}
}

// TestService_Archive_ForceOverridesOpenBacklog confirms force=true
// bypasses the new gate.
func TestService_Archive_ForceOverridesOpenBacklog(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "archive force backlog", Status: model.StatusActive}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.AddBacklogItem(ctx, idea.Slug, model.BacklogItem{
		Title: "in flight",
	}); err != nil {
		t.Fatalf("AddBacklogItem: %v", err)
	}

	if _, err := svc.Archive(ctx, idea.Slug, true); err != nil {
		t.Fatalf("Archive with force should have succeeded: %v", err)
	}

	got, _ := svc.Get(ctx, idea.Slug)
	if got.Status != model.StatusArchived {
		t.Errorf("Status = %q, want archived", got.Status)
	}

	// Backlog file preserved so unarchive restores intent.
	items, err := svc.ListBacklog(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("ListBacklog after force-archive: %v", err)
	}
	if len(items) != 1 || items[0].Title != "in flight" {
		t.Errorf("backlog lost on force-archive; got %+v", items)
	}
}

// TestService_Archive_ErrOpenBacklogTitlesCapped confirms the error
// truncates its Titles slice at 10 while Count carries the full total.
func TestService_Archive_ErrOpenBacklogTitlesCapped(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "archive many items", Status: model.StatusActive}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	const total = 15
	for i := 0; i < total; i++ {
		if _, err := svc.AddBacklogItem(ctx, idea.Slug, model.BacklogItem{
			Title: fmt.Sprintf("item %02d", i),
		}); err != nil {
			t.Fatalf("AddBacklogItem: %v", err)
		}
	}

	_, err := svc.Archive(ctx, idea.Slug, false)
	var openErr *store.ErrOpenBacklogItems
	if !errors.As(err, &openErr) {
		t.Fatalf("expected *store.ErrOpenBacklogItems, got %T: %v", err, err)
	}
	if openErr.Count != total {
		t.Errorf("Count = %d, want %d", openErr.Count, total)
	}
	if len(openErr.Titles) != 10 {
		t.Errorf("Titles length = %d, want 10 (capped)", len(openErr.Titles))
	}
}

func TestService_Unarchive_RestoresStatusAndReportsRepos(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	coord := &stubCoord{}
	svc, _ := newTestServiceWithCoord(t, coord)

	idea := &model.Idea{
		Name:   "unarchive test",
		Status: model.StatusActive,
		Resources: []model.Resource{
			{Type: "repo", URL: "https://github.com/example/r.git", Label: "r"},
		},
	}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Archive manually via Update so we skip the full Archive flow.
	idea.Status = model.StatusArchived
	if err := svc.Update(ctx, idea); err != nil {
		t.Fatalf("Update (archive): %v", err)
	}

	report, err := svc.Unarchive(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Unarchive: %v", err)
	}
	if len(report.RepoResources) != 1 {
		t.Fatalf("RepoResources: got %d, want 1", len(report.RepoResources))
	}

	got, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != model.StatusActive {
		t.Errorf("Status = %q, want %q", got.Status, model.StatusActive)
	}
}

func TestService_Pause_SetsStatusAndPauseUntil(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "pause test", Status: model.StatusActive}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	until := time.Now().Add(24 * time.Hour)
	if err := svc.Pause(ctx, idea.Slug, &until); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	got, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != model.StatusPaused {
		t.Errorf("Status = %q, want %q", got.Status, model.StatusPaused)
	}
	if got.PauseUntil == nil {
		t.Error("PauseUntil should be set")
	}
}

func TestService_Resume_ClearsPauseUntil(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "resume test", Status: model.StatusActive}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	until := time.Now().Add(24 * time.Hour)
	if err := svc.Pause(ctx, idea.Slug, &until); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	if err := svc.Resume(ctx, idea.Slug); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	got, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != model.StatusActive {
		t.Errorf("Status = %q, want %q", got.Status, model.StatusActive)
	}
	if got.PauseUntil != nil {
		t.Errorf("PauseUntil should be nil after Resume, got %v", got.PauseUntil)
	}
}

// TestService_Archive_RefusesAtomically pins the contract that when Archive
// returns ErrIdeaBusy (session gate), no session has been stopped and the
// idea status remains unchanged. This is the regression case for the
// collect-then-validate-then-mutate ordering fix.
func TestService_Archive_RefusesAtomically(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	ctx := context.Background()
	coord := &stubCoord{
		sessions: []agent.SessionInfo{
			{ID: "ses-atomic", Status: agent.StatusRunning},
		},
	}
	svc, ideasDir := newTestServiceWithCoord(t, coord)

	idea := &model.Idea{Name: "archive atomic refuse", Status: model.StatusActive}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	coord.sessions[0].IdeaSlug = idea.Slug

	// Link a clean repo so we can verify it is not unlinked.
	repoDir := t.TempDir()
	initTestRepoForService(t, repoDir, "https://github.com/example/atomic.git")
	if _, err := svc.LinkRepo(ctx, idea.Slug, repoDir, "", "atomicrepo"); err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}

	_, archErr := svc.Archive(ctx, idea.Slug, false)
	if archErr == nil {
		t.Fatal("expected ErrIdeaBusy, got nil")
	}
	if !errors.Is(archErr, store.ErrIdeaBusy) {
		t.Fatalf("expected errors.Is(err, store.ErrIdeaBusy), got %v", archErr)
	}

	// No session must have been stopped.
	if len(coord.stopped) != 0 {
		t.Errorf("Stop was called %d time(s) before all gates passed; stopped=%v", len(coord.stopped), coord.stopped)
	}

	// Idea status must be unchanged.
	got, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != model.StatusActive {
		t.Errorf("Status = %q, want %q (must not change on refusal)", got.Status, model.StatusActive)
	}

	// Worktree must still exist (no UnlinkRepo happened).
	worktreePath := filepath.Join(ideasDir, idea.Slug, "repos", "atomicrepo")
	if _, statErr := os.Stat(worktreePath); statErr != nil {
		t.Errorf("worktree %s should still exist after refusal: %v", worktreePath, statErr)
	}
}

func TestLinkRepo_AutoAddsRepoResource(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{Name: "link repo test", Status: model.StatusActive}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	repoDir := t.TempDir()
	const originURL = "https://github.com/example/myrepo.git"
	initTestRepoForService(t, repoDir, originURL)

	name, err := svc.LinkRepo(ctx, idea.Slug, repoDir, "", "myrepo")
	if err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}
	if name != "myrepo" {
		t.Errorf("LinkRepo name = %q, want %q", name, "myrepo")
	}

	got, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Resources) != 1 {
		t.Fatalf("expected 1 resource after LinkRepo, got %d", len(got.Resources))
	}
	r := got.Resources[0]
	if r.Type != "repo" {
		t.Errorf("resource Type = %q, want repo", r.Type)
	}
	if r.URL != originURL {
		t.Errorf("resource URL = %q, want %q", r.URL, originURL)
	}
	if r.Label != name {
		t.Errorf("resource Label = %q, want %q", r.Label, name)
	}

	// Unlink and re-link: the resource should still be exactly one (dedupe).
	if err := svc.UnlinkRepo(ctx, idea.Slug, name, true); err != nil {
		t.Fatalf("UnlinkRepo: %v", err)
	}
	if _, err := svc.LinkRepo(ctx, idea.Slug, repoDir, "", "myrepo"); err != nil {
		t.Fatalf("LinkRepo (second): %v", err)
	}
	got2, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get after re-link: %v", err)
	}
	if len(got2.Resources) != 1 {
		t.Fatalf("expected 1 resource after re-link (dedupe), got %d", len(got2.Resources))
	}
}

func TestService_DeleteResource_RemovesMatchingURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{
		Name:   "resource test",
		Status: model.StatusPaused,
		Resources: []model.Resource{
			{Type: "github_pr", URL: "https://github.com/o/r/pull/1", Label: "PR"},
		},
	}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	deleted, err := svc.DeleteResource(ctx, idea.Slug, "https://github.com/o/r/pull/1")
	if err != nil {
		t.Fatalf("DeleteResource: %v", err)
	}
	if !deleted {
		t.Errorf("expected deleted=true, got false")
	}

	got, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Resources) != 0 {
		t.Errorf("expected 0 resources after delete, got %d", len(got.Resources))
	}
}

func TestService_DeleteResource_IsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _ := newTestService(t)

	idea := &model.Idea{
		Name:   "idempotent test",
		Status: model.StatusPaused,
		Resources: []model.Resource{
			{Type: "notion", URL: "https://notion.so/page-123", Label: "Spec"},
		},
	}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	deleted, err := svc.DeleteResource(ctx, idea.Slug, "https://example.com/not-tracked")
	if err != nil {
		t.Fatalf("DeleteResource: %v", err)
	}
	if deleted {
		t.Errorf("expected deleted=false for unknown URL, got true")
	}

	got, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Resources) != 1 {
		t.Errorf("expected idea unchanged (1 resource), got %d", len(got.Resources))
	}
}

// Task 11b — state gating: LinkRepo + EnsureStartable refuse on archived;
// cleanup ops don't.

func TestService_LinkRepo_RefusesOnArchived(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	ctx := context.Background()

	idea := &model.Idea{Name: "Archived", Status: model.StatusArchived}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err := svc.LinkRepo(ctx, idea.Slug, t.TempDir(), "", "myrepo")
	if !errors.Is(err, service.ErrIdeaArchived) {
		t.Errorf("err = %v, want ErrIdeaArchived", err)
	}
}

func TestService_EnsureStartable_RefusesOnArchived(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	ctx := context.Background()

	archived := &model.Idea{Name: "Archived", Status: model.StatusArchived}
	if err := svc.Create(ctx, archived); err != nil {
		t.Fatalf("Create archived: %v", err)
	}
	if err := svc.EnsureStartable(ctx, archived.Slug); !errors.Is(err, service.ErrIdeaArchived) {
		t.Errorf("archived: err = %v, want ErrIdeaArchived", err)
	}

	for _, st := range []model.Status{model.StatusActive, model.StatusPaused} {
		other := &model.Idea{Name: "Other " + string(st), Status: st}
		if err := svc.Create(ctx, other); err != nil {
			t.Fatalf("Create %s: %v", st, err)
		}
		if err := svc.EnsureStartable(ctx, other.Slug); err != nil {
			t.Errorf("%s: err = %v, want nil", st, err)
		}
	}

	// Missing idea passes through (downstream produces the not-found shape).
	if err := svc.EnsureStartable(ctx, "no-such-idea"); err != nil {
		t.Errorf("missing idea: err = %v, want nil", err)
	}
}

func TestService_CleanupOps_AllowedOnArchived(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	ctx := context.Background()

	idea := &model.Idea{
		Name:   "Archived",
		Status: model.StatusArchived,
		Resources: []model.Resource{
			{Type: "web", URL: "https://example.com/x", Label: "x"},
		},
	}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := svc.AddResource(ctx, idea.Slug, model.Resource{Type: "notion", URL: "https://notion.so/y", Label: "y"}); err != nil {
		t.Errorf("AddResource on archived: %v", err)
	}
	if _, err := svc.DeleteResource(ctx, idea.Slug, "https://example.com/x"); err != nil {
		t.Errorf("DeleteResource on archived: %v", err)
	}
}
