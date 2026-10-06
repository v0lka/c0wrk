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

  it('mounts the autocomplete tooltip list with font-family: var(--font-sans)', () => {
    const view = mountView([createChatEditorTheme(true)])
    const css = mountedStylesCSS()

    // The tooltip (/ skills, @ files, # agents) must ride the same UI-font
    // chain as the rest of the chat UI. The rule lives in THIS theme (not
    // index.css) because @codemirror/autocomplete's baseTheme beats any
    // global rule there — see the cascade comment in cmChatTheme.ts; the
    // live cascade winner is asserted by test/cmTooltipCascade.test.ts.
    expect(css).toMatch(
      /\.cm-tooltip\.cm-tooltip-autocomplete > ul[^{}]*\{[^{}]*font-family:\s*var\(--font-sans\)/,
    )
    // CM's base theme sets font-size/max-height too — ours must be present
    // (the var-only size token keeps the type scale authoritative).
    expect(css).toMatch(
      /\.cm-tooltip\.cm-tooltip-autocomplete > ul[^{}]*\{[^{}]*font-size:\s*var\(--text-xs\)/,
    )
    expect(css).toMatch(
      /\.cm-tooltip\.cm-tooltip-autocomplete > ul[^{}]*\{[^{}]*max-height:\s*480px/,
    )

    view.destroy()
  })
})
