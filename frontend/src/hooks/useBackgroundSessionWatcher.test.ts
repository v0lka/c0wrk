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

const reportDroppedEventMock = vi.fn()

vi.mock('@/api/runtime', () => ({
  onSessionEvent: onSessionEventMock,
  reportDroppedEvent: (...args: unknown[]) => reportDroppedEventMock(...args),
}))

// --- Mock sound + notification cue wiring ---
// The background watcher routes events through the SAME event→sound mapping
// the active-session hook uses (classifySessionEvent → playSound), and the
// SAME notification dispatch (notifySessionCue → sendSystemNotification). We
// spy on both so a test can assert the wiring independently of the mapping
// logic (which is unit-tested in useSoundEvents.test.ts).

const playSoundMock = vi.fn()
vi.mock('@/lib/sound', () => ({
  playSound: (...args: unknown[]) => playSoundMock(...args),
}))

const classifySessionEventMock = vi.fn((..._args: unknown[]): string | null => 'attention')
const notifySessionCueMock = vi.fn()
vi.mock('@/hooks/events/useSoundEvents', () => ({
  classifySessionEvent: (...args: unknown[]) => classifySessionEventMock(...args),
  notifySessionCue: (...args: unknown[]) => notifySessionCueMock(...args),
}))

// --- Mock HITL / goal handlers (no-ops) ---
// The watcher delegates pending-action message creation to these; for the
// sound-wiring tests we only care that the watcher reached the handler, so we
// stub them and assert on the spied handler mocks below.

const handleToolConfirmEventMock = vi.fn()
const handleAskUserEventMock = vi.fn()
const handleStepLimitEventMock = vi.fn()
const handlePlanReviewEventMock = vi.fn()
vi.mock('@/hooks/events/hitlHandlers', () => ({
  handleToolConfirmEvent: (...args: unknown[]) => handleToolConfirmEventMock(...args),
  handleAskUserEvent: (...args: unknown[]) => handleAskUserEventMock(...args),
  handleStepLimitEvent: (...args: unknown[]) => handleStepLimitEventMock(...args),
  handlePlanReviewEvent: (...args: unknown[]) => handlePlanReviewEventMock(...args),
}))

const handleGoalProposalEventMock = vi.fn()
const handleGoalStatusEventMock = vi.fn()
const handleGoalProgressEventMock = vi.fn()
vi.mock('@/hooks/events/goalHandlers', () => ({
  handleGoalProposalEvent: (...args: unknown[]) => handleGoalProposalEventMock(...args),
  handleGoalStatusEvent: (...args: unknown[]) => handleGoalStatusEventMock(...args),
  handleGoalProgressEvent: (...args: unknown[]) => handleGoalProgressEventMock(...args),
}))

// --- Mock chat store ---
// A plain object that doubles as a hook (calls selector with state) and
// exposes getState/setState so the hook's imperative calls work.

