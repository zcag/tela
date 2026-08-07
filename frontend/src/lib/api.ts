import type { ApiErrorBody } from './types'

// Same-origin: Caddy proxies /api/* to backend in prod; Vite dev proxy does the same.
const BASE = ''

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  // Set only on code=forbidden_public: the no-login reader route for content
  // denied on the authed route but published public. Callers redirect there
  // instead of rendering a permission wall (see PageView).
  readonly publicPath?: string

  constructor(status: number, code: string, message: string, publicPath?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.publicPath = publicPath
  }
}

// Dispatched when any non-auth API call comes back 401 — the session has
// expired or the user was deactivated mid-page. main.tsx subscribes once at
// module load and bounces the user to /login?next=<current>. We use a window
// event rather than importing the router directly to avoid an import cycle
// (router imports queries which import api).
const AUTH_REQUIRED_EVENT = 'tela:auth-required'

export interface AuthRequiredDetail {
  next: string
}

export function subscribeToAuthRequired(
  cb: (detail: AuthRequiredDetail) => void,
): () => void {
  function handler(e: Event) {
    cb((e as CustomEvent<AuthRequiredDetail>).detail)
  }
  window.addEventListener(AUTH_REQUIRED_EVENT, handler as EventListener)
  return () =>
    window.removeEventListener(AUTH_REQUIRED_EVENT, handler as EventListener)
}

function isAuthEndpoint(path: string): boolean {
  // /api/auth/me probing must NOT trigger the redirect — its 401 is the
  // canonical "no session" state, not a mid-session expiry.
  return path.startsWith('/api/auth/')
}

function emitAuthRequired(): void {
  const next = window.location.pathname + window.location.search
  window.dispatchEvent(
    new CustomEvent<AuthRequiredDetail>(AUTH_REQUIRED_EVENT, {
      detail: { next },
    }),
  )
}

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers)
  const hasBody = init?.body !== undefined && init.body !== null
  if (hasBody && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  if (!headers.has('Accept')) {
    headers.set('Accept', 'application/json')
  }

  let res: Response
  try {
    res = await fetch(BASE + path, { ...init, headers })
  } catch (err) {
    throw new ApiError(0, 'network', err instanceof Error ? err.message : 'network error')
  }

  if (res.status === 204) {
    return undefined as T
  }

  const contentType = res.headers.get('Content-Type') ?? ''
  const isJson = contentType.includes('application/json')

  if (!res.ok) {
    if (res.status === 401 && !isAuthEndpoint(path)) {
      emitAuthRequired()
    }
    if (isJson) {
      const body = (await res.json().catch(() => null)) as ApiErrorBody | null
      if (body && typeof body.error === 'string' && typeof body.code === 'string') {
        throw new ApiError(res.status, body.code, body.error, body.public_path)
      }
    }
    const fallback = await res.text().catch(() => '')
    throw new ApiError(res.status, 'http_error', fallback || `HTTP ${res.status}`)
  }

  if (!isJson) {
    throw new ApiError(res.status, 'unexpected_content_type', `expected JSON, got "${contentType}"`)
  }
  return (await res.json()) as T
}

// Mirrors backend/internal/api/search.go's searchHit. `snippet` contains
// literal `<mark>…</mark>` delimiters around matched tokens; the body is NOT
// HTML-escaped server-side, so it must be rendered via HighlightedSnippet
// (split-and-emit) rather than dangerouslySetInnerHTML.
export interface SearchResult {
  page_id: number
  space_id: number
  title: string
  snippet: string
  breadcrumb: string[]
  // True when the hit is reachable only because its space is published — the
  // caller holds no membership, so it must open in the no-login reader; the
  // authed route would 403.
  public: boolean
}

export function searchPages(
  q: string,
  signal?: AbortSignal,
): Promise<{ results: SearchResult[] }> {
  return api<{ results: SearchResult[] }>(
    `/api/search?q=${encodeURIComponent(q)}`,
    { signal },
  )
}

