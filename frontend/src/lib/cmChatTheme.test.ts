// Tests that the chat-editor CodeMirror theme resolves its scroller font
// through the `--font-sans` CSS custom property instead of a baked literal
// stack. The chat input renders through this theme; a literal stack there
// would silently ignore the user's UI-font choice, which fontStore delivers
// as an inline override of `--font-sans` on <html> — the same cascade every
// other DOM element reads.
//
// EditorView.theme compiles its spec and mounts the stylesheet into
// document.head when a view is constructed, so the assertions read the
// mounted CSS text.
// @vitest-environment jsdom
import { describe, it, expect, afterEach } from 'vitest'
import { EditorView } from '@codemirror/view'
import { EditorState } from '@codemirror/state'

import { createChatEditorTheme } from './cmChatTheme'
import { FONT_SANS_STACK } from './fonts'

/** CSS text of every <style> element currently mounted in document.head. */
function mountedStylesCSS(): string {
  return Array.from(document.head.querySelectorAll('style'))
    .map((s) => s.textContent ?? '')
    .join('\n')
}

/** Mount a throwaway view carrying the theme under test. */
function mountView(extensions: unknown[]): EditorView {
  const host = document.createElement('div')
  document.body.appendChild(host)
  return new EditorView({
    state: EditorState.create({ extensions: extensions as never }),
    parent: host,
  })
}

describe('createChatEditorTheme — scroller font follows the CSS var', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  it('mounts .cm-scroller with font-family: var(--font-sans)', () => {
    const view = mountView([createChatEditorTheme(true)])
    const css = mountedStylesCSS()

    expect(css).toMatch(/\.cm-scroller[^{}]*\{[^{}]*font-family:\s*var\(--font-sans\)/)

    view.destroy()
  })

  it('never bakes the stock literal sans stack (the regression this pins)', () => {
    const view = mountView([createChatEditorTheme(true)])
    const css = mountedStylesCSS()

    // The literal would render with the default font regardless of the
    // user's UI-font choice.
    expect(css).not.toContain(FONT_SANS_STACK)

    view.destroy()
  })
})
