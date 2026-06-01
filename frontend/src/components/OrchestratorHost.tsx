import { useEffect, useRef } from 'react'
import { useOrchestrator } from '../contexts/OrchestratorContext'
import { useOrchestratorDrawer, MIN_DRAWER_HEIGHT } from '../hooks/useOrchestratorDrawer'
import TerminalPanel from './TerminalPanel'

// OrchestratorHost mounts the orchestrator's TerminalPanel only while a
// surface (drawer or fullscreen) is asking to show it. Hidden mode
// unmounts: vscreen replay seeds a fresh xterm on next open. The
// AgentCoordinator's Go-side session is unaffected — uuid persists,
// the PTY keeps running, RegisterSessionViewer / UnregisterSessionViewer
// gates the live wire while no viewer is attached. Mount-on-visible
// keeps cell-pixel state always-fresh (no stale-buffer reflow on
// re-surface) and means the WebGL atlas never outlives a hide.
export default function OrchestratorHost() {
  const { uuid, mode } = useOrchestrator()
  const { setHeight } = useOrchestratorDrawer()
  const dragStateRef = useRef<{ startY: number; startHeight: number } | null>(null)

  useEffect(() => {
    const onMove = (e: MouseEvent) => {
      const s = dragStateRef.current
      if (!s) return
      const max = Math.max(
        MIN_DRAWER_HEIGHT,
        window.innerHeight - 24, // leave a sliver for the topbar/footer
      )
      const next = Math.min(max, Math.max(MIN_DRAWER_HEIGHT, s.startHeight + (e.clientY - s.startY)))
      setHeight(next)
    }
    const onUp = () => {
      if (!dragStateRef.current) return
      dragStateRef.current = null
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
    return () => {
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
    }
  }, [setHeight])

  if (!uuid || mode === 'hidden') return null

  const onHandleMouseDown = (e: React.MouseEvent) => {
    e.preventDefault()
    const styleHeight = parseInt(getComputedStyle(document.documentElement).getPropertyValue('--app-drawer-height'), 10)
    dragStateRef.current = {
      startY: e.clientY,
      startHeight: Number.isFinite(styleHeight) ? styleHeight : MIN_DRAWER_HEIGHT,
    }
    document.body.style.cursor = 'row-resize'
    document.body.style.userSelect = 'none'
  }

  return (
    <div
      className={`orchestrator-host orchestrator-host--${mode}`}
      data-testid="orchestrator-host"
    >
      <TerminalPanel sessionId={uuid} />
      {mode === 'drawer' && (
        <div
          className="orchestrator-host-resize"
          role="separator"
          aria-orientation="horizontal"
          aria-label="Resize orchestrator"
          onMouseDown={onHandleMouseDown}
        />
      )}
    </div>
  )
}
