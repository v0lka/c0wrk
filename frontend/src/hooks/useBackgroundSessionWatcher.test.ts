import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'

// --- Mock react (only useEffect is used by the hook) ---

let effectCleanup: (() => void) | undefined
let lastDeps: unknown[] | undefined

vi.mock('react', () => ({
  useEffect: (fn: () => void | (() => void), deps: unknown[]) => {
    const prev = lastDeps
    if (!prev) {
      effectCleanup = fn() ?? undefined
      lastDeps = deps
      return
    }
    const changed = deps.some((d, i) => d !== prev[i])
    if (changed) {
      if (effectCleanup) effectCleanup()
      effectCleanup = fn() ?? undefined
      lastDeps = deps
    }
  },
}))

// --- Mock @/api/runtime ---

const subscriptions = new Map<string, (...args: unknown[]) => void>()

const onSessionEventMock = vi.fn((sessionId: string, event: string, callback: (...args: unknown[]) => void) => {
  const key = `${sessionId}:${event}`
  subscriptions.set(key, callback)
  return () => { subscriptions.delete(key) }
})

vi.mock('@/api/runtime', () => ({
  onSessionEvent: onSessionEventMock,
}))

// --- Mock chat store ---
// A plain object that doubles as a hook (calls selector with state) and
// exposes getState/setState so the hook's imperative calls work.

const chatStoreState = {
  taskActive: {} as Record<string, boolean>,
  activityStatus: {} as Record<string, string>,
  streamingText: {} as Record<string, string>,
  setTaskActive: (sid: string, active: boolean) => {
    chatStoreState.taskActive = { ...chatStoreState.taskActive, [sid]: active }
  },
  setActivityStatus: (sid: string, status: string | null) => {
    if (status === null) {
      delete chatStoreState.activityStatus[sid]
    } else {
      chatStoreState.activityStatus = { ...chatStoreState.activityStatus, [sid]: status }
    }
  },
  clearStreamingText: (sid: string) => {
    delete chatStoreState.streamingText[sid]
  },
}

const useChatStoreMock = Object.assign(
  vi.fn((selector: (s: typeof chatStoreState) => unknown) => selector(chatStoreState)),
  {
    getState: () => chatStoreState,
    setState: (partial: Partial<typeof chatStoreState>) => Object.assign(chatStoreState, partial),
  },
)

vi.mock('@/stores/chatStore', () => ({ useChatStore: useChatStoreMock }))

// --- Mock session store ---

const sessionStoreState = {
  activeSessionId: null as string | null,
}

const useSessionStoreMock = Object.assign(
  vi.fn((selector: (s: typeof sessionStoreState) => unknown) => selector(sessionStoreState)),
  {
    getState: () => sessionStoreState,
    setState: (partial: Partial<typeof sessionStoreState>) => Object.assign(sessionStoreState, partial),
  },
)

vi.mock('@/stores/sessionStore', () => ({ useSessionStore: useSessionStoreMock }))

// Import AFTER mocks are set up.
const { useBackgroundSessionWatcher } = await import('@/hooks/useBackgroundSessionWatcher')

function resetMockState(): void {
  subscriptions.clear()
  onSessionEventMock.mockClear()
  effectCleanup = undefined
  lastDeps = undefined
}

function resetStores(): void {
  chatStoreState.taskActive = {}
  chatStoreState.activityStatus = {}
  chatStoreState.streamingText = {}
  sessionStoreState.activeSessionId = null
}

/** Call the hook (simulates a render). Named with `use` prefix to satisfy
 * react-hooks/rules-of-hooks since it wraps a hook call. */
function useRenderWatcher(): void {
  useBackgroundSessionWatcher()
}

/** Fire a session event for a given session + event type. */
function fireSessionEvent(sessionId: string, event: string, data?: unknown): void {
  const key = `${sessionId}:${event}`
  const cb = subscriptions.get(key)
  if (!cb) throw new Error(`No subscription for ${key}`)
  cb(data as never)
}

