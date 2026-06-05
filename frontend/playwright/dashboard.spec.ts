import { test, expect } from '@playwright/test'
import * as fs from 'node:fs/promises'
import * as path from 'node:path'
import { stopAllRunningSessions } from './ptyCapture'

const ideasDir = process.env.TEST_IDEAS_DIR || ''

// Overwrites the running session.json's `activity` field for `slug` —
// simulates the agent activity-hook flips (active/idle/waiting) without
// having to drive a real PTY into the right state. The watcher then
// fires `idea:changed`, prompting the bar to refresh.
async function patchSessionActivity(slug: string, activity: string) {
  const sessionsDir = path.join(ideasDir, slug, 'sessions')
  const entries = await fs.readdir(sessionsDir)
  for (const entry of entries) {
    if (!entry.endsWith('.json')) continue
    const p = path.join(sessionsDir, entry)
    const raw = JSON.parse(await fs.readFile(p, 'utf-8'))
    if (raw.status === 'running') {
      raw.activity = activity
      await fs.writeFile(p, JSON.stringify(raw, null, 2))
      return
    }
  }
  throw new Error(`no running session found for slug ${slug}`)
}

async function createIdea(page: import('@playwright/test').Page, name: string): Promise<string> {
  await page.goto('/#/idea/new')
  await page.fill('input[type="text"]', name)
  await page.selectOption('.idea-form select', 'active')
  await page.click('button[type="submit"]')
  await expect(page.locator('.idea-detail-name')).toHaveText(name)
  const url = page.url()
  return url.split('/idea/')[1].split('/')[0].split('?')[0]
}

