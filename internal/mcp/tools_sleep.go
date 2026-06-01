package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// SleepController gates the in-app sleep inhibitor. Defined here at
// the consumer site so the mcp package only depends on the surface
// it actually uses; the App provides an adapter that forwards to its
// SetSleepEnabled / GetSleepState methods.
type SleepController interface {
	SetSleepEnabled(enabled bool)
	// SleepState returns (toggle, currently-held). Held is true only
	// when toggle=true AND at least one busy session exists.
	SleepState() (enabled, held bool)
}

// SetSleepController wires the sleep adapter onto the manager.
// Called once on App.Startup. Empty = the set_sleep_enabled tool
// returns an error rather than silently no-op'ing.
func (m *Manager) SetSleepController(sc SleepController) {
	m.mu.Lock()
	m.sleep = sc
	m.mu.Unlock()
}

func setSleepEnabledTool() mcp.Tool {
	return mcp.Tool{
		Name:        "set_sleep_enabled",
		Description: desc("set_sleep_enabled"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"enabled": map[string]any{
					"type":        "boolean",
					"description": "true to acquire the sleep inhibitor while sessions are busy; false to release it.",
				},
			},
			Required: []string{"enabled"},
		},
	}
}

func (m *Manager) handleSetSleepEnabled() server.ToolHandlerFunc {
	return func(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		m.mu.RLock()
		sc := m.sleep
		m.mu.RUnlock()
		if sc == nil {
			return mcp.NewToolResultError("sleep controller is not wired"), nil
		}
		enabled := request.GetBool("enabled", false)
		sc.SetSleepEnabled(enabled)
		gotEnabled, held := sc.SleepState()
		body, err := json.Marshal(map[string]any{
			"enabled": gotEnabled,
			"held":    held,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("encoding result: %v", err)), nil
		}
		return mcp.NewToolResultText(string(body)), nil
	}
}
