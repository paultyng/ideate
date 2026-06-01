// Editor commands for the CriticMarkup marks/nodes.
//
// These funnel through ProseMirror's standard command pattern
// (`(state, dispatch) => boolean`) so they compose with the toolbar,
// keymap, and Phase 2's appendTransaction filter.

import { toggleMark } from '@milkdown/prose/commands'
import type { Command, EditorState, Transaction } from '@milkdown/prose/state'
import type { MarkType, NodeType } from '@milkdown/prose/model'

// toggleInsertion / toggleDeletion: standard ProseMirror toggleMark — wraps
// the selection in the mark, or removes the mark if the selection is fully
// inside one. No-op without a selection (toggleMark falls back to "stored
// mark" mode in that case, which we don't expose since Phase 2 will turn
// every keystroke into an insertion automatically).
export function toggleInsertionCommand(markType: MarkType): Command {
  return toggleMark(markType)
}

export function toggleDeletionCommand(markType: MarkType): Command {
  return toggleMark(markType)
}

// insertCommentCommand: inserts a `criticComment` atom node carrying `text`
// at the cursor. The text comes from a UI affordance (in-React modal); this
// command no longer prompts — Wails' WKWebView silently no-ops window.prompt
// on macOS, so we render our own input.
export function insertCommentCommand(nodeType: NodeType, text: string): Command {
  return (state: EditorState, dispatch?: (tr: Transaction) => void): boolean => {
    if (!text) return false
    if (!dispatch) return true
    const node = nodeType.create({ text })
    dispatch(state.tr.replaceSelectionWith(node, false))
    return true
  }
}
