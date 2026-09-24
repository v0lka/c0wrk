// Embedded local-model RPC wrappers.
//
// Thin, validating wrappers over the desktop App bindings of
// backend/frontend_api_embedded.go: GetEmbeddedLLMStatus / InstallEmbeddedLLM /
// RemoveEmbeddedLLM / LoadEmbeddedLLM / UnloadEmbeddedLLM /
// SetEmbeddedLLMAutoUnload. Every embedded-LLM surface (the Settings block, the
// status-bar indicator) routes through this module — components never import
// wailsjs directly, so the boundary validation lives here exactly once.
//
// The two global events (`embedded_llm:state`, `embedded_llm:install_progress`)
// are typed in @/types/events; the subscription helpers below validate each
// payload with the matching guard and REPORT a malformed one instead of
// dropping it silently (module convention — see @/api/gitConfigRisk).
//
// Blocking semantics (mirrors specs/contracts/desktop-frontend.md):
//   - InstallEmbeddedLLM runs only the synchronous gates (single-run, bounded
//     hardware probe, the 16 GiB RAM refusal) and returns; the multi-gigabyte
//     download continues in the background and reports through
//     `embedded_llm:install_progress`. A rejection is therefore an actionable
//     REFUSAL, not a failed download.
//   - LoadEmbeddedLLM blocks for the whole weight load (an explicit user action
//     whose outcome the caller needs), so the UI must show progress from the
//     `loading` state rather than treat the pending promise as a hang.

import { getApp, onGlobalEvent, reportDroppedEvent } from './runtime'
import { logger } from '@/lib/logger'
import {
  isEmbeddedLLMInstallProgressData,
  isEmbeddedLLMStateData,
} from '@/types/events'
import type {
  EmbeddedLLMInstallProgressData,
  EmbeddedLLMStateData,
} from '@/types/events'

/** The idle budget an install establishes when the operator never set one.
 *  Mirrors backend `config.EmbeddedLLMDefaultAutoUnloadMinutes` /
 *  `embeddedllm.DefaultAutoUnloadMinutes`; used only as the field's fallback
 *  before the first status arrives — the backend stays authoritative. */
export const DEFAULT_AUTO_UNLOAD_MINUTES = 60

/** Inclusive lower bound of the auto-unload budget. `SetEmbeddedLLMAutoUnload`
 *  refuses anything below it, so the wrapper refuses it locally too instead of
 *  paying a round trip for a guaranteed rejection. */
export const MIN_AUTO_UNLOAD_MINUTES = 1

/** Supervision state of the embedded local model. Mirrors the string values of
 *  core/embeddedllm `State`; kept open (not a union) so a newly added state
 *  degrades to "unknown" instead of failing validation. */
export type EmbeddedLLMState = string

/** The status snapshot. Mirrors backend `EmbeddedLLMStatus`
 *  (frontend/wailsjs/go/models.ts) field for field — every field is always
 *  present in the DTO (no omitempty), so the frontend never distinguishes
 *  "absent" from "zero". */
export interface EmbeddedLLMStatus {
  /** not_installed | installed | loading | loaded | unloading | error. */
  readonly state: EmbeddedLLMState
  /** The runtime and the weights are on disk and verified. Does NOT imply
   *  resident — `loaded` does. */
  readonly installed: boolean
  /** A background install run is in flight. */
  readonly installing: boolean
  /** A weight load is in progress (process up, /v1/models not answered). */
  readonly loading: boolean
  /** A model is serving (a non-empty /v1/models answer). */
  readonly loaded: boolean
  /** The ternary quantization on disk ("PQ2_0" | "PTQ1_0"), "" when not
   *  installed. INFORMATIONAL — the effective packing the resolver chose. */
  readonly packing: string
  /** The accelerator the runtime was provisioned for ("metal", "cuda-12.4",
   *  …, "cpu"), "" when not installed. */
  readonly backend: string
  /** The persisted loopback port (0 when nothing is installed). */
  readonly port: number
  /** The RAM-tiered context frozen in the manifest (0 when not installed). */
  readonly context_size: number
  /** The resolved idle-timer master switch. */
  readonly auto_unload_enabled: boolean
  /** The resolved idle budget in minutes. */
  readonly auto_unload_minutes: number
  /** Idle budget left before the process is stopped; 0 when no timer is
   *  armed. */
  readonly idle_remaining_seconds: number
  /** The OpenAI-compatible endpoint derived from the port ("" when nothing is
   *  installed). */
  readonly base_url: string
  /** The composite provider/model id the router exposes
   *  ("embedded/Bonsai 2 27B"). */
  readonly model_id: string
  /** The bare model name of the generated provider record. */
  readonly model_name: string
  /** The pinned fork release the runtime came from. */
  readonly runtime_version: string
  /** RFC 3339 install timestamp. */
  readonly installed_at: string
  /** Absolute path of the GGUF weights. */
  readonly model_file: string
  /** OS process id of the supervised server (0 when not running). */
  readonly pid: number
  /** Human-readable cause: the supervisor's message while state is "error",
   *  otherwise the last failed install or removal. */
  readonly error: string
  /** Whether the subsystem could be constructed at all (false only before
   *  startup, when the agent directory is unset). */
  readonly available: boolean
}

/** Guard for a `GetEmbeddedLLMStatus` response. Field presence and types only —
 *  the values of `state`, `packing` and `backend` are deliberately NOT
 *  enumerated, so a newly pinned backend or a new supervision state cannot make
 *  an otherwise healthy response fail validation (the same reasoning as the
 *  `embedded_llm:state` event guard). */
