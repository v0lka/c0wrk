import type { ChatMessageUI, MessageType } from '@/stores/chatStore'
import type { session } from '../../wailsjs/go/models'

// Role-to-type mapping for history conversion
export const roleToType: Record<string, MessageType> = {
  user: 'user',
  assistant: 'assistant',
  tool_call: 'tool_call',
  tool_result: 'tool_result',
  routing: 'routing',
  eval: 'eval',
  reflection: 'reflection',
  plan: 'plan',
  error: 'error',
  thought: 'thought',
  thinking: 'thinking',
  step_done: 'step_done',
  plan_step_start: 'plan_step_start',
  plan_step_complete: 'plan_step_complete',
  retry: 'retry',
  escalation: 'escalation',
  ac_extracted: 'ac_extracted',
  subagent_launch: 'subagent_launch',
  subagent_complete: 'subagent_complete',
}

// Convert ChatMessage to ChatMessageUI
export function chatMessageToUI(msg: session.ChatMessage): ChatMessageUI {
  let metadata: Record<string, unknown> | undefined
  if (msg.metadata) {
    try {
      metadata = typeof msg.metadata === 'string' ? JSON.parse(msg.metadata) : msg.metadata
    } catch {
      metadata = undefined
    }
  }
  return {
    id: `history-${msg.id}`,
    sessionId: msg.session_id,
    type: roleToType[msg.role] || 'assistant',
    content: msg.content,
    metadata,
    timestamp: msg.created_at ? new Date(msg.created_at).getTime() : 0,
  }
}
