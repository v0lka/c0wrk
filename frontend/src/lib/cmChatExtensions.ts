import { markdown } from '@codemirror/lang-markdown'
import { EditorView, drawSelection } from '@codemirror/view'
import { history, defaultKeymap, historyKeymap } from '@codemirror/commands'
import { keymap } from '@codemirror/view'
import { Compartment, type Extension } from '@codemirror/state'
import type { MutableRefObject } from 'react'
import { createChatEditorTheme } from './cmChatTheme'
import { referenceHighlighter } from './cmReferenceHighlighter'
import { createChatKeymap } from './cmChatKeymap'
import { createChatAutocomplete } from './cmChatAutocomplete'

/**
 * Assemble all CodeMirror extensions for the chat input editor.
 * Placeholder and editable state are managed via Compartments in useChatEditor.
 *
 * `themeCompartment` wraps the editor theme so the caller can reconfigure it
 * (swap the palette + { dark } flag) when the app theme changes without
 * recreating the view.
 */
export function createChatExtensions(
  onSendRef: MutableRefObject<(() => void) | null>,
  themeCompartment: Compartment,
): Extension[] {
  return [
    themeCompartment.of(createChatEditorTheme(true)),
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
