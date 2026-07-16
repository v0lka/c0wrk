import { useCallback, useEffect, useRef } from 'react'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useProjectStore } from '@/stores/projectStore'
import { subscribe } from '@/api/runtime'
import { readFile, getFileDiff } from '@/api/workspace'
import { getFileDiffHunks } from '@/api/git'
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
  const setFileHunks = useFileViewerStore((s) => s.setFileHunks)
  const setFileError = useFileViewerStore((s) => s.setFileError)
  const setFileBinary = useFileViewerStore((s) => s.setFileBinary)
  const setFileLoading = useFileViewerStore((s) => s.setFileLoading)

  const loadFile = useCallback(async (path: string, silent: boolean) => {
    if (!silent) setFileLoading(path, true)
    try {
      const content = await readFile(path)
      if (isBinaryContent(content)) { setFileBinary(path); return }
      setFileContent(path, content, detectLanguageFromPath(path))
      // Skip diff for No Project (no git operations)
      const activeProject = useProjectStore.getState().projects?.find(
        (p) => p.id === useProjectStore.getState().activeProjectId
      )
      if (activeProject?.is_no_project !== true) {
        // Always set the diff — even when empty — so stale diffs from a
        // previous load (e.g. changes reverted, or file moved to a non-git
        // context) are cleared and the hunk-staging panel does not linger.
        try { const diff = await getFileDiff(path); setFileDiff(path, diff) } catch { /* optional */ }
        // Fetch structured hunk info (staging status, per-hunk diff text)
        // for the hunk panel. Errors are non-fatal — the panel just won't
        // render.
        try { const hunks = await getFileDiffHunks(path); setFileHunks(path, hunks) } catch { setFileHunks(path, []) }
      }
    } catch (err) { setFileError(path, err instanceof Error ? err.message : String(err)) }
  }, [setFileLoading, setFileBinary, setFileContent, setFileDiff, setFileHunks, setFileError])

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
    // Virtual files are not backed by a path on disk — never load from disk.
    if (data?.virtual) return
    if (data && !data.loading && (data.content || data.error || data.isBinary)) return
    loadFileRef.current(activeFile, false)
  }, [activeFile])

  // Auto-refresh on workspace:tree_changed — silently reload all open files.
  useEffect(() => {
    const unsub = subscribe('workspace:tree_changed', () => {
      for (const path of openTabsRef.current) {
        // Virtual files have no on-disk counterpart to reload.
        if (useFileViewerStore.getState().files[path]?.virtual) continue
        loadFileRef.current(path, true)
      }
    })
    return unsub
  }, [])
}
