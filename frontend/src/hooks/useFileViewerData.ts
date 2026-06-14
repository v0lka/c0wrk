import { useCallback, useEffect, useRef } from 'react'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { subscribe } from '@/api/runtime'
import { readFile, getFileDiff } from '@/api/workspace'
import { isBinaryContent } from '@/lib/fileViewerUtils'
import { detectLanguageFromPath } from '@/lib/cmLanguages'

/**
 * useFileViewerData centralizes the file-loading and tree-change subscription
 * logic for the file viewer. It exposes a single `loadFile(path, silent)`
 * function that updates the file-viewer store, and subscribes to
 * `workspace:tree_changed` to silently refresh every open tab.
 *
 * The hook is referentially stable: the returned `loadFile` is wrapped in
 * useCallback over store-action references that themselves are stable, so
 * callers can include it in effect deps without triggering reloads.
 */
export function useFileViewerData(activeFile: string | null, openTabs: string[]): void {
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

  // Stable refs so the workspace event subscription does not re-bind on every
  // openTabs change. We re-read the current openTabs/loadFile inside the
  // event handler.
  const loadFileRef = useRef(loadFile)
  loadFileRef.current = loadFile
  const openTabsRef = useRef(openTabs)
  openTabsRef.current = openTabs

  // Load the active file when it changes (skipped if cache already populated).
  useEffect(() => {
    if (!activeFile) return
    const data = useFileViewerStore.getState().files[activeFile]
    if (data && !data.loading && (data.content || data.error || data.isBinary)) return
    loadFileRef.current(activeFile, false)
  }, [activeFile])

  // Auto-refresh on workspace:tree_changed — silently reload all open files.
  useEffect(() => {
    const unsub = subscribe('workspace:tree_changed', () => {
      for (const path of openTabsRef.current) {
        loadFileRef.current(path, true)
      }
    })
    return unsub
  }, [])
}
