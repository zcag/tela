import { useBrand } from '../lib/queries/host-context'
import { cn } from '../lib/utils'

// A discreet product credit shown whenever the host wears another brand (an
// org's custom domain, or a rebranded instance); a tela-branded host renders
// nothing. Always on: there is deliberately no setting to hide it.
// Links to the tela product site, so every white-labeled org domain quietly
// credits (and markets) tela. Deliberately small/muted: present, not loud.
const TELA_HOME = 'https://telawiki.com'

export function PoweredByTela({ className }: { className?: string }) {
  if (!useBrand().custom) return null
  return (
    <a
      href={TELA_HOME}
      target="_blank"
      rel="noopener noreferrer"
      className={cn(
        'inline-block text-[length:var(--text-xs)] text-[var(--text-muted)]',
        'no-underline hover:text-[var(--text-primary)] transition-colors',
        className,
      )}
    >
      Powered by tela
    </a>
  )
}
