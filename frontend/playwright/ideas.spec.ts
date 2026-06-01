import { test, expect } from '@playwright/test'

test.describe('Ideas', () => {
  test('renders idea list with new idea topbar button', async ({ page }) => {
    await page.goto('/')
    // Home view has no h1 anymore — anchor on the topbar Home button
    // + the dashboard container, which together identify the route.
    await expect(page.locator('button[aria-label="Home"]')).toBeVisible()
    await expect(page.locator('.dashboard')).toBeVisible()
    // "New Idea" affordance lives in the topbar now, not on the dashboard.
    await expect(page.locator('button[aria-label="New idea"]')).toBeVisible()
  })

  test('create idea flow', async ({ page }) => {
    await page.goto('/')

    // Navigate to new idea form
    await page.click('button[aria-label="New idea"]')
    await expect(page.locator('h1')).toHaveText('New Idea')

    // Fill form
    await page.fill('input[type="text"]', 'Test Idea')
    await page.selectOption('.idea-form select', 'active')
    await page.fill('textarea', 'A test idea summary')

    // Submit
    await page.click('button[type="submit"]')

    // Should navigate to idea detail
    await expect(page.locator('.idea-detail-name')).toHaveText('Test Idea')

    // Sidebar should show idea.md
    await expect(page.locator('.idea-sidebar')).toBeVisible()
    await expect(page.locator('.idea-sidebar')).toContainText('idea.md')

    // Main panel should show the summary content
    await expect(page.locator('.idea-main')).toContainText('A test idea summary')
  })

  test('cancel from new idea returns to list', async ({ page }) => {
    await page.goto('/')
    await page.click('button[aria-label="New idea"]')
    await expect(page.locator('h1')).toHaveText('New Idea')
    await page.click('.btn-secondary')
    await expect(page.locator('.dashboard')).toBeVisible()
    await expect(page.locator('button[aria-label="New idea"]')).toBeVisible()
  })

  test('cancel with dirty new-idea form prompts before discarding', async ({ page }) => {
    await page.goto('/#/idea/new')
    await expect(page.locator('h1')).toHaveText('New Idea')
    await page.fill('input[type="text"]', 'Dirty draft')

    // Confirming the discard prompt — nav proceeds. Backed by the
    // in-app React confirm modal, not window.confirm (WKWebView no-op).
    await page.click('.btn-secondary')
    await page.locator('[data-testid="confirm-dialog-confirm"]').click()
    // Home view has no h1 anymore — anchor on the topbar Home button
    // + the dashboard container, which together identify the route.
    await expect(page.locator('button[aria-label="Home"]')).toBeVisible()
    await expect(page.locator('.dashboard')).toBeVisible()
  })

  test('cancel with dirty new-idea form stays put when discard is declined', async ({ page }) => {
    await page.goto('/#/idea/new')
    await expect(page.locator('h1')).toHaveText('New Idea')
    await page.fill('input[type="text"]', 'Stays put')

    // Dismissing the discard prompt — form stays.
    await page.click('.btn-secondary')
    await page.locator('[data-testid="confirm-dialog-cancel"]').click()
    await expect(page.locator('h1')).toHaveText('New Idea')
    await expect(page.locator('input[type="text"]')).toHaveValue('Stays put')
  })

  test('idea appears in list after creation', async ({ page }) => {
    // Create an idea first
    await page.goto('/#/idea/new')
    await page.fill('input[type="text"]', 'Listed Idea')
    await page.selectOption('.idea-form select', 'paused')
    await page.click('button[type="submit"]')
    await expect(page.locator('.idea-detail-name')).toBeVisible()

    // Go back to list (Home button in the topbar — idea-detail's nav
    // button now jumps to a session, not back to the dashboard).
    await page.click('.app-home')
    await expect(page.locator('.idea-card-name')).toContainText(['Listed Idea'])
  })

  test('idea detail shows sidebar and history panel', async ({ page }) => {
    // Create an idea
    await page.goto('/#/idea/new')
    await page.fill('input[type="text"]', 'Detail Idea')
    await page.click('button[type="submit"]')
    await expect(page.locator('.idea-detail-name')).toBeVisible()

    // Check sidebar exists
    await expect(page.locator('.idea-sidebar')).toBeVisible()

    // Check history panel exists (collapsed)
    await expect(page.locator('.history-panel')).toBeVisible()
    await expect(page.locator('.history-panel-header')).toContainText('History')

    // Expand history
    await page.click('.history-panel-header')
    await expect(page.locator('.history-panel.expanded')).toBeVisible()
  })

  test('archived ideas hidden by default with toggle', async ({ page }) => {
    // Unique-suffixed names so the substring matchers below don't
    // collide with ideas other tests in the suite have already
    // created. The single-worker shared store accumulates entries
    // across the run.
    const stamp = Date.now()
    const archivedName = `Archived Idea ${stamp}`
    const activeName = `Active Idea ${stamp}`

    // Land on the dashboard then create both ideas via the Wails
    // binding directly — the IdeaForm's submit-then-navigate flow
    // races with chained page.goto('/#/idea/new') calls when the
    // ideas list has accumulated entries from earlier tests in the
    // run, occasionally leaving the second goto stuck on the first
    // idea's detail page. Direct App.CreateIdea matches the pattern
    // used in dashboard / orchestrator tests and is faster.
    await page.goto('/')
    await page.evaluate(async ({ archivedName, activeName }) => {
      // @ts-expect-error wails binding
      await window.go.app.App.CreateIdea(archivedName, 'archived', '')
      // @ts-expect-error wails binding
      await window.go.app.App.CreateIdea(activeName, 'active', '')
    }, { archivedName, activeName })
    // Force a re-render so the dashboard picks up the new ideas.
    await page.reload()
    await expect(page.locator('button[aria-label="Home"]')).toBeVisible()
    await expect(page.locator('.dashboard')).toBeVisible()

    // Archived idea should not be visible in the main list
    const cards = page.locator('.idea-card-name')
    await expect(cards.filter({ hasText: activeName })).toHaveCount(1)
    await expect(cards.filter({ hasText: archivedName })).toHaveCount(0)

    // Toggle should be present
    const toggle = page.locator('.btn-toggle-archived')
    await expect(toggle).toBeVisible()
    await expect(toggle).toContainText('Show archived')

    // Click toggle to show archived
    await toggle.click()
    await expect(toggle).toContainText('Hide archived')
    const archivedNow = page.locator('.idea-card-name').filter({ hasText: archivedName })
    await expect(archivedNow).toBeVisible()

    // Click toggle to hide again
    await toggle.click()
    await expect(toggle).toContainText('Show archived')
    await expect(archivedNow).not.toBeVisible()
  })
})
