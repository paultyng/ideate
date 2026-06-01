import { useEffect, useState } from 'react'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { ForceQuit } from '../wailsjs/go/app/App'

interface BusySession {
  slug: string
  ideaName: string
  uuid: string
  agentType: string
  activity: string
}

// CloseGuardDialog renders a modal when the backend blocks an app close
// because one or more sessions are non-idle. The user picks "Stop & Quit"
// (forceQuit → graceful Shutdown) or Cancel (dismiss; app stays open).
export default function CloseGuardDialog() {
  const [busy, setBusy] = useState<BusySession[] | null>(null)

  useEffect(() => {
    const cancel = EventsOn('app:close-blocked', (sessions: BusySession[]) => {
      if (sessions && sessions.length > 0) {
        setBusy(sessions)
      }
    })
    return () => { cancel() }
  }, [])

  if (!busy) return null

  const handleStopAndQuit = () => {
    setBusy(null)
    ForceQuit().catch(() => { /* shutting down anyway */ })
  }

  const handleCancel = () => setBusy(null)

  return (
    <div className="close-guard-overlay" role="dialog" aria-modal="true">
      <div className="close-guard-dialog">
        <h2>{busy.length} active session{busy.length !== 1 ? 's' : ''}</h2>
        <p>The following are still working:</p>
        <ul>
          {busy.map((s) => (
            <li key={s.uuid}>
              <strong>{s.ideaName}</strong> — {s.agentType} ({s.activity})
            </li>
          ))}
        </ul>
        <p>Quit anyway? They will be stopped and auto-resumed on next launch.</p>
        <div className="close-guard-actions">
          <button type="button" className="btn-secondary" onClick={handleCancel}>
            Cancel
          </button>
          <button type="button" className="btn-primary" onClick={handleStopAndQuit}>
            Stop &amp; Quit
          </button>
        </div>
      </div>
    </div>
  )
}
