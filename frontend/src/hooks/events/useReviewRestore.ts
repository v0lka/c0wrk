// Restore review state on session activation: reload comments from backend,
// reopen the review page if mid-loop, and reconcile stale states.

import { useEffect } from 'react'
import { useReviewStore } from '@/stores/reviewStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useChatStore } from '@/stores/chatStore'

export function useReviewRestore(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return
    let cancelled = false

    const reviewStore = useReviewStore.getState()

    // Always reload the review buffer from the backend on session switch so
    // the store reflects the persisted truth (comments survive restart).
    void reviewStore.loadReview(sessionId).then(() => {
      if (cancelled) return

      const s = useReviewStore.getState()
      const sessionReview = s.bySession[sessionId]
      const isLoopActive = !!s.reviewLoopActive[sessionId]
      const isTaskRunning = useChatStore.getState().taskActive[sessionId] === true

      // Reconcile stale loop state: if the loop is active but the task is NOT
      // running (it failed or was cancelled mid-loop), exit the loop so the
      // user gets a fresh prompt on the next task_complete.
      if (isLoopActive && !isTaskRunning) {
        // Check if the review status is approved — if so, don't reopen.
        if (sessionReview?.status === 'approved') {
          s.exitReviewLoop(sessionId)
          return
        }
        // If status is active/submitted and task not running, the loop is stale.
        s.exitReviewLoop(sessionId)
        return
      }

      // Restore open review page if mid-loop and task is still running
      if (isLoopActive && isTaskRunning && sessionReview &&
          (sessionReview.status === 'active' || sessionReview.status === 'submitted')) {
        const fvStore = useFileViewerStore.getState()
        fvStore.openFile('c0wrk:review')
        fvStore.setCollapsed(false)
        useReviewStore.getState().openReviewPage(sessionId)
      }
    })

    return () => { cancelled = true }
  }, [sessionId])
}
