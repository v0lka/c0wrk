// Config API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isConfigResponse, isSecuritySettingsResponse, isModelProfilesResponse, isShellExecSettingsResponse } from '@/types/guards'
import type { ConfigResponse, SecuritySettingsResponse, ShellExecSettingsResponse, LLMFullConfigRequest, SearchSettingsRequest, ProxySettingsRequest, ModelConfigResponse, ModelConfigRequest, ModelProfilesResponse, ModelProfileUpdateRequest, VectorIndexSettingsResponse, GetProviderTLSCertificateRequest, TLSCertificateResponse } from '@/types/models'

/** Sentinel value returned by backend when an API key is configured but should not be displayed */
export const MASKED_API_KEY = '***configured***'

/** Guard for the GetModelConfig response. The Configure dialog copies
 *  capabilities/default_capabilities into React state and dereferences their
 *  four boolean flags in the render body (outside its try/catch), so a
 *  payload missing the nested objects must fail HERE instead of throwing a
 *  render TypeError. The Go DTO is fully typed with no omitempty, so a
 *  conforming payload always passes. */
function isModelCapabilities(v: unknown): v is { attachment: boolean; reasoning: boolean; temperature: boolean; tool_call: boolean } {
  return typeof v === 'object' && v !== null
    && typeof (v as Record<string, unknown>).attachment === 'boolean'
    && typeof (v as Record<string, unknown>).reasoning === 'boolean'
    && typeof (v as Record<string, unknown>).temperature === 'boolean'
    && typeof (v as Record<string, unknown>).tool_call === 'boolean'
}

function isModelConfigResponse(v: unknown): v is ModelConfigResponse {
  if (typeof v !== 'object' || v === null) return false
  const o = v as Record<string, unknown>
  return typeof o.model === 'string'
    && typeof o.context_window === 'number'
    && typeof o.output_limit === 'number'
    && typeof o.tokenizer_type === 'string'
    && typeof o.family === 'string'
    && typeof o.protocol === 'string'
    && isModelCapabilities(o.capabilities)
    && typeof o.default_context_window === 'number'
    && typeof o.default_output_limit === 'number'
    && typeof o.default_tokenizer_type === 'string'
    && typeof o.default_family === 'string'
    && typeof o.default_protocol === 'string'
    && isModelCapabilities(o.default_capabilities)
    && typeof o.has_override === 'boolean'
}

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

