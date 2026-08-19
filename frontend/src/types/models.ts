// Backend model types — mirrors Go structs exposed via Wails

export interface ProjectInfo {
  readonly id: string
  readonly name: string
  readonly workspace_path: string
  readonly is_external: boolean
  readonly is_no_project: boolean
  /** Persisted research workspace root (<workspace>/.research) when RESEARCH mode is enabled; empty otherwise. */
  readonly research_root: string
  /** Derived: true for a real (non-No-Project) project with a non-empty research_root. */
  readonly is_research: boolean
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
  readonly pinned: boolean
  readonly active: boolean
  readonly total_input_tokens: number
  readonly total_output_tokens: number
  readonly model: string
  readonly family: string
  /** True when the session has an in-progress or failed task (cannot be forked). */
  readonly has_unfinished_task: boolean
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

/** UI-facing attachment record (camelCase). Mirrors the backend's
 *  snake_case AttachmentInfo, mapped at the @/api/attachments boundary.
 *  `isImage`/`thumbnail` are present for image attachments (png/jpg/gif/webp);
 *  `thumbnail` is a JPEG data URI suitable for inline UI display. */
export interface AttachmentInfoUI {
  readonly id: string
  readonly originalName: string
  readonly format: string
  readonly sizeBytes: number
  /** True for image attachments (png/jpg/jpeg/gif/webp). Absent on older payloads. */
  readonly isImage?: boolean
  /** JPEG data URI (64px) for image attachments; absent for non-images. */
  readonly thumbnail?: string
  /** On-disk location of the staged image; absent for non-images. Mirrored
   *  into the optimistic user-message metadata (StoredImageMeta) at send time
   *  so image thumbnails render immediately. */
  readonly path?: string
  /** MIME type of the staged image (e.g. "image/png"); absent for non-images. */
  readonly mediaType?: string
}

/** Discriminator for the kind of content found on the clipboard by the
 *  backend PasteFromClipboard, in priority order image → files → text → empty.
 *  Shared by the raw (events.ts PasteResultRaw) and UI (PasteResultUI) shapes. */
export type PasteKind = 'image' | 'files' | 'text' | 'empty'

/** CamelCase paste result — the api/attachments.ts `pasteFromClipboard` wrapper
 *  maps the backend's snake_case PasteResult to this at the boundary. `kind`
 *  determines which fields are populated:
 *  - image: `files` holds the staged image attachment when accepted; `rejected`
 *    holds the reason when the image was not staged (vision sentinel or error).
 *  - files: `files` holds the staged attachments from AttachFiles;
 *    `skippedImages` counts image-ext files dropped because the model lacks vision.
 *  - text:  `text` holds the clipboard string.
 *  - empty: nothing is populated. */
export interface PasteResultUI {
  readonly kind: PasteKind
  readonly text?: string
  readonly files: AttachmentInfoUI[]
  readonly rejected?: string
  readonly skippedImages?: number
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

/** Structured per-hunk diff info with staging status (for the hunk panel). */
export interface HunkDiffInfo {
  old_start: number
  old_count: number
  new_start: number
  new_count: number
  /** First actually-changed line in old-file coordinates (excludes context). */
  old_change_start: number
  /** First actually-changed line in new-file coordinates (excludes context). */
  new_change_start: number
  staged: boolean
  /** Raw unified-diff block (header + body) for tooltip display. */
  diff: string
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

export interface AgentDescriptor {
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
  /** Whether the model accepts image attachments (vision capability). */
  vision: boolean
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
  /** Optional to keep existing typed mocks/test fixtures compatible. */
  experimental?: ConfigExperimentalResponse
}

/** Master experimental-features switch (all-or-nothing). */
export interface ConfigExperimentalResponse {
  enabled: boolean
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

/** A model's capability flags: which features it supports. */
export interface ModelCapabilities {
  attachment: boolean
  reasoning: boolean
  temperature: boolean
  tool_call: boolean
}

/** A single model's configurable parameters returned by GetModelConfig.
 *  Effective values reflect an override when set, otherwise the built-in
 *  default; the Default* fields expose the factory defaults so the UI can show
 *  what would change. */
export interface ModelConfigResponse {
  model: string
  context_window: number
  output_limit: number
  tokenizer_type: string
  family: string
  protocol: string
  capabilities: ModelCapabilities
  default_context_window: number
  default_output_limit: number
  default_tokenizer_type: string
  default_family: string
  default_protocol: string
  default_capabilities: ModelCapabilities
  has_override: boolean
}

/** Per-model parameter overrides submitted from the Configure dialog.
 *  TokenizerType/Family/Protocol use '' as "inherit"; capabilities uses
 *  null/undefined as "inherit" (the backend stores the full set atomically). */
export interface ModelConfigRequest {
  context_window: number
  output_limit: number
  tokenizer_type: string
  family: string
  protocol: string
  capabilities?: ModelCapabilities | null
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

export type GroupPolicy = 'allow' | 'user_confirm' | 'deny'

/** One security tool group's policy (mirrors backend GroupPolicyResponse). */
export interface SecurityGroupPolicy {
  policy: GroupPolicy
  /**
   * Regex patterns of shell commands forced to confirmation; execute group
   * only. Present-but-null for the other groups (the backend serializes the
   * key unconditionally so the execute group's explicit-empty choice
   * survives the round trip); the execute group always carries a non-null
   * list — the effective defaults when unset.
   */
  blacklist?: string[] | null
}

export interface SecuritySettingsResponse {
  /** The seven configurable tool groups (the reserved "system" group is never sent). */
  groups: Record<string, SecurityGroupPolicy>
  auto_approve_workspace_writes: boolean
  smart_approve: boolean
  /** Read-only: whether the strict judge is operational. Sent by the backend. */
  judge_available?: boolean
  /**
   * Read-only: the shipped default execute-blacklist patterns, sent by the
   * backend so the settings UI can offer a one-click restore. Saving them
   * lands the config back in the track-defaults state (store-as-unset).
   */
  execute_blacklist_defaults?: string[]
}

// --- Small LLM profile config ---
// Mirrors the Wails-generated backend.SmallLLMConfigResponse classes
// (frontend/wailsjs/go/models.ts). Used by the "Small LLM" settings tab for
// both reading (GetSmallLLMConfig) and writing (UpdateSmallLLMConfig).

export interface SmallLLMEssentialTools {
  enabled: boolean
  always_present: string[]
  max_tools: number
  /** Replace builtin tool descriptions with one-line compact variants. */
  compact_descriptions: boolean
  /**
   * Read-only: protected orchestration tools the backend always keeps
   * (unioned into always_present). Rendered as locked chips; ignored on write.
   */
  protected_tools: string[]
}

export interface SmallLLMSystemPrompt {
  lite: boolean
  few_shot: boolean
  reasoning_scaffold: boolean
}

export interface SmallLLMSampling {
  enabled: boolean
  temperature: number
  top_p: number
  top_k: number
  repetition_penalty: number
  reasoning_effort: string
}

export interface SmallLLMLoopHardening {
  enabled: boolean
  repeat_nudge_threshold: number
  parse_error_abort_threshold: number
  fruitless_nudge_threshold: number
  fruitless_abort_threshold: number
  same_tool_repeat_nudge_threshold: number
}

/** Context-management variant: aggressive compaction/pruning/reserve tuning. */
export interface SmallLLMContext {
  enabled: boolean
  compaction: {
    keep_last: number
    block_size: number
    trigger_percent: number
  }
  tool_output_keep_last_n: number
  output_token_reserve: number
}

export interface SmallLLMConfigResponse {
  enabled: boolean
  essential_tools: SmallLLMEssentialTools
  system_prompt: SmallLLMSystemPrompt
  sampling: SmallLLMSampling
  loop_hardening: SmallLLMLoopHardening
  context: SmallLLMContext
}

export interface ToolInfo {
  name: string
  description: string
  source: string
  /** Capability group (e.g. "execute", "local_read") — system tools are never listed. */
  group: string
  /** Effective group policy ("allow"|"user_confirm"|"deny"); display-only, not editable per tool. */
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
  starting: boolean
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
  attachments: BlackboardAttachment[]
  final_output?: string
}

/** Committed attachment (flushed from pending on SendMessage). */
export interface BlackboardAttachment {
  id: string
  original_name: string
  format: string
  size_bytes: number
  attached_at: string
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

// ---------------------------------------------------------------------------
// RESEARCH mode (mirrors core/research model + backend DTOs)
// ---------------------------------------------------------------------------

/** Lifecycle status of a hypothesis (see core/research HypothesisStatus). */
export type HypothesisStatus =
  | 'open'
  | 'in-progress'
  | 'confirmed'
  | 'refuted'
  | 'cancelled'

export interface HypothesisNode {
  id: string
  title: string
  status: HypothesisStatus | string
  parents?: string[]
  timebox?: string
  result?: string
}

export interface HypothesisEdge {
  from: string
  to: string
}

export interface HypothesisGraph {
  nodes: HypothesisNode[]
  edges: HypothesisEdge[]
}

export interface ResearchBrief {
  id: string
  title: string
  status?: string
  problem_domain?: string
  quarter?: string
  researchers?: string
  related_researches?: string
  research_question?: string
  success_criteria?: string
}

export interface ResearchIndexEntry {
  id: string
  title?: string
  path?: string
}

export interface ResearchMetrics {
  total: number
  by_status: Record<string, number>
  confirmation_rate: number
  depth: number
  breadth: number
  active_front?: string[]
}

export interface ResearchProject {
  id: string
  brief: ResearchBrief
  graph: HypothesisGraph
  metrics: ResearchMetrics
  prior_art_count: number
  has_report: boolean
}

export interface ResearchRoot {
  path: string
  index: ResearchIndexEntry[]
  projects: ResearchProject[]
  /** The active R-NNN (latest index entry, else highest-numbered project).
   *  Single source of truth shared with the orchestrator's research context.
   *  Empty when no project exists yet. */
  active_project_id?: string
}

/** Per-skill outcome of seeding the research skill-pack (mirrors backend DTO). */
export interface ResearchSeedResult {
  seeded: string[]
  updated: string[]
  current: string[]
  preserved: string[]
}

/** View model for GetResearchStatus: toggle state + parsed research root. */
export interface ResearchStatus {
  enabled: boolean
  project_id: string
  research_root: string
  root?: ResearchRoot
  seed_result?: ResearchSeedResult
}

/** Lightweight response for GetResearchGraph: only the hypothesis graph,
 *  metrics, and report flag for a single project — no brief, index, or root
 *  metadata. Used by the incremental file-change update path. */
export interface ResearchGraphResponse {
  project_id: string
  graph: {
    nodes: HypothesisNode[]
    edges: HypothesisEdge[]
  }
  metrics: {
    total: number
    by_status: Record<string, number>
    confirmation_rate: number
    depth: number
    breadth: number
    active_front?: string[]
  }
  has_report: boolean
}
