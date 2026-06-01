import { useEffect, useRef } from 'react'
import { Crepe } from '@milkdown/crepe'
import '@milkdown/crepe/theme/common/style.css'
import '@milkdown/crepe/theme/frame-dark.css'

import { splitFrontmatter } from '../lib/frontmatter'
import { classify, openExternal, gitHubBlobURL } from '../lib/links'
import type { store } from '../wailsjs/go/models'

interface Props {
  content: string
  className?: string
  // sourcePath is the doc's path relative to the idea root (e.g. "idea.md",
  // "repos/foo/README.md"). Used to resolve relative hrefs.
  sourcePath?: string
  // repos is the idea's linked-repo list. Used to (a) decide whether a
  // relative non-md href is a repo file, and (b) pick the originURL/branch
  // for translating to a GitHub blob URL.
  repos?: store.RepoLink[]
  // onSelectFile is called when a click resolves to an in-app .md file.
  // Hosts (IdeaDetail) wire this to their selectedFile state.
  onSelectFile?: (path: string) => void
}

// MarkdownViewer renders markdown read-only via Milkdown / Crepe — the same
// editor stack used for review (so styling and rendering match) but without
// the CriticMarkup plugin, since regular markdown files shouldn't interpret
// `{++...++}` etc. as marks.
//
// Frontmatter (YAML between `---` fences) is stripped before rendering. For
// idea.md the frontmatter holds metadata (status, tags) that's already shown
// elsewhere in the UI; for other files it's typically absent.
//
// Link clicks inside the rendered doc are intercepted via DOM event delegation
// and routed through the shared classifier in lib/links — see that file's
// header for the decision tree.
export default function MarkdownViewer({ content, className, sourcePath, repos, onSelectFile }: Props) {
  const containerRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    const root = containerRef.current
    if (!root) return

    const { body } = splitFrontmatter(content)
    const crepe = new Crepe({ root, defaultValue: body })

    let cancelled = false
    crepe.create().then(() => {
      if (!cancelled) crepe.setReadonly(true)
    }).catch(() => { /* init failed — leave the empty container */ })

    return () => {
      cancelled = true
      void crepe.destroy()
    }
  }, [content])

  // Link interception: one delegated handler on the container. Runs in the
  // capture phase so we beat any default navigation Crepe might attach.
  useEffect(() => {
    const root = containerRef.current
    if (!root) return
    const onClick = (e: MouseEvent) => {
      const a = (e.target as HTMLElement | null)?.closest('a')
      if (!a) return
      const href = a.getAttribute('href') || ''
      const result = classify(href, sourcePath || '', repos || [])
      switch (result.kind) {
        case 'external':
          e.preventDefault()
          openExternal(result.url)
          return
        case 'in-app-md':
          if (onSelectFile) {
            e.preventDefault()
            onSelectFile(result.path)
          }
          return
        case 'repo-file': {
          const link = (repos || []).find((r) => r.name === result.repo)
          if (link?.originUrl) {
            e.preventDefault()
            const url = gitHubBlobURL(link.originUrl, link.branch || '', result.relPath)
            if (url) openExternal(url)
          }
          return
        }
        case 'anchor':
          // Let the browser scroll to the in-doc anchor.
          return
        case 'unhandled':
          // Drop the click — refuses javascript:, file:, etc., and any href
          // that doesn't resolve to a known target.
          e.preventDefault()
          return
      }
    }
    root.addEventListener('click', onClick, true)
    return () => { root.removeEventListener('click', onClick, true) }
  }, [sourcePath, repos, onSelectFile])

  return <div ref={containerRef} className={className} />
}
