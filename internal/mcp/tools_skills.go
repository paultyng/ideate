package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// SkillsManager is the consumer-site interface for the orchestrator
// skill bundle. The App provides an adapter that forwards to
// internal/skills. Defined here so the mcp package doesn't import
// internal/skills directly.
type SkillsManager interface {
	List() []SkillStatus
	Reset(name string) (resetNames []string, err error)
}

// SkillStatus mirrors the public shape returned by internal/skills.List.
// Duplicated here so the interface is self-contained (the mcp package
// stays free of internal/skills imports).
type SkillStatus struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	Path         string `json:"path"`
	CanonicalSHA string `json:"canonical_sha256"`
	OnDiskSHA    string `json:"on_disk_sha256,omitempty"`
}

// SetSkillsManager wires the adapter onto the manager. Called once
// on App.Startup. Nil disables the tools.
func (m *Manager) SetSkillsManager(sm SkillsManager) {
	m.mu.Lock()
	m.skills = sm
	m.mu.Unlock()
}

func listDefaultSkillsTool() mcp.Tool {
	return mcp.Tool{
		Name:        "list_default_skills",
		Description: desc("list_default_skills"),
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{},
		},
	}
}

func (m *Manager) handleListDefaultSkills() server.ToolHandlerFunc {
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		m.mu.RLock()
		sm := m.skills
		m.mu.RUnlock()
		if sm == nil {
			return mcp.NewToolResultError("skills manager is not wired"), nil
		}
		body, err := json.Marshal(sm.List())
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("encoding result: %v", err)), nil
		}
		return mcp.NewToolResultText(string(body)), nil
	}
}

func resetDefaultSkillTool() mcp.Tool {
	return mcp.Tool{
		Name:        "reset_default_skill",
		Description: desc("reset_default_skill"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Canonical skill name to reset. Omit to reset every default skill.",
				},
			},
		},
	}
}

func (m *Manager) handleResetDefaultSkill() server.ToolHandlerFunc {
	return func(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		m.mu.RLock()
		sm := m.skills
		m.mu.RUnlock()
		if sm == nil {
			return mcp.NewToolResultError("skills manager is not wired"), nil
		}
		name := request.GetString("name", "")
		done, err := sm.Reset(name)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		body, err := json.Marshal(map[string]any{"reset": done})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("encoding result: %v", err)), nil
		}
		return mcp.NewToolResultText(string(body)), nil
	}
}
