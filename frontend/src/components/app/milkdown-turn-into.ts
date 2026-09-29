import { commandsCtx } from '@milkdown/kit/core'
import type { Ctx } from '@milkdown/ctx'
import type { EditorState } from '@milkdown/kit/prose/state'
import {
  createCodeBlockCommand,
  turnIntoTextCommand,
  wrapInBlockquoteCommand,
  wrapInBulletListCommand,
  wrapInHeadingCommand,
  wrapInOrderedListCommand,
} from '@milkdown/kit/preset/commonmark'
import {
  Code,
  Heading1,
  Heading2,
  Heading3,
  List,
  ListOrdered,
  Quote,
  Type,
  type LucideIcon,
} from 'lucide-react'

// Block-type transforms, shared by the slash menu (as the insert for the
// matching manifest ids), the block handle's "Turn into" menu and the bubble
// toolbar's block-type dropdown. Each is a commonmark command acting on the
// selection's textblock, so everything round-trips to plain markdown. Ids are
// the blocks-manifest ids ('text' has no manifest entry: it isn't insertable).

export interface TurnIntoOption {
  id: string
  label: string
  icon: LucideIcon
  run: (ctx: Ctx) => void
}

const cmd = (ctx: Ctx) => ctx.get(commandsCtx)

export const TURN_INTO: TurnIntoOption[] = [
  { id: 'text', label: 'Text', icon: Type, run: (ctx) => cmd(ctx).call(turnIntoTextCommand.key) },
  { id: 'h1', label: 'Heading 1', icon: Heading1, run: (ctx) => cmd(ctx).call(wrapInHeadingCommand.key, 1) },
  { id: 'h2', label: 'Heading 2', icon: Heading2, run: (ctx) => cmd(ctx).call(wrapInHeadingCommand.key, 2) },
  { id: 'h3', label: 'Heading 3', icon: Heading3, run: (ctx) => cmd(ctx).call(wrapInHeadingCommand.key, 3) },
  { id: 'bullet-list', label: 'Bulleted list', icon: List, run: (ctx) => cmd(ctx).call(wrapInBulletListCommand.key) },
  { id: 'ordered-list', label: 'Numbered list', icon: ListOrdered, run: (ctx) => cmd(ctx).call(wrapInOrderedListCommand.key) },
  { id: 'quote', label: 'Quote', icon: Quote, run: (ctx) => cmd(ctx).call(wrapInBlockquoteCommand.key) },
  { id: 'code', label: 'Code block', icon: Code, run: (ctx) => cmd(ctx).call(createCodeBlockCommand.key) },
]

const byId = (id: string) => TURN_INTO.find((o) => o.id === id) ?? null

// Which option describes the block the selection starts in, or null when it's
// none of them exactly (a callout, table cell, to-do item, h4+ …) so callers
// don't mislabel it.
export function currentTurnInto(state: EditorState): TurnIntoOption | null {
  const { $from } = state.selection
  const block = $from.parent
  if (block.type.name === 'code_block') return byId('code')
  if (block.type.name === 'heading') return byId(`h${block.attrs.level as number}`)
  if (block.type.name !== 'paragraph') return null
  const wrap = $from.depth > 1 ? $from.node($from.depth - 1) : null
  if (!wrap) return byId('text')
  if (wrap.type.name === 'blockquote') return byId('quote')
  if (wrap.type.name === 'list_item' && wrap.attrs.checked == null) {
    const list = $from.node($from.depth - 2).type.name
    if (list === 'bullet_list') return byId('bullet-list')
    if (list === 'ordered_list') return byId('ordered-list')
  }
  return null
}
