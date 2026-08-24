// Integration test: switching between two CONCURRENTLY-RUNNING sessions (A→B→A).
//
// This test does NOT import the real React/Wails/Zustand stack. Instead it
// faithfully simulates the THREE things that matter for this bug, using logic
// copied verbatim from the real source (cited inline):
//
//   1. The Wails v2.12 event runtime  — ported 1:1 from
//      internal/frontend/runtime/desktop/events.js (Listener ref-identity,
//      listenerOff filter-by-ref, notifyListeners slice+call+splice+replace).
//   2. React's passive-effect lifecycle — for a single commit, ALL cleanups
//      run in declaration order, THEN ALL setups run in declaration order;
//      an effect only re-runs when its deps changed (Object.is per element).
//   3. The store + hook orchestration logic — copied verbatim from
//      stores/chatStore.ts, hooks/useSessionEvents.ts, hooks/events/useChatEvents.ts,
//      hooks/useBackgroundSessionWatcher.ts, and the two ChatArea effects.

import { describe, it, expect, beforeEach, vi } from 'vitest'

// ──────────────────────────────────────────────────────────────────────────
// 1. FAITHFUL WAILS v2.12 EVENT RUNTIME  (port of events.js)
// ──────────────────────────────────────────────────────────────────────────

class WailsListener {
  eventName: string
  maxCallbacks: number
  cb: (...args: unknown[]) => void
  constructor(eventName: string, cb: (...args: unknown[]) => void, maxCallbacks = -1) {
    this.eventName = eventName
    this.maxCallbacks = maxCallbacks
    this.cb = cb
  }
  Callback(data: unknown[]): boolean {
    this.cb.apply(null, data)
    if (this.maxCallbacks === -1) return false
    this.maxCallbacks -= 1
    return this.maxCallbacks === 0
  }
}

class WailsRuntime {
  private eventListeners: Record<string, WailsListener[]> = {}

  reset(): void {
    this.eventListeners = {}
  }

  /** EventsOnMultiple — pushes a ref-identity listener; returns cancel. */
  on(eventName: string, cb: (...args: unknown[]) => void): () => void {
    this.eventListeners[eventName] = this.eventListeners[eventName] || []
    const listener = new WailsListener(eventName, cb)
    this.eventListeners[eventName].push(listener)
    return () => this.listenerOff(listener)
  }

  /** listenerOff — filter by reference; delete key when empty (removeListener). */
  private listenerOff(listener: WailsListener): void {
    const arr = this.eventListeners[listener.eventName]
    if (arr === undefined) return
    const next = arr.filter(l => l !== listener)
    if (next.length === 0) {
      delete this.eventListeners[listener.eventName]
    } else {
      this.eventListeners[listener.eventName] = next
    }
  }

  /** notifyListeners — slice, call, splice destroyed, replace/delete. */
  private notify(eventData: { name: string; data: unknown[] }): void {
    const eventName = eventData.name
    const newList = (this.eventListeners[eventName] || []).slice()
    if (newList.length === 0) return
    for (let count = newList.length - 1; count >= 0; count -= 1) {
      const listener = newList[count]
      if (!listener) continue
      const destroy = listener.Callback(eventData.data)
      if (destroy) newList.splice(count, 1)
    }
    if (newList.length === 0) {
      delete this.eventListeners[eventName]
    } else {
      this.eventListeners[eventName] = newList
    }
  }

  /** Emit an event (like Go ExecJS → EventsNotify, but JS-side). */
  emit(eventName: string, ...data: unknown[]): void {
    this.notify({ name: eventName, data })
  }

  /** Convenience: emit a session-scoped event. */
  emitSession(sessionId: string, event: string, data?: unknown): void {
    this.emit(`session:${sessionId}:${event}`, data)
  }

  /** Test helper: how many listeners are registered for an event name. */
  listenerCount(eventName: string): number {
    return (this.eventListeners[eventName] || []).length
  }
}

const wails = new WailsRuntime()

