package review

import "context"

// DiffSource produces diff data from some source (local git, GitHub API, etc.)
type DiffSource interface {
	GetDiff(ctx context.Context) (*DiffResult, error)
}

// DiffResult is the top-level diff result returned to the frontend.
type DiffResult struct {
	Files []FileDiff `json:"files"`
	Base  string     `json:"base"`
	Head  string     `json:"head"`
}

// FileDiff represents a single changed file.
type FileDiff struct {
	OldName    string `json:"oldName"`
	NewName    string `json:"newName"`
	Status     string `json:"status"`     // added, modified, deleted, renamed
	Hunks      string `json:"hunks"`      // raw unified diff hunks for this file
	OldContent string `json:"oldContent"` // full file content at base ref
	NewContent string `json:"newContent"` // full file content at head ref
	Language   string `json:"language"`   // detected language for syntax highlighting
	// Truncated is true when one or both file contents exceeded MaxFileBytes
	// and were dropped to keep the renderer payload bounded.
	Truncated bool `json:"truncated,omitempty"`
}
