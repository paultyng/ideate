import { test, expect } from '@playwright/test'
import * as fs from 'fs'
import * as os from 'os'
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
//
// Review creation goes through App.RequestMarkdownReview — the same
// path the live UI exercises. An earlier version of this spec wrote
// review records directly to disk, which bypassed the `review:changed`
// Wails event PendingReviewsBar listens for, so the bar never updated
// in tests even when its event-wiring was broken in production. See
// docs/test-drift-audit.md.

async function requestMarkdownReview(page: import('@playwright/test').Page, fileContent: string): Promise<string> {
  // Write a real .md file to a temp dir so the binding's
  // os.ReadFile + path validation runs the same code as in production.
  const tmpFile = path.join(os.tmpdir(), `ideate-md-${Date.now()}-${Math.floor(Math.random() * 1e6)}.md`)
  await fs.promises.writeFile(tmpFile, fileContent)
  const reviewId = await page.evaluate(async (p) => {
    // @ts-expect-error wails binding
    const r = (await window.go.app.App.RequestMarkdownReview(p, '')) as { ID: string }
    return r.ID
  }, tmpFile)
  return reviewId
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
  // afterEach cancels any pending review via the real binding so the
  // central reviews store reaches the same terminal state production
  // hits after a user dismiss.
  test.afterEach(async ({ page }) => {
    await page.evaluate(async () => {
      // @ts-expect-error wails binding
      const W = (window as any)
      if (!W.go?.app?.App?.ListPendingReviews) return
      const pending = (await W.go.app.App.ListPendingReviews()) ?? []
      for (const r of pending) {
        try { await W.go.app.App.CancelReview(r.id) } catch { /* already gone */ }
      }
    })
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

    // Open the markdown review route. Creating the review via the
    // real Wails binding fires `review:changed`, which the bar listens
    // for — same path the live UI uses.
    const reviewId = await requestMarkdownReview(page, '# Review\n\nFooter-chip transition test.\n')
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
