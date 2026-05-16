import { EditorView } from '@codemirror/view'
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { tags } from '@lezer/highlight'
import type { Extension } from '@codemirror/state'

export function getCSSVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

/**
 * One Dark highlight style using CSS custom properties as color source.
 * Shared between the read-only file viewer theme and the editable chat theme.
 */
export function createOneDarkHighlightStyle(): HighlightStyle {
  const comment = getCSSVar('--color-hljs-comment')
  const keyword = getCSSVar('--color-hljs-keyword')
  const literal = getCSSVar('--color-hljs-literal')
  const string = getCSSVar('--color-success')
  const number = getCSSVar('--color-warning')
  const type = getCSSVar('--color-highlight')
  const fn = getCSSVar('--color-info')
  const tag = getCSSVar('--color-destructive')

  return HighlightStyle.define([
    { tag: [tags.comment, tags.lineComment, tags.blockComment], color: comment, fontStyle: 'italic' },
    { tag: [tags.keyword, tags.controlKeyword, tags.operatorKeyword, tags.definitionKeyword, tags.moduleKeyword], color: keyword },
    { tag: [tags.bool, tags.null, tags.self, tags.atom, tags.unit], color: literal },
    { tag: [tags.string, tags.special(tags.string), tags.regexp], color: string },
    { tag: [tags.number, tags.integer, tags.float], color: number },
    { tag: [tags.variableName, tags.attributeName, tags.propertyName], color: number },
    { tag: [tags.typeName, tags.className, tags.namespace], color: type },
    { tag: [tags.function(tags.variableName), tags.function(tags.definition(tags.variableName))], color: fn },
    { tag: [tags.tagName, tags.angleBracket], color: tag },
    { tag: tags.meta, color: literal },
    { tag: [tags.name, tags.deleted], color: tag },
    { tag: tags.emphasis, fontStyle: 'italic' },
    { tag: tags.strong, fontWeight: 'bold' },
    { tag: tags.link, color: fn, textDecoration: 'underline' },
    { tag: tags.heading, color: tag, fontWeight: 'bold' },
  ])
}

/**
 * Read-only One Dark theme for the file viewer.
 * Cursor and caret are hidden; gutters are transparent.
 */
export function createOneDarkCMTheme(): Extension {
  const fg = getCSSVar('--color-foreground')
  const gutterFg = getCSSVar('--color-hljs-comment')
  const selection = getCSSVar('--color-muted')

  const theme = EditorView.theme({
    '&': {
      backgroundColor: 'transparent',
      color: fg,
    },
    '.cm-content': {
      caretColor: 'transparent',
      fontFamily: "'SauceCodePro NF', Menlo, Monaco, 'Courier New', monospace",
      lineHeight: '1.25rem',
    },
    '.cm-cursor, .cm-dropCursor': {
      display: 'none',
    },
    '&.cm-focused .cm-selectionBackground, .cm-selectionBackground': {
      backgroundColor: selection,
    },
    '.cm-activeLine': {
      backgroundColor: 'transparent',
    },
    '.cm-gutters': {
      backgroundColor: 'transparent',
      color: gutterFg,
      borderRight: 'none',
    },
    '.cm-activeLineGutter': {
      backgroundColor: 'transparent',
    },
    '.cm-lineNumbers .cm-gutterElement': {
      minWidth: '3rem',
      paddingRight: '0.75rem',
    },
    '.cm-scroller': {
      overflow: 'auto',
    },
  }, { dark: true })

  return [theme, syntaxHighlighting(createOneDarkHighlightStyle())]
}
