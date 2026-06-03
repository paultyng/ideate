import { test, expect, Page } from '@playwright/test'
import * as fs from 'fs'
import * as path from 'path'

// Survival across app close/reopen has two halves: drafts persist to disk
// while editing, and the footer's alerts zone surfaces pending reviews
// regardless of whether they were spawned by a still-running session.
// Playwright can't
// truly restart the daemon, so the autosave half is verified by reading
// the on-disk review record after the debounce, and the hydrate half is
// verified by seeding draft_* fields on a pending record and opening the
// URL fresh.

const REPO_PATH = process.env.TEST_DIFF_REPO || process.cwd().replace('/frontend', '')

const REVIEWS_DIR = process.env.TEST_REVIEWS_DIR
  || path.join(process.env.IDEATE_CONFIG_DIR || path.join(REPO_PATH, '.ideate-dev'), 'reviews')

interface DiffReviewRecord {
  id: string
  kind: 'diff'
  status: 'pending' | 'complete' | 'cancelled'
  created: string
  repo: string
  base_commit: string
  head_commit: string
  comments?: Array<{ path: string; line: number; side: string; body: string }>
  body?: string
  draft_body?: string
  draft_comments?: Array<{ path: string; line: number; side: string; body: string }>
}

interface MarkdownReviewRecord {
  id: string
  kind: 'markdown'
  status: 'pending' | 'complete' | 'cancelled'
  created: string
  body?: string
  draft_body?: string
  markdown: {
    path: string
    original?: string
    marked_up?: string
    draft_marked_up?: string
  }
}

function reviewPath(id: string): string {
  return path.join(REVIEWS_DIR, `${id}.json`)
}

function writeReview(record: DiffReviewRecord | MarkdownReviewRecord): void {
  fs.mkdirSync(REVIEWS_DIR, { recursive: true, mode: 0o700 })
  fs.writeFileSync(reviewPath(record.id), JSON.stringify(record, null, 2) + '\n', { mode: 0o600 })
}

function readDiffReview(id: string): DiffReviewRecord {
  return JSON.parse(fs.readFileSync(reviewPath(id), 'utf-8')) as DiffReviewRecord
}

function readMarkdownReview(id: string): MarkdownReviewRecord {
  return JSON.parse(fs.readFileSync(reviewPath(id), 'utf-8')) as MarkdownReviewRecord
}

function cleanReview(id: string): void {
  try { fs.unlinkSync(reviewPath(id)) } catch { /* ignore */ }
}

// Wait until the on-disk review record satisfies `pred`, polling every
// 100ms up to `timeoutMs`. The autosave debounce is 500ms; a 5s timeout
// covers debounce + filesystem latency without making slow/parallel runs
// flaky.
async function waitForRecord<T>(
  read: () => T,
  pred: (rec: T) => boolean,
  timeoutMs = 5000,
): Promise<T> {
  const deadline = Date.now() + timeoutMs
  let last: T | undefined
  while (Date.now() < deadline) {
    try {
      last = read()
      if (pred(last)) return last
    } catch {
      // record may not exist yet (autosave hasn't fired) — keep polling
    }
    await new Promise((r) => setTimeout(r, 100))
  }
  throw new Error(`record never satisfied predicate, last=${JSON.stringify(last)}`)
}

