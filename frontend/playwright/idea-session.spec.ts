import { test, expect } from '@playwright/test'
import {
  AGENT_LIFECYCLE_TIMEOUT_MS,
  enablePtyCapture,
  getMountedSessionId,
  readSessionReplay,
  stopAllRunningSessions,
} from './ptyCapture'

// Helper: create an idea and return its slug from the URL.
async function createIdea(page: import('@playwright/test').Page, name: string): Promise<string> {
  await page.goto('/#/idea/new')
  await page.fill('input[type="text"]', name)
  await page.selectOption('.idea-form select', 'active')
  await page.click('button[type="submit"]')
  await expect(page.locator('.idea-detail-name')).toHaveText(name)
  const url = page.url()
  return url.split('/idea/')[1].split('/')[0].split('?')[0]
}

// Helper: wait for testagent session to start and show terminal.
async function waitForTerminal(page: import('@playwright/test').Page) {
  await page.waitForSelector('.terminal-container', { timeout: 10000 })
  await page.waitForSelector('.xterm-screen', { timeout: 5000 })
}

// Helper: read the xterm.js buffer type ('normal' | 'alternate')
// for the given session, via the test-only window.__ideateTerminals
// registry. Returns 'missing' when no terminal is mounted, so the
// caller can poll without a try/catch.
async function readBufferType(page: import('@playwright/test').Page, sessionId: string): Promise<string> {
  return page.evaluate((id) => {
    const reg = (window as Window & {
      __ideateTerminals?: Record<string, { buffer: { active: { type: string } } }>
    }).__ideateTerminals
    return reg?.[id]?.buffer.active.type ?? 'missing'
  }, sessionId)
}

// Helper: read the xterm.js cursor coords for the given session.
// Returns null when no terminal is mounted; otherwise `{x, y, rows}`
// where (x, y) are 0-indexed buffer coords and rows is the viewport
// height (cursorY is relative to the viewport, not the absolute
// buffer line — alt-screen has no scrollback so they match).
async function readCursorState(
  page: import('@playwright/test').Page,
  sessionId: string,
): Promise<{ x: number; y: number; rows: number } | null> {
  return page.evaluate((id) => {
    const reg = (window as Window & {
      __ideateTerminals?: Record<string, {
        rows: number
        buffer: { active: { cursorX: number; cursorY: number } }
      }>
    }).__ideateTerminals
    const t = reg?.[id]
    if (!t) return null
    return { x: t.buffer.active.cursorX, y: t.buffer.active.cursorY, rows: t.rows }
  }, sessionId)
}

// Helper: drive the upstream testagent's /exit slash to end the
// session, then wait for status to flip. Driving /exit explicitly is
// faster (and more deterministic) than relying on the
// inactivity-exit watchdog, which is set to 30s in test:ui to keep
// orchestrator-driven sessions alive long enough to receive
// MCP-mediated writes.
async function waitForSessionEnd(page: import('@playwright/test').Page) {
  // Wait for upstream's banner so Bubbletea has put stdin in raw
  // mode — bytes typed before that go through the PTY's line
  // discipline and the slash-command parser never sees them.
  await page.waitForFunction(
    () => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const reg = (window as any).__ideateTerminals as
        | Record<string, { buffer: { active: { length: number; getLine: (i: number) => { translateToString: (trim: boolean) => string } | undefined } } }>
        | undefined
      if (!reg) return false
      for (const term of Object.values(reg)) {
        for (let i = 0; i < term.buffer.active.length; i++) {
          if (term.buffer.active.getLine(i)?.translateToString(true).includes('mcp connected:')) return true
        }
      }
      return false
    },
    { timeout: 15000 },
  )
  await page.locator('.terminal-container').click()
  await page.keyboard.type('/exit', { delay: 50 })
  await page.keyboard.press('Enter')
  await expect(page.locator('.session-toolbar-status, .status-badge'))
    .toContainText(/exited|stopped|completed/, { timeout: AGENT_LIFECYCLE_TIMEOUT_MS })
}

// Helper: expand the Sessions section if it's collapsed (default when
// sessions exist). Idempotent — no-op if already expanded.
async function expandSessions(page: import('@playwright/test').Page) {
  const title = page.locator('.sessions-section .idea-sidebar-section-title')
  if ((await title.getAttribute('aria-expanded')) === 'false') {
    await title.click()
  }
}

