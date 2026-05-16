import { EditorView } from '@codemirror/view'
import { syntaxHighlighting } from '@codemirror/language'
import type { Extension } from '@codemirror/state'
import { getCSSVar, createOneDarkHighlightStyle } from './cmTheme'

/**
 * Editable One Dark theme for the chat input editor.
 * Visible cursor, no gutters, chat-appropriate font sizing.
 */
export function createChatEditorTheme(): Extension {
  const fg = getCSSVar('--color-foreground')
  const caret = getCSSVar('--color-info')
  const selection = getCSSVar('--color-muted')

  const theme = EditorView.theme({
    '&': {
      backgroundColor: 'transparent',
      color: fg,
    },
    '.cm-content': {
      caretColor: caret,
      fontFamily: "'SauceCodePro NF', Menlo, Monaco, 'Courier New', monospace",
      fontSize: '0.875rem',
      lineHeight: '1.5',
      padding: '0.25rem 0',
    },
    '.cm-cursor, .cm-dropCursor': {
      borderLeftColor: caret,
      borderLeftWidth: '2px',
    },
    '&.cm-focused .cm-selectionBackground, .cm-selectionBackground': {
      backgroundColor: selection,
    },
    '.cm-activeLine': {
      backgroundColor: 'transparent',
    },
    '.cm-gutters': {
      display: 'none',
    },
    '.cm-scroller': {
      overflow: 'auto',
    },
    '&.cm-focused': {
      outline: 'none',
    },
  }, { dark: true })

  return [theme, syntaxHighlighting(createOneDarkHighlightStyle())]
}
