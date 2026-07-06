Update one or more backlog items on the current idea. Bulk-by-default: pass a single-element `patches` array for one item, or sweep multiple items in one call (status transition over a whole queue, dependency cleanup after a blocker landed, scope extensions on a related set).

Each patch must carry `id` plus at least one mutable field. Slice fields (`depends_on`, `affects`, `labels`) **replace** the existing value; pass `[]` to clear; omit to leave unchanged.

When to update:

- start work: `status: "in_progress"`.
- finish: `status: "done"`.
- abandon: `status: "wontfix"` with a body explaining why so a future agent doesn't re-take it.
- refine title/body as the task gets clearer.
- a dependency landed: drop the resolved id from `depends_on`.
- scope grew: extend `affects` so downstream agents know the file footprint.

## Args

- `patches` (array, required): one or more patches. Each:
  - `id` (string, required).
  - `title`, `body` (string, optional): new value; empty string leaves alone (v1 has no explicit-clear path for strings).
  - `status` (string, optional): one of `open`, `in_progress`, `done`, `wontfix`.
  - `depends_on`, `affects`, `labels` (arrays, optional): replace; `[]` clears; omit to leave alone.
  - `external_url` (string, optional): set the upstream tracker URL; empty leaves the existing value alone.

## Returns

Per-patch result array: `[{id, status: "ok" | "not_found" | "error", error?}]`. Unknown ids return `not_found` (not an abort — the rest of the batch still applies). Missing-fields patches return `error: "no fields supplied"`. Each successful update records a `backlog_item_updated` history event.
