import { test, expect, Page } from '@playwright/test'
import * as fs from 'fs'
import * as os from 'os'
import * as path from 'path'
import { callBinding, cancelPendingReviews } from './backend'

// useDirtyGuard protects unsubmitted edits on two surfaces:
//   - confirmIfDirty(action): in-app Cancel buttons route through the
//     ConfirmDialog (Discard / Keep editing) only when the form is dirty.
//   - a beforeunload listener that exists only while dirty.
//
// Invariants:
//   1. Clean form: Cancel navigates immediately, no dialog.
//   2. Dirty form (any one of name / status / summary): Cancel opens the
//      dialog; "Keep editing" leaves route and field values untouched.
//   3. "Discard" navigates away and the edits are gone.
//   4. A successful save bypasses the guard: no dialog, lands on the idea.
//   5. A beforeunload listener is registered iff the form is dirty. The
//      OS dialog is not drivable, and dispatching a synthetic beforeunload
//      tears down the Wails dev bridge, so listener registration is
//      tracked by wrapping window.addEventListener and compared against
//      the clean-state baseline (other code registers its own).
//   6. MarkdownReview: the same guard covers Cancel Review and
//      beforeunload once the human has edited.

const DIALOG = '[data-testid="confirm-dialog"]'
const KEEP = '[data-testid="confirm-dialog-cancel"]'
const DISCARD = '[data-testid="confirm-dialog-confirm"]'

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    const w = window as unknown as { __beforeUnloadListeners: Set<unknown> }
    const live = new Set<unknown>()
    w.__beforeUnloadListeners = live
    const add = window.addEventListener.bind(window)
    const remove = window.removeEventListener.bind(window)
    window.addEventListener = ((type: string, l: EventListenerOrEventListenerObject, o?: boolean | AddEventListenerOptions) => {
      if (type === 'beforeunload') live.add(l)
      return add(type, l, o)
    }) as typeof window.addEventListener
    window.removeEventListener = ((type: string, l: EventListenerOrEventListenerObject, o?: boolean | EventListenerOptions) => {
      if (type === 'beforeunload') live.delete(l)
      return remove(type, l, o)
    }) as typeof window.removeEventListener
  })
})

function beforeUnloadCount(page: Page): Promise<number> {
  return page.evaluate(
    () => (window as unknown as { __beforeUnloadListeners: Set<unknown> }).__beforeUnloadListeners.size,
  )
}

const dirtyEdits: Array<{ field: string; edit: (page: Page) => Promise<void> }> = [
  { field: 'name', edit: (p) => p.fill('input[type="text"]', 'Dirty name') },
  { field: 'summary', edit: (p) => p.fill('.idea-form textarea', 'Dirty summary') },
  { field: 'status', edit: (p) => p.selectOption('.idea-form select', 'active').then(() => undefined) },
]

