import { test, expect } from '@playwright/test'
import { enablePtyCapture, readSessionReplay, stopAllRunningSessions, waitForAgentReady } from './ptyCapture'

// Coverage for terminal OSC 8 hyperlink clicks in the orchestrator
// drawer's TerminalPanel. Drives the upstream paultyng/testagent
// /link command — which emits a single OSC 8 hyperlink:
//
//   /link <url> [text]
//   →  <ESC>]8;;<url><ESC>\<text><ESC>]8;;<ESC>\
//
// The spec patches window.runtime.BrowserOpenURL, clicks the rendered
// link's canvas coords, and asserts the URL was captured — proves
// xterm's linkHandler → openExternal → BrowserOpenURL path works
// in the drawer (where a previously-reported click bug lived).

interface OpenURLCapture {
  urls: string[]
}

// Patch window.runtime.BrowserOpenURL so we can observe what
// openExternal would route through. The wails runtime helper
// (frontend/src/wailsjs/runtime/runtime.js BrowserOpenURL) just
// forwards to window.runtime.BrowserOpenURL — patching here covers
// every code path that ends up calling it.
async function patchBrowserOpenURL(page: import('@playwright/test').Page): Promise<void> {
  await page.evaluate(() => {
    const w = window as unknown as Record<string, unknown>
    const captured: string[] = []
    w.__capturedOpenURLs = captured
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const rt = (w as any).runtime ?? ((w as any).runtime = {})
    rt.BrowserOpenURL = (url: string) => { captured.push(url) }
  })
}

async function getCapturedURLs(page: import('@playwright/test').Page): Promise<OpenURLCapture> {
  return await page.evaluate(() => {
    const w = window as unknown as Record<string, unknown>
    const urls = (w.__capturedOpenURLs as string[] | undefined) ?? []
    return { urls: [...urls] }
  })
}

// Find the on-screen row whose cells carry OSC 8 link metadata for
// `linkText` (preferred) and click the middle of that text. Falls back
// to the first row containing `linkText` when no row has an OSC 8 link
// (e.g. plain-URL tests that route through `WebLinksAddon`, which
// registers per-line providers rather than per-cell metadata).
//
// The link-cell preference matters when `linkText` appears twice on
// screen: e.g. testagent's `/link` echoes the user-input command
// (`> /link <url> example link`) AND emits the OSC 8 link as a
// separate output row. The user-input row has no link metadata; a
// naive first-match click lands there and silently no-ops. The
// OSC 8-marked row is the one the user would visually click.
//
// xterm viewport rows map 1:1 to on-screen pixel rows; cell height is
// screen.height / terminal.rows. Cell width is screen.width /
// terminal.cols (NOT a hardcoded 80, which broke when the drawer was
// resized to a different col count).
// `requireOscLink: true` switches clickLinkRow into an event-driven wait
// for xterm.js's OSC 8 `urlId` attribute to land on the matched cell. After
// a drawer resize the buffer reflows asynchronously (FitAddon → xterm
// render loop), and during that window the OSC 8 metadata briefly detaches
// from the visible cells — locally it settles in <50ms, but the macOS-14
// CI runner has been observed taking longer. waitForFunction polls every
// ~100ms and resolves the moment xterm reapplies the metadata, so we add
// zero waste on a fast settle and stay deterministic on a slow one.
//
// Plain-URL tests (WebLinksAddon) don't set `urlId` — the matcher attaches
// the link region per-line at render time, with no cell attribute. Pass
// `requireOscLink: false` (the default) so those tests fall back to the
// first text match and click that.
async function clickLinkRow(
  page: import('@playwright/test').Page,
  containerSelector: string,
  linkText: string,
  options: { requireOscLink?: boolean; timeoutMs?: number } = {},
): Promise<boolean> {
  const { requireOscLink = false, timeoutMs = 5000 } = options
  const handle = page.locator(containerSelector)
  await handle.waitFor({ state: 'visible' })
  const screen = handle.locator('.xterm-screen').first()
  const box = await screen.boundingBox()
  if (!box) return false

  const queryArgs = { sel: containerSelector, needle: linkText, requireOscLink }
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const queryFn = ({ sel, needle, requireOscLink }: typeof queryArgs): any => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const reg = (window as any).__ideateTerminals as Record<string, unknown> | undefined
    if (!reg) return null
    const root = document.querySelector(sel) as HTMLElement | null
    if (!root) return null
    // Pick the terminal whose .terminal-container ancestor matches
    // the requested selector — there can be multiple terminals
    // mounted (idea-session + orchestrator).
    for (const [, termU] of Object.entries(reg)) {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const term = termU as any
      const elem: HTMLElement | undefined = term.element
      if (!elem) continue
      if (!root.contains(elem)) continue
      const buf = term.buffer.active
      const start: number = buf.viewportY
      const candidates: Array<{ row: number; col: number; hasOscLink: boolean }> = []
      for (let visible = 0; visible < term.rows; visible++) {
        const line = buf.getLine(start + visible)
        if (!line) continue
        const text: string = line.translateToString(true)
        const col = text.indexOf(needle)
        if (col < 0) continue
        // Probe a cell inside the matched text for an OSC 8 link
        // marker. `cell.extended.urlId` is xterm v6's per-cell link
        // attribute (set by InputHandler.setHyperlink).
        const probeCol = col + Math.floor(needle.length / 2)
        const cell = line.getCell(probeCol)
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const hasOscLink = !!(cell as any)?.extended?.urlId
        candidates.push({ row: visible, col, hasOscLink })
      }
      if (candidates.length === 0) continue
      const oscRow = candidates.find((c) => c.hasOscLink)
      // OSC 8 tests require a cell with urlId or the click won't fire
      // linkHandler.activate. Return null so waitForFunction keeps polling
      // while xterm settles the buffer.
      if (requireOscLink && !oscRow) continue
      const pick = oscRow ?? candidates[0]
      return { row: pick.row, col: pick.col, totalRows: term.rows, totalCols: term.cols }
    }
    return null
  }

  let meta: { row: number; col: number; totalRows: number; totalCols: number } | null
  if (requireOscLink) {
    // waitForFunction polls until queryFn returns a truthy object; we then
    // jsonValue() it back into the test-side shape.
    const jsHandle = await page.waitForFunction(queryFn, queryArgs, { timeout: timeoutMs })
    meta = (await jsHandle.jsonValue()) as typeof meta
  } else {
    meta = (await page.evaluate(queryFn, queryArgs)) as typeof meta
  }
  if (!meta) return false

  const cellW = box.width / meta.totalCols
  const cellH = box.height / meta.totalRows
  // Click the middle of the matched text. For OSC 8 links every cell
  // in the region carries the same urlId, so any in-range column
  // fires linkHandler.activate; for WebLinksAddon plain URLs the
  // matcher's range covers the full URL text the same way.
  const x = box.x + (meta.col + linkText.length / 2) * cellW
  const y = box.y + (meta.row + 0.5) * cellH
  await page.mouse.click(x, y)
  return true
}

