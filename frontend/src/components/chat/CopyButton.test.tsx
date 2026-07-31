// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  g.IS_REACT_ACT_ENVIRONMENT = true
})

import { CopyButton } from './CopyButton'

describe('CopyButton', () => {
  let container: HTMLElement
  let root: Root
  let clipboardSetText: ReturnType<typeof vi.fn>

  beforeEach(() => {
    vi.useFakeTimers()
    clipboardSetText = vi.fn().mockResolvedValue(true)
    // Copy must go through the native Wails runtime, NOT navigator.clipboard
    // (mirrors FileViewerTabContextMenu/FileTreeContextMenu). Set the Web
    // Clipboard API to undefined to prove the native path is taken.
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: undefined,
    })
    Object.defineProperty(globalThis, 'runtime', {
      configurable: true,
      value: { ClipboardSetText: clipboardSetText },
    })
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  const render = (props: React.ComponentProps<typeof CopyButton>) =>
    act(() => {
      root.render(<CopyButton {...props} />)
    })

  it('renders the idle "Copy" label with the provided text', () => {
    render({ text: 'hello world' })
    expect(container.textContent).toContain('Copy')
    expect(clipboardSetText).not.toHaveBeenCalled()
  })

  it('copies the text to the clipboard and flips to "Copied" on click', async () => {
    render({ text: 'answer text' })
    const button = container.querySelector('button')!
    await act(async () => {
      button.click()
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(clipboardSetText).toHaveBeenCalledTimes(1)
    expect(clipboardSetText).toHaveBeenCalledWith('answer text')
    expect(container.textContent).toContain('Copied')
  })

  it('reverts to "Copy" after the timeout elapses', async () => {
    render({ text: 'revert me' })
    const button = container.querySelector('button')!
    await act(async () => {
      button.click()
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(container.textContent).toContain('Copied')
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })
    expect(container.textContent).toContain('Copy')
    expect(container.textContent).not.toContain('Copied')
  })
})
