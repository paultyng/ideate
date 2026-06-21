import { test, expect } from '@playwright/test'
import * as fs from 'node:fs/promises'
import * as path from 'node:path'
import { stopAllRunningSessions } from './ptyCapture'

const ideasDir = process.env.TEST_IDEAS_DIR || ''

// Cmd+K (or Ctrl+K on Linux/Windows) opens a modal palette that
// lists every non-archived idea, fuzzy-matches against the idea
// name as the user types, and follows the dashboard's click rule
// on selection (running session if one exists, idea detail
// otherwise).

async function createIdea(page: import('@playwright/test').Page, name: string): Promise<string> {
  await page.goto('/#/idea/new')
  await page.fill('input[type="text"]', name)
  await page.selectOption('.idea-form select', 'active')
  await page.click('button[type="submit"]')
  await expect(page.locator('.idea-detail-name')).toHaveText(name)
  const url = page.url()
  return url.split('/idea/')[1].split('/')[0].split('?')[0]
}

async function openPalette(page: import('@playwright/test').Page) {
  // Keyboard shortcut works regardless of focus; press it on body.
  await page.locator('body').press('Meta+k')
  await expect(page.locator('[data-testid="command-palette"]')).toBeVisible({ timeout: 2000 })
}

test.describe('Command palette', () => {
  test.afterEach(async ({ page }) => {
    await stopAllRunningSessions(page)
  })

  // Regression: first Cmd+K with focus inside an xterm terminal
  // flashed the palette then immediately closed it. Subsequent
  // presses worked because focus had shifted off the terminal.
  // Cause: window-level capture listener AND TerminalPanel's
  // attachCustomKeyEventHandler both fired, toggling state twice.
  test('Cmd+K opens the palette when the orchestrator terminal is focused', async ({ page }) => {
    await page.goto('/')

    await page.evaluate(async () => {
      // @ts-expect-error wails binding
      await window.go.app.App.StartRootSession('testagent')
    })

    // Drawer is pinned on home; wait for the host terminal to mount.
    await page.waitForSelector('.orchestrator-host .terminal-container', { timeout: 10000 })

    // Click into the terminal so xterm's helper textarea owns focus.
    await page.locator('.orchestrator-host .xterm-screen').click()

    // Single Cmd+K press should leave the palette open. Before the
    // fix, both the window capture listener and the terminal's
    // custom-key handler fired, toggling state twice and closing
    // the palette mid-frame.
    await page.keyboard.press('Meta+k')
    await expect(page.locator('[data-testid="command-palette"]')).toBeVisible({ timeout: 2000 })
  })

  // Same regression check for the per-idea session terminal.
  test('Cmd+K opens the palette when the idea-session terminal is focused', async ({ page }) => {
    const name = `Palette Idea Term ${Date.now()}`
    await createIdea(page, name)

    await page.click('.idea-sidebar .btn-small')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await page.waitForSelector('.terminal-container', { timeout: 10000 })

    // Focus xterm's helper textarea.
    await page.locator('.terminal-container .xterm-screen').click()

    await page.keyboard.press('Meta+k')
    await expect(page.locator('[data-testid="command-palette"]')).toBeVisible({ timeout: 2000 })
  })

  test('Cmd+K opens; Esc closes', async ({ page }) => {
    await createIdea(page, `Palette Open ${Date.now()}`)

    await openPalette(page)
    await page.locator('body').press('Escape')
    await expect(page.locator('[data-testid="command-palette"]')).toHaveCount(0)
  })

  test('typing fuzzy-matches idea names and Enter navigates', async ({ page }) => {
    const target = `Palette Target ${Date.now()}`
    const noise = `Different Subject ${Date.now()}`
    await createIdea(page, noise)
    await createIdea(page, target)

    await page.goto('/')
    await openPalette(page)
    await page.locator('.command-palette-input').fill('palette tar')

    const rows = page.locator('.command-palette-list .session-card')
    await expect(rows.first()).toContainText(target)
    await page.keyboard.press('Enter')
    await expect(page.locator('.idea-detail-name')).toHaveText(target)
  })

  test('empty query lists ideas in MRU order', async ({ page }) => {
    // Visit A then B. With nothing typed, the palette should list
    // B above A (most recently focused) — both below the pinned
    // orchestrator entry. The filter strips orchestrator out so
    // we only assert the relative MRU order between A and B.
    const aName = `Palette MRU A ${Date.now()}`
    const bName = `Palette MRU B ${Date.now()}`
    await createIdea(page, aName)
    await createIdea(page, bName) // most recent visit

    await page.goto('/')
    await openPalette(page)

    const names = await page.locator('.command-palette-list .session-card-name')
      .allInnerTexts()
    const filtered = names.filter((n) => n === aName || n === bName)
    expect(filtered).toEqual([bName, aName])
  })

  // Empty-query view always pins the orchestrator entry first,
  // dashboard second, so the user can land on either with Cmd+K +
  // arrow + Enter without typing.
  test('orchestrator and dashboard are pinned at the top of the empty-query view', async ({ page }) => {
    await createIdea(page, `Palette Pin ${Date.now()}`)

    await page.goto('/#/idea/new') // Start somewhere other than the dashboard so we can see
    await openPalette(page)

    const topTwo = await page.locator('.command-palette-list .session-card-name')
      .allInnerTexts()
    expect(topTwo.slice(0, 2)).toEqual(['Orchestrator', 'Dashboard'])
  })

  // Selecting Orchestrator drops the drawer down without
  // navigating. Focus-into-terminal behavior is intentionally not
  // covered here — it depends on a live testagent session and the
  // OrchestratorContext's probe completing, which both make the
  // test flaky against tight timeouts. Manual verification:
  // start a session, navigate off /, Cmd+K, Enter → cursor blinks
  // in the orchestrator terminal.
  test('selecting Orchestrator opens the drawer with no navigation', async ({ page }) => {
    // Start off-dashboard so the drawer is closed (not pinned).
    await page.goto('/#/idea/new')
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toHaveCount(0)

    await openPalette(page)
    // First row is orchestrator; Enter activates.
    await page.keyboard.press('Enter')

    // Stayed on /idea/new — no nav.
    await expect(page).toHaveURL(/#\/idea\/new$/)
    // Drawer dropped down.
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toBeVisible({ timeout: 2000 })
  })

  // Regression: after PR #93 mount-on-visible, picking Orchestrator from the
  // palette dropped the drawer but the user had to click into the terminal
  // before keystrokes registered — the helper textarea didn't exist when
  // the palette's unmount-microtask fired, so the focus() call missed.
  // The fix polls rAF for the helper to mount before focusing.
  test('selecting Orchestrator focuses the terminal helper textarea', async ({ page }) => {
    await page.goto('/')
    // OrchestratorHost only mounts when a root session exists.
    await page.evaluate(async () => {
      // @ts-expect-error wails binding
      await window.go.app.App.StartRootSession('testagent')
    })
    // Navigate off-dashboard so the drawer goes from closed → opened
    // via the palette path (on / the drawer is pinned and the test
    // doesn't exercise the mount-on-visible race).
    await page.goto('/#/idea/new')
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toHaveCount(0)

    await openPalette(page)
    await page.keyboard.press('Enter')

    // Drawer appears.
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toBeVisible({ timeout: 2000 })

    // After mount + focus-transfer, document.activeElement is the helper
    // textarea inside the orchestrator host. Poll briefly — focus lands
    // within ~10 rAF frames per the fix's bounded retry.
    await expect.poll(async () => await page.evaluate(() => {
      const helper = document.querySelector<HTMLTextAreaElement>(
        '.orchestrator-host .xterm-helper-textarea',
      )
      return document.activeElement === helper
    }), { timeout: 2000 }).toBe(true)
  })

  // Dashboard pin navigates to / (the existing browse surface).
  test('selecting Dashboard navigates to /', async ({ page }) => {
    await createIdea(page, `Palette Dash ${Date.now()}`)

    await openPalette(page)
    // Orchestrator first, Dashboard second — ArrowDown once.
    await page.keyboard.press('ArrowDown')
    await page.keyboard.press('Enter')
    await expect(page).toHaveURL(/#\/$/)
  })

  // Commands only surface when typing — empty-query view hides them
  // so the default list stays focused on navigation targets.
  test('commands appear only when typing matches', async ({ page }) => {
    await createIdea(page, `Palette Cmd Hide ${Date.now()}`)

    await page.goto('/')
    await openPalette(page)
    await expect(page.locator('.command-palette-command')).toHaveCount(0)

    await page.locator('.command-palette-input').fill('new idea')
    await expect(page.locator('.command-palette-command', { hasText: 'New idea' })).toBeVisible()
  })

  // Hover decorates the card visually (CardShell's :hover) but
  // must NOT move the keyboard cursor. Without this guarantee,
  // a mouse drifting near another row would steal Enter from
  // the row the keyboard was on.
  test('hover does not steal keyboard selection', async ({ page }) => {
    const target = `Palette Hover Target ${Date.now()}`
    const other = `Palette Hover Other ${Date.now()}`
    await createIdea(page, other)
    await createIdea(page, target) // visited last → ranks first below orchestrator

    await page.goto('/')
    await openPalette(page)

    // Wait for the async ListIdeas/ListSessionSummaries load to land
    // before pressing keys — otherwise rows.length only includes the
    // pinned entries and ArrowDown clamps at the Dashboard row.
    await expect(
      page.locator('.command-palette-list .session-card', { hasText: target }),
    ).toBeVisible()

    // Default selectedIndex=0 is the orchestrator pin; index 1 is
    // Dashboard; index 2 is the most-recently-visited idea (target).
    // Two ArrowDowns to land on it.
    await page.keyboard.press('ArrowDown')
    await page.keyboard.press('ArrowDown')
    const targetCard = page.locator('.command-palette-list .session-card.current', { hasText: target })
    await expect(targetCard).toBeVisible()

    // Hover the second-ideas row (other). The .current class must
    // stay on `target`, not jump to `other`.
    await page.locator('.command-palette-list .session-card', { hasText: other }).hover()
    await expect(targetCard).toBeVisible()
    await expect(page.locator('.command-palette-list .session-card.current', { hasText: other })).toHaveCount(0)

    // Enter activates the keyboard-selected row, not the hovered one.
    await page.keyboard.press('Enter')
    await expect(page.locator('.idea-detail-name')).toHaveText(target)
  })

  // Selecting a dormant session from the palette should resume it
  // and drop the user straight into the session terminal. The idea
  // page is reserved for the no-non-terminated-session case.
  test('selecting an idea with a dormant session resumes and navigates into it', async ({ page }) => {
    test.skip(!ideasDir, 'TEST_IDEAS_DIR not set')

    const name = `Palette Dormant ${Date.now()}`
    const slug = await createIdea(page, name)

    await page.click('.idea-sidebar .btn-small')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await page.waitForSelector('.terminal-container .xterm-screen', { timeout: 10000 })
    const uuid = await page.evaluate(() => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const reg = (window as any).__ideateTerminals as Record<string, unknown> | undefined
      if (!reg) return ''
      const keys = Object.keys(reg)
      return keys[keys.length - 1] || ''
    })
    expect(uuid).not.toBe('')

    // Stop the live session; the coordinator flips the on-disk record
    // to status="stopped" once the PTY exits. Poll until that lands.
    await page.evaluate(async (id) => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      await (window as any).go.app.App.StopSession(id)
    }, uuid)
    const sessionPath = path.join(ideasDir, slug, 'sessions', `${uuid}.json`)
    await expect.poll(async () => {
      const raw = await fs.readFile(sessionPath, 'utf-8').catch(() => '')
      if (!raw) return ''
      return (JSON.parse(raw) as { status?: string }).status || ''
    }, { timeout: 5000 }).not.toBe('running')

    // Promote stopped → dormant on disk. The adopt sweep does this on
    // startup for idle idea sessions; the test mimics it directly so
    // we don't have to restart the app mid-spec.
    const record = JSON.parse(await fs.readFile(sessionPath, 'utf-8')) as Record<string, unknown>
    record.status = 'dormant'
    await fs.writeFile(sessionPath, JSON.stringify(record, null, 2))

    await page.goto('/')
    await openPalette(page)
    await page.locator('.command-palette-input').fill('palette dormant')
    const row = page.locator('.command-palette-list .session-card', { hasText: name })
    await expect(row).toBeVisible()
    await page.keyboard.press('Enter')

    // The resume call awaits before navigating, so the URL must land
    // on the session view (not the idea page) and a terminal must
    // mount under the resumed uuid.
    await expect(page).toHaveURL(new RegExp(`#/idea/${slug}/session/${uuid}$`), { timeout: 15000 })
    await page.waitForSelector('.terminal-container .xterm-screen', { timeout: 10000 })
  })

  // Repo basenames are invisible search aliases on idea rows — typing
  // a repo name surfaces the owning idea even though that name never
  // appears on the rendered card.
  test('typing a repo basename matches the owning idea', async ({ page }) => {
    test.skip(!ideasDir, 'TEST_IDEAS_DIR not set')

    const stamp = Date.now()
    const ideaName = `Palette Repo Alias ${stamp}`
    const repoName = `zorblax-${stamp}`
    const slug = await createIdea(page, ideaName)
    // Drop the dir directly — listRepoNames just scans repos/ basenames.
    await fs.mkdir(path.join(ideasDir, slug, 'repos', repoName), { recursive: true })

    await page.goto('/')
    await openPalette(page)
    await page.locator('.command-palette-input').fill(repoName)

    const ideaRow = page.locator('.command-palette-list .session-card', { hasText: ideaName })
    await expect(ideaRow).toBeVisible({ timeout: 5000 })
    // The repo name must NOT appear on the rendered card — it's an
    // invisible alias, surface signal only via fuzzy match.
    await expect(ideaRow).not.toContainText(repoName)
  })
})