// A semantic (RAG) chunk hit — meaning-aware, chunk-level. heading_path is the
// section breadcrumb; chunk_id can feed a future read_chunk. The server returns
// 503 when no embedder is configured (feature dark) — callers treat that as "no
// smart results", not an error.
export interface SemanticHit {
  chunk_id: number
  page_id: number
  space_id: number
  title: string
  heading_path: string
  snippet: string
  score: number
  updated_at: string
}

export function searchSemantic(
  q: string,
  signal?: AbortSignal,
): Promise<{ results: SemanticHit[] }> {
  return api<{ results: SemanticHit[] }>(
    `/api/rag/search?q=${encodeURIComponent(q)}&mode=hybrid`,
    { signal },
  )
}

// "Ask your docs" — POST /api/rag/ask. The backend retrieves the top hybrid
// chunks (scoped to the caller's space_access, same anti-leak path as
// searchSemantic), grounds an LLM prompt on them, and returns a generated
// answer plus the cited source chunks (same shape as SemanticHit, minus the
// search-only score/heading framing — it IS rag.Hit). Sources may be empty when
// retrieval found nothing, in which case the answer says so.
//
// 503 codes to handle as a tasteful "unavailable" state (not an error toast):
//   rag_disabled — no embedder configured (TELA_RAG_EMBED_URL unset)
//   llm_disabled — no managed AI / completion model configured
export const ASK_UNAVAILABLE_CODES = ['rag_disabled', 'llm_disabled'] as const

export interface AskAnswer {
  answer: string
  sources: SemanticHit[]
  // Weak retrieval: the answer is a best-effort from loosely-related excerpts and
  // already carries a CAUTION callout in its prose — the flag lets the view add a
  // quiet chip too. Absent (older payloads) reads as confident.
  low_confidence?: boolean
  // Up to 3 model-suggested next questions — the ask-first navigation thread.
  // Best-effort server-side, so may be absent or empty.
  followups?: string[]
}

export function askDocs(
  body: { question: string; space_id?: number },
  signal?: AbortSignal,
): Promise<AskAnswer> {
  return api<AskAnswer>('/api/rag/ask', {
    method: 'POST',
    body: JSON.stringify(body),
    signal,
  })
}

// Streaming "Ask your docs" — POST /api/rag/ask/stream (SSE). Same retrieval and
// answer as askDocs, but the answer streams token-by-token so the UI renders it
// live AND the connection stays continuously active (a slow model can't trip the
// idle/proxy timeout the blocking endpoint hit). Raw fetch + ReadableStream, not
// api() — that wrapper assumes one JSON body. Events: sources → token* →
// (followups) → done, or a single error frame on a mid-stream failure. Clean HTTP
// errors (503/429/…) are thrown as ApiError before the stream starts, so the
// caller's existing ASK_UNAVAILABLE_CODES / model-unreachable handling still works.
export interface AskStreamHandlers {
  // onMeta carries the server's resume id (a `meta` event, sent first). The hook
  // stashes it so a dropped connection can reconnect via attachAskStream.
  onMeta?: (id: string) => void
  onSources?: (sources: SemanticHit[], lowConfidence: boolean) => void
  onToken?: (text: string) => void
  onFollowups?: (followups: string[]) => void
  onDone?: () => void
  onError?: (err: ApiError) => void
}

const ASK_STREAM_PATH = '/api/rag/ask/stream'

// askDocsStream starts a new streamed ask (POST) and pumps its SSE events to the
// handlers. The first `meta` event gives a resume id; if the connection later
// drops mid-answer, the caller reconnects with attachAskStream(id).
export async function askDocsStream(
  body: { question: string; space_id?: number },
  handlers: AskStreamHandlers,
  signal?: AbortSignal,
): Promise<void> {
  let res: Response
  try {
    res = await fetch(BASE + ASK_STREAM_PATH, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
      body: JSON.stringify(body),
      signal,
    })
  } catch (err) {
    if (signal?.aborted) return
    throw new ApiError(0, 'network', err instanceof Error ? err.message : 'network error')
  }
  await consumeAskStream(res, handlers, signal)
}

