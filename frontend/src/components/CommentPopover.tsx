import { useEffect, useRef, useState } from 'react'
import { useFloating, autoUpdate, offset, flip, shift } from '@floating-ui/react'

interface AnchorRect {
  // Screen-space coordinates. Width/height optional — if omitted the
  // popover anchors to a 0x0 point (e.g. the editor cursor).
  x: number
  y: number
  width?: number
  height?: number
}

interface Props {
  open: boolean
  anchor: AnchorRect | null
  onSubmit: (text: string) => void
  onClose: () => void
}

// CommentPopover collects the body of a CriticMarkup `{>>comment<<}` mark.
// Replaces window.prompt (Wails' WKWebView no-ops it on macOS) and the
// previous centered modal.
//
// Anchored to the editor cursor via Floating UI: positions next to the
// caret with offset / flip / shift middleware so the popover never drifts
// off-screen. Esc dismisses, Cmd/Ctrl+Enter submits.
export default function CommentPopover({ open, anchor, onSubmit, onClose }: Props) {
  const [text, setText] = useState('')
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)

  const { refs, floatingStyles } = useFloating({
    open,
    onOpenChange: (next) => {
      if (!next) onClose()
    },
    placement: 'bottom-start',
    middleware: [offset(8), flip(), shift({ padding: 8 })],
    whileElementsMounted: autoUpdate,
  })

  // Anchor to a virtual element built from the supplied screen rect.
  useEffect(() => {
    if (!anchor) return
    refs.setReference({
      getBoundingClientRect: () => ({
        x: anchor.x,
        y: anchor.y,
        top: anchor.y,
        left: anchor.x,
        right: anchor.x + (anchor.width ?? 0),
        bottom: anchor.y + (anchor.height ?? 0),
        width: anchor.width ?? 0,
        height: anchor.height ?? 0,
      }),
    })
  }, [anchor, refs])

  // Reset and focus when (re-)opened.
  useEffect(() => {
    if (!open) return
    setText('')
    queueMicrotask(() => textareaRef.current?.focus())
  }, [open])

  // Esc to dismiss; click outside to dismiss.
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        onClose()
      }
    }
    const onClick = (e: MouseEvent) => {
      const popover = refs.floating.current
      if (!popover) return
      if (e.target instanceof Node && popover.contains(e.target)) return
      onClose()
    }
    window.addEventListener('keydown', onKey)
    // mousedown so we beat the editor's focus-on-click and avoid races.
    document.addEventListener('mousedown', onClick)
    return () => {
      window.removeEventListener('keydown', onKey)
      document.removeEventListener('mousedown', onClick)
    }
  }, [open, onClose, refs.floating])

  if (!open || !anchor) return null

  const submit = () => {
    const trimmed = text.trim()
    if (!trimmed) return
    onSubmit(trimmed)
  }

  const handleKey = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault()
      submit()
    }
  }

  return (
    <div
      ref={refs.setFloating}
      style={floatingStyles}
      className="comment-popover"
      data-testid="cm-comment-popover"
      role="dialog"
      aria-label="Insert comment"
    >
      <textarea
        ref={textareaRef}
        data-testid="cm-comment-popover-input"
        className="comment-popover-input"
        rows={3}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={handleKey}
        placeholder="Comment for the agent…"
      />
      <div className="comment-popover-actions">
        <span className="comment-popover-hint">⌘/Ctrl+Enter</span>
        <button type="button" className="btn-secondary btn-tight" onClick={onClose}>
          Cancel
        </button>
        <button
          type="button"
          className="btn-primary btn-tight"
          data-testid="cm-comment-popover-submit"
          onClick={submit}
          disabled={!text.trim()}
        >
          Insert
        </button>
      </div>
    </div>
  )
}
