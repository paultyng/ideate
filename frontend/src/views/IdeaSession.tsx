import { useState, useEffect, useCallback } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { Square, Play, CircleHelp } from 'lucide-react'
import { EventsOn } from '../wailsjs/runtime/runtime'
import TerminalPanel from '../components/TerminalPanel'
import TopbarActions from '../components/TopbarActions'
import { GetRunningIdeaSession, ListIdeaSessions } from '../wailsjs/go/app/App'

interface SessionStatus {
  status: 'running' | 'exited' | 'stopped' | ''
  exitCode?: number
}

interface AgentSession {
  uuid: string
  agent: string
  status: string
  started: string
  ended?: string
  outcome?: string
  working_dir: string
  repo_name?: string
}

async function callBinding(method: string, ...args: unknown[]) {
  const mod = await import('../wailsjs/go/app/App')
  const fn = (mod as Record<string, unknown>)[method] as (...a: unknown[]) => Promise<unknown>
  if (!fn) throw new Error(`Binding ${method} not available`)
  return await fn(...args)
}

export default function IdeaSession() {
  const { slug, sessionId } = useParams<{ slug: string; sessionId: string }>()
  const navigate = useNavigate()

  const [activeSessionId, setActiveSessionId] = useState<string | null>(null)
  const [completedSession, setCompletedSession] = useState<AgentSession | null>(null)
  const [canResume, setCanResume] = useState(false)
  const [sessionStatus, setSessionStatus] = useState<SessionStatus>({ status: '' })
  const [starting, setStarting] = useState(false)
  const [stopping, setStopping] = useState(false)
  const [error, setError] = useState('')
  const [existingRunningUUID, setExistingRunningUUID] = useState('')
  // ActiveReviewID for the running session, if any. Polled alongside
  // session status so the banner reflects state pushed in by the agent
  // (request_*_review) or cleared by submit/cancel.
  const [activeReviewID, setActiveReviewID] = useState('')

  // Form state for new sessions. Sessions always run at the idea root —
  // the user picks the agent type only.
  const [agentType, setAgentType] = useState('claude-code')
  const [agentTypes, setAgentTypes] = useState<string[]>(['claude-code'])

  // Load idea, repos, and agent types.
  useEffect(() => {
    callBinding('ListAgentTypes').then((types) => {
      if (Array.isArray(types) && types.length > 0) {
        setAgentTypes(types as string[])
      }
    }).catch(() => {})
  }, [])

  // Idea metadata is no longer fetched here — AppTopbarTitle owns the
  // idea name display and looks it up by slug from the route params.

  // Determine mode from sessionId param.
  useEffect(() => {
    // Clear both branch-state slots up front so a transition between
    // sessions (e.g. clicking a topbar chip while viewing an ended
    // session) doesn't leave the previous session's state shadowing
    // the new one. Without this, the completed-session metadata view
    // — which renders if `completedSession` is set, regardless of
    // `activeSessionId` — keeps showing the prior ended session even
    // after the new running session's uuid lands in state, because
    // the resolver below only sets the slot for the matching kind.
    setActiveSessionId(null)
    setCompletedSession(null)
    setSessionStatus({ status: '' })
    setError('')

    if (!slug || !sessionId || sessionId === 'new') {
      return
    }

    let cancelled = false
    ;(callBinding('ListIdeaSessions', slug) as Promise<AgentSession[]>).then((ideaSessions) => {
      if (cancelled) return
      const rec = ideaSessions?.find((s) => s.uuid === sessionId)
      if (!rec) {
        setError(`Session ${sessionId} not found`)
        return
      }
      if (rec.status === 'running') {
        setActiveSessionId(sessionId)
        setSessionStatus({ status: 'running' })
        setAgentType(rec.agent)
        return
      }
      setCompletedSession(rec)
    }).catch((e) => { if (!cancelled) setError(String(e)) })
    return () => { cancelled = true }
  }, [slug, sessionId])

  // Check if the agent supports resume.
  useEffect(() => {
    const agent = completedSession?.agent || agentType
    callBinding('AgentSupportsResume', agent)
      .then((result) => setCanResume(result === true))
      .catch(() => setCanResume(false))
  }, [completedSession, agentType])

  // On the new-session form, surface any existing running session for the
  // selected (idea, agent) so the user gets an "Open running" affordance
  // instead of clicking Start and hitting the backend M14 guard. Re-checks
  // when agent type changes.
  useEffect(() => {
    if (!slug || activeSessionId || completedSession || sessionId !== 'new') {
      setExistingRunningUUID('')
      return
    }
    let cancelled = false
    GetRunningIdeaSession(slug, agentType)
      .then((result) => {
        if (cancelled) return
        setExistingRunningUUID(result?.uuid || '')
      })
      .catch(() => {
        if (!cancelled) setExistingRunningUUID('')
      })
    return () => { cancelled = true }
  }, [slug, sessionId, agentType, activeSessionId, completedSession])

  // Listen for session status.
  useEffect(() => {
    if (!activeSessionId) return
    const cancel = EventsOn(`session:${activeSessionId}:status`, (info: SessionStatus) => {
      setSessionStatus(info)
    })
    return () => { cancel() }
  }, [activeSessionId])

  // When the running session terminates (exit / stop / crash), swap
  // to the completed-session metadata view by fetching the now-final
  // record. The metadata view exposes resume + "start new session"
  // affordances so the user has a path forward without leaving the
  // route.
  useEffect(() => {
    if (!slug || !activeSessionId) return
    if (sessionStatus.status !== 'exited' && sessionStatus.status !== 'stopped') return
    let cancelled = false
    ;(callBinding('ListIdeaSessions', slug) as Promise<AgentSession[]>).then((ideaSessions) => {
      if (cancelled) return
      const rec = ideaSessions?.find((s) => s.uuid === activeSessionId)
      if (rec) {
        setCompletedSession(rec)
        setActiveSessionId(null)
      }
    }).catch(() => undefined)
    return () => { cancelled = true }
  }, [slug, activeSessionId, sessionStatus.status])

  // Poll the running session record for ActiveReviewID. Drives the
  // "review pending" banner. Falls silent on completed/stopped sessions.
  useEffect(() => {
    if (!slug || !activeSessionId) return
    let cancelled = false
    const refresh = () => {
      ListIdeaSessions(slug).then((list) => {
        if (cancelled) return
        const running = (list || []).find((s) => s.status === 'running')
        setActiveReviewID(running?.active_review_id || '')
      }).catch(() => undefined)
    }
    refresh()
    const id = setInterval(refresh, 2000)
    return () => { cancelled = true; clearInterval(id) }
  }, [slug, activeSessionId])

  // Pass agent explicitly — React state updates are batched and may not be
  // visible within the same event handler that called setState.
  const startSession = useCallback(async (resume: boolean, agent?: string) => {
    if (!slug) return
    const useAgent = agent ?? agentType
    setStarting(true)
    setError('')
    try {
      const result = await callBinding('StartIdeaSession', slug, useAgent, resume) as { uuid: string } | null
      if (result) {
        setAgentType(useAgent)
        setActiveSessionId(result.uuid)
        setCompletedSession(null)
        setSessionStatus({ status: 'running' })
        navigate(`/idea/${slug}/session/${result.uuid}`, { replace: true })
      }
    } catch (err) {
      const msg = String(err)
      // Backend M14 guard — surface the running UUID so the user can open it.
      const uuidMatch = msg.match(/uuid=([0-9a-f-]+)/i)
      if (uuidMatch && /already running|stuck in running/i.test(msg)) {
        setExistingRunningUUID(uuidMatch[1])
      }
      setError(msg)
    } finally {
      setStarting(false)
    }
  }, [slug, agentType, navigate])

  const handleStart = useCallback(() => startSession(false), [startSession])

  const handleStop = async () => {
    if (!activeSessionId) return
    setStopping(true)
    try {
      await callBinding('StopSession', activeSessionId)
    } catch (err) {
      setError(String(err))
    } finally {
      setStopping(false)
    }
  }

  const isEnded = sessionStatus.status === 'exited' || sessionStatus.status === 'stopped'

  // Build topbar actions inline as a component so each view branch
  // below can drop it in. The portal updates automatically every time
  // this view re-renders, so status / stopping / canResume stay in
  // sync with no register/unregister state machine.
  const topbarActions = (
    <TopbarActions>
      <button
        type="button"
        className="btn-back btn-nav-idea"
        title="Back to idea"
        aria-label="Back to idea"
        onClick={() => navigate(`/idea/${slug}`)}
      >
        {/* Inline italic 'i' matching the app icon mark: enlarged amber
            tittle (evokes a lightbulb sphere), no top serif, slight
            italic lean, left-biased bottom serif. The app icon's
            "threading" serifs between tittle and stem don't render
            legibly at 14px so they're dropped here — the bulb + italic
            stem still convey the same intent. */}
        <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true">
          <circle cx="8" cy="4" r="2" fill="#c8883a" />
          <path d="M6.5 7 L8 7 L9 13 L7.5 13 Z" fill="currentColor" />
          <path d="M5.5 12.5 L9.5 12.5 L9.5 13.5 L5.5 13.5 Z" fill="currentColor" />
        </svg>
      </button>
      {completedSession && (
        <span className={`status-badge ${completedSession.status}`}>{completedSession.status}</span>
      )}
      {!completedSession && activeSessionId && (
        <>
          {/* Drop the explicit "running" label — the Stop button is
              visible alongside it, which is enough to convey state.
              Keep the label for connecting / exited / stopped where
              the visual cue isn't otherwise present. */}
          {sessionStatus.status !== 'running' && (
            <span className={`session-toolbar-status ${sessionStatus.status}`}>
              {sessionStatus.status || 'connecting'}
              {sessionStatus.status === 'exited' && sessionStatus.exitCode !== undefined
                ? ` (code ${sessionStatus.exitCode})`
                : ''}
            </span>
          )}
          {!isEnded && (
            <button
              type="button"
              className="btn-stop"
              title={stopping ? 'Stopping…' : 'Stop session'}
              aria-label="Stop session"
              onClick={handleStop}
              disabled={stopping}
            >
              <Square size={14} strokeWidth={2} fill="currentColor" />
            </button>
          )}
          {isEnded && canResume && (
            <button
              type="button"
              title={starting ? 'Resuming…' : 'Resume session'}
              aria-label="Resume session"
              disabled={starting}
              onClick={() => startSession(true, agentType)}
            >
              <Play size={14} strokeWidth={1.75} />
            </button>
          )}
        </>
      )}
    </TopbarActions>
  )

  // --- View: Completed session metadata ---
  if (completedSession) {
    return (
      <div className="idea-detail">
        {topbarActions}
        <div className="session-metadata">
          {canResume && (
            <button
              className="btn-secondary icon-btn"
              title={starting ? 'Resuming…' : 'Resume session'}
              aria-label="Resume session"
              disabled={starting}
              onClick={() => startSession(true, completedSession.agent)}
            >
              <Play size={14} strokeWidth={1.75} />
            </button>
          )}
          <button
            className="btn-primary"
            title="Start a new session"
            aria-label="Start new session"
            disabled={starting}
            onClick={() => navigate(`/idea/${slug}/session/new`)}
          >
            Start new session
          </button>

          <dl>
            <dt>Agent</dt>
            <dd>{completedSession.agent}</dd>

            {completedSession.outcome && <>
              <dt>Outcome</dt>
              <dd>{completedSession.outcome}</dd>
            </>}

            <dt>Started</dt>
            <dd>{new Date(completedSession.started).toLocaleString()}</dd>

            {completedSession.ended && <>
              <dt>Ended</dt>
              <dd>{new Date(completedSession.ended).toLocaleString()}</dd>
            </>}

            <dt>Working Directory</dt>
            <dd className="session-metadata-path">{completedSession.working_dir}</dd>
          </dl>
        </div>

        {error && <p className="session-error">{error}</p>}
      </div>
    )
  }

  // --- View: New session form ---
  if (!activeSessionId) {
    return (
      <div className="session-start">
        {topbarActions}
        <label>
          Agent Type
          <select value={agentType} onChange={(e) => setAgentType(e.target.value)}>
            {agentTypes.map((t) => (
              <option key={t} value={t}>{t}</option>
            ))}
          </select>
        </label>

        {existingRunningUUID && (
          <div className="session-already-running" role="status">
            <p>Session already running for this idea.</p>
            <button
              type="button"
              aria-label="Open running session"
              onClick={() => navigate(`/idea/${slug}/session/${existingRunningUUID}`)}
            >
              Open running session
            </button>
          </div>
        )}

        <button onClick={handleStart} disabled={starting || !!existingRunningUUID}>
          {starting ? 'Starting...' : 'Start Session'}
        </button>

        {error && <p className="session-error">{error}</p>}
      </div>
    )
  }

  // --- View: Active session with terminal ---
  return (
    <div className="session-container">
      {topbarActions}
      {activeReviewID && (
        <div className="session-review-banner" role="status">
          <span className="session-review-banner-icon" aria-hidden>
            <CircleHelp size={16} strokeWidth={2} />
          </span>
          <span className="session-review-banner-text">
            This session has a pending review.
          </span>
          <button
            type="button"
            className="btn-secondary"
            onClick={() => navigate(`/review?reviewId=${activeReviewID}&fromSession=${slug}:${sessionId}`)}
          >
            Open review
          </button>
        </div>
      )}

      <TerminalPanel sessionId={activeSessionId} />

      {error && <p className="session-error" style={{ padding: '8px 16px' }}>{error}</p>}
    </div>
  )
}
