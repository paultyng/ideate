package mcp

import (
	"testing"

	"github.com/paultyng/ideate/internal/model"
)

func TestClassifySessionState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		activity string
		want     string
	}{
		{string(model.SessionActivityActive), "active"},
		{string(model.SessionActivityReviewing), "active"},
		{string(model.SessionActivityWaiting), "awaiting"},
		{string(model.SessionActivityIdle), "idle"},
		{"", "idle"},
		{"unknown", "idle"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.activity, func(t *testing.T) {
			t.Parallel()
			if got := classifySessionState(c.activity); got != c.want {
				t.Errorf("classifySessionState(%q) = %q, want %q", c.activity, got, c.want)
			}
		})
	}
}

func TestIdleBucket(t *testing.T) {
	t.Parallel()
	cases := []struct {
		secs int64
		want string
	}{
		{0, "<1m"},
		{59, "<1m"},
		{60, "1m"},
		{600, "10m"},
		{3599, "59m"},
		{3600, "1h"},
		{86399, "23h"},
		{86400, "1d"},
		{86400 * 7, "7d"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.want, func(t *testing.T) {
			t.Parallel()
			if got := idleBucket(c.secs); got != c.want {
				t.Errorf("idleBucket(%d) = %q, want %q", c.secs, got, c.want)
			}
		})
	}
}

func TestParseSessionFilter_Roundtrip(t *testing.T) {
	t.Parallel()
	f, err := parseSessionFilter("s:active s:awaiting a:claude-code #142")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(f.states) != 2 || f.states[0] != "active" || f.states[1] != "awaiting" {
		t.Errorf("states = %v", f.states)
	}
	if len(f.agents) != 1 || f.agents[0] != "claude-code" {
		t.Errorf("agents = %v", f.agents)
	}
	if f.prNumber != 142 {
		t.Errorf("prNumber = %d, want 142", f.prNumber)
	}
}

func TestParseSessionFilter_Errors(t *testing.T) {
	t.Parallel()
	for _, tok := range []string{"x:foo", "s:", "a:", "#abc", "#0", "#1 #2"} {
		tok := tok
		t.Run(tok, func(t *testing.T) {
			t.Parallel()
			if _, err := parseSessionFilter(tok); err == nil {
				t.Errorf("expected error for token %q", tok)
			}
		})
	}
}

func TestSessionFilter_Matches(t *testing.T) {
	t.Parallel()
	view := sessionView{
		UUID:      "uuid-1",
		IdeaSlug:  "idea-a",
		AgentType: "claude-code",
		Activity:  "active",
		State:     "active",
	}
	idea := model.Idea{
		Slug: "idea-a",
		Resources: []model.Resource{
			{Type: "github_pr", URL: "https://github.com/o/r/pull/142"},
			{Type: "notion", URL: "https://notion.so/x"},
		},
	}

	f, _ := parseSessionFilter("s:active a:claude-code #142")
	if !f.matches(view, idea) {
		t.Errorf("expected match for active claude-code on PR 142")
	}

	f2, _ := parseSessionFilter("s:awaiting")
	if f2.matches(view, idea) {
		t.Errorf("expected no match — state is active not awaiting")
	}

	f3, _ := parseSessionFilter("#999")
	if f3.matches(view, idea) {
		t.Errorf("expected no match — PR 999 not linked")
	}

	f4, _ := parseSessionFilter("s:active s:awaiting")
	if !f4.matches(view, idea) {
		t.Errorf("expected match — s: tokens OR within key")
	}

	// Empty filter matches everything.
	f5, _ := parseSessionFilter("")
	if !f5.matches(view, idea) {
		t.Errorf("empty filter should always match")
	}
}

func TestGithubPRNumberFromURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		url  string
		want int64
		ok   bool
	}{
		{"https://github.com/o/r/pull/142", 142, true},
		{"https://github.com/o/r/pull/142?diff=split", 142, true},
		{"https://github.com/o/r/pull/142#issuecomment-1", 142, true},
		{"https://github.com/o/r/issues/142", 0, false},
		{"https://example.com", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.url, func(t *testing.T) {
			t.Parallel()
			got, ok := githubPRNumberFromURL(model.Resource{Type: "github_pr", URL: c.url})
			if ok != c.ok || got != c.want {
				t.Errorf("githubPRNumberFromURL(%q) = (%d, %v), want (%d, %v)", c.url, got, ok, c.want, c.ok)
			}
		})
	}
	// Non-PR resource type returns (0, false) even with a /pull/ URL.
	if got, ok := githubPRNumberFromURL(model.Resource{Type: "notion", URL: "https://github.com/o/r/pull/1"}); ok || got != 0 {
		t.Errorf("non-pr type should not match")
	}
}

func TestStripPromptPlaceholder(t *testing.T) {
	t.Parallel()
	in := "Some output\n\n❯ Try \"fix the bug\"\n\nMore output\n  ❯ Try \"do a thing\"  \nTail line\n"
	got := stripPromptPlaceholder(in)
	if got == in {
		t.Errorf("placeholder lines were not removed: %q", got)
	}
	for _, bad := range []string{"❯ Try", "Try \""} {
		if containsAny(got, bad) {
			t.Errorf("output still contains placeholder fragment %q: %q", bad, got)
		}
	}
	for _, want := range []string{"Some output", "More output", "Tail line"} {
		if !containsAny(got, want) {
			t.Errorf("output dropped real content %q: %q", want, got)
		}
	}
}

func containsAny(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
