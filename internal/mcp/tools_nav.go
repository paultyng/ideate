package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Nav tools — orchestrator-only. Each tool's handler emits a single
// `orchestrator:navigate` Wails event with `{path: string}` payload; the
// frontend's App-level listener calls `useNavigate()(path)` so the
// underlying view changes while the orchestrator drawer stays open.
// Deliberately not coupled to `create_idea`: spawning N ideas should
// not trigger N navigations.

func gotoIdeaTool() mcp.Tool {
	return mcp.Tool{
		Name:        "goto_idea",
		Description: desc("goto_idea"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug": map[string]any{
					"type":        "string",
					"description": "Idea slug as returned by list_ideas or create_idea.",
				},
			},
			Required: []string{"slug"},
		},
	}
}

func gotoDashboardTool() mcp.Tool {
	return mcp.Tool{
		Name:        "goto_dashboard",
		Description: desc("goto_dashboard"),
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{},
		},
	}
}

func gotoSessionTool() mcp.Tool {
	return mcp.Tool{
		Name:        "goto_session",
		Description: desc("goto_session"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug": map[string]any{
					"type":        "string",
					"description": "Idea slug.",
				},
				"uuid": map[string]any{
					"type":        "string",
					"description": "Session UUID.",
				},
			},
			Required: []string{"slug", "uuid"},
		},
	}
}

func (m *Manager) emitNavigate(path string) error {
	if m.events == nil {
		return fmt.Errorf("nav events not wired (no event func)")
	}
	m.emit(EventOrchestratorNavigate, map[string]any{"path": path})
	return nil
}

func (m *Manager) handleGotoIdea(sessionID string) server.ToolHandlerFunc {
	return func(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		slug := strings.TrimSpace(request.GetString("slug", ""))
		if slug == "" {
			return mcp.NewToolResultError("slug is required"), nil
		}
		if err := m.emitNavigate("/idea/" + slug); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("ok"), nil
	}
}

func (m *Manager) handleGotoDashboard(sessionID string) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		if err := m.emitNavigate("/"); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("ok"), nil
	}
}

func (m *Manager) handleGotoSession(sessionID string) server.ToolHandlerFunc {
	return func(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		slug := strings.TrimSpace(request.GetString("slug", ""))
		uuid := strings.TrimSpace(request.GetString("uuid", ""))
		if slug == "" || uuid == "" {
			return mcp.NewToolResultError("slug and uuid are required"), nil
		}
		if err := m.emitNavigate("/idea/" + slug + "/session/" + uuid); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("ok"), nil
	}
}
