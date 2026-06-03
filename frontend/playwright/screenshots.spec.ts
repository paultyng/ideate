import { test, expect, Page } from '@playwright/test'
import { execFileSync } from 'child_process'
import * as fs from 'fs'
import * as path from 'path'

// Screenshot specs run as part of `task test:ui` (assertions exercise
// the views every run) and as `task screenshots` (sets SAVE_SCREENSHOTS=1
// to also persist PNGs into docs/media/ for the README). Splitting these
// concerns keeps git diffs clean — the test suite never rewrites the
// committed PNGs, only the explicit screenshots task does.

const REPO_PATH = process.env.TEST_DIFF_REPO || process.cwd().replace('/frontend', '')
const MEDIA_DIR = path.resolve(REPO_PATH, 'docs/media')
const SAVE = process.env.SAVE_SCREENSHOTS === '1'

const REVIEWS_DIR = process.env.TEST_REVIEWS_DIR
  || path.join(process.env.IDEATE_CONFIG_DIR || path.join(REPO_PATH, '.ideate-dev'), 'reviews')

// Hero orchestrator screenshot transcript. Previously baked into the
// in-tree testagent as `playDemoTranscript()` and triggered by
// TESTAGENT_DEMO=1; moved into the playwright spec so the testagent
// stays a plain echo agent (matching upstream paultyng/testagent
// behavior, ahead of the migration to that binary).
//
// Written directly into xterm via terminal.write(...) — bypasses the
// PTY because we want it to look like agent output, not user input.
// Lines end with \r\n so raw-mode rendering positions each subsequent
// line at column 0; the leading \r\n drops cursor to column 0 from
// wherever testagent's prompt left it.
const HERO_TRANSCRIPT =
  '\r\n' +
  '\x1b[32m>\x1b[0m Optimize bulk import performance — start a new idea.\r\n' +
  '\r\n' +
  '  \x1b[2m⏺ create_idea(name="Optimize bulk import performance", status=active)\x1b[0m\r\n' +
  '  \x1b[2m⎿ slug optimize-bulk-import-performance\x1b[0m\r\n' +
  '\r\n' +
  '\x1b[32m>\x1b[0m Spin up a research agent on it and resume the search-index session.\r\n' +
  '\r\n' +
  '  \x1b[2m⏺ goto_idea(slug=optimize-bulk-import-performance)  →  starts session\x1b[0m\r\n' +
  '  \x1b[2m⏺ goto_idea(slug=evaluate-search-index-strategy)    →  resumes session\x1b[0m\r\n' +
  '\r\n' +
  "\x1b[32m>\x1b[0m What's the search-index agent up to?\r\n" +
  '\r\n' +
  '  \x1b[2m⏺ get_session_output(uuid=…search-index…, lines=8)\x1b[0m\r\n' +
  '  \x1b[2m⎿ Drafted decision-doc: incremental WAL streaming over\x1b[0m\r\n' +
  '  \x1b[2m   full-rebuild. Dual-write footgun ruled out. Ready for review.\x1b[0m\r\n' +
  '\r\n' +
  '\x1b[32m>\x1b[0m Ask them to open a markdown review on it.\r\n' +
  '\r\n' +
  '  \x1b[2m⏺ send_session_input(uuid=…search-index…,\x1b[0m\r\n' +
  '  \x1b[2m                     text="Open a markdown review on the decision doc.")\x1b[0m\r\n' +
  '\x1b[32m✓\x1b[0m delivered.\r\n' +
  '\r\n'

// Names the seedtest binary writes (cmd/seedtest/main.go is the source
// of truth for the full manifest — names, statuses, resources, backlog).
// Kept here only for visibility assertions; if the Go manifest evolves,
// update this list to match.
const EXPECTED_SEEDED_NAMES = [
  'Migrate auth service to OIDC',
  'p99 latency regression in search',
  'v2 API design doc',
  'Evaluate vector DB for semantic search',
  'Optimize bulk import performance',
  'Postmortem: 2026-05-14 checkout outage',
  'Refactor session lifecycle',
  'Sunset old admin dashboard',
]

