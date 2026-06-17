List backlog items for the current idea, sorted oldest-first. Returns an array of `{id, title, status, created, updated, source?, assignee_session?, external_url?, depends_on?, affects?}`.

The `body` field is dropped by default to keep responses small — large backlogs blow tool-output caps otherwise. Pass `include_body=true` to round-trip it.

Use to triage open work, surface follow-ups to the user before exiting, or pick the next item to take on. Backlog is the idea's durable task list — separate from external trackers (GitHub Issues, Jira) and from session-local TODOs that die with the session.

## Args

- `status` (string[], optional) — filter to items whose status is in the given set. Values: `open` | `in_progress` | `done` | `wontfix`. Pass `["open"]` for the common triage case; `["open", "in_progress"]` to surface active work. Omit to return all. Unknown values error rather than silently empty.
- `include_body` (boolean, optional, default `false`) — include each item's `body` (Markdown context). Set true when you need the body to pick up or summarize a task.

`status` ∈ {`open`, `in_progress`, `done`, `wontfix`}.
