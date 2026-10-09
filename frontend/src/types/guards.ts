// Runtime type guards for RPC response validation.
// These validate that data received from the Go backend matches expected shapes.

import type {
    SessionInfo,
    ProjectInfo,
    ProjectSwitchState,
    ChatMessage,
    SessionBookmark,
    TokenInfo,
    FileEntry,
    ConfigResponse,
    MCPServerStatus,
    MCPMentionableServer,
    AutonomyMode,
    SecuritySettingsResponse,
    ShellExecSettingsResponse,
    ShellExecToolSettings,
    BlackboardState,
    ModelProfile,
    ModelProfileKind,
    ModelProfileValues,
    ModelProfilesResponse,
    ModelProfilesBuiltinTool,
    ModelProfilesToolGroup,
    AgentDescriptor,
    SkillDescriptor,
    ToolInfo,
    GPUDeviceResponse,
    VectorStoreEntry,
} from './models'

export function isObj(v: unknown): v is Record<string, unknown> {
    return typeof v === 'object' && v !== null
}

export function has(v: Record<string, unknown>, ...keys: string[]): boolean {
    return keys.every(k => k in v)
}

/**
 * Validates that value is an array and every element passes the guard.
 *
 * DESIGN NOTE: Previously validated only the first element (O(1)), now validates
 * every element (O(n)). This is a deliberate correctness improvement: a single
 * malformed element from a Wails serialization edge case would previously pass
 * validation and cause runtime errors during iteration. All upstream callers
 * in api/ catch validation failures and return [] (empty array), so a rejected
 * array is handled gracefully. For very large arrays (session history, directory
 * listings), the O(n) cost is acceptable given typical sizes.
 */
export function isArrayOf<T>(v: unknown, guard: (item: unknown) => item is T): v is T[] {
    if (!Array.isArray(v)) return false
    return v.every(item => guard(item))
}

export function isSessionInfo(v: unknown): v is SessionInfo {
    // The declared-string identity fields are type-checked, not just
    // key-presence-checked: consumers call string methods on them
    // (sessionStore.trim(), SessionSelector.toLowerCase()) and render them as
    // React children, so a present non-string must fail the guard instead of
    // throwing during the sidebar render.
    // unfinished_task_status is optional for backward compatibility: payloads
    // from older backends (and non-list readers) omit it entirely; when
    // present it must be a string status ("failed"|"in_progress"|"paused"|"").
    // workspace_binding follows the same pattern (CHAT sessions and older
    // backends omit it); when present it must carry a typed kind string.
    return isObj(v)
        && has(v, 'id', 'project_id', 'name')
        && typeof v.id === 'string'
        && typeof v.project_id === 'string'
        && typeof v.name === 'string'
        && (!('unfinished_task_status' in v) || typeof v.unfinished_task_status === 'string')
        && (!('workspace_binding' in v)
            || v.workspace_binding === null
            || (isObj(v.workspace_binding)
                && (v.workspace_binding as { kind?: unknown }).kind === 'local'
                || (v.workspace_binding as { kind?: unknown }).kind === 'managed_worktree'))
}

export function isProjectInfo(v: unknown): v is ProjectInfo {
    // `name` is rendered as a React child (ProjectSelector) and trimmed by the
    // app-root selectTitleScope selector; `id` drives the active-project
    // lookup — a present non-string must fail here, not in the shell render.
    return isObj(v)
        && has(v, 'id', 'name', 'workspace_path')
        && typeof v.id === 'string'
        && typeof v.name === 'string'
        && typeof v.workspace_path === 'string'
}

export function isProjectSwitchState(v: unknown): v is ProjectSwitchState {
    return isObj(v) && has(v, 'project_id', 'open_tabs')
}

export function isChatMessage(v: unknown): v is ChatMessage {
    // `content` is a declared string that the history reconstruction calls
    // .startsWith/.replace on and renders as a React child — key presence
    // alone lets a present non-string reach those call sites.
    return isObj(v)
        && has(v, 'session_id', 'role', 'content')
        && typeof v.session_id === 'string'
        && typeof v.role === 'string'
        && typeof v.content === 'string'
}

export function isSessionBookmark(v: unknown): v is SessionBookmark {
    // `title` is rendered as a React child (BookmarksPanel); the rest are
    // identity/timestamp strings.
    return isObj(v)
        && has(v, 'id', 'session_id', 'event_key', 'title', 'created_at')
        && typeof v.id === 'string'
        && typeof v.session_id === 'string'
        && typeof v.event_key === 'string'
        && typeof v.title === 'string'
        && typeof v.created_at === 'string'
}

export function isTokenInfo(v: unknown): v is TokenInfo {
    // `model`/`family` are declared strings the status bar renders as React
    // children; the counts are read as numbers.
    return isObj(v)
        && has(v, 'total_input_tokens', 'total_output_tokens', 'model', 'family')
        && typeof v.total_input_tokens === 'number'
        && typeof v.total_output_tokens === 'number'
        && typeof v.model === 'string'
        && typeof v.family === 'string'
}

