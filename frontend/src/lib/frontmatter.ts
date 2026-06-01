// YAML frontmatter delimiter pattern. Matches `---\n...\n---\n` at the start
// of a document — the contents between the delimiters are non-markdown
// (YAML data) and Crepe / CommonMark would mangle them on round-trip
// (`---` parses as a thematic break).
const FRONTMATTER_RE = /^(---\r?\n[\s\S]*?\r?\n---\r?\n)([\s\S]*)$/

export function splitFrontmatter(content: string): {
  frontmatter: string
  body: string
} {
  const match = content.match(FRONTMATTER_RE)
  if (match) return { frontmatter: match[1], body: match[2] }
  return { frontmatter: '', body: content }
}
