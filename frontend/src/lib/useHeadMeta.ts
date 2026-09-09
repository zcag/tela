import { useEffect } from 'react'

// Per-page document head for the public (no-login) surfaces — title, meta
// description, canonical, OpenGraph/Twitter, and an optional RSS alternate.
// These pages are a real SPA served to humans AND to JS-rendering crawlers
// (Googlebot), so client-set meta is what makes them index with the right
// title/description; non-JS social bots are handled server-side by the OG
// bot-gate (backend api/public_og.go). Tags are created on mount and removed on
// unmount so navigating away restores the prior head.

export interface HeadMeta {
  title: string
  description?: string
  /** Path (`/public/...`) or absolute URL; resolved to absolute for canonical/og:url. */
  canonicalPath?: string
  /** Path or absolute URL for og:image / twitter:image. */
  image?: string
  ogType?: 'website' | 'article' | 'profile'
  /** RSS feed href for a `<link rel="alternate">`. */
  feedHref?: string
}

function toAbsolute(pathOrUrl?: string): string | undefined {
  if (!pathOrUrl) return undefined
  if (/^https?:\/\//.test(pathOrUrl)) return pathOrUrl
  return window.location.origin + pathOrUrl
}

export function useHeadMeta({
  title,
  description,
  canonicalPath,
  image,
  ogType = 'website',
  feedHref,
}: HeadMeta) {
  useEffect(() => {
    const prevTitle = document.title
    document.title = title
    const head = document.head
    const created: HTMLElement[] = []

    const addMeta = (attr: 'name' | 'property', key: string, content?: string) => {
      if (!content) return
      const el = document.createElement('meta')
      el.setAttribute(attr, key)
      el.setAttribute('content', content)
      head.appendChild(el)
      created.push(el)
    }
    const addLink = (rel: string, href?: string, type?: string) => {
      if (!href) return
      const el = document.createElement('link')
      el.setAttribute('rel', rel)
      el.setAttribute('href', href)
      if (type) el.setAttribute('type', type)
      head.appendChild(el)
      created.push(el)
    }

    const canonical = toAbsolute(canonicalPath)
    const img = toAbsolute(image)
    addMeta('name', 'description', description)
    addLink('canonical', canonical)
    addMeta('property', 'og:site_name', 'tela')
    addMeta('property', 'og:title', title)
    addMeta('property', 'og:description', description)
    addMeta('property', 'og:type', ogType)
    addMeta('property', 'og:url', canonical)
    addMeta('property', 'og:image', img)
    addMeta('name', 'twitter:card', img ? 'summary_large_image' : 'summary')
    addMeta('name', 'twitter:title', title)
    addMeta('name', 'twitter:description', description)
    addMeta('name', 'twitter:image', img)
    addLink('alternate', toAbsolute(feedHref), 'application/rss+xml')

    return () => {
      document.title = prevTitle
      for (const el of created) el.remove()
    }
  }, [title, description, canonicalPath, image, ogType, feedHref])
}

// Keep a not-found surface out of the index, whatever the status line said.
// The edge only downgrades a handle URL to a real 404 when the backend gives a
// DEFINITE "no" (the auth_request probe in frontend/nginx.conf): a fail-open
// miss, a URL shape the handle regex doesn't cover, or an in-app route still
// answers 200 with this screen. Googlebot executes JS and honours a client-set
// robots meta, so this is the belt to that 404's braces. Removed on unmount, so
// navigating on to a real page drops it again.
export function useNoindex() {
  useEffect(() => {
    const el = document.createElement('meta')
    el.setAttribute('name', 'robots')
    el.setAttribute('content', 'noindex')
    document.head.appendChild(el)
    return () => el.remove()
  }, [])
}
