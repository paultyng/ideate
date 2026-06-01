// consoleBridge mirrors browser console output into the Go-side
// Wails logger so DevTools messages survive a session — DevTools
// flushes on dev-server restart / app close, but the Go logger
// writes to stderr (captured by `task dev`'s tee'd log file).
//
// Always on. The Go-side LogLevel does the filtering: production
// drops everything below Error; dev (with IDEATE_TERM_DEBUG=1)
// includes Debug. So a chatty `console.debug` is only persisted when
// the env var is set, without the frontend having to know.
//
// Replaces the prior ad-hoc bridge in main.tsx — same window
// 'error' + 'unhandledrejection' coverage, plus all five console
// levels with safe stringification and a per-second rate cap.

import * as wailsRuntime from '../wailsjs/runtime/runtime'

// Per-second cap on forwarded lines — protects the UI thread from a
// tight console.log loop turning into a flood of synchronous
// WailsInvoke round-trips. When tripped, drops are summarized in a
// single trailing warning.
const MAX_PER_SECOND = 100

interface Bucket {
  windowStart: number
  count: number
  dropped: number
}

const bucket: Bucket = { windowStart: 0, count: 0, dropped: 0 }

function admitOrDrop(now: number): boolean {
  if (now - bucket.windowStart >= 1000) {
    if (bucket.dropped > 0) {
      const dropped = bucket.dropped
      bucket.dropped = 0
      original.warn(`[consoleBridge] dropped ${dropped} line(s) over rate cap`)
    }
    bucket.windowStart = now
    bucket.count = 0
  }
  if (bucket.count >= MAX_PER_SECOND) {
    bucket.dropped++
    return false
  }
  bucket.count++
  return true
}

// Stringify args defensively so DOM nodes / React refs / circular
// references don't crash the bridge. Falls back to a string token
// rather than throwing.
function safeStringify(arg: unknown): string {
  if (typeof arg === 'string') return arg
  if (arg instanceof Error) {
    return `${arg.name}: ${arg.message}${arg.stack ? '\n' + arg.stack : ''}`
  }
  try {
    const seen = new WeakSet<object>()
    return JSON.stringify(arg, (_, v) => {
      if (typeof v === 'object' && v !== null) {
        if (seen.has(v as object)) return '[circular]'
        seen.add(v as object)
      }
      return v
    })
  } catch {
    return String(arg)
  }
}

function format(args: unknown[]): string {
  return args.map(safeStringify).join(' ')
}

// Cache the originals before wrapping so the wrapper can call them
// (DevTools still sees everything) and the rate-cap drop notice can
// use an un-bridged warn.
const original = {
  log: console.log.bind(console),
  debug: console.debug.bind(console),
  info: console.info.bind(console),
  warn: console.warn.bind(console),
  error: console.error.bind(console),
}

type Level = 'log' | 'debug' | 'info' | 'warn' | 'error'

function forward(level: Level, args: unknown[]): void {
  if (!admitOrDrop(Date.now())) return
  const msg = format(args)
  try {
    switch (level) {
      case 'debug': wailsRuntime.LogDebug(msg); break
      case 'info':  wailsRuntime.LogInfo(msg); break
      case 'warn':  wailsRuntime.LogWarning(msg); break
      case 'error': wailsRuntime.LogError(msg); break
      case 'log':   wailsRuntime.LogPrint(msg); break
    }
  } catch {
    // Wails runtime not yet attached (very early boot) — drop silently.
  }
}

let installed = false

export function installConsoleBridge(): void {
  if (installed) return
  installed = true

  console.log = (...args: unknown[]) => { original.log(...args); forward('log', args) }
  console.debug = (...args: unknown[]) => { original.debug(...args); forward('debug', args) }
  console.info = (...args: unknown[]) => { original.info(...args); forward('info', args) }
  console.warn = (...args: unknown[]) => { original.warn(...args); forward('warn', args) }
  console.error = (...args: unknown[]) => { original.error(...args); forward('error', args) }

  window.addEventListener('error', (e) => {
    forward('error', [`[window.onerror] ${e.message}`, e.error ?? '', `${e.filename}:${e.lineno}:${e.colno}`])
  })
  window.addEventListener('unhandledrejection', (e) => {
    forward('error', ['[unhandledrejection]', e.reason ?? ''])
  })
}
