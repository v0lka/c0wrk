package embeddedllm

// This file owns the numeric CEILINGS that are shared across the layer
// boundary. The package's own bounds live next to their subject
// (`maxTrainingContext` in memory.go, `DefaultReadyTimeout` in server.go); the
// ones here are exported because `backend/config` validates the persisted
// operator surface against the very same figures, and a bound that exists at
// only one of the two layers is a bound an operator can walk around by
// hand-editing `config.yaml`.
//
// Every ceiling here is an OVERFLOW or absurdity guard, not a tuning opinion:
// it is set far above any real machine so that rejecting it can never refuse a
// legitimate configuration, while still making the arithmetic downstream
// total. Values an operator could plausibly want are bounded by the memory gate
// and the planner, not by these constants.

const (
	// MaxAutoUnloadMinutes bounds `embedded_llm.auto_unload.minutes`.
	//
	// The bound exists because the minutes→nanoseconds conversion is a multiply
	// that wraps: `time.Duration(minutes) * time.Minute` overflows int64 above
	// 153,722,867 minutes, and the wrapped result can be a positive budget of
	// tens of seconds — or, at the residues of the 2^11 divisor, single-digit
	// microseconds — so an "effectively never" value silently inverts into
	// "unload immediately". One year of residency is the largest budget with a
	// meaning an operator would recognise; anything above it is a typo.
	MaxAutoUnloadMinutes = 525600

	// MaxTuningMiB bounds the MiB-valued tuning knobs (`fit_target_mib`,
	// `cache_ram_mib`). It mirrors `backend/config.maxMemoryLimitMiB`: at this
	// figure the MiB→bytes shift still fits an int64 with room to spare, and it
	// is 2 PiB — orders of magnitude past any accelerator or host pool, so the
	// memory gate remains the authority on what actually fits.
	MaxTuningMiB = 1 << 31

	// MaxTuningLayers bounds `embedded_llm.tuning.offload.layers`, the explicit
	// `-ngl` count. The pinned model has a few hundred layers; the ceiling is
	// an absurdity guard that keeps the value representable wherever it is
	// multiplied by a per-layer footprint.
	MaxTuningLayers = 1 << 20

	// MaxTuningParallel bounds `embedded_llm.tuning.parallel`, the `-np` slot
	// count. Slots SPLIT the context (`-c` is divided across them) and each one
	// carries its own KV cache, so a large value is already refused by the
	// memory gate; the ceiling keeps the multiplication in the KV projection
	// total for the values the gate never sees (an unmeasured host, a disabled
	// gate in a test harness).
	MaxTuningParallel = 64

	// MaxTuningHostReserveGiB bounds `embedded_llm.tuning.host_reserve_gib`.
	// The knob is a float64 that the planner converts to an integer MiB count,
	// and a float→int conversion whose value the result type cannot represent
	// is implementation-defined in Go — it saturates on arm64 and yields the
	// negative "indefinite value" on amd64, which would make `host = ram −
	// reserve` enormous and fail the memory gate OPEN. Rejecting above 1 PiB
	// keeps the conversion total on every architecture.
	MaxTuningHostReserveGiB = 1 << 20
)
