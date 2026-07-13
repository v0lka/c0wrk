// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  g.IS_REACT_ACT_ENVIRONMENT = true
})

import { MessageFooter } from './MessageFooter'

describe('MessageFooter', () => {
  let container: HTMLElement
  let root: Root
  let writeText: ReturnType<typeof vi.fn>

  beforeEach(() => {
    vi.useFakeTimers()
    writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    })
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  const render = (props: React.ComponentProps<typeof MessageFooter>) =>
    act(() => {
      root.render(<MessageFooter {...props} />)
    })

  it('renders the timestamp text', () => {
    render({ time: '12:34' })
    expect(container.textContent).toContain('12:34')
  })

  it('renders a copy control when copyText is provided', () => {
    render({ copyText: 'hello' })
    expect(container.textContent).toContain('Copy')
  })

  it('hides the copy control when copyText is omitted', () => {
    render({ time: '12:34' })
    expect(container.textContent).not.toContain('Copy')
    expect(container.textContent).toContain('12:34')
  })

  it('hides the copy control when copyText is blank', () => {
    render({ copyText: '   ', time: '12:34' })
    expect(container.textContent).not.toContain('Copy')
  })

  it('reserves a fixed-height row and reveals the copy control via opacity only (no height animation)', () => {
    render({ copyText: 'hello', time: '12:34' })
    const row = container.firstElementChild as HTMLElement
    // Fixed row height keeps the layout stable regardless of hover state.
    expect(row.className).toContain('h-6')
    expect(row.className).not.toContain('max-h')
    // The copy control is present but starts transparent — revealed on
    // group-hover without changing the row height.
    const copyWrapper = row.querySelector('button')?.parentElement as HTMLElement
    expect(copyWrapper.className).toContain('opacity-0')
    expect(copyWrapper.className).toContain('group-hover:opacity-100')
    expect(copyWrapper.className).not.toContain('max-h')
  })

  it('copies the provided text to the clipboard on click', async () => {
    render({ copyText: 'copy me' })
    const button = container.querySelector('button')!
    await act(async () => {
      button.click()
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(writeText).toHaveBeenCalledTimes(1)
    expect(writeText).toHaveBeenCalledWith('copy me')
  })

  it('does not bubble the copy click to ancestor handlers', async () => {
    const parentClick = vi.fn()
    act(() => {
      root.render(
        <div onClick={parentClick}>
          <MessageFooter copyText="hello" time="12:34" />
        </div>,
      )
    })
    const button = container.querySelector('button')!
    await act(async () => {
      button.click()
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(writeText).toHaveBeenCalledTimes(1)
    expect(parentClick).not.toHaveBeenCalled()
  })
})
