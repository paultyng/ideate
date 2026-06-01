import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { X } from 'lucide-react'
import { EventsOn } from '../wailsjs/runtime/runtime'

interface RecoveredSession {
  slug: string
  ideaName: string
  uuid: string
  agentType: string
  repoName?: string
  reason: string
}

// CrashRecoveryToast listens for the one-shot session:recovery event the
// backend emits after the auto-resume sweep on startup detected and
// resumed crash-stopped sessions. Non-persistent: dismissable, clears
// itself after a few seconds, only fires once per app launch.
export default function CrashRecoveryToast() {
  const navigate = useNavigate()
  const [recovered, setRecovered] = useState<RecoveredSession[] | null>(null)

  useEffect(() => {
    const cancel = EventsOn('session:recovery', (sessions: RecoveredSession[]) => {
      if (sessions && sessions.length > 0) {
        setRecovered(sessions)
      }
    })
    return () => { cancel() }
  }, [])

  if (!recovered) return null

  return (
    <div className="crash-recovery-toast" role="status">
      <div className="crash-recovery-toast-body">
        <strong>Restored {recovered.length} session{recovered.length !== 1 ? 's' : ''} after unexpected exit.</strong>
        <ul>
          {recovered.map((s) => (
            <li key={s.uuid}>
              <button
                type="button"
                className="btn-link"
                onClick={() => navigate(`/idea/${s.slug}/session/${s.uuid}`)}
              >
                {s.ideaName} — {s.agentType}
              </button>
            </li>
          ))}
        </ul>
      </div>
      <button
        type="button"
        className="crash-recovery-toast-dismiss"
        onClick={() => setRecovered(null)}
        title="Dismiss"
        aria-label="Dismiss"
      >
        <X size={14} strokeWidth={2} />
      </button>
    </div>
  )
}
