import { useState } from 'react'
import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, within } from 'storybook/test'
import { MarkdownSourceEditor } from './markdown-source-editor'

const meta: Meta<typeof MarkdownSourceEditor> = {
  title: 'UI/MarkdownSourceEditor',
  component: MarkdownSourceEditor,
}
export default meta

type Story = StoryObj<typeof MarkdownSourceEditor>

const SAMPLE = `# Release checklist

Ship on **Thursday** unless the _migration_ is still ~~running~~ pending.

- [x] Freeze \`main\`
- [ ] Tag the build, see [the runbook](https://example.com/runbook)

> [!WARNING]
> Never deploy during the payroll run.

\`\`\`sh
make deploy
\`\`\`

---

| Step | Owner |
| --- | --- |
| Tag | Ana |
`

function Controlled({ initial }: { initial: string }) {
  const [value, setValue] = useState(initial)
  return (
    <div className="w-[40rem] min-h-[calc(var(--space-8)*6)]">
      <MarkdownSourceEditor value={value} onChange={setValue} placeholder="Write markdown…" ariaLabel="Markdown source" />
    </div>
  )
}

export const Basic: Story = {
  render: () => <Controlled initial={SAMPLE} />,
  play: async ({ canvasElement }) => {
    const box = within(canvasElement).getByRole('textbox', { name: 'Markdown source' })
    await expect(box.textContent).toContain('Release checklist')
  },
}

export const Empty: Story = {
  render: () => <Controlled initial="" />,
}
