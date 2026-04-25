// MCP API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import type { MCPServerStatus, MCPServerConfig, ToolInfo } from '@/types/models'

export async function getMCPStatus(): Promise<MCPServerStatus[]> {
  try {
    const app = getApp()
    return await app.GetMCPStatus() as MCPServerStatus[]
  } catch (err) {
    logger.error('Failed to get MCP status:', err)
    throw err
  }
}

export async function getMCPServers(): Promise<Record<string, MCPServerConfig>> {
  try {
    const app = getApp()
    return await app.GetMCPServers() as Record<string, MCPServerConfig>
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
    return await app.GetToolList() as ToolInfo[]
  } catch (err) {
    logger.error('Failed to get tool list:', err)
    throw err
  }
}

export async function listProviderModels(provider: string): Promise<string[]> {
  try {
    const app = getApp()
    return await app.ListProviderModels(provider) as string[]
  } catch (err) {
    logger.error('Failed to list provider models:', err)
    throw err
  }
}
