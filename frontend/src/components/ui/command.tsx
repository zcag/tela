import {
  forwardRef,
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import * as DialogPrimitive from '@radix-ui/react-dialog'
import {
  Command as CmdkRoot,
  CommandInput as CmdkInput,
  CommandList as CmdkList,
  CommandItem as CmdkItem,
  CommandEmpty as CmdkEmpty,
  CommandGroup as CmdkGroup,
  CommandSeparator as CmdkSeparator,
} from 'cmdk'
import { Search } from 'lucide-react'
import { cn } from '../../lib/utils'
import { IS_MAC, useGlobalShortcut } from '../../lib/useGlobalShortcut'

// Exported item shape — referenced by every consumer of this primitive.
// `subtitle` accepts a ReactNode so search hits can render a multi-token
// snippet with native <mark> highlights; string subtitles render inline next
// to the title (command rows), ReactNode subtitles trigger the stacked row
// layout (title-row on top, snippet underneath).
export interface CommandItem {
  id: string
  title: string
  subtitle?: ReactNode
  breadcrumb?: string
  icon?: ReactNode
  keywords?: string[]
  onSelect: () => void
  // Set true for items that manage their own close behaviour (e.g., commands
  // that switch the palette into help mode or open a sub-picker). Default —
  // the modal palette closes after the item's onSelect fires.
  keepPaletteOpen?: boolean
}

// Inline sub-picker descriptor — used by CommandPalette to render a picker
// surface inside the same modal instead of the regular CommandShell. Mirrors
// CommandInlinePickerProps but typed as data for registry-driven commands.
export interface CommandSubPicker {
  label: string
  placeholder: string
  emptyMessage?: string
  items: CommandItem[]
}

export type CommandMode = 'pages' | 'commands' | 'help' | 'mentions' | 'tags'

const PREFIX_TO_MODE: Record<string, CommandMode> = {
  '>': 'commands',
  '?': 'help',
  '@': 'mentions',
  '#': 'tags',
}

const MODE_LABEL: Record<CommandMode, string> = {
  pages: 'Pages',
  commands: 'Commands',
  help: 'Help',
  mentions: 'Mentions',
  tags: 'Tags',
}

const MODE_PLACEHOLDER: Record<CommandMode, string> = {
  pages: 'Search pages…',
  commands: 'Run a command…',
  help: 'Keyboard shortcuts',
  mentions: 'Mention a user…',
  tags: 'Find by tag…',
}

function prefixForMode(mode: CommandMode): string {
  if (mode === 'pages') return ''
  const entry = Object.entries(PREFIX_TO_MODE).find(([, m]) => m === mode)
  return entry ? entry[0] : ''
}

function detectMode(
  search: string,
): { mode: CommandMode; query: string; prefixActive: boolean } {
  const first = search[0]
  if (first && PREFIX_TO_MODE[first]) {
    return {
      mode: PREFIX_TO_MODE[first],
      query: search.slice(1),
      prefixActive: true,
    }
  }
  return { mode: 'pages', query: search, prefixActive: false }
}

const MOD_LABEL = IS_MAC ? '⌘' : 'Ctrl'
const SHIFT_LABEL = IS_MAC ? '⇧' : 'Shift'

// Always-visible prefix reminder rendered in the modal palette footer. Order
// mirrors PREFIX_TO_MODE; "type to search pages" follows as the default-mode
// hint. Kept here (not in CommandShell) because the footer stays visible across
// every mode, including sub-pickers — it's a chrome element of the palette.
const FOOTER_PREFIXES: Array<{ key: string; label: string }> = [
  { key: '>', label: 'Commands' },
  { key: '?', label: 'Help' },
  { key: '@', label: 'Mentions' },
  { key: '#', label: 'Tags' },
]

function CommandPaletteFooter() {
  return (
    <div className="tela-command-footer">
      {FOOTER_PREFIXES.map(({ key, label }) => (
        <span key={key} className="tela-command-footer-chip">
          <kbd className="tela-command-kbd">{key}</kbd>
          <span>{label}</span>
        </span>
      ))}
      <span className="tela-command-footer-hint">type to search pages</span>
    </div>
  )
}

const KEYBOARD_HELP: Array<{ keys: string[]; description: string }> = [
  { description: 'Open command palette', keys: [MOD_LABEL, 'K'] },
  { description: 'Open commands', keys: [MOD_LABEL, SHIFT_LABEL, 'P'] },
  { description: 'Create new page', keys: [MOD_LABEL, 'N'] },
  { description: 'Navigate items', keys: ['↑', '↓'] },
  { description: 'Select item', keys: ['↵'] },
  { description: 'Close or step back', keys: ['Esc'] },
  { description: 'Switch to commands', keys: ['>'] },
  { description: 'Show this help', keys: ['?'] },
  { description: 'Mention a user (coming soon)', keys: ['@'] },
  { description: 'Find by tag (coming soon)', keys: ['#'] },
]

function ModeBadge({ mode }: { mode: CommandMode }) {
  return <span className="tela-command-mode-badge">{MODE_LABEL[mode]}</span>
}

function isStackedSubtitle(subtitle: CommandItem['subtitle']): boolean {
  return subtitle != null && typeof subtitle !== 'string'
}

function CommandRowContent({
  icon,
  title,
  subtitle,
  breadcrumb,
}: Pick<CommandItem, 'icon' | 'title' | 'subtitle' | 'breadcrumb'>) {
  if (isStackedSubtitle(subtitle)) {
    return (
      <>
        {icon ? <span className="tela-command-item-icon">{icon}</span> : null}
        <span className="tela-command-item-stack">
          <span className="tela-command-item-stack-head">
            <span className="tela-command-item-title">{title}</span>
            {breadcrumb ? (
              <span className="tela-command-item-breadcrumb">{breadcrumb}</span>
            ) : null}
          </span>
          <span className="tela-command-item-snippet">{subtitle}</span>
        </span>
      </>
    )
  }
  return (
    <>
      {icon ? <span className="tela-command-item-icon">{icon}</span> : null}
      <span className="tela-command-item-title">{title}</span>
      {subtitle ? (
        <span className="tela-command-item-subtitle">{subtitle}</span>
      ) : null}
      {breadcrumb ? (
        <span className="tela-command-item-breadcrumb">{breadcrumb}</span>
      ) : null}
    </>
  )
}

function commandItemClass(item: CommandItem): string {
  return cn(
    'tela-command-item',
    isStackedSubtitle(item.subtitle) && 'tela-command-item--stacked',
  )
}

function HelpModeBody() {
  return (
    <div className="tela-command-help" role="list" aria-label="Keyboard shortcuts">
      {KEYBOARD_HELP.map(({ keys, description }, i) => (
        <div key={i} className="tela-command-help-row" role="listitem">
          <span className="tela-command-help-desc">{description}</span>
          <div className="tela-command-help-keys">
            {keys.map((k, j) => (
              <kbd key={j} className="tela-command-kbd">
                {k}
              </kbd>
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}

// Pages-mode items can be supplied flat (one unlabelled list) OR pre-grouped
// when the host wants headers — e.g. tier-1 "Titles" vs tier-2 "All pages" in
// M5.1. The grouped form preserves host-authored ordering inside each group.
export interface CommandItemGroup {
  label: string
  items: CommandItem[]
}
export type PagesItems = CommandItem[] | CommandItemGroup[]

function isGroupedPagesItems(p: PagesItems | undefined): p is CommandItemGroup[] {
  return Array.isArray(p) && p.length > 0 && 'items' in (p[0] as object)
}

interface CommandShellProps {
  search: string
  onSearchChange: (next: string) => void
  pagesItems?: PagesItems
  commandsItems?: CommandItem[]
  mentionsItems?: CommandItem[]
  tagsItems?: CommandItem[]
  pagesPlaceholder?: string
  emptyMessage?: string
  // Pages-mode-only empty message. Falls back to `emptyMessage` when absent.
  // Separate from `emptyMessage` because pages-mode messaging depends on host
  // state (recently-viewed vs no-results vs in-flight search), and shouldn't
  // leak into the other modes' "No results." default.
  pagesEmptyMessage?: string
  // When provided, the host owns hit ordering for pages mode — every query
  // change is published here (debounced/queried server-side, etc.) and cmdk's
  // own filter is bypassed so server-ranked results render in order. Fires
  // with the empty string whenever the user has switched into a prefixed mode
  // (`>`, `?`, `@`, `#`) so the host doesn't keep searching after the user
  // moves on.
  onPagesQueryChange?: (q: string) => void
}

function CommandShell({
  search,
  onSearchChange,
  pagesItems,
  commandsItems,
  mentionsItems,
  tagsItems,
  pagesPlaceholder,
  emptyMessage,
  pagesEmptyMessage,
  onPagesQueryChange,
}: CommandShellProps) {
  const { mode, query, prefixActive } = detectMode(search)

  // Pages mode supports grouped rendering (M5.1 tier-1 "Titles" / tier-2 "All
  // pages"). Other modes render as a single flat list — fold their props into
  // the same group shape so the renderer below has one path.
  const groups = useMemo<CommandItemGroup[]>(() => {
    if (mode === 'pages') {
      if (!pagesItems) return []
      if (isGroupedPagesItems(pagesItems)) {
        return pagesItems.filter((g) => g.items.length > 0)
      }
      return pagesItems.length > 0 ? [{ label: '', items: pagesItems }] : []
    }
    const flat =
      mode === 'commands'
        ? commandsItems
        : mode === 'mentions'
          ? mentionsItems
          : mode === 'tags'
            ? tagsItems
            : undefined
    if (!flat || flat.length === 0) return []
    return [{ label: '', items: flat }]
  }, [mode, pagesItems, commandsItems, mentionsItems, tagsItems])

  const itemCount = groups.reduce((n, g) => n + g.items.length, 0)

  // Publish the active pages-mode query to the host so it can drive a
  // server-side search. Fire empty string in any non-pages mode so the host
  // stops searching the moment the user switches prefixes.
  useEffect(() => {
    if (!onPagesQueryChange) return
    onPagesQueryChange(mode === 'pages' ? query : '')
  }, [mode, query, onPagesQueryChange])

  const placeholder =
    mode === 'pages' && pagesPlaceholder
      ? pagesPlaceholder
      : MODE_PLACEHOLDER[mode]

  const effectiveEmpty =
    mode === 'pages' && pagesEmptyMessage
      ? pagesEmptyMessage
      : (emptyMessage ?? 'No results.')

  // cmdk's built-in fuzzy filter beats us in commands/mentions/tags/help, but
  // for pages-mode-with-host the host has authoritative BM25 ranking and we
  // must render in that order.
  const filterEnabled =
    mode !== 'help' && !(mode === 'pages' && onPagesQueryChange != null)

  return (
    <CmdkRoot
      label={`Command palette — ${MODE_LABEL[mode]}`}
      shouldFilter={filterEnabled}
      className="tela-command-root"
    >
      <div className="tela-command-input-row">
        {prefixActive ? (
          <ModeBadge mode={mode} />
        ) : (
          <Search
            aria-hidden
            width={16}
            height={16}
            className="tela-command-input-icon"
          />
        )}
        <CmdkInput
          value={query}
          onValueChange={(next) => {
            onSearchChange(prefixActive ? search[0] + next : next)
          }}
          onKeyDown={(e) => {
            // Backspace at the empty position 0 of a prefixed mode clears the
            // prefix and returns to pages mode.
            if (e.key === 'Backspace' && prefixActive && query === '') {
              e.preventDefault()
              onSearchChange('')
              return
            }
            // Esc steps back through state before closing: prefix → query → close.
            // stopPropagation keeps Radix Dialog's outer Esc-to-close handler
            // from also firing this round; the next Esc (with nothing left to
            // clear) bubbles up and closes.
            if (e.key === 'Escape') {
              if (prefixActive) {
                e.stopPropagation()
                onSearchChange('')
                return
              }
              if (query.length > 0) {
                e.stopPropagation()
                onSearchChange('')
                return
              }
            }
          }}
          placeholder={placeholder}
          className="tela-command-input"
          autoFocus
        />
      </div>
      <CmdkList className="tela-command-list">
        {mode === 'help' ? (
          <HelpModeBody />
        ) : (
          <>
            <CmdkEmpty className="tela-command-empty">
              {effectiveEmpty}
            </CmdkEmpty>
            {itemCount > 0 &&
              groups.map((group, gi) => {
                const body = group.items.map((item) => (
                  <CmdkItem
                    key={item.id}
                    value={item.id}
                    keywords={[item.title, ...(item.keywords ?? [])]}
                    onSelect={item.onSelect}
                    className={commandItemClass(item)}
                  >
                    <CommandRowContent
                      icon={item.icon}
                      title={item.title}
                      subtitle={item.subtitle}
                      breadcrumb={item.breadcrumb}
                    />
                  </CmdkItem>
                ))
                if (group.label) {
                  return (
                    <CmdkGroup
                      key={`g-${gi}-${group.label}`}
                      heading={group.label}
                      className="tela-command-group"
                    >
                      {body}
                    </CmdkGroup>
                  )
                }
                // Unlabelled group renders inline without a heading; wrap so
                // arrow-key navigation still treats it as one logical block.
                return (
                  <CmdkGroup key={`g-${gi}`} className="tela-command-group">
                    {body}
                  </CmdkGroup>
                )
              })}
          </>
        )}
      </CmdkList>
    </CmdkRoot>
  )
}

export interface CommandPaletteProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  initialMode?: CommandMode
  pagesItems?: PagesItems
  commandsItems?: CommandItem[]
  mentionsItems?: CommandItem[]
  tagsItems?: CommandItem[]
  pagesPlaceholder?: string
  emptyMessage?: string
  pagesEmptyMessage?: string
  // Programmatic search push from outside. When `value` changes, the palette
  // overwrites its internal search — lets a command (e.g., "Show keyboard
  // shortcuts") switch the open palette into help mode without re-opening.
  searchRequest?: { value: string; nonce: number }
  // When non-null, replaces CommandShell with an inline picker inside the
  // same modal. Selecting an item closes the palette.
  subPicker?: CommandSubPicker | null
  // Threads through to CommandShell. See CommandShellProps for semantics.
  onPagesQueryChange?: (q: string) => void
}

// Modal (overlay + portal) variant. Drives the global search/command palette.
export function CommandPalette({
  open,
  onOpenChange,
  initialMode,
  pagesItems,
  commandsItems,
  mentionsItems,
  tagsItems,
  pagesPlaceholder,
  emptyMessage,
  pagesEmptyMessage,
  searchRequest,
  subPicker,
  onPagesQueryChange,
}: CommandPaletteProps) {
  const [search, setSearch] = useState('')

  // Seed the search with the prefix that matches initialMode each time the
  // palette opens. Clearing on close avoids leaking the previous query into
  // the next open.
  useEffect(() => {
    if (open) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional seed on open
      setSearch(prefixForMode(initialMode ?? 'pages'))
    } else {
      setSearch('')
    }
  }, [open, initialMode])

  // External search push (e.g., a command switching into help mode). Nonce
  // forces the effect to re-fire even when the same value is requested twice.
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- external search push is an imperative request
    if (searchRequest) setSearch(searchRequest.value)
  }, [searchRequest])

  const wrapItem = useCallback(
    (item: CommandItem): CommandItem =>
      item.keepPaletteOpen
        ? item
        : {
            ...item,
            onSelect: () => {
              item.onSelect()
              onOpenChange(false)
            },
          },
    [onOpenChange],
  )

  const closeAfter = useCallback(
    (items?: CommandItem[]): CommandItem[] | undefined => items?.map(wrapItem),
    [wrapItem],
  )

  // Pages-mode supports the grouped form (M5.1 tier-1/tier-2). Preserve the
  // group shape while wrapping every item's onSelect with the close-on-select
  // behaviour the modal expects.
  const closeAfterPages = useCallback(
    (items?: PagesItems): PagesItems | undefined => {
      if (!items) return undefined
      if (isGroupedPagesItems(items)) {
        return items.map((g) => ({ label: g.label, items: g.items.map(wrapItem) }))
      }
      return items.map(wrapItem)
    },
    [wrapItem],
  )

  // Sub-picker items always close the palette after selection — distinct from
  // top-level items that may opt out via keepPaletteOpen.
  const subPickerItems = useMemo<CommandItem[] | undefined>(() => {
    if (!subPicker) return undefined
    return subPicker.items.map((item) => ({
      ...item,
      keepPaletteOpen: false,
      onSelect: () => {
        item.onSelect()
        onOpenChange(false)
      },
    }))
  }, [subPicker, onOpenChange])

  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="tela-dialog-overlay" />
        <DialogPrimitive.Content
          className="tela-command-content"
          aria-label="Command palette"
        >
          <DialogPrimitive.Title className="tela-sr-only">
            {subPicker ? subPicker.label : 'Command palette'}
          </DialogPrimitive.Title>
          <DialogPrimitive.Description className="tela-sr-only">
            {subPicker
              ? subPicker.placeholder
              : 'Type to search pages, run commands, or get help.'}
          </DialogPrimitive.Description>
          {subPicker && subPickerItems ? (
            <CommandInlinePicker
              items={subPickerItems}
              placeholder={subPicker.placeholder}
              label={subPicker.label}
              emptyMessage={subPicker.emptyMessage}
              className="tela-command-inline--in-modal"
            />
          ) : (
            <CommandShell
              search={search}
              onSearchChange={setSearch}
              pagesItems={closeAfterPages(pagesItems)}
              commandsItems={closeAfter(commandsItems)}
              mentionsItems={closeAfter(mentionsItems)}
              tagsItems={closeAfter(tagsItems)}
              pagesPlaceholder={pagesPlaceholder}
              emptyMessage={emptyMessage}
              pagesEmptyMessage={pagesEmptyMessage}
              onPagesQueryChange={onPagesQueryChange}
            />
          )}
          <CommandPaletteFooter />
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  )
}

export interface CommandInlinePickerProps {
  items: CommandItem[]
  placeholder?: string
  emptyMessage?: string
  label?: string
  className?: string
  autoFocus?: boolean
  // Controlled highlighted-row value. Drives both arrow-key cursor position and
  // the visual selected state. When uncontrolled, cmdk auto-highlights the first
  // matching row each time the query changes.
  value?: string
  onValueChange?: (next: string) => void
  // Controlled query text. Hosts that derive items FROM what was typed (e.g. the
  // user picker offering "invite <typed email>") need to read it; omit both for
  // the default self-managed input.
  search?: string
  onSearchChange?: (next: string) => void
}

// Non-modal inline-picker variant. Embedded inside other dialogs (M4.2 parent
// picker, M4.2 move target, M5.2 [[wikilink]] popover). No overlay, no portal.
export const CommandInlinePicker = forwardRef<
  HTMLDivElement,
  CommandInlinePickerProps
>(function CommandInlinePicker(
  {
    items,
    placeholder = 'Search…',
    emptyMessage = 'No matches.',
    label = 'Picker',
    className,
    autoFocus = true,
    value,
    onValueChange,
    search,
    onSearchChange,
  },
  ref,
) {
  return (
    <CmdkRoot
      ref={ref}
      label={label}
      value={value}
      onValueChange={onValueChange}
      className={cn('tela-command-root tela-command-inline', className)}
    >
      <div className="tela-command-input-row">
        <Search
          aria-hidden
          width={16}
          height={16}
          className="tela-command-input-icon"
        />
        <CmdkInput
          value={search}
          onValueChange={onSearchChange}
          placeholder={placeholder}
          className="tela-command-input"
          autoFocus={autoFocus}
        />
      </div>
      <CmdkList className="tela-command-list tela-command-list--inline">
        <CmdkEmpty className="tela-command-empty">{emptyMessage}</CmdkEmpty>
        {items.map((item) => (
          <CmdkItem
            key={item.id}
            value={item.id}
            keywords={[item.title, ...(item.keywords ?? [])]}
            onSelect={item.onSelect}
            className={commandItemClass(item)}
          >
            <CommandRowContent
              icon={item.icon}
              title={item.title}
              subtitle={item.subtitle}
              breadcrumb={item.breadcrumb}
            />
          </CmdkItem>
        ))}
      </CmdkList>
    </CmdkRoot>
  )
})

// Re-export lower-level cmdk pieces under stable names so callers needing
// custom composition (groups, separators) don't reach into cmdk directly.
export { CmdkGroup as CommandGroup, CmdkSeparator as CommandSeparator }

// Mode helper re-exported so the app-level host can compute prefix-seeded
// search-request payloads without re-deriving the prefix table.
// eslint-disable-next-line react-refresh/only-export-components
export { prefixForMode }

// Shared utility for app-level mounts: registers the Cmd-K / Cmd-Shift-P /
// Cmd-N keyboard contract. Callers supply handlers; the hook scopes them to
// non-editable focus per useGlobalShortcut's rules.
export interface PaletteShortcuts {
  onOpenPages: () => void
  onOpenCommands: () => void
  onNewPage?: () => void
}

// eslint-disable-next-line react-refresh/only-export-components
export function usePaletteShortcuts({
  onOpenPages,
  onOpenCommands,
  onNewPage,
}: PaletteShortcuts): void {
  useGlobalShortcut({
    'mod+k': () => onOpenPages(),
    'mod+shift+p': () => onOpenCommands(),
    'mod+n': () => onNewPage?.(),
  })
}
