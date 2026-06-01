import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { CreateIdea } from '../wailsjs/go/app/App'
import { useDirtyGuard } from '../hooks/useDirtyGuard'

const STATUSES = ['active', 'paused', 'archived'] as const

// IdeaForm is the create-only "New Idea" surface at /idea/new — fast-
// path first-touch capture from the home dashboard's + button.
//
// All other idea mutations (rename, status / name / summary edits,
// delete, link/unlink repos, add/update resources) live on the
// orchestrator MCP per the AGENTS.md "MCP-tooling first" principle.
// Ask the orchestrator: "rename this idea to X", "delete this idea",
// "link repo at /path/to/foo", "add a Notion link", etc.
export default function IdeaForm() {
  const navigate = useNavigate()

  const [name, setName] = useState('')
  const [status, setStatus] = useState<string>('paused')
  const [summary, setSummary] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const isDirty = name !== '' || status !== 'paused' || summary !== ''
  const dirtyGuard = useDirtyGuard(isDirty)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return

    setSaving(true)
    setError(null)
    try {
      const newSlug = await CreateIdea(name.trim(), status, summary)
      dirtyGuard.bypass()
      navigate(`/idea/${newSlug}`)
    } catch (e) {
      setError(String(e))
      setSaving(false)
    }
  }

  return (
    <div className="idea-form">
      <h1>New Idea</h1>
      <p className="idea-form-hint">
        You can also create ideas from the orchestrator — ask it (e.g. "create
        an idea called X") and it'll call <code>create_idea</code> for you.
      </p>
      <form onSubmit={handleSubmit}>
        <label>
          Name
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Idea name"
            required
            autoFocus
          />
        </label>

        <label>
          Status
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            {STATUSES.map((s) => (
              <option key={s} value={s}>{s.charAt(0).toUpperCase() + s.slice(1)}</option>
            ))}
          </select>
        </label>

        <label>
          Summary
          <textarea
            value={summary}
            onChange={(e) => setSummary(e.target.value)}
            placeholder="Markdown summary..."
            rows={8}
          />
        </label>

        {error && <div className="idea-form-error">{error}</div>}

        <div className="idea-form-actions">
          <button type="submit" className="btn-primary" disabled={saving || !name.trim()}>
            {saving ? 'Saving...' : 'Create'}
          </button>
          <button
            type="button"
            className="btn-secondary"
            onClick={() => dirtyGuard.confirmIfDirty(() => navigate('/'))}
          >
            Cancel
          </button>
        </div>
      </form>
    </div>
  )
}
