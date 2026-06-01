// Package mcp — integration test exercising the archive_idea MCP handler
// end-to-end through *service.IdeaService and *store.FSStore with a real
// filesystem under t.TempDir(). Lives in package mcp (internal) rather than
// a separate app_test package because handleArchiveIdea is unexported; this
// is the lowest-friction home that lets the test call it without an export
// shim.
package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/paultyng/ideate/internal/agent"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/service"
	"github.com/paultyng/ideate/internal/store"
)

// stubCoordinator satisfies service's sessionCoordinator interface.
// List returns the injected sessions; Stop records each UUID it's called with.
type stubCoordinator struct {
	sessions []agent.SessionInfo
	stopped  []string
}

func (c *stubCoordinator) List() []agent.SessionInfo { return c.sessions }
func (c *stubCoordinator) Stop(_ context.Context, uuid string) error {
	c.stopped = append(c.stopped, uuid)
	return nil
}

// initIntegrationRepo creates a real git repo at dir with an initial commit
// and sets its origin remote to origin.
func initIntegrationRepo(t *testing.T, dir, origin string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--initial-branch=main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if origin != "" {
		run("config", "remote.origin.url", origin)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "initial")
}

func TestIntegration_ArchiveIdea_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	ideasDir := t.TempDir()
	reviewsDir := filepath.Join(t.TempDir(), "reviews")
	st := store.NewFSStore(ideasDir, reviewsDir, "pt/", "")

	// Real git repo for LinkRepo to attach.
	repoDir := t.TempDir()
	initIntegrationRepo(t, repoDir, "git@github.com:foo/bar.git")

	// One running session — Archive(force=true) should stop it.
	coord := &stubCoordinator{
		sessions: []agent.SessionInfo{{
			ID:       "ses-uuid-1",
			Status:   agent.StatusRunning,
			IdeaSlug: "", // filled after Create below
		}},
	}
	svc := service.New(st, coord)

	// Seed the idea.
	idea := &model.Idea{Name: "Archive Me", Status: model.StatusActive}
	if err := svc.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Wire the coordinator session to the new slug.
	coord.sessions[0].IdeaSlug = idea.Slug

	// Link the repo so Archive has a worktree to release.
	if _, err := svc.LinkRepo(ctx, idea.Slug, repoDir, "", "myrepo"); err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}

	// Build the MCP manager backed by the real service.
	// resolver=nil is fine: archive_idea resolves the slug from the
	// request arg, not from a session resolver.
	mgr := NewManager(svc, nil, nil)

	// Call the handler directly (unexported — must be package mcp).
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"slug": idea.Slug, "force": true}
	res, err := mgr.handleArchiveIdea("orch-ses")(ctx, req)
	if err != nil {
		t.Fatalf("handleArchiveIdea: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", res.Content)
	}

	// (a) Coordinator received a Stop call for the running session.
	if len(coord.stopped) != 1 || coord.stopped[0] != "ses-uuid-1" {
		t.Errorf("coord.stopped = %v, want [ses-uuid-1]", coord.stopped)
	}

	// (b) Idea status is now archived.
	after, err := svc.Get(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("Get after archive: %v", err)
	}
	if after.Status != model.StatusArchived {
		t.Errorf("status = %q, want %q", after.Status, model.StatusArchived)
	}

	// (c) Idea has a repo resource (persisted from LinkRepo's auto-track).
	hasRepo := false
	for _, r := range after.Resources {
		if r.Type == "repo" && r.URL != "" {
			hasRepo = true
		}
	}
	if !hasRepo {
		t.Errorf("no repo resource on archived idea; resources = %+v", after.Resources)
	}

	// (d) Worktree directory removed from disk.
	wt := filepath.Join(ideasDir, idea.Slug, "repos", "myrepo")
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree %q still present after archive", wt)
	}

	// (e) History contains an "archived" event.
	hist, err := svc.ReadHistory(ctx, idea.Slug)
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	found := false
	for _, h := range hist {
		if h.Event == "archived" {
			found = true
		}
	}
	if !found {
		t.Errorf(`no "archived" history event; got %d events`, len(hist))
	}
}
