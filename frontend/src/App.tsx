import { useEffect, useState } from 'react'
import { Routes, Route, useNavigate, useLocation } from 'react-router-dom'
import { Home, Notebook, Plus } from 'lucide-react'
import { useWailsNavigation } from './hooks/useNavigation'
import { useOrchestratorDrawer } from './hooks/useOrchestratorDrawer'
import { EventsOn, WindowIsFullscreen, WindowToggleMaximise } from './wailsjs/runtime/runtime'
import IdeaList from './views/IdeaList'
import IdeaDetail from './views/IdeaDetail'
import IdeaForm from './views/IdeaForm'
import IdeaSession from './views/IdeaSession'
import Review from './views/Review'
import Orchestrator from './views/Orchestrator'
import CrashRecoveryToast from './components/CrashRecoveryToast'
import CloseGuardDialog from './components/CloseGuardDialog'
import ConfirmDialog from './components/ConfirmDialog'
import AppFooter from './components/AppFooter'
import AppTopbarTitle from './components/AppTopbarTitle'
import CommandPalette from './components/CommandPalette'
import OrchestratorDrawer from './components/OrchestratorDrawer'
import OrchestratorHost from './components/OrchestratorHost'
import { OrchestratorProvider } from './contexts/OrchestratorContext'
import { MRUProvider } from './contexts/MRUContext'
import { TOPBAR_SLOT_ID } from './components/TopbarActions'

// useOrchestratorNavBridge subscribes to `orchestrator:navigate` events
// emitted by the orchestration MCP nav tools (goto_idea, goto_dashboard,
// goto_session) and routes the payload's `path` through the SPA router.
// Hash-mutation here would also work, but useNavigate keeps router
// state in sync with the rest of the app's nav surface.
function useOrchestratorNavBridge() {
  const navigate = useNavigate()
  useEffect(() => {
    const cancel = EventsOn('orchestrator:navigate', (payload: { path?: string }) => {
      if (payload?.path) navigate(payload.path)
    })
    return () => cancel()
  }, [navigate])
}

// useIdeaRenamedRedirect listens for the `idea:renamed` broker event
// (emitted by rename_idea on the orchestrator MCP) and, when the user
// is currently sitting on /idea/<old-slug> (or any /idea/<old-slug>/…
// subroute), translates the slug-bearing segment to the new value
// without losing the rest of the path. Other routes are untouched.
function useIdeaRenamedRedirect(): void {
  const navigate = useNavigate()
  const location = useLocation()
  useEffect(() => {
    const cancel = EventsOn('idea:renamed', (payload: { old_slug?: string; new_slug?: string }) => {
      if (!payload?.old_slug || !payload?.new_slug) return
      const prefix = `/idea/${payload.old_slug}`
      // Only rewrite when the slug is the path's *segment*, not a
      // substring of another idea's slug — match the prefix followed
      // by end-of-string, "/", or "?".
      const path = location.pathname
      if (path !== prefix && !path.startsWith(prefix + '/')) return
      const rest = path.slice(prefix.length)
      navigate(`/idea/${payload.new_slug}${rest}${location.search}`, { replace: true })
    })
    return () => cancel()
  }, [navigate, location.pathname, location.search])
}

// useDisableWebviewNav swallows the WKWebView shortcuts that would
// navigate the browser history (Cmd+[ back, Cmd+] forward, plus the
// Cmd+Arrow variants). HashRouter is the only nav surface; webview
// history-pop drops the user out of the app's React state.
function useDisableWebviewNav(): void {
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (!e.metaKey || e.ctrlKey || e.altKey) return
      if (e.key === '[' || e.key === ']' || e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
        e.preventDefault()
      }
    }
    window.addEventListener('keydown', onKeyDown, { capture: true })
    return () => window.removeEventListener('keydown', onKeyDown, { capture: true })
  }, [])
}

// useCmdK toggles the command palette open on Cmd+K / Ctrl+K from
// anywhere in the app. Capture-phase at window level so it fires
// before xterm's helper textarea (or any other focused input) can
// consume the keystroke. TerminalPanel's custom-key handler swallows
// the K so it doesn't reach the PTY but does NOT re-dispatch it —
// the capture-phase listener has already handled the toggle.
function useCmdK(setOpen: (fn: (o: boolean) => boolean) => void): void {
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey
      if (!mod || e.altKey) return
      if (e.key.toLowerCase() !== 'k') return
      e.preventDefault()
      setOpen((o) => !o)
    }
    window.addEventListener('keydown', onKeyDown, { capture: true })
    return () => {
      window.removeEventListener('keydown', onKeyDown, { capture: true })
    }
  }, [setOpen])
}

