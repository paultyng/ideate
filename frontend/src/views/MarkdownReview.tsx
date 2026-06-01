import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowLeft, Plus, Minus, MessageCircle } from 'lucide-react'
import { Crepe } from '@milkdown/crepe'
import { editorViewCtx } from '@milkdown/core'
import { listener, listenerCtx } from '@milkdown/plugin-listener'
import type { Ctx } from '@milkdown/ctx'
import '@milkdown/crepe/theme/common/style.css'
import '@milkdown/crepe/theme/frame-dark.css'

import { Quit } from '../wailsjs/runtime/runtime'
import { CancelReview, SaveMarkdownReviewDraft, SubmitMarkdownReview } from '../wailsjs/go/app/App'
import {
  criticMarkupPlugins,
  insertionSchema,
  deletionSchema,
  commentSchema,
  toggleInsertionCommand,
  toggleDeletionCommand,
  insertCommentCommand,
  collapseToSubstitutions,
  OPEN_COMMENT_MODAL_EVENT,
} from '../criticmarkup'
import type { Command as ProseCommand } from '@milkdown/prose/state'
import { buildCriticMarkupToolbar } from '../criticmarkup/toolbar'
import '../criticmarkup/style.css'
import { splitFrontmatter } from '../lib/frontmatter'
import { classify, openExternal } from '../lib/links'
import { useDirtyGuard } from '../hooks/useDirtyGuard'
import CommentPopover from '../components/CommentPopover'

interface MarkdownPayload {
  path: string
  original?: string
  marked_up?: string
  draft_marked_up?: string
}

interface ReviewData {
  id: string
  kind: string
  status: string
  body?: string
  event?: string
  markdown?: MarkdownPayload
  draft_body?: string
}

interface Props {
  review: ReviewData
  standalone: boolean
  // backToSession is a route path ("/idea/<slug>/session/<id>") to render
  // a "Back to session" affordance. Empty when not arriving from a session.
  backToSession?: string
  onStatusChange: (status: 'complete' | 'cancelled') => void
}

type EditorMode = 'wysiwyg' | 'source'