/** onSessionEvent — mirrors api/runtime.ts (prefix + null filter). */
function onSessionEvent(
  sessionId: string,
  event: string,
  callback: (data: unknown) => void,
): () => void {
  return wails.on(`session:${sessionId}:${event}`, (data: unknown) => {
    if (data === null || data === undefined) {
      callback(undefined)
      return
    }
    callback(data)
  })
}

// ──────────────────────────────────────────────────────────────────────────
// 2. FAITHFUL REACT PASSIVE-EFFECT SCHEDULER
//    (all cleanups in declaration order, THEN all setups in declaration order)
// ──────────────────────────────────────────────────────────────────────────

interface EffectSlot {
  prevDeps: unknown[] | undefined
  cleanup: (() => void) | undefined
}

const slots: EffectSlot[] = []
let cursor = 0
const pending: Array<{ deps: unknown[]; fn: () => (void | (() => void)); slot: EffectSlot }> = []

function depsChanged(a: unknown[], b: unknown[] | undefined): boolean {
  if (b === undefined) return true
  if (a.length !== b.length) return true
  for (let i = 0; i < a.length; i++) {
    if (!Object.is(a[i], b[i])) return true
  }
  return false
}

/** useEffect — register an effect for the current render (mirrors React). */
function registerEffect(fn: () => (void | (() => void)), deps: unknown[]): void {
  const idx = cursor++
  if (!slots[idx]) slots[idx] = { prevDeps: undefined, cleanup: undefined }
  const slot = slots[idx]
  if (depsChanged(deps, slot.prevDeps)) {
    pending.push({ deps, fn, slot })
  }
}

function beginRender(): void {
  cursor = 0
  pending.length = 0
}

/** flush — run ALL pending cleanups in order, THEN ALL setups in order. */
function flush(): void {
  for (const { slot } of pending) {
    if (slot.cleanup) {
      try { slot.cleanup() } catch { /* ignore */ }
    }
  }
  for (const { deps, fn, slot } of pending) {
    const c = fn()
    slot.cleanup = typeof c === 'function' ? c : undefined
    slot.prevDeps = deps
  }
}

// ──────────────────────────────────────────────────────────────────────────
// 3. FAITHFUL STORE  (verbatim actions from stores/chatStore.ts)
// ──────────────────────────────────────────────────────────────────────────

interface ChatMessageUI {
  id: string
  sessionId: string
  type: string
  content: string
  metadata: Record<string, unknown>
  timestamp: number
}

let msgCounter = 0
function generateMessageId(): string {
  msgCounter += 1
  return `msg-${msgCounter}`
}

interface ChatStoreState {
  messages: Record<string, Record<string, ChatMessageUI>>
  messageOrder: Record<string, string[]>
  // Per-session streaming/activity (absent key = not streaming / no activity)
  streamingText: Record<string, string>
  activityStatus: Record<string, string>
  taskActive: Record<string, boolean>
  stepContextFill: Record<string, Record<string, number>>
  // Stamped by the streaming/activity actions (mirrors chatStore.ts) so the
  // reconcile can detect a stale status snapshot.
  runtimeEventAt: Record<string, number>
}

const store: ChatStoreState = {
  messages: {},
  messageOrder: {},
  streamingText: {},
  activityStatus: {},
  taskActive: {},
  stepContextFill: {},
  runtimeEventAt: {},
}

function indexMessages(msgs: ChatMessageUI[]): Record<string, ChatMessageUI> {
  const index: Record<string, ChatMessageUI> = {}
  for (const msg of msgs) index[msg.id] = msg
  return index
}

// --- store actions (verbatim from chatStore.ts) ---

function addMessage(sessionId: string, message: ChatMessageUI): void {
  const sessionIndex = store.messages[sessionId] ?? {}
  const sessionOrder = store.messageOrder[sessionId] ?? []
  store.messages = { ...store.messages, [sessionId]: { ...sessionIndex, [message.id]: message } }
  store.messageOrder = { ...store.messageOrder, [sessionId]: [...sessionOrder, message.id] }
}

