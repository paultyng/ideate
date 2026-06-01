import { test, expect } from '@playwright/test'
import * as fs from 'fs'
import * as path from 'path'

const REPO_PATH = process.env.TEST_DIFF_REPO || process.cwd().replace('/frontend', '')
const REVIEWS_DIR = process.env.TEST_REVIEWS_DIR
  || path.join(process.env.IDEATE_CONFIG_DIR || path.join(REPO_PATH, '.ideate-dev'), 'reviews')
const IDEAS_DIR = process.env.TEST_IDEAS_DIR
  || path.join(REPO_PATH, '.ideate-dev', 'ideas')

interface SeedRecord {
  id: string
  kind: 'markdown'
  status: 'pending'
  created: string
  // JSON tag is `idea_slug` (snake_case) on the Go side — the
  // wails-generated `ideaSlug` is a property-name choice that ONLY
  // affects the binding's TS shape; the on-disk record uses the Go
  // tag. Don't conflate the two.
  idea_slug: string
  markdown: { path: string; original: string }
}

function seedReview(rec: SeedRecord): void {
  fs.mkdirSync(REVIEWS_DIR, { recursive: true, mode: 0o700 })
  fs.writeFileSync(path.join(REVIEWS_DIR, `${rec.id}.json`),
    JSON.stringify(rec, null, 2) + '\n', { mode: 0o600 })
}

function unlinkReview(id: string): void {
  try { fs.unlinkSync(path.join(REVIEWS_DIR, `${id}.json`)) } catch { /* ignore */ }
}

test.describe('IdeaDetail pending-review indicator', () => {
  const reviewID = 'playwright-pending-review-indicator'

  test.afterEach(() => unlinkReview(reviewID))

  test('shows a dot next to a file with a pending markdown review', async ({ page }) => {
    const name = `Review Indicator ${Date.now()}`
    await page.goto('/')

    const slug = await page.evaluate(async (n) => {
      // @ts-expect-error wails binding
      return (await window.go.app.App.CreateIdea(n, 'active', '')) as string
    }, name)

    // Seed a context.md so the idea sidebar lists more than idea.md.
    const ideaDir = path.join(IDEAS_DIR, slug)
    fs.writeFileSync(path.join(ideaDir, 'context.md'), '# Context\n\nseed.\n')

    // Seed a pending markdown review keyed to this idea + file.
    seedReview({
      id: reviewID,
      kind: 'markdown',
      status: 'pending',
      created: new Date().toISOString(),
      idea_slug: slug,
      markdown: {
        path: path.join(ideaDir, 'context.md'),
        original: '# Context\n\nseed.\n',
      },
    })

    await page.goto(`/#/idea/${slug}`)

    // The context.md row should render with the pending-review indicator.
    const contextRow = page.locator('.idea-sidebar-item', { hasText: 'context.md' })
    await expect(contextRow).toBeVisible({ timeout: 10000 })
    await expect(
      contextRow.locator('.idea-file-review-pending'),
    ).toBeVisible({ timeout: 10000 })
    await expect(
      contextRow.locator('.idea-file-review-pending'),
    ).toHaveAttribute('title', 'Pending markdown review')

    // idea.md should NOT have an indicator — only context.md was reviewed.
    const ideaRow = page.locator('.idea-sidebar-item', { hasText: 'idea.md' })
    await expect(ideaRow.locator('.idea-file-review-pending')).toHaveCount(0)
  })
})
