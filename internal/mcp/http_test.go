package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/paultyng/ideate/internal/claudecode"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/pubsub"
)

// jsonRPCRequest builds a JSON-RPC 2.0 request body.
func jsonRPCRequest(id int, method string, params any) string {
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		body["params"] = params
	}
	data, _ := json.Marshal(body)
	return string(data)
}

// mcpSession initializes an MCP session and returns the MCP protocol session ID header.
func mcpSession(t *testing.T, url, ideateSessionID string) string {
	t.Helper()
	body := jsonRPCRequest(1, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
	})
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(claudecode.SessionHeader, ideateSessionID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("initialize status %d: %s", resp.StatusCode, b)
	}
	sid := resp.Header.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("no Mcp-Session-Id in initialize response")
	}
	return sid
}

// callTool sends a tools/call request and returns the parsed JSON-RPC result.
func callTool(t *testing.T, url, ideateSessionID, mcpSID, toolName string, args map[string]any) map[string]any {
	t.Helper()
	body := jsonRPCRequest(2, "tools/call", map[string]any{
		"name":      toolName,
		"arguments": args,
	})
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", mcpSID)
	req.Header.Set(claudecode.SessionHeader, ideateSessionID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("tools/call %s: %v", toolName, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("tools/call %s status %d: %s", toolName, resp.StatusCode, b)
	}

	var rpcResp map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return rpcResp
}

// extractText pulls the text string from a JSON-RPC tool result.
func extractText(t *testing.T, rpcResp map[string]any) string {
	t.Helper()
	result, ok := rpcResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result in response: %v", rpcResp)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("missing content in result: %v", result)
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] not a map: %v", content[0])
	}
	text, _ := first["text"].(string)
	return text
}

func setupHTTPTest(t *testing.T) (*httptest.Server, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	store.ideas["test-idea"] = &model.Idea{
		Slug:   "test-idea",
		Name:   "Test Idea",
		Status: model.StatusActive,
		Resources: []model.Resource{
			{Type: "github_pr", URL: "https://github.com/owner/repo/pull/1", Label: "Core PR"},
		},
	}

	resolver := &fakeResolver{mapping: map[string]string{
		"ses-http": "test-idea",
	}}

	m := NewManager(store, resolver, nil)
	m.RegisterSession("ses-http")

	mux := http.NewServeMux()
	mux.Handle("/mcp", m)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, store
}

func TestHTTPGetIdea(t *testing.T) {
	t.Parallel()
	ts, _ := setupHTTPTest(t)

	url := ts.URL + "/mcp"
	sid := mcpSession(t, url, "ses-http")

	rpcResp := callTool(t, url, "ses-http", sid, "get_idea", nil)
	text := extractText(t, rpcResp)

	var idea map[string]any
	if err := json.Unmarshal([]byte(text), &idea); err != nil {
		t.Fatalf("parsing idea JSON: %v", err)
	}
	if idea["name"] != "Test Idea" {
		t.Errorf("name = %q, want %q", idea["name"], "Test Idea")
	}
	if idea["status"] != "active" {
		t.Errorf("status = %q, want %q", idea["status"], "active")
	}
}

func TestHTTPListResources(t *testing.T) {
	t.Parallel()
	ts, _ := setupHTTPTest(t)

	url := ts.URL + "/mcp"
	sid := mcpSession(t, url, "ses-http")

	rpcResp := callTool(t, url, "ses-http", sid, "list_resources", nil)
	text := extractText(t, rpcResp)

	var resources []model.Resource
	if err := json.Unmarshal([]byte(text), &resources); err != nil {
		t.Fatalf("parsing resources: %v", err)
	}
	if len(resources) != 1 || resources[0].Label != "Core PR" {
		t.Errorf("resources = %+v", resources)
	}
}

func TestHTTPAddResource(t *testing.T) {
	t.Parallel()
	ts, store := setupHTTPTest(t)

	url := ts.URL + "/mcp"
	sid := mcpSession(t, url, "ses-http")

	rpcResp := callTool(t, url, "ses-http", sid, "add_resource", map[string]any{
		"type":  "notion",
		"url":   "https://notion.so/page",
		"label": "Design doc",
	})
	text := extractText(t, rpcResp)
	if !strings.Contains(text, "notion") {
		t.Errorf("unexpected response: %s", text)
	}

	idea := store.ideas["test-idea"]
	if len(idea.Resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(idea.Resources))
	}
	if idea.Resources[1].Label != "Design doc" {
		t.Errorf("added resource label = %q", idea.Resources[1].Label)
	}
	if len(store.history) != 1 || store.history[0].Event != "resource_added" {
		t.Errorf("history = %+v", store.history)
	}
}

