// Config API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isConfigResponse, isSecuritySettingsResponse, isSmallLLMConfigResponse } from '@/types/guards'
import type { ConfigResponse, SecuritySettingsResponse, LLMFullConfigRequest, SearchSettingsRequest, ProxySettingsRequest, ModelConfigResponse, ModelConfigRequest, SmallLLMConfigResponse } from '@/types/models'

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

/**
 * Cheap probe: whether a default LLM model is configured. Used by flows that
 * only need this single fact (e.g. the settings close check) and must not pay
 * for a full GetConfig response.
 */
export async function hasDefaultModel(): Promise<boolean> {
  try {
    const app = getApp()
    const result = await app.HasDefaultModel()
    if (typeof result !== 'boolean') {
      throw new Error('hasDefaultModel: backend returned non-boolean data')
    }
    return result
  } catch (err) {
    logger.error('Failed to check default model:', err)
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
 * Fetch a single model's configurable parameters (effective values + built-in
 * defaults). Used by the per-model Configure dialog to pre-fill inputs and show
 * what would change.
 */
export async function getModelConfig(model: string): Promise<ModelConfigResponse> {
  try {
    const app = getApp()
    return await app.GetModelConfig(model)
  } catch (err) {
    logger.error('Failed to get model config:', err)
    throw err
  }
}

/**
 * Persist per-model parameter overrides from the Configure dialog. The backend
 * stores only fields that differ from the built-in default. Callers should
 * invalidate the config cache afterwards.
 */
export async function setModelConfig(model: string, req: ModelConfigRequest): Promise<void> {
  try {
    const app = getApp()
    await app.SetModelConfig(model, req)
  } catch (err) {
    logger.error('Failed to set model config:', err)
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

export async function updateProxySettings(settings: ProxySettingsRequest): Promise<void> {
  try {
    const app = getApp()
    await app.UpdateProxySettings(settings)
  } catch (err) {
    logger.error('Failed to update proxy settings:', err)
    throw err
  }
}

export async function getSmallLLMConfig(): Promise<SmallLLMConfigResponse> {
  try {
    const app = getApp()
    const result = await app.GetSmallLLMConfig()
    if (!isSmallLLMConfigResponse(result)) {
      throw new Error('getSmallLLMConfig: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to get Small LLM config:', err)
    throw err
  }
}

export async function updateSmallLLMConfig(config: SmallLLMConfigResponse): Promise<void> {
  try {
    const app = getApp()
    await app.UpdateSmallLLMConfig(config)
  } catch (err) {
    logger.error('Failed to update Small LLM config:', err)
    throw err
  }
}

/**
 * Toggle the master experimental-features switch. The backend persists the
 * change and applies the effective Small-LLM profile immediately; RESEARCH
 * mode is gated at its RPC boundary.
 */
export async function updateExperimentalFeatures(enabled: boolean): Promise<void> {
  try {
    const app = getApp()
    await app.UpdateExperimentalFeatures(enabled)
  } catch (err) {
    logger.error('Failed to update experimental features:', err)
    throw err
  }
}