// useFullscreenBodyAttr toggles `body[data-fullscreen]` whenever the
// macOS window enters/leaves native fullscreen so CSS can reclaim the
// 80px traffic-lights pad in the topbar. WKWebView fires the standard
// `fullscreenchange` event on the document when the green-button
// fullscreen toggles, and we additionally probe WindowIsFullscreen()
// once on mount so a window that started fullscreen renders correctly.
function useFullscreenBodyAttr(): void {
  useEffect(() => {
    const apply = (on: boolean) => {
      if (on) document.body.setAttribute('data-fullscreen', '')
      else document.body.removeAttribute('data-fullscreen')
    }
    WindowIsFullscreen().then(apply).catch(() => {})
    const onChange = () => apply(!!document.fullscreenElement)
    document.addEventListener('fullscreenchange', onChange)
    // Wails v2 doesn't reliably emit a JS-level fullscreen event for
    // native green-button toggles on macOS, so as a belt-and-braces
    // fallback poll WindowIsFullscreen on resize. The check is cheap
    // and only runs on user-driven layout changes.
    const onResize = () => { WindowIsFullscreen().then(apply).catch(() => {}) }
    window.addEventListener('resize', onResize)
    return () => {
      document.removeEventListener('fullscreenchange', onChange)
      window.removeEventListener('resize', onResize)
    }
  }, [])
}

function AppTopbar({ orchestratorPinned }: { orchestratorPinned: boolean }) {
  const navigate = useNavigate()
  const { open: orchestratorOpen, toggle: toggleOrchestrator } = useOrchestratorDrawer()
  // On home the drawer is pinned: the user-toggle state still tracks
  // their preference for non-home routes, but the visible affordance
  // (button pressed, drawer rendered) overrides to "open".
  const visiblyOpen = orchestratorOpen || orchestratorPinned
  // The view-actions slot is a portal target. The separator and the
  // slot's own visual presence are both gated by CSS `:empty` so the
  // home/orchestrator/+ cluster doesn't trail an orphaned divider when
  // no view has injected actions.
  // Native macOS titlebars toggle maximize on double-click; Wails
  // doesn't replicate that for hidden-titlebar custom topbars, so wire
  // it up explicitly so the topbar feels like one.
  const onDoubleClick = (e: React.MouseEvent<HTMLElement>) => {
    // Only the bare topbar / non-interactive container areas — let
    // double-clicks on buttons land where they belong.
    if ((e.target as HTMLElement).closest('button, a, [role="button"]')) return
    WindowToggleMaximise()
  }
  return (
    <header className="app-topbar" onDoubleClick={onDoubleClick}>
      <div className="app-topbar-left">
        <span className="app-wordmark" aria-hidden="true">ideate</span>
        <button
          type="button"
          className="app-home"
          title="Home — idea list"
          aria-label="Home"
          onClick={() => navigate('/')}
        >
          <Home size={16} strokeWidth={1.75} />
        </button>
        <button
          type="button"
          className={`app-orchestrator${visiblyOpen ? ' open' : ''}`}
          title="Orchestrator"
          aria-label="Orchestrator"
          aria-pressed={visiblyOpen}
          onClick={toggleOrchestrator}
          disabled={orchestratorPinned}
        >
          <Notebook size={16} strokeWidth={1.75} />
        </button>
        <button
          type="button"
          className="app-new-idea"
          title="New idea"
          aria-label="New idea"
          onClick={() => navigate('/idea/new')}
        >
          <Plus size={16} strokeWidth={1.75} />
        </button>
        <div className="app-topbar-view-actions" id={TOPBAR_SLOT_ID} />
        <AppTopbarTitle />
      </div>
    </header>
  )
}

export default function App() {
  useWailsNavigation()
  useOrchestratorNavBridge()
  useIdeaRenamedRedirect()
  useFullscreenBodyAttr()
  useDisableWebviewNav()
  const [paletteOpen, setPaletteOpen] = useState(false)
  useCmdK(setPaletteOpen)
  const location = useLocation()
  // The dashboard (home) pins the orchestrator drawer open: ideation
  // starts there, the orchestrator tools are the primary creation
  // surface. The home pin is transient — it does NOT update the
  // user's persisted open/closed preference, so navigating away from
  // home returns the drawer to whatever the user last toggled it to
  // on a non-home route. Result: the drawer is a deliberate choice
  // off-home, regardless of whether the user came from /.
  const orchestratorPinned = location.pathname === '/'
  // On /orchestrator the full-screen view owns the host's positioning;
  // hide the drawer entirely so its mode='drawer' effect doesn't race
  // with the view's mode='fullscreen' on mount.
  const drawerSuppressed = location.pathname === '/orchestrator'

  return (
    <OrchestratorProvider>
      <MRUProvider>
      <AppTopbar orchestratorPinned={orchestratorPinned} />
      {!drawerSuppressed && <OrchestratorDrawer pinned={orchestratorPinned} />}
      <CrashRecoveryToast />
      <CloseGuardDialog />
      <ConfirmDialog />
      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} />
      <main className="app-main">
        <Routes>
          <Route path="/" element={<IdeaList />} />
          <Route path="/idea/new" element={<IdeaForm />} />
          <Route path="/idea/:slug" element={<IdeaDetail />} />
          <Route path="/idea/:slug/session/:sessionId" element={<IdeaSession />} />
          <Route path="/review" element={<Review />} />
          <Route path="/orchestrator" element={<Orchestrator />} />
        </Routes>
      </main>
      <AppFooter />
      <OrchestratorHost />
      </MRUProvider>
    </OrchestratorProvider>
  )
}
