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
import { ResumeIdeaSession } from '../wailsjs/go/app/App'
import type { model } from '../wailsjs/go/models'

// Minimal sessions shape: running + dormant for resume targets, plus the
// most-recent terminated entry so the resolver can decide whether an
// explicit termination should win over an older dormant. IdeaDetail
// constructs one inline from its AgentSession[].
export type ResumableSessions = {
  running?: model.AgentSession[]
  dormant?: model.AgentSession[]
  // mostRecent is the newest non-running session (any status). Set by the
  // store-side summary builder; surfaces user-terminated sessions
  // (completed/stopped) that wouldn't appear in `dormant` so the resolver
  // can prefer them over older dormants per the recency rule below.
  mostRecent?: model.AgentSession
}

export type SessionTarget =
  | { kind: 'running'; session: model.AgentSession }
  | { kind: 'dormant'; session: model.AgentSession }
  // 'terminated' means a user-ended session (completed/stopped) is the
  // newest non-running entry. Navigate to its session-detail UI; do NOT
  // auto-resume — the user already said "stop." From session-detail they
  // can explicitly resume via the Resume button.
  | { kind: 'terminated'; session: model.AgentSession }
  | { kind: 'none' }

// Pick the newest entry from a non-empty AgentSession[] by `started`.
// Mirrors the reduce used in CommandPalette/IdeaCard before the extraction.
function newestByStarted(list: model.AgentSession[]): model.AgentSession {
  return list.reduce((a, b) => ((a.started || '') > (b.started || '') ? a : b))
}

// Pure: resolve a navigation target for a given idea's session summary.
//
// Order of preference:
//   1. running → open the live terminal
//   2. user-terminated newer than every dormant → session-detail (NO auto-resume)
//   3. dormant → auto-resume the newest dormant
//   4. terminated alone (no dormant) → session-detail
//   5. none → idea-detail
//
// The recency check on step 2 fixes the bug where an older orphaned
// dormant would shadow a freshly /exit'd session and auto-resume the
// wrong thing.
export function resolveSessionTarget(
  sessions: ResumableSessions | undefined,
): SessionTarget {
  const runningList = sessions?.running ?? []
  if (runningList.length > 0) {
    return { kind: 'running', session: newestByStarted(runningList) }
  }
  const dormantList = sessions?.dormant ?? []
  const newestDormant = dormantList.length > 0 ? newestByStarted(dormantList) : undefined
  const mostRecent = sessions?.mostRecent
  // mostRecent is non-running; if it isn't dormant, it was user-terminated
  // (completed/stopped) and outranks any dormant strictly older than it.
  if (
    mostRecent &&
    mostRecent.status !== 'dormant' &&
    mostRecent.status !== 'running' &&
    (!newestDormant || (mostRecent.started || '') > (newestDormant.started || ''))
  ) {
    return { kind: 'terminated', session: mostRecent }
  }
  if (newestDormant) {
    return { kind: 'dormant', session: newestDormant }
  }
  if (mostRecent) {
    return { kind: 'terminated', session: mostRecent }
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
  sessions: ResumableSessions | undefined,
) => Promise<void> {
  const navigate = useNavigate()
  return useCallback(
    async (slug: string, sessions: ResumableSessions | undefined) => {
      const target = resolveSessionTarget(sessions)
      if (target.kind === 'running') {
        navigate(`/idea/${slug}/session/${target.session.uuid}`)
        focusTerminal(target.session.uuid)
        return
      }
      if (target.kind === 'dormant') {
        try {
          // Explicit-UUID resume: ResumeIdeaSession respects the caller's
          // chosen UUID. StartIdeaSession(resume=true) would re-pick on the
          // backend side and could resume a different session if a newer
          // user-terminated one exists.
          await ResumeIdeaSession(slug, target.session.uuid)
          navigate(`/idea/${slug}/session/${target.session.uuid}`)
          focusTerminal(target.session.uuid)
        } catch {
          navigate(`/idea/${slug}`)
        }
        return
      }
      if (target.kind === 'terminated') {
        // User-terminated session is the newest. Show its session-detail
        // UI (resume / new-session buttons). Do NOT auto-resume — the
        // user said "stop."
        navigate(`/idea/${slug}/session/${target.session.uuid}`)
        return
      }
      navigate(`/idea/${slug}`)
    },
    [navigate],
  )
}
