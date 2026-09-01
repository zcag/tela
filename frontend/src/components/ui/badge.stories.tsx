import type { Meta, StoryObj } from '@storybook/react-vite'
import { Badge } from './badge'

const meta: Meta<typeof Badge> = {
  title: 'UI/Badge',
  component: Badge,
  argTypes: {
    variant: {
      control: 'select',
      options: ['muted', 'accent', 'danger', 'solid', 'positive', 'warning', 'negative', 'ghost'],
    },
  },
  args: { children: 'This device' },
}
export default meta

type Story = StoryObj<typeof Badge>

export const Muted: Story = { args: { variant: 'muted' } }
export const Accent: Story = { args: { variant: 'accent' } }
export const Danger: Story = { args: { variant: 'danger', children: 'Bug' } }

export const Variants: Story = {
  render: () => (
    <div className="flex flex-wrap gap-[var(--space-3)] items-center">
      <Badge variant="muted">Engineering</Badge>
      <Badge variant="accent">This device</Badge>
      <Badge variant="danger">Bug</Badge>
      <Badge variant="solid">Admin</Badge>
      <Badge variant="positive">Power</Badge>
      <Badge variant="warning">Churned</Badge>
      <Badge variant="negative">Never started</Badge>
      <Badge variant="ghost">You</Badge>
    </div>
  ),
}

// The three weights a row of chips should read in: role outranks capability
// outranks state. All-outline chips make everything look equally important.
export const Taxonomy: Story = {
  render: () => (
    <div className="flex flex-wrap gap-[var(--space-2)] items-center">
      <span className="font-medium">Hazal Pekesen</span>
      <Badge variant="solid">Admin</Badge>
      <Badge variant="muted">MCP</Badge>
      <Badge variant="ghost">You</Badge>
    </div>
  ),
}
