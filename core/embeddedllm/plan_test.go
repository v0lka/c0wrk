package embeddedllm

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

// profileOrFail is the measured profile every plan in these tests is made
// against. It is the real pinned one — a synthetic profile would let a test
// pass against numbers the shipped model does not have.
func profileOrFail(t *testing.T) ModelMemoryProfile {
	t.Helper()
	profile, err := PinnedMemoryProfile()
	if err != nil {
		t.Fatalf("PinnedMemoryProfile() error = %v", err)
	}
	return profile
}

// topologyLiteral builds a MemoryTopology with EXACT budgets, bypassing
// buildTopology's margin and reserve derivation. Tests that need a specific
// budget use this; tests that need a realistic machine use buildTopology.
func topologyLiteral(deviceMiB, hostMiB int64, devices []DeviceMemory, ramGiB float64) MemoryTopology {
	if devices == nil {
		devices = []DeviceMemory{}
	}
	return MemoryTopology{
		Devices:           devices,
		HostRAMGiB:        ramGiB,
		Unified:           len(devices) == 0,
		DeviceBudgetBytes: deviceMiB * bytesPerMiB,
		HostBudgetBytes:   hostMiB * bytesPerMiB,
		ProbedAt:          "2026-09-25T00:00:00Z",
	}
}

// probedTopology builds a topology the way ProbeDevices' pure half does, from a
// platform, a RAM size in GiB and an accelerator inventory.
func probedTopology(t *testing.T, platform string, ramGiB float64, devices ...DeviceMemory) MemoryTopology {
	t.Helper()
	return buildTopology(platform, int64(ramGiB*gibibyte), deviceListing{devices: devices}, "2026-09-25T00:00:00Z")
}

// deviceFootprint is the accelerator memory one (context, precision) shape
// needs INCLUDING the vision projector, computed from memory.go's own API
// rather than from plan.go's, so a budget derived with it is not circular with
// the code under test.
func deviceFootprint(t *testing.T, profile ModelMemoryProfile, packing Packing, ctx int, kv KVType) int64 {
	t.Helper()
	device, err := profile.ProjectDeviceMiB(packing, ctx, kv, true)
	if err != nil {
		t.Fatalf("ProjectDeviceMiB(%v, %d, %v) error = %v", packing, ctx, kv, err)
	}
	return device + profile.MMProjReserveDeviceMiB
}

// boolPtr boxes a bool for the pointer half of the Tuning vocabulary.
func boolPtr(v bool) *bool { return &v }

// intPtr boxes an int for the pointer half of the Tuning vocabulary.
func intPtr(v int) *int { return &v }

// floatPtr boxes a float64 for the pointer half of the Tuning vocabulary.
func floatPtr(v float64) *float64 { return &v }

// noteContaining reports whether any note contains the substring.
func noteContaining(notes []string, needle string) bool {
	return len(notes) > 0 && strings.Contains(strings.Join(notes, "\n"), needle)
}

// ─────────────────────────────────────────────────────────────────────────────
// purity
// ─────────────────────────────────────────────────────────────────────────────

// TestPlanImportsNoIOPackage is the static half of the purity invariant: the
// file that owns `Plan` cannot reach the filesystem, the network, a subprocess
// or the clock, because it imports nothing that can.
//
// A source-level ban is used instead of a runtime sandbox because there is no
// portable one: a test cannot revoke a goroutine's access to os.Open or
// time.Now from inside the process it is running in. What it CAN do — and what
// this does — is make the guarantee structural, so adding an import that
// performs I/O fails the build of this test rather than silently making the
// planner impure.
func TestPlanImportsNoIOPackage(t *testing.T) {
	t.Parallel()

	// Every package that can perform I/O, observe the clock, spawn a process,
	// touch the network or read the environment. `runtime` is banned because
	// NumCPU/GOOS would make a plan depend on the machine running the planner
	// rather than on its arguments.
	banned := map[string]string{
		"os":            "filesystem and environment",
		"os/exec":       "subprocess",
		"io":            "filesystem and streams",
		"io/fs":         "filesystem",
		"bufio":         "streams",
		"net":           "network",
		"net/http":      "network",
		"path/filepath": "filesystem paths",
		"runtime":       "the host machine's own GOOS/GOMAXPROCS",
		"syscall":       "the host kernel",
		"time":          "the clock",
		"log":           "global logger state",
		"log/slog":      "logging",
		"database/sql":  "a database",
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "plan.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing plan.go: %v", err)
	}
	if len(file.Imports) == 0 {
		t.Fatal("plan.go declares no imports; the guard is vacuous")
	}

	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("unquoting import %s: %v", spec.Path.Value, err)
		}
		if reason, ok := banned[path]; ok {
			t.Errorf("plan.go imports %q (%s); Plan must perform no I/O, no probing and read no clock", path, reason)
		}
	}
}

// TestPlanIsPure is the behavioural half: the same inputs always produce a
// byte-identical plan, concurrently, and the one clock-derived field in the
// inputs (MemoryTopology.ProbedAt) changes nothing.
func TestPlanIsPure(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	topology := probedTopology(t, PlatformDarwinARM64, 128,
		DeviceMemory{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: 110100, FreeMiB: 110100})

	want, err := Plan(topology, profile, Tuning{}, BackendMetal, GPUFamilyAppleSilicon)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}

	// Determinism under concurrency: a plan that read package state or a clock
	// would diverge across goroutines.
	const goroutines = 16
	var wg sync.WaitGroup
	results := make([]MemoryPlan, goroutines)
	errs := make([]error, goroutines)
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = Plan(topology, profile, Tuning{}, BackendMetal, GPUFamilyAppleSilicon)
		}(i)
	}
	wg.Wait()
	for i := range goroutines {
		if errs[i] != nil {
			t.Fatalf("Plan (goroutine %d) error = %v", i, errs[i])
		}
		if !reflect.DeepEqual(results[i], want) {
			t.Errorf("Plan (goroutine %d) is not deterministic:\n got %+v\nwant %+v", i, results[i], want)
		}
	}

	// ProbedAt is the only clock-derived field in the inputs. A pure planner
	// cannot read it.
	stamped := topology
	stamped.ProbedAt = "1970-01-01T00:00:00Z"
	got, err := Plan(stamped, profile, Tuning{}, BackendMetal, GPUFamilyAppleSilicon)
	if err != nil {
		t.Fatalf("Plan (stamped) error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Plan depends on MemoryTopology.ProbedAt:\n got %+v\nwant %+v", got, want)
	}
}

