// The embedded local model's numeric tuning bounds — the TS mirror of the Go
// validation figures.
//
// Split out of @/api/embeddedTuning so that module stays at DTOs + guards +
// RPC wrappers: the bounds are shared by THREE consumers (the API wrapper's
// local range check, lib/embeddedTuningDisplay's paintable ranges and display
// fallbacks, and the inputs' own `min`/`max` attributes), and every figure here
// carries the backend rationale a reader needs to judge a change. One copy, so
// a control can never offer a value the guard would refuse.
//
// The floors mirror backend/config's `ToTuning` validation; the CEILINGS mirror
// core/embeddedllm/limits.go (each constant names the Go figure it mirrors).
// A pin bump changes the backend figure, not this mirror — the wrapper's refusal
// is only a fast path, the backend stays authoritative.

// --- Floors ---

/** Smallest legal explicit context / fit floor. There is no zero-token context
 *  window. Mirrors config.EmbeddedLLMMinContextTokens. */
export const MIN_CONTEXT_TOKENS = 1

/** The `-np` default and floor (core DefaultParallel: one slot, because -np
 *  also SPLITS the context across slots). */
export const MIN_PARALLEL = 1

// --- Ceilings (mirror core/embeddedllm/limits.go) ---
//
// Every figure below is an OVERFLOW or absurdity guard on the Go side, not a
// tuning opinion: it is set far above any real machine so refusing it can never
// reject a legitimate configuration, while keeping the backend's arithmetic
// total. They are mirrored here (and as `max` on the inputs) so an out-of-range
// entry is refused in the UI with an actionable message instead of round-
// tripping into a Go rejection.

/** The pinned model's own training context — the ceiling for every
 *  context-shaped knob (core/embeddedllm `maxTrainingContext`, 1<<18). */
export const MAX_CONTEXT_TOKENS = 262144

/** Mirrors `embeddedllm.MaxTuningMiB` (1 << 31): the largest MiB value whose
 *  MiB→bytes shift still fits an int64. Bounds `fit_target_mib` and
 *  `cache_ram_mib`. */
export const MAX_TUNING_MIB = 2147483648

/** Mirrors `embeddedllm.MaxTuningLayers` (1 << 20): the absurdity ceiling of
 *  the explicit `-ngl` count (`offload.layers`). */
export const MAX_TUNING_LAYERS = 1048576

/** Mirrors `embeddedllm.MaxTuningParallel` (64): the `-np` slot ceiling. Slots
 *  SPLIT the context and each carries its own KV cache, so the memory gate
 *  refuses anything realistic long before this figure. */
export const MAX_TUNING_PARALLEL = 64

/** Mirrors `embeddedllm.MaxTuningHostReserveGiB` (1 << 20): keeps the planner's
 *  float→int MiB conversion of `host_reserve_gib` total on every architecture —
 *  a conversion whose value the result type cannot represent is
 *  implementation-defined in Go (it saturates on arm64 and yields a negative
 *  "indefinite value" on amd64, which would fail the memory gate OPEN). */
export const MAX_TUNING_HOST_RESERVE_GIB = 1048576

// --- Documented defaults (the fallback an unset knob displays) ---

/** The fit-floor default when unset (core DefaultFitMinContext — deliberately
 *  NOT the runtime's own 4096, which truncates answers on this model). */
export const DEFAULT_FIT_MIN_CONTEXT = 65536

/** The `context.tokens` seed when an Exact context has no count yet.
 *
 *  The figure equals DEFAULT_FIT_MIN_CONTEXT today, and that is deliberate: in
 *  core the SAME `DefaultFitMinContext` constant is the `-fitc` floor AND the
 *  context the memory gate prices when no exact override exists
 *  (`embeddedllm.gateContext`) and the value `shrinkContextToFloor` pins
 *  `Context.Tokens` to. They are still SEPARATE knobs — `fit_min_context` is a
 *  launch-argv floor, `context.tokens` is the requested window — so if core
 *  ever splits the two figures this alias is the line to change, never the seed
 *  site, which must not silently keep following the fit floor. */
export const DEFAULT_CONTEXT_TOKENS = DEFAULT_FIT_MIN_CONTEXT

/** The per-device fit margin the runtime itself uses when unset. */
export const DEFAULT_FIT_TARGET_MIB = 1024

/** The `-np` slot count an unset knob displays. */
export const DEFAULT_PARALLEL = MIN_PARALLEL
