// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock @/api/runtime so tests never touch the Wails backend ---

const { runtimeMocks } = vi.hoisted(() => ({
  runtimeMocks: {
    onGlobalEvent: vi.fn(),
    confirmExit: vi.fn(),
    reportDroppedEvent: vi.fn(),
  },
}))

vi.mock('@/api/runtime', () => runtimeMocks)

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn() },
}))

import { useExitGuard } from './useExitGuard'
import { useExitGuardStore } from '@/stores/exitGuardStore'
import { isExitRequestedData } from '@/types/events'

let root: Root
let container: HTMLDivElement

function Harness() {
  useExitGuard()
  return null
}

function renderHook() {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(<Harness />)
  })
}

/** Fire the captured app:exit_requested subscription callback. */
function emitExitRequested(data: unknown) {
  const calls = runtimeMocks.onGlobalEvent.mock.calls
  const cb = calls[calls.length - 1]?.[1] as ((d: unknown) => void) | undefined
  if (!cb) throw new Error('app:exit_requested subscription not registered')
  act(() => {
    cb(data)
  })
}

const validPayload = {
  sessions: [
    { id: 'sess-1', name: 'Refactor', compacting: false },
    { id: 'sess-2', name: 'Docs pass', compacting: true },
  ],
}

beforeEach(() => {
  runtimeMocks.onGlobalEvent.mockReset()
  runtimeMocks.confirmExit.mockReset()
  runtimeMocks.reportDroppedEvent.mockReset()
  runtimeMocks.onGlobalEvent.mockImplementation((_event: string, _cb: (d: unknown) => void) => {
    // Callbacks stay reachable via mock.calls; emitExitRequested fires the
    // latest one. Returning a no-op unsubscribe keeps the hook contract.
    return () => {}
  })
  runtimeMocks.confirmExit.mockResolvedValue(undefined)
  useExitGuardStore.getState().clear()
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
})

describe('useExitGuard — subscription', () => {
  it('subscribes to app:exit_requested once', () => {
    renderHook()

    expect(runtimeMocks.onGlobalEvent).toHaveBeenCalledTimes(1)
    expect(runtimeMocks.onGlobalEvent).toHaveBeenCalledWith('app:exit_requested', expect.any(Function))
  })

  it('keeps the modal closed before any event arrives', () => {
    renderHook()

    const state = useExitGuardStore.getState()
    expect(state.open).toBe(false)
    expect(state.sessions).toEqual([])
    expect(state.updatePending).toBe(false)
  })
})

describe('useExitGuard — event handling', () => {
  it('opens the modal with the session list on a valid payload', () => {
    renderHook()

    emitExitRequested(validPayload)

    const state = useExitGuardStore.getState()
    expect(state.open).toBe(true)
    expect(state.sessions).toHaveLength(2)
    expect(state.sessions[0]).toEqual({ id: 'sess-1', name: 'Refactor', compacting: false })
    expect(state.sessions[1]?.compacting).toBe(true)
    expect(state.updatePending).toBe(false)
  })

  it('propagates the update context (update_pending) to the store', () => {
    renderHook()

    emitExitRequested({ ...validPayload, update_pending: true })

    const state = useExitGuardStore.getState()
    expect(state.open).toBe(true)
    expect(state.updatePending).toBe(true)
  })

  it('reports malformed payloads and still opens the generic modal', () => {
    renderHook()

    emitExitRequested({ sessions: 'nope' })

    // The quit is already prevented by the backend; the modal must stay
    // answerable — list-less variant — and the drop must be reported.
    const state = useExitGuardStore.getState()
    expect(state.open).toBe(true)
    expect(state.sessions).toEqual([])
    expect(state.updatePending).toBe(false)
    expect(runtimeMocks.reportDroppedEvent).toHaveBeenCalledTimes(1)
    expect(runtimeMocks.reportDroppedEvent).toHaveBeenCalledWith('app:exit_requested', { sessions: 'nope' })
  })

  it('reports a null payload as dropped and opens the generic modal', () => {
    renderHook()

    // onGlobalEvent normalizes a null emission to undefined before the
    // callback — assert the production-shaped input.
    emitExitRequested(undefined)

    const state = useExitGuardStore.getState()
    expect(state.open).toBe(true)
    expect(state.sessions).toEqual([])
    expect(runtimeMocks.reportDroppedEvent).toHaveBeenCalledWith('app:exit_requested', undefined)
  })

  it('replaces the session list on a repeated event while open', () => {
    renderHook()
    emitExitRequested(validPayload)

    emitExitRequested({ sessions: [{ id: 'sess-3', name: 'Late start', compacting: false }] })

    const state = useExitGuardStore.getState()
    expect(state.open).toBe(true)
    expect(state.sessions).toHaveLength(1)
    expect(state.sessions[0]?.id).toBe('sess-3')
  })
})

describe('exitGuardStore — decisions', () => {
  it('clear closes the modal and resets the list', () => {
    renderHook()
    emitExitRequested(validPayload)

    act(() => {
      useExitGuardStore.getState().clear()
    })

    const state = useExitGuardStore.getState()
    expect(state.open).toBe(false)
    expect(state.sessions).toEqual([])
    expect(state.updatePending).toBe(false)
  })

  it('keeps the modal state across a remount (phase transition)', () => {
    renderHook()
    emitExitRequested(validPayload)

    // Simulate an app-phase transition: the dialog component tree remounts,
    // the root subscription persists.
    act(() => {
      root.render(<Harness />)
    })

    const state = useExitGuardStore.getState()
    expect(state.open).toBe(true)
    expect(state.sessions).toHaveLength(2)
  })
})

describe('isExitRequestedData — payload guard', () => {
  it('accepts a well-formed payload', () => {
    expect(isExitRequestedData(validPayload)).toBe(true)
    expect(isExitRequestedData({ sessions: [] })).toBe(true)
  })

  it('accepts an optional boolean update_pending', () => {
    expect(isExitRequestedData({ ...validPayload, update_pending: true })).toBe(true)
    expect(isExitRequestedData({ ...validPayload, update_pending: false })).toBe(true)
  })

  it('rejects non-object and malformed payloads', () => {
    expect(isExitRequestedData(undefined)).toBe(false)
    expect(isExitRequestedData(null)).toBe(false)
    expect(isExitRequestedData('sessions')).toBe(false)
    expect(isExitRequestedData({})).toBe(false)
    expect(isExitRequestedData({ sessions: {} })).toBe(false)
    expect(isExitRequestedData({ sessions: [{ id: 'ok' }] })).toBe(false) // missing name
    expect(isExitRequestedData({ sessions: [{ id: 'ok', name: 5 }] })).toBe(false)
    expect(isExitRequestedData({ sessions: [], update_pending: 'yes' })).toBe(false)
  })

  it('tolerates an absent optional compacting flag', () => {
    expect(isExitRequestedData({ sessions: [{ id: 's', name: 'n' }] })).toBe(true)
  })
})
