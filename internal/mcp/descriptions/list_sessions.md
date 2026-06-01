List every live idea-bound agent session across the workspace — both
`running` and `dormant` (process exited cleanly but resumable via
`start_idea_session`). The orchestrator's own orchestrator sessions are
intentionally excluded — these tools cannot target the orchestrator
itself.

Dormant entries report `state="dormant"`. `send_session_input` /
`get_session_output` will refuse them because no live process is
attached; resume first with `start_idea_session(slug, agent_type)`.

**Batch shape: inlines everything `get_session` returns per entry,
plus an optional output tail.** Pass `include_output_lines: N` to
fold the per-UUID `get_session_output` round-trips into the same
response. Don't loop calling `get_session` / `get_session_output`
after this — it's a 1+2N anti-pattern.

For idea-centric recaps (`summarize-ideas`) and switching
(`work-idea`), prefer `list_ideas` — it inlines the same session
data plus backlog state + idea summary in one call.

Returns an array of:
`{uuid, idea_slug, idea_name, idea_summary, agent_type, status,
activity, state, started, working_dir, last_activity_at, idle_seconds,
idle_bucket, recent_output, session_url, idea_url, idea_active_session_url}`.

URL fields are `ideate://` deep-links — `session_url` points at this
specific session, `idea_active_session_url` is the synthetic
"open whichever session is live for this idea" link, `idea_url` is
the idea detail page. Skills should emit these as link targets on
every session/idea reference.

- `state` ∈ {`active`, `awaiting`, `idle`, `dormant`} — shared with the filter DSL and skills.
- `idle_bucket` ∈ {`<1m`, `Nm`, `Nh`, `Nd`} — pre-bucketed for terse output.
- `idea_summary` is the one-line headless-generated summary of the
  idea, lifted from the persisted sidecar. Empty when no summary has
  been generated yet. Same value for every session on the same idea —
  it describes the idea, not the session — so it gives dormant entries
  the same historical context as running ones.
- `recent_output` is non-empty only when `include_output_lines > 0`.

## Args

- `exclude_archived` (boolean, default `true`): drop sessions whose
  parent idea has `status=archived`. Pass `false` to surface them.
- `include_output_lines` (integer, default `0`): if > 0, each entry's
  `recent_output` is populated with the last N lines of the session's
  screen (same as `get_session_output` with the defaults
  `strip_prompt_placeholder=true`, `raw=false`). Use this in place of
  per-UUID `get_session_output` calls.
- `filter` (string, optional): whitespace-separated tokens AND together;
  same-key tokens OR within. Supported tokens:
  - `s:<state>` — `s:active`, `s:awaiting`, `s:idle`, `s:dormant`.
    Multiple `s:` tokens widen the match (`s:active s:awaiting` =
    either).
  - `a:<agent_type>` — `a:claude-code`, `a:testagent`, etc. Multiple
    `a:` tokens widen the match.
  - `#<pr_number>` — match sessions whose parent idea has a `github_pr`
    resource pointing at that PR number. At most one `#` token per
    call.

Example: `s:awaiting a:claude-code #142` returns awaiting Claude
sessions on the idea linked to PR 142.
