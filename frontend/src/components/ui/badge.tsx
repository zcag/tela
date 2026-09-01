import { forwardRef } from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '../../lib/utils'

const badgeVariants = cva(
  [
    'inline-flex items-center gap-[var(--space-1)]',
    'font-[family-name:var(--font-sans)]',
    'leading-[var(--leading-tight)]',
    'rounded-[var(--radius-sm)]',
    'border',
    'whitespace-nowrap',
    'text-[length:var(--text-xs)]',
    'px-[var(--space-2)] py-[1px]',
  ],
  {
    variants: {
      variant: {
        // Subdued chip — same surface tone as the rest of the row, just
        // outlined. Use for neutral labels like 'This device' or a
        // space-name disambiguator.
        muted: [
          'bg-[var(--surface-1)] text-[var(--text-muted)]',
          'border-[var(--border-subtle)]',
        ],
        // Brand-tinted chip — uses accent for the foreground/border with a
        // soft surface fill so it stays legible without shouting. Use when
        // the chip needs to read as "this is the active / current one".
        accent: [
          'bg-[var(--surface-2)] text-[var(--accent)]',
          'border-[var(--accent)]',
        ],
        // Danger-tinted chip — same shape as accent but on the --danger token.
        // Use to flag something that needs attention (e.g. a 'bug' report).
        danger: [
          'bg-[color-mix(in_srgb,var(--danger)_10%,var(--surface-1))] text-[var(--danger)]',
          'border-[color-mix(in_srgb,var(--danger)_45%,transparent)]',
        ],
        // Filled — the heaviest weight, for a chip that outranks its neighbours
        // (a ROLE like instance-admin, not a capability or a state). Use
        // sparingly: everything filled is nothing filled.
        solid: ['bg-[var(--accent)] text-[var(--accent-fg)]', 'border-transparent'],
        // Positive / warning / negative on the semantic accent scale — for a
        // value that sits somewhere on a good→bad range (a lifecycle label),
        // where --accent would only say "interactive".
        positive: [
          'bg-[var(--accent-positive-soft)] text-[var(--accent-positive-fg)]',
          'border-transparent',
        ],
        warning: [
          'bg-[var(--accent-warning-soft)] text-[var(--accent-warning-fg)]',
          'border-transparent',
        ],
        negative: [
          'bg-[var(--accent-negative-soft)] text-[var(--accent-negative-fg)]',
          'border-transparent',
        ],
        // Borderless muted text — for STATE that should be legible but never
        // compete ('You', 'Deactivated').
        ghost: ['bg-transparent text-[var(--text-muted)]', 'border-transparent px-0'],
      },
    },
    defaultVariants: {
      variant: 'muted',
    },
  },
)

export interface BadgeProps
  extends React.HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {}

export const Badge = forwardRef<HTMLSpanElement, BadgeProps>(function Badge(
  { className, variant, ...props },
  ref,
) {
  return (
    <span
      ref={ref}
      className={cn(badgeVariants({ variant }), className)}
      {...props}
    />
  )
})
