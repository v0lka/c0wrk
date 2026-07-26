import { useEffect, useRef, useCallback, useMemo, useState } from 'react'
import { Code, Eye } from 'lucide-react'
import { EditorView } from '@codemirror/view'
import { EditorState, Compartment } from '@codemirror/state'
import { lineNumbers } from '@codemirror/view'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useWorkspacePath } from '@/hooks/useWorkspacePath'
import { relativePath } from '@/lib/localFileLink'
import { parseUnifiedDiff, buildDisplayLines } from '@/lib/diffParser'
import { Markdown } from '@/lib/markdownConfig'
import { Button } from '@/components/ui/button'
import { FileViewerContextMenu } from '@/components/fileViewer/FileViewerContextMenu'
import {
  diffDecorationField,
  highlightLineField,
  setDiffEffect,
  setHighlightLineEffect,
} from '@/lib/cmDiffDecorations'
import { conflictMarkerPlugin } from '@/lib/cmConflictMarkers'
import { loadLanguageByName } from '@/lib/cmLanguages'
import { createOneDarkCMTheme } from '@/lib/cmTheme'

interface CodeMirrorViewerProps {
  content: string
  language: string
  diff?: string
  highlightLine: number | null
}

/**
 * CodeMirrorFileViewer hosts the CodeMirror editor instance for source-code
 * viewing plus the markdown preview / source toggle for .md files. Extracted
 * from FileViewerContent so the data-loading shell stays focused on
 * load/error/binary states. (W-29 split)
 */
export function CodeMirrorFileViewer(props: CodeMirrorViewerProps) {
  const { content, language, diff, highlightLine } = props
  const isMarkdown = language === 'Markdown' || language === 'markdown'
  const [showSource, setShowSource] = useState(false)
  const activeFile = useFileViewerStore((s) => s.activeFile)
  const workspacePath = useWorkspacePath()

  if (isMarkdown) {
    return (
      <div className="flex-1 flex flex-col overflow-hidden relative">
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={() => setShowSource((s) => !s)}
          className="absolute top-2 right-4 z-10"
          title={showSource ? 'Preview' : 'Source'}
        >
          {showSource ? <Eye className="size-4" /> : <Code className="size-4" />}
        </Button>
        {showSource ? (
          <CodeMirrorEditor
            content={content}
            language="Markdown"
            diff={diff}
            highlightLine={highlightLine}
          />
        ) : (
          <Markdown
            content={content}
            className="flex-1 overflow-auto custom-scrollbar p-4"
            baseFilePath={activeFile}
            workspaceRoot={workspacePath}
          />
        )}
      </div>
    )
  }

  return (
    <div className="flex-1 flex flex-col overflow-hidden">
      <CodeMirrorEditor
        content={content}
        language={language}
        diff={diff}
        highlightLine={highlightLine}
      />
    </div>
  )
}

// CodeMirrorEditor wraps a single CodeMirror EditorView and updates it
// imperatively as content / language / diff / highlight props change.
function CodeMirrorEditor({ content, language, diff, highlightLine }: CodeMirrorViewerProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)
  const langCompartment = useRef(new Compartment())
  const clearHighlightLine = useFileViewerStore((s) => s.clearHighlightLine)
  const activeFile = useFileViewerStore((s) => s.activeFile)
  const workspacePath = useWorkspacePath()

  // Memoize the theme so it's only resolved once
  const theme = useMemo(() => createOneDarkCMTheme(), [])

  // Create EditorView on mount, destroy on unmount
  useEffect(() => {
    if (!containerRef.current) return

    const state = EditorState.create({
      doc: content,
      extensions: [
        EditorView.editable.of(false),
        EditorState.readOnly.of(true),
        theme,
        lineNumbers(),
        langCompartment.current.of([]),
        diffDecorationField,
        highlightLineField,
        conflictMarkerPlugin,
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
    // Intentionally only run on mount/unmount. Content/language/diff updates
    // are handled by separate effects that dispatch to the existing view.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Update document content when it changes
  useEffect(() => {
    const view = viewRef.current
    if (!view) return

    const currentDoc = view.state.doc.toString()
    if (currentDoc === content) return

    view.dispatch({
      changes: { from: 0, to: view.state.doc.length, insert: content },
    })
  }, [content])

  // Load language asynchronously and reconfigure compartment
  useEffect(() => {
    let cancelled = false

    loadLanguageByName(language).then((langSupport) => {
      if (cancelled || !viewRef.current) return
      viewRef.current.dispatch({
        effects: langCompartment.current.reconfigure(langSupport ? [langSupport] : []),
      })
    })

    return () => { cancelled = true }
  }, [language])

  // Update diff decorations when diff or content changes
  useEffect(() => {
    const view = viewRef.current
    if (!view) return

    if (!diff) {
      view.dispatch({ effects: setDiffEffect.of(null) })
      return
    }

    const lines = content.split('\n')
    const { hunks } = parseUnifiedDiff(diff)
    const displayLines = hunks.length > 0 ? buildDisplayLines(lines, hunks) : []

    if (displayLines.length > 0) {
      view.dispatch({ effects: setDiffEffect.of(displayLines) })
    } else {
      view.dispatch({ effects: setDiffEffect.of(null) })
    }
  }, [diff, content])

  // Handle scroll-to-line and highlight
  useEffect(() => {
    const view = viewRef.current
    if (!view) return

    if (highlightLine == null) {
      view.dispatch({ effects: setHighlightLineEffect.of(null) })
      return
    }

    if (highlightLine < 1 || highlightLine > view.state.doc.lines) return

    const line = view.state.doc.line(highlightLine)
    view.dispatch({
      effects: [
        setHighlightLineEffect.of(highlightLine),
        EditorView.scrollIntoView(line.from, { y: 'center' }),
      ],
    })

    const timer = setTimeout(() => {
      clearHighlightLine()
      if (viewRef.current) {
        viewRef.current.dispatch({ effects: setHighlightLineEffect.of(null) })
      }
    }, 3000)

    return () => clearTimeout(timer)
  }, [highlightLine, clearHighlightLine])

  // -- Right-click context menu support ----------------------------------

  const [contextMenuPos, setContextMenuPos] = useState<{ x: number; y: number } | null>(null)
  const contextMenuRef = useRef('')

  const handleContextMenu = useCallback(
    (e: React.MouseEvent) => {
      const view = viewRef.current
      if (!view || !activeFile) return

      const selection = view.state.selection.main
      if (selection.empty) return // let the browser show its native context menu

      e.preventDefault()

      const doc = view.state.doc
      const startLine = doc.lineAt(selection.from).number
      const endLine = doc.lineAt(selection.to).number

      const fileRef = workspacePath ? relativePath(workspacePath, activeFile) : activeFile

      contextMenuRef.current =
        endLine > startLine
          ? `@${fileRef}#${startLine}-${endLine}`
          : `@${fileRef}#${startLine}`

      setContextMenuPos({ x: e.clientX, y: e.clientY })
    },
    [activeFile, workspacePath],
  )

  const closeContextMenu = useCallback(() => setContextMenuPos(null), [])

  return (
    <>
      <div
        ref={containerRef}
        className="flex-1 overflow-hidden cm-viewer-container"
        onContextMenu={handleContextMenu}
      />
      <FileViewerContextMenu
        reference={contextMenuRef.current}
        position={contextMenuPos}
        onClose={closeContextMenu}
      />
    </>
  )
}
