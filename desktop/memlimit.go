package desktop

import (
	"log/slog"
	"runtime/debug"
)

// GC memory soft limit for the desktop app.
//
// Problem being solved: with the default GOGC=100 the Go runtime lets the
// heap grow to ~2x the live set before collecting, so a full project
// re-index (chunk buffers + embedding batches + chromem/bleve documents)
// leaves the resident set at twice its steady state, and the scavenger
// returns the excess to the OS only lazily. Two levers fix this:
//
//  1. debug.SetMemoryLimit — a GOMEMLIMIT-style SOFT limit (a GC target,
//     not a hard cap: the heap may exceed it under live-set pressure, the
//     GC just runs more often) — applied once at startup, after config load
//     and before background indexing starts (see startup.go).
//  2. debug.FreeOSMemory — forced GC + scavenge after the transient spikes
//     (full indexing passes, park evictions, the content-less migration; see
//     core/vectorindex's single freeOSMemory seam).

const (
	// memLimitAutoFractionDenom is the denominator of the share of physical
	// RAM used by AUTO mode: a value of 2 means 1/2 = 50% of RAM.
	memLimitAutoFractionDenom = 2 // 1/2 = 50%

	// memLimitAutoMinBytes / memLimitAutoMaxBytes clamp the AUTO value:
	// 2 GiB floors tiny machines (and unknown-RAM fallbacks) at a value that
	// still bounds the overlay without thrashing the GC; 8 GiB caps huge
	// workstations so the desktop app stays a polite citizen.
	memLimitAutoMinBytes int64 = 2 << 30 // 2 GiB
	memLimitAutoMaxBytes int64 = 8 << 30 // 8 GiB
)

// Memory limit decision sources, reported in the startup log.
const (
	memLimitSourceConfig = "config" // explicit runtime.memory_soft_limit_mb (>0)
	memLimitSourceAuto   = "auto"   // derived from physical RAM
	memLimitSourceEnv    = "env"    // deferred to a GOMEMLIMIT seen at process start
	memLimitSourceOff    = "off"    // disabled via runtime.memory_soft_limit_mb: -1
)

// memoryLimitDecision is the resolved outcome for the soft limit: whether to
// apply one, its byte value (valid only when Apply), and the source that
// decided it (for the startup log).
type memoryLimitDecision struct {
	Apply  bool
	Limit  int64 // bytes; meaningful only when Apply
	Source string
}

// computeAutoMemoryLimit returns clamp(totalRAM/2, 2 GiB, 8 GiB). A zero or
// negative totalRAM (unknown RAM) clamps to the 2 GiB floor — the most
// conservative value that still bounds the GOGC overlay. Pure function; unit
// tested on 4/8/16/64 GB inputs.
func computeAutoMemoryLimit(totalRAMBytes int64) int64 {
	half := totalRAMBytes / memLimitAutoFractionDenom
	if half < memLimitAutoMinBytes {
		return memLimitAutoMinBytes
	}
	if half > memLimitAutoMaxBytes {
		return memLimitAutoMaxBytes
	}
	return half
}

// resolveMemoryLimit decides the soft memory limit from the three inputs,
// in precedence order:
//
//  1. runtime.memory_soft_limit_mb: -1 — OFF, nothing is set.
//  2. An explicit positive config value (>0 MiB) wins — app-specific config
//     is the most specific expression of intent for THIS app, so it also
//     overrides a GOMEMLIMIT env var (logged as an override).
//  3. AUTO mode (0/unset): a GOMEMLIMIT env var that the Go runtime actually
//     saw at PROCESS START takes priority — the runtime already applied it,
//     and overriding an operator-level env setting from inside the app would
//     silently discard it. Only a value present before the login shell is
//     sourced counts: a GOMEMLIMIT injected later by shell-env loading was
//     never read by the runtime, so it does NOT defer here — that case falls
//     through to the RAM-derived limit. Callers pass that captured value as
//     gomemlimitEnv (see setupMemorySoftLimit). Without a runtime-seen env
//     var, the limit is derived from physical RAM.
func resolveMemoryLimit(cfgSoftLimitMB int, totalRAMBytes int64, gomemlimitEnv string) memoryLimitDecision {
	switch {
	case cfgSoftLimitMB < 0: // -1 = off sentinel (validation rejects < -1)
		return memoryLimitDecision{Source: memLimitSourceOff}
	case cfgSoftLimitMB > 0:
		return memoryLimitDecision{
			Apply:  true,
			Limit:  int64(cfgSoftLimitMB) << 20,
			Source: memLimitSourceConfig,
		}
	case gomemlimitEnv != "":
		return memoryLimitDecision{Source: memLimitSourceEnv}
	default:
		return memoryLimitDecision{
			Apply:  true,
			Limit:  computeAutoMemoryLimit(totalRAMBytes),
			Source: memLimitSourceAuto,
		}
	}
}