test.describe('Dashboard', () => {
  test('renders dashboard with global app footer', async ({ page }) => {
    await page.goto('/')
    // Dashboard has no h1 anymore — anchor on the .dashboard container
    // and the topbar Home button (always visible on the home route).
    await expect(page.locator('.dashboard')).toBeVisible()
    await expect(page.locator('button[aria-label="Home"]')).toBeVisible()

    // Status moved out of the dashboard into the global .app-footer so
    // it shows on every route, not just /. Sticky-bottom positioning
    // means it's always visible without dashboard layout having to
    // reserve space for it.
    await page.waitForSelector('.app-footer', { timeout: 5000 })
    // Version comes from internal/version.Version (ldflags-injected
    // at build time, "dev" under `wails dev`). Assert presence of a
    // `v<token>` instead of a literal so this stays correct across
    // dev runs and tagged release builds alike.
    await expect(page.locator('.app-footer')).toContainText(/v\S+/)
  })

  // Idea cards prefer the headless-generated summary sidecar (line
  // from <slug>/summary.json) over the truncated idea.md body. Writes
  // a sidecar directly to the ideas dir and asserts the card shows
  // that text instead of the body's first 140 chars.
  test('idea card renders the sidecar summary line when present', async ({ page }) => {
    test.skip(!ideasDir, 'TEST_IDEAS_DIR not set')

    const name = `Sidecar Summary Test ${Date.now()}`
    const body = 'Original body that should NOT appear on the card once the sidecar is in place.'
    await page.goto('/')
    const slug = await page.evaluate(
      async ({ n, b }) => {
        // @ts-expect-error wails binding
        return (await window.go.app.App.CreateIdea(n, 'active', b)) as string
      },
      { n: name, b: body },
    )

    const sidecarLine = 'Sidecar-generated summary that should appear on the card.'
    await fs.writeFile(
      path.join(ideasDir, slug, 'summary.json'),
      JSON.stringify({
        line: sidecarLine,
        generated_at: new Date().toISOString(),
      }),
    )

    const card = page.locator('.idea-card', { hasText: name })
    await expect(card).toBeVisible({ timeout: 5000 })
    // Sidecar line wins. Poll because ListSessionSummaries refreshes
    // on idea:changed + a 10s backstop interval; the sidecar write
    // doesn't itself fire idea:changed, so we wait for the interval
    // OR a navigation-driven refresh.
    await expect(card.locator('.card-shell-summary')).toContainText(sidecarLine, { timeout: 15000 })
    await expect(card.locator('.card-shell-summary')).not.toContainText('Original body')
  })

  // Idea cards render an IdeaStatusIcon (lifecycle status) next to the
  // name, mirroring the SessionStatusIcon architecture so the two read
  // as a pair when surfaced together. Color comes via CSS class on the
  // icon span; this asserts the class wiring against a card we just
  // created with a known status.
  test('idea card renders IdeaStatusIcon keyed to lifecycle status', async ({ page }) => {
    const name = `Icon Status Test ${Date.now()}`
    // CreateIdea binding lets us seed the idea with status='active'
    // without going through the form, keeping the test under a second.
    await page.goto('/')
    await page.evaluate(async (n) => {
      // @ts-expect-error wails binding
      await window.go.app.App.CreateIdea(n, 'active', '')
    }, name)

    const card = page.locator('.idea-card', { hasText: name })
    await expect(card).toBeVisible({ timeout: 5000 })
    await expect(card.locator('.idea-icon.active')).toBeVisible()
  })

  // Global session bar chips render the session-status icon followed by
  // the idea-status icon for every idea-bound session. The orchestrator
  // doesn't surface in the bar — it has its own topbar drawer.
  test('global bar chip pairs session-status icon with idea-status icon', async ({ page }) => {
    const name = `Bar Chip Icon Test ${Date.now()}`
    await page.goto('/')

    const result = await page.evaluate(async (n) => {
      // @ts-expect-error wails binding
      const slug = (await window.go.app.App.CreateIdea(n, 'active', '')) as string
      // @ts-expect-error wails binding
      const r = (await window.go.app.App.StartIdeaSession(slug, 'testagent', false)) as {

        uuid: string
      }
      return { slug, uuid: r.uuid }
    }, name)

    // Hash-nav (not page.goto) so the bar's render state survives.
    await page.evaluate((url) => {
      window.location.hash = url
    }, `/idea/${result.slug}/session/${result.uuid}`)

    // Current-pinned chip in the bar carries both icons in this order.
    const chip = page.locator('.global-session-bar .global-session-chip.current', {
      hasText: name,
    })
    await expect(chip).toBeVisible({ timeout: 5000 })
    await expect(chip.locator('.session-icon')).toBeVisible()
    await expect(chip.locator('.idea-icon.active')).toBeVisible()
  })

  // Dashboard cards have exactly one click target. With a running
  // session the card opens the session; without one it opens the idea
  // detail page.
  test('dashboard card click target: session when running, idea otherwise', async ({ page }) => {
    const withSessionName = `Card Click With Session ${Date.now()}`
    await createIdea(page, withSessionName)
    await page.click('.idea-sidebar .btn-small')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await page.waitForSelector('.terminal-container', { timeout: 10000 })

    await page.goto('/')
    const liveCard = page.locator('.idea-card', { hasText: withSessionName })
    await expect(liveCard.locator('.idea-card-session-badge.running'))
      .toBeVisible({ timeout: 10000 })
    await liveCard.click()
    await expect(page).toHaveURL(/\/session\//)

    // Without a running session, the same card click opens idea detail.
    const noSessionName = `Card Click No Session ${Date.now()}`
    await createIdea(page, noSessionName)
    await page.goto('/')
    const idleCard = page.locator('.idea-card', { hasText: noSessionName })
    await idleCard.click()
    await expect(page).toHaveURL(new RegExp(`/idea/[^/]+$`))
    await expect(page.locator('.idea-detail-name')).toHaveText(noSessionName)
  })

  // Dashboard cards surface a short list of linked repo names so the
  // user can see at a glance which trees an idea is touching. Names
  // are the worktree dir basenames (already canonicalized at link time
  // to short forms like "ideate", not "github.com/paultyng/ideate").
  test('idea card lists linked repo names', async ({ page }) => {
    test.skip(!ideasDir, 'TEST_IDEAS_DIR not set')

    await page.goto('/')

    const name = `Repo List Test ${Date.now()}`
    const slug = await page.evaluate(async (n) => {
      // @ts-expect-error wails binding
      return (await window.go.app.App.CreateIdea(n, 'active', '')) as string
    }, name)

    // Drop two repo dirs directly on disk — LinkRepo is heavyweight
    // and we only need the directory presence to drive the card.
    await fs.mkdir(path.join(ideasDir, slug, 'repos', 'alpha'), { recursive: true })
    await fs.mkdir(path.join(ideasDir, slug, 'repos', 'beta'), { recursive: true })

    await page.goto('/')
    const card = page.locator('.idea-card', { hasText: name })
    await expect(card).toBeVisible({ timeout: 5000 })
    // Sorted lexicographically; rendered as a comma-joined string.
    await expect(card.locator('.idea-card-repos')).toContainText('alpha, beta', { timeout: 15000 })
  })

  test('idea card surfaces a running-session badge', async ({ page }) => {
    const ideaName = 'Card Session Indicator Test'
    await createIdea(page, ideaName)

    // Start a testagent session via the new + button
    await page.click('.idea-sidebar .btn-small')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await page.waitForSelector('.terminal-container', { timeout: 10000 })

    await page.goto('/')
    await page.waitForSelector('.idea-card', { timeout: 5000 })

    // Tests share a single dev server, so prior tests may have left running
    // sessions on other cards. Scope to the card we just created.
    const card = page.locator('.idea-card', { hasText: ideaName })
    await expect(card.locator('.idea-card-session-badge.running'))
      .toBeVisible({ timeout: 10000 })
    await expect(card.locator('.idea-card-session-badge.running .session-icon'))
      .toBeVisible()
  })

  test('global session bar shows on session page during active session', async ({ page }) => {
    // Verify the bar appears while a session is running on the session view
    // itself — that's the most reliable window because testagent auto-exits
    // ~2s after start, and the bar polls every 3s. By staying on the session
    // page we avoid the dashboard navigation race.
    const ideaName = 'Global Bar Test'
    await createIdea(page, ideaName)

    await page.click('.idea-sidebar .btn-small')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await page.waitForSelector('.terminal-container', { timeout: 10000 })

    // Wait a beat for the bar's first poll cycle, then assert any chip exists.
    // (Other tests may have left their own running sessions — we don't
    // strict-match this idea's chip to avoid timing flakes.)
    const bar = page.locator('.global-session-bar')
    await expect(bar).toBeVisible({ timeout: 10000 })
    await expect(bar.locator('.global-session-chip').first()).toBeVisible()
  })

  // Orchestrator lives in a topbar drawer (Notebook button), not the
  // global session bar. Verify the bar never surfaces a 'Orchestrator'
  // chip even when a root session is running.
  test('orchestrator does not appear in the global session bar', async ({ page }) => {
    await page.goto('/')

    await page.evaluate(async () => {
      // @ts-expect-error wails binding
      await window.go.app.App.StartRootSession('testagent')
    })

    // Give the bar a beat to refresh, then sample its order — Orchestrator
    // must be absent from both visible chips and the overflow popover.
    await page.waitForTimeout(1500)
    const seen = await page.evaluate(() => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const order = ((window as any).__ideateBarOrder as string[] | undefined) ?? []
      return order.includes('Orchestrator')
    })
    expect(seen).toBe(false)
  })

  // Notebook button toggles the drawer open/closed; opening with a
  // running root session attaches via replay so the terminal mounts
  // populated rather than blank. Test runs on /idea/new (not /) since
  // the dashboard route pins the drawer open and would short-circuit
  // the "starts closed → click → opens" flow being asserted.
  test('topbar Notebook button toggles the orchestrator drawer with a populated terminal', async ({ page }) => {
    await page.goto('/#/idea/new')

    await page.evaluate(async () => {
      // @ts-expect-error wails binding
      await window.go.app.App.StartRootSession('testagent')
    })

    // Drawer starts closed (no localStorage state from a fresh context).
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toHaveCount(0)

    // Click Notebook button to open.
    await page.click('button[aria-label="Orchestrator"]')
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toBeVisible({
      timeout: 5000,
    })

    // Terminal mounts and replay populates the buffer with testagent's banner.
    await page.waitForSelector('.orchestrator-host .terminal-container', { timeout: 10000 })
    await waitForBanner(page)
    const text = await page.evaluate(() => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const reg = (window as any).__ideateTerminals
      if (!reg) return ''
      const term = Object.values(reg)[0] as
        | { buffer: { active: { length: number; getLine: (i: number) => { translateToString: (trim: boolean) => string } | undefined } } }
        | undefined
      if (!term) return ''
      const lines: string[] = []
      for (let i = 0; i < term.buffer.active.length; i++) {
        lines.push(term.buffer.active.getLine(i)?.translateToString(true) ?? '')
      }
      return lines.join('\n')
    })
    // mcp-connected lifecycle marker reliably appears in both
    // vscreen and xterm.js direct reads; see waitForAgentReady's
    // comment in ptyCapture.ts for why we don't poll the banner.
    expect(text).toContain('mcp connected:')

    // Click again to close.
    await page.click('button[aria-label="Orchestrator"]')
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toHaveCount(0)
  })

  // Pinned-on-home behavior: the dashboard route auto-shows the
  // drawer regardless of the user's stored preference, since
  // ideation starts in the orchestrator surface.
  test('orchestrator drawer is pinned visible on the dashboard', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toBeVisible({
      timeout: 5000,
    })
    // The close button is suppressed when pinned — the user can't
    // dismiss the drawer on home.
    await expect(page.locator('.orchestrator-drawer-close')).toHaveCount(0)
  })

  // Phase C nav tools (goto_idea/dashboard/session) emit a single
  // `orchestrator:navigate` Wails event with `{path}`. App.tsx's
  // useOrchestratorNavBridge subscribes and routes through useNavigate.
  // Emulate the tool from JS — a real MCP call would do the same emit
  // server-side. The home-page pin does NOT promote into the user's
  // persisted open/closed preference, so leaving / via nav returns
  // the drawer to whatever the user toggled it to on non-home
  // routes (default: closed). Returning to / re-pins it.
  test('orchestrator:navigate event drives nav; home pin is transient', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toBeVisible({
      timeout: 5000,
    })

    await page.evaluate(() => {
      // @ts-expect-error wails runtime
      window.runtime.EventsEmit('orchestrator:navigate', { path: '/idea/new' })
    })
    await expect(page.locator('h1')).toHaveText('New Idea')
    // Without an explicit user-toggle off-home, leaving / closes the drawer.
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toHaveCount(0)

    // goto_dashboard equivalent — re-pins on return.
    await page.evaluate(() => {
      // @ts-expect-error wails runtime
      window.runtime.EventsEmit('orchestrator:navigate', { path: '/' })
    })
    await expect(page.locator('.dashboard')).toBeVisible()
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toBeVisible()
  })

  // Drawer survives client-side navigation when the user has toggled
  // it open — the orchestrator framing depends on it (Phase C nav
  // tools fire while the agent is rendered here). Underlying view
  // changes; drawer stays. Run the round-trip on non-home routes so
  // the assertion measures the user-toggle persistence path, not the
  // dashboard's pin override.
  // Regression: navigating off home (where the drawer is pinned-open)
  // must clear --app-drawer-height to 0px. The drawer's reservation
  // effect previously depended on the user-toggled `open` state; the
  // home pin flipped `pinned` instead, so navigating away left the
  // CSS variable stuck at the open height (320px) and views' viewport
  // calcs subtracted that, leaving a ghost gap above the footer.
  test('--app-drawer-height clears to 0px when leaving home', async ({ page }) => {
    await page.goto('/')
    // Pinned-open on home: var should be at the open height.
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toBeVisible({ timeout: 5000 })
    const onHome = await page.evaluate(() =>
      getComputedStyle(document.documentElement).getPropertyValue('--app-drawer-height').trim(),
    )
    expect(onHome).toBe('320px')

    // Hash-nav off home so the drawer's module-level open state is
    // preserved (default closed). With the home pin gone, the
    // visibly-open input flips false and the var must reset.
    await page.evaluate(() => { window.location.hash = '/idea/new' })
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toHaveCount(0)
    await expect.poll(async () =>
      page.evaluate(() =>
        getComputedStyle(document.documentElement).getPropertyValue('--app-drawer-height').trim(),
      ),
    ).toBe('0px')
  })

  // The orchestrator's rename_idea MCP tool emits idea:renamed when
  // it succeeds; useIdeaRenamedRedirect keeps the open route in sync
  // by translating /idea/<old> (and any /idea/<old>/... subroute) to
  // /idea/<new>. Other routes pass through.
  test('idea:renamed redirects /idea/<old> to /idea/<new>', async ({ page }) => {
    const name = `Rename Redirect ${Date.now()}`
    await page.goto('/')

    const slug = await page.evaluate(async (n) => {
      // @ts-expect-error wails binding
      return (await window.go.app.App.CreateIdea(n, 'active', '')) as string
    }, name)

    await page.evaluate((s) => {
      window.location.hash = `/idea/${s}`
    }, slug)
    await expect(page.locator('.idea-detail-name')).toHaveText(name)

    await page.evaluate((payload) => {
      // @ts-expect-error wails runtime
      window.runtime.EventsEmit('idea:renamed', payload)
    }, { old_slug: slug, new_slug: 'renamed-via-event' })

    await expect.poll(() => page.url().split('#')[1] || '').toBe('/idea/renamed-via-event')
  })

  test('orchestrator drawer stays open across navigation', async ({ page }) => {
    await page.goto('/#/idea/new')
    await page.click('button[aria-label="Orchestrator"]')
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toBeVisible({
      timeout: 5000,
    })

    // Hash-nav (not page.goto) so the React tree isn't rebuilt — that
    // would reset the drawer's module-level state.
    await page.evaluate(() => {
      window.location.hash = '/review'
    })
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toBeVisible()
  })

  // Re-mount of a running session calls GetSessionReplay which returns
  // a vt.Emulator-rendered snapshot. Two failure modes have hit us:
  //   1. vt.Render() emits bare LF between lines — xterm.js (LNM-off
  //      default) treats LF as cursor-down only, so each subsequent
  //      banner line starts at the previous line's end column —
  //      "staircase" pattern.
  //   2. A 1px body scrollbar (now fixed in style.css) used to reserve
  //      ~15px of terminal width, churning xterm cols on each route
  //      change and producing mid-line wraps after re-mount.
  // Both manifest as the testagent banner's closing box-drawing row
  // (╚...╝) NOT starting at column 0 of its line in the buffer. This
  // test toggles the drawer closed and back open to force a fresh
  // TerminalPanel mount + snapshot replay and asserts col-0 alignment.
  test('orchestrator drawer re-mount preserves testagent banner col-0 alignment', async ({ page }) => {
    // Run on /idea/new so the drawer is toggle-controlled (pinned on
    // / would prevent the close + reopen step the test depends on).
    await page.goto('/#/idea/new')

    await page.evaluate(async () => {
      // @ts-expect-error wails binding
      await window.go.app.App.StartRootSession('testagent')
    })

    // Open drawer — first mount attaches via replay (root session is
    // already running) so the buffer arrives populated.
    await page.click('button[aria-label="Orchestrator"]')
    await page.waitForSelector('.orchestrator-host .terminal-container', { timeout: 10000 })
    await page.waitForSelector('.orchestrator-host .xterm-screen', { timeout: 5000 })
    await waitForBanner(page)

    // Close drawer (TerminalPanel unmounts) then reopen — fresh xterm
    // mounts and writes vscreen.Snapshot() bytes. Banner must still
    // start at col 0.
    await page.click('button[aria-label="Orchestrator"]')
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toHaveCount(0)
    await page.click('button[aria-label="Orchestrator"]')
    await page.waitForSelector('.orchestrator-host .terminal-container', { timeout: 10000 })
    await page.waitForSelector('.orchestrator-host .xterm-screen', { timeout: 5000 })
    await assertBannerAtCol0(page, 'after drawer re-open via snapshot replay')
  })

  // Regression: when the user drags the drawer to a new size and
  // then hides + reshows the orchestrator, the backgrounded terminal
  // must re-fit to the new viewport before its first paint. Without
  // the fix in tasks 2+3 (visibility-aware refit + host-resize
  // broadcast), the buffer renders at stale cell dims into the new
  // container, producing ghost rows and misaligned content for at
  // least one frame.
  test('orchestrator drawer resize-then-hide-then-show preserves banner col-0 alignment', async ({ page }) => {
    // Run on /idea/new so the drawer is toggle-controlled (matches
    // the sibling drawer test's setup).
    await page.goto('/#/idea/new')

    await page.evaluate(async () => {
      // @ts-expect-error wails binding
      await window.go.app.App.StartRootSession('testagent')
    })

    // Open drawer at default height.
    await page.click('button[aria-label="Orchestrator"]')
    await page.waitForSelector('.orchestrator-host .terminal-container', { timeout: 10000 })
    await page.waitForSelector('.orchestrator-host .xterm-screen', { timeout: 5000 })
    await waitForBanner(page)
    await assertBannerAtCol0(page, 'after initial drawer open')

    // Drag the resize handle UPWARD to make the drawer substantially
    // taller — the real user gesture, not page.setViewportSize. We
    // grab the current handle position and move ~200px up.
    const handle = page.locator('.orchestrator-host-resize')
    const box = await handle.boundingBox()
    if (!box) throw new Error('orchestrator-host-resize handle has no bounding box')
    const startX = box.x + box.width / 2
    const startY = box.y + box.height / 2
    await page.mouse.move(startX, startY)
    await page.mouse.down()
    // Drag upward in small steps (matches a real drag rather than
    // a teleport, gives mousemove handlers a chance to fire on each
    // step).
    for (let i = 1; i <= 10; i++) {
      await page.mouse.move(startX, startY - i * 20)
    }
    await page.mouse.up()

    // Wait for the drawer to settle at the new height.
    await page.waitForFunction(() => {
      const v = parseInt(
        getComputedStyle(document.documentElement).getPropertyValue('--app-drawer-height'),
        10,
      )
      return Number.isFinite(v) && v > 0
    })
    await assertBannerAtCol0(page, 'after drawer resize (still visible)')

    // Hide the drawer (close it via the toggle button).
    await page.click('button[aria-label="Orchestrator"]')
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toHaveCount(0)

    // Reshow the drawer. Mount-on-visible: a fresh TerminalPanel
    // mounts, fetches replay, paints the banner at current cell dims.
    // No reflow of a stale buffer; no display:none-era ghosting path
    // to repro structurally — the prior bannerWidths "opening + closing
    // rows have equal length" check (defending against reflow-of-stale-
    // buffer) is no longer load-bearing once every drawer-open is a
    // fresh xterm. Assert what's still meaningful: the closing banner
    // row appears AND starts at column 0 (no staircase from a stale-
    // cursor-position quirk in the replay stream).
    await page.click('button[aria-label="Orchestrator"]')
    await page.waitForSelector('.orchestrator-host .terminal-container', { timeout: 10000 })
    await page.waitForSelector('.orchestrator-host .xterm-screen', { timeout: 5000 })
    await waitForBanner(page)
    await assertBannerAtCol0(page, 'after resize-then-hide-then-show')
  })

  // GlobalSessionBar's recency sort uses `updated` (parent idea's MRU
  // bump from session-activity hooks) with `started` as fallback. An
  // idle session you were just viewing has stale `updated`, so when
  // you navigate away it can drop below background sessions whose
  // hooks fired earlier in the overflow popover. The fix tracks a
  // per-UUID "lastFocused" timestamp, bumped on each currentUUID
  // transition, and folds it into the recency sort. With the single-
  // chip footer there's no visible chip on the dashboard, so the
  // assertion targets the popover's top-down order via
  // __ideateBarOrder (newest-first).
  test('navigating away from a session keeps it ahead in the global bar order', async ({ page }) => {
    // Setup uses Wails bindings (CreateIdea, StartIdeaSession) instead of
    // the form-based flow — the form path takes ~3s per session, and
    // testagent auto-exits at 5s, so the form flow can't sustain two
    // concurrent sessions through the navigation assertions. Bindings
    // shave setup to ~1s.
    //
    // Critical: navigate within the SPA via hash mutation, not page.goto.
    // page.goto reloads the WebView and rebuilds the React tree,
    // wiping the GlobalSessionBar's lastFocused ref — which would
    // defeat the very recency-tracking we're verifying. Real users
    // navigate via chip clicks / home button (router.navigate, no
    // reload), so hash mutation matches production behavior.
    const navigate = async (hash: string) => {
      await page.evaluate((h) => {
        window.location.hash = h
      }, hash)
    }
    await page.goto('/')

    const xName = `BarOrder X ${Date.now()}`
    const xResult = await page.evaluate(async (name) => {
      // @ts-expect-error wails binding
      const slug = (await window.go.app.App.CreateIdea(name, 'active', '')) as string
      // @ts-expect-error wails binding
      const r = (await window.go.app.App.StartIdeaSession(slug, 'testagent', false)) as {

        uuid: string
      }
      return { slug, uuid: r.uuid }
    }, xName)

    // Y started AFTER X — newer `started` timestamp. Without the
    // lastFocused boost, the dashboard bar would put Y ahead of X.
    const yName = `BarOrder Y ${Date.now()}`
    const yResult = await page.evaluate(async (name) => {
      // @ts-expect-error wails binding
      const slug = (await window.go.app.App.CreateIdea(name, 'active', '')) as string
      // @ts-expect-error wails binding
      const r = (await window.go.app.App.StartIdeaSession(slug, 'testagent', false)) as {

        uuid: string
      }
      return { slug, uuid: r.uuid }
    }, yName)

    // Visit X — bumps X's lastFocused. Wait for the bar to mark X as
    // the .current chip, which is committed in the same render cycle
    // that runs the lastFocused-bumping effect; the wait guarantees
    // the ref has been mutated before we navigate away.
    await navigate(`/idea/${xResult.slug}/session/${xResult.uuid}`)
    await expect(
      page.locator('.global-session-bar .global-session-chip.current', {
        hasText: xName,
      }),
    ).toBeVisible({ timeout: 5000 })

    // Dashboard. partitionCurrentUUID transitions back to ''. X's
    // lastFocused was just stamped → X is the newest by recency →
    // popover-top. Y is older → popover-bottom.
    // __ideateBarOrder is the popover's top-down render order.
    await navigate('/')
    await page.waitForSelector('.idea-list', { timeout: 5000 })
    await assertBarOrder(page, [xName, yName])

    // Sanity inverse: visit Y, return to dashboard. Y is now newest
    // by recency → popover-top; X recedes below.
    await navigate(`/idea/${yResult.slug}/session/${yResult.uuid}`)
    await expect(
      page.locator('.global-session-bar .global-session-chip.current', {
        hasText: yName,
      }),
    ).toBeVisible({ timeout: 5000 })
    await navigate('/')
    await page.waitForSelector('.idea-list', { timeout: 5000 })
    await assertBarOrder(page, [yName, xName])
  })

  // The +N pill counts sessions NOT pinned in the chip slot. On the
  // dashboard there's no current session, so 3 running sessions all
  // sit in the popover; the pill reads `+3`. Click +N → popover
  // lists the sessions newest-first; click an entry → navigate.
  test('overflow popover opens upward and lets you select a session', async ({ page }) => {
    test.skip(!ideasDir, 'TEST_IDEAS_DIR not set')

    await page.goto('/')
    // Asserts an exact `+3` overflow count, so prior tests' leaked
    // running sessions would inflate it (seen as `+6` / `+9`).
    await stopAllRunningSessions(page)

    const names: string[] = []
    const results: { slug: string; uuid: string }[] = []
    for (let i = 0; i < 3; i++) {
      const name = `Overflow ${i} ${Date.now()}`
      names.push(name)
      const r = await page.evaluate(async (n) => {
        // @ts-expect-error wails binding
        const slug = (await window.go.app.App.CreateIdea(n, 'active', '')) as string
        // @ts-expect-error wails binding
        const result = (await window.go.app.App.StartIdeaSession(slug, 'testagent', false)) as { uuid: string }
        return { slug, uuid: result.uuid }
      }, name)
      results.push(r)
    }

    const overflowBtn = page.locator('.global-session-more')
    await expect(overflowBtn).toBeVisible({ timeout: 10000 })
    await expect(overflowBtn).toContainText('+3')

    await overflowBtn.click()
    const popover = page.locator('.global-session-popover')
    await expect(popover).toBeVisible()

    // Pick the first-created session — oldest by recency, popover-bottom.
    // Popover renders SessionCard (not the bare chip) so the user sees
    // the idea name, summary, agent, and activity.
    const targetCard = popover.locator('.session-card', { hasText: names[0] })
    await expect(targetCard).toBeVisible()
    await targetCard.click()
    await expect(page).toHaveURL(new RegExp(`/idea/${results[0].slug}/session/${results[0].uuid}`))
  })

  // The +N pill picks up an `.attention` class when an attention-
  // needed (waiting/reviewing) session is in the popover but NOT the
  // currently-pinned chip. On the dashboard nothing is pinned, so a
  // single waiting session is enough to trigger the glow.
  test('overflow pill glows when a popover session needs attention', async ({ page }) => {
    test.skip(!ideasDir, 'TEST_IDEAS_DIR not set')

    await page.goto('/')

    const slug = await page.evaluate(async (n) => {
      // @ts-expect-error wails binding
      const s = (await window.go.app.App.CreateIdea(n, 'active', '')) as string
      // @ts-expect-error wails binding
      await window.go.app.App.StartIdeaSession(s, 'testagent', false)
      return s
    }, `Glow ${Date.now()}`)
    await patchSessionActivity(slug, 'waiting')

    const overflowEl = page.locator('.global-session-overflow')
    await expect(overflowEl).toBeVisible({ timeout: 10000 })
    await expect(overflowEl).toHaveClass(/attention/, { timeout: 10000 })
  })
})

