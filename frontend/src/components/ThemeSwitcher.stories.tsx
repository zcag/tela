import type { Meta, StoryObj } from '@storybook/react-vite'
import { useEffect } from 'react'
import { ThemeSwitcher } from './ThemeSwitcher'
import { followSystem, setTheme, type ThemeName } from '../lib/theme'

// The switcher reads module-level theme state, so each story drives it into the
// shape it wants to show before rendering. Stories mutate the live document
// theme by design — that IS the component.
const meta: Meta<typeof ThemeSwitcher> = {
  title: 'App/ThemeSwitcher',
  component: ThemeSwitcher,
}
export default meta

type Story = StoryObj<typeof ThemeSwitcher>

function Seeded({
  pick,
  system,
  inline,
}: {
  pick: ThemeName[]
  system: boolean
  inline?: boolean
}) {
  useEffect(() => {
    // Each manual pick also teaches follow-system that side's theme, so the
    // order here is what produces the pairing under test.
    pick.forEach(setTheme)
    if (system) followSystem()
  }, [pick, system])
  return <ThemeSwitcher inline={inline} />
}

export const Collapsed: Story = {
  name: 'Header — resting (hover to open the row)',
  render: () => <Seeded pick={['dark', 'light']} system={true} />,
}

export const CollapsedPinned: Story = {
  name: 'Header — a theme pinned, so the glyph is muted',
  render: () => <Seeded pick={['warm']} system={false} />,
}

export const InlineFollowingSystem: Story = {
  name: 'Inline — band encloses the pair (light + dark)',
  render: () => <Seeded pick={['dark', 'light']} system={true} inline />,
}

export const InlineWarmPair: Story = {
  name: 'Inline — warm learned as the light-side theme',
  render: () => <Seeded pick={['dark', 'warm']} system={true} inline />,
}

export const InlineManual: Story = {
  name: 'Inline — manual, no band',
  render: () => <Seeded pick={['light']} system={false} inline />,
}
