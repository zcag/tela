import { useMutation } from '@tanstack/react-query'
import { useCallback, useEffect, useRef, useState } from 'react'
import {
  ApiError,
  askDocs,
  askDocsStream,
  attachAskStream,
  type AskAnswer,
  type AskStreamHandlers,
  type SemanticHit,
} from '../api'

// "Ask your docs" mutation (non-streaming). Kept for any caller that wants the
// whole answer in one shot; the /ask view uses useAskDocsStream below.
//
// Errors surface as ApiError on `error`: the view treats the 503 codes
// (rag_disabled / llm_disabled, see ASK_UNAVAILABLE_CODES) as a tasteful
// "feature not available on this instance" state, everything else as a retry.
// retry:false so a dark/unconfigured instance isn't hammered.
export function useAskDocs() {
  return useMutation<AskAnswer, Error, { question: string; spaceId?: number }>({
    mutationFn: ({ question, spaceId }) =>
      askDocs({ question, space_id: spaceId }),
    retry: false,
  })
}

export type AskStatus = 'idle' | 'streaming' | 'done' | 'error'

interface AskStreamState {
  status: AskStatus
  answer: string
  sources: SemanticHit[]
  lowConfidence: boolean
  // Retrieval found `considered` distinct sources; truncated says the per-call
  // cap clipped them, so the citation list is a window, not the whole match set.
  considered: number
  truncated: boolean
  followups: string[]
  error: unknown
}

const IDLE: AskStreamState = {
  status: 'idle',
  answer: '',
  sources: [],
  lowConfidence: false,
  considered: 0,
  truncated: false,
  followups: [],
  error: null,
}

// Max automatic reconnects per ask, as a runaway guard. Reset to 0 whenever the
// reconnected stream makes progress (a fresh token/sources), so a long answer the
// user backgrounds repeatedly keeps resuming — only a tight failing loop is cut.
const MAX_RECONNECTS = 6

// Streaming "Ask your docs". react-query's mutation models a single resolved
// value, which doesn't fit a token stream — so this is a small state machine over
// askDocsStream: it accumulates the answer as tokens land, surfaces sources the
// moment they arrive (before generation), and aborts the in-flight stream when a
// new question starts or the view unmounts.
//
// Resilience to a dropped connection (the backgrounded-mobile-Safari case): the
// server runs generation as a detached job and hands back a resume id (the `meta`
// event). If the stream drops mid-answer, we reconnect by id and the server
// replays the whole event log — so we reset the accumulated answer and rebuild
// from the replay. While the tab is hidden (JS suspended) we can't reconnect, so
// we flag it and a visibilitychange listener fires the reconnect on return.
export function useAskDocsStream() {
  const [state, setState] = useState<AskStreamState>(IDLE)
  const abortRef = useRef<AbortController | null>(null)
  // Refs the async stream callbacks read synchronously (state is too laggy here).
  const phaseRef = useRef<AskStatus>('idle')
  const askIdRef = useRef<string | null>(null)
  const reconnectsRef = useRef(0)
  const wantReconnectRef = useRef(false)
  // Latest-ref to attemptReconnect so the reconnect's own failure handler can
  // re-enter it without a forward self-reference (assigned just below).
  const attemptReconnectRef = useRef<() => boolean>(() => false)

  const setPhase = (p: AskStatus) => {
    phaseRef.current = p
  }

  // streamHandlers wires SSE events into state. Shared by the initial stream and
  // every reconnect, so the replay rehydrates the same way the live stream filled.
  // A fresh token/sources resets the reconnect counter — progress means the loop
  // is healthy, so only a tight failing loop ever hits MAX_RECONNECTS.
  const streamHandlers = useCallback(
    (): AskStreamHandlers => ({
      onMeta: (id) => {
        askIdRef.current = id
      },
      onSources: ({ sources, lowConfidence, considered, truncated }) => {
        reconnectsRef.current = 0
        setState((s) => ({ ...s, sources, lowConfidence, considered, truncated }))
      },
      onToken: (t) => {
        reconnectsRef.current = 0
        setState((s) => ({ ...s, answer: s.answer + t }))
      },
      onFollowups: (followups) => setState((s) => ({ ...s, followups })),
      onError: (err) => {
        setPhase('error')
        setState((s) => ({ ...s, status: 'error', error: err }))
      },
      onDone: () =>
        setState((s) => {
          if (s.status === 'error') return s
          setPhase('done')
          return { ...s, status: 'done' }
        }),
    }),
    [],
  )

  // attemptReconnect re-attaches to the detached job by id, replaying from the
  // start. Returns true if it took ownership of recovery (reconnected, or deferred
  // until the tab is visible again); false means "give up, surface the error".
  const attemptReconnect = useCallback((): boolean => {
    if (phaseRef.current !== 'streaming' || !askIdRef.current) return false
    if (reconnectsRef.current >= MAX_RECONNECTS) return false
    // Hidden tab → JS can't run a fetch reliably; defer to the visibility listener.
    if (typeof document !== 'undefined' && document.visibilityState !== 'visible') {
      wantReconnectRef.current = true
      return true
    }
    reconnectsRef.current += 1
    const ctrl = new AbortController()
    abortRef.current = ctrl
    // Replay rebuilds the answer from scratch — clear it so tokens don't double up.
    setState((s) => ({ ...s, answer: '', followups: [] }))
    void attachAskStream(askIdRef.current, streamHandlers(), ctrl.signal).catch(
      (err: unknown) => {
        if (ctrl.signal.aborted) return
        if (!attemptReconnectRef.current()) {
          setPhase('error')
          setState((s) => ({ ...s, status: 'error', error: err }))
        }
      },
    )
    return true
  }, [streamHandlers])
  useEffect(() => {
    attemptReconnectRef.current = attemptReconnect
  }, [attemptReconnect])

  // Abort on unmount; reconnect when the tab returns to the foreground mid-answer.
  useEffect(() => {
    const onVisible = () => {
      if (
        document.visibilityState === 'visible' &&
        wantReconnectRef.current &&
        phaseRef.current === 'streaming'
      ) {
        wantReconnectRef.current = false
        attemptReconnect()
      }
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      document.removeEventListener('visibilitychange', onVisible)
      abortRef.current?.abort()
    }
  }, [attemptReconnect])

  const reset = useCallback(() => {
    abortRef.current?.abort()
    abortRef.current = null
    askIdRef.current = null
    wantReconnectRef.current = false
    setPhase('idle')
    setState(IDLE)
  }, [])

  const ask = useCallback(
    (question: string, spaceId?: number) => {
      abortRef.current?.abort()
      const ctrl = new AbortController()
      abortRef.current = ctrl
      askIdRef.current = null
      reconnectsRef.current = 0
      wantReconnectRef.current = false
      setPhase('streaming')
      setState({ ...IDLE, status: 'streaming' })
      void askDocsStream(
        { question, space_id: spaceId },
        streamHandlers(),
        ctrl.signal,
      ).catch((err: unknown) => {
        if (ctrl.signal.aborted) return
        // A torn-down connection (status 0) mid-answer → try to resume the detached
        // job; only a non-recoverable failure becomes a visible error.
        const recoverable = err instanceof ApiError && err.status === 0
        if (recoverable && attemptReconnect()) return
        setPhase('error')
        setState((s) => ({ ...s, status: 'error', error: err }))
      })
    },
    [streamHandlers, attemptReconnect],
  )

  return { ...state, ask, reset, isPending: state.status === 'streaming' }
}
