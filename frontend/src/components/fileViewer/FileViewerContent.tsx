import { Loader2 } from 'lucide-react'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useFileViewerData } from '@/hooks/useFileViewerData'
import { CodeMirrorFileViewer } from '@/components/fileViewer/CodeMirrorFileViewer'

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
    <CodeMirrorFileViewer
      content={fileData.content}
      language={fileData.language ?? 'text/plain'}
      diff={fileData.diff}
      highlightLine={highlightLine}
    />
  )
}
