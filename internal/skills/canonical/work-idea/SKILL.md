---
description: Use when the user wants to switch into, resume, or get caught up on a single Ideate idea. X may be referenced as an idea name, slug, session UUID, or fuzzy phrase. The unit of work is the idea — a session is one facet of it. Scope is the local Ideate workspace exposed by the ideate MCP.
when_to_use: |
  Trigger phrases: "switch to X", "go to X", "open X", "dive into X",
  "catch me up on X", "let me work on X next", "resume X",
  "what was I doing in X", "where did I leave off in X", "show me X",
  "pick X back up".
---

# work-idea

Workflow for producing a detailed brief on one Ideate idea, then
navigating into its session.

The unit of work is the **idea**. Sessions are one facet of an
idea, alongside the backlog (tasks queued or mid-flight), resources
(linked artifacts), and recent activity. The brief covers the
whole idea, not just whatever session happens to be running.

## Inputs

The user supplies an identifier for the target. Resolve in this
order:

1. Session UUID (matches `^[0-9a-f-]{36}$`) — resolve to its owning
   idea via the `sessions.running` / `sessions.dormant` UUIDs on
   each `list_ideas` row.
2. Idea slug (exact match).
3. Idea name (case-insensitive substring).
4. Fuzzy keyword from the user's phrasing (e.g. "the boilerplate
   idea", "PlanetScale", "JWKS").

If the input matches zero ideas or more than one, stop and ask the
user to disambiguate. Do not guess.

## Steps

1. **Batched workspace fetch.** Call `ideate:list_ideas` with
   `include_output_lines: 120`. The result carries every
   non-archived idea's `summary`, `backlog` counts +
   in-progress titles, `sessions.running` + `sessions.dormant`
   lists, `last_activity_at`, AND the most-recent running session's
   last 120 lines of screen — enough context to recover detailed
   state on the resolved idea. Resolve the user's input against the
   returned rows using the order above.

2. **(Optional) cross-references.** If the user's phrasing carries
   a URL (PR link, doc URL), call `ideate:match_resource_urls` to
   confirm the resolution and check whether the URL appears on
   sibling ideas the user might also want to know about. Mention
   sibling hits in the brief, do not switch to them.

3. **Parse the running session's tail (if any)** for:
   - Working branch (from the worktree banner, e.g.
     `testagent@refactor/rootflags-shared wt`).
   - Recent commits or pushes (commit subjects, PR numbers).
   - Open question(s), verbatim if short.
   - Any explicit "Next:" / "TODO:" markers the agent emitted.
   - PR / Jira / Slack links surfaced in the last screen.
   - Spinner state (`✻`/`✳`/`Doing…`/`Running…`) to flag if the
     agent is mid-turn.

   When there is no running session (the idea is dormant or
   between sessions), draw the same signals from the backlog
   in-progress items and the most-recent dormant session's
   snapshot. Don't fake live state; say the idea is dormant.

4. **Refine the running-session state from the tail.** A
   tool-reported `awaiting` only counts if the tail has a trailing
   question ("Want me to…", "Should I…") or real user text sitting
   unsubmitted in the `❯` prompt buffer. Otherwise treat the
   session as `idle` even when the store says `awaiting`. Dormant
   sessions stay dormant.

5. **Render** using the template below.

6. **Navigate.** This skill is one of the few that's allowed to
   take a state-changing step at the end — switching into the
   idea's session is the point of the trigger:

   - **Running session present**: call `ideate:goto_session` with
     the resolved slug + most-recent running session UUID.
   - **Dormant session present, no running**: call
     `ideate:start_idea_session(slug, agent_type, resume=true)` to
     resume the dormant session, then `ideate:goto_session` with
     the returned (same) UUID. Resume is the explicit price of
     this trigger phrase — the user said "switch to X", they want
     to be working in X.
   - **Neither**: call `ideate:goto_idea` with the slug so the
     user lands on the idea's detail view and can decide whether
     to spawn a fresh session.

## Output template

```
**<Idea name>** — <status>[ · <session state>][ · idle <bucket>][ · branch `<branch>`]
<one-line summary of the idea's purpose, from idea.summary>

### Backlog
- <N open, M in-progress[, K done]>
- doing: <in_progress_titles, one per line>
- next up: <up to 2 open titles, only when in-progress is empty AND open > 0>

### Recent work
- <2–4 bullets: completed work, commits, pushes, decisions>

### Open question
> <verbatim question from the agent, only if one exists; OMIT section if none>

### Action items
1. <ordered by urgency: thing to do right now>
2. <next thing>
3. <... up to 5 total>

### Links
- <PR / Jira / Slack / Notion URLs surfaced in the tail or resources; OMIT section if none>

### Cross-references
- <sibling ideas that reference the same URLs, when match_resource_urls turned up hits; OMIT section if none>
```

