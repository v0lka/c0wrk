// MCP API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isMentionableInContext } from '@/lib/capabilityModes'
import type { CapabilityContext } from '@/lib/capabilityModes'
import { isMCPServerStatus, isMCPMentionableServer, isToolInfo, isArrayOf } from '@/types/guards'
import type { MCPServerStatus, MCPServerConfig, MCPMentionableServer, MCPServerMode, ToolInfo } from '@/types/models'

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
      // The backend marshals `timeout`/`call_timeout` with `omitempty`,
      // so an unset value is absent from the payload. Normalize the missing
      // keys at this boundary so every downstream consumer sees the
      // non-optional shape the type declares: '' timeouts mean "use the
      // default". (Activation modes left the MCPServerConfig wire shape —
      // they live in the capabilities section, read via getCapabilityModes.)
      const valid = cfg as MCPServerConfig
      servers[name] = {
        ...valid,
        timeout: valid.timeout ?? '',
        call_timeout: valid.call_timeout ?? '',
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
 * Fetch the secret-free `{name, chat_mode, code_mode}` identity of every
 * configured MCP server (GetMCPMentionableServers), name-sorted by the
 * backend. The single read behind the chat input's `/`-completion and the
 * send path's mention partitioning — no transport details, no gateway
 * state, no credentials. Returns [] on an unexpected shape or failure;
 * callers degrade to "no mentionable servers" rather than blocking the
 * input.
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
 * Per-context mentionability: `auto` (always on, still mentionable to
 * surface the directive) and `manual` (mention-triggered by design) count;
 * a `disabled` context is inert — the tools are never advertised there, so
 * a mention is never threaded for it.
 */
export function isMentionableMCPMode(mode: MCPServerMode): boolean {
  return mode === 'auto' || mode === 'manual'
}

/**
 * Whether a server is eligible for `/`-mentions in AT LEAST one context.
 * DISPLAY-only semantics: the chip kind resolution for persisted messages
 * (refCatalogs) renders against history it cannot re-contextualize, so a
 * server mentionable anywhere still resolves to the MCP kind there. The
 * completion and the send path use the SESSION-KIND-AWARE filter instead
 * (isMentionableInContext / mentionableMCPNames) — never this one.
 */
export function isMentionableMCPServer(server: MCPMentionableServer): boolean {
  return isMentionableMCPMode(server.chat_mode) || isMentionableMCPMode(server.code_mode)
}

/**
 * Mentionable server names for ONE session kind (the active session's kind
 * decides the column): only servers whose mode for that context is auto or
 * manual — a server disabled there is inert and its name is excluded, so a
 * plain `/name` ref never partitions into an MCP mention it could not
 * enable. Preserves the backend's name-sorted order. Shared by the send-path
 * partition catalog so it can never disagree with the completion.
 */
export function mentionableMCPNames(servers: MCPMentionableServer[], context: CapabilityContext): string[] {
  return servers
    .filter((s) => isMentionableInContext(context, s))
    .map((s) => s.name)
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

/** Boundary guard for one detector-chain metadata entry: every field is
 *  fail-soft — a malformed value drops the entry rather than poisoning the
 *  UI (the fallback path is "no metadata", the pre-existing behavior). */
function isModelMetadataInfo(v: unknown): v is import('@/types/models').ModelMetadataInfo {
  if (typeof v !== 'object' || v === null) return false
  const o = v as Record<string, unknown>
  if (typeof o.context_window !== 'number' || typeof o.output_limit !== 'number') return false
  if (o.capabilities === undefined) return true
  if (typeof o.capabilities !== 'object' || o.capabilities === null) return false
  const c = o.capabilities as Record<string, unknown>
  return (
    typeof c.attachment === 'boolean' &&
    typeof c.reasoning === 'boolean' &&
    typeof c.temperature === 'boolean' &&
    typeof c.tool_call === 'boolean'
  )
}

export async function listProviderModels(
  req: import('@/types/models').ListProviderModelsRequest,
): Promise<import('@/types/models').ProviderModelsResponse> {
  try {
    const app = getApp()
    // getApp() is deliberately untyped (transport shim) — assert the response
    // shape locally so downstream guards stay checked, not implicit-any.
    const result = await app.ListProviderModels(req) as import('@/types/models').ProviderModelsResponse | null
    if (!result || !Array.isArray(result.models)) {
      logger.error('listProviderModels: unexpected response shape, returning empty', result)
      return { models: [] }
    }
    // Elements are rendered directly as picker options — keep strings only.
    const models = result.models.filter((m): m is string => typeof m === 'string')
    let metadata: import('@/types/models').ProviderModelsResponse['metadata']
    if (typeof result.metadata === 'object' && result.metadata !== null) {
      metadata = {}
      for (const [name, info] of Object.entries(result.metadata)) {
        if (typeof name === 'string' && name !== '' && isModelMetadataInfo(info)) {
          metadata[name] = info
        }
      }
      if (Object.keys(metadata).length === 0) metadata = undefined
    }
    return { models, metadata }
  } catch (err) {
    logger.error('Failed to list provider models:', err)
    throw err
  }
}
