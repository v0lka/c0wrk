// Backend model types — mirrors Go structs exposed via Wails

export interface ProjectInfo {
  readonly id: string
  readonly name: string
  readonly workspace_path: string
  readonly is_external: boolean
  readonly is_no_project: boolean
  readonly created_at: string
  readonly last_active_at: string
}

export interface ProjectSwitchState {
  project_id: string
  saved_session_id: string
  open_tabs: string[]
  active_file: string
  updated_at: string
}

export interface ProjectSwitchStatePayload {
  project_id: string
  saved_session_id?: string
  open_tabs: string[]
  active_file?: string
}

export interface SessionInfo {
  readonly id: string
  readonly project_id: string
  readonly name: string
  readonly created_at: string
  readonly last_active_at: string
  readonly archived: boolean
  readonly active: boolean
  readonly total_input_tokens: number
  readonly total_output_tokens: number
  readonly model: string
  readonly family: string
}

export interface ChatMessage {
  readonly id: number
  readonly session_id: string
  readonly role: string
  readonly content: string
  readonly metadata: number[]
  readonly created_at: string
}

export interface FileEntry {
  readonly name: string
  readonly path: string
  readonly is_dir: boolean
  readonly icon?: string
  readonly icon_color?: string
  readonly hidden?: boolean
  readonly gitignored?: boolean
}

export interface GitStatusEntry {
  status: string
  staged: boolean
  index_status: string
  worktree_status: string
}

export interface DiffStat {
  added: number
  deleted: number
}

export interface Branch {
  name: string
  is_current: boolean
}

/** A ref usable as a start-point for CreateBranch (local/remote/tag/commit). */
export interface BranchBase {
  ref: string
  label: string
  type: 'local' | 'remote' | 'tag' | 'commit'
  detail: string
}

/** Current branch with upstream tracking + ahead/behind counts (Phase 5). */
export interface BranchInfo {
  name: string
  upstream: string
  ahead: number
  behind: number
}

/** A file changed by a given commit (Phase 5). Status is a single letter A/M/D/R/C. */
export interface CommitFile {
  path: string
  status: string
}

/** A stash entry (Phase 5). */
export interface StashEntry {
  index: number
  message: string
}

/**
 * A commit in the unified history+graph view. Carries both the
 * human-readable log fields (author/email/date) and the graph topology
 * fields (parents/refs) so the frontend can render lane topology and
 * expandable commit details from one paginated source.
 */
export interface GitHistoryCommit {
  sha: string
  /** Parent commit SHAs (empty for the root commit). */
  parents: string[]
  author: string
  email: string
  date: string
  message: string
  /** Git decorations: branch names, tags, HEAD pointer (e.g. `HEAD -> main`). */
  refs: string[]
}

/** A line range identifying a diff hunk for partial staging (Phase 6). */
export interface HunkRange {
  start_line: number
  end_line: number
}

/** Active merge/rebase state for the current repository (Phase 6). */
export interface MergeRebaseState {
  is_merging: boolean
  is_rebasing: boolean
}

export type SearchMode = 'hybrid' | 'vector' | 'lexical'

export type IndexPhase = 'both' | 'embedding' | 'lexical'

export interface VectorIndexStatus {
  state: 'idle' | 'indexing' | 'ready' | 'reindexing' | 'unavailable'
  progress: number
  files_indexed: number
  total_files: number
  current_file?: string
  branch?: string
  phase?: IndexPhase
  indices?: string[]
}

export interface VectorStoreEntry {
  file_path: string
  file_name: string
  content: string
  score: number
  start_line: number
  end_line: number
  language: string
  vector_score?: number
  lexical_score?: number
  vector_rank?: number
  lexical_rank?: number
}

export interface SearchRequest {
  query: string
  top_k: number
  file_pattern: string
  must_match: string[]
  mode: SearchMode | ''
}

export interface TokenInfo {
  total_input_tokens: number
  total_output_tokens: number
  model: string
  family: string
  fill_percent?: number
  /** Conductor's context-window used token count (session-root only, from session_tokens events). */
  used_tokens?: number
  /** Conductor's context-window total capacity (session-root only, from session_tokens events). */
  max_tokens?: number
}

export interface TodoItem {
  text: string
  checked: boolean
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
  /** Steps that finished successfully (status === 'completed'). */
  completedCount?: number
  /** Steps that finished with an error (status === 'failed'). */
  failedCount?: number
  totalCount?: number
}

export interface SkillDescriptor {
  name: string
  description: string
}

// --- Config types ---

export interface ConfigProviderFull {
  api_key: string
  base_url?: string
  models: string[]  // enabled models for THIS provider
}

export interface ModelInfo {
  name: string
  /**
   * Config key of the provider that exposes this model ("anthropic",
   * "chatgpt", or a named openai_compatible provider). The composite value
   * "provider/name" is the internal selector; the bare `name` is what is
   * displayed to the user.
   */
  provider: string
  family: string
  reasoning?: {
    options: string[]
    default: string
  } | null
}

export interface ConfigLLMResponse {
  default_model: string  // single, global, cross-provider
  anthropic: ConfigProviderFull
  openai_compatible: Record<string, ConfigProviderFull>
  anthropic_compatible: Record<string, ConfigProviderFull>
  chatgpt: ConfigProviderFull
  all_models: ModelInfo[]
  models_ready: boolean
}

export interface ConfigSearchResp {
  provider: string
  api_key: string
}

export interface ConfigResponse {
  loaded: boolean
  log_level: string
  config_errors: string[]
  llm: ConfigLLMResponse
  search: ConfigSearchResp
  proxy: ProxySettingsResponse
}

export interface ProviderConfigRequest {
  api_key?: string
  base_url?: string
  models?: string[]
}

export interface LLMFullConfigRequest {
  default_model?: string
  anthropic?: ProviderConfigRequest
  openai_compatible?: Record<string, ProviderConfigRequest>
  anthropic_compatible?: Record<string, ProviderConfigRequest>
  chatgpt?: ProviderConfigRequest
}

export interface SearchSettingsRequest {
  provider: string
  api_key: string
}

export interface ProxySettingsResponse {
  enabled: boolean
  url: string
  bypass_list: string[]
  tls_cert_dir: string
}

export interface ProxySettingsRequest {
  enabled: boolean
  url: string
  bypass_list: string[]
  tls_cert_dir: string
}

export interface ToolPolicyResponse {
  policy: string
  blacklist?: string[]
}

export interface SecuritySettingsResponse {
  default_policy: string
  tool_policies: Record<string, ToolPolicyResponse>
  auto_approve_workspace_writes: boolean
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

export interface MCPServerStatus {
  name: string
  transport: string
  connected: boolean
  tool_count: number
  tools: string[]
  error?: string
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
