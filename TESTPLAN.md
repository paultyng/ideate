# Manual Test Plan

Scenarios that require human verification — either because they need a real Claude Code session, depend on macOS system behavior, or exercise interactions that Playwright/Go tests can't cover.

Run `task dev:manual-test` (builds testagent, seeds test data, starts dev server) before testing unless otherwise noted.

Items marked **[P]** have at least partial Playwright coverage in `frontend/playwright/`. The manual check still verifies behaviors the test can't (rendering quality, UX, real-process integration, OS-level interactions).

---

## App Shell & Window

- [ ] `task dev` opens window with dark macOS titlebar, draggable, resizable
- [ ] Status footer shows version and uptime, refreshes every ~5s
- [ ] Stale socket recovery: `kill -9` the app process, relaunch — starts normally
- [ ] Duplicate instance: launch second `ideate` while running — shows error
- [ ] Window state persistence (**production binary only**, not `task dev`): `task build && open cmd/ideate/build/bin/ideate.app`, resize/move, close, relaunch — window restores position and size
- [ ] StartHidden: no flash at default position before window appears at saved position

## CLI & IPC Navigation

The CLI exposes only `status`, `review`, and the root daemon entry. Session start/stop, idea CRUD, and repo linking are intentionally UI + MCP only.

- [ ] `task cli -- status` returns version and uptime JSON
- [ ] `task cli -- review diff --repo . --base <ref> --head <ref>` navigates to running app's review view
- [ ] `task cli -- review <id>` opens existing review by id (kind auto-detected)
- [ ] Direct launch: close app, run `task cli -- review diff ...` — app opens directly to the diff review view

## Idea Management

- [ ] Create idea via UI: fill Name/Status/Summary, click Create, redirects to detail view **[P]**
- [ ] Edit idea: change status/summary, save, badge and content update
- [ ] Filesystem matches: `cat <ideas-dir>/<slug>/idea.md` shows correct YAML frontmatter + body
- [ ] Same-day slug collision: create two ideas on same day, second gets time component in slug
- [ ] History panel: expand at bottom of detail, shows created/updated events with timestamps **[P]**
- [ ] Archived toggle: archived ideas hidden by default, "Show archived (N)" toggle reveals them **[P]**
- [ ] List ordering: active ideas first, most recently updated within each group
- [ ] Live reload: with the app open on the idea list or a detail view, edit `<ideas-dir>/<slug>/idea.md` frontmatter in an external editor — the list/detail updates within ~1s without a manual refresh
- [ ] Live reload — new idea dir: `mkdir <ideas-dir>/test-new-idea` in a terminal — list refreshes and the new entry appears (will look incomplete until `idea.md` is created, but the watcher should pick up the dir)

## Backlog (per-idea)

- [ ] Idea detail shows backlog panel; seeded ideas (per `task seed:testdata`) carry items
- [ ] Add backlog item via orchestrator MCP (`add_backlog_items_by_slug`): item appears in panel without manual refresh
- [ ] Update item status to `in_progress` via MCP: chip / icon reflects new state
- [ ] Update item status to `done` via MCP: item moves to done group (or hides, depending on view)
- [ ] Delete item via MCP: removed from panel
- [ ] `depends_on` and `affects` fields persist (round-trip): MCP `list_backlog` returns the fields set on add

## Agent Sessions (testagent)

- [ ] Start testagent session from idea via "+" button, terminal shows banner and prompt **[P]**
- [ ] Terminal interaction: type text, see echo response **[P]**
- [ ] Window resize: terminal reflows, testagent shows `[resized: WxH]`
- [ ] Stop session: click Stop, shows "Stopping...", terminal shows "Goodbye!", status changes to exited
- [ ] Session record persisted: `cat <ideas-dir>/<slug>/sessions/<uuid>.json` has correct metadata
- [ ] Completed session in sidebar: gray dot, date, clicking shows metadata view **[P]**
- [ ] Running session reconnect: navigate away from running session and back, terminal replays buffered output
- [ ] Resume from metadata view: "Resume Session" button appears for resumable agents **[P]**
- [ ] Resume doesn't create duplicate sidebar entry: session count stays the same **[P]**
- [ ] New session via "+" creates a new sidebar entry: count increments **[P]**

