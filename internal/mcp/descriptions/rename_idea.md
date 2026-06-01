Rename an idea by its slug.

Moves `<ideasDir>/<slug>/` to `<ideasDir>/<new_slug>/` and rewires the
bookkeeping that pointed at the old path:

- Per-session `WorkingDir` is updated. Sessions whose cwd was inside
  the old idea tree keep their relative portion under the new tree;
  sessions whose cwd was outside the idea tree (e.g. started against
  an arbitrary path the human typed in) are rehomed to the new idea
  root.
- Linked git worktrees under `repos/` are repaired so both the
  canonical clone's gitdir pointer and the worktree's own `.git` file
  refer to the new location.
- Claude Code transcript directories under `~/.claude/projects/` are
  moved so `claude --resume <uuid>` still finds the prior session.
- A `renamed` history event is appended.

**Refuses with an error** when:

- the source slug doesn't exist;
- the target slug already names a directory under `<ideasDir>`;
- any session under the source idea is currently `Status=running` —
  rename is a destructive bookkeeping pass and won't run while a
  live agent's PTY cwd, transcript path, and worktree links could
  drift mid-operation. Stop the session first, or wait for it to
  exit.

Args:

- `slug` (string, required): the idea's current slug.
- `new_slug` (string, required): the target slug. Must be a valid
  slugified form (lowercase alphanumeric + hyphens, no spaces).

Returns the new slug as plain text on success.
