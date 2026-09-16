// @vitest-environment jsdom
//
// Tests for the useProcessMemory polling hook: immediate first sample, tick
// refresh, timer cleanup on unmount, and the silent failure contract (last
// good sample kept, null when nothing succeeded).

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const getProcessMemoryMock = vi.fn()
vi.mock('@/api/system', () => ({
  getProcessMemory: (...args: unknown[]) => getProcessMemoryMock(...args),
}))

import { PROCESS_MEMORY_POLL_MS, useProcessMemory } from './useProcessMemory'

let container: HTMLElement
let root: Root
/** Latest value the probe component rendered from the hook. */
let latest: number | null | 'unset' = 'unset'

function Probe() {
  const value = useProcessMemory()
  latest = value
  return null
}

const render = async () => {
  // Async act: the mount effect starts the first poll, whose .then must
  // resolve INSIDE the act scope — a sync act followed by an await would
  // drain the microtask outside act and log "not wrapped in act" noise.
  await act(async () => {
    root.render(<Probe />)
  })
}

const flushMicrotasks = () => act(async () => {})

beforeEach(() => {
  vi.useFakeTimers()
  getProcessMemoryMock.mockReset()
  latest = 'unset'
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  vi.useRealTimers()
})

describe('useProcessMemory', () => {
  it('takes an immediate first sample on mount', async () => {
    getProcessMemoryMock.mockResolvedValue(1_234_567_896)

    await render()
    await flushMicrotasks()

    expect(getProcessMemoryMock).toHaveBeenCalledTimes(1)
    expect(latest).toBe(1_234_567_896)
  })

  it('refreshes the value on every poll tick', async () => {
    getProcessMemoryMock
      .mockResolvedValueOnce(1_234_567_896)
      .mockResolvedValueOnce(3 * 1024 * 1024)

    await render()
    await flushMicrotasks()
    expect(latest).toBe(1_234_567_896)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(PROCESS_MEMORY_POLL_MS)
    })
    expect(getProcessMemoryMock).toHaveBeenCalledTimes(2)
    expect(latest).toBe(3 * 1024 * 1024)
  })

  it('clears the interval timer on unmount (no further polls)', async () => {
    getProcessMemoryMock.mockResolvedValue(1)

    await render()
    await flushMicrotasks()
    expect(vi.getTimerCount()).toBe(1)

    act(() => {
      root.unmount()
    })
    expect(vi.getTimerCount()).toBe(0)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(PROCESS_MEMORY_POLL_MS * 3)
    })
    expect(getProcessMemoryMock).toHaveBeenCalledTimes(1)
  })

  it('stays null when the API never succeeds (unavailable runtime)', async () => {
    getProcessMemoryMock.mockRejectedValue(new Error('Wails App bindings are not available'))

    await render()
    await flushMicrotasks()

    expect(latest).toBeNull()
  })

  it('keeps the last good sample across a failed poll', async () => {
    getProcessMemoryMock
      .mockResolvedValueOnce(1_234_567_896)
      .mockRejectedValueOnce(new Error('transient'))

    await render()
    await flushMicrotasks()
    expect(latest).toBe(1_234_567_896)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(PROCESS_MEMORY_POLL_MS)
    })
    expect(latest).toBe(1_234_567_896)
  })

  it('polls on the agreed cadence (no extra calls between ticks)', async () => {
    getProcessMemoryMock.mockResolvedValue(1)

    await render()
    await flushMicrotasks()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(PROCESS_MEMORY_POLL_MS - 1)
    })
    expect(getProcessMemoryMock).toHaveBeenCalledTimes(1)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
    expect(getProcessMemoryMock).toHaveBeenCalledTimes(2)
  })
})
