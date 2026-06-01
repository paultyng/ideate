import { ReactNode, KeyboardEvent, MouseEvent } from 'react'

interface Props {
  header: ReactNode
  summary?: ReactNode
  body?: ReactNode
  meta?: ReactNode
  // Click handler for the whole card. Cards have exactly one
  // interactable unit by design — no nested click targets, no
  // secondary navigation affordances.
  onActivate?: () => void
  ariaLabel?: string
  // Extra classnames appended after `card-shell`. Variants (current,
  // attention, active/waiting) live here so consumers can color the
  // card without re-declaring the shell.
  className?: string
  title?: string
}

// CardShell is the shared visual primitive used by every list-style
// surface: dashboard idea cards, the footer's overflow popover
// session cards, and (later) the Cmd+K palette. The shell owns
// padding, border, hover, and the header/summary/body/meta slot
// layout; consumers compose their content into those slots and pick
// the click target. Anything that lives in one of these lists —
// even non-card items — should at minimum match the shell's outer
// shape so the lists read as one kind of object.
export default function CardShell({
  header,
  summary,
  body,
  meta,
  onActivate,
  ariaLabel,
  className,
  title,
}: Props) {
  const interactable = !!onActivate
  const handleKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (!onActivate) return
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      onActivate()
    }
  }
  const handleClick = (_e: MouseEvent<HTMLDivElement>) => {
    if (onActivate) onActivate()
  }
  return (
    <div
      role={interactable ? 'button' : undefined}
      tabIndex={interactable ? 0 : undefined}
      aria-label={ariaLabel}
      title={title}
      className={`card-shell${className ? ' ' + className : ''}`}
      onClick={interactable ? handleClick : undefined}
      onKeyDown={interactable ? handleKey : undefined}
    >
      <div className="card-shell-header">{header}</div>
      {summary && <p className="card-shell-summary">{summary}</p>}
      {body}
      {meta && <div className="card-shell-meta">{meta}</div>}
    </div>
  )
}
