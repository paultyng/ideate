//go:build bindings

package config

import (
	"strings"
	"testing"
)

func TestBindingsBuildAvoidsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	for _, env := range []string{"IDEATE_IDEAS_DIR", "IDEATE_CONFIG_DIR", "IDEATE_CLAUDE_PROJECTS_DIR"} {
		t.Setenv(env, "")
	}
	for name, got := range map[string]string{
		"ideas":           DefaultIdeasDir(),
		"config":          DefaultConfigDir(),
		"claude-projects": DefaultClaudeProjectsDir(),
	} {
		if got == "" || strings.HasPrefix(got, home) {
			t.Errorf("%s dir %q is empty or under HOME %q", name, got, home)
		}
	}
}
