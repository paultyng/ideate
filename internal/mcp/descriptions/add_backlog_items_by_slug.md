PROACTIVELY add one or more backlog items to the idea identified by `slug`. Bulk variant of `add_backlog_items` for cross-idea handoffs and multi-item seeding.

Typical use: an agent working on `idea-A` realizes "these follow-ups belong on `idea-B`" — drop the full list on B's backlog in one call instead of bouncing the user through the orchestrator. `list_ideas` first to confirm the target slug.

## Args

- `slug` (string, required): target idea slug.
- `items` (array, required): same shape as `add_backlog_items.items` — `title` required, `body` / `status` / `depends_on` / `affects` / `labels` / `external_url` optional.

Returns the stored items in input order. Records one `backlog_item_added` history event per item on the target idea (not the calling idea).
