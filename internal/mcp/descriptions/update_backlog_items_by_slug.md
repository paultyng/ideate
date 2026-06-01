Update one or more backlog items on the idea identified by `slug`. Bulk variant of `update_backlog_items` for cross-idea sweeps.

## Args

- `slug` (string, required).
- `patches` (array, required): same shape and semantics as `update_backlog_items.patches`.

Returns the same per-patch result array as `update_backlog_items`. Records `backlog_item_updated` history on the target idea.
