// Link classification + handlers shared by MarkdownViewer, MarkdownReview,
// resource sidebar items, and the terminal addon.
//
// Decision tree (markdown contexts):
//   absolute http/https/mailto → openExternal  (Wails BrowserOpenURL)
//   "#anchor"                  → caller decides (scroll, etc.)
//   relative *.md / *.markdown → onSelectFile(resolvedPath)
//   relative non-md, in a repo → translate to GitHub blob URL → openExternal
//   anything else              → no-op
//
// Scheme allow-list is enforced inside openExternal so a `javascript:` href in
// rendered markdown can't reach BrowserOpenURL.

import { BrowserOpenURL } from '../wailsjs/runtime/runtime'
import type { store } from '../wailsjs/go/models'

export type Classified =
  | { kind: 'external'; url: string }
  | { kind: 'anchor'; hash: string }
  | { kind: 'in-app-md'; path: string }
  | { kind: 'repo-file'; repo: string; relPath: string }
  | { kind: 'unhandled'; href: string }

const ALLOWED_SCHEMES = new Set(['http:', 'https:', 'mailto:'])

export function openExternal(url: string): boolean {
  try {
    const u = new URL(url)
    if (!ALLOWED_SCHEMES.has(u.protocol)) return false
    BrowserOpenURL(u.toString())
    return true
  } catch {
    return false
  }
}

// classify returns what kind of navigation the given href needs from the
// perspective of a document at sourcePath (relative to idea root). repos is
// the idea's linked repo list — used to spot repo-file hrefs that should
// route through the GitHub URL translator.
export function classify(
  href: string,
  sourcePath: string,
  repos: store.RepoLink[],
): Classified {
  if (!href) return { kind: 'unhandled', href }

  if (href.startsWith('#')) {
    return { kind: 'anchor', hash: href.slice(1) }
  }

  // Absolute URL — try parsing without a base. URL throws for relative.
  try {
    const abs = new URL(href)
    if (ALLOWED_SCHEMES.has(abs.protocol)) {
      return { kind: 'external', url: abs.toString() }
    }
    return { kind: 'unhandled', href }
  } catch {
    // fall through — relative
  }

  const resolved = resolveRelative(sourcePath, href)
  if (!resolved) return { kind: 'unhandled', href }

  if (/\.(md|markdown)$/i.test(resolved.replace(/[#?].*$/, ''))) {
    return { kind: 'in-app-md', path: resolved }
  }

  // Repo file? Match on `repos/<name>/...` prefix.
  const repoMatch = /^repos\/([^/]+)\/(.*)$/.exec(resolved)
  if (repoMatch && repos.some((r) => r.name === repoMatch[1])) {
    return { kind: 'repo-file', repo: repoMatch[1], relPath: repoMatch[2] }
  }

  return { kind: 'unhandled', href }
}

// resolveRelative joins a relative href onto the directory of sourcePath
// (POSIX semantics, normalizing . and ..). Returns "" if the href escapes
// the idea root or otherwise can't be resolved.
export function resolveRelative(sourcePath: string, href: string): string {
  const dir = sourcePath.includes('/') ? sourcePath.slice(0, sourcePath.lastIndexOf('/')) : ''
  const base = dir ? dir + '/' : ''
  const combined = base + href
  const segments: string[] = []
  for (const seg of combined.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') {
      if (segments.length === 0) return '' // escapes root
      segments.pop()
      continue
    }
    segments.push(seg)
  }
  return segments.join('/')
}

// gitHubBlobURL translates a (originURL, branch, path) tuple into a browser-
// openable URL. github.com origins get the full /blob/<branch>/<path> form.
// Other hosts get a best-effort `https://host/org/repo` (no path) — better
// than handing BrowserOpenURL an SSH URL that the browser can't open.
export function gitHubBlobURL(originURL: string, branch: string, relPath: string): string {
  const parsed = parseGitOrigin(originURL)
  if (!parsed) return ''
  const cleanBranch = branch || parsed.defaultBranch || 'HEAD'
  if (parsed.host === 'github.com') {
    const seg = relPath ? `/blob/${cleanBranch}/${relPath}` : `/tree/${cleanBranch}`
    return `https://github.com/${parsed.org}/${parsed.repo}${seg}`
  }
  // Non-GitHub: drop the path; let the user navigate from the repo home.
  return `https://${parsed.host}/${parsed.org}/${parsed.repo}`
}

interface ParsedOrigin {
  host: string
  org: string
  repo: string
  defaultBranch?: string
}

// Recognized shapes:
//   git@github.com:org/repo.git
//   https://github.com/org/repo.git
//   ssh://git@github.com/org/repo
function parseGitOrigin(origin: string): ParsedOrigin | null {
  if (!origin) return null
  const stripGit = (s: string) => s.replace(/\.git$/, '')

  // SCP-like: git@host:org/repo
  const scp = /^[\w-]+@([^:]+):(.+)$/.exec(origin)
  if (scp) {
    const path = stripGit(scp[2])
    const [org, ...rest] = path.split('/')
    if (!org || rest.length === 0) return null
    return { host: scp[1], org, repo: rest.join('/') }
  }

  // ssh:// or https://
  try {
    const u = new URL(origin)
    if (u.protocol !== 'https:' && u.protocol !== 'http:' && u.protocol !== 'ssh:') {
      return null
    }
    const path = stripGit(u.pathname.replace(/^\//, ''))
    const [org, ...rest] = path.split('/')
    if (!org || rest.length === 0) return null
    return { host: u.host.replace(/:.*$/, ''), org, repo: rest.join('/') }
  } catch {
    return null
  }
}