test.describe('Idea Session Flow', () => {
  test.afterEach(async ({ page }) => {
    await stopAllRunningSessions(page)
  })

  test('sessions section appears in idea detail sidebar', async ({ page }) => {
    const slug = await createIdea(page, 'Session Sidebar Test')

    // Sessions section should be in the sidebar
    await expect(page.locator('.idea-sidebar')).toContainText('Sessions')

    // Should show "No sessions yet" initially
    await expect(page.locator('.idea-sidebar-empty')).toContainText('No sessions yet')

    // "+" button should be visible
    await expect(page.locator('.idea-sidebar .btn-small')).toBeVisible()
  })

  test('start session from idea via + button', async ({ page }) => {
    const slug = await createIdea(page, 'Start Session Test')

    // Click + to start new session
    await page.click('.idea-sidebar .btn-small')

    // Should navigate to new session form
    await expect(page.locator('.idea-detail-name')).toContainText('Start Session Test')

    // Agent type selector should be visible
    const select = page.locator('select')
    await expect(select.first()).toBeVisible()

    // Select testagent and start
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')

    // Terminal should appear
    await waitForTerminal(page)
  })

  // When a running session terminates while the user is viewing it,
  // the view should swap in-place to the completed-session metadata
  // (with Resume + Start-new affordances). Without this, the user is
  // left staring at a dead terminal.
  test('exited session swaps in-place to terminated view', async ({ page }) => {
    await createIdea(page, 'In Place Exit Test')

    await page.click('.idea-sidebar .btn-small')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await waitForTerminal(page)

    // Stop the session via the binding for a deterministic exit.
    await page.evaluate(async () => {
      // @ts-expect-error wails binding
      const active = ((await window.go.app.App.ListActiveSessions()) ?? []) as Array<{ uuid: string }>
      const mine = active[active.length - 1]
      if (!mine) throw new Error('no running session found')
      // @ts-expect-error wails binding
      await window.go.app.App.StopSession(mine.uuid)
    })

    // View should transition without any user navigation. Metadata
    // div is the marker for the completed branch.
    await expect(page.locator('.session-metadata')).toBeVisible({ timeout: 10000 })
    await expect(page.locator('button[aria-label="Start new session"]')).toBeVisible()
    await expect(page.locator('button[aria-label="Resume session"]')).toBeVisible()
  })

  test('completed session shows in sidebar with metadata view', async ({ page }) => {
    const slug = await createIdea(page, 'Completed Session Test')

    // Start a testagent session
    await page.click('.idea-sidebar .btn-small')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await waitForTerminal(page)

    // Wait for testagent to exit
    await waitForSessionEnd(page)

    // Navigate back to idea detail
    await page.click('.btn-back')
    await expect(page.locator('.idea-detail-name')).toHaveText('Completed Session Test')

    // Section is collapsed by default once sessions exist — expand to inspect.
    await expandSessions(page)

    // Wait for session list to update (polls every 3s)
    await expect(page.locator('.idea-sidebar-item.session.completed')).toBeVisible({ timeout: 10000 })

    // Completed session should show gray dot
    await expect(page.locator('.session-icon.completed')).toBeVisible()

    // Click the completed session
    await page.click('.idea-sidebar-item.session.completed')

    // Should show metadata view (not terminal)
    await expect(page.locator('.session-metadata')).toBeVisible()
    await expect(page.locator('.session-metadata')).toContainText('testagent')
  })

  test('resume button appears for resumable agent on completed session', async ({ page }) => {
    const slug = await createIdea(page, 'Resume Button Test')

    // Start and wait for testagent to complete
    await page.click('.idea-sidebar .btn-small')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await waitForTerminal(page)
    await waitForSessionEnd(page)

    // Go back, click completed session
    await page.click('.btn-back')
    await expandSessions(page)
    await expect(page.locator('.idea-sidebar-item.session.completed')).toBeVisible({ timeout: 10000 })
    await page.click('.idea-sidebar-item.session.completed')

    // Resume button should be visible (testagent is resumable)
    await expect(page.locator('.session-metadata')).toBeVisible()
    await expect(page.locator('button[aria-label="Resume session"]')).toBeVisible()
  })

  test('resume starts session, shows terminal, and keeps same sidebar count', async ({ page }) => {
    const slug = await createIdea(page, 'Resume Flow Test')

    // First session
    await page.click('.idea-sidebar .btn-small')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await waitForTerminal(page)
    await waitForSessionEnd(page)

    // Go back — 1 completed session
    await page.click('.btn-back')
    await expect(page.locator('.idea-sidebar-item.session')).toHaveCount(1, { timeout: 10000 })

    // Click completed session, click Resume
    await expandSessions(page)
    await page.click('.idea-sidebar-item.session.completed')
    await expect(page.locator('button[aria-label="Resume session"]')).toBeVisible()
    await page.click('button[aria-label="Resume session"]')
    await waitForTerminal(page)
    await waitForSessionEnd(page)

    // Go back — still 1 session (resume doesn't create a new entry)
    await page.click('.btn-back')
    await expect(page.locator('.idea-sidebar-item.session')).toHaveCount(1, { timeout: 10000 })
  })

  test('new session after completed creates second sidebar entry', async ({ page }) => {
    const slug = await createIdea(page, 'Multiple Sessions Test')

    // First session
    await page.click('.idea-sidebar .btn-small')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await waitForTerminal(page)
    await waitForSessionEnd(page)

    // Go back — 1 completed session
    await page.click('.btn-back')
    await expect(page.locator('.idea-sidebar-item.session')).toHaveCount(1, { timeout: 10000 })

    // Start a NEW session via + (not resume)
    await page.click('.idea-sidebar .btn-small')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await waitForTerminal(page)
    await waitForSessionEnd(page)

    // Go back — should see 2 completed sessions
    await page.click('.btn-back')
    await expect(page.locator('.idea-sidebar-item.session')).toHaveCount(2, { timeout: 10000 })
  })

  test('+ button is disabled while a session is running', async ({ page }) => {
    const slug = await createIdea(page, 'Single Session Lock Test')

    // Start testagent — keep it running, don't wait for end.
    await page.click('.idea-sidebar .btn-small[aria-label="Start a new session"]')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await waitForTerminal(page)

    // Back to idea detail; sidebar polls every 3s.
    await page.goto(`/#/idea/${slug}`)
    await page.waitForSelector('.idea-sidebar', { timeout: 10000 })

    // Within the polling window, the + button should be disabled and
    // the Open running session button should appear.
    await expect(page.locator('.idea-sidebar .btn-small[aria-label="Cannot start: a session is already running"]'))
      .toBeVisible({ timeout: 5000 })
    await expect(page.locator('.idea-sidebar .btn-small[aria-label="Open running session"]'))
      .toBeVisible()
  })

  test('Open running session button navigates to live terminal', async ({ page }) => {
    const slug = await createIdea(page, 'Open Running Session Test')

    await page.click('.idea-sidebar .btn-small[aria-label="Start a new session"]')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await waitForTerminal(page)

    await page.goto(`/#/idea/${slug}`)
    await page.waitForSelector('.idea-sidebar', { timeout: 10000 })

    const openBtn = page.locator('.idea-sidebar .btn-small[aria-label="Open running session"]')
    await expect(openBtn).toBeVisible({ timeout: 5000 })
    await openBtn.click()

    // Should land back on the live terminal.
    await waitForTerminal(page)
  })

  test('new-session form blocks Start when a session is already running', async ({ page }) => {
    const slug = await createIdea(page, 'Form Block Test')

    // Start one session, leave it running.
    await page.click('.idea-sidebar .btn-small[aria-label="Start a new session"]')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await waitForTerminal(page)

    // Manually navigate to the new-session form — bypasses the disabled +.
    await page.goto(`/#/idea/${slug}/session/new`)

    // Notice should appear; Start Session disabled.
    await expect(page.locator('.session-already-running')).toBeVisible({ timeout: 5000 })
    await expect(page.locator('button:has-text("Start Session")')).toBeDisabled()
    await expect(page.locator('button[aria-label="Open running session"]')).toBeVisible()
  })

  test('backend rejects a direct StartIdeaSession when one is already running', async ({ page }) => {
    const slug = await createIdea(page, 'Backend Race Test')

    await page.click('.idea-sidebar .btn-small[aria-label="Start a new session"]')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await waitForTerminal(page)

    // Call the binding directly, simulating a UI race.
    const result = await page.evaluate(async (s: string) => {
      try {
        // @ts-expect-error wails runtime binding
        await window.go.app.App.StartIdeaSession(s, 'testagent', false)
        return { ok: true }
      } catch (e) {
        return { ok: false, err: String(e) }
      }
    }, slug)

    expect(result.ok).toBe(false)
    expect(result.err).toMatch(/already running|stuck in running/i)
  })

  test('idea-detail topbar shows Terminal session-nav button when a session exists', async ({ page }) => {
    // No sessions → button absent.
    await createIdea(page, 'Terminal Nav Empty')
    await expect(page.locator('.btn-nav-session')).toHaveCount(0)

    // Spawn a session so the topbar gets the forward affordance.
    await page.click('.idea-sidebar .btn-small')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await waitForTerminal(page)

    // Lightbulb back-nav from session view → idea detail.
    await page.click('.btn-nav-idea')
    await expect(page.locator('.idea-detail-name')).toHaveText('Terminal Nav Empty')

    // Terminal forward-nav from idea detail → session view.
    await expect(page.locator('.btn-nav-session')).toBeVisible()
    await page.click('.btn-nav-session')
    await expect(page.locator('.terminal-container')).toBeVisible({ timeout: 10000 })
  })

  // Removed: 'session replay re-enters alt-screen after nav-away + nav-back'
  // Both testagent v0.4+ and Claude Code v2.x render inline (main buffer)
  // and never enter alt-screen (DECSET ?1049). Verified by direct PTY
  // capture against Claude Code v2.1.153 — no \x1b[?1049h in 3139 bytes
  // of interactive output. The regression this test guarded ("vscreen
  // snapshot drops the ?1049h enter so remount lands in main-buffer
  // scrollback") can no longer fire because no agent we support is in
  // alt-screen when the snapshot is taken. The dead vscreen alt-screen
  // emit path is tracked in the backlog for cleanup.

  // Note: there's a sibling cursor-restore bug — pre-fix, the
  // alt-screen Snapshot ended at end-of-last-rendered-character
  // instead of vt's cursor position, so xterm's cursor parked in the
  // wrong row until the agent's next full repaint. An e2e regression
  // test would be racy: bubbletea repaints with a CUP within the same
  // frame, masking the wrong cursor before the test can read it. The
  // unit test
  // `TestBuffer_Snapshot_AltScreen_EndsWithCursorPositionEscape`
  // covers it at the snapshot byte level.

  test('Shift+Enter inserts a newline (multi-line input)', async ({ page }) => {
    const slug = await createIdea(page, 'shift-enter')

    await page.goto(`/#/idea/${slug}/session/new`)
    await enablePtyCapture(page)
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await page.waitForSelector('.terminal-container', { timeout: 10000 })
    await page.waitForSelector('.xterm-screen', { timeout: 5000 })
    const uuid = await getMountedSessionId(page)

    const readTerminalText = () => readSessionReplay(page, uuid)

    // mcp-connected lifecycle marker — see waitForAgentReady's comment
    // for why this is the readiness signal instead of the banner text.
    await expect.poll(readTerminalText, { timeout: AGENT_LIFECYCLE_TIMEOUT_MS }).toContain('mcp connected:')

    await page.locator('.terminal-container').click()
    await page.keyboard.type('line1', { delay: 100 })
    await page.keyboard.press('Shift+Enter')
    await page.waitForTimeout(100)
    await page.keyboard.type('line2', { delay: 100 })
    await page.keyboard.press('Enter')

    await expect.poll(readTerminalText, { timeout: 5000 }).toContain('[shift-enter] line1')
    await expect.poll(readTerminalText, { timeout: 5000 }).toContain('line2')
  })

  test('testagent /fake-tool fires the PostToolUse hook', async ({ page }) => {
    const slug = await createIdea(page, 'Hook Fire Test')

    await page.goto(`/#/idea/${slug}/session/new`)
    await enablePtyCapture(page)
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await page.waitForSelector('.terminal-container', { timeout: 10000 })
    await page.waitForSelector('.xterm-screen', { timeout: 5000 })

    const uuid = await getMountedSessionId(page)
    await expect.poll(() => readSessionReplay(page, uuid), { timeout: AGENT_LIFECYCLE_TIMEOUT_MS }).toContain('mcp connected:')

    await page.locator('.terminal-container').click()
    await page.keyboard.type('/fake-tool Bash {"cmd":"echo hi"}', { delay: 50 })
    await page.keyboard.press('Enter')
    await page.keyboard.type('/fake-tool-result {}', { delay: 50 })
    await page.keyboard.press('Enter')

    await expect.poll(async () => {
      return await page.evaluate(async () => {
        // @ts-expect-error wails binding
        const busy = ((await window.go.app.App.BusyRunningSessions()) ?? []) as Array<{ slug: string }>
        return busy.map((s) => s.slug)
      })
    }, { timeout: 10000 }).toContain(slug)
  })

  test('testagent /clear creates a sibling session linked to predecessor', async ({ page }) => {
    const slug = await createIdea(page, 'Clear Flow Test')

    await page.goto(`/#/idea/${slug}/session/new`)
    await enablePtyCapture(page)
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await page.waitForSelector('.terminal-container', { timeout: 10000 })
    await page.waitForSelector('.xterm-screen', { timeout: 5000 })

    const uuid = await getMountedSessionId(page)
    const readTerminalText = () => readSessionReplay(page, uuid)

    await expect.poll(readTerminalText, { timeout: AGENT_LIFECYCLE_TIMEOUT_MS }).toContain('mcp connected:')

    await page.locator('.terminal-container').click()
    await page.keyboard.type('/clear', { delay: 50 })
    await page.keyboard.press('Enter')

    await expect.poll(async () => {
      return await page.evaluate(async (s) => {
        // @ts-expect-error wails binding
        const list = (await window.go.app.App.ListIdeaSessions(s)) as Array<{
          uuid: string
          status: string
          stop_reason?: string
          previous_uuid?: string
        }>
        return list.map((r) => ({
          status: r.status,
          stopReason: r.stop_reason ?? '',
          hasPrev: Boolean(r.previous_uuid),
        }))
      }, slug)
    }, { timeout: 10000 }).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ status: 'completed', stopReason: 'cleared' }),
        expect.objectContaining({ status: 'running', hasPrev: true }),
      ]),
    )

    await page.goto(`/#/idea/${slug}`)
    await page.waitForSelector('.idea-sidebar', { timeout: 5000 })
    await expect(page.locator('.idea-sidebar-item.session')).toHaveCount(2, { timeout: 10000 })
  })

  test('testagent /compact creates a sibling session with stop_reason=compacted', async ({ page }) => {
    const slug = await createIdea(page, 'Compact Flow Test')

    await page.goto(`/#/idea/${slug}/session/new`)
    await enablePtyCapture(page)
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await page.waitForSelector('.terminal-container', { timeout: 10000 })
    await page.waitForSelector('.xterm-screen', { timeout: 5000 })

    const uuid = await getMountedSessionId(page)
    const readTerminalText = () => readSessionReplay(page, uuid)
    await expect.poll(readTerminalText, { timeout: AGENT_LIFECYCLE_TIMEOUT_MS }).toContain('mcp connected:')

    await page.locator('.terminal-container').click()
    await page.keyboard.type('/compact', { delay: 50 })
    await page.keyboard.press('Enter')

    await expect.poll(async () => {
      return await page.evaluate(async (s) => {
        // @ts-expect-error wails binding
        const list = (await window.go.app.App.ListIdeaSessions(s)) as Array<{
          uuid: string
          status: string
          stop_reason?: string
          previous_uuid?: string
        }>
        return list.map((r) => ({
          status: r.status,
          stopReason: r.stop_reason ?? '',
          hasPrev: Boolean(r.previous_uuid),
        }))
      }, slug)
    }, { timeout: 10000 }).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ status: 'completed', stopReason: 'compacted' }),
        expect.objectContaining({ status: 'running', hasPrev: true }),
      ]),
    )

    await page.goto(`/#/idea/${slug}`)
    await page.waitForSelector('.idea-sidebar', { timeout: 5000 })
    await expect(page.locator('.idea-sidebar-item.session')).toHaveCount(2, { timeout: 10000 })
  })

  test('agent prompt stays on the bottom row when history overflows the viewport', async ({ page }) => {
    const slug = await createIdea(page, 'viewport-overflow')

    await page.goto(`/#/idea/${slug}/session/new`)
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await page.waitForSelector('.terminal-container', { timeout: 10000 })
    await page.waitForSelector('.xterm-screen', { timeout: 5000 })
    const uuid = await getMountedSessionId(page)

    const readTerminalText = () => readSessionReplay(page, uuid)
    await expect.poll(readTerminalText, { timeout: AGENT_LIFECYCLE_TIMEOUT_MS }).toContain('mcp connected:')

    for (let i = 0; i < 12; i++) {
      await page.evaluate(({ id, n }) => {
        // @ts-expect-error wails binding
        return window.go.app.App.WriteToSession(id, `/stream 0s pad line ${n}\r`)
      }, { id: uuid, n: i })
    }

    await expect.poll(readTerminalText, { timeout: 15000 })
      .toContain('[viewport-overflow] pad line 11')

    await expect.poll(async () => {
      const screen = await readTerminalText()
      const rows = screen.split('\n').map((r) => r.replace(/\s+$/, ''))
      let lastNonBlank = rows.length - 1
      while (lastNonBlank >= 0 && rows[lastNonBlank] === '') lastNonBlank--
      return lastNonBlank >= 0 ? rows[lastNonBlank] : ''
    }, {
      timeout: 5000,
      message: "bottom-most rendered row didn't start with '>'",
    }).toMatch(/^>/)
  })

  test('input echoes on the bottom row after a viewport-filling stream', async ({ page }) => {
    const slug = await createIdea(page, 'viewport-input-echo')

    await page.goto(`/#/idea/${slug}/session/new`)
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await page.waitForSelector('.terminal-container', { timeout: 10000 })
    await page.waitForSelector('.xterm-screen', { timeout: 5000 })
    const uuid = await getMountedSessionId(page)

    const readTerminalText = () => readSessionReplay(page, uuid)
    await expect.poll(readTerminalText, { timeout: AGENT_LIFECYCLE_TIMEOUT_MS }).toContain('mcp connected:')

    for (let i = 0; i < 12; i++) {
      await page.evaluate(({ id, n }) => {
        // @ts-expect-error wails binding
        return window.go.app.App.WriteToSession(id, `/stream 0s pad line ${n}\r`)
      }, { id: uuid, n: i })
    }
    await expect.poll(readTerminalText, { timeout: 15000 })
      .toContain('[viewport-input-echo] pad line 11')

    await page.locator('.terminal-container').click()
    const probe = 'echo-me'
    await page.keyboard.type(probe, { delay: 50 })

    await expect.poll(async () => {
      const screen = await readTerminalText()
      const rows = screen.split('\n').map((r) => r.replace(/\s+$/, ''))
      let lastNonBlank = rows.length - 1
      while (lastNonBlank >= 0 && rows[lastNonBlank] === '') lastNonBlank--
      return lastNonBlank >= 0 ? rows[lastNonBlank] : ''
    }, { timeout: 5000 }).toContain(probe)
  })

  test('toolbar Resume on exit keeps same sidebar count', async ({ page }) => {
    const slug = await createIdea(page, 'Toolbar Resume Test')

    // Start testagent
    await page.click('.idea-sidebar .btn-small')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await waitForTerminal(page)

    // Wait for exit — toolbar should show Resume (icon-only, aria-labeled).
    await waitForSessionEnd(page)
    await expect(page.locator('button[aria-label="Resume session"]')).toBeVisible()

    await page.click('button[aria-label="Resume session"]')
    await waitForTerminal(page)
    await waitForSessionEnd(page)

    // Go back — still 1 session (toolbar resume doesn't create a new entry).
    // Navigate via full page load: hash-only goto can stall when xterm holds focus.
    await page.goto(`about:blank`)
    await page.goto(`/#/idea/${slug}`)
    await page.waitForSelector('.idea-sidebar', { timeout: 10000 })
    await expect(page.locator('.idea-sidebar-item.session')).toHaveCount(1, { timeout: 10000 })
  })
})
