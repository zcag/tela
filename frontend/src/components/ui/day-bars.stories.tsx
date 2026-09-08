import type { Meta, StoryObj } from '@storybook/react-vite'
import { DayBars } from './day-bars'

const days = Array.from({ length: 30 }, (_, i) => {
  const d = new Date(Date.UTC(2026, 7, 1 + i))
  return d.toISOString().slice(0, 10)
})

const meta: Meta<typeof DayBars> = {
  title: 'UI/DayBars',
  component: DayBars,
  args: {
    values: [0, 0, 1, 0, 3, 2, 0, 0, 5, 1, 0, 0, 0, 2, 7, 4, 0, 1, 0, 0, 3, 9, 2, 0, 0, 1, 4, 0, 2, 6],
    labels: days,
    ariaLabel: 'Signups per day',
  },
}
export default meta

type Story = StoryObj<typeof DayBars>

export const Default: Story = {
  render: (args) => (
    <div className="w-[520px] text-[var(--accent)]">
      <DayBars {...args} />
    </div>
  ),
}

// Every bucket empty: the floor ticks must still read as thirty days of zero,
// not as a broken chart.
export const AllZero: Story = {
  render: (args) => (
    <div className="w-[520px] text-[var(--accent)]">
      <DayBars {...args} values={days.map(() => 0)} />
    </div>
  ),
}

// One outlier dwarfing the rest — the small days still have to be visible,
// which is what the 8% minimum bar height is for.
export const Spiky: Story = {
  render: (args) => (
    <div className="w-[520px] text-[var(--accent)]">
      <DayBars {...args} values={days.map((_, i) => (i === 20 ? 120 : i % 5 === 0 ? 1 : 0))} />
    </div>
  ),
}

// Narrow container — bars stay legible rather than collapsing to nothing.
export const Narrow: Story = {
  render: (args) => (
    <div className="w-[220px] text-[var(--accent-positive-fg)]">
      <DayBars {...args} />
    </div>
  ),
}
