import { Link, useParams, useRouterState } from '@tanstack/react-router'
import {
  BookOpen,
  FilePlus,
  FolderPlus,
  Home,
  Plus,
  Search,
  Share2,
  Sparkles,
  Wand2,
} from 'lucide-react'
import { DOCS } from '../../lib/docs'
import { SpaceTree } from './SpaceTree'
import { FavoritesSidebarSection } from './FavoritesSidebarSection'
import { UserMenu } from './UserMenu'
import { BrandLogo } from '../BrandLogo'
import { PoweredByTela } from '../PoweredByTela'
import { Button } from '../ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '../ui/dropdown-menu'
import { emitOpenNewPage } from '../../lib/newPageEvent'
import { emitOpenNewSpace } from '../../lib/newSpaceEvent'
import { emitOpenPalette } from '../../lib/paletteEvent'
import { IS_MAC } from '../../lib/useGlobalShortcut'
import { cn } from '../../lib/utils'

export function Sidebar({
  open = false,
  collapsed = false,
}: {
  open?: boolean
  collapsed?: boolean
}) {
  // Read both params loosely — sidebar lives in the root layout, so we may be
  // anywhere in the tree (index, space, or page route).
  const params = useParams({ strict: false }) as {
    spaceId?: number
    pageId?: number
  }
  const activeSpaceId = typeof params.spaceId === 'number' ? params.spaceId : null
  const activePageId = typeof params.pageId === 'number' ? params.pageId : null

  // Current path → which top-level nav row is active. The page tree highlights
  // its own active row (SpacePages); these top rows had no active state at all.
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const navActive = (path: string, exact = false) =>
    exact ? pathname === path : pathname === path || pathname.startsWith(path + '/')
  // Same active treatment as the page/space rows: accent veil + left accent bar
  // + accent label, so "where am I" reads identically across the whole sidebar.
  const activeRow =
    'bg-[var(--sidebar-item-active)] text-[var(--accent)] font-medium shadow-[inset_2px_0_0_0_var(--sidebar-item-active-bar)]'

  // 'c', not Ctrl/⌘-N: browsers claim Ctrl/⌘-N for a new window before the page
  // sees the keydown, so the modifier chord never reaches us. See lib/keys/bindings.
  const newPageShortcut = 'c'
  const searchShortcut = IS_MAC ? '⌘K' : 'Ctrl+K'

  return (
    <aside
      aria-label="Navigation"
      className={cn(
        'flex flex-col w-[var(--sidebar-width)] shrink-0 border-r border-[var(--border-subtle)] bg-[var(--surface-2)] overflow-hidden',
        // Mobile: a fixed slide-in drawer toggled by the header hamburger.
        // Desktop (md+): static, in-flow, always visible — unless collapsed.
        'fixed inset-y-0 left-0 z-50 transition-[transform,width] duration-200 ease-out md:static md:z-auto md:translate-x-0',
        open ? 'translate-x-0' : '-translate-x-full',
        // Desktop collapse: animate width to 0 (content clipped by overflow),
        // main content reflows to fill. Independent of the mobile drawer.
        collapsed && 'md:w-0 md:border-r-0',
      )}
    >
      <div className="px-[var(--space-3)] pt-[var(--space-4)] flex flex-col gap-[var(--space-1)]">
        <Link
          to="/"
          aria-label="tela — home"
          className="mb-[var(--space-2)] inline-flex items-center px-[var(--space-1)] no-underline transition-opacity duration-[var(--duration-fast)] hover:opacity-80"
        >
          <BrandLogo
            size={20}
            className="text-[length:var(--text-base)] font-medium leading-none tracking-[-0.01em]"
          />
        </Link>
        {/* Search reads as a field — the one input affordance up top. */}
        <Button
          variant="secondary"
          size="sm"
          className="w-full justify-start mb-[var(--space-1)]"
          onClick={() => emitOpenPalette('pages')}
          aria-label={`Search (${searchShortcut})`}
          title={`Search (${searchShortcut})`}
        >
          <Search width={14} height={14} className="text-[var(--text-muted)]" />
          <span className="flex-1 text-left text-[var(--text-muted)]">Search…</span>
          <kbd
            aria-hidden
            className="text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]"
          >
            {searchShortcut}
          </kbd>
        </Button>
        {/* Below the search field: one uniform row language. Ask is the landmark
            feature — distinguished ONLY by its accent sparkle icon; the label is
            normal text like every other row, so it never reads as "selected". */}
        <Button
          asChild
          variant="ghost"
          size="sm"
          className={cn('w-full justify-start', navActive('/ask') && activeRow)}
        >
          <Link to="/ask" aria-label="Ask your docs" title="Ask your docs">
            <Sparkles width={14} height={14} className="text-[var(--accent)]" />
            <span className="flex-1 text-left">Ask</span>
          </Link>
        </Button>
        {/* New page is one of the two most-used actions; the menu folds New
            space in beside it so the nav carries a single create affordance. */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="sm"
              className="w-full justify-start"
              aria-label="Create new"
              title="Create new"
            >
              <Plus width={14} height={14} />
              <span className="flex-1 text-left">New…</span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="min-w-[12rem]">
            <DropdownMenuItem onSelect={() => emitOpenNewPage()}>
              <FilePlus width={14} height={14} />
              <span className="flex-1">Page</span>
              <kbd
                aria-hidden
                className="text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]"
              >
                {newPageShortcut}
              </kbd>
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => emitOpenNewSpace()}>
              <FolderPlus width={14} height={14} />
              <span className="flex-1">Space</span>
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        <Button asChild variant="ghost" size="sm" className={cn('w-full justify-start', navActive('/', true) && activeRow)}>
          <Link to="/" aria-label="Home" title="Home">
            <Home width={14} height={14} />
            <span className="flex-1 text-left">Home</span>
          </Link>
        </Button>
        <Button asChild variant="ghost" size="sm" className={cn('w-full justify-start', navActive('/graph') && activeRow)}>
          <Link to="/graph" aria-label="Graph view" title="Graph view">
            <Share2 width={14} height={14} />
            <span className="flex-1 text-left">Graph</span>
          </Link>
        </Button>
        <Button asChild variant="ghost" size="sm" className={cn('w-full justify-start', navActive('/atlas') && activeRow)}>
          <Link to="/atlas" aria-label="Atlas" title="Atlas — generate docs from sources">
            <Wand2 width={14} height={14} />
            <span className="flex-1 text-left">Atlas</span>
          </Link>
        </Button>
        <Button asChild variant="ghost" size="sm" className="w-full justify-start">
          <a
            href={DOCS.home}
            target="_blank"
            rel="noopener"
            aria-label="Documentation"
            title="Documentation"
          >
            <BookOpen width={14} height={14} />
            <span className="flex-1 text-left">Docs</span>
          </a>
        </Button>
      </div>
      <FavoritesSidebarSection activePageId={activePageId} />
      <SpaceTree activeSpaceId={activeSpaceId} activePageId={activePageId} />
      <UserMenu />
      {/* Discreet product credit on org custom domains (renders nothing on the
          canonical host). */}
      <PoweredByTela className="px-[var(--space-3)] pb-[var(--space-2)] text-center" />
    </aside>
  )
}
