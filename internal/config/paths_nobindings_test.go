//go:build !bindings

package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestDefaultDirsPanicWhenEnvUnset(t *testing.T) {
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
			t.Setenv(c.env, "")
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic")
				}
				if msg := fmt.Sprint(r); !strings.Contains(msg, c.env) {
					t.Fatalf("panic %q does not name %s", msg, c.env)
				}
			}()
			c.fn()
		})
	}
}
