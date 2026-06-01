import { useEffect, useRef } from 'react'
import { requestConfirm } from '../lib/confirmDialog'

const DEFAULT_MESSAGE = 'You have unsaved changes. Discard?'

// useDirtyGuard prompts the user before they discard unsaved work.
//
// Two surfaces:
//   - `beforeunload` for browser-level reloads (dev refresh, etc.).
//   - `confirmIfDirty(action)` — caller wraps in-app nav (Cancel, Back)
//     to opt the action through an in-app confirm dialog when dirty.
//     Backed by requestConfirm (lib/confirmDialog) rather than the
//     native window.confirm, which Wails WKWebView no-ops on macOS —
//     calling it silently returned false, so cancel-with-edits never
//     fired the action in production.
//
// Returns:
//   - `bypass()` — flip the internal dirty flag false for the next nav
//     (use right before a save-then-navigate).
//   - `confirmIfDirty(action)` — run `action` immediately if not dirty;
//     otherwise prompt and only run on confirm. Promise resolves after
//     the user decides; callers can fire-and-forget.
export function useDirtyGuard(
  isDirty: boolean,
  message: string = DEFAULT_MESSAGE,
): { bypass: () => void; confirmIfDirty: (action: () => void) => Promise<void> } {
  const isDirtyRef = useRef(isDirty)
  isDirtyRef.current = isDirty

  useEffect(() => {
    if (!isDirty) return
    const handler = (e: BeforeUnloadEvent) => {
      e.preventDefault()
      e.returnValue = message
      return message
    }
    window.addEventListener('beforeunload', handler)
    return () => window.removeEventListener('beforeunload', handler)
  }, [isDirty, message])

  return {
    bypass: () => {
      isDirtyRef.current = false
    },
    confirmIfDirty: async (action) => {
      if (!isDirtyRef.current) {
        action()
        return
      }
      const ok = await requestConfirm(message, { confirmLabel: 'Discard', cancelLabel: 'Keep editing' })
      if (ok) action()
    },
  }
}
