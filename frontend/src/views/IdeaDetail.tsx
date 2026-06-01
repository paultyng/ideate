import { useState, useEffect, useCallback } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import {
  GetIdea,
  ReadIdeaFile,
  ListRepos,
  ListRepoFiles,
  ListIdeaSessions,
  ListPendingReviews,
} from '../wailsjs/go/app/App'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { app, store, model } from '../wailsjs/go/models'
import HistoryPanel from '../components/HistoryPanel'
import MarkdownViewer from '../components/MarkdownViewer'
import TopbarActions from '../components/TopbarActions'
import { openExternal } from '../lib/links'
import { Terminal, Plus, ArrowUp, ArrowDown, Circle, Play, ChevronRight, ChevronDown } from 'lucide-react'
import SessionStatusIcon from '../components/SessionStatusIcon'

function parseCreatedDate(slug: string): string {
  const match = slug.match(/^(\d{4}-\d{2}-\d{2})/)
  return match ? match[1] : ''
}

function repoFilePath(repoName: string, file: string): string {
  return `repos/${repoName}/${file}`
}

function agentLabel(agentType: string): string {
  if (agentType === 'claude-code') return 'Claude'
  if (agentType === 'claude-code-debug') return 'Claude (Debug)'
  if (agentType === 'testagent') return 'Test agent'
  return agentType
}

function sessionLabel(s: model.AgentSession): string {
  if (!s.started) return s.uuid.slice(0, 8)
  const d = new Date(s.started)
  const date = d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
  return `${date} ${time}`
}

