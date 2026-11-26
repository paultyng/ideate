/* eslint-disable @typescript-eslint/no-explicit-any */
import type { JSHandle, Page } from '@playwright/test'

// The only Playwright module that touches Wails globals (`window.go`,
// `window.runtime`) or the `window.__ideateTerminals` test registry.
// Specs and shared helpers go through these functions so a backend swap
// changes this file, not every spec.
//
// Functions passed to page.evaluate / waitForFunction are serialized and
// run in the browser: they MUST NOT close over Node variables. Method
// names and args travel as the serializable `arg` parameter.

// Terminal registry entries are live xterm.js Terminal instances; each
// caller narrows to the members it reads.
export type TerminalRegistry = Record<string, any>

// callBinding invokes `window.go.app.App.<method>(...args)` in the page.
export async function callBinding<T = unknown>(
  page: Page,
  method: string,
  ...args: unknown[]
): Promise<T> {
  return (await page.evaluate(
    ({ m, a }) => (window as any).go.app.App[m](...a),
    { m: method, a: args },
  )) as T
}

// settleBindings starts every call in the page at once and reports each
// outcome like Promise.allSettled (rejection reasons stringified).
export async function settleBindings(
  page: Page,
  calls: Array<[method: string, ...args: unknown[]]>,
): Promise<Array<{ status: 'fulfilled' | 'rejected'; reason: string }>> {
  return page.evaluate(async (cs) => {
    const app = (window as any).go.app.App
    const settled = await Promise.allSettled(cs.map(([m, ...a]) => app[m](...a)))
    return settled.map((r) => ({
      status: r.status,
      reason: r.status === 'rejected' ? String(r.reason) : '',
    }))
  }, calls)
}

// delayBinding makes `method` wait `ms` before running the real binding.
// Installed on the live page; survives in-app navigation, not reloads.
export async function delayBinding(page: Page, method: string, ms: number): Promise<void> {
  await page.evaluate(({ m, delay }) => {
    const app = (window as any).go.app.App
    const orig = app[m]
    app[m] = async (...a: unknown[]) => {
      await new Promise((r) => setTimeout(r, delay))
      return orig.apply(app, a)
    }
  }, { m: method, delay: ms })
}

function installSpy(page: Page, method: string, passthrough: boolean): Promise<void> {
  return page.evaluate(({ m, pass }) => {
    const w = window as any
    const calls: unknown[][] = []
    w.__bindingCalls = { ...(w.__bindingCalls ?? {}), [m]: calls }
    const app = w.go?.app?.App
    if (!app) return
    const orig = app[m].bind(app)
    app[m] = (...a: unknown[]) => {
      calls.push(a)
      return pass ? orig(...a) : Promise.resolve()
    }
  }, { m: method, pass: passthrough })
}

// spyBinding records each call's args and forwards to the real binding.
export function spyBinding(page: Page, method: string): Promise<void> {
  return installSpy(page, method, true)
}

// stubBinding records each call's args and replaces the binding with a
// no-op that resolves undefined, so the real side effect never runs.
export function stubBinding(page: Page, method: string): Promise<void> {
  return installSpy(page, method, false)
}

// getBindingCalls returns the args of every recorded call to `method`
// (empty if neither spyBinding nor stubBinding ran on this page).
export async function getBindingCalls<A extends unknown[] = unknown[]>(
  page: Page,
  method: string,
): Promise<A[]> {
  return page.evaluate((m) => (window as any).__bindingCalls?.[m] ?? [], method)
}

// emitEvent fires a backend event at the frontend's listeners, as if the
// Go side had emitted it.
export async function emitEvent(page: Page, name: string, ...args: unknown[]): Promise<void> {
  await page.evaluate(({ n, a }) => {
    ;(window as any).runtime?.EventsEmit?.(n, ...a)
  }, { n: name, a: args })
}

// waitForBrowserOpenURL waits for the runtime to expose BrowserOpenURL.
// The runtime injects after addInitScript runs, so patch only after this.
export async function waitForBrowserOpenURL(page: Page, timeoutMs: number): Promise<void> {
  await page.waitForFunction(() => {
    return Boolean((window as any).runtime?.BrowserOpenURL)
  }, undefined, { timeout: timeoutMs })
}

// captureBrowserOpenURL replaces runtime.BrowserOpenURL with a recorder
// appending each URL to the array at `window[key]` (created if absent).
export async function captureBrowserOpenURL(page: Page, key: string): Promise<void> {
  await page.evaluate((k) => {
    const w = window as any
    w[k] = w[k] ?? []
    const rt = w.runtime ?? (w.runtime = {})
    rt.BrowserOpenURL = (url: string) => { w[k].push(url) }
  }, key)
}

// stopAllSessions stops every running session (including the root
// orchestrator) and polls up to 5s until none report as running. Runs
// in one page evaluation so the poll stays in the page.
export async function stopAllSessions(page: Page): Promise<void> {
  await page.evaluate(async () => {
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

// cancelPendingReviews cancels every pending review through the real
// binding; a no-op when the binding is absent.
export async function cancelPendingReviews(page: Page): Promise<void> {
  await page.evaluate(async () => {
    const W = window as any
    if (!W.go?.app?.App?.ListPendingReviews) return
    const pending = (await W.go.app.App.ListPendingReviews()) ?? []
    for (const r of pending) {
      try { await W.go.app.App.CancelReview(r.id) } catch { /* already gone */ }
    }
  })
}

// waitForBindings waits for the Wails app bindings to be injected.
export async function waitForBindings(page: Page): Promise<void> {
  await page.waitForFunction(() => (window as any).go?.app?.App)
}

// evalTerminals runs `fn` in the page with the terminal registry
// (`undefined` until a TerminalPanel mounts) and a serializable `arg`.
// `fn` is serialized with toString, so it MUST NOT close over outer
// variables; pass them through `arg`.
export async function evalTerminals<A, R>(
  page: Page,
  fn: (reg: TerminalRegistry | undefined, arg: A) => R | Promise<R>,
  arg?: A,
): Promise<R> {
  return (await page.evaluate(
    `(${fn.toString()})(window.__ideateTerminals, ${JSON.stringify(arg)})`,
  )) as R
}

// waitForTerminals polls `fn(reg, arg)` in the page until truthy, with
// waitForFunction's polling and options. Resolves to the truthy value.
export async function waitForTerminals<A, R>(
  page: Page,
  fn: (reg: TerminalRegistry | undefined, arg: A) => R,
  arg?: A,
  options?: { timeout?: number },
): Promise<JSHandle<R>> {
  return (await page.waitForFunction(
    `(${fn.toString()})(window.__ideateTerminals, ${JSON.stringify(arg)})`,
    undefined,
    options,
  )) as unknown as JSHandle<R>
}

// getMountedSessionId returns one mounted TerminalPanel's session id —
// the last one in insertion order — for tests that started a session
// via the form flow (no binding return value handing back the uuid).
// Waits for the registry entry: `.xterm-screen` can appear before
// TerminalPanel's effect registers the id.
export async function getMountedSessionId(page: Page, timeoutMs = 5_000): Promise<string> {
  const handle = await waitForTerminals(page, (reg) => {
    const keys = reg ? Object.keys(reg) : []
    return keys.length ? keys[keys.length - 1] : false
  }, undefined, { timeout: timeoutMs })
  return (await handle.jsonValue()) as string
}
