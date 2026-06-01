package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/paultyng/ideate/internal/model"
)

// updateIdeaTestCase drives one call to handleUpdateIdeaBySlug
// against a seeded idea and asserts the post-update field state.
// Args is the raw map passed to the handler — keys present here
// reach the partial-update logic; keys absent are "no action".
type updateIdeaTestCase struct {
	name          string
	seedName      string
	seedStatus    model.Status
	seedSummary   string
	args          map[string]any
	wantName      string
	wantStatus    model.Status
	wantSummary   string
	wantNoChanges bool
}

func TestHandleUpdateIdeaBySlug_NullVsEmpty(t *testing.T) {
	t.Parallel()

	cases := []updateIdeaTestCase{
		{
			name:          "absent fields are no-ops",
			seedName:      "Original",
			seedStatus:    model.StatusActive,
			seedSummary:   "seed body",
			args:          map[string]any{}, // only slug supplied
			wantName:      "Original",
			wantStatus:    model.StatusActive,
			wantSummary:   "seed body",
			wantNoChanges: true,
		},
		{
			name:          "null summary leaves the field unchanged",
			seedName:      "Original",
			seedStatus:    model.StatusActive,
			seedSummary:   "seed body",
			args:          map[string]any{"summary": nil},
			wantName:      "Original",
			wantStatus:    model.StatusActive,
			wantSummary:   "seed body",
			wantNoChanges: true,
		},
		{
			name:        "empty summary explicitly clears it",
			seedName:    "Original",
			seedStatus:  model.StatusActive,
			seedSummary: "seed body",
			args:        map[string]any{"summary": ""},
			wantName:    "Original",
			wantStatus:  model.StatusActive,
			wantSummary: "",
		},
		{
			name:        "non-empty summary replaces the body",
			seedName:    "Original",
			seedStatus:  model.StatusActive,
			seedSummary: "seed body",
			args:        map[string]any{"summary": "new body"},
			wantName:    "Original",
			wantStatus:  model.StatusActive,
			wantSummary: "new body",
		},
		{
			name:        "name-only update (status field is no longer accepted)",
			seedName:    "Original",
			seedStatus:  model.StatusPaused,
			seedSummary: "keep me",
			args:        map[string]any{"name": "Renamed"},
			wantName:    "Renamed",
			wantStatus:  model.StatusPaused, // unchanged — use archive/pause/resume tools
			wantSummary: "keep me",
		},
	}

	// Separate test for the rejection path — handlers return IsError=true,
	// so it doesn't fit the happy-path harness above.
	t.Run("status field is rejected with pointer to lifecycle tools", func(t *testing.T) {
		t.Parallel()
		s := newFakeStore()
		s.ideas["alpha"] = &model.Idea{Slug: "alpha", Name: "X", Status: model.StatusActive, Summary: ""}
		m := NewManager(s, &fakeResolver{mapping: map[string]string{}}, nil)
		req := mcp.CallToolRequest{}
		req.Params.Arguments = map[string]any{"slug": "alpha", "status": "archived"}
		res, err := m.handleUpdateIdeaBySlug("ses-1")(context.Background(), req)
		if err != nil {
			t.Fatalf("handler returned error: %v", err)
		}
		if !res.IsError {
			t.Fatalf("expected IsError=true, got success: %v", res.Content)
		}
		body := toolResultText(res)
		for _, want := range []string{"archive_idea", "pause_idea", "resume_idea", "unarchive_idea"} {
			if !strings.Contains(body, want) {
				t.Errorf("rejection message missing %q in: %q", want, body)
			}
		}
		// Idea must not have been mutated.
		if got := s.ideas["alpha"]; got.Status != model.StatusActive {
			t.Errorf("status mutated to %q after rejection", got.Status)
		}
	})

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newFakeStore()
			s.ideas["alpha"] = &model.Idea{
				Slug:    "alpha",
				Name:    tt.seedName,
				Status:  tt.seedStatus,
				Summary: tt.seedSummary,
			}
			m := NewManager(s, &fakeResolver{mapping: map[string]string{}}, nil)

			req := mcp.CallToolRequest{}
			args := map[string]any{"slug": "alpha"}
			for k, v := range tt.args {
				args[k] = v
			}
			req.Params.Arguments = args

			res, err := m.handleUpdateIdeaBySlug("ses-1")(context.Background(), req)
			if err != nil || res.IsError {
				t.Fatalf("handler: %v isErr=%v content=%v", err, res.IsError, res.Content)
			}

			// "No changes specified" body is the contract for the
			// absent-fields case; assert it shows up there and only
			// there so we don't accidentally elide a real edit.
			body := toolResultText(res)
			if tt.wantNoChanges && !strings.Contains(body, "No changes specified") {
				t.Errorf("expected no-changes message, got %q", body)
			}
			if !tt.wantNoChanges && strings.Contains(body, "No changes specified") {
				t.Errorf("expected an update to land, got no-changes: %q", body)
			}

			got := s.ideas["alpha"]
			if got.Name != tt.wantName {
				t.Errorf("name = %q, want %q", got.Name, tt.wantName)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Summary != tt.wantSummary {
				t.Errorf("summary = %q, want %q", got.Summary, tt.wantSummary)
			}
		})
	}
}

