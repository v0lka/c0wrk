import { useMemo } from 'react'
import { Loader2 } from 'lucide-react'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useFileViewerData } from '@/hooks/useFileViewerData'
import { parseUnifiedDiff, type DiffHunk } from '@/lib/diffParser'
import { CodeMirrorFileViewer } from '@/components/fileViewer/CodeMirrorFileViewer'
import { DiffHunkStageBar } from '@/components/fileViewer/DiffHunkStageBar'
import { PlanEditor } from '@/components/fileViewer/PlanEditor'

/** Stable empty array reused by the hunks memo to avoid per-render allocation. */
const EMPTY_HUNKS: DiffHunk[] = []

/**
 * FileViewerContent is the data-loading shell for the file viewer. It chooses
 * between loading / error / binary / source views and delegates source
 * rendering to CodeMirrorFileViewer. (W-29 split)
 */
export function FileViewerContent() {
  const activeFile = useFileViewerStore((s) => s.activeFile)
  const files = useFileViewerStore((s) => s.files)
  const openTabs = useFileViewerStore((s) => s.openTabs)
  const highlightLine = useFileViewerStore((s) => s.highlightLine)

  // Subscribes to the active file and workspace tree changes.
  useFileViewerData(activeFile, openTabs)

  // Parse the active file's diff into hunks for per-hunk staging (Phase 6).
  // Memoized so the bar only re-parses when the diff text actually changes.
  const hunks = useMemo<DiffHunk[]>(() => {
    if (!activeFile) return EMPTY_HUNKS
    const diff = files[activeFile]?.diff
    if (!diff) return EMPTY_HUNKS
    const result = parseUnifiedDiff(diff)
    return result.hunks.length > 0 ? result.hunks : EMPTY_HUNKS
  }, [activeFile, files])

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

  // Plan files: render structured editor instead of plain markdown viewer
  const isPlanFile = activeFile.includes('.c0wrk/plans/')
  if (isPlanFile) {
    return <PlanEditor content={fileData.content} path={activeFile} />
  }

  return (
    <div className="flex flex-1 flex-col min-h-0">
      {hunks.length > 0 && (
        <DiffHunkStageBar filePath={activeFile} hunks={hunks} />
      )}
      <CodeMirrorFileViewer
        content={fileData.content}
        language={fileData.language ?? 'text/plain'}
        diff={fileData.diff}
        highlightLine={highlightLine}
      />
    </div>
  )
}