export async function getShellExecSettings(): Promise<ShellExecSettingsResponse> {
  try {
    const app = getApp()
    const result = await app.GetShellExecSettings()
    if (!isShellExecSettingsResponse(result)) {
      throw new Error('getShellExecSettings: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to get shell exec settings:', err)
    throw err
  }
}

export async function updateShellExecSettings(settings: ShellExecSettingsResponse): Promise<void> {
  try {
    const app = getApp()
    await app.UpdateShellExecSettings(settings)
  } catch (err) {
    logger.error('Failed to update shell exec settings:', err)
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
 * Fetch the certificate fingerprint the provider's endpoint currently
 * presents (the settings "Get" button, ADR-054). Performs only the TLS
 * handshake — no HTTP request, no API key.
 *
 * Unconditional with respect to any configured pin: the request carries no
 * fingerprint and the answer is always "what is this endpoint serving right
 * now?". The draft base_url wins over the persisted one.
 *
 * Rejects while an effective HTTP proxy is enabled: the probe dials directly,
 * so a pin fetched then would be inert (proxy wins).
 */
export async function getProviderTLSCertificate(
  req: GetProviderTLSCertificateRequest,
): Promise<TLSCertificateResponse> {
  try {
    const app = getApp()
    const result = await app.GetProviderTLSCertificate(req)
    if (!result || typeof result.fingerprint !== 'string') {
      throw new Error('getProviderTLSCertificate: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to fetch provider TLS certificate:', err)
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
    const result = await app.GetModelConfig(model)
    if (!isModelConfigResponse(result)) {
      throw new Error('getModelConfig: backend returned invalid data')
    }
    return result
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

/**
 * Persist the vector-index embedding settings (ONNX Runtime execution
 * provider + GPU device id). The backend validates BEFORE mutating or
 * writing: an invalid provider or a negative device id leaves both the
 * in-memory config and the YAML file untouched. There is deliberately NO
 * hot application — the embedder is created once per process (after
 * EventBackendReady), so the new provider/device only takes effect after
 * an app restart. Callers should invalidate the config cache afterwards.
 */
export async function updateVectorIndexSettings(settings: VectorIndexSettingsResponse): Promise<void> {
  try {
    const app = getApp()
    await app.UpdateVectorIndexSettings(settings)
  } catch (err) {
    logger.error('Failed to update vector index settings:', err)
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

/**
 * How long a delivered OS notification banner stays on screen, in seconds:
 * -1 = the notification daemon's own default, 0 = never expires (stays until
 * clicked or dismissed), >0 = an explicit lifetime. Linux only — macOS and
 * Windows notification centers own banner lifetime themselves.
 */
export async function getNotificationBannerTimeout(): Promise<number> {
  try {
    const app = getApp()
    const result = await app.GetNotificationBannerTimeout()
    if (typeof result !== 'number') {
      throw new Error('getNotificationBannerTimeout: backend returned non-number data')
    }
    return result
  } catch (err) {
    logger.error('Failed to get notification banner timeout:', err)
    throw err
  }
}

/** See getNotificationBannerTimeout for the accepted values. */
export async function setNotificationBannerTimeout(seconds: number): Promise<void> {
  try {
    const app = getApp()
    await app.SetNotificationBannerTimeout(seconds)
  } catch (err) {
    logger.error('Failed to set notification banner timeout:', err)
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

export async function getModelProfiles(): Promise<ModelProfilesResponse> {
  try {
    const app = getApp()
    const result = await app.GetModelProfiles()
    if (!isModelProfilesResponse(result)) {
      throw new Error('getModelProfiles: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to get Model Profiles profiles:', err)
    throw err
  }
}

/**
 * Toggle the manual-only Model Profiles master switch (config.yaml model_profiles.enabled).
 * The change is persisted and applied immediately. Model Profiles is a
 * first-class feature, independent of the experimental-features switch (which
 * gates the E2S execution mode and the embedded local model's frontend
 * surfaces).
 */
export async function setModelProfilesEnabled(enabled: boolean): Promise<void> {
  try {
    const app = getApp()
    await app.SetModelProfilesEnabled(enabled)
  } catch (err) {
    logger.error('Failed to set Model Profiles enabled:', err)
    throw err
  }
}

/** Duplicate the base profile (empty baseId = generic) under a new name; returns the new profile id. */
export async function createModelProfile(baseId: string, name: string): Promise<string> {
  try {
    const app = getApp()
    const id = await app.CreateModelProfile(baseId, name)
    // The returned id keys the subsequent select/update RPCs — a non-string
    // (or empty) id is schema drift and must fail here, not downstream.
    if (typeof id !== 'string' || id === '') {
      throw new Error('createModelProfile: backend returned an invalid profile id')
    }
    return id
  } catch (err) {
    logger.error('Failed to create Model Profiles profile:', err)
    throw err
  }
}

/** Partially update a CUSTOM profile (name and/or the 25 knob values). */
export async function updateModelProfile(id: string, req: ModelProfileUpdateRequest): Promise<void> {
  try {
    const app = getApp()
    await app.UpdateModelProfile(id, req)
  } catch (err) {
    logger.error('Failed to update Model Profiles profile:', err)
    throw err
  }
}

/** Delete a CUSTOM profile; deleting the active one falls back to generic. */
export async function deleteModelProfile(id: string): Promise<void> {
  try {
    const app = getApp()
    await app.DeleteModelProfile(id)
  } catch (err) {
    logger.error('Failed to delete Model Profiles profile:', err)
    throw err
  }
}

/** Make a catalog profile the active one (persisted to config.yaml). */
export async function selectModelProfile(id: string): Promise<void> {
  try {
    const app = getApp()
    await app.SelectModelProfile(id)
  } catch (err) {
    logger.error('Failed to select Model Profiles profile:', err)
    throw err
  }
}

/**
 * Toggle the experimental-features switch. The backend persists the change; the
 * switch gates the E2S execution mode and the embedded local model's frontend
 * surfaces (Model Profiles and RESEARCH are always available).
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
