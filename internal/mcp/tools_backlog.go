package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/store"
)

// Backlog tools mirror the resources surface but are bulk-by-default
// (per the CLAUDE.md MCP-batching rule). All mutations take arrays;
// invoke with a single-element array for the trivial case.
//
// Three operations × current-idea vs by-slug = 6 tools:
//   list_backlog                    list_backlog_by_slug
//   add_backlog_items               add_backlog_items_by_slug
//   update_backlog_items            update_backlog_items_by_slug
//   delete_backlog_items            delete_backlog_items_by_slug

// listBacklogTool — current-idea read.
func listBacklogTool() mcp.Tool {
	return mcp.Tool{
		Name:        "list_backlog",
		Description: desc("list_backlog"),
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: listBacklogFilterSchema(map[string]any{}),
		},
	}
}

// listBacklogFilterSchema adds the read-side projection + filter args
// (status, include_body) to an existing properties map. Shared by
// list_backlog and list_backlog_by_slug so both tools accept the same
// filter contract.
func listBacklogFilterSchema(props map[string]any) map[string]any {
	props["status"] = map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "string",
			"enum": []string{"open", "in_progress", "done", "wontfix"},
		},
		"description": "Optional status filter; returns only items whose status is in the given set. Pass `[\"open\"]` for the common triage case or `[\"open\", \"in_progress\"]` to surface active work. Omit to return all.",
	}
	props["include_body"] = map[string]any{
		"type":        "boolean",
		"description": "If true, include each item's `body` (Markdown context). Defaults to false because large backlogs blow tool-output caps otherwise. Set true when you need the body to pick up or summarize a task.",
	}
	return props
}

func addBacklogItemsTool() mcp.Tool {
	return mcp.Tool{
		Name:        "add_backlog_items",
		Description: desc("add_backlog_items"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"items": map[string]any{
					"type":        "array",
					"description": "One or more backlog items to add. Each must carry `title`; `body`, `status`, `depends_on`, `affects` are optional.",
					"items":       backlogItemInputSchema(),
				},
			},
			Required: []string{"items"},
		},
	}
}

func updateBacklogItemsTool() mcp.Tool {
	return mcp.Tool{
		Name:        "update_backlog_items",
		Description: desc("update_backlog_items"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"patches": map[string]any{
					"type":        "array",
					"description": "One or more patches. Each must carry `id` plus at least one of `title`, `body`, `status`, `depends_on`, `affects`. Slice fields replace; `[]` clears; omit to leave unchanged.",
					"items":       backlogPatchInputSchema(),
				},
			},
			Required: []string{"patches"},
		},
	}
}

func deleteBacklogItemsTool() mcp.Tool {
	return mcp.Tool{
		Name:        "delete_backlog_items",
		Description: desc("delete_backlog_items"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"ids": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "One or more item ids to delete. Unknown ids are reported in the response but do not abort the call (idempotent).",
				},
			},
			Required: []string{"ids"},
		},
	}
}

func listBacklogBySlugTool() mcp.Tool {
	return mcp.Tool{
		Name:        "list_backlog_by_slug",
		Description: desc("list_backlog_by_slug"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: listBacklogFilterSchema(map[string]any{
				"slug": map[string]any{"type": "string"},
			}),
			Required: []string{"slug"},
		},
	}
}

func addBacklogItemsBySlugTool() mcp.Tool {
	return mcp.Tool{
		Name:        "add_backlog_items_by_slug",
		Description: desc("add_backlog_items_by_slug"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug":  map[string]any{"type": "string", "description": "Target idea slug."},
				"items": map[string]any{"type": "array", "items": backlogItemInputSchema()},
			},
			Required: []string{"slug", "items"},
		},
	}
}

func updateBacklogItemsBySlugTool() mcp.Tool {
	return mcp.Tool{
		Name:        "update_backlog_items_by_slug",
		Description: desc("update_backlog_items_by_slug"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug":    map[string]any{"type": "string"},
				"patches": map[string]any{"type": "array", "items": backlogPatchInputSchema()},
			},
			Required: []string{"slug", "patches"},
		},
	}
}

func deleteBacklogItemsBySlugTool() mcp.Tool {
	return mcp.Tool{
		Name:        "delete_backlog_items_by_slug",
		Description: desc("delete_backlog_items_by_slug"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug": map[string]any{"type": "string"},
				"ids":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			Required: []string{"slug", "ids"},
		},
	}
}

