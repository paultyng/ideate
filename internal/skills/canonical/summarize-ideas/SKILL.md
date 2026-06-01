---
description: Use when the user asks for a recap, status readout, or check-in across Ideate ideas (a logical work item; "session" is one facet of an idea, not the unit). Scope is the local Ideate workspace exposed by the ideate MCP. Use `work-idea` instead when the user wants to focus on or switch into a single idea.
when_to_use: |
  Trigger phrases: "where am I", "where was I", "what's going on",
  "what's the status of my ideas", "what's the status of my sessions",
  "summarize my ideas", "summarize my sessions",
  "check in on my ideas", "check in on my sessions",
  "any active ideas", "any in-flight work",
  "what's pending", "what should I work on next",
  "give me a glance of everything".
---

# summarize-ideas

Workflow for producing a terse, idea-centric readout of every
in-flight idea in the workspace, then a consolidated `## Next` block
of high-impact actions for juggling multiple ideas at once.

The unit of work is the **idea**. Sessions are one facet ("what's
running on this idea"); backlog state is the other primary signal
("what's queued / blocked / mid-flight"). The recap reads by idea.

## Steps

1. **One batched call.** `ideate:list_ideas` with
   `include_output_lines: 40`. That single call returns every
   non-archived idea's `summary`, `backlog` counts +
   in-progress titles, `sessions.running` and `sessions.dormant`
   lists, `last_activity_at`, AND the most-recent running session's
   last 40 lines of screen — every signal this skill needs.

   Do **not** loop over the result calling `list_backlog_by_slug` /
   `get_session_output` / `get_idea_by_slug` per idea. One batched
   call covers it.

2. **Filter and sort.** Default: keep ideas with any signal —
   running session, dormant session, or non-zero `backlog.open +
   in_progress`. Drop ideas with neither (they're parked; the user
   hasn't asked for them). Sort by `last_activity_at` descending so
   the most-touched idea leads. If the user named a filter
   (`s:paused`, etc.), honor it.

3. **Pick 1–3 high-impact next actions across the whole readout.**
   After the per-idea sections, surface a consolidated `## Next`
   block. High impact, in priority order:

   - **Awaiting input.** Sessions in `awaiting` state (refined per
     step 4 below) need a human answer to keep moving. Surface
     these first.
   - **CI / review blocks.** PRs with failing CI or
     CHANGES_REQUESTED — pick up the agent's most recent revision
     loop.
   - **In-progress backlog idle a while.** An `in_progress` item on
     an idea whose `last_activity_at` is hours old means the agent
     left work half-done; suggest resuming.
   - **Dormant sessions with open backlog.** "Resume idea-X to take
     on its remaining 3 open tasks."

   Concrete shape: "answer the question on idea-X", "review CI
   failure on PR #N", "resume idea-Y to continue `<task title>`".
   Never generic ("consider next steps", "review the work"). Cap at
   3. If nothing is actually actionable across the readout, omit
   the section entirely rather than fabricating filler.

4. **Refine `state` from each running session's `recent_output`.**
   The session-list rows show `activity` from the store; `awaiting`
   requires:
   - A trailing question ("Want me to…", "Should I…", "Or move
     to…") in `recent_output`, OR
   - Real user text sitting unsubmitted in the `❯` prompt buffer
     (the `❯ Try "..."` hint is already stripped; any remaining
     `❯`-prefixed line is buffered input).

   If neither holds, treat the session as `idle` even if the store
   returned `awaiting`. Dormant sessions stay dormant — don't
   reclassify based on output.

5. **Enrich with PR status when applicable.** While traversing
   `recent_output` and idea resources for PR refs, collect every
   `#NNN` reference for which `owner/repo` is determinable.
   Deduplicate the resulting `owner/repo#N` set.

   If the set is non-empty, spawn ONE subagent to fetch status:

   - Tool: `Task`, `subagent_type: general-purpose`, `model: haiku`.
   - Input: the deduplicated list of `owner/repo#N` strings, plus a
     mapping of `owner/repo#N` → set of idea names that reference
     it.
   - Prompt: the literal block under "PR-status subagent prompt"
     below.
   - Output: a JSON array of records
     `{ref, state, ci, reviewDecision, mergeable, title, url, source, error?}`.

   Fold each record into the per-idea `PRs:` bullet of every idea
   that referenced it. When the set is empty, skip the spawn
   entirely — don't pay the Haiku round-trip for `[]`.

### PR-status subagent prompt

Embed this prompt verbatim in the `Task` invocation:

```
You are enriching a list of GitHub PR references with their current
status. Input: a list of "<owner>/<repo>#<n>" strings.

Group the input by <owner>. For each owner group:

1. Run `gh pr view <owner>/<repo>#<n> --json state,statusCheckRollup,reviewDecision,mergeable,title,url`
   on the first ref in the group. On success, fall through to step 3.
2. If stderr contains "Could not resolve to a Repository", the
   active `gh` account can't see this org. Enumerate every
   locally-configured account on github.com and try each:

     gh auth status --hostname github.com 2>&1 \
       | awk '/Logged in to github\.com account/ {print $7}'

   That prints one user handle per line. Pick the non-active ones
   (the active account already failed). For each candidate `<u>`:

   a. `gh auth switch -h github.com -u <u>`
   b. Retry step 1 with `<u>` active.
   c. On success, the group's auth is settled — fall through to
      step 3.
   d. On another "Could not resolve to a Repository", try the next
      `<u>`.

   `gh` accounts are named after user handles, not orgs — a single
   handle may have access to many orgs. Don't assume `<u> == <owner>`.

   If every locally-configured account fails to resolve, OR no
   other accounts exist, route every ref in the group to the
   unauthenticated public-API fallback below. Don't mark the group
   dead before trying that.
3. Once the group's auth is settled, fetch every remaining ref via
   the same `gh pr view ... --json ...` invocation. Do NOT re-run
   `gh auth switch` mid-group.

Map each successful `gh` response to a flat record:
- `state`: PR state (OPEN | CLOSED | MERGED).
- `ci`: collapsed CI rollup — "passing" (all SUCCESS),
  "failing" (any FAILURE), "pending" (any IN_PROGRESS / PENDING /
  QUEUED), or "none" (no checks).
- `reviewDecision`: APPROVED | CHANGES_REQUESTED | REVIEW_REQUIRED | null.
- `mergeable`: MERGEABLE | CONFLICTING | UNKNOWN.
- `title`, `url`: as returned.
- `source`: "gh".

Unauthenticated fallback. Use this when `gh` is unavailable for a
ref — `command -v gh` empty, the org-group auth retry already
failed, or a transient `gh` error:

  curl -sf -H "Accept: application/vnd.github+json" \
    https://api.github.com/repos/<owner>/<repo>/pulls/<n>

On HTTP 200, derive a degraded record:
- `state`: "MERGED" when JSON `merged` is true, else
  `state.toUpperCase()` (OPEN | CLOSED).
- `ci`: "unknown" — skip the second check-runs round-trip; recap
  speed matters more than CI granularity in this path.
- `reviewDecision`: null (unauth REST doesn't expose it cleanly).
- `mergeable`: derived from `mergeable_state` when present, else
  "UNKNOWN".
- `title`, `url`: from response fields `title` and `html_url`.
- `source`: "public-api".

On HTTP 404 (private repo without auth, or deleted PR), HTTP
non-2xx, or network failure, record `error` per the per-ref error
rules.

On unrecoverable per-ref error after both `gh` and the public API
have been tried, include an `error` field with a one-line summary:
"PR not found", "ref invalid", "network unreachable", or
"auth required for <owner>".

Constraints:
- Read-only. Do NOT comment, review, merge, or otherwise mutate
  PR state.
- Process refs sequentially within and across groups; don't fan
  out further.
- Don't restore the original `gh` account at exit — leave it
  wherever the last group's auth landed.
- Return ONLY the JSON array. No surrounding prose, no markdown
  fence, no commentary.
```

6. **Render** using the template below. No preamble, no recap, no
   closing.

## Output template

One section per idea, sorted by `last_activity_at` desc. After all
sections, a single consolidated `## Next` block when there's
anything actionable across the workspace.

```
**<Idea name>** — <status>[ · <session state>]
- Idea: <summary verbatim; OMIT bullet entirely when summary is empty>
- Backlog: <N open, M in-progress[, K done]>[, doing: <in_progress_titles[0]>[, <[1]>]]; OMIT entirely when all counts are zero
- Sessions: <running count> running[, <dormant count> dormant]; OMIT entirely when both lists are empty
- Last: <one-line distilled action from recent_output, if a running session has one>; OMIT entirely if no recent_output
- Pending: <verbatim question or buffered prompt; OMIT entirely if none>
- PRs: [#NNN](url) <state> · CI <ci-rollup>[, …]; OMIT entirely when no PR refs were resolved

… additional ideas …

## Next
- <Idea name>: <concrete action>
- <Idea name>: <concrete action>
```

Rules:
- `<status>` is the idea's lifecycle status (`active`, `paused`).
- `<session state>`: if any session is `awaiting` (after step-4
  refinement), put `· awaiting input` in the header so the most
  important state is in the first line of the section.
- **`Idea:` bullet**: `summary` verbatim when present. Describes
  the idea, not the session. Omit when empty; don't print `Idea:
  none` or fabricate a substitute.
- **`Backlog:` bullet**: counts in `N open, M in-progress` form; if
  there are `in_progress_titles`, append the first 1–2 as `, doing:
  <title>`. Omit `done` and `wontfix` counts unless the user asked
  for the full picture.
- **`Sessions:` bullet**: `N running, M dormant` — only the
  non-zero parts. Omit if both are zero. The header's `<session
  state>` already covers the awaiting case.
- **`Last:` bullet**: one human-readable line distilled from the
  running session's `recent_output`. No raw paths, stack traces,
  command output unless load-bearing. Omit when there's no
  `recent_output` (no running session, or include_output_lines was
  0).
- **`Pending:` bullet**: open question / buffered prompt from
  `recent_output`. Verbatim only when short. Longer goes to a
  one-line distillation. Omit when none.
- **`PRs:` bullet**: see PR rules below.
- No preamble, no trailing summary, no totals.
- **Omit the `## Next` section entirely** when nothing is
  actionable across the workspace. Don't print an empty block or
  "Next: none".

### `PRs:` bullet rules

- **Omit the bullet entirely** when no PR refs were resolved for
  this idea. Don't print `PRs: none`.
- Cap visible refs at 3 per idea; suffix `(+N more)` when
  truncated.
- Sort within the bullet by `state` priority:
  open-with-changes-requested > open-with-failing-CI >
  open-with-pending-CI > approved > merged > closed. Same priority
  dictates which 3 land when truncated.
- One-line per-ref shape (from the subagent's record):
  - Default (`source: "gh"`): `[#NNN](url) <state-lower> ·
    <decision>[ · CI <ci>]`, e.g. `[#146](...) open ·
    changes-requested · CI failing`. Omit decision when null. Omit
    CI when `"none"` or absent.
  - Public-API fallback (`source: "public-api"`): append `
    (unauth)` after state.
  - Error (`error` field set): `[#NNN](url) (status fetch failed:
    <reason>)`. Don't drop the ref.

### Linkify deeplinkable references

Every reference rendered in the readout — bullets AND the `## Next`
block — should be a markdown link when the destination URL is
constructible from observable context. Don't fabricate URLs; fall
back to plain text when unsure.

**Idea and session references** — `list_ideas` returns the URL
fields directly. Use them as the link target, never reconstruct:
- **Idea name** in section headers and `## Next` items → link
  target = `idea_active_session_url` (the synthetic "open the live
  session, resume if dormant, fall back to idea page" URL). The
  user clicking the recap wants to land in the active work, not a
  static detail page.
- **Specific session permalink** (rare — when a recap needs to cite
  one particular session by uuid) → link target = `session_url`.
- **Idea detail page** (when the user explicitly wants the
  not-the-session view) → link target = `idea_url`.

**External references** the response doesn't already URL-ize:
- **GitHub PRs** — `#NNN` when surrounding text or idea resources
  name `owner/repo`. URL: `https://github.com/<owner>/<repo>/pull/<n>`.
- **GitHub commit SHAs** — full or short SHAs when `owner/repo` is
  determinable. URL: `https://github.com/<owner>/<repo>/commit/<sha>`.
- **GitHub repo paths** — `owner/repo` tokens. URL:
  `https://github.com/<owner>/<repo>`.
- **Jira tickets** — keys like `PROJ-123` when the user's Jira host
  is known from prior context. URL: `https://<host>/browse/<KEY>`.
- **Branch / file paths** — usually not URL-able from context;
  leave as plain text with backtick formatting.

Markdown link syntax: `[#146](...)`. Use the original token as the
link text so the readout reads naturally; don't paraphrase ("the
auth PR" → keep `#146`).

## Example

```
**[Rate-limit policy refactor](ideate://ideas/rate-limit-policy-refactor/active-session)** — active · awaiting input
- Idea: Add per-tenant rate limits to the public API.
- Backlog: 2 open, 1 in-progress, doing: wire COV2/COV3 test cases
- Sessions: 1 running
- Last: flagged COV2/COV3 on [#146](https://github.com/example-org/api/pull/146) (`requireOwnership` not exercised via Apply/Delete)
- Pending: needs your nod on adding the missing test cases
- PRs: [#146](https://github.com/example-org/api/pull/146) open · changes-requested · CI failing

**[Datastore POC](ideate://ideas/datastore-poc/active-session)** — active
- Idea: Prove the candidate datastore can host the analytics fleet without rewriting the query layer.
- Backlog: 1 open, 0 in-progress
- Sessions: 1 dormant
- PRs: [#42](https://github.com/example-org/datastore/pull/42) merged (unauth)

**[Desktop app](ideate://ideas/desktop-app/active-session)** — active
- Idea: Cross-platform desktop app for tracking ideas through their lifecycle.
- Backlog: 4 open, 2 in-progress, doing: backlog UI panel, decision-record dirs
- Sessions: 1 running
- Last: running `task test:ui` (Playwright subset), ~3m elapsed

## Next
- [Rate-limit policy refactor](ideate://ideas/rate-limit-policy-refactor/active-session): confirm or push back on the COV2/COV3 additions on [#146](https://github.com/example-org/api/pull/146)
- [Desktop app](ideate://ideas/desktop-app/active-session): check the `task test:ui` run when it lands
- [Datastore POC](ideate://ideas/datastore-poc/active-session): resume to take on the one remaining open backlog item
```

## Constraints

- **Read-only.** Never call `ideate:send_session_input`,
  `ideate:goto_*`, `ideate:update_idea_*`, `ideate:create_idea`,
  `ideate:add_resource_*`, `ideate:add_backlog_item*`,
  `ideate:update_backlog_item*`, `ideate:delete_backlog_item*`,
  `ideate:rename_idea`, `ideate:delete_idea`, or any other
  state-changing tool. The `send_session_input` tool will
  auto-resume a dormant target — that side effect is exactly what
  this skill must avoid.
- If `ideate:list_ideas` errors, surface the error in one line and
  stop. Do not retry.
- If zero ideas return after filtering, emit exactly `No in-flight
  ideas.` and nothing else.

## Rationalization counters

- "It would be more useful if I sent a nudge to the awaiting
  session" → **No.** This skill is read-only. Surface the pending
  item; the user drives.
- "I should re-poll until something changes" → **No.** One pass per
  invocation. If the user wants polling, they'll invoke `/loop`.
- "I should fan out get_idea_by_slug / list_backlog_by_slug for
  details" → **No.** `list_ideas` returns everything inline; don't
  re-fetch per idea.
- "I'll add a couple of generic next actions so the section isn't
  empty" → **No.** If nothing's actually actionable, omit the
  `## Next` block.
- "I'll guess the GitHub owner/repo for this PR even though it's
  not in context" → **No.** If you can't confidently construct the
  URL, render the reference as plain text.
- "There are zero PR refs — I'll spawn the PR subagent anyway just
  to be safe" → **No.** Skip the spawn entirely; don't pay the
  Haiku round-trip for `[]`.
