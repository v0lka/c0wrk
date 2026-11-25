// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { PathWithBreaks } from './wrapPath'

describe('PathWithBreaks', () => {
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

  function renderPath(path: string, className?: string): HTMLElement {
    act(() => {
      root.render(<PathWithBreaks path={path} className={className} />)
    })
    expect(container.children).toHaveLength(1)
    return container.firstElementChild as HTMLElement
  }

  /**
   * Copy-safety assertion: walks the rendered DOM in order, replaying the
   * character stream a user would select/copy. Every text node must match the
   * source at its offset; every `<wbr>` must sit immediately after a `/` or
   * `\` — which keeps `textContent` char-identical to the input path, since a
   * `<wbr>` contributes no characters at all.
   */
  function assertCopySafe(path: string, span: HTMLElement) {
    expect(span.textContent).toBe(path)
    let offset = 0
    let wbrCount = 0
    for (const node of Array.from(span.childNodes)) {
      if (node.nodeType === Node.TEXT_NODE) {
        const text = node.textContent ?? ''
        expect(path.startsWith(text, offset)).toBe(true)
        offset += text.length
      } else {
        expect(node.nodeType).toBe(Node.ELEMENT_NODE)
        expect((node as Element).tagName).toBe('WBR')
        wbrCount++
        expect(offset).toBeGreaterThan(0)
        expect(path[offset - 1]).toMatch(/[/\\]/)
      }
    }
    expect(offset).toBe(path.length)
    const separators = path.match(/[/\\]/g) ?? []
    expect(wbrCount).toBe(separators.length)
    expect(span.querySelectorAll('wbr')).toHaveLength(wbrCount)
  }

  it('leaves a path without separators as a single untouched text node', () => {
    const span = renderPath('plainfile.txt')
    assertCopySafe('plainfile.txt', span)
    expect(span.childNodes).toHaveLength(1)
    expect(span.childNodes[0]?.nodeType).toBe(Node.TEXT_NODE)
    expect(span.textContent).toBe('plainfile.txt')
    expect(span.querySelectorAll('wbr')).toHaveLength(0)
  })

  it('renders an empty path as an empty span with no breaks', () => {
    const span = renderPath('')
    expect(span.textContent).toBe('')
    expect(span.querySelectorAll('wbr')).toHaveLength(0)
    assertCopySafe('', span)
  })

  it('keeps textContent char-identical for POSIX paths and places a break after every separator', () => {
    const path = '/usr/local/share/c0wrk/very/deeply/nested/file.ts'
    const span = renderPath(path)
    assertCopySafe(path, span)
    expect(span.querySelectorAll('wbr')).toHaveLength(path.match(/[/\\]/g)?.length ?? 0)
  })

  it('handles consecutive separators without altering the text', () => {
    const path = 'a//b///c'
    const span = renderPath(path)
    assertCopySafe(path, span)
    expect(span.querySelectorAll('wbr')).toHaveLength(path.match(/[/\\]/g)?.length ?? 0)
  })

  it('handles a leading separator', () => {
    const path = '/root'
    const span = renderPath(path)
    assertCopySafe(path, span)
  })

  it('handles Windows paths with backslash separators', () => {
    const path = 'C:\\dir\\file'
    const span = renderPath(path)
    assertCopySafe(path, span)
    expect(span.querySelectorAll('wbr')).toHaveLength(2)
  })

  it('handles mixed separators', () => {
    const path = 'C:\\repo/pkg/sub\\mod.rs'
    const span = renderPath(path)
    assertCopySafe(path, span)
    expect(span.querySelectorAll('wbr')).toHaveLength(4)
  })

  it('never injects zero-width or extra characters into the copied text', () => {
    const path = '/tmp/some dir/file name (1).md'
    const span = renderPath(path)
    const text = span.textContent ?? ''
    expect(text).toBe(path)
    expect(text.length).toBe(path.length)
    expect(text).not.toContain('\u200b')
    expect(text).not.toContain('\u200c')
    expect(text).not.toContain('\u200d')
    expect(text).not.toContain('\ufeff')
    assertCopySafe(path, span)
  })

  it('applies className to the wrapper span', () => {
    const span = renderPath('/a/b', 'break-all text-xs')
    expect(span.className).toBe('break-all text-xs')
    assertCopySafe('/a/b', span)
  })
})
