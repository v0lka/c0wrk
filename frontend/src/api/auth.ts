// ChatGPT and Kimi subscription-auth API wrappers. This module validates RPC
// boundary data, opens the system browser and fans global auth transitions out
// to guarded callbacks; OAuth policy and credentials stay behind the RPCs.
// Secrets never cross this boundary: only identity fields, auth state and
// provider-required authorization URLs appear in events, results or logs.

import { getApp, onGlobalEvent, openExternalURL, reportDroppedEvent } from './runtime'
import { logger } from '@/lib/logger'
import {
  isChatGPTAuthEventData,
  isKimiAuthEventData,
  type ChatGPTAuthEventData,
  type KimiAuthEventData,
} from '@/types/events'
import type {
  ChatGPTAuthMode,
  ChatGPTAuthStatusResponse,
  ChatGPTModelPresetResponse,
  ChatGPTSignInResponse,
  KimiAuthMode,
  KimiAuthRegion,
  KimiAuthStatusResponse,
  KimiModelPresetResponse,
  KimiSignInResponse,
  SubscriptionAuthMode,
  SubscriptionAuthStatusResponse,
  SubscriptionModelPresetEntry,
  SubscriptionModelPresetResponse,
  SubscriptionSignInResponse,
} from '@/types/models'

/** Normalize a wire auth-mode value to the shared two-state enum. */
export function normalizeSubscriptionAuthMode(mode: unknown): SubscriptionAuthMode {
  return mode === 'oauth' ? 'oauth' : 'api_key'
}

export function normalizeChatGPTAuthMode(mode: unknown): ChatGPTAuthMode {
  return normalizeSubscriptionAuthMode(mode)
}

function isSubscriptionAuthStatusResponse(v: unknown): v is SubscriptionAuthStatusResponse {
  if (typeof v !== 'object' || v === null) return false
  const o = v as Record<string, unknown>
  if (typeof o.signed_in !== 'boolean') return false
  if (typeof o.in_flight !== 'boolean') return false
  if (typeof o.mode !== 'string') return false
  for (const key of ['email', 'account_id', 'expires_at', 'last_error'] as const) {
    if (key in o && o[key] !== undefined && typeof o[key] !== 'string') return false
  }
  return true
}

function isSubscriptionSignInResponse(v: unknown): v is SubscriptionSignInResponse {
  return (
    typeof v === 'object' &&
    v !== null &&
    typeof (v as Record<string, unknown>).auth_url === 'string' &&
    (v as Record<string, unknown>).auth_url !== ''
  )
}

function isSubscriptionModelPresetEntry(v: unknown): v is SubscriptionModelPresetEntry {
  if (typeof v !== 'object' || v === null) return false
  const o = v as Record<string, unknown>
  if (typeof o.name !== 'string' || o.name === '') return false
  for (const key of ['context_window', 'output_limit'] as const) {
    if (key in o && o[key] !== undefined && typeof o[key] !== 'number') return false
  }
  if ('reasoning' in o && o.reasoning !== undefined && typeof o.reasoning !== 'boolean') return false
  return true
}

function isSubscriptionModelPresetResponse(v: unknown): v is SubscriptionModelPresetResponse {
  return (
    typeof v === 'object' &&
    v !== null &&
    Array.isArray((v as Record<string, unknown>).models) &&
    ((v as Record<string, unknown>).models as unknown[]).every(isSubscriptionModelPresetEntry)
  )
}

/**
 * Snapshot the ChatGPT subscription auth state (GetChatGPTAuthStatus). The
 * backend getter never fails: an unavailable subsystem (e.g. a Linux desktop
 * without a reachable Secret Service) reports the signed-out posture with
 * the construction failure as `last_error`. The wire `mode` is normalized
 * to the enum so consumers never see a third state.
 */
export async function getChatGPTAuthStatus(): Promise<ChatGPTAuthStatusResponse> {
  try {
    const app = getApp()
    const result = await app.GetChatGPTAuthStatus()
    if (!isSubscriptionAuthStatusResponse(result)) {
      throw new Error('getChatGPTAuthStatus: backend returned invalid data')
    }
    return { ...result, mode: normalizeChatGPTAuthMode(result.mode) }
  } catch (err) {
    logger.error('Failed to get ChatGPT auth status:', err)
    throw err
  }
}

/**
 * Start the browser OAuth flow (StartChatGPTSignIn). The RPC returns as soon
 * as the background flow has published the authorization URL — this wrapper
 * also opens it in the system browser (the backend never does), so a caller
 * cannot forget. Later transitions arrive through `chatgpt_auth:state`
 * (subscribe via onChatGPTAuthState); a second concurrent start is refused
 * by the backend with an actionable error.
 */
export async function startChatGPTSignIn(): Promise<ChatGPTSignInResponse> {
  try {
    const app = getApp()
    const result = await app.StartChatGPTSignIn()
    if (!isSubscriptionSignInResponse(result)) {
      throw new Error('startChatGPTSignIn: backend returned invalid data')
    }
    openExternalURL(result.auth_url)
    return result
  } catch (err) {
    logger.error('Failed to start ChatGPT sign-in:', err)
    throw err
  }
}

/**
 * Cancel the in-flight sign-in (CancelChatGPTSignIn). Idempotent server-side;
 * the run closes with the quiet `cancelled` event, not an error.
 */
