package mcp

import (
	"testing"

	"github.com/paultyng/ideate/internal/model"
)

func TestFilterAndProjectBacklog(t *testing.T) {
	t.Parallel()

	items := []model.BacklogItem{
		{ID: "a", Title: "open one", Status: model.BacklogStatusOpen, Body: "body-a"},
		{ID: "b", Title: "in progress", Status: model.BacklogStatusInProgress, Body: "body-b"},
		{ID: "c", Title: "done", Status: model.BacklogStatusDone, Body: "body-c"},
		{ID: "d", Title: "wontfix", Status: model.BacklogStatusWontFix, Body: "body-d"},
	}

	cases := []struct {
		name        string
		statuses    []model.BacklogStatus
		includeBody bool
		wantIDs     []string
		wantBodies  bool
	}{
		{
			name:    "default drops body, returns all items",
			wantIDs: []string{"a", "b", "c", "d"},
		},
		{
			name:        "include_body=true round-trips bodies",
			includeBody: true,
			wantIDs:     []string{"a", "b", "c", "d"},
			wantBodies:  true,
		},
		{
			name:     "single status filter narrows to one item",
			statuses: []model.BacklogStatus{model.BacklogStatusOpen},
			wantIDs:  []string{"a"},
		},
		{
			name:     "multi status filter ORs across set",
			statuses: []model.BacklogStatus{model.BacklogStatusOpen, model.BacklogStatusInProgress},
			wantIDs:  []string{"a", "b"},
		},
		{
			name:        "filter composes with include_body",
			statuses:    []model.BacklogStatus{model.BacklogStatusDone},
			includeBody: true,
			wantIDs:     []string{"c"},
			wantBodies:  true,
		},
		{
			name:     "filter matching nothing returns empty",
			statuses: []model.BacklogStatus{model.BacklogStatus("ghost")},
			wantIDs:  nil,
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := filterAndProjectBacklog(items, c.statuses, c.includeBody)
			if len(got) != len(c.wantIDs) {
				t.Fatalf("len = %d, want %d (got %+v)", len(got), len(c.wantIDs), got)
			}
			for i, id := range c.wantIDs {
				if got[i].ID != id {
					t.Errorf("[%d] id = %q, want %q", i, got[i].ID, id)
				}
				if c.wantBodies && got[i].Body == "" {
					t.Errorf("[%d] body dropped but include_body=true (id=%s)", i, got[i].ID)
				}
				if !c.wantBodies && got[i].Body != "" {
					t.Errorf("[%d] body present but include_body=false (id=%s, body=%q)", i, got[i].ID, got[i].Body)
				}
			}
		})
	}
}

func TestValidateBacklogStatus(t *testing.T) {
	t.Parallel()

	for _, s := range []model.BacklogStatus{
		model.BacklogStatusOpen,
		model.BacklogStatusInProgress,
		model.BacklogStatusDone,
		model.BacklogStatusWontFix,
	} {
		s := s
		t.Run("valid_"+string(s), func(t *testing.T) {
			t.Parallel()
			got, err := validateBacklogStatus(string(s))
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != s {
				t.Errorf("got %q, want %q", got, s)
			}
		})
	}

	for _, bad := range []string{"", "pending", "OPEN", "open ", "in-progress"} {
		bad := bad
		t.Run("invalid_"+bad, func(t *testing.T) {
			t.Parallel()
			_, err := validateBacklogStatus(bad)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", bad)
			}
		})
	}
}

func TestFilterAndProjectBacklog_DoesNotMutateInput(t *testing.T) {
	t.Parallel()
	items := []model.BacklogItem{
		{ID: "a", Status: model.BacklogStatusOpen, Body: "keep-me"},
	}
	_ = filterAndProjectBacklog(items, nil, false)
	if items[0].Body != "keep-me" {
		t.Errorf("input body mutated; got %q, want %q", items[0].Body, "keep-me")
	}
}

func TestParseListBacklogArgs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		args        map[string]any
		wantStatus  []model.BacklogStatus
		wantInclude bool
		wantErr     string
	}{
		{
			name: "no args",
		},
		{
			name:       "single status",
			args:       map[string]any{"status": []any{"open"}},
			wantStatus: []model.BacklogStatus{model.BacklogStatusOpen},
		},
		{
			name:       "multi status",
			args:       map[string]any{"status": []any{"open", "in_progress"}},
			wantStatus: []model.BacklogStatus{model.BacklogStatusOpen, model.BacklogStatusInProgress},
		},
		{
			name:        "include_body=true",
			args:        map[string]any{"include_body": true},
			wantInclude: true,
		},
		{
			name:        "filter + include_body",
			args:        map[string]any{"status": []any{"done"}, "include_body": true},
			wantStatus:  []model.BacklogStatus{model.BacklogStatusDone},
			wantInclude: true,
		},
		{
			name:    "status not an array",
			args:    map[string]any{"status": "open"},
			wantErr: "status must be an array of strings",
		},
		{
			name:    "status element not a string",
			args:    map[string]any{"status": []any{1}},
			wantErr: "status[0] must be a string",
		},
		{
			name:    "status element not in enum",
			args:    map[string]any{"status": []any{"banana"}},
			wantErr: "status[0]: \"banana\" is not one of open|in_progress|done|wontfix",
		},
		{
			name:    "include_body not a boolean",
			args:    map[string]any{"include_body": "yes"},
			wantErr: "include_body must be a boolean",
		},
		{
			name: "status=null is treated as omitted",
			args: map[string]any{"status": nil},
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			gotStatus, gotInclude, err := parseListBacklogArgs(c.args)
			if c.wantErr != "" {
				if err == nil || err.Error() != c.wantErr {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if gotInclude != c.wantInclude {
				t.Errorf("includeBody = %v, want %v", gotInclude, c.wantInclude)
			}
			if len(gotStatus) != len(c.wantStatus) {
				t.Fatalf("status len = %d, want %d (got %v)", len(gotStatus), len(c.wantStatus), gotStatus)
			}
			for i, s := range c.wantStatus {
				if gotStatus[i] != s {
					t.Errorf("status[%d] = %q, want %q", i, gotStatus[i], s)
				}
			}
		})
	}
}
