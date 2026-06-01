import { createContext, useCallback, useContext, useRef, ReactNode } from 'react'

// MRUContext tracks per-session user-navigation timestamps. Two
// surfaces consume it: GlobalSessionBar's popover (recency-folded
// sort so a just-left session ranks ahead of background ones) and
// the Cmd+K command palette (MRU as the default sort before the
// user types). Held in a ref so updates don't churn the React tree;
// consumers re-render when their owning data sources (active
// sessions, route changes) push fresh inputs through the normal
// flow.
//
// Not persisted across reloads in v1 — the data is cheap to
// recompute as the user navigates, and localStorage churn on every
// route change isn't worth the complexity.
interface MRUValue {
  markFocused: (uuid: string) => void
  // score returns a string suitable for localeCompare-based sort
  // (ISO timestamp, or empty string for sessions the user has never
  // focused).
  score: (uuid: string) => string
}

const MRUContextInternal = createContext<MRUValue | null>(null)

export function MRUProvider({ children }: { children: ReactNode }) {
  const ref = useRef<Map<string, string>>(new Map())
  const markFocused = useCallback((uuid: string) => {
    if (!uuid) return
    ref.current.set(uuid, new Date().toISOString())
  }, [])
  const score = useCallback((uuid: string) => ref.current.get(uuid) || '', [])
  return (
    <MRUContextInternal.Provider value={{ markFocused, score }}>
      {children}
    </MRUContextInternal.Provider>
  )
}

export function useMRU(): MRUValue {
  const v = useContext(MRUContextInternal)
  if (!v) throw new Error('useMRU must be used inside MRUProvider')
  return v
}
