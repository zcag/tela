import { forwardRef } from 'react'
import * as PopoverPrimitive from '@radix-ui/react-popover'
import { cn } from '../../lib/utils'

// Owned Popover primitive (Radix) — a click-triggered floating panel for richer
// content than a tooltip (links, prose, small lists) that a DropdownMenu would
// model wrongly (it's not a menu of actions). Styling lives in the `tela-popover-*`
// component-layer classes, mirroring the dropdown's tokens-only approach.

// eslint-disable-next-line react-refresh/only-export-components
export const Popover = PopoverPrimitive.Root
// eslint-disable-next-line react-refresh/only-export-components
export const PopoverTrigger = PopoverPrimitive.Trigger
// eslint-disable-next-line react-refresh/only-export-components
export const PopoverAnchor = PopoverPrimitive.Anchor

export const PopoverContent = forwardRef<
  React.ElementRef<typeof PopoverPrimitive.Content>,
  React.ComponentPropsWithoutRef<typeof PopoverPrimitive.Content> & {
    // Portal to `body` by default. Pass `portal={false}` when the popover lives
    // inside a modal Dialog: a portaled sibling gets `aria-hidden`/inert by the
    // dialog's focus scope, so its inputs can't take focus — rendering inline
    // keeps it inside the dialog subtree and focusable.
    portal?: boolean
  }
>(function PopoverContent(
  { className, sideOffset = 6, align = 'start', portal = true, ...props },
  ref,
) {
  const content = (
    <PopoverPrimitive.Content
      ref={ref}
      sideOffset={sideOffset}
      align={align}
      className={cn('tela-popover-content', className)}
      {...props}
    />
  )
  return portal ? (
    <PopoverPrimitive.Portal>{content}</PopoverPrimitive.Portal>
  ) : (
    content
  )
})
