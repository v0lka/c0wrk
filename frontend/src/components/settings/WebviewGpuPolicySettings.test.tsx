// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the backend boundary so tests never touch the Wails runtime ---
const { getPolicyMock, setPolicyMock } = vi.hoisted(() => ({
  getPolicyMock: vi.fn<() => Promise<unknown>>(),
  setPolicyMock: vi.fn<(setting: string) => Promise<unknown>>(),
}))
vi.mock('@/api/webview', () => ({
  getWebviewGpuPolicy: getPolicyMock,
  setWebviewGpuPolicy: setPolicyMock,
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { logger } from '@/lib/logger'

import { WebviewGpuPolicySettings } from './WebviewGpuPolicySettings'

function makeInfo(overrides: Partial<Record<string, unknown>> = {}): Record<string, unknown> {
  return {
    setting: 'auto',
    running: 'on-demand',
    running_source: 'auto',
    fallback: null,
    restart_required: false,
    linux_only: true,
    ...overrides,
  }
}

describe('WebviewGpuPolicySettings', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    getPolicyMock.mockReset()
    setPolicyMock.mockReset()
    vi.mocked(logger.error).mockClear()
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    vi.restoreAllMocks()
  })

  function render(): void {
    act(() => {
      root.render(<WebviewGpuPolicySettings />)
    })
  }

  it('renders hidden (null) on non-Linux hosts', async () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Windows NT 10.0')
    render()
    await act(async () => {
      await Promise.resolve()
    })
    expect(container.querySelector('[data-testid="webview-gpu-policy-settings"]')).toBeNull()
    expect(getPolicyMock).not.toHaveBeenCalled()
  })

  it('loads and renders the current policy on Linux', async () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Linux x86_64')
    getPolicyMock.mockResolvedValue(makeInfo())
    render()
    await act(async () => {
      await Promise.resolve()
    })
    expect(getPolicyMock).toHaveBeenCalledTimes(1)
    const running = container.querySelector('[data-testid="webview-gpu-running"]')
    expect(running?.textContent).toContain('on-demand')
    expect(container.querySelector('[data-testid="webview-gpu-fallback"]')).toBeNull()
  })

  it('applies a picked setting and shows the restart hint', async () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Linux x86_64')
    getPolicyMock.mockResolvedValue(makeInfo())
    setPolicyMock.mockResolvedValue(makeInfo({ setting: 'never', restart_required: true }))
    render()
    await act(async () => {
      await Promise.resolve()
    })

    const neverButton = container.querySelector('[data-testid="webview-gpu-never"]') as HTMLButtonElement
    expect(neverButton).not.toBeNull()
    await act(async () => {
      neverButton.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
    })

    expect(setPolicyMock).toHaveBeenCalledWith('never')
    const running = container.querySelector('[data-testid="webview-gpu-running"]')
    expect(running?.textContent).toContain('restart to apply')
  })

  it('surfaces the watchdog fallback with a retry action that re-chooses auto', async () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Linux x86_64')
    getPolicyMock.mockResolvedValue(
      makeInfo({
        running: 'never',
        running_source: 'auto-fallback',
        restart_required: false,
        fallback: { policy: 'never', reason: 'dmabuf_render_failure', detected_at: '2026-10-15T00:00:00Z' },
      }),
    )
    setPolicyMock.mockResolvedValue(makeInfo())
    render()
    await act(async () => {
      await Promise.resolve()
    })

    const fallback = container.querySelector('[data-testid="webview-gpu-fallback"]')
    expect(fallback).not.toBeNull()
    expect(fallback?.textContent).toContain('disabled automatically')

    const retry = container.querySelector('[data-testid="webview-gpu-retry"]') as HTMLButtonElement
    await act(async () => {
      retry.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
    })
    expect(setPolicyMock).toHaveBeenCalledWith('auto')
    // The fallback banner disappears once the refreshed info has none.
    expect(container.querySelector('[data-testid="webview-gpu-fallback"]')).toBeNull()
  })

  it('explains the unobserved demotion — fresh auto without a stderr capture runs never', async () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Linux x86_64')
    getPolicyMock.mockResolvedValue(
      makeInfo({
        running: 'never',
        running_source: 'auto-unobserved',
        restart_required: true,
      }),
    )
    render()
    await act(async () => {
      await Promise.resolve()
    })
    const running = container.querySelector('[data-testid="webview-gpu-running"]')
    expect(running?.textContent).toContain('never')
    expect(running?.textContent).toContain('stderr capture unavailable')
    // No fallback banner: nothing was detected — the run simply started safe.
    expect(container.querySelector('[data-testid="webview-gpu-fallback"]')).toBeNull()
  })

  it('keeps the UI usable when the load fails', async () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Linux x86_64')
    getPolicyMock.mockRejectedValue(new Error('bridge unavailable'))
    render()
    await act(async () => {
      await Promise.resolve()
    })
    // Buttons still render (auto selected by default display state); no crash.
    expect(container.querySelector('[data-testid="webview-gpu-never"]')).not.toBeNull()
  })

  it('logs a failed set exactly once — the component is the single logging site', async () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Linux x86_64')
    getPolicyMock.mockResolvedValue(makeInfo())
    setPolicyMock.mockRejectedValue(new Error('persist failed'))
    render()
    await act(async () => {
      await Promise.resolve()
    })

    const neverButton = container.querySelector('[data-testid="webview-gpu-never"]') as HTMLButtonElement
    await act(async () => {
      neverButton.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
    })

    // The api layer propagates the rejection without logging it; the
    // component's catch is the one diagnostic a failed choice produces.
    expect(vi.mocked(logger.error)).toHaveBeenCalledTimes(1)
    expect(vi.mocked(logger.error)).toHaveBeenCalledWith('Failed to set webview GPU policy:', expect.any(Error))
  })

  it('renders the compact Autonomy-mode track — no fullWidth/flex-1 stretch', async () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Linux x86_64')
    getPolicyMock.mockResolvedValue(makeInfo())
    render()
    await act(async () => {
      await Promise.resolve()
    })

    // Security → Autonomy mode uses the primitive's default track: content
    // width (`inline-flex`) with intrinsically sized segments. `fullWidth` +
    // `itemClassName="flex-1"` stretched both, which was the reported visual
    // mismatch.
    const group = container.querySelector('[role="radiogroup"]')
    expect(group).not.toBeNull()
    expect(group?.className).toContain('inline-flex')
    expect(group?.className).not.toContain('w-full')
    const items = container.querySelectorAll('[role="radio"]')
    expect(items.length).toBe(4)
    for (const item of items) {
      expect(item.className).not.toContain('flex-1')
    }
  })
})
