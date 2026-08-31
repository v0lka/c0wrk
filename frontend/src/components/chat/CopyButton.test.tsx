// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { CopyButton } from './CopyButton'
import { clipboardSetText, emit } from '@/api/runtime'
import { saveMessageAsMarkdown } from '@/api/files'

// Copy must go through the native Wails runtime, NOT navigator.clipboard
// (mirrors FileViewerTabContextMenu/FileTreeContextMenu); the save path goes
// through the backend RPC. Both boundary modules are mocked so no real
// window.runtime / window.go binding is touched.
vi.mock('@/api/runtime', () => ({
  clipboardSetText: vi.fn().mockResolvedValue(true),
  emit: vi.fn(),
}))

vi.mock('@/api/files', () => ({
  saveMessageAsMarkdown: vi.fn(),
}))

const pressShift = () =>
  act(() => {
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Shift' }))
  })

const releaseShift = () =>
  act(() => {
    window.dispatchEvent(new KeyboardEvent('keyup', { key: 'Shift' }))
  })

describe('CopyButton', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    vi.useFakeTimers()
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
    // Reset the module-level Shift state shared by all useShiftHeld consumers
    // BEFORE unmounting: unmount detaches the store's window listeners, so a
    // keyup/blur dispatched afterwards never reaches it and a held Shift
    // would leak into the next test (its button would mount as "Save").
    releaseShift()
    act(() => {
      window.dispatchEvent(new Event('blur'))
    })
    act(() => root.unmount())
    vi.clearAllMocks()
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

  it('morphs into "Save" while Shift is held and back on release', () => {
    render({ text: 'hello' })
    expect(container.textContent).toContain('Copy')
    expect(container.textContent).not.toContain('Save')

    pressShift()
    expect(container.textContent).toContain('Save')
    expect(container.textContent).not.toContain('Copy')

    releaseShift()
    expect(container.textContent).toContain('Copy')
    expect(container.textContent).not.toContain('Save')
  })

  it('saves the message to a file on Shift+click and flashes "Saved"', async () => {
    vi.mocked(saveMessageAsMarkdown).mockResolvedValue('/proj/message.md')
    render({ text: 'answer text' })
    // Guards Shift-state isolation: a leak from a previous test would mount
    // the button in its "Save" variant before pressShift() runs.
    expect(container.textContent).toContain('Copy')
    pressShift()

    const button = container.querySelector('button')!
    await act(async () => {
      button.click()
      await vi.advanceTimersByTimeAsync(0)
    })

    expect(saveMessageAsMarkdown).toHaveBeenCalledTimes(1)
    expect(saveMessageAsMarkdown).toHaveBeenCalledWith('answer text')
    // The Shift variant must never touch the clipboard.
    expect(clipboardSetText).not.toHaveBeenCalled()
    expect(container.textContent).toContain('Saved')
  })

  it('stays idle when the save dialog is cancelled', async () => {
    vi.mocked(saveMessageAsMarkdown).mockResolvedValue('')
    render({ text: 'answer text' })
    // Guards Shift-state isolation (see the save test above).
    expect(container.textContent).toContain('Copy')
    pressShift()

    const button = container.querySelector('button')!
    await act(async () => {
      button.click()
      await vi.advanceTimersByTimeAsync(0)
    })

    expect(saveMessageAsMarkdown).toHaveBeenCalledTimes(1)
    expect(container.textContent).not.toContain('Saved')
    expect(container.textContent).toContain('Save')
    expect(emit).not.toHaveBeenCalled()
  })

  it('surfaces a runtime_error toast when the save fails', async () => {
    vi.mocked(saveMessageAsMarkdown).mockRejectedValue(new Error('boom'))
    render({ text: 'answer text' })
    // Guards Shift-state isolation (see the save test above).
    expect(container.textContent).toContain('Copy')
    pressShift()

    const button = container.querySelector('button')!
    await act(async () => {
      button.click()
      await vi.advanceTimersByTimeAsync(0)
    })

    expect(emit).toHaveBeenCalledWith(
      'runtime_error',
      expect.objectContaining({ message: 'Failed to save message to file' }),
    )
    expect(container.textContent).not.toContain('Saved')
  })
})
