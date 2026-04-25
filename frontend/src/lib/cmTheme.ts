import { EditorView } from '@codemirror/view'
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { tags } from '@lezer/highlight'
import type { Extension } from '@codemirror/state'

function getCSSVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

/**
 * Create a CodeMirror One Dark theme that reads colors from CSS custom properties.
 *
 * Follows the same pattern as useXTermTheme.ts — all colors are resolved
 * from the design tokens defined in index.css @theme, ensuring a single
 * source of truth for the visual design.
 */
export function createOneDarkCMTheme(): Extension {
  const fg = getCSSVar('--color-foreground')
  const gutterFg = getCSSVar('--color-hljs-comment')
  const selection = getCSSVar('--color-muted')
  const comment = getCSSVar('--color-hljs-comment')
  const keyword = getCSSVar('--color-hljs-keyword')
  const literal = getCSSVar('--color-hljs-literal')
  const string = getCSSVar('--color-success')
  const number = getCSSVar('--color-warning')
  const type = getCSSVar('--color-highlight')
  const fn = getCSSVar('--color-info')
  const tag = getCSSVar('--color-destructive')

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

  const highlightStyle = HighlightStyle.define([
    // Comments
    { tag: [tags.comment, tags.lineComment, tags.blockComment], color: comment, fontStyle: 'italic' },

    // Keywords
    { tag: [tags.keyword, tags.controlKeyword, tags.operatorKeyword, tags.definitionKeyword, tags.moduleKeyword], color: keyword },

    // Literals (booleans, null, atoms)
    { tag: [tags.bool, tags.null, tags.self, tags.atom, tags.unit], color: literal },

    // Strings
    { tag: [tags.string, tags.special(tags.string), tags.regexp], color: string },

    // Numbers
    { tag: [tags.number, tags.integer, tags.float], color: number },

    // Variables, attributes, properties
    { tag: [tags.variableName, tags.attributeName, tags.propertyName], color: number },

    // Types, classes
    { tag: [tags.typeName, tags.className, tags.namespace], color: type },

    // Functions
    { tag: [tags.function(tags.variableName), tags.function(tags.definition(tags.variableName))], color: fn },

    // Tags (HTML)
    { tag: [tags.tagName, tags.angleBracket], color: tag },

    // Meta
    { tag: tags.meta, color: literal },

    // Semantic (section, name, deletion — matching hljs)
    { tag: [tags.name, tags.deleted], color: tag },

    // Emphasis / Strong
    { tag: tags.emphasis, fontStyle: 'italic' },
    { tag: tags.strong, fontWeight: 'bold' },

    // Links
    { tag: tags.link, color: fn, textDecoration: 'underline' },

    // Headings
    { tag: tags.heading, color: tag, fontWeight: 'bold' },
  ])

  return [theme, syntaxHighlighting(highlightStyle)]
}
