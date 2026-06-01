import { test, expect } from '@playwright/test'
import { promises as fs } from 'fs'
import * as path from 'path'

const projectsDir = process.env.TEST_CLAUDE_PROJECTS_DIR || ''
const ideasDir = process.env.TEST_IDEAS_DIR || ''

// Encodes a working directory the way Claude does: replace each "/" with
// "-". Mirrors claudefmt.EncodeProjectDir in Go.
function encodeProjectDir(absCwd: string): string {
  return absCwd.split('/').join('-')
}

async function createIdea(page: import('@playwright/test').Page, name: string): Promise<string> {
  await page.goto('/#/idea/new')
  await page.fill('input[type="text"]', name)
  await page.selectOption('.idea-form select', 'active')
  await page.click('button[type="submit"]')
  await expect(page.locator('.idea-detail-name')).toHaveText(name)
  return page.url().split('/idea/')[1].split('/')[0].split('?')[0]
}

async function startAndEndTestagent(page: import('@playwright/test').Page) {
  await page.click('.idea-sidebar .btn-small[aria-label="Start a new session"]')
  await page.selectOption('.session-start select', 'testagent')
  await page.click('button:has-text("Start Session")')
  await page.waitForSelector('.terminal-container', { timeout: 10000 })
  // Drive /exit explicitly — TESTAGENT_AUTO_EXIT is 30s, which the
  // 10s status-flip wait below would race. Wait for the banner first
  // so Bubbletea has put stdin in raw mode.
  await page.waitForFunction(
    () => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const reg = (window as any).__ideateTerminals as
        | Record<string, { buffer: { active: { length: number; getLine: (i: number) => { translateToString: (trim: boolean) => string } | undefined } } }>
        | undefined
      if (!reg) return false
      for (const term of Object.values(reg)) {
        for (let i = 0; i < term.buffer.active.length; i++) {
          if (term.buffer.active.getLine(i)?.translateToString(true).includes('/help for commands')) return true
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

async function expandSessions(page: import('@playwright/test').Page) {
  const title = page.locator('.sessions-section .idea-sidebar-section-title')
  if ((await title.getAttribute('aria-expanded')) === 'false') {
    await title.click()
  }
}

async function runSync(page: import('@playwright/test').Page) {
  await page.evaluate(async () => {
    // @ts-expect-error wails runtime binding (dev build only)
    await window.go.app.App.RunClaudeSync()
  })
}

async function writeTranscript(slug: string, sessUUID: string, entrypoint: string) {
  const ideaPath = path.join(ideasDir, slug)
  const encDir = path.join(projectsDir, encodeProjectDir(ideaPath))
  await fs.mkdir(encDir, { recursive: true })
  const ts = new Date().toISOString()
  const line = JSON.stringify({
    type: 'user',
    sessionId: sessUUID,
    cwd: ideaPath,
    entrypoint,
    timestamp: ts,
  })
  await fs.writeFile(path.join(encDir, sessUUID + '.jsonl'), line + '\n')
  return path.join(encDir, sessUUID + '.jsonl')
}

test.describe('Claude transcript sync', () => {
  test.skip(!projectsDir || !ideasDir, 'TEST_CLAUDE_PROJECTS_DIR / TEST_IDEAS_DIR not set')

  test('cli transcript is ingested as a claude-code session', async ({ page }) => {
    const slug = await createIdea(page, 'Sync Ingest Test')
    const sessUUID = '11111111-2222-3333-4444-555555555555'
    await writeTranscript(slug, sessUUID, 'cli')

    await runSync(page)
    await page.reload()
    await expandSessions(page)

    await expect(page.locator('.idea-sidebar-item.session')).toHaveCount(1, { timeout: 10000 })
    await expect(page.locator('.session-agent')).toContainText('Claude')
  })

  test('idempotency: 5 syncs after a testagent run keep the count at 1', async ({ page }) => {
    const slug = await createIdea(page, 'Sync Idempotency Test')
    await startAndEndTestagent(page)

    await page.click('.btn-back')
    await expandSessions(page)
    await expect(page.locator('.idea-sidebar-item.session')).toHaveCount(1, { timeout: 10000 })

    for (let i = 0; i < 5; i++) {
      await runSync(page)
    }
    await page.reload()
    await expandSessions(page)
    await expect(page.locator('.idea-sidebar-item.session')).toHaveCount(1, { timeout: 10000 })
    expect(slug).toBeTruthy()
  })

  test('orphan: deleting an ingested transcript flips it to orphaned', async ({ page }) => {
    const slug = await createIdea(page, 'Sync Orphan Test')
    const sessUUID = '22222222-3333-4444-5555-666666666666'
    const jsonlPath = await writeTranscript(slug, sessUUID, 'cli')

    // First sync ingests as claude-code.
    await runSync(page)
    await page.goto(`/#/idea/${slug}`)
    await expandSessions(page)
    await expect(page.locator('.idea-sidebar-item.session')).toHaveCount(1, { timeout: 10000 })

    // Delete + re-sync — orphan sweep should flip the record.
    await fs.rm(jsonlPath, { force: true })
    await runSync(page)
    await page.reload()
    await expandSessions(page)
    await expect(page.locator('.session-icon.orphaned')).toBeVisible({ timeout: 5000 })
  })

  test('non-interactive sdk-cli transcripts are not ingested', async ({ page }) => {
    const slug = await createIdea(page, 'Sync NonInteractive Test')
    const sessUUID = '33333333-4444-5555-6666-777777777777'
    await writeTranscript(slug, sessUUID, 'sdk-cli')

    await runSync(page)
    await page.reload()
    // Section auto-expands when empty — assert the empty state remains.
    await expect(page.locator('.idea-sidebar-empty')).toContainText('No sessions yet')
  })

  test('subagent transcripts are not ingested', async ({ page }) => {
    const slug = await createIdea(page, 'Sync Subagent Test')

    // Subagent transcripts live at <encoded>/<parent-uuid>/subagents/agent-*.jsonl
    const ideaPath = path.join(ideasDir, slug)
    const subDir = path.join(
      projectsDir,
      encodeProjectDir(ideaPath),
      '44444444-5555-6666-7777-888888888888',
      'subagents',
    )
    await fs.mkdir(subDir, { recursive: true })
    const ts = new Date().toISOString()
    await fs.writeFile(
      path.join(subDir, 'agent-deadbeef.jsonl'),
      JSON.stringify({ type: 'user', sessionId: 'sub', cwd: ideaPath, entrypoint: 'cli', timestamp: ts }) + '\n',
    )

    await runSync(page)
    await page.reload()
    await expect(page.locator('.idea-sidebar-empty')).toContainText('No sessions yet')
  })
})
