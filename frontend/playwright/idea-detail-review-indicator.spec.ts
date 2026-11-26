import { test, expect } from '@playwright/test'
import * as fs from 'fs'
import * as path from 'path'
import { callBinding, cancelPendingReviews } from './backend'

const REPO_PATH = process.env.TEST_DIFF_REPO || process.cwd().replace('/frontend', '')
const IDEAS_DIR = process.env.TEST_IDEAS_DIR
  || path.join(REPO_PATH, '.ideate-dev', 'ideas')

test.describe('IdeaDetail pending-review indicator', () => {
  test.afterEach(async ({ page }) => {
    await cancelPendingReviews(page)
  })

  test('shows a dot next to a file with a pending markdown review', async ({ page }) => {
    const name = `Review Indicator ${Date.now()}`
    await page.goto('/')

    const slug = await callBinding<string>(page, 'CreateIdea', name, 'active', '')

    // Seed a context.md so the idea sidebar lists more than idea.md.
    const ideaDir = path.join(IDEAS_DIR, slug)
    fs.writeFileSync(path.join(ideaDir, 'context.md'), '# Context\n\nseed.\n')

    await page.goto(`/#/idea/${slug}`)
    const contextRow = page.locator('.idea-sidebar-item', { hasText: 'context.md' })
    await expect(contextRow).toBeVisible({ timeout: 10000 })
    await expect(contextRow.locator('.idea-file-review-pending')).toHaveCount(0)

    // Create the review through the real binding so the review:changed
    // event drives the indicator while the page is already mounted.
    await callBinding(page, 'RequestMarkdownReview', path.join(ideaDir, 'context.md'), slug)

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
