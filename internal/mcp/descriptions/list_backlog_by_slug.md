List backlog items for the idea identified by `slug`, sorted oldest-first. Same shape and filter args as `list_backlog`: returns `{id, title, status, created, updated, source?, assignee_session?, external_url?, depends_on?, affects?}` with `body` dropped by default.

Use to inspect a sibling idea's open work — e.g. "what's outstanding on the migration idea before I file a new task there?"

## Args

- `slug` (string, required) — target idea slug.
- `status` (string[], optional) — filter to items whose status is in the given set. Values: `open` | `in_progress` | `done` | `wontfix`. Pass `["open"]` for the common triage case; `["open", "in_progress"]` to surface active work. Omit to return all. Unknown values error rather than silently empty.
- `include_body` (boolean, optional, default `false`) — include each item's `body` (Markdown context). Default-off because large backlogs blow tool-output caps otherwise.
