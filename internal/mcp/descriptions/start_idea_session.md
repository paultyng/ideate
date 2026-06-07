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
- `initial_prompt` (optional string): typed into the new session's
  prompt buffer once the agent's TUI is ready. Skipped when empty.
  Use this to brief the subagent in the same tool call that spawned
  it — no follow-up `send_session_input` round-trip needed.
- `initial_prompt_submit` (optional bool, default true): when true,
  the prompt is submitted as the first turn (Enter is sent after the
  text). When false, the text is left in the prompt buffer pre-filled
  so the human can review and submit. Ignored when `initial_prompt`
  is empty.

Response shape: `{uuid, resumed?, initial_prompt_delivered?,
initial_prompt_submitted?, initial_prompt_error?}`. The
`initial_prompt_*` fields only appear when `initial_prompt` was
supplied. `initial_prompt_error` surfaces when the agent took longer
than the ready-marker timeout to come up (10s default, override via
`IDEATE_AGENT_READY_TIMEOUT_MS`); the session is live and the caller
can retry the briefing via `send_session_input`.

Errors when the idea is missing, when a session is already running
for `(slug, agent_type)` (the single-session invariant), or when
`resume=true` is requested but no resumable session exists.

Orchestrator-only — the per-idea MCP server doesn't expose this
because an idea-bound agent shouldn't spin up another agent inside
its own idea (use the orchestrator to orchestrate across ideas).
