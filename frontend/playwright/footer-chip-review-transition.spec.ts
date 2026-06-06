import { test, expect } from '@playwright/test'
import * as fs from 'fs'
import * as path from 'path'
import { stopAllRunningSessions } from './ptyCapture'

// User report: after opening a markdown review and navigating back to
// a session, the footer's current-session chip renders cut off /
// missing its idea-name + agent-label content — only the leading
// SessionStatusIcon stays visible inside the .current border.
//
// This spec reproduces the navigation flow (start session → open
// markdown review → return to session) and asserts the chip's full
// rendered content. Failure surfaces both as DOM assertions and a
// screenshot artifact under test-results/ so we can see what state
// the chip ended up in even when an assertion is the surface failure.

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

test.describe('Footer chip survives review→session navigation', () => {
  const reviewId = 'playwright-md-footer-chip-transition'

  test.afterEach(async ({ page }) => {
    cleanReview(reviewId)
    await stopAllRunningSessions(page)
  })

  test('current-session chip keeps its content after returning from a markdown review', async ({ page }) => {
    const ideaName = `Footer Chip Transition ${Date.now()}`
    const slug = await createIdea(page, ideaName)

    // Start a testagent session — the chip the bug report is about.
    await page.click('.idea-sidebar .btn-small')
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await page.waitForSelector('.terminal-container .xterm-screen', { timeout: 10000 })

    // The bar polls ListActiveSessions; wait for the current chip to
    // mount before navigating away.
    const chipBefore = page.locator('.global-session-chip.current')
    await expect(chipBefore).toBeVisible({ timeout: 10000 })
    await expect(chipBefore.locator('.global-session-chip-idea')).toHaveText(ideaName, { timeout: 5000 })

    // Capture the chip's pre-navigation bounding box so we can compare
    // post-navigation — a width collapse means content vanished.
    const baselineBox = await chipBefore.boundingBox()
    expect(baselineBox).not.toBeNull()
    const baselineWidth = baselineBox?.width ?? 0
    expect(baselineWidth).toBeGreaterThan(40) // sanity — icons alone would be < 40px

    // Capture the session UUID from the URL so we can navigate back to
    // the same route post-review.
    const sessionUrl = page.url()
    const sessionMatch = sessionUrl.match(/\/idea\/([^/]+)\/session\/([0-9a-f-]+)/)
    expect(sessionMatch).not.toBeNull()
    const sessionUuid = sessionMatch?.[2] ?? ''

    // Open the markdown review route.
    seedMarkdownReview(reviewId, '# Review\n\nFooter-chip transition test.\n')
    await page.goto(`/#/review?reviewId=${reviewId}`)
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })

    // Navigate back to the session.
    await page.goto(`/#/idea/${slug}/session/${sessionUuid}`)
    await page.waitForSelector('.terminal-container .xterm-screen', { timeout: 10000 })

    // Capture a screenshot of the footer so failures land alongside
    // visual evidence. test-info.outputPath puts the file under
    // test-results/<spec>/...
    const chipAfter = page.locator('.global-session-chip.current')
    await page.locator('.app-footer').screenshot({ path: 'test-results/footer-after-review-return.png' })

    // The bug: chip content disappears, leaving only the leading
    // SessionStatusIcon inside the .current border. Assert content is
    // intact.
    await expect(chipAfter).toBeVisible({ timeout: 5000 })
    await expect(chipAfter.locator('.global-session-chip-idea')).toHaveText(ideaName, { timeout: 5000 })
    await expect(chipAfter.locator('.global-session-chip-agent')).toBeVisible()

    // Width regression: a collapsed chip with only the icon visible
    // would be ~24-30px wide; full chip is typically 200px+. Anything
    // less than half the baseline strongly suggests content lost.
    const postBox = await chipAfter.boundingBox()
    expect(postBox).not.toBeNull()
    const postWidth = postBox?.width ?? 0
    expect(postWidth).toBeGreaterThan(baselineWidth * 0.5)
  })
})
