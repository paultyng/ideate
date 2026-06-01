import { useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { ListIdeas, ListSessionSummaries } from '../wailsjs/go/app/App'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { model, store } from '../wailsjs/go/models'
import IdeaCard from '../components/IdeaCard'

function sortByDate(ideas: model.Idea[]): model.Idea[] {
  return [...ideas].sort((a, b) => {
    const dateA = a.updated || a.created || a.slug.substring(0, 10)
    const dateB = b.updated || b.created || b.slug.substring(0, 10)
    // Most recent first
    return String(dateB).localeCompare(String(dateA))
  })
}

export default function IdeaList() {
  const navigate = useNavigate()
  const [ideas, setIdeas] = useState<model.Idea[]>([])
  const [sessionSummaries, setSessionSummaries] = useState<Record<string, store.IdeaSessionSummary>>({})
  const [loading, setLoading] = useState(true)
  const [showArchived, setShowArchived] = useState(false)

  useEffect(() => {
    const refreshIdeas = () => {
      ListIdeas()
        .then((list) => setIdeas(list || []))
        .catch(() => setIdeas([]))
        .finally(() => setLoading(false))
    }
    const refreshSummaries = () => {
      ListSessionSummaries()
        .then((list) => {
          const map: Record<string, store.IdeaSessionSummary> = {}
          for (const s of list || []) map[s.slug] = s
          setSessionSummaries(map)
        })
        .catch(() => setSessionSummaries({}))
    }
    refreshIdeas()
    refreshSummaries()

    // idea:changed (subscribed below) drives most refreshes. Keep a
    // coarse backstop in case the watcher misses an event.
    const sessionInterval = setInterval(refreshSummaries, 10000)

    // Live-reload the list when any idea changes on disk.
    const cancelChanged = EventsOn('idea:changed', () => {
      refreshIdeas()
      refreshSummaries()
    })

    return () => {
      clearInterval(sessionInterval)
      cancelChanged()
    }
  }, [])

  return (
    <div className="dashboard">
      {/* No header chrome on the dashboard — the topbar (Home,
          Orchestrator, New Idea) covers the actions, and the pinned
          orchestrator drawer is the primary creation surface. */}

      {loading && <p>Loading ideas...</p>}

      {!loading && ideas.length === 0 && (
        <div className="idea-list-empty idea-list-onboarding">
          <div className="onboarding-wordmark">ideate</div>
          <p className="onboarding-tagline">One home for every dev idea, with agents wired in.</p>
          <p className="onboarding-pitch">Track ideas from spark to ship — research, code, review, and deploy, all in one place.</p>
          <div className="onboarding-slugs">
            <span className="onboarding-slug">fix-prod-deploy-bug</span>
            <span className="onboarding-slug">evaluate-new-graphql-lib</span>
          </div>
          <button
            className="btn-primary onboarding-cta"
            onClick={() => navigate('/idea/new')}
          >
            Create your first idea
          </button>
        </div>
      )}

      {!loading && ideas.length > 0 && (() => {
        const active = sortByDate(ideas.filter((i) => i.status !== 'archived'))
        const archived = sortByDate(ideas.filter((i) => i.status === 'archived'))
        return (
          <>
            <div className="idea-list">
              {active.map((idea) => (
                <IdeaCard key={idea.slug} idea={idea} sessions={sessionSummaries[idea.slug]} />
              ))}
              {active.length === 0 && <p>No active ideas.</p>}
            </div>
            {archived.length > 0 && (
              <div className="idea-list-archived">
                <button
                  className="btn-toggle-archived"
                  onClick={() => setShowArchived((v) => !v)}
                >
                  {showArchived ? 'Hide' : 'Show'} archived ({archived.length})
                </button>
                {showArchived && (
                  <div className="idea-list">
                    {archived.map((idea) => (
                      <IdeaCard key={idea.slug} idea={idea} sessions={sessionSummaries[idea.slug]} />
                    ))}
                  </div>
                )}
              </div>
            )}
          </>
        )
      })()}
    </div>
  )
}
