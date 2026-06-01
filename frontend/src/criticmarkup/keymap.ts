// Keymap binding the CriticMarkup commands to Cmd/Ctrl-Shift shortcuts.
//
// Insertion and Deletion are pure editor commands. Comment dispatches a
// DOM event the React shell catches to open the comment modal — keeps the
// modal entirely in React without leaking ProseMirror state out, and
// avoids the editor commanding any React state directly.
//
// Substitution stays unbindable on purpose — it's awkward as a single
// keystroke (needs two payloads) and source mode is the escape hatch.

import { keymap } from '@milkdown/prose/keymap'
import { $prose } from '@milkdown/utils'

import { insertionSchema, deletionSchema } from './schema'
import { toggleInsertionCommand, toggleDeletionCommand } from './commands'

// Event name the React shell listens for to open the comment modal.
export const OPEN_COMMENT_MODAL_EVENT = 'criticmarkup:open-comment-modal'

export const criticMarkupKeymap = $prose((ctx) =>
  keymap({
    'Mod-Shift-i': toggleInsertionCommand(insertionSchema.type(ctx)),
    'Mod-Shift-k': toggleDeletionCommand(deletionSchema.type(ctx)),
    'Mod-Shift-n': () => {
      document.dispatchEvent(new CustomEvent(OPEN_COMMENT_MODAL_EVENT))
      return true
    },
  }),
)
