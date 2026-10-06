// Tests that the shared CodeMirror theme resolves its content font through
// the `--font-mono` CSS custom property instead of a baked literal stack.
//
// The file viewer and MiniCodeMirrorField render text through this theme; a
// literal stack there would silently ignore the user's monospace choice,
// which fontStore delivers as an inline override of `--font-mono` on <html> —
// the same cascade every other DOM element reads.
//
// EditorView.theme compiles its spec and mounts the stylesheet into
// document.head when a view is constructed, so the assertions read the
// mounted CSS text.
// @vitest-environment jsdom
import { describe, it, expect, afterEach } from 'vitest'
import { EditorView } from '@codemirror/view'
import { EditorState } from '@codemirror/state'

import { createOneDarkCMTheme } from './cmTheme'
import { FONT_MONO_STACK } from './fonts'

/** CSS text of every <style> element currently mounted in document.head. */
function mountedStylesCSS(): string {
  return Array.from(document.head.querySelectorAll('style'))
    .map((s) => s.textContent ?? '')
    .join('\n')
}

/** Mount a throwaway read-only view carrying the theme under test. */
function mountView(extensions: unknown[]): EditorView {
  const host = document.createElement('div')
  document.body.appendChild(host)
  return new EditorView({
    // The theme factory is Extension-typed; the cast keeps this helper
    // generic over both CM theme modules without weakening the assertions.
    state: EditorState.create({ extensions: extensions as never }),
    parent: host,
  })
}

describe('createOneDarkCMTheme — content font follows the CSS var', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  it('mounts .cm-content with font-family: var(--font-mono)', () => {
    const view = mountView([createOneDarkCMTheme(true)])
    const css = mountedStylesCSS()

    expect(css).toMatch(/\.cm-content[^{}]*\{[^{}]*font-family:\s*var\(--font-mono\)/)

    view.destroy()
  })

  it('never bakes the stock literal mono stack (the regression this pins)', () => {
    const view = mountView([createOneDarkCMTheme(true)])
    const css = mountedStylesCSS()

    // The literal would render with the default font regardless of the
    // user's monospace choice.
    expect(css).not.toContain(FONT_MONO_STACK)

    view.destroy()
  })
})
