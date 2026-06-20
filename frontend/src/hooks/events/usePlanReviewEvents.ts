import { useEffect } from 'react'
import { onSessionEvent } from '@/api/runtime'
import { isPlanReviewReadyData, isPlanValidationFailedData } from '@/types/events'
import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { generateMessageId } from '@/lib/ids'

export function usePlanReviewEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups: Array<() => void> = []

    // plan_review_ready
    cleanups.push(
      onSessionEvent(sessionId, 'plan_review_ready', (data) => {
        if (!isPlanReviewReadyData(data)) return

        const store = useChatStore.getState()
        const msgs = selectSessionMessages(store, sessionId)

        // Resolve any previous unreviewed plan messages
        for (let i = msgs.length - 1; i >= 0; i--) {
          const m = msgs[i]!
          if (m.type === 'plan_review' && m.metadata?.resolved !== true) {
            const oldPath = m.metadata?.planPath as string | undefined
            store.updateMessage(sessionId, m.id, {
              metadata: { ...m.metadata, resolved: true, decision: 'superseded' },
            })
            if (oldPath) useFileViewerStore.getState().closeFile(oldPath)
            break
          }
        }

        store.addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'plan_review',
          content: 'Plan is ready for review.',
          metadata: { planPath: data.plan_path, planContent: data.plan_content, resolved: false },
          timestamp: Date.now(),
        })
        store.setActivityStatus('Plan ready for review')

        // Open the plan file in the file viewer
        useFileViewerStore.getState().openFile(data.plan_path)
        useFileViewerStore.getState().setFileContent(data.plan_path, data.plan_content, 'Markdown')
      })
    )

    // plan_validation_failed
    cleanups.push(
      onSessionEvent(sessionId, 'plan_validation_failed', (data) => {
        if (!isPlanValidationFailedData(data)) return
        const issues = data.issues
          .map((i) => `${i.step_index != null ? `Step ${i.step_index}: ` : ''}${i.field}: ${i.description}`)
          .join('; ')
        useChatStore.getState().addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'service' as never,
          content: `Plan validation failed: ${issues}`,
          timestamp: Date.now(),
        })
        useChatStore.getState().setActivityStatus(null)
      })
    )

    // plan_review_awaiting_feedback
    cleanups.push(
      onSessionEvent(sessionId, 'plan_review_awaiting_feedback', () => {
        useChatStore.getState().addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'service' as never,
          content: 'Plan rejected. Describe what needs to change, then send a message.',
          timestamp: Date.now(),
        })
        useChatStore.getState().setActivityStatus(null)
      })
    )

    // plan_review_accepted
    cleanups.push(
      onSessionEvent(sessionId, 'plan_review_accepted', () => {
        const store = useChatStore.getState()
        const msgs = selectSessionMessages(store, sessionId)
        for (let i = msgs.length - 1; i >= 0; i--) {
          const m = msgs[i]!
          if (m.type === 'plan_review' && m.metadata?.resolved !== true) {
            const planPath = m.metadata?.planPath as string | undefined
            store.updateMessage(sessionId, m.id, {
              metadata: { ...m.metadata, resolved: true, decision: 'accepted' },
            })
            if (planPath) useFileViewerStore.getState().closeFile(planPath)
            break
          }
        }
        store.setActivityStatus(null)
      })
    )

    return () => cleanups.forEach((fn) => fn())
  }, [sessionId])
}