test.describe('Review persistence — drafts survive close/reopen', () => {
  const base = process.env.TEST_DIFF_BASE || '35b9245'
  const head = process.env.TEST_DIFF_HEAD || 'ae32bef'

  const seededIds = [
    'persistence-diff-autosave',
    'persistence-diff-hydrate',
    'persistence-md-autosave',
    'persistence-md-hydrate',
    'persistence-bar-diff',
    'persistence-bar-md',
  ]

  test.afterEach(() => {
    for (const id of seededIds) cleanReview(id)
  })

  test('diff review summary autosaves to draft_body', async ({ page }) => {
    const id = 'persistence-diff-autosave'
    writeReview({
      id, kind: 'diff', status: 'pending',
      created: new Date().toISOString(),
      repo: REPO_PATH, base_commit: base, head_commit: head,
    })

    await page.goto(`/#/review?repo=${encodeURIComponent(REPO_PATH)}&base=${base}&head=${head}&reviewId=${id}`)
    await page.waitForSelector('.review-summary-input', { timeout: 15000 })

    await page.locator('.review-summary-input').fill('mid-edit summary text')

    const persisted = await waitForRecord(
      () => readDiffReview(id),
      (r) => r.draft_body === 'mid-edit summary text',
    )
    // Authoritative body must remain empty until submit so a polling
    // agent doesn't see the draft as the final review payload.
    expect(persisted.body ?? '').toBe('')
    expect(persisted.status).toBe('pending')
  })

  test('diff review hydrates summary + comments from draft fields', async ({ page }) => {
    const id = 'persistence-diff-hydrate'
    writeReview({
      id, kind: 'diff', status: 'pending',
      created: new Date().toISOString(),
      repo: REPO_PATH, base_commit: base, head_commit: head,
      draft_body: 'restored summary',
      // Path doesn't need to match the active diff file — the toolbar
      // count chip aggregates across files, so the assertion is stable
      // regardless of which file the FileTree opens first.
      draft_comments: [
        { path: 'some-file.go', line: 1, side: 'RIGHT', body: 'restored comment' },
      ],
    })

    await page.goto(`/#/review?repo=${encodeURIComponent(REPO_PATH)}&base=${base}&head=${head}&reviewId=${id}`)
    await page.waitForSelector('.review-summary-input', { timeout: 15000 })

    await expect(page.locator('.review-summary-input')).toHaveValue('restored summary')
    // Toolbar count chip reflects DRAFT comments — the user should see
    // their pending comment count after restart, not zero just because
    // the authoritative Comments[] is empty.
    await expect(page.locator('.review-toolbar-comments')).toContainText('1 comment')
  })

  test('markdown review source-mode edit autosaves to draft_marked_up', async ({ page }) => {
    const id = 'persistence-md-autosave'
    writeReview({
      id, kind: 'markdown', status: 'pending',
      created: new Date().toISOString(),
      markdown: { path: '/tmp/test/persistence-md.md', original: '# Original\n' },
    })

    await page.goto(`/#/review?reviewId=${id}`)
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })

    // Source mode bypasses Crepe so the autosave path under test is the
    // currentContent dependency, not the listener-driven bodyVersion path.
    await page.locator('[data-testid="markdown-review-mode-toggle"]').click()
    const source = page.locator('[data-testid="markdown-review-source"]')
    await expect(source).toBeVisible()
    await source.fill('# Original\n\nhalf-typed paragraph')

    await page.locator('.review-summary-input').fill('still thinking')

    const persisted = await waitForRecord(
      () => readMarkdownReview(id),
      (r) => r.markdown.draft_marked_up?.includes('half-typed paragraph') === true
        && r.draft_body === 'still thinking',
    )
    expect(persisted.markdown.marked_up ?? '').toBe('')
    expect(persisted.body ?? '').toBe('')
    expect(persisted.status).toBe('pending')
  })

  test('markdown draft flushes on route nav-away before debounce fires', async ({ page }) => {
    const id = 'persistence-md-autosave'
    writeReview({
      id, kind: 'markdown', status: 'pending',
      created: new Date().toISOString(),
      markdown: { path: '/tmp/test/persistence-md-flush.md', original: '# Original\n' },
    })

    await page.goto(`/#/review?reviewId=${id}`)
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })

    await page.locator('[data-testid="markdown-review-mode-toggle"]').click()
    const source = page.locator('[data-testid="markdown-review-source"]')
    await expect(source).toBeVisible()
    // Type then immediately navigate — well under the 500ms debounce.
    await source.fill('# Original\n\nrace-the-debounce edit')
    // Hash nav to '/' unmounts MarkdownReview; the unmount cleanup must
    // flush the in-flight draft before the React tree tears down.
    await page.evaluate(() => { window.location.hash = '/' })
    await page.waitForURL(/#\/$/)

    const persisted = await waitForRecord(
      () => readMarkdownReview(id),
      (r) => r.markdown.draft_marked_up?.includes('race-the-debounce edit') === true,
    )
    expect(persisted.status).toBe('pending')
  })

  test('diff draft flushes on route nav-away before debounce fires', async ({ page }) => {
    const id = 'persistence-diff-autosave'
    writeReview({
      id, kind: 'diff', status: 'pending',
      created: new Date().toISOString(),
      repo: REPO_PATH, base_commit: base, head_commit: head,
    })

    await page.goto(`/#/review?repo=${encodeURIComponent(REPO_PATH)}&base=${base}&head=${head}&reviewId=${id}`)
    await page.waitForSelector('.review-summary-input', { timeout: 15000 })

    await page.locator('.review-summary-input').fill('race-the-debounce summary')
    await page.evaluate(() => { window.location.hash = '/' })
    await page.waitForURL(/#\/$/)

    const persisted = await waitForRecord(
      () => readDiffReview(id),
      (r) => r.draft_body === 'race-the-debounce summary',
    )
    expect(persisted.status).toBe('pending')
  })

  test('markdown review hydrates source from draft_marked_up', async ({ page }) => {
    const id = 'persistence-md-hydrate'
    const drafted = '# Original\n\n{++a sentence the human added++}\n'
    writeReview({
      id, kind: 'markdown', status: 'pending',
      created: new Date().toISOString(),
      draft_body: 'restored summary',
      markdown: {
        path: '/tmp/test/persistence-md-hydrate.md',
        original: '# Original\n',
        draft_marked_up: drafted,
      },
    })

    await page.goto(`/#/review?reviewId=${id}`)
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })

    await expect(page.locator('.review-summary-input')).toHaveValue('restored summary')

    await page.locator('[data-testid="markdown-review-mode-toggle"]').click()
    await expect(page.locator('[data-testid="markdown-review-source"]')).toHaveValue(drafted)
  })
})

