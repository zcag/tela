import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClientProvider, QueryClient } from '@tanstack/react-query'
import type { HostContext } from '../lib/types'
import { BrandLogo } from './BrandLogo'

// BrandLogo reads host-context via TanStack Query. Each story seeds a client
// with a fixed ['host-context'] value so the three branding states render
// deterministically (no network): canonical tela, an org name wordmark, and an
// org logo image.
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
const orgNamed: HostContext = {
  org: { id: 1, name: 'Acme Corp', slug: 'acme', logo_url: '', accent: '' },
  canonical_base: '',
  login: { password_enabled: true, social_enabled: true, org_sso_available: true },
  ai_available: true,
}
const orgLogo: HostContext = {
  org: {
    id: 1,
    name: 'Acme Corp',
    slug: 'acme',
    logo_url:
      'data:image/svg+xml;utf8,' +
      encodeURIComponent(
        '<svg xmlns="http://www.w3.org/2000/svg" width="96" height="24"><rect width="96" height="24" rx="4" fill="%234f46e5"/><text x="48" y="17" font-family="sans-serif" font-size="13" fill="white" text-anchor="middle">ACME</text></svg>',
      ),
    accent: '',
  },
  canonical_base: '',
  login: { password_enabled: true, social_enabled: true, org_sso_available: true },
  ai_available: true,
}

const instanceLogo =
  'data:image/svg+xml;utf8,' +
  encodeURIComponent(
    '<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"><rect width="24" height="24" rx="6" fill="#0e7490"/><text x="12" y="17" font-family="sans-serif" font-size="14" font-weight="700" fill="white" text-anchor="middle">B</text></svg>',
  )
const instanceNamed: HostContext = {
  ...canonical,
  brand: { name: 'Boustead Wiki', logo_url: '', accent: '' },
}
const instanceLogoAndName: HostContext = {
  ...canonical,
  brand: { name: 'Boustead Wiki', logo_url: instanceLogo, accent: '#0e7490' },
}

const meta: Meta<typeof BrandLogo> = {
  title: 'Brand/BrandLogo',
  component: BrandLogo,
}
export default meta

type Story = StoryObj<typeof BrandLogo>

export const Canonical: Story = { decorators: [withHostContext(canonical)] }
export const OrgName: Story = { decorators: [withHostContext(orgNamed)] }
export const OrgLogo: Story = { decorators: [withHostContext(orgLogo)] }
export const InstanceName: Story = { decorators: [withHostContext(instanceNamed)] }
export const InstanceLogoAndName: Story = { decorators: [withHostContext(instanceLogoAndName)] }