function mergeHistoryMessages(sessionId: string, history: ChatMessageUI[], loadStartedAt: number): void {
  const liveIndex = store.messages[sessionId] ?? {}
  const liveOrder = store.messageOrder[sessionId] ?? []
  const historyIds = new Set(history.map(m => m.id))
  const preserved: ChatMessageUI[] = []
  for (const id of liveOrder) {
    const msg = liveIndex[id]
    if (!msg || historyIds.has(id)) continue
    if (msg.timestamp >= loadStartedAt) preserved.push(msg)
  }
  const merged = [...history, ...preserved]
  store.messages = { ...store.messages, [sessionId]: indexMessages(merged) }
  store.messageOrder = { ...store.messageOrder, [sessionId]: merged.map(m => m.id) }
}

function setStreamingText(sessionId: string, text: string): void {
  store.streamingText = { ...store.streamingText, [sessionId]: text }
  store.runtimeEventAt = { ...store.runtimeEventAt, [sessionId]: Date.now() }
}
function appendStreamingText(sessionId: string, delta: string): void {
  const prev = store.streamingText[sessionId] ?? ''
  store.streamingText = { ...store.streamingText, [sessionId]: prev + delta }
  store.runtimeEventAt = { ...store.runtimeEventAt, [sessionId]: Date.now() }
}
function clearStreamingText(sessionId: string): void {
  if (!(sessionId in store.streamingText)) return
  delete store.streamingText[sessionId]
  store.runtimeEventAt = { ...store.runtimeEventAt, [sessionId]: Date.now() }
}
function setActivityStatus(sessionId: string, status: string | null): void {
  if (status === null) {
    if (!(sessionId in store.activityStatus)) return
    delete store.activityStatus[sessionId]
  } else {
    store.activityStatus = { ...store.activityStatus, [sessionId]: status }
  }
  store.runtimeEventAt = { ...store.runtimeEventAt, [sessionId]: Date.now() }
}
function setTaskActive(sessionId: string, active: boolean): void {
  store.taskActive = { ...store.taskActive, [sessionId]: active }
}

function selectSessionMessages(sessionId: string): ChatMessageUI[] {
  const order = store.messageOrder[sessionId]
  const index = store.messages[sessionId]
  if (!order || !index) return []
  const result: ChatMessageUI[] = []
  for (const id of order) {
    const msg = index[id]
    if (msg) result.push(msg)
  }
  return result
}

// ──────────────────────────────────────────────────────────────────────────
// 4. SESSION STORE + RPC MOCKS
// ──────────────────────────────────────────────────────────────────────────

let activeSessionId: string | null = null

interface RuntimeStatus { active: boolean; has_unfinished_task: boolean; activity?: string; streaming?: boolean }
interface PendingActions { tool_confirms: unknown[]; step_limits: unknown[]; plan_approvals: unknown[]; ask_user: unknown[] }

interface Deferred<T> { promise: Promise<T>; resolve: (v: T | PromiseLike<T>) => void }

function makeDeferred<T>(): Deferred<T> {
  let resolveFn!: (v: T | PromiseLike<T>) => void
  const promise = new Promise<T>(resolve => { resolveFn = resolve })
  return { promise, resolve: resolveFn }
}

// Per-session deferred RPCs, controllable by the test to model async timing.
const statusDeferred = new Map<string, Deferred<RuntimeStatus>>()
const pendingDeferred = new Map<string, Deferred<PendingActions>>()
const historyDeferred = new Map<string, Deferred<ChatMessageUI[]>>()

