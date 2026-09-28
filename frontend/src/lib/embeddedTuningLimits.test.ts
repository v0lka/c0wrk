// @vitest-environment node
//
// lib/embeddedTuningLimits — the TS mirror of the backend's numeric tuning
// bounds. Every figure that HAS a Go counterpart is extracted from the Go
// source and compared against the TS one, so a bump on either side shows up
// here instead of drifting silently: the ceilings from
// core/embeddedllm/limits.go, the model's own training context from memory.go,
// the planner defaults from plan.go, the context floor from
// backend/config/config.go. The two figures with no Go constant are asserted as
// literals, each with the reason stated at its assertion.
//
// Reading the sources with `fs` follows the project's other cross-boundary
// invariant guards (src/test/zoomViewportInvariant.test.ts,
// src/test/customScrollbarInvariant.test.ts): resolve the paths from this
// file's own location, and FAIL LOUDLY when an extraction stops matching — a
// scan that silently matches nothing is worse than no scan, so `readConstant`
// throws instead of returning undefined.

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import {
  DEFAULT_CONTEXT_TOKENS,
  DEFAULT_FIT_MIN_CONTEXT,
  DEFAULT_FIT_TARGET_MIB,
  DEFAULT_PARALLEL,
  MAX_CONTEXT_TOKENS,
  MAX_TUNING_HOST_RESERVE_GIB,
  MAX_TUNING_LAYERS,
  MAX_TUNING_MIB,
  MAX_TUNING_PARALLEL,
  MIN_CONTEXT_TOKENS,
  MIN_PARALLEL,
} from './embeddedTuningLimits'

// This file lives at <repo>/frontend/src/lib/, so three levels up is the repo
// root — the Go sources it pins against ship in the same checkout.
const REPO_ROOT = fileURLToPath(new URL('../../..', import.meta.url))

const LIMITS_GO = join(REPO_ROOT, 'core/embeddedllm/limits.go')
const MEMORY_GO = join(REPO_ROOT, 'core/embeddedllm/memory.go')
const PLAN_GO = join(REPO_ROOT, 'core/embeddedllm/plan.go')
const CONFIG_GO = join(REPO_ROOT, 'backend/config/config.go')
// The auto-unload ceiling's mirror lives in @/api/embedded, a browser-facing
// module this node-env suite must not import (it pulls the Wails runtime
// wrapper in). Its declaration is read instead: the same drift question,
// answered from source on both sides.
const EMBEDDED_TS = fileURLToPath(new URL('../api/embedded.ts', import.meta.url))

/** One numeric declaration — `const Name = 64`, `export const Name = 64`, or a
 *  bare `Name = 1 << 31` inside a `const ( … )` block. The line is anchored at
 *  both ends so a qualified USE (`embeddedllm.MaxAutoUnloadMinutes` in a format
 *  string) is never mistaken for the declaration. */
function declarationOf(name: string): RegExp {
  return new RegExp(
    `^[ \\t]*(?:export[ \\t]+)?(?:const[ \\t]+)?${name}[ \\t]*=[ \\t]*(\\d+)(?:[ \\t]*<<[ \\t]*(\\d+))?[ \\t]*$`,
    'm',
  )
}

/** Extract `name` from `file` and EVALUATE it: the shift-literal forms
 *  (`1 << 31`, `1 << 20`, `1 << 18`) are computed, never string-compared, so
 *  the figure this suite compares is the number Go compiles. Throws when the
 *  declaration is gone or was re-shaped. */
function readConstant(file: string, name: string): number {
  const match = declarationOf(name).exec(readFileSync(file, 'utf8'))
  if (!match) throw new Error(`${name} no longer matches a declaration in ${file}`)
  const base = Number(match[1])
  return match[2] === undefined ? base : base * 2 ** Number(match[2])
}

describe('the extractor reads the Go sources it claims to pin', () => {
  it('evaluates a shift literal to the number Go compiles', () => {
    expect(readConstant(LIMITS_GO, 'MaxTuningMiB')).toBe(2147483648)
    expect(readConstant(MEMORY_GO, 'maxTrainingContext')).toBe(262144)
  })

  it('reads a plain literal from a single-const declaration', () => {
    expect(readConstant(PLAN_GO, 'DefaultFitMinContext')).toBe(65536)
    expect(readConstant(CONFIG_GO, 'EmbeddedLLMMinContextTokens')).toBe(1)
  })

  it('throws when a declaration stops matching, instead of passing silently', () => {
    expect(() => readConstant(LIMITS_GO, 'MaxTuningNothing')).toThrow(/no longer matches/)
    expect(() => readConstant(LIMITS_GO, 'maxTrainingContext')).toThrow(/no longer matches/)
  })

  it('never mistakes a qualified use for the declaration', () => {
    // config.go REFERS to `embeddedllm.MaxAutoUnloadMinutes` while validating
    // the persisted budget; that line must not satisfy the extraction.
    expect(() => readConstant(CONFIG_GO, 'MaxAutoUnloadMinutes')).toThrow(/no longer matches/)
  })
})

