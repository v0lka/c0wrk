// Embedded local-model RPC wrappers.
//
// Thin, validating wrappers over the desktop App bindings of
// backend/frontend_api_embedded.go: GetEmbeddedLLMStatus / InstallEmbeddedLLM /
// CancelEmbeddedLLMInstall / RemoveEmbeddedLLM / LoadEmbeddedLLM /
// UnloadEmbeddedLLM / SetEmbeddedLLMAutoUnload. Every embedded-LLM surface (the Settings block, the
// status-bar indicator) routes through this module — components never import
// wailsjs directly, so the boundary validation lives here exactly once. The
// TUNING RPCs (Get/SetEmbeddedLLMTuning) and the status snapshot's
// measured-topology additions live in the sibling @/api/embeddedTuning.
//
// The two global events (`embedded_llm:state`, `embedded_llm:install_progress`)
// are typed in @/types/events; the subscription helpers below validate each
// payload with the matching guard and REPORT a malformed one instead of
// dropping it silently (module convention — see @/api/gitConfigRisk).
//
// Blocking semantics (mirrors specs/contracts/desktop-frontend.md):
//   - InstallEmbeddedLLM runs only the synchronous gates (single-run, bounded
//     hardware probe, and the COMBINED memory gate — `embeddedllm.
//     CheckMemoryBudget` prices BOTH memory pools, the accelerator's and system
//     RAM; there is no flat RAM floor) and returns; the multi-gigabyte
//     download continues in the background and reports through
//     `embedded_llm:install_progress`. A rejection is therefore an actionable
//     REFUSAL, not a failed download.
//   - LoadEmbeddedLLM blocks for the whole weight load (an explicit user action
//     whose outcome the caller needs), so the UI must show progress from the
//     `loading` state rather than treat the pending promise as a hang.

import { getApp, onGlobalEvent, reportDroppedEvent } from './runtime'
import { isEmbeddedLLMStatusExtras, type EmbeddedLLMStatusExtras } from './embeddedTuning'
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

/** Inclusive upper bound of the auto-unload budget (one year of residency).
 *  Mirrors `embeddedllm.MaxAutoUnloadMinutes` in core/embeddedllm/limits.go,
 *  which the backend refuses anything above: the minutes→nanoseconds multiply
 *  overflows int64 above 153,722,867 minutes, and the wrapped result can be a
 *  budget of tens of SECONDS — or, at the residues of the 2^11 divisor, single
 *  microseconds — so an "effectively never" value silently inverts into
 *  "unload immediately". The wrapper refuses it locally, with an actionable
 *  message, instead of persisting a budget that means the opposite of what the
 *  field says. */
export const MAX_AUTO_UNLOAD_MINUTES = 525600

/** Supervision state of the embedded local model. Mirrors the string values of
 *  core/embeddedllm `State`; kept open (not a union) so a newly added state
 *  degrades to "unknown" instead of failing validation. */
export type EmbeddedLLMState = string

/** The scope of a removal: which embedded-LLM bytes are deleted. Mirrors
 *  core/embeddedllm `RemoveScope`. "all" is the historical full removal; the
 *  partial scopes clear the install record and the config under every value,
 *  so what they spare is a verified cache a reinstall re-uses — not a
 *  half-registered install. */
export type EmbeddedLLMRemoveScope = 'all' | 'runtime' | 'weights' | 'projection'

/** One backend-compatibility decision of an install (mirrors backend
 *  `EmbeddedLLMGuard`, i.e. core/embeddedllm `GuardDecision`): the documented
 *  upstream failure the install was planned under, what c0wrk did about it
 *  (`applied`) and the human sentence describing both. The string enum fields
 *  are the core values verbatim and deliberately not enumerated, so a new
 *  guard reason cannot break validation. */
export interface EmbeddedLLMGuard {
  readonly guard: string
  readonly action: string
  readonly reason: string
  readonly severity: string
  /** Upstream citation, "<repo>#<number>" (e.g.
   *  "PrismML-Eng/llama.cpp#222"). */
  readonly issue: string
  /** Whether the install actually changed because of this decision. */
  readonly applied: boolean
  /** The substituted backend, "" unless action is prefer_backend. */
  readonly backend: string
  /** The substituted packing, "" unless action is prefer_packing. */
  readonly packing: string
  /** The user-facing sentence: what is documented, what c0wrk did or could
   *  not do, and what the upstream workaround is. */
  readonly guidance: string
}

function isEmbeddedLLMGuard(d: unknown): d is EmbeddedLLMGuard {
  if (typeof d !== 'object' || d === null) return false
  const o = d as Record<string, unknown>
  return (
    typeof o.guard === 'string' &&
    typeof o.action === 'string' &&
    typeof o.reason === 'string' &&
    typeof o.severity === 'string' &&
    typeof o.issue === 'string' &&
    typeof o.applied === 'boolean' &&
    typeof o.backend === 'string' &&
    typeof o.packing === 'string' &&
    typeof o.guidance === 'string'
  )
}

/** The status snapshot. Mirrors backend `EmbeddedLLMStatus`
 *  (frontend/wailsjs/go/models.ts) field for field — every field is always
 *  present in the DTO (no omitempty), so the frontend never distinguishes
 *  "absent" from "zero". The measured-topology / effective-plan / reload
 *  fields (the T10 additive block) come from @/api/embeddedTuning via
 *  `extends`, keeping this module at its single lifecycle concern. */
