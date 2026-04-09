// Type definitions for Wails v2 runtime
// These match the Go backend types

export interface ProjectInfo {
  id: string
  name: string
  workspace_path: string
  is_external: boolean
  created_at: string
  last_active_at: string
}

export interface SessionInfo {
  id: string
  project_id: string
  name: string
  created_at: string
  last_active_at: string
  archived: boolean
  active: boolean
}

export interface ChatMessage {
  id: number
  session_id: string
  role: string // "user" | "assistant" | "tool_call" | "tool_result" | "routing" | "eval" | "reflection" | "error"
  content: string
  metadata: string // JSON
  created_at: string
}

// Event data types
export interface RoutingData {
  domain: string
  complexity: string
}

export interface ToolCallData {
  step: number
  tool: string
  args: string
  parsed_args?: Record<string, unknown>  // pre-parsed by backend
  plan_step_id?: string
}

export interface ToolResultData {
  step: number
  result_len: number
  result: string
  result_preview?: string // legacy backward compat
  plan_step_id?: string
}

export interface ThoughtData {
  step_num: number
  content: string
  reasoning?: string
  plan_step_id?: string
}

export interface StepData {
  step: number
}

export interface PlanStepData {
  id?: string
  description: string
  status: string // "pending" | "running" | "completed" | "failed"
  depends_on?: string[]
}

export interface PlanData {
  step_count: number
  steps?: PlanStepData[]
  progress?: number           // 0.0–1.0, computed by backend
  current_step_index?: number // -1 if none active
  completed_count?: number
  total_count?: number
}

export interface EvalCriterionData {
  name: string
  description: string
  passed: boolean
  status?: 'pass' | 'fail' | 'unclear'
  diagnostic?: string
}

export interface EvalData {
  passed: number
  total: number
  criteria?: EvalCriterionData[]
}

export interface ReflectionData {
  summary: string
  insights?: string[]
  attempt?: number
  max_attempts?: number
}

// Additional event types
export interface RetryData {
  attempt: number
  max_attempts: number
}

export interface StepRetryData {
  step_id: string
  attempt: number
  max_attempts: number
}

export interface ACExtractedData {
  count: number
}

export interface SubAgentData {
  step_id: string
  description?: string
  success?: boolean
  duration?: number
  plan_step_id?: string
}

export interface AssistantDoneData {
  content: string
  input_tokens: number
  output_tokens: number
}

export interface AssistantChunkData {
  content: string
  accumulated_content?: string  // full accumulated text, computed by backend
}

export interface TaskCompleteData {
  session_id: string
  output: string
  attempt_count?: number
  routing_decision?: RoutingData
}

export interface ToolConfirmData {
  confirm_id: string
  tool: string
  args: string
  reasoning?: string  // judge's explanation of why confirmation is needed
}

export interface PlanStepStartData {
  step_id: string
  description: string
}

export interface PlanStepCompleteData {
  step_id: string
  success: boolean
  duration: number // milliseconds
}

export interface ContextFillData {
  fill_percent: number
  used_tokens: number
  max_tokens: number
  status: string // "ok" | "compact" | "warning" | "emergency" | "reject"
  plan_step_id?: string
  session_input_tokens: number
  session_output_tokens: number
}

export interface SessionTokensData {
  session_input_tokens: number
  session_output_tokens: number
}

export function isSessionTokensData(data: unknown): data is SessionTokensData {
  return typeof data === 'object' && data !== null && 'session_input_tokens' in data && 'session_output_tokens' in data
}

export interface AskUserOption {
  label: string
  value: string
}

export interface AskUserData {
  request_id: string
  question: string
  options: AskUserOption[]
  multi_select: boolean
  recommended?: string[]
}