// In the real Wails build the macOS title bar overlays the topbar's
// reserved 80px left padding (TitleBarHidden + FullSizeContent), so
// the traffic lights sit exactly there. Playwright's headless Chromium
// has no such chrome, leaving that strip empty in captures. Inject
// three CSS-faked circles at the same coordinates so the README
// screenshots read like a real macOS window. Production CSS is
// untouched — this overlay only exists during the screenshots spec.
async function injectFakeMacChrome(page: Page): Promise<void> {
  await page.addStyleTag({
    content: `
      .app-topbar::before {
        content: '';
        position: absolute;
        top: 50%;
        left: 12px;
        transform: translateY(-50%);
        width: 56px;
        height: 12px;
        background:
          radial-gradient(circle at 6px 6px, #ff5f57 5.5px, transparent 6.25px),
          radial-gradient(circle at 26px 6px, #febc2e 5.5px, transparent 6.25px),
          radial-gradient(circle at 46px 6px, #28c840 5.5px, transparent 6.25px);
        pointer-events: none;
      }
    `,
  })
}

async function captureIfRequested(page: Page, name: string): Promise<void> {
  if (!SAVE) return
  await injectFakeMacChrome(page)
  fs.mkdirSync(MEDIA_DIR, { recursive: true })
  await page.screenshot({ path: path.join(MEDIA_DIR, `${name}.png`), fullPage: false })
}

// runSeedtest invokes the canonical seedtest binary against the test's
// ideas dir, populating it with the manifest defined in cmd/seedtest.
// Single source of truth shared with `task seed:testdata` (dogfood).
function runSeedtest(): void {
  const ideasDir = process.env.TEST_IDEAS_DIR
  if (!ideasDir) {
    throw new Error(
      'runSeedtest requires TEST_IDEAS_DIR (set by task test:ui). ' +
      'Run the screenshots spec via `task generate:screenshots` or `task test:ui`.',
    )
  }
  // seedtest also reads IDEATE_CONFIG_DIR; derive from the ideas dir's
  // parent ($TEST_CONFIG by convention).
  const configDir = path.dirname(ideasDir)
  execFileSync('go', ['run', './cmd/seedtest'], {
    cwd: REPO_PATH,
    env: {
      ...process.env,
      IDEATE_IDEAS_DIR: ideasDir,
      IDEATE_CONFIG_DIR: configDir,
    },
    stdio: 'inherit',
  })
}

