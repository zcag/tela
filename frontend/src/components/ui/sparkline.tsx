import { useId } from 'react'
import { cn } from '../../lib/utils'

export interface SparklineProps {
  values: number[]
  width?: number
  height?: number
  // Draw a soft area fill under the line. Stroke + fill both inherit
  // `currentColor`, so the caller sets the hue via a text-color token class.
  area?: boolean
  // Fixed [min, max] instead of per-series auto-scaling. REQUIRED whenever a
  // COLUMN of sparklines is meant to be read row against row: auto-scaling
  // normalizes every series to its own range, so "1 active day a week" and
  // "7 active days a week" draw the identical shape — actively misleading in a
  // column whose whole job is comparison. Pass the domain the data is really
  // bounded by and the set reads as small multiples.
  domain?: [number, number]
  // Mark the final point — a cheap "this is where they are now".
  showLast?: boolean
  // Faint rule along the domain floor, so a flat line reads as zero rather than
  // as missing data.
  baseline?: boolean
  className?: string
  ariaLabel?: string
}

// A tiny dependency-free SVG sparkline. Color comes from `currentColor` (set a
// text-[var(--…)] token on the wrapper); geometry scales to `domain` when given,
// else to the series' own range.
export function Sparkline({
  values,
  width = 120,
  height = 32,
  area = true,
  domain,
  showLast = false,
  baseline = false,
  className,
  ariaLabel,
}: SparklineProps) {
  const gradId = useId()
  const n = values.length
  if (n === 0) return <svg width={width} height={height} className={className} />

  const max = domain ? domain[1] : Math.max(...values)
  const min = domain ? domain[0] : Math.min(...values)
  const span = max - min || 1
  const pad = 1.5
  const stepX = n > 1 ? (width - pad * 2) / (n - 1) : 0
  const y = (v: number) => height - pad - ((v - min) / span) * (height - pad * 2)
  const x = (i: number) => pad + i * stepX

  const line = values.map((v, i) => `${x(i).toFixed(2)},${y(v).toFixed(2)}`).join(' ')
  const areaPath = `M ${x(0).toFixed(2)},${height} L ${line.split(' ').join(' L ')} L ${x(n - 1).toFixed(2)},${height} Z`

  return (
    <svg
      width="100%"
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      preserveAspectRatio="none"
      role="img"
      aria-label={ariaLabel}
      // Fill the container, never exceed it. `width` is only the viewBox
      // coordinate space; preserveAspectRatio="none" stretches x to the box and
      // vector-effect keeps the stroke crisp. (A fixed px width + overflow leaked
      // the line past narrow cards.)
      className={cn('block w-full max-w-full', className)}
    >
      {area ? (
        <>
          <defs>
            <linearGradient id={gradId} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="currentColor" stopOpacity="0.22" />
              <stop offset="100%" stopColor="currentColor" stopOpacity="0" />
            </linearGradient>
          </defs>
          <path d={areaPath} fill={`url(#${gradId})`} stroke="none" />
        </>
      ) : null}
      {baseline ? (
        <line
          x1={x(0)}
          y1={height - pad}
          x2={x(n - 1)}
          y2={height - pad}
          stroke="currentColor"
          strokeWidth={1}
          strokeOpacity={0.18}
          vectorEffect="non-scaling-stroke"
        />
      ) : null}
      <polyline
        points={line}
        fill="none"
        stroke="currentColor"
        strokeWidth={1.75}
        strokeLinejoin="round"
        strokeLinecap="round"
        vectorEffect="non-scaling-stroke"
      />
      {showLast ? (
        // preserveAspectRatio="none" stretches x, so a circle would render as an
        // ellipse — a tiny vertical bar is the shape that survives the scaling.
        <line
          x1={x(n - 1)}
          y1={y(values[n - 1]) - 1.6}
          x2={x(n - 1)}
          y2={y(values[n - 1]) + 1.6}
          stroke="currentColor"
          strokeWidth={3}
          strokeLinecap="round"
          vectorEffect="non-scaling-stroke"
        />
      ) : null}
    </svg>
  )
}
