import { useRef, useEffect, useCallback, useMemo } from 'react'
import { EditorView, placeholder } from '@codemirror/view'
import { EditorState, Compartment } from '@codemirror/state'
import { createChatExtensions } from '@/lib/cmChatExtensions'

interface UseChatEditorOptions {
  disabled: boolean
  placeholder: string
  onSend: () => void
  onContentChange?: (hasContent: boolean) => void
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
  const editableComp = useRef(new Compartment())
  const placeholderComp = useRef(new Compartment())

  // Keep callback refs up to date without recreating extensions.
  onSendRef.current = options.onSend
  onContentChangeRef.current = options.onContentChange

  // Create the editor on mount.
  useEffect(() => {
    const container = containerRef.current
    if (!container) return

    const extensions = createChatExtensions(onSendRef)

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