## Dormant Sessions

Dormancy is now set only by the startup adoption sweep — sessions whose PID is dead on next launch flip to dormant. There is no runtime path to dormant while the app is running.

- [ ] Sessions stay `running` while the app is up regardless of how long they sit idle — no auto-dormancy
- [ ] App quit + relaunch with PIDs gone: prior `running` sessions appear as dormant in the bar
- [ ] Dormant session appears in the global session bar (chip in tabs zone) **[P]**
- [ ] Overflow popover lists dormant sessions alongside running ones
- [ ] Cmd+K palette: select a dormant entry → auto-resumes via the existing session UUID, terminal replays prior scrollback
- [ ] Orchestrator `list_sessions` MCP includes dormant entries with `status: "dormant"`
- [ ] Orchestrator `get_session_output` on a dormant session: returns last-captured terminal output without crashing
- [ ] Orchestrator `send_session_input` on a dormant session: resumes the session and submits input

## Orphan Recovery

- [ ] Force-delete a session's transcript file (`rm ~/.claude/projects/<encoded>/<uuid>.jsonl`): on next sweep, session record's `stop_reason` flips to `orphaned`, `outcome` reads "claude transcript deleted"
- [ ] Restore the transcript file from backup or rerun: on next sweep, `stop_reason` and the deleted-outcome marker both clear
- [ ] Resume an orphan-marked session: `Outcome` clears to empty on flip back to running

## Agent Sessions (real Claude Code)

These require `claude` CLI installed and authenticated. Start from an idea with seeded resources.

### Binary discovery (`internal/bindisco`)

The launchd-PATH gap is the most likely first-launch failure on macOS. Exercise each tier:

- [ ] **$PATH hit**: terminal-launch with `claude` on $PATH (`open -a Ideate` from a shell where `which claude` succeeds). Session starts.
- [ ] **Curated-paths hit**: launch from Finder/Dock with `claude` installed only at `~/.local/bin/claude` (or `/opt/homebrew/bin/claude` etc) and a barebones launchd PATH. Session starts; no override set.
- [ ] **`~/.claude/local/claude` hit**: same as above but with `claude` only at the Anthropic-installer location.
- [ ] **`IDEATE_CLAUDE_BINARY` override**: set the env var before launch (e.g. `IDEATE_CLAUDE_BINARY=/tmp/claude-stub /Applications/Ideate.app/Contents/MacOS/ideate`). Session uses the override even if a real claude is on $PATH.
- [ ] **`agents.claude.binary` config**: set in `<ideas-dir>/config.json`, restart Ideate, start session — override used. Env var (if set) wins over config.
- [ ] **Not-found error**: rename / remove `claude` from every searched location, start session — error toast starts with `binary not found: "claude"; looked in: $PATH, …` (lists every searched path), followed by the two escape hatches (`IDEATE_CLAUDE_BINARY` env, `agents.claude.binary` config) and a `which claude` hint.

- [ ] **Context injection**: start Claude session, ask "What idea are you working on?" — Claude mentions idea name, status, linked resources (from `--append-system-prompt`)
- [ ] **MCP get_idea**: ask Claude to call `get_idea` — returns current idea metadata
- [ ] **MCP list_resources**: ask Claude to call `list_resources` — returns linked resources
- [ ] **MCP add_resource**: ask Claude to add a resource — resource appears in idea detail sidebar after refresh. Re-adding the same URL with a richer `type` (e.g. promoting `web` to `github_pr`) updates in place (upsert semantics).
- [ ] **MCP delete_resource**: ask Claude to delete a resource by URL — resource disappears from idea detail
- [ ] **MCP update_idea**: ask Claude to update idea status/summary — changes reflected in UI
- [ ] **Session end hook**: after Claude session ends naturally, session JSON shows `status: "completed"` with `ended` timestamp, `session_ended` event in history
- [ ] **Resume behavior**: after completed Claude session, start another for same repo — Claude uses `--resume`, conversation continues from prior context
- [ ] **Session identity**: verify `--session-id` (new) and `--resume` (existing) flags pass the correct UUID

