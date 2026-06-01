package model

import "time"

// Summary is the persisted idea-level summary sidecar. One per idea
// at `<ideas-dir>/<slug>/summary.json`. Authored by the headless
// summarizer pipeline (see internal/agent/summarizer), not by the
// user — divergence from `idea.md`'s body is intentional, so this
// stays out of frontmatter.
type Summary struct {
	// Line is the one-sentence intent snapshot. Caps at ~120 chars
	// in practice; the writer is responsible for truncation.
	Line string `json:"line"`

	// GeneratedAt is when the summarizer wrote this record. Compared
	// against the parent idea's Updated to detect staleness for the
	// (future) periodic refresh sweep.
	GeneratedAt time.Time `json:"generated_at"`

	// SourceSessionUUID identifies which session's transcript was
	// the dominant input. Empty when the summary was generated from
	// the idea body alone (no sessions yet).
	SourceSessionUUID string `json:"source_session_uuid,omitempty"`

	// SourceSessionEndedAt is the source session's Ended timestamp.
	// Lets the sweep skip regeneration when the latest session
	// hasn't changed.
	SourceSessionEndedAt *time.Time `json:"source_session_ended_at,omitempty"`
}