export function isFileEntry(v: unknown): v is FileEntry {
    // `name`/`path` are declared strings the file tree lower-cases,
    // locale-compares and renders as React children; `is_dir` selects the
    // icon and expansion behavior.
    return isObj(v)
        && has(v, 'name', 'path', 'is_dir')
        && typeof v.name === 'string'
        && typeof v.path === 'string'
        && typeof v.is_dir === 'boolean'
}

/** Element guard for ListAgents (`/`-mention agent catalog). Both fields are
 *  rendered/collected by lib/refCatalogs (`new Set(agents.map(a => a.name))`). */
export function isAgentDescriptor(v: unknown): v is AgentDescriptor {
    return isObj(v) && typeof v.name === 'string' && typeof v.description === 'string'
}

/** Element guard for ListSkills (`/`-mention skill catalog). Same consumer
 *  shape as isAgentDescriptor. */
export function isSkillDescriptor(v: unknown): v is SkillDescriptor {
    return isObj(v) && typeof v.name === 'string' && typeof v.description === 'string'
}

/** Element guard for GetToolList (Settings → Tools). Every field is rendered
 *  directly (name as key + child, group/policy as badges). */
export function isToolInfo(v: unknown): v is ToolInfo {
    return isObj(v)
        && typeof v.name === 'string'
        && typeof v.description === 'string'
        && typeof v.source === 'string'
        && typeof v.group === 'string'
        && typeof v.policy === 'string'
}

/** Element guard for ListVectorIndexGPUs (Settings device picker): rendered
 *  as `#{index} — {name}`. */
export function isGPUDevice(v: unknown): v is GPUDeviceResponse {
    return isObj(v) && typeof v.index === 'number' && typeof v.name === 'string'
}

/** Element guard for SearchVectorStore results: the results list calls string
 *  methods on `content`/`file_path` and renders every required field. */
export function isVectorStoreEntry(v: unknown): v is VectorStoreEntry {
    return isObj(v)
        && typeof v.file_path === 'string'
        && typeof v.file_name === 'string'
        && typeof v.content === 'string'
        && typeof v.language === 'string'
        && typeof v.score === 'number'
        && typeof v.start_line === 'number'
        && typeof v.end_line === 'number'
        && (v.vector_score === undefined || typeof v.vector_score === 'number')
        && (v.lexical_score === undefined || typeof v.lexical_score === 'number')
        && (v.vector_rank === undefined || typeof v.vector_rank === 'number')
        && (v.lexical_rank === undefined || typeof v.lexical_rank === 'number')
}

export function isConfigResponse(v: unknown): v is ConfigResponse {
    return isObj(v) && has(v, 'loaded', 'llm')
}

export function isMCPServerStatus(v: unknown): v is MCPServerStatus {
    // `name` is rendered as a React child (MCPServerCard) and `connected`
    // drives the status indicator — type-check both instead of trusting key
    // presence. (The remaining fields stay presence-only: `tools` is a Go
    // slice the backend legitimately marshals as null when empty, and the
    // card tolerates that today.)
    return isObj(v) && has(v, 'name', 'connected')
        && typeof v.name === 'string'
        && typeof v.connected === 'boolean'
}

/** Validates one GetMCPMentionableServers entry: a name plus a normalized
 *  mode. An unrecognized mode string (older/hand-edited backend payload)
 *  rejects the entry so the caller degrades to "no mentionable servers"
 *  instead of threading an unknown mode into completion filtering. */
export function isMCPMentionableServer(v: unknown): v is MCPMentionableServer {
    return isObj(v)
        && typeof v.name === 'string'
        && (v.mode === 'auto' || v.mode === 'manual' || v.mode === 'disabled')
}

/** Whether v is one of the three autonomy-mode enum values (config.AutonomyMode*). */
export function isAutonomyMode(v: unknown): v is AutonomyMode {
    return v === 'standard' || v === 'assisted' || v === 'silent'
}

export function isSecuritySettingsResponse(v: unknown): v is SecuritySettingsResponse {
    return isObj(v) && has(v, 'groups', 'auto_approve_workspace_writes', 'autonomy_mode')
}

function isShellExecToolSettings(v: unknown): v is ShellExecToolSettings {
    return (
        isObj(v) &&
        has(v, 'command', 'shell') &&
        Array.isArray(v.command) &&
        v.command.every((c) => typeof c === 'string') &&
        typeof v.shell === 'string'
    )
}

export function isShellExecSettingsResponse(v: unknown): v is ShellExecSettingsResponse {
    return (
        isObj(v) &&
        has(v, 'bash_exec', 'posh_exec') &&
        isShellExecToolSettings(v.bash_exec) &&
        isShellExecToolSettings(v.posh_exec)
    )
}

