import { ReactNode, useEffect, useState } from 'react'
import { createPortal } from 'react-dom'

// TopbarActions lets detail views inject their toolbar buttons (back,
// stop, edit, etc.) into the global topbar so the page-level toolbar
// row goes away entirely. The macOS chrome + topbar then collapse into
// a single row containing both the global affordances (home / orchestrator
// / new) and the route-specific ones.
//
// Implemented as a React portal: the consumer view renders <TopbarActions/>
// inside its own JSX tree, and the buttons inside it portal into the
// topbar's slot div. The portal updates every time the consumer re-renders
// (no state-machine, no closures, no version counters) and unmounts
// cleanly when the consumer unmounts.

export const TOPBAR_SLOT_ID = 'topbar-actions-slot'

interface Props {
  children: ReactNode
}

// TopbarActions portals its children into the global topbar slot.
// Wait for the target to mount on first paint before portaling so the
// initial mount of the consumer doesn't try to portal into a missing
// node (the slot div renders at the top of the React tree, but
// portals need the live DOM node, which exists after the first commit).
export default function TopbarActions({ children }: Props) {
  const [target, setTarget] = useState<HTMLElement | null>(null)

  useEffect(() => {
    setTarget(document.getElementById(TOPBAR_SLOT_ID))
  }, [])

  if (!target) return null
  return createPortal(children, target)
}
