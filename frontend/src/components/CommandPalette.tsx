import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Plus, Coffee, Cpu, Home } from 'lucide-react'
import { ListIdeas, ListSessionSummaries, GetSleepState, SetSleepEnabled } from '../wailsjs/go/app/App'
import { resolveSessionTarget, useNavigateToIdeaSession } from '../lib/sessionNav'
import { model, store } from '../wailsjs/go/models'
import IdeaStatusIcon from './IdeaStatusIcon'
import SessionStatusIcon from './SessionStatusIcon'
import CardShell from './CardShell'
import { useMRU } from '../contexts/MRUContext'
import { useOrchestratorDrawer } from '../hooks/useOrchestratorDrawer'

// CommandPalette is the Cmd+K modal switcher. Two row kinds:
//
//   - Navigation: ideas (each may carry a running session), plus the
//     orchestrator surface as a virtual entry. Sorted by MRU with a
//     hard pin for the orchestrator at the top of the empty-query
//     view.
//   - Commands: actions like "New idea" or "Toggle prevent sleep".
//     Hidden from the empty-query view; surface only when typing
//     matches them.
//
// Hover decoration is intentionally subtle and does NOT change the
// keyboard cursor — selection commits only on click or Enter so a
// mouse hovering near the wrong row doesn't steal Enter from the
// row the keyboard is on.

const MAX_NAV = 20
const MAX_COMMANDS = 4

type NavEntry = {
  kind: 'nav'
  id: string
  group: 'nav'
  label: string
  // Leading-icon node rendered next to the label. For ideas this
  // is an IdeaStatusIcon keyed to lifecycle status; for pinned
  // surfaces it's a lucide glyph (Cpu / Home).
  icon: React.ReactNode
  // Optional one-line summary rendered under the label.
  summary?: string
  // MRU score used for empty-query sort.
  mruScore: string
  // Render the meta row.
  meta?: React.ReactNode
  // Variant class hooks (e.g. activity color for sessions).
  variant?: string
  // Invisible strings the fuzzy matcher also considers — e.g. repo
  // names under an idea — so typing a repo basename surfaces the
  // owning idea without crowding the rendered card.
  searchAliases?: string[]
  activate: () => void
}

type CommandEntry = {
  kind: 'command'
  id: string
  group: 'command'
  label: string
  // Short hint shown to the right of the label.
  hint?: string
  icon: React.ReactNode
  activate: () => void
}

type Entry = NavEntry | CommandEntry

interface Props {
  open: boolean
  onClose: () => void
}

// subseqScore scores a single contiguous token as a subsequence of hay.
// Returns -Infinity when the token can't be found at all so callers can
// gate on "all tokens matched". The streak + early-match bonuses match
// fzf-style heuristics: contiguous runs and matches near the head of
// the haystack are amplified.
function subseqScore(token: string, hay: string): number {
  if (!token) return 0
  let hi = 0
  let ti = 0
  let score = 0
  let streak = 0
  while (ti < token.length && hi < hay.length) {
    if (token[ti] === hay[hi]) {
      streak += 1
      score += 10 + streak * 4 + Math.max(0, 50 - hi)
      ti += 1
    } else {
      streak = 0
    }
    hi += 1
  }
  if (ti < token.length) return -Infinity
  return score
}

// fuzzyScore splits the query on whitespace and requires EVERY token
// to subsequence-match the haystack — but the tokens themselves may
// appear in any order. So "app ideate" and "ideate app" both rank the
// idea "Ideate app" the same way. A query without whitespace behaves
// exactly as the single-subseq matcher did before this change.
//
// Order-independence matters because users type the words they
// remember, not the words in the order the label happens to use.
function fuzzyScore(query: string, hay: string): number {
  if (!query) return 0
  const h = hay.toLowerCase()
  const tokens = query.toLowerCase().split(/\s+/).filter(Boolean)
  if (tokens.length === 0) return 0
  let total = 0
  for (const tok of tokens) {
    const s = subseqScore(tok, h)
    if (s === -Infinity) return -Infinity
    total += s
  }
  return total
}

// Penalty subtracted from alias matches so a label hit wins ties —
// keeps visible-name matches at the top when the same query also
// happens to subseq-match a hidden repo basename.
const ALIAS_PENALTY = 5

// scoreEntry takes the best of label + alias scores, with the alias
// path penalized so label hits win ties.
function scoreEntry(query: string, entry: { label: string; searchAliases?: string[] }): number {
  let best = fuzzyScore(query, entry.label)
  for (const alias of entry.searchAliases ?? []) {
    const s = fuzzyScore(query, alias)
    if (s === -Infinity) continue
    const adjusted = s - ALIAS_PENALTY
    if (adjusted > best) best = adjusted
  }
  return best
}

