PROACTIVELY add one or more backlog items to the current idea. Bulk-by-default: pass a single-element array for the trivial case, or seed a whole stack of items in one call (e.g. when importing follow-ups from a plan, mining a session transcript, or filing a graph of related tasks).

Returns the stored items in input order, each with its server-assigned `id` and timestamps.

When to add (without waiting for the user to ask):

- mid-flow follow-up the user hasn't asked for yet ("we should write a regression test for this", "the doc needs a section on rollback")
- work the user explicitly defers ("park that for later")
- blocker discovered mid-session ("need approval from X before continuing", "waiting on the staging deploy")
- a task that belongs on a *different* idea — use `add_backlog_items_by_slug` for that, not this tool

This is your durable cross-session memory. Without it, follow-ups die when the session ends.

## Args

- `items` (array, required): one or more items. Each entry:
  - `title` (string, required): one-line summary.
  - `body` (string, optional): markdown context — links to commits, blockers, references, anything a future session needs to pick the task up cleanly.
  - `status` (string, optional): initial status. Defaults to `open`. Allowed: `open`, `in_progress`, `done`, `wontfix`.
  - `depends_on` (array of strings, optional): blocker item ids. Bare id for same-idea, `"slug:id"` for cross-idea. Stored verbatim; no cycle detection. Intra-batch references aren't auto-resolved — pre-mint and pass ids if you need a chain inside one call.
  - `affects` (array of strings, optional): file paths this item is expected to touch, relative to the idea root. Lets a subagent fan-out partition work into non-overlapping file sets.
  - `labels` (array of strings, optional): free-form string tags for cheap triage (e.g. `"quick-win"`, `"blocked-external"`, `"nit"`). Case-sensitive. `list_backlog`'s `labels` filter matches on any-overlap so multi-labeled items surface in every relevant filter.
  - `external_url` (string, optional): upstream tracker URL the item mirrors — GitHub issue, Jira ticket, Todoist task, etc. Both the navigation target and the canonical identity for sync. Empty for local-only items.

Records one `backlog_item_added` history event per item.
