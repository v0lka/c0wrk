import { useState, useCallback } from 'react'
import { useReviewStore, hunkCommentKey } from '@/stores/reviewStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import * as reviewApi from '@/api/review'
import * as gitApi from '@/api/git'
import * as chatApi from '@/api/chat'
import { logger } from '@/lib/logger'

export function useReviewActions(sessionId: string) {
  const [isStaging, setIsStaging] = useState(false)
  const [isSubmitting, setIsSubmitting] = useState(false)
  const reviewState = useReviewStore((s) => s.bySession[sessionId])
  const closeReviewPage = useReviewStore((s) => s.closeReviewPage)
  const enterReviewLoop = useReviewStore((s) => s.enterReviewLoop)
  const exitReviewLoop = useReviewStore((s) => s.exitReviewLoop)
  const clearSessionReview = useReviewStore((s) => s.clearSessionReview)

  const hasComments = !!reviewState && (
    reviewState.generalComment.trim() ||
    Object.values(reviewState.fileComments).some((c) => c.trim()) ||
    Object.values(reviewState.hunkComments).some((c) => c.trim())
  )

  const handleApprove = useCallback(async () => {
    setIsStaging(true)
    try {
      await gitApi.stageAll()
      await reviewApi.clearReview(sessionId)
      clearSessionReview(sessionId)
      useFileViewerStore.getState().closeFile('c0wrk:review')
      closeReviewPage()
      exitReviewLoop(sessionId)
    } catch (err) {
      logger.error('Approve flow failed:', err)
    } finally {
      setIsStaging(false)
    }
  }, [sessionId, closeReviewPage, clearSessionReview, exitReviewLoop])

  const handleSubmit = useCallback(async () => {
    if (!reviewState) return
    setIsSubmitting(true)
    try {
      // Snapshot which files/hunks are present in the live working-tree diff so
      // orphan comments — whose file or hunk has since been reverted, deleted,
      // or discarded between the last silent re-fetch and submit — are excluded
      // from the payload. Fetching at submit time (rather than trusting
      // ReviewPage's possibly-stale state) guarantees the prune set matches the
      // tree the user just reviewed.
      const validFiles = new Set<string>()
      const validHunkKeys = new Set<string>()
      try {
        const currentDiff = await reviewApi.getReviewDiff()
        for (const file of currentDiff) {
          validFiles.add(file.path)
          file.hunks.forEach((_, i) => {
            validHunkKeys.add(hunkCommentKey(file.path, `hunk-${i}`))
          })
        }
      } catch (err) {
        // If the live diff can't be fetched, fall back to including all
        // comments rather than blocking submission entirely.
        logger.error('Failed to fetch diff for comment pruning; submitting all comments:', err)
      }

      // Build formatted comments, skipping orphans. When the diff snapshot is
      // empty (fetch failed), the size guards keep all comments.
      const parts: string[] = []
      if (reviewState.generalComment.trim()) {
        parts.push(`General comment:\n${reviewState.generalComment.trim()}`)
      }
      for (const [filePath, body] of Object.entries(reviewState.fileComments)) {
        if (!body.trim()) continue
        if (validFiles.size > 0 && !validFiles.has(filePath)) continue
        parts.push(`File: ${filePath}:\n${body.trim()}`)
      }
      for (const [key, body] of Object.entries(reviewState.hunkComments)) {
        if (!body.trim()) continue
        if (validHunkKeys.size > 0 && !validHunkKeys.has(key)) continue
        const idx = key.lastIndexOf('::')
        const filePath = key.slice(0, idx)
        const hunkId = key.slice(idx + 2)
        parts.push(`File: ${filePath}, ${hunkId}:\n${body.trim()}`)
      }
      const commentsText = parts.join('\n\n')

      // Flush buffer and set status
      await reviewApi.clearReviewComments(sessionId)
      await reviewApi.setReviewStatus(sessionId, 'submitted')
      clearSessionReview(sessionId)
      enterReviewLoop(sessionId)

      // Close review page and send follow-up message. reviewMode: true tells
      // the orchestrator to treat this message as actionable review feedback
      // (a Code Review section is added to the system prompt), so the message
      // text itself stays as the clean user comments without an instruction prefix.
      useFileViewerStore.getState().closeFile('c0wrk:review')
      closeReviewPage()
      await chatApi.sendMessage(sessionId, commentsText, [], '', '', false, '', true)
    } catch (err) {
      logger.error('Submit flow failed:', err)
    } finally {
      setIsSubmitting(false)
    }
  }, [sessionId, reviewState, closeReviewPage, enterReviewLoop, clearSessionReview])

  return {
    hasComments,
    isStaging,
    isSubmitting,
    handleApprove,
    handleSubmit,
  }
}
