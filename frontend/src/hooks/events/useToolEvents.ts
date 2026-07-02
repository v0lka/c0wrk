// Tool events: tool_call, tool_result, tool_confirm, tool_judge_response

import { useEffect } from 'react'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { isToolCallData, isToolResultData, isToolConfirmData } from '@/types/events'
import { useChatStore, selectSessionMessages } from '@/stores/chatStore'

/** Build the message ID used to correlate tool_call ↔ tool_result */
function buildToolMsgId(d: { tool_call_id?: string; plan_step_id?: string; step: number; call_idx?: number; retry_attempt?: number }): string {
  if (d.tool_call_id) return `tool-${d.tool_call_id}`
  const retrySuffix = d.retry_attempt ? `-r${d.retry_attempt}` : ''
  const idxSuffix = d.call_idx !== undefined ? `-${d.call_idx}` : ''
  return d.plan_step_id
    ? `tool-${d.plan_step_id}-${d.step}${idxSuffix}${retrySuffix}`
    : `tool-${d.step}${idxSuffix}${retrySuffix}`
}

export function useToolEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups: Array<() => void> = []

    // --- tool_call ---
    cleanups.push(
      onSessionEvent(sessionId, 'tool_call', (data) => {
        if (!isToolCallData(data)) { reportDroppedEvent('tool_call', data); return }
        const isMemoryTool = ['read_evidence', 'read_step_output', 'list_step_outputs', 'store_fact', 'search_facts'].includes(data.tool)
        const activityLabel = isMemoryTool
          ? 'Using memory...'
          : data.tool === 'finish' ? 'Finishing...' : `Running tool: ${data.tool}...`
        useChatStore.getState().setActivityStatus(activityLabel)

        const toolMsgId = buildToolMsgId(data)
        useChatStore.getState().addMessage(sessionId, {
          id: toolMsgId,
          sessionId,
          type: 'tool_call',
          content: `${data.tool}(${data.args})`,
          metadata: {
            step: data.step, tool: data.tool, args: data.args,
            parsed_args: data.parsed_args, plan_step_id: data.plan_step_id,
            source: data.source, call_idx: data.call_idx,
            retry_attempt: data.retry_attempt, tool_call_id: data.tool_call_id,
          },
          timestamp: Date.now(),
        })
      }),
    )

    // --- tool_result ---
    cleanups.push(
      onSessionEvent(sessionId, 'tool_result', (data) => {
        if (!isToolResultData(data)) { reportDroppedEvent('tool_result', data); return }
        const toolMsgId = buildToolMsgId(data)
        const store = useChatStore.getState()
        const sessionIndex = store.messages[sessionId]
        const exists = sessionIndex ? toolMsgId in sessionIndex : false

        const resultMeta = {
          step: data.step, completed: true,
          result: data.result ?? data.result_preview,
          result_preview: data.result_preview, result_len: data.result_len,
          plan_step_id: data.plan_step_id, call_idx: data.call_idx,
          retry_attempt: data.retry_attempt, tool_call_id: data.tool_call_id,
          // Preserve the backend error flag so failed tool calls render as
          // errors in the live view, matching the reloaded-history view.
          error: data.error === true,
        }

        if (exists) {
          store.updateMessage(sessionId, toolMsgId, { metadata: resultMeta })
        } else {
          store.addMessage(sessionId, {
            id: `tool-result-${toolMsgId}`,
            sessionId,
            type: 'tool_result',
            content: '',
            metadata: resultMeta,
            timestamp: Date.now(),
          })
        }
      }),
    )

    // --- tool_confirm ---
    cleanups.push(
      onSessionEvent(sessionId, 'tool_confirm', (data) => {
        if (!isToolConfirmData(data)) { reportDroppedEvent('tool_confirm', data); return }
        const store = useChatStore.getState()
        const msgs = selectSessionMessages(store, sessionId)

        let toolMsgId: string | undefined
        let toolPlanStepId: string | undefined
        for (let i = msgs.length - 1; i >= 0; i--) {
          const m = msgs[i]!
          if (m.type === 'tool_call' && m.metadata?.tool === data.tool) {
            toolMsgId = m.id
            toolPlanStepId = m.metadata?.plan_step_id as string | undefined
            store.updateMessage(sessionId, m.id, {
              metadata: { ...m.metadata, awaiting_confirmation: true },
            })
            break
          }
        }

        store.addMessage(sessionId, {
          id: `tool-confirm-${data.confirm_id}`,
          sessionId,
          type: 'tool_confirm',
          content: `Confirm: ${data.tool}`,
          metadata: {
            confirm_id: data.confirm_id, tool: data.tool,
            args: data.args, reasoning: data.reasoning,
            tool_msg_id: toolMsgId, plan_step_id: toolPlanStepId,
          } as Record<string, unknown>,
          timestamp: Date.now(),
        })
        store.setActivityStatus('Awaiting confirmation...')
      }),
    )

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}
