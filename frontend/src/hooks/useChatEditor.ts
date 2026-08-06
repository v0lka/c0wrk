import { useRef, useEffect, useCallback, useMemo } from 'react'
import { EditorView, placeholder } from '@codemirror/view'
import { EditorState, Compartment } from '@codemirror/state'
import { createChatExtensions } from '@/lib/cmChatExtensions'
import { createChatEditorTheme } from '@/lib/cmChatTheme'
import { useThemeStore } from '@/stores/themeStore'

/**
 * Decide whether a paste event should take the NATIVE fast path (let
 * CodeMirror insert the text) instead of routing through the Go backend.
 *
 * The fast path applies when the clipboard carries ONLY text — either plain
 * text or text/html — and NO file-like content. Rationale:
 *  - A screenshot/"copy image" paste is exposed by the browser as an image
 *    File in `items` (kind 'file', type image/*). A Finder/Explorer "copy"
 *    of files is exposed as file entries too (type may be empty or a URI-list
 *    flavor). Either way, the presence of a file-typed item means the paste
 *    is NOT plain text and must be routed through the backend probe.
 *  - text/html (rich text from a browser/AI chat, without any image) has no
 *    file payload, so it is still a text paste — fast-path it to avoid a Go
 *    roundtrip and to keep the rich-text→markdown handling CodeMirror already
 *    does. The backend's text probe would return the same string anyway.
 *
 * Returns true → caller returns false (no preventDefault), letting CM insert
 * natively. Returns false → caller preventDefault()s and invokes onPaste.
 */
export function shouldFastPathPaste(data: DataTransfer): boolean {
  // Any file-typed item (image, or copied file/URI) ⇒ not a text fast path.
  const items = data.items
  if (items) {
    for (let i = 0; i < items.length; i++) {
      const it = items[i]
      // kind is 'string' | 'file' per the DataTransferItem spec.
      if (it && it.kind === 'file') return false
    }
  }
  // No file payload: a pure-text paste (plain and/or html). Fast path.
  return true
}

interface UseChatEditorOptions {
  disabled: boolean
  placeholder: string
  onSend: () => void
  onContentChange?: (hasContent: boolean) => void
  /** Async paste handler invoked when the editor (focused) receives a paste
   *  that is NOT a plain-text/html fast path (i.e. it may carry an image or
   *  files). Receives the clipboard DataTransfer so the handler can branch on
   *  content type. The handler MUST do its own async work (vision resolution,
   *  backend paste, staging); the editor extension does not fall back to the
   *  native paste for non-fast-path events, so the handler owns the UX. */
  onPaste?: (data: DataTransfer) => Promise<void>
}

export interface ChatEditorAPI {
  containerRef: React.RefObject<HTMLDivElement | null>
  getText: () => string
  setText: (s: string) => void
  clear: () => void
  focus: () => void
  /** Insert text at the current cursor position, or append to the end if unfocused. */
  insertAtCursor: (text: string) => void
}

/**
 * Hook that creates and manages a CodeMirror EditorView for the chat input.
 * Returns an imperative API for reading/writing text and focusing.
 */
export function useChatEditor(options: UseChatEditorOptions): ChatEditorAPI {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const viewRef = useRef<EditorView | null>(null)
  const onSendRef = useRef<(() => void) | null>(options.onSend)
  const onContentChangeRef = useRef(options.onContentChange)
  const onPasteRef = useRef<((data: DataTransfer) => Promise<void>) | null>(options.onPaste)
  const editableComp = useRef(new Compartment())
  const placeholderComp = useRef(new Compartment())
  const themeCompartment = useRef(new Compartment())
  const theme = useThemeStore((s) => s.theme)

  // Keep callback refs up to date without recreating extensions.
  onSendRef.current = options.onSend
  onContentChangeRef.current = options.onContentChange
  onPasteRef.current = options.onPaste

  // Create the editor on mount.
  useEffect(() => {
    const container = containerRef.current
    if (!container) return

    const extensions = createChatExtensions(onSendRef, themeCompartment.current)

    const state = EditorState.create({
      doc: '',
      extensions: [
        editableComp.current.of(EditorView.editable.of(!options.disabled)),
        placeholderComp.current.of(placeholder(options.placeholder)),
        ...extensions,
        EditorView.updateListener.of((update) => {
          if (update.docChanged) {
            const hasContent = update.state.doc.length > 0
            onContentChangeRef.current?.(hasContent)
          }
        }),
        // Paste interception — SCOPED to the editor: this handler only fires
        // when the CodeMirror editor itself is the paste target (it is
        // focused). It is NOT a document-wide 'paste' listener. Plain
        // text/html pastes take the fast path (default CM behavior, no Go
        // roundtrip); anything that may carry an image/files is routed
        // through the async onPaste handler (vision-gated staging).
        EditorView.domEventHandlers({
          paste(event: ClipboardEvent): boolean {
            const data = event.clipboardData
            if (!data) return false
            if (shouldFastPathPaste(data)) {
              // Let CodeMirror insert the text natively — no Go roundtrip.
              return false
            }
            // Potentially an image/file: take over and route via the backend.
            event.preventDefault()
            void onPasteRef.current?.(data)
            return true
          },
        }),
      ],
    })

    const view = new EditorView({ state, parent: container })
    viewRef.current = view

    return () => {
      view.destroy()
      viewRef.current = null
    }
    // Only run on mount/unmount — dynamic updates via compartments.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Reconfigure editable compartment when disabled changes.
  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    view.dispatch({
      effects: editableComp.current.reconfigure(EditorView.editable.of(!options.disabled)),
    })
  }, [options.disabled])

  // Reconfigure placeholder compartment when placeholder text changes.
  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    view.dispatch({
      effects: placeholderComp.current.reconfigure(placeholder(options.placeholder)),
    })
  }, [options.placeholder])

  // Re-resolve the editor theme (palette + { dark } flag) on app theme change.
  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    view.dispatch({
      effects: themeCompartment.current.reconfigure(createChatEditorTheme(theme === 'dark')),
    })
  }, [theme])

  const getText = useCallback((): string => {
    return viewRef.current?.state.doc.toString() ?? ''
  }, [])

  const setText = useCallback((s: string): void => {
    const view = viewRef.current
    if (!view) return
    view.dispatch({
      changes: { from: 0, to: view.state.doc.length, insert: s },
    })
  }, [])

  const clear = useCallback((): void => {
    setText('')
  }, [setText])

  const focus = useCallback((): void => {
    viewRef.current?.focus()
  }, [])

  const insertAtCursor = useCallback((text: string): void => {
    const view = viewRef.current
    if (!view) return
    const from = view.hasFocus
      ? view.state.selection.main.head
      : view.state.doc.length
    view.dispatch({
      changes: { from, insert: text },
      // Place cursor after the inserted text.
      selection: { anchor: from + text.length },
    })
    view.focus()
  }, [])

  return useMemo(() => ({ containerRef, getText, setText, clear, focus, insertAtCursor }), [getText, setText, clear, focus, insertAtCursor])
}
