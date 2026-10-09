// MCP API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isMCPServerStatus, isMCPMentionableServer, isToolInfo, isArrayOf } from '@/types/guards'
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
    // Per-entry tolerance: each map VALUE is validated individually. A valid
    // entry is kept (and normalized below); a malformed entry (null or
    // non-object) is warned about and skipped, so one bad backend entry can
    // no longer erase every other server from the next settings save
    // (MCPSettings replaces the whole map it loaded here). Only a wholly
    // malformed payload (non-object) still throws above.
    const raw = result as Record<string, unknown>
    const servers: Record<string, MCPServerConfig> = {}
    const malformed: string[] = []
    for (const [name, cfg] of Object.entries(raw)) {
      if (cfg === null || typeof cfg !== 'object') {
        malformed.push(name)
        continue
      }
      // The backend marshals `timeout`/`call_timeout`/`mode` with `omitempty`,
      // so an unset value is absent from the payload. Normalize the missing
      // keys at this boundary so every downstream consumer sees the
      // non-optional shape the type declares: '' timeouts mean "use the
      // default", and an absent mode means "auto" (the backend's effective
      // default).
      const valid = cfg as MCPServerConfig
      servers[name] = {
        ...valid,
        timeout: valid.timeout ?? '',
        call_timeout: valid.call_timeout ?? '',
        mode: valid.mode ?? 'auto',
      }
    }
    if (malformed.length > 0) {
      logger.warn(`getMCPServers: skipping malformed server entries: ${malformed.join(', ')}`)
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
    if (!isArrayOf(result, isToolInfo)) {
      logger.error('getToolList: unexpected response shape, returning []', result)
      return []
    }
    return result
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
    // Elements are rendered directly as picker options — keep strings only.
    return result.filter((m): m is string => typeof m === 'string')
  } catch (err) {
    logger.error('Failed to list provider models:', err)
    throw err
  }
}