export interface EmbeddedLLMStatus extends EmbeddedLLMStatusExtras {
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
  /** The fit-contract finding of the last failed launch: the fork's
   *  "failed to fit params to free device memory" complaint, scanned from the
   *  dead run's output tail. Absent while no launch has failed that way (the
   *  backend marks it omitempty); a launch that becomes ready clears it. */
  readonly fit_warning?: string
  /** Human-readable cause of the SUPERVISION state: the supervisor's message
   *  while state is "error" (a failed load, a crashed process). Empty
   *  otherwise — install failures live in `install_error`, a Settings-only
   *  surface the status bar deliberately does not render. */
  readonly error: string
  /** Operator-friendly cause of the last FAILED install run (fatal download
   *  failures only — resumable ones are retried silently in the backend).
   *  Empty when no install has failed since the last successful/cancelled
   *  run. */
  readonly install_error: string
  /** Whether the subsystem could be constructed at all (false only before
   *  startup, when the agent directory is unset). */
  readonly available: boolean
  /** The backend-compatibility decisions this install was planned under,
   *  empty on a machine no documented failure covers (the healthy common
   *  case). ALWAYS an array — never undefined — so a renderer has one code
   *  path. This is how a degraded install (an unapplied guard: "it works, but
   *  not the way you think") becomes visible in Settings. */
  readonly guards: readonly EmbeddedLLMGuard[]
  /** Which embedded-LLM artifacts are on disk right now. While `installed`
   *  these are simply the install's own bytes; their purpose is the
   *  not-installed state, where they describe what a scoped removal left
   *  behind — a cache the next install re-verifies without re-downloading, or
   *  residue a further removal can reclaim. */
  readonly leftover_runtime: boolean
  readonly leftover_weights: boolean
  readonly leftover_projection: boolean
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
    // Optional omitempty fields stay valid when absent; a present one must be
    // a string — fit_warning is rendered as a React child (title + body) in
    // the install record.
    (o.fit_warning === undefined || typeof o.fit_warning === 'string') &&
    typeof o.error === 'string' &&
    typeof o.install_error === 'string' &&
    typeof o.available === 'boolean' &&
    Array.isArray(o.guards) &&
    o.guards.every(isEmbeddedLLMGuard) &&
    typeof o.leftover_runtime === 'boolean' &&
    typeof o.leftover_weights === 'boolean' &&
    typeof o.leftover_projection === 'boolean' &&
    isEmbeddedLLMStatusExtras(o)
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
 *  flight, an unreadable hardware probe, or the combined accelerator+RAM
 *  memory-budget gate) and means NOT ONE BYTE was downloaded. A started run
 *  reports through
 *  `embedded_llm:install_progress`, its outcome through `embedded_llm:state` +
 *  `config:updated`, and a BACKGROUND failure additionally through a
 *  `runtime_error` toast (`embedded_llm_install_failed`). Never loads the
 *  model. */
export async function installEmbeddedLLM(): Promise<void> {
  const app = getApp()
  await app.InstallEmbeddedLLM()
}

/** Ask the in-flight background install to stop. IDEMPOTENT: with no install
 *  running it is a success no-op — the outcome the operator wants already
 *  holds, so a late double click must not surface as an error. The stop is
 *  COOPERATIVE and asynchronous: the RPC resolves once the request is
 *  delivered, not once the run has unwound — the run itself keeps the
 *  operation gate held until its cleanup finishes, then reports the quiet
 *  outcome through `embedded_llm:state` (no `runtime_error` toast, no recorded
 *  error: the click IS the report). Core keeps the partial bytes as the resume
 *  point, so a retry continues the download instead of restarting it. */
export async function cancelEmbeddedLLMInstall(): Promise<void> {
  const app = getApp()
  await app.CancelEmbeddedLLMInstall()
}

/** Stop a running server and remove the parts of the installation `scope`
 *  names (mirrors core/embeddedllm `RemoveScope`; the default "all" is the
 *  historical full removal). Under EVERY scope the manifest and the generated
 *  provider record are cleared and `llm.default_model` migrates off the
 *  embedded composite — a partial removal leaves a CACHE, never a
 *  half-registered install, so a runtime-scoped removal followed by an
 *  install re-verifies the surviving weights instead of re-downloading the
 *  multi-gigabyte model. Blocking; refused while an install is in flight. */
export async function removeEmbeddedLLM(scope: EmbeddedLLMRemoveScope = 'all'): Promise<void> {
  const app = getApp()
  await app.RemoveEmbeddedLLM(scope)
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
 *  survives a removal. `minutes` outside
 *  MIN_AUTO_UNLOAD_MINUTES..MAX_AUTO_UNLOAD_MINUTES (or not a whole number) is
 *  refused locally — the backend refuses the same range — without touching the
 *  config. */
export async function setEmbeddedLLMAutoUnload(enabled: boolean, minutes: number): Promise<void> {
  if (
    !Number.isInteger(minutes) ||
    minutes < MIN_AUTO_UNLOAD_MINUTES ||
    minutes > MAX_AUTO_UNLOAD_MINUTES
  ) {
    throw new Error(
      `The auto-unload budget must be a whole number of minutes between ${MIN_AUTO_UNLOAD_MINUTES} and ${MAX_AUTO_UNLOAD_MINUTES} (got ${minutes})`,
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
