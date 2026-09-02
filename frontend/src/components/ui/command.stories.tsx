import { useMemo, useState } from 'react'
import type { Meta, StoryObj } from '@storybook/react-vite'
import {
  Bell,
  FileText,
  Folder,
  Globe,
  HelpCircle,
  Moon,
  Palette,
  Sun,
} from 'lucide-react'
import {
  CommandInlinePicker,
  CommandPalette,
  type CommandItem,
  type CommandSubPicker,
} from './command'
import {
  materializeCommands,
  type CommandContext,
} from '../../lib/commands'
// Side effect: registers the 3 starter commands so the Registry story below
// shows them populated.
import '../../lib/commands/starters'
import type { Space } from '../../lib/types'
import { Button } from './button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from './dialog'

const meta: Meta = {
  title: 'UI/Command',
  parameters: {
    layout: 'fullscreen',
  },
}
export default meta

type Story = StoryObj

const SAMPLE_PAGES: Omit<CommandItem, 'onSelect'>[] = [
  {
    id: 'p1',
    title: 'Onboarding',
    subtitle: 'Welcome to tela — start here.',
    breadcrumb: 'Engineering',
    // (space root — breadcrumb is the bare space name)
    icon: <FileText width={14} height={14} />,
    keywords: ['intro', 'getting started', 'new hire'],
  },
  {
    id: 'p2',
    title: 'Architecture Overview',
    subtitle: 'How the services fit together.',
    breadcrumb: 'Engineering · Docs',
    icon: <FileText width={14} height={14} />,
    keywords: ['system', 'design', 'services'],
  },
  {
    id: 'p3',
    title: 'Q3 OKRs',
    subtitle: 'Objectives and key results for Q3.',
    breadcrumb: 'Operations',
    icon: <FileText width={14} height={14} />,
    keywords: ['okrs', 'goals', 'quarterly'],
  },
  {
    id: 'p4',
    title: 'On-call Runbook',
    subtitle: 'What to do when the pager fires.',
    breadcrumb: 'Engineering · Ops / Runbooks',
    icon: <FileText width={14} height={14} />,
    keywords: ['oncall', 'incident', 'runbook'],
  },
  {
    id: 'p5',
    title: 'Brand Guidelines',
    subtitle: 'Logo, palette, voice.',
    breadcrumb: 'Design',
    icon: <FileText width={14} height={14} />,
    keywords: ['brand', 'logo', 'palette'],
  },
]

const SAMPLE_COMMANDS: Omit<CommandItem, 'onSelect'>[] = [
  {
    id: 'cmd.toggle-theme',
    title: 'Toggle theme',
    subtitle: 'Light → Dark → Warm',
    icon: <Palette width={14} height={14} />,
    keywords: ['dark', 'light', 'warm', 'appearance'],
  },
  {
    id: 'cmd.go-to-space',
    title: 'Go to space…',
    subtitle: 'Jump to another space',
    icon: <Folder width={14} height={14} />,
    keywords: ['space', 'navigate', 'jump'],
  },
  {
    id: 'cmd.help',
    title: 'Show keyboard shortcuts',
    subtitle: 'See every shortcut',
    icon: <HelpCircle width={14} height={14} />,
    keywords: ['help', 'shortcuts', 'keys'],
  },
  {
    id: 'cmd.dark',
    title: 'Switch to dark theme',
    icon: <Moon width={14} height={14} />,
    keywords: ['dark', 'night'],
  },
  {
    id: 'cmd.light',
    title: 'Switch to light theme',
    icon: <Sun width={14} height={14} />,
    keywords: ['light', 'day'],
  },
]

function makeItems(
  source: Omit<CommandItem, 'onSelect'>[],
  onSelect: (id: string) => void,
): CommandItem[] {
  return source.map((item) => ({ ...item, onSelect: () => onSelect(item.id) }))
}

