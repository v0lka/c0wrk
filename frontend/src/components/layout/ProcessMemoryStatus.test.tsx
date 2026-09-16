// @vitest-environment jsdom
//
// Tests for the ProcessMemoryStatus status-bar indicator: the agreed
// "RSS N MB" format with the exact-bytes tooltip, tick-driven updates, timer
// cleanup on unmount, and silent hiding while the API is unavailable.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const getProcessMemoryMock = vi.fn()
vi.mock('@/api/system', () => ({
  getProcessMemory: (...args: unknown[]) => getProcessMemoryMock(...args),
}))

import { ProcessMemoryStatus } from './ProcessMemoryStatus'
import { PROCESS_MEMORY_POLL_MS } from '@/hooks/useProcessMemory'

let container: HTMLElement
let root: Root

const render = async () => {
  // Async act: the mount effect starts the first poll, whose .then must
  // resolve INSIDE the act scope — a sync act followed by an await would
  // drain the microtask outside act and log "not wrapped in act" noise.
  await act(async () => {
    root.render(<ProcessMemoryStatus />)
  })
}

const flushMicrotasks = () => act(async () => {})

beforeEach(() => {
  vi.useFakeTimers()
  getProcessMemoryMock.mockReset()
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

describe('ProcessMemoryStatus', () => {
  it('renders "RSS 1177 MB" for 1,234,567,896 bytes with an exact-bytes tooltip', async () => {
    getProcessMemoryMock.mockResolvedValue(1_234_567_896)

    await render()
    await flushMicrotasks()

    expect(container.textContent).toBe('RSS 1177 MB')
    const el = container.firstElementChild as HTMLElement
    expect(el).not.toBeNull()
    expect(el.getAttribute('title')).toBe('Process memory (RSS): 1,234,567,896 bytes')
    expect(el.getAttribute('aria-label')).toBe('Process memory (RSS): 1,234,567,896 bytes')
    // tabular-nums keeps the digits width-stable between ticks.
    expect(el.querySelector('.tabular-nums')).not.toBeNull()
  })

  it('rounds to the nearest whole megabyte', async () => {
    getProcessMemoryMock.mockResolvedValue(3 * 1024 * 1024 + 512 * 1024) // 3.5 MB

    await render()
    await flushMicrotasks()

    expect(container.textContent).toBe('RSS 4 MB')
  })

  it('updates the label on every poll tick', async () => {
    getProcessMemoryMock
      .mockResolvedValueOnce(1_234_567_896)
      .mockResolvedValueOnce(2048 * 1024 * 1024)

    await render()
    await flushMicrotasks()
    expect(container.textContent).toBe('RSS 1177 MB')

    await act(async () => {
      await vi.advanceTimersByTimeAsync(PROCESS_MEMORY_POLL_MS)
    })
    expect(container.textContent).toBe('RSS 2048 MB')
  })

  it('clears the interval timer on unmount (no further polls)', async () => {
    getProcessMemoryMock.mockResolvedValue(1_234_567_896)

    await render()
    await flushMicrotasks()
    expect(vi.getTimerCount()).toBe(1)

    act(() => {
      root.unmount()
    })
    expect(vi.getTimerCount()).toBe(0)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(PROCESS_MEMORY_POLL_MS * 2)
    })
    expect(getProcessMemoryMock).toHaveBeenCalledTimes(1)
  })

  it('renders nothing while the API is unavailable (silent failure)', async () => {
    getProcessMemoryMock.mockRejectedValue(new Error('Wails App bindings are not available'))

    await render()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PROCESS_MEMORY_POLL_MS * 2)
    })

    expect(container.textContent).toBe('')
    expect(container.firstElementChild).toBeNull()
  })

  it('keeps showing the last good sample across a failed poll', async () => {
    getProcessMemoryMock
      .mockResolvedValueOnce(1_234_567_896)
      .mockRejectedValueOnce(new Error('transient'))

    await render()
    await flushMicrotasks()
    expect(container.textContent).toBe('RSS 1177 MB')

    await act(async () => {
      await vi.advanceTimersByTimeAsync(PROCESS_MEMORY_POLL_MS)
    })
    expect(container.textContent).toBe('RSS 1177 MB')
  })
})