export function isEmbeddedLLMStatus(d: unknown): d is EmbeddedLLMStatus {
  if (typeof d !== 'object' || d === null) return false
  const o = d as Record<string, unknown>
  return (
    typeof o.state === 'string' &&
    typeof o.installed === 'boolean' &&
    typeof o.installing === 'boolean' &&
    typeof o.loading === 'boolean' &&
    typeof o.loaded === 'boolean' &&
    typeof o.packing === 'string' &&
    typeof o.backend === 'string' &&
    typeof o.port === 'number' &&
    typeof o.context_size === 'number' &&
    typeof o.auto_unload_enabled === 'boolean' &&
    typeof o.auto_unload_minutes === 'number' &&
    typeof o.idle_remaining_seconds === 'number' &&
    typeof o.base_url === 'string' &&
    typeof o.model_id === 'string' &&
    typeof o.model_name === 'string' &&
    typeof o.runtime_version === 'string' &&
    typeof o.installed_at === 'string' &&
    typeof o.model_file === 'string' &&
    typeof o.pid === 'number' &&
    typeof o.error === 'string' &&
    typeof o.available === 'boolean'
  )
}

/** Read the supervision state plus the install record. A read-only getter, so
 *  the backend returns no error: an unconstructable subsystem reports
 *  `available: false` with the not-installed state. Performs no network I/O and
 *  no hardware probe. Throws only when the bindings are absent (dev-frontend,
 *  vitest) or the response fails validation (backend schema drift). */
export async function getEmbeddedLLMStatus(): Promise<EmbeddedLLMStatus> {
  const app = getApp()
  const result = await app.GetEmbeddedLLMStatus()
  if (!isEmbeddedLLMStatus(result)) {
    logger.error('getEmbeddedLLMStatus: unexpected response shape', result)
    throw new Error('GetEmbeddedLLMStatus returned an invalid status payload')
  }
  return result
}

/** Provision the pinned runtime and weights. Runs ONLY the synchronous gates and
 *  then returns: a rejection is an actionable refusal (an install already in
 *  flight, an unreadable hardware probe, the 16 GiB RAM gate) and means NOT ONE
 *  BYTE was downloaded. A started run reports through
 *  `embedded_llm:install_progress`, its outcome through `embedded_llm:state` +
 *  `config:updated`, and a BACKGROUND failure additionally through a
 *  `runtime_error` toast (`embedded_llm_install_failed`). Never loads the
 *  model. */
export async function installEmbeddedLLM(): Promise<void> {
  const app = getApp()
  await app.InstallEmbeddedLLM()
}

/** Stop a running server, delete both trees and the manifest, clear the
 *  generated provider record and migrate `llm.default_model` off the embedded
 *  composite. Blocking; refused while an install is in flight. Irreversible —
 *  the multi-gigabyte weights have to be downloaded again. */
export async function removeEmbeddedLLM(): Promise<void> {
  const app = getApp()
  await app.RemoveEmbeddedLLM()
}

/** Start the server and BLOCK until the model can answer (a non-empty
 *  `/v1/models` list) — a load of a 6–7 GiB weight file takes minutes, so the
 *  pending promise is expected, not a hang. Idempotent and single-instance;
 *  refused while an install runs and actionable when nothing is installed. */
export async function loadEmbeddedLLM(): Promise<void> {
  const app = getApp()
  await app.LoadEmbeddedLLM()
}

/** Stop the process, returning its RAM/VRAM; the bytes stay on disk, so the
 *  state afterwards is `installed`. Blocking and idempotent. */
export async function unloadEmbeddedLLM(): Promise<void> {
  const app = getApp()
  await app.UnloadEmbeddedLLM()
}

/** Persist `embedded_llm.auto_unload` and apply it to the supervisor
 *  immediately. The setting is an operator preference, not install state: it
 *  survives a removal. `minutes` below MIN_AUTO_UNLOAD_MINUTES is refused
 *  locally (the backend refuses it too) without touching the config. */
export async function setEmbeddedLLMAutoUnload(enabled: boolean, minutes: number): Promise<void> {
  if (!Number.isInteger(minutes) || minutes < MIN_AUTO_UNLOAD_MINUTES) {
    throw new Error(
      `The auto-unload budget must be a whole number of minutes ≥ ${MIN_AUTO_UNLOAD_MINUTES} (got ${minutes})`,
    )
  }
  const app = getApp()
  await app.SetEmbeddedLLMAutoUnload(enabled, minutes)
}

// --- Typed event subscriptions ---
//
// Both events are global (bare names, not session-scoped). Each helper
// validates the payload with its guard from @/types/events and reports a
// malformed emission, so a dropped event never disappears silently.

/** Subscribe to `embedded_llm:state` — every observable supervision transition,
 *  the startup snapshot, and the completion of an install / removal /
 *  auto-unload change. Returns an unsubscribe function. */
export function onEmbeddedLLMState(cb: (data: EmbeddedLLMStateData) => void): () => void {
  return onGlobalEvent('embedded_llm:state', (data) => {
    if (!data || !isEmbeddedLLMStateData(data)) {
      reportDroppedEvent('embedded_llm:state', data)
      return
    }
    cb(data)
  })
}

/** Subscribe to `embedded_llm:install_progress` — one component's one stage of
 *  a background install. Every artifact reports its OWN bytes, so the payloads
 *  are never aggregated by the transport. Returns an unsubscribe function. */
export function onEmbeddedLLMInstallProgress(
  cb: (data: EmbeddedLLMInstallProgressData) => void,
): () => void {
  return onGlobalEvent('embedded_llm:install_progress', (data) => {
    if (!data || !isEmbeddedLLMInstallProgressData(data)) {
      reportDroppedEvent('embedded_llm:install_progress', data)
      return
    }
    cb(data)
  })
}
