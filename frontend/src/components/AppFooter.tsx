import { useEffect, useState } from 'react'
import { Coffee } from 'lucide-react'
import { GetAppStatus } from '../wailsjs/go/app/App'
import useSleepState from '../hooks/useSleepState'
import GlobalSessionBar from './GlobalSessionBar'
import PendingReviewsBar from './PendingReviewsBar'

interface StatusInfo {
  version: string
  uptime: string
}

// sleepTitle maps the (enabled, held) tuple to a hover hint that
// teaches the icon's three states without the user having to guess.
function sleepTitle(enabled: boolean, held: boolean): string {
  if (!enabled) {
    return 'Prevent sleep: off — Mac may sleep while a session is working. Click to enable.'
  }
  if (held) {
    return 'Prevent sleep: on (active) — holding the Mac awake while a session is working. Click to disable.'
  }
  return 'Prevent sleep: on (idle) — will keep the Mac awake when a session becomes active. Click to disable.'
}

function sleepLabel(enabled: boolean, held: boolean): string {
  if (!enabled) return 'Prevent sleep: off'
  return held ? 'Prevent sleep: on (active)' : 'Prevent sleep: on (idle)'
}

// AppFooter is the global status strip at the bottom of every view —
// version (always) + uptime (debug-only). Lives at the app level so
// views don't have to reserve their own space at the bottom of their
// layout (which previously caused whitespace below short content when
// the orchestrator drawer compressed the viewport).
//
// uptime is a developer datum — interesting for catching stale dev
// servers, noise in daily-driver use. Surface it only when the URL
// hash carries `?debug=1` so power users can flip it on without a
// rebuild. Could later move to a settings toggle.
function isDebugMode(): boolean {
  if (typeof window === 'undefined') return false
  const hash = window.location.hash || ''
  const q = hash.indexOf('?')
  if (q < 0) return false
  const params = new URLSearchParams(hash.slice(q + 1))
  return params.get('debug') === '1'
}

export default function AppFooter() {
  const [status, setStatus] = useState<StatusInfo | null>(null)
  const debug = isDebugMode()
  const { state: sleep, setEnabled: setSleepEnabled } = useSleepState()

  useEffect(() => {
    const fetchStatus = () => {
      GetAppStatus()
        .then(setStatus)
        .catch(() => setStatus(null))
    }
    fetchStatus()
    // Polling cadence is only worth it when uptime is visible.
    if (!debug) return
    const interval = setInterval(fetchStatus, 5000)
    return () => clearInterval(interval)
  }, [debug])

  // Sleep toggle's three visual states are encoded as classes so CSS
  // owns the styling — `disabled` for off, `idle` for on-but-not-held,
  // `held` for on-and-actively-keeping-awake. The `active` className is
  // a separate marker the Playwright tests assert against (held === active).
  const sleepClass = !sleep.enabled
    ? 'disabled'
    : sleep.held
      ? 'held active'
      : 'idle'

  const sleepToggle = (
    <button
      type="button"
      className={`app-sleep-toggle ${sleepClass}`}
      title={sleepTitle(sleep.enabled, sleep.held)}
      aria-label={sleepLabel(sleep.enabled, sleep.held)}
      aria-pressed={sleep.enabled}
      data-state={sleepClass.split(' ')[0]}
      onClick={() => setSleepEnabled(!sleep.enabled)}
    >
      <Coffee size={14} strokeWidth={1.75} />
    </button>
  )

  // Four-zone layout (Phase A of the bottom-tabs reframe):
  //   overflow  | tabs (sessions)  | alerts (reviews)  | system
  // overflow is empty in Phase A — Phase B turns it into the upward-
  // expanding `+N` stack. The session chips and the review chip move
  // here from the topbar so the orchestrator drawer doesn't sever
  // the chrome when expanded.
  return (
    <footer className="app-footer">
      <div className="app-footer-overflow" aria-hidden />
      <div className="app-footer-tabs">
        <GlobalSessionBar />
      </div>
      <div className="app-footer-alerts">
        <PendingReviewsBar />
      </div>
      <div className="app-footer-system">
        {/* version.Version already carries the leading 'v' from `git
            describe --tags` (e.g. "v0.1.3"); don't prepend a second one. */}
        {status && <span>{status.version}</span>}
        {status && debug && <span>uptime {status.uptime}</span>}
        {sleepToggle}
      </div>
    </footer>
  )
}
