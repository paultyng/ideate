import { useEffect, useState } from 'react'

// Module-level store for the orchestrator drawer's open/closed state.
// Lifted out of any single component so the topbar Notebook button and
// the IdeaList "Orchestrator" button can drive the same drawer without
// prop-drilling. Mirrors a hand-rolled Zustand: localStorage-backed,
// fan-out subscribers via Set.
const OPEN_KEY = 'ideate.orchestrator.open'
const HEIGHT_KEY = 'ideate.orchestrator.height'

export const DEFAULT_DRAWER_HEIGHT = 320
export const MIN_DRAWER_HEIGHT = 120

function readInitialOpen(): boolean {
  try {
    return typeof localStorage !== 'undefined' && localStorage.getItem(OPEN_KEY) === '1'
  } catch {
    return false
  }
}

function readInitialHeight(): number {
  try {
    if (typeof localStorage === 'undefined') return DEFAULT_DRAWER_HEIGHT
    const raw = localStorage.getItem(HEIGHT_KEY)
    if (!raw) return DEFAULT_DRAWER_HEIGHT
    const n = Number(raw)
    if (!Number.isFinite(n) || n < MIN_DRAWER_HEIGHT) return DEFAULT_DRAWER_HEIGHT
    return n
  } catch {
    return DEFAULT_DRAWER_HEIGHT
  }
}

const openListeners = new Set<(v: boolean) => void>()
const heightListeners = new Set<(v: number) => void>()
let openValue = readInitialOpen()
let heightValue = readInitialHeight()

function setOpenValue(next: boolean): void {
  if (next === openValue) return
  openValue = next
  try {
    localStorage.setItem(OPEN_KEY, next ? '1' : '0')
  } catch {
    // private mode / quota: drawer just won't persist
  }
  openListeners.forEach((l) => l(next))
}

function setHeightValue(next: number): void {
  const clamped = Math.max(MIN_DRAWER_HEIGHT, Math.round(next))
  if (clamped === heightValue) return
  heightValue = clamped
  try {
    localStorage.setItem(HEIGHT_KEY, String(clamped))
  } catch {
    // private mode / quota: height just won't persist
  }
  heightListeners.forEach((l) => l(clamped))
}

export function useOrchestratorDrawer(): {
  open: boolean
  setOpen: (next: boolean) => void
  toggle: () => void
  height: number
  setHeight: (next: number) => void
} {
  const [open, setOpen] = useState<boolean>(openValue)
  const [height, setHeight] = useState<number>(heightValue)
  useEffect(() => {
    openListeners.add(setOpen)
    heightListeners.add(setHeight)
    return () => {
      openListeners.delete(setOpen)
      heightListeners.delete(setHeight)
    }
  }, [])
  return {
    open,
    setOpen: setOpenValue,
    toggle: () => setOpenValue(!openValue),
    height,
    setHeight: setHeightValue,
  }
}
