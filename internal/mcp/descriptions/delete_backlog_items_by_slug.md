Remove one or more backlog items from the idea identified by `slug`. Bulk variant of `delete_backlog_items` for cross-idea sweeps. Idempotent — unknown ids are reported, not aborted.

## Args

- `slug` (string, required).
- `ids` (array of strings, required).

Returns `{deleted: [...], not_found: [...]}`.
