// @vitest-environment jsdom
//
// Tests for the file-tree delete confirmation (issue #60): the target is
// named, Cancel is a no-op, Delete dispatches onConfirm, a backend error
// renders inline with role=alert.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { FileTreeDeleteConfirmDialog } from './FileTreeDeleteConfirmDialog'
import type { FileEntry } from '@/types/models'

const fileEntry: FileEntry = { name: 'main.go', path: '/ws/main.go', is_dir: false }
const dirEntry: FileEntry = { name: 'src', path: '/ws/src', is_dir: true }

describe('FileTreeDeleteConfirmDialog', () => {
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

  function render(target: FileEntry | null, opts: { deleting?: boolean; deleteError?: string | null } = {}): {
    onConfirm: ReturnType<typeof vi.fn>
    onClose: ReturnType<typeof vi.fn>
  } {
    const onConfirm = vi.fn()
    const onClose = vi.fn()
    act(() => {
      root.render(
        <FileTreeDeleteConfirmDialog
          target={target}
          deleting={opts.deleting ?? false}
          deleteError={opts.deleteError ?? null}
          onConfirm={onConfirm}
          onClose={onClose}
        />,
      )
    })
    return { onConfirm, onClose }
  }

  // Radix Dialog portals content into document.body.
  function buttonByText(label: string): HTMLButtonElement | undefined {
    return Array.from(document.body.querySelectorAll<HTMLButtonElement>('button')).find(
      (b) => b.textContent?.trim() === label,
    )
  }

  /**
   * Long-token assertions for PathWithBreaks: the token renders as ONE span
   * whose textContent is char-identical to the source (a <wbr> is an element,
   * not a character), with exactly one <wbr> per `/`/`\` separator, each
   * sitting immediately after its separator, and no injected zero-width chars.
   */
  function assertTokenBreaks(token: string): void {
    const text = document.body.textContent ?? ''
    expect(text).toContain(token)
    expect(text).not.toContain('\u200b')
    expect(text).not.toContain('\u200c')
    expect(text).not.toContain('\u200d')
    expect(text).not.toContain('\ufeff')
    const span = Array.from(document.body.querySelectorAll('span')).find(
      (s) => s.textContent === token,
    )
    expect(span).not.toBeUndefined()
    const wbrs = span!.querySelectorAll('wbr')
    expect(wbrs.length).toBe((token.match(/[/\\]/g) ?? []).length)
    for (const wbr of wbrs) {
      const prev = (wbr.previousSibling?.textContent ?? '').slice(-1)
      expect(prev === '/' || prev === '\\').toBe(true)
    }
  }

  it('renders nothing (closed) without a target', () => {
    render(null)
    expect(document.body.querySelector('[role="dialog"]')).toBeNull()
  })

  it('names a file target without the directory caveat', () => {
    render(fileEntry)
    expect(document.body.querySelector('[role="dialog"]')).not.toBeNull()
    expect(document.body.textContent).toContain('main.go')
    expect(document.body.textContent).not.toContain('everything inside it')
  })

  it('names a directory target with the recursive caveat', () => {
    render(dirEntry)
    expect(document.body.textContent).toContain('src')
    expect(document.body.textContent).toContain('everything inside it')
  })

  it('wraps a long nested target name at separators without altering the text', () => {
    const longName = 'deeply/nested/very-long-directory-name'
    render({ name: longName, path: `/ws/${longName}`, is_dir: true })
    assertTokenBreaks(longName)
    expect(document.body.textContent).toContain(
      `${longName} and everything inside it will be removed permanently.`,
    )
  })

  it('Cancel closes without confirming', () => {
    const { onConfirm, onClose } = render(fileEntry)
    buttonByText('Cancel')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it('Delete confirms', () => {
    const { onConfirm, onClose } = render(fileEntry)
    buttonByText('Delete')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(onConfirm).toHaveBeenCalledTimes(1)
    expect(onClose).not.toHaveBeenCalled()
  })

  it('renders a backend failure inline', () => {
    render(fileEntry, { deleteError: 'failed to delete' })
    expect(document.body.querySelector('[role="alert"]')?.textContent).toBe('failed to delete')
  })
})
