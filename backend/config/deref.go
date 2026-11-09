package config

import (
	"github.com/v0lka/c0wrk/core/vectorindex"
)

// Deref helpers for the pointer-valued vector_index config knobs: the YAML
// schema models "unset" as a nil pointer, and several knobs distinguish
// "unset → resolved default" from "explicit zero". Relocated here by stage
// P-12 of the desktop layering audit (issue #42) — this package owns the
// config schema, so pointer dereferencing belongs beside it, not in the
// consumers. The exported wrappers are the cross-package surface; the
// lowercase originals stay for in-package callers and grep stability.

// DerefFloat returns *p when non-nil, else 0 — the pointer-float64 hybrid
// thresholds from config into vectorindex.HybridConfig.
func DerefFloat(p *float64) float64 {
	return derefFloat(p)
}

// derefFloat returns *p when non-nil, else 0 — the pointer-float64 hybrid
// thresholds from config into vectorindex.HybridConfig.
func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// DerefInt returns *p when non-nil, else 0 — the pointer-int vector-index
// sentinel (search_wait_timeout_ms) as a plain value.
func DerefInt(p *int) int {
	return derefInt(p)
}

// derefInt returns *p when non-nil, else 0 — the pointer-int vector-index
// sentinel (search_wait_timeout_ms) as a plain value.
func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// DerefParkBudgetBytes converts vector_index.park_budget_mb into bytes. A nil
// pointer resolves to vectorindex.DefaultParkBudgetBytes (1024 MiB), not 0 —
// the service reads a zero budget as "no byte budget", so the old
// derefInt64(nil)<<20 fail-open silently dropped the ceiling. -1 keeps its
// "byte budget disabled" sentinel meaning.
func DerefParkBudgetBytes(p *int64) int64 {
	return derefParkBudgetBytes(p)
}

// derefParkBudgetBytes converts vector_index.park_budget_mb into bytes. A nil
// pointer resolves to vectorindex.DefaultParkBudgetBytes (1024 MiB), not 0 —
// the service reads a zero budget as "no byte budget", so the old
// derefInt64(nil)<<20 fail-open silently dropped the ceiling. -1 keeps its
// "byte budget disabled" sentinel meaning.
func derefParkBudgetBytes(p *int64) int64 {
	if p == nil {
		return vectorindex.DefaultParkBudgetBytes
	}
	return *p << 20
}

// DerefCorpusResidencyBudgetBytes converts
// vector_index.corpus_residency_budget_mb into bytes. The knob has no
// sentinel semantics (0 is the documented "no byte bound"; negatives never
// reach here — validate() rejects them at load), so the nil path is the
// resolved default alone.
func DerefCorpusResidencyBudgetBytes(p *int64) int64 {
	return derefCorpusResidencyBudgetBytes(p)
}

// derefCorpusResidencyBudgetBytes converts
// vector_index.corpus_residency_budget_mb into bytes. The knob has no
// sentinel semantics (0 is the documented "no byte bound"; negatives never
// reach here — validate() rejects them at load), so the nil path is the
// resolved default alone.
func derefCorpusResidencyBudgetBytes(p *int64) int64 {
	if p == nil {
		return vectorindex.DefaultCorpusResidencyBudgetBytes
	}
	return *p << 20
}

// DerefStoreDiskBudgetBytes converts vector_index.store_disk_budget_mb into
// bytes. Same no-sentinel semantics as the residency budget (0 = no cap;
// negatives never reach here — validate() rejects them at load).
func DerefStoreDiskBudgetBytes(p *int64) int64 {
	return derefStoreDiskBudgetBytes(p)
}

// derefStoreDiskBudgetBytes converts vector_index.store_disk_budget_mb into
// bytes. Same no-sentinel semantics as the residency budget (0 = no cap;
// negatives never reach here — validate() rejects them at load).
func derefStoreDiskBudgetBytes(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p << 20
}
