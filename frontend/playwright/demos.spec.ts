import { test, expect, Page } from '@playwright/test'
import { execFileSync } from 'child_process'
import * as fs from 'fs'
import * as path from 'path'
import { waitForAgentReady } from './ptyCapture'

// Demo specs produce the README's animated GIFs. Each test name maps
// 1:1 to a `docs/media/<name>.gif` output via `task generate:demos`,
// which runs Playwright with video recording on and pipes the resulting
// WebMs through ffmpeg.
//
// Tests in this file are NOT correctness checks for the app — they're
// scripted interactions tuned for the GIF rendering. Keep them short
// (target ≤6s of interaction including final hold), deterministic, and
// driven via the same seeded data the README screenshots use.

const REPO_PATH = process.env.TEST_DIFF_REPO || process.cwd().replace('/frontend', '')

const REVIEWS_DIR = process.env.TEST_REVIEWS_DIR
  || path.join(process.env.IDEATE_CONFIG_DIR || path.join(REPO_PATH, '.ideate-dev'), 'reviews')

function runSeedtest(): void {
  const ideasDir = process.env.TEST_IDEAS_DIR
  if (!ideasDir) {
    throw new Error('runSeedtest requires TEST_IDEAS_DIR (set by task test:ui).')
  }
  const configDir = path.dirname(ideasDir)
  execFileSync('go', ['run', './cmd/seedtest'], {
    cwd: REPO_PATH,
    env: { ...process.env, IDEATE_IDEAS_DIR: ideasDir, IDEATE_CONFIG_DIR: configDir },
    stdio: 'inherit',
  })
}

// holdFinalFrame parks the recording on its closing state long enough
// for the GIF's loop to feel like a natural pause before restart.
async function holdFinalFrame(page: Page, ms = 800): Promise<void> {
  await page.waitForTimeout(ms)
}

// Each entry is one "agent turn" written into the orchestrator's xterm
// in sequence with a pause between, producing a typewriter-on-turn feel
// in the recorded GIF (rather than the static-screenshot pattern of
// dumping the entire transcript at once). Lines reference seeded ideas
// from cmd/seedtest/main.go so the dashboard and the drawer tell the
// same story.
const HERO_TURNS: string[] = [
  '\r\n' +
  '\x1b[32m>\x1b[0m What\'s open on the latency regression?\r\n' +
  '\r\n' +
  '  \x1b[2m⏺ get_idea(slug=p99-latency-regression-in-search)\x1b[0m\r\n' +
  '  \x1b[2m⎿ active · 1 backlog · 2 resources (Grafana, #search-incident)\x1b[0m\r\n' +
  '\r\n',

  '\x1b[32m>\x1b[0m Spin up a session on the OIDC migration too.\r\n' +
  '\r\n' +
  '  \x1b[2m⏺ goto_idea(slug=migrate-auth-service-to-oidc)  →  starts session\x1b[0m\r\n' +
  '\r\n',

  '\x1b[32m>\x1b[0m Open a markdown review on the vector-DB notes.\r\n' +
  '\r\n' +
  '  \x1b[2m⏺ request_markdown_review(slug=evaluate-vector-db,\x1b[0m\r\n' +
  '  \x1b[2m                          path="notes/bake-off.md")\x1b[0m\r\n' +
  '\x1b[32m✓\x1b[0m delivered.\r\n' +
  '\r\n',

  '\x1b[32m>\x1b[0m File a follow-up on the rate-limiter algorithm choice.\r\n' +
  '\r\n' +
  '  \x1b[2m⏺ add_backlog_item_by_slug(slug=public-api-rate-limiting,\x1b[0m\r\n' +
  '  \x1b[2m                            title="Pick the algorithm: GCRA vs token bucket")\x1b[0m\r\n' +
  '\x1b[32m✓\x1b[0m queued.\r\n',
]