function ideaMRUScore(idea: model.Idea, runningUUID: string | undefined, mruScore: (uuid: string) => string): string {
  // Prefer the running session's focus stamp, fall back to
  // idea.updated so never-visited ideas still order by activity.
  const focus = runningUUID ? mruScore(runningUUID) : ''
  const updated = idea.updated || idea.created || ''
  return [focus, updated].filter(Boolean).reduce((a, b) => (a > b ? a : b), '')
}

// Special MRU score reserved for permanently-pinned nav entries
// (orchestrator, dashboard). Sorts above any real ISO timestamp so
// they stay at the top of the empty-query view, and breaks ties
// gracefully if anything ever competes.
const PINNED_MRU = '￿' // U+FFFF — sorts after any normal-range char

// focusOrchestratorHelper polls for the orchestrator terminal's
// xterm-helper-textarea and calls .focus() once it appears. The drawer
// + TerminalPanel mount conditionally on the drawer being visible
// (PR #93 mount-on-visible), so the helper element does not exist at
// the moment the command-palette unmount-microtask fires when the
// drawer was closed. A bare setTimeout(0) misses; a rAF poll waits for
// React to commit the drawer-open render then the TerminalPanel mount,
// which together can span 2-3 frames in cold-mount paths. 10 frames
// (~170ms at 60Hz) is the hard cap so a never-mounting host doesn't
// pin a spin loop.
//
// focusGeneration is a module-level token: each new invocation
// increments it; in-flight rAF callbacks bail when their generation no
// longer matches. Without this guard, a poll started for "pick
// Orchestrator" would still fire if the user reopened the palette and
// picked something else — silently stealing focus into the
// orchestrator after the host eventually mounts. cancelOrchestratorFocus
// also increments the generation so the palette's open/close hook can
// invalidate any pending poll on transition.
const FOCUS_RETRY_FRAMES = 10
let focusGeneration = 0

function focusOrchestratorHelper(): void {
  const myGen = ++focusGeneration
  const step = (remaining: number) => {
    if (myGen !== focusGeneration) return
    const term = document.querySelector<HTMLTextAreaElement>(
      '.orchestrator-host .xterm-helper-textarea',
    )
    if (term) { term.focus(); return }
    if (remaining <= 0) return
    requestAnimationFrame(() => step(remaining - 1))
  }
  step(FOCUS_RETRY_FRAMES)
}

function cancelOrchestratorFocus(): void {
  focusGeneration++
}

