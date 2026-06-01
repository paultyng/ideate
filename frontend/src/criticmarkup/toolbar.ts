// Crepe toolbar integration — adds CriticMarkup buttons to the floating
// contextual toolbar that pops up on text selection.
//
// Only Delete is exposed here. The floating toolbar implies "act on the
// selected text", which fits Delete (toggle deletion mark on selection)
// but not Comment (at-cursor) or Insert (which becomes implicit in
// Phase 2's always-on suggesting mode and so doesn't need a toolbar
// affordance once that lands). Substitution is not currently surfaced —
// see the note at the top of ./schema.ts.

import type { Ctx } from '@milkdown/kit/ctx'
import { editorViewCtx } from '@milkdown/core'

import { deletionSchema } from './schema'
import { toggleDeletionCommand } from './commands'

const deleteIcon = `
<span title="Delete selected text (⌘⇧K)" style="display:inline-flex">
<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
  <line x1="6" y1="12" x2="18" y2="12"/>
  <line x1="5" y1="20" x2="19" y2="20" stroke-width="1.2"/>
</svg>
</span>`

interface ToolbarItem {
  active: (ctx: Ctx) => boolean
  icon: string
  onRun?: (ctx: Ctx) => void
}

interface ToolbarGroup {
  addItem: (
    key: string,
    item: Omit<ToolbarItem & { onRun?: (ctx: Ctx) => void }, 'key' | 'index'>,
  ) => ToolbarGroup
}

interface GroupBuilderShape {
  addGroup: (key: string, label: string) => ToolbarGroup
}

export function buildCriticMarkupToolbar(builder: GroupBuilderShape): void {
  const group = builder.addGroup('criticmarkup', 'CriticMarkup')
  group.addItem('deletion', {
    icon: deleteIcon,
    active: (ctx) => {
      const view = ctx.get(editorViewCtx)
      const { from, $from, to, empty } = view.state.selection
      const markType = deletionSchema.type(ctx)
      if (empty) return !!markType.isInSet(view.state.storedMarks ?? $from.marks())
      return view.state.doc.rangeHasMark(from, to, markType)
    },
    onRun: (ctx) => {
      const view = ctx.get(editorViewCtx)
      const cmd = toggleDeletionCommand(deletionSchema.type(ctx))
      cmd(view.state, view.dispatch)
      view.focus()
    },
  })
}
