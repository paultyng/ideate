import { useMemo, ReactNode } from 'react'
import { DiffView, DiffModeEnum, SplitSide } from '@git-diff-view/react'
import { DiffFile } from '@git-diff-view/core'
import '@git-diff-view/react/styles/diff-view.css'

interface DiffPanelProps {
  file: {
    oldName: string
    newName: string
    status: string
    hunks: string
    oldContent: string
    newContent: string
    language: string
  }
  mode: DiffModeEnum
  enableComments?: boolean
  onAddComment?: (lineNumber: number, side: SplitSide) => void
  renderWidgetLine?: (props: {
    lineNumber: number
    side: SplitSide
    diffFile: DiffFile
    onClose: () => void
  }) => ReactNode
  extendData?: {
    oldFile?: Record<string, { data: unknown }>
    newFile?: Record<string, { data: unknown }>
  }
  renderExtendLine?: (props: {
    lineNumber: number
    side: SplitSide
    data: unknown
    diffFile: DiffFile
    onUpdate: () => void
  }) => ReactNode
}

export default function DiffPanel({ file, mode, enableComments, onAddComment, renderWidgetLine, extendData, renderExtendLine }: DiffPanelProps) {
  const diffFileInstance = useMemo(() => {
    const instance = DiffFile.createInstance({
      oldFile: {
        fileName: file.oldName || file.newName,
        fileLang: file.language,
        content: file.oldContent,
      },
      newFile: {
        fileName: file.newName || file.oldName,
        fileLang: file.language,
        content: file.newContent,
      },
      hunks: [file.hunks],
    })
    instance.initTheme('dark')
    instance.init()
    instance.buildSplitDiffLines()
    instance.buildUnifiedDiffLines()
    return instance
  }, [file])

  return (
    <div className="diff-panel">
      <DiffView
        diffFile={diffFileInstance}
        diffViewMode={mode}
        diffViewTheme="dark"
        diffViewHighlight={true}
        diffViewWrap={false}
        diffViewFontSize={13}
        diffViewAddWidget={enableComments}
        onAddWidgetClick={onAddComment}
        renderWidgetLine={renderWidgetLine}
        extendData={extendData}
        renderExtendLine={renderExtendLine}
        style={{ height: '100%', width: '100%' }}
      />
    </div>
  )
}

export { DiffModeEnum, SplitSide }
