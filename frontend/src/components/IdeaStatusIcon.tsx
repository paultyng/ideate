import { Feather } from 'lucide-react'

interface Props {
  status: string // active | paused | archived
  size?: number
}

// IdeaStatusIcon renders the idea's lifecycle status as a Feather icon.
// Color comes from CSS via `currentColor` — see .idea-icon.* in style.css.
export default function IdeaStatusIcon({ status, size = 14 }: Props) {
  if (status === 'archived') {
    return (
      <span className="idea-icon archived" aria-label="archived">
        <Feather size={size} strokeWidth={1.5} />
      </span>
    )
  }
  if (status === 'active') {
    return (
      <span className="idea-icon active" aria-label="active">
        <Feather size={size} strokeWidth={2} fill="currentColor" />
      </span>
    )
  }
  if (status === 'paused') {
    return (
      <span className="idea-icon paused" aria-label="paused">
        <Feather size={size} strokeWidth={1.5} />
      </span>
    )
  }
  return (
    <span className="idea-icon" aria-label={status || 'unknown'}>
      <Feather size={size} strokeWidth={1.5} />
    </span>
  )
}
