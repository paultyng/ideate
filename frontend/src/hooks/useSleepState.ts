import { useEffect, useState } from 'react'
import { EventsOn } from '../wailsjs/runtime/runtime'

export interface SleepState {
  enabled: boolean
  held: boolean
}

// useSleepState mirrors the backend's sleep-inhibitor toggle. The
// backend emits `sleep:state-changed` after every recompute (toggle
// flips, session activity transitions), so we never poll. Bindings
// are pulled via dynamic import to tolerate the dev-server window
// where wails has regenerated the Go side but not the JS bindings
// yet — same pattern as GlobalSessionBar.
export default function useSleepState(): {
  state: SleepState
  setEnabled: (enabled: boolean) => void
} {
  const [state, setState] = useState<SleepState>({ enabled: false, held: false })

  useEffect(() => {
    let cancelled = false
    const refresh = async () => {
      try {
        const mod = await import('../wailsjs/go/app/App')
        const fn = (mod as Record<string, unknown>)['GetSleepState'] as
          | (() => Promise<SleepState>)
          | undefined
        if (!fn) return
        const next = await fn()
        if (!cancelled && next) setState(next)
      } catch {
        // Bindings briefly unavailable during regen. Will retry on
        // the next event tick.
      }
    }
    refresh()
    const cancelEvent = EventsOn('sleep:state-changed', (next: SleepState) => {
      if (!cancelled && next) setState(next)
    })
    return () => {
      cancelled = true
      cancelEvent()
    }
  }, [])

  const setEnabled = (enabled: boolean) => {
    // Optimistic flip so the toggle feels instantaneous — backend
    // will publish the authoritative state moments later.
    setState((s) => ({ ...s, enabled }))
    void (async () => {
      try {
        const mod = await import('../wailsjs/go/app/App')
        const fn = (mod as Record<string, unknown>)['SetSleepEnabled'] as
          | ((v: boolean) => Promise<void>)
          | undefined
        if (fn) await fn(enabled)
      } catch {
        // If the binding wasn't ready, the optimistic state stays;
        // the next event tick will reconcile.
      }
    })()
  }

  return { state, setEnabled }
}
