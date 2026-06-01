import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import {
  StartRootSession,
  GetRunningRootSession,
  ListAgentTypes,
} from '../wailsjs/go/app/App'
import { EventsOn } from '../wailsjs/runtime/runtime'

// OrchestratorMode controls where the orchestrator's TerminalPanel renders.
// 'drawer'     — pinned to the top pushdown drawer area.
// 'fullscreen' — fills the main pane on /orchestrator.
// 'hidden'     — TerminalPanel is unmounted; vscreen replay seeds a fresh
//                 xterm on the next mode change. The Go-side session keeps
//                 running regardless of mount state.
export type OrchestratorMode = 'drawer' | 'fullscreen' | 'hidden'

interface OrchestratorContextValue {
  uuid: string | null
  agentTypes: string[]
  agentType: string
  setAgentType: (t: string) => void
  starting: boolean
  error: string
  start: () => Promise<void>
  mode: OrchestratorMode
  setMode: (m: OrchestratorMode) => void
}

const OrchestratorContext = createContext<OrchestratorContextValue | null>(null)

export function useOrchestrator(): OrchestratorContextValue {
  const ctx = useContext(OrchestratorContext)
  if (!ctx) throw new Error('useOrchestrator must be used within OrchestratorProvider')
  return ctx
}

export function OrchestratorProvider({ children }: { children: ReactNode }) {
  const [uuid, setUUID] = useState<string | null>(null)
  const [agentTypes, setAgentTypes] = useState<string[]>(['claude-code'])
  const [agentType, setAgentType] = useState('claude-code')
  const [starting, setStarting] = useState(false)
  const [error, setError] = useState('')
  const [mode, setMode] = useState<OrchestratorMode>('hidden')

  useEffect(() => {
    ListAgentTypes()
      .then((types) => {
        if (types && types.length) {
          setAgentTypes(types)
          setAgentType((cur) => (types.includes(cur) ? cur : types[0]))
        }
      })
      .catch(() => undefined)
  }, [])

  // Probe for a live root session on mount AND on every idea:changed
  // event (e.g. a session spawned via the orchestrator tool, or a
  // future headless trigger). Without re-probing on change, the drawer
  // would sit on its start form while a live session ran.
  useEffect(() => {
    let cancelled = false
    const probe = () => {
      GetRunningRootSession()
        .then((res) => {
          if (cancelled) return
          if (res?.uuid) {
            setUUID((prev) => prev ?? res.uuid)
          }
        })
        .catch(() => undefined)
    }
    probe()
    const cancelChanged = EventsOn('idea:changed', probe)
    return () => {
      cancelled = true
      cancelChanged()
    }
  }, [])

  // Clear uuid when the active root session terminates so the drawer
  // re-renders its start form (with the agent-type selector) instead
  // of sitting on a dead terminal. Without this, exiting a session
  // gives the user no path back to a fresh runner choice.
  useEffect(() => {
    if (!uuid) return
    const cancel = EventsOn(`session:${uuid}:status`, (info: { status?: string }) => {
      if (info?.status === 'exited' || info?.status === 'stopped') {
        setUUID(null)
        setError('')
      }
    })
    return () => { cancel() }
  }, [uuid])

  const start = useCallback(async () => {
    setStarting(true)
    setError('')
    try {
      const result = await StartRootSession(agentType)
      if (result?.uuid) {
        setUUID(result.uuid)
      } else {
        setError('StartRootSession returned no ID')
      }
    } catch (err) {
      setError(String(err))
    } finally {
      setStarting(false)
    }
  }, [agentType])

  const value: OrchestratorContextValue = {
    uuid,
    agentTypes,
    agentType,
    setAgentType,
    starting,
    error,
    start,
    mode,
    setMode,
  }
  return <OrchestratorContext.Provider value={value}>{children}</OrchestratorContext.Provider>
}