export default function CommandPalette({ open, onClose }: Props) {
  const navigate = useNavigate()
  const navigateToSession = useNavigateToIdeaSession()
  const [ideas, setIdeas] = useState<model.Idea[]>([])
  const [summaries, setSummaries] = useState<Record<string, store.IdeaSessionSummary>>({})
  const [sleepEnabled, setSleepEnabled] = useState<boolean>(false)
  const [query, setQuery] = useState('')
  // selectedIndex is the keyboard cursor — driven by arrow keys
  // only. Mouse hover leaves it alone (CardShell's :hover provides
  // the subtle background hint without committing selection); the
  // mouse activates a row by click, not by hovering near it.
  const [selectedIndex, setSelectedIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement | null>(null)
  const listRef = useRef<HTMLDivElement | null>(null)
  const restoreFocusRef = useRef<HTMLElement | null>(null)
  const { score: mruScore } = useMRU()
  const drawer = useOrchestratorDrawer()

  useEffect(() => {
    // Any palette transition cancels a pending orchestrator-focus poll
    // from a prior open. Without this, "pick Orchestrator, Esc, reopen,
    // pick Dashboard" could still steal focus into the orchestrator
    // when its host mounts a few frames later.
    cancelOrchestratorFocus()
    if (!open) {
      const prev = restoreFocusRef.current
      restoreFocusRef.current = null
      if (prev && typeof prev.focus === 'function') {
        queueMicrotask(() => prev.focus())
      }
      return
    }
    restoreFocusRef.current = (document.activeElement as HTMLElement | null) ?? null
    setQuery('')
    setSelectedIndex(0)
    void (async () => {
      try {
        const [ideaList, summaryList, sleep] = await Promise.all([
          ListIdeas(),
          ListSessionSummaries(),
          GetSleepState().catch(() => ({ enabled: false, held: false })),
        ])
        setIdeas(ideaList || [])
        const map: Record<string, store.IdeaSessionSummary> = {}
        for (const s of summaryList || []) map[s.slug] = s
        setSummaries(map)
        setSleepEnabled(Boolean(sleep?.enabled))
      } catch {
        setIdeas([])
        setSummaries({})
      }
    })()
    queueMicrotask(() => inputRef.current?.focus())
  }, [open])

  // Build the full Entry catalog. The query filter / sort happens
  // downstream so this list stays stable across keystrokes.
  const catalog = useMemo<{ nav: NavEntry[]; commands: CommandEntry[]; pinned: NavEntry[] }>(() => {
    const orchestrator: NavEntry = {
      kind: 'nav',
      id: '__orchestrator__',
      group: 'nav',
      label: 'Orchestrator',
      icon: <Cpu size={14} strokeWidth={1.75} />,
      summary: 'Workspace-wide session terminal.',
      mruScore: PINNED_MRU,
      meta: <span className="session-card-agent">orchestrator surface</span>,
      activate: () => {
        // Don't restore focus to the pre-palette element — we're
        // explicitly steering focus into the orchestrator terminal.
        restoreFocusRef.current = null
        // Drop the drawer down if it's not already visible. On the
        // dashboard the drawer is pinned regardless, so this is a
        // no-op there; off-home the user gets the drawer opened
        // alongside their current route.
        if (!drawer.open) drawer.setOpen(true)
        onClose()
        // Off-home, OrchestratorHost mounts conditionally on the
        // drawer being visible (PR #93 "mount-on-visible"), so the
        // helper textarea may not exist when the palette's
        // unmount-microtask fires. Poll rAF for the helper until it
        // mounts; cap retries so a never-mounting scenario doesn't
        // spin. ~10 frames at 60Hz is ~170ms — beyond user-perceptible
        // for a focus drop without being a hard freeze on slow paint.
        focusOrchestratorHelper()
      },
    }
    const dashboard: NavEntry = {
      kind: 'nav',
      id: '__dashboard__',
      group: 'nav',
      label: 'Dashboard',
      icon: <Home size={14} strokeWidth={1.75} />,
      summary: 'Browse every idea.',
      mruScore: PINNED_MRU,
      meta: <span className="session-card-agent">overview</span>,
      activate: () => {
        onClose()
        navigate('/')
      },
    }

    const ideaNav: NavEntry[] = ideas
      .filter((i) => i.status !== 'archived')
      .map((idea): NavEntry => {
        const sessions = summaries[idea.slug]
        const target = resolveSessionTarget(sessions)
        const running = target.kind === 'running' ? target.session : undefined
        const dormant = target.kind === 'dormant' ? target.session : undefined
        const summaryLine = sessions?.ideaSummary?.line?.trim() || undefined
        const meta = running ? (
          <>
            <SessionStatusIcon status="running" activity={running.activity || 'idle'} />
            <span className="session-card-agent">{running.agent}</span>
          </>
        ) : dormant ? (
          <>
            <SessionStatusIcon status="dormant" />
            <span className="session-card-agent">{dormant.agent}</span>
          </>
        ) : (
          <span className="session-card-agent">no running session</span>
        )
        const variant = running ? `session ${running.activity || 'idle'}` : dormant ? 'session dormant' : 'session'
        const focusUUID = running?.uuid ?? dormant?.uuid
        return {
          kind: 'nav',
          id: `idea:${idea.slug}`,
          group: 'nav',
          label: idea.name,
          icon: <IdeaStatusIcon status={idea.status} size={14} />,
          summary: summaryLine,
          mruScore: ideaMRUScore(idea, focusUUID, mruScore),
          variant,
          meta,
          searchAliases: sessions?.repoNames,
          activate: () => {
            // Clear the focus-restore target so re-selecting the
            // session the user is currently "on" doesn't bounce them
            // back to the orchestrator terminal. The shared helper
            // takes it from there: running → navigate + focus,
            // dormant → resume + navigate + focus, else → idea.
            restoreFocusRef.current = null
            onClose()
            void navigateToSession(idea.slug, sessions)
          },
        }
      })

    const commands: CommandEntry[] = [
      {
        kind: 'command',
        id: 'cmd:new-idea',
        group: 'command',
        label: 'New idea',
        hint: 'Create',
        icon: <Plus size={14} strokeWidth={1.75} />,
        activate: () => {
          onClose()
          navigate('/idea/new')
        },
      },
      {
        kind: 'command',
        id: 'cmd:toggle-prevent-sleep',
        group: 'command',
        label: sleepEnabled ? 'Disable prevent sleep' : 'Enable prevent sleep',
        hint: 'Toggle',
        icon: <Coffee size={14} strokeWidth={1.75} />,
        activate: () => {
          onClose()
          SetSleepEnabled(!sleepEnabled).catch(() => undefined)
        },
      },
    ]

    return { nav: ideaNav, commands, pinned: [orchestrator, dashboard] }
  }, [ideas, summaries, mruScore, navigate, onClose, sleepEnabled, drawer])

  const rows = useMemo<Entry[]>(() => {
    const q = query.trim()
    if (!q) {
      // Empty query: pinned entries at top (orchestrator, dashboard),
      // then top-N idea nav rows by MRU below them.
      const navByMRU = [...catalog.nav].sort((a, b) => b.mruScore.localeCompare(a.mruScore))
      const remaining = Math.max(0, MAX_NAV - catalog.pinned.length)
      return [...catalog.pinned, ...navByMRU.slice(0, remaining)]
    }
    // Typing: fuzzy filter nav (pinned entries participate) + commands.
    const allNav = [...catalog.pinned, ...catalog.nav]
    const navMatched = allNav
      .map((entry) => ({ entry, score: scoreEntry(q, entry) }))
      .filter((m) => m.score > -Infinity)
      .sort((a, b) => b.score - a.score)
      .slice(0, MAX_NAV)
      .map((m) => m.entry)
    const cmdMatched = catalog.commands
      .map((entry) => ({ entry, score: fuzzyScore(q, entry.label) }))
      .filter((m) => m.score > -Infinity)
      .sort((a, b) => b.score - a.score)
      .slice(0, MAX_COMMANDS)
      .map((m) => m.entry)
    return [...navMatched, ...cmdMatched]
  }, [query, catalog])

  // The index where the command section starts (for inserting a
  // divider header). undefined means no commands rendered.
  const commandSectionStart = useMemo<number | undefined>(() => {
    const i = rows.findIndex((r) => r.group === 'command')
    return i === -1 ? undefined : i
  }, [rows])

  // Clamp selection to current row count.
  useEffect(() => {
    if (selectedIndex >= rows.length) {
      setSelectedIndex(Math.max(0, rows.length - 1))
    }
  }, [rows.length, selectedIndex])

  useEffect(() => {
    if (!open) return
    const el = listRef.current?.querySelector<HTMLElement>(`[data-palette-row="${selectedIndex}"]`)
    el?.scrollIntoView({ block: 'nearest' })
  }, [selectedIndex, open])

  if (!open) return null

  const activate = (entry: Entry | undefined) => {
    if (!entry) return
    entry.activate()
  }

  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      onClose()
    } else if (e.key === 'Enter') {
      e.preventDefault()
      activate(rows[selectedIndex])
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      setSelectedIndex((i) => Math.min(rows.length - 1, i + 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setSelectedIndex((i) => Math.max(0, i - 1))
    }
  }

  return (
    <div
      className="command-palette-backdrop"
      data-testid="command-palette"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
      onKeyDown={onKeyDown}
    >
      <div className="command-palette" role="dialog" aria-label="Command palette">
        <input
          ref={inputRef}
          type="text"
          className="command-palette-input"
          placeholder="Jump to a session, idea, or command…"
          value={query}
          onChange={(e) => {
            setQuery(e.target.value)
            setSelectedIndex(0)
          }}
          aria-label="Search"
          autoComplete="off"
          autoCorrect="off"
          autoCapitalize="off"
          spellCheck={false}
        />
        <div className="command-palette-list" ref={listRef} role="listbox">
          {rows.length === 0 && (
            <div className="command-palette-empty">No matches</div>
          )}
          {rows.map((entry, i) => {
            const sectionHeader = i === commandSectionStart && (
              <div className="command-palette-section" aria-hidden>
                Commands
              </div>
            )
            const selected = i === selectedIndex
            return (
              <div key={entry.id}>
                {sectionHeader}
                <div
                  data-palette-row={i}
                  data-palette-kind={entry.kind}
                  className="command-palette-row"
                >
                  {entry.kind === 'nav' ? (
                    <NavRow entry={entry} selected={selected} onActivate={() => activate(entry)} />
                  ) : (
                    <CommandRow entry={entry} selected={selected} onActivate={() => activate(entry)} />
                  )}
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}

function NavRow({ entry, selected, onActivate }: { entry: NavEntry; selected: boolean; onActivate: () => void }) {
  const cls = ['session-card', entry.variant || '']
  if (selected) cls.push('current')
  const header = (
    <div className="session-card-title">
      {entry.icon}
      <span className="session-card-name">{entry.label}</span>
    </div>
  )
  return (
    <CardShell
      className={cls.filter(Boolean).join(' ')}
      header={header}
      summary={entry.summary}
      meta={entry.meta}
      onActivate={onActivate}
      ariaLabel={entry.label}
    />
  )
}

function CommandRow({ entry, selected, onActivate }: { entry: CommandEntry; selected: boolean; onActivate: () => void }) {
  const cls = ['command-palette-command']
  if (selected) cls.push('selected')
  return (
    <div
      role="button"
      tabIndex={0}
      className={cls.join(' ')}
      onClick={onActivate}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onActivate()
        }
      }}
    >
      <span className="command-palette-command-icon" aria-hidden>{entry.icon}</span>
      <span className="command-palette-command-label">{entry.label}</span>
      {entry.hint && <span className="command-palette-command-hint">{entry.hint}</span>}
    </div>
  )
}