// backlogItemInputSchema is the shape of each entry in `items` for
// the add tools. Kept inline (not a top-level $ref) so callers see
// the field list directly in their tool-listing UI.
func backlogItemInputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":  map[string]any{"type": "string"},
			"body":   map[string]any{"type": "string"},
			"status": map[string]any{"type": "string", "enum": []string{"open", "in_progress", "done", "wontfix"}},
			"depends_on": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Bare id for same-idea; \"slug:id\" for cross-idea. Stored verbatim; no cycle detection.",
			},
			"affects": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "File paths this item is expected to touch, relative to the idea root. Enables subagent parallelization on non-overlapping sets.",
			},
			"external_url": map[string]any{
				"type":        "string",
				"description": "Upstream tracker URL the item mirrors — GitHub issue, Jira ticket, Todoist task, etc. Both the navigation target and the canonical sync identity. Empty for local-only items.",
			},
		},
		"required": []string{"title"},
	}
}

func backlogPatchInputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":     map[string]any{"type": "string"},
			"title":  map[string]any{"type": "string"},
			"body":   map[string]any{"type": "string"},
			"status": map[string]any{"type": "string", "enum": []string{"open", "in_progress", "done", "wontfix"}},
			"depends_on": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"affects": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"external_url": map[string]any{
				"type":        "string",
				"description": "Set the upstream tracker URL; empty leaves the existing value alone.",
			},
		},
		"required": []string{"id"},
	}
}

// ----- Handlers ------------------------------------------------------------

func (m *Manager) handleListBacklog(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, slug, err := m.resolveIdea(ctx, sessionID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return m.listBacklogResponse(ctx, slug, request)
	}
}

func (m *Manager) handleListBacklogBySlug(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		slug := request.GetString("slug", "")
		if slug == "" {
			return mcp.NewToolResultError("slug is required"), nil
		}
		return m.listBacklogResponse(ctx, slug, request)
	}
}

func (m *Manager) listBacklogResponse(ctx context.Context, slug string, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	statuses, includeBody, err := parseListBacklogArgs(request.GetArguments())
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	items, err := m.store.ListBacklog(ctx, slug)
	if err != nil {
		return marshalBacklog(nil, err)
	}
	return marshalBacklog(filterAndProjectBacklog(items, statuses, includeBody), nil)
}

// parseListBacklogArgs pulls the optional `status` (array of enum) and
// `include_body` (bool) filters off the raw MCP arguments map.
//
// Unknown status values return an error (vs. silently dropping) so
// callers learn the enum surface rather than getting empty results.
func parseListBacklogArgs(args map[string]any) ([]model.BacklogStatus, bool, error) {
	var statuses []model.BacklogStatus
	if v, ok := args["status"]; ok && v != nil {
		raw, ok := v.([]any)
		if !ok {
			return nil, false, errors.New("status must be an array of strings")
		}
		for i, e := range raw {
			s, ok := e.(string)
			if !ok {
				return nil, false, fmt.Errorf("status[%d] must be a string", i)
			}
			parsed, err := validateBacklogStatus(s)
			if err != nil {
				return nil, false, fmt.Errorf("status[%d]: %w", i, err)
			}
			statuses = append(statuses, parsed)
		}
	}
	var includeBody bool
	if v, ok := args["include_body"]; ok && v != nil {
		b, ok := v.(bool)
		if !ok {
			return nil, false, errors.New("include_body must be a boolean")
		}
		includeBody = b
	}
	return statuses, includeBody, nil
}

// validateBacklogStatus checks a status string against the BacklogStatus
// enum. Used by every tool that accepts a status from MCP input — reads
// (list_backlog filter) and writes (add_backlog_items, update_backlog_items)
// share the same validation so corrupt values can't be stored on the
// write side and then silently read-repaired to `open` on the read side.
func validateBacklogStatus(s string) (model.BacklogStatus, error) {
	switch model.BacklogStatus(s) {
	case model.BacklogStatusOpen, model.BacklogStatusInProgress, model.BacklogStatusDone, model.BacklogStatusWontFix:
		return model.BacklogStatus(s), nil
	default:
		return "", fmt.Errorf("%q is not one of open|in_progress|done|wontfix", s)
	}
}

