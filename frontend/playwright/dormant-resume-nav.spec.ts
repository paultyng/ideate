import { test, expect } from '@playwright/test'
import * as fs from 'node:fs/promises'
import * as path from 'node:path'
import { stopAllRunningSessions } from './ptyCapture'

// Dormant sessions should be navigation-treated like running sessions
// from every entry point (home card, idea-detail terminal toggle,
// quick switcher). Click → auto-resume + drop into the terminal,
// instead of routing through idea-detail → expand sessions → click
// Resume.
//
// The quick switcher case is already covered in
// `command-palette.spec.ts`. This spec covers the two new entry
// points wired up in PR #30: home card click + idea-detail topbar
// Terminal button. Plus a regression case for terminated sessions
// (must NOT auto-resume — those keep the old "open most-recent
// metadata view" behavior).

const ideasDir = process.env.TEST_IDEAS_DIR || ''

async function createIdea(page: import('@playwright/test').Page, name: string): Promise<string> {
  await page.goto('/#/idea/new')
  await page.fill('input[type="text"]', name)
  await page.selectOption('.idea-form select', 'active')
  await page.click('button[type="submit"]')
  await expect(page.locator('.idea-detail-name')).toHaveText(name)
  const url = page.url()
  return url.split('/idea/')[1].split('/')[0].split('?')[0]
}

// Start a testagent session and return its uuid, then stop it and
// promote the on-disk record to status=dormant. Mirrors the dormant
// fixture in command-palette.spec.ts so the two specs use the same
// shape. Returns the uuid for the dormant session.
async function createDormantSession(page: import('@playwright/test').Page, slug: string): Promise<string> {
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
  // startup for idle idea sessions; the test mimics it directly.
  const record = JSON.parse(await fs.readFile(sessionPath, 'utf-8')) as Record<string, unknown>
  record.status = 'dormant'
  await fs.writeFile(sessionPath, JSON.stringify(record, null, 2))

  return uuid
}

test.describe('Dormant resume on navigation', () => {
  test.afterEach(async ({ page }) => {
    await stopAllRunningSessions(page)
  })

  test('home card click auto-resumes dormant session', async ({ page }) => {
    test.skip(!ideasDir, 'TEST_IDEAS_DIR not set')

    const name = `Dormant Card Click ${Date.now()}`
    const slug = await createIdea(page, name)
    const uuid = await createDormantSession(page, slug)

    // From the dashboard, clicking the card should resume + navigate
    // into the session terminal (not the idea-detail page).
    await page.goto('/')
    const card = page.locator('.idea-card', { hasText: name })
    await expect(card).toBeVisible({ timeout: 10000 })
    await card.click()

    await expect(page).toHaveURL(new RegExp(`#/idea/${slug}/session/${uuid}$`), { timeout: 15000 })
    await page.waitForSelector('.terminal-container .xterm-screen', { timeout: 10000 })
  })

  test('idea-detail terminal toggle auto-resumes dormant session', async ({ page }) => {
    test.skip(!ideasDir, 'TEST_IDEAS_DIR not set')

    const name = `Dormant Toggle ${Date.now()}`
    const slug = await createIdea(page, name)
    const uuid = await createDormantSession(page, slug)

    // Navigate to idea-detail (not the session page).
    await page.goto(`/#/idea/${slug}`)
    await expect(page.locator('.idea-detail-name')).toHaveText(name)

    // The topbar Terminal button should now be the dormant-resume
    // entry. Title reflects intent.
    const toggle = page.locator('.btn-nav-session')
    await expect(toggle).toBeVisible()
    await expect(toggle).toHaveAttribute('title', 'Resume dormant session')
    await toggle.click()

    await expect(page).toHaveURL(new RegExp(`#/idea/${slug}/session/${uuid}$`), { timeout: 15000 })
    await page.waitForSelector('.terminal-container .xterm-screen', { timeout: 10000 })
  })

  test('terminated session: home card opens idea-detail, NOT auto-resume', async ({ page }) => {
    test.skip(!ideasDir, 'TEST_IDEAS_DIR not set')

    const name = `Terminated Card Click ${Date.now()}`
    const slug = await createIdea(page, name)

    // Run a session and stop it. Status lands at "stopped" (not
    // "dormant" — the test never promotes it on disk). The resume
    // helper should treat this as terminated and NOT auto-resume.
    await page.click('.idea-sidebar .btn-small')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await page.waitForSelector('.terminal-container .xterm-screen', { timeout: 10000 })
    const uuid = await page.evaluate(() => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const reg = (window as any).__ideateTerminals as Record<string, unknown> | undefined
      return reg ? (Object.keys(reg).pop() || '') : ''
    })
    await page.evaluate(async (id) => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      await (window as any).go.app.App.StopSession(id)
    }, uuid)
    const sessionPath = path.join(ideasDir, slug, 'sessions', `${uuid}.json`)
    await expect.poll(async () => {
      const raw = await fs.readFile(sessionPath, 'utf-8').catch(() => '')
      return raw ? (JSON.parse(raw) as { status?: string }).status || '' : ''
    }, { timeout: 5000 }).not.toBe('running')

    await page.goto('/')
    const card = page.locator('.idea-card', { hasText: name })
    await expect(card).toBeVisible({ timeout: 10000 })
    await card.click()

    // Should land on idea-detail, NOT a session URL.
    await expect(page).toHaveURL(new RegExp(`#/idea/${slug}$`), { timeout: 5000 })
    await expect(page.locator('.idea-detail-name')).toHaveText(name)
  })
})
