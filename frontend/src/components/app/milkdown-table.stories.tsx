import type { Meta, StoryObj } from '@storybook/react-vite'
import { useEffect, useRef } from 'react'
import { enhanceReadonlyTable } from '../../lib/blocks/table-enhance'

// Renders a static GFM-style table with the same classes the decoration plugin
// adds (glyph cells, featured column) and runs the REAL enhanceReadonlyTable for
// sort/filter — so the story exercises the actual code, theme-driven.

type Cell = { glyph?: 'check' | 'cross' | 'dash'; text?: string }
type Row = Cell[]

const HEAD = ['Feature', 'Free', 'Pro', 'Team']
const ROWS: Row[] = [
  [{ text: 'Pages' }, { text: '50' }, { text: '5,000' }, { text: 'Unlimited' }],
  [{ text: 'SSO / SAML' }, { glyph: 'cross' }, { glyph: 'check' }, { glyph: 'check' }],
  [{ text: 'Audit log' }, { glyph: 'cross' }, { glyph: 'dash' }, { glyph: 'check' }],
  [{ text: 'Seats' }, { text: '3' }, { text: '25' }, { text: 'Unlimited' }],
  [{ text: 'API access' }, { glyph: 'check' }, { glyph: 'check' }, { glyph: 'check' }],
  [{ text: 'Priority support' }, { glyph: 'cross' }, { glyph: 'check' }, { glyph: 'check' }],
  [{ text: 'Custom domain' }, { glyph: 'cross' }, { glyph: 'check' }, { glyph: 'check' }],
]

function ComparisonTable() {
  const ref = useRef<HTMLTableElement>(null)
  useEffect(() => {
    if (ref.current) enhanceReadonlyTable(ref.current)
  }, [])
  return (
    <div className="tela-milkdown">
      <div className="ProseMirror" style={{ maxWidth: '40rem' }}>
        <div className="tableWrapper">
          <table ref={ref}>
            <thead>
              <tr>
                {HEAD.map((h) => (
                  <th key={h}>
                    <p>{h}</p>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {ROWS.map((row, r) => (
                <tr key={r}>
                  {row.map((cell, c) => (
                    <td
                      key={c}
                      className={
                        cell.glyph
                          ? `tela-cell-glyph tela-cell-glyph-${cell.glyph}`
                          : undefined
                      }
                    >
                      <p>{cell.glyph ?? cell.text}</p>
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  )
}

const meta: Meta<typeof ComparisonTable> = {
  title: 'App/Milkdown Table',
  component: ComparisonTable,
  parameters: { layout: 'padded' },
}
export default meta

type Story = StoryObj<typeof ComparisonTable>

export const Comparison: Story = {
  name: 'Feature comparison (glyphs + featured + sort/filter)',
}
