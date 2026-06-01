import { useState, useEffect } from 'react'
import { GetHistory } from '../wailsjs/go/app/App'
import { model } from '../wailsjs/go/models'

interface Props {
  slug: string
}

export default function HistoryPanel({ slug }: Props) {
  const [expanded, setExpanded] = useState(false)
  const [events, setEvents] = useState<model.HistoryEvent[]>([])
  const [loading, setLoading] = useState(false)
  const [loaded, setLoaded] = useState(false)

  useEffect(() => {
    if (expanded && !loaded) {
      setLoading(true)
      GetHistory(slug)
        .then((evts) => {
          setEvents(evts || [])
          setLoaded(true)
        })
        .catch(() => setEvents([]))
        .finally(() => setLoading(false))
    }
  }, [expanded, loaded, slug])

  // Reset when slug changes
  useEffect(() => {
    setLoaded(false)
    setEvents([])
    setExpanded(false)
  }, [slug])

  const formatTimestamp = (ts: any): string => {
    try {
      return new Date(ts).toLocaleString()
    } catch {
      return String(ts)
    }
  }

  return (
    <div className={`history-panel ${expanded ? 'expanded' : 'collapsed'}`}>
      <div className="history-panel-header" onClick={() => setExpanded(!expanded)}>
        <span className="history-panel-toggle">{expanded ? '\u25BC' : '\u25B6'}</span>
        <span>History{loaded ? ` (${events.length} event${events.length !== 1 ? 's' : ''})` : ''}</span>
      </div>
      <div className="history-panel-body">
        {loading && <div className="history-loading">Loading...</div>}
        {expanded && !loading && events.length === 0 && (
          <div className="history-empty">No history events</div>
        )}
        {expanded && events.map((evt, i) => (
          <div className="history-event" key={i}>
            <span className="history-event-ts">{formatTimestamp(evt.ts)}</span>
            <span className="history-event-type">{evt.event}</span>
            {evt.fields && Object.keys(evt.fields).length > 0 && (
              <span className="history-event-fields">
                {Object.entries(evt.fields).map(([k, v]) => `${k}=${v}`).join(', ')}
              </span>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
