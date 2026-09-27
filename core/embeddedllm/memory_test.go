package embeddedllm

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The measurement the whole file is checked against
// ---------------------------------------------------------------------------

// measuredRun is one row of the run matrix captured on 2026-09-25 with the
// pinned fork release `prism-b10735-842b188` (`llama-server --version` reports
// "0.2.0-dev (build 10735, commit 842b18804)") on an Apple M4 Max (Metal device
// `MTL0`, 110100 MiB), against the pinned `Ternary-Bonsai-2-27B-PQ2_0.gguf`.
//
// The command line is c0wrk's own launch shape minus the flags the memory model
// does not depend on:
//
//	llama-server -v -m Ternary-Bonsai-2-27B-PQ2_0.gguf --host 127.0.0.1 \
//	  -ngl <99|0> -fa on -c 262144 -np 1 --no-webui [-ctk T -ctv T]
//
// wantDevice is the fork's own dry-run fit projection
// (`common_params_fit_impl: projected to use N MiB of device memory`, echoed by
// the `common_memory_breakdown_print` MTL0 row); wantHost is the LOADED pass's
// Host row, printed at shutdown. See the "Measurement provenance" block in
// memory.go for why the two come from different passes.
type measuredRun struct {
	name       string
	packing    Packing
	kv         KVType
	offloaded  bool
	wantKV     int64 // llama_kv_cache: size = <wantKV>.00 MiB
	wantDevice int64 // projected device self total
	wantHost   int64 // loaded Host self total
	source     string
}

// measuredMatrix is the complete run matrix. Every row is a verbatim reading of
// a llama.cpp log line from the runs above; `source` quotes it.
var measuredMatrix = []measuredRun{
	{
		name: "PQ2_0, full offload, f16 cache", packing: PackingPQ2_0, kv: KVTypeF16, offloaded: true,
		wantKV: 16384, wantDevice: 24450, wantHost: 598,
		source: "llama_kv_cache: size = 16384.00 MiB (262144 cells, 16 layers, 1/1 seqs), K (f16): 8192.00 MiB, V (f16): 8192.00 MiB | " +
			"| - MTL0 (Apple M4 Max) | 110100 = 109950 + (24450 = 6539 + 16533 + 1377) + -24300 | | " +
			"projected to use 24450 MiB of device memory | | - Host | 598 = 322 + 0 + 276 |",
	},
	{
		name: "PQ2_0, full offload, q8_0 cache", packing: PackingPQ2_0, kv: KVTypeQ8_0, offloaded: true,
		wantKV: 8704, wantDevice: 16782, wantHost: 598,
		source: "llama_kv_cache: size = 8704.00 MiB (262144 cells, 16 layers, 1/1 seqs), K (q8_0): 4352.00 MiB, V (q8_0): 4352.00 MiB | " +
			"| - MTL0 (Apple M4 Max) | 110100 = 109950 + (16782 = 6539 + 8853 + 1389) + -16632 | | " +
			"projected to use 16782 MiB of device memory | | - Host | 598 = 322 + 0 + 276 |",
	},
	{
		name: "PQ2_0, full offload, q4_0 cache", packing: PackingPQ2_0, kv: KVTypeQ4_0, offloaded: true,
		wantKV: 4608, wantDevice: 12686, wantHost: 598,
		source: "llama_kv_cache: size = 4608.00 MiB (262144 cells, 16 layers, 1/1 seqs), K (q4_0): 2304.00 MiB, V (q4_0): 2304.00 MiB | " +
			"| - MTL0 (Apple M4 Max) | 110100 = 109950 + (12686 = 6539 + 4757 + 1389) + -12536 | | " +
			"projected to use 12686 MiB of device memory | | - Host | 598 = 322 + 0 + 276 |",
	},
	{
		name: "PQ2_0, no offload, f16 cache", packing: PackingPQ2_0, kv: KVTypeF16, offloaded: false,
		wantKV: 16384, wantDevice: 0, wantHost: 23789,
		source: "projected to use 0 MiB of device memory | " +
			"| - MTL0 (Apple M4 Max) | 110100 = 110100 + (0 = 0 + 0 + 0) + 0 | | " +
			"| - Host | 23789 = 6865 + 16533 + 390 |",
	},
	{
		name: "PTQ1_0, full offload, f16 cache", packing: PackingPTQ1_0, kv: KVTypeF16, offloaded: true,
		wantKV: 16384, wantDevice: 23306, wantHost: 541,
		source: "| - MTL0 (Apple M4 Max) | 110100 = 109950 + (23306 = 5395 + 16533 + 1377) + -23156 | | " +
			"projected to use 23306 MiB of device memory | dry-run | - Host | 817 = 265 + 0 + 552 |, whose " +
			"552 is the reserve pass counted twice (2 x 276.02), so the single-buffer host figure is 265 + 276 = 541",
	},
	{
		name: "PTQ1_0, no offload, f16 cache", packing: PackingPTQ1_0, kv: KVTypeF16, offloaded: false,
		wantKV: 16384, wantDevice: 0, wantHost: 22588,
		source: "projected to use 0 MiB of device memory | " +
			"| - Host | 22588 = 5664 + 16533 + 390 | | load_tensors: CPU_Mapped model buffer size = 5660.56 MiB",
	},
}

