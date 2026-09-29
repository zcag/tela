import type { Meta, StoryObj } from '@storybook/react-vite'
import {
  Bold,
  Check,
  ChevronDown,
  Code,
  Highlighter,
  Italic,
  Link as LinkIcon,
  MessageSquare,
  Strikethrough,
  Type,
} from 'lucide-react'
import { TURN_INTO } from './milkdown-turn-into'
import { Input } from '../ui/input'

// Visual reference for the selection bubble-toolbar chrome. The live component
// (BubbleToolbarView) needs a Milkdown plugin-view context, so — like the
// callouts story — this renders the same DOM/classes in plain React. The
// toolbar is `position: fixed` and hidden until `data-show`, so the preview
// forces it static + shown.

function Btn({
  label,
  active,
  children,
}: {
  label: string
  active?: boolean
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      className="tela-bubble-btn"
      aria-label={label}
      aria-pressed={active}
      data-active={active ? 'true' : 'false'}
    >
      {children}
    </button>
  )
}

function Toolbar({ children }: { children: React.ReactNode }) {
  return (
    <div
      role="toolbar"
      aria-label="Format selection"
      className="tela-bubble-toolbar"
      data-show="true"
      style={{ position: 'static' }}
    >
      {children}
    </div>
  )
}

const Sep = () => <span className="tela-bubble-sep" aria-hidden />

// The full bubble: block-type pill, marks, link, comment. Below the md
// breakpoint the two text labels collapse (see .tela-bubble-label).
function FullToolbar({ menuOpen = false }: { menuOpen?: boolean }) {
  return (
    <Toolbar>
      <div className="tela-bubble-menu-anchor">
        <button
          type="button"
          className="tela-bubble-btn tela-bubble-btn--wide"
          aria-haspopup="menu"
          aria-expanded={menuOpen}
          data-active={menuOpen ? 'true' : 'false'}
        >
          <Type size="1em" strokeWidth={2.5} aria-hidden />
          <span className="tela-bubble-label">Text</span>
          <ChevronDown className="tela-bubble-chevron" size="1em" aria-hidden />
        </button>
        {menuOpen ? (
          <div className="tela-block-menu tela-bubble-menu" role="menu">
            <div className="tela-block-menu-label">Turn into</div>
            {TURN_INTO.map((o) => (
              <button
                key={o.id}
                type="button"
                role="menuitemradio"
                aria-checked={o.id === 'text'}
                className="tela-block-menu-item"
              >
                <o.icon size="1em" aria-hidden />
                <span>{o.label}</span>
                {o.id === 'text' ? <Check className="tela-bubble-menu-check" size="1em" aria-hidden /> : null}
              </button>
            ))}
          </div>
        ) : null}
      </div>
      <Sep />
      <Btn label="Bold">
        <Bold size="1em" strokeWidth={2.5} aria-hidden />
      </Btn>
      <Btn label="Italic">
        <Italic size="1em" strokeWidth={2.5} aria-hidden />
      </Btn>
      <Btn label="Strikethrough">
        <Strikethrough size="1em" strokeWidth={2.5} aria-hidden />
      </Btn>
      <Btn label="Inline code">
        <Code size="1em" strokeWidth={2.5} aria-hidden />
      </Btn>
      <Btn label="Highlight">
        <Highlighter size="1em" strokeWidth={2.5} aria-hidden />
      </Btn>
      <Sep />
      <Btn label="Link">
        <LinkIcon size="1em" strokeWidth={2.5} aria-hidden />
      </Btn>
      <Sep />
      <button type="button" className="tela-bubble-btn tela-bubble-btn--wide">
        <MessageSquare size="1em" strokeWidth={2.5} aria-hidden />
        <span className="tela-bubble-label">Comment</span>
      </button>
    </Toolbar>
  )
}

const meta: Meta = {
  title: 'App/Milkdown Bubble Toolbar',
  parameters: { layout: 'padded' },
}
export default meta

type Story = StoryObj

export const Default: Story = {
  render: () => <FullToolbar />,
}

export const TurnIntoOpen: Story = {
  name: 'Turn into open',
  render: () => (
    <div style={{ minHeight: '22rem' }}>
      <FullToolbar menuOpen />
    </div>
  ),
}

export const WithActiveMarks: Story = {
  name: 'Bold + italic active',
  render: () => (
    <Toolbar>
      <Btn label="Bold" active>
        <Bold size="1em" strokeWidth={2.5} aria-hidden />
      </Btn>
      <Btn label="Italic" active>
        <Italic size="1em" strokeWidth={2.5} aria-hidden />
      </Btn>
      <Btn label="Strikethrough">
        <Strikethrough size="1em" strokeWidth={2.5} aria-hidden />
      </Btn>
      <Btn label="Inline code">
        <Code size="1em" strokeWidth={2.5} aria-hidden />
      </Btn>
      <Btn label="Link">
        <LinkIcon size="1em" strokeWidth={2.5} aria-hidden />
      </Btn>
    </Toolbar>
  ),
}

export const LinkMode: Story = {
  name: 'Link entry mode',
  render: () => (
    <Toolbar>
      <Input
        size="sm"
        type="url"
        placeholder="Paste or type a link, then Enter"
        aria-label="Link URL"
        className="tela-bubble-link-input"
        defaultValue="https://telawiki.com"
      />
    </Toolbar>
  ),
}
