import type { ChatMessageUI, MessageType } from '@/stores/chatStore'
import type { session } from '../../wailsjs/go/models'

// Role-to-type mapping for history conversion
export const roleToType: Record<string, MessageType> = {
  user: 'user',
  assistant: 'assistant',
  tool_call: 'tool_call',
  tool_result: 'tool_result',
  routing: 'routing',
  reflection: 'reflection',
  plan: 'plan',
  error: 'error',
  thought: 'thought',
  thinking: 'thinking',
  step_done: 'step_done',
  plan_step_start: 'plan_step_start',
  plan_step_complete: 'plan_step_complete',
  retry: 'retry',
  step_retry: 'step_retry',
  subagent_launch: 'subagent_launch',
  subagent_complete: 'subagent_complete',
  tool_confirm: 'tool_confirm',
  ask_user: 'ask_user',
  task_cancelled: 'error',
  status: 'status',
  task_resumed: 'task_resumed',
}

// Reconstruct human-readable content from metadata to match what live events produce.
// The backend stores metadata JSON as content for most non-assistant types,
// but the live event path in useSessionEvents.ts builds descriptive strings.
function reconstructContent(
  role: string,
  rawContent: string,
  meta: Record<string, unknown> | undefined,
): string {
  if (!meta) return rawContent

  switch (role) {
    case 'routing': {
      const domain = meta.domain as string | undefined
      const complexity = meta.complexity as string | undefined
      if (domain || complexity) {
        return `Domain: ${domain ?? ''} | Complexity: ${complexity ?? ''}`
      }
      return rawContent
    }
    case 'tool_call': {
      const tool = meta.tool as string | undefined
      const args = meta.args as string | undefined
      if (tool) return `${tool}(${args ?? ''})`
      return rawContent
    }
    case 'thought': {
      // Backend extracts content for thought events, so rawContent is already correct
      return rawContent
    }
    case 'thinking': {
      const stepNum = meta.step_num as number | undefined
      return `Step ${stepNum ?? ''}...`
    }
    case 'error': {
      const errorStr = meta.error as string | undefined
      if (errorStr) return errorStr
      return rawContent
    }
    case 'plan_step_start': {
      const desc = meta.description as string | undefined
      return desc || ''
    }
    case 'plan_step_complete': {
      // Live path uses empty content
      return ''
    }
    case 'plan': {
      // Live path uses empty content
      return ''
    }
    case 'retry': {
      const attempt = meta.attempt as number | undefined
      const maxAttempts = meta.max_attempts as number | undefined
      if (attempt !== undefined && maxAttempts !== undefined) {
        return `Retry attempt ${attempt}/${maxAttempts}`
      }
      return rawContent
    }
    case 'step_retry': {
      const attempt = meta.attempt as number | undefined
      const maxAttempts = meta.max_attempts as number | undefined
      if (attempt !== undefined && maxAttempts !== undefined) {
        return `Retrying step (attempt ${attempt}/${maxAttempts})`
      }
      return rawContent
    }
    case 'subagent_launch': {
      const desc = meta.description as string | undefined
      if (desc) return `SubAgent: ${desc}`
      return rawContent
    }
    case 'tool_confirm': {
      const tool = meta.tool as string | undefined
      if (tool) return `Confirm: ${tool}`
      return rawContent
    }
    case 'ask_user': {
      const question = meta.question as string | undefined
      if (question) return question
      return rawContent
    }
    case 'task_cancelled': {
      return 'Task was cancelled'
    }
    case 'status': {
      const content = meta.content as string | undefined
      if (content) return content
      return rawContent
    }
    case 'task_resumed': {
      return rawContent
    }
    // For these types, rawContent from DB is fine as-is:
    // 'user', 'assistant', 'reflection', 'step_done',
    // 'tool_result', 'subagent_complete'
    default:
      return rawContent
  }
}

