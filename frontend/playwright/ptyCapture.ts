import type { Page } from '@playwright/test'

// enablePtyCapture turns on the test-only side-channel for raw PTY
// bytes (see frontend/src/lib/ptyCapture.ts). Call once per test
// before mounting any TerminalPanel so the captured stream covers
// the session from byte 0.
//
// page.goto resolves on DOMContentLoaded, but the React bundle that
// installs window.__enableCapturePty is loaded asynchronously — so
// we explicitly wait for the hook to be present before flipping it.
// Without this, enablePtyCapture is a no-op when it races the bundle
// and capturePty silently drops every byte.
export async function enablePtyCapture(page: Page): Promise<void> {
  await page.waitForFunction(() => {
    const w = window as Window & { __enableCapturePty?: () => void }
    return typeof w.__enableCapturePty === 'function'
  })
  await page.evaluate(() => {
    const w = window as Window & {
      __enableCapturePty?: () => void
      __resetCapturePty?: () => void
    }
    w.__resetCapturePty?.()
    w.__enableCapturePty?.()
  })
}

// readSessionReplay returns the agent's current vscreen state as
// ANSI-stripped text. Backend RPC that always reflects the current
// screen regardless of when the TerminalPanel mounted, so it side-
// steps the RegisterSessionViewer/replay-fetch race that capturePty
// hits when the agent emits content before the test's TerminalPanel
// finishes subscribing.
//
// **Use this for all assertions on recent / on-screen content** —
// banner, current prompt echo, slash-command lifecycle markers
// (`[clear]`, `[compact]`), the bottom prompt line. The vscreen also
// captures viewport cropping behavior, so assertions against
// post-overflow screen state stay honest: if the agent failed to
// pin its prompt to the bottom row after a screen-fill, the replay
// will show it.
//
// Reserve `readCapturedPty` for assertions on content that has
// scrolled out of the alt-screen viewport (backscroll) — never use
// it for "did this just print?" checks, because it will pass even
// when the user can't actually see the output.
export async function readSessionReplay(page: Page, uuid: string): Promise<string> {
  return await page.evaluate(async (id) => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const W = window as any
    try {
      const b64 = (await W.go.app.App.GetSessionReplay(id)) as string
      if (!b64) return ''
      // atob returns a Latin-1 string (one char per byte). Multi-byte
      // UTF-8 characters like "▶" split into 3 chars under Latin-1
      // and won't match against UTF-8 string literals in the test.
      // Re-decode through TextDecoder so the result is proper UTF-8.
      const binStr = atob(b64)
      const arr = new Uint8Array(binStr.length)
      for (let i = 0; i < binStr.length; i++) arr[i] = binStr.charCodeAt(i)
      const utf8 = new TextDecoder('utf-8', { fatal: false }).decode(arr)
      // Same ANSI strip as capturePty's readCaptured — keep semantics aligned.
      return utf8
        // eslint-disable-next-line no-control-regex
        .replace(/\x1b\][^\x07\x1b]*(\x07|\x1b\\)/g, '')
        // eslint-disable-next-line no-control-regex
        .replace(/\x1b[@-Z\\-_]/g, '')
        // eslint-disable-next-line no-control-regex
        .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '')
        // eslint-disable-next-line no-control-regex
        .replace(/[\x00-\x08\x0b-\x1f\x7f]/g, '')
    } catch {
      return ''
    }
  }, uuid)
}

// stopAllRunningSessions kills every running session via the
// coordinator AND polls until the session records are no longer
// reported as running. StopSession's status transition is async
// (coordinator kills the PTY; the status callback writes Status=
// stopped on PTY exit), so awaiting StopSession alone leaves the
// disk records in Status=running for a brief window. Downstream
// specs that read list_sessions/ListActiveSessions then see leaked
// sessions, bloating MCP responses past the alt-screen viewport and
// failing visibility-dependent assertions (orchestrator.spec.ts:19).
//
// test:ui shares one wails dev backend across every spec file, and
// TESTAGENT_AUTO_EXIT is 30s — without an explicit teardown each
// test leaks a session into the global bar, which causes strict-
// mode violations on chip locators in downstream specs and
// contaminates capturePty's combined stream when tests read without
// a sessionId.
export async function stopAllRunningSessions(page: Page): Promise<void> {
  await page.evaluate(async () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const W = window as any
    if (!W.go?.app?.App) return
    // Wails serializes a nil Go slice as JSON null; coerce so the
    // for-of below doesn't throw when there's nothing to clean up.
    const active = ((await W.go.app.App.ListActiveSessions()) ?? []) as Array<{ uuid: string }>
    for (const s of active) {
      try { await W.go.app.App.StopSession(s.uuid) } catch { /* already gone */ }
    }
    // ListActiveSessions intentionally omits the root orchestrator
    // (it has its own dedicated drawer surface). Stop it explicitly
    // so the inactivity-exit watchdog doesn't fire mid-test on a
    // orchestrator that's shared across every orchestrator spec.
    const root = await W.go.app.App.GetRunningRootSession() as { uuid?: string } | null
    if (root?.uuid) {
      try { await W.go.app.App.StopSession(root.uuid) } catch { /* already gone */ }
    }
    // Wait for the async status-callback chain to flip disk records
    // to Status!=running. Poll for up to 5s; long enough for PTY
    // exit + finalizeSession in normal cases, short enough that a
    // genuinely-wedged session surfaces as a flake rather than
    // wedging the whole suite.
    const deadline = Date.now() + 5000
    while (Date.now() < deadline) {
      const remaining = ((await W.go.app.App.ListActiveSessions()) ?? []) as Array<{ uuid: string }>
      const rootNow = await W.go.app.App.GetRunningRootSession() as { uuid?: string } | null
      if (remaining.length === 0 && !rootNow?.uuid) return
      await new Promise((r) => setTimeout(r, 50))
    }
  })
}

