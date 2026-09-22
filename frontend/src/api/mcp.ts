// MCP API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isMCPServerStatus, isArrayOf } from '@/types/guards'
import type { MCPServerStatus, MCPServerConfig, ToolInfo } from '@/types/models'

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
    // The backend marshals `timeout`/`call_timeout` with `omitempty`, so an
    // unset value is absent from the payload. Normalize the missing keys to ''
    // at this boundary so every downstream consumer sees the non-optional
    // `string` the type declares (an empty string means "use the default").
    const servers: Record<string, MCPServerConfig> = {}
    for (const [name, cfg] of Object.entries(
      result as Record<string, MCPServerConfig>,
    )) {
      servers[name] = {
        ...cfg,
        timeout: cfg.timeout ?? '',
        call_timeout: cfg.call_timeout ?? '',
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