describe('the mirrored ceilings match core/embeddedllm/limits.go', () => {
  it('mirrors MaxTuningMiB (1 << 31)', () => {
    expect(MAX_TUNING_MIB).toBe(readConstant(LIMITS_GO, 'MaxTuningMiB'))
    expect(MAX_TUNING_MIB).toBe(2 ** 31)
  })

  it('mirrors MaxTuningLayers (1 << 20)', () => {
    expect(MAX_TUNING_LAYERS).toBe(readConstant(LIMITS_GO, 'MaxTuningLayers'))
    expect(MAX_TUNING_LAYERS).toBe(2 ** 20)
  })

  it('mirrors MaxTuningParallel (64)', () => {
    expect(MAX_TUNING_PARALLEL).toBe(readConstant(LIMITS_GO, 'MaxTuningParallel'))
    expect(MAX_TUNING_PARALLEL).toBe(64)
  })

  it('mirrors MaxTuningHostReserveGiB (1 << 20)', () => {
    expect(MAX_TUNING_HOST_RESERVE_GIB).toBe(readConstant(LIMITS_GO, 'MaxTuningHostReserveGiB'))
    expect(MAX_TUNING_HOST_RESERVE_GIB).toBe(2 ** 20)
  })

  it('mirrors maxTrainingContext (1 << 18) as the context ceiling', () => {
    expect(MAX_CONTEXT_TOKENS).toBe(readConstant(MEMORY_GO, 'maxTrainingContext'))
    expect(MAX_CONTEXT_TOKENS).toBe(2 ** 18)
  })

  it('mirrors MaxAutoUnloadMinutes on both sides of the boundary', () => {
    // The TS mirror lives in @/api/embedded, so BOTH sides are read from source:
    // bumping either one without the other fails here.
    expect(readConstant(EMBEDDED_TS, 'MAX_AUTO_UNLOAD_MINUTES')).toBe(
      readConstant(LIMITS_GO, 'MaxAutoUnloadMinutes'),
    )
    expect(readConstant(EMBEDDED_TS, 'MAX_AUTO_UNLOAD_MINUTES')).toBe(525600)
  })
})

describe('the mirrored floors match config.ToTuning', () => {
  it('never offers a zero-token context window', () => {
    expect(MIN_CONTEXT_TOKENS).toBe(readConstant(CONFIG_GO, 'EmbeddedLLMMinContextTokens'))
    expect(MIN_CONTEXT_TOKENS).toBe(1)
  })

  it('mirrors DefaultParallel as the -np floor', () => {
    expect(MIN_PARALLEL).toBe(readConstant(PLAN_GO, 'DefaultParallel'))
    expect(MIN_PARALLEL).toBe(1)
    expect(DEFAULT_PARALLEL).toBe(MIN_PARALLEL)
  })
})

// `context.tokens` and `fit_min_context` are SEPARATE knobs, but core derives
// both fallbacks from the one `DefaultFitMinContext` constant: `gateContext`
// prices that floor when no exact override exists, and `shrinkContextToFloor`
// pins `Context.Tokens` to it. The DEFAULT_CONTEXT_TOKENS alias makes the shared
// origin explicit, so a future split in core is a one-line change here rather
// than a silent drift at the seed site in useEmbeddedLLMTuning.
describe('the context-token seed and the fit floor', () => {
  it('mirrors DefaultFitMinContext (65536)', () => {
    expect(DEFAULT_FIT_MIN_CONTEXT).toBe(readConstant(PLAN_GO, 'DefaultFitMinContext'))
    expect(DEFAULT_FIT_MIN_CONTEXT).toBe(2 ** 16)
  })

  it('seeds context.tokens from the same figure today', () => {
    expect(DEFAULT_CONTEXT_TOKENS).toBe(DEFAULT_FIT_MIN_CONTEXT)
  })

  it('mirrors the runtime’s own per-device fit margin', () => {
    // The one figure with NO Go constant to read: `-fitt`'s default belongs to
    // the upstream runtime, and c0wrk only DOCUMENTS it (plan.go's
    // `FitTargetMiB` comments: "keep the runtime's own default of 1024 MiB").
    // Asserted as a literal so the fallback the UI paints stays quotable.
    expect(DEFAULT_FIT_TARGET_MIB).toBe(1024)
  })
})

describe('every bound is usable as an inclusive range', () => {
  it('keeps each floor at or below its ceiling', () => {
    expect(MIN_CONTEXT_TOKENS).toBeLessThanOrEqual(MAX_CONTEXT_TOKENS)
    expect(MIN_PARALLEL).toBeLessThanOrEqual(MAX_TUNING_PARALLEL)
    expect(0).toBeLessThanOrEqual(MAX_TUNING_MIB)
    expect(0).toBeLessThanOrEqual(MAX_TUNING_LAYERS)
    expect(0).toBeLessThanOrEqual(MAX_TUNING_HOST_RESERVE_GIB)
  })

  it('keeps every figure a safe integer the Go side can parse back', () => {
    for (const figure of [
      MIN_CONTEXT_TOKENS,
      MAX_CONTEXT_TOKENS,
      MIN_PARALLEL,
      MAX_TUNING_MIB,
      MAX_TUNING_LAYERS,
      MAX_TUNING_PARALLEL,
      MAX_TUNING_HOST_RESERVE_GIB,
      DEFAULT_FIT_MIN_CONTEXT,
      DEFAULT_CONTEXT_TOKENS,
      DEFAULT_FIT_TARGET_MIB,
      DEFAULT_PARALLEL,
    ]) {
      expect(Number.isSafeInteger(figure)).toBe(true)
    }
  })
})