test.describe('Pending reviews bar — chip surfaces open reviews', () => {
  const ids = ['persistence-bar-diff', 'persistence-bar-md']

  test.afterEach(() => {
    for (const id of ids) cleanReview(id)
  })

  async function waitForChip(page: Page, reviewId: string): Promise<void> {
    // The bar refreshes on review:changed (primary) with a 30s polling
    // backstop. For seeded-on-disk reviews the initial mount fetch picks
    // them up before either of those fires.
    await expect(
      page.locator(`[data-testid="pending-review-chip"][data-review-id="${reviewId}"]`),
    ).toBeVisible({ timeout: 10000 })
  }

  test('diff review chip appears and navigates to the review', async ({ page }) => {
    const id = 'persistence-bar-diff'
    writeReview({
      id, kind: 'diff', status: 'pending',
      created: new Date().toISOString(),
      repo: REPO_PATH, base_commit: 'aaaaaaa1234', head_commit: 'bbbbbbb5678',
    })

    await page.goto('/')
    await waitForChip(page, id)

    const chip = page.locator(`[data-testid="pending-review-chip"][data-review-id="${id}"]`)
    // Label format: "<repoLeaf> <base7>..<head7>" — assert both halves so
    // a regression in either branch is caught.
    await expect(chip).toContainText('aaaaaaa..bbbbbbb')

    await chip.click()
    await expect(page).toHaveURL(new RegExp(`reviewId=${id}`))
  })

  test('markdown review chip uses the file basename', async ({ page }) => {
    const id = 'persistence-bar-md'
    writeReview({
      id, kind: 'markdown', status: 'pending',
      created: new Date().toISOString(),
      markdown: { path: '/tmp/test/proposal.md', original: '# Title\n' },
    })

    await page.goto('/')
    const chip = page.locator(`[data-testid="pending-review-chip"][data-review-id="${id}"]`)
    await expect(chip).toBeVisible({ timeout: 10000 })
    await expect(chip).toContainText('proposal.md')
  })

  test('chip disappears via review:changed event, not polling backstop', async ({ page }) => {
    const id = 'persistence-bar-md'
    writeReview({
      id, kind: 'markdown', status: 'pending',
      created: new Date().toISOString(),
      markdown: { path: '/tmp/test/event.md', original: '# Title\n' },
    })

    await page.goto('/')
    const chip = page.locator(`[data-testid="pending-review-chip"][data-review-id="${id}"]`)
    await expect(chip).toBeVisible({ timeout: 10000 })

    // CancelReview emits review:changed. The bar should react well
    // before the 30s polling backstop — assert with a short timeout
    // so a regression that drops the event listener fails fast.
    await page.evaluate(async (reviewId) => {
      // @ts-expect-error wails binding
      await window.go.app.App.CancelReview(reviewId)
    }, id)
    await expect(chip).toBeHidden({ timeout: 5000 })
  })
})