// MarkdownReview renders a Milkdown editor seeded with the agent's original
// markdown content. The human edits in place; CriticMarkup syntax
// ({++ ++}, {-- --}, {~~ ~> ~~}, {>> <<}) is preserved through the round-trip.
// A source-mode toggle pairs the WYSIWYG editor with a plain textarea —
// switching mode round-trips the content through state, but discards the
// editor's undo history (Crepe is destroyed and recreated).
export default function MarkdownReview({ review, standalone, backToSession, onStatusChange }: Props) {
  const navigate = useNavigate()
  const containerRef = useRef<HTMLDivElement | null>(null)
  const crepeRef = useRef<Crepe | null>(null)
  // While pending, draft_* takes precedence over the agent's snapshot so
  // unsubmitted edits (autosaved on every change) survive an app restart.
  const isPendingInitial = review.status === 'pending'
  const seededBody = (isPendingInitial ? review.draft_body : undefined) ?? review.body ?? ''
  const seededContent = (isPendingInitial ? review.markdown?.draft_marked_up : undefined)
    ?? review.markdown?.marked_up
    ?? review.markdown?.original
    ?? ''
  const [summaryText, setSummaryText] = useState(seededBody)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [mode, setMode] = useState<EditorMode>('wysiwyg')
  // currentContent is the full content (body + any leading YAML
  // frontmatter). Source mode binds to it directly so the human can edit
  // frontmatter as text. WYSIWYG mode strips the frontmatter on mount
  // (Crepe / CommonMark would render `---` as a thematic break) and
  // re-attaches the (possibly source-edited) frontmatter on unmount.
  const [currentContent, setCurrentContent] = useState(seededContent)
  const [commentAnchor, setCommentAnchor] = useState<{ x: number; y: number } | null>(null)
  // bodyVersion bumps on every WYSIWYG doc change (via the Milkdown
  // listener plugin) so the autosave effect can debounce off it without
  // pulling the body out of Crepe on every keystroke.
  const [bodyVersion, setBodyVersion] = useState(0)
  // hasEdited flips true on the user's first real edit (any of: a Crepe
  // doc change after the seed set, a summary keystroke, a source-mode
  // keystroke). Drives the unsaved-changes guard's beforeunload prompt
  // and the Cancel-Review confirm. Stays true once flipped — submit and
  // cancel both go through dirtyGuard.bypass() before nav-away.
  const [hasEdited, setHasEdited] = useState(false)

  const isPending = review.status === 'pending'
  const isComplete = review.status === 'complete'
  const isCancelled = review.status === 'cancelled'

  // Guard the destructive Cancel Review action and browser-level
  // close/refresh while the user has un-submitted edits. Drafts are
  // autosaved server-side, so nav-away inside the SPA is non-
  // destructive — this is for the explicit Cancel button (which clears
  // the drafts) and beforeunload (which catches Cmd+W / dev-server
  // refresh / window close where the user hasn't reached for Submit
  // or Cancel yet).
  const isDirty = isPending && hasEdited
  const dirtyGuard = useDirtyGuard(
    isDirty,
    'You have unsaved review changes. Discard?',
  )

  // Mount Crepe only when WYSIWYG mode is active. Frontmatter is stripped
  // here (Crepe doesn't grok YAML) and re-attached on unmount so source
  // mode round-trips losslessly. On unmount (mode flip or review change),
  // capture the current body and combine with the (possibly source-edited)
  // frontmatter to update currentContent.
  useEffect(() => {
    if (mode !== 'wysiwyg' || !containerRef.current) return
    const { frontmatter, body } = splitFrontmatter(currentContent)
    const root = containerRef.current
    const crepe = new Crepe({
      root,
      defaultValue: body,
      featureConfigs: {
        [Crepe.Feature.Toolbar]: { buildToolbar: buildCriticMarkupToolbar },
      },
    })
    crepe.editor.use(criticMarkupPlugins)
    crepe.editor.use(listener)
    crepe.editor.config((ctx) => {
      ctx.get(listenerCtx).markdownUpdated((_ctx, next, prev) => {
        if (next === prev) return
        setBodyVersion((v) => v + 1)
        // Crepe's first markdownUpdated fires when it commits the seeded
        // doc into ProseMirror — prev is empty there. Skip that one so
        // bare-open of a pending review doesn't immediately read as
        // dirty. Subsequent edits all carry a non-empty prev.
        if (prev !== '') setHasEdited(true)
      })
    })
    crepeRef.current = crepe
    crepe.create().catch((err) => setError(`editor init failed: ${String(err)}`))
    if (!isPending) {
      crepe.setReadonly(true)
    }

    // Same link-classification rules as MarkdownViewer. Reviews aren't
    // idea-bound so there's no repo list — only external/anchor/unhandled
    // are reachable. Capture-phase listener beats Crepe's own bindings.
    const onClick = (e: MouseEvent) => {
      const a = (e.target as HTMLElement | null)?.closest('a')
      if (!a) return
      const result = classify(a.getAttribute('href') || '', '', [])
      if (result.kind === 'external') {
        e.preventDefault()
        openExternal(result.url)
      } else if (result.kind === 'unhandled') {
        e.preventDefault()
      }
    }
    root.addEventListener('click', onClick, true)

    return () => {
      root.removeEventListener('click', onClick, true)
      try {
        const newBody = crepe.getMarkdown()
        const next = frontmatter + newBody
        // Sync the ref synchronously so the unmount-flush path sees
        // the latest WYSIWYG output even before React commits the
        // setState below.
        contentRef.current = next
        setCurrentContent(next)
      } catch {
        /* editor may not have completed init — ignore */
      }
      void crepe.destroy()
      crepeRef.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode, review.id])

  // Toggle readonly when status changes (e.g. after submit/cancel in standalone mode).
  useEffect(() => {
    if (crepeRef.current) {
      crepeRef.current.setReadonly(!isPending)
    }
  }, [isPending])

  // runMarkCommand dispatches a ProseCommand built from a fresh ctx
  // lookup. The factory is invoked inside the milkdown action callback so
  // it picks up the current schema's MarkType/NodeType — necessary because
  // schemas are registered after the React component constructs.
  const runMarkCommand = useCallback(
    (factory: (ctx: Ctx) => ProseCommand) => {
      const crepe = crepeRef.current
      if (!crepe) return
      crepe.editor.action((ctx) => {
        const view = ctx.get(editorViewCtx)
        factory(ctx)(view.state, view.dispatch)
        view.focus()
      })
    },
    [],
  )

  // openCommentPopover anchors the popover to the editor's caret. Reads
  // the current selection's screen coords via ProseMirror's
  // view.coordsAtPos. Falls back to the viewport center when there's no
  // editor (e.g. source mode — though the comment button is hidden there).
  const openCommentPopover = useCallback(() => {
    const crepe = crepeRef.current
    if (!crepe) {
      setCommentAnchor({ x: window.innerWidth / 2, y: window.innerHeight / 2 })
      return
    }
    crepe.editor.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      const { from } = view.state.selection
      const coords = view.coordsAtPos(from)
      // coordsAtPos returns top/bottom/left/right in viewport space.
      setCommentAnchor({ x: coords.left, y: coords.bottom })
    })
  }, [])

  // Toolbar button + Mod-Shift-N both route through here. The keymap fires
  // a DOM event from inside the editor since ProseMirror commands can't
  // safely poke React state directly.
  useEffect(() => {
    if (!isPending) return
    const handler = () => openCommentPopover()
    document.addEventListener(OPEN_COMMENT_MODAL_EVENT, handler)
    return () => document.removeEventListener(OPEN_COMMENT_MODAL_EVENT, handler)
  }, [isPending, openCommentPopover])

  const handleInsertComment = useCallback(
    (text: string) => {
      runMarkCommand((ctx) => insertCommentCommand(commentSchema.type(ctx), text))
      setCommentAnchor(null)
    },
    [runMarkCommand],
  )

  // contentRef mirrors currentContent synchronously alongside setState so
  // the unmount-flush path can read the latest source-mode edit even when
  // the cleanup fires between the user's keystroke and React's next
  // render commit (Playwright fill exhibits this; real users hit it on
  // very fast type-then-navigate paths).
  const contentRef = useRef(currentContent)
  // summaryTextRef has the same purpose for the summary textarea.
  const summaryTextRef = useRef(summaryText)
  useEffect(() => { contentRef.current = currentContent }, [currentContent])
  useEffect(() => { summaryTextRef.current = summaryText }, [summaryText])

  // currentMarkdown returns the latest full content (frontmatter + body)
  // regardless of mode. WYSIWYG mode pulls the body from Crepe, runs the
  // substitution collapse (turns adjacent del/ins into `{~~ ~> ~~}` so
  // the agent sees a unified substitution), then re-attaches the
  // frontmatter from currentContent (which holds the canonical
  // frontmatter for the lifetime of the WYSIWYG mount). Source mode
  // reads contentRef so a not-yet-committed source edit is visible to
  // the flush path.
  const currentMarkdown = useCallback((): string => {
    if (mode === 'wysiwyg' && crepeRef.current) {
      const { frontmatter } = splitFrontmatter(contentRef.current)
      const body = collapseToSubstitutions(crepeRef.current.getMarkdown())
      return frontmatter + body
    }
    return contentRef.current
  }, [mode])

  // Debounced autosave of the in-progress markdown edits + summary. Pending
  // status only — once a review is complete or cancelled the drafts are
  // cleared backend-side and we shouldn't keep writing them. Triggers off
  // bodyVersion (WYSIWYG listener), currentContent (source mode), and
  // summaryText so all three edit paths converge on a single save.
  // lastSavedRef dedupes against the round-trip from Crepe's initial doc
  // set (markdownUpdated fires on mount with the seeded content) — without
  // it, every fresh open would re-save the seeded value as a new draft.
  const lastSavedRef = useRef<{ body: string; md: string } | null>(null)
  // flushDraftRef holds a closure with the latest state so the unmount /
  // beforeunload paths can fire one final write — keystrokes within the
  // 500ms debounce window before nav-away would otherwise be lost.
  const flushDraftRef = useRef<() => void>(() => {})
  useEffect(() => {
    flushDraftRef.current = () => {
      if (!isPending) return
      const md = currentMarkdown()
      const body = summaryTextRef.current
      if (lastSavedRef.current && lastSavedRef.current.body === body && lastSavedRef.current.md === md) {
        return
      }
      lastSavedRef.current = { body, md }
      SaveMarkdownReviewDraft(review.id, body, md).catch(() => {})
    }
  })
  useEffect(() => {
    if (!isPending) return
    const handle = window.setTimeout(() => flushDraftRef.current(), 500)
    return () => window.clearTimeout(handle)
  }, [review.id, isPending, bodyVersion, currentContent, summaryText, currentMarkdown])
  // Mount-only flush hooks: route nav-away unmounts the component, app
  // close fires beforeunload — both flush the latest draft synchronously
  // (via the IPC binding's fire-and-forget Promise) so a fast Cmd+W or
  // Cmd+[ doesn't drop in-flight edits.
  useEffect(() => {
    const onBeforeUnload = () => flushDraftRef.current()
    window.addEventListener('beforeunload', onBeforeUnload)
    return () => {
      window.removeEventListener('beforeunload', onBeforeUnload)
      flushDraftRef.current()
    }
  }, [])

  const handleSubmit = useCallback(async () => {
    setSubmitting(true)
    try {
      const markedUp = currentMarkdown()
      const event = markedUp !== (review.markdown?.original ?? '') ? 'REQUEST_CHANGES' : 'APPROVE'
      await SubmitMarkdownReview(review.id, event, summaryText, markedUp)
      // Drafts cleared server-side on submit; flip the guard before
      // any nav so a tab close immediately after Submit doesn't
      // re-prompt with a stale dirty flag.
      dirtyGuard.bypass()
      if (standalone) {
        Quit()
      } else if (backToSession) {
        navigate(backToSession)
      } else {
        onStatusChange('complete')
        setSubmitting(false)
      }
    } catch (err) {
      setError(String(err))
      setSubmitting(false)
    }
  }, [review.id, review.markdown, summaryText, standalone, onStatusChange, currentMarkdown, dirtyGuard, backToSession, navigate])

  // Cancel Review is the destructive path (clears server-side drafts).
  // Wrap in confirmIfDirty so the user gets one chance to back out
  // when they have un-submitted CriticMarkup edits or summary text.
  const handleCancel = useCallback(() => {
    dirtyGuard.confirmIfDirty(() => {
      void (async () => {
        setSubmitting(true)
        try {
          await CancelReview(review.id)
          dirtyGuard.bypass()
          if (standalone) {
            Quit()
          } else {
            onStatusChange('cancelled')
            setSubmitting(false)
          }
        } catch (err) {
          setError(String(err))
          setSubmitting(false)
        }
      })()
    })
  }, [review.id, standalone, onStatusChange, dirtyGuard])

  const path = review.markdown?.path ?? ''
  const fileName = path.split('/').pop() ?? path

  return (
    <div className="markdown-review-container">
      <div className="markdown-review-toolbar">
        {backToSession && (
          <a
            className="btn-back"
            href={`#${backToSession}`}
            title="Back to session"
            aria-label="Back to session"
          >
            <ArrowLeft size={16} strokeWidth={1.75} />
          </a>
        )}
        <span className="markdown-review-toolbar-file" title={path}>{fileName}</span>
        {isPending && mode === 'wysiwyg' && (
          <div className="markdown-review-mark-buttons">
            <button
              className="cm-mark-btn cm-mark-btn-insertion"
              data-testid="cm-insert-btn"
              onClick={() =>
                runMarkCommand((ctx) => toggleInsertionCommand(insertionSchema.type(ctx)))
              }
              title="Toggle insertion on selection (⌘⇧I)"
            >
              <Plus size={12} strokeWidth={2.5} />
              Insert
            </button>
            <button
              className="cm-mark-btn cm-mark-btn-deletion"
              data-testid="cm-delete-btn"
              onClick={() =>
                runMarkCommand((ctx) => toggleDeletionCommand(deletionSchema.type(ctx)))
              }
              title="Toggle deletion on selection (⌘⇧K)"
            >
              <Minus size={12} strokeWidth={2.5} />
              Delete
            </button>
            <button
              className="cm-mark-btn cm-mark-btn-comment"
              data-testid="cm-comment-btn"
              onClick={() => openCommentPopover()}
              title="Insert comment at cursor (⌘⇧N)"
            >
              <MessageCircle size={12} strokeWidth={2.5} />
              Comment
            </button>
          </div>
        )}
        <span className="markdown-review-spacer" />
        <button
          className="toggle-btn"
          data-testid="markdown-review-mode-toggle"
          onClick={() => setMode((m) => (m === 'wysiwyg' ? 'source' : 'wysiwyg'))}
          title="Toggle between WYSIWYG and source view"
        >
          {mode === 'wysiwyg' ? 'Source' : 'WYSIWYG'}
        </button>
        {isPending && (
          <>
            <button
              className="toggle-btn review-submit-btn"
              data-testid="markdown-review-submit-btn"
              onClick={handleSubmit}
              disabled={submitting}
            >
              {submitting ? 'Submitting...' : 'Submit Review'}
            </button>
            <button
              className="toggle-btn"
              data-testid="markdown-review-cancel-btn"
              onClick={handleCancel}
              disabled={submitting}
            >
              Cancel Review
            </button>
          </>
        )}
        {isComplete && <span className="status-badge complete review-status-badge">Submitted</span>}
        {isCancelled && <span className="status-badge cancelled review-status-badge">Cancelled</span>}
      </div>

      {isPending && (
        <div className="review-summary-bar">
          <textarea
            className="review-summary-input"
            placeholder="Review summary (optional)"
            value={summaryText}
            onChange={(e) => {
              summaryTextRef.current = e.target.value
              setSummaryText(e.target.value)
              setHasEdited(true)
            }}
            rows={1}
          />
        </div>
      )}

      {error && <div className="markdown-review-error">{error}</div>}

      {mode === 'wysiwyg' ? (
        <div
          className="markdown-review-editor"
          ref={containerRef}
          data-testid="markdown-review-editor"
        />
      ) : (
        <textarea
          className="markdown-review-source"
          data-testid="markdown-review-source"
          value={currentContent}
          onChange={(e) => {
            contentRef.current = e.target.value
            setCurrentContent(e.target.value)
            setHasEdited(true)
          }}
          readOnly={!isPending}
          spellCheck={false}
        />
      )}

      <CommentPopover
        open={commentAnchor !== null}
        anchor={commentAnchor}
        onSubmit={handleInsertComment}
        onClose={() => setCommentAnchor(null)}
      />
    </div>
  )
}