// LATENCY_HERO_TURNS pre-populates the latency idea's testagent
// session before the quick-switch demo navigates into it. Story arc:
// triage open work → reproduce + bisect → file the finding. When the
// user Cmd+K's into the session, the terminal mounts and vscreen
// replays this content, so the GIF lands on a populated session
// rather than an empty boot screen.
const LATENCY_HERO_TURNS: string[] = [
  '\r\n' +
  '\x1b[32m>\x1b[0m Where are we on the p99 search latency regression?\r\n' +
  '\r\n' +
  '  \x1b[2m⏺ list_backlog(status=["open","in_progress"])\x1b[0m\r\n' +
  '  \x1b[2m⎿ 1 open: "Bisect: which deploy introduced the regression?"\x1b[0m\r\n' +
  '\r\n',

  '\x1b[32m>\x1b[0m Diff between last green and first red deploy.\r\n' +
  '\r\n' +
  '  \x1b[2m⏺ Bash(git log --oneline search@a3f2c19..search@b4ee8a1)\x1b[0m\r\n' +
  '  \x1b[2m⎿ b4ee8a1 search: switch ranker to BERT cross-encoder\x1b[0m\r\n' +
  '  \x1b[2m⎿ 8c12d4f search: cache query embeddings (24h TTL)\x1b[0m\r\n' +
  '\r\n',

  '\x1b[32m>\x1b[0m The BERT swap looks suspicious. File the rollback step.\r\n' +
  '\r\n' +
  '  \x1b[2m⏺ add_backlog_item(title="Revert BERT cross-encoder; verify p99 in staging")\x1b[0m\r\n' +
  '\x1b[32m✓\x1b[0m queued.\r\n',
]

test.use({
  viewport: { width: 1200, height: 750 },
  video: { mode: 'on', size: { width: 1200, height: 750 } },
})

