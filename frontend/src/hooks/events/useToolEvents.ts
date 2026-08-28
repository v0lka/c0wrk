// Tool events: tool_call, tool_result, tool_confirm, tool_judge_response,
// tool_judge_started, tool_judge_finished

import { useEffect } from 'react'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { isToolCallData, isToolResultData, isToolConfirmData, isToolJudgePhaseData } from '@/types/events'
import { useChatStore } from '@/stores/chatStore'
import { handleToolConfirmEvent, handleToolJudgeStartedEvent, handleToolJudgeFinishedEvent } from './hitlHandlers'

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
        const isMemoryTool = ['read_final_result', 'read_evidence', 'read_step_output', 'list_step_outputs', 'store_fact', 'search_facts'].includes(data.tool)
        const activityLabel = isMemoryTool
          ? 'Using memory...'
          : data.tool === 'finish' ? 'Finishing...' : `Running tool: ${data.tool}...`
        useChatStore.getState().setActivityStatus(sessionId, activityLabel)

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
            attachment_name: data.attachment_name,
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
        handleToolConfirmEvent(sessionId, data)
      }),
    )

    // --- tool_judge_started / tool_judge_finished ---
    // Strict-judge (Smart Approve) phases: the judge runs BEFORE a
    // confirmation card exists, so the status must honestly say the judge is
    // working instead of implying a pending user response.
    cleanups.push(
      onSessionEvent(sessionId, 'tool_judge_started', (data) => {
        if (!isToolJudgePhaseData(data)) { reportDroppedEvent('tool_judge_started', data); return }
        handleToolJudgeStartedEvent(sessionId, data)
      }),
    )
    cleanups.push(
      onSessionEvent(sessionId, 'tool_judge_finished', (data) => {
        if (!isToolJudgePhaseData(data)) { reportDroppedEvent('tool_judge_finished', data); return }
        handleToolJudgeFinishedEvent(sessionId, data)
      }),
    )

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}
