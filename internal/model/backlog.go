package model

import "time"

// BacklogStatus is the lifecycle state of a single backlog item.
// Mirrors the read-repair shape of SessionStatus / idea Status:
// unknown values coerce to BacklogStatusOpen on parse.
type BacklogStatus string

const (
	BacklogStatusOpen       BacklogStatus = "open"
	BacklogStatusInProgress BacklogStatus = "in_progress"
	BacklogStatusDone       BacklogStatus = "done"
	BacklogStatusWontFix    BacklogStatus = "wontfix"
)

// BacklogSource identifies who or what added the item, for audit
// and UI affordances (e.g. surfacing agent-captured follow-ups
// differently from human-jotted tasks). Free-form string; common
// values: "human", "session", "orchestrator", "external".
type BacklogSource = string

// BacklogItem is one entry on an idea's backlog. The list lives at
// <ideasDir>/<slug>/backlog.json as a JSON array; full-rewrite on
// every mutation (matches the idea.json resource pattern).
type BacklogItem struct {
	ID      string        `json:"id"`             // UUID, set by AddBacklogItem
	Title   string        `json:"title"`          // one-line summary
	Body    string        `json:"body,omitempty"` // optional markdown context
	Status  BacklogStatus `json:"status"`         // default open
	Created time.Time     `json:"created"`
	Updated time.Time     `json:"updated"`
	Source  string        `json:"source,omitempty"` // BacklogSource

	// AssigneeSession is the UUID of the session that's actively
	// working the item, set on status → in_progress to surface
	// "who's doing this right now" without scanning every session
	// record. Optional; not all in-progress items have a single owner.
	AssigneeSession string `json:"assignee_session,omitempty"`

	// ExternalURL points at the upstream issue / ticket / task this
	// backlog item mirrors when it's synced to an external tracker
	// (GitHub Issues, Jira, Todoist, etc.). The URL is both the
	// navigation target the UI/agent can open and the canonical
	// identity for sync — same pattern Resource uses. Empty when
	// the item is local-only (the default for v1 since the sync
	// pipeline isn't built yet; the field is reserved so a future
	// sync milestone doesn't force a schema migration).
	ExternalURL string `json:"external_url,omitempty"`

	// DependsOn is a list of backlog item IDs this item blocks on.
	// Bare ID = same-idea (e.g. "abc-123"); "slug:id" = cross-idea
	// (e.g. "platform-migration:def-456"). v1 stores the strings
	// without validation — no cycle detection, no existence check.
	// Cycle detection and cross-idea resolution land in a follow-up
	// once the data shows it's worth the complexity.
	DependsOn []string `json:"depends_on,omitempty"`

	// Affects is a list of file paths this item is expected to
	// touch, relative to the idea root (`repos/<name>/<path>` for
	// linked worktrees; `<path>` for idea-root files). Key use
	// case: partitioning open work into non-overlapping file sets
	// so multiple subagents can run in parallel without stomping
	// each other.
	//
	// Paths are stored verbatim — no existence check (an agent
	// often marks a task that will create a new file).
	Affects []string `json:"affects,omitempty"`
}

// RepairBacklogStatus coerces an unknown BacklogStatus into Open.
// Same pattern as repairSessionStatus / read-repair in
// frontmatter.go — a typo'd or older-schema status becomes the safe
// default so the rest of the app doesn't have to defensively check.
func RepairBacklogStatus(item *BacklogItem) {
	switch item.Status {
	case "", BacklogStatusOpen, BacklogStatusInProgress, BacklogStatusDone, BacklogStatusWontFix:
		if item.Status == "" {
			item.Status = BacklogStatusOpen
		}
		return
	}
	item.Status = BacklogStatusOpen
}
