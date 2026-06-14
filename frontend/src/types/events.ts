import type { ProjectInfo, VectorIndexStatus } from '@/types/models'
import { isObj, has } from '@/types/guards'

// Re-export isObj/has from guards for backward compatibility
export { isObj, has }

// Typed event system — session-scoped and global event payloads and maps

// --- Session event payload interfaces ---

export interface RoutingData { domain: string; complexity: string; mode: string }

export interface ToolCallData {
  tool_call_id?: string; step: number; tool: string; args: string
  parsed_args?: Record<string, unknown>; plan_step_id?: string
  source?: string; call_idx?: number; retry_attempt?: number
}

export interface ToolResultData {
  tool_call_id?: string; step: number; result_len: number; result: string
  result_preview?: string; plan_step_id?: string
  call_idx?: number; retry_attempt?: number
}

export interface ThoughtData { step_num: number; content: string; reasoning?: string; plan_step_id?: string }
export interface StepData { step_num: number }
export interface ErrorData { error: string }

export interface PlanStepData { id?: string; description: string; summary?: string; status: string; depends_on?: string[] }

export interface PlanData {
  step_count: number; steps?: PlanStepData[]; progress?: number
  current_step_index?: number; completed_count?: number; total_count?: number
}

export interface PlanStepStartData { step_id: string; description: string; summary?: string }
export interface PlanStepCompleteData { step_id: string; success: boolean; duration: number; error?: string; progress?: number; current_step_index?: number; completed_count?: number; total_count?: number }

export interface ToolConfirmData { confirm_id: string; tool: string; args: string; reasoning?: string }

export interface AskUserQuestion {
  id: string; question: string
  options: Array<{ label: string; value: string }>
  multi_select?: boolean; recommended?: string[]
}
export interface AskUserData { request_id: string; questions: AskUserQuestion[] }
export interface StepLimitData { request_id: string; current_step: number; max_steps: number; reason?: string }

export interface ContextFillData {
  fill_percent: number; used_tokens: number; max_tokens: number; status: string
  plan_step_id?: string; session_input_tokens: number; session_output_tokens: number
  model: string; family: string
}

export interface ContextCompactionData { before_percent: number; after_percent: number; plan_step_id?: string }
export interface SessionTokensData { session_input_tokens: number; session_output_tokens: number; model: string; family: string }
export interface AssistantChunkData { content: string; accumulated_content?: string }
export interface TaskCompleteData { session_id?: string; output?: string; attempt_count?: number; routing_decision?: Record<string, unknown> }
export interface SubAgentLaunchData { step_id: string; description: string; plan_step_id?: string }
export interface SubAgentCompleteData { step_id: string; success: boolean; duration: number; plan_step_id?: string }
export interface RetryData { attempt: number; max_attempts: number }
export interface StepRetryData { step_id: string; attempt: number; max_attempts: number }
export interface ServiceData { content: string; phase?: string }
export interface SessionRenamedData { new_name: string; old_name?: string; id?: string }
export interface TaskFailedResumableData { message?: string }
export interface ReflectionData {
  summary: string
  insights?: string[]
  suggested_action?: string
  root_cause?: string
  failure_analysis?: string
  action_plan?: string
  reasoning?: string
  attempt: number
  max_attempts: number
}

export interface ToolJudgeResponseData { confirm_id: string; reasoning?: string; error?: string }
export interface TerminalOutputData { data: string }
export interface SkillsActivatedData { skills: string[] }
export interface BlackboardUpdatedData { change_type: string }

export interface TodoItemData { text: string; checked: boolean }
export interface StepTodoUpdateData {
  step_id: string
  items: TodoItemData[]
  completed_count: number
  total_count: number
}

// --- Session event map ---

