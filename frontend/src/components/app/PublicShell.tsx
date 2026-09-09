import { ThemeSwitcher } from '../ThemeSwitcher'
import { BrandLogo } from '../BrandLogo'
import { useHostContext, useTelaHomeHref } from '../../lib/queries/host-context'
import { cn } from '../../lib/utils'
import { Card, CardDescription, CardHeader, CardTitle } from '../ui/card'
import { useNoindex } from '../../lib/useHeadMeta'

// The chrome around every logged-out surface (public reader, handle homes, the
// file page): brand + theme switcher over a full-height column. On an org custom
// domain BrandLogo white-labels to the org and the brand links to the org root;
// on the canonical host it's the tela wordmark → marketing landing.
//
// `align` picks how <main> lays its child out: 'center' for a card-sized panel
// (the default every public.tsx surface uses), 'stretch' for content that wants
// the full column (the file page's preview).
export function PublicShell({
  children,
  align = 'center',
}: {
  children: React.ReactNode
  align?: 'center' | 'stretch'
}) {
  const telaHome = useTelaHomeHref()
  const org = useHostContext().data?.org ?? null
  const brandHref = org ? '/' : telaHome
  const brandLabel = org ? `${org.name} home` : 'tela home'
  return (
    <div className="min-h-dvh flex flex-col bg-[var(--surface-1)] text-[var(--text-primary)]">
      <header className="flex items-center justify-between px-[var(--space-6)] py-[var(--space-3)] border-b border-[var(--border-subtle)] shrink-0">
        <h1 className="m-0 text-[length:var(--text-lg)] leading-[var(--leading-tight)] font-[family-name:var(--font-sans)]">
          <a
            href={brandHref}
            aria-label={brandLabel}
            className="inline-flex items-center rounded-[var(--radius-xs)] no-underline transition-opacity duration-[var(--duration-fast)] hover:opacity-70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
          >
            <BrandLogo size={20} />
          </a>
        </h1>
        <ThemeSwitcher />
      </header>
      <main
        className={cn(
          'flex-1 flex p-[var(--space-7)]',
          align === 'center'
            ? 'items-center justify-center'
            : 'flex-col min-h-0',
        )}
      >
        {children}
      </main>
    </div>
  )
}

// The neutral miss for a public surface: a page/space/file that isn't there, or
// isn't public. Never a bounce to /login — a logged-out visitor stays put.
export function PublicUnavailable({
  message = 'This page is not publicly available.',
}: {
  message?: string
}) {
  useNoindex()
  return (
    <PublicShell>
      <Card className="w-full max-w-[24rem]">
        <CardHeader>
          <CardTitle className="text-[length:var(--text-2xl)]">Not available</CardTitle>
          <CardDescription>{message}</CardDescription>
        </CardHeader>
      </Card>
    </PublicShell>
  )
}