describe('useBackgroundSessionWatcher', () => {
  beforeEach(() => {
    resetMockState()
    resetStores()
  })

  afterEach(() => {
    if (effectCleanup) effectCleanup()
  })

  it('subscribes to task_complete, task_cancelled, error, and HITL events for running background sessions', () => {
    chatStoreState.setTaskActive('bg-1', true)
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()

    expect(subscriptions.has('bg-1:task_complete')).toBe(true)
    expect(subscriptions.has('bg-1:task_cancelled')).toBe(true)
    expect(subscriptions.has('bg-1:error')).toBe(true)
    expect(subscriptions.has('bg-1:tool_confirm')).toBe(true)
    expect(subscriptions.has('bg-1:step_limit')).toBe(true)
    expect(subscriptions.has('bg-1:plan_review_ready')).toBe(true)
    expect(subscriptions.has('bg-1:goal_proposal')).toBe(true)
    expect(subscriptions.has('bg-1:ask_user')).toBe(true)
  })

  it('does not subscribe to the active session (handled by useChatEvents)', () => {
    chatStoreState.setTaskActive('active-1', true)
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()

    expect(subscriptions.has('active-1:task_complete')).toBe(false)
    expect(subscriptions.has('active-1:task_cancelled')).toBe(false)
    expect(subscriptions.has('active-1:error')).toBe(false)
    expect(subscriptions.has('active-1:tool_confirm')).toBe(false)
    expect(subscriptions.has('active-1:step_limit')).toBe(false)
    expect(subscriptions.has('active-1:plan_review_ready')).toBe(false)
    expect(subscriptions.has('active-1:ask_user')).toBe(false)
    expect(subscriptions.has('active-1:goal_proposal')).toBe(false)
  })

  it('does not subscribe to sessions with taskActive === false', () => {
    chatStoreState.setTaskActive('idle-1', false)
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()

    expect(subscriptions.size).toBe(0)
  })

  it('subscribes to multiple running background sessions', () => {
    chatStoreState.setTaskActive('bg-1', true)
    chatStoreState.setTaskActive('bg-2', true)
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()

    expect(subscriptions.has('bg-1:task_complete')).toBe(true)
    expect(subscriptions.has('bg-2:task_complete')).toBe(true)
    // 8 events × 2 sessions = 16 subscriptions
    expect(subscriptions.size).toBe(16)
  })

  it('resets taskActive to false on task_complete without touching the active session state', () => {
    chatStoreState.setTaskActive('bg-1', true)
    // activityStatus is now per-session; the active session's status must be
    // untouched by a background session's completion.
    chatStoreState.setActivityStatus('active-1', 'Processing...')
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()
    expect(chatStoreState.taskActive['bg-1']).toBe(true)

    fireSessionEvent('bg-1', 'task_complete', { output: 'done', success: true })

    expect(chatStoreState.taskActive['bg-1']).toBe(false)
    // The background completion must NOT clear the active session's activity
    // indicator (cross-session global-state contamination).
    expect(chatStoreState.activityStatus['active-1']).toBe('Processing...')
  })

  it('resets taskActive to false on task_cancelled', () => {
    chatStoreState.setTaskActive('bg-1', true)
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()

    fireSessionEvent('bg-1', 'task_cancelled')

    expect(chatStoreState.taskActive['bg-1']).toBe(false)
  })

  it('resets taskActive to false on error', () => {
    chatStoreState.setTaskActive('bg-1', true)
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()

    fireSessionEvent('bg-1', 'error', { error: 'something went wrong' })

    expect(chatStoreState.taskActive['bg-1']).toBe(false)
  })

  it('finalizes only the completing background session, leaving the active session untouched', () => {
    chatStoreState.setTaskActive('bg-1', true)
    // Per-session buffers: active-1 holds the live stream; bg-1 holds a stale
    // partial left over from before it went to the background.
    chatStoreState.streamingText = { 'active-1': 'active-1 partial response', 'bg-1': 'bg-1 stale partial' }
    chatStoreState.activityStatus = { 'active-1': 'Generating response...', 'bg-1': 'Generating response...' }
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()

    fireSessionEvent('bg-1', 'task_complete', { output: 'done', success: true })

    // bg-1's OWN ephemeral state is finalized — no stale partial lingers to
    // surface when the user later switches to bg-1.
    expect(chatStoreState.streamingText['bg-1']).toBeUndefined()
    expect(chatStoreState.activityStatus['bg-1']).toBeUndefined()
    expect(chatStoreState.taskActive['bg-1']).toBe(false)
    // The ACTIVE session is completely untouched — no cross-session contamination.
    expect(chatStoreState.streamingText['active-1']).toBe('active-1 partial response')
    expect(chatStoreState.activityStatus['active-1']).toBe('Generating response...')
  })

  it('unsubscribes when a session is no longer running', () => {
    chatStoreState.setTaskActive('bg-1', true)
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()
    expect(subscriptions.size).toBe(8)

    // Session completes via another path.
    chatStoreState.setTaskActive('bg-1', false)

    // Re-render — watcher detects bg-1 is no longer running, cleans up.
    useRenderWatcher()

    expect(subscriptions.size).toBe(0)
  })

  it('does not re-subscribe when the watched set has not changed', () => {
    chatStoreState.setTaskActive('bg-1', true)
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()
    const initialCallCount = onSessionEventMock.mock.calls.length

    // Re-render with no change to the watched set.
    useRenderWatcher()

    expect(onSessionEventMock.mock.calls.length).toBe(initialCallCount)
  })

  it('re-subscribes when the active session changes and a previously active session becomes background', () => {
    chatStoreState.setTaskActive('sess-a', true)
    chatStoreState.setTaskActive('sess-b', true)
    sessionStoreState.activeSessionId = 'sess-a'

    useRenderWatcher()

    // Only sess-b should be watched (sess-a is active).
    expect(subscriptions.has('sess-b:task_complete')).toBe(true)
    expect(subscriptions.has('sess-a:task_complete')).toBe(false)

    // User switches to sess-b — sess-a becomes a background running session.
    sessionStoreState.activeSessionId = 'sess-b'
    useRenderWatcher()

    expect(subscriptions.has('sess-a:task_complete')).toBe(true)
    expect(subscriptions.has('sess-b:task_complete')).toBe(false)
  })
})
