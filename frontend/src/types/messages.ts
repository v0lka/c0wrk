// All message and display item types for the chat system

import { isObj } from '@/types/guards'
import type { AskUserQuestion } from '@/types/events'
export type { AskUserQuestion }

export type MessageType =
  | 'user' | 'assistant' | 'thinking' | 'step_done' | 'tool_call' | 'tool_result'
  | 'tool_confirm' | 'ask_user' | 'routing' | 'reflection' | 'plan' | 'error' | 'thought'
  | 'plan_step_start' | 'plan_step_complete' | 'retry' | 'step_retry'
  | 'subagent_launch' | 'subagent_complete' | 'status'
  | 'task_failed_resumable' | 'task_resumed' | 'step_limit' | 'context_compaction'
  | 'step_todo_update' | 'memory_read' | 'plan_review'
  | 'service'

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
  | 'reflection' | 'step_finish' | 'action_placeholder'
  | 'context_compaction' | 'memory_read' | 'plan_review'

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
  | { kind: 'action_placeholder'; id: string; label: string }
  | { kind: 'context_compaction'; id: string; beforePercent: number; afterPercent: number }
  | { kind: 'memory_read'; id: string; content: string; stepNum?: number }
  | { kind: 'plan_review'; message: ChatMessageUI }

export interface GroupedMessages {
  items: DisplayItem[]
  pendingActions: DisplayItem[]
}

// --- Metadata type helpers for action components ---

// isObj imported from @/types/guards

// -- Typed resolution metadata (write side) --

export type ToolConfirmDecision = 'confirmed' | 'denied'
export type StepLimitDecision = 'allow_once' | 'allow_always' | 'deny'

interface ToolConfirmResolved { resolved: true; decision: ToolConfirmDecision; [key: string]: unknown }
interface StepLimitResolved { resolved: true; decision: StepLimitDecision; [key: string]: unknown }
interface AskUserResolved { resolved: true; answer: string; [key: string]: unknown }
interface ResumeResolved { resolved: true; [key: string]: unknown }

export function toolConfirmResolved(decision: ToolConfirmDecision): ToolConfirmResolved {
  return { resolved: true, decision }
}

export function stepLimitResolved(decision: StepLimitDecision): StepLimitResolved {
  return { resolved: true, decision }
}

export function askUserResolved(answer: string): AskUserResolved {
  return { resolved: true, answer }
}

export function resumeResolved(): ResumeResolved {
  return { resolved: true }
}

// -- Read-side type guards --

const TOOL_CONFIRM_DECISIONS: ReadonlySet<string> = new Set(['confirmed', 'denied'])

export function getToolConfirmResolution(metadata: Record<string, unknown> | undefined): ToolConfirmDecision | null {
  if (!isObj(metadata) || metadata.resolved !== true) return null
  return typeof metadata.decision === 'string' && TOOL_CONFIRM_DECISIONS.has(metadata.decision)
    ? metadata.decision as ToolConfirmDecision
    : null
}

const STEP_LIMIT_DECISIONS: ReadonlySet<string> = new Set(['allow_once', 'allow_always', 'deny'])

export function getStepLimitResolution(metadata: Record<string, unknown> | undefined): StepLimitDecision | null {
  if (!isObj(metadata) || metadata.resolved !== true) return null
  return typeof metadata.decision === 'string' && STEP_LIMIT_DECISIONS.has(metadata.decision)
    ? metadata.decision as StepLimitDecision
    : null
}

export function getAskUserResolution(metadata: Record<string, unknown> | undefined): string | null {
  if (!isObj(metadata) || metadata.resolved !== true) return null
  return typeof metadata.answer === 'string' ? metadata.answer : ''
}

export function isResolved(metadata: Record<string, unknown> | undefined): boolean {
  return isObj(metadata) && metadata.resolved === true
}

export function parseAskUserQuestions(metadata: Record<string, unknown> | undefined): AskUserQuestion[] {
  if (!isObj(metadata) || !Array.isArray(metadata.questions)) return []
  return metadata.questions.filter(
    (q): q is AskUserQuestion =>
      isObj(q) && typeof q.id === 'string' && typeof q.question === 'string' && Array.isArray(q.options),
  )
}
