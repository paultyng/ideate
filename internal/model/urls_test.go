package model

import "testing"

func TestURLBuilders(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"orchestrator", OrchestratorURL(), "ideate://orchestrator"},
		{"idea", IdeaURL("my-idea"), "ideate://ideas/my-idea"},
		{"idea active session", IdeaActiveSessionURL("my-idea"), "ideate://ideas/my-idea/active-session"},
		{"session permalink", SessionURL("my-idea", "abc-123"), "ideate://ideas/my-idea/sessions/abc-123"},
		{"idea empty slug", IdeaURL(""), ""},
		{"active-session empty slug", IdeaActiveSessionURL(""), ""},
		{"session empty slug", SessionURL("", "abc"), ""},
		{"session empty uuid", SessionURL("my-idea", ""), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}