function getStatus(sid: string): Promise<RuntimeStatus> {
  if (!statusDeferred.has(sid)) statusDeferred.set(sid, makeDeferred<RuntimeStatus>())
  return statusDeferred.get(sid)!.promise
}
function getPending(sid: string): Promise<PendingActions> {
  if (!pendingDeferred.has(sid)) pendingDeferred.set(sid, makeDeferred<PendingActions>())
  return pendingDeferred.get(sid)!.promise
}
function getHistory(sid: string): Promise<ChatMessageUI[]> {
  if (!historyDeferred.has(sid)) historyDeferred.set(sid, makeDeferred<ChatMessageUI[]>())
  return historyDeferred.get(sid)!.promise
}

// ──────────────────────────────────────────────────────────────────────────
// 5. THE ORCHESTRATION  (verbatim logic from the real hooks/components)
// ──────────────────────────────────────────────────────────────────────────

// 5a. useSessionEvents — reset effect + live chat-event subscriptions.
//     Source: hooks/useSessionEvents.ts (reset effect) + hooks/events/useChatEvents.ts
function simulateSessionEvents(sessionId: string | null): void {
  // --- reset effect [deps: sessionId] (useSessionEvents.ts:27-49) ---
  // NOTE: streamingText/activityStatus/stepContextFill are per-session keyed
  // maps, so they are NOT reset here — they are naturally preserved across
  // A→B→A switches. Only the just-switched session's taskActive flag is reset
  // (restored asynchronously by the reconcile effect if still running); the
  // runtime reconcile refreshes/clears the activity label and streaming text
  // from the backend snapshot.
  registerEffect(() => {
    if (!sessionId) return
    store.taskActive = { ...store.taskActive, [sessionId]: false }
    // getSessionTokens(...) is omitted; no async work to guard here.
  }, [sessionId])

  // --- live chat events [deps: sessionId] (useChatEvents.ts:38-200) ---
  registerEffect(() => {
    if (!sessionId) return
    const cleanups: Array<() => void> = []

    // assistant_chunk
    cleanups.push(onSessionEvent(sessionId, 'assistant_chunk', (data) => {
      const d = data as { content?: string; accumulated_content?: string } | undefined
      if (!d) return
      setActivityStatus(sessionId, 'Generating response...')
      if (d.accumulated_content !== undefined) {
        setStreamingText(sessionId, d.accumulated_content)
      } else if (d.content) {
        if (!store.streamingText[sessionId]) {
          setStreamingText(sessionId, d.content)
        } else {
          appendStreamingText(sessionId, d.content)
        }
      }
    }))

    // assistant_done
    cleanups.push(onSessionEvent(sessionId, 'assistant_done', () => {
      const text = store.streamingText[sessionId]
      if (text) {
        addMessage(sessionId, { id: generateMessageId(), sessionId, type: 'assistant', content: text, metadata: {}, timestamp: Date.now() })
        clearStreamingText(sessionId)
      }
    }))

    // thought
    cleanups.push(onSessionEvent(sessionId, 'thought', (data) => {
      const d = data as { content: string } | undefined
      if (!d) return
      setActivityStatus(sessionId, 'Reasoning...')
      addMessage(sessionId, { id: generateMessageId(), sessionId, type: 'thought', content: d.content, metadata: {}, timestamp: Date.now() })
    }))

    // error
    cleanups.push(onSessionEvent(sessionId, 'error', (data) => {
      const d = data as { error?: string } | undefined
      addMessage(sessionId, { id: generateMessageId(), sessionId, type: 'error', content: d?.error || 'An error occurred', metadata: {}, timestamp: Date.now() })
      clearStreamingText(sessionId)
      setActivityStatus(sessionId, null)
      setTaskActive(sessionId, false)
    }))

    // task_complete
    cleanups.push(onSessionEvent(sessionId, 'task_complete', (data) => {
      const d = (data ?? {}) as { output?: string; success?: boolean; completion?: string; failed_steps?: number }
      clearStreamingText(sessionId)
      setActivityStatus(sessionId, null)
      setTaskActive(sessionId, false)
      const msgs = selectSessionMessages(sessionId)
      const lastAssistant = [...msgs].reverse().find(m => m.type === 'assistant')
      if (d.output && (!lastAssistant || lastAssistant.content !== d.output)) {
        addMessage(sessionId, { id: generateMessageId(), sessionId, type: 'assistant', content: d.output, metadata: {}, timestamp: Date.now() })
      }
    }))

    // task_cancelled
    cleanups.push(onSessionEvent(sessionId, 'task_cancelled', () => {
      clearStreamingText(sessionId)
      setActivityStatus(sessionId, null)
      setTaskActive(sessionId, false)
      addMessage(sessionId, { id: generateMessageId(), sessionId, type: 'error', content: 'Task was cancelled', metadata: {}, timestamp: Date.now() })
    }))

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}

// 5b. useBackgroundSessionWatcher [deps: watchedKey] (useBackgroundSessionWatcher.ts)
function simulateBackgroundWatcher(): void {
  const taskActive = store.taskActive
  const watchedKey = Object.keys(taskActive)
    .filter(id => taskActive[id] === true && id !== activeSessionId)
    .sort()
    .join('\n')

  registerEffect(() => {
    const sessionIds = watchedKey ? watchedKey.split('\n') : []
    if (sessionIds.length === 0) return
    const cleanups: Array<() => void> = []
    for (const sessionId of sessionIds) {
      // Mirrors the patched useBackgroundSessionWatcher: finalize THIS
      // background session's own per-session streaming/activity/running flag.
      // Because the state is keyed per session, this cannot touch the
      // active session (no cross-session contamination).
      const handleCompletion = (): void => {
        setTaskActive(sessionId, false)
        clearStreamingText(sessionId)
        setActivityStatus(sessionId, null)
      }
      cleanups.push(onSessionEvent(sessionId, 'task_complete', () => handleCompletion()))
      cleanups.push(onSessionEvent(sessionId, 'task_cancelled', () => handleCompletion()))
      cleanups.push(onSessionEvent(sessionId, 'error', () => handleCompletion()))
      cleanups.push(onSessionEvent(sessionId, 'tool_confirm', () => { /* HITL — out of scope */ }))
      cleanups.push(onSessionEvent(sessionId, 'ask_user', () => { /* HITL — out of scope */ }))
      cleanups.push(onSessionEvent(sessionId, 'step_limit', () => { /* HITL — out of scope */ }))
      cleanups.push(onSessionEvent(sessionId, 'plan_review_ready', () => { /* HITL — out of scope */ }))
    }
    return () => cleanups.forEach(fn => fn())
  }, [watchedKey])
}

// 5c. ChatArea effects [deps: activeSessionId] (ChatArea.tsx:66-150)
function simulateChatAreaEffects(active: string | null): void {
  // history + reconcile effect
  registerEffect(() => {
    if (!active) return
    let cancelled = false
    const loadStartedAt = Date.now()
    void (async () => {
      let history: ChatMessageUI[]
      try {
        history = await getHistory(active)
      } catch {
        if (!cancelled) { /* would add an error message */ }
        return
      }
      if (cancelled) return
      if (history.length > 0) mergeHistoryMessages(active, history, loadStartedAt)
      // UNGUARDED Promise.all — a rejection here abandons reconciliation.
      const statusReadAt = Date.now()
      const [status, _pending] = await Promise.all([getStatus(active), getPending(active)])
      void _pending
      if (cancelled) return
      // reconcileRuntimeStatus (sessionRuntime.ts): taskActive from the
      // snapshot; the frozen activity label is replaced by the backend's
      // tracked phase and streaming text cleared when no stream is open —
      // unless a live event already updated them after statusReadAt (the
      // stale-snapshot guard: live beats snapshot).
      const hasFresherLiveState = (store.runtimeEventAt[active] ?? 0) > statusReadAt
      setTaskActive(active, status.active)
      if (status.active) {
        if (!hasFresherLiveState) {
          setActivityStatus(active, status.activity ?? 'Processing...')
          if (!status.streaming) clearStreamingText(active)
        }
      } else if (!hasFresherLiveState) {
        setActivityStatus(active, null)
        clearStreamingText(active)
      }
    })()
    return () => { cancelled = true }
  }, [active])

  // restore effect — fast taskActive restore
  registerEffect(() => {
    if (!active) return
    let cancelled = false
    getStatus(active).then((status) => {
      if (cancelled || !status) return
      setTaskActive(active, status.active)
    }).catch(() => { /* logged */ })
    return () => { cancelled = true }
  }, [active])
}

// ──────────────────────────────────────────────────────────────────────────
// 6. RENDER / COMMIT / ASYNC HELPERS
// ──────────────────────────────────────────────────────────────────────────

/** One React commit: re-run every hook (re-register effects) then flush. */
function render(): void {
  beginRender()
  // Declaration order mirrors App.tsx (useSessionEvents → useBackgroundSessionWatcher)
  // plus the ChatArea child effects.
  simulateSessionEvents(activeSessionId)
  simulateBackgroundWatcher()
  simulateChatAreaEffects(activeSessionId)
  flush()
}

/** Switch the active session (sessionStore.setActiveSessionId) + commit. */
function switchTo(sid: string): void {
  activeSessionId = sid
  render()
}

/** Flush pending microtasks (resolves resolved deferreds' .then callbacks). */
function flushMicrotasks(): Promise<void> {
  return Promise.resolve().then(() => Promise.resolve()).then(() => Promise.resolve()).then()
}

/** Simulate the user sending a message (useMessageSender.send happy path). */
function sendUserMessage(sid: string, text: string): void {
  addMessage(sid, { id: generateMessageId(), sessionId: sid, type: 'user', content: text, metadata: {}, timestamp: Date.now() })
  setTaskActive(sid, true)
  setActivityStatus(sid, 'Processing...')
  render() // taskActive mutation → re-render
}

// ──────────────────────────────────────────────────────────────────────────
// 7. THE TESTS
// ──────────────────────────────────────────────────────────────────────────

function resetAll(): void {
  wails.reset()
  slots.length = 0
  cursor = 0
  pending.length = 0
  store.messages = {}
  store.messageOrder = {}
  store.streamingText = {}
  store.activityStatus = {}
  store.taskActive = {}
  store.stepContextFill = {}
  store.runtimeEventAt = {}
  activeSessionId = null
  statusDeferred.clear()
  pendingDeferred.clear()
  historyDeferred.clear()
  msgCounter = 0
}

/**
 * Resolve the runtime-status RPC for a session then re-render. activity/
 * streaming mirror the extended backend snapshot (SessionRuntimeStatus).
 */
async function deliverStatus(
  sid: string,
  active: boolean,
  hasUnfinished = false,
  activity?: string,
  streaming?: boolean,
): Promise<void> {
  // A fresh deferred is created by getStatus() only when awaited. Ensure it exists.
  getStatus(sid)
  statusDeferred.get(sid)!.resolve({ active, has_unfinished_task: hasUnfinished, activity, streaming })
  await flushMicrotasks()
  render()
}

/** Resolve history + pending RPCs for a session then re-render. */
async function deliverHistory(sid: string, history: ChatMessageUI[] = []): Promise<void> {
  getPending(sid)
  pendingDeferred.get(sid)!.resolve({ tool_confirms: [], step_limits: [], plan_approvals: [], ask_user: [] })
  historyDeferred.get(sid)!.resolve(history)
  await flushMicrotasks()
  render()
}

beforeEach(() => {
  resetAll()
})

describe('concurrent session switching (A→B→A)', () => {
  it('active session A keeps receiving live events after switching back from B', async () => {
    const A = 'session-A'
    const B = 'session-B'

    // ── Start: view A, start a task in A ──
    switchTo(A)
    sendUserMessage(A, 'do something in A')
    expect(store.taskActive[A]).toBe(true)
    expect(wails.listenerCount(`session:${A}:assistant_chunk`)).toBe(1)

    // A streams a chunk → shows in chat (per-session state)
    wails.emitSession(A, 'assistant_chunk', { content: 'A is working', accumulated_content: 'A is working' })
    expect(store.streamingText[A]).toBe('A is working')
    expect(store.activityStatus[A]).toBe('Generating response...')

    // ── Switch A → B (A still running in background) ──
    switchTo(B)
    // Per-session streaming/activity are PRESERVED across the switch — the
    // reset effect no longer clears them (the heart of this refactor), so no
    // event is lost and the partial is restored instantly on return.
    expect(store.streamingText[A]).toBe('A is working')
    expect(store.activityStatus[A]).toBe('Generating response...')
    expect(store.taskActive[A]).toBe(true) // A still running
    expect(store.taskActive[B]).toBe(false) // reset on switch, restored shortly

    // background watcher now watches A's terminal events
    expect(wails.listenerCount(`session:${A}:task_complete`)).toBe(1)
    // A's LIVE assistant_chunk listener is gone (only the active session streams)
    expect(wails.listenerCount(`session:${A}:assistant_chunk`)).toBe(0)

    // Start a task in B
    sendUserMessage(B, 'do something in B')
    expect(store.taskActive[B]).toBe(true)

    // ── Switch B → A (both still running) ──
    switchTo(A)
    // reset effect set taskActive[A]=false synchronously; restore is async
    expect(store.taskActive[A]).toBe(false)

    // A's LIVE listeners re-subscribe
    expect(wails.listenerCount(`session:${A}:assistant_chunk`)).toBe(1)
    expect(wails.listenerCount(`session:${A}:task_complete`)).toBe(1)
    // background watcher no longer watches A (it's active now); watches B instead
    expect(wails.listenerCount(`session:${A}:error`)).toBe(1) // live, not bg
    expect(wails.listenerCount(`session:${B}:task_complete`)).toBe(1) // bg

    // Restore + history RPCs resolve: A is genuinely still running
    await deliverStatus(A, true)
    await deliverHistory(A)

    // ── THE KEY CHECK: new events for A still arrive & sync correctly ──
    // The backend always sends accumulated_content (full state), so the
    // preserved partial is REPLACED with the current full text — no loss.
    wails.emitSession(A, 'assistant_chunk', { content: 'A still working', accumulated_content: 'A still working' })
    expect(store.streamingText[A]).toBe('A still working')
    expect(store.activityStatus[A]).toBe('Generating response...')
    expect(store.taskActive[A]).toBe(true)
    expect(wails.listenerCount(`session:${A}:assistant_chunk`)).toBe(1)
  })

  it('a background session completing does NOT corrupt the active session state', async () => {
    const A = 'session-A'
    const B = 'session-B'

    switchTo(A)
    sendUserMessage(A, 'task A')
    switchTo(B)
    sendUserMessage(B, 'task B')
    switchTo(A)
    await deliverStatus(A, true)
    await deliverHistory(A)

    // A is actively streaming into its OWN per-session buffer.
    wails.emitSession(A, 'assistant_chunk', { content: 'A streaming', accumulated_content: 'A streaming' })
    expect(store.streamingText[A]).toBe('A streaming')
    expect(store.activityStatus[A]).toBe('Generating response...')

    // B completes in the background while A is the viewed, active session.
    wails.emitSession(B, 'task_complete', { output: 'B done' })

    // B's completion finalizes ONLY B's own per-session state.
    expect(store.taskActive[B]).toBe(false)
    expect(store.streamingText[B]).toBeUndefined()
    expect(store.activityStatus[B]).toBeUndefined()
    // The active session A's in-progress stream/activity must be untouched —
    // structurally impossible to contaminate now that state is keyed per
    // session (the original global-state bug class is eliminated).
    expect(store.streamingText[A]).toBe('A streaming')
    expect(store.activityStatus[A]).toBe('Generating response...')
  })

  it('the active session final message is never lost, even when a background session completes mid-flush', async () => {
    const A = 'session-A'
    const B = 'session-B'

    switchTo(A)
    sendUserMessage(A, 'task A')
    switchTo(B)
    sendUserMessage(B, 'task B')
    switchTo(A)
    await deliverStatus(A, true)
    await deliverHistory(A)

    // A streams its final answer into A's OWN (per-session) streaming buffer.
    wails.emitSession(A, 'assistant_chunk', { content: 'A final answer', accumulated_content: 'A final answer' })
    expect(store.streamingText[A]).toBe('A final answer')

    const assistantBefore = selectSessionMessages(A).filter(m => m.type === 'assistant').length

    // B completes in the background. With per-session state this touches ONLY
    // B and cannot wipe A's streaming buffer (the original global-state bug).
    wails.emitSession(B, 'task_complete', { output: 'B done' })
    expect(store.streamingText[A]).toBe('A final answer')

    // A's assistant_done flushes A's streaming buffer to a permanent assistant
    // message — the flush is NOT a no-op.
    wails.emitSession(A, 'assistant_done')

    const assistantAfter = selectSessionMessages(A).filter(m => m.type === 'assistant').length
    expect(assistantAfter).toBe(assistantBefore + 1)
    expect(store.streamingText[A]).toBeUndefined()
  })

  it('a live event landing while the status RPC is in flight beats the stale snapshot', async () => {
    // Fake only Date: the guard compares millisecond stamps, and in-process
    // the read and the event would land in the same millisecond. Deferred
    // promises are microtasks and stay real.
    vi.useFakeTimers({ toFake: ['Date'] })
    try {
      const A = 'session-A'
      const B = 'session-B'

      switchTo(A)
      sendUserMessage(A, 'task A')
      switchTo(B)
      sendUserMessage(B, 'task B')

      // Switch back to A: the history+status effect is in flight.
      switchTo(A)

      // History resolves first — the effect has captured statusReadAt and is
      // now parked on the status RPC.
      await deliverHistory(A)

      // Model the RPC flight: a live assistant_chunk lands strictly after
      // statusReadAt but before the response is applied — its label/stream
      // are fresher than the snapshot's phase.
      vi.setSystemTime(Date.now() + 10)
      wails.emitSession(A, 'assistant_chunk', { content: 'live partial', accumulated_content: 'live partial' })
      expect(store.streamingText[A]).toBe('live partial')

      // The (older) snapshot claims an earlier phase and no open stream.
      await deliverStatus(A, true, false, 'Thinking...', false)

      // The stale-snapshot guard preserves the live label and stream...
      expect(store.activityStatus[A]).toBe('Generating response...')
      expect(store.streamingText[A]).toBe('live partial')
      // ...while taskActive still comes from the snapshot.
      expect(store.taskActive[A]).toBe(true)
    } finally {
      vi.useRealTimers()
    }
  })

  it('two sessions streaming concurrently keep fully independent streams', () => {
    const A = 'session-A'
    const B = 'session-B'

    switchTo(A)
    sendUserMessage(A, 'task A')
    switchTo(B)
    sendUserMessage(B, 'task B')

    // B is the viewed session — it streams into its OWN per-session buffer.
    wails.emitSession(B, 'assistant_chunk', { content: 'B chunk', accumulated_content: 'B chunk' })
    expect(store.streamingText[B]).toBe('B chunk')
    // A's buffer is untouched by B's stream — no global contamination.
    expect(store.streamingText[A]).toBeUndefined()

    // Switch back to A: B's buffer is preserved for when we return to it,
    // and A's buffer is still empty (no leakage from B).
    switchTo(A)
    expect(store.streamingText[B]).toBe('B chunk')
    expect(store.streamingText[A]).toBeUndefined()

    // Now A streams — independent from B.
    wails.emitSession(A, 'assistant_chunk', { content: 'A chunk', accumulated_content: 'A chunk' })
    expect(store.streamingText[A]).toBe('A chunk')
    expect(store.streamingText[B]).toBe('B chunk') // still independent
  })
})
