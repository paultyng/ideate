// Always-on suggesting mode for the WYSIWYG editor.
//
// Two cooperating ProseMirror plugins:
//
// 1. `criticMarkupSuggestingPlugin` (appendTransaction) keeps the
//    `insertion` mark in `storedMarks` whenever the cursor sits in
//    unmarked text. The next typed character (or pasted slice) inherits
//    the mark automatically, so the user "types in green" without any
//    extra wiring around `handleTextInput`.
//
// 2. `criticMarkupSuggestingKeymap` rebinds `Backspace` / `Delete` /
//    `Mod-Backspace` / `Mod-Delete` to convert removals of *normal* text
//    into deletion marks (text stays in the doc, struck through). When
//    the targeted range is already insertion-marked, removal proceeds
//    normally — that's "shrinking a fresh insertion" rather than
//    proposing an edit to underlying prose.
//
// Source mode is unaffected (the textarea bypasses Crepe entirely), so
// the human can always drop into source to type raw text or hand-edit
// CriticMarkup. WYSIWYG mode is *only* suggesting mode — there is no
// toggle.

import {
  Plugin,
  TextSelection,
  type Command,
  type EditorState,
  type Transaction,
} from '@milkdown/prose/state'
import type { MarkType, Node as ProseNode } from '@milkdown/prose/model'
import { $prose, $useKeymap } from '@milkdown/utils'

import { insertionSchema, deletionSchema } from './schema'

const META_KEY = 'criticmarkup/suggesting'

// rangeFullyHasMark returns true when every text position in [from, to)
// carries `markType`. Used to detect "delete inside an insertion" — those
// removals are actual deletes, not deletion-mark wrapping.
function rangeFullyHasMark(
  doc: ProseNode,
  from: number,
  to: number,
  markType: MarkType,
): boolean {
  if (from >= to) return false
  let fully = true
  doc.nodesBetween(from, to, (node, pos) => {
    if (!node.isText) return true
    const start = Math.max(from, pos)
    const end = Math.min(to, pos + node.nodeSize)
    if (start >= end) return false
    if (!markType.isInSet(node.marks)) {
      fully = false
      return false
    }
    return false
  })
  return fully
}

// wrapAsDeletion converts a remove-this-range intent into either an
// actual delete (if the range was a fresh insertion) or a deletion-mark
// addition (preserving the underlying prose). Cursor lands at `from` so
// repeated Backspace keeps eating leftward.
function wrapAsDeletion(
  state: EditorState,
  dispatch: ((tr: Transaction) => void) | undefined,
  from: number,
  to: number,
  insertionType: MarkType,
  deletionType: MarkType,
): boolean {
  if (from >= to) return false

  // If the range crosses a block boundary (e.g. Backspace at the start of
  // a paragraph), defer to ProseMirror's default Backspace/Delete chain
  // (joinBackward etc). Suggesting mode only manages text-level removals.
  const $from = state.doc.resolve(from)
  const $to = state.doc.resolve(to)
  if ($from.parent !== $to.parent || !$from.parent.isTextblock) return false

  if (rangeFullyHasMark(state.doc, from, to, insertionType)) {
    if (dispatch) {
      const tr = state.tr.delete(from, to).setMeta(META_KEY, true)
      dispatch(tr)
    }
    return true
  }

  if (dispatch) {
    const tr = state.tr.addMark(from, to, deletionType.create())
    // setSelection must reference the post-step doc (`tr.doc`), even
    // though addMark doesn't change positions — ProseMirror enforces
    // that the selection's doc matches the transaction's current doc.
    tr.setSelection(TextSelection.create(tr.doc, from))
    tr.setMeta(META_KEY, true)
    dispatch(tr)
  }
  return true
}

// Backspace command: extends the selection one char leftward when empty,
// then routes through `wrapAsDeletion`.
function backspaceCommand(insertionType: MarkType, deletionType: MarkType): Command {
  return (state, dispatch) => {
    const { from, empty } = state.selection
    let rangeFrom: number
    let rangeTo: number
    if (empty) {
      if (from === 0) return false
      rangeFrom = Math.max(0, from - 1)
      rangeTo = from
    } else {
      rangeFrom = from
      rangeTo = state.selection.to
    }
    return wrapAsDeletion(state, dispatch, rangeFrom, rangeTo, insertionType, deletionType)
  }
}

function deleteForwardCommand(insertionType: MarkType, deletionType: MarkType): Command {
  return (state, dispatch) => {
    const { from, to, empty } = state.selection
    let rangeFrom: number
    let rangeTo: number
    if (empty) {
      if (to === state.doc.content.size) return false
      rangeFrom = to
      rangeTo = Math.min(state.doc.content.size, to + 1)
    } else {
      rangeFrom = from
      rangeTo = to
    }
    return wrapAsDeletion(state, dispatch, rangeFrom, rangeTo, insertionType, deletionType)
  }
}

