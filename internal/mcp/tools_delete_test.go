package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/pubsub"
	"github.com/paultyng/ideate/internal/store"
)

// Reuses the rename setup helper (real FSStore) — delete cares about
// the same store wiring + event broker subscription pattern.
func setupDeleteManager(t *testing.T) (renameTestEnv, context.Context) {
	t.Helper()
	return setupRenameManager(t), context.Background()
}

func TestDeleteIdea_HandlerHappyPath(t *testing.T) {
	t.Parallel()
	env, ctx := setupDeleteManager(t)
	m, s, rec := env.m, env.store, env.rec

	idea := &model.Idea{Name: "Delete Me", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatal(err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"slug": idea.Slug}
	res, err := m.handleDeleteIdea("scratch-ses")(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler: %v isErr=%v content=%v", err, res.IsError, res.Content)
	}

	// Idea dir is gone.
	if _, err := s.Get(ctx, idea.Slug); err == nil {
		t.Errorf("Get on deleted slug succeeded; expected error")
	}

	// idea:deleted event with the slug.
	ev := rec.next(t)
	if ev.name != EventIdeaDeleted {
		t.Errorf("event = %q, want %q", ev.name, EventIdeaDeleted)
	}
	p, ok := ev.data.(map[string]any)
	if !ok || p["slug"] != idea.Slug {
		t.Errorf("payload = %+v", ev.data)
	}
}

func TestDeleteIdea_HandlerRefusesOnRunningSession(t *testing.T) {
	t.Parallel()
	env, ctx := setupDeleteManager(t)
	m, s := env.m, env.store

	idea := &model.Idea{Name: "Busy Delete", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSession(ctx, idea.Slug, "running-uuid", model.AgentSession{
		UUID:   "running-uuid",
		Status: model.SessionStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"slug": idea.Slug}
	res, err := m.handleDeleteIdea("scratch-ses")(ctx, req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected tool-error result for running session")
	}

	// Idea still on disk.
	if _, err := os.Stat(filepath.Join(env.ideasDir, idea.Slug)); err != nil {
		t.Errorf("idea dir gone after refused delete: %v", err)
	}
}

func TestDeleteIdea_HandlerForceTrueBypassesDirtyCheck(t *testing.T) {
	t.Parallel()
	env, ctx := setupDeleteManager(t)
	m, s := env.m, env.store

	idea := &model.Idea{Name: "Forceable", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatal(err)
	}
	// No linked worktree → not actually dirty, but exercising the
	// force=true path through the handler is the assertion that
	// matters here.

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"slug": idea.Slug, "force": true}
	res, err := m.handleDeleteIdea("scratch-ses")(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler: %v isErr=%v", err, res.IsError)
	}

	if _, err := os.Stat(filepath.Join(env.ideasDir, idea.Slug)); !os.IsNotExist(err) {
		t.Errorf("idea dir still exists after force delete: %v", err)
	}
}

func TestDeleteIdea_HandlerRefusesEmptySlug(t *testing.T) {
	t.Parallel()
	env, ctx := setupDeleteManager(t)
	m := env.m

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"slug": ""}
	res, err := m.handleDeleteIdea("scratch-ses")(ctx, req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected tool-error result for empty slug")
	}
}

// --- delete_resource (session-scoped) handler tests ---

func TestDeleteResource_RemovesMatchingURL(t *testing.T) {
	t.Parallel()
	m, fs := setupManager(t)
	fs.deleteResourceResult = deleteResourceCall{deleted: true}
	drain := captureEvents(t, m)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"url": "https://github.com/owner/repo/pull/1"}
	res, err := m.handleDeleteResource("ses-1-test")(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("handler: %v isErr=%v content=%v", err, res.IsError, res.Content)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "https://github.com/owner/repo/pull/1") {
		t.Errorf("unexpected text: %q", text)
	}
	if len(fs.deleteResourceCalls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fs.deleteResourceCalls))
	}
	if evs := drain(); !slicesContains(evs, EventResourceDeleted) {
		t.Errorf("expected %q event, got %v", EventResourceDeleted, evs)
	}
}

func TestDeleteResource_IsIdempotent(t *testing.T) {
	t.Parallel()
	m, fs := setupManager(t)
	fs.deleteResourceResult = deleteResourceCall{deleted: false}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"url": "https://example.com/not-tracked"}
	res, err := m.handleDeleteResource("ses-1-test")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Errorf("expected non-error result for idempotent no-op, got error")
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "no-op") {
		t.Errorf("unexpected text: %q", text)
	}
}

func TestDeleteResource_RequiresURL(t *testing.T) {
	t.Parallel()
	m, _ := setupManager(t)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	res, err := m.handleDeleteResource("ses-1-test")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected tool-error result for missing url")
	}
}

// --- delete_resource_by_slug (orchestrator) handler tests ---

func TestDeleteResourceBySlug_RemovesMatchingURL(t *testing.T) {
	t.Parallel()
	m, fs := setupManager(t)
	fs.deleteResourceResult = deleteResourceCall{deleted: true}
	drain := captureEvents(t, m)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"slug": "test-idea", "url": "https://github.com/owner/repo/pull/1"}
	res, err := m.handleDeleteResourceBySlug("orch")(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("handler: %v isErr=%v content=%v", err, res.IsError, res.Content)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "https://github.com/owner/repo/pull/1") {
		t.Errorf("unexpected text: %q", text)
	}
	if evs := drain(); !slicesContains(evs, EventResourceDeleted) {
		t.Errorf("expected %q event, got %v", EventResourceDeleted, evs)
	}
}

func TestDeleteResourceBySlug_RequiresSlugAndURL(t *testing.T) {
	t.Parallel()
	m, _ := setupManager(t)

	for _, args := range []map[string]any{
		{"slug": "", "url": "https://x.com"},
		{"slug": "test-idea", "url": ""},
		{},
	} {
		req := mcp.CallToolRequest{}
		req.Params.Arguments = args
		res, err := m.handleDeleteResourceBySlug("orch")(context.Background(), req)
		if err != nil {
			t.Fatalf("handler error: %v", err)
		}
		if !res.IsError {
			t.Errorf("expected tool-error for args %v", args)
		}
	}
}

// slicesContains reports whether ss contains s. Replaces slices.Contains to
// avoid adding a dep on Go 1.21+ slices package if not already imported.
func slicesContains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// Compile-time assert the unused imports stay tied to packages we
// rely on so future trimming doesn't silently drop them.
var _ = pubsub.New[pubsub.Event]
var _ = store.NewFSStore
