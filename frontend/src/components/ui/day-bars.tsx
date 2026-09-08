import { useState } from 'react'
import { cn } from '../../lib/utils'

export interface DayBarsProps {
  // One count per bucket, oldest→newest.
  values: number[]
  // Bucket labels aligned to `values` ('YYYY-MM-DD' for daily series). Used in
  // the hover readout and the axis ends.
  labels?: string[]
  // Plot height in px. Bars scale to the series max within it.
  height?: number
  // Readout text for one bucket. Default: "<label> · <value>".
  format?: (value: number, label: string | undefined, index: number) => string
  ariaLabel?: string
  className?: string
}

// A discrete daily-count chart: one bar per bucket, hover (or tap) a day for its
// exact number. Deliberately NOT a Sparkline — a line implies a continuous
// quantity you read the shape of, while "how many people joined on the 4th" is a
// countable event per day, and the answer people want is the number itself.
// Color rides `currentColor` like Sparkline, so the caller sets the hue with a
// text-token class.
export function DayBars({
  values,
  labels,
  height = 44,
  format,
  ariaLabel,
  className,
}: DayBarsProps) {
  const [hover, setHover] = useState<number | null>(null)
  const max = Math.max(1, ...values)
  const n = values.length
  const read = (i: number) =>
    format ? format(values[i], labels?.[i], i) : `${labels?.[i] ?? `#${i + 1}`} · ${values[i]}`

  // The readout sits over the hovered column and is clamped at the ends, so a
  // day near either edge doesn't push the label out of the card.
  const pct = hover == null ? 0 : ((hover + 0.5) / n) * 100
  const shift = pct < 12 ? '0%' : pct > 88 ? '-100%' : '-50%'

  return (
    <div
      className={cn('flex flex-col gap-[var(--space-1)] w-full', className)}
      onPointerLeave={() => setHover(null)}
    >
      {/* Reserve the readout row rather than letting it push the chart down —
          a layout that jumps under the cursor is unreadable. */}
      <div className="relative w-full pt-[var(--space-5)]">
        <div className="flex w-full items-end gap-[1px]" style={{ height }} aria-hidden="true">
          {values.map((v, i) => (
            <div
              key={labels?.[i] ?? i}
              // The hit target is the whole COLUMN, not the drawn bar: a one-signup
              // day is a few pixels tall, and nobody can hover that on purpose.
              className="flex h-full flex-1 min-w-[2px] items-end"
              onPointerEnter={() => setHover(i)}
              // Tap works too — a coarse pointer has no hover to give.
              onPointerDown={() => setHover(i)}
            >
              <div
                // A zero day keeps a faint floor tick: an empty column and a missing
                // one must not look the same, and the row of ticks is what makes the
                // gaps between bars readable as days.
                className={cn(
                  'w-full rounded-t-[var(--radius-xs)] bg-current transition-opacity',
                  v > 0 ? 'opacity-80' : 'opacity-15',
                  hover === i ? 'opacity-100' : '',
                )}
                style={{ height: v > 0 ? `${Math.max(8, (v / max) * 100)}%` : '2px' }}
              />
            </div>
          ))}
        </div>
        {hover != null ? (
          <div
            className={cn(
              'pointer-events-none absolute top-0 z-10 whitespace-nowrap',
              'rounded-[var(--radius-sm)] border border-[var(--border-subtle)]',
              'bg-[var(--surface-2)] px-[var(--space-2)] py-[1px]',
              'text-[length:var(--text-xs)] tabular-nums text-[var(--text-primary)]',
            )}
            style={{ left: `${pct}%`, transform: `translateX(${shift})` }}
          >
            {read(hover)}
          </div>
        ) : null}
      </div>
      {labels && labels.length > 1 ? (
        <div className="flex justify-between text-[length:var(--text-xs)] text-[var(--text-muted)] tabular-nums">
          <span>{labels[0]}</span>
          <span>{labels[labels.length - 1]}</span>
        </div>
      ) : null}
      {/* The numbers themselves for anyone not using a pointer — the bars are
          decorative once every value is in the list. */}
      <ul className="sr-only" aria-label={ariaLabel}>
        {values.map((_, i) => (
          <li key={labels?.[i] ?? i}>{read(i)}</li>
        ))}
      </ul>
    </div>
  )
}
