import { test, expect } from '@playwright/test'
import { getMountedSessionId } from './ptyCapture'

// In-line copy of the createIdea helper used elsewhere — kept inline
// matching the established pattern (idea-session.spec.ts, dashboard.spec.ts,
// command-palette.spec.ts each carry their own). Worth lifting to a shared
// module once anyone needs it for a 4th spec.
async function createIdea(page: import('@playwright/test').Page, name: string): Promise<string> {
  await page.goto('/#/idea/new')
  await page.fill('input[type="text"]', name)
  await page.selectOption('.idea-form select', 'active')
  await page.click('button[type="submit"]')
  await expect(page.locator('.idea-detail-name')).toHaveText(name)
  const url = page.url()
  return url.split('/idea/')[1].split('/')[0].split('?')[0]
}

// Regression coverage for the Dock-launch monochrome bug: launchd-spawned
// processes inherit a minimal env (no TERM, no COLORTERM), so any
// subprocess Ideate's PTY hosts emits zero ANSI color. `buildClaudeEnv`
// now overrides both with xterm.js's emulated capability — see
// internal/agent/claude_test.go:TestBuildClaudeEnv_TerminalIdentity for
// the unit assertion. This file is the rendering-side smoke check: if
// ANSI color reaches xterm.js, does it land as a non-default foreground
// on the buffer cell? Catches theme regressions and any future
// refactor that drops color support at the xterm-config layer.
//
// The test bypasses the PTY (testagent has no color-emitting slash
// command surfaced through `WriteToSession`) and writes the escape
// sequence directly to the Terminal instance via the existing
// `__ideateTerminals` test affordance (TerminalPanel.tsx:65). The
// rendered cell's foreground colour is then read from the same buffer
// API a real claude render would land on.

test.describe('Terminal color rendering', () => {
  test('ANSI 32m (green) renders with non-default fg color', async ({ page }) => {
    const slug = await createIdea(page, 'Color Render Smoke')

    await page.goto(`/#/idea/${slug}/session/new`)
    await page.selectOption('.session-start select', 'testagent')
    await page.click('button:has-text("Start Session")')
    await page.waitForSelector('.terminal-container', { timeout: 10000 })
    await page.waitForSelector('.xterm-screen', { timeout: 5000 })

    const uuid = await getMountedSessionId(page)

    // Inject ANSI green via the Terminal instance directly. Picks a free
    // row below testagent's banner so the marker doesn't collide with
    // the boot text. The sequence: `\r\n` + green + literal text + reset.
    const marker = 'IDEATE_COLOR_PROBE_GREEN'
    await page.evaluate(({ id, payload }) => {
      const reg = (window as unknown as { __ideateTerminals?: Record<string, { write: (s: string) => void } > }).__ideateTerminals
      const t = reg?.[id]
      if (!t) throw new Error(`no Terminal registered for session ${id}`)
      t.write('\r\n\x1b[32m' + payload + '\x1b[0m\r\n')
    }, { id: uuid, payload: marker })

    // Walk the buffer for the marker row and read its first cell's fg.
    // xterm.js's getFgColor() returns the palette index (or RGB packed
    // when truecolor); 0 means "default" (no color). The exact index
    // is xterm-version-dependent — assert "not default" rather than
    // pinning to a specific number.
    const result = await page.evaluate(async ({ id, payload }) => {
      // Allow xterm one frame to commit the buffer write.
      await new Promise((r) => requestAnimationFrame(() => r(null)))

      const reg = (window as unknown as {
        __ideateTerminals?: Record<string, {
          buffer: {
            active: {
              length: number
              getLine: (i: number) => {
                translateToString: (trim?: boolean) => string
                getCell: (col: number) => { getFgColor: () => number; isFgDefault: () => boolean }
              } | undefined
            }
          }
        }>
      }).__ideateTerminals
      const t = reg?.[id]
      if (!t) return { error: 'no terminal' }

      // Find the row that contains our marker.
      const buf = t.buffer.active
      for (let i = 0; i < buf.length; i++) {
        const line = buf.getLine(i)
        if (!line) continue
        const text = line.translateToString(true)
        if (text.includes(payload)) {
          const cell = line.getCell(text.indexOf(payload))
          if (!cell) return { error: 'no cell at marker col' }
          return {
            row: i,
            fg: cell.getFgColor(),
            isFgDefault: cell.isFgDefault(),
            text,
          }
        }
      }
      return { error: 'marker row not found' }
    }, { id: uuid, payload: marker })

    if ('error' in result) {
      throw new Error(`color probe failed: ${result.error}`)
    }
    // The marker cell must NOT have the default foreground — if it does,
    // xterm rendered the bytes as plain text and dropped the SGR.
    expect(result.isFgDefault).toBe(false)
    // Sanity: getFgColor() should report a non-zero palette index for
    // ANSI 32 (green = index 2 in the standard 16-color table).
    expect(result.fg).not.toBe(0)
  })
})
