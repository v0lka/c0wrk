package desktop

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/vectorindex"
)

const gib = int64(1) << 30

// TestComputeAutoMemoryLimit pins the AUTO clamp — 50% of physical RAM,
// clamped to [2 GiB, 8 GiB] — across the machine sizes the fleet actually
// spans. 4 GB hits the floor (a 2 GB half is exactly the minimum), 8/16 GB
// interpolate, 64 GB hits the ceiling, and unknown RAM (0) falls back to
// the conservative floor rather than skipping the limit.
func TestComputeAutoMemoryLimit(t *testing.T) {
	tests := []struct {
		name string
		ram  int64
		want int64
	}{
		{"4GB machine → floor (2 GiB)", 4 * gib, 2 * gib},
		{"6GB machine → 3 GiB (mid-range)", 6 * gib, 3 * gib},
		{"8GB machine → 4 GiB", 8 * gib, 4 * gib},
		{"16GB machine → 8 GiB (ceiling boundary)", 16 * gib, 8 * gib},
		{"64GB machine → ceiling (8 GiB)", 64 * gib, 8 * gib},
		{"unknown RAM (0) → conservative floor", 0, 2 * gib},
		{"nonsense negative RAM → conservative floor", -1, 2 * gib},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeAutoMemoryLimit(tt.ram); got != tt.want {
				t.Errorf("computeAutoMemoryLimit(%d bytes) = %d, want %d", tt.ram, got, tt.want)
			}
		})
	}
}

// TestResolveMemoryLimit_PriorityOrder pins the precedence: off sentinel →
// explicit config (even over a GOMEMLIMIT env var) → GOMEMLIMIT env var
// (over AUTO only) → AUTO from physical RAM.
func TestResolveMemoryLimit_PriorityOrder(t *testing.T) {
	tests := []struct {
		name        string
		cfgMB       int
		ram         int64
		env         string
		wantApply   bool
		wantLimit   int64
		wantSource  string
		description string
	}{
		{
			name:  "auto mode derives from RAM",
			cfgMB: 0, ram: 16 * gib, env: "",
			wantApply: true, wantLimit: 8 * gib, wantSource: memLimitSourceAuto,
		},
		{
			name:  "GOMEMLIMIT env preempts auto mode",
			cfgMB: 0, ram: 16 * gib, env: "4GiB",
			wantApply: false, wantSource: memLimitSourceEnv,
		},
		{
			name:  "explicit config applies verbatim",
			cfgMB: 2048, ram: 64 * gib, env: "",
			wantApply: true, wantLimit: 2048 << 20, wantSource: memLimitSourceConfig,
		},
		{
			name:  "explicit config wins over GOMEMLIMIT env",
			cfgMB: 2048, ram: 16 * gib, env: "4GiB",
			wantApply: true, wantLimit: 2048 << 20, wantSource: memLimitSourceConfig,
		},
		{
			name:  "off sentinel sets nothing",
			cfgMB: -1, ram: 16 * gib, env: "",
			wantApply: false, wantSource: memLimitSourceOff,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveMemoryLimit(tt.cfgMB, tt.ram, tt.env)
			if got.Apply != tt.wantApply {
				t.Errorf("Apply = %v, want %v", got.Apply, tt.wantApply)
			}
			if got.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tt.wantSource)
			}
			if tt.wantApply && got.Limit != tt.wantLimit {
				t.Errorf("Limit = %d, want %d", got.Limit, tt.wantLimit)
			}
		})
	}
}

// captureLogs runs fn with a text-handler logger writing into a buffer and
// returns the captured output.
func captureLogs(fn func(log *slog.Logger)) string {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fn(log)
	return buf.String()
}