## Diff Viewer

- [ ] Open diff: `task cli -- review diff --repo . --base <ref> --head <ref>` — toolbar shows repo, range, file count **[P]**
- [ ] File tree navigation: click files, diff panel updates with syntax highlighting
- [ ] Split/Unified toggle: click toggles between two-column and single-column view
- [ ] Resizable pane: drag border between file tree and diff panel
- [ ] No params: visit `/#/review` directly — shows instruction text **[P]**

### Review submit/cancel (agent flow)

- [ ] Standalone via `diff start`: with the daemon stopped, run `task cli -- review diff start --repo . --base <ref> --head <ref>` — a new app window opens showing the diff with Submit/Cancel buttons. Click Submit — the window closes and the process exits.
- [ ] In-app via MCP: with the daemon running, trigger `request_diff_review` from a Claude Code session — the daemon navigates to the review view. Click Submit — the daemon stays running and the view shows "Submitted". **[P]** (in-app submit path)
- [ ] Multiple comments on the same line: in either flow, add two comments on the same line+side; both render in the inline thread; the file tree badge shows the correct count.
- [ ] Cancel: same paths as Submit — standalone closes window, in-app stays running and shows "Cancelled". **[P]** (in-app cancel path)
- [ ] Pending review survives clean shutdown: with a pending review on disk linked to a running session, quit the app and relaunch — review stays `pending`, session auto-resumes, agent's `get_diff_review_result` poll continues to block until the human submits.
- [ ] Pending review survives crash: same as above but `kill -9` instead of clean quit — startup marks the running session `crash`, auto-resumes it, review stays `pending`.
- [ ] Orphaned review is cancelled on startup: with a pending review on disk whose linked session ended cleanly (StopReason=exit / cleared / compacted / user / orphaned, or session record deleted), relaunch — review flips to `cancelled` and `get_diff_review_result` poll returns `cancelled`.
- [ ] Stale review is cancelled on startup: with a pending review whose `created` is >30 days old, relaunch — review flips to `cancelled` regardless of session status.
- [ ] Draft autosave: type a summary + add an inline comment on a pending review, wait ~1s, read `<configDir>/reviews/<id>.json` — `draft_body` and `draft_comments` reflect the in-progress edits; `body` and `comments` remain empty.
- [ ] Draft hydrate on reopen: with `draft_body` / `draft_comments` populated on a pending record, navigate to `/#/review?reviewId=<id>` — summary textarea and toolbar count chip restore the drafted values.
- [ ] Submit clears drafts: after a successful submit, `draft_*` fields are empty in the on-disk record (the authoritative `body`/`comments` carry the submitted values).
- [ ] Pending reviews bar: with one or more pending reviews on disk, navigate to `/` — chips appear in the topbar with the kind-appropriate label (file basename for markdown, `<repoLeaf> <base7>..<head7>` for diff). Click → navigates to that review.

## Markdown Review (M4d)

### Standalone CLI flow

- [ ] `task cli -- review md start --path README.md` opens the Milkdown editor seeded with the file's current content; CLI prints a review ID (e.g. `md-readme-<hash>`) **[P]** (editor-render path; CLI invocation is Go-tested)
- [ ] After Submit, the standalone window closes and `<configDir>/reviews/<id>.json` shows `status: complete` with `markdown.marked_up` reflecting the edit **[P]** (record state)
- [ ] After Cancel, the standalone window closes and the record shows `status: cancelled` **[P]** (record state)
- [ ] `task cli -- review status <id>` prints JSON with `kind: "markdown"`, `markdown.path`, `markdown.original`, `status`, and (post-submit) `markdown.marked_up`, `event`, `body`
- [ ] `task cli -- review <id>` reopens an existing markdown review by ID (kind auto-detected, no `--path` needed)

### Re-request semantics