export const ModalPagesEmpty: Story = {
  name: 'Modal — pages mode, empty',
  render: () => {
    const [open, setOpen] = useState(true)
    return (
      <div className="min-h-[80vh] p-[var(--space-7)]">
        <Button onClick={() => setOpen(true)}>Open palette</Button>
        <CommandPalette
          open={open}
          onOpenChange={setOpen}
          initialMode="pages"
          pagesPlaceholder="Search pages… (M5 wires real search)"
        />
      </div>
    )
  },
}

export const ModalPagesWithItems: Story = {
  name: 'Modal — pages mode with items',
  render: () => {
    const [open, setOpen] = useState(true)
    const [selected, setSelected] = useState<string | null>(null)
    return (
      <div className="min-h-[80vh] p-[var(--space-7)]">
        <Button onClick={() => setOpen(true)}>Open palette</Button>
        {selected ? (
          <p className="mt-[var(--space-4)] text-[length:var(--text-sm)] text-[var(--text-muted)]">
            Selected: <code>{selected}</code>
          </p>
        ) : null}
        <CommandPalette
          open={open}
          onOpenChange={setOpen}
          initialMode="pages"
          pagesItems={makeItems(SAMPLE_PAGES, setSelected)}
        />
      </div>
    )
  },
}

export const ModalCommandsMode: Story = {
  name: 'Modal — commands mode (> prefix)',
  render: () => {
    const [open, setOpen] = useState(true)
    const [selected, setSelected] = useState<string | null>(null)
    return (
      <div className="min-h-[80vh] p-[var(--space-7)]">
        <Button onClick={() => setOpen(true)}>Open commands</Button>
        {selected ? (
          <p className="mt-[var(--space-4)] text-[length:var(--text-sm)] text-[var(--text-muted)]">
            Ran: <code>{selected}</code>
          </p>
        ) : null}
        <CommandPalette
          open={open}
          onOpenChange={setOpen}
          initialMode="commands"
          commandsItems={makeItems(SAMPLE_COMMANDS, setSelected)}
        />
      </div>
    )
  },
}

export const ModalHelpMode: Story = {
  name: 'Modal — help mode (?)',
  render: () => {
    const [open, setOpen] = useState(true)
    return (
      <div className="min-h-[80vh] p-[var(--space-7)]">
        <Button onClick={() => setOpen(true)}>Open help</Button>
        <CommandPalette
          open={open}
          onOpenChange={setOpen}
          initialMode="help"
        />
      </div>
    )
  },
}

export const ModalMentionsStub: Story = {
  name: 'Modal — mentions stub (@)',
  render: () => {
    const [open, setOpen] = useState(true)
    return (
      <div className="min-h-[80vh] p-[var(--space-7)]">
        <Button onClick={() => setOpen(true)}>Open mentions</Button>
        <CommandPalette
          open={open}
          onOpenChange={setOpen}
          initialMode="mentions"
          mentionsItems={[
            {
              id: 'u1',
              title: 'Anna Karenina',
              subtitle: '@anna',
              icon: <Bell width={14} height={14} />,
              onSelect: () => {},
            },
            {
              id: 'u2',
              title: 'Boris Yeltsin',
              subtitle: '@boris',
              icon: <Bell width={14} height={14} />,
              onSelect: () => {},
            },
          ]}
        />
      </div>
    )
  },
}

export const InlinePicker: Story = {
  name: 'Inline picker — embedded in a host dialog',
  render: () => {
    const [selected, setSelected] = useState<string | null>(null)
    const items: CommandItem[] = SAMPLE_PAGES.map((p) => ({
      ...p,
      onSelect: () => setSelected(p.id),
    }))
    return (
      <div className="min-h-[80vh] p-[var(--space-7)]">
        <Dialog defaultOpen>
          <DialogTrigger asChild>
            <Button>Open host dialog</Button>
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Pick a parent page</DialogTitle>
            </DialogHeader>
            <div className="flex flex-col gap-[var(--space-2)]">
              <label className="text-[length:var(--text-sm)] text-[var(--text-muted)]">
                Parent
              </label>
              <CommandInlinePicker
                items={items}
                placeholder="Search pages in this space…"
                label="Parent page"
              />
              {selected ? (
                <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
                  Picked: <code>{selected}</code>
                </p>
              ) : null}
            </div>
          </DialogContent>
        </Dialog>
      </div>
    )
  },
}

