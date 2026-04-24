// All message and display item types for the chat system

export type MessageType =
  | 'user' | 'assistant' | 'thinking' | 'step_done' | 'tool_call' | 'tool_result'
  | 'tool_confirm' | 'ask_user' | 'routing' | 'reflection' | 'plan' | 'error' | 'thought'
  | 'plan_step_start' | 'plan_step_complete' | 'retry' | 'step_retry'
  | 'subagent_launch' | 'subagent_complete' | 'status'
  | 'task_failed_resumable' | 'task_resumed' | 'step_limit' | 'context_compaction'

export interface ChatMessageUI {
  id: string
  sessionId: string
  type: MessageType
  content: string
  metadata?: Record<string, unknown>
  timestamp: number
}

export type DisplayItemKind =
  | 'user' | 'assistant' | 'thought' | 'thought_group' | 'tool' | 'tool_confirm'
  | 'ask_user' | 'step_limit' | 'resume_action' | 'error' | 'service' | 'plan_step'
  | 'reflection' | 'step_finish' | 'memory_read' | 'action_placeholder'
  | 'context_compaction'

export type DisplayItem =
  | { kind: 'user'; message: ChatMessageUI }
  | { kind: 'assistant'; message: ChatMessageUI }
  | { kind: 'thought'; id: string; stepNum: number; content: string; reasoning?: string }
  | { kind: 'thought_group'; id: string; thoughts: Array<{ content: string; reasoning?: string }> }
  | { kind: 'tool'; id: string; toolName: string; args: string; parsedArgs?: Record<string, unknown>; result?: string; resultLen?: number; status: 'running' | 'success' | 'error' | 'awaiting_confirmation'; source?: string }
  | { kind: 'tool_confirm'; message: ChatMessageUI }
  | { kind: 'ask_user'; message: ChatMessageUI }
  | { kind: 'step_limit'; message: ChatMessageUI }
  | { kind: 'resume_action'; message: ChatMessageUI }
  | { kind: 'error'; message: ChatMessageUI }
  | { kind: 'service'; id: string; variant: 'routing' | 'retry' | 'step_retry' | 'status'; content: string; metadata?: Record<string, unknown> }
  | { kind: 'plan_step'; id: string; stepId: string; stepNum: number; title: string; description?: string; status: 'running' | 'completed' | 'failed'; duration?: number; error?: string; isRetry?: boolean; children: DisplayItem[] }
  | { kind: 'reflection'; id: string; summary: string; suggestedAction: string; rootCause: string; failureAnalysis: string; actionPlan: string; reasoning: string; hypotheses: string[]; attempt: number; maxAttempts: number }
  | { kind: 'step_finish'; id: string; stepNum?: number }
  | { kind: 'memory_read'; id: string; toolName: string; args: string; parsedArgs?: Record<string, unknown>; result?: string; resultLen?: number; status: 'running' | 'success' | 'error' }
  | { kind: 'action_placeholder'; id: string; label: string }
  | { kind: 'context_compaction'; id: string; beforePercent: number; afterPercent: number }

export interface GroupedMessages {
  items: DisplayItem[]
  pendingActions: DisplayItem[]
}
