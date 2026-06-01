Delete an idea by its slug. Removes the idea directory and any
linked git worktrees under it.

**Refuses with an error** when:

- the source slug doesn't exist;
- any session under the source idea is currently `Status=running`
  — stop the session first;
- any linked worktree has uncommitted changes and `force` is not
  set. The error names the dirty worktree(s) so the orchestrator
  can warn the user before retrying with `force: true`.

`force: true` skips the dirty-worktree check and force-removes the
worktrees. Uncommitted changes inside them are gone for good — the
canonical clones are untouched.

Args:

- `slug` (string, required): the idea's slug.
- `force` (bool, optional, default false): if true, delete despite
  dirty linked worktrees.

Returns the deleted slug as plain text on success.
