import type { ProjectInfo, VectorIndexStatus } from '@/types/models'

// Typed event system — session-scoped and global event payloads and maps

// --- Session event payload interfaces ---

export interface RoutingData { domain: string; complexity: string }

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
export interface PlanStepCompleteData { step_id: string; success: boolean; duration: number; error?: string }

export interface ToolConfirmData { confirm_id: string; tool: string; args: string; reasoning?: string }

export interface AskUserQuestion {
  id: string; question: string
  options: Array<{ label: string; value: string }>
  multi_select?: boolean; recommended?: string[]
}
export interface AskUserData { request_id: string; questions: AskUserQuestion[] }
export interface StepLimitData { request_id: string; current_step: number; max_steps: number }

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
export interface SessionRenamedData { new_name: string }
export interface TaskFailedResumableData { message?: string }
export interface ToolJudgeResponseData { confirm_id: string; reasoning?: string; error?: string }
export interface TerminalOutputData { data: string }
export interface SkillsActivatedData { skills: string[] }
export interface BlackboardUpdatedData { change_type: string }

// --- Session event map ---

export interface SessionEventMap {
  routing: RoutingData
  step_start: StepData
  step_complete: StepData
  thought: ThoughtData
  tool_call: ToolCallData
  tool_result: ToolResultData
  tool_confirm: ToolConfirmData
  ask_user: AskUserData
  step_limit: StepLimitData
  plan_generated: PlanData
  plan_step_start: PlanStepStartData
  plan_step_complete: PlanStepCompleteData
  assistant_chunk: AssistantChunkData
  assistant_done: void
  error: ErrorData
  task_complete: TaskCompleteData
  task_cancelled: void
  retry: RetryData
  step_retry: StepRetryData
  service: ServiceData
  subagent_launch: SubAgentLaunchData
  subagent_complete: SubAgentCompleteData
  context_fill: ContextFillData
  context_compaction: ContextCompactionData
  session_tokens: SessionTokensData
  task_failed_resumable: TaskFailedResumableData
  task_resumed: void
  tool_judge_response: ToolJudgeResponseData
  finishing: void
  reflection: void
  session_renamed: SessionRenamedData
  terminal_output: TerminalOutputData
  skills_activated: SkillsActivatedData
  blackboard_updated: BlackboardUpdatedData
}

export type SessionEventKey = keyof SessionEventMap

// --- Global event map ---

export interface GlobalEventMap {
  'startup_error': { message: string; error: string }
  'backend:ready': void
  'projects:loaded': void
  'sessions:loaded': void
  'workspace:tree_changed': { path: string }
  'vector_index:status': VectorIndexStatus
  'project:created': ProjectInfo
  'project:deleted': string
  'project:renamed': { id: string; name: string }
  'project:switched': ProjectInfo
}

export type GlobalEventKey = keyof GlobalEventMap

// --- Type guard helpers ---

function isObj(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null
}

function has<K extends string>(v: Record<string, unknown>, ...keys: K[]): boolean {
  return keys.every(k => k in v)
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
export function isPlanStepCompleteData(d: unknown): d is PlanStepCompleteData { return isObj(d) && has(d, 'step_id', 'success') }
export function isAssistantChunkData(d: unknown): d is AssistantChunkData { return isObj(d) && (has(d, 'content') || has(d, 'accumulated_content')) }
export function isErrorData(d: unknown): d is ErrorData { return isObj(d) && has(d, 'error') }
export function isTaskCompleteData(d: unknown): d is TaskCompleteData { return isObj(d) && (has(d, 'output') || has(d, 'attempt_count') || has(d, 'routing_decision')) }
export function isRetryData(d: unknown): d is RetryData { return isObj(d) && has(d, 'attempt', 'max_attempts') }
export function isStepRetryData(d: unknown): d is StepRetryData { return isObj(d) && has(d, 'step_id', 'attempt', 'max_attempts') }
export function isServiceData(d: unknown): d is ServiceData { return isObj(d) && has(d, 'content') }
export function isSubAgentLaunchData(d: unknown): d is SubAgentLaunchData { return isObj(d) && has(d, 'step_id') }
export function isSubAgentCompleteData(d: unknown): d is SubAgentCompleteData { return isObj(d) && has(d, 'step_id', 'success') }
export function isContextFillData(d: unknown): d is ContextFillData { return isObj(d) && has(d, 'fill_percent', 'status') }
export function isContextCompactionData(d: unknown): d is ContextCompactionData { return isObj(d) && has(d, 'before_percent', 'after_percent') }
export function isSessionTokensData(d: unknown): d is SessionTokensData { return isObj(d) && has(d, 'session_input_tokens', 'session_output_tokens') }
export function isSessionRenamedData(d: unknown): d is SessionRenamedData { return isObj(d) && has(d, 'new_name') }
export function isTaskFailedResumableData(d: unknown): d is TaskFailedResumableData { return isObj(d) }
export function isTerminalOutputData(d: unknown): d is TerminalOutputData { return isObj(d) && typeof d.data === 'string' }
export function isSkillsActivatedData(d: unknown): d is SkillsActivatedData { return isObj(d) && Array.isArray(d.skills) }
export function isBlackboardUpdatedData(d: unknown): d is BlackboardUpdatedData { return isObj(d) && has(d, 'change_type') }

// --- Global event type guards ---

export type StartupError = GlobalEventMap['startup_error']

export function isStartupError(d: unknown): d is StartupError {
  return isObj(d) && typeof d.message === 'string' && typeof d.error === 'string'
}

export function isVectorIndexPayload(d: unknown): d is VectorIndexStatus {
  return isObj(d) && typeof d.state === 'string'
}