Rules:
- `<status>` is the idea's lifecycle status (`active`, `paused`).
- `<session state>` appears only when a session is attached:
  `running · active` / `running · awaiting` / `running · idle` /
  `dormant`. Use the refined value from step 4 if the tail
  disagrees with the store classification.
- `<idle bucket>` shows the time since `last_activity_at`, only
  when the session state is `idle` or `dormant`. Format:
  `<1m`/`Nm`/`Nh`/`Nd`.
- `<branch>` appears only when parsed from a running session's
  banner.
- **`### Backlog`**: counts line is always present (zero is a
  signal). `doing:` is the verbatim in-progress titles, capped at
  what `list_ideas` returned (5). `next up:` only renders when
  `in-progress == 0 AND open > 0` so the user sees the obvious
  starting point.
- **`### Recent work`**: distill to human-readable lines. No raw
  stack traces or full command output. Pull from `recent_output`
  for running sessions; pull from the backlog `done` history (if
  available) when no live session.
- **`### Open question`**: verbatim only when short. Longer
  questions get a one-line distillation with a pointer to scroll
  up.
- **`### Action items`**: concrete next steps the user can take.
  Not "consider doing X." Imperative voice. Pull from any explicit
  `Next:` / `TODO:` markers in the tail, then in-progress backlog
  titles, then open question, then suggested cleanup of any
  `done` items still hanging around.
- **`### Cross-references`**: only when `match_resource_urls`
  turned up sibling-idea hits for any URL in the brief. One line
  per cross-ref: `<sibling idea name>: <resource label>`. Link the
  sibling name to its `idea_active_session_url` from the
  `match_resource_urls` response so the user can jump straight
  into the sibling's live work.
- **Link the idea name** in the header to `idea_active_session_url`
  from `list_ideas`. The user clicking the brief wants to land in
  the live work; the synthetic URL handles running/dormant/none.
  Link specific session references to their `session_url`.
- If `<session state>` == `running · active` (spinner present),
  put **"agent is mid-turn — wait or interrupt deliberately"** at
  the very top, before the header line.
- No preamble, no closing.

## Example

```
**[Refactor service entrypoint](ideate://ideas/refactor-service-entrypoint/active-session)** — active · running · awaiting · branch `pt-entrypoint-split`
Split the service's data and control planes to remove a monolithic Config and reduce per-service entrypoint boilerplate.

### Backlog
- 2 open, 1 in-progress, 5 done
- doing: post rebase summary on PR #140

### Recent work
- Rebased PR #140 onto `main` after the per-subcommand Config split (PR #153) landed
- Resolved conflicts in `serve.go`, `serve_extauth.go`, `config.go`
- Dropped two obsolete commits about a removed comment label

### Open question
> Want me to post a summary comment on PR #140 explaining the rebase resolutions, or skip that since it's relatively obvious from the diff?

### Action items
1. Decide: post the rebase summary on PR #140 or skip
2. Find a reviewer for PR #140 and request review
3. Wait for CI on #140, #141, #67 to clear before requesting merge

### Links
- https://github.com/example-org/service/pull/140
- https://github.com/example-org/service/pull/141
- https://github.com/example-org/service/pull/67

### Cross-references
- [Rate limit follow-ups](ideate://ideas/rate-limit-follow-ups/active-session): same [#140](https://github.com/example-org/service/pull/140) referenced as the Service PR
```

## Constraints

- **One state change is allowed:** `ideate:goto_session` (or, when
  resuming a dormant session, `ideate:start_idea_session` then
  `ideate:goto_session`; or `ideate:goto_idea` when no session
  exists). The auto-resume is the explicit point of the trigger
  phrase — it is not autonomy creep.
- **Forbidden:** every other state-changing tool —
  `ideate:send_session_input` (auto-resumes a dormant target as a
  side effect), `ideate:update_idea_*`, `ideate:create_idea`,
  `ideate:add_resource_*`, `ideate:add_backlog_item*` /
  `ideate:update_backlog_item*` / `ideate:delete_backlog_item*`,
  `ideate:rename_idea`, `ideate:delete_idea`,
  `ideate:set_sleep_enabled`. The user drives all real actions;
  this skill orients them and lands them in the right surface.
- If `ideate:list_ideas` errors, surface the error in one line and
  stop. Do not retry.

## Rationalization counters

- "The user said 'switch to X' so I should also resolve their open
  question / add to the backlog / fire off the next task" →
  **No.** Brief + navigate. Nothing else.
- "Multiple matches; the most recent one is probably right" →
  **No.** Ask. Wrong target wastes more time than a clarification.
- "I should send a follow-up nudge based on the open question" →
  **No.** Surface the question. The user drives.
- "The idea has no session, I'll start a fresh one so the user
  doesn't have to" → **No.** Land on the idea page; the user
  decides whether the right move is a new session or something
  else (e.g. catching up on the backlog first).
- "Auto-resume is destructive autonomy" → **No.** The user said
  "switch to X" — resuming the dormant session is the cheapest
  read of "switch to". Skip resume only when there's no session at
  all.
