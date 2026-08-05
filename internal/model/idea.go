package model

import (
	"time"

	okf "github.com/paultyng/go-okf"
)

// Status represents the current state of an idea.
type Status string

const (
	StatusActive   Status = "active"
	StatusPaused   Status = "paused"
	StatusArchived Status = "archived"
)

// Idea is the core data type representing an idea and its metadata.
type Idea struct {
	Name string `yaml:"name" json:"name"`
	Slug string `yaml:"-" json:"slug"`
	// Created is persisted to frontmatter so bare-slug ideas (no
	// embedded date in the directory name) have an authoritative
	// creation timestamp. Date-prefixed legacy slugs fall back to
	// the parsed slug date when this field is absent — see
	// store.FSStore.List / Get.
	Created    time.Time  `yaml:"created,omitempty" json:"created" ts_type:"string"`
	Status     Status     `yaml:"status" json:"status"`
	PauseUntil *time.Time `yaml:"pause_until,omitempty" json:"pause_until,omitempty" ts_type:"string"`
	Updated    time.Time  `yaml:"updated,omitempty" json:"updated,omitempty" ts_type:"string"`
	Resources  []Resource `yaml:"resources,omitempty" json:"resources,omitempty"`
	// Body is the idea.md Markdown body (frontmatter is the rest of Idea).
	Body string `yaml:"-" json:"summary,omitempty"`

	// Description maps to the OKF concept's core `description` key.
	Description string `yaml:"-" json:"description,omitempty"`

	// Generated maps to the OKF concept's `generated.at` (v0.2 §5.2: "last
	// content change"). It advances only when Body or Description actually
	// change — never on a metadata-only write such as a session-activity
	// touch (see store.TouchIdea). This is the distinction Updated lacks:
	// Updated bumps on every write, so the summarizer's staleness sweep keys
	// off Generated to tell "a session ended since we last summarized" apart
	// from "something touched the idea". See store.updateUnlocked, okfmap.go.
	Generated time.Time `yaml:"-" json:"generated,omitempty" ts_type:"string"`

	// raw is the fully-parsed OKF concept this Idea was loaded from, if
	// any (nil for a freshly-constructed Idea, e.g. from create_idea).
	// conceptFromIdea clones it as the base for re-serialization before
	// overlaying the Ideate-managed fields above, so frontmatter keys
	// Ideate doesn't model (producer extensions, unmodeled OKF core
	// fields) survive a parse/serialize round trip. See okfmap.go.
	raw *okf.Concept
}

// IsArchived reports whether the idea is archived. Status is the single
// authoritative lifecycle field; the OKF `archived` ext key is derived from
// it on save — see okfmap.go.
func (i *Idea) IsArchived() bool {
	return i.Status == StatusArchived
}

// IsPaused reports whether the idea is paused *right now* — today is before
// PauseUntil (date-only comparison) — as opposed to Status, which answers the
// coarser "is a pause configured at all". Auto-resurfacing (flipping Status to
// active once PauseUntil elapses) is a later milestone, not M1: Status stays
// "paused" until the idea is explicitly resumed, even after IsPaused(today)
// goes false.
func (i *Idea) IsPaused(today okf.Date) bool {
	if i.PauseUntil == nil {
		return false
	}
	until := okf.NewDate(i.PauseUntil.Year(), i.PauseUntil.Month(), i.PauseUntil.Day())
	return today.Time.Before(until.Time)
}

// Resource links an idea to an external system.
type Resource struct {
	Type   string `yaml:"type" json:"type"`
	URL    string `yaml:"url,omitempty" json:"url,omitempty"`
	Label  string `yaml:"label,omitempty" json:"label,omitempty"`
	Status string `yaml:"status,omitempty" json:"status,omitempty"`
}

// ArchiveReport describes what was released when an idea was archived.
type ArchiveReport struct {
	ReleasedRepos []Resource
}

// UnarchiveReport lists repo resources the caller should re-link after
// restoring an idea from archive.
type UnarchiveReport struct {
	RepoResources []Resource
}

// HistoryEvent is one entry in an idea's append-only event log (JSON lines).
type HistoryEvent struct {
	Timestamp time.Time      `json:"ts" ts_type:"string"`
	Event     string         `json:"event"`
	Session   string         `json:"session,omitempty"`
	Fields    map[string]any `json:"fields,omitempty"`
}
