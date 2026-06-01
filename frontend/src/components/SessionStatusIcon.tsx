import { Circle, CircleDot, BellRing, CircleCheck, Square, OctagonAlert, CircleOff, CircleHelp, Moon } from 'lucide-react'

interface Props {
  status: string // running | completed | stopped | failed | dormant
  activity?: string // active | idle | waiting (only when running)
  stopReason?: string // orphaned | exit | shutdown | crash | user | cleared | compacted
  size?: number
}

// SessionStatusIcon renders a lucide icon for a session's lifecycle state.
// Color comes from CSS via `currentColor` on the icon — see .session-icon.*
// rules in style.css. Wraps in a span so layout/animation classes apply.
export default function SessionStatusIcon({ status, activity, stopReason, size = 12 }: Props) {
  // Orphaned trumps the underlying status — the record is no longer
  // backed by an on-disk transcript, so the lifecycle state is moot.
  if (stopReason === 'orphaned') {
    return (
      <span className="session-icon orphaned" aria-label="orphaned">
        <CircleOff size={size} strokeWidth={2} />
      </span>
    )
  }
  if (status === 'running') {
    if (activity === 'active') {
      return (
        <span className="session-icon active" aria-label="active">
          <CircleDot size={size} strokeWidth={2.5} />
        </span>
      )
    }
    if (activity === 'reviewing') {
      return (
        <span className="session-icon reviewing" aria-label="reviewing">
          <CircleHelp size={size} strokeWidth={2} />
        </span>
      )
    }
    if (activity === 'waiting') {
      return (
        <span className="session-icon waiting" aria-label="waiting">
          <BellRing size={size} strokeWidth={2} />
        </span>
      )
    }
    return (
      <span className="session-icon idle" aria-label="idle">
        <Circle size={size} strokeWidth={2.5} />
      </span>
    )
  }
  if (status === 'completed') {
    return (
      <span className="session-icon completed" aria-label="completed">
        <CircleCheck size={size} strokeWidth={2} />
      </span>
    )
  }
  if (status === 'stopped') {
    return (
      <span className="session-icon stopped" aria-label="stopped">
        <Square size={size} strokeWidth={2} />
      </span>
    )
  }
  if (status === 'failed') {
    return (
      <span className="session-icon failed" aria-label="failed">
        <OctagonAlert size={size} strokeWidth={2} />
      </span>
    )
  }
  if (status === 'dormant') {
    return (
      <span className="session-icon dormant" aria-label="dormant">
        <Moon size={size} strokeWidth={2} />
      </span>
    )
  }
  return (
    <span className="session-icon" aria-label={status}>
      <Circle size={size} strokeWidth={2} />
    </span>
  )
}