// criticMarkupSuggestingPlugin: ensures the next-typed character picks up
// the insertion mark by injecting it into storedMarks. We re-check on
// every transaction because selection movement clears storedMarks.
//
// We deliberately *don't* try to reapply this on transactions we
// generated ourselves (META_KEY) — those are deletion-mark wraps and
// should leave storedMarks alone (the cursor is now adjacent to a
// deletion mark, but the next typed char should be a fresh insertion,
// which the next user transaction handles).
// criticMarkupSuggestingPlugin guarantees that every direct text input —
// typing a char, pasting plain text, IME composition end — produces
// insertion-marked content, even when the selection is currently
// deletion-marked.
//
// Why `handleTextInput` instead of `appendTransaction` + storedMarks:
// when a `<del>` element wraps the selection, the browser inserts new
// chars *inside* the del element. ProseMirror's DOM observer then reads
// the mutation as `<del>X</del>` and assigns the deletion mark to X.
// storedMarks isn't consulted on DOM-driven steps — it's only used by
// `tr.insertText`, `tr.replaceSelectionWith`, etc. So we intercept the
// text input upstream of the DOM mutation, dispatch our own transaction
// with explicit marks, and return true to suppress the default.
//
// We also clear adjacent deletion marks at the boundary by passing only
// the insertion mark (filtering out any deletion mark from the cursor
// context). Adjacent insertions merge naturally via ProseMirror's
// inclusive flag and Milkdown's `#maybeMergeChildren` serializer step.
export const criticMarkupSuggestingPlugin = $prose((ctx) => {
  const insertionType = insertionSchema.type(ctx)
  const deletionType = deletionSchema.type(ctx)
  return new Plugin({
    props: {
      handleTextInput(view, from, to, text) {
        const { schema, doc } = view.state
        // Inherit other formatting marks (bold, emphasis) from the
        // cursor position so typing inside e.g. a `**bold**` still
        // produces bold-and-insertion text. Filter out any deletion
        // mark so the new char doesn't wear it.
        const $from = doc.resolve(from)
        const inherited = $from.marks().filter((m) => m.type !== deletionType)
        const marks = insertionType.create().addToSet(inherited)
        const newText = schema.text(text, marks)

        const tr = view.state.tr
        const hasSelection = from < to
        switch (true) {
          case !hasSelection:
            // Empty selection — plain typing, insertion-marked.
            tr.replaceWith(from, to, newText)
            tr.setSelection(TextSelection.create(tr.doc, from + text.length))
            break

          case rangeFullyHasMark(doc, from, to, insertionType):
            // Overwriting a fresh insertion — drop the old chars and
            // emit fresh insertion-marked text. No substitution: this
            // is "fixing your typo", not "proposing a replacement".
            tr.replaceWith(from, to, newText)
            tr.setSelection(TextSelection.create(tr.doc, from + text.length))
            break

          case rangeFullyHasMark(doc, from, to, deletionType):
            // Already deletion-marked selection (toolbar Delete,
            // Backspace-then-type) — preserve the deletion and insert
            // new text after it. ./collapse.ts folds adjacent
            // `{--old--}{++new++}` into `{~~old~>new~~}` on serialize.
            tr.insert(to, newText)
            tr.setSelection(TextSelection.create(tr.doc, to + text.length))
            break

          default:
            // Plain (or mixed) selection — same substitution flow,
            // but we have to mark the original chars as deletion
            // first. Then collapse handles the rest. This is what
            // the user expects from "select text, type to replace".
            tr.addMark(from, to, deletionType.create())
            tr.insert(to, newText)
            tr.setSelection(TextSelection.create(tr.doc, to + text.length))
            break
        }
        view.dispatch(tr)
        return true
      },
    },
    appendTransaction: (trs, _oldState, newState) => {
      // Backstop for paste / drag-drop / programmatic insertions that
      // don't go through handleTextInput. Sets storedMarks for the next
      // text-emitting transaction so a subsequent type-after-paste also
      // wears the insertion mark.
      if (!trs.some((tr) => tr.docChanged || tr.selectionSet)) return null
      const { selection, storedMarks } = newState
      if (!selection.empty) return null
      const $pos = selection.$from
      if (insertionType.isInSet($pos.marks())) return null
      const inherited = (storedMarks ?? $pos.marks()).filter((m) => m.type !== deletionType)
      if (insertionType.isInSet(inherited)) return null
      const next = insertionType.create().addToSet(inherited)
      return newState.tr.setStoredMarks(next)
    },
  })
})

// criticMarkupSuggestingKeymap registers Backspace / Delete through
// Milkdown's KeymapManager with priority 100 so it runs *before*
// `baseKeymap`'s default Backspace chain (priority 50). When our command
// returns false (e.g. cursor at a block boundary, deferring to
// joinTextblockBackward), KeymapManager chains to the next handler — so
// list-item / heading specific bindings still get their turn.
const SUGGESTING_PRIORITY = 100

export const criticMarkupSuggestingKeymap = $useKeymap(
  'criticMarkupSuggesting',
  {
    SuggestingBackspace: {
      shortcuts: 'Backspace',
      priority: SUGGESTING_PRIORITY,
      command: (ctx) =>
        backspaceCommand(insertionSchema.type(ctx), deletionSchema.type(ctx)),
    },
    SuggestingDelete: {
      shortcuts: 'Delete',
      priority: SUGGESTING_PRIORITY,
      command: (ctx) =>
        deleteForwardCommand(insertionSchema.type(ctx), deletionSchema.type(ctx)),
    },
    SuggestingModBackspace: {
      shortcuts: 'Mod-Backspace',
      priority: SUGGESTING_PRIORITY,
      command: (ctx) =>
        backspaceCommand(insertionSchema.type(ctx), deletionSchema.type(ctx)),
    },
    SuggestingModDelete: {
      shortcuts: 'Mod-Delete',
      priority: SUGGESTING_PRIORITY,
      command: (ctx) =>
        deleteForwardCommand(insertionSchema.type(ctx), deletionSchema.type(ctx)),
    },
  },
)
