import { test, expect } from '@playwright/test'
import * as fs from 'fs'
import * as path from 'path'

const REPO_PATH = process.env.TEST_DIFF_REPO || process.cwd().replace('/frontend', '')

// Reviews live in the central reviews dir under the dev config dir (set by
// `task test:ui` to `<repo>/.ideate-dev/`). Tests seed records there directly
// to drive submit/cancel flows.
const REVIEWS_DIR = process.env.TEST_REVIEWS_DIR
  || path.join(process.env.IDEATE_CONFIG_DIR || path.join(REPO_PATH, '.ideate-dev'), 'reviews')

interface ReviewRecord {
  id: string
  kind: 'diff' | 'markdown'
  repo: string
  base_commit: string
  head_commit: string
  status: 'pending' | 'complete' | 'cancelled'
  created: string
  comments?: Array<{ path: string; line: number; side: string; body: string }>
}

function reviewPath(reviewId: string): string {
  return path.join(REVIEWS_DIR, `${reviewId}.json`)
}

function seedPendingReview(reviewId: string, baseSHA: string, headSHA: string): void {
  fs.mkdirSync(REVIEWS_DIR, { recursive: true, mode: 0o700 })
  const record: ReviewRecord = {
    id: reviewId,
    kind: 'diff',
    repo: REPO_PATH,
    base_commit: baseSHA,
    head_commit: headSHA,
    status: 'pending',
    created: new Date().toISOString(),
  }
  fs.writeFileSync(reviewPath(reviewId), JSON.stringify(record, null, 2) + '\n', { mode: 0o600 })
}

function readReview(reviewId: string): ReviewRecord {
  return JSON.parse(fs.readFileSync(reviewPath(reviewId), 'utf-8'))
}

function cleanReview(reviewId: string): void {
  try {
    fs.unlinkSync(reviewPath(reviewId))
  } catch {
    /* ignore */
  }
}

test.describe('Review View', () => {
  test('renders diff for local repo', async ({ page }) => {
    const errors: string[] = []
    const logs: string[] = []
    page.on('console', msg => {
      if (msg.type() === 'error') errors.push(msg.text())
      logs.push(`[${msg.type()}] ${msg.text()}`)
    })

    // Default to well-known commits for deterministic diffs
    const base = process.env.TEST_DIFF_BASE || '35b9245'
    const head = process.env.TEST_DIFF_HEAD || 'ae32bef'

    await page.goto(`/#/review?repo=${encodeURIComponent(REPO_PATH)}&base=${base}&head=${head}`)

    // Wait for loading to finish — toolbar appears when data is loaded
    await page.waitForSelector('.review-toolbar', { timeout: 15000 })

    // Screenshot after data loads
    await page.screenshot({ path: 'test-results/review-loaded.png', fullPage: true })

    // Verify toolbar content
    await expect(page.locator('.review-toolbar-ref')).toContainText(`${base}...${head}`)

    // Verify file tree has items
    const fileItems = page.locator('.file-tree-item')
    const count = await fileItems.count()
    expect(count).toBeGreaterThan(0)

    // Verify diff panel container
    const diffPanel = page.locator('.diff-panel')
    await expect(diffPanel).toBeVisible()

    // Check the diff panel has meaningful height
    const panelBox = await diffPanel.boundingBox()
    expect(panelBox).not.toBeNull()
    if (panelBox!.height < 100) {
      // Dump diagnostics before failing
      const html = await diffPanel.innerHTML()
      console.log(`DIAG: diff-panel height=${panelBox!.height}, innerHTML length=${html.length}`)
      console.log(`DIAG: first 500 chars: ${html.substring(0, 500)}`)
      console.log(`DIAG: console logs:\n${logs.join('\n')}`)
      await page.screenshot({ path: 'test-results/review-blank-panel.png', fullPage: true })
    }
    expect(panelBox!.height).toBeGreaterThan(100)

    // Check for actual diff line content — @git-diff-view renders td elements
    // with diff lines. If the component rendered but has no lines, something is wrong.
    const diffLines = diffPanel.locator('td')
    const lineCount = await diffLines.count()
    if (lineCount === 0) {
      const html = await diffPanel.innerHTML()
      console.log(`DIAG: no td elements found. innerHTML length=${html.length}`)
      console.log(`DIAG: first 1000 chars: ${html.substring(0, 1000)}`)
      await page.screenshot({ path: 'test-results/review-no-lines.png', fullPage: true })
    }
    expect(lineCount).toBeGreaterThan(0)

    // Screenshot after verification
    await page.screenshot({ path: 'test-results/review-verified.png', fullPage: true })

    // Click second file if available
    if (count > 1) {
      await fileItems.nth(1).click()
      await page.waitForTimeout(500)
      const newLineCount = await diffPanel.locator('td').count()
      expect(newLineCount).toBeGreaterThan(0)
    }

    // Check no console errors (ignore favicon and wails internal)
    const realErrors = errors.filter(e =>
      !e.includes('favicon') && !e.includes('wails:')
    )
    expect(realErrors).toEqual([])
  })

  test('shows instructions when no params provided', async ({ page }) => {
    await page.goto('/#/review')
    await expect(page.locator('text=No diff parameters provided')).toBeVisible()
  })

  test('shows error for invalid repo path', async ({ page }) => {
    await page.goto('/#/review?repo=/nonexistent&base=main&head=HEAD')
    await page.waitForSelector('.review-error', { timeout: 10000 })
  })
})

