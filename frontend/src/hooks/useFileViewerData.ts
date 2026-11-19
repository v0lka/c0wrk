import { useCallback, useEffect, useRef } from 'react'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useProjectStore } from '@/stores/projectStore'
import { onGlobalEvent } from '@/api/runtime'
import { readFile, readImageAsDataURL } from '@/api/workspace'
import { getFileDiffHunks } from '@/api/git'
import { isBinaryContent, isImageFilePath } from '@/lib/fileViewerUtils'
import { assembleUnifiedDiff } from '@/lib/diffParser'
import { detectLanguageFromPath } from '@/lib/cmLanguages'
import { markViewerDiffsStale, viewerDiffEpoch } from '@/lib/viewerDiffEpoch'
import { isTreeChangedPayload } from '@/types/events'

/**
 * Trailing-debounce window coalescing a burst of `workspace:tree_changed`
 * events into ONE silent-refresh cycle. A busy agent fires this event after
 * every write and the fs watcher batches add more; refreshing every open tab
 * once per event stacked parallel readFile + diff chains. 400ms absorbs the
 * burst (the watcher itself debounces at 200ms) without visibly delaying a
 * quiet single-change refresh.
 */
export const TREE_CHANGED_DEBOUNCE_MS = 400

/**
 * useFileViewerData centralizes the file-loading and tree-change subscription
 * logic for the file viewer. It exposes a single `loadFile(path, silent)`
 * function that updates the file-viewer store, and subscribes to
 * `workspace:tree_changed` + `git:status_changed` to silently refresh the
 * open tabs:
 *
 *  - tree_changed bursts are coalesced into one trailing-debounced cycle;
 *    a payload carrying `paths` refreshes only the tabs whose exact path
 *    changed, a payload without one (the agent-write PostExecuteHook emitter
 *    carries no path metadata) refreshes every tab — fail-open, mirroring the
 *    SPECS panel's relevance filter.
 *  - Per-path in-flight dedup: an explicit (re)open racing an in-flight load
 *    joins it; a silent refresh queued while a chain runs waits for the chain
 *    to settle and then runs exactly one fresh follow-up read — parallel
 *    readFile + diff pipelines never stack, and a refresh can never join a
 *    chain that predates the change it was scheduled by.
 *  - Content short-circuit: a reload whose re-read is byte-identical to the
 *    cached content skips the diff/hunks RPCs entirely — unless a git
 *    mutation moved the viewer-diff epoch (lib/viewerDiffEpoch) since that
 *    diff was fetched: staging or committing moves hunks without touching
 *    the file bytes, and only the epoch proves the cached diff stale.
 *  - The unified diff overlay is assembled from the hunks RPC alone
 *    (assembleUnifiedDiff — parseUnifiedDiff consumes bare `@@` sections):
 *    one RPC / two git spawns replaces the former GetFileDiff +
 *    GetFileDiffHunks pair (two RPCs / four spawns) per refreshed tab.
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
  const setFileImage = useFileViewerStore((s) => s.setFileImage)
  const setFileLoading = useFileViewerStore((s) => s.setFileLoading)

  // In-flight load chains per path. Entries exist only while a chain runs;
  // both silent refreshes and the active-file effect join through here.
  const inFlightRef = useRef(new Map<string, Promise<void>>())
  // Viewer-diff epoch at which each path's diff was last fetched
  // SUCCESSFULLY — the short-circuit key. A failed fetch records nothing, so
  // the next refresh retries. Entries outlive their tab (bounded by the
  // distinct files opened during the session: a path → number pair each).
  const diffFetchedAtRef = useRef(new Map<string, number>())
  // tree_changed coalescing state: paths seen since the last fired cycle and
  // the "cannot scope" flag any pathless event poisons the cycle with.
  const reloadTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const pendingPathsRef = useRef(new Set<string>())
  const pendingAllRef = useRef(false)
  // Stable indirections so loadFile's own deferred follow-ups and the
  // workspace event subscription always call the latest loadFile / read the
  // latest openTabs without re-binding effects.
  const loadFileRef = useRef<(path: string, silent: boolean) => Promise<void>>(async () => {})
  const openTabsRef = useRef(openTabs)
  openTabsRef.current = openTabs

  const loadFile = useCallback(async (path: string, silent: boolean): Promise<void> => {
    if (!silent) setFileLoading(path, true)
    // Overlapping load for the same path. An explicit (re)open joins the
    // running chain — it performs the same store updates this call would
    // have. A silent refresh does NOT: the running chain may have started
    // (and read) BEFORE the change that scheduled this refresh, and joining
    // it would pin stale content until the next unrelated event. It queues
    // exactly one fresh follow-up read for after the chain settles, so two
    // parallel pipelines never stack either.
    const running = inFlightRef.current.get(path)
    if (running) {
      if (!silent) return running
      void running.then(() => {
        if (!inFlightRef.current.has(path)) void loadFileRef.current(path, true)
      })
      return running
    }
    const chain = (async () => {
      try {
        // Image files are binary and cannot be shown as text — fetch them as a
        // base64 data URL and let the viewer render the picture instead. Done
        // before readFile so image bytes never round-trip as a (lossy) string,
        // and the diff/hunk pipeline is skipped entirely (a picture has no
        // meaningful line diff).
        if (isImageFilePath(path)) {
          const dataUrl = await readImageAsDataURL(path)
          setFileImage(path, dataUrl)
          return
        }
        const content = await readFile(path)
        if (isBinaryContent(content)) { setFileBinary(path); return }
        setFileContent(path, content, detectLanguageFromPath(path))
        // Skip diff for No Project (no git operations)
        const activeProject = useProjectStore.getState().projects?.find(
          (p) => p.id === useProjectStore.getState().activeProjectId
        )
        if (activeProject?.is_no_project !== true) {
          // Content short-circuit: byte-identical content with a diff
          // fetched at the CURRENT git-mutation epoch is known-current —
          // skip the hunks RPC. The epoch guard is what keeps a staged
          // change (hunks move, bytes do not) from being skipped over.
          const cached = useFileViewerStore.getState().files[path]
          const fetchedAt = diffFetchedAtRef.current.get(path)
          const diffCurrent =
            cached !== undefined &&
            cached.content === content &&
            fetchedAt !== undefined &&
            fetchedAt === viewerDiffEpoch()
          if (!diffCurrent) {
            // Fetch structured hunk info (staging status, per-hunk diff text)
            // for the hunk panel, and assemble the overlay's unified diff from
            // the same blocks — one RPC / two git spawns total (three for an
            // untracked file, whose synthetic --no-index hunks the backend
            // appends). Errors are non-fatal: the panel just won't render and
            // the epoch stays unmoved, so the next refresh retries. Setting
            // the diff even when empty clears stale overlay state, matching
            // the old always-set GetFileDiff behavior on success.
            try {
              const hunks = await getFileDiffHunks(path)
              setFileHunks(path, hunks)
              setFileDiff(path, assembleUnifiedDiff(hunks))
              diffFetchedAtRef.current.set(path, viewerDiffEpoch())
            } catch {
              setFileHunks(path, [])
            }
          }
        }
      } catch (err) { setFileError(path, err instanceof Error ? err.message : String(err)) }
      finally { inFlightRef.current.delete(path) }
    })()
    inFlightRef.current.set(path, chain)
    return chain
  }, [setFileLoading, setFileBinary, setFileImage, setFileContent, setFileDiff, setFileHunks, setFileError])

  // Keep the indirection pointed at the latest loadFile (deps are stable
  // store actions, so this rarely changes — the re-assignment is cheap).
  loadFileRef.current = loadFile

  // Load the active file when it changes (skipped if cache already populated).
  useEffect(() => {
    if (!activeFile) return
    const data = useFileViewerStore.getState().files[activeFile]
    // Virtual files are not backed by a path on disk — never load from disk.
    if (data?.virtual) return
    // Already loaded (text content, an image data URL, a binary verdict, or an
    // error) — nothing to (re)fetch.
    if (data && !data.loading && (data.content || data.imageDataUrl || data.error || data.isBinary)) return
    loadFileRef.current(activeFile, false)
  }, [activeFile])

  // Auto-refresh subscriptions. tree_changed bursts coalesce into one
  // trailing-debounced cycle scoped to the payload's changed paths when it
  // carries them (exact absolute-path match — watcher events and viewer tabs
  // draw their paths from the same source, so the strings agree); every
  // backend-mutating git RPC's git:status_changed moves the viewer-diff
  // epoch, so the NEXT refresh refetches diffs even for byte-identical
  // content.
  useEffect(() => {
    const runCoalescedReload = () => {
      reloadTimerRef.current = null
      const all = pendingAllRef.current
      // Snapshot-and-reset BEFORE iterating: an event landing mid-cycle
      // repopulates the fresh set and schedules the next cycle instead of
      // racing this one's iteration.
      const paths = pendingPathsRef.current
      pendingAllRef.current = false
      pendingPathsRef.current = new Set()
      for (const path of openTabsRef.current) {
        // Virtual files have no on-disk counterpart to reload.
        if (useFileViewerStore.getState().files[path]?.virtual) continue
        if (!all && !paths.has(path)) continue
        void loadFileRef.current(path, true)
      }
    }
    const scheduleReload = (payload: unknown) => {
      const scoped = isTreeChangedPayload(payload) ? payload.paths : undefined
      if (scoped === undefined || scoped.length === 0) {
        // No usable path list — cannot scope; this cycle refreshes every tab.
        pendingAllRef.current = true
      } else {
        for (const p of scoped) pendingPathsRef.current.add(p)
      }
      if (reloadTimerRef.current !== null) clearTimeout(reloadTimerRef.current)
      reloadTimerRef.current = setTimeout(runCoalescedReload, TREE_CHANGED_DEBOUNCE_MS)
    }
    const unsubTree = onGlobalEvent('workspace:tree_changed', (payload) => scheduleReload(payload))
    const unsubGit = onGlobalEvent('git:status_changed', () => markViewerDiffsStale())
    return () => {
      unsubTree()
      unsubGit()
      if (reloadTimerRef.current !== null) {
        clearTimeout(reloadTimerRef.current)
        reloadTimerRef.current = null
      }
    }
  }, [])
}
