package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/store"
)

func TestArchiveIdea_HappyPath(t *testing.T) {
	t.Parallel()
	m, _ := setupManager(t)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"slug": "test-idea"}
	res, err := m.handleArchiveIdea("ses-1-test")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", res.Content)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "Archived test-idea") {
		t.Errorf("unexpected text: %q", text)
	}
}

func TestArchiveIdea_SessionSlugFallback(t *testing.T) {
	t.Parallel()
	// No slug in request — should fall back to session-resolved slug.
	m, _ := setupManager(t)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	res, err := m.handleArchiveIdea("ses-1-test")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", res.Content)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "Archived test-idea") {
		t.Errorf("unexpected text: %q", text)
	}
}

func TestArchiveIdea_RefusesOnDirty(t *testing.T) {
	t.Parallel()
	m, fs := setupManager(t)
	fs.archiveErr = &store.ErrDirtyRepos{Repos: []string{"foo"}}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"slug": "test-idea", "force": false}
	res, err := m.handleArchiveIdea("ses-1-test")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool-error result for dirty repos")
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(strings.ToLower(text), "uncommitted") && !strings.Contains(strings.ToLower(text), "dirty") {
		t.Errorf("error text does not mention uncommitted changes: %q", text)
	}
}

func TestArchiveIdea_RefusesOnRunningSession(t *testing.T) {
	t.Parallel()
	m, fs := setupManager(t)
	fs.archiveErr = store.ErrIdeaBusy

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"slug": "test-idea"}
	res, err := m.handleArchiveIdea("ses-1-test")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool-error result for running session")
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(strings.ToLower(text), "running") && !strings.Contains(strings.ToLower(text), "session") {
		t.Errorf("error text does not mention running session: %q", text)
	}
}

func TestUnarchiveIdea_HappyPath(t *testing.T) {
	t.Parallel()
	m, fs := setupManager(t)
	// Seed a repo resource so the response mentions re-link.
	fs.ideas["test-idea"].Resources = append(fs.ideas["test-idea"].Resources,
		model.Resource{Type: "repo", URL: "https://github.com/owner/repo", Label: "myrepo"},
	)
	// Override fakeStore.Unarchive to return a non-empty report.
	fs.unarchiveErr = nil

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"slug": "test-idea"}
	res, err := m.handleUnarchiveIdea("orch-ses")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", res.Content)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "Unarchived test-idea") {
		t.Errorf("unexpected text: %q", text)
	}
}

func TestUnarchiveIdea_RequiresSlug(t *testing.T) {
	t.Parallel()
	m, _ := setupManager(t)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	res, err := m.handleUnarchiveIdea("orch-ses")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error when slug absent")
	}
}

func TestPauseIdea_HappyPath(t *testing.T) {
	t.Parallel()
	m, _ := setupManager(t)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"slug": "test-idea"}
	res, err := m.handlePauseIdea("ses-1-test")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", res.Content)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "Paused test-idea") {
		t.Errorf("unexpected text: %q", text)
	}
}

func TestPauseIdea_WithUntil(t *testing.T) {
	t.Parallel()
	m, _ := setupManager(t)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"slug":  "test-idea",
		"until": "2026-06-01T09:00:00Z",
	}
	res, err := m.handlePauseIdea("ses-1-test")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", res.Content)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "until") {
		t.Errorf("expected 'until' in response text: %q", text)
	}
}

func TestPauseIdea_BadUntil(t *testing.T) {
	t.Parallel()
	m, _ := setupManager(t)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"slug":  "test-idea",
		"until": "not-a-date",
	}
	res, err := m.handlePauseIdea("ses-1-test")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error for bad until value")
	}
}

func TestResumeIdea_HappyPath(t *testing.T) {
	t.Parallel()
	m, _ := setupManager(t)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"slug": "test-idea"}
	res, err := m.handleResumeIdea("ses-1-test")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", res.Content)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "Resumed test-idea") {
		t.Errorf("unexpected text: %q", text)
	}
}

func TestResumeIdea_SessionSlugFallback(t *testing.T) {
	t.Parallel()
	m, _ := setupManager(t)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	res, err := m.handleResumeIdea("ses-1-test")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", res.Content)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "Resumed test-idea") {
		t.Errorf("unexpected text: %q", text)
	}
}