export interface SessionEventMap {
  readonly routing: RoutingData
  readonly step_start: StepData
  readonly step_complete: StepData
  readonly thought: ThoughtData
  readonly tool_call: ToolCallData
  readonly tool_result: ToolResultData
  readonly tool_confirm: ToolConfirmData
  readonly ask_user: AskUserData
  readonly step_limit: StepLimitData
  readonly plan_generated: PlanData
  readonly plan_step_start: PlanStepStartData
  readonly plan_step_complete: PlanStepCompleteData
  readonly assistant_chunk: AssistantChunkData
  readonly assistant_done: { readonly content: string; readonly input_tokens: number; readonly output_tokens: number }
  readonly error: ErrorData
  readonly task_complete: TaskCompleteData
  readonly task_cancelled: void
  readonly retry: RetryData
  readonly step_retry: StepRetryData
  readonly service: ServiceData
  readonly subagent_launch: SubAgentLaunchData
  readonly subagent_complete: SubAgentCompleteData
  readonly context_fill: ContextFillData
  readonly context_compaction: ContextCompactionData
  readonly session_tokens: SessionTokensData
  readonly task_failed_resumable: TaskFailedResumableData
  readonly task_resumed: void
  readonly tool_judge_response: ToolJudgeResponseData
  readonly finishing: void
  readonly reflection: ReflectionData
  readonly session_renamed: SessionRenamedData
  readonly terminal_output: TerminalOutputData
  readonly skills_activated: SkillsActivatedData
  readonly blackboard_updated: BlackboardUpdatedData
  readonly step_todo_update: StepTodoUpdateData
}

export type SessionEventKey = keyof SessionEventMap

// --- Global event map ---

export interface GlobalEventMap {
  readonly 'startup_error': { readonly message: string; readonly error: string }
  readonly 'backend:ready': void
  readonly 'projects:loaded': void
  readonly 'sessions:loaded': void
  readonly 'workspace:tree_changed': void
  readonly 'vector_index:status': VectorIndexStatus
  readonly 'project:created': ProjectInfo
  readonly 'project:deleted': string
  readonly 'project:renamed': { readonly id: string; readonly name: string }
  readonly 'project:switched': ProjectInfo
}

export type GlobalEventKey = keyof GlobalEventMap

// --- Type guard helpers ---

function isObjLocal(v: unknown): v is Record<string, unknown> {
  return isObj(v)
}

