Restore an archived idea back to active status.

**Refuses with an error** when the idea is not currently archived.

If the idea had linked git worktrees before being archived, those
repo resources are listed in the response so the orchestrator can
prompt the user to re-link them via `link_repo`.

Args:

- `slug` (string, required): the slug of the archived idea.

Returns a plain-text summary on success, e.g.
`Unarchived my-idea; re-link 1 repo resource(s) with link_repo`.
