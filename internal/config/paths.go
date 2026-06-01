package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// DefaultIdeasDir returns the directory for storing ideas.
// Respects IDEATE_IDEAS_DIR env var for dev/test isolation.
func DefaultIdeasDir() string {
	if dir := os.Getenv("IDEATE_IDEAS_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Documents", "Ideate")
}

// DefaultConfigDir returns the per-user directory for app config and persistent
// state (reviews, window state, pending operations, session manifests).
// Respects IDEATE_CONFIG_DIR env var for dev/test isolation.
func DefaultConfigDir() string {
	if dir := os.Getenv("IDEATE_CONFIG_DIR"); dir != "" {
		return dir
	}
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "ideate")
	case "linux":
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "ideate")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, ".config", "ideate")
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, ".ideate")
	}
}

// ReviewsDir returns the directory for review records, derived from configDir.
// All review kinds (diff, markdown) live here keyed by review ID.
func ReviewsDir(configDir string) string {
	return filepath.Join(configDir, "reviews")
}

// DefaultClaudeProjectsDir returns the directory where Claude Code stores
// per-session JSONL transcripts. Respects IDEATE_CLAUDE_PROJECTS_DIR for
// dev/test isolation.
func DefaultClaudeProjectsDir() string {
	if dir := os.Getenv("IDEATE_CLAUDE_PROJECTS_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}
