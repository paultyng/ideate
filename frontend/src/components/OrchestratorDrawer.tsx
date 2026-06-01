import { useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { Maximize2, X } from 'lucide-react'
import { useOrchestratorDrawer } from '../hooks/useOrchestratorDrawer'
import { useOrchestrator } from '../contexts/OrchestratorContext'


// Drawer that hosts the workspace-wide orchestrator session. Sits between
// .app-topbar and .app-main when open, pushing main content down — the
// terminal benefits from full width more than full height. Replaces the
// previous /orchestrator route + chip-in-bar surface; the drawer is now
// the single mount point for the root session, so Phase C nav tools can
// move the user across ideas without yanking the orchestrator with
// them.
//
// The terminal itself is mounted once by OrchestratorHost at App root.
// The drawer's role is two-part:
//   1. Reserve the 320px viewport slot (sets --app-drawer-height).
//   2. Declare mode='drawer' so the host's CSS positions the terminal
//      over the slot. Unmounting the drawer cleans the mode back to
//      'hidden', letting another surface claim the terminal.
interface Props {
  // pinned forces the drawer visible and hides the close affordance —
  // used on the dashboard so ideation starts in the orchestrator
  // surface. The user-toggle state is preserved for non-pinned routes.
  pinned?: boolean
}

export default function OrchestratorDrawer({ pinned = false }: Props) {
  const navigate = useNavigate()
  const { open, setOpen, height } = useOrchestratorDrawer()
  const visiblyOpen = open || pinned
  const {
    uuid,
    agentTypes,
    agentType,
    setAgentType,
    starting,
    error,
    start,
    setMode,
  } = useOrchestrator()

  // Reserve viewport space when visible. Views' `100vh - …` calcs read
  // --app-drawer-height; flipping it here keeps them inside the
  // visible area when the drawer pushes them down. Reset to 0 on
  // unmount so a stale variable doesn't leak across hot reloads.
  useEffect(() => {
    document.documentElement.style.setProperty(
      '--app-drawer-height',
      visiblyOpen ? `${height}px` : '0px',
    )
    return () => {
      document.documentElement.style.setProperty('--app-drawer-height', '0px')
    }
  }, [visiblyOpen, height])

  // Tell the host to render in drawer mode while the drawer is visible
  // AND has a session to host. Cleanup returns the host to 'hidden'.
  useEffect(() => {
    if (!visiblyOpen || !uuid) return
    setMode('drawer')
    return () => setMode('hidden')
  }, [visiblyOpen, uuid, setMode])

  // Hidden: render nothing. Aria-hidden so screen readers skip it; the
  // topbar button carries the toggle affordance.
  if (!visiblyOpen) return null

  return (
    <aside
      className="orchestrator-drawer"
      aria-label="Orchestrator"
      data-testid="orchestrator-drawer"
    >
      {/* Floating expand button — only when a session is running.
          When no session, the start-form toolbar carries its own
          affordances; an expand affordance there would be confusing
          (nothing to expand). Lives ABOVE the host overlay via
          z-index, otherwise the terminal would eat the click. */}
      {uuid && (
        <button
          type="button"
          className="orchestrator-mode-overlay"
          title="Expand orchestrator"
          aria-label="Expand orchestrator"
          onClick={() => navigate('/orchestrator')}
        >
          <Maximize2 size={14} strokeWidth={1.75} />
        </button>
      )}
      {/* While the orchestrator session is running the toolbar is hidden
          entirely — the terminal carries the conversation, the topbar's
          Notebook button toggles drawer visibility, and stop/restart
          live in the agent's own /clear / /end commands rather than a
          chrome button. The toolbar reappears when there's no live
          session so the user can pick an agent and Start. The close
          button is shown alongside Start when the drawer isn't pinned. */}
      {!uuid && (
        <div className="orchestrator-drawer-toolbar">
          <span className="orchestrator-drawer-title">Ideate</span>
          <label className="orchestrator-drawer-agent">
            Agent
            <select value={agentType} onChange={(e) => setAgentType(e.target.value)}>
              {agentTypes.map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </select>
          </label>
          <button className="btn-primary" onClick={start} disabled={starting}>
            {starting ? 'Starting…' : 'Start'}
          </button>
          {!pinned && (
            <button
              type="button"
              className="orchestrator-drawer-close"
              aria-label="Close orchestrator"
              title="Close orchestrator"
              onClick={() => setOpen(false)}
            >
              <X size={16} strokeWidth={1.75} />
            </button>
          )}
        </div>
      )}

      {/* Body intentionally empty when uuid is set — the host
          overlays this region via fixed positioning. When no session,
          the empty placeholder explains what the orchestrator is for. */}
      <div className="orchestrator-drawer-body">
        {!uuid && (
          <p className="orchestrator-drawer-empty">
            A workspace-wide session that isn't attached to any idea. The agent runs in
            your ideas root and can list, create, and update ideas via the cross-idea
            MCP tools.
          </p>
        )}
        {error && <p className="session-error" style={{ padding: '8px 16px' }}>{error}</p>}
      </div>
    </aside>
  )
}
