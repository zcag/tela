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

function Seeded({ pick, system }: { pick: ThemeName[]; system: boolean }) {
  useEffect(() => {
    // Each manual pick also teaches follow-system that side's theme, so the
    // order here is what produces the pairing under test.
    pick.forEach(setTheme)
    if (system) followSystem()
  }, [pick, system])
  return <ThemeSwitcher />
}

export const Manual: Story = {
  name: 'Manual — a theme is pinned',
  render: () => <Seeded pick={['light']} system={false} />,
}

export const FollowingSystem: Story = {
  name: 'Following system — dots show the pair (light + dark)',
  render: () => <Seeded pick={['dark', 'light']} system={true} />,
}

export const FollowingSystemWarmPair: Story = {
  name: 'Following system — warm learned as the light-side theme',
  render: () => <Seeded pick={['dark', 'warm']} system={true} />,
}
