Unlink a worktree from the idea identified by `slug` (orchestrator
equivalent of `unlink_repo`).

`name` is the worktree leaf name (as returned by `list_repos`). With
`force=false` (default), refuses if the worktree has uncommitted changes;
`force=true` skips the check.

Works on any idea status (active / paused / archived) — cleanup paths
shouldn't be blocked by lifecycle gates.