const chatStoreState = {
  taskActive: {} as Record<string, boolean>,
  unfinishedTaskStatus: {} as Record<string, string>,
  activityStatus: {} as Record<string, string>,
  streamingText: {} as Record<string, string>,
  paused: {} as Record<string, boolean>,
  pausing: {} as Record<string, boolean>,
  compacting: {} as Record<string, boolean>,
  setCompacting: (sid: string, compacting: boolean) => {
    if (compacting) {
      chatStoreState.compacting = { ...chatStoreState.compacting, [sid]: true }
    } else {
      delete chatStoreState.compacting[sid]
    }
  },
  setTaskActive: (sid: string, active: boolean) => {
    chatStoreState.taskActive = { ...chatStoreState.taskActive, [sid]: active }
  },
  setUnfinishedTaskStatus: (sid: string, status: string) => {
    chatStoreState.unfinishedTaskStatus = { ...chatStoreState.unfinishedTaskStatus, [sid]: status }
  },
  setPaused: (sid: string, paused: boolean) => {
    if (paused) {
      chatStoreState.paused = { ...chatStoreState.paused, [sid]: true }
    } else {
      delete chatStoreState.paused[sid]
    }
  },
  setPausing: (sid: string, pausing: boolean) => {
    if (pausing) {
      chatStoreState.pausing = { ...chatStoreState.pausing, [sid]: true }
    } else {
      delete chatStoreState.pausing[sid]
    }
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

// --- Mock active-sessions store ---
// The watcher folds the authoritative DB snapshot (sessions the backend still
// reports unfinished) into its watch set, so a reload that empties chatStore
// no longer blinds it. Keep the real store out of this node-environment test
// (it pulls the RPC layer) and expose the hook/getState/setState shape the
// other store mocks use.

const activeSessionsStoreState = {
  sessions: null as ReadonlyArray<{ id: string; unfinished_task_status?: string }> | null,
}

const useActiveSessionsStoreMock = Object.assign(
  vi.fn((selector: (s: typeof activeSessionsStoreState) => unknown) => selector(activeSessionsStoreState)),
  {
    getState: () => activeSessionsStoreState,
    setState: (partial: Partial<typeof activeSessionsStoreState>) => Object.assign(activeSessionsStoreState, partial),
  },
)

vi.mock('@/stores/activeSessionsStore', () => ({ useActiveSessionsStore: useActiveSessionsStoreMock }))

// Import AFTER mocks are set up.
const { useBackgroundSessionWatcher } = await import('@/hooks/useBackgroundSessionWatcher')

/** Reset per-test mock state. */
function resetMockState(): void {
  subscriptions.clear()
  onSessionEventMock.mockClear()
  reportDroppedEventMock.mockClear()
  playSoundMock.mockClear()
  classifySessionEventMock.mockClear()
  notifySessionCueMock.mockClear()
  handleToolConfirmEventMock.mockClear()
  handleAskUserEventMock.mockClear()
  handleStepLimitEventMock.mockClear()
  handlePlanReviewEventMock.mockClear()
  handleGoalProposalEventMock.mockClear()
  handleGoalStatusEventMock.mockClear()
  handleGoalProgressEventMock.mockClear()
  effectCleanup = undefined
  lastDeps = undefined
}

function resetStores(): void {
  chatStoreState.taskActive = {}
  chatStoreState.unfinishedTaskStatus = {}
  chatStoreState.activityStatus = {}
  chatStoreState.streamingText = {}
  chatStoreState.paused = {}
  chatStoreState.pausing = {}
  chatStoreState.compacting = {}
  sessionStoreState.activeSessionId = null
  activeSessionsStoreState.sessions = null
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
    expect(subscriptions.has('bg-1:session_paused')).toBe(true)
    expect(subscriptions.has('bg-1:session_resumed')).toBe(true)
    expect(subscriptions.has('bg-1:tool_confirm')).toBe(true)
    expect(subscriptions.has('bg-1:step_limit')).toBe(true)
    expect(subscriptions.has('bg-1:plan_review_ready')).toBe(true)
    expect(subscriptions.has('bg-1:goal_proposal')).toBe(true)
    expect(subscriptions.has('bg-1:goal_status')).toBe(true)
    expect(subscriptions.has('bg-1:goal_progress')).toBe(true)
    expect(subscriptions.has('bg-1:ask_user')).toBe(true)
  })

  it('routes a background goal_status/goal_progress to the shared goal handlers (status-bar badge parity)', () => {
    // Regression: without these subscriptions the goal store was only written
    // by the ACTIVE session's useGoalEvents, so a session whose goal advanced
    // while backgrounded had no live goal state — the status-bar goal badge
    // (GoalStatusIndicator) then showed nothing (or a stale turn) on switch
    // until the session emitted its NEXT goal update.
    chatStoreState.setTaskActive('bg-1', true)
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()

    const status = { status: 'active', turn: 2, condition: 'ship it', max_turns: 5, created_at: 1000 }
    fireSessionEvent('bg-1', 'goal_status', status)
    expect(handleGoalStatusEventMock).toHaveBeenCalledWith('bg-1', status)

    const progress = { turn: 3, max_turns: 5, condition: 'ship it' }
    fireSessionEvent('bg-1', 'goal_progress', progress)
    expect(handleGoalProgressEventMock).toHaveBeenCalledWith('bg-1', progress)

    // A malformed payload is dropped, never handed to the handler.
    fireSessionEvent('bg-1', 'goal_status', { status: 'active' })
    expect(handleGoalStatusEventMock).toHaveBeenCalledTimes(1)
    expect(reportDroppedEventMock).toHaveBeenCalledWith('goal_status', { status: 'active' })
  })

  it('does not subscribe to the active session (handled by useChatEvents)', () => {
    chatStoreState.setTaskActive('active-1', true)
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()

    expect(subscriptions.has('active-1:task_complete')).toBe(false)
    expect(subscriptions.has('active-1:task_cancelled')).toBe(false)
    expect(subscriptions.has('active-1:error')).toBe(false)
    expect(subscriptions.has('active-1:session_paused')).toBe(false)
    expect(subscriptions.has('active-1:session_resumed')).toBe(false)
    expect(subscriptions.has('active-1:tool_confirm')).toBe(false)
    expect(subscriptions.has('active-1:step_limit')).toBe(false)
    expect(subscriptions.has('active-1:plan_review_ready')).toBe(false)
    expect(subscriptions.has('active-1:ask_user')).toBe(false)
    expect(subscriptions.has('active-1:goal_proposal')).toBe(false)
    // Goal status/progress parity: the ACTIVE session's goal events are owned
    // by useGoalEvents (mounted per session), never the background watcher.
    expect(subscriptions.has('active-1:goal_status')).toBe(false)
    expect(subscriptions.has('active-1:goal_progress')).toBe(false)
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
    // 13 events × 2 sessions = 26 subscriptions
    expect(subscriptions.size).toBe(26)
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

  it('mirrors the unfinished-task overlay: cleared on completion, re-armed on resumable failure', () => {
    chatStoreState.setTaskActive('bg-1', true)
    sessionStoreState.activeSessionId = 'active-1'
    useRenderWatcher()

    fireSessionEvent('bg-1', 'task_complete', { output: 'done', success: true })
    expect(chatStoreState.unfinishedTaskStatus['bg-1']).toBe('')

    // A degraded completion stays resumable: the backend's follow-up event
    // re-arms the live overlay so every status dot repaints red and
    // archive/delete keeps protecting the task.
    fireSessionEvent('bg-1', 'task_failed_resumable', { task_id: 't1' })
    expect(chatStoreState.unfinishedTaskStatus['bg-1']).toBe('failed')
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

  it('transitions a background pause-in-flight session to paused at the checkpoint', () => {
    chatStoreState.setTaskActive('bg-1', true)
    chatStoreState.setPausing('bg-1', true)
    chatStoreState.setActivityStatus('bg-1', 'Pausing')
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()
    fireSessionEvent('bg-1', 'session_paused')

    expect(chatStoreState.pausing['bg-1']).toBeUndefined()
    expect(chatStoreState.paused['bg-1']).toBe(true)
    expect(chatStoreState.taskActive['bg-1']).toBe(false)
    expect(chatStoreState.activityStatus['bg-1']).toBe('Paused')
  })

  it('keeps a paused background session subscribed and transitions it on resume', () => {
    chatStoreState.setTaskActive('bg-1', false)
    chatStoreState.setPaused('bg-1', true)
    chatStoreState.setActivityStatus('bg-1', 'Paused')
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()
    expect(subscriptions.has('bg-1:session_resumed')).toBe(true)
    fireSessionEvent('bg-1', 'session_resumed')

    expect(chatStoreState.pausing['bg-1']).toBeUndefined()
    expect(chatStoreState.paused['bg-1']).toBeUndefined()
    expect(chatStoreState.taskActive['bg-1']).toBe(true)
    expect(chatStoreState.activityStatus['bg-1']).toBe('Resuming...')
  })

  it('unsubscribes when a session is no longer running', () => {
    chatStoreState.setTaskActive('bg-1', true)
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()
    expect(subscriptions.size).toBe(13)

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

  // --- Authoritative snapshot fallback: sessions the backend still reports
  // unfinished are watched even when chatStore has no local flag for them
  // (reload, or a session in a project the user has not opened).

  it('watches a snapshot session chatStore has no flag for and cues its completion (reload path)', () => {
    // chatStore maps are empty — a fresh webview reload. Only the DB snapshot
    // knows the task is still running.
    activeSessionsStoreState.sessions = [{ id: 'reload-1', unfinished_task_status: 'in_progress' }]
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()

    expect(subscriptions.has('reload-1:task_complete')).toBe(true)
    expect(subscriptions.has('reload-1:error')).toBe(true)

    fireSessionEvent('reload-1', 'task_complete', { output: 'done', success: true })

    expect(playSoundMock).toHaveBeenCalledWith('attention')
    // The completion still finalizes the (chatStore-unknown) session's state.
    expect(chatStoreState.taskActive['reload-1']).toBe(false)
  })

  it('keeps the active session excluded from the watcher even when the snapshot reports it unfinished', () => {
    activeSessionsStoreState.sessions = [{ id: 'active-1', unfinished_task_status: 'in_progress' }]
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()

    // No double signal: the active session's cues belong to useSoundEvents.
    expect(subscriptions.size).toBe(0)
  })

  it('watches a snapshot-paused session so a later resume is followed', () => {
    activeSessionsStoreState.sessions = [{ id: 'reload-paused', unfinished_task_status: 'paused' }]
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()

    expect(subscriptions.has('reload-paused:session_resumed')).toBe(true)
  })

  it('does not watch snapshot sessions with no unfinished task (idle / failed)', () => {
    activeSessionsStoreState.sessions = [
      { id: 'idle-1', unfinished_task_status: '' },
      { id: 'failed-1', unfinished_task_status: 'failed' },
      { id: 'missing-status' },
    ]
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()

    expect(subscriptions.size).toBe(0)
  })

  it('subscribes once the snapshot arrives after mount (async load, no chatStore change)', () => {
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()
    expect(subscriptions.size).toBe(0)

    // The loader (App root) fills the snapshot after this hook mounted.
    activeSessionsStoreState.sessions = [{ id: 'bg-1', unfinished_task_status: 'in_progress' }]
    useRenderWatcher()

    expect(subscriptions.has('bg-1:task_complete')).toBe(true)
  })

  it('unions chatStore flags with the snapshot without duplicating subscriptions', () => {
    chatStoreState.setTaskActive('bg-1', true)
    activeSessionsStoreState.sessions = [
      { id: 'bg-1', unfinished_task_status: 'in_progress' },
      { id: 'bg-2', unfinished_task_status: 'in_progress' },
    ]
    sessionStoreState.activeSessionId = 'active-1'

    useRenderWatcher()

    // 13 events × 2 unique sessions = 26 — the shadowed bg-1 is not counted twice.
    expect(subscriptions.size).toBe(26)
  })

  // --- Sound parity: a background session gets the same audible cues the
  // active session would, routed through the shared classifySessionEvent map.

  it('plays the cue when a background task completes (consults classifySessionEvent → playSound)', () => {
    chatStoreState.setTaskActive('bg-1', true)
    sessionStoreState.activeSessionId = 'active-1'
    useRenderWatcher()

    fireSessionEvent('bg-1', 'task_complete', { output: 'done', success: true })

    expect(classifySessionEventMock).toHaveBeenCalledWith('task_complete', { output: 'done', success: true })
    expect(playSoundMock).toHaveBeenCalledWith('attention')
  })

  it('plays the cue on task_cancelled and error', () => {
    chatStoreState.setTaskActive('bg-1', true)
    sessionStoreState.activeSessionId = 'active-1'
    useRenderWatcher()

    fireSessionEvent('bg-1', 'task_cancelled')
    expect(playSoundMock).toHaveBeenCalledTimes(1)

    fireSessionEvent('bg-1', 'error', { error: 'boom' })
    expect(playSoundMock).toHaveBeenCalledTimes(2)
  })

  it('does not play a cue when classifySessionEvent maps the event to silence', () => {
    chatStoreState.setTaskActive('bg-1', true)
    sessionStoreState.activeSessionId = 'active-1'
    classifySessionEventMock.mockReturnValueOnce(null)
    useRenderWatcher()

    fireSessionEvent('bg-1', 'task_complete', { success: true })

    expect(classifySessionEventMock).toHaveBeenCalled()
    expect(playSoundMock).not.toHaveBeenCalled()
  })

  it('plays the cue for a HITL event only when the payload is valid (dropped events stay silent)', () => {
    chatStoreState.setTaskActive('bg-1', true)
    sessionStoreState.activeSessionId = 'active-1'
    useRenderWatcher()

    // Invalid payload → dropped, no handler, NO cue (tone or banner).
    fireSessionEvent('bg-1', 'tool_confirm', { not: 'valid' })
    expect(reportDroppedEventMock).toHaveBeenCalledWith('tool_confirm', { not: 'valid' })
    expect(playSoundMock).not.toHaveBeenCalled()
    expect(notifySessionCueMock).not.toHaveBeenCalled()
    expect(handleToolConfirmEventMock).not.toHaveBeenCalled()

    // Valid payload → handler invoked AND both cues sent.
    fireSessionEvent('bg-1', 'tool_confirm', { confirm_id: 'c1', tool: 'bash' })
    expect(handleToolConfirmEventMock).toHaveBeenCalledWith('bg-1', { confirm_id: 'c1', tool: 'bash' })
    expect(playSoundMock).toHaveBeenCalledWith('attention')
    expect(notifySessionCueMock).toHaveBeenCalledWith('tool_confirm', { confirm_id: 'c1', tool: 'bash' }, { sessionId: 'bg-1' })
  })

  // --- Notification parity: a background session raises the same system
  // banner the active session would, through the shared notifySessionCue
  // dispatch, with that session's id as context (so the banner names it).

  it('sends the notification when a background task completes (notifySessionCue with session context)', () => {
    chatStoreState.setTaskActive('bg-1', true)
    sessionStoreState.activeSessionId = 'active-1'
    useRenderWatcher()

    fireSessionEvent('bg-1', 'task_complete', { output: 'done', success: true })

    expect(notifySessionCueMock).toHaveBeenCalledWith('task_complete', { output: 'done', success: true }, { sessionId: 'bg-1' })
  })

  it('sends the notification on task_cancelled, error, and task_failed_resumable', () => {
    chatStoreState.setTaskActive('bg-1', true)
    sessionStoreState.activeSessionId = 'active-1'
    useRenderWatcher()

    fireSessionEvent('bg-1', 'task_cancelled')
    expect(notifySessionCueMock).toHaveBeenCalledWith('task_cancelled', undefined, { sessionId: 'bg-1' })

    fireSessionEvent('bg-1', 'error', { error: 'boom' })
    expect(notifySessionCueMock).toHaveBeenCalledWith('error', { error: 'boom' }, { sessionId: 'bg-1' })

    fireSessionEvent('bg-1', 'task_failed_resumable', { message: 'died at step 3' })
    expect(notifySessionCueMock).toHaveBeenCalledWith('task_failed_resumable', { message: 'died at step 3' }, { sessionId: 'bg-1' })
  })

  it('sends no notification when the payload guard drops a HITL event (no banner without an action)', () => {
    chatStoreState.setTaskActive('bg-1', true)
    sessionStoreState.activeSessionId = 'active-1'
    useRenderWatcher()

    fireSessionEvent('bg-1', 'ask_user', { not: 'valid' })
    expect(reportDroppedEventMock).toHaveBeenCalledWith('ask_user', { not: 'valid' })
    expect(notifySessionCueMock).not.toHaveBeenCalled()
  })

  it('does not send a notification for a non-cued lifecycle event (session_paused / session_resumed)', () => {
    chatStoreState.setTaskActive('bg-1', true)
    sessionStoreState.activeSessionId = 'active-1'
    useRenderWatcher()

    fireSessionEvent('bg-1', 'session_paused')
    fireSessionEvent('bg-1', 'session_resumed')

    expect(notifySessionCueMock).not.toHaveBeenCalled()
    expect(playSoundMock).not.toHaveBeenCalled()
  })
})
