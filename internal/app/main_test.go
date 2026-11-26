package app

import (
	"fmt"
	"os"
	"testing"
)

// Tests that don't set their own dirs must never fall back to real user data.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "ideate-app-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, "creating temp dir:", err)
		os.Exit(1)
	}
	for _, env := range []string{"IDEATE_IDEAS_DIR", "IDEATE_CONFIG_DIR", "IDEATE_CLAUDE_PROJECTS_DIR"} {
		dir, err := os.MkdirTemp(root, "d")
		if err == nil {
			err = os.Setenv(env, dir)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "setting", env+":", err)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
