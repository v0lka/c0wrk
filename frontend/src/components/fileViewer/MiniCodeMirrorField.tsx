import { useEffect, useRef } from 'react'
import { EditorView } from '@codemirror/view'
import { EditorState, Compartment } from '@codemirror/state'
import { markdown } from '@codemirror/lang-markdown'
import { createOneDarkCMTheme } from '@/lib/cmTheme'
import { useThemeStore } from '@/stores/themeStore'

interface MiniCodeMirrorFieldProps {
  value: string
  onChange: (value: string) => void
}

/**
 * A small editable CodeMirror instance for individual plan fields.
 */
export function MiniCodeMirrorField({ value, onChange }: MiniCodeMirrorFieldProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)
  const themeCompartment = useRef(new Compartment())
  const theme = useThemeStore((s) => s.theme)

  useEffect(() => {
    if (!containerRef.current) return

    const state = EditorState.create({
      doc: value,
      extensions: [
        EditorView.editable.of(true),
        markdown(),
        themeCompartment.current.of(createOneDarkCMTheme(theme === 'dark')),
        EditorView.updateListener.of((update) => {
          if (update.docChanged) {
            onChange(update.state.doc.toString())
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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Re-resolve the CodeMirror theme on app theme change (see CodeMirrorFileViewer).
  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    view.dispatch({
      effects: themeCompartment.current.reconfigure(createOneDarkCMTheme(theme === 'dark')),
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
      className="min-h-[60px] max-h-[200px] border border-border rounded overflow-auto cm-viewer-container"
    />
  )
}
