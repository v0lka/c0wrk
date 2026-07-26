import { useState, useEffect, useCallback, useRef, useMemo } from 'react'
import { Loader2, RefreshCw, GitCommit } from 'lucide-react'
import { Button } from '@/components/ui/button'
import * as reviewApi from '@/api/review'
import { subscribe } from '@/api/runtime'
import { useReviewStore } from '@/stores/reviewStore'
import { ReviewHeader } from './ReviewHeader'
import { FileReviewBlock } from './FileReviewBlock'

interface ReviewPageProps {
  /** Active session id — required for the interactive working-tree review. */
  sessionId?: string
  /** Commit SHA — when set, renders a read-only view of that commit's diff. */
  commitSha?: string
}

export function ReviewPage({ sessionId, commitSha }: ReviewPageProps) {
  const readOnly = !!commitSha
  const [diff, setDiff] = useState<reviewApi.ReviewFileDiff[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const loadReview = useReviewStore((s) => s.loadReview)

  const fetchDiff = useCallback(async (silent = false) => {
    // `silent` background refreshes (triggered by git status changes) keep the
    // existing diff on screen instead of flashing the full-screen spinner —
    // only the initial load / explicit retry shows the loader.
    if (!silent) setLoading(true)
    setError(null)
    try {
      const result = commitSha
        ? await reviewApi.getCommitDiff(commitSha)
        : await reviewApi.getReviewDiff()
      setDiff(result)
      // Only an explicit (non-silent) load resets the hunk cursor. A silent
      // background re-fetch (git stage/discard/commit during a review) must
      // preserve the user's current position rather than snapping to the top.
      if (!silent) {
        setCurrentHunk(0)
      } else {
        // Clamp the cursor into range in case the re-fetch shrank the diff
        // (e.g. the user discarded/committed hunks). The scroll-tracker
        // recomputes from the DOM shortly after, so this is just to avoid a
        // transient out-of-range indicator value.
        setCurrentHunk((prev) => {
          const total = result.reduce((count, file) => count + file.hunks.length, 0)
          return total === 0 ? 0 : Math.min(prev, total - 1)
        })
      }
    } catch (err) {
      setError(commitSha ? 'Failed to load commit diff' : 'Failed to load review diff')
      console.error('fetchDiff failed:', err)
    } finally {
      // Only the fetch that turned the loader on may turn it off — a silent
      // background re-fetch must never dismiss a concurrently-running initial
      // load's spinner, nor can two in-flight requests race on setLoading.
      if (!silent) setLoading(false)
    }
  }, [commitSha])

  useEffect(() => {
    void fetchDiff()
    // Only load the persisted review buffer (comments/status) in interactive
    // mode — a read-only commit view has no associated review buffer.
    if (!readOnly && sessionId) {
      void loadReview(sessionId)
    }
  }, [fetchDiff, loadReview, sessionId, readOnly])

  // ─── Working-tree sync ──────────────────────────────────────────────────
  // The interactive review mirrors `git diff HEAD`, which changes whenever
  // files are staged/unstaged/discarded/committed/edited in the Git panel's
  // "Changes" section or externally. Re-fetch on git status changes so the
  // review never drifts from the live working tree. Commit-review mode is
  // excluded — a commit's diff is immutable. Both events share a single
  // debounce (matching useGitStatusEvents) so a UI git op followed by the
  // watcher's workspace:tree_changed coalesce into one silent re-fetch.
  useEffect(() => {
    if (readOnly) return
    let timer: ReturnType<typeof setTimeout> | null = null
    const debouncedFetch = () => {
      if (timer !== null) clearTimeout(timer)
      timer = setTimeout(() => {
        timer = null
        void fetchDiff(true)
      }, 150)
    }
    const unsubs = [
      subscribe('git:status_changed', debouncedFetch),
      subscribe('workspace:tree_changed', debouncedFetch),
    ]
    return () => {
      for (const unsub of unsubs) unsub()
      if (timer !== null) clearTimeout(timer)
    }
  }, [readOnly, fetchDiff])

  // ─── Hunk navigation ────────────────────────────────────────────────────
  const scrollRef = useRef<HTMLDivElement>(null)
  const [currentHunk, setCurrentHunk] = useState(0)
  // While a prev/next click is animating a smooth scroll, this holds the
  // requested target index so the scroll-position tracker (below) knows to
  // ignore the animation's intermediate frames. Without it, those frames still
  // pin the *previous* hunk at the top of the viewport and would snap the
  // indicator back (the "new → old → new" flicker). See goToHunk / onScroll.
  const pendingNav = useRef<number | null>(null)
  // Safety net: clears pendingNav if the target can't be scrolled to the very
  // top (e.g. the last hunk near the bottom) so manual-scroll tracking resumes.
  const pendingNavTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const totalHunks = useMemo(
    () => diff.reduce((count, file) => count + file.hunks.length, 0),
    [diff],
  )

  // Note: the hunk cursor is reset to 0 inside fetchDiff on non-silent loads
  // (initial load / explicit retry). A silent background re-fetch preserves
  // the user's current position instead of snapping back to the top.

  // Track which hunk is pinned to the top of the scroll viewport so the
  // prev/next buttons stay in sync with manual scrolling.
  useEffect(() => {
    if (totalHunks <= 0) return
    const container = scrollRef.current
    if (!container) return
    let raf = 0
    const onScroll = () => {
      cancelAnimationFrame(raf)
      raf = requestAnimationFrame(() => {
        const hunkEls = container.querySelectorAll<HTMLElement>('[data-review-hunk]')
        if (hunkEls.length === 0) return
        const viewportTop = container.getBoundingClientRect().top
        let active = 0
        hunkEls.forEach((el, i) => {
          if (el.getBoundingClientRect().top - viewportTop <= 8) active = i
        })
        // A prev/next click is animating the viewport. The intermediate frames
        // of the smooth scroll still show the previous hunk pinned at the top,
        // so ignore them until the viewport actually reaches the requested
        // target — otherwise the indicator snaps back mid-animation. (The
        // pendingNavTimer handles the case where the target can't reach the
        // top, e.g. the last hunk.)
        const target = pendingNav.current
        if (target !== null) {
          if (active === target) {
            pendingNav.current = null
            if (pendingNavTimer.current !== null) {
              clearTimeout(pendingNavTimer.current)
              pendingNavTimer.current = null
            }
          }
          return
        }
        setCurrentHunk(active)
      })
    }
    container.addEventListener('scroll', onScroll, { passive: true })
    return () => {
      cancelAnimationFrame(raf)
      container.removeEventListener('scroll', onScroll)
    }
  }, [totalHunks])

  // Scroll a hunk so its top edge aligns with the top of the review pane.
  const goToHunk = useCallback((index: number) => {
    const container = scrollRef.current
    if (!container) return
    const target = container.querySelectorAll<HTMLElement>('[data-review-hunk]')[index]
    if (!target) return
    // Arm the guard before kicking off the animation so the scroll tracker
    // ignores the intermediate frames (see onScroll). A safety timer clears
    // it when the target genuinely can't reach the viewport top (e.g. the
    // last hunk, where scrollBy hits the bottom edge first) — otherwise
    // manual-scroll tracking would stay frozen.
    pendingNav.current = index
    if (pendingNavTimer.current !== null) clearTimeout(pendingNavTimer.current)
    pendingNavTimer.current = setTimeout(() => {
      pendingNav.current = null
      pendingNavTimer.current = null
    }, 600)
    const delta = target.getBoundingClientRect().top - container.getBoundingClientRect().top
    container.scrollBy({ top: delta, behavior: 'smooth' })
    setCurrentHunk(index)
  }, [])

  const goToPrevHunk = useCallback(() => {
    goToHunk(Math.max(currentHunk - 1, 0))
  }, [currentHunk, goToHunk])

  const goToNextHunk = useCallback(() => {
    goToHunk(Math.min(currentHunk + 1, totalHunks - 1))
  }, [currentHunk, totalHunks, goToHunk])

  if (loading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex-1 flex flex-col items-center justify-center gap-2">
        <p className="text-sm text-destructive">{error}</p>
        <Button size="xs" onClick={() => void fetchDiff()}>
          <RefreshCw className="h-3 w-3 mr-1" />Retry
        </Button>
      </div>
    )
  }

  if (diff.length === 0) {
    return (
      <div className="flex-1 flex flex-col">
        <ReviewHeader sessionId={sessionId ?? ''} readOnly={readOnly} commitSha={commitSha} />
        <div className="flex-1 flex flex-col items-center justify-center gap-2 text-muted-foreground">
          <GitCommit className="h-8 w-8 opacity-50" />
          <p className="text-sm">
            {readOnly ? 'No changes to display' : 'No uncommitted changes to review'}
          </p>
          <Button size="xs" variant="ghost" onClick={() => void fetchDiff()}>
            <RefreshCw className="h-3 w-3 mr-1" />Refresh
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="flex-1 flex flex-col min-h-0">
      <ReviewHeader
        sessionId={sessionId ?? ''}
        readOnly={readOnly}
        commitSha={commitSha}
        currentHunk={currentHunk}
        totalHunks={totalHunks}
        onPrevHunk={goToPrevHunk}
        onNextHunk={goToNextHunk}
      />
      <div ref={scrollRef} className="flex-1 overflow-y-auto custom-scrollbar p-3 space-y-4">
        {diff.map((file) => (
          <FileReviewBlock
            key={file.path}
            sessionId={sessionId ?? ''}
            file={file}
            readOnly={readOnly}
          />
        ))}
      </div>
    </div>
  )
}
