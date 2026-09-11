import { Tooltip, TooltipContent, TooltipTrigger } from '../ui/tooltip'

// A small amber dot signalling that something's index is out of date (a space
// with stale pages, or a single stale/unindexed page). Non-intrusive: just a
// dot with a tooltip; render it only when there's actually something stale.
//
// It owns its own row layout (both sidebars drop it in bare) and its own hover
// behaviour: it RECEDES on row hover rather than hiding. It used to be
// `group-hover:hidden` to clear room for the ⋯ cluster, which made the tooltip
// unreachable by mouse — hovering the row is the only way to get a pointer onto
// the dot, and that same hover deleted it. Users saw an unexplained amber dot
// with no way to ask what it was (feedback: "what the hell is that yellow
// dot?"). Receding keeps the hover cluster visually dominant while leaving the
// answer one point away; the `.group:hover &:hover` rule outranks the dimming by
// specificity, so it doesn't depend on utility order.
export function StalenessDot({
  label,
  side = 'right',
}: {
  label: string
  side?: 'top' | 'right' | 'bottom' | 'left'
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          tabIndex={0}
          aria-label={label}
          className="shrink-0 inline-flex items-center justify-center cursor-default outline-none rounded-[var(--radius-xs)] focus-visible:ring-2 focus-visible:ring-[var(--accent)] transition-opacity duration-[var(--duration-fast)] [.group:hover_&]:opacity-40 [.group:hover_&:hover]:opacity-100"
        >
          <span
            aria-hidden
            className="block w-[var(--space-2)] h-[var(--space-2)] rounded-full"
            style={{ backgroundColor: 'var(--warning)' }}
          />
        </span>
      </TooltipTrigger>
      <TooltipContent side={side}>{label}</TooltipContent>
    </Tooltip>
  )
}
