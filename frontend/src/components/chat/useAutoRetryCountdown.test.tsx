// @vitest-environment jsdom
//
// Tests for the auto-resend countdown hook (useAutoRetryCountdown) — the
// UI-owned 1s ticker behind the resume banner's `Resume (Ns)` → disabled
// `Auto-resend…` transition. The hook owns the countdown entirely from the
// banner metadata's LIVE keys (`auto_retry_at` + `auto_retry_live`): there
// is no backend timer. On zero it fires resumeTask exactly once; any manual
// click stops it optimistically via `stop()`.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { readAutoRetryAt, useAutoRetryCountdown } from './useAutoRetryCountdown'

vi.mock('@/api/chat', () => ({
  resumeTask: vi.fn().mockResolvedValue(undefined),
}))
const { resumeTask } = await import('@/api/chat') as typeof import('@/api/chat')

describe('readAutoRetryAt', () => {
  it('reads a positive unix-seconds deadline from LIVE banner metadata', () => {
    expect(readAutoRetryAt({ auto_retry_at: 1_900_000_000, auto_retry_live: true })).toBe(1_900_000_000)
  })

  it('returns null without the live flag (restored rows never count down)', () => {
    // A history-reload banner carries the raw persisted payload — the
    // deadline WITHOUT auto_retry_live. It must render the plain manual
    // banner: after a restart there is no timer to keep.
    expect(readAutoRetryAt({ auto_retry_at: 1_900_000_000 })).toBeNull()
    expect(readAutoRetryAt({})).toBeNull()
    expect(readAutoRetryAt(undefined)).toBeNull()
  })

  it('returns null for zero, negative, non-finite and non-number values', () => {
    expect(readAutoRetryAt({ auto_retry_at: 0, auto_retry_live: true })).toBeNull()
    expect(readAutoRetryAt({ auto_retry_at: -5, auto_retry_live: true })).toBeNull()
    expect(readAutoRetryAt({ auto_retry_at: Number.NaN, auto_retry_live: true })).toBeNull()
    expect(readAutoRetryAt({ auto_retry_at: '1900000000', auto_retry_live: true })).toBeNull()
    expect(readAutoRetryAt({ auto_retry_at: null, auto_retry_live: true })).toBeNull()
  })
})

describe('useAutoRetryCountdown', () => {
  let container: HTMLDivElement
  let root: Root
  let latest: ReturnType<typeof useAutoRetryCountdown>
  let pending: boolean
  let autoRetryAt: number | null

  function Harness(): null {
    latest = useAutoRetryCountdown(pending, 'sess-1', 'msg-1', autoRetryAt)
    return null
  }

  beforeEach(() => {
    vi.useFakeTimers()
    vi.mocked(resumeTask).mockClear().mockResolvedValue(undefined)
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => {
      root.unmount()
    })
    container.remove()
    vi.useRealTimers()
  })

  function mount(): void {
    act(() => {
      root.render(createElement(Harness))
    })
  }

  it('counts down N→0 over the deadline window', () => {
    const deadline = Math.floor(Date.now() / 1000) + 30
    pending = true
    autoRetryAt = deadline

    mount()
    expect(latest.counting).toBe(true)
    expect(latest.secondsLeft).toBe(30)
    expect(latest.autoResending).toBe(false)

    act(() => { vi.advanceTimersByTime(10_000) })
    expect(latest.secondsLeft).toBe(20)

    act(() => { vi.advanceTimersByTime(20_000) })
    expect(latest.counting).toBe(false)
    expect(latest.secondsLeft).toBe(0)
    expect(latest.autoResending).toBe(true)
  })

  it('fires resumeTask exactly once when the deadline is reached', () => {
    const deadline = Math.floor(Date.now() / 1000) + 2
    pending = true
    autoRetryAt = deadline
    mount()

    act(() => { vi.advanceTimersByTime(2_000) })
    expect(latest.autoResending).toBe(true)
    expect(resumeTask).toHaveBeenCalledTimes(1)
    expect(resumeTask).toHaveBeenCalledWith('sess-1')

    // The ticker is cleared and the one-shot guard holds: no second fire
    // even after a long idle wait for task_resumed.
    act(() => { vi.advanceTimersByTime(60_000) })
    expect(resumeTask).toHaveBeenCalledTimes(1)
  })

  it('stop() disarms the auto fire — a manual click before zero never triggers it', () => {
    const deadline = Math.floor(Date.now() / 1000) + 30
    pending = true
    autoRetryAt = deadline
    mount()
    expect(latest.counting).toBe(true)

    // The optimistic manual stop (any click: Resume or Cancel).
    act(() => { latest.stop() })
    expect(latest.counting).toBe(false)
    expect(latest.autoResending).toBe(false)

    // The whole deadline window passes: no fire, no disabled state.
    act(() => { vi.advanceTimersByTime(60_000) })
    expect(resumeTask).not.toHaveBeenCalled()
    expect(latest.autoResending).toBe(false)
  })

  it('renders the plain banner state when the deadline is null (no live keys)', () => {
    pending = true
    autoRetryAt = null
    mount()
    expect(latest.counting).toBe(false)
    expect(latest.autoResending).toBe(false)
    // No ticker is scheduled — nothing fires.
    act(() => { vi.advanceTimersByTime(60_000) })
    expect(latest.counting).toBe(false)
    expect(resumeTask).not.toHaveBeenCalled()
  })

  it('never arms for a deadline already in the past at mount', () => {
    // Covers a deadline that expired between the live event and the mount
    // (restored banners never reach the hook at all — readAutoRetryAt
    // returns null without the live flag).
    pending = true
    autoRetryAt = Math.floor(Date.now() / 1000) - 10
    mount()
    expect(latest.counting).toBe(false)
    expect(latest.autoResending).toBe(false)
    act(() => { vi.advanceTimersByTime(60_000) })
    expect(latest.counting).toBe(false)
    expect(latest.autoResending).toBe(false)
    expect(resumeTask).not.toHaveBeenCalled()
  })

  it('strips the live keys from the banner when the auto fire fails', async () => {
    const { useChatStore } = await import('@/stores/chatStore')
    const meta = { resolved: false, auto_retry_at: 1, auto_retry_live: true }
    useChatStore.getState().messages['sess-1'] = {
      'msg-1': { id: 'msg-1', sessionId: 'sess-1', type: 'task_failed_resumable', content: 'x', metadata: meta, timestamp: 1 },
    } as never

    vi.mocked(resumeTask).mockRejectedValueOnce(new Error('session busy'))
    const deadline = Math.floor(Date.now() / 1000) + 1
    pending = true
    autoRetryAt = deadline
    mount()

    await act(async () => {
      vi.advanceTimersByTime(1_000)
    })
    // The rejection handler stripped BOTH live keys → plain manual banner.
    const after = useChatStore.getState().messages['sess-1']?.['msg-1' as never] as { metadata?: Record<string, unknown> } | undefined
    expect(after?.metadata).toEqual({ resolved: false })
    delete useChatStore.getState().messages['sess-1']
  })

  it('clears the interval on unmount', () => {
    const deadline = Math.floor(Date.now() / 1000) + 30
    pending = true
    autoRetryAt = deadline
    mount()

    const clearIntervalSpy = vi.spyOn(globalThis, 'clearInterval')
    act(() => {
      root.unmount()
    })
    expect(clearIntervalSpy).toHaveBeenCalled()
    clearIntervalSpy.mockRestore()
  })
})
