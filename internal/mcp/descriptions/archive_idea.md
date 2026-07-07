Archive an idea, stopping any running sessions and releasing linked
git worktrees.

**Refuses with an error** when:

- the idea doesn't exist;
- any linked worktree has uncommitted changes and `force` is not set
  — the error names the dirty worktree(s) so the caller can warn
  the user before retrying with `force: true`;
- any session under the idea is currently running and `force` is not
  set — the error identifies the blocking session;
- any backlog item is still `open` or `in_progress` and `force` is
  not set — the error names up to 10 titles plus a total count so
  the caller can surface what would be buried. Backlog is the idea's
  durable memory of in-flight work; the gate exists so archives are
  deliberate, not accidental.

`force: true` skips the dirty-worktree, running-session, and
open-backlog checks (and stops running sessions before archiving).

Args:

- `slug` (string, optional): the idea's slug. If omitted, uses the
  current session's idea.
- `force` (bool, optional, default false): override dirty-worktree,
  running-session, and open-backlog blocks.

Returns a plain-text summary on success, e.g.
`Archived my-idea (released 2 repos)`.
