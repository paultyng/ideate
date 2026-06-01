Start a new agent session in an idea, or resume the most recent one.

Lets an orchestrator spin up a subagent in an idea without going
through the UI. Returns `{uuid}` — the stable identifier every other
orchestration tool (`send_session_input`, `get_session`,
`get_session_output`, `reply_to_orchestrator`, `goto_session`)
accepts.

Behavior:

- `slug`: idea slug as returned by `list_ideas` / `create_idea`.
- `agent_type`: the registered runner name. Defaults to `claude-code`
  when omitted. Use `list_ideas` plus an out-of-band probe if you
  need to know which agent types are available — in practice
  `claude-code` is the common case.
- `resume`: when true, looks up the most recent terminated session
  for this `(slug, agent_type)` and resumes it (Claude reuses its
  prior conversation transcript). When false (default), starts a
  fresh session.

Errors when the idea is missing, when a session is already running
for `(slug, agent_type)` (the single-session invariant), or when
`resume=true` is requested but no resumable session exists.

Orchestrator-only — the per-idea MCP server doesn't expose this
because an idea-bound agent shouldn't spin up another agent inside
its own idea (use the orchestrator to orchestrate across ideas).
