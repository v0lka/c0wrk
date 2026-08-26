import { markdown } from '@codemirror/lang-markdown'
import { EditorView, drawSelection, tooltips } from '@codemirror/view'
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
    // Tooltips — the /, @, # autocomplete hint lists — must render in a
    // body-level container, NOT inside the editor.
    //
    // By default CodeMirror parents tooltips to the `.cm-editor` element and
    // positions them `position: fixed` (viewport-relative, immune to
    // `overflow: hidden` ancestors). On WebKit (the app runs in WKWebView on
    // macOS via Wails), CodeMirror additionally probes for transformed
    // ancestors via a Safari kludge that reads the tooltip's live rect after
    // each re-measure; once the tooltip carries a real coordinate, the kludge
    // misfires and CodeMirror permanently falls back to
    // `position: absolute` relative to the editor. An absolutely positioned
    // tooltip inside the editor is then clipped by the ChatInput shell's
    // `overflow-hidden`, cutting the hint list off at the input area
    // boundaries.
    //
    // A body-level parent keeps the tooltip unclipped in BOTH modes: fixed
    // tooltips are viewport-positioned, and the WebKit absolute fallback is
    // confined to CodeMirror's own container at body level (the app never
    // scrolls the page, so its coordinates match the viewport). CodeMirror
    // removes that container again when the editor is destroyed.
    // https://codemirror.net/docs/ref/#view.tooltips
    tooltips({ parent: document.body }),
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
