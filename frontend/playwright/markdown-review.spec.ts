import { test, expect } from '@playwright/test'
import * as fs from 'fs'
import * as path from 'path'

const REPO_PATH = process.env.TEST_DIFF_REPO || process.cwd().replace('/frontend', '')

const REVIEWS_DIR = process.env.TEST_REVIEWS_DIR
  || path.join(process.env.IDEATE_CONFIG_DIR || path.join(REPO_PATH, '.ideate-dev'), 'reviews')

interface MarkdownReviewRecord {
  id: string
  kind: 'markdown'
  status: 'pending' | 'complete' | 'cancelled'
  created: string
  markdown: {
    path: string
    original?: string
    marked_up?: string
  }
  body?: string
  event?: string
}

function reviewPath(id: string): string {
  return path.join(REVIEWS_DIR, `${id}.json`)
}

function seedMarkdownReview(id: string, original: string): void {
  fs.mkdirSync(REVIEWS_DIR, { recursive: true, mode: 0o700 })
  const record: MarkdownReviewRecord = {
    id,
    kind: 'markdown',
    status: 'pending',
    created: new Date().toISOString(),
    markdown: {
      path: '/tmp/test/' + id + '.md',
      original,
    },
  }
  fs.writeFileSync(reviewPath(id), JSON.stringify(record, null, 2) + '\n', { mode: 0o600 })
}

function readMarkdownReview(id: string): MarkdownReviewRecord {
  return JSON.parse(fs.readFileSync(reviewPath(id), 'utf-8'))
}

function cleanReview(id: string): void {
  try {
    fs.unlinkSync(reviewPath(id))
  } catch {
    /* ignore */
  }
}

