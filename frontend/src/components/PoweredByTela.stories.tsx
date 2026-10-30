import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClientProvider, QueryClient } from '@tanstack/react-query'
import type { HostContext } from '../lib/types'
import { PoweredByTela } from './PoweredByTela'

// PoweredByTela reads host-context via TanStack Query and renders only where
// the host wears another brand (an org custom domain or a rebranded instance). Each story seeds a fixed ['host-context'] so it renders
// deterministically (no network).
function withHostContext(ctx: HostContext) {
  const client = new QueryClient()
  client.setQueryData(['host-context'], ctx)
  return function Decorator(Story: () => React.ReactElement) {
    return (
      <QueryClientProvider client={client}>
        <Story />
      </QueryClientProvider>
    )
  }
}

const canonical: HostContext = {
  org: null,
  canonical_base: '',
  login: { password_enabled: true, social_enabled: true, org_sso_available: false },
  ai_available: true,
}
const onOrgDomain: HostContext = {
  org: { id: 1, name: 'Acme Corp', slug: 'acme', logo_url: '', accent: '' },
  canonical_base: '',
  login: { password_enabled: true, social_enabled: true, org_sso_available: true },
  ai_available: true,
}

const rebrandedInstance: HostContext = {
  ...canonical,
  brand: { name: 'Boustead Wiki', logo_url: '', accent: '' },
}

const meta: Meta<typeof PoweredByTela> = {
  title: 'Brand/PoweredByTela',
  component: PoweredByTela,
}
export default meta

type Story = StoryObj<typeof PoweredByTela>

// Renders nothing on the canonical host.
export const Canonical: Story = { decorators: [withHostContext(canonical)] }
// The discreet credit, shown on an org's custom domain.
export const OnCustomDomain: Story = { decorators: [withHostContext(onOrgDomain)] }
// Also shown on a rebranded instance's own address.
export const OnRebrandedInstance: Story = { decorators: [withHostContext(rebrandedInstance)] }
