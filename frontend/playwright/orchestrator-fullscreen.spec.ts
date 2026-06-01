import { test, expect } from '@playwright/test'
import {
  enablePtyCapture,
  stopAllRunningSessions,
  waitForAgentReady,
  waitForTerminalMount,
} from './ptyCapture'

// Orchestrator full-screen route + drawer pin behavior.
// The terminal is mounted exactly once at App root and CSS-positioned
// by mode (drawer / fullscreen / hidden). The tests verify:
//   (1) the dashboard scrolls without the drawer scrolling out of view,
//   (2) expanding the drawer navigates to /orchestrator and the host
//       switches to fullscreen mode,
//   (3) the *same* xterm.js Terminal instance survives expand/collapse
//       (fingerprint poked onto the instance before transition, asserted
//       after — disposal would replace the instance with a fresh one).

test.describe('Orchestrator full-screen', () => {
  test.afterEach(async ({ page }) => {
    await stopAllRunningSessions(page)
  })

  test('drawer stays pinned while the dashboard scrolls', async ({ page }) => {
    await page.goto('/')
    const drawer = page.locator('[data-testid="orchestrator-drawer"]')
    await expect(drawer).toBeVisible()
    const drawerTopBefore = await drawer.evaluate((el) => el.getBoundingClientRect().top)

    // Force the dashboard to overflow. The view's height is bound to
    // 100vh - chrome, so any content past that scrolls *inside* the
    // .dashboard container — not the document. Drawer is outside .dashboard.
    await page.evaluate(() => {
      const dash = document.querySelector('.dashboard') as HTMLElement | null
      if (!dash) throw new Error('.dashboard not present')
      const filler = document.createElement('div')
      filler.style.height = '4000px'
      filler.setAttribute('data-test-filler', '1')
      dash.appendChild(filler)
      dash.scrollTop = dash.scrollHeight
    })

    // Drawer's viewport-relative top should not have moved — the
    // dashboard scrolled in its own container, the drawer didn't move.
    const drawerTopAfter = await drawer.evaluate((el) => el.getBoundingClientRect().top)
    expect(drawerTopAfter).toBe(drawerTopBefore)
  })

  test('expand → /orchestrator, collapse → back; xterm.js instance survives', async ({ page }) => {
    await page.goto('/')
    await enablePtyCapture(page)

    const root = await page.evaluate(async () => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const W = window as any
      return (await W.go.app.App.StartRootSession('testagent')) as { uuid: string }
    })

    // Drawer is auto-pinned on /; wait for the host's terminal to mount.
    await page.waitForSelector('[data-testid="orchestrator-host"] .terminal-container', { timeout: 10_000 })
    await waitForTerminalMount(page, root.uuid, 15_000)
    await waitForAgentReady(page, root.uuid)

    // Fingerprint the Terminal instance. If expand/collapse disposed
    // it (e.g. portal target change unmounted the React subtree),
    // the post-transition lookup would either miss the property or
    // find a different instance.
    const fingerprint = await page.evaluate((id) => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const t = (window as any).__ideateTerminals[id] as Record<string, unknown> | undefined
      if (!t) return null
      const fp = `fp-${Math.random()}`
      t.__ideateTestFingerprint = fp
      return fp
    }, root.uuid)
    expect(fingerprint).toBeTruthy()

    // Expand via the floating overlay button. The button is inside the
    // drawer's aside; aria-label is the stable test target.
    await page.click('button[aria-label="Expand orchestrator"]')
    await expect(page).toHaveURL(/#\/orchestrator$/)
    await expect(page.locator('[data-testid="orchestrator-fullscreen"]')).toBeVisible()

    // Host should be in fullscreen mode, and the *same* terminal
    // instance — recognizable by its test fingerprint — is still there.
    const duringFullscreen = await page.evaluate((id) => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const t = (window as any).__ideateTerminals[id] as Record<string, unknown> | undefined
      const host = document.querySelector('[data-testid="orchestrator-host"]')
      const mode = host?.className.split(' ').find((c) => c.startsWith('orchestrator-host--')) ?? null
      return { fp: (t?.__ideateTestFingerprint as string | undefined) ?? null, mode }
    }, root.uuid)
    expect(duringFullscreen.fp).toBe(fingerprint)
    expect(duringFullscreen.mode).toBe('orchestrator-host--fullscreen')

    // Collapse via the floating overlay button on the full-screen view.
    await page.click('button[aria-label="Collapse orchestrator"]')
    // HashRouter may resolve the root to either `/` or `/#/` depending
    // on history; the meaningful assertion is "no longer on /orchestrator
    // AND the drawer is back". Use the drawer visibility as the
    // ground truth.
    await expect(page).not.toHaveURL(/#\/orchestrator/)
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toBeVisible()

    const afterCollapse = await page.evaluate((id) => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const t = (window as any).__ideateTerminals[id] as Record<string, unknown> | undefined
      const host = document.querySelector('[data-testid="orchestrator-host"]')
      const mode = host?.className.split(' ').find((c) => c.startsWith('orchestrator-host--')) ?? null
      return { fp: (t?.__ideateTestFingerprint as string | undefined) ?? null, mode }
    }, root.uuid)
    expect(afterCollapse.fp).toBe(fingerprint)
    expect(afterCollapse.mode).toBe('orchestrator-host--drawer')
  })

  // When the user exits the orchestrator's session (via /exit, an
  // upstream crash, or StopSession), the drawer must drop back to
  // its start form so a new session can be picked with a different
  // runner. Without this, OrchestratorContext sticks on the dead UUID
  // and the user has no path to a fresh session.
  test('stopping the root session re-shows the start form', async ({ page }) => {
    await page.goto('/')

    const root = await page.evaluate(async () => {
      // @ts-expect-error wails binding
      return (await window.go.app.App.StartRootSession('testagent')) as { uuid: string }
    })
    await page.waitForSelector('[data-testid="orchestrator-host"] .terminal-container', { timeout: 10_000 })

    // Stop it via the binding (same code path as /exit, modulo the
    // status reason). The context's status-listener should clear uuid.
    await page.evaluate(async (id) => {
      // @ts-expect-error wails binding
      await window.go.app.App.StopSession(id)
    }, root.uuid)

    // Start form returns: agent picker + Start button.
    const agentSelect = page.locator('.orchestrator-drawer-agent select')
    await expect(agentSelect).toBeVisible({ timeout: 5_000 })
    await expect(page.locator('.orchestrator-drawer-toolbar .btn-primary')).toHaveText('Start')
  })
})