test.describe('Markdown Review', () => {
  // Track seeded review IDs so the afterEach can clean them up regardless
  // of which test ran. Add new IDs to this list when you add a test.
  const seededIds = [
    'playwright-md-render',
    'playwright-md-source',
    'playwright-md-frontmatter',
    'playwright-md-submit',
    'playwright-md-cancel',
    'playwright-md-cancel-dirty',
    'playwright-md-source-edit',
    'playwright-md-insert',
    'playwright-md-roundtrip',
    'playwright-md-suggest-type',
    'playwright-md-suggest-backspace',
    'playwright-md-suggest-shrink-insertion',
    'playwright-md-suggest-substitution',
    'playwright-md-suggest-replace-selection',
    'playwright-md-suggest-substitute-plain',
    'playwright-md-comment-submit',
    'playwright-md-comment-esc',
  ]

  test.afterEach(() => {
    for (const id of seededIds) cleanReview(id)
  })

  test('renders Milkdown editor with seeded content', async ({ page }) => {
    seedMarkdownReview('playwright-md-render', '# Hello\n\nFirst paragraph.\n')
    await page.goto('/#/review?reviewId=playwright-md-render')
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })
    // Crepe wraps the editor surface in `.milkdown` — the rendered prose
    // content should include the heading and body text.
    await expect(page.locator('.milkdown')).toContainText('Hello', { timeout: 10000 })
    await expect(page.locator('.milkdown')).toContainText('First paragraph')
  })

  test('source mode toggle shows raw markdown', async ({ page }) => {
    const content = '# Title\n\nbody text.\n'
    seedMarkdownReview('playwright-md-source', content)
    await page.goto('/#/review?reviewId=playwright-md-source')
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })

    await page.locator('[data-testid="markdown-review-mode-toggle"]').click()
    const source = page.locator('[data-testid="markdown-review-source"]')
    await expect(source).toBeVisible()
    await expect(source).toHaveValue(content)
  })

  test('frontmatter is preserved verbatim in source mode', async ({ page }) => {
    const content = '---\ntitle: Test\nstatus: active\n---\n# Body\n\nContent here.\n'
    seedMarkdownReview('playwright-md-frontmatter', content)
    await page.goto('/#/review?reviewId=playwright-md-frontmatter')
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })

    await page.locator('[data-testid="markdown-review-mode-toggle"]').click()
    await expect(page.locator('[data-testid="markdown-review-source"]')).toHaveValue(content)
  })

  test('submit writes marked_up preserving frontmatter', async ({ page }) => {
    const content = '---\ntitle: Test\n---\n# Body\n\nContent.\n'
    seedMarkdownReview('playwright-md-submit', content)
    await page.goto('/#/review?reviewId=playwright-md-submit')
    await page.waitForSelector('[data-testid="markdown-review-submit-btn"]', { timeout: 15000 })

    await page.locator('[data-testid="markdown-review-submit-btn"]').click()
    await expect(page.locator('.review-status-badge.complete')).toBeVisible({ timeout: 10000 })

    const r = readMarkdownReview('playwright-md-submit')
    expect(r.status).toBe('complete')
    expect(r.markdown.marked_up).toContain('title: Test') // frontmatter survived
    expect(r.markdown.marked_up).toContain('Content') // body survived
  })

  test('cancel writes cancelled status', async ({ page }) => {
    seedMarkdownReview('playwright-md-cancel', '# Cancel test\n')
    await page.goto('/#/review?reviewId=playwright-md-cancel')
    await page.waitForSelector('[data-testid="markdown-review-cancel-btn"]', { timeout: 15000 })

    await page.locator('[data-testid="markdown-review-cancel-btn"]').click()
    await expect(page.locator('.review-status-badge.cancelled')).toBeVisible({ timeout: 10000 })

    const r = readMarkdownReview('playwright-md-cancel')
    expect(r.status).toBe('cancelled')
  })

  test('cancel with unsaved edits prompts; dismiss preserves review', async ({ page }) => {
    seedMarkdownReview('playwright-md-cancel-dirty', '# Original\n')
    await page.goto('/#/review?reviewId=playwright-md-cancel-dirty')
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })

    // Swap to source mode and edit so hasEdited flips true. WYSIWYG-mode
    // edits would also work, but source mode is the deterministic path
    // for Playwright (no Crepe selection ceremony).
    await page.locator('[data-testid="markdown-review-mode-toggle"]').click()
    const source = page.locator('[data-testid="markdown-review-source"]')
    await source.fill('# Original\n\nMy in-progress feedback.\n')

    // Cancel review while edits are unsaved opens the in-app confirm
    // dialog (window.confirm is no-op in Wails WKWebView, so we use a
    // React modal). Dismiss it — review stays pending.
    await page.locator('[data-testid="markdown-review-cancel-btn"]').click()
    await page.locator('[data-testid="confirm-dialog-cancel"]').click()
    await expect(page.locator('[data-testid="markdown-review-cancel-btn"]')).toBeVisible()
    expect(readMarkdownReview('playwright-md-cancel-dirty').status).toBe('pending')

    // Accept the second time — review goes cancelled.
    await page.locator('[data-testid="markdown-review-cancel-btn"]').click()
    await page.locator('[data-testid="confirm-dialog-confirm"]').click()
    await expect(page.locator('.review-status-badge.cancelled')).toBeVisible({ timeout: 10000 })
    expect(readMarkdownReview('playwright-md-cancel-dirty').status).toBe('cancelled')
  })

  test('source-mode edit round-trips through submit', async ({ page }) => {
    seedMarkdownReview('playwright-md-source-edit', '# Original\n')
    await page.goto('/#/review?reviewId=playwright-md-source-edit')
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })

    await page.locator('[data-testid="markdown-review-mode-toggle"]').click()
    const source = page.locator('[data-testid="markdown-review-source"]')
    await expect(source).toBeVisible()
    await source.fill('# Edited\n\nAdded paragraph.\n')

    await page.locator('[data-testid="markdown-review-submit-btn"]').click()
    await expect(page.locator('.review-status-badge.complete')).toBeVisible({ timeout: 10000 })

    const r = readMarkdownReview('playwright-md-source-edit')
    expect(r.status).toBe('complete')
    expect(r.markdown.marked_up).toContain('Edited')
    expect(r.markdown.marked_up).toContain('Added paragraph')
  })

  test('Insert toolbar applies insertion mark and serializes to {++ ++}', async ({ page }) => {
    seedMarkdownReview('playwright-md-insert', '# Test\n\nClick here.\n')
    await page.goto('/#/review?reviewId=playwright-md-insert')
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })

    // Wait until Crepe has rendered the seeded content — the editor
    // container is in the DOM before Crepe's async create() finishes, so
    // clicks that race the editor's first dispatch silently no-op.
    await expect(page.locator('.milkdown')).toContainText('Click here', { timeout: 10000 })

    // Select the paragraph text so the toggleMark command has a range to
    // wrap. Triple-click selects the whole line/paragraph in ProseMirror.
    const paragraph = page.locator('.milkdown p').first()
    await paragraph.click({ clickCount: 3 })
    await page.waitForTimeout(100)

    await page.locator('[data-testid="cm-insert-btn"]').click()

    // Schema mark renders as <ins class="cm-insertion">; the CriticMarkup
    // delimiters are a serialization detail and don't appear in the DOM.
    await expect(page.locator('.milkdown ins.cm-insertion')).toContainText('Click here', {
      timeout: 5000,
    })

    // Switching to source mode round-trips the mark through the markdown
    // serializer, which produces literal CriticMarkup syntax.
    await page.locator('[data-testid="markdown-review-mode-toggle"]').click()
    await expect(page.locator('[data-testid="markdown-review-source"]')).toHaveValue(
      /\{\+\+Click here\.\+\+\}/,
    )
  })

  test('typing in WYSIWYG auto-wraps text in insertion mark', async ({ page }) => {
    seedMarkdownReview('playwright-md-suggest-type', '# Test\n\nBefore.\n')
    await page.goto('/#/review?reviewId=playwright-md-suggest-type')
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })
    await expect(page.locator('.milkdown')).toContainText('Before', { timeout: 10000 })

    // Click at the end of the paragraph (end of "Before.") to position
    // the cursor just past the period, then type new text. Suggesting
    // mode should wrap the typed text in an insertion mark.
    const paragraph = page.locator('.milkdown p').first()
    await paragraph.click()
    await page.waitForTimeout(100)
    await page.keyboard.press('End')
    await page.waitForTimeout(50)
    await page.keyboard.type(' ADDED')
    await page.waitForTimeout(100)

    await expect(page.locator('.milkdown ins.cm-insertion')).toContainText('ADDED', {
      timeout: 5000,
    })

    // Switch to source mode — typed text should serialize with `{++ ++}`.
    await page.locator('[data-testid="markdown-review-mode-toggle"]').click()
    await expect(page.locator('[data-testid="markdown-review-source"]')).toHaveValue(
      /\{\+\+[^}]*ADDED[^}]*\+\+\}/,
    )
  })

  test('Backspace on normal text wraps in deletion mark', async ({ page }) => {
    seedMarkdownReview('playwright-md-suggest-backspace', '# Test\n\nKeepme.\n')
    await page.goto('/#/review?reviewId=playwright-md-suggest-backspace')
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })
    await expect(page.locator('.milkdown')).toContainText('Keepme', { timeout: 10000 })

    const paragraph = page.locator('.milkdown p').first()
    await paragraph.click()
    await page.waitForTimeout(100)
    await page.keyboard.press('End')
    await page.waitForTimeout(50)
    // Backspace twice — should mark the last 2 chars (".", "e") as deletion
    // without removing them from the doc.
    await page.keyboard.press('Backspace')
    await page.keyboard.press('Backspace')
    await page.waitForTimeout(100)

    await expect(page.locator('.milkdown del.cm-deletion').first()).toBeVisible({
      timeout: 5000,
    })

    // Source mode shows the deletion-marked text serialized with `{-- --}`.
    await page.locator('[data-testid="markdown-review-mode-toggle"]').click()
    await expect(page.locator('[data-testid="markdown-review-source"]')).toHaveValue(
      /\{--[^}]+--\}/,
    )
  })

  test('Backspace inside fresh insertion shrinks the mark instead of wrapping', async ({ page }) => {
    seedMarkdownReview('playwright-md-suggest-shrink-insertion', '# Test\n\nbase\n')
    await page.goto('/#/review?reviewId=playwright-md-suggest-shrink-insertion')
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })
    await expect(page.locator('.milkdown')).toContainText('base', { timeout: 10000 })

    const paragraph = page.locator('.milkdown p').first()
    await paragraph.click()
    await page.waitForTimeout(100)
    await page.keyboard.press('End')
    await page.waitForTimeout(50)
    // Type 3 chars (auto-marked as insertion), then backspace once.
    // The insertion mark should shrink to 2 chars — no deletion mark.
    await page.keyboard.type('xyz')
    await page.waitForTimeout(50)
    await page.keyboard.press('Backspace')
    await page.waitForTimeout(100)

    // Source serializes to {++xy++} with no deletion marker.
    await page.locator('[data-testid="markdown-review-mode-toggle"]').click()
    const source = page.locator('[data-testid="markdown-review-source"]')
    await expect(source).toHaveValue(/\{\+\+xy\+\+\}/)
    const value = await source.inputValue()
    expect(value).not.toContain('{--')
  })

  test('typing inside a deletion produces a substitution on submit (no mark overlap)', async ({ page }) => {
    seedMarkdownReview(
      'playwright-md-suggest-substitution',
      '# Test\n\nThis is {--frontend (custom layout)--} content.\n',
    )
    await page.goto('/#/review?reviewId=playwright-md-suggest-substitution')
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })
    await expect(page.locator('.milkdown del.cm-deletion')).toContainText('frontend', {
      timeout: 10000,
    })

    // Click inside the deletion-marked text. The deletion runs over the
    // exact characters "frontend (custom layout)" — clicking on "custom"
    // lands the cursor inside the deletion.
    await page.locator('.milkdown del.cm-deletion').first().click({ position: { x: 5, y: 5 } })
    await page.waitForTimeout(100)
    // Move to a known-internal position in the deletion via Home + ArrowRight.
    // Actually simpler: just type — wherever the click landed (inside the
    // deletion) is fine.
    await page.keyboard.type('library')
    await page.waitForTimeout(100)

    // Editor: the typed text must NOT carry the deletion mark — assert
    // that no element has both classes simultaneously. The split form
    // shows up as <del>...</del><ins>library</ins><del>...</del>.
    const overlapping = await page
      .locator('.milkdown del.cm-deletion ins.cm-insertion, .milkdown ins.cm-insertion del.cm-deletion')
      .count()
    expect(overlapping).toBe(0)
    await expect(page.locator('.milkdown ins.cm-insertion')).toContainText('library', {
      timeout: 5000,
    })

    // Submit → the substitution-collapse pass turns the split form into
    // `{~~old~>library~~}` for the agent.
    await page.locator('[data-testid="markdown-review-submit-btn"]').click()
    await expect(page.locator('.review-status-badge.complete')).toBeVisible({ timeout: 10000 })
    const r = readMarkdownReview('playwright-md-suggest-substitution')
    expect(r.markdown.marked_up).toMatch(/\{~~[^}]*frontend[^}]*~>library~~\}/)
    expect(r.markdown.marked_up).not.toContain('{--frontend')
  })

  test('replacing a deletion-marked selection: first char is insertion, not deletion', async ({ page }) => {
    // Regression for "first character was mis-styled as a removal":
    // when you toggle the deletion mark on a selection (toolbar Delete)
    // and *immediately* type without clearing the selection, the typed
    // chars replace the selection. ProseMirror picks up the marks at
    // the selection's *start* — which were deletion-marked — so the
    // first char inherited the deletion mark unless we override
    // storedMarks. The suggesting-mode plugin sets storedMarks on
    // selectionSet for non-empty selections too, so the typed text is
    // always insertion-only.
    seedMarkdownReview(
      'playwright-md-suggest-replace-selection',
      '# Test\n\nReplaceme please.\n',
    )
    await page.goto('/#/review?reviewId=playwright-md-suggest-replace-selection')
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })
    await expect(page.locator('.milkdown')).toContainText('Replaceme', { timeout: 10000 })

    // Triple-click the paragraph to select the whole line, then mark it
    // as deletion via the toolbar — selection stays active.
    const paragraph = page.locator('.milkdown p').first()
    await paragraph.click({ clickCount: 3 })
    await page.waitForTimeout(100)
    await page.locator('[data-testid="cm-delete-btn"]').click()
    await page.waitForTimeout(100)

    // Type without clearing the selection — first char must NOT be
    // deletion-marked. We then verify both the editor visual and the
    // serialized output.
    await page.keyboard.type('Hello world')
    await page.waitForTimeout(100)

    // The leading 'H' must live inside an <ins>, not a <del>.
    await expect(page.locator('.milkdown ins.cm-insertion').first()).toContainText('Hello', {
      timeout: 5000,
    })

    // Submit and check the structured marks. The new content should be
    // an insertion (or substitution if the original deletion-marked
    // text remained in the doc).
    await page.locator('[data-testid="markdown-review-submit-btn"]').click()
    await expect(page.locator('.review-status-badge.complete')).toBeVisible({ timeout: 10000 })
    const r = readMarkdownReview('playwright-md-suggest-replace-selection')
    const marks = r.markdown.marked_up ?? ''
    // First-char regression: the first thing in the new text must be a
    // fresh insertion 'H', not a deletion 'H' or a substitution starting
    // with 'H' as old. Easiest assertion: there is no `{--H` anywhere.
    expect(marks).not.toMatch(/\{--H/)
    // And there is some marker carrying "Hello world" as the new content
    // — either as an insertion or a substitution.
    const hasInsertionWithHello = /\{\+\+[^}]*Hello world[^}]*\+\+\}/.test(marks)
    const hasSubstitutionWithHello = /~>[^}]*Hello world[^}]*~~\}/.test(marks)
    expect(hasInsertionWithHello || hasSubstitutionWithHello).toBe(true)
  })

  test('selecting plain text and typing produces a substitution', async ({ page }) => {
    // The user-visible bug we're fixing: "select text and type" used to
    // discard the selection and only emit an insertion. Now it should
    // produce `{~~old~>new~~}` because the selection is preserved as a
    // deletion mark and the typed text is appended as an insertion —
    // the collapse step folds the pair into a substitution on serialize.
    seedMarkdownReview(
      'playwright-md-suggest-substitute-plain',
      '# Test\n\nReplaceme.\n',
    )
    await page.goto('/#/review?reviewId=playwright-md-suggest-substitute-plain')
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })
    await expect(page.locator('.milkdown')).toContainText('Replaceme', { timeout: 10000 })

    // Triple-click the paragraph to select the whole line as plain
    // text (no toolbar Delete first — we want to verify the substitute
    // path fires from a *plain* selection).
    const paragraph = page.locator('.milkdown p').first()
    await paragraph.click({ clickCount: 3 })
    await page.waitForTimeout(100)

    // Type the replacement while the selection is still active.
    await page.keyboard.type('Swapped')
    await page.waitForTimeout(100)

    // Visual: the original prose now lives inside a <del>, and the new
    // text lives inside an <ins>.
    await expect(page.locator('.milkdown del.cm-deletion').first()).toContainText('Replaceme', {
      timeout: 5000,
    })
    await expect(page.locator('.milkdown ins.cm-insertion').first()).toContainText('Swapped', {
      timeout: 5000,
    })

    // Submit and verify the serialized output is a substitution that
    // carries both the old and new text.
    await page.locator('[data-testid="markdown-review-submit-btn"]').click()
    await expect(page.locator('.review-status-badge.complete')).toBeVisible({ timeout: 10000 })
    const r = readMarkdownReview('playwright-md-suggest-substitute-plain')
    expect(r.markdown.marked_up).toMatch(/\{~~[^}]*Replaceme[^}]*~>Swapped~~\}/)
  })

  test('Comment popover inserts a {>>...<<} comment mark', async ({ page }) => {
    seedMarkdownReview('playwright-md-comment-submit', '# Test\n\nNeeds annotation.\n')
    await page.goto('/#/review?reviewId=playwright-md-comment-submit')
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })
    await expect(page.locator('.milkdown')).toContainText('Needs annotation', { timeout: 10000 })

    // Place the caret somewhere in the paragraph.
    const paragraph = page.locator('.milkdown p').first()
    await paragraph.click()
    await page.waitForTimeout(100)

    // Open the popover via the toolbar button.
    await page.locator('[data-testid="cm-comment-btn"]').click()
    const popover = page.locator('[data-testid="cm-comment-popover"]')
    await expect(popover).toBeVisible({ timeout: 5000 })

    // Type and submit via the Insert button.
    const input = page.locator('[data-testid="cm-comment-popover-input"]')
    await input.fill('a note for the agent')
    await page.locator('[data-testid="cm-comment-popover-submit"]').click()
    await expect(popover).toBeHidden()

    // Submit the review and verify the comment landed in marked_up.
    await page.locator('[data-testid="markdown-review-submit-btn"]').click()
    await expect(page.locator('.review-status-badge.complete')).toBeVisible({ timeout: 10000 })

    const r = readMarkdownReview('playwright-md-comment-submit')
    expect(r.markdown.marked_up).toMatch(/\{>>a note for the agent<<\}/)
  })

  test('Comment popover dismisses on Escape without inserting', async ({ page }) => {
    seedMarkdownReview('playwright-md-comment-esc', '# Test\n\nUntouched.\n')
    await page.goto('/#/review?reviewId=playwright-md-comment-esc')
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })
    await expect(page.locator('.milkdown')).toContainText('Untouched', { timeout: 10000 })

    const paragraph = page.locator('.milkdown p').first()
    await paragraph.click()
    await page.waitForTimeout(100)

    await page.locator('[data-testid="cm-comment-btn"]').click()
    const popover = page.locator('[data-testid="cm-comment-popover"]')
    await expect(popover).toBeVisible({ timeout: 5000 })

    await page.locator('[data-testid="cm-comment-popover-input"]').fill('discarded')
    await page.keyboard.press('Escape')
    await expect(popover).toBeHidden()

    // No comment node should have been inserted.
    await expect(page.locator('.milkdown .cm-comment')).toHaveCount(0)

    // And submitting yields a marked_up with no `{>>` marker.
    await page.locator('[data-testid="markdown-review-submit-btn"]').click()
    await expect(page.locator('.review-status-badge.complete')).toBeVisible({ timeout: 10000 })

    const r = readMarkdownReview('playwright-md-comment-esc')
    expect(r.markdown.marked_up).not.toContain('{>>')
  })

  // CriticMarkup marks + comment atom are inline:true; ProseMirror's
  // default code_block accepts only text children, so dispatch silently
  // no-ops there. The toolbar buttons disable themselves when the caret
  // enters a code_block (and re-enable on exit) so users see the limit
  // instead of clicking a no-op.
  test('Insert/Delete/Comment buttons disable inside code blocks', async ({ page }) => {
    seedMarkdownReview(
      'playwright-md-codeblock-disable',
      '# Test\n\nA paragraph for context.\n\n```ts\nconst x = 1\n```\n\nAnother paragraph.\n',
    )
    await page.goto('/#/review?reviewId=playwright-md-codeblock-disable')
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })
    await expect(page.locator('.milkdown')).toContainText('A paragraph for context', { timeout: 10000 })

    const insertBtn = page.locator('[data-testid="cm-insert-btn"]')
    const deleteBtn = page.locator('[data-testid="cm-delete-btn"]')
    const commentBtn = page.locator('[data-testid="cm-comment-btn"]')

    // Caret in the first paragraph → all 3 enabled.
    await page.locator('.milkdown p').first().click()
    await page.waitForTimeout(100)
    await expect(insertBtn).not.toBeDisabled()
    await expect(deleteBtn).not.toBeDisabled()
    await expect(commentBtn).not.toBeDisabled()

    // Caret inside the code block → all 3 disabled.
    // Crepe / Milkdown renders code_block content as a <pre><code>;
    // clicking the code text lands the caret inside the block.
    await page.locator('.milkdown pre').first().click()
    await page.waitForTimeout(100)
    await expect(insertBtn).toBeDisabled()
    await expect(deleteBtn).toBeDisabled()
    await expect(commentBtn).toBeDisabled()
    await expect(commentBtn).toHaveAttribute('title', /code blocks/)

    // Caret in the trailing paragraph → re-enabled.
    await page.locator('.milkdown p').last().click()
    await page.waitForTimeout(100)
    await expect(insertBtn).not.toBeDisabled()
    await expect(deleteBtn).not.toBeDisabled()
    await expect(commentBtn).not.toBeDisabled()
  })

  test('seeded {++ ++} CriticMarkup renders as insertion mark and round-trips', async ({ page }) => {
    seedMarkdownReview(
      'playwright-md-roundtrip',
      '# Test\n\nThis is {++added++} text.\n',
    )
    await page.goto('/#/review?reviewId=playwright-md-roundtrip')
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })

    await expect(page.locator('.milkdown ins.cm-insertion')).toContainText('added', {
      timeout: 10000,
    })

    // Submit without any edits — marked_up should still contain the
    // original `{++added++}` syntax (proves the round-trip survives
    // schema marks).
    await page.locator('[data-testid="markdown-review-submit-btn"]').click()
    await expect(page.locator('.review-status-badge.complete')).toBeVisible({ timeout: 10000 })

    const r = readMarkdownReview('playwright-md-roundtrip')
    expect(r.markdown.marked_up).toContain('{++added++}')
  })
})
