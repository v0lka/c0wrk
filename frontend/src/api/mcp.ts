// MCP API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isMCPServerStatus, isMCPMentionableServer, isArrayOf } from '@/types/guards'
import type { MCPServerStatus, MCPServerConfig, MCPMentionableServer, ToolInfo } from '@/types/models'

export async function getMCPStatus(): Promise<MCPServerStatus[]> {
  try {
    const app = getApp()
    const result = await app.GetMCPStatus()
    if (!isArrayOf(result, isMCPServerStatus)) {
      logger.error('getMCPStatus: unexpected response shape, returning []', result)
      return []
    }
    return result
  } catch (err) {
    logger.error('Failed to get MCP status:', err)
    throw err
  }
}

export async function getMCPServers(): Promise<Record<string, MCPServerConfig>> {
  try {
    const app = getApp()
    const result = await app.GetMCPServers()
    if (typeof result !== 'object' || result === null) {
      throw new Error('getMCPServers: backend returned invalid data')
    }
    // The backend marshals `timeout`/`call_timeout`/`mode` with `omitempty`,
    // so an unset value is absent from the payload. Normalize the missing
    // keys at this boundary so every downstream consumer sees the non-optional
    // shape the type declares: '' timeouts mean "use the default", and an
    // absent mode means "auto" (the backend's effective default).
    const servers: Record<string, MCPServerConfig> = {}
    for (const [name, cfg] of Object.entries(
      result as Record<string, MCPServerConfig>,
    )) {
      servers[name] = {
        ...cfg,
        timeout: cfg.timeout ?? '',
        call_timeout: cfg.call_timeout ?? '',
        mode: cfg.mode ?? 'auto',
      }
    }
    return servers
  } catch (err) {
    logger.error('Failed to get MCP servers:', err)
    throw err
  }
}

export async function updateMCPServers(servers: Record<string, MCPServerConfig>): Promise<void> {
  try {
    const app = getApp()
    await app.UpdateMCPServers(servers)
  } catch (err) {
    logger.error('Failed to update MCP servers:', err)
    throw err
  }
}

/**
 * Fetch the secret-free `{name, mode}` identity of every configured MCP
 * server (GetMCPMentionableServers), name-sorted by the backend. The single
 * read behind the chat input's `/`-completion and the send path's mention
 * partitioning — no transport details, no gateway state, no credentials.
 * Returns [] on an unexpected shape or failure; callers degrade to "no
 * mentionable servers" rather than blocking the input.
 */
export async function getMCPMentionableServers(): Promise<MCPMentionableServer[]> {
  try {
    const app = getApp()
    const result = await app.GetMCPMentionableServers()
    if (!isArrayOf(result, isMCPMentionableServer)) {
      logger.error('getMCPMentionableServers: unexpected response shape, returning []', result)
      return []
    }
    return result
  } catch (err) {
    logger.error('Failed to get MCP mentionable servers:', err)
    return []
  }
}

/**
 * The servers eligible for `/`-mentions: `auto` (always connected, still
 * mentionable to surface the directive) and `manual` (mention-triggered by
 * design). A `disabled` server is inert — never offered in the completion
 * and never threaded as a mention — so it is filtered out of the mentionable
 * set. Shared by the autocomplete source and the send-path partitioning so
 * the two can never disagree.
 */
export function isMentionableMCPMode(mode: 'auto' | 'manual' | 'disabled'): boolean {
  return mode === 'auto' || mode === 'manual'
}

/**
 * Mentionable server names (auto + manual only, disabled hidden), in the
 * backend's name-sorted order — the shape the `/`-completion sections and
 * the send-path partition catalog consume.
 */
export function mentionableMCPNames(servers: MCPMentionableServer[]): string[] {
  return servers.filter((s) => isMentionableMCPMode(s.mode)).map((s) => s.name)
}

export async function getToolList(): Promise<ToolInfo[]> {
  try {
    const app = getApp()
    const result = await app.GetToolList()
    if (!Array.isArray(result)) {
      logger.error('getToolList: unexpected response shape, returning []', result)
      return []
    }
    return result as ToolInfo[]
  } catch (err) {
    logger.error('Failed to get tool list:', err)
    throw err
  }
}

export async function listProviderModels(
  req: import('@/types/models').ListProviderModelsRequest,
): Promise<string[]> {
  try {
    const app = getApp()
    const result = await app.ListProviderModels(req)
    if (!Array.isArray(result)) {
      logger.error('listProviderModels: unexpected response shape, returning []', result)
      return []
    }
    return result as string[]
  } catch (err) {
    logger.error('Failed to list provider models:', err)
    throw err
  }
}
