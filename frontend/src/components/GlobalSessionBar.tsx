import { useEffect, useRef, useState } from 'react'
import { useNavigate, useLocation, matchPath } from 'react-router-dom'
import SessionStatusIcon from './SessionStatusIcon'
import IdeaStatusIcon from './IdeaStatusIcon'
import { useMRU } from '../contexts/MRUContext'
import { EventsOn } from '../wailsjs/runtime/runtime'

interface ActiveSession {
  slug: string
  ideaName: string
  // Idea lifecycle status (active|paused|archived).
  ideaStatus: string
  uuid: string
  agentType: string
  activity: string
  started: string
  // Parent idea's Updated timestamp; bumped on every session-activity hook
  // via TouchIdea, so it doubles as the session's last-activity signal.
  updated: string
  // Parent idea's one-line description (idea.Description) when present.
  ideaSummary?: string
}

function agentLabel(agentType: string): string {
  if (agentType === 'claude-code') return 'Claude'
  if (agentType === 'claude-code-debug') return 'Claude (Debug)'
  if (agentType === 'testagent') return 'Test'
  return agentType
}

// Activities that mean "agent is paused, user input needed" — waiting on a
// permission/Notification hook, or blocked on a review tool result. Drives
// the overflow pill's attention glow.
function isAttentionNeeded(activity: string): boolean {
  return activity === 'waiting' || activity === 'reviewing'
}

// Recency = max of `updated` (parent idea's MRU bump from session-activity
// events), `started` (fallback for sessions that haven't fired any hooks
// yet), and the MRUContext's per-UUID focus timestamp so a session the
// user just navigated away from ranks most-recent even when its agent is
// idle. mruScore is the closure over the MRU context's `score(uuid)`.
function sessionRecency(s: ActiveSession, mruScore: (uuid: string) => string): string {
  const candidates = [mruScore(s.uuid), s.updated || '', s.started || ''].filter(Boolean)
  if (candidates.length === 0) return ''
  return candidates.reduce((a, b) => (a > b ? a : b))
}

// Pure recency sort, newest first. The bar and dashboard share this
// rule so the user sees the same ordering across surfaces (the
// dashboard's IdeaList already sorts by idea.updated, which is the
// same signal).
function sortSessions(sessions: ActiveSession[], mruScore: (uuid: string) => string): ActiveSession[] {
  return [...sessions].sort((a, b) =>
    sessionRecency(b, mruScore).localeCompare(sessionRecency(a, mruScore)),
  )
}

// The tabs zone reserves exactly one slot for the currently-viewed
// session. On non-session routes the slot is empty.
function partition(
  sessions: ActiveSession[],
  currentUUID: string,
  mruScore: (uuid: string) => string,
): { visible: ActiveSession[]; hidden: ActiveSession[] } {
  const sorted = sortSessions(sessions, mruScore)
  const current = currentUUID ? sorted.find((s) => s.uuid === currentUUID) : undefined
  return {
    visible: current ? [current] : [],
    hidden: sorted,
  }
}

interface ChipProps {
  session: ActiveSession
  onActivate: () => void
  isCurrent?: boolean
}

function Chip({ session, onActivate, isCurrent }: ChipProps) {
  const cls = ['global-session-chip', session.activity]
  if (isCurrent) cls.push('current')
  return (
    <div
      role="button"
      tabIndex={0}
      aria-current={isCurrent ? 'page' : undefined}
      className={cls.join(' ')}
      title={`${session.ideaName} — ${agentLabel(session.agentType)} (${session.activity})${isCurrent ? ' — current view' : ''}`}
      onClick={onActivate}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onActivate()
        }
      }}
    >
      <SessionStatusIcon status="running" activity={session.activity} />
      <IdeaStatusIcon status={session.ideaStatus} />
      <span className="global-session-chip-idea">{session.ideaName}</span>
      <span className="global-session-chip-agent">{agentLabel(session.agentType)}</span>
    </div>
  )
}

