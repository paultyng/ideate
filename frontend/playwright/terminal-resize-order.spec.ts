import { test, expect, type Page } from '@playwright/test'
import {
  AGENT_LIFECYCLE_TIMEOUT_MS,
  readSessionReplay,
  stopAllRunningSessions,
  waitForAgentReady,
} from './ptyCapture'
import { spyBinding, getBindingCalls, evalTerminals, waitForTerminals, getMountedSessionId } from './backend'

async function createIdea(page: Page, name: string): Promise<string> {
  await page.goto('/#/idea/new')
  await page.fill('input[type="text"]', name)
  await page.selectOption('.idea-form select', 'active')
  await page.click('button[type="submit"]')
  await expect(page.locator('.idea-detail-name')).toHaveText(name)
  return page.url().split('/idea/')[1].split('/')[0].split('?')[0]
}

async function startSession(page: Page, slug: string): Promise<string> {
  await page.goto(`/#/idea/${slug}/session/new`)
  await page.selectOption('.session-start select', 'testagent')
  await page.click('button:has-text("Start Session")')
  await page.waitForSelector('.xterm-screen', { timeout: 10000 })
  const uuid = await getMountedSessionId(page)
  await waitForAgentReady(page, uuid)
  return uuid
}

async function readResizeCalls(page: Page): Promise<Array<{ id: string; rows: number; cols: number }>> {
  const calls = await getBindingCalls<[string, number, number]>(page, 'ResizeSession')
  return calls.map(([id, rows, cols]) => ({ id, rows, cols }))
}

async function setHash(page: Page, hash: string) {
  await page.evaluate((h) => { window.location.hash = h }, hash)
}

async function waitForMounted(page: Page, id: string, mounted: boolean) {
  await waitForTerminals(
    page,
    (reg, { id, mounted }: { id: string; mounted: boolean }) => Boolean(reg?.[id]) === mounted,
    { id, mounted },
    { timeout: 10000 },
  )
}

// testagent exits after 30s without a write; a space+backspace keeps it alive.
async function keepAlive(page: Page, id: string) {
  await evalTerminals(page, (regAny, id: string) => {
    const reg = regAny as Record<string, { input(d: string, w: boolean): void }>
    reg[id].input(' ', true)
    reg[id].input('\x7f', true)
  }, id)
}

test.describe('Terminal resize and write ordering', () => {
  test.afterEach(async ({ page }) => {
    await stopAllRunningSessions(page)
  })

  test('no tiny ResizeSession while navigating away and back; real resizes still sent', async ({ page }) => {
    const slug = await createIdea(page, 'resize-on-unmount')
    const uuid = await startSession(page, slug)
    const sessionHash = new URL(page.url()).hash

    await spyBinding(page, 'ResizeSession')

    for (let i = 0; i < 20; i++) {
      await setHash(page, '/')
      await waitForMounted(page, uuid, false)
      await setHash(page, sessionHash.replace(/^#/, ''))
      await waitForMounted(page, uuid, true)
      if (i % 5 === 4) await keepAlive(page, uuid)
    }

    const calls = await readResizeCalls(page)
    expect(calls.length).toBeGreaterThan(0)
    expect(calls.filter((c) => c.rows < 5 || c.cols < 10)).toEqual([])
    await waitForAgentReady(page, uuid)

    const before = calls.length
    const vp = page.viewportSize()!
    await page.setViewportSize({ width: vp.width - 200, height: vp.height - 150 })
    await expect.poll(async () => {
      const now = await readResizeCalls(page)
      return now.slice(before).filter((c) => c.rows >= 5 && c.cols >= 10).length
    }, { timeout: 5000 }).toBeGreaterThan(0)
  })

  test('keystrokes sent back to back reach the PTY in order', async ({ page }) => {
    const slug = await createIdea(page, 'write-order')
    const uuid = await startSession(page, slug)

    const alphabet = 'abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ'
    let probe = ''
    for (let i = 0; i < 60; i++) probe += alphabet[(i * 7) % alphabet.length]
    expect(new Set(probe).size).toBe(60)

    await evalTerminals(page, (regAny, { id, s }: { id: string; s: string }) => {
      const reg = regAny as Record<string, { input(d: string, w: boolean): void }>
      for (const ch of s) reg[id].input(ch, true)
    }, { id: uuid, s: probe })

    await expect.poll(async () => (await readSessionReplay(page, uuid)).replace(/\n/g, ''), {
      timeout: AGENT_LIFECYCLE_TIMEOUT_MS,
    }).toContain(probe)
  })
})
