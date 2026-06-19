// In-app routing for `ideate://` URLs. Agent-emitted deep links inside
// xterm terminals and markdown views become clickable and navigate the
// running webview via HashRouter — no OS scheme registration (that's
// backlog item 6e88c3eb, separate).
//
// Grammar (canonical in `internal/model/urls.go`):
//
//   ideate://orchestrator                            → /orchestrator
//   ideate://ideas/<slug>                            → /idea/<slug>
//   ideate://ideas/<slug>/sessions/<uuid>            → /idea/<slug>/session/<uuid>
//   ideate://ideas/<slug>/active-session             → synthetic: runtime ResolveActiveSession
//                                                      decides; uuid present → session route,
//                                                      else idea detail.
//
// Active-session resolution is async (Wails binding); xterm link
// callbacks are sync. `routeIdeateURL` returns synchronously after
// dispatching the binding, so callers don't await — the navigation
// happens in the binding's `.then()`.

import type { NavigateFunction } from 'react-router-dom'

import { openExternal } from './links'
import { ResolveActiveSession } from '../wailsjs/go/app/App'

// TranslatedIdeateURL is the tagged-union return shape of
// `translateIdeateURL`. `'route'` carries a ready-to-navigate HashRouter
// path; `'active-session'` carries the slug for downstream resolution;
// `null` means malformed or not an `ideate://` URL.
export type TranslatedIdeateURL =
  | { kind: 'route'; path: string }
  | { kind: 'active-session'; slug: string }
  | null

// translateIdeateURL parses an `ideate://...` URL into its HashRouter
// route, or surfaces the active-session synthetic for the dispatcher to
// resolve. Returns null for malformed input or any non-`ideate:` scheme.
//
// The parser is intentionally strict: empty slugs, empty UUIDs, missing
// path components, and unknown verbs all return null. The dispatcher
// treats null as "ignore the click."
export function translateIdeateURL(url: string): TranslatedIdeateURL {
  if (!url || !url.startsWith('ideate://')) return null

  const rest = url.slice('ideate://'.length)
  if (!rest) return null

  // Strip an optional trailing slash before splitting so
  // `ideate://orchestrator/` matches `ideate://orchestrator`.
  const normalized = rest.endsWith('/') ? rest.slice(0, -1) : rest
  const segments = normalized.split('/')

  if (segments.length === 1 && segments[0] === 'orchestrator') {
    return { kind: 'route', path: '/orchestrator' }
  }

  if (segments[0] !== 'ideas' || segments.length < 2) return null

  const slug = segments[1]
  if (!slug) return null

  if (segments.length === 2) {
    return { kind: 'route', path: `/idea/${slug}` }
  }

  if (segments.length === 3 && segments[2] === 'active-session') {
    return { kind: 'active-session', slug }
  }

  if (segments.length === 4 && segments[2] === 'sessions') {
    const uuid = segments[3]
    if (!uuid) return null
    return { kind: 'route', path: `/idea/${slug}/session/${uuid}` }
  }

  return null
}

// routeIdeateURL dispatches an `ideate://` URL to the HashRouter via
// `navigate`. Sync for non-active-session URLs; for active-session it
// kicks off `App.ResolveActiveSession(slug)` and resolves in `.then()`
// — callers do NOT await. Returns true when the URL was recognized
// (navigation either fired or was dispatched), false for unrecognized
// or malformed input.
//
// Active-session fallback: if `ResolveActiveSession` returns `ok=false`
// (no session, or runner spawn failed on dormant resume), navigate to
// the idea detail page so the user lands somewhere useful.
export function routeIdeateURL(url: string, navigate: NavigateFunction): boolean {
  const translated = translateIdeateURL(url)
  if (translated === null) return false

  if (translated.kind === 'route') {
    navigate(translated.path)
    return true
  }

  // active-session — fire the binding and navigate in .then().
  const { slug } = translated
  ResolveActiveSession(slug).then((res) => {
    if (res.ok && res.uuid) {
      navigate(`/idea/${slug}/session/${res.uuid}`)
      return
    }
    navigate(`/idea/${slug}`)
  }).catch((err) => {
    console.error('ResolveActiveSession failed', { slug, err })
    navigate(`/idea/${slug}`)
  })
  return true
}

// handleLink is the unified click-dispatch entry point. `ideate://`
// URLs route in-app; everything else routes through `openExternal`'s
// scheme allow-list (http/https/mailto). Returns true on a recognized
// dispatch (in-app navigation OR external open succeeded), false when
// the URL was refused by both paths.
//
// Callers (xterm link callbacks, markdown click handlers) invoke this
// instead of `openExternal` so an agent-emitted `ideate://` URL works
// the same in every surface that renders agent output.
export function handleLink(url: string, navigate: NavigateFunction): boolean {
  if (url.startsWith('ideate://')) {
    return routeIdeateURL(url, navigate)
  }
  return openExternal(url)
}
