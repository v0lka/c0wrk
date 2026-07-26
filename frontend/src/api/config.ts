// Config API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isConfigResponse, isSecuritySettingsResponse, isProxySettingsResponse } from '@/types/guards'
import type { ConfigResponse, SecuritySettingsResponse, LLMFullConfigRequest, SearchSettingsRequest, ProxySettingsResponse, ProxySettingsRequest } from '@/types/models'

/** Sentinel value returned by backend when an API key is configured but should not be displayed */
export const MASKED_API_KEY = '***configured***'

export async function getConfig(): Promise<ConfigResponse> {
  try {
    const app = getApp()
    const result = await app.GetConfig()
    if (!isConfigResponse(result)) {
      throw new Error('getConfig: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to get config:', err)
    throw err
  }
}

export async function getSecuritySettings(): Promise<SecuritySettingsResponse> {
  try {
    const app = getApp()
    const result = await app.GetSecuritySettings()
    if (!isSecuritySettingsResponse(result)) {
      throw new Error('getSecuritySettings: backend returned invalid data')
    }
    return result
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

export async function updateLLMConfig(req: LLMFullConfigRequest): Promise<void> {
  try {
    const app = getApp()
    await app.UpdateLLMConfig(req)
  } catch (err) {
    logger.error('Failed to update LLM config:', err)
    throw err
  }
}

/**
 * Persist a new `default_model` (LLM section) without touching provider
 * configs or API keys. The backend's UpdateLLMConfig is a partial merge: when
 * only `default_model` is set, provider maps and credentials are left intact.
 * Callers should invalidate the config cache afterwards so model selectors and
 * the "default" badge refresh.
 */
export async function setDefaultModel(model: string): Promise<void> {
  await updateLLMConfig({ default_model: model })
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
    const result = await app.GetLogLevel()
    if (typeof result !== 'string') {
      throw new Error('getLogLevel: backend returned non-string data')
    }
    return result
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

export async function getProxySettings(): Promise<ProxySettingsResponse> {
  try {
    const app = getApp()
    const result = await app.GetProxySettings()
    if (!isProxySettingsResponse(result)) {
      throw new Error('getProxySettings: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to get proxy settings:', err)
    throw err
  }
}

export async function updateProxySettings(settings: ProxySettingsRequest): Promise<void> {
  try {
    const app = getApp()
    await app.UpdateProxySettings(settings)
  } catch (err) {
    logger.error('Failed to update proxy settings:', err)
    throw err
  }
}
