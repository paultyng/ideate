Remove one or more backlog items from the current idea. Idempotent: unknown ids are reported in the response but don't abort the call.

Prefer `update_backlog_items` with `status: "done"` or `"wontfix"` when the item was addressed — it leaves a paper trail for future agents. Delete is for items that turned out to be wrong / duplicate / never-should-have-been-tracked.

## Args

- `ids` (array of strings, required): one or more item ids to remove.

## Returns

`{deleted: [ids that were removed], not_found: [ids that were unknown]}`. Records one `backlog_item_deleted` history event per successful delete.