const SAMPLE_SPACES: Space[] = [
  { id: 1, name: 'Engineering', slug: 'engineering', created_at: '', updated_at: '' },
  { id: 2, name: 'Operations', slug: 'operations', created_at: '', updated_at: '' },
  { id: 3, name: 'Design', slug: 'design', created_at: '', updated_at: '' },
]

export const ModalCommandsModeRegistry: Story = {
  name: 'Modal — commands mode populated from registry (3 starters)',
  render: () => {
    const [open, setOpen] = useState(true)
    const [subPicker, setSubPicker] = useState<CommandSubPicker | null>(null)
    const [searchRequest, setSearchRequest] =
      useState<{ value: string; nonce: number } | null>(null)
    const [log, setLog] = useState<string | null>(null)

    const ctx = useMemo<CommandContext>(
      () => ({
        spaces: SAMPLE_SPACES,
        navigateToSpace: (spaceId) => {
          setLog(
            `(stub) navigate to ${
              SAMPLE_SPACES.find((s) => s.id === spaceId)?.name ?? spaceId
            }`,
          )
        },
        openHelpMode: () => {
          setSubPicker(null)
          setSearchRequest((prev) => ({
            value: '?',
            nonce: (prev?.nonce ?? 0) + 1,
          }))
        },
        openSubPicker: (spec) => setSubPicker(spec),
        closePalette: () => setOpen(false),
      }),
      [],
    )

    const commandsItems = useMemo(() => materializeCommands(ctx), [ctx])

    return (
      <div className="min-h-[80vh] p-[var(--space-7)]">
        <Button
          onClick={() => {
            setSubPicker(null)
            setSearchRequest(null)
            setOpen(true)
          }}
        >
          Open commands palette
        </Button>
        {log ? (
          <p className="mt-[var(--space-4)] text-[length:var(--text-sm)] text-[var(--text-muted)]">
            {log}
          </p>
        ) : null}
        <CommandPalette
          open={open}
          onOpenChange={(next) => {
            setOpen(next)
            if (!next) {
              setSubPicker(null)
              setSearchRequest(null)
            }
          }}
          initialMode="commands"
          commandsItems={commandsItems}
          subPicker={subPicker}
          searchRequest={searchRequest ?? undefined}
        />
      </div>
    )
  },
}

export const InlinePickerStandalone: Story = {
  name: 'Inline picker — standalone (no dialog)',
  render: () => {
    const [selected, setSelected] = useState<string | null>(null)
    const items: CommandItem[] = SAMPLE_PAGES.map((p) => ({
      ...p,
      onSelect: () => setSelected(p.id),
    }))
    return (
      <div className="p-[var(--space-7)] max-w-[32rem]">
        <h2 className="m-0 mb-[var(--space-3)] text-[length:var(--text-lg)] text-[var(--text-primary)]">
          Move page to…
        </h2>
        <CommandInlinePicker
          items={[
            {
              id: '__root__',
              title: '(Top of space)',
              subtitle: 'Move to space root',
              icon: <Globe width={14} height={14} />,
              onSelect: () => setSelected('__root__'),
            },
            ...items,
          ]}
          placeholder="Pick a target page…"
          label="Move target"
        />
        {selected ? (
          <p className="mt-[var(--space-3)] text-[length:var(--text-sm)] text-[var(--text-muted)]">
            Target: <code>{selected}</code>
          </p>
        ) : null}
      </div>
    )
  },
}
