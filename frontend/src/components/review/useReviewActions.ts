import { useState, useCallback } from 'react'
import { useReviewStore, hunkCommentKey } from '@/stores/reviewStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useAttachmentsStore, EMPTY_ATTACHMENTS } from '@/stores/attachmentsStore'
import { buildUserMessageMeta } from '@/lib/userMessageMeta'
import { generateMessageId } from '@/lib/ids'
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

  const hasComments =
    !!reviewState &&
    (!!reviewState.generalComment.trim() ||
      Object.values(reviewState.fileComments).some((c) => c.trim()) ||
      Object.values(reviewState.hunkComments).some((c) => c.trim()))

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

      // Present the feedback as a user message optimistically. The chat
      // renders user messages from its own optimistic copy only — the
      // message_received event the backend emits is intentionally ignored by
      // the UI (normal sends add the card in useMessageSender before the
      // RPC). A review submit bypasses the chat input, so it must add the
      // card itself; without this the feedback the agent is already working
      // on stays invisible until a session reload brings in the persisted
      // row. Mirrors useMessageSender: task-state handling for the three
      // dispatch paths (fresh / nudge-resume / live interjection) and a full
      // rollback when the send is rejected.
      const wasPaused = useChatStore.getState().paused[sessionId] ?? false
      const isRunning = useChatStore.getState().taskActive[sessionId] ?? false
      const wasActivity = useChatStore.getState().activityStatus[sessionId] ?? null
      const pendingAttachments =
        useAttachmentsStore.getState().attachmentsBySession[sessionId] ?? EMPTY_ATTACHMENTS
      const metadata = buildUserMessageMeta(false, pendingAttachments, wasPaused || isRunning)
      const optimisticId = generateMessageId()
      useChatStore.getState().addMessage(sessionId, {
        id: optimisticId,
        sessionId,
        type: 'user',
        content: commentsText,
        metadata,
        timestamp: Date.now(),
      })
      useSessionStore.getState().touchSession(sessionId)
      if (wasPaused) {
        // Nudge-resume: optimistically leave the paused state (input
        // re-locks, Pause/Stop return).
        useChatStore.getState().setPaused(sessionId, false)
        useChatStore.getState().setTaskActive(sessionId, true)
        useChatStore.getState().setActivityStatus(sessionId, 'Processing...')
      }
      if (!wasPaused && !isRunning) {
        // Fresh task: mark active and show the activity label.
        useChatStore.getState().setTaskActive(sessionId, true)
        useChatStore.getState().setActivityStatus(sessionId, 'Processing...')
      }

      // Send the review message FIRST — it is the only carrier of the review
      // text. If it throws, the review must stay intact so the user can retry.
      // Earlier versions cleared comments and set status='submitted' before
      // the send; a send failure then permanently lost the review (cleared DB
      // + a review loop expecting feedback that never arrived).
      //
      // Set the auto-reopen loop flag up front (non-destructive — it only
      // reopens the review page on the next task_complete event). If the send
      // fails, clear the flag so no spurious reopen happens.
      enterReviewLoop(sessionId)
      try {
        await chatApi.sendMessage(sessionId, commentsText, [], [], '', '', false, '', true)
      } catch (err) {
        // Roll back the optimistic card and restore the exact pre-send task
        // state (mirrors useMessageSender's rollback): the feedback text
        // lives on in the review buffer, which stays intact for a retry.
        useChatStore.getState().removeMessage(sessionId, optimisticId)
        useChatStore.getState().setTaskActive(sessionId, isRunning)
        useChatStore.getState().setPaused(sessionId, wasPaused)
        useChatStore.getState().setActivityStatus(sessionId, wasActivity)
        exitReviewLoop(sessionId)
        throw err
      }

      // The send succeeded — the review text is on its way. Now it is safe to
      // tear down the persisted review state and close the UI.
      await reviewApi.clearReviewComments(sessionId)
      await reviewApi.setReviewStatus(sessionId, 'submitted')
      clearSessionReview(sessionId)

      // Close review page. reviewMode: true tells the orchestrator to treat
      // this message as actionable review feedback (a Code Review section is
      // added to the system prompt), so the message text itself stays as the
      // clean user comments without an instruction prefix.
      useFileViewerStore.getState().closeFile('c0wrk:review')
      closeReviewPage()
    } catch (err) {
      logger.error('Submit flow failed:', err)
    } finally {
      setIsSubmitting(false)
    }
  }, [sessionId, reviewState, closeReviewPage, enterReviewLoop, exitReviewLoop, clearSessionReview])

  return {
    hasComments,
    isStaging,
    isSubmitting,
    handleApprove,
    handleSubmit,
  }
}
