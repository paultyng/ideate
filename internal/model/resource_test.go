package model

import (
	"testing"
)

func TestNormalizeResourceKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   Resource
		want string
	}{
		{
			name: "empty URL falls back to type:label",
			in:   Resource{Type: "notion", Label: "Design doc"},
			want: "notion:Design doc",
		},
		{
			name: "simple HTTPS URL passes through",
			in:   Resource{URL: "https://github.com/o/r"},
			want: "https://github.com/o/r",
		},
		{
			name: "host lowercased",
			in:   Resource{URL: "HTTPS://GITHUB.COM/foo"},
			want: "https://github.com/foo",
		},
		{
			name: "fragment stripped",
			in:   Resource{URL: "https://x/y#frag"},
			want: "https://x/y",
		},
		{
			name: "trailing slash stripped",
			in:   Resource{URL: "https://x/y/"},
			want: "https://x/y",
		},
		{
			name: ".git suffix stripped",
			in:   Resource{URL: "https://github.com/o/r.git"},
			want: "https://github.com/o/r",
		},
		{
			name: "SSH form normalizes to HTTPS",
			in:   Resource{URL: "git@github.com:o/r.git"},
			want: "https://github.com/o/r",
		},
		{
			name: "SSH and HTTPS same repo produce same key",
			in:   Resource{URL: "git@github.com:o/r.git"},
			want: NormalizeResourceKey(Resource{URL: "https://github.com/o/r.git"}),
		},
		{
			name: "querystring preserved",
			in:   Resource{URL: "https://notion.so/p?v=123"},
			want: "https://notion.so/p?v=123",
		},
		{
			name: "malformed URL falls back to type:label",
			in:   Resource{URL: "://bad", Type: "web", Label: "bad url"},
			want: "web:bad url",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := NormalizeResourceKey(tc.in)
			if got != tc.want {
				t.Errorf("NormalizeResourceKey(%+v) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestUpsertResource(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		initial   []Resource
		incoming  Resource
		wantLen   int
		wantCheck func(t *testing.T, got []Resource)
	}{
		{
			name:     "append on no match",
			initial:  []Resource{{Type: "notion", URL: "https://notion.so/a"}},
			incoming: Resource{Type: "github_pr", URL: "https://github.com/o/r/pull/1"},
			wantLen:  2,
			wantCheck: func(t *testing.T, got []Resource) {
				t.Helper()
				if got[1].URL != "https://github.com/o/r/pull/1" {
					t.Errorf("expected appended resource URL, got %q", got[1].URL)
				}
			},
		},
		{
			name:     "dedupe by exact URL match",
			initial:  []Resource{{Type: "web", URL: "https://github.com/o/r", Label: "old"}},
			incoming: Resource{Type: "web", URL: "https://github.com/o/r", Label: "new"},
			wantLen:  1,
			wantCheck: func(t *testing.T, got []Resource) {
				t.Helper()
				if got[0].Label != "new" {
					t.Errorf("expected label updated to %q, got %q", "new", got[0].Label)
				}
			},
		},
		{
			name:     "dedupe by canonical URL match (SSH vs HTTPS)",
			initial:  []Resource{{Type: "web", URL: "https://github.com/o/r", Label: "existing"}},
			incoming: Resource{Type: "repo", URL: "git@github.com:o/r.git", Label: "ssh label"},
			wantLen:  1,
			wantCheck: func(t *testing.T, got []Resource) {
				t.Helper()
				if got[0].Label != "ssh label" {
					t.Errorf("expected label %q, got %q", "ssh label", got[0].Label)
				}
			},
		},
		{
			name:     "dedupe by fragment-stripping",
			initial:  []Resource{{Type: "web", URL: "https://notion.so/p?v=123", Label: "orig"}},
			incoming: Resource{Type: "notion", URL: "https://notion.so/p?v=123#section", Label: "updated"},
			wantLen:  1,
			wantCheck: func(t *testing.T, got []Resource) {
				t.Helper()
				if got[0].Label != "updated" {
					t.Errorf("expected label %q, got %q", "updated", got[0].Label)
				}
			},
		},
		{
			name:     "type promotion: web upgraded to github_pr",
			initial:  []Resource{{Type: "web", URL: "https://github.com/o/r/pull/1"}},
			incoming: Resource{Type: "github_pr", URL: "https://github.com/o/r/pull/1"},
			wantLen:  1,
			wantCheck: func(t *testing.T, got []Resource) {
				t.Helper()
				if got[0].Type != "github_pr" {
					t.Errorf("expected type %q, got %q", "github_pr", got[0].Type)
				}
			},
		},
		{
			name:     "type non-promotion: github_pr stays when incoming is web",
			initial:  []Resource{{Type: "github_pr", URL: "https://github.com/o/r/pull/1"}},
			incoming: Resource{Type: "web", URL: "https://github.com/o/r/pull/1"},
			wantLen:  1,
			wantCheck: func(t *testing.T, got []Resource) {
				t.Helper()
				if got[0].Type != "github_pr" {
					t.Errorf("expected type to remain %q, got %q", "github_pr", got[0].Type)
				}
			},
		},
		{
			// A web source must NOT overwrite a richer-typed resource's
			// Label. Mirrors the Type-promotion guard so a WebFetch on a
			// link_repo'd URL can't replace the user's chosen worktree
			// name with the fetched page title.
			name: "label NOT overwritten when web source hits a richer-typed resource",
			initial: []Resource{
				{Type: "repo", URL: "git@github.com:foo/bar.git", Label: "bar"},
			},
			incoming: Resource{Type: "web", URL: "https://github.com/foo/bar", Label: "GitHub - foo/bar: A great repo"},
			wantLen:  1,
			wantCheck: func(t *testing.T, got []Resource) {
				t.Helper()
				if got[0].Type != "repo" {
					t.Errorf("expected type to remain %q, got %q", "repo", got[0].Type)
				}
				if got[0].Label != "bar" {
					t.Errorf("expected label to remain %q, got %q (web source should not clobber richer label)", "bar", got[0].Label)
				}
			},
		},
		{
			// Type-equal channel CAN refine label. A second link_repo on
			// the same URL with an explicit nameOverride still updates.
			name: "label DOES update when types match (same channel re-add)",
			initial: []Resource{
				{Type: "repo", URL: "git@github.com:foo/bar.git", Label: "bar"},
			},
			incoming: Resource{Type: "repo", URL: "https://github.com/foo/bar", Label: "renamed"},
			wantLen:  1,
			wantCheck: func(t *testing.T, got []Resource) {
				t.Helper()
				if got[0].Label != "renamed" {
					t.Errorf("expected label %q, got %q (same-type re-add should refine label)", "renamed", got[0].Label)
				}
			},
		},
		{
			// Non-web source CAN update Label on any-typed resource.
			// Type-promotion still applies (web → repo) AND label updates.
			name: "label DOES update when incoming is non-web (promotion + label refine)",
			initial: []Resource{
				{Type: "web", URL: "https://github.com/foo/bar", Label: "GitHub page"},
			},
			incoming: Resource{Type: "repo", URL: "git@github.com:foo/bar.git", Label: "bar"},
			wantLen:  1,
			wantCheck: func(t *testing.T, got []Resource) {
				t.Helper()
				if got[0].Type != "repo" {
					t.Errorf("expected type promoted to %q, got %q", "repo", got[0].Type)
				}
				if got[0].Label != "bar" {
					t.Errorf("expected label refined to %q, got %q", "bar", got[0].Label)
				}
			},
		},
		{
			name:     "label update on match",
			initial:  []Resource{{Type: "notion", URL: "https://notion.so/p", Label: "old label"}},
			incoming: Resource{Type: "notion", URL: "https://notion.so/p", Label: "new label"},
			wantLen:  1,
			wantCheck: func(t *testing.T, got []Resource) {
				t.Helper()
				if got[0].Label != "new label" {
					t.Errorf("expected label %q, got %q", "new label", got[0].Label)
				}
			},
		},
		{
			name:     "status update on match",
			initial:  []Resource{{Type: "github_pr", URL: "https://github.com/o/r/pull/2", Status: "open"}},
			incoming: Resource{Type: "github_pr", URL: "https://github.com/o/r/pull/2", Status: "merged"},
			wantLen:  1,
			wantCheck: func(t *testing.T, got []Resource) {
				t.Helper()
				if got[0].Status != "merged" {
					t.Errorf("expected status %q, got %q", "merged", got[0].Status)
				}
			},
		},
		{
			name:     "label NOT overwritten by empty new label",
			initial:  []Resource{{Type: "notion", URL: "https://notion.so/p", Label: "keep me"}},
			incoming: Resource{Type: "notion", URL: "https://notion.so/p", Label: ""},
			wantLen:  1,
			wantCheck: func(t *testing.T, got []Resource) {
				t.Helper()
				if got[0].Label != "keep me" {
					t.Errorf("expected label preserved as %q, got %q", "keep me", got[0].Label)
				}
			},
		},
		{
			name:     "status NOT overwritten by empty new status",
			initial:  []Resource{{Type: "github_pr", URL: "https://github.com/o/r/pull/3", Status: "approved"}},
			incoming: Resource{Type: "github_pr", URL: "https://github.com/o/r/pull/3", Status: ""},
			wantLen:  1,
			wantCheck: func(t *testing.T, got []Resource) {
				t.Helper()
				if got[0].Status != "approved" {
					t.Errorf("expected status preserved as %q, got %q", "approved", got[0].Status)
				}
			},
		},
		{
			name:     "label-only resource dedupes by type:label",
			initial:  []Resource{{Type: "notion", Label: "Design doc"}},
			incoming: Resource{Type: "notion", Label: "Design doc", Status: "reviewed"},
			wantLen:  1,
			wantCheck: func(t *testing.T, got []Resource) {
				t.Helper()
				if got[0].Status != "reviewed" {
					t.Errorf("expected status %q, got %q", "reviewed", got[0].Status)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			idea := &Idea{Resources: append([]Resource(nil), tc.initial...)}
			UpsertResource(idea, tc.incoming)
			if len(idea.Resources) != tc.wantLen {
				t.Errorf("len(Resources) = %d; want %d", len(idea.Resources), tc.wantLen)
			}
			if tc.wantCheck != nil {
				tc.wantCheck(t, idea.Resources)
			}
		})
	}
}

// TestUpsertResource_IsIdempotent pins the contract: calling UpsertResource
// with the same arg twice leaves idea.Resources unchanged after the second
// call. Already implied by the dedupe cases in TestUpsertResource but worth
// asserting directly so future maintainers see the property called out.
func TestUpsertResource_IsIdempotent(t *testing.T) {
	t.Parallel()

	idea := &Idea{}
	res := Resource{
		Type:   "github_pr",
		URL:    "https://github.com/o/r/pull/42",
		Label:  "Initial PR",
		Status: "open",
	}

	UpsertResource(idea, res)
	if len(idea.Resources) != 1 {
		t.Fatalf("after first call: len = %d, want 1", len(idea.Resources))
	}
	first := idea.Resources[0]

	UpsertResource(idea, res)
	if len(idea.Resources) != 1 {
		t.Fatalf("after second call: len = %d, want 1 (idempotence violated)", len(idea.Resources))
	}
	got := idea.Resources[0]
	if got != first {
		t.Errorf("second call mutated the resource: before=%+v after=%+v", first, got)
	}
}
