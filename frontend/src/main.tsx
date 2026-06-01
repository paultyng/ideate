import { createRoot } from 'react-dom/client'
import { HashRouter } from 'react-router-dom'
import App from './App'
import { installConsoleBridge } from './lib/consoleBridge'
import { installPtyCaptureHooks } from './lib/ptyCapture'
import { initTermDebug } from './lib/termDebug'
import '@fontsource/geist-sans/400.css'
import '@fontsource/geist-sans/500.css'
import '@fontsource/geist-mono/400.css'
import '@fontsource/newsreader/400.css'
import '@fontsource/newsreader/400-italic.css'
import './style.css'

// Forward every console.* + window 'error' + 'unhandledrejection'
// into the Wails logger so dev-session output survives app restart
// (DevTools doesn't persist; the Go logger's stderr stream is
// captured by `task dev`'s tee'd log file). The Go-side LogLevel
// drops debug calls in production / when IDEATE_TERM_DEBUG isn't
// set, so this stays cheap when no one is debugging.
installConsoleBridge()

// Resolve the env-derived termDebug flag. Fire-and-forget — early
// termDebug() calls before the binding resolves are dropped, which
// is acceptable for a dev-only tool.
void initTermDebug()

// Install playwright's PTY raw-byte capture hooks. No-op until a test
// calls window.__enableCapturePty(); see lib/ptyCapture.ts.
installPtyCaptureHooks()

createRoot(document.getElementById('root')!).render(
  <HashRouter>
    <App />
  </HashRouter>
)