test.describe('Terminal link clicks', () => {
  test.afterEach(async ({ page }) => {
    await stopAllRunningSessions(page)
  })

  // SKIP: orchestrator's testagent dies ~7s into the test on CI macOS-14
  // (e2e log shows `session_end hook reason=other` with exit_code=0 before
  // the click can fire). Locally passes deterministically. Root cause not
  // yet pinned — tracked in backlog 726a2a3c (the diagnostic-log upload
  // landed in PR #19 so the next investigator has data). Unskip once the
  // orchestrator-session-lifetime race is fixed.
  test.fixme('OSC 8 hyperlink click routes through openExternal — orchestrator drawer', async ({ page }) => {
    await page.goto('/')
    await enablePtyCapture(page)

    const uuid = await page.evaluate(async () => {
      // @ts-expect-error wails binding
      const r = (await window.go.app.App.StartRootSession('testagent')) as { uuid: string }
      return r.uuid
    })

    const containerSelector = '.orchestrator-host .terminal-container'
    await page.waitForSelector(containerSelector, { timeout: 10000 })
    await patchBrowserOpenURL(page)

    // Bubbletea puts stdin in raw mode only after the first render —
    // bytes written before then go to the PTY's line discipline and
    // never reach the slash-command parser.
    await waitForAgentReady(page, uuid)

    // Upstream /link <url> [text] emits an OSC 8 hyperlink with the
    // given URL + display text. ?via=osc8 marker is preserved through
    // BrowserOpenURL so the assertion can confirm the link click
    // routed through xterm's linkHandler (not WebLinksAddon's regex
    // matcher, which would fire if the user clicked a bare URL).
    await page.evaluate(async (id) => {
      // @ts-expect-error wails binding
      await window.go.app.App.WriteToSession(id, '/link https://example.com/from-testagent?via=osc8 example link\r')
    }, uuid)

    // Read on-screen state via vscreen replay — the link text is
    // currently visible on the agent's screen and we want the check
    // to fail if it isn't actually rendered (vs. just written to the
    // PTY).
    await expect.poll(() => readSessionReplay(page, uuid), { timeout: 10000 })
      .toContain('example link')

    const clicked = await clickLinkRow(page, containerSelector, 'example link', { requireOscLink: true })
    expect(clicked).toBeTruthy()

    await expect.poll(async () => (await getCapturedURLs(page)).urls, { timeout: 5000 })
      .toContain('https://example.com/from-testagent?via=osc8')
  })

  test('plain http URL click routes through openExternal — orchestrator drawer (WebLinksAddon path)', async ({ page }) => {
    await page.goto('/')
    await enablePtyCapture(page)

    const uuid = await page.evaluate(async () => {
      // @ts-expect-error wails binding
      const r = (await window.go.app.App.StartRootSession('testagent')) as { uuid: string }
      return r.uuid
    })

    const containerSelector = '.orchestrator-host .terminal-container'
    await page.waitForSelector(containerSelector, { timeout: 10000 })
    await patchBrowserOpenURL(page)
    await waitForAgentReady(page, uuid)

    await page.evaluate(async (id) => {
      // @ts-expect-error wails binding
      await window.go.app.App.WriteToSession(id, 'visit https://example.com/plain-url?via=weblinks for docs\r')
    }, uuid)

    await expect.poll(() => readSessionReplay(page, uuid), { timeout: 10000 })
      .toContain('https://example.com/plain-url?via=weblinks')

    const clicked = await clickLinkRow(page, containerSelector, 'https://example.com/plain-url')
    expect(clicked).toBeTruthy()

    await expect.poll(async () => (await getCapturedURLs(page)).urls, { timeout: 5000 })
      .toContain('https://example.com/plain-url?via=weblinks')
  })

  // SKIP: same orchestrator-session-dies-on-CI race as the sibling test
  // above. Tracked in backlog 726a2a3c.
  test.fixme('OSC 8 hyperlink click routes through openExternal after drawer drag', async ({ page }) => {
    await page.goto('/')
    await enablePtyCapture(page)

    const uuid = await page.evaluate(async () => {
      // @ts-expect-error wails binding
      const r = (await window.go.app.App.StartRootSession('testagent')) as { uuid: string }
      return r.uuid
    })

    const containerSelector = '.orchestrator-host .terminal-container'
    await page.waitForSelector(containerSelector, { timeout: 10000 })
    await patchBrowserOpenURL(page)
    await waitForAgentReady(page, uuid)

    await page.evaluate(async (id) => {
      // @ts-expect-error wails binding
      await window.go.app.App.WriteToSession(id, '/link https://example.com/from-testagent?via=osc8-after-drag example link drag\r')
    }, uuid)

    await expect.poll(() => readSessionReplay(page, uuid), { timeout: 10000 })
      .toContain('example link drag')

    const handle = page.locator('.orchestrator-host-resize')
    const box = await handle.boundingBox()
    if (!box) throw new Error('orchestrator-host-resize handle has no bounding box')
    const startX = box.x + box.width / 2
    const startY = box.y + box.height / 2
    await page.mouse.move(startX, startY)
    await page.mouse.down()
    for (let i = 1; i <= 10; i++) {
      await page.mouse.move(startX, startY - i * 20)
    }
    await page.mouse.up()
    await page.waitForFunction(() => {
      const v = parseInt(
        getComputedStyle(document.documentElement).getPropertyValue('--app-drawer-height'),
        10,
      )
      return Number.isFinite(v) && v > 0
    })

    const clicked = await clickLinkRow(page, containerSelector, 'example link drag', { requireOscLink: true })
    expect(clicked).toBeTruthy()

    await expect.poll(async () => (await getCapturedURLs(page)).urls, { timeout: 5000 })
      .toContain('https://example.com/from-testagent?via=osc8-after-drag')
  })

  test('plain http URL click routes through openExternal after drawer drag', async ({ page }) => {
    await page.goto('/')
    await enablePtyCapture(page)

    const uuid = await page.evaluate(async () => {
      // @ts-expect-error wails binding
      const r = (await window.go.app.App.StartRootSession('testagent')) as { uuid: string }
      return r.uuid
    })

    const containerSelector = '.orchestrator-host .terminal-container'
    await page.waitForSelector(containerSelector, { timeout: 10000 })
    await patchBrowserOpenURL(page)
    await waitForAgentReady(page, uuid)

    await page.evaluate(async (id) => {
      // @ts-expect-error wails binding
      await window.go.app.App.WriteToSession(id, 'visit https://example.com/plain-url?via=weblinks-after-drag for docs\r')
    }, uuid)

    await expect.poll(() => readSessionReplay(page, uuid), { timeout: 10000 })
      .toContain('https://example.com/plain-url?via=weblinks-after-drag')

    const handle = page.locator('.orchestrator-host-resize')
    const box = await handle.boundingBox()
    if (!box) throw new Error('orchestrator-host-resize handle has no bounding box')
    const startX = box.x + box.width / 2
    const startY = box.y + box.height / 2
    await page.mouse.move(startX, startY)
    await page.mouse.down()
    for (let i = 1; i <= 10; i++) {
      await page.mouse.move(startX, startY - i * 20)
    }
    await page.mouse.up()
    await page.waitForFunction(() => {
      const v = parseInt(
        getComputedStyle(document.documentElement).getPropertyValue('--app-drawer-height'),
        10,
      )
      return Number.isFinite(v) && v > 0
    })

    const clicked = await clickLinkRow(page, containerSelector, 'https://example.com/plain-url')
    expect(clicked).toBeTruthy()

    await expect.poll(async () => (await getCapturedURLs(page)).urls, { timeout: 5000 })
      .toContain('https://example.com/plain-url?via=weblinks-after-drag')
  })

  // SKIP: same orchestrator-session-dies-on-CI race as the sibling tests
  // above. Tracked in backlog 726a2a3c.
  test.fixme('OSC 8 hyperlink click routes through openExternal after drawer hide/show', async ({ page }) => {
    // Off-dashboard route so the orchestrator toggle button isn't
    // disabled-by-pinning (dashboard pins the drawer open). The
    // drawer starts closed on /idea/new; an initial click opens it.
    await page.goto('/#/idea/new')
    await enablePtyCapture(page)

    const uuid = await page.evaluate(async () => {
      // @ts-expect-error wails binding
      const r = (await window.go.app.App.StartRootSession('testagent')) as { uuid: string }
      return r.uuid
    })

    await page.click('button[aria-label="Orchestrator"]')
    const containerSelector = '.orchestrator-host .terminal-container'
    await page.waitForSelector(containerSelector, { timeout: 10000 })
    await patchBrowserOpenURL(page)
    await waitForAgentReady(page, uuid)

    await page.evaluate(async (id) => {
      // @ts-expect-error wails binding
      await window.go.app.App.WriteToSession(id, '/link https://example.com/from-testagent?via=osc8-after-hide-show example link hide show\r')
    }, uuid)

    await expect.poll(() => readSessionReplay(page, uuid), { timeout: 10000 })
      .toContain('example link hide show')

    await page.click('button[aria-label="Orchestrator"]')
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toHaveCount(0)
    await page.click('button[aria-label="Orchestrator"]')
    await page.waitForSelector('.orchestrator-host .terminal-container', { timeout: 10000 })
    await page.waitForSelector('.orchestrator-host .xterm-screen', { timeout: 5000 })

    const clicked = await clickLinkRow(page, containerSelector, 'example link hide show', { requireOscLink: true })
    expect(clicked).toBeTruthy()

    await expect.poll(async () => (await getCapturedURLs(page)).urls, { timeout: 5000 })
      .toContain('https://example.com/from-testagent?via=osc8-after-hide-show')
  })

  test('plain http URL click routes through openExternal after drawer hide/show', async ({ page }) => {
    // Off-dashboard route — see sibling hide/show test for why.
    await page.goto('/#/idea/new')
    await enablePtyCapture(page)

    const uuid = await page.evaluate(async () => {
      // @ts-expect-error wails binding
      const r = (await window.go.app.App.StartRootSession('testagent')) as { uuid: string }
      return r.uuid
    })

    await page.click('button[aria-label="Orchestrator"]')
    const containerSelector = '.orchestrator-host .terminal-container'
    await page.waitForSelector(containerSelector, { timeout: 10000 })
    await patchBrowserOpenURL(page)
    await waitForAgentReady(page, uuid)

    await page.evaluate(async (id) => {
      // @ts-expect-error wails binding
      await window.go.app.App.WriteToSession(id, 'visit https://example.com/plain-url?via=weblinks-after-hide-show for docs\r')
    }, uuid)

    await expect.poll(() => readSessionReplay(page, uuid), { timeout: 10000 })
      .toContain('https://example.com/plain-url?via=weblinks-after-hide-show')

    await page.click('button[aria-label="Orchestrator"]')
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toHaveCount(0)
    await page.click('button[aria-label="Orchestrator"]')
    await page.waitForSelector('.orchestrator-host .terminal-container', { timeout: 10000 })
    await page.waitForSelector('.orchestrator-host .xterm-screen', { timeout: 5000 })

    const clicked = await clickLinkRow(page, containerSelector, 'https://example.com/plain-url')
    expect(clicked).toBeTruthy()

    await expect.poll(async () => (await getCapturedURLs(page)).urls, { timeout: 5000 })
      .toContain('https://example.com/plain-url?via=weblinks-after-hide-show')
  })
})