export function isBlackboardState(v: unknown): v is BlackboardState {
    // All four collections are REQUIRED on the type and the panel dereferences
    // them unguarded (Object.keys(state.step_results), .length on each array),
    // so the guard must assert their kind — the backend builder always emits
    // non-nil collections (make([]T, 0, …) / make(map…)), so requiring them
    // cannot reject a conforming payload.
    return isObj(v)
        && has(v, 'task_id', 'session_id', 'status', 'step_results', 'facts', 'reflections', 'attachments')
        && typeof v.task_id === 'string'
        && typeof v.session_id === 'string'
        && typeof v.status === 'string'
        && isObj(v.step_results)
        && Array.isArray(v.facts)
        && Array.isArray(v.reflections)
        && Array.isArray(v.attachments)
}

export function isProjectRenamed(v: unknown): v is { id: string; name: string } {
    return isObj(v) && typeof v.id === 'string' && typeof v.name === 'string'
}

export function isSessionRenamed(v: unknown): v is { id: string; name: string } {
    return isObj(v) && typeof v.id === 'string' && typeof v.name === 'string'
}

function isString(v: unknown): v is string {
    return typeof v === 'string'
}

function isModelProfilesBuiltinTool(v: unknown): v is ModelProfilesBuiltinTool {
    return isObj(v) && typeof v.name === 'string' && typeof v.description === 'string'
}

function isModelProfilesToolGroup(v: unknown): v is ModelProfilesToolGroup {
    return isObj(v)
        && typeof v.id === 'string'
        && typeof v.title === 'string'
        && typeof v.description === 'string'
        && isArrayOf(v.tools, isString)
}

export function isModelProfileKind(v: unknown): v is ModelProfileKind {
    return v === 'predefined' || v === 'custom'
}

/**
 * Validates the 25 knob values of one profile. The backend always serializes
 * every field (zero values included), so a missing key is schema drift and
 * must be rejected — the settings form writes whole sections back and would
 * otherwise corrupt the profile with undefined fields.
 */
export function isModelProfileValues(v: unknown): v is ModelProfileValues {
    if (!isObj(v)) return false
    const et = v.essential_tools
    const sp = v.system_prompt
    const sampling = v.sampling
    const lh = v.loop_hardening
    const ctx = v.context
    if (!isObj(et) || !isObj(sp) || !isObj(sampling) || !isObj(lh) || !isObj(ctx)) return false
    if (!isObj(ctx.compaction)) return false
    return typeof et.enabled === 'boolean'
        && isArrayOf(et.always_present, isString)
        && typeof et.compact_descriptions === 'boolean'
        && typeof sp.lite === 'boolean'
        && typeof sp.few_shot === 'boolean'
        && typeof sp.reasoning_scaffold === 'boolean'
        && typeof sampling.enabled === 'boolean'
        && typeof sampling.temperature === 'number'
        && typeof sampling.top_p === 'number'
        && typeof sampling.top_k === 'number'
        && typeof sampling.repetition_penalty === 'number'
        && typeof sampling.presence_penalty === 'number'
        && typeof sampling.reasoning_effort === 'string'
        && typeof lh.enabled === 'boolean'
        && typeof lh.repeat_nudge_threshold === 'number'
        && typeof lh.parse_error_abort_threshold === 'number'
        && typeof lh.fruitless_nudge_threshold === 'number'
        && typeof lh.fruitless_abort_threshold === 'number'
        && typeof lh.same_tool_repeat_nudge_threshold === 'number'
        && typeof ctx.enabled === 'boolean'
        && typeof ctx.compaction.keep_last === 'number'
        && typeof ctx.compaction.block_size === 'number'
        && typeof ctx.compaction.trigger_percent === 'number'
        && typeof ctx.tool_output_keep_last_n === 'number'
        && typeof ctx.output_token_reserve === 'number'
}

export function isModelProfile(v: unknown): v is ModelProfile {
    return isObj(v)
        && typeof v.id === 'string'
        && typeof v.name === 'string'
        && isModelProfileKind(v.kind)
        && isModelProfileValues(v.values)
}

/**
 * Validates the GetModelProfiles response. The backend guarantees non-nil
 * slices (JSON [], never null) for every list field and always emits
 * suggested_profile_id (string or JSON null), so strict checks are safe.
 */
export function isModelProfilesResponse(v: unknown): v is ModelProfilesResponse {
    if (!isObj(v)) return false
    return isArrayOf(v.profiles, isModelProfile)
        && typeof v.enabled === 'boolean'
        && typeof v.active_id === 'string'
        && (v.suggested_profile_id === null || typeof v.suggested_profile_id === 'string')
        && isArrayOf(v.builtin_tools, isModelProfilesBuiltinTool)
        && isArrayOf(v.tool_groups, isModelProfilesToolGroup)
        && isArrayOf(v.protected_tools, isString)
        && isArrayOf(v.warnings, isString)
}