// GlobalSessionBar renders the footer's tabs zone: the current
// session pinned rightmost, and an overflow button on the left that
// opens the Cmd+K command palette when clicked.
export default function GlobalSessionBar() {
  const navigate = useNavigate()
  const location = useLocation()
  const [sessions, setSessions] = useState<ActiveSession[]>([])
  // MRU context: the Cmd+K palette consumes the same per-UUID
  // "last focused" timestamps so the two surfaces stay in sync.
  // mruScore is read synchronously during render; markFocused fires
  // from the navigation-transition effect below.
  const { markFocused, score: mruScore } = useMRU()

  useEffect(() => {
    let cancelled = false
    const refresh = async () => {
      try {
        // Dynamic import: ListActiveSessions binding may not exist on older
        // dev-server bundles between Wails regen cycles.
        const mod = await import('../wailsjs/go/app/App')
        const fn = (mod as Record<string, unknown>)['ListActiveSessions'] as (() => Promise<ActiveSession[]>) | undefined
        if (!fn) return
        const list = await fn()
        if (cancelled) return
        setSessions(Array.isArray(list) ? list : [])
      } catch {
        if (!cancelled) setSessions([])
      }
    }
    refresh()
    // Watcher pushes `idea:changed` whenever a session record is written
    // (status flips, activity changes). Keep a coarse backstop in case
    // the watcher misses an event on a flaky filesystem.
    const cancelChanged = EventsOn('idea:changed', () => { refresh() })
    const id = setInterval(refresh, 5000)
    return () => {
      cancelled = true
      clearInterval(id)
      cancelChanged()
    }
  }, [])

  // Detect whether the user is currently viewing a session — pin that
  // session's chip leftmost in the visible row. HashRouter's useLocation
  // returns the in-hash path, so location.pathname is /idea/.../session/...
  const ideaMatch = matchPath('/idea/:slug/session/:sessionId', location.pathname)
  const currentUUID = ideaMatch?.params?.sessionId || ''
  const partitionCurrentUUID = currentUUID

  // Bump the just-left session's last-focused stamp on transition. The
  // previously-focused UUID is held in a ref so the effect can compare
  // against the new partitionCurrentUUID without depending on stale
  // state. Bumps on every transition where the value actually changes,
  // including navigating *into* the session (so when a hook also fires
  // a refresh, the current-session timestamp is fresh). Declared above
  // any conditional return so React's hook count is stable across
  // empty-vs-populated session lists.
  const previousFocusedRef = useRef<string>('')
  useEffect(() => {
    if (previousFocusedRef.current && previousFocusedRef.current !== partitionCurrentUUID) {
      markFocused(previousFocusedRef.current)
    }
    if (partitionCurrentUUID) {
      markFocused(partitionCurrentUUID)
    }
    previousFocusedRef.current = partitionCurrentUUID
  }, [partitionCurrentUUID, markFocused])

  if (sessions.length === 0) return null

  const isCurrent = (s: ActiveSession): boolean => s.uuid === currentUUID

  const { visible, hidden } = partition(sessions, partitionCurrentUUID, mruScore)

  // Test affordance: expose the bar's idea-name list in render order
  // (top-to-bottom, newest-first). Tests assert against this so they
  // don't have to open the palette and race its DOM mount.
  if (typeof window !== 'undefined') {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ;(window as any).__ideateBarOrder = hidden.map((s) => s.ideaName)
  }

  // Per-idea collapsed counts of work NOT already represented by the
  // visible chip. Excluding the current chip's idea is what makes the
  // pill mean "there is more to switch to" — without that filter the
  // user viewing their only session would see "1 active" with nothing
  // else to surface.
  //
  // An idea is "active" if at least one of its sessions has
  // activity !== 'dormant'; it's "dormant" only if ALL its sessions
  // are dormant (so an idea with both running + dormant sessions
  // counts once as active, never double-counted).
  const currentSlug = visible[0]?.slug ?? ''
  const slugGroups = new Map<string, ActiveSession[]>()
  for (const s of sessions) {
    if (s.slug === currentSlug) continue
    const group = slugGroups.get(s.slug) ?? []
    group.push(s)
    slugGroups.set(s.slug, group)
  }
  let activeIdeas = 0
  let dormantIdeas = 0
  for (const group of slugGroups.values()) {
    const allDormant = group.every((s) => s.activity === 'dormant')
    if (allDormant) {
      dormantIdeas++
    } else {
      activeIdeas++
    }
  }

  // Glow class when an attention-needed session exists outside the
  // current chip. Drives the pill's colour so the signal isn't lost.
  const extraInOverflow = hidden.filter((s) => s.uuid !== partitionCurrentUUID)
  const extraAttention = extraInOverflow.some((s) => isAttentionNeeded(s.activity))
  const extraDominant = extraAttention
    ? extraInOverflow.find((s) => s.activity === 'waiting')?.activity || 'reviewing'
    : extraInOverflow.some((s) => s.activity === 'active') ? 'active' : 'idle'

  // Build the overflow pill label.
  let overflowLabel: string | null = null
  if (activeIdeas > 0 && dormantIdeas > 0) {
    overflowLabel = `${activeIdeas} active (${dormantIdeas} dormant)`
  } else if (activeIdeas > 0) {
    overflowLabel = `${activeIdeas} active`
  } else if (dormantIdeas > 0) {
    overflowLabel = `${dormantIdeas} dormant`
  }

  const chipPath = (s: ActiveSession): string => `/idea/${s.slug}/session/${s.uuid}`
  const goTo = (s: ActiveSession) => () => navigate(chipPath(s))

  return (
    <div className="global-session-bar">
      {overflowLabel !== null && (
        <div className={`global-session-overflow${extraAttention ? ' attention' : ''}`}>
          <button
            type="button"
            className="global-session-more"
            aria-label="Open command palette"
            onClick={() => window.dispatchEvent(new CustomEvent('ideate:cmdk-open'))}
          >
            <SessionStatusIcon status="running" activity={extraDominant} />
            {overflowLabel}
          </button>
        </div>
      )}
      {visible.map((s) => (
        <Chip key={s.uuid} session={s} onActivate={goTo(s)} isCurrent={isCurrent(s)} />
      ))}
    </div>
  )
}