test.describe('Review submit flow (in-app)', () => {
  const base = process.env.TEST_DIFF_BASE || '35b9245'
  const head = process.env.TEST_DIFF_HEAD || 'ae32bef'

  test.afterEach(() => {
    // Clean up any reviews seeded in tests below.
    for (const id of ['playwright-submit', 'playwright-cancel', 'playwright-multi', 'playwright-edit-comment']) {
      cleanReview(id)
    }
  })

  // seedReviewWithComment creates a pending review and pre-populates it
  // with a single inline comment on the given file/line — exercises the
  // edit affordance without needing a full add-comment flow first.
  function seedReviewWithComment(
    reviewId: string,
    baseSHA: string,
    headSHA: string,
    commentPath: string,
    commentLine: number,
    body: string,
  ): void {
    fs.mkdirSync(REVIEWS_DIR, { recursive: true, mode: 0o700 })
    const record: ReviewRecord = {
      id: reviewId,
      kind: 'diff',
      repo: REPO_PATH,
      base_commit: baseSHA,
      head_commit: headSHA,
      status: 'pending',
      created: new Date().toISOString(),
      comments: [{ path: commentPath, line: commentLine, side: 'RIGHT', body }],
    }
    fs.writeFileSync(reviewPath(reviewId), JSON.stringify(record, null, 2) + '\n', { mode: 0o600 })
  }

  async function openReview(page: import('@playwright/test').Page, reviewId: string) {
    seedPendingReview(reviewId, base, head)
    const url = `/#/review?repo=${encodeURIComponent(REPO_PATH)}&base=${base}&head=${head}&reviewId=${reviewId}`
    await page.goto(url)
    await page.waitForSelector('[data-testid="review-submit-btn"]', { timeout: 15000 })
  }

  test('submit writes complete status and view stays open', async ({ page }) => {
    await openReview(page, 'playwright-submit')

    await page.locator('[data-testid="review-submit-btn"]').click()
    await expect(page.locator('.review-status-badge.complete')).toBeVisible({ timeout: 5000 })

    const r = readReview('playwright-submit')
    expect(r.status).toBe('complete')
  })

  test('cancel writes cancelled status and view stays open', async ({ page }) => {
    await openReview(page, 'playwright-cancel')

    await page.locator('[data-testid="review-cancel-btn"]').click()
    await expect(page.locator('.review-status-badge.cancelled')).toBeVisible({ timeout: 5000 })

    const r = readReview('playwright-cancel')
    expect(r.status).toBe('cancelled')
  })

  test('inline comment edit replaces body in place and persists on submit', async ({ page }) => {
    seedReviewWithComment(
      'playwright-edit-comment',
      base, head,
      '.gitignore',
      1,
      'first draft of feedback',
    )
    const url = `/#/review?repo=${encodeURIComponent(REPO_PATH)}&base=${base}&head=${head}&reviewId=playwright-edit-comment`
    await page.goto(url)
    await page.waitForSelector('[data-testid="review-submit-btn"]', { timeout: 15000 })

    // Sidebar entry for .gitignore should carry the seeded-comment
    // badge — click it to make .gitignore the active diff so the
    // comment thread renders.
    await page
      .locator('.file-tree-item', { hasText: '.gitignore' })
      .first()
      .click()

    // Original body renders; edit ✎ swaps in a textarea pre-populated
    // with the existing body.
    await expect(page.locator('.review-comment-body')).toContainText('first draft of feedback')
    await page.locator('[data-testid="review-comment-edit"]').click()
    const ta = page.locator('[data-testid="review-comment-edit-textarea"]')
    await expect(ta).toBeVisible()
    await expect(ta).toHaveValue('first draft of feedback')

    // Cancel restores the original body without persisting.
    await page.locator('[data-testid="review-comment-edit-cancel"]').click()
    await expect(ta).toHaveCount(0)
    await expect(page.locator('.review-comment-body')).toContainText('first draft of feedback')

    // Edit again and save — body is replaced in place; submit persists.
    await page.locator('[data-testid="review-comment-edit"]').click()
    await ta.fill('revised feedback after a re-read')
    await page.locator('[data-testid="review-comment-edit-save"]').click()
    await expect(page.locator('.review-comment-body')).toContainText('revised feedback after a re-read')

    await page.locator('[data-testid="review-submit-btn"]').click()
    await expect(page.locator('.review-status-badge.complete')).toBeVisible({ timeout: 5000 })

    const r = readReview('playwright-edit-comment')
    expect(r.status).toBe('complete')
    expect(r.comments?.[0].body).toBe('revised feedback after a re-read')
  })
})
