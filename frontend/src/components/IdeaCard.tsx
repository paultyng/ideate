import { useNavigateToIdeaSession } from '../lib/sessionNav'
import { model, store } from '../wailsjs/go/models'
import CardShell from './CardShell'
import SessionStatusIcon from './SessionStatusIcon'
import IdeaStatusIcon from './IdeaStatusIcon'

function parseDate(slug: string, updated?: any, created?: any): string {
  if (updated) {
    try {
      return new Date(updated).toLocaleDateString()
    } catch { /* fall through */ }
  }
  if (created) {
    try {
      return new Date(created).toLocaleDateString()
    } catch { /* fall through */ }
  }
  // Fallback: parse YYYY-MM-DD from slug prefix
  const prefix = slug.substring(0, 10)
  if (/^\d{4}-\d{2}-\d{2}$/.test(prefix)) {
    return prefix
  }
  return ''
}

function agentLabel(agentType: string): string {
  if (agentType === 'claude-code') return 'Claude'
  if (agentType === 'claude-code-debug') return 'Claude (Debug)'
  if (agentType === 'testagent') return 'Test agent'
  return agentType
}

// truncateSummary collapses every whitespace run (newlines, tabs,
// multiple spaces) into single spaces, trims, and truncates with an
// ellipsis. Used as the fallback when no headless-generated summary
// exists yet for the idea — the dashboard renders the raw idea.md
// body in that case, but flattened so the card stays one line tall.
const SUMMARY_MAX = 140
function truncateSummary(raw: string | undefined): string {
  if (!raw) return ''
  const flat = raw.replace(/\s+/g, ' ').trim()
  if (flat.length <= SUMMARY_MAX) return flat
  return flat.slice(0, SUMMARY_MAX).trimEnd() + '…'
}

// pickSummaryLine prefers the headless-generated sidecar line over
// the idea.md body. The sidecar is authored by the summarizer
// pipeline against the latest session's transcript tail, so it
// reflects "what the work has actually become" rather than what the
// user originally wrote. Falls back to the truncated body until the
// summarizer has run.
function pickSummaryLine(summary: store.IdeaSessionSummary | undefined, body: string | undefined): string {
  const line = summary?.ideaSummary?.line?.trim()
  if (line) return line
  return truncateSummary(body)
}

interface Props {
  idea: model.Idea
  sessions?: store.IdeaSessionSummary
}

export default function IdeaCard({ idea, sessions }: Props) {
  const date = parseDate(idea.slug, idea.updated, idea.created)
  const resourceCount = idea.resources?.length ?? 0

  const running = sessions?.running ?? []
  const mostRecent = sessions?.mostRecent
  // Single-target rule: prefer running session → auto-resume dormant
  // → otherwise open idea detail. Quick-switcher uses the same shape
  // via lib/sessionNav so all three entry points stay aligned.
  const runningSession = running.length > 0
    ? running.reduce((a, b) => ((a.started || '') > (b.started || '') ? a : b))
    : undefined
  const dormantList = sessions?.dormant ?? []
  const dormantSession = !runningSession && dormantList.length > 0
    ? dormantList.reduce((a, b) => ((a.started || '') > (b.started || '') ? a : b))
    : undefined
  const navigateToSession = useNavigateToIdeaSession()

  // Worst-case activity drives the card-level dot — waiting > active > idle.
  const dominantActivity = (() => {
    if (running.length === 0) return null
    const activities = running.map((s) => s.activity || 'idle')
    if (activities.includes('waiting')) return 'waiting'
    if (activities.includes('active')) return 'active'
    return 'idle'
  })()

  const summary = pickSummaryLine(sessions, idea.summary)

  const header = (
    <div className="idea-card-title">
      <IdeaStatusIcon status={idea.status} size={14} />
      <span className="idea-card-name">{idea.name}</span>
    </div>
  )

  const body = (sessions?.repoNames?.length ?? 0) > 0 ? (
    <div
      className="idea-card-repos"
      title={(sessions?.repoNames ?? []).join(', ')}
    >
      {(sessions?.repoNames ?? []).join(', ')}
    </div>
  ) : null

  const meta = (
    <>
      {date && <span className="idea-card-date">{date}</span>}
      {resourceCount > 0 && (
        <span className="idea-card-resources">{resourceCount} resource{resourceCount !== 1 ? 's' : ''}</span>
      )}
      {running.length > 0 && (
        <span
          className="idea-card-session-badge running"
          title={`${running.length} running session${running.length !== 1 ? 's' : ''} — ${agentLabel(running[0].agent)}`}
        >
          <SessionStatusIcon status="running" activity={dominantActivity || 'idle'} />
          <span className="idea-card-session-label">
            {running.length === 1 ? agentLabel(running[0].agent) : `${running.length} sessions`}
          </span>
        </span>
      )}
      {running.length === 0 && mostRecent && (
        <span
          className="idea-card-session-badge completed"
          title={`Most recent: ${agentLabel(mostRecent.agent)} — ${mostRecent.status}`}
        >
          <SessionStatusIcon status={mostRecent.status} />
          <span className="idea-card-session-label">{agentLabel(mostRecent.agent)}</span>
        </span>
      )}
    </>
  )

  // Left-rail class encodes status + session activity for CSS differentiation.
  // Dormant gets its own rail color so the resumable affordance reads as
  // distinct from a fully-terminated idea on the dashboard.
  const railClass = runningSession
    ? 'idea-card idea-card--running'
    : dormantSession
      ? 'idea-card idea-card--dormant'
      : `idea-card idea-card--${idea.status}`

  return (
    <CardShell
      className={railClass}
      header={header}
      summary={summary || undefined}
      body={body}
      meta={meta}
      onActivate={() => { void navigateToSession(idea.slug, sessions) }}
      ariaLabel={idea.name}
    />
  )
}