// getMountedSessionId returns one mounted TerminalPanel's session id —
// the last one in insertion order — for tests that started a session
// via the form flow (no binding return value handing back the uuid).
// Throws if no terminal has mounted yet; pair with `waitForSelector`
// on `.xterm-screen` first.
export async function getMountedSessionId(page: Page): Promise<string> {
  const id = await page.evaluate(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const reg = (window as any).__ideateTerminals as Record<string, unknown> | undefined
    if (!reg) return ''
    const keys = Object.keys(reg)
    return keys.length ? keys[keys.length - 1] : ''
  })
  if (!id) throw new Error('getMountedSessionId: no terminal mounted yet')
  return id
}

// waitForTerminalMount blocks until TerminalPanel has registered the
// session id on `window.__ideateTerminals` — which happens in the
// same useEffect that wires the `session:<id>:output` subscription
// and the capturePty hook. Use this between hash-nav and any
// WriteToSession on the just-mounted session so live bytes aren't
// missed by an unsubscribed capture.
export async function waitForTerminalMount(
  page: Page,
  sessionId: string,
  timeoutMs = 5_000,
): Promise<void> {
  await page.waitForFunction(
    (id) => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const reg = (window as any).__ideateTerminals as Record<string, unknown> | undefined
      return Boolean(reg && reg[id])
    },
    sessionId,
    { timeout: timeoutMs },
  )
}

// waitForAgentReady blocks until the upstream testagent has finished
// booting (MCP connected + SessionStart fired) for `sessionId`. The
// TUI puts stdin in raw mode only after Bubbletea finishes its first
// render; PTY bytes written before then go through the kernel's line
// discipline and are silently dropped (or echoed). Tests that drive
// slash commands via WriteToSession must wait for readiness first or
// the input never reaches the agent.
//
// We poll for the `[mcp connected: N tools]` lifecycle marker, not
// the banner's "/help for commands" hint:
//   - testagent v0.4+ renders the TUI banner via bubbletea v2's
//     inline mode, which uses \x1b[5L (Insert Lines) to push existing
//     scrollback down and write the banner into the freed rows. Our
//     vscreen vt-emulator handles IL inconsistently — the middle
//     banner rows ("session <uuid>", "Type anything; /help for
//     commands") don't reliably appear in vscreen, while the top
//     border + title row + post-banner lifecycle markers do.
//   - The `[mcp connected:` line is emitted after MCP.Connect()
//     succeeds and is rendered via tea.Println (committed to native
//     scrollback as a single line, not part of the IL block), so it
//     lands in vscreen deterministically across v0.3.1 and v0.6.3.
//   - It's also a stronger readiness signal: the agent isn't actually
//     ready to accept slash commands until SessionStart has fired,
//     which is bracketed by the MCP-connected line.
//
// Reads from the on-screen replay (not the raw capture stream)
// because the marker is currently visible — using the replay keeps
// readiness honest: if it scrolled off the screen for any reason we
// want to know.
// AGENT_LIFECYCLE_TIMEOUT_MS is the shared budget for any wait that blocks
// on the testagent reaching a lifecycle state — boot/MCP-connect readiness or
// teardown/exit. On loaded CI runners the testagent boot → MCP `Connect()`
// handshake routinely exceeds 10s (ac3607a bumped dashboard's wait 5s→15s for
// exactly this). Kept as one exported constant so per-spec literals can't
// drift back below the proven value; that duplication was the flake's root.
export const AGENT_LIFECYCLE_TIMEOUT_MS = 15_000

export async function waitForAgentReady(
  page: Page,
  sessionId: string,
  timeoutMs = AGENT_LIFECYCLE_TIMEOUT_MS,
): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const t = await readSessionReplay(page, sessionId)
    if (t.includes('mcp connected:')) return
    await page.waitForTimeout(100)
  }
  throw new Error(`waitForAgentReady: testagent boot marker not seen for ${sessionId} after ${timeoutMs}ms`)
}

// readCapturedPty returns the cumulative ANSI-stripped text emitted
// for the session. **Only use this for assertions on content that has
// scrolled out of the alt-screen viewport** (backscroll) — never for
// recent text that should currently be visible. Reading raw PTY bytes
// can succeed even when the agent failed to render the content on the
// user's screen (e.g. the testagent <v0.3.1 viewport-overflow bug).
// Use `readSessionReplay` for on-screen assertions instead.
//
// Omit `sessionId` to read every captured session concatenated; useful
// for tests with a single agent where the generated id isn't handy.
export async function readCapturedPty(page: Page, sessionId?: string): Promise<string> {
  return await page.evaluate((id) => {
    const w = window as Window & { __capturedPty?: (id?: string) => string }
    return w.__capturedPty?.(id) ?? ''
  }, sessionId)
}
