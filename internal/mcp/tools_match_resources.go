package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func matchResourceURLsTool() mcp.Tool {
	return mcp.Tool{
		Name:        "match_resource_urls",
		Description: desc("match_resource_urls"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"urls": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Bulk list of URLs to look up. Canonicalized server-side (SSH/HTTPS, trailing slash, .git suffix, host case all collapse), so callers can pass URLs in whatever form they have.",
				},
			},
			Required: []string{"urls"},
		},
	}
}

func (m *Manager) handleMatchResourceURLs(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		args := request.GetArguments()
		raw, ok := optionalStringSlice(args, "urls")
		if !ok {
			return mcp.NewToolResultError("urls is required (array of strings)"), nil
		}
		matches, err := m.store.MatchResourceURLs(ctx, raw)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("matching resource URLs: %v", err)), nil
		}
		data, err := json.MarshalIndent(matches, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshaling: %v", err)), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}
