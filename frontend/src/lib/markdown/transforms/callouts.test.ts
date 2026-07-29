import { describe, expect, it } from 'vitest'
import { unified } from 'unified'
import remarkParse from 'remark-parse'
import remarkGfm from 'remark-gfm'
import type { Root } from 'mdast'
import { calloutsRemark, type MdastNode } from './callouts'

// Parse markdown through the same transform the editor + view share.
function parse(md: string): Root {
  const p = unified().use(remarkParse).use(remarkGfm).use(calloutsRemark)
  return p.runSync(p.parse(md)) as Root
}

// Flatten every text/inline value under a node so we can assert on content
// survival without depending on the exact inline node shape.
function textOf(node: MdastNode): string {
  if (typeof node.value === 'string') return node.value
  return (node.children ?? []).map(textOf).join('')
}

function firstCallout(md: string): MdastNode {
  const callout = (parse(md).children as unknown as MdastNode[]).find(
    (n) => n.type === 'callout',
  )
  if (!callout) throw new Error('no callout node produced')
  return callout
}

describe('callout transform', () => {
  it('strips the marker from a single-paragraph callout', () => {
    const c = firstCallout('> [!NOTE]\n> Body text here.')
    expect(c.calloutType).toBe('note')
    expect(textOf(c)).toBe('Body text here.')
  })

  it('keeps the marker on its own line with inline body markup', () => {
    // Regression: the marker text node is `"[!IMPORTANT]\n"` and the REST of
    // the same paragraph continues in sibling inline nodes (strong, text).
    // Dropping the whole paragraph deleted real content.
    const c = firstCallout(
      '> [!IMPORTANT]\n> **Bold lead.** Trailing sentence.',
    )
    expect(c.calloutType).toBe('important')
    expect(textOf(c)).toBe('Bold lead. Trailing sentence.')
  })

  it('preserves both paragraphs of a multi-paragraph callout', () => {
    const c = firstCallout(
      '> [!IMPORTANT]\n> **First para lead.** More of the first.\n>\n> Second paragraph.',
    )
    expect(c.children).toHaveLength(2)
    expect(textOf(c.children![0])).toBe('First para lead. More of the first.')
    expect(textOf(c.children![1])).toBe('Second paragraph.')
  })

  it('drops the marker-only paragraph our own serializer emits', () => {
    // Round-trip shape: marker paragraph, blank `>`, then the body.
    const c = firstCallout('> [!TIP]\n>\n> Body paragraph.')
    expect(c.children).toHaveLength(1)
    expect(textOf(c.children![0])).toBe('Body paragraph.')
  })

  it('keeps an empty paragraph for a marker-only callout', () => {
    const c = firstCallout('> [!WARNING]')
    expect(c.children).toHaveLength(1)
    expect(c.children![0].type).toBe('paragraph')
  })

  it('leaves a non-callout blockquote alone', () => {
    const tree = parse('> just a quote')
    expect((tree.children[0] as unknown as MdastNode).type).toBe('blockquote')
  })

  it('is stable across the editor round-trip shape', () => {
    // The editor re-emits the marker as its own paragraph plus a blank `>`
    // line. Re-parsing that output must yield the same two body paragraphs,
    // not shed the first one.
    const authored =
      '> [!IMPORTANT]\n> **Lead sentence.** Context follows.\n>\n> Therefore the conclusion.'
    const roundTripped =
      '> [!IMPORTANT]\n>\n> **Lead sentence.** Context follows.\n>\n> Therefore the conclusion.'
    for (const md of [authored, roundTripped]) {
      const c = firstCallout(md)
      expect(c.children).toHaveLength(2)
      expect(textOf(c.children![0])).toBe('Lead sentence. Context follows.')
      expect(textOf(c.children![1])).toBe('Therefore the conclusion.')
    }
  })
})
