# Test-drift audit

> Backlog: `77885b55-5e55-4729-948c-d91122090f3d`.
> Trigger: the 2026-06-06 footer-chip episode — a Playwright test seeded
> review records to disk, bypassing `PendingReviewsBar`'s event-driven
> update path. The bar never showed a chip, the test passed, the user's
> bug stayed visible to them in the production app.

## What "test drift" means here

A test drifts when it bypasses a real production code path with a
shortcut (disk-seed, fake, mock) and the shortcut produces *almost*
the same state. The test passes; the production path stays broken or
untested. Worst case: a regression ships under a green suite.

Not all fakes are drift. A fake that stubs a cross-process boundary
(GitHub API, Slack) with a paired integration test for the real path is
fine. The audit catalogs places where the shortcut is the **only**
coverage.

See `~/.claude/rules/testing-philosophy.md` for the project preference:
real code → local `httptest`-style servers → dependency injection →
fakes (last resort).

## Inventory

Severity rubric:

- **H** = produces state the live code would never produce in the same
  way (different events, different timing). High risk of false confidence.
- **M** = produces functionally-equivalent state but skips the trigger
  (e.g. writes a file that a watcher would normally produce). Test
  passes when the trigger is broken.
- **L** = unavoidable boundary (cross-process, external API) AND there's
  a paired integration test for the real path.

| File:Line | Drift point | Real path bypassed | Sev |
|---|---|---|---|
| `frontend/playwright/idea-detail-review-indicator.spec.ts:24-28` | `seedReview()` disk injection | `CreateOrReopenMarkdownReview` → `review:changed` event → `PendingReviewsBar` refresh | H |
| `frontend/playwright/reviews-persistence.spec.ts:52-55` | `writeReview()` disk seed | `App.GetReview` Wails binding on hydration; draft fields never traverse the binding | H |
| `frontend/playwright/footer-chip-review-transition.spec.ts:21-34` | `seedMarkdownReview()` disk seed | Same as `idea-detail-review-indicator.spec.ts` — review creation → event emission → bar refresh | H |
| `frontend/playwright/dormant-resume-nav.spec.ts:63-65` | `record.status = 'dormant'` disk patch | `App.markSessionDormant` → `session:<uuid>:status` event → sidebar/dormant-resume UI | M |
| `frontend/playwright/command-palette.spec.ts:256-261` | Disk-write `status='dormant'` | Same as `dormant-resume-nav.spec.ts` | M |
| `frontend/playwright/dashboard.spec.ts:12-26` | `patchSessionActivity()` mutates session JSON | Activity-hook POST → store.UpdateSession → `idea:changed` event | M |
| `frontend/playwright/claude-sync.spec.ts:68-82` | `writeTranscript()` fixture | Headless summarizer's transcript-watcher + post-sync hook coordination | M |
| `frontend/playwright/dashboard.spec.ts:78-84` | Sidecar `summary.json` disk-write | Headless summarizer generation → `ListSessionSummaries` refresh | M |
| `frontend/playwright/dashboard.spec.ts:193-194` | `fs.mkdir('repos/')` direct seeding | `App.LinkRepo` worktree setup + path canonicalization | L |
| `internal/mcp/server_test.go:450-510` | `fakeResolver.GetSessionReplay` canned bytes | Real `vscreen.Snapshot()` bytes + xterm.js replay path | M |
| `internal/agent/summarizer/headless_generator_test.go:17-26` | `fakeRunner` synthetic NDJSON | Real Claude subprocess + wire-frame streaming + partial-output error handling | M |
| `internal/hooks/handler_test.go:31-96` | `fakeSessionStore` in-memory sessions | Real `FSStore.WriteSession` + `TouchIdea` + history-append side effects | M |

## Patterns

**Pattern 1: disk-seed review records.** Three H-severity drift points
all share the same shape — write JSON into `reviewsDir`, expect the
frontend to display it. None of them exercise the
`CreateOrReopenMarkdownReview` → `review:changed` event chain that the
real UI listens for. Fix: replace with the corresponding Wails binding
(`App.RequestMarkdownReview` / `App.RequestDiffReview`).

**Pattern 2: disk-seed session state.** Two M-severity drift points
hand-write `status='dormant'` into session records. The real dormant
path fires session-status events; the disk write doesn't. Fix: a
dev-build-only `App.ForceDormantSession(uuid)` Wails binding that
routes through the real `markSessionDormant` so the same events fire.

**Pattern 3: fakeResolver in MCP unit tests.** Used heavily across
`internal/mcp/*_test.go`. Crafts vscreen byte sequences inline. Risk is
shape drift if real vscreen output evolves. Mitigation already in
flight: `internal/mcp/agent_ready_integration_test.go` exercises the
same handlers against a real testagent PTY + coordinator. Recommend
this pattern for new MCP work — keep fake-based tests only for the
pure predicate / fast-path coverage they were authored for.

## Fixes shipping in this PR

1. **Pattern 1 fix #1:** `seedMarkdownReview` in
   `footer-chip-review-transition.spec.ts` swapped for the Wails
   binding. Exercises the real creation → event path.
2. **Pattern 2 fix:** new dev-build-only Wails binding
   `App.ForceDormantSession(uuid)`. Wraps the real `markSessionDormant`
   so tests fire the same events production does. `dormant-resume-nav.spec.ts`
   migrated to use it. `command-palette.spec.ts:256-261` flagged for
   the next pass.

## Backlog for follow-up

Out of this PR, into separate items:

- Pattern 1 across the other two specs (idea-detail-review-indicator,
  reviews-persistence) — same fix shape, separate PRs to keep diffs
  focused.
- `fakeResolver` audit by call site — for each test method, is the
  fake the only coverage or is there a paired integration test?
- `patchSessionActivity` — needs a Wails binding that wraps the
  activity-hook update path so the `idea:changed` event fires.

## Not drift

Excluded from the table even though they look fake-ish:

- `internal/store/*_test.go` filesystem tests — they write to
  `t.TempDir()` because that's the unit under test.
- `internal/mcp/agent_ready_integration_test.go` — real
  AgentCoordinator + real testagent, intentional and load-bearing.
- `internal/mcp/server_test.go` `fakeStore` — the store is the unit
  whose behavior is being asserted; the fake represents external
  callers, not internal state.

## Acceptance

Met when:

- This document exists at `docs/test-drift-audit.md`.
- At least two H/M-severity catalog entries are fixed (this PR ships
  two from Pattern 1 and Pattern 2).
- Follow-up items filed for the remaining entries.
