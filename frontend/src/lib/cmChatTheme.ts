import { EditorView } from '@codemirror/view'
import { syntaxHighlighting } from '@codemirror/language'
import type { Extension } from '@codemirror/state'
import { getCSSVar, createOneDarkHighlightStyle } from './cmTheme'
import { FONT_SANS_STACK } from './fonts'

/**
 * Editable One Dark theme for the chat input editor.
 * Visible cursor, no gutters, chat-appropriate font sizing.
 *
 * Colors are resolved from CSS custom properties at call time; callers must
 * re-create (via a Compartment reconfigure) on theme change. `isDark` is
 * forwarded to CodeMirror so the autocomplete tooltip and selection layer use
 * palette-appropriate defaults.
 */
export function createChatEditorTheme(isDark: boolean = true): Extension {
  const fg = getCSSVar('--color-foreground')
  const caret = getCSSVar('--color-info')
  const selection = getCSSVar('--color-muted')

  const theme = EditorView.theme({
    '&': {
      backgroundColor: 'transparent',
      color: fg,
    },
    '.cm-content': {
      caretColor: 'transparent',
      fontSize: 'var(--text-sm)',
      lineHeight: '1.5',
      padding: '0.25rem 0',
    },
    '.cm-cursor, .cm-dropCursor': {
      borderLeftColor: caret,
      borderLeftWidth: '1.5px',
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
      fontFamily: FONT_SANS_STACK,
    },
    '&.cm-focused': {
      outline: 'none',
    },
  }, { dark: isDark })

  return [theme, syntaxHighlighting(createOneDarkHighlightStyle())]
}
