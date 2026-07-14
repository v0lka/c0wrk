import { Loader2 } from 'lucide-react'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useFileViewerData } from '@/hooks/useFileViewerData'
import { CodeMirrorFileViewer } from '@/components/fileViewer/CodeMirrorFileViewer'
import { DiffHunkStageBar } from '@/components/fileViewer/DiffHunkStageBar'
import { PlanEditor } from '@/components/fileViewer/PlanEditor'
import { ReviewPage } from '@/components/review/ReviewPage'
import { useSessionStore } from '@/stores/sessionStore'
import type { HunkDiffInfo } from '@/types/models'

/** Stable empty array reused when hunks are absent to avoid per-render allocation. */
const EMPTY_HUNKS: HunkDiffInfo[] = []

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
  const activeSessionId = useSessionStore((s) => s.activeSessionId)

  // Subscribes to the active file and workspace tree changes.
  useFileViewerData(activeFile, openTabs)

  if (!activeFile) return null

  // Review page: synthetic pseudo-path renders the review UI instead of a file
  if (activeFile === 'c0wrk:review') {
    if (!activeSessionId) return null
    return <ReviewPage sessionId={activeSessionId} />
  }

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

  const hunks = fileData.hunks ?? EMPTY_HUNKS

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
