// @vitest-environment jsdom
//
// Tests for the in-tree inline create/rename input (issue #60): Enter
// commits, Escape and blur cancel, key events during an active IME
// composition are ignored, invalid names block the commit with a
// clear message, a busy commit locks the input, and a server-side error
// surfaces through the error prop.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { FileTreeInlineInput } from './FileTreeInlineInput'

describe('FileTreeInlineInput', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    document.body.replaceChildren()
  })

  type RenderProps = Partial<Parameters<typeof FileTreeInlineInput>[0]> & { mode?: 'file' | 'folder' | 'rename' }

  function renderInput(props: RenderProps = {}): { onCommit: ReturnType<typeof vi.fn>; onCancel: ReturnType<typeof vi.fn> } {
    const onCommit = vi.fn()
    const onCancel = vi.fn()
    act(() => {
      root.render(
        <FileTreeInlineInput
          mode="file"
          initialName=""
          depth={1}
          busy={false}
          error={null}
          siblings={['exists.txt']}
          onCommit={onCommit}
          onCancel={onCancel}
          {...props}
        />,
      )
    })
    return { onCommit, onCancel }
  }

  function input(): HTMLInputElement {
    const el = container.querySelector('input')
    if (!el) throw new Error('inline input not rendered')
    return el
  }

  function type(value: string): void {
    // React's value tracker swallows programmatic input events — go through
    // the native setter (the house input-typing pattern).
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!
    act(() => {
      setter.call(input(), value)
      input().dispatchEvent(new Event('input', { bubbles: true }))
    })
  }

  function press(key: string): void {
    act(() => {
      input().dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
    })
  }

  function pressComposing(key: string): void {
    act(() => {
      const evt = new KeyboardEvent('keydown', { key, bubbles: true })
      // jsdom may not honor isComposing in the init dict — define the own
      // property on the instance so the component's guard sees it either way.
      Object.defineProperty(evt, 'isComposing', { value: true })
      input().dispatchEvent(evt)
    })
  }

  it('commits the typed name on Enter', () => {
    const { onCommit, onCancel } = renderInput()
    type('new-file.ts')
    press('Enter')
    expect(onCommit).toHaveBeenCalledWith('new-file.ts')
    expect(onCancel).not.toHaveBeenCalled()
  })

  it('cancels on Escape', () => {
    const { onCommit, onCancel } = renderInput({ initialName: 'old.txt', mode: 'rename' })
    press('Escape')
    expect(onCancel).toHaveBeenCalledTimes(1)
    expect(onCommit).not.toHaveBeenCalled()
  })

  it('ignores Enter and Escape during an active IME composition', () => {
    // CJK input: Enter confirms the conversion candidate and Escape cancels
    // it — neither may commit or tear down the input; after compositionend
    // the keys work again.
    const { onCommit, onCancel } = renderInput({ initialName: 'old.txt', mode: 'rename' })
    pressComposing('Enter')
    expect(onCommit).not.toHaveBeenCalled()
    pressComposing('Escape')
    expect(onCancel).not.toHaveBeenCalled()
    press('Enter')
    expect(onCommit).toHaveBeenCalledTimes(1)
    expect(onCommit).toHaveBeenCalledWith('old.txt')
  })

  it('cancels on blur', () => {
    const { onCommit, onCancel } = renderInput()
    act(() => {
      // React maps onBlur to the native focusout event.
      input().dispatchEvent(new FocusEvent('focusout', { bubbles: true }))
    })
    expect(onCancel).toHaveBeenCalledTimes(1)
    expect(onCommit).not.toHaveBeenCalled()
  })

  it('blocks the commit and shows a message for a path separator', () => {
    const { onCommit } = renderInput()
    type('a/b.txt')
    press('Enter')
    expect(onCommit).not.toHaveBeenCalled()
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('path separators')
  })

  it('blocks the commit for an existing sibling (case-insensitive)', () => {
    const { onCommit } = renderInput()
    type('EXISTS.TXT')
    press('Enter')
    expect(onCommit).not.toHaveBeenCalled()
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('already exists')
  })

  it('blocks the commit for a whitespace-only name', () => {
    const { onCommit } = renderInput()
    type('   ')
    press('Enter')
    expect(onCommit).not.toHaveBeenCalled()
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('empty')
  })

  it('ignores blur while the commit RPC is in flight', () => {
    const { onCancel } = renderInput({ busy: true })
    act(() => {
      input().dispatchEvent(new FocusEvent('focusout', { bubbles: true }))
    })
    expect(onCancel).not.toHaveBeenCalled()
  })

  it('disables the input while busy', () => {
    renderInput({ busy: true })
    expect(input().disabled).toBe(true)
  })

  it('surfaces a server-side error through the error prop', () => {
    renderInput({ error: 'path already exists' })
    expect(container.querySelector('[role="alert"]')?.textContent).toBe('path already exists')
  })
})
