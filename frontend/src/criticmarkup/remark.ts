// CriticMarkup remark plugin.
//
// Two responsibilities, both wired through one unified plugin so Milkdown's
// markdown <-> ProseMirror round-trip understands CriticMarkup:
//
// 1. Parser: walk text mdast nodes, split CriticMarkup syntax out into
//    custom mdast node types (`criticInsertion`, `criticDeletion`,
//    `criticSubstitution`, `criticComment`). Code spans/blocks are
//    naturally untouched because their text lives in `value` on a
//    non-`text` node — `unist-util-visit('text', ...)` skips them.
//
// 2. Serializer: register `mdast-util-to-markdown` handlers for the four
//    custom node types so they stringify back to their CriticMarkup form.
//
// Schema specs in ./schema.ts produce these mdast node shapes from the
// ProseMirror doc on save and consume them on load.

import { $remark } from '@milkdown/utils'
import type { Node } from '@milkdown/transformer'
import { visit, SKIP } from 'unist-util-visit'

interface CriticBuilder {
  regex: RegExp
  build: (m: RegExpExecArray) => Node
}

// Patterns are tried left-to-right at each `{` position; first match wins.
// Substitution must precede deletion since `{--...--}` looks like a
// truncated `{~~...~>...~~}` only after we've ruled out the `~>` middle.
const PATTERNS: CriticBuilder[] = [
  {
    regex: /\{\+\+([\s\S]*?)\+\+\}/g,
    build: (m) =>
      ({
        type: 'criticInsertion',
        children: [{ type: 'text', value: m[1] }],
      }) as unknown as Node,
  },
  {
    regex: /\{~~([\s\S]*?)~>([\s\S]*?)~~\}/g,
    build: (m) =>
      ({
        type: 'criticSubstitution',
        oldText: m[1],
        newText: m[2],
      }) as unknown as Node,
  },
  {
    regex: /\{--([\s\S]*?)--\}/g,
    build: (m) =>
      ({
        type: 'criticDeletion',
        children: [{ type: 'text', value: m[1] }],
      }) as unknown as Node,
  },
  {
    regex: /\{>>([\s\S]*?)<<\}/g,
    build: (m) =>
      ({ type: 'criticComment', value: m[1] }) as unknown as Node,
  },
]

// tokenize splits a raw text string into a sequence of `text` mdast nodes
// and CriticMarkup nodes. Returns null if no CriticMarkup found (caller
// keeps the original text node in place).
function tokenize(text: string): Node[] | null {
  const result: Node[] = []
  let cursor = 0
  let lastEnd = 0
  let foundAny = false

  while (cursor < text.length) {
    if (text[cursor] !== '{') {
      cursor++
      continue
    }
    let matched: { end: number; node: Node } | null = null
    for (const { regex, build } of PATTERNS) {
      regex.lastIndex = cursor
      const m = regex.exec(text)
      if (m && m.index === cursor) {
        matched = { end: cursor + m[0].length, node: build(m) }
        break
      }
    }
    if (matched) {
      foundAny = true
      if (cursor > lastEnd) {
        result.push({
          type: 'text',
          value: text.slice(lastEnd, cursor),
        } as unknown as Node)
      }
      result.push(matched.node)
      lastEnd = matched.end
      cursor = matched.end
    } else {
      cursor++
    }
  }

  if (!foundAny) return null
  if (lastEnd < text.length) {
    result.push({
      type: 'text',
      value: text.slice(lastEnd),
    } as unknown as Node)
  }
  return result
}

// transform walks the mdast tree once, replacing text nodes that contain
// CriticMarkup syntax with split sequences. SKIP + new index keeps visit
// from re-traversing the inserted nodes.
function transform(tree: Node): void {
  visit(
    tree as never,
    'text',
    (
      node: Node & { value?: unknown },
      index: number | undefined,
      parent: (Node & { children?: Node[] }) | undefined,
    ) => {
      if (typeof node.value !== 'string') return
      if (!parent || index === undefined || !parent.children) return
      const replacement = tokenize(node.value)
      if (!replacement) return
      parent.children.splice(index, 1, ...replacement)
      return [SKIP, index + replacement.length]
    },
  )
}

// Serializer handlers for `mdast-util-to-markdown`. Insertion / deletion
// wrap their phrasing children (so nested marks like emphasis still
// render). Substitution / comment are leaves with their content in the
// node itself.
//
// Type imports from `mdast-util-to-markdown` would be useful but the
// package isn't in our direct deps; the `any` casts are scoped tightly
// here.
type ToMdHandler = (
  node: Record<string, unknown>,
  parent: unknown,
  state: {
    enter: (name: string) => () => void
    containerPhrasing: (
      node: Record<string, unknown>,
      info: Record<string, unknown>,
    ) => string
  },
  info: Record<string, unknown>,
) => string

const handlers: Record<string, ToMdHandler> = {
  criticInsertion: (node, _parent, state, info) => {
    const exit = state.enter('criticInsertion')
    const inner = state.containerPhrasing(node, {
      ...info,
      before: '++}',
      after: '{++',
    })
    exit()
    return `{++${inner}++}`
  },
  criticDeletion: (node, _parent, state, info) => {
    const exit = state.enter('criticDeletion')
    const inner = state.containerPhrasing(node, {
      ...info,
      before: '--}',
      after: '{--',
    })
    exit()
    return `{--${inner}--}`
  },
  criticSubstitution: (node) =>
    `{~~${String(node.oldText ?? '')}~>${String(node.newText ?? '')}~~}`,
  criticComment: (node) => `{>>${String(node.value ?? '')}<<}`,
}

// criticMarkupRemark wires the parser transform AND the stringify handlers
// into the unified pipeline. `this.data().toMarkdownExtensions` is the
// official channel for adding `mdast-util-to-markdown` handlers from a
// remark plugin.
// Cast through `unknown` because remark's typed `this` does not expose
// `toMarkdownExtensions` directly — it's a pass-through bag for
// `mdast-util-to-markdown` extensions and remark-stringify reads it via
// a different generic. The runtime shape is correct.
export const criticMarkupRemark = $remark('criticMarkupRemark', () =>
  function plugin(this: unknown) {
    const data = (this as { data: () => Record<string, unknown> }).data()
    const list = (data.toMarkdownExtensions ?? (data.toMarkdownExtensions = [])) as Array<{ handlers: typeof handlers }>
    list.push({ handlers })
    return (tree) => transform(tree as Node)
  },
)
