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
import { Button } from '@/components/ui/button'
import { useReadingPosition } from '@/components/fileViewer/useReadingPosition'
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
  // the shared useReadingPosition hook owns the scroll ref and the
  // save/restore effects (unmount / tab re-target); this component only
  // feeds it the view's scrollDOM once created — the bind is the mount
  // restore point, since the view (and its scroll geometry) does not exist
  // when the hook's own path effect runs.
  const { bindScrollElement, restore } = useReadingPosition(activeFile)

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
    // Bind the listener and restore the saved reading position — the live
    // scroll DOM died with the previous unmount (viewer collapse / tab
    // close). restoreScrollTop scales a stale offset when the file changed
    // between save and restore, and the browser clamps the rest. The
    // highlightLine effect below runs AFTER this one (effect definition
    // order), so an explicit open-at-line still wins.
    bindScrollElement(view.scrollDOM)

    return () => {
      // Detach the listener before destroy() — afterwards the scrollDOM is
      // gone. The unmount save itself lives in the hook and reads the
      // listener-fed ref, so its order against this cleanup is irrelevant.
      bindScrollElement(null)
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
  const contentPathRef = useRef<string | null>(activeFile)
  useEffect(() => {
    const view = viewRef.current
    if (!view) return

    // Consume the path BEFORE the byte-identical early return below: a
    // re-target onto identical bytes must still mark the path consumed, or
    // the next same-tab refresh would be misclassified as a re-target and
    // restore() would overwrite the live reading position with the stale
    // saved offset. A byte-identical re-target itself needs no re-anchor —
    // the shared hook's path effect already restored against an identical
    // document.
    const retargeted = contentPathRef.current !== activeFile
    contentPathRef.current = activeFile

    const currentDoc = view.state.doc.toString()
    if (currentDoc === content) return

    view.dispatch({
      changes: { from: 0, to: view.state.doc.length, insert: content },
    })
    // A tab re-target restored against the OUTGOING document's geometry (the
    // shared hook's restore runs before this swap): re-anchor the incoming
    // path's position against the new document. A same-tab refresh must not
    // re-anchor — the live reading position wins over the stale saved one.
    if (retargeted) restore()
  }, [content, activeFile, restore])

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
 * The markdown PREVIEW branch of the viewer with reading-position memory —
 * the same save-on-unmount / restore-on-bind contract as CodeMirrorEditor,
 * shared through useReadingPosition, so the preview scroll also survives
 * viewer collapse and tab re-targets. Source and preview share ONE store
 * slot per file — the toggle carries the single position across the panes
 * (restoreScrollTop re-anchors it to the other pane's height), it does not
 * keep two independent positions.
 *
 * The wrapper div is the scroll container itself (overflow-auto + padding
 * live here) and binds the hook directly via a commit-phase ref callback —
 * before effects, so the hook's path effect finds the element and restores
 * directly. Markdown renders INSIDE it as plain flowing content: nothing
 * here depends on Markdown's internal DOM shape.
 */
function MarkdownPreviewPane({ path, content, workspaceRoot }: {
  path: string | null
  content: string
  workspaceRoot: string | null
}) {
  const { bindScrollElement } = useReadingPosition(path)

  const attachScrollContainer = useCallback((el: HTMLDivElement | null) => {
    bindScrollElement(el)
  }, [bindScrollElement])

  return (
    <div
      ref={attachScrollContainer}
      className="flex-1 min-h-0 overflow-auto custom-scrollbar p-4"
    >
      <Markdown
        content={content}
        baseFilePath={path}
        workspaceRoot={workspaceRoot}
      />
    </div>
  )
}