// measuredContextColumn is the dry-run breakdown's `context` column: the KV
// cache plus the recurrent state, as llama.cpp prints it (each column floored).
// It is the independent check on RecurrentStateMiB — the profile stores the
// CEILING of 149.62, so the floor (149) is what reproduces the printed column.
var measuredContextColumn = map[KVType]int64{
	KVTypeF16:  16533, // (24450 = 6539 + 16533 + 1377)
	KVTypeQ8_0: 8853,  // (16782 = 6539 +  8853 + 1389)
	KVTypeQ4_0: 4757,  // (12686 = 6539 +  4757 + 1389)
}

// measuredRecurrentStateFloorMiB is floor(149.62), the recurrent state as the
// breakdown's context column carries it.
// MEASURED 2026-09-25: `llama_memory_recurrent: size = 149.62 MiB (1 cells,
// 64 layers, 1 seqs 0 rs_seq), R (f32): 5.62 MiB, S (f32): 144.00 MiB`.
const measuredRecurrentStateFloorMiB = 149

// mustProfile fails the test if the pinned profile cannot be built.
func mustProfile(t *testing.T) ModelMemoryProfile {
	t.Helper()
	profile, err := PinnedMemoryProfile()
	if err != nil {
		t.Fatalf("PinnedMemoryProfile: %v", err)
	}
	return profile
}

// ---------------------------------------------------------------------------
// KVType: exactly three precisions, closed set
// ---------------------------------------------------------------------------

func TestKVTypeSetIsExactlyThree(t *testing.T) {
	t.Parallel()

	got := KVTypes()
	want := []KVType{KVTypeF16, KVTypeQ8_0, KVTypeQ4_0}
	if len(got) != len(want) {
		t.Fatalf("KVTypes() returned %d precisions (%v), want exactly %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("KVTypes()[%d] = %q, want %q", i, got[i], want[i])
		}
		if !got[i].Valid() {
			t.Errorf("KVType %q reports itself invalid", got[i])
		}
	}

	// The returned slice must be a copy: mutating it may not reach the package
	// table.
	got[0] = KVType("mutated")
	if KVTypes()[0] != KVTypeF16 {
		t.Error("KVTypes() exposed the package table for mutation")
	}
}

// TestKVTypeRejectsUnmodelledPrecisions is the closed-set guard. `q5_0` is
// excluded on performance (PrismML-Eng/llama.cpp#191: pp 11.4 / tg 4.0 t/s
// against f16's pp 343.5 / tg 31.8 on the same hardware, model and prompt,
// reproduced after a full reboot, and it buys no capacity — a 73728 context
// ceiling against q4_0's 72960). `q4_1`, `iq4_nl` and `q5_1` are excluded as
// unverified. None of them may be coerced to a modelled precision.
func TestKVTypeRejectsUnmodelledPrecisions(t *testing.T) {
	t.Parallel()

	rejected := []string{
		"q5_0", // the #191 performance bug
		"q4_1", // unverified on this model
		"iq4_nl",
		"q5_1",
		"f32",
		"q8_1",
		"q2_k",
		"",
		"none",
		"default",
		"f16 q8_0",
		"q4_00",
	}

	profile := mustProfile(t)
	for _, s := range rejected {
		t.Run("reject/"+s, func(t *testing.T) {
			t.Parallel()

			parsed, err := ParseKVType(s)
			if !errors.Is(err, ErrKVTypeUnsupported) {
				t.Fatalf("ParseKVType(%q) error = %v, want ErrKVTypeUnsupported", s, err)
			}
			if parsed.Valid() {
				t.Errorf("ParseKVType(%q) returned the valid KVType %q", s, parsed)
			}
			if _, ok := profile.KVDivisor[KVType(s)]; ok {
				t.Errorf("KVDivisor carries an entry for the rejected precision %q", s)
			}
			if _, err := profile.KVCacheMiB(1024, KVType(s)); !errors.Is(err, ErrKVTypeUnsupported) {
				t.Errorf("KVCacheMiB(1024, %q) error = %v, want ErrKVTypeUnsupported", s, err)
			}
		})
	}
}

