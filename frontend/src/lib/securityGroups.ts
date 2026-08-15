// Security tool-group presentation metadata.
//
// The GROUP DATA (which groups exist, their policies, their blacklists) comes
// from the backend (GetSecuritySettings / GetToolList). This module holds only
// presentation concerns: the canonical display order of the seven configurable
// groups and their human-readable copy. The reserved "system" group is never
// configurable and must never appear here.

import type { GroupPolicy } from '@/types/models'

/** The single group that supports a shell-command blacklist. */
export const EXECUTE_GROUP = 'execute'

/** Canonical display order of the seven configurable security groups. */
export const GROUP_ORDER: readonly string[] = [
  'local_read',
  'remote_read',
  'local_write',
  EXECUTE_GROUP,
  'local_mcp',
  'remote_mcp',
  'remote_write',
]

export interface GroupMeta {
  title: string
  description: string
}

/** Human-readable copy per group. Keys mirror backend config.ToolGroup* names. */
export const GROUP_META: Record<string, GroupMeta> = {
  local_read: {
    title: 'Local Read',
    description: 'Read-only access to local workspace files (read_file, glob, ripgrep, list_directory).',
  },
  remote_read: {
    title: 'Remote Read',
    description: 'Read-only access to remote resources (web_search, web_fetch).',
  },
  local_write: {
    title: 'Local Write',
    description: 'Creates, modifies, or deletes local workspace files.',
  },
  execute: {
    title: 'Execute',
    description: 'Shell command execution (bash_exec / posh_exec).',
  },
  local_mcp: {
    title: 'Local MCP',
    description: 'Tools provided by MCP servers launched locally (stdio transport).',
  },
  remote_mcp: {
    title: 'Remote MCP',
    description: 'Tools provided by MCP servers reached over the network (http transport).',
  },
  remote_write: {
    title: 'Remote Write',
    description: 'Writes to remote resources (e.g. mutating tools of remote MCP servers).',
  },
}

/** Dropdown options for a group policy, ordered least → most restrictive. */
export const POLICY_OPTIONS: { value: GroupPolicy; label: string }[] = [
  { value: 'allow', label: 'Allow' },
  { value: 'user_confirm', label: 'User Confirm' },
  { value: 'deny', label: 'Deny' },
]

/** Fail-safe default when a group entry is missing from the backend response. */
export const DEFAULT_GROUP_POLICY: GroupPolicy = 'user_confirm'
