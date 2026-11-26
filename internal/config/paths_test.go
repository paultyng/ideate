package config

import "testing"

func TestDefaultDirsRespectEnv(t *testing.T) {
	cases := []struct {
		env string
		fn  func() string
	}{
		{"IDEATE_IDEAS_DIR", DefaultIdeasDir},
		{"IDEATE_CONFIG_DIR", DefaultConfigDir},
		{"IDEATE_CLAUDE_PROJECTS_DIR", DefaultClaudeProjectsDir},
	}
	for _, c := range cases {
		t.Run(c.env, func(t *testing.T) {
			want := t.TempDir()
			t.Setenv(c.env, want)
			if got := c.fn(); got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}
