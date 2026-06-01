import { test, expect, type Page } from '@playwright/test'
import { promises as fs } from 'fs'
import * as path from 'path'
import { stopAllRunningSessions } from './ptyCapture'

const ideasDir = process.env.TEST_IDEAS_DIR || ''
const reviewsDir = process.env.TEST_REVIEWS_DIR || ''

// writePendingMarkdownReview persists a minimal pending markdown review
// record on disk so the App.CancelReview binding has something to flip
// (the banner test patches the session record but doesn't create a real
// review; cancel needs both halves to fire end-to-end).
async function writePendingMarkdownReview(reviewID: string, slug: string, sessionUUID: string) {
  const p = path.join(reviewsDir, reviewID + '.json')
  await fs.mkdir(reviewsDir, { recursive: true })
  await fs.writeFile(p, JSON.stringify({
    id: reviewID,
    kind: 'markdown',
    status: 'pending',
    created: new Date().toISOString(),
    session: sessionUUID,
    idea_slug: slug,
    markdown: { path: '/tmp/x.md', original: 'x' },
  }, null, 2))
}

async function createIdea(page: Page, name: string): Promise<string> {
  await page.goto('/#/idea/new')
  await page.fill('input[type="text"]', name)
  await page.selectOption('.idea-form select', 'active')
  await page.click('button[type="submit"]')
  await expect(page.locator('.idea-detail-name')).toHaveText(name)
  return page.url().split('/idea/')[1].split('/')[0].split('?')[0]
}

async function startTestagent(page: Page) {
  await page.click('.idea-sidebar .btn-small[aria-label="Start a new session"]')
  await page.selectOption('.session-start select', 'testagent')
  await page.click('button:has-text("Start Session")')
  await page.waitForSelector('.terminal-container', { timeout: 10000 })
}


// patchActiveReviewID sets active_review_id on the running session record
// for the given idea slug, simulating a request_*_review MCP call without
// needing a real review pipeline.
async function patchActiveReviewID(slug: string, reviewID: string) {
  const sessionsDir = path.join(ideasDir, slug, 'sessions')
  const entries = await fs.readdir(sessionsDir)
  for (const entry of entries) {
    if (!entry.endsWith('.json')) continue
    const p = path.join(sessionsDir, entry)
    const raw = JSON.parse(await fs.readFile(p, 'utf-8'))
    if (raw.status === 'running') {
      raw.active_review_id = reviewID
      raw.activity = reviewID ? 'reviewing' : ''
      await fs.writeFile(p, JSON.stringify(raw, null, 2))
      return
    }
  }
  throw new Error('no running session found to patch')
}

test.describe('Review-session integration', () => {
  test.skip(!ideasDir, 'TEST_IDEAS_DIR not set')

  test.afterEach(async ({ page }) => {
    await stopAllRunningSessions(page)
  })

  test('session view shows review-pending banner when active_review_id is set', async ({ page }) => {
    const slug = await createIdea(page, 'Review Banner Test')
    await startTestagent(page)
    await patchActiveReviewID(slug, 'rev-fake-1')

    // Banner appears within the polling window.
    const banner = page.locator('.session-review-banner')
    await expect(banner).toBeVisible({ timeout: 5000 })
    await expect(banner).toContainText(/pending review/i)
    await expect(page.locator('.session-review-banner button:has-text("Open review")')).toBeVisible()
  })

  test('Open review button navigates to /review with fromSession param', async ({ page }) => {
    const slug = await createIdea(page, 'Open Review Test')
    await startTestagent(page)
    await patchActiveReviewID(slug, 'rev-fake-2')

    await page.locator('.session-review-banner button:has-text("Open review")').click({ timeout: 5000 })
    await expect(page).toHaveURL(/\/#\/review\?reviewId=rev-fake-2&fromSession=/)
    expect(page.url()).toContain(`fromSession=${slug}:`)
  })

  test('global session bar marks the current session chip with .current', async ({ page }) => {
    const slug = await createIdea(page, 'Current Chip Test')
    await startTestagent(page)

    // Navigate to the active session view (we're already there post-start).
    // The global session bar polls + reflects route — wait for the chip.
    const currentChip = page.locator('.global-session-chip.current')
    await expect(currentChip).toBeVisible({ timeout: 5000 })
    await expect(currentChip).toContainText('Current Chip Test')
    expect(slug).toBeTruthy()
  })

  test('cancelling a review clears the banner and the reviewing icon', async ({ page }) => {
    test.skip(!reviewsDir, 'TEST_REVIEWS_DIR not set')
    const slug = await createIdea(page, 'Cancel Clears Banner Test')
    await startTestagent(page)

    // Get the running session UUID; both records (session + review) need
    // to point at the same UUID for the clear lookup to match.
    const sessions = await page.evaluate(async (s: string) => {
      // @ts-expect-error wails binding
      return await window.go.app.App.ListIdeaSessions(s)
    }, slug)
    const sessionUUID = sessions[0].uuid

    const reviewID = 'rev-cancel-clears-1234567890abcdef'
    await writePendingMarkdownReview(reviewID, slug, sessionUUID)
    await patchActiveReviewID(slug, reviewID)

    await expect(page.locator('.session-review-banner')).toBeVisible({ timeout: 5000 })

    // Cancel via the Wails binding (the same path the UI Cancel button uses).
    await page.evaluate(async (id: string) => {
      // @ts-expect-error wails binding
      await window.go.app.App.CancelReview(id)
    }, reviewID)

    // Banner should clear within the polling window.
    await expect(page.locator('.session-review-banner')).toBeHidden({ timeout: 5000 })
    await expect(page.locator('.global-session-chip .session-icon.reviewing')).toBeHidden()
  })

  // Submit on a markdown review opened with fromSession should land the
  // user back on the originating session view, not leave them sitting
  // on the review's "Submitted" badge.
  test('submitting a markdown review with fromSession navigates back to the session', async ({ page }) => {
    test.skip(!reviewsDir, 'TEST_REVIEWS_DIR not set')
    const slug = await createIdea(page, 'Submit Returns Session Test')
    await startTestagent(page)

    const sessions = await page.evaluate(async (s: string) => {
      // @ts-expect-error wails binding
      return await window.go.app.App.ListIdeaSessions(s)
    }, slug)
    const sessionUUID = sessions[0].uuid

    const reviewID = 'rev-submit-returns-1234567890abcdef'
    await writePendingMarkdownReview(reviewID, slug, sessionUUID)
    await patchActiveReviewID(slug, reviewID)

    await page.goto(`/#/review?reviewId=${reviewID}&fromSession=${slug}:${sessionUUID}`)
    await page.waitForSelector('[data-testid="markdown-review-submit-btn"]', { timeout: 10000 })

    await page.locator('[data-testid="markdown-review-submit-btn"]').click()

    await expect(page).toHaveURL(new RegExp(`#/idea/${slug}/session/${sessionUUID}$`), { timeout: 5000 })
  })

  test('session status icon shows reviewing variant when activity=reviewing', async ({ page }) => {
    const slug = await createIdea(page, 'Reviewing Icon Test')
    await startTestagent(page)
    await patchActiveReviewID(slug, 'rev-fake-3')

    // The chip in the bar updates via idea:changed; the .reviewing class
    // on the icon is the assertable signal.
    await expect(page.locator('.global-session-chip .session-icon.reviewing'))
      .toBeVisible({ timeout: 5000 })
  })
})
