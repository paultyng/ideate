import { useEffect, useMemo, useState } from 'react'
import { matchPath, useLocation, useSearchParams } from 'react-router-dom'

// AppTopbarTitle renders a route-aware label centered in the topbar:
// "Dashboard" / idea name / session name / "Review: <repo or file>" /
// "New idea". Anchors the topbar so the empty middle isn't dead space
// and gives the user a "where am I" cue at a glance. Falls back to a
// raw slug on routes whose title needs an async lookup before the
// resolved name lands (idea name; session via the parent idea).
function useDocumentTitle(title: string): void {
  useEffect(() => {
    document.title = title ? `Ideate — ${title}` : 'Ideate'
  }, [title])
}

interface IdeaSummary {
  name: string
}

export default function AppTopbarTitle() {
  const location = useLocation()
  const [params] = useSearchParams()
  const [ideaName, setIdeaName] = useState<string>('')

  // Match against the route table. Order matters: more-specific routes
  // first, since matchPath returns the first hit.
  const ideaSessionNew = matchPath('/idea/:slug/session/new', location.pathname)
  const ideaSession = matchPath('/idea/:slug/session/:sessionId', location.pathname)
  const ideaEdit = matchPath('/idea/:slug/edit', location.pathname)
  const ideaDetail = matchPath('/idea/:slug', location.pathname)
  const ideaNew = matchPath('/idea/new', location.pathname)
  const review = matchPath('/review', location.pathname)
  const slug = ideaSession?.params.slug
    || ideaEdit?.params.slug
    || (ideaDetail && !ideaNew ? ideaDetail.params.slug : undefined)

  // Look up the idea's display name lazily — the binding is cheap and
  // memoization on `slug` keeps it to one call per navigation.
  useEffect(() => {
    if (!slug || slug === 'new') {
      setIdeaName('')
      return
    }
    let cancelled = false
    import('../wailsjs/go/app/App')
      .then((mod) => {
        const fn = (mod as Record<string, unknown>)['GetIdea'] as
          | ((s: string) => Promise<IdeaSummary>)
          | undefined
        if (!fn) return
        return fn(slug)
      })
      .then((idea) => {
        if (cancelled || !idea) return
        setIdeaName(idea.name || slug)
      })
      .catch(() => {
        if (!cancelled) setIdeaName(slug || '')
      })
    return () => { cancelled = true }
  }, [slug])

  const title = useMemo(() => {
    if (location.pathname === '/') return 'Dashboard'
    if (ideaNew) return 'New idea'
    if (ideaEdit && ideaName) return `Edit: ${ideaName}`
    if (ideaSessionNew && ideaName) return `${ideaName} — New Session`
    if (ideaSession && ideaName) return ideaName
    if (ideaDetail && ideaName) return ideaName
    if (review) {
      const reviewId = params.get('reviewId')
      const repo = params.get('repo')
      if (repo) {
        const leaf = repo.split('/').filter(Boolean).pop() || repo
        return `Review: ${leaf}`
      }
      return reviewId ? `Review: ${reviewId}` : 'Review'
    }
    return ''
  }, [location.pathname, ideaNew, ideaEdit, ideaSessionNew, ideaSession, ideaDetail, ideaName, review, params])

  useDocumentTitle(title)

  // Always render the title slot — falling back to "Ideate" when the
  // route has no route-specific title. The slot lives inside
  // .app-topbar-left so it flows after the view-actions cluster with a
  // single separator between sections.
  const displayed = title || 'Ideate'
  // The .idea-detail-name class is preserved here to keep the existing
  // Playwright selectors working; same span doubles as a stable test
  // anchor (data-testid="topbar-title") for new tests.
  return (
    <div className="app-topbar-title" aria-live="polite">
      <span
        className="app-topbar-title-text idea-detail-name"
        data-testid="topbar-title"
      >
        {displayed}
      </span>
    </div>
  )
}
