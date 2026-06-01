// CriticMarkup schema — ProseMirror marks for insertion/deletion and inline
// atom nodes for substitution/comment. Pairs with ./remark.ts which handles
// the markdown <-> mdast translation.
//
// Why marks for insertion/deletion: they wrap arbitrary phrasing content
// (text, emphasis, strong) and merge naturally — adjacent marks of the same
// type combine via Milkdown's serializer, which is exactly what we need for
// Phase 2 suggesting-mode consolidation.
//
// Why atom nodes for substitution/comment: their internal structure isn't
// ordinary prose. Substitution carries two payloads (old/new) in attrs,
// comment carries a single string note. Treating them as atoms also keeps
// other formatting marks from getting applied (you can't bold a comment).

import { $markSchema, $nodeSchema } from '@milkdown/utils'
import type { MarkdownNode } from '@milkdown/transformer'

// Insertion mark: green-underline styling, applies to phrasing content.
// Round-trips as `{++text++}` via the remark stringify handler.
export const insertionSchema = $markSchema('criticInsertion', () => ({
  parseDOM: [{ tag: 'ins.cm-insertion' }, { tag: 'span.cm-insertion' }],
  toDOM: () => ['ins', { class: 'cm-insertion' }, 0],
  parseMarkdown: {
    match: (node) => node.type === 'criticInsertion',
    runner: (state, node, markType) => {
      state.openMark(markType)
      state.next(node.children ?? [])
      state.closeMark(markType)
    },
  },
  toMarkdown: {
    match: (mark) => mark.type.name === 'criticInsertion',
    runner: (state, mark) => {
      state.withMark(mark, 'criticInsertion')
    },
  },
}))

// Deletion mark: red-strikethrough styling.
// Round-trips as `{--text--}`.
export const deletionSchema = $markSchema('criticDeletion', () => ({
  parseDOM: [{ tag: 'del.cm-deletion' }, { tag: 'span.cm-deletion' }],
  toDOM: () => ['del', { class: 'cm-deletion' }, 0],
  parseMarkdown: {
    match: (node) => node.type === 'criticDeletion',
    runner: (state, node, markType) => {
      state.openMark(markType)
      state.next(node.children ?? [])
      state.closeMark(markType)
    },
  },
  toMarkdown: {
    match: (mark) => mark.type.name === 'criticDeletion',
    runner: (state, mark) => {
      state.withMark(mark, 'criticDeletion')
    },
  },
}))

// Substitution atom: stores old + new text in attrs. Rendered as
// `{~~old~>new~~}` so the human can read the swap inline.
// Round-trips as `{~~old~>new~~}`.
export const substitutionSchema = $nodeSchema('criticSubstitution', () => ({
  group: 'inline',
  inline: true,
  atom: true,
  attrs: {
    oldText: { default: '' },
    newText: { default: '' },
  },
  parseDOM: [
    {
      tag: 'span.cm-substitution',
      getAttrs: (dom) => {
        const el = dom as HTMLElement
        return {
          oldText: el.getAttribute('data-old') ?? '',
          newText: el.getAttribute('data-new') ?? '',
        }
      },
    },
  ],
  toDOM: (node) => [
    'span',
    {
      class: 'cm-substitution',
      'data-old': String(node.attrs.oldText ?? ''),
      'data-new': String(node.attrs.newText ?? ''),
    },
    `{~~${String(node.attrs.oldText ?? '')}~>${String(node.attrs.newText ?? '')}~~}`,
  ],
  parseMarkdown: {
    match: (node) => node.type === 'criticSubstitution',
    runner: (state, node, nodeType) => {
      const n = node as MarkdownNode & { oldText?: string; newText?: string }
      state.addNode(nodeType, {
        oldText: n.oldText ?? '',
        newText: n.newText ?? '',
      })
    },
  },
  toMarkdown: {
    match: (node) => node.type.name === 'criticSubstitution',
    runner: (state, node) => {
      state.addNode('criticSubstitution', undefined, undefined, {
        oldText: String(node.attrs.oldText ?? ''),
        newText: String(node.attrs.newText ?? ''),
      })
    },
  },
}))

// Comment atom: stores the human's note text. Rendered as `{>>note<<}` so
// the literal CriticMarkup form is visible in the editor.
// Round-trips as `{>>note<<}`.
export const commentSchema = $nodeSchema('criticComment', () => ({
  group: 'inline',
  inline: true,
  atom: true,
  attrs: {
    text: { default: '' },
  },
  parseDOM: [
    {
      tag: 'span.cm-comment',
      getAttrs: (dom) => ({
        text: (dom as HTMLElement).getAttribute('data-text') ?? '',
      }),
    },
  ],
  toDOM: (node) => [
    'span',
    {
      class: 'cm-comment',
      'data-text': String(node.attrs.text ?? ''),
    },
    `{>>${String(node.attrs.text ?? '')}<<}`,
  ],
  parseMarkdown: {
    match: (node) => node.type === 'criticComment',
    runner: (state, node, nodeType) => {
      const n = node as MarkdownNode & { value?: string }
      state.addNode(nodeType, { text: n.value ?? '' })
    },
  },
  toMarkdown: {
    match: (node) => node.type.name === 'criticComment',
    runner: (state, node) => {
      state.addNode(
        'criticComment',
        undefined,
        String(node.attrs.text ?? ''),
      )
    },
  },
}))
