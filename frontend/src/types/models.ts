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
  readonly pinned: boolean
  readonly active: boolean
  readonly total_input_tokens: number
  readonly total_output_tokens: number
  readonly model: string
  readonly family: string
  /** True when the session has an in-progress, paused, or failed task (cannot
   *  be forked). Mirrors the backend's GetUnfinishedTask status list. */
  readonly has_unfinished_task: boolean
  /** Machine status of the unfinished task: "failed" | "in_progress" |
   *  "paused", or "" when there is none. Only session list queries populate
   *  it; payloads from older backends may omit the field entirely (the field
   *  is therefore optional — isSessionInfo admits its absence). */
  readonly unfinished_task_status?: string
}

/** Per-strategy manual-compaction prediction (mirrors the backend's
 *  core.CompactionAvailability): is this strategy currently available (would
 *  it actually shrink the conversation history right now), how many tokens it
 *  would reclaim, and whether that reclaim is exact (sliding_window — a dry
 *  run) or a forecast (LLM-backed strategies). */
export interface CompactionAvailability {
  readonly strategy: string
  readonly available: boolean
  readonly reclaim_tokens: number
  readonly exact: boolean
}

export interface ChatMessage {
  readonly id: number
  readonly session_id: string
  readonly role: string
  readonly content: string
  readonly metadata: number[]
  readonly created_at: string
}

/** A user bookmark pinning a chat event within a session (mirrors the backend
 *  session.SessionBookmark). `event_key` is the stable DisplayItem key produced
 *  by groupMessages and consumed for navigation and preview. */