func TestHTTPUpdateIdea(t *testing.T) {
	t.Parallel()
	ts, store := setupHTTPTest(t)

	url := ts.URL + "/mcp"
	sid := mcpSession(t, url, "ses-http")

	// status field is no longer accepted — use pause_idea / archive_idea / etc.
	rpcResp := callTool(t, url, "ses-http", sid, "update_idea", map[string]any{
		"summary": "New summary",
	})
	text := extractText(t, rpcResp)
	if !strings.Contains(text, "summary updated") {
		t.Errorf("unexpected response: %s", text)
	}

	idea := store.ideas["test-idea"]
	if idea.Body != "New summary" {
		t.Errorf("summary = %q", idea.Body)
	}
}

func TestHTTPMissingSessionHeader(t *testing.T) {
	t.Parallel()
	ts, _ := setupHTTPTest(t)

	body := jsonRPCRequest(1, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
	})
	// No session header — should get 400.
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHTTPUnknownSession(t *testing.T) {
	t.Parallel()
	ts, _ := setupHTTPTest(t)

	body := jsonRPCRequest(1, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(claudecode.SessionHeader, "unknown-session")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestHTTPEventFn(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.ideas["test-idea"] = &model.Idea{
		Slug:   "test-idea",
		Name:   "Test",
		Status: model.StatusActive,
	}
	resolver := &fakeResolver{mapping: map[string]string{"ses-ev": "test-idea"}}

	br := pubsub.New[pubsub.Event]()
	ch, _ := br.Subscribe()
	m := NewManager(store, resolver, br)
	m.RegisterSession("ses-ev")

	mux := http.NewServeMux()
	mux.Handle("/mcp", m)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	url := ts.URL + "/mcp"
	sid := mcpSession(t, url, "ses-ev")

	callTool(t, url, "ses-ev", sid, "add_resource", map[string]any{
		"type":  "doc",
		"label": "test",
	})

	select {
	case ev := <-ch:
		if ev.Name != "idea:resource_added" {
			t.Errorf("event name = %q, want idea:resource_added", ev.Name)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for emit")
	}
}

// TestHTTPSiblingIdeaTools — per-session MCP exposes the slug-based
// idea management tools so an agent inside `idea-A` can discover,
// spin off, read, and update `idea-B` without leaving its surface.
// addTools calls addCrossIdeaTools (see internal/mcp/server.go),
// which is what wires this surface; this test pins the contract.
func TestHTTPSiblingIdeaTools(t *testing.T) {
	t.Parallel()
	ts, store := setupHTTPTest(t)

	url := ts.URL + "/mcp"
	sid := mcpSession(t, url, "ses-http")

	// list_ideas — the per-session caller can discover sibling slugs.
	{
		rpcResp := callTool(t, url, "ses-http", sid, "list_ideas", nil)
		text := extractText(t, rpcResp)
		var summaries []map[string]any
		if err := json.Unmarshal([]byte(text), &summaries); err != nil {
			t.Fatalf("parsing list_ideas: %v", err)
		}
		if len(summaries) != 1 || summaries[0]["slug"] != "test-idea" {
			t.Errorf("list_ideas = %+v", summaries)
		}
	}

	// create_idea — spin off a sibling. Returns the new slug as text.
	var newSlug string
	{
		rpcResp := callTool(t, url, "ses-http", sid, "create_idea", map[string]any{
			"name":   "Sibling Idea",
			"status": "paused",
		})
		newSlug = extractText(t, rpcResp)
		if newSlug == "" {
			t.Fatal("create_idea returned empty slug")
		}
		if _, ok := store.ideas[newSlug]; !ok {
			t.Errorf("store missing newly-created idea %q", newSlug)
		}
	}

	// get_idea_by_slug — read the sibling back via its slug.
	{
		rpcResp := callTool(t, url, "ses-http", sid, "get_idea_by_slug", map[string]any{
			"slug": newSlug,
		})
		text := extractText(t, rpcResp)
		var idea map[string]any
		if err := json.Unmarshal([]byte(text), &idea); err != nil {
			t.Fatalf("parsing get_idea_by_slug: %v", err)
		}
		if idea["name"] != "Sibling Idea" {
			t.Errorf("name = %q, want Sibling Idea", idea["name"])
		}
	}

	// update_idea_by_slug — write to the sibling.
	{
		rpcResp := callTool(t, url, "ses-http", sid, "update_idea_by_slug", map[string]any{
			"slug": newSlug,
			"name": "Renamed Sibling",
		})
		if _, ok := rpcResp["result"]; !ok {
			t.Fatalf("update_idea_by_slug missing result: %v", rpcResp)
		}
		if store.ideas[newSlug].Name != "Renamed Sibling" {
			t.Errorf("name not persisted: %q", store.ideas[newSlug].Name)
		}
	}
}

// TestHTTPBacklogCRUD — per-session MCP exposes the four current-idea
// backlog tools end-to-end: add → list → update → delete. Pins the
// wire contract (item shape, idempotent delete, error on unknown id).
func TestHTTPBacklogCRUD(t *testing.T) {
	t.Parallel()
	ts, store := setupHTTPTest(t)

	url := ts.URL + "/mcp"
	sid := mcpSession(t, url, "ses-http")

	// list_backlog on a fresh idea returns []. Empty inline array is
	// the empty-state contract; a missing field would confuse callers.
	{
		rpcResp := callTool(t, url, "ses-http", sid, "list_backlog", nil)
		text := extractText(t, rpcResp)
		if text != "[]" && text != "[\n]" {
			t.Errorf("list_backlog empty = %q, want []", text)
		}
	}

	// add_backlog_items returns the stored items in input order with
	// ids + timestamps. Bulk shape: invoke with a single-element
	// array for the trivial case.
	var firstID, secondID string
	{
		rpcResp := callTool(t, url, "ses-http", sid, "add_backlog_items", map[string]any{
			"items": []any{
				map[string]any{"title": "write a regression test", "body": "covers dormant-resume"},
				map[string]any{"title": "ship the doc update"},
			},
		})
		text := extractText(t, rpcResp)
		var items []model.BacklogItem
		if err := json.Unmarshal([]byte(text), &items); err != nil {
			t.Fatalf("parsing add response: %v", err)
		}
		if len(items) != 2 {
			t.Fatalf("expected 2 stored items, got %d", len(items))
		}
		if items[0].ID == "" || items[0].Title != "write a regression test" {
			t.Errorf("first item = %+v", items[0])
		}
		if items[0].Status != model.BacklogStatusOpen {
			t.Errorf("default status = %q, want open", items[0].Status)
		}
		if items[0].Source != "session" {
			t.Errorf("source = %q, want session", items[0].Source)
		}
		firstID = items[0].ID
		secondID = items[1].ID
	}

	// list_backlog now returns both items.
	{
		rpcResp := callTool(t, url, "ses-http", sid, "list_backlog", nil)
		var items []model.BacklogItem
		_ = json.Unmarshal([]byte(extractText(t, rpcResp)), &items)
		if len(items) != 2 {
			t.Errorf("list = %d items, want 2", len(items))
		}
	}

	// update_backlog_items sweeps both: flip first to in_progress,
	// second to done. Per-patch result array exposes ok / not_found
	// / error per id without aborting the batch.
	{
		rpcResp := callTool(t, url, "ses-http", sid, "update_backlog_items", map[string]any{
			"patches": []any{
				map[string]any{"id": firstID, "status": "in_progress"},
				map[string]any{"id": secondID, "status": "done"},
				map[string]any{"id": "no-such-id", "status": "wontfix"},
			},
		})
		text := extractText(t, rpcResp)
		var results []map[string]any
		_ = json.Unmarshal([]byte(text), &results)
		if len(results) != 3 {
			t.Fatalf("results = %d, want 3", len(results))
		}
		if results[0]["status"] != "ok" || results[1]["status"] != "ok" {
			t.Errorf("first/second update should be ok: %+v", results)
		}
		if results[2]["status"] != "not_found" {
			t.Errorf("unknown id should be not_found: %+v", results[2])
		}
		if store.backlog["test-idea"][0].Status != model.BacklogStatusInProgress {
			t.Errorf("first status not persisted: %q", store.backlog["test-idea"][0].Status)
		}
		if store.backlog["test-idea"][1].Status != model.BacklogStatusDone {
			t.Errorf("second status not persisted: %q", store.backlog["test-idea"][1].Status)
		}
	}

	// Patch with no mutable fields surfaces as a per-item error,
	// not a tool-level abort — batch still applies the rest.
	{
		rpcResp := callTool(t, url, "ses-http", sid, "update_backlog_items", map[string]any{
			"patches": []any{map[string]any{"id": firstID}},
		})
		text := extractText(t, rpcResp)
		var results []map[string]any
		_ = json.Unmarshal([]byte(text), &results)
		if len(results) != 1 || results[0]["status"] != "error" {
			t.Errorf("empty patch should report error: %+v", results)
		}
	}

	// delete_backlog_items reports both deleted + not_found id sets
	// in one response — no separate idempotency dance per id.
	{
		rpcResp := callTool(t, url, "ses-http", sid, "delete_backlog_items", map[string]any{
			"ids": []any{firstID, secondID, "no-such-id"},
		})
		text := extractText(t, rpcResp)
		var out struct {
			Deleted  []string `json:"deleted"`
			NotFound []string `json:"not_found"`
		}
		_ = json.Unmarshal([]byte(text), &out)
		if len(out.Deleted) != 2 {
			t.Errorf("deleted = %v, want both ids", out.Deleted)
		}
		if len(out.NotFound) != 1 || out.NotFound[0] != "no-such-id" {
			t.Errorf("not_found = %v, want [no-such-id]", out.NotFound)
		}
		if len(store.backlog["test-idea"]) != 0 {
			t.Errorf("backlog should be empty post-delete: %+v", store.backlog["test-idea"])
		}
	}
}

// TestHTTPBacklogStatusValidation — bad-status values are rejected on
// every tool that accepts a status, so corrupt enum values can't be
// stored on write and silently read-repaired to `open` later. Read-side
// (`list_backlog` filter) and write-side (`add_backlog_items`,
// `update_backlog_items`) share the same `validateBacklogStatus`.
func TestHTTPBacklogStatusValidation(t *testing.T) {
	t.Parallel()
	ts, store := setupHTTPTest(t)

	url := ts.URL + "/mcp"
	sid := mcpSession(t, url, "ses-http")

	// add_backlog_items with bad status aborts the whole batch with a
	// tool-level error. Per-item validation; no items persisted.
	{
		rpcResp := callTool(t, url, "ses-http", sid, "add_backlog_items", map[string]any{
			"items": []any{
				map[string]any{"title": "valid", "status": "open"},
				map[string]any{"title": "bad", "status": "pending"},
			},
		})
		text := extractText(t, rpcResp)
		if !strings.Contains(text, "items[1].status") || !strings.Contains(text, "pending") {
			t.Errorf("expected items[1].status error mentioning %q, got %q", "pending", text)
		}
		if got := len(store.backlog["test-idea"]); got != 0 {
			t.Errorf("backlog must stay empty on whole-batch abort, got %d items", got)
		}
	}

	// Seed a valid item to exercise the update path.
	var existingID string
	{
		rpcResp := callTool(t, url, "ses-http", sid, "add_backlog_items", map[string]any{
			"items": []any{map[string]any{"title": "seed"}},
		})
		var items []model.BacklogItem
		if err := json.Unmarshal([]byte(extractText(t, rpcResp)), &items); err != nil {
			t.Fatalf("seed parse: %v", err)
		}
		existingID = items[0].ID
	}

	// update_backlog_items with bad status reports per-item error and
	// does not mutate the stored item. Batch semantics (other patches in
	// the same call still apply) match the existing "no fields supplied"
	// per-item-error pattern.
	{
		rpcResp := callTool(t, url, "ses-http", sid, "update_backlog_items", map[string]any{
			"patches": []any{
				map[string]any{"id": existingID, "status": "banana"},
			},
		})
		text := extractText(t, rpcResp)
		var results []map[string]any
		if err := json.Unmarshal([]byte(text), &results); err != nil {
			t.Fatalf("update parse: %v", err)
		}
		if len(results) != 1 || results[0]["status"] != "error" {
			t.Fatalf("expected per-item error, got %+v", results)
		}
		if errMsg, _ := results[0]["error"].(string); !strings.Contains(errMsg, "status") {
			t.Errorf("expected status-related error, got %q", errMsg)
		}
		if got := store.backlog["test-idea"][0].Status; got != model.BacklogStatusOpen {
			t.Errorf("status mutated to %q on rejected update; should remain open", got)
		}
	}

	// list_backlog with bad status filter returns a tool-level error.
	{
		rpcResp := callTool(t, url, "ses-http", sid, "list_backlog", map[string]any{
			"status": []any{"ghost"},
		})
		text := extractText(t, rpcResp)
		if !strings.Contains(text, "status") || !strings.Contains(text, "ghost") {
			t.Errorf("expected status-related error mentioning %q, got %q", "ghost", text)
		}
	}
}

// TestHTTPBacklogBySlugCrossIdea — an agent inside idea-A can add a
// backlog item on idea-B without going through the orchestrator,
// matching the user's "spin off / read / write another idea"
// scenario.
func TestHTTPBacklogBySlugCrossIdea(t *testing.T) {
	t.Parallel()
	ts, store := setupHTTPTest(t)
	// Seed a sibling idea so add_backlog_items_by_slug has a target.
	store.ideas["sibling-idea"] = &model.Idea{Slug: "sibling-idea", Name: "Sibling"}

	url := ts.URL + "/mcp"
	sid := mcpSession(t, url, "ses-http")

	rpcResp := callTool(t, url, "ses-http", sid, "add_backlog_items_by_slug", map[string]any{
		"slug": "sibling-idea",
		"items": []any{
			map[string]any{"title": "handed off from another idea"},
			map[string]any{"title": "second handoff in the same call"},
		},
	})
	text := extractText(t, rpcResp)
	var items []model.BacklogItem
	if err := json.Unmarshal([]byte(text), &items); err != nil {
		t.Fatalf("parsing add by slug: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	// Items must land on the sibling idea, not on the calling session's
	// idea.
	if len(store.backlog["test-idea"]) != 0 {
		t.Errorf("items leaked onto calling idea: %+v", store.backlog["test-idea"])
	}
	if len(store.backlog["sibling-idea"]) != 2 {
		t.Errorf("sibling-idea backlog = %+v", store.backlog["sibling-idea"])
	}

	// list_backlog_by_slug returns the same items.
	listResp := callTool(t, url, "ses-http", sid, "list_backlog_by_slug", map[string]any{
		"slug": "sibling-idea",
	})
	listText := extractText(t, listResp)
	var listed []model.BacklogItem
	if err := json.Unmarshal([]byte(listText), &listed); err != nil {
		t.Fatalf("parsing list by slug: %v", err)
	}
	if len(listed) != 2 {
		t.Errorf("list by slug = %+v", listed)
	}
}

// TestHTTPBacklogDependsOnAndAffects — depends_on and affects round-
// trip through the MCP wire: present on add, returned by list,
// replaced (and explicit-cleared) by update. Lock down the
// present-vs-absent semantics that drive subagent parallelization.
func TestHTTPBacklogDependsOnAndAffects(t *testing.T) {
	t.Parallel()
	ts, store := setupHTTPTest(t)

	url := ts.URL + "/mcp"
	sid := mcpSession(t, url, "ses-http")

	rpcResp := callTool(t, url, "ses-http", sid, "add_backlog_items", map[string]any{
		"items": []any{
			map[string]any{
				"title":      "carve up coordinator",
				"depends_on": []any{"abc-123", "platform-migration:def-456"},
				"affects":    []any{"internal/agent/coordinator.go", "internal/agent/session.go"},
			},
		},
	})
	text := extractText(t, rpcResp)
	var items []model.BacklogItem
	if err := json.Unmarshal([]byte(text), &items); err != nil {
		t.Fatalf("parsing add response: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	item := items[0]
	if len(item.DependsOn) != 2 || item.DependsOn[1] != "platform-migration:def-456" {
		t.Errorf("DependsOn = %+v", item.DependsOn)
	}
	if len(item.Affects) != 2 || item.Affects[0] != "internal/agent/coordinator.go" {
		t.Errorf("Affects = %+v", item.Affects)
	}

	// Update with explicit empty depends_on clears it; affects is
	// omitted entirely so it stays put. This is the distinction that
	// makes the present-vs-absent encoding load-bearing.
	callTool(t, url, "ses-http", sid, "update_backlog_items", map[string]any{
		"patches": []any{
			map[string]any{"id": item.ID, "depends_on": []any{}},
		},
	})
	stored := store.backlog["test-idea"][0]
	if len(stored.DependsOn) != 0 {
		t.Errorf("DependsOn should be cleared, got %+v", stored.DependsOn)
	}
	if len(stored.Affects) != 2 {
		t.Errorf("Affects was clobbered: %+v", stored.Affects)
	}
}

// TestHTTPMatchResourceURLs — bulk URL → ideas lookup over the wire.
// Canonicalization (trailing slash) round-trips; URLs without matches
// come back as empty arrays (not absent), preserving the
// distinguishable-empty contract.
func TestHTTPMatchResourceURLs(t *testing.T) {
	t.Parallel()
	ts, store := setupHTTPTest(t)

	// Seed a second idea that also tracks the same PR — match the
	// "PR appears on multiple ideas" case the user's cross-reference
	// use case cares about.
	store.ideas["beta-idea"] = &model.Idea{
		Slug: "beta-idea",
		Name: "Beta",
		Resources: []model.Resource{
			{Type: "github_pr", URL: "https://github.com/owner/repo/pull/1", Label: "Same PR, beta side"},
		},
	}

	url := ts.URL + "/mcp"
	sid := mcpSession(t, url, "ses-http")

	rpcResp := callTool(t, url, "ses-http", sid, "match_resource_urls", map[string]any{
		"urls": []any{
			"https://github.com/owner/repo/pull/1/", // trailing slash, normalized to match
			"https://example.com/unrelated",
		},
	})
	text := extractText(t, rpcResp)

	var out map[string][]map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("parsing match response: %v", err)
	}

	prMatches, ok := out["https://github.com/owner/repo/pull/1/"]
	if !ok {
		t.Fatalf("missing PR URL key in response: %v", out)
	}
	if len(prMatches) != 2 {
		t.Errorf("PR matches = %d, want 2 (test-idea + beta-idea): %+v", len(prMatches), prMatches)
	}
	slugs := map[string]bool{}
	for _, m := range prMatches {
		if s, ok := m["slug"].(string); ok {
			slugs[s] = true
		}
	}
	if !slugs["test-idea"] || !slugs["beta-idea"] {
		t.Errorf("PR matches missing one of {test-idea, beta-idea}: slugs=%v", slugs)
	}

	// Unrelated URL: empty array (not absent). The skill needs this
	// to distinguish "URL not tracked anywhere" from "URL wasn't
	// passed in" without checking the map's presence bit.
	unrelated, ok := out["https://example.com/unrelated"]
	if !ok {
		t.Fatalf("unrelated URL should still be a key in the response: %v", out)
	}
	if len(unrelated) != 0 {
		t.Errorf("unrelated URL should map to empty array, got %+v", unrelated)
	}
}

// TestHTTPListIdeasInlinesState — list_ideas returns the full
// in-flight envelope per idea: summary, backlog counts (+ titles),
// session lists, last_activity_at. Pins the contract
// summarize-ideas / work-idea will rely on so a future shape edit
// surfaces here instead of as a silent skill regression.
func TestHTTPListIdeasInlinesState(t *testing.T) {
	t.Parallel()
	ts, store := setupHTTPTest(t)

	// Seed structure: open + in_progress + done backlog items so
	// every counter has a non-zero value; a running and a dormant
	// session so both lists populate; a sidecar summary so the
	// summary field round-trips.
	now := time.Now().UTC()
	store.ideas["test-idea"].Updated = now
	store.summaries = map[string]*model.Summary{
		"test-idea": {Line: "Test the inlined state shape."},
	}
	store.backlog = map[string][]model.BacklogItem{
		"test-idea": {
			{ID: "bl-1", Title: "first open task", Status: model.BacklogStatusOpen},
			{ID: "bl-2", Title: "in-flight task", Status: model.BacklogStatusInProgress},
			{ID: "bl-3", Title: "shipped already", Status: model.BacklogStatusDone},
		},
	}
	store.sessions = map[string][]model.AgentSession{
		"test-idea": {
			{UUID: "ses-running", Agent: "claude-code", Status: model.SessionStatusRunning, Activity: model.SessionActivityActive, Started: now},
			{UUID: "ses-dormant", Agent: "claude-code", Status: model.SessionStatusDormant, Started: now.Add(-1 * time.Hour)},
		},
	}

	url := ts.URL + "/mcp"
	sid := mcpSession(t, url, "ses-http")
	rpcResp := callTool(t, url, "ses-http", sid, "list_ideas", nil)
	text := extractText(t, rpcResp)

	var ideas []map[string]any
	if err := json.Unmarshal([]byte(text), &ideas); err != nil {
		t.Fatalf("parsing list_ideas: %v", err)
	}
	if len(ideas) != 1 {
		t.Fatalf("ideas = %d, want 1: %s", len(ideas), text)
	}
	idea := ideas[0]
	if idea["slug"] != "test-idea" {
		t.Errorf("slug = %v", idea["slug"])
	}
	if idea["summary"] != "Test the inlined state shape." {
		t.Errorf("summary = %v", idea["summary"])
	}
	if idea["last_activity_at"] == nil || idea["last_activity_at"] == "" {
		t.Errorf("last_activity_at should populate from idea.Updated, got %v", idea["last_activity_at"])
	}

	backlog := idea["backlog"].(map[string]any)
	if int(backlog["open"].(float64)) != 1 {
		t.Errorf("backlog.open = %v", backlog["open"])
	}
	if int(backlog["in_progress"].(float64)) != 1 {
		t.Errorf("backlog.in_progress = %v", backlog["in_progress"])
	}
	if int(backlog["done"].(float64)) != 1 {
		t.Errorf("backlog.done = %v", backlog["done"])
	}
	titles, _ := backlog["in_progress_titles"].([]any)
	if len(titles) != 1 || titles[0] != "in-flight task" {
		t.Errorf("in_progress_titles = %v", titles)
	}

	sessions := idea["sessions"].(map[string]any)
	running := sessions["running"].([]any)
	dormant := sessions["dormant"].([]any)
	if len(running) != 1 || len(dormant) != 1 {
		t.Errorf("running=%d dormant=%d, want 1/1", len(running), len(dormant))
	}
	if running[0].(map[string]any)["uuid"] != "ses-running" {
		t.Errorf("running uuid mismatch: %+v", running[0])
	}
	if running[0].(map[string]any)["activity"] != string(model.SessionActivityActive) {
		t.Errorf("running activity = %v", running[0].(map[string]any)["activity"])
	}
	if dormant[0].(map[string]any)["uuid"] != "ses-dormant" {
		t.Errorf("dormant uuid mismatch: %+v", dormant[0])
	}
}

// TestHTTPListIdeasExcludesArchivedByDefault — archived ideas are
// dropped from the default response so recap callers don't have to
// filter post-hoc.
func TestHTTPListIdeasExcludesArchivedByDefault(t *testing.T) {
	t.Parallel()
	ts, store := setupHTTPTest(t)
	store.ideas["archived-idea"] = &model.Idea{
		Slug: "archived-idea", Name: "Archived", Status: model.StatusArchived,
	}

	url := ts.URL + "/mcp"
	sid := mcpSession(t, url, "ses-http")

	// Default — archived is dropped.
	{
		rpcResp := callTool(t, url, "ses-http", sid, "list_ideas", nil)
		var ideas []map[string]any
		_ = json.Unmarshal([]byte(extractText(t, rpcResp)), &ideas)
		for _, idea := range ideas {
			if idea["slug"] == "archived-idea" {
				t.Errorf("archived idea should be dropped, got: %+v", ideas)
			}
		}
	}

	// Opt back in.
	{
		rpcResp := callTool(t, url, "ses-http", sid, "list_ideas", map[string]any{
			"exclude_archived": false,
		})
		var ideas []map[string]any
		_ = json.Unmarshal([]byte(extractText(t, rpcResp)), &ideas)
		seen := false
		for _, idea := range ideas {
			if idea["slug"] == "archived-idea" {
				seen = true
			}
		}
		if !seen {
			t.Errorf("exclude_archived=false should include archived, ideas=%+v", ideas)
		}
	}
}
