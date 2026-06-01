import { useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { Minimize2 } from 'lucide-react'
import { useOrchestrator } from '../contexts/OrchestratorContext'

// Orchestrator full-screen view. The OrchestratorHost (at App root) keeps
// the terminal mounted across the route change; this component's only
// jobs are declaring mode='fullscreen' so the host repositions over
// the main pane, and offering a collapse affordance back to whatever
// the user was on before.
export default function Orchestrator() {
  const navigate = useNavigate()
  const { uuid, setMode } = useOrchestrator()

  useEffect(() => {
    if (!uuid) return
    setMode('fullscreen')
    return () => setMode('hidden')
  }, [uuid, setMode])

  const collapse = () => {
    if (window.history.length > 1) {
      navigate(-1)
    } else {
      navigate('/')
    }
  }

  return (
    <div className="orchestrator-fullscreen" data-testid="orchestrator-fullscreen">
      <button
        type="button"
        className="orchestrator-mode-overlay"
        title="Collapse orchestrator"
        aria-label="Collapse orchestrator"
        onClick={collapse}
      >
        <Minimize2 size={14} strokeWidth={1.75} />
      </button>
      {!uuid && (
        <p className="orchestrator-drawer-empty">
          No orchestrator session is running. Open the drawer and click Start.
        </p>
      )}
    </div>
  )
}
