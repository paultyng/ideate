import { useEffect } from 'react'
import { useConfirmRequest, resolveCurrentConfirm } from '../lib/confirmDialog'

// ConfirmDialog renders the in-app modal that backs requestConfirm. Mount
// once at App root. Escape resolves false (Cancel); the overlay click
// does NOT resolve so a stray click outside the dialog can't accidentally
// discard work.
export default function ConfirmDialog() {
  const req = useConfirmRequest()

  useEffect(() => {
    if (!req) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        resolveCurrentConfirm(false)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [req])

  if (!req) return null
  return (
    <div className="confirm-dialog-overlay" role="dialog" aria-modal="true" data-testid="confirm-dialog">
      <div className="confirm-dialog">
        <p>{req.message}</p>
        <div className="confirm-dialog-actions">
          <button
            type="button"
            className="btn-secondary"
            data-testid="confirm-dialog-cancel"
            onClick={() => resolveCurrentConfirm(false)}
          >
            {req.cancelLabel}
          </button>
          <button
            type="button"
            className="btn-primary"
            data-testid="confirm-dialog-confirm"
            onClick={() => resolveCurrentConfirm(true)}
            autoFocus
          >
            {req.confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}
