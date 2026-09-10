import { useEffect, useRef } from 'react'
import { EditorView, placeholder as cmPlaceholder } from '@codemirror/view'
import { EditorState, Compartment } from '@codemirror/state'
import { markdown } from '@codemirror/lang-markdown'
import { cn } from '@/lib/utils'
import { createOneDarkCMTheme } from '@/lib/cmTheme'
import { useThemeStore } from '@/stores/themeStore'

interface MiniCodeMirrorFieldProps {
  value: string
  onChange: (value: string) => void
  /** Optional placeholder rendered while the document is empty. */
  placeholder?: string
  /**
   * Optional accessible label for the editable region (rendered as
   * role="textbox" aria-label). Required when multiple fields share a
   * screen/form so tests and AT can address them individually.
   */
  ariaLabel?: string
  /**
   * Enable soft word wrap (`EditorView.lineWrapping`) so long lines fold
   * instead of scrolling horizontally.
   */
  lineWrapping?: boolean
  /**
   * Tailwind classes merged over the container defaults via twMerge, so
   * callers can override the height bounds (e.g. `min-h-0 max-h-none flex-1`
   * to let the field fill a flex column).
   */
  className?: string
}

/**
 * A small editable CodeMirror instance for individual plan fields.
 */
export function MiniCodeMirrorField({ value, onChange, placeholder, ariaLabel, lineWrapping, className }: MiniCodeMirrorFieldProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)
  const themeCompartment = useRef(new Compartment())
  const theme = useThemeStore((s) => s.theme)

  // The view is created exactly once per mount (effect below), so the update
  // listener must call the LATEST onChange through a ref: capturing the
  // mount-time callback would freeze whatever mutable state the caller's
  // handler closed over at mount (e.g. an editable draft object), and every
  // later keystroke would silently write that stale state back — reverting
  // sibling-field edits made after mount, or another card's fields entirely
  // when the same field instance is reused across selections.
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange

  // Creation-time snapshot of the doc/config props. The view must NOT be
  // recreated when they change (a value-driven re-run would rebuild the
  // editor on every keystroke and drop the cursor), so the mount effect
  // reads them through this ref and stays dependency-free; later changes
  // flow through the sync effects below.
  const mountPropsRef = useRef({ value, placeholder, lineWrapping, theme })

  useEffect(() => {
    if (!containerRef.current) return

    const {
      value: initialDoc,
      placeholder: initialPlaceholder,
      lineWrapping: initialLineWrapping,
      theme: initialTheme,
    } = mountPropsRef.current

    const state = EditorState.create({
      doc: initialDoc,
      extensions: [
        EditorView.editable.of(true),
        markdown(),
        ...(initialLineWrapping ? [EditorView.lineWrapping] : []),
        ...(initialPlaceholder ? [cmPlaceholder(initialPlaceholder)] : []),
        // Editable variant of the shared theme: the caret and cursor stay
        // visible (the default hides them for the read-only file viewer).
        themeCompartment.current.of(
          createOneDarkCMTheme(initialTheme === 'dark', { editable: true }),
        ),
        EditorView.updateListener.of((update) => {
          if (update.docChanged) {
            onChangeRef.current(update.state.doc.toString())
          }
        }),
      ],
    })

    const view = new EditorView({
      state,
      parent: containerRef.current,
    })

    viewRef.current = view

    return () => {
      view.destroy()
      viewRef.current = null
    }
  }, [])

  // Re-resolve the CodeMirror theme on app theme change (see CodeMirrorFileViewer).
  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    view.dispatch({
      effects: themeCompartment.current.reconfigure(
        createOneDarkCMTheme(theme === 'dark', { editable: true }),
      ),
    })
  }, [theme])

  // Update document when value prop changes externally (e.g., file reload).
  // Preserve cursor position to avoid jarring jumps.
  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    const currentDoc = view.state.doc.toString()
    if (currentDoc !== value) {
      const cursor = view.state.selection.main.head
      view.dispatch({
        changes: { from: 0, to: view.state.doc.length, insert: value },
        selection: { anchor: Math.min(cursor, value.length) },
      })
    }
  }, [value])

  return (
    <div
      ref={containerRef}
      role="textbox"
      aria-label={ariaLabel}
      aria-multiline="true"
      className={cn(
        // cm-viewer-container carries the shared structural styling (font,
        // scroller, scrollbar); cm-editable-field re-enables the caret the
        // viewer-scoped global rules hide (see index.css). focus-within
        // gives the field the same focus affordance as the surrounding
        // native inputs (focus:border-primary), so it reads as editable.
        'min-h-[60px] max-h-[200px] rounded border border-border bg-background focus-within:border-primary overflow-auto custom-scrollbar cm-viewer-container cm-editable-field',
        className,
      )}
    />
  )
}
