import { cn } from '../../lib/utils'

export interface DayBarsProps {
  // One count per bucket, oldest→newest.
  values: number[]
  // Bucket labels aligned to `values` ('YYYY-MM-DD' for daily series). Used in
  // the per-bar tooltip and the axis ends.
  labels?: string[]
  // Plot height in px. Bars scale to the series max within it.
  height?: number
  // Tooltip text for one bar. Default: "<label> · <value>".
  format?: (value: number, label: string | undefined, index: number) => string
  ariaLabel?: string
  className?: string
}

// A discrete daily-count chart: one bar per bucket, hoverable for its exact
// number. Deliberately NOT a Sparkline — a line implies a continuous quantity
// you read the shape of, while "how many people joined on the 4th" is a
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
  const max = Math.max(1, ...values)
  const tip = (v: number, i: number) =>
    format ? format(v, labels?.[i], i) : `${labels?.[i] ?? `#${i + 1}`} · ${v}`

  return (
    <div className={cn('flex flex-col gap-[var(--space-1)] w-full', className)}>
      <div
        className="flex w-full items-end gap-[1px]"
        style={{ height }}
        role="img"
        aria-label={ariaLabel}
      >
        {values.map((v, i) => (
          <div
            key={labels?.[i] ?? i}
            title={tip(v, i)}
            // A zero day keeps a faint floor tick: an empty column and a missing
            // one must not look the same, and the row of ticks is what makes the
            // gaps between bars readable as days.
            className={cn(
              'flex-1 min-w-[2px] rounded-t-[var(--radius-xs)] transition-opacity',
              v > 0 ? 'bg-current opacity-80 hover:opacity-100' : 'bg-current opacity-15',
            )}
            style={{ height: v > 0 ? `${Math.max(8, (v / max) * 100)}%` : '2px' }}
          />
        ))}
      </div>
      {labels && labels.length > 1 ? (
        <div className="flex justify-between text-[length:var(--text-xs)] text-[var(--text-muted)] tabular-nums">
          <span>{labels[0]}</span>
          <span>{labels[labels.length - 1]}</span>
        </div>
      ) : null}
    </div>
  )
}