test.describe('Screenshots', () => {
  test.use({ viewport: { width: 1400, height: 900 } })

  test('dashboard with seeded ideas', async ({ page }) => {
    runSeedtest()
    await page.goto('/')
    await page.reload()
    await expect(page.locator('.dashboard')).toBeVisible({ timeout: 5000 })
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toBeVisible()

    // Non-archived ideas are visible by default; archived ones are
    // tucked behind a "Show archived" toggle. Click it so the screenshot
    // shows the full lifecycle including the archived section expanded.
    await page.click('.btn-toggle-archived')

    for (const name of EXPECTED_SEEDED_NAMES) {
      await expect(page.locator('.idea-card', { hasText: name })).toBeVisible()
    }
  })

  test('idea detail with running session', async ({ page }) => {
    // Land on /idea/new (not /) so the dashboard's drawer-pin logic
    // doesn't auto-open the orchestrator and crowd the screenshot.
    await page.goto('/#/idea/new')

    const result = await page.evaluate(async () => {
      // Reuse the seeded "Optimize bulk import performance" idea so this
      // test doesn't create a duplicate when the prior test already
      // seeded it.
      // @ts-expect-error wails binding
      const ideas = (await window.go.app.App.ListIdeas()) as Array<{ name: string; slug: string }>
      let slug = ideas.find((i) => i.name === 'Optimize bulk import performance')?.slug
      if (!slug) {
        // @ts-expect-error wails binding
        slug = (await window.go.app.App.CreateIdea('Optimize bulk import performance', 'active', 'Investigate batched-insert strategies for the import pipeline')) as string
      }
      // @ts-expect-error wails binding
      const r = (await window.go.app.App.StartIdeaSession(slug, 'testagent', false)) as { uuid: string }
      return { slug, uuid: r.uuid }
    })

    await page.evaluate((slug) => {
      window.location.hash = `/idea/${slug}`
    }, result.slug)

    await expect(page.locator('.idea-detail-name')).toContainText('Optimize bulk import performance', { timeout: 5000 })
    await page.waitForSelector('.idea-sidebar', { timeout: 5000 })
  })

  test('orchestrator drawer pinned on home', async ({ page }) => {
    await page.goto('/')

    // Spin up a session on a seeded idea so the global session bar
    // shows multiple chips (alongside the one already running from
    // the idea-detail test) — gives the hero shot a busy workspace
    // feel rather than a single solo session.
    const searchIdeaSlug = await page.evaluate(async () => {
      // @ts-expect-error wails binding
      const ideas = (await window.go.app.App.ListIdeas()) as Array<{ name: string; slug: string }>
      let slug = ideas.find((i) => i.name === 'Evaluate search index strategy')?.slug
      if (!slug) {
        // @ts-expect-error wails binding
        slug = (await window.go.app.App.CreateIdea(
          'Evaluate search index strategy',
          'active',
          'Compare incremental vs full-rebuild index approaches',
        )) as string
      }
      return slug
    })
    await page.evaluate(async (slug) => {
      // @ts-expect-error wails binding
      await window.go.app.App.StartIdeaSession(slug, 'testagent', false)
    }, searchIdeaSlug)

    await page.evaluate(async () => {
      // @ts-expect-error wails binding
      await window.go.app.App.StartRootSession('testagent')
    })

    await page.reload()
    await expect(page.locator('[data-testid="orchestrator-drawer"]')).toBeVisible({ timeout: 5000 })
    await page.waitForSelector('.orchestrator-host .terminal-container', { timeout: 10000 })
    await page.waitForSelector('.orchestrator-host .xterm-screen', { timeout: 5000 })

    // Wait for testagent's banner to land in the orchestrator terminal
    // so the subsequent transcript write happens after a stable
    // baseline (banner box draws "╚" on its closing border row).
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
            if (buf.getLine(i)?.translateToString(true).includes('╚')) return true
          }
        }
        return false
      },
      { timeout: 10000 },
    )

    // Inject the hero transcript directly into xterm — bypasses the
    // PTY (we want it to render as if it were agent output, not user
    // input). xterm 6 exposes terminal.write(data); we look up the
    // orchestrator's terminal instance via __ideateTerminals.
    await page.evaluate((transcript) => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const reg = (window as any).__ideateTerminals as Record<string, { write: (data: string) => void; element?: HTMLElement }> | undefined
      if (!reg) return
      // The orchestrator terminal is mounted in .orchestrator-host (the
      // App-root overlay), not inside .orchestrator-drawer — the drawer
      // is now just chrome that reserves layout. Pick the one whose
      // root lives inside the host.
      const host = document.querySelector('.orchestrator-host')
      if (!host) return
      for (const term of Object.values(reg)) {
        if (term.element && host.contains(term.element)) {
          term.write(transcript)
          break
        }
      }
    }, HERO_TRANSCRIPT)

    // Wait for the tail of the transcript to land before snapping —
    // a row containing "delivered." is the last visible line; if
    // it's present xterm has finished rendering everything above.
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
            if (buf.getLine(i)?.translateToString(true).includes('delivered.')) return true
          }
        }
        return false
      },
      { timeout: 5000 },
    )

    // Hero shot is now docs/media/orchestrator-driving.gif, produced by
    // demos.spec.ts. This test still exercises the orchestrator-pinned
    // flow as a smoke check but no longer emits a PNG.
  })

  test('diff review with synthetic diff and inline comment', async ({ page }) => {
    const id = 'screenshot-diff-review'
    // Override the standard test diff with a small real PR that has
    // interspersed +/- on a few files (PR #14: app icon + lightbulb
    // swap — .gitignore allow-list, Taskfile.yaml icon:build task,
    // IdeaSession.tsx Lightbulb→inline-SVG) so the screenshot shows
    // realistic review content. Stable post-OSS-release SHAs.
    const base = '2a34f59'
    const head = '06f17d5'

    fs.mkdirSync(REVIEWS_DIR, { recursive: true, mode: 0o700 })
    const record = {
      id,
      kind: 'diff',
      repo: REPO_PATH,
      base_commit: base,
      head_commit: head,
      status: 'pending',
      created: new Date().toISOString(),
      comments: [
        // Anchor on a row that's BOTH in a visible hunk AND on the
        // file that @git-diff-view's tree opens by default. The test
        // doesn't click a file — it just waits for the comment thread
        // to render — so the comment has to be on the alphabetically-
        // first file in the diff (.gitignore here). Line 2 lands inside
        // the first hunk (`build/`); line 1 would sit behind "Expand Up"
        // and have no anchor row.
        {
          path: '.gitignore',
          line: 2,
          side: 'RIGHT',
          body: 'Worth tracking appicon.png explicitly in CI to prevent the regression?',
        },
      ],
    }
    const recordPath = path.join(REVIEWS_DIR, `${id}.json`)
    fs.writeFileSync(recordPath, JSON.stringify(record, null, 2) + '\n', { mode: 0o600 })

    try {
      // Diff reviews need repo/base/head URL params to load the diff —
      // reviewId alone wires the comment thread but not the diff content.
      const url = `/#/review?repo=${encodeURIComponent(REPO_PATH)}&base=${base}&head=${head}&reviewId=${id}`
      await page.goto(url)
      await page.waitForSelector('.review-toolbar', { timeout: 15000 })
      await expect(page.locator('.file-tree-item').first()).toBeVisible({ timeout: 5000 })
      // Persisted comment renders inline as an extend widget — wait for the
      // thread before screenshotting so the PNG always shows the comment.
      await expect(page.locator('.review-comment-thread').first()).toBeVisible({ timeout: 5000 })

      await captureIfRequested(page, 'diff-review')
    } finally {
      try { fs.unlinkSync(recordPath) } catch { /* ignore */ }
    }
  })

  test('markdown review with criticmarkup edits', async ({ page }) => {
    const id = 'screenshot-md-review'
    const original = `# Search index strategy

We currently rebuild the search index nightly via a full Postgres scan.
This works but takes ~45 minutes during peak hours and creates pressure
on the primary database.

## Goal

Reduce reindex time to under 10 minutes without compromising correctness.

## Options

1. **Incremental updates from the WAL** — stream change events and apply
   to the search cluster directly.
2. **Background workers with throttling** — keep nightly rebuilds but
   parallelise across shards.

## Decision

Going with option 1 — incremental WAL streaming. Cleaner integration with
the existing search infrastructure and avoids the dual-write footgun.
`
    fs.mkdirSync(REVIEWS_DIR, { recursive: true, mode: 0o700 })
    const record = {
      id,
      kind: 'markdown',
      status: 'pending',
      created: new Date().toISOString(),
      markdown: { path: '/tmp/screenshot/search.md', original },
    }
    const recordPath = path.join(REVIEWS_DIR, `${id}.json`)
    fs.writeFileSync(recordPath, JSON.stringify(record, null, 2) + '\n', { mode: 0o600 })

    try {
      await page.goto(`/#/review?reviewId=${id}`)
      await page.waitForSelector('[data-testid="markdown-review-editor"]', { timeout: 15000 })
      await page.waitForSelector('.milkdown', { timeout: 5000 })

      // Apply suggesting-mode insertions so the CriticMarkup overlay
      // (green-underline ins marks) is visible in the screenshot.
      const editor = page.locator('.milkdown [contenteditable="true"]').first()
      await editor.waitFor({ state: 'visible', timeout: 5000 })

      const goalSentence = page.locator('.milkdown p', { hasText: 'Reduce reindex time' }).first()
      await goalSentence.click()
      await page.keyboard.press('End')
      await page.keyboard.type(' Stretch goal: under 5 minutes for the hot tier.')

      const optionsList = page.locator('.milkdown li', { hasText: 'Background workers' }).first()
      await optionsList.click()
      await page.keyboard.press('End')
      await page.keyboard.type(' (deferred — see WAL throughput notes)')

      // Add a deletion mark by backspacing the trailing phrase
      // " and avoids the dual-write footgun." (35 chars) — surfaces the
      // red-strike deletion overlay alongside the green insertion ones.
      const decisionPara = page.locator('.milkdown p', { hasText: 'Cleaner integration' }).first()
      await decisionPara.click()
      await page.keyboard.press('End')
      for (let i = 0; i < 35; i++) await page.keyboard.press('Backspace')

      // README's markdown-review surface is now docs/media/markdown-review.gif
      // (animated, produced by demos.spec.ts). This test still asserts the
      // editor flow but no longer emits the static PNG.
    } finally {
      try { fs.unlinkSync(recordPath) } catch { /* ignore */ }
    }
  })
})
