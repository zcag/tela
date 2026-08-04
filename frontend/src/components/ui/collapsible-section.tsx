import { useState, type ReactNode } from 'react'
import { ChevronRight } from 'lucide-react'
import { cn } from '../../lib/utils'

interface CollapsibleSectionProps {
  /** Header label shown in the always-visible summary row. */
  title: ReactNode
  /** Start expanded. Default collapsed. Ignored when `persistKey` has a stored value. */
  defaultOpen?: boolean
  /** localStorage key — remembers the user's open/closed choice across pages. */
  persistKey?: string
  /** Mount children only after the section is first opened (for heavy/lazy content). */
  mountOnOpen?: boolean
  children: ReactNode
  className?: string
  /** Extra classes for the body wrapper. */
  bodyClassName?: string
}

function readStored(key: string | undefined, fallback: boolean): boolean {
  if (!key || typeof window === 'undefined') return fallback
  const v = window.localStorage.getItem(key)
  return v === null ? fallback : v === '1'
}

// A disclosure section: a token-styled native <details> whose body collapses
// behind a clickable header. Used for the below-page Connections / backlinks
// rails so they don't add visual noise to every page. `mountOnOpen` keeps heavy
// children (the d3 graph) out of the DOM until the user actually expands.
export function CollapsibleSection({
  title,
  defaultOpen = false,
  persistKey,
  mountOnOpen = false,
  children,
  className,
  bodyClassName,
}: CollapsibleSectionProps) {
  const [open, setOpen] = useState(() => readStored(persistKey, defaultOpen))
  const [hasOpened, setHasOpened] = useState(open)

  const handleToggle = (e: React.SyntheticEvent<HTMLDetailsElement>) => {
    const next = e.currentTarget.open
    // `toggle` is NOT a click event: React sets the `open` attribute after
    // creating the <details>, which the browser counts as opening it, so a
    // section persisted open re-fires this on every mount. Only a real user
    // toggle can outrun the state — on mount the two already agree. Without
    // this, `persistKey` turned "I left Connections open" into "every page I
    // open scrolls itself to the bottom", on every page, until I closed it.
    const userToggled = next !== open
    setOpen(next)
    if (next) setHasOpened(true)
    if (persistKey) window.localStorage.setItem(persistKey, next ? '1' : '0')
    if (next && userToggled) {
      // These sections sit at the bottom of the page, so the body usually
      // expands below the fold. Scroll the section fully into view once the
      // body has mounted (double rAF: mountOnOpen children render next frame).
      const el = e.currentTarget
      requestAnimationFrame(() =>
        requestAnimationFrame(() =>
          el.scrollIntoView({ block: 'nearest', behavior: 'smooth' }),
        ),
      )
    }
  }

  return (
    <details
      open={open}
      onToggle={handleToggle}
      className={cn('pt-[var(--space-4)] border-t border-[var(--border-subtle)]', className)}
    >
      <summary
        className={cn(
          'flex items-center gap-[var(--space-2)] cursor-pointer select-none',
          'list-none [&::-webkit-details-marker]:hidden',
          'text-[length:var(--text-xs)] uppercase tracking-wider',
          'text-[var(--text-muted)] hover:text-[var(--text-primary)]',
          'font-[family-name:var(--font-sans)]',
          'rounded-[var(--radius-sm)] focus-visible:outline-none',
          'focus-visible:ring-2 focus-visible:ring-[var(--accent)]',
        )}
      >
        <ChevronRight
          aria-hidden
          width={14}
          height={14}
          className={cn('shrink-0 transition-transform', open && 'rotate-90')}
        />
        {title}
      </summary>
      <div className={cn('pt-[var(--space-3)]', bodyClassName)}>
        {mountOnOpen ? (hasOpened ? children : null) : children}
      </div>
    </details>
  )
}
