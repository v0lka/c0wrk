// Config API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import type { ConfigResponse, SecuritySettingsResponse, LLMSettingsRequest, SearchSettingsRequest } from '@/types/models'

/** Sentinel value returned by backend when an API key is configured but should not be displayed */
export const MASKED_API_KEY = '***configured***'

export async function getConfig(): Promise<ConfigResponse> {
  try {
    const app = getApp()
    return await app.GetConfig() as ConfigResponse
  } catch (err) {
    logger.error('Failed to get config:', err)
    throw err
  }
}

export async function getSecuritySettings(): Promise<SecuritySettingsResponse> {
  try {
    const app = getApp()
    return await app.GetSecuritySettings() as SecuritySettingsResponse
  } catch (err) {
    logger.error('Failed to get security settings:', err)
    throw err
  }
}

export async function updateSecuritySettings(settings: SecuritySettingsResponse): Promise<void> {
  try {
    const app = getApp()
    await app.UpdateSecuritySettings(settings)
  } catch (err) {
    logger.error('Failed to update security settings:', err)
    throw err
  }
}

export async function updateLLMSettings(settings: LLMSettingsRequest): Promise<void> {
  try {
    const app = getApp()
    await app.UpdateLLMSettings(settings)
  } catch (err) {
    logger.error('Failed to update LLM settings:', err)
    throw err
  }
}

export async function updateSearchSettings(settings: SearchSettingsRequest): Promise<void> {
  try {
    const app = getApp()
    await app.UpdateSearchSettings(settings)
  } catch (err) {
    logger.error('Failed to update search settings:', err)
    throw err
  }
}

export async function getLogLevel(): Promise<string> {
  try {
    const app = getApp()
    return await app.GetLogLevel() as string
  } catch (err) {
    logger.error('Failed to get log level:', err)
    throw err
  }
}

export async function setLogLevel(level: string): Promise<void> {
  try {
    const app = getApp()
    await app.SetLogLevel(level)
  } catch (err) {
    logger.error('Failed to set log level:', err)
    throw err
  }
}