// TestApplyMemorySoftLimit_SetterAndLogging verifies the setter is invoked
// exactly when the decision says so, and that every branch is logged: the
// applied limit (auto AND config sources), the GOMEMLIMIT deferral, and the
// off sentinel are all visible in the startup log.
func TestApplyMemorySoftLimit_SetterAndLogging(t *testing.T) {
	t.Run("auto applies and logs the derived limit", func(t *testing.T) {
		var setCalls []int64
		out := captureLogs(func(log *slog.Logger) {
			applyMemorySoftLimit(
				memoryLimitDecision{Apply: true, Limit: 8 * gib, Source: memLimitSourceAuto},
				16*gib, true, "", func(l int64) int64 { setCalls = append(setCalls, l); return 0 }, log,
			)
		})
		if len(setCalls) != 1 || setCalls[0] != 8*gib {
			t.Errorf("setter calls = %v, want one call with %d", setCalls, 8*gib)
		}
		if !strings.Contains(out, "memory soft limit set") || !strings.Contains(out, "source=auto") {
			t.Errorf("auto application must be logged, got: %s", out)
		}
		if !strings.Contains(out, "total_ram_mib=16384") {
			t.Errorf("known RAM must be logged, got: %s", out)
		}
	})

	t.Run("config applies the explicit value and logs the env override", func(t *testing.T) {
		var setCalls []int64
		out := captureLogs(func(log *slog.Logger) {
			applyMemorySoftLimit(
				memoryLimitDecision{Apply: true, Limit: 2048 << 20, Source: memLimitSourceConfig},
				16*gib, true, "4GiB", func(l int64) int64 { setCalls = append(setCalls, l); return 0 }, log,
			)
		})
		if len(setCalls) != 1 || setCalls[0] != 2048<<20 {
			t.Errorf("setter calls = %v, want one call with %d", setCalls, 2048<<20)
		}
		if !strings.Contains(out, "source=config") {
			t.Errorf("config application must be logged, got: %s", out)
		}
		if !strings.Contains(out, "overridden by an explicit runtime.memory_soft_limit_mb") {
			t.Errorf("config-over-env override must be logged, got: %s", out)
		}
	})

	t.Run("env defers to GOMEMLIMIT and never sets", func(t *testing.T) {
		var setCalls []int64
		out := captureLogs(func(log *slog.Logger) {
			applyMemorySoftLimit(
				memoryLimitDecision{Source: memLimitSourceEnv},
				16*gib, true, "4GiB", func(l int64) int64 { setCalls = append(setCalls, l); return 0 }, log,
			)
		})
		if len(setCalls) != 0 {
			t.Errorf("setter must not be called when deferring to GOMEMLIMIT, got %v", setCalls)
		}
		if !strings.Contains(out, "GOMEMLIMIT") {
			t.Errorf("env deferral must be logged, got: %s", out)
		}
		if !strings.Contains(out, "source=env") {
			t.Errorf("env branch must log a machine-parseable source=, got: %s", out)
		}
		if !strings.Contains(out, "total_ram_mib=16384") {
			t.Errorf("env branch must log total RAM, got: %s", out)
		}
	})

	t.Run("off sentinel sets nothing and says so", func(t *testing.T) {
		var setCalls []int64
		out := captureLogs(func(log *slog.Logger) {
			applyMemorySoftLimit(
				memoryLimitDecision{Source: memLimitSourceOff},
				16*gib, true, "", func(l int64) int64 { setCalls = append(setCalls, l); return 0 }, log,
			)
		})
		if len(setCalls) != 0 {
			t.Errorf("setter must not be called when disabled, got %v", setCalls)
		}
		if !strings.Contains(out, "disabled by config") {
			t.Errorf("off sentinel must be logged, got: %s", out)
		}
		if !strings.Contains(out, "source=off") {
			t.Errorf("off branch must log a machine-parseable source=, got: %s", out)
		}
		if !strings.Contains(out, "total_ram_mib=16384") {
			t.Errorf("off branch must log total RAM, got: %s", out)
		}
	})

	t.Run("off sentinel alongside a runtime-seen GOMEMLIMIT explains it stays in force", func(t *testing.T) {
		out := captureLogs(func(log *slog.Logger) {
			applyMemorySoftLimit(
				memoryLimitDecision{Source: memLimitSourceOff},
				16*gib, true, "4GiB", func(int64) int64 { return 0 }, log,
			)
		})
		if !strings.Contains(out, "stays in force") {
			t.Errorf("off+env must explain the runtime GOMEMLIMIT stays in force, got: %s", out)
		}
	})
}

// TestDerefParkBudgetBytes pins the nil→default contract: an unset
// park_budget_mb (nil pointer) must resolve to the documented 1024 MiB default
// rather than 0 (which the service reads as "byte budget disabled"), while a
// non-nil value is scaled MiB→bytes verbatim — preserving the negative -1
// disable sentinel.
func TestDerefParkBudgetBytes(t *testing.T) {
	if got := derefParkBudgetBytes(nil); got != vectorindex.DefaultParkBudgetBytes {
		t.Errorf("derefParkBudgetBytes(nil) = %d, want %d (the 1024 MiB default)",
			got, vectorindex.DefaultParkBudgetBytes)
	}
	if vectorindex.DefaultParkBudgetBytes != 1024<<20 {
		t.Errorf("vectorindex.DefaultParkBudgetBytes = %d, want %d (1024 MiB)",
			vectorindex.DefaultParkBudgetBytes, int64(1024)<<20)
	}

	explicit := int64(2048)
	if got := derefParkBudgetBytes(&explicit); got != 2048<<20 {
		t.Errorf("derefParkBudgetBytes(&2048) = %d, want %d", got, int64(2048)<<20)
	}

	disabled := int64(-1)
	if got := derefParkBudgetBytes(&disabled); got != -1<<20 {
		t.Errorf("derefParkBudgetBytes(&-1) = %d, want %d (the disable sentinel stays negative)",
			got, int64(-1)<<20)
	}
}

// TestPhysicalMemoryBytes_Sanity is a smoke test for the platform probe on
// the test machine: on darwin/linux it must report a plausible positive
// value; the unknown-RAM fallback is exercised implicitly on other builds.
func TestPhysicalMemoryBytes_Sanity(t *testing.T) {
	total, known := physicalMemoryBytes()
	if !known {
		t.Skip("platform reports unknown RAM; the 2 GiB floor applies")
	}
	if total < 512*1024*1024 {
		t.Errorf("physicalMemoryBytes() = %d, implausibly small", total)
	}
}
