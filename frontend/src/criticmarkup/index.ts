// CriticMarkup support for Milkdown — schema marks/nodes + remark
// round-trip + manual keymap + always-on suggesting plugin.
// Pass `criticMarkupPlugins` to `editor.use(...)`.
//
// Suggesting plugins come AFTER the manual keymap so their Backspace /
// Delete bindings take precedence — toolbar-driven mark commands and the
// suggesting auto-marks share the same schema marks, so they compose
// without conflict.

import { criticMarkupRemark } from './remark'
import {
  insertionSchema,
  deletionSchema,
  substitutionSchema,
  commentSchema,
} from './schema'
import { criticMarkupKeymap } from './keymap'
import {
  criticMarkupSuggestingPlugin,
  criticMarkupSuggestingKeymap,
} from './suggesting'

export const criticMarkupPlugins = [
  criticMarkupRemark,
  insertionSchema,
  deletionSchema,
  substitutionSchema,
  commentSchema,
  criticMarkupKeymap,
  criticMarkupSuggestingPlugin,
  criticMarkupSuggestingKeymap,
].flat()

export {
  criticMarkupRemark,
  insertionSchema,
  deletionSchema,
  substitutionSchema,
  commentSchema,
  criticMarkupKeymap,
  criticMarkupSuggestingPlugin,
  criticMarkupSuggestingKeymap,
}
export {
  toggleInsertionCommand,
  toggleDeletionCommand,
  insertCommentCommand,
} from './commands'
export { collapseToSubstitutions } from './collapse'
export { OPEN_COMMENT_MODAL_EVENT } from './keymap'