test.describe('Demos', () => {
  test('quick-switch', async ({ page }) => {
    runSeedtest()
    await page.goto('/')
    await expect(page.locator('.dashboard')).toBeVisible({ timeout: 5000 })

    // Off-camera setup (visible in the recording as a static dashboard):
    // start a fresh testagent session on the latency idea and write a
    // few hero turns into its PTY. vscreen captures the output so when
    // the user later Cmd+K's into the session, the terminal mounts and
    // replays the populated transcript instead of an empty boot screen.
    const latencyUUID = await page.evaluate(async () => {
      const W = window as unknown as { go: { app: { App: {
        ListIdeas: () => Promise<Array<{ name: string; slug: string }>>;
        StartIdeaSession: (slug: string, agent: string, resume: boolean) => Promise<{ uuid: string }>;
      } } } }
      const ideas = await W.go.app.App.ListIdeas()
      const latency = ideas.find((i) => i.name === 'p99 latency regression in search')
      if (!latency) throw new Error('latency idea not seeded')
      const res = await W.go.app.App.StartIdeaSession(latency.slug, 'testagent', false)
      return res.uuid
    })

    // Wait for testagent to finish booting (MCP connected). Without
    // this, WriteToSession bytes can race the agent's first render and
    // get dropped through the kernel's line discipline.
    await waitForAgentReady(page, latencyUUID)

    // Populate the session terminal with latency-investigation turns.
    for (const turn of LATENCY_HERO_TURNS) {
      await page.evaluate(async ({ uuid, chunk }) => {
        const W = window as unknown as { go: { app: { App: {
          WriteToSession: (uuid: string, data: string) => Promise<void>;
        } } } }
        await W.go.app.App.WriteToSession(uuid, chunk)
      }, { uuid: latencyUUID, chunk: turn })
      // Brief between-turn pause so testagent processes each chunk
      // before the next lands (matches the orchestrator-driving cadence).
      await page.waitForTimeout(150)
    }

    // Initial settle frame so the GIF starts on a stable dashboard
    // AFTER the off-camera setup completes.
    await page.waitForTimeout(600)

    // Open the command palette.
    await page.keyboard.press('Meta+k')
    await expect(page.locator('[data-testid="command-palette"]')).toBeVisible({ timeout: 2000 })

    // Type a fuzzy query slowly so the GIF shows the filtering animate.
    await page.keyboard.type('latency', { delay: 90 })
    await page.waitForTimeout(400)

    // Select the top match. sessionNav.resolveSessionTarget picks the
    // running latency session we just set up and navigates straight in;
    // the xterm mount replays the populated vscreen transcript.
    await page.keyboard.press('Enter')

    await expect(page).toHaveURL(/\/idea\/p99-latency-regression-in-search\/session\//, { timeout: 5000 })
    await holdFinalFrame(page)
  })

  test('markdown-review', async ({ page }) => {
    // Set up a pending markdown review record so the editor opens
    // directly on a real document. Doesn't depend on an agent session.
    const id = 'demo-md-review'
    const original = `# Search index strategy

We currently rebuild the search index nightly via a full Postgres scan.

## Goal

Reduce reindex time to under 10 minutes without compromising correctness.

## Decision

Going with incremental WAL streaming. Cleaner integration with the
existing search infrastructure and avoids the dual-write footgun.
`
    fs.mkdirSync(REVIEWS_DIR, { recursive: true, mode: 0o700 })
    const record = {
      id,
      kind: 'markdown',
      status: 'pending',
      created: new Date().toISOString(),
      markdown: { path: '/tmp/demo/search.md', original },
    }
    const recordPath = path.join(REVIEWS_DIR, `${id}.json`)
    fs.writeFileSync(recordPath, JSON.stringify(record, null, 2) + '\n', { mode: 0o600 })

    try {
      await page.goto(`/#/review?reviewId=${id}`)
      await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })
      await page.waitForSelector('.milkdown', { timeout: 5000 })
      const editor = page.locator('.milkdown [contenteditable="true"]').first()
      await editor.waitFor({ state: 'visible', timeout: 5000 })

      // Initial settle frame so the GIF opens on the rendered doc.
      await page.waitForTimeout(700)

      // Edit 1: insertion. Slow `delay` so the green ins-mark animates in
      // as the human types — that motion is the headline of the demo.
      const goalSentence = page.locator('.milkdown p', { hasText: 'Reduce reindex time' }).first()
      await goalSentence.click()
      await page.keyboard.press('End')
      await page.keyboard.type(' Stretch goal: under 5 minutes.', { delay: 55 })
      await page.waitForTimeout(500)

      // Edit 2: deletion. Backspace the trailing 35 chars to surface the
      // red strike-through mark.
      const decisionPara = page.locator('.milkdown p', { hasText: 'Cleaner integration' }).first()
      await decisionPara.click()
      await page.keyboard.press('End')
      for (let i = 0; i < 35; i++) {
        await page.keyboard.press('Backspace')
        if (i % 4 === 3) await page.waitForTimeout(40)
      }

      // Hold so the loop ends on the final marked-up state.
      await holdFinalFrame(page, 1000)
    } finally {
      try { fs.unlinkSync(recordPath) } catch { /* ignore */ }
    }
  })

  test('orchestrator-driving', async ({ page }) => {
    // Hero shot for the README. Replaces the static orchestrator-pinned.png
    // with an animation showing the drawer pinned + driving in one clip.
    runSeedtest()
    await page.goto('/')
    await expect(page.locator('.dashboard')).toBeVisible({ timeout: 5000 })

    // Start an idea session so the global session bar isn't empty and a
    // root orchestrator session so the drawer is alive.
    const searchSlug = await page.evaluate(async () => {
      // @ts-expect-error wails binding
      const ideas = (await window.go.app.App.ListIdeas()) as Array<{ name: string; slug: string }>
      return ideas.find((i) => i.name === 'p99 latency regression in search')?.slug || ''
    })
    if (searchSlug) {
      await page.evaluate(async (slug) => {
        // @ts-expect-error wails binding
        await window.go.app.App.StartIdeaSession(slug, 'testagent', false)
      }, searchSlug)
    }
    await page.evaluate(async () => {
      // @ts-expect-error wails binding
      await window.go.app.App.StartRootSession('testagent')
    })

    await page.reload()
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toBeVisible({ timeout: 5000 })
    await page.waitForSelector('.orchestrator-host .terminal-container', { timeout: 10000 })
    await page.waitForSelector('.orchestrator-host .xterm-screen', { timeout: 5000 })

    // Wait for testagent's banner to land so subsequent writes happen
    // on a stable baseline.
    await page.waitForFunction(
      () => {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const reg = (window as any).__ideateTerminals as Record<string, {
          buffer: { active: { length: number; getLine: (i: number) => { translateToString: (trim: boolean) => string } | undefined } }
        }> | undefined
        if (!reg) return false
        for (const term of Object.values(reg)) {
          const buf = term.buffer.active
          for (let i = 0; i < buf.length; i++) {
            if (buf.getLine(i)?.translateToString(true).includes('mcp connected:')) return true
          }
        }
        return false
      },
      { timeout: 10000 },
    )

    // Settle frame so the GIF starts on the dashboard + idle drawer.
    await page.waitForTimeout(700)

    // Write each turn-block into the orchestrator's xterm with a pause
    // between, producing the typewriter-on-turn animation.
    for (const turn of HERO_TURNS) {
      await page.evaluate((chunk) => {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const reg = (window as any).__ideateTerminals as Record<string, { write: (data: string) => void; element?: HTMLElement }> | undefined
        if (!reg) return
        const host = document.querySelector('.orchestrator-host')
        if (!host) return
        for (const term of Object.values(reg)) {
          if (term.element && host.contains(term.element)) {
            term.write(chunk)
            break
          }
        }
      }, turn)
      await page.waitForTimeout(800)
    }

    // Hold so the GIF loops on the final transcript state.
    await holdFinalFrame(page, 1200)
  })
})
