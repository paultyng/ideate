import { test, expect } from '@playwright/test'
import * as fs from 'fs'
import * as path from 'path'
import { stopAllRunningSessions } from './ptyCapture'

// User report: after opening a markdown review and navigating back to
// a session, the footer's current-session chip renders cut off /
// missing its idea-name + agent-label content — only the leading
// SessionStatusIcon stays visible inside the .current border. The
// observed screenshot showed an overflow `+29` chip alongside the
// broken current chip, so the bug appears in the multi-session /
// popover-overflow scenario.
//
// This spec reproduces the navigation flow with multiple running
// sessions so the overflow `+N` is active. Always screenshots the
// footer post-transition (regardless of assertion outcome) so the
// user can manually verify visually.

const REPO_PATH = process.env.TEST_DIFF_REPO || process.cwd().replace('/frontend', '')
const REVIEWS_DIR = process.env.TEST_REVIEWS_DIR
  || path.join(process.env.IDEATE_CONFIG_DIR || path.join(REPO_PATH, '.ideate-dev'), 'reviews')

function seedMarkdownReview(id: string, original: string): void {
  fs.mkdirSync(REVIEWS_DIR, { recursive: true, mode: 0o700 })
  const record = {
    id,
    kind: 'markdown',
    status: 'pending',
    created: new Date().toISOString(),
    markdown: {
      path: '/tmp/test/' + id + '.md',
      original,
    },
  }
  fs.writeFileSync(path.join(REVIEWS_DIR, `${id}.json`), JSON.stringify(record, null, 2) + '\n', { mode: 0o600 })
}

function cleanReview(id: string): void {
  try { fs.unlinkSync(path.join(REVIEWS_DIR, `${id}.json`)) } catch { /* ignore */ }
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

// Start a testagent session for the current idea and return its UUID
// once the terminal mounts.
async function startSession(page: import('@playwright/test').Page): Promise<string> {
  await page.click('.idea-sidebar .btn-small')
  await page.selectOption('.session-start select', 'testagent')
  await page.click('button:has-text("Start Session")')
  await page.waitForSelector('.terminal-container .xterm-screen', { timeout: 10000 })
  // The latest-registered xterm is the one we just mounted.
  return await page.evaluate(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const reg = (window as any).__ideateTerminals as Record<string, unknown> | undefined
    if (!reg) return ''
    const keys = Object.keys(reg)
    return keys[keys.length - 1] || ''
  })
}

test.describe('Footer chip survives review→session navigation', () => {
  const reviewId = 'playwright-md-footer-chip-transition'

  test.afterEach(async ({ page }) => {
    cleanReview(reviewId)
    await stopAllRunningSessions(page)
  })

  test('current-session chip keeps content after returning from a markdown review (single session)', async ({ page }) => {
    const ideaName = `Footer Chip Single ${Date.now()}`
    const slug = await createIdea(page, ideaName)
    const uuid = await startSession(page)
    expect(uuid).not.toBe('')

    const chipBefore = page.locator('.global-session-chip.current')
    await expect(chipBefore).toBeVisible({ timeout: 10000 })
    await expect(chipBefore.locator('.global-session-chip-idea')).toHaveText(ideaName, { timeout: 5000 })
    const baselineWidth = (await chipBefore.boundingBox())?.width ?? 0
    expect(baselineWidth).toBeGreaterThan(40)

    // Open + return from review.
    seedMarkdownReview(reviewId, '# Review\n\nFooter-chip single-session test.\n')
    await page.goto(`/#/review?reviewId=${reviewId}`)
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })
    await page.goto(`/#/idea/${slug}/session/${uuid}`)
    await page.waitForSelector('.terminal-container .xterm-screen', { timeout: 10000 })

    // Capture screenshot for manual inspection regardless of outcome.
    await page.locator('.app-footer').screenshot({ path: 'test-results/footer-after-review-return-single.png' })

    const chipAfter = page.locator('.global-session-chip.current')
    await expect(chipAfter).toBeVisible({ timeout: 5000 })
    await expect(chipAfter.locator('.global-session-chip-idea')).toHaveText(ideaName, { timeout: 5000 })
    const postWidth = (await chipAfter.boundingBox())?.width ?? 0
    expect(postWidth).toBeGreaterThan(baselineWidth * 0.5)
  })

  // The user's screenshot showed `+29` overflow alongside the broken
  // chip — the multi-session scenario is what they actually hit. Set
  // up 4 extra sessions so the overflow indicator is active when we
  // navigate.
  test('current-session chip keeps content after review return — with overflow +N visible', async ({ page }) => {
    const stamp = Date.now()
    const primaryIdeaName = `Footer Chip Primary ${stamp}`
    const primarySlug = await createIdea(page, primaryIdeaName)
    const primaryUuid = await startSession(page)
    expect(primaryUuid).not.toBe('')

    // Spin up 4 more idea+session pairs. partition() pins the
    // current chip and bundles every other running session into the
    // popover, so 4 extras → +4 overflow.
    const EXTRA = 4
    for (let i = 0; i < EXTRA; i++) {
      await createIdea(page, `Footer Chip Extra ${stamp}-${i}`)
      await startSession(page)
    }

    // Return to the primary session so it's the .current chip.
    await page.goto(`/#/idea/${primarySlug}/session/${primaryUuid}`)
    await page.waitForSelector('.terminal-container .xterm-screen', { timeout: 10000 })

    // Confirm the overflow indicator is visible BEFORE navigating
    // away — otherwise this test isn't actually exercising the
    // overflow scenario.
    const overflow = page.locator('.global-session-overflow .global-session-more')
    await expect(overflow).toBeVisible({ timeout: 10000 })
    await expect(overflow).toHaveText(new RegExp(`\\+${EXTRA}`))

    const chipBefore = page.locator('.global-session-chip.current')
    await expect(chipBefore).toBeVisible()
    await expect(chipBefore.locator('.global-session-chip-idea')).toHaveText(primaryIdeaName, { timeout: 5000 })
    const baselineWidth = (await chipBefore.boundingBox())?.width ?? 0
    expect(baselineWidth).toBeGreaterThan(40)

    // Capture pre-navigation footer for comparison.
    await page.locator('.app-footer').screenshot({ path: 'test-results/footer-overflow-before-review.png' })

    // Open + return from the review.
    seedMarkdownReview(reviewId, '# Review\n\nFooter-chip overflow scenario.\n')
    await page.goto(`/#/review?reviewId=${reviewId}`)
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })
    await page.goto(`/#/idea/${primarySlug}/session/${primaryUuid}`)
    await page.waitForSelector('.terminal-container .xterm-screen', { timeout: 10000 })

    // Always capture the footer post-navigation — this is the
    // artifact the user wants to manually inspect.
    await page.locator('.app-footer').screenshot({ path: 'test-results/footer-overflow-after-review.png' })

    // Assertions — these may pass while the bug still reproduces
    // visually (CSS-side cutoff that doesn't change the DOM
    // content), so the screenshot above is the load-bearing
    // artifact. Keep the assertions as a safety net.
    const chipAfter = page.locator('.global-session-chip.current')
    await expect(chipAfter).toBeVisible({ timeout: 5000 })
    await expect(chipAfter.locator('.global-session-chip-idea')).toHaveText(primaryIdeaName, { timeout: 5000 })
    await expect(chipAfter.locator('.global-session-chip-agent')).toBeVisible()
    const postWidth = (await chipAfter.boundingBox())?.width ?? 0
    expect(postWidth).toBeGreaterThan(baselineWidth * 0.5)
  })
})
