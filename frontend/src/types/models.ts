// Backend model types — mirrors Go structs exposed via Wails

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
  total_input_tokens: number
  total_output_tokens: number
  model: string
  family: string
}

export interface ChatMessage {
  id: number
  session_id: string
  role: string
  content: string
  metadata: number[] | string
  created_at: string
}

export interface FileEntry {
  name: string
  path: string
  is_dir: boolean
  icon?: string
  icon_color?: string
  hidden?: boolean
  gitignored?: boolean
}

export interface GitStatusEntry {
  status: string
  staged: boolean
}

export interface VectorIndexStatus {
  state: 'idle' | 'indexing' | 'ready' | 'reindexing'
  progress: number
  files_indexed: number
  total_files: number
  current_file?: string
  branch?: string
}

export interface VectorStoreEntry {
  file_path: string
  file_name: string
  content: string
  score: number
  start_line: number
  end_line: number
  language: string
}

export interface TokenInfo {
  total_input_tokens: number
  total_output_tokens: number
  model: string
  family: string
}

export interface PlanItem {
  id: string
  title: string
  description?: string
  summary?: string
  status: 'pending' | 'running' | 'completed' | 'failed'
  duration?: number
  dependsOn: string[]
}

export interface PlanGroup {
  id: string
  items: PlanItem[]
  progress?: number
  completedCount?: number
  totalCount?: number
}

// --- Config types ---

export interface ConfigProviderFull {
  base_url: string
  api_key: string
  model: string
}

export interface ConfigProviderKeyModel {
  api_key: string
  model: string
}

export interface ConfigLLMResponse {
  active_provider: string
  anthropic: ConfigProviderKeyModel
  gemini: ConfigProviderKeyModel
  lmstudio: ConfigProviderFull
  openai_compatible: ConfigProviderFull
  chatgpt: ConfigProviderKeyModel
}

export interface ConfigMemResponse {
  database: string
}

export interface ConfigSearchResp {
  provider: string
  api_key: string
}

export interface ConfigResponse {
  loaded: boolean
  log_level: string
  config_migrated: boolean
  config_migration_msg: string
  config_errors: string[]
  llm: ConfigLLMResponse
  memory: ConfigMemResponse
  search: ConfigSearchResp
}

export interface LLMSettingsRequest {
  active_provider: string
  api_key: string
  base_url: string
  model: string
}

export interface SearchSettingsRequest {
  provider: string
  api_key: string
}

export interface ToolPolicyResponse {
  policy: string
  blacklist?: string[]
}

export interface SecuritySettingsResponse {
  default_policy: string
  tool_policies: Record<string, ToolPolicyResponse>
}

export interface ToolInfo {
  name: string
  description: string
  source: string
  policy: string
}

// --- MCP types ---

export interface MCPServerConfig {
  transport: string
  command: string
  args: string[]
  env: Record<string, string>
  url: string
  headers: Record<string, string>
}

export interface CodeMemoryStatus {
  installed: boolean
  path: string
}

export interface MCPServerStatus {
  name: string
  transport: string
  connected: boolean
  tool_count: number
  tools: string[]
  error?: string
}

export interface RtkStatus {
  installed: boolean
  path: string
  version: string
}

// --- Blackboard Viewer types ---

export interface BlackboardState {
  task_id: string
  session_id: string
  status: string
  original_request: string
  plan?: BlackboardPlan
  step_results: Record<string, BlackboardStepResult>
  reflections: BlackboardReflection[]
  facts: BlackboardFact[]
  final_output?: string
  file_changes: Record<string, number>
}

export interface BlackboardPlan {
  steps: BlackboardPlanStep[]
}

export interface BlackboardPlanStep {
  id: string
  summary: string
  description: string
  depends_on: string[]
}

export interface BlackboardStepResult {
  step_id: string
  summary: string
  error?: string
}

export interface BlackboardReflection {
  summary: string
  hypotheses?: string[]
  suggested_action?: string
  reasoning?: string
  failure_analysis?: string
  root_cause?: string
  action_plan?: string
  timestamp: string
}

export interface BlackboardFact {
  keywords: string[]
  content: string
  author: string
}
