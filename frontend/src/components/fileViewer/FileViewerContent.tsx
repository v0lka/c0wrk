import { useEffect, useRef, useCallback, useMemo } from 'react'
import { Loader2 } from 'lucide-react'
import { EditorView } from '@codemirror/view'
import { EditorState, Compartment } from '@codemirror/state'
import { lineNumbers } from '@codemirror/view'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { subscribe } from '@/api/runtime'
import { readFile, getFileDiff } from '@/api/workspace'
import { parseUnifiedDiff, buildDisplayLines } from '@/lib/diffParser'
import { MarkdownViewer } from '@/components/MarkdownViewer'
import { isBinaryContent } from '@/lib/fileViewerUtils'
import { detectLanguageFromPath } from '@/lib/cmLanguages'
import { createOneDarkCMTheme } from '@/lib/cmTheme'
import {
  diffDecorationField,
  highlightLineField,
  setDiffEffect,
  setHighlightLineEffect,
} from '@/lib/cmDiffDecorations'
import { loadLanguageByName } from '@/lib/cmLanguages'

export function FileViewerContent() {
  const activeFile = useFileViewerStore((s) => s.activeFile)
  const files = useFileViewerStore((s) => s.files)
  const openTabs = useFileViewerStore((s) => s.openTabs)
  const highlightLine = useFileViewerStore((s) => s.highlightLine)
  const setFileContent = useFileViewerStore((s) => s.setFileContent)
  const setFileDiff = useFileViewerStore((s) => s.setFileDiff)
  const setFileError = useFileViewerStore((s) => s.setFileError)
  const setFileBinary = useFileViewerStore((s) => s.setFileBinary)
  const setFileLoading = useFileViewerStore((s) => s.setFileLoading)

  const loadFile = useCallback(async (path: string, silent: boolean) => {
    if (!silent) setFileLoading(path, true)
    try {
      const content = await readFile(path)
      if (isBinaryContent(content)) { setFileBinary(path); return }
      setFileContent(path, content, detectLanguageFromPath(path))
      try { const diff = await getFileDiff(path); if (diff) setFileDiff(path, diff) } catch { /* optional */ }
    } catch (err) { setFileError(path, err instanceof Error ? err.message : String(err)) }
  }, [setFileLoading, setFileBinary, setFileContent, setFileDiff, setFileError])

  const loadFileRef = useRef(loadFile)
  loadFileRef.current = loadFile
  const filesRef = useRef(files)
  filesRef.current = files

  // Load active file when it changes
  useEffect(() => {
    if (!activeFile) return
    const data = filesRef.current[activeFile]
    if (data && !data.loading && (data.content || data.error || data.isBinary)) return
    loadFileRef.current(activeFile, false)
  }, [activeFile])

  // Auto-refresh on workspace:tree_changed — silently reload all open files
  useEffect(() => {
    const unsub = subscribe('workspace:tree_changed', () => {
      for (const path of openTabs) {
        loadFile(path, true)
      }
    })
    return unsub
  }, [openTabs, loadFile])

  if (!activeFile) return null
  const fileData = files[activeFile]
  if (!fileData) return null

  if (fileData.loading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
    )
  }

  if (fileData.error) {
    return (
      <div className="flex-1 flex items-center justify-center p-4">
        <p className="text-sm text-destructive text-center">{fileData.error}</p>
      </div>
    )
  }

  if (fileData.isBinary) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <p className="text-sm text-muted-foreground">Unsupported file format</p>
      </div>
    )
  }

  return (
    <CodeMirrorViewer
      content={fileData.content}
      language={fileData.language ?? 'text/plain'}
      diff={fileData.diff}
      highlightLine={highlightLine}
    />
  )
}

// -- CodeMirror editor (mounts only when raw/source is needed) ---------------

function CodeMirrorEditor({ content, language, diff, highlightLine }: {
  content: string
  language: string
  diff?: string
  highlightLine: number | null
}) {
  const containerRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)
  const langCompartment = useRef(new Compartment())
  const clearHighlightLine = useFileViewerStore((s) => s.clearHighlightLine)

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
    const view = viewRef.current

    loadLanguageByName(language).then((langSupport) => {
      if (cancelled || !viewRef.current) return
      viewRef.current.dispatch({
        effects: langCompartment.current.reconfigure(langSupport ? [langSupport] : []),
      })
    })

    // If the language is already loaded synchronously, reconfigure immediately
    if (view) {
      // Language loading is async; the .then() above handles it.
    }

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

  return (
    <div ref={containerRef} className="flex-1 overflow-hidden cm-viewer-container" />
  )
}

// -- Viewer shell (handles markdown preview vs raw toggle) -------------------

function CodeMirrorViewer({ content, language, diff, highlightLine }: {
  content: string
  language: string
  diff?: string
  highlightLine: number | null
}) {
  const isMarkdown = language === 'Markdown' || language === 'markdown'

  if (isMarkdown) {
    return (
      <div className="flex-1 flex flex-col overflow-hidden">
        <MarkdownViewer content={content} className="flex-1 overflow-auto custom-scrollbar p-4" />
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
