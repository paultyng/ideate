import { test, expect, type Page } from '@playwright/test'

// Capture BrowserOpenURL calls into window.__browserOpens for assertion.
// Wails injects window.runtime at page load (after addInitScript runs), so
// we patch *after* the page is up. SPA-internal navigation (HashRouter)
// doesn't reload, so a single patch survives the rest of the test.
async function applyStub(page: Page) {
  await page.waitForFunction(() => {
    const w = window as unknown as Record<string, unknown>
    return Boolean((w.runtime as Record<string, unknown> | undefined)?.BrowserOpenURL)
  }, { timeout: 10000 })
  await page.evaluate(() => {
    const w = window as unknown as Record<string, unknown>
    w.__browserOpens = (w.__browserOpens as string[]) ?? []
    const runtime = w.runtime as Record<string, unknown>
    runtime.BrowserOpenURL = (url: string) => {
      ;(w.__browserOpens as string[]).push(url)
    }
  })
}

async function stubBrowserOpenURL(page: Page) {
  await page.goto('/')
  await applyStub(page)
}

async function readOpens(page: Page): Promise<string[]> {
  return await page.evaluate(() => {
    const w = window as unknown as Record<string, unknown>
    return [...((w.__browserOpens as string[]) || [])]
  })
}

async function createIdeaWithSummary(page: Page, name: string, summary: string): Promise<string> {
  await page.goto('/#/idea/new')
  await page.fill('input[type="text"]', name)
  await page.selectOption('.idea-form select', 'active')
  await page.fill('textarea', summary)
  await page.click('button[type="submit"]')
  await expect(page.locator('.idea-detail-name')).toHaveText(name)
  return page.url().split('/idea/')[1].split('/')[0].split('?')[0]
}

test.describe('Link handling', () => {
  test.beforeEach(async ({ page }) => {
    await stubBrowserOpenURL(page)
  })

  test('absolute https link in markdown opens via BrowserOpenURL', async ({ page }) => {
    await createIdeaWithSummary(
      page,
      'External Link Test',
      'Visit [example](https://example.com/page) for details.',
    )
    const link = page.locator('.idea-main a', { hasText: 'example' })
    await expect(link).toBeVisible({ timeout: 10000 })
    await link.click()

    expect(await readOpens(page)).toEqual(['https://example.com/page'])
  })

  test('javascript: link is blocked by the scheme allow-list', async ({ page }) => {
    await createIdeaWithSummary(
      page,
      'XSS Allowlist Test',
      'Click [evil](javascript:alert(1)) to trigger.',
    )
    const link = page.locator('.idea-main a', { hasText: 'evil' })
    await expect(link).toBeVisible({ timeout: 10000 })

    let dialogFired = false
    page.on('dialog', () => { dialogFired = true })
    await link.click()
    await page.waitForTimeout(200)

    expect(await readOpens(page)).toEqual([])
    expect(dialogFired).toBe(false)
  })
})
