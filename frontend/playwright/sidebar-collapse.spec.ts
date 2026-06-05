import { test, expect } from '@playwright/test'

async function createIdea(page: import('@playwright/test').Page, name: string): Promise<string> {
  await page.goto('/#/idea/new')
  await page.fill('input[type="text"]', name)
  await page.selectOption('.idea-form select', 'active')
  await page.click('button[type="submit"]')
  await expect(page.locator('.idea-detail-name')).toHaveText(name)
  return page.url().split('/idea/')[1].split('/')[0].split('?')[0]
}

async function startAndEndSession(page: import('@playwright/test').Page) {
  await page.click('.idea-sidebar .btn-small[aria-label="Start a new session"]')
  await page.selectOption('.session-start select', 'testagent')
  await page.click('button:has-text("Start Session")')
  await page.waitForSelector('.terminal-container', { timeout: 10000 })
  // Drive /exit explicitly — TESTAGENT_AUTO_EXIT is 30s (keeps the
  // orchestrator-driven sessions alive long enough for MCP-mediated
  // writes), which the 10s status-flip wait below would race.
  // Wait for the banner first so Bubbletea has put stdin in raw mode.
  await page.waitForFunction(
    () => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const reg = (window as any).__ideateTerminals as
        | Record<string, { buffer: { active: { length: number; getLine: (i: number) => { translateToString: (trim: boolean) => string } | undefined } } }>
        | undefined
      if (!reg) return false
      for (const term of Object.values(reg)) {
        for (let i = 0; i < term.buffer.active.length; i++) {
          if (term.buffer.active.getLine(i)?.translateToString(true).includes('mcp connected:')) return true
        }
      }
      return false
    },
    { timeout: 15000 },
  )
  await page.locator('.terminal-container').click()
  await page.keyboard.type('/exit', { delay: 50 })
  await page.keyboard.press('Enter')
  await expect(page.locator('.session-toolbar-status, .status-badge'))
    .toContainText(/exited|stopped|completed/, { timeout: 10000 })
}

test.describe('Sessions sidebar section collapse', () => {
  test('expanded by default when no sessions exist', async ({ page }) => {
    await createIdea(page, 'Empty Sessions Collapse Test')

    const title = page.locator('.sessions-section .idea-sidebar-section-title')
    await expect(title).toHaveAttribute('aria-expanded', 'true')
    await expect(page.locator('.idea-sidebar-empty')).toContainText('No sessions yet')
  })

  test('collapsed by default once a session exists', async ({ page }) => {
    const slug = await createIdea(page, 'Collapsed Default Test')

    await startAndEndSession(page)

    await page.click('.btn-back')
    await expect(page.locator('.idea-detail-name')).toHaveText('Collapsed Default Test')

    const title = page.locator('.sessions-section .idea-sidebar-section-title')
    await expect(title).toHaveAttribute('aria-expanded', 'false', { timeout: 10000 })
    // Count still shown in title.
    await expect(title).toContainText(/Sessions \(1\)/)
    // Items are present in DOM but their wrapper is hidden.
    await expect(page.locator('.sessions-section-body')).toHaveAttribute('hidden', '')
    // Confirm slug is the one we created (use it so lint doesn't trip).
    expect(slug).toBeTruthy()
  })

  test('clicking section title toggles and persists across reloads', async ({ page }) => {
    const slug = await createIdea(page, 'Toggle Persist Test')

    await startAndEndSession(page)
    await page.click('.btn-back')

    const title = page.locator('.sessions-section .idea-sidebar-section-title')
    await expect(title).toHaveAttribute('aria-expanded', 'false', { timeout: 10000 })

    // Expand.
    await title.click()
    await expect(title).toHaveAttribute('aria-expanded', 'true')
    await expect(page.locator('.idea-sidebar-item.session')).toBeVisible()

    // Reload — preference persists.
    await page.reload()
    await expect(page.locator('.sessions-section .idea-sidebar-section-title'))
      .toHaveAttribute('aria-expanded', 'true', { timeout: 10000 })
    await expect(page.locator('.idea-sidebar-item.session')).toBeVisible()

    // Collapse and reload — persists.
    await page.click('.sessions-section .idea-sidebar-section-title')
    await expect(page.locator('.sessions-section .idea-sidebar-section-title'))
      .toHaveAttribute('aria-expanded', 'false')
    await page.reload()
    await expect(page.locator('.sessions-section .idea-sidebar-section-title'))
      .toHaveAttribute('aria-expanded', 'false', { timeout: 10000 })

    expect(slug).toBeTruthy()
  })

  test('+ button is reachable from collapsed section without expanding it first', async ({ page }) => {
    await createIdea(page, 'Collapsed Plus Reach Test')

    await startAndEndSession(page)
    await page.click('.btn-back')

    const title = page.locator('.sessions-section .idea-sidebar-section-title')
    await expect(title).toHaveAttribute('aria-expanded', 'false', { timeout: 10000 })

    // Click + while collapsed — clicking the button should not toggle the section
    // (e.stopPropagation), it should navigate to the new-session form.
    await page.click('.idea-sidebar .btn-small[aria-label="Start a new session"]')
    await expect(page.locator('h1, .idea-detail-name')).toContainText(/New Session/, { timeout: 5000 })
  })
})
