import { describe, expect, it } from 'vitest'
import { isExternalUrl } from './link-target'

const SELF = 'https://telawiki.com'

describe('isExternalUrl', () => {
  it('treats another origin as external', () => {
    expect(isExternalUrl('https://github.com/serpapi/SerpApi/blob/abc/main.py#L12', SELF)).toBe(true)
    expect(isExternalUrl('http://example.com', SELF)).toBe(true)
    // a different port/scheme on the same host is still a different origin
    expect(isExternalUrl('http://telawiki.com', SELF)).toBe(true)
  })

  it('keeps our own origin internal', () => {
    expect(isExternalUrl(SELF + '/p/123', SELF)).toBe(false)
    expect(isExternalUrl(SELF + '/spaces/241/pages/2650/x', SELF)).toBe(false)
  })

  it('keeps in-document, relative and root-relative links internal', () => {
    expect(isExternalUrl('#some-heading', SELF)).toBe(false)
    expect(isExternalUrl('/spaces/241/pages/2650/entry-points', SELF)).toBe(false)
    expect(isExternalUrl('./sibling.md', SELF)).toBe(false)
    expect(isExternalUrl('../up.md', SELF)).toBe(false)
    expect(isExternalUrl('notes.md', SELF)).toBe(false)
  })

  it('keeps the wikilink scheme internal', () => {
    expect(isExternalUrl('tela://page/2650', SELF)).toBe(false)
  })

  it('does not new-tab non-web schemes', () => {
    // A target on mailto:/tel: opens an empty tab in some browsers.
    expect(isExternalUrl('mailto:someone@example.com', SELF)).toBe(false)
    expect(isExternalUrl('tel:+900000000', SELF)).toBe(false)
  })

  it('never guesses a new tab for junk', () => {
    expect(isExternalUrl('', SELF)).toBe(false)
    expect(isExternalUrl('   ', SELF)).toBe(false)
    expect(isExternalUrl('http://[bad', SELF)).toBe(false)
  })
})
