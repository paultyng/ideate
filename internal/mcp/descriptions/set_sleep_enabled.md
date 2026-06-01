Toggle the system sleep inhibitor (the "stay awake" assertion).

When `enabled=true`, the OS is prevented from sleeping while at least
one running session has Activity in {active, waiting}. When `false`,
the assertion is released immediately and the OS may sleep. State is
in-memory only — every app start defaults to disabled.

Use this when an orchestrator-driven workflow needs the machine to
stay awake (long-running agent sessions, scheduled tasks, deploy
windows) and toggle it back off when done so the user's machine can
sleep normally.

The returned object reflects the post-toggle state:

- `enabled`: the toggle value (whatever was just set).
- `held`: whether an OS sleep assertion is currently in effect.
  Only true when `enabled=true` AND at least one busy session
  exists; flipping `enabled=true` with no busy sessions returns
  `held=false`.

Orchestrator-only — the per-idea MCP server doesn't expose this since
sleep is a workspace-wide concern.
