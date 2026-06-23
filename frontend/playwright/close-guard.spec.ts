import { test, expect, Page } from '@playwright/test'

// CloseGuardDialog renders when Go-side `App.BeforeClose` emits the
// `app:close-blocked` Wails event because one or more sessions are
// non-idle. The dialog names each busy session and offers Cancel
// (dismiss) or Stop & Quit (calls `App.ForceQuit` to bypass the
// guard).
//
// These tests drive the event from the frontend via
// `window.runtime.EventsEmit` rather than triggering the real close
// flow — Wails has no clean Playwright hook for "the window is
// closing", and the dialog's contract is the event payload it sees.
// The button handlers' Wails bindings (`ForceQuit`) are stubbed so we
// can observe invocation without actually quitting the app.

const FAKE_BUSY = [
  { slug: 'demo-idea', ideaName: 'Demo Idea', uuid: 'uuid-1', agentType: 'claude-code', activity: 'active' },
  { slug: 'another',   ideaName: 'Another',   uuid: 'uuid-2', agentType: 'testagent',  activity: 'waiting' },
]

interface WailsRuntime {
  EventsEmit: (eventName: string, ...data: unknown[]) => void
}
interface PageGlobals {
  runtime?: WailsRuntime
  go?: { app?: { App?: { ForceQuit?: () => Promise<void> } } }
  __forceQuitCalls?: number
}

async function fireCloseBlocked(page: Page, busy: unknown): Promise<void> {
  await page.evaluate((payload) => {
    const w = window as unknown as PageGlobals
    w.runtime?.EventsEmit?.('app:close-blocked', payload)
  }, busy)
}

async function stubForceQuit(page: Page): Promise<void> {
  await page.evaluate(() => {
    const w = window as unknown as PageGlobals
    w.__forceQuitCalls = 0
    if (!w.go?.app?.App) return
    w.go.app.App.ForceQuit = async () => {
      w.__forceQuitCalls = (w.__forceQuitCalls ?? 0) + 1
    }
  })
}

async function forceQuitCallCount(page: Page): Promise<number> {
  return page.evaluate(() => (window as unknown as PageGlobals).__forceQuitCalls ?? 0)
}

test.describe('Close guard dialog', () => {
  test('renders busy session list when app:close-blocked fires', async ({ page }) => {
    await page.goto('/')
    // The dialog mounts inside App; goto / and wait for the dashboard
    // so we know the listener has bound.
    await expect(page.locator('.dashboard')).toBeVisible({ timeout: 5000 })

    await fireCloseBlocked(page, FAKE_BUSY)

    const overlay = page.locator('.close-guard-overlay')
    await expect(overlay).toBeVisible({ timeout: 2000 })
    await expect(overlay.locator('h2')).toContainText('2 active sessions')
    // Each busy session's name + agent + activity appears as a row.
    await expect(overlay).toContainText('Demo Idea')
    await expect(overlay).toContainText('claude-code')
    await expect(overlay).toContainText('Another')
    await expect(overlay).toContainText('waiting')
  })

  test('singular wording with one session', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.dashboard')).toBeVisible({ timeout: 5000 })

    await fireCloseBlocked(page, [FAKE_BUSY[0]])

    const overlay = page.locator('.close-guard-overlay')
    await expect(overlay).toBeVisible({ timeout: 2000 })
    // "1 active session" — no trailing 's'.
    await expect(overlay.locator('h2')).toHaveText('1 active session')
  })

  test('Cancel dismisses the dialog without quitting', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.dashboard')).toBeVisible({ timeout: 5000 })
    await stubForceQuit(page)

    await fireCloseBlocked(page, FAKE_BUSY)
    const overlay = page.locator('.close-guard-overlay')
    await expect(overlay).toBeVisible({ timeout: 2000 })

    await overlay.locator('button:has-text("Cancel")').click()
    await expect(overlay).toHaveCount(0)
    expect(await forceQuitCallCount(page)).toBe(0)
  })

  test('Stop & Quit calls ForceQuit and dismisses', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.dashboard')).toBeVisible({ timeout: 5000 })
    await stubForceQuit(page)

    await fireCloseBlocked(page, FAKE_BUSY)
    const overlay = page.locator('.close-guard-overlay')
    await expect(overlay).toBeVisible({ timeout: 2000 })

    await overlay.locator('button:has-text("Stop & Quit")').click()
    await expect(overlay).toHaveCount(0)
    // Stub records the invocation; the real binding would call
    // Wails Quit. The dialog clears local state before the call
    // returns, so the overlay disappears regardless of the binding's
    // resolution timing.
    await expect.poll(() => forceQuitCallCount(page), { timeout: 1000 }).toBe(1)
  })

  test('empty busy list is ignored', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.dashboard')).toBeVisible({ timeout: 5000 })

    // CloseGuardDialog gates on `sessions && sessions.length > 0` —
    // an empty payload must NOT render the modal.
    await fireCloseBlocked(page, [])

    await page.waitForTimeout(200)
    await expect(page.locator('.close-guard-overlay')).toHaveCount(0)
  })
})
