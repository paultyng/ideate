// Session-navigation helper shared by:
//
//   - IdeaCard (home dashboard)            — click an idea, end up where work is.
//   - IdeaDetail topbar Terminal button   — same.
//   - CommandPalette (Cmd+K quick switch) — already had this behavior; now sourced from here.
//
// The behavior: prefer a running session if one exists; otherwise auto-resume
// the most-recent dormant session and navigate into it; otherwise fall back
// to the idea-detail page. Errors during resume fall back to idea-detail too.
//
// "Dormant" is the status of an idea session whose claude process exited via
// idle-timeout / RSS-trigger but whose record persists — these are intentionally
// resumable. Terminated statuses (completed/stopped/failed) are not.
//
// The resume call uses Wails' StartIdeaSession with `resume=true`, which the
// existing IdeaSession view already uses for its Resume button. That binding
// flips the persisted record to status=running before returning, so by the time
// we navigate the IdeaSession view's mount sees the live terminal branch
// instead of the dormant-metadata branch.

import { useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { StartIdeaSession } from '../wailsjs/go/app/App'
import type { model, store } from '../wailsjs/go/models'

export type SessionTarget =
  | { kind: 'running'; session: model.AgentSession }
  | { kind: 'dormant'; session: model.AgentSession }
  | { kind: 'none' }

// Pick the newest entry from a non-empty AgentSession[] by `started`.
// Mirrors the reduce used in CommandPalette/IdeaCard before the extraction.
function newestByStarted(list: model.AgentSession[]): model.AgentSession {
  return list.reduce((a, b) => ((a.started || '') > (b.started || '') ? a : b))
}

// Pure: resolve a navigation target for a given idea's session summary.
// Returns 'running' when a running session exists, 'dormant' when no running
// but at least one dormant, 'none' otherwise. Multiple in either bucket: newest
// by `started` wins, matching the existing CommandPalette + IdeaCard logic.
export function resolveSessionTarget(
  sessions: store.IdeaSessionSummary | undefined,
): SessionTarget {
  const runningList = sessions?.running ?? []
  if (runningList.length > 0) {
    return { kind: 'running', session: newestByStarted(runningList) }
  }
  const dormantList = sessions?.dormant ?? []
  if (dormantList.length > 0) {
    return { kind: 'dormant', session: newestByStarted(dormantList) }
  }
  return { kind: 'none' }
}

// After navigating to a session terminal, steer keyboard focus into the
// xterm.js instance once it mounts. The TerminalPanel registers each xterm
// on the global `window.__ideateTerminals` map keyed by session UUID
// (see TerminalPanel.tsx:64-66). setTimeout(0) defers past the navigate's
// re-render so the ref is populated by the time we look it up.
function focusTerminal(uuid: string): void {
  setTimeout(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const reg = (window as any).__ideateTerminals as
      | Record<string, { focus?: () => void }>
      | undefined
    reg?.[uuid]?.focus?.()
  }, 0)
}

// Returns an async navigation callback. Callers pass an idea slug + the
// session summary and the hook does the rest. Designed to be called from
// onClick handlers — the returned function is stable across renders so
// it's safe to use directly (no need to wrap in useCallback at the call site).
//
// Error handling: any failure during resume falls back to idea-detail. We
// intentionally don't surface the error inline — the existing IdeaSession
// resume path will retry / show its own UI if the user navigates there
// manually.
export function useNavigateToIdeaSession(): (
  slug: string,
  sessions: store.IdeaSessionSummary | undefined,
) => Promise<void> {
  const navigate = useNavigate()
  return useCallback(
    async (slug: string, sessions: store.IdeaSessionSummary | undefined) => {
      const target = resolveSessionTarget(sessions)
      if (target.kind === 'running') {
        navigate(`/idea/${slug}/session/${target.session.uuid}`)
        focusTerminal(target.session.uuid)
        return
      }
      if (target.kind === 'dormant') {
        try {
          await StartIdeaSession(slug, target.session.agent, true)
          navigate(`/idea/${slug}/session/${target.session.uuid}`)
          focusTerminal(target.session.uuid)
        } catch {
          navigate(`/idea/${slug}`)
        }
        return
      }
      navigate(`/idea/${slug}`)
    },
    [navigate],
  )
}