// applyMemorySoftLimit applies the decision through the injectable setter
// (runtime/debug.SetMemoryLimit in production; a recorder in tests) and logs
// what was decided. The decision is never silent: every branch logs
// source=auto|config|env|off and total_ram_mib, so the effective GC target is
// always machine-parseable in the startup/session log regardless of source.
func applyMemorySoftLimit(
	decision memoryLimitDecision,
	totalRAMBytes int64,
	ramKnown bool,
	gomemlimitEnv string,
	set func(int64) int64,
	log *slog.Logger,
) {
	// total_ram_mib is reported on every branch; an unknown total is rendered
	// as the literal "unknown" (mirroring the AUTO branch's historical shape).
	totalRAMAttr := any("unknown")
	if ramKnown {
		totalRAMAttr = totalRAMBytes >> 20
	}
	switch decision.Source {
	case memLimitSourceOff:
		attrs := []any{"source", decision.Source, "total_ram_mib", totalRAMAttr}
		msg := "memory soft limit disabled by config (runtime.memory_soft_limit_mb: -1)"
		if gomemlimitEnv != "" {
			// A GOMEMLIMIT the runtime applied at process start cannot be
			// unset from inside the process: -1 disables only the app's own
			// limit, not that one.
			msg += "; the app's own limit is disabled, but a GOMEMLIMIT the Go runtime applied at process start stays in force"
			attrs = append(attrs, "gomemlimit", gomemlimitEnv)
		}
		log.Info(msg, attrs...)
	case memLimitSourceEnv:
		log.Info("GOMEMLIMIT was present at process start; keeping it and skipping the auto memory soft limit",
			"source", decision.Source,
			"gomemlimit", gomemlimitEnv,
			"total_ram_mib", totalRAMAttr,
		)
	case memLimitSourceConfig:
		if gomemlimitEnv != "" {
			// Explicit config beats the runtime-seen env var for this app; say so.
			log.Info("GOMEMLIMIT environment variable is overridden by an explicit runtime.memory_soft_limit_mb",
				"gomemlimit", gomemlimitEnv)
		}
		log.Info("memory soft limit set",
			"limit_mib", decision.Limit>>20,
			"source", decision.Source,
			"total_ram_mib", totalRAMAttr,
		)
		set(decision.Limit)
	default: // auto
		log.Info("memory soft limit set",
			"limit_mib", decision.Limit>>20,
			"source", decision.Source,
			"total_ram_mib", totalRAMAttr,
		)
		set(decision.Limit)
	}
}

// setupMemorySoftLimit is the startup entry point: resolve the decision from
// the loaded config, the machine's physical RAM, and runtimeSeenGomemlimit —
// the GOMEMLIMIT value the Go runtime actually saw at process start, captured
// in Startup BEFORE the login shell is sourced (a shell-exported GOMEMLIMIT
// the runtime never read must not spuriously defer the decision) — then apply
// it. Called from Startup right after Phase 2 (config load + logger re-init)
// and strictly before background indexing begins, so the limit governs the
// very first indexing pass.
func setupMemorySoftLimit(cfgSoftLimitMB int, runtimeSeenGomemlimit string, log *slog.Logger) {
	totalRAM, ramKnown := physicalMemoryBytes()
	decision := resolveMemoryLimit(cfgSoftLimitMB, totalRAM, runtimeSeenGomemlimit)
	applyMemorySoftLimit(decision, totalRAM, ramKnown, runtimeSeenGomemlimit, debug.SetMemoryLimit, log)
}
