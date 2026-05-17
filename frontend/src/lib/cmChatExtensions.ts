import { markdown } from '@codemirror/lang-markdown'
import { EditorView, drawSelection } from '@codemirror/view'
import { history, defaultKeymap, historyKeymap } from '@codemirror/commands'
import { keymap } from '@codemirror/view'
import type { Extension } from '@codemirror/state'
import type { MutableRefObject } from 'react'
import { createChatEditorTheme } from './cmChatTheme'
import { referenceHighlighter } from './cmReferenceHighlighter'
import { createChatKeymap } from './cmChatKeymap'
import { createChatAutocomplete } from './cmChatAutocomplete'

/**
 * Assemble all CodeMirror extensions for the chat input editor.
 * Placeholder and editable state are managed via Compartments in useChatEditor.
 */
export function createChatExtensions(
  onSendRef: MutableRefObject<(() => void) | null>,
): Extension[] {
  return [
    createChatEditorTheme(),
    drawSelection(),
    markdown(),
    referenceHighlighter,
    createChatAutocomplete(),
    createChatKeymap(onSendRef),
    keymap.of([...defaultKeymap, ...historyKeymap]),
    history(),
    EditorView.lineWrapping,
  ]
}