export default function IdeaDetail() {
  const { slug } = useParams<{ slug: string }>()
  const navigate = useNavigate()
  const [idea, setIdea] = useState<app.IdeaDetail | null>(null)
  const [repos, setRepos] = useState<store.RepoLink[]>([])
  const [repoFiles, setRepoFiles] = useState<Record<string, string[]>>({})
  const [sessions, setSessions] = useState<model.AgentSession[]>([])
  const [selectedFile, setSelectedFile] = useState<string | null>(null)
  const [fileContent, setFileContent] = useState<string>('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  // Per-idea sessions-section collapse override. Default behavior:
  // collapsed when there are sessions, expanded when none. localStorage
  // value (when present) overrides that default.
  const [sessionsCollapsedOverride, setSessionsCollapsedOverride] = useState<boolean | null>(null)
  // Basenames of idea-root .md files with a pending markdown review.
  // Drives the dot indicator on each sidebar file row so reviewers
  // can spot which files have outstanding feedback at a glance.
  const [pendingReviewFiles, setPendingReviewFiles] = useState<Set<string>>(new Set())


  // Load any persisted sessions-collapse override when the slug changes.
  useEffect(() => {
    if (!slug) return
    const stored = localStorage.getItem(`ideate:idea:${slug}:sessionsCollapsed`)
    setSessionsCollapsedOverride(stored === null ? null : stored === '1')
  }, [slug])

  const toggleSessionsCollapsed = useCallback(() => {
    if (!slug) return
    const next = !(sessionsCollapsedOverride ?? sessions.length > 0)
    setSessionsCollapsedOverride(next)
    localStorage.setItem(`ideate:idea:${slug}:sessionsCollapsed`, next ? '1' : '0')
  }, [slug, sessionsCollapsedOverride, sessions.length])

  const refreshSessions = useCallback(() => {
    if (!slug) return
    ListIdeaSessions(slug)
      .then((list) => setSessions(list || []))
      .catch(() => undefined)
  }, [slug])

  // refreshPendingReviews narrows the global pending-reviews list to
  // markdown reviews bound to this idea, then keys them by basename so
  // the sidebar's bare-filename rendering can match. Diff reviews and
  // reviews bound to other ideas are ignored.
  const refreshPendingReviews = useCallback(() => {
    if (!slug) return
    ListPendingReviews()
      .then((list) => {
        const next = new Set<string>()
        for (const r of list || []) {
          if (r.kind !== 'markdown' || r.ideaSlug !== slug || !r.path) continue
          const base = r.path.split('/').pop() || ''
          if (base) next.add(base)
        }
        setPendingReviewFiles(next)
      })
      .catch(() => undefined)
  }, [slug])

  useEffect(() => {
    if (!slug) return
    setLoading(true)
    setError(null)

    let cancelled = false
    const load = (initial: boolean) => {
      Promise.all([
        GetIdea(slug),
        ListRepos(slug).catch(() => [] as store.RepoLink[]),
      ])
        .then(([detail, repoList]) => {
          if (cancelled) return
          setIdea(detail)
          setRepos(repoList || [])
          if (initial) {
            setSelectedFile('idea.md')
          }
        })
        .catch((e) => {
          if (!cancelled) setError(String(e))
        })
        .finally(() => {
          if (!cancelled && initial) setLoading(false)
        })
    }
    load(true)
    refreshSessions()
    refreshPendingReviews()

    const cancelChanged = EventsOn('idea:changed', (payload: { slug: string }) => {
      if (payload?.slug === slug) {
        load(false)
        refreshSessions()
      }
    })
    const cancelRepo = EventsOn('repo:changed', (payload: { slug: string }) => {
      if (payload?.slug === slug) {
        ListRepos(slug)
          .then((list) => setRepos(list || []))
          .catch(() => undefined)
      }
    })
    // Recompute the pending-review file set on review status writes
    // so the dot appears/disappears immediately on submit/cancel.
    const cancelReviewCreated = EventsOn('review:created', refreshPendingReviews)
    const cancelReviewChanged = EventsOn('review:changed', refreshPendingReviews)

    // Session writes now fire idea:changed via the fsnotify watcher (see
    // internal/app/watcher.go), which the listener above already routes
    // through refreshSessions. Keep a coarse backstop poll in case the
    // watcher is slow or misses an event.
    const sessionTimer = setInterval(refreshSessions, 5000)

    return () => {
      cancelled = true
      cancelChanged()
      cancelRepo()
      cancelReviewCreated()
      cancelReviewChanged()
      clearInterval(sessionTimer)
    }
  }, [slug, refreshSessions, refreshPendingReviews])

  useEffect(() => {
    if (!slug || repos.length === 0) {
      setRepoFiles({})
      return
    }
    let cancelled = false
    Promise.all(
      repos.map((r) =>
        ListRepoFiles(slug, r.name)
          .then((files) => [r.name, files || []] as const)
          .catch(() => [r.name, [] as string[]] as const),
      ),
    ).then((entries) => {
      if (cancelled) return
      const map: Record<string, string[]> = {}
      for (const [name, files] of entries) {
        map[name] = files
      }
      setRepoFiles(map)
    })
    return () => {
      cancelled = true
    }
  }, [slug, repos])

  useEffect(() => {
    if (!slug || !selectedFile) {
      setFileContent('')
      return
    }
    if (selectedFile === 'idea.md' && idea) {
      setFileContent(idea.summary || '')
      return
    }
    ReadIdeaFile(slug, selectedFile)
      .then(setFileContent)
      .catch(() => setFileContent('(failed to load file)'))
  }, [slug, selectedFile, idea])

  if (loading) return <div className="idea-detail-loading">Loading...</div>
  if (error) return <div className="idea-detail-error">{error}</div>
  if (!idea || !slug) return <div className="idea-detail-error">Idea not found</div>

  const additionalFiles = (idea.files || []).sort((a: string, b: string) => a.localeCompare(b))
  const ideaFiles = ['idea.md', ...additionalFiles]

  // M14: sessions live in their own top sidebar section, not nested under
  // files or repos. Sorted by Started desc — running sessions naturally lead
  // because the single-session lock keeps at most one running per agent.
  const sortedSessions = [...sessions].sort((a, b) =>
    (b.started || '').localeCompare(a.started || ''),
  )
  const runningSession = sortedSessions.find((s) => s.status === 'running')
  // Forward-nav target: prefer the running session so the terminal
  // button always lands on something live when one exists. Falls back
  // to the most-recent terminal record so the button still surfaces
  // history for ideas with no running session. Hides entirely when
  // there are no sessions at all.
  const sessionNavTarget = runningSession || sortedSessions[0]
  const sessionsCollapsed = sessionsCollapsedOverride ?? sortedSessions.length > 0

  return (
    <div className="idea-detail">
      <TopbarActions>
        {sessionNavTarget && (
          <button
            type="button"
            className="btn-back btn-nav-session"
            title={runningSession ? 'Open running session' : 'Open most recent session'}
            aria-label={runningSession ? 'Open running session' : 'Open most recent session'}
            onClick={() => navigate(`/idea/${slug}/session/${sessionNavTarget.uuid}`)}
          >
            <Terminal size={14} strokeWidth={1.75} />
          </button>
        )}
      </TopbarActions>

      <div className="idea-detail-body">
        <div className="idea-sidebar">
          <div className={`idea-sidebar-section sessions-section ${sessionsCollapsed ? 'collapsed' : 'expanded'}`}>
            <div
              className="idea-sidebar-section-title"
              role="button"
              tabIndex={0}
              aria-expanded={!sessionsCollapsed}
              aria-controls={`sessions-body-${slug}`}
              onClick={toggleSessionsCollapsed}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  toggleSessionsCollapsed()
                }
              }}
            >
              <span className="sessions-section-chevron" aria-hidden>
                {sessionsCollapsed ? <ChevronRight size={12} strokeWidth={2} /> : <ChevronDown size={12} strokeWidth={2} />}
              </span>
              Sessions{sortedSessions.length > 0 ? ` (${sortedSessions.length})` : ''}
              <span className="idea-sidebar-section-actions">
                {runningSession && (
                  <button
                    type="button"
                    className="btn-small icon-btn"
                    title="Open running session"
                    aria-label="Open running session"
                    onClick={(e) => {
                      e.stopPropagation()
                      navigate(`/idea/${slug}/session/${runningSession.uuid}`)
                    }}
                  >
                    <Play size={14} strokeWidth={2} />
                  </button>
                )}
                <button
                  type="button"
                  className="btn-small icon-btn"
                  title={runningSession ? 'Cannot start: a session is already running' : 'Start a new session'}
                  aria-label={runningSession ? 'Cannot start: a session is already running' : 'Start a new session'}
                  disabled={!!runningSession}
                  onClick={(e) => {
                    e.stopPropagation()
                    navigate(`/idea/${slug}/session/new`)
                  }}
                >
                  <Plus size={14} strokeWidth={2} />
                </button>
              </span>
            </div>
            <div className="sessions-section-body" id={`sessions-body-${slug}`} hidden={sessionsCollapsed}>
            {sortedSessions.length === 0 ? (
              <div className="idea-sidebar-empty">No sessions yet</div>
            ) : (
              sortedSessions.map((s) => {
                const titleParts = [agentLabel(s.agent), s.status]
                if (s.status === 'running' && s.activity) titleParts.push(s.activity)
                if (s.outcome) titleParts.push(s.outcome)
                return (
                  <div
                    key={s.uuid}
                    className={`idea-sidebar-item session ${s.status}`}
                    title={titleParts.join(' — ')}
                    onClick={() => navigate(`/idea/${slug}/session/${s.uuid}`)}
                  >
                    <SessionStatusIcon status={s.status} activity={s.activity || undefined} stopReason={s.stop_reason || undefined} />
                    <span className="session-agent">{agentLabel(s.agent)}</span>
                    <span className="session-time">{sessionLabel(s)}</span>
                  </div>
                )
              })
            )}
            </div>
          </div>

          <div className="idea-sidebar-section">
            {ideaFiles.map((f) => {
              const hasPendingReview = pendingReviewFiles.has(f)
              return (
                <div
                  key={f}
                  className={`idea-sidebar-item ${selectedFile === f ? 'selected' : ''}`}
                  onClick={() => setSelectedFile(f)}
                >
                  <span className="idea-file-name">{f}</span>
                  {hasPendingReview && (
                    <span
                      className="idea-file-review-pending"
                      title="Pending markdown review"
                      aria-label="Pending markdown review"
                    >
                      <Circle size={6} strokeWidth={0} fill="currentColor" />
                    </span>
                  )}
                </div>
              )
            })}

            {repos.map((r) => {
              const files = repoFiles[r.name] || []
              return (
                <div key={r.name} className="repo-tree">
                  <div className="idea-sidebar-item repo" title={`${r.path}${r.branch ? ' — branch ' + r.branch : ''}`}>
                    <span className="repo-name">{r.name}</span>
                    {!r.isDefaultBranch && r.branch && (
                      <>
                        <span className="repo-at">@</span>
                        <span className="repo-branch">{r.branch}</span>
                      </>
                    )}
                    {r.dirty && (
                      <span className="repo-dirty" title="Uncommitted changes" aria-label="Uncommitted changes">
                        <Circle size={8} strokeWidth={0} fill="currentColor" />
                      </span>
                    )}
                    {r.ahead > 0 && (
                      <span className="repo-ahead" title="Commits ahead of upstream">
                        <ArrowUp size={12} strokeWidth={2} />{r.ahead}
                      </span>
                    )}
                    {r.behind > 0 && (
                      <span className="repo-behind" title="Commits behind upstream">
                        <ArrowDown size={12} strokeWidth={2} />{r.behind}
                      </span>
                    )}
                  </div>
                  {files.map((f) => {
                    const fullPath = repoFilePath(r.name, f)
                    return (
                      <div
                        key={fullPath}
                        className={`idea-sidebar-item repo-file repo-child ${selectedFile === fullPath ? 'selected' : ''}`}
                        onClick={() => setSelectedFile(fullPath)}
                      >
                        {f}
                      </div>
                    )
                  })}
                </div>
              )
            })}
          </div>

          {idea.resources && idea.resources.length > 0 && (
            <div className="idea-sidebar-section">
              <div className="idea-sidebar-section-title">Resources ({idea.resources.length})</div>
              {idea.resources.map((r, i) => (
                <div
                  key={i}
                  className="idea-sidebar-item resource"
                  title={r.url || ''}
                  onClick={() => { if (r.url) openExternal(r.url) }}
                >
                  {r.label || r.type}
                </div>
              ))}
            </div>
          )}
        </div>

        <div className="idea-main">
          {selectedFile ? (
            <div className="idea-main-content">
              <MarkdownViewer
                content={fileContent}
                className="idea-main-file"
                sourcePath={selectedFile || 'idea.md'}
                repos={repos}
                onSelectFile={setSelectedFile}
              />
            </div>
          ) : (
            <div className="idea-main-empty">
              {idea.summary ? (
                <MarkdownViewer
                  content={idea.summary}
                  className="idea-main-file"
                  sourcePath="idea.md"
                  repos={repos}
                  onSelectFile={setSelectedFile}
                />
              ) : (
                <p>Select a file from the sidebar, or create content for this idea.</p>
              )}
            </div>
          )}
        </div>
      </div>

      {parseCreatedDate(slug) && (
        <div className="idea-detail-created">Created {parseCreatedDate(slug)}</div>
      )}
      <HistoryPanel slug={slug} />
    </div>
  )
}
