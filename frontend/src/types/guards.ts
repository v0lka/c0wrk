// Runtime type guards for RPC response validation.
// These validate that data received from the Go backend matches expected shapes.

import type {
    SessionInfo,
    ProjectInfo,
    ProjectSwitchState,
    ChatMessage,
    TokenInfo,
    FileEntry,
    ConfigResponse,
    MCPServerStatus,
    SecuritySettingsResponse,
    BlackboardState,
    SmallLLMConfigResponse,
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
    return isObj(v) && has(v, 'id', 'project_id', 'name')
}

export function isProjectInfo(v: unknown): v is ProjectInfo {
    return isObj(v) && has(v, 'id', 'name', 'workspace_path')
}

export function isProjectSwitchState(v: unknown): v is ProjectSwitchState {
    return isObj(v) && has(v, 'project_id', 'open_tabs')
}

export function isChatMessage(v: unknown): v is ChatMessage {
    return isObj(v) && has(v, 'session_id', 'role', 'content')
}

export function isTokenInfo(v: unknown): v is TokenInfo {
    return isObj(v) && has(v, 'total_input_tokens', 'total_output_tokens')
}

export function isFileEntry(v: unknown): v is FileEntry {
    return isObj(v) && has(v, 'name', 'path', 'is_dir')
}

export function isConfigResponse(v: unknown): v is ConfigResponse {
    return isObj(v) && has(v, 'loaded', 'llm')
}

export function isMCPServerStatus(v: unknown): v is MCPServerStatus {
    return isObj(v) && has(v, 'name', 'connected')
}

export function isSecuritySettingsResponse(v: unknown): v is SecuritySettingsResponse {
    return isObj(v) && has(v, 'default_policy', 'tool_policies', 'auto_approve_workspace_writes', 'smart_approve')
}

export function isBlackboardState(v: unknown): v is BlackboardState {
    return isObj(v) && has(v, 'task_id', 'session_id', 'status')
}

export function isProjectRenamed(v: unknown): v is { id: string; name: string } {
    return isObj(v) && typeof v.id === 'string' && typeof v.name === 'string'
}

export function isSessionRenamed(v: unknown): v is { id: string; name: string } {
    return isObj(v) && typeof v.id === 'string' && typeof v.name === 'string'
}

export function isSmallLLMConfigResponse(v: unknown): v is SmallLLMConfigResponse {
    return isObj(v)
        && has(v, 'enabled', 'essential_tools', 'system_prompt', 'sampling', 'loop_hardening')
}
