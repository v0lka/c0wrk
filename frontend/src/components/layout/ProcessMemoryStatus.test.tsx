// @vitest-environment jsdom
//
// Tests for the ProcessMemoryStatus status-bar indicator: the agreed
// "RSS N MiB" format with the exact-bytes tooltip, tick-driven updates, timer
// cleanup on unmount, its leading separator appearing only together with the
// label, and silent hiding while the API is unavailable.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const getProcessMemoryMock = vi.fn()
vi.mock('@/api/system', () => ({
  getProcessMemory: (...args: unknown[]) => getProcessMemoryMock(...args),
}))

// Countable separator stub (the UI primitive itself is not under test).
vi.mock('@/components/ui/separator', () => ({
  Separator: () => <span data-testid="sep" />,
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
  it('renders "RSS 1177 MiB" for 1,234,567,896 bytes with an exact-bytes tooltip', async () => {
    getProcessMemoryMock.mockResolvedValue(1_234_567_896)

    await render()
    await flushMicrotasks()

    expect(container.textContent).toBe('RSS 1177 MiB')
    // Exactly one separator, rendered together with the visible indicator.
    expect(container.querySelectorAll('[data-testid="sep"]')).toHaveLength(1)
    const el = container.querySelector('span[title]') as HTMLElement
    expect(el).not.toBeNull()
    expect(el.getAttribute('title')).toBe('Process memory (RSS): 1,234,567,896 bytes')
    // No aria-label: it would sit on a generic span that AT ignores.
    expect(el.getAttribute('aria-label')).toBeNull()
    // tabular-nums keeps the digits width-stable between ticks.
    expect(el.querySelector('.tabular-nums')).not.toBeNull()
  })

  it('rounds to the nearest whole mebibyte', async () => {
    getProcessMemoryMock.mockResolvedValue(3 * 1024 * 1024 + 512 * 1024) // 3.5 MiB

    await render()
    await flushMicrotasks()

    expect(container.textContent).toBe('RSS 4 MiB')
  })

  it('updates the label on every poll tick', async () => {
    getProcessMemoryMock
      .mockResolvedValueOnce(1_234_567_896)
      .mockResolvedValueOnce(2048 * 1024 * 1024)

    await render()
    await flushMicrotasks()
    expect(container.textContent).toBe('RSS 1177 MiB')

    await act(async () => {
      await vi.advanceTimersByTimeAsync(PROCESS_MEMORY_POLL_MS)
    })
    expect(container.textContent).toBe('RSS 2048 MiB')
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

  it('renders nothing — not even a stray separator — while the API is unavailable (silent failure)', async () => {
    getProcessMemoryMock.mockRejectedValue(new Error('Wails App bindings are not available'))

    await render()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PROCESS_MEMORY_POLL_MS * 2)
    })

    expect(container.textContent).toBe('')
    expect(container.querySelectorAll('[data-testid="sep"]')).toHaveLength(0)
    expect(container.firstElementChild).toBeNull()
  })

  it('keeps showing the last good sample across a failed poll', async () => {
    getProcessMemoryMock
      .mockResolvedValueOnce(1_234_567_896)
      .mockRejectedValueOnce(new Error('transient'))

    await render()
    await flushMicrotasks()
    expect(container.textContent).toBe('RSS 1177 MiB')

    await act(async () => {
      await vi.advanceTimersByTimeAsync(PROCESS_MEMORY_POLL_MS)
    })
    expect(container.textContent).toBe('RSS 1177 MiB')
  })
})
