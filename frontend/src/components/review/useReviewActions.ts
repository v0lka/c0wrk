import { useState, useCallback } from 'react'
import { useReviewStore } from '@/stores/reviewStore'
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
      // Build formatted comments
      const parts: string[] = []
      if (reviewState.generalComment.trim()) {
        parts.push(`General comment:\n${reviewState.generalComment.trim()}`)
      }
      for (const [key, body] of Object.entries(reviewState.hunkComments)) {
        if (!body.trim()) continue
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