test.describe('useDirtyGuard: IdeaForm', () => {
  test('clean form: Cancel navigates without a dialog', async ({ page }) => {
    await page.goto('/#/idea/new')
    await page.locator('button:has-text("Cancel")').click()
    await expect(page).toHaveURL(/#\/$/)
    await expect(page.locator(DIALOG)).toHaveCount(0)
  })

  for (const { field, edit } of dirtyEdits) {
    test(`dirty ${field}: Keep editing stays on the form with edits intact`, async ({ page }) => {
      await page.goto('/#/idea/new')
      // Only the field under test is edited, so each of the three dirty
      // conditions is shown to trigger the dialog by itself.
      await edit(page)
      const nameBefore = await page.inputValue('input[type="text"]')
      const summaryBefore = await page.inputValue('.idea-form textarea')
      const statusBefore = await page.inputValue('.idea-form select')

      await page.locator('button:has-text("Cancel")').click()
      await expect(page.locator(DIALOG)).toBeVisible()
      await page.locator(KEEP).click()

      await expect(page.locator(DIALOG)).toHaveCount(0)
      await expect(page).toHaveURL(/#\/idea\/new$/)
      await expect(page.locator('input[type="text"]')).toHaveValue(nameBefore)
      await expect(page.locator('.idea-form textarea')).toHaveValue(summaryBefore)
      await expect(page.locator('.idea-form select')).toHaveValue(statusBefore)
    })
  }

  test('dirty form: Discard navigates away and the edits are lost', async ({ page }) => {
    await page.goto('/#/idea/new')
    await page.fill('input[type="text"]', 'Throwaway')

    await page.locator('button:has-text("Cancel")').click()
    await expect(page.locator(DIALOG)).toBeVisible()
    await page.locator(DISCARD).click()

    await expect(page).toHaveURL(/#\/$/)
    await expect(page.locator('.idea-form')).toHaveCount(0)
    await page.goto('/#/idea/new')
    await expect(page.locator('input[type="text"]')).toHaveValue('')
  })

  test('save bypasses the guard: no dialog, lands on the new idea', async ({ page }) => {
    const name = `Dirty Guard Save ${Date.now()}`
    await page.goto('/#/idea/new')
    const clean = await beforeUnloadCount(page)
    await page.fill('input[type="text"]', name)
    await expect.poll(() => beforeUnloadCount(page)).toBe(clean + 1)
    await page.locator('button[type="submit"]').click()

    await expect(page.locator('.idea-detail-name')).toHaveText(name)
    await expect(page).toHaveURL(/#\/idea\/[^/]+/)
    await expect(page.locator(DIALOG)).toHaveCount(0)
    await expect.poll(() => beforeUnloadCount(page)).toBe(clean)
  })

  test('beforeunload listener is registered only while dirty', async ({ page }) => {
    await page.goto('/#/idea/new')
    const clean = await beforeUnloadCount(page)

    await page.fill('input[type="text"]', 'x')
    await expect.poll(() => beforeUnloadCount(page)).toBe(clean + 1)

    await page.fill('input[type="text"]', '')
    await expect.poll(() => beforeUnloadCount(page)).toBe(clean)
  })
})

test.describe('useDirtyGuard: MarkdownReview', () => {
  test.afterEach(async ({ page }) => {
    await cancelPendingReviews(page)
  })

  async function openReview(page: Page): Promise<string> {
    const file = path.join(os.tmpdir(), `ideate-dirty-${Date.now()}-${Math.floor(Math.random() * 1e6)}.md`)
    await fs.promises.writeFile(file, '# Original\n')
    await page.goto('/')
    const r = await callBinding<{ id: string }>(page, 'RequestMarkdownReview', file, '')
    await page.goto(`/#/review?reviewId=${r.id}`)
    await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })
    return r.id
  }

  test('clean review: Cancel Review skips the dialog', async ({ page }) => {
    await openReview(page)

    await page.locator('[data-testid="markdown-review-cancel-btn"]').click()
    await expect(page.locator('.review-status-badge.cancelled')).toBeVisible({ timeout: 10000 })
    await expect(page.locator(DIALOG)).toHaveCount(0)
  })

  test('edited review: Cancel Review prompts, Keep editing preserves, Discard cancels', async ({ page }) => {
    const id = await openReview(page)
    const clean = await beforeUnloadCount(page)

    await page.locator('[data-testid="markdown-review-mode-toggle"]').click()
    await page.locator('[data-testid="markdown-review-source"]').fill('# Original\n\nmy feedback\n')
    await expect.poll(() => beforeUnloadCount(page)).toBe(clean + 1)

    const cancelBtn = page.locator('[data-testid="markdown-review-cancel-btn"]')
    await cancelBtn.click()
    await expect(page.locator(DIALOG)).toBeVisible()
    await page.locator(KEEP).click()
    await expect(page.locator(DIALOG)).toHaveCount(0)
    await expect(cancelBtn).toBeVisible()
    await expect(page.locator('[data-testid="markdown-review-source"]')).toHaveValue('# Original\n\nmy feedback\n')
    const pending = await callBinding<{ status: string }>(page, 'GetReview', id)
    expect(pending.status).toBe('pending')

    await cancelBtn.click()
    await page.locator(DISCARD).click()
    await expect(page.locator('.review-status-badge.cancelled')).toBeVisible({ timeout: 10000 })
    const cancelled = await callBinding<{ status: string }>(page, 'GetReview', id)
    expect(cancelled.status).toBe('cancelled')
    await expect.poll(() => beforeUnloadCount(page)).toBe(clean)
  })
})
