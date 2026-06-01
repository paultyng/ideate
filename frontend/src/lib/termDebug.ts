// termDebug emits xterm/PTY-pipeline diagnostics from the frontend.
// Gated on the backend's IDEATE_TERM_DEBUG env var so a single switch
// controls both layers (no localStorage / URL hash drift between
// frontend and backend opinions). The Go binding is queried once at
// startup and cached; early-boot calls (before init resolves) are
// dropped, which is acceptable for a dev-only tool.
//
// Every line is prefixed `[term]`. Pass the session id as the second
// argument so a noisy DevTools console can be narrowed to a single
// session under investigation.

let enabled = false
let initPromise: Promise<void> | null = null

// initTermDebug fetches the env-derived enabled flag from the Go
// side. Safe to call multiple times — the binding lookup is cached.
// Tolerates the dev-server window where bindings haven't regenerated
// yet by leaving enabled=false.
export function initTermDebug(): Promise<void> {
  if (initPromise) return initPromise
  initPromise = (async () => {
    try {
      const mod = await import('../wailsjs/go/app/App')
      const fn = (mod as Record<string, unknown>)['IsTermDebugEnabled'] as
        | (() => Promise<boolean>)
        | undefined
      if (fn) enabled = await fn()
    } catch {
      // Binding not ready — leave disabled.
    }
  })()
  return initPromise
}

export function termDebugEnabled(): boolean {
  return enabled
}

export function termDebug(label: string, ...args: unknown[]): void {
  if (!enabled) return
  // eslint-disable-next-line no-console
  console.debug('[term]', label, ...args)
}
