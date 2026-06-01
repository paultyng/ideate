// ptyCapture is a test-only side-channel for the raw PTY byte stream.
//
// Why it exists: real coding agents (Claude Code, Codex) and our testagent
// all run in alt-screen mode. xterm.js's alt buffer has no scrollback —
// once content scrolls past the viewport it's gone, and the buffer is
// cleared on alt-screen exit. Playwright assertions that walk
// `terminal.buffer.active` therefore can't verify content emitted earlier
// in a session.
//
// This module captures the unmodified byte stream as it arrives at
// TerminalPanel, before xterm processes it, so tests can read the
// cumulative output regardless of where xterm rendered it.
//
// Disabled by default (no overhead). Tests enable it via
// `window.__enableCapturePty()` and read with `window.__capturedPty(id)`.

interface CaptureWindow {
  __enableCapturePty?: () => void
  __capturedPty?: (sessionId?: string) => string
  __resetCapturePty?: () => void
}

let enabled = false
const buffers = new Map<string, Uint8Array[]>()

export function capturePty(sessionId: string, bytes: Uint8Array): void {
  if (!enabled) return
  let chunks = buffers.get(sessionId)
  if (!chunks) {
    chunks = []
    buffers.set(sessionId, chunks)
  }
  chunks.push(bytes)
}

// Strip ANSI escape sequences (CSI, OSC, single-char ESC) so substring
// assertions don't have to know about them. Preserves printable text and
// newlines.
function stripAnsi(s: string): string {
  return s
    .replace(/\x1b\][^\x07\x1b]*(\x07|\x1b\\)/g, '') // OSC...BEL or ST
    .replace(/\x1b[@-Z\\-_]/g, '') // single-char ESC
    .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '') // CSI
    .replace(/[\x00-\x08\x0b-\x1f\x7f]/g, '') // other C0 (keep \t \n)
}

function readCaptured(sessionId?: string): string {
  // sessionId omitted → concatenate every captured session in
  // registration order. Lets tests that don't track the generated
  // session id read "the agent's output" without juggling keys.
  const chunkLists: Uint8Array[][] = []
  if (sessionId === undefined) {
    for (const v of buffers.values()) chunkLists.push(v)
  } else {
    const v = buffers.get(sessionId)
    if (v) chunkLists.push(v)
  }
  let total = 0
  for (const list of chunkLists) for (const c of list) total += c.length
  if (total === 0) return ''
  const all = new Uint8Array(total)
  let offset = 0
  for (const list of chunkLists) {
    for (const c of list) {
      all.set(c, offset)
      offset += c.length
    }
  }
  const decoded = new TextDecoder('utf-8', { fatal: false }).decode(all)
  return stripAnsi(decoded)
}

// Install the test-side hooks on window. Idempotent.
export function installPtyCaptureHooks(): void {
  const w = window as CaptureWindow
  if (w.__enableCapturePty) return
  w.__enableCapturePty = () => {
    enabled = true
  }
  w.__capturedPty = readCaptured
  w.__resetCapturePty = () => {
    buffers.clear()
  }
}
