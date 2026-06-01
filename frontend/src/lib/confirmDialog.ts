import { useEffect, useState } from 'react'

// Wails WKWebView no-ops window.confirm and window.prompt on macOS, which
// silently breaks any in-app discard flow that relies on them — the call
// returns falsy without showing a dialog, so the caller treats it as "user
// denied" and the action never runs. (Comment popover dodges this for
// window.prompt; this module is the equivalent for window.confirm.)
//
// `requestConfirm(message)` returns a Promise<boolean>. A React modal
// component mounted at App root reads the same module-level state and
// renders the dialog; clicking Confirm/Cancel resolves the promise.
//
// At most one confirm is active at a time. A second requestConfirm while
// one is pending denies the older to keep the model deterministic.

export interface ConfirmRequest {
  message: string
  confirmLabel: string
  cancelLabel: string
}

interface ActiveRequest extends ConfirmRequest {
  resolve: (ok: boolean) => void
}

let current: ActiveRequest | null = null
const listeners = new Set<(r: ConfirmRequest | null) => void>()

function notify() {
  const snapshot = current
    ? { message: current.message, confirmLabel: current.confirmLabel, cancelLabel: current.cancelLabel }
    : null
  listeners.forEach((l) => l(snapshot))
}

export interface ConfirmOptions {
  confirmLabel?: string
  cancelLabel?: string
}

export function requestConfirm(message: string, opts: ConfirmOptions = {}): Promise<boolean> {
  return new Promise((resolve) => {
    if (current) {
      current.resolve(false)
    }
    current = {
      message,
      confirmLabel: opts.confirmLabel ?? 'OK',
      cancelLabel: opts.cancelLabel ?? 'Cancel',
      resolve,
    }
    notify()
  })
}

export function useConfirmRequest(): ConfirmRequest | null {
  const [r, setR] = useState<ConfirmRequest | null>(() =>
    current ? { message: current.message, confirmLabel: current.confirmLabel, cancelLabel: current.cancelLabel } : null,
  )
  useEffect(() => {
    listeners.add(setR)
    return () => { listeners.delete(setR) }
  }, [])
  return r
}

export function resolveCurrentConfirm(ok: boolean): void {
  const c = current
  if (!c) return
  current = null
  notify()
  c.resolve(ok)
}