// filterAndProjectBacklog applies the optional status filter and, when
// includeBody is false, strips `Body` from each returned item.
//
// Default-drop-body is the projection that keeps `list_backlog`
// responses under the harness tool-output cap on real ideas (the
// driver behind backlog item e1d17b25). Callers that need bodies
// opt in via include_body=true.
func filterAndProjectBacklog(items []model.BacklogItem, statuses []model.BacklogStatus, includeBody bool) []model.BacklogItem {
	statusSet := make(map[model.BacklogStatus]struct{}, len(statuses))
	for _, s := range statuses {
		statusSet[s] = struct{}{}
	}
	out := make([]model.BacklogItem, 0, len(items))
	for _, item := range items {
		if len(statusSet) > 0 {
			if _, ok := statusSet[item.Status]; !ok {
				continue
			}
		}
		if !includeBody {
			item.Body = ""
		}
		out = append(out, item)
	}
	return out
}

func (m *Manager) handleAddBacklogItems(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, slug, err := m.resolveIdea(ctx, sessionID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return m.addBacklogItemsBulk(ctx, slug, "session", sessionID, request)
	}
}

func (m *Manager) handleAddBacklogItemsBySlug(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		slug := request.GetString("slug", "")
		if slug == "" {
			return mcp.NewToolResultError("slug is required"), nil
		}
		source := "session"
		if isOrch, _ := m.isOrchestratorUUID(ctx, sessionID); isOrch {
			source = "orchestrator"
		}
		return m.addBacklogItemsBulk(ctx, slug, source, sessionID, request)
	}
}

func (m *Manager) handleUpdateBacklogItems(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, slug, err := m.resolveIdea(ctx, sessionID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return m.updateBacklogItemsBulk(ctx, slug, sessionID, request)
	}
}

func (m *Manager) handleUpdateBacklogItemsBySlug(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		slug := request.GetString("slug", "")
		if slug == "" {
			return mcp.NewToolResultError("slug is required"), nil
		}
		return m.updateBacklogItemsBulk(ctx, slug, sessionID, request)
	}
}

func (m *Manager) handleDeleteBacklogItems(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, slug, err := m.resolveIdea(ctx, sessionID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return m.deleteBacklogItemsBulk(ctx, slug, sessionID, request)
	}
}

func (m *Manager) handleDeleteBacklogItemsBySlug(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		slug := request.GetString("slug", "")
		if slug == "" {
			return mcp.NewToolResultError("slug is required"), nil
		}
		return m.deleteBacklogItemsBulk(ctx, slug, sessionID, request)
	}
}

// ----- Bulk implementations ------------------------------------------------