func TestParseKVTypeAcceptsTheModelledPrecisions(t *testing.T) {
	t.Parallel()

	for _, want := range KVTypes() {
		// llama.cpp spells these lowercase on the command line; the parser is
		// tolerant of the casing and of surrounding whitespace, but never of a
		// different precision.
		for _, spelling := range []string{string(want), strings.ToUpper(string(want)), "  " + string(want) + "\n"} {
			got, err := ParseKVType(spelling)
			if err != nil {
				t.Fatalf("ParseKVType(%q): %v", spelling, err)
			}
			if got != want {
				t.Errorf("ParseKVType(%q) = %q, want %q", spelling, got, want)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The divisor table: derived from block geometry, exact against measurement
// ---------------------------------------------------------------------------

func TestKVDivisorsMatchBlockGeometry(t *testing.T) {
	t.Parallel()

	profile := mustProfile(t)

	if len(profile.KVDivisor) != len(kvTypes) {
		t.Fatalf("KVDivisor has %d entries, want one per modelled precision (%d)",
			len(profile.KVDivisor), len(kvTypes))
	}
	for _, kv := range kvTypes {
		divisor, ok := profile.KVDivisor[kv]
		if !ok {
			t.Fatalf("KVDivisor has no entry for %q", kv)
		}
		if divisor <= 0 {
			t.Fatalf("KVDivisor[%q] = %v, want a positive ratio", kv, divisor)
		}

		var want float64
		switch kv {
		case KVTypeF16:
			want = 1
		case KVTypeQ8_0:
			// A q8_0 block is 32 values x 8 bits plus one fp16 scale: 8.5 bits
			// per value, so the ratio is 32/17, NOT the nominal 2. Spelled out
			// as a literal fraction rather than via kvDivisor, so the assertion
			// is not a restatement of the implementation.
			want = float64(32) / float64(17)
		case KVTypeQ4_0:
			// 32 values x 4 bits plus one fp16 scale: 4.5 bits per value. The
			// docs' "roughly 3.5x" rounds to 3.556, which projects 4607 MiB
			// for a cache that measures 4608.00 — only the exact ratio lands.
			want = float64(32) / float64(9)
		}
		if divisor != want {
			t.Errorf("KVDivisor[%q] = %v, want %v", kv, divisor, want)
		}
	}

	// The ratios the block geometry implies, spelled out so a change to the
	// constants above cannot silently move them.
	if got := kvDivisor(8); got != float64(kvBlockValues*kvF16ValueBits)/float64(kvBlockValues*8+kvBlockScaleBits) {
		t.Errorf("kvDivisor(8) = %v, want 32/17", got)
	}
	if got := kvDivisor(4); got != float64(kvBlockValues*kvF16ValueBits)/float64(kvBlockValues*4+kvBlockScaleBits) {
		t.Errorf("kvDivisor(4) = %v, want 32/9", got)
	}
}

// ---------------------------------------------------------------------------
// KVCacheMiB: exact against the measured cache sizes
// ---------------------------------------------------------------------------

func TestKVCacheMiBMatchesMeasuredSizes(t *testing.T) {
	t.Parallel()

	profile := mustProfile(t)

	cases := []struct {
		ctx  int
		kv   KVType
		want int64
	}{
		// The measured matrix at the model's full training context.
		{ctx: 262144, kv: KVTypeF16, want: 16384},
		{ctx: 262144, kv: KVTypeQ8_0, want: 8704},
		{ctx: 262144, kv: KVTypeQ4_0, want: 4608},
		// Half and quarter context: the cache is linear in tokens, so these
		// are the measured full-context figures halved and quartered.
		{ctx: 131072, kv: KVTypeF16, want: 8192},
		{ctx: 131072, kv: KVTypeQ8_0, want: 4352},
		{ctx: 131072, kv: KVTypeQ4_0, want: 2304},
		{ctx: 65536, kv: KVTypeF16, want: 4096},
		{ctx: 65536, kv: KVTypeQ4_0, want: 1152},
		// A non-power-of-two context: 100000 * 65536 / 2^20 = 6250 exactly.
		{ctx: 100000, kv: KVTypeF16, want: 6250},
		{ctx: 1, kv: KVTypeF16, want: 0}, // 64 KiB rounds to 0 whole MiB
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s@%d", tc.kv, tc.ctx), func(t *testing.T) {
			t.Parallel()

			got, err := profile.KVCacheMiB(tc.ctx, tc.kv)
			if err != nil {
				t.Fatalf("KVCacheMiB(%d, %q): %v", tc.ctx, tc.kv, err)
			}
			if got != tc.want {
				t.Errorf("KVCacheMiB(%d, %q) = %d MiB, want %d MiB", tc.ctx, tc.kv, got, tc.want)
			}
		})
	}
}

// TestKVCachePlusRecurrentStateReproducesContextColumn cross-checks
// RecurrentStateMiB against a second, independent reading of the same runs: the
// dry-run breakdown's `context` column, which is the KV cache plus the
// recurrent state. The column is floored, so the floor of the measured 149.62
// is what must reproduce it.
func TestKVCachePlusRecurrentStateReproducesContextColumn(t *testing.T) {
	t.Parallel()

	profile := mustProfile(t)

	for kv, wantColumn := range measuredContextColumn {
		t.Run(string(kv), func(t *testing.T) {
			t.Parallel()

			cache, err := profile.KVCacheMiB(profile.MaxContext, kv)
			if err != nil {
				t.Fatalf("KVCacheMiB: %v", err)
			}
			if got := cache + measuredRecurrentStateFloorMiB; got != wantColumn {
				t.Errorf("KV %d MiB + recurrent %d MiB = %d, want the measured context column %d",
					cache, measuredRecurrentStateFloorMiB, got, wantColumn)
			}
		})
	}
}

func TestKVCacheMiBRejectsContextsOutsideTheModelledRange(t *testing.T) {
	t.Parallel()

	profile := mustProfile(t)

	for _, ctx := range []int{0, -1, profile.MaxContext + 1, 1 << 20} {
		if _, err := profile.KVCacheMiB(ctx, KVTypeF16); !errors.Is(err, ErrContextOutOfRange) {
			t.Errorf("KVCacheMiB(%d, f16) error = %v, want ErrContextOutOfRange", ctx, err)
		}
	}
	// The ceiling itself is modelled.
	if _, err := profile.KVCacheMiB(profile.MaxContext, KVTypeF16); err != nil {
		t.Errorf("KVCacheMiB(MaxContext) = %v, want success", err)
	}
}

// ---------------------------------------------------------------------------
// The projections against the measured run matrix
// ---------------------------------------------------------------------------

func TestProjectionMatchesMeasuredMatrix(t *testing.T) {
	t.Parallel()

	profile := mustProfile(t)

	for _, run := range measuredMatrix {
		t.Run(run.name, func(t *testing.T) {
			t.Parallel()

			// The KV term on its own, so a total that drifts points at the term
			// that drifted.
			cache, err := profile.KVCacheMiB(profile.MaxContext, run.kv)
			if err != nil {
				t.Fatalf("KVCacheMiB: %v", err)
			}
			if cache != run.wantKV {
				t.Errorf("KVCacheMiB(%d, %q) = %d MiB, want the measured %d MiB\n  source: %s",
					profile.MaxContext, run.kv, cache, run.wantKV, run.source)
			}

			device, err := profile.ProjectDeviceMiB(run.packing, profile.MaxContext, run.kv, run.offloaded)
			if err != nil {
				t.Fatalf("ProjectDeviceMiB: %v", err)
			}
			if device != run.wantDevice {
				t.Errorf("ProjectDeviceMiB(%q, %d, %q, %v) = %d MiB, want the measured %d MiB\n  source: %s",
					run.packing, profile.MaxContext, run.kv, run.offloaded, device, run.wantDevice, run.source)
			}

			host, err := profile.ProjectHostMiB(run.packing, profile.MaxContext, run.kv, run.offloaded)
			if err != nil {
				t.Fatalf("ProjectHostMiB: %v", err)
			}
			if host != run.wantHost {
				t.Errorf("ProjectHostMiB(%q, %d, %q, %v) = %d MiB, want the measured %d MiB\n  source: %s",
					run.packing, profile.MaxContext, run.kv, run.offloaded, host, run.wantHost, run.source)
			}
		})
	}
}

// TestProjectionScalesWithContext checks the projections move in the right
// direction away from the single measured context: a longer context costs more
// device memory, a coarser cache costs less, and the recurrent state keeps the
// device figure positive even at a one-token context.
func TestProjectionScalesWithContextAndPrecision(t *testing.T) {
	t.Parallel()

	profile := mustProfile(t)

	project := func(ctx int, kv KVType) int64 {
		t.Helper()
		got, err := profile.ProjectDeviceMiB(PackingPQ2_0, ctx, kv, true)
		if err != nil {
			t.Fatalf("ProjectDeviceMiB(%d, %q): %v", ctx, kv, err)
		}
		return got
	}

	if smaller, larger := project(65536, KVTypeF16), project(262144, KVTypeF16); smaller >= larger {
		t.Errorf("device projection did not grow with context: %d MiB at 65536 vs %d MiB at 262144",
			smaller, larger)
	}

	f16 := project(profile.MaxContext, KVTypeF16)
	for _, kv := range []KVType{KVTypeQ8_0, KVTypeQ4_0} {
		if quantised := project(profile.MaxContext, kv); quantised >= f16 {
			t.Errorf("%s projection %d MiB is not below the f16 projection %d MiB",
				kv, quantised, f16)
		}
	}
	if q4 := project(profile.MaxContext, KVTypeQ4_0); q4 >= project(profile.MaxContext, KVTypeQ8_0) {
		t.Errorf("q4_0 projection %d MiB is not below the q8_0 projection", q4)
	}

	if got := project(1, KVTypeF16); got <= 0 {
		t.Errorf("device projection at a 1-token context = %d MiB, want a positive figure", got)
	}
}

// TestProjectDeviceMiBIsZeroWithoutOffload pins the `-ngl 0` shape (the Intel
// Mac and the CPU build): nothing is placed on the accelerator, so the device
// projection is 0 for every precision and context, and the whole footprint
// moves to the host projection.
func TestProjectDeviceMiBIsZeroWithoutOffload(t *testing.T) {
	t.Parallel()

	profile := mustProfile(t)

	for _, kv := range KVTypes() {
		for _, ctx := range []int{1024, 65536, profile.MaxContext} {
			got, err := profile.ProjectDeviceMiB(PackingPQ2_0, ctx, kv, false)
			if err != nil {
				t.Fatalf("ProjectDeviceMiB(%d, %q, offloaded=false): %v", ctx, kv, err)
			}
			if got != 0 {
				t.Errorf("ProjectDeviceMiB(%d, %q, offloaded=false) = %d MiB, want 0", ctx, kv, got)
			}
		}
	}
}

// TestProjectionCoversEveryPinnedPackingAndRefusesOthers is the fail-closed
// guard on the residency maps: every packing the registry pins must have a
// measurement behind it (an unmeasured packing would make the gate blind on
// exactly the platform that selects it), and a packing the registry does not
// pin must be refused rather than projected from its file size.
func TestProjectionCoversEveryPinnedPackingAndRefusesOthers(t *testing.T) {
	t.Parallel()

	profile := mustProfile(t)

	for _, packing := range SupportedPackings() {
		if _, ok := profile.DeviceWeightsMiB[packing]; !ok {
			t.Errorf("DeviceWeightsMiB has no measured entry for the pinned packing %q", packing)
		}
		if _, ok := profile.HostWeightsMiB[packing]; !ok {
			t.Errorf("HostWeightsMiB has no measured entry for the pinned packing %q", packing)
		}
		if _, ok := profile.HostWeightSpillMiB[packing]; !ok {
			t.Errorf("HostWeightSpillMiB has no measured entry for the pinned packing %q", packing)
		}
		if _, err := profile.ProjectDeviceMiB(packing, profile.MaxContext, KVTypeF16, true); err != nil {
			t.Errorf("ProjectDeviceMiB(%q) = %v, want success", packing, err)
		}
		if _, err := profile.ProjectHostMiB(packing, profile.MaxContext, KVTypeF16, false); err != nil {
			t.Errorf("ProjectHostMiB(%q) = %v, want success", packing, err)
		}
	}

	// An unpinned packing is refused on both projections, and the refusal does
	// not depend on the offload flag reaching the residency lookup.
	unpinned := Packing("Q4_K_M")
	if _, err := profile.ProjectDeviceMiB(unpinned, profile.MaxContext, KVTypeF16, true); !errors.Is(err, ErrMemoryNotMeasured) {
		t.Errorf("ProjectDeviceMiB(%q) error = %v, want ErrMemoryNotMeasured", unpinned, err)
	}
	if _, err := profile.ProjectHostMiB(unpinned, profile.MaxContext, KVTypeF16, false); !errors.Is(err, ErrMemoryNotMeasured) {
		t.Errorf("ProjectHostMiB(%q, offloaded=false) error = %v, want ErrMemoryNotMeasured", unpinned, err)
	}
	if _, err := profile.ProjectHostMiB(unpinned, profile.MaxContext, KVTypeF16, true); !errors.Is(err, ErrMemoryNotMeasured) {
		t.Errorf("ProjectHostMiB(%q, offloaded=true) error = %v, want ErrMemoryNotMeasured", unpinned, err)
	}

	// A bad shape is reported before the packing lookup matters.
	if _, err := profile.ProjectDeviceMiB(PackingPQ2_0, 0, KVTypeF16, true); !errors.Is(err, ErrContextOutOfRange) {
		t.Errorf("ProjectDeviceMiB(ctx=0) error = %v, want ErrContextOutOfRange", err)
	}
	if _, err := profile.ProjectDeviceMiB(PackingPQ2_0, profile.MaxContext, KVType("q5_0"), false); !errors.Is(err, ErrKVTypeUnsupported) {
		t.Errorf("ProjectDeviceMiB(q5_0) error = %v, want ErrKVTypeUnsupported", err)
	}
	if _, err := profile.ProjectHostMiB(PackingPQ2_0, profile.MaxContext, KVType("q5_0"), true); !errors.Is(err, ErrKVTypeUnsupported) {
		t.Errorf("ProjectHostMiB(q5_0) error = %v, want ErrKVTypeUnsupported", err)
	}
}

// TestProjectHostMiBAtFullOffloadIgnoresContext pins the loaded-pass host row:
// once every layer is offloaded, no part of the KV cache or the recurrent state
// lives in system RAM, so the host figure is the same 598 MiB at every context
// and precision.
func TestProjectHostMiBAtFullOffloadIgnoresContext(t *testing.T) {
	t.Parallel()

	profile := mustProfile(t)

	for _, kv := range KVTypes() {
		for _, ctx := range []int{1024, 65536, profile.MaxContext} {
			got, err := profile.ProjectHostMiB(PackingPQ2_0, ctx, kv, true)
			if err != nil {
				t.Fatalf("ProjectHostMiB(%d, %q, offloaded=true): %v", ctx, kv, err)
			}
			want := profile.HostWeightSpillMiB[PackingPQ2_0] + profile.ComputeHostMiB
			if got != want {
				t.Errorf("ProjectHostMiB(%d, %q, offloaded=true) = %d MiB, want %d", ctx, kv, got, want)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Every constant, against its measurement
// ---------------------------------------------------------------------------

// TestEveryConstantMatchesItsMeasurement is the table the acceptance criterion
// asks for: one row per constant, the measured value, and the log line or
// upstream source the value came from. A constant that drifts without a new
// measurement fails here with its own provenance in the message.
func TestEveryConstantMatchesItsMeasurement(t *testing.T) {
	t.Parallel()

	profile := mustProfile(t)

	cases := []struct {
		name   string
		got    int64
		want   int64
		source string
	}{
		{
			name: "HostWeightSpillMiB[PQ2_0]", got: profile.HostWeightSpillMiB[PackingPQ2_0], want: 322,
			source: "2026-09-25, fork prism-b10735-842b188: load_tensors: CPU_Mapped model buffer size = 322.07 MiB; " +
				"dry-run | - Host | 874 = 322 + 0 + 552 | under f16, q8_0 and q4_0",
		},
		{
			name: "HostWeightSpillMiB[PTQ1_0]", got: profile.HostWeightSpillMiB[PackingPTQ1_0], want: 265,
			source: "2026-09-25, fork prism-b10735-842b188: dry-run | - Host | 817 = 265 + 0 + 552 |",
		},
		{
			name: "RecurrentStateMiB", got: profile.RecurrentStateMiB, want: 150,
			source: "2026-09-25, fork prism-b10735-842b188: llama_memory_recurrent: size = 149.62 MiB " +
				"(1 cells, 64 layers, 1 seqs 0 rs_seq), R (f32): 5.62 MiB, S (f32): 144.00 MiB; " +
				"identical under f16/q8_0/q4_0 and at -c 262144/131072/65536 (context-independent)",
		},
		{
			name: "ComputeDeviceMiB", got: profile.ComputeDeviceMiB, want: 1377,
			source: "2026-09-25, fork prism-b10735-842b188: sched_reserve: MTL0 compute buffer size = 1377.52 MiB (-np 1, -c 262144, f16)",
		},
		{
			name: "KVQuantComputeExtraMiB", got: profile.KVQuantComputeExtraMiB, want: 12,
			source: "2026-09-25, fork prism-b10735-842b188: sched_reserve: MTL0 compute buffer size = 1389.03 MiB under q8_0 AND q4_0, " +
				"against 1377.52 MiB for f16 (delta 11.51, rounded up)",
		},
		{
			name: "ComputeHostMiB", got: profile.ComputeHostMiB, want: 276,
			source: "2026-09-25, fork prism-b10735-842b188: sched_reserve: CPU compute buffer size = 276.02 MiB (f16) / 276.28 MiB (q8_0, q4_0); " +
				"~llama_context: CPU compute buffer size is 276.0176 MiB, matches expectation",
		},
		{
			name: "ComputeHostCPUOnlyMiB", got: profile.ComputeHostCPUOnlyMiB, want: 390,
			source: "2026-09-25, fork prism-b10735-842b188, -ngl 0: sched_reserve: CPU compute buffer size = 390.02 MiB; " +
				"~llama_context: CPU compute buffer size is 390.0176 MiB, matches expectation",
		},
		{
			name: "MMProjReserveDeviceMiB", got: profile.MMProjReserveDeviceMiB, want: 849,
			source: "2026-09-25, fork prism-b10735-842b188 with --mmproj: [mtmd] estimated worst-case memory usage of mmproj is 873.10 MiB; " +
				"[mtmd] adding 848.18 MiB to fit_params_target for device MTL0",
		},
		{
			name: "MMProjReserveHostMiB", got: profile.MMProjReserveHostMiB, want: 25,
			source: "2026-09-25, fork prism-b10735-842b188 with --mmproj: [mtmd] adding 24.93 MiB to fit_params_target for device CPU",
		},
		{
			name: "KVBytesPerTokenF16", got: profile.KVBytesPerTokenF16, want: 65536,
			source: "2026-09-25, fork prism-b10735-842b188: llama_kv_cache: size = 16384.00 MiB (262144 cells, 16 layers) -> exactly 64 KiB/token; " +
				"corroborated by PrismML-Eng/Bonsai-demo README.md (\"The FP16 KV cache costs 64 KiB per token\") and KV-CACHE.md",
		},
		{
			name: "KVValuesPerLayerToken", got: profile.KVValuesPerLayerToken, want: 2048,
			source: "2026-09-25, fork prism-b10735-842b188: print_info: n_embd_k_gqa = 1024 and n_embd_v_gqa = 1024",
		},
		{
			name: "MaxContext", got: int64(profile.MaxContext), want: 262144,
			source: "2026-09-25, fork prism-b10735-842b188: print_info: n_ctx_train = 262144 (and n_ctx_orig_yarn = 262144); " +
				"PrismML-Eng/Bonsai-demo README.md: \"The 27B models support up to 262,144 tokens of context\"",
		},
		{
			name: "LayerCount", got: int64(profile.LayerCount), want: 64,
			source: "2026-09-25, fork prism-b10735-842b188: print_info: n_layer = 64, n_layer_all = 64; " +
				"corroborated by llama_memory_recurrent: (1 cells, 64 layers, ...)",
		},
		{
			name: "OffloadableLayers", got: int64(profile.OffloadableLayers), want: 65,
			source: "2026-09-25, fork prism-b10735-842b188: -ngl 65 and -ngl 99 produce identical projections (MTL0 6539 16533 1377); " +
				"PrismML-Eng/llama.cpp#191 describes the same model as \"all 65/65 layers offloaded to GPU\"",
		},
		{
			name: "FullAttentionLayers", got: int64(profile.FullAttentionLayers), want: 16,
			source: "2026-09-25, fork prism-b10735-842b188: llama_kv_cache: size = 16384.00 MiB (262144 cells, 16 layers, 1/1 seqs); " +
				"the llama_memory_recurrent layer trace keeps 0,1,2 / skips 3 / keeps 4,5,6 / skips 7 ... (1 in 4 of 64)",
		},
		{
			name: "DeviceWeightsMiB[PQ2_0]", got: profile.DeviceWeightsMiB[PackingPQ2_0], want: 6539,
			source: "2026-09-25, fork prism-b10735-842b188: | - MTL0 (Apple M4 Max) | 110100 = 109950 + (24450 = 6539 + 16533 + 1377) + -24300 |",
		},
		{
			name: "DeviceWeightsMiB[PTQ1_0]", got: profile.DeviceWeightsMiB[PackingPTQ1_0], want: 5395,
			source: "2026-09-25, fork prism-b10735-842b188: | - MTL0 (Apple M4 Max) | 110100 = 109950 + (23306 = 5395 + 16533 + 1377) + -23156 |",
		},
		{
			name: "HostWeightsMiB[PQ2_0]", got: profile.HostWeightsMiB[PackingPQ2_0], want: 6865,
			source: "2026-09-25, fork prism-b10735-842b188, -ngl 0, loaded pass: | - Host | 23789 = 6865 + 16533 + 390 |",
		},
		{
			name: "HostWeightsMiB[PTQ1_0]", got: profile.HostWeightsMiB[PackingPTQ1_0], want: 5664,
			source: "2026-09-25, fork prism-b10735-842b188, -ngl 0, loaded pass: | - Host | 22588 = 5664 + 16533 + 390 |",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.got != tc.want {
				t.Errorf("%s = %d, want %d\n  measured source: %s", tc.name, tc.got, tc.want, tc.source)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Derived, never duplicated
// ---------------------------------------------------------------------------

// TestKVBytesPerTokenIsDerivedFromGeometry ties the per-token cost to the
// model's own shape, so the figure cannot drift away from the layer counts the
// same run reported.
func TestKVBytesPerTokenIsDerivedFromGeometry(t *testing.T) {
	t.Parallel()

	profile := mustProfile(t)

	const bytesPerF16Value = 2
	want := int64(profile.FullAttentionLayers) * profile.KVValuesPerLayerToken * bytesPerF16Value
	if profile.KVBytesPerTokenF16 != want {
		t.Errorf("KVBytesPerTokenF16 = %d, want FullAttentionLayers * KVValuesPerLayerToken * 2 = %d * %d * 2 = %d",
			profile.KVBytesPerTokenF16, profile.FullAttentionLayers, profile.KVValuesPerLayerToken, want)
	}

	if profile.FullAttentionLayers <= 0 || profile.FullAttentionLayers >= profile.LayerCount {
		t.Errorf("FullAttentionLayers = %d, want strictly between 0 and LayerCount = %d",
			profile.FullAttentionLayers, profile.LayerCount)
	}
	if profile.OffloadableLayers != profile.LayerCount+1 {
		t.Errorf("OffloadableLayers = %d, want LayerCount + 1 = %d",
			profile.OffloadableLayers, profile.LayerCount+1)
	}
}

// TestWeightsMiBDerivedFromRegistry proves the on-disk figures come from the
// registry's exact byte counts rather than being re-typed here.
func TestWeightsMiBDerivedFromRegistry(t *testing.T) {
	t.Parallel()

	profile := mustProfile(t)

	packings := SupportedPackings()
	if len(packings) == 0 {
		t.Fatal("SupportedPackings() is empty")
	}
	if len(profile.WeightsMiB) != len(packings) {
		t.Fatalf("WeightsMiB has %d entries, want one per supported packing (%d)",
			len(profile.WeightsMiB), len(packings))
	}

	for _, packing := range packings {
		asset, ok := ModelAsset(packing)
		if !ok {
			t.Fatalf("ModelAsset(%q) is not pinned", packing)
		}
		want := mibCeil(asset.SizeBytes)
		if got := profile.WeightsMiB[packing]; got != want {
			t.Errorf("WeightsMiB[%q] = %d MiB, want ceil(SizeBytes/2^20) = %d", packing, got, want)
		}
	}

	mmproj := MMProjAsset()
	if want := mibCeil(mmproj.SizeBytes); profile.MMProjMiB != want {
		t.Errorf("MMProjMiB = %d MiB, want ceil(SizeBytes/2^20) = %d", profile.MMProjMiB, want)
	}
}

// TestNoRegistryByteSizeIsRetypedInMemoryGo is the source-level form of the
// same rule: memory.go must not contain a single one of the registry's exact
// byte counts, in a comment or in code. A pin bump changes those numbers in
// registry.go alone.
func TestNoRegistryByteSizeIsRetypedInMemoryGo(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("memory.go")
	if err != nil {
		t.Fatalf("reading memory.go: %v", err)
	}
	text := string(source)

	sizes := map[string]int64{"the vision projector": MMProjAsset().SizeBytes}
	for _, packing := range SupportedPackings() {
		asset, ok := ModelAsset(packing)
		if !ok {
			t.Fatalf("ModelAsset(%q) is not pinned", packing)
		}
		sizes["model "+string(packing)] = asset.SizeBytes
	}
	for _, platform := range SupportedPlatforms() {
		for _, backend := range []Backend{
			BackendCPU, BackendMetal, BackendVulkan, BackendROCm,
			BackendCUDA124, BackendCUDA128, BackendCUDA133,
		} {
			if asset, ok := RuntimeAsset(platform, backend); ok {
				sizes["runtime "+platform+"/"+string(backend)] = asset.SizeBytes
			}
			if asset, ok := CudartAsset(platform, backend); ok {
				sizes["cudart "+platform+"/"+string(backend)] = asset.SizeBytes
			}
		}
	}

	if len(sizes) < 10 {
		t.Fatalf("collected only %d registry sizes; the scan would be vacuous", len(sizes))
	}
	for what, size := range sizes {
		if literal := strconv.FormatInt(size, 10); strings.Contains(text, literal) {
			t.Errorf("memory.go re-types the registry byte size of %s (%s); derive it from the registry instead",
				what, literal)
		}
	}
}

// TestDevicePlusSpillNeverExceedsTheFile keeps the two weight terms honest
// against the third: what the runtime actually resident (device + host spill)
// must fit inside the file, and the gap is the GGUF metadata and tensor-info
// table that is never loaded into a compute buffer.
func TestDevicePlusSpillNeverExceedsTheFile(t *testing.T) {
	t.Parallel()

	profile := mustProfile(t)

	for packing, device := range profile.DeviceWeightsMiB {
		file, ok := profile.WeightsMiB[packing]
		if !ok {
			t.Fatalf("WeightsMiB has no entry for the measured packing %q", packing)
		}
		spill, ok := profile.HostWeightSpillMiB[packing]
		if !ok {
			t.Fatalf("HostWeightSpillMiB has no entry for the measured packing %q", packing)
		}

		resident := device + spill
		if resident > file {
			t.Errorf("packing %q: device %d MiB + host spill %d MiB = %d MiB exceeds the %d MiB file",
				packing, device, spill, resident, file)
		}
		// Measured gaps: 6873 - (6539 + 322) = 12 MiB for PQ2_0 and
		// 5671 - (5395 + 265) = 11 MiB for PTQ1_0 — the GGUF metadata and
		// tensor-info table, which is never loaded into a compute buffer. A
		// much larger gap would mean the residency figures no longer describe
		// the artifact the registry pins.
		if gap := file - resident; gap < 0 || gap > 64 {
			t.Errorf("packing %q: file %d MiB - resident %d MiB = %d MiB of unaccounted overhead, want 0..64",
				packing, file, resident, gap)
		}

		// The non-offloaded host residency is the same weights plus the small
		// host-side buffers the loaded pass also counts. Measured: 6865 =
		// 6539 + 322 + 4 and 5664 = 5395 + 265 + 4.
		host, ok := profile.HostWeightsMiB[packing]
		if !ok {
			t.Fatalf("HostWeightsMiB has no entry for the measured packing %q", packing)
		}
		if extra := host - resident; extra < 0 || extra > 16 {
			t.Errorf("packing %q: host weights %d MiB - (device %d + spill %d) = %d MiB of extra host buffers, want 0..16",
				packing, host, device, spill, extra)
		}
	}
}

// TestMibCeilRoundsUp pins the rounding direction every budget in the profile
// depends on.
func TestMibCeilRoundsUp(t *testing.T) {
	t.Parallel()

	cases := []struct {
		bytes int64
		want  int64
	}{
		{bytes: 0, want: 0},
		{bytes: -1, want: 0},
		{bytes: 1, want: 1},
		{bytes: bytesPerMiB - 1, want: 1},
		{bytes: bytesPerMiB, want: 1},
		{bytes: bytesPerMiB + 1, want: 2},
	}
	for _, tc := range cases {
		if got := mibCeil(tc.bytes); got != tc.want {
			t.Errorf("mibCeil(%d) = %d, want %d", tc.bytes, got, tc.want)
		}
	}
}
