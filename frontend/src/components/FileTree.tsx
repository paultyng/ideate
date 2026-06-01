interface FileTreeProps {
  files: Array<{
    oldName: string
    newName: string
    status: string
  }>
  selectedIndex: number
  onSelect: (index: number) => void
  width?: number
  commentCounts?: Record<string, number> // path → comment count
}

const statusIndicators: Record<string, { char: string; className: string }> = {
  added: { char: '+', className: 'file-tree-status-added' },
  modified: { char: '~', className: 'file-tree-status-modified' },
  deleted: { char: '-', className: 'file-tree-status-deleted' },
  renamed: { char: '\u2192', className: 'file-tree-status-renamed' },
}

export default function FileTree({ files, selectedIndex, onSelect, width, commentCounts }: FileTreeProps) {
  return (
    <div className="file-tree" style={width ? { width, minWidth: width } : undefined}>
      {files.map((file, index) => {
        const indicator = statusIndicators[file.status] ?? statusIndicators.modified
        const displayName = file.status === 'deleted' ? file.oldName : file.newName
        const count = commentCounts?.[displayName] ?? 0

        return (
          <div
            key={index}
            className={`file-tree-item${index === selectedIndex ? ' selected' : ''}`}
            onClick={() => onSelect(index)}
          >
            <span className={`file-tree-status ${indicator.className}`}>
              {indicator.char}
            </span>
            <span className="file-tree-name" title={displayName}>
              {displayName}
            </span>
            {count > 0 && (
              <span className="file-tree-comment-badge" data-testid="file-tree-comment-badge" title={`${count} comment${count !== 1 ? 's' : ''}`}>
                💬 {count}
              </span>
            )}
          </div>
        )
      })}
    </div>
  )
}
