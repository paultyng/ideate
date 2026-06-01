package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// fakeSleepController records SetSleepEnabled calls and returns a
// configurable SleepState. Held mirrors enabled by default to model
// the typical "toggle on with a busy session" path.
type fakeSleepController struct {
	enabled bool
	held    bool
	calls   int
}

func (f *fakeSleepController) SetSleepEnabled(enabled bool) {
	f.enabled = enabled
	f.held = enabled // simulate a busy session
	f.calls++
}

func (f *fakeSleepController) SleepState() (enabled, held bool) {
	return f.enabled, f.held
}

func TestSetSleepEnabled_TogglesAndReturnsState(t *testing.T) {
	t.Parallel()

	m := NewManager(newFakeStore(), &fakeResolver{}, nil)
	sc := &fakeSleepController{}
	m.SetSleepController(sc)

	handler := m.handleSetSleepEnabled()

	// Toggle on.
	res, err := handler(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "set_sleep_enabled",
			Arguments: map[string]any{"enabled": true},
		},
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res.Content)
	}
	if sc.calls != 1 || !sc.enabled {
		t.Errorf("controller not toggled on; calls=%d enabled=%v", sc.calls, sc.enabled)
	}
	if !contentContains(res, `"enabled":true`) || !contentContains(res, `"held":true`) {
		t.Errorf("expected enabled+held both true in result; got %s", contentText(res))
	}

	// Toggle off.
	res, err = handler(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "set_sleep_enabled",
			Arguments: map[string]any{"enabled": false},
		},
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if sc.enabled || sc.held {
		t.Errorf("controller not toggled off; enabled=%v held=%v", sc.enabled, sc.held)
	}
	if !contentContains(res, `"enabled":false`) || !contentContains(res, `"held":false`) {
		t.Errorf("expected enabled+held both false; got %s", contentText(res))
	}
}

func TestSetSleepEnabled_NoControllerWiredReturnsError(t *testing.T) {
	t.Parallel()

	m := NewManager(newFakeStore(), &fakeResolver{}, nil)
	// Intentionally skip SetSleepController.

	res, err := m.handleSetSleepEnabled()(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "set_sleep_enabled",
			Arguments: map[string]any{"enabled": true},
		},
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true when controller is unwired; got %+v", res.Content)
	}
}

func contentText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func contentContains(res *mcp.CallToolResult, needle string) bool {
	return strings.Contains(contentText(res), needle)
}