export function isRoutingData(d: unknown): d is RoutingData { return isObj(d) && has(d, 'domain', 'complexity') }
export function isStepData(d: unknown): d is StepData { return isObj(d) && has(d, 'step_num') }
export function isThoughtData(d: unknown): d is ThoughtData { return isObj(d) && has(d, 'content', 'step_num') }
export function isToolCallData(d: unknown): d is ToolCallData { return isObj(d) && has(d, 'tool', 'step') }
export function isToolResultData(d: unknown): d is ToolResultData { return isObj(d) && has(d, 'step', 'result_len') }
export function isToolConfirmData(d: unknown): d is ToolConfirmData { return isObj(d) && has(d, 'confirm_id', 'tool') }
export function isAskUserData(d: unknown): d is AskUserData { return isObj(d) && has(d, 'request_id', 'questions') }
export function isStepLimitData(d: unknown): d is StepLimitData { return isObj(d) && has(d, 'request_id', 'current_step', 'max_steps') }
export function isPlanData(d: unknown): d is PlanData { return isObj(d) && has(d, 'step_count') }
export function isPlanStepStartData(d: unknown): d is PlanStepStartData { return isObj(d) && has(d, 'step_id') }
export function isPlanStepCompleteData(d: unknown): d is PlanStepCompleteData {
  if (!isObj(d) || !has(d, 'step_id', 'success')) return false
  // Validate optional progress fields when present.
  if ('progress' in d && d.progress !== undefined && typeof d.progress !== 'number') return false
  if ('current_step_index' in d && d.current_step_index !== undefined && typeof d.current_step_index !== 'number') return false
  if ('completed_count' in d && d.completed_count !== undefined && typeof d.completed_count !== 'number') return false
  if ('total_count' in d && d.total_count !== undefined && typeof d.total_count !== 'number') return false
  return true
}
export function isAssistantChunkData(d: unknown): d is AssistantChunkData {
  if (!isObjLocal(d)) return false
  const hasContent = 'content' in d && typeof d.content === 'string'
  const hasAccumulated = 'accumulated_content' in d && typeof d.accumulated_content === 'string'
  return hasContent || hasAccumulated
}
export function isErrorData(d: unknown): d is ErrorData { return isObj(d) && has(d, 'error') }
export function isTaskCompleteData(d: unknown): d is TaskCompleteData {
  if (!isObjLocal(d)) return false
  const hasValidOutput = typeof d.output === 'string'
  const hasValidAttempt = typeof d.attempt_count === 'number'
  const hasValidRouting = isObj(d.routing_decision)
  // Accept any valid field as sufficient evidence of a task_complete event;
  // a missing output field (e.g. Wails serialization edge case) is tolerable
  // as long as attempt_count or routing_decision validates.
  if (!(hasValidOutput || hasValidAttempt || hasValidRouting)) return false
  if ('session_id' in d && d.session_id !== undefined && typeof d.session_id !== 'string') return false
  // Allow missing output when other validators pass (defensive fallback).
  if ('output' in d && d.output !== undefined && typeof d.output !== 'string') return false
  return true
}
export function isRetryData(d: unknown): d is RetryData { return isObj(d) && has(d, 'attempt', 'max_attempts') }
export function isStepRetryData(d: unknown): d is StepRetryData { return isObj(d) && has(d, 'step_id', 'attempt', 'max_attempts') }
export function isServiceData(d: unknown): d is ServiceData { return isObj(d) && has(d, 'content') }
export function isSubAgentLaunchData(d: unknown): d is SubAgentLaunchData { return isObj(d) && has(d, 'step_id') }
export function isSubAgentCompleteData(d: unknown): d is SubAgentCompleteData { return isObj(d) && has(d, 'step_id', 'success') }
export function isContextFillData(d: unknown): d is ContextFillData { return isObj(d) && has(d, 'fill_percent', 'status') }
export function isContextCompactionData(d: unknown): d is ContextCompactionData { return isObj(d) && has(d, 'before_percent', 'after_percent') }
export function isSessionTokensData(d: unknown): d is SessionTokensData { return isObj(d) && has(d, 'session_input_tokens', 'session_output_tokens') }
export function isSessionRenamedData(d: unknown): d is SessionRenamedData { return isObj(d) && has(d, 'new_name') }
export function isTaskFailedResumableData(d: unknown): d is TaskFailedResumableData {
  if (!isObjLocal(d)) return false
  return !('message' in d) || typeof d.message === 'string'
}
export function isTerminalOutputData(d: unknown): d is TerminalOutputData { return isObj(d) && typeof d.data === 'string' }
export function isSkillsActivatedData(d: unknown): d is SkillsActivatedData { return isObj(d) && Array.isArray(d.skills) }
export function isReflectionData(d: unknown): d is ReflectionData { return isObj(d) && has(d, 'summary', 'attempt') }
export function isToolJudgeResponseData(d: unknown): d is ToolJudgeResponseData { return isObj(d) && has(d, 'confirm_id') }
export function isBlackboardUpdatedData(d: unknown): d is BlackboardUpdatedData { return isObj(d) && has(d, 'change_type') }
export function isStepTodoUpdateData(d: unknown): d is StepTodoUpdateData {
  return isObj(d) && has(d, 'step_id', 'items') && Array.isArray(d.items)
}

// --- Global event type guards ---

export type StartupError = GlobalEventMap['startup_error']

export function isStartupError(d: unknown): d is StartupError {
  return isObj(d) && typeof d.message === 'string' && typeof d.error === 'string'
}

const VALID_VECTOR_STATES: ReadonlySet<string> = new Set(['idle', 'indexing', 'ready', 'reindexing', 'unavailable'])

export function isVectorIndexPayload(d: unknown): d is VectorIndexStatus {
  if (!isObjLocal(d)) return false
  if (typeof d.state !== 'string' || !VALID_VECTOR_STATES.has(d.state)) return false
  if (typeof d.progress !== 'number') return false
  if (typeof d.files_indexed !== 'number') return false
  if (typeof d.total_files !== 'number') return false
  return true
}