export interface SessionBookmark {
  readonly id: string
  readonly session_id: string
  readonly event_key: string
  readonly title: string
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

/** Optimistic in-flight attachment upload rendered as a spinner chip. Created
 *  locally the moment a file/image staging starts (picker, drag-and-drop,
 *  paste) — before the backend RPC resolves — so the user sees immediate
 *  feedback and can cancel it. Replaced by the real AttachmentInfoUI when the
 *  backend's incremental `attachments:changed` events land. The join between
 *  a placeholder and its landed record is made by the lib's ID-window claim
 *  pass — never by string identity (image records carry a processed path and
 *  pasted images are renamed server-side). */
export interface AttachmentUploadUI {
  /** Client-generated stable id; doubles as the cancel key. */
  readonly id: string
  /** Display label: the staged path's basename for picker/drop, or a generic
   *  "pasted-image" label for clipboard images. Used only as an attribution
   *  hint inside the upload's ID window. */
  readonly fileName: string
  /** Absolute staged path ('' when unknown — clipboard image pastes). Kept
   *  as an exact-path attribution hint; it is NOT the landed record's path
   *  (images are re-encoded to a server-side copy). */
  readonly path: string
  /** True for image files (drives the placeholder chip icon). */
  readonly isImage: boolean
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
  kind: 'local' | 'remote'
  upstream: string
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

/**
 * One page of the unified commit history. `GetGitHistory` is paginated
 * (git log -n <limit> --skip <skip>): the hook accumulates pages and
 * requests the next one with `skip = next_skip` while `has_more` is true.
 */
export interface GitHistoryPage {
  commits: GitHistoryCommit[]
  /** Offset to pass as `skip` when requesting the following page. */
  next_skip: number
  /** True when the backend filled the page (more commits may exist). */
  has_more: boolean
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

export type IndexPhase = 'both' | 'embedding' | 'lexical' | 'open'

/**
 * ONNX Runtime execution provider for the vector-index embedder
 * (vector_index.execution_provider config knob). "auto" tries CUDA and
 * falls back to CPU; "cpu" forces CPU; "cuda" targets an NVIDIA GPU.
 */
export type ExecutionProvider = 'auto' | 'cpu' | 'cuda'

export interface VectorIndexStatus {
  state: 'idle' | 'indexing' | 'ready' | 'reindexing' | 'unavailable' | 'loading'
  progress: number
  files_indexed: number
  total_files: number
  current_file?: string
  branch?: string
  phase?: IndexPhase
  indices?: string[]
  /**
   * ONNX Runtime execution provider the embedder effectively runs on:
   * "cpu" or "cuda" — never "auto" ("auto" is resolved once, at embedder
   * creation; the winner is reported here). Absent when no embedder
   * exists (model files missing or creation failed). Comparing it with
   * requested_execution_provider classifies the outcome: an explicit
   * "cuda" landing on "cpu" is a fallback; "auto" always diverges (it is
   * resolved to either winner), so auto→cuda is a success and auto→cpu
   * is Auto's expected degradation.
   */
  execution_provider?: string
  /**
   * Config value ("auto"|"cpu"|"cuda") the embedder was created with.
   * Diverges from execution_provider for every "auto" request (auto is
   * resolved to the winner) and for an explicit "cuda" degrading to CPU
   * after init failure (WARN + toast) — only the latter is a fallback.
   */
  requested_execution_provider?: string
  /**
   * External nvidia-smi verdict, present only when the effective provider
   * is "cuda" and the startup verification probe ran: true = the driver
   * lists this process among CUDA compute apps; false = absent (possible
   * silent CPU fallback inside the CUDA-capable build). Absent when no
   * probe ran (CPU embedder, embedder unavailable, never initialized).
   */
  cuda_verified?: boolean
  /**
   * Why the effective provider deviates from the requested one (CUDA init
   * failure text). Absent/empty when the requested provider was honored.
   */
  provider_fallback_reason?: string
  /**
   * ONNX device index the running embedder was created with
   * (vector_index.device_id at embedder-creation time), for
   * restart-pending detection: a live config device_id diverging from
   * this value means the running embedder predates the config change.
   * Omitted when 0 (the default "first GPU") — read absence as 0.
   */
  device_id?: number
}

/**
 * One GPU visible to the NVIDIA driver, for the vector-index settings UI
 * (populating the execution-provider device picker). Mirrors the backend's
 * GPUDeviceResponse (which mirrors sp4rk's embedding.GPUDevice): `index`
 * is the driver-assigned ordinal ("0" is the primary GPU — the ONNX device
 * index used by vector_index.device_id); `name` is the informational
 * product name ("NVIDIA GeForce RTX 5060 Ti").
 */
export interface GPUDeviceResponse {
  index: number
  name: string
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
  /** 'paused': cooperative pause checkpoint — started but unfinished;
   *  untouched steps keep 'pending'. 'interrupted': the durable unit ledger
   *  settled the step as abandoned by a crash/app exit (a restart left it in
   *  flight with no terminal event) — NOT a completion, so it renders as a
   *  distinct stopped state rather than a permanent running spinner. */
  status: 'pending' | 'running' | 'completed' | 'failed' | 'paused' | 'interrupted'
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
  /** Per-provider TLS pin (ADR-054). Display round-trip from config.
   *  Empty/absent = standard CA verification (no override). */
  tls_fingerprint?: string
  /** Per-provider session-layer auto-retry interval in seconds (compatible
   *  providers only). 0/absent = disabled. The fixed anthropic/chatgpt
   *  providers always report 0. */
  auto_retry_seconds?: number
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
  /** Inclusive upper bound for per-provider auto_retry_seconds — the SAME
   *  bound the backend's validate()/UpdateLLMConfig enforce (ADR-065). The
   *  Settings form clamps its interval input against this server-provided
   *  limit instead of a duplicated frontend constant. REQUIRED: the field
   *  always serializes (no omitempty on the Go side) and frontend/backend
   *  ship in one binary — a missing or non-positive value fails the
   *  loadConfig contract loudly instead of silently clamping against a
   *  stale compiled-in constant. */
  auto_retry_max_seconds: number
}

export interface ConfigSearchResp {
  provider: string
  api_key: string
}

/**
 * vector_index embedding settings block in ConfigResponse: which ONNX
 * Runtime execution provider the embedder runs on ("auto" | "cpu" |
 * "cuda") and the GPU device index used when the provider resolves to
 * CUDA. Changing these requires an app restart to take effect (the
 * embedder is created once per process); the update path only validates
 * and persists them.
 */
export interface VectorIndexSettingsResponse {
  execution_provider: ExecutionProvider
  device_id: number
}

export interface ConfigResponse {
  loaded: boolean
  log_level: string
  config_errors: string[]
  llm: ConfigLLMResponse
  search: ConfigSearchResp
  /** Optional so older payloads/mocks without the block stay compatible. */
  vector_index?: VectorIndexSettingsResponse
  proxy: ProxySettingsResponse
  /** Optional to keep existing typed mocks/test fixtures compatible. */
  experimental?: ConfigExperimentalResponse
  /**
   * Effective (resolved) Model Profiles profile gate. Optional so existing typed
   * mocks/test fixtures — which predate the field — stay compatible; a payload
   * without it is treated as "ModelProfiles off".
   */
  model_profiles?: ConfigModelProfilesResponse
}

/** Experimental-features switch (all-or-nothing). Its sole availability gate is
 *  the E2S execution mode; Model Profiles is NOT gated by this switch. */
export interface ConfigExperimentalResponse {
  enabled: boolean
}

/** Effective (resolved) Model Profiles profile state exposed by GetConfig — mirrors
 *  the backend's ModelProfilesSettingsResponse, NOT the raw persisted `model_profiles:` section.
 *  `enabled` is the resolved master toggle (config `model_profiles.enabled`);
 *  `essential_tools_enabled` is the resolved essential-tools
 *  variant sub-toggle. Both are false when the config is not yet loaded. */
export interface ConfigModelProfilesResponse {
  enabled: boolean
  essential_tools_enabled: boolean
}

export interface ProviderConfigRequest {
  api_key?: string
  base_url?: string
  models?: string[]
  /** Per-provider TLS pin (ADR-054). Omitted = keep the persisted value
   *  (debounce-safe); a present value is applied verbatim, so '' clears the
   *  pin and disables the override. */
  tls_fingerprint?: string
  /** Per-provider session-layer auto-retry interval (compatible providers
   *  only). Omitted = keep the persisted value (debounce-safe); a present
   *  value is applied verbatim, so 0 disables the retry timer. Ignored for
   *  the fixed anthropic/chatgpt providers. */
  auto_retry_seconds?: number
}

/** Draft credentials for ListProviderModels — lets Fetch Models work for a
 *  compatible provider that has not been persisted yet. */
export interface ListProviderModelsRequest {
  provider: string
  api_key?: string
  base_url?: string
  /** Transport: "openai" | "anthropic". Empty → derive / default openai. */
  type?: string
  /** Draft TLS pin (ADR-054) so Fetch Models reaches self-signed endpoints
   *  before the provider is persisted. Omitted = keep the persisted value;
   *  '' = standard verification. */
  tls_fingerprint?: string
}

/** Ask the backend for the fingerprint the provider's endpoint currently
 *  presents (TLS handshake only, no verification, no API key).
 *
 *  Deliberately carries NO fingerprint field: the "Get" button is
 *  unconditional with respect to any configured pin (ADR-054) — it always
 *  reports what the endpoint serves right now. */
export interface GetProviderTLSCertificateRequest {
  provider: string
  /** Draft base URL from the settings form; empty → persisted base_url. */
  base_url?: string
}

export interface TLSCertificateResponse {
  /** base64(SHA-256(SPKI DER)) of the server's leaf certificate. */
  fingerprint: string
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
   * survives the round trip). Null and an empty list both mean "no
   * patterns" — the blocklist is user-authored and empty by default.
   */
  blocklist?: string[] | null
}

/** The closed set of shells the launch-shape override accepts (mirrors backend config). */
export type ShellExecShellKind = 'bash' | 'sh' | 'zsh' | 'ksh' | 'dash' | 'powershell' | 'pwsh'

/** One shell-exec tool's launch-shape override (mirrors backend ShellExecToolSettings). */
export interface ShellExecToolSettings {
  /**
   * argv template launching the agent's command; exactly one element equals
   * "{command}". Empty = built-in default launch shape.
   */
  command: string[]
  /** The shell the command text is written in; empty when there is no override. */
  shell: string
}

/** The shell_exec config section (mirrors backend ShellExecSettingsResponse). */
export interface ShellExecSettingsResponse {
  /** Consumed on Unix (bash_exec); inert elsewhere. */
  bash_exec: ShellExecToolSettings
  /** Consumed on Windows (posh_exec); inert elsewhere. */
  posh_exec: ShellExecToolSettings
}

/**
 * security.autonomy_mode — the unified autonomy posture replacing the former
 * Smart Approve flag + silent-mode master switch. Mirrors the backend enum
 * (config.AutonomyMode*): "standard" (every prompt reaches a human),
 * "assisted" (the strict judge resolves escalated calls), "silent"
 * (unattended operation via the silent_mode sub-policies).
 */
export type AutonomyMode = 'standard' | 'assisted' | 'silent'

/** One silent-mode sub-policy: a Mode drawn from that sub-policy's enum. */
export interface SilentSubPolicy {
  mode: string
}

/**
 * security.silent_mode — the silent-mode sub-policy container. It has no
 * master switch: the sub-policies are live only while the autonomy mode is
 * "silent" (see AutonomyMode) and inert otherwise. Mirrors the backend
 * SilentModeResponse.
 */
export interface SilentModeSettings {
  tool_confirm: SilentSubPolicy
  step_limit: SilentSubPolicy
  ask_user: SilentSubPolicy
}

export interface SecuritySettingsResponse {
  /** The seven configurable tool groups (the reserved "system" group is never sent). */
  groups: Record<string, SecurityGroupPolicy>
  auto_approve_workspace_writes: boolean
  /**
   * The unified autonomy posture (security.autonomy_mode). Replaces the
   * former smart_approve flag and the silent_mode.enabled master switch.
   */
  autonomy_mode: AutonomyMode
  /** Read-only: whether the strict judge is operational. Sent by the backend. */
  judge_available?: boolean
  /**
   * Silent-mode posture. The backend always sends it; it is optional here so a
   * payload without it still type-checks (consumers fall back to the documented
   * defaults). It must be echoed back on every save or the update would reset it.
   */
  silent_mode?: SilentModeSettings
}

// --- Model Profiles profiles ---
// Wire types for the profile CRUD API (GetModelProfiles / CreateModelProfile /
// UpdateModelProfile / DeleteModelProfile / SelectModelProfile). The ModelProfilesEssentialTools /
// ModelProfilesSystemPrompt / ModelProfilesSampling / ModelProfilesLoopHardening / ModelProfilesContext interfaces below
// double as the form views used by the settings sections.

/** One pin-able built-in tool with its registry description (tooltip text). */
export interface ModelProfilesBuiltinTool {
  name: string
  description: string
}

/** A workflow cluster of built-in tools pinned together as one atomic entry. */
export interface ModelProfilesToolGroup {
  id: string
  title: string
  description: string
  tools: string[]
}

export interface ModelProfilesEssentialTools {
  enabled: boolean
  always_present: string[]
  /** Replace builtin tool descriptions with one-line compact variants. */
  compact_descriptions: boolean
  /**
   * Read-only: protected orchestration tools the backend always keeps
   * regardless of any selection. The UI unions them into the displayed
   * always-present list and renders them as locked chips; ignored on write.
   */
  protected_tools: string[]
  /**
   * Read-only picker universe: every registered built-in tool that is neither
   * MCP-sourced nor goal-mode-only (both are "pin-free" — MCP tools are always
   * kept and goal-mode tools are stripped before any selection runs), sorted by
   * name, each with its registry description. It still contains the
   * always-protected tools (protected_tools), which the selection always keeps,
   * so the picker must subtract the already-allowed set (always_present, into
   * which the backend unions the protected set) before offering an entry.
   * Ignored on write.
   */
  builtin_tools: ModelProfilesBuiltinTool[]
  /**
   * Read-only: workflow clusters (plan, subagents) whose members are pinned
   * together. The picker offers each as one atomic entry and renders the
   * cluster description + member list in its hover tooltip; ignored on write.
   */
  tool_groups: ModelProfilesToolGroup[]
}

export interface ModelProfilesSystemPrompt {
  lite: boolean
  few_shot: boolean
  reasoning_scaffold: boolean
}

export interface ModelProfilesSampling {
  enabled: boolean
  temperature: number
  top_p: number
  top_k: number
  repetition_penalty: number
  /** 0 = inherit the vendor preset (field not sent). Qwen card: 0–2. */
  presence_penalty: number
  reasoning_effort: string
}

export interface ModelProfilesLoopHardening {
  enabled: boolean
  repeat_nudge_threshold: number
  parse_error_abort_threshold: number
  fruitless_nudge_threshold: number
  fruitless_abort_threshold: number
  same_tool_repeat_nudge_threshold: number
}

/** Context-management variant: aggressive compaction/pruning/reserve tuning. */
export interface ModelProfilesContext {
  enabled: boolean
  compaction: {
    keep_last: number
    block_size: number
    trigger_percent: number
  }
  tool_output_keep_last_n: number
  output_token_reserve: number
}

/** Profile origin: shipped catalog entries are read-only, custom ones are user-created. */
export type ModelProfileKind = 'predefined' | 'custom'

/** One catalog profile: stable id, display name, kind, and the 25 knob values. */
export interface ModelProfile {
  id: string
  name: string
  kind: ModelProfileKind
  values: ModelProfileValues
}

/** The 25 knob values of one profile (the master enabled toggle is config.yaml's model_profiles.enabled, not a profile value). */
export interface ModelProfileValues {
  essential_tools: ModelProfilesEssentialToolsValues
  system_prompt: ModelProfilesSystemPrompt
  sampling: ModelProfilesSampling
  loop_hardening: ModelProfilesLoopHardening
  context: ModelProfilesContext
}

/** Value part of the always-present variant (the picker universe lives on ModelProfilesResponse). */
export interface ModelProfilesEssentialToolsValues {
  enabled: boolean
  always_present: string[]
  compact_descriptions: boolean
}

/** Partial update for updateModelProfile: absent fields keep their stored value. */
export interface ModelProfileUpdateRequest {
  name?: string
  config?: ModelProfileValues
}

/** The profile catalog view returned by GetModelProfiles. */
export interface ModelProfilesResponse {
  /** Master Model Profiles toggle (config.yaml model_profiles.enabled) reported verbatim — it
   *  is NOT a value of any profile. False when config is not yet initialized. */
  enabled: boolean
  profiles: ModelProfile[]
  /** Stored active profile id (verbatim, may dangle; warnings explain the fallback). */
  active_id: string
  /** Normalized default-model match against the predefined slugs; null when nothing matches. */
  suggested_profile_id: string | null
  /** Read-only picker universe (every registered built-in that is neither MCP-sourced nor goal-mode-only), sorted by name. */
  builtin_tools: ModelProfilesBuiltinTool[]
  /** Workflow clusters offered as atomic picker entries. */
  tool_groups: ModelProfilesToolGroup[]
  /** Orchestration tools the backend always keeps (rendered as locked chips). */
  protected_tools: string[]
  /** Store/resolver warnings plus one-shot notices. */
  warnings: string[]
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
  /** Initialization handshake timeout (initialize + tools/list) as a Go
   *  duration string (e.g. "30s"), applied to both transports. Empty selects
   *  the backend default (60s); when set it must parse and be positive. */
  timeout: string
  /** Single tools/call invocation timeout as a Go duration string (e.g. "2m"),
   *  applied to both transports. Empty inherits `timeout`; when set it must
   *  parse and be positive. */
  call_timeout: string
}

export interface MCPServerStatus {
  name: string
  transport: string
  connected: boolean
  /** Degraded-but-reachable state: the server answered but is misbehaving
   *  (e.g. it has been timing out back-to-back). Distinct from `connected`
   *  (down) and `starting` (initializing). Optional so older payloads that
   *  predate the flag stay valid. */
  unhealthy?: boolean
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

/** Category of a research-log entry (see core/research LogKind). */
export type LogKind = 'experiment' | 'decision' | 'status_change' | 'note'

/**
 * Iteration decision vocabulary from the research methodology
 * (research-decision skill): after each experiment the researcher chooses
 * continue / pivot / kill / fork. Unlike the status, the backend stores the
 * card's Decision field verbatim (no validation), so a card may carry a
 * legacy free-text value — node/draft/update fields stay `string` and only
 * the editor's option list is fixed (see hypothesisDecision.ts).
 */
export type HypothesisDecision = 'continue' | 'pivot' | 'kill' | 'fork'

export interface HypothesisNode {
  id: string
  title: string
  status: HypothesisStatus | string
  parents?: string[]
  timebox?: string
  result?: string
  /** Long-form card sections (verbatim Markdown bodies, core/research). */
  statement?: string
  verification_criterion?: string
  experiment_notes?: string
  /** Iteration decision (continue / pivot / kill / fork); '' while undecided. */
  decision?: string
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

/** A single timestamped entry in a project's log.md (see core/research
 *  ResearchLogEntry). Mirrors the backend DTO field-for-field. */
export interface ResearchLogEntry {
  id: string
  kind: LogKind
  hypothesis_id?: string
  message: string
  created_at: string
}

export interface ResearchProject {
  id: string
  brief: ResearchBrief
  graph: HypothesisGraph
  metrics: ResearchMetrics
  prior_art_count: number
  has_report: boolean
  log: ResearchLogEntry[]
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

/** View model for GetResearchStatus: enabled flag + parsed research root. */
export interface ResearchStatus {
  enabled: boolean
  project_id: string
  research_root: string
  root?: ResearchRoot
  /** Pinned research projects — brief paths, research-root-relative with
   *  forward slashes (`R-NNN-<slug>/brief.md`), mirrored from the persisted
   *  project pins. Optional on the wire (older payloads omit it); the RPC
   *  boundary normalizes it to `[]`. */
  pinned_research?: string[]
  /** Pinned hypothesis cards keyed by hypothesis id (H-NNN): each entry
   *  lists the pinned card paths (`R-NNN-<slug>/hypotheses/H-NNN.md`) —
   *  the same H-NNN exists across R-NNN projects, so one key can carry
   *  cards from several research projects. Optional on the wire; the RPC
   *  boundary normalizes it to `{}`. */
  pinned_hypotheses?: Record<string, string[]>
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
  log: ResearchLogEntry[]
}

/** Recommended next research action kinds (mirrors core/research ActionKind). */
export type ResearchActionKind =
  | 'research-init'
  | 'research-hypothesis'
  | 'research-experiment'
  | 'research-decision'
  | 'research-synthesis'

/** Recommended next research action for the active project's current phase
 *  (mirrors backend ResearchNextStepDTO). `target` is the hypothesis ID the
 *  action operates on, or empty when the action is not hypothesis-scoped. */
export interface ResearchNextStep {
  project_id: string
  action: ResearchActionKind
  target?: string
  reason: string
  skill: string
}

/** Editable draft of a hypothesis card's mutable fields (title / parents /
 *  status / decision / statement / verification criterion / experiment
 *  notes / timebox / result), held between user edits and an explicit Save.
 *  `parents` is the raw comma-separated input from the card's field — the
 *  change-set derivation parses it into ids before sending. UI-level view
 *  model: lives here (not in a component file) because it is persisted in the
 *  research store so an unsaved draft survives workspace remounts (floating
 *  viewer auto-collapse, tab switches). */
export interface HypothesisDraft {
  title: string
  parents: string
  status: string
  decision: string
  statement: string
  verification_criterion: string
  experiment_notes: string
  timebox: string
  result: string
}

/** Structured field update for an existing hypothesis card (mirrors backend
 *  HypothesisUpdateFields). Omit a field (leave it undefined) to leave it
 *  unchanged; set it to an empty string to clear it. */
export interface HypothesisUpdateFields {
  title?: string
  status?: HypothesisStatus
  result?: string
  timebox?: string
  decision?: string
  statement?: string
  verification_criterion?: string
  experiment_notes?: string
  /** Replaces the card's parent set; validated server-side (existence, no
   *  self-reference, no cycle) before any write. An empty array clears it. */
  parents?: string[]
}

/** Structured input for creating a new hypothesis card (mirrors backend
 *  NewHypothesisCard). The card's identifier is assigned by the backend. */
export interface NewHypothesisCard {
  title: string
  statement?: string
  verification_criterion?: string
  timebox?: string
  parents?: string[]
}
