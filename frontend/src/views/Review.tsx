import { useState, useEffect, useCallback, ReactNode, useMemo, useRef } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'
import { DiffFile } from '@git-diff-view/core'
import { Quit } from '../wailsjs/runtime/runtime'
import {
  CancelReview,
  GetLaunchConfig,
  GetLocalDiff,
  GetReview,
  SaveReviewDraft,
  SubmitDiffReview,
} from '../wailsjs/go/app/App'
import FileTree from '../components/FileTree'
import DiffPanel, { DiffModeEnum, SplitSide } from '../components/DiffPanel'
import ResizeHandle from '../components/ResizeHandle'
import MarkdownReview from './MarkdownReview'

interface FileDiff {
  oldName: string
  newName: string
  status: string
  hunks: string
  oldContent: string
  newContent: string
  language: string
}

interface ReviewComment {
  path: string
  line: number
  start_line?: number
  side: string
  start_side?: string
  body: string
}

interface MarkdownPayload {
  path: string
  original?: string
  marked_up?: string
  draft_marked_up?: string
}

interface ReviewData {
  id: string
  kind?: string
  status: string
  repo?: string
  base_commit?: string
  head_commit?: string
  head_ref?: string
  comments?: ReviewComment[]
  body?: string
  event?: string
  markdown?: MarkdownPayload
  draft_body?: string
  draft_comments?: ReviewComment[]
}

// Widget state for the inline comment form
interface CommentWidget {
  file: string
  lineNumber: number
  side: SplitSide
}

function groupBySide(comments: ReviewComment[], leftSide: boolean): Record<string, { data: ReviewComment[] }> {
  const grouped: Record<string, { data: ReviewComment[] }> = {}
  for (const c of comments) {
    const onLeft = c.side === 'LEFT'
    if (onLeft !== leftSide) continue
    const key = String(c.line)
    if (!grouped[key]) {
      grouped[key] = { data: [] }
    }
    grouped[key].data.push(c)
  }
  return grouped
}