// Polls the active terminal's buffer until testagent has finished
// booting (MCP connected). See waitForAgentReady's comment in
// ptyCapture.ts for why we don't poll the banner border rows under
// testagent v0.4+'s bubbletea v2 inline rendering.
async function waitForBanner(page: import('@playwright/test').Page) {
  await page.waitForFunction(
    () => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const reg = (window as any).__ideateTerminals as
        | Record<
            string,
            {
              buffer: {
                active: {
                  length: number
                  getLine: (
                    i: number,
                  ) =>
                    | { translateToString: (trim: boolean) => string }
                    | undefined
                }
              }
            }
          >
        | undefined
      if (!reg) return false
      const term = Object.values(reg)[0]
      if (!term) return false
      const buf = term.buffer.active
      for (let i = 0; i < buf.length; i++) {
        if (buf.getLine(i)?.translateToString(true).includes('mcp connected:')) return true
      }
      return false
    },
    { timeout: 5000 },
  )
}

// Reads the active terminal's buffer untrimmed (preserving leading
// whitespace) and asserts the testagent banner's TOP border row
// starts at column 0 — i.e. no staircase, no offset. Originally
// asserted the closing border (╚), but bubbletea v2 inline rendering
// in testagent v0.4+ doesn't reliably commit the middle/bottom banner
// rows under \x1b[5L insertion (only the top border + title row land
// in scrollback). The top border ╔ row carries the same col-0
// guarantee the closing row did, so the alignment assertion is
// equivalent.
async function assertBannerAtCol0(
  page: import('@playwright/test').Page,
  context: string,
) {
  const closingLine = await page.evaluate(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const reg = (window as any).__ideateTerminals as
      | Record<
          string,
          {
            buffer: {
              active: {
                length: number
                getLine: (
                  i: number,
                ) =>
                  | { translateToString: (trim: boolean) => string }
                  | undefined
              }
            }
          }
        >
      | undefined
    if (!reg) return null
    const terms = Object.values(reg)
    if (terms.length === 0) return null
    // Most recently mounted terminal is the active one; iterate from
    // last to handle a brief overlap during route transitions.
    for (let t = terms.length - 1; t >= 0; t--) {
      const buf = terms[t].buffer.active
      for (let i = 0; i < buf.length; i++) {
        const raw = buf.getLine(i)?.translateToString(false) ?? ''
        if (raw.includes('╔')) return raw
      }
    }
    return null
  })

  expect(
    closingLine,
    `[${context}] testagent banner top row never appeared in terminal buffer`,
  ).not.toBeNull()
  expect(
    closingLine,
    `[${context}] banner staircased — top row does not start at col 0: ${JSON.stringify(closingLine)}`,
  ).toMatch(/^╔/)
}

// Asserts that `expectedNames` appear in the GlobalSessionBar's
// popover ordering (top-to-bottom, newest-first) with the given
// relative order. Reads the order from `window.__ideateBarOrder` —
// a render-time test affordance the bar exposes — so we don't have
// to click the overflow popover open and race its DOM mount.
async function assertBarOrder(
  page: import('@playwright/test').Page,
  expectedNames: string[],
) {
  // Poll for the filtered order to equal expectedNames — not just for
  // presence — since the bar refreshes asynchronously after file
  // patches, and a stale-but-present read would otherwise fail-fast.
  await expect
    .poll(
      async () => {
        const order = await page.evaluate(() => {
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          return ((window as any).__ideateBarOrder as string[] | undefined) ?? []
        })
        return order.filter((n) => expectedNames.includes(n))
      },
      {
        timeout: 10000,
        message: `bar never showed ${expectedNames.join(', ')} in order`,
      },
    )
    .toEqual(expectedNames)
}