// Build a semantic ID from history metadata to improve groupMessages cross-referencing.
// Falls back to `history-${dbId}` when metadata doesn't provide enough info.
function buildHistoryId(
  dbId: number,
  role: string,
  meta: Record<string, unknown> | undefined,
  timestamp: number,
): string {
  if (!meta) return `history-${dbId}`

  switch (role) {
    case 'routing':
      return `routing-${timestamp}`
    case 'thinking': {
      const stepNum = meta.step_num as number | undefined
      return stepNum !== undefined ? `step-${stepNum}` : `history-${dbId}`
    }
    case 'step_done': {
      const stepNum = meta.step_num as number | undefined
      return stepNum !== undefined ? `step-${stepNum}` : `history-${dbId}`
    }
    case 'thought': {
      const stepNum = meta.step_num as number | undefined
      return `thought-${stepNum ?? 0}-${timestamp}`
    }
    case 'tool_call': {
      const planStepId = meta.plan_step_id as string | undefined
      const step = meta.step as number | string | undefined
      const callIdx = meta.call_idx as number | string | undefined
      if (planStepId && step !== undefined) return `tool-${planStepId}-${step}${callIdx !== undefined ? `-${callIdx}` : ''}`
      if (step !== undefined) return `tool-${step}${callIdx !== undefined ? `-${callIdx}` : ''}`
      return `history-${dbId}`
    }
    case 'tool_result': {
      // tool_result doesn't need a matching ID since groupMessages
      // matches by step number from metadata, not by message ID
      return `history-${dbId}`
    }
    case 'plan':
      return `plan-${timestamp}`
    case 'plan_step_start': {
      const stepId = meta.step_id as string | undefined
      return stepId ? `plan-step-start-${stepId}-${timestamp}` : `history-${dbId}`
    }
    case 'plan_step_complete': {
      const stepId = meta.step_id as string | undefined
      return stepId ? `plan-step-complete-${stepId}-${timestamp}` : `history-${dbId}`
    }
    case 'retry':
      return `retry-${timestamp}`
    case 'step_retry':
      return `step-retry-${timestamp}`
    case 'subagent_launch': {
      const stepId = meta.step_id as string | undefined
      return stepId ? `subagent-${stepId}-launch` : `history-${dbId}`
    }
    case 'subagent_complete': {
      const stepId = meta.step_id as string | undefined
      return stepId ? `subagent-${stepId}-complete` : `history-${dbId}`
    }
    case 'assistant':
      return `assistant-${timestamp}`
    case 'error':
      return `error-${timestamp}`
    case 'task_cancelled':
      return `cancelled-${timestamp}`
    case 'tool_confirm': {
      const confirmId = meta.confirm_id as string | undefined
      return confirmId ? `tool-confirm-${confirmId}` : `history-${dbId}`
    }
    case 'ask_user': {
      const requestId = meta.request_id as string | undefined
      return requestId ? `ask-user-${requestId}` : `history-${dbId}`
    }
    case 'status':
      return `status-${timestamp}`
    case 'task_resumed':
      return `task-resumed-${timestamp}`
    default:
      return `history-${dbId}`
  }
}

// Convert ChatMessage to ChatMessageUI, producing the same shape
// as live events from useSessionEvents.ts.
export function chatMessageToUI(msg: session.ChatMessage): ChatMessageUI {
  let metadata: Record<string, unknown> | undefined
  if (msg.metadata) {
    try {
      metadata = typeof msg.metadata === 'string' ? JSON.parse(msg.metadata) : msg.metadata
    } catch {
      metadata = undefined
    }
  }

  const msgType = roleToType[msg.role] || 'assistant'
  const timestamp = msg.created_at ? new Date(msg.created_at).getTime() : 0
  const content = reconstructContent(msg.role, msg.content, metadata)
  const id = buildHistoryId(msg.id, msg.role, metadata, timestamp)

  return {
    id,
    sessionId: msg.session_id,
    type: msgType,
    content,
    metadata,
    timestamp,
  }
}
