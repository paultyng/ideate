package model

import "time"

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
	Summary    string     `yaml:"-" json:"summary,omitempty"`
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
