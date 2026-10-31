package embeddedllm

// This file owns the numeric CEILINGS shared across the layer boundary: they
// are exported because backend/config validates the persisted operator
// surface against the very same figures — a bound at only one of the two
// layers is one an operator can walk around by hand-editing config.yaml.
// (The package's own bounds live next to their subject.) Every ceiling is an
// OVERFLOW or absurdity guard, not a tuning opinion: far above any real
// machine, so a rejection never refuses a legitimate configuration while the
// arithmetic downstream stays total. See specs/domains/embedded-llm.md
// (Key Files, limits.go).

const (
	// MaxAutoUnloadMinutes bounds `embedded_llm.auto_unload.minutes`: the
	// minutes→nanoseconds multiply wraps int64 far below it, turning an
	// "effectively never" into a tiny positive budget — "unload immediately".
	// One year of residency is the largest meaningful budget.
	MaxAutoUnloadMinutes = 525600

	// MaxTuningMiB bounds the MiB-valued tuning knobs (`fit_target_mib`,
	// `cache_ram_mib`), mirroring backend/config.maxMemoryLimitMiB: the
	// MiB→bytes shift still fits an int64 with room to spare.
	MaxTuningMiB = 1 << 31

	// MaxTuningLayers bounds `embedded_llm.tuning.offload.layers` (`-ngl`):
	// an absurdity guard keeping the value representable under multiplication.
	MaxTuningLayers = 1 << 20

	// MaxTuningParallel bounds `embedded_llm.tuning.parallel` (`-np`): slots
	// split the context, each with its own KV cache; the gate refuses large
	// values, the ceiling keeps the KV projection total elsewhere.
	MaxTuningParallel = 64

	// MaxTuningCtxCheckpoints bounds `embedded_llm.tuning.ctx_checkpoints`:
	// each per-slot KV snapshot is a memory multiplier the gate does not
	// model; the ceiling keeps the multiplication total (runtime default 32).
	MaxTuningCtxCheckpoints = 1 << 20

	// MaxTuningHostReserveGiB bounds `embedded_llm.tuning.host_reserve_gib`:
	// the float64→int conversion is implementation-defined when unrepresentable
	// (saturates on arm64, negative "indefinite" on amd64), which would make
	// `host = ram − reserve` enormous and fail the memory gate OPEN. Rejecting
	// above 1 PiB keeps the conversion total everywhere.
	MaxTuningHostReserveGiB = 1 << 20
)
