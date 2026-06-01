package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/paultyng/ideate/internal/agent/transcript/claudefmt"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/pubsub"
	"github.com/paultyng/ideate/internal/store"
)

// renameTestEnv bundles everything a rename handler test needs: the
// manager, the real store, the locations of the ideas + claude-
// projects dirs, and a recording subscription for emit assertions.
type renameTestEnv struct {
	m           *Manager
	store       *store.FSStore
	ideasDir    string
	projectsDir string
	rec         *recordingEventFn
}

// Real FSStore + temp claudeProjectsDir so the handler exercises both
// the store-level rename and the transcript-dir migration.
func setupRenameManager(t *testing.T) renameTestEnv {
	t.Helper()
	br := pubsub.New[pubsub.Event]()
	ch, cancel := br.Subscribe()
	t.Cleanup(cancel)
	rec := &recordingEventFn{ch: ch, cancel: cancel}

	root := t.TempDir()
	ideasDir := filepath.Join(root, "ideas")
	reviewsDir := filepath.Join(root, "reviews")
	projectsDir := filepath.Join(root, "claude-projects")
	for _, d := range []string{ideasDir, reviewsDir, projectsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := store.NewFSStore(ideasDir, reviewsDir, "", "")
	resolver := &fakeResolver{mapping: map[string]string{}}
	m := NewManager(s, resolver, br)
	m.SetClaudeProjectsDir(projectsDir)
	return renameTestEnv{m: m, store: s, ideasDir: ideasDir, projectsDir: projectsDir, rec: rec}
}

func TestRenameIdea_HandlerHappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := setupRenameManager(t)
	m, s, rec := env.m, env.store, env.rec

	idea := &model.Idea{Name: "Original", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatal(err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"slug":     idea.Slug,
		"new_slug": "renamed-target",
	}
	res, err := m.handleRenameIdea("scratch-ses")(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler error: %v isErr=%v content=%v", err, res.IsError, res.Content)
	}

	// Tool returns the new slug as plain text — sanity check the dir
	// actually moved.
	if _, err := s.Get(ctx, "renamed-target"); err != nil {
		t.Errorf("Get on new slug: %v", err)
	}

	// idea:renamed event published with the expected payload shape.
	ev := rec.next(t)
	if ev.name != EventIdeaRenamed {
		t.Errorf("event = %q, want %q", ev.name, EventIdeaRenamed)
	}
	p, ok := ev.data.(map[string]any)
	if !ok {
		t.Fatalf("payload shape: %T", ev.data)
	}
	if p["old_slug"] != idea.Slug || p["new_slug"] != "renamed-target" {
		t.Errorf("payload slugs = %+v", p)
	}
}

func TestRenameIdea_HandlerMovesClaudeTranscriptDir(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := setupRenameManager(t)
	m, s, ideasDir, projectsDir := env.m, env.store, env.ideasDir, env.projectsDir

	idea := &model.Idea{Name: "Has Sessions", Status: model.StatusActive}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatal(err)
	}

	// Seed a session whose WorkingDir is the idea root (the
	// orchestrator-style case). The handler should rename the
	// claude-projects subdir keyed on that path.
	oldWD, err := filepath.Abs(filepath.Join(ideasDir, idea.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSession(ctx, idea.Slug, "uuid-1", model.AgentSession{
		UUID:       "uuid-1",
		Status:     model.SessionStatusCompleted,
		WorkingDir: oldWD,
	}); err != nil {
		t.Fatal(err)
	}

	// Plant a transcript directory under the OLD encoded path with
	// one .jsonl in it so we can prove it survived the move.
	oldEncDir := filepath.Join(projectsDir, claudefmt.EncodeProjectDir(oldWD))
	if err := os.MkdirAll(oldEncDir, 0o755); err != nil {
		t.Fatal(err)
	}
	canary := []byte(`{"type":"summary","sessionId":"uuid-1"}` + "\n")
	if err := os.WriteFile(filepath.Join(oldEncDir, "uuid-1.jsonl"), canary, 0o644); err != nil {
		t.Fatal(err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"slug": idea.Slug, "new_slug": "renamed-with-tx"}
	res, err := m.handleRenameIdea("scratch-ses")(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler: %v isErr=%v", err, res.IsError)
	}

	// Old encoded dir gone, new encoded dir present with the canary.
	if _, err := os.Stat(oldEncDir); !os.IsNotExist(err) {
		t.Errorf("old transcript dir still exists: %v", err)
	}
	_ = s
	newWD, _ := filepath.Abs(filepath.Join(ideasDir, "renamed-with-tx"))
	newEncDir := filepath.Join(projectsDir, claudefmt.EncodeProjectDir(newWD))
	got, err := os.ReadFile(filepath.Join(newEncDir, "uuid-1.jsonl"))
	if err != nil {
		t.Fatalf("transcript not at new encoded dir: %v", err)
	}
	if string(got) != string(canary) {
		t.Errorf("transcript payload corrupted: %q", got)
	}
}

func TestRenameIdea_HandlerRefusesEmptyArgs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := setupRenameManager(t).m

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"slug": ""}
	res, err := m.handleRenameIdea("scratch-ses")(ctx, req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected tool-error result for missing args")
	}
}
