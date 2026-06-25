import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { FileDiff, FileText } from 'lucide-react'
import { EventsOn } from '../wailsjs/runtime/runtime'

interface PendingReviewSummary {
  id: string
  kind: string
  label: string
  created: string
  ideaSlug?: string
  // session is the agent session UUID that requested the review when
  // known. Used to wire `?fromSession=<slug>:<uuid>` onto the chip nav
  // URL so the review's Submit/Cancel handlers return to the launching
  // session instead of stranding the user on /review.
  session?: string
}

// Polling backstop in case a review:changed event is dropped (e.g. the
// frontend mounts during a status flip and misses the emit). Long
// enough to avoid burning cycles when nothing's happening — the event
// is the primary refresh trigger.
const REFRESH_INTERVAL_MS = 30_000

function ageLabel(createdISO: string, now: number): string {
  const t = Date.parse(createdISO)
  if (Number.isNaN(t)) return ''
  const seconds = Math.max(0, Math.floor((now - t) / 1000))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h`
  const days = Math.floor(hours / 24)
  return `${days}d`
}

// PendingReviewsBar renders chips for every pending review (diff or markdown)
// alongside the GlobalSessionBar. Click to open the review by ID. Refreshes
// on the review:changed event (created / submitted / cancelled / swept)
// with a 30s polling backstop in case an event is dropped.
export default function PendingReviewsBar() {
  const navigate = useNavigate()
  const [reviews, setReviews] = useState<PendingReviewSummary[]>([])
  // now ticks every 30s so the age labels update without re-fetching the
  // backend list. Starts at first render time so existing chips don't
  // flash an empty age.
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    let cancelled = false
    const refresh = async () => {
      try {
        // Dynamic import so older bundles missing the binding don't crash
        // the footer's alerts zone (matches GlobalSessionBar's pattern).
        const mod = await import('../wailsjs/go/app/App')
        const fn = (mod as Record<string, unknown>)['ListPendingReviews'] as
          | (() => Promise<PendingReviewSummary[]>)
          | undefined
        if (!fn) return
        const list = await fn()
        if (cancelled) return
        setReviews(Array.isArray(list) ? list : [])
      } catch {
        if (!cancelled) setReviews([])
      }
    }
    refresh()
    const cancelReviewChanged = EventsOn('review:changed', () => { refresh() })
    const id = setInterval(refresh, REFRESH_INTERVAL_MS)
    const tick = setInterval(() => setNow(Date.now()), 30_000)
    return () => {
      cancelled = true
      clearInterval(id)
      clearInterval(tick)
      cancelReviewChanged()
    }
  }, [])

  if (reviews.length === 0) return null

  const goTo = (r: PendingReviewSummary) => () => {
    let url = `/review?reviewId=${encodeURIComponent(r.id)}`
    // When the review came from an agent session, carry fromSession so
    // Review/MarkdownReview's submit + cancel handlers can navigate back
    // to the launching session instead of stranding the user on
    // /review. Matches the in-session "Open review" banner path.
    if (r.ideaSlug && r.session) {
      url += `&fromSession=${encodeURIComponent(`${r.ideaSlug}:${r.session}`)}`
    }
    navigate(url)
  }

  return (
    <div className="pending-reviews-bar" data-testid="pending-reviews-bar">
      {reviews.map((r) => {
        const Icon = r.kind === 'markdown' ? FileText : FileDiff
        const age = ageLabel(r.created, now)
        return (
          <div
            key={r.id}
            role="button"
            tabIndex={0}
            className={`pending-review-chip kind-${r.kind}`}
            data-testid="pending-review-chip"
            data-review-id={r.id}
            title={`${r.kind === 'markdown' ? 'Markdown review' : 'Diff review'}: ${r.label} — pending ${age}`}
            onClick={goTo(r)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault()
                goTo(r)()
              }
            }}
          >
            <Icon size={12} strokeWidth={1.75} />
            <span className="pending-review-chip-label">{r.label}</span>
            {age && <span className="pending-review-chip-age">{age}</span>}
          </div>
        )
      })}
    </div>
  )
}
