import { useEffect, useRef, useCallback } from 'react'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { restoreScrollTop } from '@/lib/fileViewerUtils'

/**
 * Shared reading-position memory for the viewer's two scrolling hosts (the
 * CodeMirror view and the markdown preview) — the file-viewer twin of
 * ChatScrollManager + chatStore.scrollPositions. Owns the listener-fed scroll
 * ref and the save/restore effects: the outgoing path's position is saved on
 * every path change (tab re-target) and again on unmount (viewer collapse /
 * tab close), the incoming path's saved position is restored with
 * proportional scaling (restoreScrollTop). Source and preview share one
 * store slot per file, and saves are gated to open tabs in the store, so a
 * close action's removal can never be resurrected from either host.
 *
 * The caller feeds the scrolling element to bindScrollElement (a passive
 * listener keeps the ref fresh; the restore writes el.scrollTop on the bound
 * element). The CodeMirror view is created inside a later effect, so its
 * path effect finds no element and defers the restore — the mount bind
 * performs it; the preview binds its own scroll container via a commit-phase
 * ref callback (before effects) and its path effect restores directly.
 */
export function useReadingPosition(path: string | null): {
  bindScrollElement: (el: Element | null) => void
  restore: () => void
} {
  const scrollStateRef = useRef<{ scrollTop: number; scrollHeight: number } | null>(null)
  const pathRef = useRef<string | null>(path)
  const scrollElRef = useRef<Element | null>(null)
  const onScrollRef = useRef<(() => void) | null>(null)
  // Set when the path effect ran before any element was bound: the next
  // bind performs the deferred restore.
  const pendingRestoreRef = useRef(false)

  // Re-anchor the bound element to the current path's saved position;
  // restoreScrollTop scales a stale offset when the file changed between
  // save and restore, and the browser clamps the rest.
  const restore = useCallback(() => {
    const el = scrollElRef.current
    const current = pathRef.current
    if (!el || !current) return
    const saved = useFileViewerStore.getState().scrollPositions[current]
    if (!saved) {
      // The ref still carries the OUTGOING path's geometry (no scroll event
      // has fired for the incoming path yet, and the re-target save already
      // consumed it): drop it, or the next save would write the outgoing
      // geometry under the incoming path's key.
      scrollStateRef.current = null
      return
    }
    const top = restoreScrollTop(saved, el.scrollHeight)
    el.scrollTop = top
    // Sync immediately — the programmatic-scroll event lands later (or, in
    // jsdom, never): a collapse in the same frame cannot save the
    // pre-restore value.
    scrollStateRef.current = { scrollTop: top, scrollHeight: el.scrollHeight }
  }, [])

  const bindScrollElement = useCallback((el: Element | null) => {
    const prevEl = scrollElRef.current
    const prevListener = onScrollRef.current
    if (prevEl && prevListener) prevEl.removeEventListener('scroll', prevListener)
    scrollElRef.current = null
    onScrollRef.current = null
    if (!el) return
    const onScroll = () => {
      scrollStateRef.current = { scrollTop: el.scrollTop, scrollHeight: el.scrollHeight }
    }
    el.addEventListener('scroll', onScroll, { passive: true })
    scrollElRef.current = el
    onScrollRef.current = onScroll
    if (pendingRestoreRef.current) {
      pendingRestoreRef.current = false
      restore()
    }
  }, [restore])

  // Save the outgoing path's position and restore the incoming path's on
  // every path change. With no element bound yet the restore is deferred to
  // the bind (see pendingRestoreRef).
  useEffect(() => {
    const outgoing = pathRef.current
    if (outgoing && outgoing !== path && scrollStateRef.current) {
      useFileViewerStore.getState().saveScrollPosition(outgoing, scrollStateRef.current)
    }
    pathRef.current = path

    if (!scrollElRef.current) {
      pendingRestoreRef.current = path !== null
      return
    }
    restore()
  }, [path, restore])

  // Save while the element still exists when the host unmounts (viewer
  // collapse, tab close). The save reads the listener-fed ref, not the
  // element, so the ref-detach commit phase that precedes this cleanup is
  // harmless.
  useEffect(() => () => {
    const outgoing = pathRef.current
    const state = scrollStateRef.current
    if (outgoing && state) useFileViewerStore.getState().saveScrollPosition(outgoing, state)
  }, [])

  return { bindScrollElement, restore }
}
