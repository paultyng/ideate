package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/paultyng/ideate/internal/pubsub"
)

// recordingEventFn captures every emitted (event, data) pair so nav-
// tool tests can assert payload shape without standing up a Wails
// runtime. Backed by a real pubsub broker subscription so the test
// reads emit traffic the same way the App's bridge goroutine does.
type recordingEventFn struct {
	ch     <-chan pubsub.Event
	cancel func()
}

type recordedEvent struct {
	name string
	data any
}

// next blocks until the manager publishes its next event, or fails
// the test on timeout. Single-event tests call this once; multi-
// event tests call it once per expected emit.
func (r *recordingEventFn) next(t *testing.T) recordedEvent {
	t.Helper()
	select {
	case ev := <-r.ch:
		return recordedEvent{name: ev.Name, data: ev.Data}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for emit")
		return recordedEvent{}
	}
}

func setupNavManager(t *testing.T) (*Manager, *recordingEventFn) {
	t.Helper()
	br := pubsub.New[pubsub.Event]()
	ch, cancel := br.Subscribe()
	t.Cleanup(cancel)
	rec := &recordingEventFn{ch: ch, cancel: cancel}
	store := newFakeStore()
	resolver := &fakeResolver{mapping: map[string]string{}}
	m := NewManager(store, resolver, br)
	return m, rec
}

func TestGotoIdea_EmitsNavigateEvent(t *testing.T) {
	t.Parallel()
	m, rec := setupNavManager(t)

	handler := m.handleGotoIdea("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"slug": "alpha"}

	res, err := handler(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("handler error: %v %v", err, res.Content)
	}
	ev := rec.next(t)
	if ev.name != EventOrchestratorNavigate {
		t.Errorf("event name = %q, want %q", ev.name, EventOrchestratorNavigate)
	}
	payload, ok := ev.data.(map[string]any)
	if !ok {
		t.Fatalf("payload shape: %T", ev.data)
	}
	if payload["path"] != "/idea/alpha" {
		t.Errorf("path = %v, want /idea/alpha", payload["path"])
	}
}

func TestGotoDashboard_EmitsRootPath(t *testing.T) {
	t.Parallel()
	m, rec := setupNavManager(t)

	res, err := m.handleGotoDashboard("orchestrator-ses")(context.Background(), mcp.CallToolRequest{})
	if err != nil || res.IsError {
		t.Fatalf("handler error: %v %v", err, res.Content)
	}
	if got := rec.next(t).data.(map[string]any)["path"]; got != "/" {
		t.Errorf("path = %v, want /", got)
	}
}

func TestGotoSession_EmitsCompositePath(t *testing.T) {
	t.Parallel()
	m, rec := setupNavManager(t)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"slug": "beta", "uuid": "abc-123"}

	res, err := m.handleGotoSession("orchestrator-ses")(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("handler error: %v %v", err, res.Content)
	}
	if got := rec.next(t).data.(map[string]any)["path"]; got != "/idea/beta/session/abc-123" {
		t.Errorf("path = %v", got)
	}
}

func TestGoto_RequiresArgs(t *testing.T) {
	t.Parallel()
	m, rec := setupNavManager(t)

	cases := []struct {
		name    string
		handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
		args    map[string]any
	}{
		{"goto_idea empty slug", m.handleGotoIdea("ses"), map[string]any{"slug": ""}},
		{"goto_session missing uuid", m.handleGotoSession("ses"), map[string]any{"slug": "alpha"}},
		{"goto_session missing slug", m.handleGotoSession("ses"), map[string]any{"uuid": "u"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = tc.args
			res, err := tc.handler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if !res.IsError {
				t.Errorf("expected validation error")
			}
		})
	}
	// No nav events should have fired through the validation failures.
	select {
	case ev := <-rec.ch:
		t.Errorf("validation failure emitted unexpected event %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}