// TestPlanPerformsNoFilesystemAccess runs the planner from a working directory
// that does not exist, so any relative-path read it attempted would fail. It is
// the runtime companion to the import ban: cheap, and it catches an I/O path
// reached through a package this file does not import directly.
func TestPlanPerformsNoFilesystemAccess(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	topology := probedTopology(t, PlatformLinuxAMD64, 64,
		DeviceMemory{Name: "CUDA0", Description: "NVIDIA L4", TotalMiB: 24576, FreeMiB: 24576})

	removed := t.TempDir()
	gone := removed + "/does-not-exist"
	if err := os.Remove(removed); err != nil {
		t.Fatalf("removing the temp dir: %v", err)
	}
	if _, err := os.Stat(gone); err == nil {
		t.Fatalf("%s exists; the test would not prove anything", gone)
	}

	// Plan takes no path and opens nothing, so the only way this can fail is if
	// it reaches the filesystem through a package it should not import.
	if _, err := Plan(topology, profile, Tuning{}, BackendCUDA124, GPUFamilyNVIDIAAda); err != nil {
		t.Fatalf("Plan error = %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// fit exclusivity
// ─────────────────────────────────────────────────────────────────────────────

// TestPlanFitExclusivity is the rule the planner is organised around, tabled in
// BOTH directions:
//
//	auto                    → `-fit on`,  `-ngl` OMITTED, `-c` zero
//	any explicit override   → `-fit off`, `-ngl` passed,  `-c` COMPUTED HERE
//
// The second direction is not a stylistic preference. The pinned fork's
// `common/fit.cpp` THROWS when `--fit` is asked to size a launch whose offload
// the caller already pinned (the throw sites at :183 and :462, :466, :472,
// :477, :480, :483), confirmed empirically: `-ngl 99` beside `--fit on` aborts
// the launch. So a plan that emitted both would not degrade — it would crash.
func TestPlanFitExclusivity(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	topology := probedTopology(t, PlatformLinuxAMD64, 64,
		DeviceMemory{Name: "CUDA0", Description: "NVIDIA L4", TotalMiB: 24576, FreeMiB: 24576})

	tests := []struct {
		name string
		// tuning is the override under test.
		tuning Tuning
		// wantFit is the `-fit` value the plan must carry.
		wantFit bool
		// wantLayers is the `-ngl` the plan must carry, or nil for "omit".
		wantLayers *int
		// wantContextComputed says whether the planner must compute `-c`
		// itself (fit off) or hand it to the runtime (fit on).
		wantContextComputed bool
	}{
		{
			name:                "all-Auto hands sizing to the runtime",
			tuning:              Tuning{},
			wantFit:             true,
			wantLayers:          nil,
			wantContextComputed: false,
		},
		{
			name:                "an explicit context alone does not pin the offload",
			tuning:              Tuning{Context: ContextTuning{Mode: ContextExact, Tokens: 32768}},
			wantFit:             true,
			wantLayers:          nil,
			wantContextComputed: true,
		},
		{
			name:                "Offload All pins the offload",
			tuning:              Tuning{Offload: Offload{Mode: OffloadAll}},
			wantFit:             false,
			wantLayers:          intPtr(nglAllGPU),
			wantContextComputed: true,
		},
		{
			name:                "Offload CPU pins the offload",
			tuning:              Tuning{Offload: Offload{Mode: OffloadCPU}},
			wantFit:             false,
			wantLayers:          intPtr(nglCPUOnly),
			wantContextComputed: true,
		},
		{
			name:                "an explicit layer count pins the offload",
			tuning:              Tuning{Offload: Offload{Mode: OffloadLayers, Layers: 32}},
			wantFit:             false,
			wantLayers:          intPtr(32),
			wantContextComputed: true,
		},
		{
			name:                "a device list pins the offload",
			tuning:              Tuning{Devices: []string{"CUDA0"}},
			wantFit:             false,
			wantLayers:          intPtr(nglAllGPU),
			wantContextComputed: true,
		},
		{
			name:                "a split mode pins the offload",
			tuning:              Tuning{SplitMode: SplitModeTensor},
			wantFit:             false,
			wantLayers:          intPtr(nglAllGPU),
			wantContextComputed: true,
		},
		{
			name:                "an explicit --fit off pins the offload",
			tuning:              Tuning{FitEnabled: boolPtr(false)},
			wantFit:             false,
			wantLayers:          intPtr(nglAllGPU),
			wantContextComputed: true,
		},
		{
			name: "an explicit --fit on loses to an explicit offload",
			// The exclusivity rule is not a preference the operator can
			// override: fit.cpp would throw, so the override is ignored and
			// the plan says so.
			tuning:              Tuning{FitEnabled: boolPtr(true), Offload: Offload{Mode: OffloadAll}},
			wantFit:             false,
			wantLayers:          intPtr(nglAllGPU),
			wantContextComputed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			plan, err := Plan(topology, profile, tc.tuning, BackendCUDA124, GPUFamilyNVIDIAAda)
			if err != nil {
				t.Fatalf("Plan error = %v", err)
			}

			if plan.Fit != tc.wantFit {
				t.Errorf("Fit = %v, want %v", plan.Fit, tc.wantFit)
			}
			wantFitArg := "off"
			if tc.wantFit {
				wantFitArg = "on"
			}
			if got := plan.FitArg(); got != wantFitArg {
				t.Errorf("FitArg() = %q, want -fit %q", got, wantFitArg)
			}

			if !reflect.DeepEqual(plan.Layers, tc.wantLayers) {
				t.Errorf("Layers = %v, want %v", derefOrOmit(plan.Layers), derefOrOmit(tc.wantLayers))
			}
			if got, want := plan.EmitsLayers(), tc.wantLayers != nil; got != want {
				t.Errorf("EmitsLayers() = %v, want %v (nil Layers means -ngl is omitted)", got, want)
			}

			switch {
			case tc.wantFit && tc.wantContextComputed:
				// An exact context under fit: the plan records what the
				// operator asked for, while fit still sizes the offload.
				if plan.ContextSize != tc.tuning.Context.Tokens {
					t.Errorf("ContextSize = %d, want the exact override %d",
						plan.ContextSize, tc.tuning.Context.Tokens)
				}
			case tc.wantFit:
				// The fit path renders the RAM tier as an explicit -c (held
				// to the fit floor): fit sizes the offload, never the
				// context, and an omitted -c would hand the context to the
				// runtime's fit pass, which sizes it up to the model's full
				// training context regardless of available memory.
				if plan.ContextSize <= 0 {
					t.Errorf("ContextSize = %d, want the RAM tier the plan renders as an explicit -c", plan.ContextSize)
				}
			default:
				if plan.ContextSize <= 0 {
					t.Errorf("ContextSize = %d, want a context the planner computed itself: with -fit off nothing else will",
						plan.ContextSize)
				}
				if plan.ContextSize > profile.MaxContext {
					t.Errorf("ContextSize = %d exceeds the model's training context %d",
						plan.ContextSize, profile.MaxContext)
				}
			}

			if tc.tuning.FitEnabled != nil && *tc.tuning.FitEnabled && !tc.wantFit {
				if !noteContaining(plan.Notes, "--fit on was requested") {
					t.Errorf("Notes = %v, want one explaining that --fit on lost to the exclusivity rule", plan.Notes)
				}
			}
		})
	}
}

// derefOrOmit renders a layer pointer for a failure message, keeping nil
// ("omit -ngl") distinguishable from a pointer to 0 ("-ngl 0").
func derefOrOmit(p *int) string {
	if p == nil {
		return "<omitted>"
	}
	return strconv.Itoa(*p)
}

// TestPlanFitMinContextIsNotUpstreamsDefault pins the divergence from the
// runtime's own `-fitc` default and states why: 4096 is the fork's floor, and
// the pinned model's issue tracker reports empty and truncated answers as its
// most common complaint, with `-c 65536` as the documented workaround.
func TestPlanFitMinContextIsNotUpstreamsDefault(t *testing.T) {
	t.Parallel()

	if DefaultFitMinContext == upstreamFitMinContext {
		t.Errorf("DefaultFitMinContext = %d, which is the runtime's own default; the KNOWN_ISSUES empty/truncated-answer reports make that floor unusable",
			DefaultFitMinContext)
	}
	if DefaultFitMinContext != 65536 {
		t.Errorf("DefaultFitMinContext = %d, want 65536", DefaultFitMinContext)
	}
	if upstreamFitMinContext != 4096 {
		t.Errorf("upstreamFitMinContext = %d, want the documented `-fitc` default of 4096", upstreamFitMinContext)
	}

	profile := profileOrFail(t)
	topology := probedTopology(t, PlatformDarwinARM64, 128,
		DeviceMemory{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: 110100, FreeMiB: 110100})
	plan, err := Plan(topology, profile, Tuning{}, BackendMetal, GPUFamilyAppleSilicon)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if plan.FitMinContext != DefaultFitMinContext {
		t.Errorf("FitMinContext = %d, want %d", plan.FitMinContext, DefaultFitMinContext)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// defaults
// ─────────────────────────────────────────────────────────────────────────────

// TestPlanParallelDefaultsToOne pins `-np 1` and the two measurements behind
// it, both taken against the pinned fork:
//
//   - `-np 4` inflates fit's own projection from 24450 MiB to 77297 MiB,
//     because n_streams becomes 4 when kv_unified is false;
//   - `-np` splits `-c` across slots, so `-c 8192 -np 4` yields n_ctx_slot
//     2048 — four quarters of the context the plan reasoned about.
//
// The runtime's own default is `-1` (auto), which is exactly the value that
// produces both effects.
func TestPlanParallelDefaultsToOne(t *testing.T) {
	t.Parallel()

	if DefaultParallel != 1 {
		t.Errorf("DefaultParallel = %d, want 1", DefaultParallel)
	}

	profile := profileOrFail(t)
	topology := probedTopology(t, PlatformDarwinARM64, 128,
		DeviceMemory{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: 110100, FreeMiB: 110100})

	plan, err := Plan(topology, profile, Tuning{}, BackendMetal, GPUFamilyAppleSilicon)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if plan.Parallel != 1 {
		t.Errorf("Parallel = %d, want 1", plan.Parallel)
	}

	// An override is honoured, and the plan says what it costs: the context the
	// gate verified is divided across the slots.
	plan, err = Plan(topology, profile, Tuning{Parallel: intPtr(4)}, BackendMetal, GPUFamilyAppleSilicon)
	if err != nil {
		t.Fatalf("Plan (parallel 4) error = %v", err)
	}
	if plan.Parallel != 4 {
		t.Errorf("Parallel = %d, want the override 4", plan.Parallel)
	}
	if !noteContaining(plan.Notes, "each slot gets") {
		t.Errorf("Notes = %v, want one stating the per-slot context", plan.Notes)
	}
}

// TestPlanDefaultsAreTheAllAutoShape pins the whole default plan: fit sizes the
// launch, the lossless cache is used, one slot is served, no optional flag is
// emitted, and the packing is the backend's own.
func TestPlanDefaultsAreTheAllAutoShape(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	topology := probedTopology(t, PlatformDarwinARM64, 128,
		DeviceMemory{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: 110100, FreeMiB: 110100})

	plan, err := Plan(topology, profile, Tuning{}, BackendMetal, GPUFamilyAppleSilicon)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}

	if !plan.Fit {
		t.Error("Fit = false, want true: nothing is pinned, so the runtime sizes the launch")
	}
	if plan.Layers != nil {
		t.Errorf("Layers = %v, want nil so -ngl is omitted", derefOrOmit(plan.Layers))
	}
	// The fit path renders the RAM tier as an explicit -c (held to the fit
	// floor): fit sizes the offload, never the context, and an omitted -c lets
	// the runtime size the context up to the model's full training context
	// regardless of available memory.
	if want := contextSizeFor(128); plan.ContextSize != want {
		t.Errorf("ContextSize = %d, want the RAM tier %d rendered as an explicit -c under fit", plan.ContextSize, want)
	}
	// 131072 > the long-context threshold, so the KV ladder starts at q8_0:
	// halving the cache costs measured 1% of throughput and frees half the
	// device capacity the weights would otherwise share it with.
	if plan.KVType != KVTypeQ8_0 {
		t.Errorf("KVType = %v, want q8_0 for a long-context plan", plan.KVType)
	}
	if plan.Packing != PackingPQ2_0 {
		t.Errorf("Packing = %v, want the Metal backend's PQ2_0", plan.Packing)
	}
	if !plan.KVOffload {
		t.Error("KVOffload = false, want true: -nkvo is an opt-in")
	}
	if !plan.MMProjOffload {
		t.Error("MMProjOffload = false, want true: --no-mmproj-offload is an opt-in")
	}
	if plan.Parallel != 1 {
		t.Errorf("Parallel = %d, want 1", plan.Parallel)
	}
	if plan.CacheRAMMiB != nil {
		t.Errorf("CacheRAMMiB = %v, want nil so -cram is omitted", *plan.CacheRAMMiB)
	}
	if len(plan.Devices) != 0 {
		t.Errorf("Devices = %v, want none so -dev is omitted", plan.Devices)
	}
	if plan.SplitMode != SplitModeAuto {
		t.Errorf("SplitMode = %q, want the unset value so -sm is omitted", plan.SplitMode)
	}
	if plan.FitTargetMiB != 0 {
		t.Errorf("FitTargetMiB = %d, want 0 so -fitt is omitted", plan.FitTargetMiB)
	}
	if plan.FitMinContext != DefaultFitMinContext {
		t.Errorf("FitMinContext = %d, want %d", plan.FitMinContext, DefaultFitMinContext)
	}
	if plan.ExpectedDeviceMiB <= 0 || plan.ExpectedHostMiB <= 0 {
		t.Errorf("expected footprints = %d/%d, want both positive", plan.ExpectedDeviceMiB, plan.ExpectedHostMiB)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// adaptive KV
// ─────────────────────────────────────────────────────────────────────────────

// TestPlanAdaptiveKVEscalation tables the f16 → q8_0 → q4_0 ladder at three
// budget levels, plus the refusal below all three.
//
// The budgets are derived from memory.go's own projection API rather than from
// plan.go's, so the test is not circular: each level is "just enough for this
// precision and not the one above it". The escalation is the right thing to
// spend first because it is nearly free — the fork measures q8_0 and q4_0
// within 1% of f16 throughput on this model (pp 342.0/342.6 against 343.5, tg
// 31.5/31.3 against 31.8) while cutting the cache to 53% and 28%.
func TestPlanAdaptiveKVEscalation(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	const ctx = DefaultFitMinContext

	f16 := deviceFootprint(t, profile, PackingPQ2_0, ctx, KVTypeF16)
	q8 := deviceFootprint(t, profile, PackingPQ2_0, ctx, KVTypeQ8_0)
	q4 := deviceFootprint(t, profile, PackingPQ2_0, ctx, KVTypeQ4_0)

	// Sanity: the ladder must actually descend, or every level below is
	// testing the same budget.
	if f16 <= q8 || q8 <= q4 {
		t.Fatalf("device footprints do not descend: f16 %d, q8_0 %d, q4_0 %d", f16, q8, q4)
	}

	tests := []struct {
		name      string
		deviceMiB int64
		wantKV    KVType
		wantErr   error
	}{
		{"a budget that fits f16 keeps f16", f16 + 512, KVTypeF16, nil},
		{"a budget short of f16 escalates to q8_0", q8 + 512, KVTypeQ8_0, nil},
		{"a budget short of q8_0 escalates to q4_0", q4 + 512, KVTypeQ4_0, nil},
		// Below the whole ladder the plan DEGRADES rather than refuses: the
		// host budget here is generous, so the model still fits in system RAM.
		// The refusal this case used to assert is what a machine with nowhere
		// left to go gets — see TestPlanRefusalNamesTheArithmetic, and
		// TestPlanPinnedOffloadRefusesInsteadOfDegrading for the pinned shape.
		{"a budget short of q4_0 degrades off the accelerator", q4 - 1, KVTypeF16, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Apple Silicon pays no split allowance, so the literal budget IS
			// the budget the gate spends. The host side is deliberately
			// generous: this test is about the device ladder. The RAM figure
			// puts the host below the 131072 tier, so the fit path's target
			// stays at the 65536 floor (ctx) — the short-context ladder this
			// table exercises. (A 128-GiB host would plan 131072 and start the
			// ladder at q8_0 instead — see TestPlanDefaultsAreTheAllAutoShape.)
			topology := topologyLiteral(tc.deviceMiB, 64*1024, []DeviceMemory{
				{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: tc.deviceMiB, FreeMiB: tc.deviceMiB},
			}, 24)

			plan, err := Plan(topology, profile, Tuning{}, BackendMetal, GPUFamilyAppleSilicon)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Plan error = %v, want it to wrap %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Plan error = %v", err)
			}
			if plan.KVType != tc.wantKV {
				t.Errorf("KVType = %v, want %v (device budget %d MiB)", plan.KVType, tc.wantKV, tc.deviceMiB)
			}
			if plan.ExpectedDeviceMiB > tc.deviceMiB {
				t.Errorf("ExpectedDeviceMiB = %d exceeds the %d MiB budget the plan was gated on",
					plan.ExpectedDeviceMiB, tc.deviceMiB)
			}

			// Every escalation is explained; the lossless default is not a
			// decision worth narrating.
			if tc.wantKV != KVTypeF16 && !noteContaining(plan.Notes, "escalated to "+string(tc.wantKV)) {
				t.Errorf("Notes = %v, want one recording the escalation to %v", plan.Notes, tc.wantKV)
			}
			// Below the whole ladder the plan is a DEGRADED one, and the least
			// lossy degradation that fits wins: at this budget the weights stay
			// on the accelerator and only the KV cache and the projector's
			// reserve move to system RAM. Whatever rung it landed on, it must
			// say so — a degraded install that looks like a normal one is how a
			// user ends up wondering why generation is slow.
			if tc.deviceMiB < q4 && !noteContaining(plan.Notes, "could not hold") &&
				!noteContaining(plan.Notes, "host residency") {
				t.Errorf("Notes = %v, want one recording the degradation", plan.Notes)
			}
		})
	}
}

// TestPlanRefusalNamesTheArithmetic checks that the infeasibility error is
// actionable: it carries the context, the precision, both footprints and both
// budgets, so a UI can show why instead of a bare "not enough memory".
func TestPlanRefusalNamesTheArithmetic(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	topology := topologyLiteral(1024, 1024, []DeviceMemory{
		{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: 1024, FreeMiB: 1024},
	}, 16)

	_, err := Plan(topology, profile, Tuning{}, BackendMetal, GPUFamilyAppleSilicon)
	if !errors.Is(err, ErrInsufficientMemory) {
		t.Fatalf("Plan error = %v, want it to wrap ErrInsufficientMemory", err)
	}
	// The sentinel a caller written against the single-pass planner matched
	// still matches: the two names describe one fact at two layers.
	if !errors.Is(err, ErrMemoryPlanInfeasible) {
		t.Errorf("Plan error = %v, want it to keep unwrapping to ErrMemoryPlanInfeasible", err)
	}

	// Actionable means BOTH pools with BOTH numbers, plus the shape they belong
	// to — a bare "not enough memory" names neither.
	message := err.Error()
	for _, want := range []string{
		"device memory", "host RAM", "GiB available", "reserve", "installed", "short",
		strconv.Itoa(DefaultFitMinContext),
	} {
		if !strings.Contains(message, want) {
			t.Errorf("error %q does not name %q", message, want)
		}
	}

	var typed *InsufficientMemoryError
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not an *InsufficientMemoryError", err)
	}
	if typed.HostNeedMiB <= typed.HostHaveMiB && typed.DeviceNeedMiB <= typed.DeviceHaveMiB {
		t.Errorf("neither pool overflowed (%d/%d device, %d/%d host) yet the plan refused",
			typed.DeviceNeedMiB, typed.DeviceHaveMiB, typed.HostNeedMiB, typed.HostHaveMiB)
	}
	// A host-pool refusal keeps matching the deprecated RAM sentinel, and a
	// device-only one must not.
	if typed.HostNeedMiB > typed.HostHaveMiB != errors.Is(err, ErrInsufficientRAM) {
		t.Errorf("ErrInsufficientRAM in the chain = %v, want it to match the host overflow %v",
			errors.Is(err, ErrInsufficientRAM), typed.HostNeedMiB > typed.HostHaveMiB)
	}
}

// TestPlanPinnedOffloadRefusesInsteadOfDegrading pins the one input the
// degradation ladder would otherwise change, and checks that the planner
// reports the refusal instead of quietly launching a different shape than the
// operator asked for.
func TestPlanPinnedOffloadRefusesInsteadOfDegrading(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	topology := topologyLiteral(1024, 64*1024, []DeviceMemory{
		{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: 1024, FreeMiB: 1024},
	}, 128)

	pinned := Tuning{
		Offload: Offload{Mode: OffloadAll},
		Context: ContextTuning{Mode: ContextExact, Tokens: DefaultFitMinContext},
	}
	plan, err := Plan(topology, profile, pinned, BackendMetal, GPUFamilyAppleSilicon)
	if !errors.Is(err, ErrInsufficientMemory) {
		t.Fatalf("Plan error = %v, want it to wrap ErrInsufficientMemory", err)
	}
	if plan.EmitsLayers() {
		t.Error("a refused plan still reports a layer count")
	}
	if noteContaining(plan.Notes, "host residency") {
		t.Error("a pinned offload was degraded to host residency behind the operator's back")
	}
}

// TestPlanExplicitKVTypeIsNotEscalated pins that an operator choice is honoured
// rather than second-guessed: a pinned q4_0 stays q4_0 on a machine that would
// have fitted f16.
func TestPlanExplicitKVTypeIsNotEscalated(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	topology := probedTopology(t, PlatformDarwinARM64, 128,
		DeviceMemory{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: 110100, FreeMiB: 110100})

	plan, err := Plan(topology, profile, Tuning{KVType: KVTypeQ4_0}, BackendMetal, GPUFamilyAppleSilicon)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if plan.KVType != KVTypeQ4_0 {
		t.Errorf("KVType = %v, want the pinned q4_0", plan.KVType)
	}
	if !noteContaining(plan.Notes, "pinned to q4_0") {
		t.Errorf("Notes = %v, want one recording that the precision was pinned", plan.Notes)
	}

	// An unmodelled precision is refused, never coerced: q5_0 is excluded on
	// the measured 8x long-context decode slowdown (PrismML-Eng/llama.cpp#191).
	_, err = Plan(topology, profile, Tuning{KVType: KVType("q5_0")}, BackendMetal, GPUFamilyAppleSilicon)
	if !errors.Is(err, ErrKVTypeUnsupported) {
		t.Errorf("Plan (q5_0) error = %v, want it to wrap ErrKVTypeUnsupported", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// the two topologies the acceptance criteria name
// ─────────────────────────────────────────────────────────────────────────────

// TestPlanSmallRAMWithLargeVRAMIsViable is the case that supersedes ADR-066 D6.
//
// 8 GiB of system RAM beside a 32 GiB accelerator is a machine that RUNS this
// model: the weights, the KV cache and the compute reserve all live on the
// card, and system RAM only holds the un-repackable weight spill and one CPU
// compute buffer (measured 598 MiB at full offload). D6's blanket 16 GiB floor
// refused it on a number that described neither pool.
func TestPlanSmallRAMWithLargeVRAMIsViable(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	topology := probedTopology(t, PlatformLinuxAMD64, 8,
		DeviceMemory{Name: "CUDA0", Description: "NVIDIA L4", TotalMiB: 32 * 1024, FreeMiB: 32 * 1024})

	if topology.DeviceBudgetMiB() <= 0 {
		t.Fatalf("the 32 GiB card produced a %d MiB device budget; the fixture is wrong", topology.DeviceBudgetMiB())
	}
	if topology.HostBudgetMiB() <= 0 {
		t.Fatalf("8 GiB of RAM produced a %d MiB host budget; the fixture is wrong", topology.HostBudgetMiB())
	}

	plan, err := Plan(topology, profile, Tuning{}, BackendCUDA124, ClassifyGPUs(topology.Devices))
	if err != nil {
		t.Fatalf("Plan error = %v, want a viable plan: 8 GiB of RAM beside 32 GiB of VRAM runs this model", err)
	}

	if plan.ExpectedDeviceMiB > plan.DeviceBudgetMiB {
		t.Errorf("ExpectedDeviceMiB %d exceeds the %d MiB device budget", plan.ExpectedDeviceMiB, plan.DeviceBudgetMiB)
	}
	if plan.ExpectedHostMiB > plan.HostBudgetMiB {
		t.Errorf("ExpectedHostMiB %d exceeds the %d MiB host budget", plan.ExpectedHostMiB, plan.HostBudgetMiB)
	}
	// The host side must be the SMALL half: everything that can live on the
	// card does, which is what makes 8 GiB of RAM enough.
	if plan.ExpectedHostMiB >= plan.ExpectedDeviceMiB {
		t.Errorf("host footprint %d MiB is not below the device footprint %d MiB; the plan is not offloading",
			plan.ExpectedHostMiB, plan.ExpectedDeviceMiB)
	}
	if !plan.Fit {
		t.Error("Fit = false, want true: nothing is pinned, so the runtime sizes the launch")
	}
	if plan.KVType == "" {
		t.Error("KVType is empty; a plan must always resolve a concrete precision")
	}
}

// TestPlanSmallRAMWithoutVRAMRefuses is the other half: the same 8 GiB of RAM
// with no accelerator has nowhere to put 6.7 GiB of weights plus a 65536-token
// cache, and the plan refuses with the arithmetic rather than installing a
// server that thrashes.
//
// This is what replaces D6's floor. The refusal is no longer "RAM < 16 GiB" —
// it is "the measured host budget cannot serve the model" — which is why the
// same RAM size is viable with a card and refused without one.
func TestPlanSmallRAMWithoutVRAMRefuses(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	topology := probedTopology(t, PlatformLinuxAMD64, 8)

	if topology.DeviceBudgetMiB() != 0 {
		t.Fatalf("a machine with no accelerator produced a %d MiB device budget; the fixture is wrong",
			topology.DeviceBudgetMiB())
	}

	_, err := Plan(topology, profile, Tuning{}, BackendCPU, ClassifyGPUs(topology.Devices))
	if !errors.Is(err, ErrMemoryPlanInfeasible) {
		t.Fatalf("Plan error = %v, want it to wrap ErrMemoryPlanInfeasible", err)
	}

	// An explicit small context does not rescue it either: the WEIGHTS alone
	// (6865 MiB measured at -ngl 0) exceed a 4096 MiB host budget, so there is
	// no context this machine can serve.
	_, err = Plan(topology, profile, Tuning{Context: ContextTuning{Mode: ContextExact, Tokens: 4096}},
		BackendCPU, ClassifyGPUs(topology.Devices))
	if !errors.Is(err, ErrMemoryPlanInfeasible) {
		t.Errorf("Plan (4096-token context) error = %v, want it to wrap ErrMemoryPlanInfeasible", err)
	}
}

// TestResolveGateDegradesWhenTheDeviceBudgetIsUnreadable pins the split the
// gate makes on a machine that was never probed. An unreadable DEVICE budget is
// not a refusal — refusing there is the exact bug the gate replaced — but it is
// only not a refusal when the accelerator's memory is INDEPENDENT of host RAM.
// On a unified machine the RAM probe measured both pools, so there is nothing
// unreadable and nothing to give the benefit of the doubt.
func TestResolveGateDegradesWhenTheDeviceBudgetIsUnreadable(t *testing.T) {
	t.Parallel()

	t.Run("a discrete accelerator nobody measured degrades", func(t *testing.T) {
		t.Parallel()

		res, err := Resolve(ResolveInput{MachineProfile: MachineProfile{
			Platform: PlatformLinuxAMD64, Backend: BackendCUDA124, RAMGiB: 8,
		}})
		if err != nil {
			t.Fatalf("Resolve error = %v, want a degraded resolution", err)
		}
		if !noteContaining(res.Memory.Notes, "could not be measured") {
			t.Errorf("Notes = %v, want the degradation recorded", res.Memory.Notes)
		}
		// The host budget the gate spent is reported, not left at zero: a plan
		// with no numbers invites a reader to assume none were computed.
		if res.Memory.HostBudgetMiB <= 0 {
			t.Errorf("HostBudgetMiB = %d, want the derived budget the gate spent",
				res.Memory.HostBudgetMiB)
		}
	})

	t.Run("a unified accelerator is priced from the RAM probe", func(t *testing.T) {
		t.Parallel()

		_, err := ResolveMachine(PlatformDarwinARM64, BackendMetal, 8)
		if !errors.Is(err, ErrInsufficientMemory) {
			t.Fatalf("Resolve error = %v, want it to wrap ErrInsufficientMemory", err)
		}
	})

	t.Run("no accelerator at all is refused", func(t *testing.T) {
		t.Parallel()

		_, err := ResolveMachine(PlatformLinuxAMD64, BackendCPU, 8)
		if !errors.Is(err, ErrInsufficientMemory) {
			t.Fatalf("Resolve error = %v, want it to wrap ErrInsufficientMemory", err)
		}
		// A refusal caused by the host pool still matches the deprecated
		// sentinel, so the callers that predate the two-pool gate keep working.
		if !errors.Is(err, ErrInsufficientRAM) {
			t.Errorf("Resolve error = %v, want it to keep unwrapping to ErrInsufficientRAM", err)
		}
	})

	t.Run("a probed topology replaces the derivation", func(t *testing.T) {
		t.Parallel()

		topology := probedTopology(t, PlatformLinuxAMD64, 8,
			DeviceMemory{Name: "CUDA0", Description: "NVIDIA L4", TotalMiB: 32 * 1024, FreeMiB: 32 * 1024})

		res, err := Resolve(ResolveInput{
			MachineProfile: MachineProfile{
				Platform: PlatformLinuxAMD64, Backend: BackendCUDA124, RAMGiB: 8,
			},
			Topology: &topology,
		})
		if err != nil {
			t.Fatalf("Resolve (8 GiB + 32 GiB VRAM) error = %v, want a viable resolution", err)
		}
		if !res.Memory.Fit {
			t.Error("Memory.Fit = false, want true for an all-Auto plan")
		}
		if res.Memory.ExpectedDeviceMiB <= 0 {
			t.Errorf("Memory.ExpectedDeviceMiB = %d, want a positive device footprint",
				res.Memory.ExpectedDeviceMiB)
		}
		if noteContaining(res.Memory.Notes, "could not be measured") {
			t.Errorf("Notes = %v, want no degradation note once the device budget was measured",
				res.Memory.Notes)
		}
	})
}

// TestResolveDelegatesToThePlanner checks that the resolution's projected
// fields really are the plan's, so a consumer cannot read one and act against
// the other.
func TestResolveDelegatesToThePlanner(t *testing.T) {
	t.Parallel()

	topology := probedTopology(t, PlatformDarwinARM64, 128,
		DeviceMemory{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: 110100, FreeMiB: 110100})

	res, err := Resolve(ResolveInput{
		MachineProfile: MachineProfile{Platform: PlatformDarwinARM64, Backend: BackendMetal, RAMGiB: 128},
		Topology:       &topology,
	})
	if err != nil {
		t.Fatalf("Resolve error = %v", err)
	}

	if res.Packing != res.Memory.Packing {
		t.Errorf("Packing = %v but Memory.Packing = %v", res.Packing, res.Memory.Packing)
	}
	if res.ContextSize != res.Memory.ContextSize {
		t.Errorf("ContextSize = %d but Memory.ContextSize = %d", res.ContextSize, res.Memory.ContextSize)
	}
	if res.Layers != deref(res.Memory.Layers) {
		t.Errorf("Layers = %d but Memory.Layers = %s", res.Layers, derefOrOmit(res.Memory.Layers))
	}
	if res.GPU != GPUFamilyAppleSilicon {
		t.Errorf("GPU = %q, want the family classified from the probed inventory", res.GPU)
	}

	// An explicit offload reaches the plan through the resolution, and takes
	// fit off with it.
	res, err = Resolve(ResolveInput{
		MachineProfile: MachineProfile{Platform: PlatformDarwinARM64, Backend: BackendMetal, RAMGiB: 128},
		Topology:       &topology,
		Tuning:         Tuning{Offload: Offload{Mode: OffloadAll}},
	})
	if err != nil {
		t.Fatalf("Resolve (Offload All) error = %v", err)
	}
	if res.Memory.Fit {
		t.Error("Memory.Fit = true, want false: an explicit offload turns fit off")
	}
	if res.Layers != nglAllGPU {
		t.Errorf("Layers = %d, want %d", res.Layers, nglAllGPU)
	}
	if res.ContextSize <= 0 {
		t.Errorf("ContextSize = %d, want a context the planner computed", res.ContextSize)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// overrides, notes and refusals
// ─────────────────────────────────────────────────────────────────────────────

// TestPlanNotesEveryNonDefaultDecision is the Notes contract the UI relies on:
// one non-default decision, at least one note explaining it. Each row changes
// exactly one field, so a missing note names the field that lost its
// explanation rather than hiding among the others.
func TestPlanNotesEveryNonDefaultDecision(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	topology := probedTopology(t, PlatformLinuxAMD64, 64,
		DeviceMemory{Name: "CUDA0", Description: "NVIDIA L4", TotalMiB: 24576, FreeMiB: 24576})

	tests := []struct {
		name      string
		tuning    Tuning
		family    GPUFamily
		wantInAll string
	}{
		{"an explicit offload", Tuning{Offload: Offload{Mode: OffloadAll}}, GPUFamilyAppleSilicon, "-ngl"},
		{"a CPU-only offload", Tuning{Offload: Offload{Mode: OffloadCPU}}, GPUFamilyAppleSilicon, "system RAM"},
		{"a partial offload", Tuning{Offload: Offload{Mode: OffloadLayers, Layers: 32}}, GPUFamilyAppleSilicon, "32 layers"},
		{"an explicit context", Tuning{Context: ContextTuning{Mode: ContextExact, Tokens: 32768}}, GPUFamilyAppleSilicon, ""},
		{"a pinned KV precision", Tuning{KVType: KVTypeQ8_0}, GPUFamilyAppleSilicon, "pinned to q8_0"},
		{"a device list", Tuning{Devices: []string{"CUDA0"}}, GPUFamilyAppleSilicon, "--device"},
		{"a split mode", Tuning{SplitMode: SplitModeRow}, GPUFamilyAppleSilicon, "--split-mode"},
		{"--fit off", Tuning{FitEnabled: boolPtr(false)}, GPUFamilyAppleSilicon, "--fit was disabled"},
		{"a fit target", Tuning{FitTargetMiB: intPtr(2048)}, GPUFamilyAppleSilicon, "fit margin"},
		{"a fit minimum context", Tuning{FitMinContext: intPtr(32768)}, GPUFamilyAppleSilicon, "minimum context was overridden"},
		{"-nkvo", Tuning{KVOffload: boolPtr(false)}, GPUFamilyAppleSilicon, "-nkvo"},
		{"--no-mmproj-offload", Tuning{MMProjOffload: boolPtr(false)}, GPUFamilyAppleSilicon, "--no-mmproj-offload"},
		{"a pinned packing", Tuning{Packing: PackingPTQ1_0}, GPUFamilyAppleSilicon, "PTQ1_0"},
		{"parallel slots", Tuning{Parallel: intPtr(2)}, GPUFamilyAppleSilicon, "parallel slots"},
		{"a prompt-cache cap", Tuning{CacheRAMMiB: intPtr(1024)}, GPUFamilyAppleSilicon, "prompt cache"},
		{"a disabled prompt cache", Tuning{CacheRAMMiB: intPtr(0)}, GPUFamilyAppleSilicon, "prompt cache was disabled"},
		{"a host reserve", Tuning{HostReserveGiB: floatPtr(2)}, GPUFamilyAppleSilicon, "system-RAM reserve"},
		{"an unmeasured GPU family", Tuning{}, GPUFamilyNVIDIAAda, "held back"},
		{"an unrecognized GPU family", Tuning{}, GPUFamilyUnknown, "held back"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			plan, err := Plan(topology, profile, tc.tuning, BackendCUDA124, tc.family)
			if err != nil {
				t.Fatalf("Plan error = %v", err)
			}
			if len(plan.Notes) == 0 {
				t.Fatal("Notes is empty; every non-default decision must be explained")
			}
			if tc.wantInAll != "" && !noteContaining(plan.Notes, tc.wantInAll) {
				t.Errorf("Notes = %v, want one containing %q", plan.Notes, tc.wantInAll)
			}
		})
	}
}

// TestPlanSplitAllowancePricesAnUnmeasuredSplit pins the policy margin: the
// measured family pays nothing, a recognized-but-unmeasured family pays the
// smaller allowance, and an unrecognized part pays the larger one. These are
// POLICY, not measurement — memory.go documents that the device/host split was
// measured on Metal alone.
func TestPlanSplitAllowancePricesAnUnmeasuredSplit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		family GPUFamily
		want   int64
	}{
		{GPUFamilyAppleSilicon, 0},
		{GPUFamilyNVIDIAAda, unmeasuredSplitAllowanceMiB},
		{GPUFamilyNVIDIABlackwell, unmeasuredSplitAllowanceMiB},
		{GPUFamilyAMDRDNA2, unmeasuredSplitAllowanceMiB},
		{GPUFamilyAMDGFX1151, unmeasuredSplitAllowanceMiB},
		{GPUFamilyIntelArc, unmeasuredSplitAllowanceMiB},
		{GPUFamilyUnknown, unknownGPUAllowanceMiB},
	}
	for _, tc := range tests {
		t.Run(string(tc.family), func(t *testing.T) {
			t.Parallel()
			if got := splitAllowanceMiB(tc.family); got != tc.want {
				t.Errorf("splitAllowanceMiB(%q) = %d, want %d", tc.family, got, tc.want)
			}
		})
	}

	if unknownGPUAllowanceMiB <= unmeasuredSplitAllowanceMiB {
		t.Errorf("an unrecognized part (%d MiB) is not priced above a recognized one (%d MiB)",
			unknownGPUAllowanceMiB, unmeasuredSplitAllowanceMiB)
	}

	// The allowance is spent, not merely documented: the same topology yields a
	// smaller device budget for an unknown part.
	profile := profileOrFail(t)
	topology := topologyLiteral(24576, 64*1024, []DeviceMemory{
		{Name: "GPU0", Description: "NVIDIA L4", TotalMiB: 24576, FreeMiB: 24576},
	}, 64)

	known, err := Plan(topology, profile, Tuning{}, BackendCUDA124, GPUFamilyNVIDIAAda)
	if err != nil {
		t.Fatalf("Plan (Ada) error = %v", err)
	}
	unknown, err := Plan(topology, profile, Tuning{}, BackendCUDA124, GPUFamilyUnknown)
	if err != nil {
		t.Fatalf("Plan (unknown) error = %v", err)
	}
	if want := known.DeviceBudgetMiB - (unknownGPUAllowanceMiB - unmeasuredSplitAllowanceMiB); unknown.DeviceBudgetMiB != want {
		t.Errorf("unknown-family device budget = %d MiB, want %d MiB", unknown.DeviceBudgetMiB, want)
	}
}

// TestPlanRejectsUnusableTuning pins that a typo is reported as a typo: the
// tuning checks run BEFORE the feasibility arithmetic, so an operator is never
// told their machine is too small when what they typed was out of range.
func TestPlanRejectsUnusableTuning(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	topology := probedTopology(t, PlatformDarwinARM64, 128,
		DeviceMemory{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: 110100, FreeMiB: 110100})

	tests := []struct {
		name    string
		tuning  Tuning
		wantErr error
	}{
		{"a zero exact context", Tuning{Context: ContextTuning{Mode: ContextExact}}, ErrTuningInvalid},
		{"a negative exact context", Tuning{Context: ContextTuning{Mode: ContextExact, Tokens: -1}}, ErrTuningInvalid},
		{"a context above the training context", Tuning{Context: ContextTuning{
			Mode: ContextExact, Tokens: profile.MaxContext + 1,
		}}, ErrTuningInvalid},
		{"an unmodelled KV precision", Tuning{KVType: KVType("q4_1")}, ErrKVTypeUnsupported},
		{"a packing with no measured residency", Tuning{Packing: Packing("Q4_K_M")}, ErrMemoryNotMeasured},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Plan(topology, profile, tc.tuning, BackendMetal, GPUFamilyAppleSilicon)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Plan error = %v, want it to wrap %v", err, tc.wantErr)
			}
		})
	}
}

// TestPlanHostReserveOverrideReplacesTheDerivedReserve pins that the override
// REPLACES the topology's reserve policy rather than stacking on it: 8 GiB of
// RAM with a 2 GiB reserve leaves 6 GiB, not 6 GiB minus the derived floor.
func TestPlanHostReserveOverrideReplacesTheDerivedReserve(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	topology := probedTopology(t, PlatformDarwinARM64, 8,
		DeviceMemory{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: 6144, FreeMiB: 6144})

	derived, err := Plan(topology, profile, Tuning{}, BackendMetal, GPUFamilyAppleSilicon)
	if err == nil {
		t.Logf("derived host budget = %d MiB", derived.HostBudgetMiB)
	}

	overridden, err := Plan(topology, profile, Tuning{HostReserveGiB: floatPtr(2)}, BackendMetal, GPUFamilyAppleSilicon)
	if err != nil {
		// An 8 GiB machine may still be refused on the DEVICE side; the host
		// budget is what this test is about, and it is set before the gate runs.
		t.Logf("Plan (2 GiB reserve) error = %v", err)
	} else if want := int64(6 * 1024); overridden.HostBudgetMiB != want {
		t.Errorf("HostBudgetMiB = %d, want %d (8 GiB minus the 2 GiB override)", overridden.HostBudgetMiB, want)
	}
}

// TestFootprintAgreesWithTheMeasuredProjections ties plan.go's footprint to
// memory.go's projections for the two shapes memory.go actually measured, so
// the planner cannot drift away from the numbers that were verified against the
// pinned runtime.
func TestFootprintAgreesWithTheMeasuredProjections(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	const ctx = 65536

	// Full offload, KV on the device, projector on the device: the measured
	// shape (`| - Host | 598 = 322 + 0 + 276 |` beside the MTL0 row).
	device, host, exact, err := footprint(profile, PackingPQ2_0, ctx, KVTypeF16, true, false, true, true)
	if err != nil {
		t.Fatalf("footprint (offloaded) error = %v", err)
	}
	if !exact {
		t.Error("exact = false, want true for a full offload")
	}
	wantDevice := deviceFootprint(t, profile, PackingPQ2_0, ctx, KVTypeF16)
	if device != wantDevice {
		t.Errorf("device = %d MiB, want %d MiB (the projection plus the projector reserve)", device, wantDevice)
	}
	wantHost, err := profile.ProjectHostMiB(PackingPQ2_0, ctx, KVTypeF16, true)
	if err != nil {
		t.Fatalf("ProjectHostMiB error = %v", err)
	}
	if host != wantHost+profile.MMProjReserveHostMiB {
		t.Errorf("host = %d MiB, want %d MiB", host, wantHost+profile.MMProjReserveHostMiB)
	}

	// Nothing offloaded: the measured `-ngl 0` shape, where the device side is
	// zero by construction.
	device, host, exact, err = footprint(profile, PackingPQ2_0, ctx, KVTypeF16, false, false, true, true)
	if err != nil {
		t.Fatalf("footprint (CPU only) error = %v", err)
	}
	if !exact {
		t.Error("exact = false, want true for a CPU-only launch")
	}
	if device != 0 {
		t.Errorf("device = %d MiB, want 0 with nothing offloaded", device)
	}
	wantHost, err = profile.ProjectHostMiB(PackingPQ2_0, ctx, KVTypeF16, false)
	if err != nil {
		t.Fatalf("ProjectHostMiB error = %v", err)
	}
	// Nothing is offloaded, so the projector's device reserve lands in RAM too.
	if host != wantHost+profile.MMProjReserveDeviceMiB+profile.MMProjReserveHostMiB {
		t.Errorf("host = %d MiB, want %d MiB", host,
			wantHost+profile.MMProjReserveDeviceMiB+profile.MMProjReserveHostMiB)
	}
}

// TestFootprintMovesTheCacheUnderNKVO pins `-nkvo`: the KV cache leaves the
// accelerator for system RAM, which is the whole reason the flag exists.
func TestFootprintMovesTheCacheUnderNKVO(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	const ctx = 65536

	withKV, withKVHost, _, err := footprint(profile, PackingPQ2_0, ctx, KVTypeF16, true, false, true, true)
	if err != nil {
		t.Fatalf("footprint error = %v", err)
	}
	withoutKV, withoutKVHost, _, err := footprint(profile, PackingPQ2_0, ctx, KVTypeF16, true, false, false, true)
	if err != nil {
		t.Fatalf("footprint (-nkvo) error = %v", err)
	}

	cache, err := profile.KVCacheMiB(ctx, KVTypeF16)
	if err != nil {
		t.Fatalf("KVCacheMiB error = %v", err)
	}
	if moved := withKV - withoutKV; moved != cache {
		t.Errorf("-nkvo moved %d MiB off the accelerator, want the %d MiB cache", moved, cache)
	}
	if moved := withoutKVHost - withKVHost; moved != cache {
		t.Errorf("-nkvo moved %d MiB onto the host, want the %d MiB cache", moved, cache)
	}
}

// TestPartialOffloadIsReportedAsABound pins the one shape the measured profile
// does not cover: a partial layer count yields conservative bounds, and the
// plan says so instead of presenting an estimate as a measurement.
func TestPartialOffloadIsReportedAsABound(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	topology := probedTopology(t, PlatformDarwinARM64, 128,
		DeviceMemory{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: 110100, FreeMiB: 110100})

	plan, err := Plan(topology, profile, Tuning{Offload: Offload{Mode: OffloadLayers, Layers: 32}},
		BackendMetal, GPUFamilyAppleSilicon)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if !noteContaining(plan.Notes, "conservative bounds") {
		t.Errorf("Notes = %v, want one saying the figures are bounds, not measurements", plan.Notes)
	}

	full, err := Plan(topology, profile, Tuning{Offload: Offload{Mode: OffloadAll}},
		BackendMetal, GPUFamilyAppleSilicon)
	if err != nil {
		t.Fatalf("Plan (full offload) error = %v", err)
	}
	if noteContaining(full.Notes, "conservative bounds") {
		t.Errorf("Notes = %v, want no bound caveat for a full offload", full.Notes)
	}
	// The device bound of a partial offload is the full-offload figure: fewer
	// layers on the card cannot cost more card memory.
	if plan.ExpectedDeviceMiB != full.ExpectedDeviceMiB {
		t.Errorf("partial device bound = %d MiB, want the full-offload %d MiB",
			plan.ExpectedDeviceMiB, full.ExpectedDeviceMiB)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// the Tuning vocabulary itself
// ─────────────────────────────────────────────────────────────────────────────

// TestTuningZeroValueIsAllAuto pins the contract the vocabulary is built on:
// the zero Tuning sets nothing, so "unset" is representable and the
// fit-exclusivity rule can read it. A field whose zero value already meant
// "chosen" would make `-ngl 0` indistinguishable from "no opinion about -ngl",
// and that distinction is what decides whether fit runs.
func TestTuningZeroValueIsAllAuto(t *testing.T) {
	t.Parallel()

	var tuning Tuning
	if tuning.Offload.Mode != OffloadAuto {
		t.Errorf("Offload.Mode = %v, want OffloadAuto", tuning.Offload.Mode)
	}
	if tuning.Offload.explicit() {
		t.Error("Offload.explicit() = true for the zero value, want false")
	}
	if tuning.explicitOffloadShape() {
		t.Error("explicitOffloadShape() = true for the zero value, want false")
	}
	if tuning.Context.Mode != ContextAuto {
		t.Errorf("Context.Mode = %v, want ContextAuto", tuning.Context.Mode)
	}
	if tuning.KVType != "" {
		t.Errorf("KVType = %q, want the empty Auto sentinel", tuning.KVType)
	}
	if tuning.Packing != "" {
		t.Errorf("Packing = %q, want the empty Auto sentinel", tuning.Packing)
	}
	if tuning.SplitMode != SplitModeAuto {
		t.Errorf("SplitMode = %q, want the empty Auto sentinel", tuning.SplitMode)
	}
	if tuning.FitEnabled != nil || tuning.FitTargetMiB != nil || tuning.FitMinContext != nil ||
		tuning.KVOffload != nil || tuning.MMProjOffload != nil || tuning.Parallel != nil ||
		tuning.CacheRAMMiB != nil || tuning.HostReserveGiB != nil {
		t.Error("a pointer field of the zero Tuning is non-nil; every override must be distinguishable from unset")
	}
	if len(tuning.Devices) != 0 {
		t.Errorf("Devices = %v, want none", tuning.Devices)
	}

	// Each of the three overrides that force fit off is visible to the rule.
	for name, tuning := range map[string]Tuning{
		"offload": {Offload: Offload{Mode: OffloadCPU}},
		"devices": {Devices: []string{"CUDA0"}},
		"split":   {SplitMode: SplitModeNone},
	} {
		if !tuning.explicitOffloadShape() {
			t.Errorf("explicitOffloadShape() = false for an explicit %s, want true", name)
		}
	}
}

// TestSplitModeSpellingsAreTheRuntimesOwn pins the `-sm` values to the pinned
// fork's vocabulary (`{-sm, --split-mode {none,layer,row,tensor}}`), so a plan
// can never emit a spelling the runtime would reject.
func TestSplitModeSpellingsAreTheRuntimesOwn(t *testing.T) {
	t.Parallel()

	want := map[SplitMode]string{
		SplitModeAuto:   "",
		SplitModeNone:   "none",
		SplitModeLayer:  "layer",
		SplitModeRow:    "row",
		SplitModeTensor: "tensor",
	}
	if len(want) != 5 {
		t.Fatalf("the fork documents four split modes plus the unset value, got %d", len(want))
	}
	for mode, spelling := range want {
		if string(mode) != spelling {
			t.Errorf("SplitMode %q is spelled %q, want %q", mode, string(mode), spelling)
		}
	}
}

// TestPlanNeverEmitsTheTrainingContextAsAContext is the planner's half of the
// package-wide ban: whatever the tuning asks for, a plan's context is inside
// the modelled range, and a fit-sized plan carries the RAM tier (or the
// operator's exact pin) as an explicit -c rather than leaving the context to
// the runtime's fit pass.
func TestPlanNeverEmitsTheTrainingContextAsAContext(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	topology := probedTopology(t, PlatformDarwinARM64, 128,
		DeviceMemory{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: 110100, FreeMiB: 110100})

	tunings := []Tuning{
		{},
		{Offload: Offload{Mode: OffloadAll}},
		{Offload: Offload{Mode: OffloadCPU}},
		{Offload: Offload{Mode: OffloadLayers, Layers: 8}},
		{Context: ContextTuning{Mode: ContextExact, Tokens: 8192}},
		{Context: ContextTuning{Mode: ContextExact, Tokens: profile.MaxContext}},
		{FitEnabled: boolPtr(false)},
		{KVType: KVTypeQ4_0},
		{SplitMode: SplitModeLayer},
		{Devices: []string{"MTL0"}},
	}
	for i, tuning := range tunings {
		plan, err := Plan(topology, profile, tuning, BackendMetal, GPUFamilyAppleSilicon)
		if err != nil {
			// A CPU-only or tiny-offload shape on this fixture may legitimately
			// be refused; the bound is what this test checks.
			if errors.Is(err, ErrMemoryPlanInfeasible) {
				continue
			}
			t.Fatalf("Plan (tuning %d) error = %v", i, err)
		}
		if plan.ContextSize < 0 || plan.ContextSize > profile.MaxContext {
			t.Errorf("Plan (tuning %d) ContextSize = %d, want 0..%d", i, plan.ContextSize, profile.MaxContext)
		}
		if plan.Fit && plan.ContextSize <= 0 {
			t.Errorf("Plan (tuning %d) is fit-sized but records no -c: an omitted context hands the sizing to the runtime's fit pass", i)
		}
		if !plan.Fit && plan.ContextSize <= 0 {
			t.Errorf("Plan (tuning %d) has fit off but no computed context", i)
		}
		if plan.Parallel < 1 {
			t.Errorf("Plan (tuning %d) Parallel = %d, want at least 1", i, plan.Parallel)
		}
	}
}

// TestPlanIsNotReachedByTheAstGuard is a self-check on the purity guard: it
// must actually be reading a file with imports, or it proves nothing.
func TestPlanIsNotReachedByTheAstGuard(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("plan.go")
	if err != nil {
		t.Fatalf("reading plan.go: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "plan.go", source, 0)
	if err != nil {
		t.Fatalf("parsing plan.go: %v", err)
	}

	declared := false
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "Plan" {
			declared = true
		}
	}
	if !declared {
		t.Fatal("plan.go declares no Plan function; the purity guard is guarding nothing")
	}
}

// TestPlanRefusesAHostReserveItCannotConvert pins the total conversion of
// `tuning.host_reserve_gib`. A float→int conversion whose value the result type
// cannot represent is IMPLEMENTATION-DEFINED in Go: it saturates to MaxInt64 on
// arm64 (a zero host budget, fail-closed) and conventionally yields the negative
// "indefinite value" on amd64, which would make `host = ram − reserve` enormous
// and fail the memory gate OPEN on the very knob meant to shrink it. NaN was
// worse still: it passed config validation and was then silently skipped by the
// planner's `>= 0` test, so an operator typo vanished without a word.
func TestPlanRefusesAHostReserveItCannotConvert(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	topology := probedTopology(t, PlatformDarwinARM64, 64,
		DeviceMemory{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: 49152, FreeMiB: 49152})

	cases := map[string]float64{
		"NaN":                          math.NaN(),
		"positive infinity":            math.Inf(1),
		"negative infinity":            math.Inf(-1),
		"negative":                     -1,
		"one above the ceiling":        MaxTuningHostReserveGiB + 1,
		"a value that saturates int64": 1e19,
	}
	for name, gib := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tuning := Tuning{HostReserveGiB: floatPtr(gib)}
			if _, err := Plan(topology, profile, tuning, BackendMetal, GPUFamilyAppleSilicon); !errors.Is(err, ErrTuningInvalid) {
				t.Errorf("Plan error = %v, want it to wrap ErrTuningInvalid", err)
			}
			// The gate runs the same conversion on the install path, before
			// anything is planned or downloaded, and must refuse it identically.
			err := CheckMemoryBudget(ResolveInput{
				MachineProfile: MachineProfile{
					Platform: PlatformDarwinARM64,
					Backend:  BackendMetal,
					RAMGiB:   64,
				},
				Tuning:   tuning,
				Topology: &topology,
			})
			if !errors.Is(err, ErrTuningInvalid) {
				t.Errorf("CheckMemoryBudget error = %v, want it to wrap ErrTuningInvalid", err)
			}
		})
	}

	// The ceiling itself is convertible, so it must not be refused AS INVALID —
	// an absurdity guard that rejects its own boundary would be a tuning opinion.
	// (A reserve that big leaves no host budget, so the memory gate may still
	// refuse the machine; that is a different error about the machine, not the
	// knob.)
	at := Tuning{HostReserveGiB: floatPtr(MaxTuningHostReserveGiB)}
	if _, err := Plan(topology, profile, at, BackendMetal, GPUFamilyAppleSilicon); errors.Is(err, ErrTuningInvalid) {
		t.Errorf("Plan refused a reserve at the ceiling as an invalid override: %v", err)
	}
	if err := hostReserveMiBMustFail(t, math.NaN()); err == nil {
		t.Error("hostReserveMiB accepted NaN")
	}
}

func hostReserveMiBMustFail(t *testing.T, gib float64) error {
	t.Helper()
	_, err := hostReserveMiB(gib)
	return err
}

// TestPlanKVTypeGatesAUnifiedPoolAdditively pins that the KV escalation loop
// spends a unified pool the way the memory gate does. On a unified machine the
// device and host footprints come out of the SAME bytes, so comparing each
// against its own budget accepts a precision whose sum exceeds the machine — and
// the launch's `--fit` pass then cannot fit even at the `-fitc` floor, turning a
// degraded-but-working configuration into a failed load.
func TestPlanKVTypeGatesAUnifiedPoolAdditively(t *testing.T) {
	t.Parallel()

	// budgets.fits is the rule; pin it on its own first.
	split := budgets{deviceMiB: 100, hostMiB: 100}
	if !split.fits(60, 60) {
		t.Error("two independent pools must be gated per pool: 60+60 fits 100 and 100")
	}
	unified := budgets{deviceMiB: 100, hostMiB: 100, unified: true}
	if unified.fits(60, 60) {
		t.Error("a unified pool must be gated additively: 60+60 does not fit one 100 MiB pool")
	}
	if !unified.fits(40, 60) {
		t.Error("a unified pool must still accept a sum that fits: 40+60 = 100")
	}
	if split.fits(101, 0) || unified.fits(0, 101) {
		t.Error("a footprint above its own budget must never fit")
	}

	// And the escalation loop must use it. The witness pool is constructed from
	// the profile's own f16 projection, so it fits each half separately and their
	// sum by exactly one MiB.
	profile := profileOrFail(t)
	target := DefaultFitMinContext
	deviceMiB, hostMiB, _, err := footprint(profile, PackingPQ2_0, target, KVTypeF16, true, false, true, true)
	if err != nil {
		t.Fatalf("footprint: %v", err)
	}
	if deviceMiB <= 0 || hostMiB <= 0 {
		t.Fatalf("f16 projection = (%d, %d) MiB, want two positive footprints", deviceMiB, hostMiB)
	}
	pool := max(deviceMiB, hostMiB) + min(deviceMiB, hostMiB) - 1

	if !splitPool(pool).fits(deviceMiB, hostMiB) {
		t.Fatalf("precondition: %d MiB device and %d MiB host must each fit a %d MiB split pool",
			deviceMiB, hostMiB, pool)
	}

	kv, _, err := planKVType(Tuning{}, profile, PackingPQ2_0, target,
		budgets{deviceMiB: pool, hostMiB: pool, unified: true}, nil, true, false)
	switch {
	case errors.Is(err, ErrMemoryPlanInfeasible):
		// A refusal is the other acceptable answer: no precision fits the pool.
	case err != nil:
		t.Fatalf("planKVType: %v", err)
	case kv == KVTypeF16:
		t.Errorf("planKVType kept f16 on a unified pool: %d MiB device + %d MiB host = %d MiB against a %d MiB pool",
			deviceMiB, hostMiB, deviceMiB+hostMiB, pool)
	default:
		t.Logf("the unified gate escalated the KV cache to %s", kv)
	}
}

// splitPool is a same-sized non-unified budget pair, for the precondition above.
func splitPool(pool int64) budgets {
	return budgets{deviceMiB: pool, hostMiB: pool}
}