func marshalBacklog(items []model.BacklogItem, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("listing backlog: %v", err)), nil
	}
	if items == nil {
		items = []model.BacklogItem{}
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshaling: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (m *Manager) addBacklogItemsBulk(
	ctx context.Context,
	slug, source, sessionID string,
	request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	raw, ok := request.GetArguments()["items"].([]any)
	if !ok {
		return mcp.NewToolResultError("items is required (array)"), nil
	}
	if len(raw) == 0 {
		return mcp.NewToolResultError("items must contain at least one entry"), nil
	}

	stored := make([]model.BacklogItem, 0, len(raw))
	for i, entry := range raw {
		fields, ok := entry.(map[string]any)
		if !ok {
			return mcp.NewToolResultError(fmt.Sprintf("items[%d] is not an object", i)), nil
		}
		title, _ := fields["title"].(string)
		if title == "" {
			return mcp.NewToolResultError(fmt.Sprintf("items[%d].title is required", i)), nil
		}
		item := model.BacklogItem{
			Title:  title,
			Source: source,
		}
		if v, ok := fields["body"].(string); ok {
			item.Body = v
		}
		if v, ok := fields["status"].(string); ok && v != "" {
			parsed, err := validateBacklogStatus(v)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("items[%d].status: %v", i, err)), nil
			}
			item.Status = parsed
		}
		if v, ok := fields["external_url"].(string); ok {
			item.ExternalURL = v
		}
		if deps, ok := stringSliceFromMap(fields, "depends_on"); ok {
			item.DependsOn = deps
		}
		if affects, ok := stringSliceFromMap(fields, "affects"); ok {
			item.Affects = affects
		}
		stored = append(stored, item)
	}

	results := make([]model.BacklogItem, 0, len(stored))
	for _, item := range stored {
		out, err := m.store.AddBacklogItem(ctx, slug, item)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("adding backlog item %q: %v", item.Title, err)), nil
		}
		_ = m.store.AppendHistory(ctx, slug, model.HistoryEvent{
			Timestamp: time.Now(),
			Event:     "backlog_item_added",
			Session:   sessionID,
			Fields: map[string]any{
				"id":    out.ID,
				"title": out.Title,
			},
		})
		results = append(results, out)
	}

	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshaling: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (m *Manager) updateBacklogItemsBulk(
	ctx context.Context,
	slug, sessionID string,
	request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	raw, ok := request.GetArguments()["patches"].([]any)
	if !ok {
		return mcp.NewToolResultError("patches is required (array)"), nil
	}
	if len(raw) == 0 {
		return mcp.NewToolResultError("patches must contain at least one entry"), nil
	}

	type result struct {
		ID     string `json:"id"`
		Status string `json:"status"` // ok | not_found | error
		Error  string `json:"error,omitempty"`
	}
	results := make([]result, 0, len(raw))
	for i, entry := range raw {
		fields, ok := entry.(map[string]any)
		if !ok {
			return mcp.NewToolResultError(fmt.Sprintf("patches[%d] is not an object", i)), nil
		}
		id, _ := fields["id"].(string)
		if id == "" {
			return mcp.NewToolResultError(fmt.Sprintf("patches[%d].id is required", i)), nil
		}
		patch := model.BacklogItem{}
		if v, ok := fields["title"].(string); ok {
			patch.Title = v
		}
		if v, ok := fields["body"].(string); ok {
			patch.Body = v
		}
		if v, ok := fields["status"].(string); ok && v != "" {
			parsed, err := validateBacklogStatus(v)
			if err != nil {
				results = append(results, result{ID: id, Status: "error", Error: fmt.Sprintf("status: %v", err)})
				continue
			}
			patch.Status = parsed
		}
		if v, ok := fields["external_url"].(string); ok && v != "" {
			patch.ExternalURL = v
		}
		if deps, ok := stringSliceFromMap(fields, "depends_on"); ok {
			patch.DependsOn = deps
		}
		if affects, ok := stringSliceFromMap(fields, "affects"); ok {
			patch.Affects = affects
		}
		if patch.Title == "" && patch.Body == "" && patch.Status == "" && patch.ExternalURL == "" && patch.DependsOn == nil && patch.Affects == nil {
			results = append(results, result{ID: id, Status: "error", Error: "no fields supplied"})
			continue
		}
		err := m.store.UpdateBacklogItem(ctx, slug, id, patch)
		switch {
		case errors.Is(err, store.ErrBacklogItemNotFound):
			results = append(results, result{ID: id, Status: "not_found"})
		case err != nil:
			results = append(results, result{ID: id, Status: "error", Error: err.Error()})
		default:
			results = append(results, result{ID: id, Status: "ok"})
			_ = m.store.AppendHistory(ctx, slug, model.HistoryEvent{
				Timestamp: time.Now(),
				Event:     "backlog_item_updated",
				Session:   sessionID,
				Fields:    map[string]any{"id": id, "status": string(patch.Status)},
			})
		}
	}

	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshaling: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (m *Manager) deleteBacklogItemsBulk(
	ctx context.Context,
	slug, sessionID string,
	request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	raw, ok := request.GetArguments()["ids"].([]any)
	if !ok {
		return mcp.NewToolResultError("ids is required (array of strings)"), nil
	}
	if len(raw) == 0 {
		return mcp.NewToolResultError("ids must contain at least one entry"), nil
	}
	ids := make([]string, 0, len(raw))
	for i, entry := range raw {
		s, ok := entry.(string)
		if !ok || s == "" {
			return mcp.NewToolResultError(fmt.Sprintf("ids[%d] must be a non-empty string", i)), nil
		}
		ids = append(ids, s)
	}

	deleted := make([]string, 0, len(ids))
	notFound := make([]string, 0)
	for _, id := range ids {
		found, err := m.store.DeleteBacklogItem(ctx, slug, id)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("deleting backlog item %q: %v", id, err)), nil
		}
		if !found {
			notFound = append(notFound, id)
			continue
		}
		deleted = append(deleted, id)
		_ = m.store.AppendHistory(ctx, slug, model.HistoryEvent{
			Timestamp: time.Now(),
			Event:     "backlog_item_deleted",
			Session:   sessionID,
			Fields:    map[string]any{"id": id},
		})
	}

	data, err := json.MarshalIndent(map[string]any{
		"deleted":   deleted,
		"not_found": notFound,
	}, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshaling: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

// stringSliceFromMap reads `key` from m. Returns (slice, true) when
// the key is present (even with an empty array — explicit clear),
// (nil, false) when absent. Non-string elements are dropped silently.
func stringSliceFromMap(m map[string]any, key string) ([]string, bool) {
	raw, ok := m[key]
	if !ok {
		return nil, false
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out, true
}
