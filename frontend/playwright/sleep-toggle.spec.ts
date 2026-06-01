import { test, expect } from '@playwright/test'

test.describe('Sleep toggle', () => {
  test('defaults to disabled and toggles on click', async ({ page }) => {
    await page.goto('/')
    const toggle = page.locator('.app-sleep-toggle')
    await expect(toggle).toBeVisible()

    // Default state on every app start: disabled (off).
    await expect(toggle).toHaveAttribute('data-state', 'disabled')
    await expect(toggle).toHaveAttribute('aria-pressed', 'false')
    await expect(toggle).toHaveClass(/disabled/)

    // Click flips the toggle to "enabled but idle" — no busy session
    // exists in a fresh dashboard, so the OS assertion isn't held.
    await toggle.click()
    await expect(toggle).toHaveAttribute('data-state', 'idle')
    await expect(toggle).toHaveAttribute('aria-pressed', 'true')
    await expect(toggle).toHaveClass(/idle/)
    await expect(toggle).not.toHaveClass(/held/)

    // Click again — back to disabled.
    await toggle.click()
    await expect(toggle).toHaveAttribute('data-state', 'disabled')
    await expect(toggle).toHaveAttribute('aria-pressed', 'false')
  })

  test('hover title teaches the off/idle/active states', async ({ page }) => {
    await page.goto('/')
    const toggle = page.locator('.app-sleep-toggle')

    // Off state — title explains what enabling does.
    await expect(toggle).toHaveAttribute(
      'title',
      /Prevent sleep: off/,
    )
    await expect(toggle).toHaveAttribute('title', /Click to enable/)

    await toggle.click()

    // On + idle — title explains the held trigger.
    await expect(toggle).toHaveAttribute('title', /Prevent sleep: on \(idle\)/)
    await expect(toggle).toHaveAttribute('title', /when a session becomes active/)
  })
})
