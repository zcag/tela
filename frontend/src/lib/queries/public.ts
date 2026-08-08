import { useQuery, type QueryClient } from '@tanstack/react-query'
import type { ApiErrorBody } from '../types'

// Public-space read queries for /public/spaces/... routes. Like the share
// queries, these deliberately bypass the `api()` helper — `api()` dispatches
// `tela:auth-required` on 401 and bounces to /login, but a public space read is
// unauthenticated: a miss is a plain 404 ("no such public space"), never "log
// in". A logged-out reader must stay on the page.

export const publicKeys = {
  all: ['public'] as const,
  space: (spaceId: number) => [...publicKeys.all, 'space', spaceId] as const,
  tree: (spaceId: number) => [...publicKeys.all, 'tree', spaceId] as const,
  page: (spaceId: number, pageId: number) =>
    [...publicKeys.all, 'page', spaceId, pageId] as const,
  discover: (sort: DiscoverSort, offset: number) =>
    [...publicKeys.all, 'discover', sort, offset] as const,
}

export interface PublicSpacePayload {
  id: number
  name: string
  slug: string
  visibility: string
  // Blog standfirst + byline author handle (links to /u/{handle}). '' when unset.
  description: string
  owner_handle?: string
}

// Blog-card metadata the backend derives per post for the index surfaces
// (a public space front page and /u/{handle}). Lets a card render an excerpt,
// reading time, cover and tags in one round trip — no per-post body fetch.
export interface BlogCardMeta {
  kind?: string // "deck" for slide decks; absent for prose docs
  excerpt: string
  reading_minutes: number
  cover?: string
  tags?: string[]
}

export interface PublicPagePayload {
  id: number
  title: string
  body: string
  props?: Record<string, unknown>
  // Present when the page is a deck — the public present (live SPA) + cover routes.
  deck?: { present_path: string; cover_path: string }
  created_at: string
  updated_at: string
  // Byline: original author + last editor (usernames; editor shown only when it
  // differs). Blank for legacy pages with no revision trail.
  author?: string
  editor?: string
}

export interface PublicPageNode extends BlogCardMeta {
  id: number
  title: string
  parent_id: number | null
  position: number
  created_at: string
  updated_at: string
}

export type PublicErrorKind = 'not_found' | 'network' | 'http_error'

export class PublicError extends Error {
  readonly kind: PublicErrorKind
  readonly status: number
  constructor(kind: PublicErrorKind, status: number, message: string) {
    super(message)
    this.name = 'PublicError'
    this.kind = kind
    this.status = status
  }
}

async function publicFetch<T>(path: string): Promise<T> {
  let res: Response
  try {
    res = await fetch(path, { headers: { Accept: 'application/json' } })
  } catch (err) {
    throw new PublicError('network', 0, err instanceof Error ? err.message : 'network error')
  }
  const isJson = (res.headers.get('Content-Type') ?? '').includes('application/json')
  if (!res.ok) {
    let body: ApiErrorBody | null = null
    if (isJson) body = (await res.json().catch(() => null)) as ApiErrorBody | null
    const message = body?.error ?? `HTTP ${res.status}`
    if (res.status === 404) throw new PublicError('not_found', res.status, message)
    throw new PublicError('http_error', res.status, message)
  }
  return (await res.json()) as T
}

export function usePublicSpace(spaceId: number) {
  return useQuery<{ space: PublicSpacePayload }, PublicError>({
    queryKey: publicKeys.space(spaceId),
    queryFn: () => publicFetch(`/api/public/spaces/${spaceId}`),
    retry: false,
    staleTime: 60_000,
  })
}

export function usePublicSpaceTree(spaceId: number, enabled = true) {
  return useQuery<{ pages: PublicPageNode[] }, PublicError>({
    queryKey: publicKeys.tree(spaceId),
    queryFn: () => publicFetch(`/api/public/spaces/${spaceId}/tree`),
    retry: false,
    staleTime: 60_000,
    enabled,
  })
}

