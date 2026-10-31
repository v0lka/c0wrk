import { useEffect, useRef, useCallback, useState } from 'react'
import { Code, Eye } from 'lucide-react'
import { EditorView, drawSelection } from '@codemirror/view'
import { EditorState, Compartment } from '@codemirror/state'
import { lineNumbers } from '@codemirror/view'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useWorkspacePath } from '@/hooks/useWorkspacePath'
import { relativePath } from '@/lib/localFileLink'
import { formatFileRefPath } from '@/lib/parseReferences'
import { parseUnifiedDiff, buildDisplayLines } from '@/lib/diffParser'
import { Markdown } from '@/lib/markdownConfig'
import { restoreScrollTop } from '@/lib/fileViewerUtils'
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
import { useThemeStore, selectActiveThemeType, selectActiveThemeId } from '@/stores/themeStore'
import { useFontStore } from '@/stores/fontStore'

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
          <MarkdownPreviewPane
            path={activeFile}
            content={content}
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
  const themeCompartment = useRef(new Compartment())
  const clearHighlightLine = useFileViewerStore((s) => s.clearHighlightLine)
  const activeFile = useFileViewerStore((s) => s.activeFile)
  const workspacePath = useWorkspacePath()
  const theme = useThemeStore(selectActiveThemeType)
  // Theme IDENTITY: a same-type custom-theme switch keeps `theme` unchanged
  // but still swaps the palette CSS variables the baked theme resolves from.
  const themeId = useThemeStore(selectActiveThemeId)
  // The mono choice: the theme's font itself rides the `--font-mono` var and
  // re-resolves on its own — this value exists to TRIGGER the reconfigure
  // below so CodeMirror re-measures line heights for the new metrics.
  const monoFontFamily = useFontStore((s) => s.monoFontFamily)

  // -- Reading-position memory (session scope) ---------------------------
  // The file-viewer twin of ChatScrollManager + chatStore.scrollPositions:
  // a passive scroll listener keeps scrollStateRef fresh; the position is
  // saved while the view is still alive — when the reused view re-targets
  // another tab, and when this component unmounts (the floating viewer
  // auto-collapses on outside focus, which unmounts the whole content) —
  // and restored on mount and after each re-target, so re-expanding the
  // viewer resumes where reading stopped instead of jumping back to the top.
  const scrollStateRef = useRef<{ scrollTop: number; scrollHeight: number } | null>(null)
  const activePathRef = useRef<string | null>(activeFile)

  // Create EditorView on mount, destroy on unmount
  useEffect(() => {
    if (!containerRef.current) return

    const state = EditorState.create({
      doc: content,
      extensions: [
        EditorView.editable.of(false),
        EditorState.readOnly.of(true),
        themeCompartment.current.of(createOneDarkCMTheme(theme === 'dark')),
        lineNumbers(),
        // Use CodeMirror's DOM-based selection rendering (not native ::selection)
        // so the selection color matches the chat input editor exactly — both
        // are themed via `.cm-selectionBackground` → var(--color-muted).
        drawSelection(),
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

    const scrollDOM = view.scrollDOM
    const onScroll = () => {
      scrollStateRef.current = { scrollTop: scrollDOM.scrollTop, scrollHeight: scrollDOM.scrollHeight }
    }
    scrollDOM.addEventListener('scroll', onScroll, { passive: true })

    // Restore the saved reading position — the live scroll DOM died with the
    // previous unmount (viewer collapse / tab close). restoreScrollTop scales
    // a stale offset when the file changed between save and restore, and the
    // browser clamps the rest. The highlightLine effect below runs AFTER this
    // one (effect definition order), so an explicit open-at-line still wins.
    const saved = activePathRef.current
      ? useFileViewerStore.getState().scrollPositions[activePathRef.current]
      : undefined
    if (saved) {
      const top = restoreScrollTop(saved, scrollDOM.scrollHeight)
      scrollDOM.scrollTop = top
      // The browser fires the scroll event for a programmatic scroll later
      // (jsdom never does): sync the ref immediately so a collapse in the
      // same frame cannot save the pre-restore value.
      scrollStateRef.current = { scrollTop: top, scrollHeight: scrollDOM.scrollHeight }
    }

    return () => {
      // Save BEFORE destroy() — afterwards the scrollDOM is gone and the
      // listener can no longer refresh the ref.
      const path = activePathRef.current
      const state = scrollStateRef.current
      if (path && state) useFileViewerStore.getState().saveScrollPosition(path, state)
      scrollDOM.removeEventListener('scroll', onScroll)
      view.destroy()
      viewRef.current = null
    }
    // Intentionally only run on mount/unmount. Content/language/diff updates
    // are handled by separate effects that dispatch to the existing view.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Re-resolve the CodeMirror theme when the app theme changes. CM themes
  // bake CSS-variable values at creation, so a compartment reconfigure is the
  // only way to swap the palette (selection bg, gutter, syntax colors, and the
  // { dark } flag that drives CM's built-in defaults) without recreating the view.
  // Keyed on BOTH the type (drives the { dark } flag) and the identity (a
  // same-type custom-theme swap changes the palette while `theme` stays).
  // The mono choice is in the deps only to force the same reconfigure when
  // the user switches fonts — CM measures heights lazily, and without the
  // redraw the new font's metrics would be applied on stale measurements.
  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    view.dispatch({
      effects: themeCompartment.current.reconfigure(createOneDarkCMTheme(theme === 'dark')),
    })
  }, [theme, themeId, monoFontFamily])

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

  // Re-target the reading position when the reused view switches tabs: save
  // the outgoing path's position (the scroll-listener ref still holds it),
  // then restore the incoming path's saved one. A single EditorView serves
  // every code tab (only the doc is replaced), so without this the scroll
  // offset would be one shared blob across all tabs. Runs after the content
  // effect above (definition order) so the restore lands on the new document.
  useEffect(() => {
    const outgoing = activePathRef.current
    if (outgoing && outgoing !== activeFile && scrollStateRef.current) {
      useFileViewerStore.getState().saveScrollPosition(outgoing, scrollStateRef.current)
    }
    activePathRef.current = activeFile

    const view = viewRef.current
    if (!view || !activeFile) return
    const saved = useFileViewerStore.getState().scrollPositions[activeFile]
    if (!saved) return
    const top = restoreScrollTop(saved, view.scrollDOM.scrollHeight)
    view.scrollDOM.scrollTop = top
    // Sync immediately — see the mount-effect restore for why.
    scrollStateRef.current = { scrollTop: top, scrollHeight: view.scrollDOM.scrollHeight }
  }, [activeFile])

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
  const contextMenuTextRef = useRef('')

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

      // The line anchor sits after the (possibly quoted) path form:
      // @'my file.go'#L20-L36 when the path has spaces, @x.go#L20 otherwise.
      const formattedRef = formatFileRefPath(fileRef)
      contextMenuRef.current =
        endLine > startLine
          ? `@${formattedRef}#${startLine}-${endLine}`
          : `@${formattedRef}#${startLine}`
      contextMenuTextRef.current = doc.sliceString(selection.from, selection.to)

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
        selectedText={contextMenuTextRef.current}
        position={contextMenuPos}
        onClose={closeContextMenu}
      />
    </>
  )
}

/**
 * The markdown PREVIEW branch of the viewer with its own reading-position
 * memory — the same save-on-unmount / restore-on-bind contract as
 * CodeMirrorEditor (see that component's block), so the preview scroll also
 * survives viewer collapse and tab re-targets, and the source/preview toggle
 * swaps between two independently remembered positions for the same file.
 *
 * The scrolling element is Markdown's own root (the overflow-auto class lands
 * there), so the listener binds to the wrapper's first element child rather
 * than the wrapper itself.
 */
function MarkdownPreviewPane({ path, content, workspaceRoot }: {
  path: string | null
  content: string
  workspaceRoot: string | null
}) {
  const scrollStateRef = useRef<{ scrollTop: number; scrollHeight: number } | null>(null)
  const scrollElRef = useRef<Element | null>(null)
  const onScrollRef = useRef<(() => void) | null>(null)
  const pathRef = useRef<string | null>(path)

  // Save the outgoing path's position and restore the incoming path's on
  // every path change (first bind included — the restore is what survives a
  // viewer collapse that unmounted the preview).
  useEffect(() => {
    const outgoing = pathRef.current
    if (outgoing && outgoing !== path && scrollStateRef.current) {
      useFileViewerStore.getState().saveScrollPosition(outgoing, scrollStateRef.current)
    }
    pathRef.current = path

    const el = scrollElRef.current
    if (!el || !path) return
    const saved = useFileViewerStore.getState().scrollPositions[path]
    if (!saved) return
    const top = restoreScrollTop(saved, el.scrollHeight)
    el.scrollTop = top
    // Sync immediately — the programmatic-scroll event lands later (or, in
    // jsdom, never); see CodeMirrorEditor's mount-effect restore.
    scrollStateRef.current = { scrollTop: top, scrollHeight: el.scrollHeight }
  }, [path])

  // Save while the element still exists when the preview unmounts (viewer
  // collapse, a switch to a code tab, or the source toggle). The save reads
  // the listener-fed ref, not the element, so the ref-detach commit phase
  // that precedes this cleanup is harmless.
  useEffect(() => () => {
    const outgoing = pathRef.current
    const state = scrollStateRef.current
    if (outgoing && state) useFileViewerStore.getState().saveScrollPosition(outgoing, state)
  }, [])

  const attachWrapper = useCallback((wrapper: HTMLDivElement | null) => {
    const prevEl = scrollElRef.current
    const prevListener = onScrollRef.current
    if (prevEl && prevListener) prevEl.removeEventListener('scroll', prevListener)
    scrollElRef.current = null
    onScrollRef.current = null
    if (!wrapper) return
    const el = wrapper.firstElementChild
    if (!el) return
    const onScroll = () => {
      scrollStateRef.current = { scrollTop: el.scrollTop, scrollHeight: el.scrollHeight }
    }
    el.addEventListener('scroll', onScroll, { passive: true })
    scrollElRef.current = el
    onScrollRef.current = onScroll
  }, [])

  return (
    <div ref={attachWrapper} className="flex flex-1 min-h-0">
      <Markdown
        content={content}
        className="flex-1 overflow-auto custom-scrollbar p-4"
        baseFilePath={path}
        workspaceRoot={workspaceRoot}
      />
    </div>
  )
}
