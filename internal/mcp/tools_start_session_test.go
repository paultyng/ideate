package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// fakeSessionStarter records calls and returns canned values.
type fakeSessionStarter struct {
	calls []startCall
	// If err is non-nil the next call returns it; otherwise the
	// canned uuid.
	err  error
	uuid string
}

type startCall struct {
	slug      string
	agentType string
	resume    bool
}

func (f *fakeSessionStarter) StartIdeaSession(slug, agentType string, resume bool) (string, error) {
	f.calls = append(f.calls, startCall{slug: slug, agentType: agentType, resume: resume})
	if f.err != nil {
		return "", f.err
	}
	return f.uuid, nil
}

func TestStartIdeaSession_DefaultAgentType(t *testing.T) {
	t.Parallel()

	m := NewManager(newFakeStore(), &fakeResolver{}, nil)
	starter := &fakeSessionStarter{uuid: "uuid-1"}
	m.SetSessionStarter(starter)

	res, err := m.handleStartIdeaSession()(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "start_idea_session",
			Arguments: map[string]any{"slug": "test-idea"},
		},
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", contentText(res))
	}
	if len(starter.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(starter.calls))
	}
	got := starter.calls[0]
	if got.slug != "test-idea" || got.agentType != "claude-code" || got.resume != false {
		t.Errorf("unexpected call: %+v", got)
	}
	if !contentContains(res, `"uuid":"uuid-1"`) {
		t.Errorf("expected uuid in result, got %s", contentText(res))
	}
	if contentContains(res, "coordId") {
		t.Errorf("coordId leaked into MCP wire output: %s", contentText(res))
	}
}

func TestStartIdeaSession_PassesAgentTypeAndResume(t *testing.T) {
	t.Parallel()

	m := NewManager(newFakeStore(), &fakeResolver{}, nil)
	starter := &fakeSessionStarter{uuid: "uuid-2"}
	m.SetSessionStarter(starter)

	_, err := m.handleStartIdeaSession()(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "start_idea_session",
			Arguments: map[string]any{
				"slug":       "other-idea",
				"agent_type": "testagent",
				"resume":     true,
			},
		},
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := starter.calls[0]
	if got.slug != "other-idea" || got.agentType != "testagent" || got.resume != true {
		t.Errorf("unexpected call: %+v", got)
	}
}

func TestStartIdeaSession_MissingSlug(t *testing.T) {
	t.Parallel()

	m := NewManager(newFakeStore(), &fakeResolver{}, nil)
	starter := &fakeSessionStarter{}
	m.SetSessionStarter(starter)

	res, err := m.handleStartIdeaSession()(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "start_idea_session",
			Arguments: map[string]any{},
		},
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true on missing slug; got %s", contentText(res))
	}
	if len(starter.calls) != 0 {
		t.Errorf("expected zero starter calls when slug is missing; got %d", len(starter.calls))
	}
}

func TestStartIdeaSession_StarterError(t *testing.T) {
	t.Parallel()

	m := NewManager(newFakeStore(), &fakeResolver{}, nil)
	starter := &fakeSessionStarter{err: errors.New("session already running")}
	m.SetSessionStarter(starter)

	res, err := m.handleStartIdeaSession()(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "start_idea_session",
			Arguments: map[string]any{"slug": "test-idea"},
		},
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true on starter failure; got %s", contentText(res))
	}
	if !contentContains(res, "session already running") {
		t.Errorf("expected error message to surface; got %s", contentText(res))
	}
}

func TestStartIdeaSession_NoStarterWired(t *testing.T) {
	t.Parallel()

	m := NewManager(newFakeStore(), &fakeResolver{}, nil)
	// Intentionally skip SetSessionStarter.

	res, err := m.handleStartIdeaSession()(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "start_idea_session",
			Arguments: map[string]any{"slug": "test-idea"},
		},
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true when starter is unwired; got %s", contentText(res))
	}
}
