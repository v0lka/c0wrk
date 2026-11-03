// Shared `EmbeddedLLMTuning` fixtures for tests.
//
// The all-unset override surface and the fold the backend performs on a patch
// are needed by all three embedded-LLM tuning suites, and the fold is what makes
// a committed patch round-trip through the mocked re-read exactly as the backend
// would. One definition here, re-exported by ./tuningTestHarness for the two
// section suites.

import type { EmbeddedLLMTuning, EmbeddedLLMTuningPatch } from '@/api/embeddedTuning'

/** The all-unset override surface — every knob "the planner decides". */
export function makeTuning(overrides: Partial<EmbeddedLLMTuning> = {}): EmbeddedLLMTuning {
  return {
    context: { mode: null, tokens: null },
    kv_cache_type: null,
    offload: { mode: null, layers: null },
    fit: null,
    fit_target_mib: null,
    fit_min_context: null,
    kv_offload: null,
    mmproj_offload: null,
    packing: null,
    parallel: null,
    cache_ram_mib: null,
    host_reserve_gib: null,
    devices: null,
    split_mode: null,
    ...overrides,
  }
}

/** The fold the backend performs (`applyEmbeddedTuningRequest`): nil keeps,
 *  present replaces — `devices` wholesale, entries trimmed, an empty array
 *  clearing to null so cleared and unset are one stored value — `split_mode`
 *  verbatim, `reset` clears. The fold is exhaustive: a patch key with no
 *  branch throws, so a future DTO field cannot be silently dropped. Works on
 *  a mutable record copy — the DTO mirror is deeply readonly. */
export function applyPatch(
  t: EmbeddedLLMTuning,
  patch: EmbeddedLLMTuningPatch,
): EmbeddedLLMTuning {
  const scalars = [
    'fit',
    'fit_target_mib',
    'fit_min_context',
    'kv_offload',
    'mmproj_offload',
    'packing',
    'parallel',
    'cache_ram_mib',
    'host_reserve_gib',
  ] as const
  const folded = new Set<string>([
    ...scalars,
    'context',
    'kv_cache_type',
    'offload',
    'devices',
    'split_mode',
    'reset',
  ])
  for (const key of Object.keys(patch)) {
    if (!folded.has(key)) throw new Error(`applyPatch: no fold branch for patch key "${key}"`)
  }
  const next = {
    ...t,
    context: { ...t.context },
    offload: { ...t.offload },
  } as unknown as Record<string, unknown>
  if (patch.context) next.context = { ...patch.context }
  if (patch.kv_cache_type !== undefined) next.kv_cache_type = patch.kv_cache_type
  if (patch.offload) next.offload = { ...patch.offload }
  for (const key of scalars) {
    if (patch[key] !== undefined) next[key] = patch[key]
  }
  if (patch.devices !== undefined) {
    next.devices = patch.devices.length === 0 ? null : patch.devices.map((name) => name.trim())
  }
  if (patch.split_mode !== undefined) next.split_mode = patch.split_mode
  if (patch.reset) {
    for (const knob of patch.reset) {
      if (knob === 'context') next.context = { mode: null, tokens: null }
      else if (knob === 'offload') next.offload = { mode: null, layers: null }
      else next[knob] = null
    }
  }
  return next as unknown as EmbeddedLLMTuning
}
