import { useRef, useEffect } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebglAddon } from '@xterm/addon-webgl'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { openExternal } from '../lib/links'
import { capturePty } from '../lib/ptyCapture'
import { termDebug } from '../lib/termDebug'
import '@xterm/xterm/css/xterm.css'

interface TerminalPanelProps {
  sessionId: string
}

function base64ToUint8Array(base64: string): Uint8Array {
  const binary = atob(base64)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i)
  }
  return bytes
}

async function callBinding(method: string, ...args: unknown[]) {
  try {
    const mod = await import('../wailsjs/go/app/App')
    const fn = (mod as Record<string, unknown>)[method] as (...a: unknown[]) => Promise<unknown>
    if (fn) return await fn(...args)
  } catch {
    console.warn(`Binding ${method} not available`)
  }
}

export default function TerminalPanel({ sessionId }: TerminalPanelProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const terminalRef = useRef<Terminal | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)

  useEffect(() => {
    if (!containerRef.current) return
    termDebug('mount', sessionId)

    const terminal = new Terminal({
      theme: {
        background: '#1a1a1a',
        foreground: '#e0e0e0',
        cursor: '#e0e0e0',
      },
      fontFamily: 'Menlo, Consolas, monospace',
      fontSize: 13,
      // OSC 8 hyperlinks (escape-sequence links with display text distinct
      // from the href, e.g. as emitted by `gh`, `claude`, modern coreutils).
      // The addon below handles plain-text URLs separately.
      linkHandler: {
        activate: (_, uri) => { openExternal(uri) },
      },
    })
    terminalRef.current = terminal

    // Expose the terminal instance on `window` keyed by session id so
    // Playwright tests can read the screen buffer (xterm renders to canvas, so
    // text isn't accessible via DOM queries). Strictly a test affordance.
    const w = window as unknown as Record<string, unknown>
    const reg = (w.__ideateTerminals ??= {}) as Record<string, Terminal>
    reg[sessionId] = terminal

    const fitAddon = new FitAddon()
    fitAddonRef.current = fitAddon
    terminal.loadAddon(fitAddon)

    // Plain-URL link matcher — turns http/https tokens in raw output into
    // hoverable links. Click routes through the same scheme-allow-listed
    // BrowserOpenURL path as everything else.
    terminal.loadAddon(new WebLinksAddon((_, uri) => { openExternal(uri) }))

    terminal.open(containerRef.current)

    // Shift+Enter sends ESC+CR (the sequence Claude Code's `/terminal-setup`
    // registers in iTerm2) so the TUI inserts a newline rather than submitting.
    // Suppress all event phases — WebKit fires keypress for Enter, and xterm's
    // default keypress handler would emit `\r` on top of our ESC+CR.
    terminal.attachCustomKeyEventHandler((e) => {
      if (e.key === 'Enter' && e.shiftKey) {
        if (e.type === 'keydown') {
          callBinding('WriteToSession', sessionId, '\x1b\r')
        }
        return false
      }
      // Cmd+K / Ctrl+K opens the global command palette. App.useCmdK
      // runs its window-level keydown listener in the CAPTURE phase,
      // which fires before xterm sees the key — so the palette
      // toggles correctly without us touching it here. Our only job
      // is to return false so xterm doesn't pass ^K through to the
      // PTY. Dispatching a redundant custom event here would toggle
      // state a second time and close the palette mid-frame.
      const mod = e.metaKey || e.ctrlKey
      if (mod && !e.altKey && e.key.toLowerCase() === 'k') {
        return false
      }
      // Esc cancels the agent's current turn. Claude's Stop hook
      // eventually catches up and flips Activity to idle, but only
      // after the agent finishes its post-cancel wrap-up — typically
      // ~30s. Fire a side-channel signal so the chip flips
      // immediately. Esc still flows through to the PTY so the agent
      // actually cancels.
      if (e.key === 'Escape' && e.type === 'keydown') {
        callBinding('SignalSessionCancel', sessionId)
      }
      return true
    })

    try {
      terminal.loadAddon(new WebglAddon())
    } catch {
      // WebGL not available, fall back to canvas renderer
    }

    fitAddon.fit()
    termDebug('initial fit', sessionId, { cols: terminal.cols, rows: terminal.rows })
    // One extra fit after the next frame: the host's position:fixed
    // bounds can settle a frame after useEffect runs, leaving the
    // first fit measuring a stale container size. Re-measure once
    // layout is committed.
    const refitRaf = requestAnimationFrame(() => {
      fitAddon.fit()
      termDebug('post-rAF fit', sessionId, { cols: terminal.cols, rows: terminal.rows })
    })
    terminal.focus()

    // Toggle a visual indicator on the container when xterm has
    // keyboard focus. Multiple terminals can be visible at once
    // (orchestrator + idea session); the user needs to see at a glance
    // which one will receive their keystrokes. xterm doesn't expose a
    // typed onFocus event, so listen for the DOM focusin/focusout on
    // its helper-textarea via the container's bubbling listeners.
    const setFocusClass = (focused: boolean) => {
      if (!containerRef.current) return
      containerRef.current.classList.toggle('terminal-focused', focused)
    }
    const onFocusIn = () => setFocusClass(true)
    const onFocusOut = () => setFocusClass(false)
    containerRef.current.addEventListener('focusin', onFocusIn)
    containerRef.current.addEventListener('focusout', onFocusOut)
    // Initial state: derive from the actual activeElement rather than
    // optimistically marking the just-mounted terminal as focused.
    // The first-mounted (orchestrator) calls terminal.focus(); the
    // second-mounted (idea session) takes focus on its own .focus()
    // call, which fires focusout on the first. Lying about initial
    // state leaves the prior terminal stuck in the focused class
    // since its textarea was never actually the activeElement.
    setFocusClass(containerRef.current.contains(document.activeElement))

    // User input -> Go PTY
    const onDataDisposable = terminal.onData((data) => {
      // xterm.js answers terminal capability queries (OSC 11 for the
      // background color, OSC 4 for palette, etc.) by writing the
      // reply through onData — i.e., into the PTY's master end as
      // if the user had typed it. Bubbletea-based agents wedge their
      // input pipeline when those replies arrive (every subsequent
      // keystroke is dropped). Strip OSC sequences so the auto-
      // replies don't reach the agent. User input doesn't
      // legitimately contain OSC here.
      // eslint-disable-next-line no-control-regex
      const cleaned = data.replace(/\x1b\][^\x07\x1b]*(\x07|\x1b\\)/g, '')
      if (cleaned.length === 0) return
      callBinding('WriteToSession', sessionId, cleaned)
    })

    // Terminal resize -> Go PTY
    const onResizeDisposable = terminal.onResize(({ cols, rows }) => {
      termDebug('terminal.onResize', sessionId, { cols, rows })
      callBinding('ResizeSession', sessionId, rows, cols)
    })

    // PTY output -> terminal (base64 encoded). The replay snapshot must
    // land BEFORE any live chunks; otherwise xterm renders live output
    // first, then the replay overwrites on top, and the buffer ends up
    // scrambled (lost scroll position, ghost chrome until the next live
    // update resyncs). Buffer live chunks here until the replay write
    // completes, then flush in order.
    //
    // Subscription ordering: attach the EventsOn listener BEFORE
    // calling RegisterSessionViewer. The Go side flips `hasSessionViewer
    // → true` the instant the register RPC resolves, so any
    // session-output emit between resolve-time and EventsOn-attach is
    // silently dropped from the live wire. (vscreen captures it
    // unconditionally, and replay rehydrates the buffer, but a real-
    // time animation frame can still flicker.) Listening first +
    // buffering until replay completes gives strict before-replay /
    // after-replay ordering with zero loss on either side.
    const pending: Uint8Array[] = []
    let replayDone = false
    const writeOrBuffer = (chunk: Uint8Array) => {
      if (replayDone) {
        terminal.write(chunk)
      } else {
        pending.push(chunk)
      }
    }
    const cancelOutput = EventsOn(`session:${sessionId}:output`, (data: string) => {
      const bytes = base64ToUint8Array(data)
      writeOrBuffer(bytes)
      capturePty(sessionId, bytes)
    })

    // Session status events
    const cancelStatus = EventsOn(`session:${sessionId}:status`, (info: { status: string; exitCode?: number }) => {
      termDebug('status', sessionId, info)
      if (info.status === 'exited') {
        terminal.write(`\r\n\x1b[90m[Session exited with code ${info.exitCode ?? 0}]\x1b[0m\r\n`)
      } else if (info.status === 'stopped') {
        terminal.write('\r\n\x1b[90m[Session stopped]\x1b[0m\r\n')
      }
    })

    // Resize observer for container size changes
    const resizeObserver = new ResizeObserver((entries) => {
      const e = entries[0]
      const rect = e?.contentRect
      termDebug('container resize -> fit', sessionId, rect ? { w: rect.width, h: rect.height } : {})
      fitAddon.fit()
    })
    resizeObserver.observe(containerRef.current)

    // Now that the EventsOn listener is attached and writeOrBuffer is
    // ready to buffer pre-replay chunks, tell the backend a viewer is
    // subscribed. This flips `hasSessionViewer → true` and the Go side
    // starts emitting session-output events. Any chunk that lands
    // before replayDone falls through to the pending[] buffer.
    void callBinding('RegisterSessionViewer', sessionId)

    // Report initial size to Go AND wait for it to land before
    // fetching the replay. Without the await, the two bindings race:
    // resumed sessions ship with vscreen at 24x80 (PTY default) and
    // an in-flight ResizeSession that hasn't reached the emulator
    // yet — GetSessionReplay returns content laid out for the OLD
    // grid, which xterm renders into the new container, producing
    // visible misalignment until the next agent paint.
    termDebug('replay fetch start', sessionId)
    callBinding('ResizeSession', sessionId, terminal.rows, terminal.cols).then(() =>
      callBinding('GetSessionReplay', sessionId),
    ).then((data) => {
      const bytes = (data && typeof data === 'string') ? base64ToUint8Array(data).length : 0
      termDebug('replay fetch done', sessionId, { bytes, pending_chunks: pending.length })
      if (data && typeof data === 'string' && data.length > 0) {
        const bytes = base64ToUint8Array(data)
        terminal.write(bytes)
        capturePty(sessionId, bytes)
      }
      replayDone = true
      for (const chunk of pending) terminal.write(chunk)
      pending.length = 0
    }).catch((err) => {
      termDebug('replay fetch error', sessionId, { err: String(err), pending_chunks: pending.length })
      // Even on error, flush buffered live chunks so the terminal
      // doesn't go silent.
      replayDone = true
      for (const chunk of pending) terminal.write(chunk)
      pending.length = 0
    })

    return () => {
      termDebug('unmount', sessionId)
      void callBinding('UnregisterSessionViewer', sessionId)
      onDataDisposable.dispose()
      onResizeDisposable.dispose()
      cancelAnimationFrame(refitRaf)
      containerRef.current?.removeEventListener('focusin', onFocusIn)
      containerRef.current?.removeEventListener('focusout', onFocusOut)
      cancelOutput()
      cancelStatus()
      resizeObserver.disconnect()
      terminal.dispose()
      terminalRef.current = null
      fitAddonRef.current = null
      delete reg[sessionId]
    }
  }, [sessionId])

  return <div ref={containerRef} className="terminal-container" />
}
