Link a repository to the idea identified by `slug` (orchestrator
equivalent of `link_repo`, which is session-scoped).

A worktree is created at `<idea>/repos/<name>/`, branched off the per-idea
default branch unless `branch` is supplied. The auto-derived leaf name is
based on the repo's origin URL; pass `name` to override on collisions.

Used by the unarchive workflow — `unarchive_idea` returns the repo
URLs that were released on archive; the orchestrator re-links them via
this tool without needing to spawn an idea session.

Refuses on archived ideas (status gating in Phase 1).
