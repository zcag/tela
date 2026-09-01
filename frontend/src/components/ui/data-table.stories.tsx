import type { Meta, StoryObj } from '@storybook/react-vite'
import { DataTable, type DataTableColumn } from './data-table'
import { Badge } from './badge'

interface Person {
  id: number
  name: string
  edits: number
  views: number
  lastActive: string
  plan: string
}

const people: Person[] = [
  { id: 1, name: 'ada', edits: 412, views: 3180, lastActive: '2026-08-31', plan: 'team' },
  { id: 2, name: 'grace', edits: 96, views: 1204, lastActive: '2026-08-29', plan: 'free' },
  { id: 3, name: 'linus', edits: 8, views: 44, lastActive: '2026-06-02', plan: 'free' },
  { id: 4, name: 'barbara', edits: 0, views: 0, lastActive: '', plan: 'free' },
]

const columns: DataTableColumn<Person>[] = [
  {
    key: 'name',
    header: 'Person',
    sortValue: (p) => p.name,
    cell: (p) => <span className="font-medium">{p.name}</span>,
  },
  {
    key: 'plan',
    header: 'Plan',
    sortValue: (p) => p.plan,
    cell: (p) => <Badge variant={p.plan === 'free' ? 'muted' : 'accent'}>{p.plan}</Badge>,
  },
  { key: 'edits', header: 'Edits', numeric: true, sortValue: (p) => p.edits, cell: (p) => p.edits },
  { key: 'views', header: 'Views', numeric: true, sortValue: (p) => p.views, cell: (p) => p.views },
  {
    key: 'last',
    header: 'Last active',
    sortValue: (p) => p.lastActive || '0',
    cell: (p) => p.lastActive || <span className="text-[var(--text-muted)]">never</span>,
  },
]

const meta: Meta<typeof DataTable<Person>> = {
  title: 'UI/DataTable',
  component: DataTable<Person>,
  parameters: { layout: 'padded' },
}
export default meta

type Story = StoryObj<typeof DataTable<Person>>

export const Default: Story = {
  args: { rows: people, columns, rowKey: (p: Person) => p.id },
}

// Numeric columns open on descending — "who does the most" is the question.
export const SortedByActivity: Story = {
  args: {
    rows: people,
    columns,
    rowKey: (p: Person) => p.id,
    defaultSort: { key: 'edits', dir: 'desc' },
  },
}

export const Clickable: Story = {
  args: {
    rows: people,
    columns,
    rowKey: (p: Person) => p.id,
    onRowClick: (p: Person) => alert(`open ${p.name}`),
  },
}

// A table wider than its container: the identity column and the row actions
// stay pinned while the middle scrolls, so you never lose track of whose row
// you're reading or scroll the actions off the end.
export const PinnedEdges: Story = {
  render: () => (
    <div className="max-w-[28rem]">
      <DataTable<Person>
        rows={people}
        rowKey={(p) => p.id}
        defaultSort={{ key: 'edits', dir: 'desc' }}
        columns={[
          { ...columns[0], sticky: 'left' },
          ...columns.slice(1),
          {
            key: 'actions',
            header: <span className="sr-only">Actions</span>,
            sticky: 'right',
            cell: () => <span aria-hidden>⋯</span>,
          },
        ]}
      />
    </div>
  ),
}

// Column families + scale tracks: the two things that turn a dozen equal-weight
// numeric columns into something you can read at a glance.
export const GroupedWithScales: Story = {
  render: () => (
    <DataTable<Person>
      rows={people}
      rowKey={(p) => p.id}
      defaultSort={{ key: 'edits', dir: 'desc' }}
      columns={[
        { ...columns[0], group: 'Who' },
        { ...columns[1], group: 'Who' },
        {
          ...columns[2],
          group: 'Activity',
          scale: (p: Person) => p.edits,
          title: 'Revisions written in the window.',
        },
        {
          ...columns[3],
          group: 'Activity',
          scale: (p: Person) => p.views,
          title: 'Pages opened.',
        },
        { ...columns[4], group: 'Cadence' },
      ]}
    />
  ),
}

export const Empty: Story = {
  args: { rows: [], columns, rowKey: (p: Person) => p.id, empty: 'No people match these filters.' },
}