export default function Review() {
  const [searchParams] = useSearchParams()
  // location.key bumps on every navigate() call, including same-URL pushes
  // from the IPC OpenReview handler. We add it to the GetReview effect's
  // deps so reopening the same review by ID re-fetches the on-disk record
  // instead of rendering whatever was last in state (e.g. showing
  // "cancelled" after the file has been re-seeded to "pending").
  const location = useLocation()
  const [repo, setRepo] = useState<string | null>(searchParams.get('repo'))
  const [base, setBase] = useState<string | null>(searchParams.get('base'))
  const [head, setHead] = useState<string | null>(searchParams.get('head'))
  const reviewId = searchParams.get('reviewId')
  const navigate = useNavigate()
  // fromSession is set by the in-session "Open review" banner so we can
  // render a back-to-session affordance. Format: "<slug>:<sessionId>".
  const backToSession = useMemo(() => {
    const fs = searchParams.get('fromSession') || ''
    const idx = fs.indexOf(':')
    if (idx <= 0) return ''
    return `/idea/${fs.slice(0, idx)}/session/${fs.slice(idx + 1)}`
  }, [searchParams])

  const [files, setFiles] = useState<FileDiff[]>([])
  const [selectedIndex, setSelectedIndex] = useState(0)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [diffMode, setDiffMode] = useState<DiffModeEnum>(() => {
    const saved = localStorage.getItem('ideate:review:diffMode')
    return saved === 'unified' ? DiffModeEnum.Unified : DiffModeEnum.Split
  })
  const [sidebarWidth, setSidebarWidth] = useState(() => {
    const saved = localStorage.getItem('ideate:review:sidebarWidth')
    return saved ? Math.max(120, Math.min(600, parseInt(saved, 10))) : 250
  })

  // Review state
  const [reviewData, setReviewData] = useState<ReviewData | null>(null)
  const [comments, setComments] = useState<ReviewComment[]>([])
  const [activeWidget, setActiveWidget] = useState<CommentWidget | null>(null)
  const [commentText, setCommentText] = useState('')
  // Edit-mode state: which comment (by array index) is currently in
  // its inline edit textarea, and the draft body. null index = no
  // active edit. Discarded on Cancel; persisted into comments[] on
  // Save (which then flows into the existing draft autosave).
  const [editingIndex, setEditingIndex] = useState<number | null>(null)
  const [editText, setEditText] = useState('')
  const [summaryText, setSummaryText] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [standalone, setStandalone] = useState(false)

  const isReviewMode = !!reviewId

  // Detect whether the app was launched as a standalone review window. Only
  // standalone launches Quit() the app on submit/cancel — in-app reviews must
  // not kill long-running daemon flows.
  useEffect(() => {
    GetLaunchConfig()
      .then((cfg) => setStandalone(!!cfg.standalone))
      .catch(() => {})
  }, [])

  // Load diff
  useEffect(() => {
    if (!repo || !base || !head) return
    setLoading(true)
    setError('')

    GetLocalDiff(repo, base, head)
      .then((r) => {
        setFiles((r.files as unknown as FileDiff[]) || [])
        setSelectedIndex(0)
      })
      .catch((err: unknown) => setError(String(err)))
      .finally(() => setLoading(false))
  }, [repo, base, head])

  // Load review data if in review mode. The record carries repo/base/head so
  // we fall back to it when the URL only has reviewId (e.g. `ideate review <id>`).
  // While pending, draft_* takes precedence over comments/body so unsubmitted
  // edits survive an app restart.
  useEffect(() => {
    if (!reviewId) return
    GetReview(reviewId)
      .then((r) => {
        const data = r as unknown as ReviewData
        setReviewData(data)
        const isPending = data.status === 'pending'
        const seedComments = (isPending && data.draft_comments) || data.comments
        const seedBody = (isPending && data.draft_body) || data.body
        if (seedComments) setComments(seedComments)
        if (seedBody) setSummaryText(seedBody)
        if (!repo && data.repo) setRepo(data.repo)
        if (!base && data.base_commit) setBase(data.base_commit)
        if (!head && data.head_commit) setHead(data.head_commit)
      })
      .catch(() => {})
  }, [reviewId, repo, base, head, location.key])

  // Debounced autosave of draft edits while the review is pending. Persisted
  // to the on-disk record so closing the app mid-review doesn't lose work.
  // Skips the initial hydration pass — comments/summaryText start at [] / ""
  // and the load effect populates them; we only save once the user actually
  // edits.
  // commentsRef / summaryRef mirror state synchronously so the unmount-
  // flush path sees the very latest edit even before React commits the
  // setState (Playwright's fill exhibits this; a fast user typing
  // immediately followed by Cmd+W or back hits the same race).
  const commentsRef = useRef(comments)
  const summaryRef = useRef(summaryText)
  useEffect(() => { commentsRef.current = comments }, [comments])
  useEffect(() => { summaryRef.current = summaryText }, [summaryText])
  // flushDraftRef holds a closure with the latest state so the unmount /
  // beforeunload paths can fire one final write — keystrokes within the
  // 500ms debounce window before nav-away would otherwise be lost.
  // Markdown reviews own their own draft path via <MarkdownReview/>;
  // Review.tsx's hooks run unconditionally even when delegating, so
  // gate every diff-autosave write on kind !== 'markdown' to avoid
  // racing the child's flush and clobbering its draft on unmount.
  const isDiffReview = reviewData?.kind !== 'markdown'
  const flushDraftRef = useRef<() => void>(() => {})
  useEffect(() => {
    flushDraftRef.current = () => {
      if (!reviewId || reviewData?.status !== 'pending' || !isDiffReview) return
      SaveReviewDraft(reviewId, summaryRef.current, commentsRef.current).catch(() => {})
    }
  })
  useEffect(() => {
    if (!reviewId) return
    if (reviewData?.status !== 'pending' || !isDiffReview) return
    const handle = window.setTimeout(() => flushDraftRef.current(), 500)
    return () => window.clearTimeout(handle)
  }, [reviewId, reviewData?.status, isDiffReview, summaryText, comments])
  // Mount-only flush hooks: route nav-away unmounts the component, app
  // close fires beforeunload — both flush the latest draft so a fast
  // Cmd+W or back-button doesn't drop in-flight edits.
  useEffect(() => {
    const onBeforeUnload = () => flushDraftRef.current()
    window.addEventListener('beforeunload', onBeforeUnload)
    return () => {
      window.removeEventListener('beforeunload', onBeforeUnload)
      flushDraftRef.current()
    }
  }, [])

  const handleAddComment = useCallback((lineNumber: number, side: SplitSide) => {
    if (!files[selectedIndex]) return
    setActiveWidget({
      file: files[selectedIndex].newName || files[selectedIndex].oldName,
      lineNumber,
      side,
    })
    setCommentText('')
  }, [files, selectedIndex])

  const handleSubmitComment = useCallback(() => {
    if (!activeWidget || !commentText.trim()) return
    const newComment: ReviewComment = {
      path: activeWidget.file,
      line: activeWidget.lineNumber,
      side: activeWidget.side === SplitSide.old ? 'LEFT' : 'RIGHT',
      body: commentText.trim(),
    }
    commentsRef.current = [...commentsRef.current, newComment]
    setComments(commentsRef.current)
    setActiveWidget(null)
    setCommentText('')
  }, [activeWidget, commentText])

  const handleRemoveComment = useCallback((index: number) => {
    commentsRef.current = commentsRef.current.filter((_, i) => i !== index)
    setComments(commentsRef.current)
    // If the removed row was the one being edited, drop the edit
    // session too — its index would now point at someone else.
    setEditingIndex((prev) => (prev === index ? null : prev))
  }, [])

  const handleStartEdit = useCallback((index: number, body: string) => {
    setEditingIndex(index)
    setEditText(body)
  }, [])

  const handleSaveEdit = useCallback(() => {
    if (editingIndex == null) return
    const trimmed = editText.trim()
    if (!trimmed) return
    commentsRef.current = commentsRef.current.map((c, i) =>
      i === editingIndex ? { ...c, body: trimmed } : c,
    )
    setComments(commentsRef.current)
    setEditingIndex(null)
    setEditText('')
  }, [editingIndex, editText])

  const handleCancelEdit = useCallback(() => {
    setEditingIndex(null)
    setEditText('')
  }, [])

  const handleSubmitReview = useCallback(async () => {
    if (!reviewId) return
    setSubmitting(true)
    try {
      const event = comments.length > 0 ? 'REQUEST_CHANGES' : 'APPROVE'
      await SubmitDiffReview(reviewId, event, summaryText, comments)
      if (standalone) {
        Quit()
      } else if (backToSession) {
        navigate(backToSession)
      } else {
        setReviewData((prev) => prev ? { ...prev, status: 'complete' } : prev)
        setSubmitting(false)
      }
    } catch (err) {
      setError(String(err))
      setSubmitting(false)
    }
  }, [reviewId, comments, summaryText, standalone, backToSession, navigate])

  const handleCancelReview = useCallback(async () => {
    if (!reviewId) return
    setSubmitting(true)
    try {
      await CancelReview(reviewId)
      if (standalone) {
        Quit()
      } else {
        setReviewData((prev) => prev ? { ...prev, status: 'cancelled' } : prev)
        setSubmitting(false)
      }
    } catch (err) {
      setError(String(err))
      setSubmitting(false)
    }
  }, [reviewId, standalone])

  const renderWidgetLine = useCallback(({ lineNumber, side, onClose }: {
    lineNumber: number
    side: SplitSide
    diffFile: DiffFile
    onClose: () => void
  }): ReactNode => {
    const currentFile = files[selectedIndex]?.newName || files[selectedIndex]?.oldName || ''
    if (!activeWidget || activeWidget.lineNumber !== lineNumber || activeWidget.side !== side || activeWidget.file !== currentFile) {
      return null
    }

    return (
      <div className="review-comment-widget">
        <textarea
          className="review-comment-input"
          data-testid="review-comment-textarea"
          placeholder="Leave a comment... (Markdown supported, use ```suggestion for code suggestions)"
          value={commentText}
          onChange={(e) => setCommentText(e.target.value)}
          autoFocus
          rows={3}
        />
        <div className="review-comment-actions">
          <button className="btn-small" data-testid="review-comment-save" onClick={handleSubmitComment} disabled={!commentText.trim()}>
            Comment
          </button>
          <button className="btn-small btn-cancel" data-testid="review-comment-cancel" onClick={() => { setActiveWidget(null); onClose() }}>
            Cancel
          </button>
        </div>
      </div>
    )
  }, [activeWidget, commentText, files, selectedIndex, handleSubmitComment])

  // Build extendData for persisted comments on the current file. The library
  // keys by line number on each side; we group all comments at the same
  // line+side into a single payload so duplicates aren't silently dropped.
  const currentFile = files[selectedIndex]?.newName || files[selectedIndex]?.oldName || ''
  const fileComments = comments.filter((c) => c.path === currentFile)
  const extendData = fileComments.length > 0 ? {
    newFile: groupBySide(fileComments, false),
    oldFile: groupBySide(fileComments, true),
  } : undefined

  const renderExtendLine = useCallback(({ data }: {
    lineNumber: number
    side: SplitSide
    data: unknown
    diffFile: DiffFile
    onUpdate: () => void
  }): ReactNode => {
    const lineComments = (data as ReviewComment[] | undefined) ?? []
    if (lineComments.length === 0) return null
    const canMutate = isReviewMode && reviewData?.status === 'pending'
    // @git-diff-view clones extendData, so reference-equality with the
    // source-of-truth comments[] no longer holds — fall back to a
    // (path, line, side, body) fingerprint so the resolved index
    // matches the array we mutate on edit/remove. Body is part of the
    // key because the same line can carry multiple comments with
    // different bodies; once the user edits one, the prior body
    // disappears and the next match resolves to a different row.
    const indexOfComment = (c: ReviewComment): number => {
      const direct = comments.indexOf(c)
      if (direct >= 0) return direct
      return comments.findIndex(
        (x) => x.path === c.path && x.line === c.line && x.side === c.side && x.body === c.body,
      )
    }
    return (
      <div className="review-comment-thread">
        {lineComments.map((comment) => {
          const idx = indexOfComment(comment)
          const isEditing = canMutate && editingIndex === idx
          return (
            <div className="review-comment-display" key={idx}>
              {isEditing ? (
                <>
                  <textarea
                    className="review-comment-input"
                    data-testid="review-comment-edit-textarea"
                    value={editText}
                    onChange={(e) => setEditText(e.target.value)}
                    autoFocus
                    rows={3}
                  />
                  <div className="review-comment-actions">
                    <button
                      className="btn-small"
                      data-testid="review-comment-edit-save"
                      onClick={handleSaveEdit}
                      disabled={!editText.trim()}
                    >
                      Save
                    </button>
                    <button
                      className="btn-small btn-cancel"
                      data-testid="review-comment-edit-cancel"
                      onClick={handleCancelEdit}
                    >
                      Cancel
                    </button>
                  </div>
                </>
              ) : (
                <>
                  <div className="review-comment-body">{comment.body}</div>
                  {canMutate && (
                    <div className="review-comment-row-actions">
                      <button
                        className="review-comment-edit"
                        data-testid="review-comment-edit"
                        onClick={() => handleStartEdit(idx, comment.body)}
                        title="Edit comment"
                      >
                        ✎
                      </button>
                      <button
                        className="review-comment-remove"
                        data-testid="review-comment-remove"
                        onClick={() => handleRemoveComment(idx)}
                        title="Remove comment"
                      >
                        x
                      </button>
                    </div>
                  )}
                </>
              )}
            </div>
          )
        })}
      </div>
    )
  }, [comments, isReviewMode, reviewData, handleRemoveComment, editingIndex, editText, handleSaveEdit, handleCancelEdit, handleStartEdit])

  // Markdown review dispatch: when the loaded record is kind=markdown, render
  // the WYSIWYG editor view instead of the diff UI. This works whether the
  // URL only had reviewId or had stale repo/base/head from a prior diff URL.
  if (reviewData?.kind === 'markdown') {
    return (
      <MarkdownReview
        review={reviewData as Required<Pick<ReviewData, 'id' | 'kind' | 'status'>> & ReviewData}
        standalone={standalone}
        backToSession={backToSession}
        onStatusChange={(status) => setReviewData((prev) => prev ? { ...prev, status } : prev)}
      />
    )
  }

  if (!repo || !base || !head) {
    return (
      <div className="review-container">
        <div className="review-empty">
          <h1>Review</h1>
          <p>No diff parameters provided.</p>
          <pre>task cli -- review diff --repo &lt;path&gt; --base main --head &lt;branch&gt;</pre>
        </div>
      </div>
    )
  }

  if (loading) {
    return (
      <div className="review-container">
        <div className="review-empty"><p>Loading diff...</p></div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="review-container">
        <div className="review-empty">
          <h1>Error</h1>
          <p className="review-error">{error}</p>
        </div>
      </div>
    )
  }

  const parts = repo.split('/')
  const repoName = parts.length >= 2 ? parts.slice(-2).join('/') : parts.pop() || repo
  const toggleMode = () => {
    setDiffMode((prev) => {
      const next = prev === DiffModeEnum.Split ? DiffModeEnum.Unified : DiffModeEnum.Split
      localStorage.setItem('ideate:review:diffMode', next === DiffModeEnum.Unified ? 'unified' : 'split')
      return next
    })
  }

  const isComplete = reviewData?.status === 'complete'
  const isCancelled = reviewData?.status === 'cancelled'
  const isPending = reviewData?.status === 'pending'

  return (
    <div className="review-container">
      <div className="review-toolbar">
        {backToSession && (
          <button
            className="btn-back"
            title="Back to session"
            aria-label="Back to session"
            onClick={() => navigate(backToSession)}
          >
            <ArrowLeft size={16} strokeWidth={1.75} />
          </button>
        )}
        <span className="review-toolbar-repo">{repoName}</span>
        <span className="review-toolbar-ref">{base}...{head}</span>
        <span className="review-toolbar-count">
          {files.length} file{files.length !== 1 ? 's' : ''} changed
        </span>
        {isReviewMode && comments.length > 0 && (
          <span className="review-toolbar-comments">
            {comments.length} comment{comments.length !== 1 ? 's' : ''}
          </span>
        )}
        <button className="toggle-btn" onClick={toggleMode}>
          {diffMode === DiffModeEnum.Split ? 'Unified' : 'Split'}
        </button>
        {isReviewMode && isPending && (
          <>
            <button className="toggle-btn review-submit-btn" data-testid="review-submit-btn" onClick={handleSubmitReview} disabled={submitting}>
              {submitting ? 'Submitting...' : 'Submit Review'}
            </button>
            <button className="toggle-btn" data-testid="review-cancel-btn" onClick={handleCancelReview} disabled={submitting}>
              Cancel Review
            </button>
          </>
        )}
        {isComplete && <span className="status-badge complete review-status-badge">Submitted</span>}
        {isCancelled && <span className="status-badge cancelled review-status-badge">Cancelled</span>}
      </div>

      {isReviewMode && isPending && (
        <div className="review-summary-bar">
          <textarea
            className="review-summary-input"
            placeholder="Review summary (optional)"
            value={summaryText}
            onChange={(e) => {
              summaryRef.current = e.target.value
              setSummaryText(e.target.value)
            }}
            rows={1}
          />
        </div>
      )}

      <div className="review-body">
        <FileTree
          files={files}
          selectedIndex={selectedIndex}
          onSelect={setSelectedIndex}
          width={sidebarWidth}
          commentCounts={isReviewMode ? comments.reduce<Record<string, number>>((acc, c) => {
            acc[c.path] = (acc[c.path] ?? 0) + 1
            return acc
          }, {}) : undefined}
        />
        <ResizeHandle onResize={(delta) => setSidebarWidth((w) => {
          const next = Math.max(120, Math.min(600, w + delta))
          localStorage.setItem('ideate:review:sidebarWidth', String(next))
          return next
        })} />
        {files.length > 0 && files[selectedIndex] && (
          <DiffPanel
            file={files[selectedIndex]}
            mode={diffMode}
            enableComments={isReviewMode && isPending}
            onAddComment={handleAddComment}
            renderWidgetLine={renderWidgetLine}
            extendData={extendData}
            renderExtendLine={renderExtendLine}
          />
        )}
      </div>
    </div>
  )
}