// toolResultText pulls the plain-text body out of a CallToolResult
// for assertions. Handlers in this package always return a single
// TextContent on success; on error res.IsError is true and we want
// the caller to fail fast, not poke through the structure.
func toolResultText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if t, ok := c.(mcp.TextContent); ok {
			return t.Text
		}
	}
	return ""
}

// Task 13 P1 follow-up — create_idea rejects unknown status values rather
// than letting them persist verbatim and silently read-repair on next read.
func TestHandleCreateIdea_RejectsInvalidStatus(t *testing.T) {
	t.Parallel()
	s := newFakeStore()
	m := NewManager(s, &fakeResolver{mapping: map[string]string{}}, nil)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "Will it persist?", "status": "thinking"}

	res, err := m.handleCreateIdea("ses-1")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true, got success: %v", res.Content)
	}
	body := toolResultText(res)
	if !strings.Contains(body, "invalid status") {
		t.Errorf("rejection message missing 'invalid status': %q", body)
	}
	for _, want := range []string{"active", "paused", "archived"} {
		if !strings.Contains(body, want) {
			t.Errorf("rejection message missing allowed value %q: %q", want, body)
		}
	}
	// Verify no idea was persisted.
	if len(s.ideas) != 0 {
		t.Errorf("idea was created despite invalid status: %+v", s.ideas)
	}
}

func TestHandleCreateIdea_AcceptsValidStatuses(t *testing.T) {
	t.Parallel()
	for _, st := range []string{"active", "paused", "archived"} {
		st := st
		t.Run(st, func(t *testing.T) {
			t.Parallel()
			s := newFakeStore()
			m := NewManager(s, &fakeResolver{mapping: map[string]string{}}, nil)
			req := mcp.CallToolRequest{}
			req.Params.Arguments = map[string]any{"name": "X-" + st, "status": st}

			res, err := m.handleCreateIdea("ses-1")(context.Background(), req)
			if err != nil {
				t.Fatalf("handler returned error: %v", err)
			}
			if res.IsError {
				t.Fatalf("expected success, got error: %v", res.Content)
			}
		})
	}
}

func TestHandleCreateIdea_DefaultsToPaused(t *testing.T) {
	t.Parallel()
	s := newFakeStore()
	m := NewManager(s, &fakeResolver{mapping: map[string]string{}}, nil)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "No Status Specified"}

	res, err := m.handleCreateIdea("ses-1")(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %v", res.Content)
	}
	if len(s.ideas) != 1 {
		t.Fatalf("expected 1 idea, got %d", len(s.ideas))
	}
	for _, idea := range s.ideas {
		if idea.Status != model.StatusPaused {
			t.Errorf("default status = %q, want %q", idea.Status, model.StatusPaused)
		}
	}
}