export async function cancelChatGPTSignIn(): Promise<void> {
  try {
    const app = getApp()
    await app.CancelChatGPTSignIn()
  } catch (err) {
    logger.error('Failed to cancel ChatGPT sign-in:', err)
    throw err
  }
}

/**
 * Clear the persisted OAuth credentials and withdraw the auth seam
 * (SignOutChatGPT). Idempotent server-side.
 */
export async function signOutChatGPT(): Promise<void> {
  try {
    const app = getApp()
    await app.SignOutChatGPT()
  } catch (err) {
    logger.error('Failed to sign out of ChatGPT:', err)
    throw err
  }
}

/**
 * The curated ChatGPT (Codex) model preset offered in oauth mode
 * (GetChatGPTModelPreset): the offline FALLBACK list — registry-known models
 * the subscription serves, ordered most capable first. The authoritative
 * per-subscription list comes from fetchChatGPTModels. A read-only getter
 * that never fails; per-entry metadata is fail-soft (zeros before the async
 * model registry is wired — the name is then the whole entry).
 */
export async function getChatGPTModelPreset(): Promise<ChatGPTModelPresetResponse> {
  try {
    const app = getApp()
    const result = await app.GetChatGPTModelPreset()
    if (!isSubscriptionModelPresetResponse(result)) {
      throw new Error('getChatGPTModelPreset: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to get the ChatGPT model preset:', err)
    throw err
  }
}

/**
 * The models THIS subscription actually serves (FetchChatGPTModels): the
 * ChatGPT Codex backend's live model catalog, filtered to picker-visible
 * entries and ordered by the backend's own priority rank — the same list the
 * Codex CLI's model picker shows. Requires a subscription sign-in; the
 * refusal is actionable when there is none. Entry metadata is fail-soft per
 * model (registry-known slugs carry full metadata, brand-new slugs carry
 * the endpoint's context window).
 */
export async function fetchChatGPTModels(): Promise<ChatGPTModelPresetResponse> {
  try {
    const app = getApp()
    const result = await app.FetchChatGPTModels()
    if (!isSubscriptionModelPresetResponse(result)) {
      throw new Error('fetchChatGPTModels: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to fetch the ChatGPT subscription model list:', err)
    throw err
  }
}

/**
 * Subscribe to the global `chatgpt_auth:state` transitions. The payload is
 * validated at the boundary; a malformed event is dropped and reported (a
 * dropped `success` would otherwise leave the Settings panel stuck in its
 * busy state). Returns an unsubscribe function.
 */
export function onChatGPTAuthState(callback: (data: ChatGPTAuthEventData) => void): () => void {
  return onGlobalEvent('chatgpt_auth:state', (data) => {
    if (!isChatGPTAuthEventData(data)) {
      reportDroppedEvent('chatgpt_auth:state', data)
      return
    }
    callback(data)
  })
}

export function normalizeKimiAuthMode(mode: unknown): KimiAuthMode {
  return normalizeSubscriptionAuthMode(mode)
}

export function normalizeKimiAuthRegion(region: unknown): KimiAuthRegion {
  return region === 'mainland_cn' ? 'mainland_cn' : 'global'
}

export async function getKimiAuthStatus(): Promise<KimiAuthStatusResponse> {
  try {
    const result = await getApp().GetKimiAuthStatus()
    if (!isSubscriptionAuthStatusResponse(result)) {
      throw new Error('getKimiAuthStatus: backend returned invalid data')
    }
    return { ...result, mode: normalizeKimiAuthMode(result.mode) }
  } catch (err) {
    logger.error('Failed to get Kimi auth status:', err)
    throw err
  }
}

export async function startKimiSignIn(): Promise<KimiSignInResponse> {
  try {
    const result = await getApp().StartKimiSignIn()
    if (!isSubscriptionSignInResponse(result)) {
      throw new Error('startKimiSignIn: backend returned invalid data')
    }
    openExternalURL(result.auth_url)
    return result
  } catch (err) {
    logger.error('Failed to start Kimi sign-in:', err)
    throw err
  }
}

export async function cancelKimiSignIn(): Promise<void> {
  try {
    await getApp().CancelKimiSignIn()
  } catch (err) {
    logger.error('Failed to cancel Kimi sign-in:', err)
    throw err
  }
}

export async function signOutKimi(): Promise<void> {
  try {
    await getApp().SignOutKimi()
  } catch (err) {
    logger.error('Failed to sign out of Kimi:', err)
    throw err
  }
}

export async function fetchKimiModels(): Promise<KimiModelPresetResponse> {
  try {
    const result = await getApp().FetchKimiModels()
    if (!isSubscriptionModelPresetResponse(result)) {
      throw new Error('fetchKimiModels: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to fetch the Kimi subscription model list:', err)
    throw err
  }
}

export async function getKimiModelPreset(): Promise<KimiModelPresetResponse> {
  try {
    const result = await getApp().GetKimiModelPreset()
    if (!isSubscriptionModelPresetResponse(result)) {
      throw new Error('getKimiModelPreset: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to get the Kimi model preset:', err)
    throw err
  }
}

export function onKimiAuthState(callback: (data: KimiAuthEventData) => void): () => void {
  return onGlobalEvent('kimi_auth:state', (data) => {
    if (!isKimiAuthEventData(data)) {
      reportDroppedEvent('kimi_auth:state', data)
      return
    }
    callback(data)
  })
}