(For the "any pending review blocks any new review" rule and CLI/MCP failure contract, see [Concurrent Reviews](#concurrent-reviews--one-pending-at-a-time).)

- [ ] After **cancel** on a markdown review, re-running `review md start --path <same file>` returns the **same deterministic ID** and refreshes `markdown.original` to the file's current content (iterate-after-cancel path)
- [ ] After **submit** on a markdown review, re-running `review md start --path <same file>` starts a fresh review (deterministic ID may be reused; check `created` timestamp differs)

### MCP / agent flow

- [ ] In-session: agent calls `request_markdown_review(path)` — daemon navigates to the markdown editor view
- [ ] Agent's `get_markdown_review_result(id)` blocks up to 60s and returns the marked-up content once the human submits
- [ ] Skill-driven application: with `.claude/skills/request-markdown-review/`, agent writes a file, requests review, then produces the next-version file by combining a textual diff + CriticMarkup scan — verbatim application for `{++ ++}` / `{-- --}` / `{~~ ~> ~~}`, interpretive application for `{>> <<}` comments and `body`

### Editor — Milkdown WYSIWYG (M4e: schema marks + suggesting mode)

- [ ] Seeded content renders with formatting (headings, paragraphs, lists, links, inline code, fenced blocks) **[P]**
- [ ] Top toolbar shows `+ Insert`, `− Delete`, `✎ Comment` buttons while status is pending; hidden once submitted/cancelled
- [ ] `+ Insert` toggles the insertion mark on the current selection (or sets storedMarks if no selection) **[P]**
- [ ] `− Delete` toggles the deletion mark on the current selection
- [ ] `✎ Comment` prompts for comment text and inserts a `criticComment` atom node at the cursor (renders as `{>>note<<}` styled pill)
- [ ] Floating selection toolbar (Crepe's bubble menu) shows only `Delete` — Insert/Comment live in the top toolbar
- [ ] Keymap: `⌘⇧I` toggle insertion, `⌘⇧K` toggle deletion, `⌘⇧N` insert comment
- [ ] **Suggesting mode (always-on in WYSIWYG)**: typing in the middle of a paragraph wraps the new text in an insertion mark (green underline). Source mode shows the typed text serialized as `{++...++}` **[P]**
- [ ] **Suggesting mode**: pressing Backspace on normal prose adds a deletion mark to the char before the cursor instead of removing it; cursor moves left past the now-struck-through char. Source mode shows `{-- --}` around it **[P]**
- [ ] **Suggesting mode**: pressing Backspace inside a fresh insertion (text typed in the same session) shrinks the insertion mark by one char — no `{-- --}` is generated **[P]**
- [ ] **Suggesting mode**: adjacent insertions (e.g. typing two separate words near each other) consolidate into a single `{++...++}` mark on serialization
- [ ] **Suggesting mode**: Backspace at the start of a paragraph defers to default Backspace behavior (joinTextblockBackward etc.) — does NOT produce a deletion mark spanning a block boundary
- [ ] **Suggesting mode — substitution**: select text, click `− Delete` (or Backspace) to mark it as deletion, then click inside the deletion-marked range and type — the typed chars wear *only* the insertion mark (green underline, no red strike). Submit and verify `marked_up` collapses the adjacent del/ins into a single `{~~old~>new~~}` substitution **[P]**
- [ ] Schema marks render per kind: `<ins class="cm-insertion">` green underline, `<del class="cm-deletion">` red strikethrough, `<span class="cm-comment">` yellow pill, `<span class="cm-substitution">` orange dashed
- [ ] CriticMarkup syntax inside fenced/inline code is preserved as plain text (the remark tokenizer only walks `text` mdast nodes, so `inlineCode` / `code` blocks pass through untouched)
- [ ] Text selection color is high-contrast blue on the dark theme (readable, not muddled with the bg)

### Source / WYSIWYG mode toggle

- [ ] Toolbar `Source` / `WYSIWYG` button toggles modes; pending submit/cancel buttons stay visible across the toggle **[P]**
- [ ] Source mode shows the full file text (including any YAML frontmatter); WYSIWYG mode hides frontmatter (Crepe would otherwise render `---` as a thematic break) **[P]**
- [ ] WYSIWYG → Source: body edits made in WYSIWYG appear in the source view; frontmatter remains intact
- [ ] Source → WYSIWYG → Source: frontmatter edits made in Source survive a WYSIWYG round-trip
- [ ] Submitting from Source mode preserves the source-edited content (frontmatter + body) in `markdown.marked_up` **[P]**

### Frontmatter (idea.md / docs with `---` blocks)

- [ ] Reviewing an `idea.md`: WYSIWYG hides frontmatter, body is editable, Submit preserves frontmatter byte-for-byte in `marked_up` **[P]**
- [ ] CriticMarkup marks added in WYSIWYG do not leak into the frontmatter region of `marked_up`

### Submit / Cancel / status

- [ ] Submit with no changes → `event: APPROVE`
- [ ] Submit with any change (CriticMarkup mark, direct prose edit, source-mode edit) → `event: REQUEST_CHANGES`
- [ ] After Submit (in-app, daemon mode), the editor view shows a `Submitted` status badge and the editor becomes read-only (toolbar mark buttons hidden) **[P]** (badge visibility)
- [ ] After Cancel (in-app), the view shows `Cancelled`, editor read-only **[P]** (badge visibility)

### Crash recovery

- [ ] `kill -9` the daemon with a pending markdown review linked to a running session — relaunch resumes the session and the review stays `pending`; agent's `get_markdown_review_result` poll keeps blocking until the human submits. (Orphaned and stale cases mirror the diff-review behavior — see the M4c crash-recovery items above.)
- [ ] Markdown draft autosave: in source mode, edit body + summary on a pending markdown review, wait ~1s, read the on-disk record — `markdown.draft_marked_up` and `draft_body` reflect the edits; `markdown.marked_up` remains empty.
- [ ] Markdown draft hydrate on reopen: with `markdown.draft_marked_up` populated on a pending record, navigate to `/#/review?reviewId=<id>` and toggle to source mode — the textarea content matches the persisted draft.

## Concurrent Reviews — One Pending at a Time

Only one pending review (any kind, any path or range) is allowed at a time. While a review is pending, attempts to start a new one fail. Once the pending review reaches a terminal state (`complete` or `cancelled`), a new review can be started and the window navigates to it.

**Failure contract:**
- **CLI** (`ideate review diff start` / `ideate review md start`) exits non-zero, prints `review already in progress: <id>` to stderr, and prints nothing to stdout. The window does not navigate.
- **MCP** (`request_diff_review` / `request_markdown_review`) returns a tool failure that includes the in-progress review ID so the calling agent can poll the existing review (via `get_*_review_result`) instead of retrying.

### Pending blocks new — CLI

- [ ] Pending markdown + `review md start --path <same path>` → exits non-zero with `review already in progress: <id>`, window unchanged
- [ ] Pending markdown + `review md start --path <different path>` → exits non-zero, window unchanged
- [ ] Pending markdown + `review diff start --repo . --base X --head Y` → exits non-zero, window unchanged
- [ ] Pending diff + `review md start --path <any>` → exits non-zero, window unchanged
- [ ] Pending diff + `review diff start` (same range) → exits non-zero, window unchanged
- [ ] Pending diff + `review diff start` (different range) → exits non-zero, window unchanged
- [ ] Reopen-by-ID of the pending review while it's displayed (`task cli -- review <id>`) → window stays on it (this is navigation, not "start a new review"); CLI exits zero

### Pending blocks new — MCP

- [ ] In-session agent calls `request_markdown_review` while any review is pending → tool returns failure naming the in-progress review ID; agent polls that ID via `get_markdown_review_result` (or `get_diff_review_result` if the in-progress kind is diff) and unblocks normally on completion
- [ ] In-session agent calls `request_diff_review` while any review is pending → same: failure with in-progress ID
- [ ] Cross-kind: pending markdown + agent calls `request_diff_review` → fails with the markdown review's ID; agent must poll that one first

### Terminal allows new — navigation

- [ ] Submit a markdown review (status → `complete`), then `review md start --path <new>` → succeeds, window navigates to the new editor
- [ ] Cancel a markdown review (status → `cancelled`), then `review diff start ...` → succeeds, window navigates to the diff viewer
- [ ] Submit a diff review, then `review md start --path <any>` → succeeds, window navigates to Milkdown editor
- [ ] Window focus on terminal→new transition: minimize the daemon window between submit and the new `review * start`, then start the new review — window unminimizes and comes to the front
- [ ] Reopen-by-ID of a different terminal review (`task cli -- review <other-id>`) → window navigates, kind auto-detected

## Deep-Links (`ideate://`)

- [ ] MCP tool responses (e.g. `get_idea`) include `ideate://idea/<slug>` deep-links in text fields
- [ ] Clicking a deep-link in the orchestrator terminal navigates the main view to the target idea
- [ ] macOS: `open ideate://idea/<slug>` from a fresh terminal launches the app (cold start) to the target idea
- [ ] macOS: with the app running, `open ideate://idea/<slug>` (hot launch) brings the existing window to front and navigates

## Resource Operations (MCP)

- [ ] `match_resource_urls`: pass an array of URLs (a mix of github_pr, notion, web) → returns the slug + label of each matching idea
- [ ] `list_ideas` includes inline `summary` field on each entry (single round trip, no follow-up `get_idea` needed)
- [ ] `list_sessions` includes inline `idea_summary` on each entry

## RSS Logging

- [ ] With `IDEATE_RSS_LOG_INTERVAL_SEC` set (default 60), structured `slog` lines for per-session RSS appear at that cadence; no session is stopped based on RSS

## Summarizer

- [ ] Real Claude session with a long transcript (>30 turns) triggers session-end summary: dashboard card shows the one-line intent within a few seconds of session end
- [ ] No autocompact thrash in `logs/`: grep for `auto-compact` patterns — should be quiet on a clean run (regression check for the env-var fix)

## Visual Regression — Folio Tokens

Spot-check after the color-token rewrite. Compare against the README's hero GIF for the canonical look.

- [ ] Dashboard: cream-on-dark background, idea-card hover states, status chip colors (active / paused / archived)
- [ ] Idea detail: sidebar, content area, header bar
- [ ] Session terminal: ANSI colors render correctly inside the host
- [ ] Diff review: green/red diff backgrounds, inline-comment chips
- [ ] Markdown review: CriticMarkup ins (green underline) + del (red strike) overlay colors
- [ ] Command palette: backdrop dim, selected row highlight
- [ ] Orchestrator drawer: pinned-state and expanded-state both readable

## macOS Release (signed build)

Run only against a real signed/notarized DMG built by the `v*` tag pipeline.

- [ ] Download notarized `.dmg` from a fresh tag release → mount → drag to Applications
- [ ] First launch from Applications: no Gatekeeper "unidentified developer" prompt, app opens directly
- [ ] Quarantine staple: `spctl -a -vv /Applications/Ideate.app` reports `accepted` / `notarized`
- [ ] Offline launch: disconnect from network, launch app — opens normally (notarization stapled, no online check needed)

## Concurrent & Edge Cases

- [ ] Concurrent cross-idea sessions: start sessions on two different ideas, verify both terminals work independently
- [ ] Single-session-per-workdir enforcement: start a session, try to start another for the same working dir — returns existing session
- [ ] Crash recovery: `kill -9` app during running agent session, relaunch — stale manifests cleaned, no orphaned processes
- [ ] Orphan process check: close app window, `ps aux | grep claude` shows no lingering agent processes

---

## Automated Coverage Reference

These are covered by automated tests and do NOT need manual verification:

**Playwright E2E** (`task test:ui` — 25 tests):
- Dashboard rendering, idea CRUD, session lifecycle with testagent, resume/new session sidebar counts, diff viewer rendering, form validation, archived toggle

**Go unit/integration** (`go test ./...`):
- Store (filesystem CRUD, history, sessions, repos, frontmatter parsing, slugs)
- Agent (coordinator lifecycle, manifest persistence, PTY session, context generation)
- MCP (tool handlers, HTTP transport, event callbacks)
- Hooks (typed handler dispatch, session finalization, history recording)
- IPC (server, socket path resolution)
- Review (diff parsing, local source)