export function usePublicSpacePage(spaceId: number, pageId: number, enabled = true) {
  return useQuery<{ page: PublicPagePayload }, PublicError>({
    queryKey: publicKeys.page(spaceId, pageId),
    queryFn: () => publicFetch(`/api/public/spaces/${spaceId}/pages/${pageId}`),
    retry: false,
    staleTime: 60_000,
    enabled,
  })
}

// Intent-preload the public page body on link hover (the reader route's loader)
// so the click renders from cache. Mirrors usePublicSpacePage's key + queryFn.
export function prefetchPublicSpacePage(
  qc: QueryClient,
  spaceId: number,
  pageId: number,
): void {
  void qc.prefetchQuery({
    queryKey: publicKeys.page(spaceId, pageId),
    queryFn: () => publicFetch(`/api/public/spaces/${spaceId}/pages/${pageId}`),
  })
}

// GitHub-style handle home: /{handle}. One endpoint resolves either a user or
// an org handle to its public presence (name, bio, public spaces, and the
// newest posts across them). 404 when the handle has no public spaces —
// rendered as a neutral not-found, never a bounce to /login (raw publicFetch,
// like every other /api/public/ read).
export interface ByHandleSpace {
  id: number
  name: string
  slug: string
  description: string
  page_count: number
  updated_at: string
}

// One post on the handle home's "Latest" strip — carries its space for the link.
export interface ByHandlePost extends BlogCardMeta {
  space_id: number
  space_name: string
  // Slug of the space the post lives in — with the home's handle this builds the
  // canonical /{handle}/{space-slug}/{id}/{slug} link.
  space_slug: string
  id: number
  title: string
  created_at: string
  updated_at: string
}

export interface ByHandleResponse {
  kind: 'user' | 'org'
  handle: string
  name: string
  bio?: string
  spaces: ByHandleSpace[]
  posts?: ByHandlePost[]
}

export const handleKeys = {
  all: ['by-handle'] as const,
  home: (handle: string) => [...handleKeys.all, handle] as const,
  space: (handle: string, slug: string) =>
    [...handleKeys.all, handle, 'space', slug] as const,
}

export function usePublicByHandle(handle: string) {
  return useQuery<ByHandleResponse, PublicError>({
    queryKey: handleKeys.home(handle),
    queryFn: () =>
      publicFetch(`/api/public/by-handle/${encodeURIComponent(handle)}`),
    retry: false,
    staleTime: 60_000,
  })
}

// /{handle}/{space-slug}: the same payload the single-space endpoint returns,
// consumed exactly like usePublicSpace — so the existing reader/index render it
// unchanged.
export function usePublicByHandleSpace(
  handle: string,
  slug: string,
  enabled = true,
) {
  return useQuery<{ space: PublicSpacePayload }, PublicError>({
    queryKey: handleKeys.space(handle, slug),
    queryFn: () =>
      publicFetch(
        `/api/public/by-handle/${encodeURIComponent(handle)}/spaces/${encodeURIComponent(slug)}`,
      ),
    retry: false,
    staleTime: 60_000,
    enabled,
  })
}

// Cross-tenant public-space discovery directory: every public space on the
// instance, sortable + paginated. Read-only, no login (GET /api/public/discover).
export type DiscoverSort = 'recent' | 'popular'

export const DISCOVER_PAGE_SIZE = 24

export interface DiscoverSpace {
  id: number
  name: string
  slug: string
  description: string
  // The space's OWNING handle — an org's slug for an org space, else the
  // personal owner's username. Links to /{handle}.
  owner_handle?: string
  // Canonical /{handle}/{space-slug}, server-derived. Absent for an ownerless
  // space, where callers fall back to the id route.
  public_path?: string
  page_count: number
  // Most-recent page activity in the space. '' when the space has no pages.
  updated_at: string
}

export interface DiscoverResponse {
  spaces: DiscoverSpace[]
  limit: number
  offset: number
}

export function usePublicDiscover(sort: DiscoverSort, offset: number) {
  return useQuery<DiscoverResponse, PublicError>({
    queryKey: publicKeys.discover(sort, offset),
    queryFn: () =>
      publicFetch(
        `/api/public/discover?sort=${sort}&limit=${DISCOVER_PAGE_SIZE}&offset=${offset}`,
      ),
    retry: false,
    staleTime: 60_000,
  })
}
