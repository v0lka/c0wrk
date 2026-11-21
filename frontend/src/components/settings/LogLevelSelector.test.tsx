// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Only the two config calls the component makes (partial-mock convention).
const mocks = vi.hoisted(() => ({
  getLogLevel: vi.fn<() => Promise<string>>(),
  setLogLevel: vi.fn<(level: string) => Promise<void>>(),
}))

vi.mock('@/api/config', () => ({
  getLogLevel: mocks.getLogLevel,
  setLogLevel: mocks.setLogLevel,
}))

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { logger } from '@/lib/logger'

import { LogLevelSelector } from './LogLevelSelector'

describe('LogLevelSelector', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    mocks.getLogLevel.mockReset()
    mocks.setLogLevel.mockReset()
    vi.mocked(logger.error).mockClear()
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    vi.restoreAllMocks()
  })

  function render(): void {
    act(() => {
      root.render(<LogLevelSelector />)
    })
  }

  async function settle(): Promise<void> {
    await act(async () => {
      await Promise.resolve()
    })
  }

  it('loads the current level and marks it selected', async () => {
    mocks.getLogLevel.mockResolvedValue('WARN')
    render()
    await settle()

    expect(mocks.getLogLevel).toHaveBeenCalledTimes(1)
    const checked = container.querySelector('[role="radio"][aria-checked="true"]')
    expect(checked?.textContent).toBe('Warn')
  })

  it('renders the compact Autonomy-mode track — no fullWidth/flex-1 stretch', async () => {
    mocks.getLogLevel.mockResolvedValue('INFO')
    render()
    await settle()

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

  it('persists a picked level and moves the selection', async () => {
    mocks.getLogLevel.mockResolvedValue('INFO')
    mocks.setLogLevel.mockResolvedValue(undefined)
    render()
    await settle()

    // Items are DEBUG / INFO / WARN / ERROR in order.
    const debug = container.querySelector('[role="radio"]') as HTMLButtonElement
    await act(async () => {
      debug.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
    })

    expect(mocks.setLogLevel).toHaveBeenCalledWith('DEBUG')
    const checked = container.querySelector('[role="radio"][aria-checked="true"]')
    expect(checked?.textContent).toBe('Debug')
    expect(vi.mocked(logger.error)).not.toHaveBeenCalled()
  })
})