// attachAskStream re-attaches to an in-flight/just-finished ask by its resume id
// (GET). The server replays the whole event log from the start, so the caller
// resets its accumulated answer first and rebuilds from the replay. A 404 means
// the job expired or never existed — surfaced as an ApiError like any other.
export async function attachAskStream(
  id: string,
  handlers: AskStreamHandlers,
  signal?: AbortSignal,
): Promise<void> {
  let res: Response
  try {
    res = await fetch(BASE + ASK_STREAM_PATH + '?id=' + encodeURIComponent(id), {
      method: 'GET',
      headers: { Accept: 'text/event-stream' },
      signal,
    })
  } catch (err) {
    if (signal?.aborted) return
    throw new ApiError(0, 'network', err instanceof Error ? err.message : 'network error')
  }
  await consumeAskStream(res, handlers, signal)
}

// consumeAskStream is the shared SSE pump for both the initial POST and the
// reconnect GET: validate the response, then dispatch each blank-line-delimited
// frame as it arrives. A torn-down read (status 0) is what backgrounded mobile
// Safari produces; the caller treats it as a reconnect trigger, not a failure.
async function consumeAskStream(
  res: Response,
  handlers: AskStreamHandlers,
  signal?: AbortSignal,
): Promise<void> {
  if (!res.ok || !res.body) {
    // A clean HTTP error landed before the stream opened (disabled/rate-limit/etc).
    let code = 'http_error'
    let message = `HTTP ${res.status}`
    if ((res.headers.get('Content-Type') ?? '').includes('application/json')) {
      const b = (await res.json().catch(() => null)) as ApiErrorBody | null
      if (b && typeof b.error === 'string' && typeof b.code === 'string') {
        code = b.code
        message = b.error
      }
    }
    if (res.status === 401 && !isAuthEndpoint(ASK_STREAM_PATH)) emitAuthRequired()
    throw new ApiError(res.status, code, message)
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      buf += decoder.decode(value, { stream: true })
      // SSE frames are blank-line delimited; dispatch each complete one.
      let sep: number
      while ((sep = buf.indexOf('\n\n')) !== -1) {
        dispatchSSE(buf.slice(0, sep), handlers)
        buf = buf.slice(sep + 2)
      }
    }
  } catch (err) {
    if (signal?.aborted) return
    throw new ApiError(0, 'network', err instanceof Error ? err.message : 'stream error')
  }
}

function dispatchSSE(frame: string, h: AskStreamHandlers): void {
  let event = ''
  let data = ''
  for (const line of frame.split('\n')) {
    if (line.startsWith('event:')) event = line.slice(6).trim()
    else if (line.startsWith('data:')) data += line.slice(5).trim()
  }
  if (!event) return
  let parsed: Record<string, unknown>
  try {
    parsed = data ? (JSON.parse(data) as Record<string, unknown>) : {}
  } catch {
    return
  }
  switch (event) {
    case 'meta':
      if (typeof parsed.id === 'string') h.onMeta?.(parsed.id)
      break
    case 'sources':
      h.onSources?.((parsed.sources as SemanticHit[]) ?? [], !!parsed.low_confidence)
      break
    case 'token':
      if (typeof parsed.t === 'string') h.onToken?.(parsed.t)
      break
    case 'followups':
      h.onFollowups?.((parsed.followups as string[]) ?? [])
      break
    case 'error':
      h.onError?.(new ApiError(502, (parsed.code as string) ?? 'completion_failed', 'generation failed'))
      break
    case 'done':
      h.onDone?.()
      break
  }
}
