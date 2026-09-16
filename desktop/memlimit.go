package desktop

import (
	"log/slog"
	"os"
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
//     (full indexing passes; see core/vectorindex/manager.go's
//     freeOSMemoryFn seam).

const (
	// memLimitAutoFraction is the share of physical RAM used by AUTO mode.
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
	memLimitSourceEnv    = "env"    // deferred to an existing GOMEMLIMIT
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
//  3. AUTO mode (0/unset): an existing GOMEMLIMIT env var takes priority —
//     the Go runtime already applied it at process start, and overriding an
//     operator-level env setting from inside the app would silently discard
//     it. Without the env var, the limit is derived from physical RAM.
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
// what was decided. The decision is never silent: every branch logs, so the
// effective GC target is always visible in the startup/session log.
func applyMemorySoftLimit(
	decision memoryLimitDecision,
	totalRAMBytes int64,
	ramKnown bool,
	gomemlimitEnv string,
	set func(int64) int64,
	log *slog.Logger,
) {
	switch decision.Source {
	case memLimitSourceOff:
		log.Info("memory soft limit disabled by config (runtime.memory_soft_limit_mb: -1)")
	case memLimitSourceEnv:
		log.Info("GOMEMLIMIT environment variable set; keeping it and skipping the auto memory soft limit",
			"gomemlimit", gomemlimitEnv)
	case memLimitSourceConfig:
		if gomemlimitEnv != "" {
			// Explicit config beats the env var for this app; say so.
			log.Info("GOMEMLIMIT environment variable is overridden by an explicit runtime.memory_soft_limit_mb",
				"gomemlimit", gomemlimitEnv)
		}
		log.Info("memory soft limit set", "limit_mib", decision.Limit>>20, "source", decision.Source)
		set(decision.Limit)
	default: // auto
		attrs := []any{"limit_mib", decision.Limit >> 20, "source", decision.Source}
		if ramKnown {
			attrs = append(attrs, "total_ram_mib", totalRAMBytes>>20)
		} else {
			attrs = append(attrs, "total_ram_mib", "unknown")
		}
		log.Info("memory soft limit set", attrs...)
		set(decision.Limit)
	}
}

// setupMemorySoftLimit is the startup entry point: resolve the decision from
// the loaded config, the machine's physical RAM, and the environment, then
// apply it. Called from Startup right after Phase 2 (config load + logger
// re-init) and strictly before background indexing begins, so the limit
// governs the very first indexing pass.
func setupMemorySoftLimit(cfgSoftLimitMB int, log *slog.Logger) {
	totalRAM, ramKnown := physicalMemoryBytes()
	gomemlimitEnv := os.Getenv("GOMEMLIMIT")
	decision := resolveMemoryLimit(cfgSoftLimitMB, totalRAM, gomemlimitEnv)
	applyMemorySoftLimit(decision, totalRAM, ramKnown, gomemlimitEnv, debug.SetMemoryLimit, log)
}
