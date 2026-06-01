# AGENTS.md

Cross-agent guidance for any AI agent (Claude Code, future runners,
external orchestrators) working on the Ideate codebase. Claude-specific
context lives in [CLAUDE.md](CLAUDE.md); this file captures principles
that apply regardless of which agent is at the keyboard.

## Build features as MCP tools first, UI second

When adding a new capability, the default surface is an **MCP tool on
the orchestrator (root orchestrator) MCP server**, not a UI control.

Why:

- The orchestrator is the most-used surface — most workflows already
  start there. A tool exposed there reaches the user without a context
  switch.
- Tools compose: the orchestrator can chain `rename_idea`,
  `add_resource_by_slug`, `update_idea_by_slug` into a single
  operation. UI buttons can't.
- Tools are scriptable. A workflow that lives only in the UI can't
  be triggered from a session, a hook, or another agent.
- Less code: an MCP tool is a handler + a schema. A UI feature adds
  React state, routing, styling, Playwright tests, and the back-and-
  forth between frontend and backend.

The default lift is therefore: pick the right MCP surface (per-idea
vs orchestrator), wire the tool, write the test. Stop there.

### When to add UI

Only when the operation **cannot reasonably be driven from a
conversation**. The clearest case is **starting an interactive
session** — the user has to physically attach to a PTY, so a button
is unavoidable. Other cases that have justified UI in the past:

- Live terminal interaction (the xterm canvas is the UI).
- Markdown / diff review surfaces — the human reads a rendered diff
  and clicks comment widgets that don't translate to text.
- Status indicators that need to stay visible across navigations
  (footer toggles, session bar chips).

If a feature is a one-shot data mutation ("rename this idea", "drop
old reviews", "add a tag") it belongs in MCP. If it's a continuous
visual state ("show me the agent's terminal", "render this diff"),
it belongs in the UI.

### Pattern

1. Define the tool schema and handler in `internal/mcp/`.
2. Register it in `addCrossIdeaTools` (per-idea + root) or
   `addRootTools` (orchestrator-only) per its scope.
3. Emit a broker event (`idea:changed`, `idea:renamed`, etc.) for any
   user-visible state change so the frontend can re-render without
   polling.
4. Write a Go-side test against the handler.
5. Stop. UI affordance comes later if dogfooding shows the orchestrator
   path isn't ergonomic enough.

If you find yourself reaching for a new React component while the
backend method doesn't exist yet, you've inverted the order. Build
the tool, use it from the orchestrator for a few days, then decide
if a UI surface is actually warranted.
