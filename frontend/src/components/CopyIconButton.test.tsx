// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { CopyIconButton } from './CopyIconButton'
import { clipboardSetText } from '@/api/runtime'

// Copy must go through the native Wails runtime, NOT navigator.clipboard
// (mirrors chat/CopyButton and the context menus). The boundary module is
// mocked so no real window.runtime binding is touched.
vi.mock('@/api/runtime', () => ({
  clipboardSetText: vi.fn().mockResolvedValue(true),
}))

describe('CopyIconButton', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    vi.useFakeTimers()
    // Pin the success verdict explicitly: the failure tests below override it,
    // and this per-test setup keeps that override from leaking across tests.
    vi.mocked(clipboardSetText).mockResolvedValue(true)
    // Set the Web Clipboard API to undefined to prove the native path is taken.
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: undefined,
    })
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    // Unmount BEFORE restoring real timers: the revert timer is cleared by
    // the component's unmount cleanup, so nothing can fire into a dead tree.
    act(() => root.unmount())
    vi.clearAllMocks()
    vi.useRealTimers()
  })

  const render = (props: React.ComponentProps<typeof CopyIconButton>) =>
    act(() => {
      root.render(<CopyIconButton {...props} />)
    })

  const click = async () => {
    const button = container.querySelector('button')!
    await act(async () => {
      button.click()
      await vi.advanceTimersByTimeAsync(0)
    })
    return button
  }

  it('renders an icon-only button with the idle label in title/aria-label and never touches the clipboard on mount', () => {
    render({ text: 'hello world' })
    const button = container.querySelector('button')!
    // Icon only: the label lives in title/aria-label, there is no text span.
    expect(button.textContent).toBe('')
    expect(button.getAttribute('title')).toBe('Copy')
    expect(button.getAttribute('aria-label')).toBe('Copy')
    expect(button.querySelector('svg.lucide-copy')).not.toBeNull()
    expect(clipboardSetText).not.toHaveBeenCalled()
  })

  it('copies the text through clipboardSetText and flashes "Copied" with the success Check icon', async () => {
    render({ text: 'answer text' })
    const button = await click()
    expect(clipboardSetText).toHaveBeenCalledTimes(1)
    expect(clipboardSetText).toHaveBeenCalledWith('answer text')
    expect(button.getAttribute('title')).toBe('Copied')
    expect(button.getAttribute('aria-label')).toBe('Copied')
    expect(button.querySelector('svg.lucide-check')).not.toBeNull()
    expect(button.querySelector('svg.lucide-copy')).toBeNull()
    const icon = button.querySelector('svg')
    expect(icon?.getAttribute('class')).toContain('text-success')
    expect(icon?.getAttribute('class')).not.toContain('text-destructive')
  })

  it('reverts to the idle label after 2 seconds', async () => {
    render({ text: 'revert me' })
    const button = await click()
    expect(button.getAttribute('aria-label')).toBe('Copied')
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })
    expect(button.getAttribute('title')).toBe('Copy')
    expect(button.getAttribute('aria-label')).toBe('Copy')
    expect(button.querySelector('svg.lucide-copy')).not.toBeNull()
    expect(button.querySelector('svg.lucide-check')).toBeNull()
  })

  it('flashes "Failed" with the destructive X icon instead of "Copied" when the clipboard write reports false, and the flash is bounded', async () => {
    vi.mocked(clipboardSetText).mockResolvedValue(false)
    render({ text: 'answer text' })
    const button = await click()
    expect(clipboardSetText).toHaveBeenCalledTimes(1)
    expect(clipboardSetText).toHaveBeenCalledWith('answer text')
    // A false verdict must never render the success state.
    expect(button.getAttribute('aria-label')).not.toBe('Copied')
    expect(button.getAttribute('aria-label')).toBe('Failed')
    expect(button.querySelector('svg.lucide-check')).toBeNull()
    const icon = button.querySelector('svg')
    expect(icon?.getAttribute('class')).toContain('text-destructive')
    expect(icon?.getAttribute('class')).not.toContain('text-success')
    // The failure flash is bounded like the success flash: back to idle after 2s.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })
    expect(button.getAttribute('aria-label')).toBe('Copy')
    expect(button.querySelector('svg.lucide-x')).toBeNull()
    expect(button.querySelector('svg.lucide-copy')).not.toBeNull()
  })

  it('flashes "Failed" when the clipboard write rejects (the defensive catch path)', async () => {
    vi.mocked(clipboardSetText).mockRejectedValue(new Error('transport exploded'))
    render({ text: 'answer text' })
    const button = await click()
    expect(button.getAttribute('aria-label')).not.toBe('Copied')
    expect(button.getAttribute('aria-label')).toBe('Failed')
    expect(button.querySelector('svg.lucide-x')).not.toBeNull()
  })

  it('clears the revert timer on unmount — nothing fires into the unmounted component', async () => {
    render({ text: 'countdown' })
    await click()
    // The 2s revert timer is the only pending timer while mounted.
    expect(vi.getTimerCount()).toBe(1)
    act(() => {
      root.unmount()
    })
    // Unmount cleanup cleared it; advancing past the window must not re-add it.
    expect(vi.getTimerCount()).toBe(0)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })
    expect(vi.getTimerCount()).toBe(0)
  })

  it('uses the custom label as the idle state and returns to it after the flash', async () => {
    render({ text: 'custom', label: 'Copy snippet' })
    const button = container.querySelector('button')!
    expect(button.getAttribute('aria-label')).toBe('Copy snippet')
    const clicked = await click()
    expect(clicked.getAttribute('aria-label')).toBe('Copied')
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })
    expect(clicked.getAttribute('aria-label')).toBe('Copy snippet')
    expect(clicked.getAttribute('title')).toBe('Copy snippet')
  })
})
