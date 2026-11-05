package embeddedllm

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

// bytesPerMiB is the unit every figure here is expressed in — llama.cpp
// prints its own memory breakdown in MiB.
const bytesPerMiB = int64(1) << 20

// maxTrainingContext is the pinned model's training context, 262,144 tokens —
// a CEILING the projections validate against, never a launch value
// (`resolve.go` stops at `contextTierTop`, half of this). It is written as a
// shift, and this file's comments spell numbers with thousands separators
// ("262,144"), because `TestPackageSourcesNeverEmitBannedContext` bans the
// bare decimal literal from every non-test source in the package.
const maxTrainingContext = 1 << 18

// Every constant below was MEASURED on 2026-09-25 against the pinned fork
// release and the pinned weights (SHA256 re-verified first), on ONE machine
// and ONE backend (Metal), text-only. The full provenance — the reference
// command line, the two non-interchangeable per-run totals (the dry-run fit
// pass vs the loaded pass) and the Metal-only limitation — is recorded in
// specs/domains/embedded-llm.md (Memory model); the projections here
// reproduce the measured matrix exactly (TestProjectionMatchesMeasuredMatrix).
//
// MEASURED 2026-11-03: one more tuple exists — the CUDA overlay below
// (NVIDIA Blackwell; its provenance and evidence live in
// docs/development/embedded-llm-cuda-measurements.md). Still priced from the
// Metal figures: ROCm, Vulkan and CPU, CUDA on every other NVIDIA family,
// PTQ1_0 and q4_0 on CUDA, and the CPU-only shape of a CUDA machine.

// KVType is the KV-cache precision — ONE value passed to BOTH `-ctk` and
// `-ctv`, because MIXED K/V types silently fall back to CPU flash attention
// (PrismML-Eng/llama.cpp#267), and one value for both makes that shape
// unrepresentable. The set is deliberately closed — `q5_0` excluded on the
// #191 long-context decode perf bug, `q4_1`/`iq4_nl`/`q5_1` excluded as
// unverified — and an unlisted precision is a refusal (`ErrKVTypeUnsupported`),
// never a silent fallback to `f16`. See specs/domains/embedded-llm.md
// (Memory model).
type KVType string

const (
	// KVTypeF16 is the default lossless baseline every other precision is
	// measured against; measured cache 16384.00 MiB at 262,144 tokens.
	KVTypeF16 KVType = "f16"
	// KVTypeQ8_0 halves the cache to within 6% at f16-identical throughput
	// (#191); measured cache 8704.00 MiB.
	KVTypeQ8_0 KVType = "q8_0"
	// KVTypeQ4_0 is the fork's own long-context option (`BONSAI_KV4`, ~3.5x
	// smaller) and the one #85 reports as mandatory for a 262k context on a
	// 16 GB card, at f16-matched throughput; measured cache 4608.00 MiB.
	KVTypeQ4_0 KVType = "q4_0"
)

// kvTypes is the exhaustive, ordered set of modelled precisions.
var kvTypes = []KVType{KVTypeF16, KVTypeQ8_0, KVTypeQ4_0}

// longContextKVThreshold is the target context above which the planner starts
// the KV-precision ladder at q8_0 instead of f16 (see planKVType).
const longContextKVThreshold = DefaultFitMinContext

// GGUF quantisation-block geometry: both quantised cache types store one fp16
// scale per block of 32 values, so a value costs MORE than its nominal width —
// 8.5 bits (q8_0, divisor 32/17) and 4.5 bits (q4_0, 32/9). The derived
// divisors reproduce the measured cache sizes exactly, the check
// `TestKVDivisorsMatchBlockGeometry` makes; see specs/domains/embedded-llm.md.
const (
	kvBlockValues    = 32
	kvBlockScaleBits = 16
	kvF16ValueBits   = 16
)

// ErrKVTypeUnsupported reports a KV-cache precision this model does not cover:
// either one deliberately excluded (see KVType) or one that does not exist.
var ErrKVTypeUnsupported = errors.New("unsupported embedded-LLM KV cache type")

// ErrMemoryNotMeasured is a refusal, not a fallback: projecting an unmeasured
// packing from its file size would guess at the device/host split, and a gate
// built on that guess could pass a configuration that does not fit.
var ErrMemoryNotMeasured = errors.New("no measured memory residency for this packing")

// ErrContextOutOfRange reports a context length the profile cannot project:
// non-positive, or above the model's own training context.
var ErrContextOutOfRange = errors.New("context size outside the modelled range")

// KVTypes returns the modelled KV-cache precisions in a stable order.
func KVTypes() []KVType { return slices.Clone(kvTypes) }

// ParseKVType maps a `--cache-type-k`/`--cache-type-v` spelling onto a KVType;
// anything outside the closed set is refused, never coerced to a default.
func ParseKVType(s string) (KVType, error) {
	t := KVType(strings.TrimSpace(strings.ToLower(s)))
	for _, known := range kvTypes {
		if t == known {
			return t, nil
		}
	}
	return "", fmt.Errorf("%w: %q (supported: %s)", ErrKVTypeUnsupported, s,
		strings.Join(kvTypeNames(), ", "))
}

// Valid reports whether the KVType is one of the three modelled precisions.
func (t KVType) Valid() bool { return slices.Contains(kvTypes, t) }

// kvTypeNames renders the supported set for diagnostics.
func kvTypeNames() []string {
	names := make([]string, 0, len(kvTypes))
	for _, t := range kvTypes {
		names = append(names, string(t))
	}
	return names
}

// ModelMemoryProfile is the measured per-term memory model of the ONE model
// c0wrk pins (Ternary-Bonsai-2-27B) — a description, not a policy: nothing
// here decides a context size or refuses an install. Every field is in MiB;
// the measurement behind each value (log line, date, fork pin) is tabulated in
// specs/domains/embedded-llm.md (Memory model).
type ModelMemoryProfile struct {
	// WeightsMiB is the ON-DISK size of each pinned GGUF, derived from the
	// registry's exact `Asset.SizeBytes` at call time (never re-typed here)
	// and rounded up — the disk cost, deliberately NOT the residency term the
	// projections use (~11 MiB of GGUF metadata never loads).
	WeightsMiB map[Packing]int64

	// MMProjMiB is the ON-DISK size of the pinned vision projector, derived
	// the same way from `MMProjAsset().SizeBytes`.
	MMProjMiB int64

	// DeviceWeightsMiB is the weight residency on the ACCELERATOR at full
	// offload, measured from the dry-run fit breakdown's model column
	// (PQ2_0 6539, PTQ1_0 5395). A packing with no entry is refused with
	// ErrMemoryNotMeasured, never projected from a file size.
	DeviceWeightsMiB map[Packing]int64

	// HostWeightsMiB is the weight residency in SYSTEM RAM at `-ngl 0`,
	// measured from the loaded Host row (PQ2_0 6865, PTQ1_0 5664) — ~4 MiB
	// above device + spill, the small host-side buffers the loaded pass
	// counts.
	HostWeightsMiB map[Packing]int64

	// HostWeightSpillMiB is the weight residency that stays in system RAM even
	// at full offload — the tensors the runtime cannot repack onto the
	// accelerator (PQ2_0 322, PTQ1_0 265). Per-packing BECAUSE it is the same
	// tensors at different bit widths; one scalar would over-count PTQ1_0 by
	// 57 MiB.
	HostWeightSpillMiB map[Packing]int64

	// RecurrentStateMiB is the SSM/GDN linear-attention state (R/S buffers):
	// 149.62 → 150, an UNDOCUMENTED term no upstream table carries. Context-
	// and KV-type-independent — byte-identical under all three precisions and
	// at 262,144/131072/65536 tokens. Stored rounded up. Re-measured
	// byte-identical on CUDA/Blackwell, so the one figure serves both shapes.
	RecurrentStateMiB int64

	// ComputeDeviceMiB is the accelerator-side compute reserve for the
	// reference shape (`-np 1`, default batch, f16, full offload): 1377.52 →
	// 1377, stored as the FLOOR while RecurrentStateMiB keeps its ceiling —
	// llama.cpp floors each breakdown column but rounds the total once from
	// the exact sum, and 150 + 1377 is exactly the measured 1527 (storing two
	// ceilings would project 24451 for a run the runtime reports as 24450).
	// NOT constant across the space: it grows with the physical batch and
	// shrinks with context; the quantised delta lives in
	// KVQuantComputeExtraMiB. METAL-ONLY — the CUDA tuple reads the measured
	// curves below instead.
	ComputeDeviceMiB int64

	// KVQuantComputeExtraMiB is the dequantisation scratch a QUANTISED cache
	// adds to the device compute reserve: 1389.03 measured under both q8_0
	// and q4_0 against f16's 1377.52 → 12 rounded up. Never read by the CUDA
	// shape — there the cost lives inside the curves.
	KVQuantComputeExtraMiB int64

	// ComputeHostMiB is the system-RAM compute reserve at full offload:
	// 276.02 → 276, the same whole MiB under every precision. METAL-ONLY —
	// on CUDA it grows with context and KV quantisation (52 → 84 → 148), so
	// the CUDA shape reads the curves below.
	ComputeHostMiB int64

	// ComputeHostCPUOnlyMiB is the system-RAM compute reserve when NOTHING is
	// offloaded and the whole graph runs on the CPU: 390.02 → 390. The dry-run
	// Host row prints 776 for the same double-sum reason it prints 552 at
	// full offload.
	ComputeHostCPUOnlyMiB int64

	// MMProjReserveDeviceMiB is the accelerator memory the vision projector
	// reserves ON TOP of the text-only projection (848.18 → 849); c0wrk always
	// passes `--mmproj`, so a gate reading only ProjectDeviceMiB under-counts
	// by this much.
	MMProjReserveDeviceMiB int64

	// MMProjReserveHostMiB is the system-RAM half of the same projector
	// reserve (24.93 → 25).
	MMProjReserveHostMiB int64

	// KVBytesPerTokenF16 is the exact f16 KV cost per token — the "~64 KiB"
	// the fork's docs quote for the 27B, measured with no remainder
	// (16384.00 MiB / 262,144) and derivable from the geometry
	// (`TestKVBytesPerTokenIsDerivedFromGeometry`). NOT a family constant: the
	// full-attention 8B costs ~140 KiB per token, which is why the profile is
	// per-model.
	KVBytesPerTokenF16 int64

	// KVValuesPerLayerToken is the cached values one full-attention layer
	// holds per token: `n_embd_k_gqa + n_embd_v_gqa` (1024 + 1024).
	KVValuesPerLayerToken int64

	// KVDivisor maps a precision to its footprint shrink against f16: f16 1,
	// q8_0 32/17, q4_0 32/9 — each reproducing a measured cache size EXACTLY.
	// The q8_0 divisor is NOT the nominal 2 (a block's fp16 scale makes a
	// value cost 8.5 bits): using 2 would under-count a full-context cache by
	// 512 MiB (6.25%), the unsafe direction for a gate, and only the exact
	// 32/9 reproduces q4_0's 4608.00 MiB. Store the ratio, never a rounding
	// of it.
	KVDivisor map[KVType]float64

	// MaxContext is the model's training context, the ceiling every projection
	// accepts — NOT the largest context c0wrk launches: the RAM-tiered ladder
	// in `resolve.go` stops at `contextTierTop` = 131072.
	MaxContext int

	// LayerCount is the model's layer count (64), independently confirmed by
	// the recurrent-state line.
	LayerCount int

	// OffloadableLayers is the `-ngl` value that offloads everything:
	// LayerCount + 1, the output/embedding layer included (`-ngl 65` and
	// `-ngl 99` produce byte-identical projections).
	OffloadableLayers int

	// FullAttentionLayers is how many layers keep a KV cache at all (16 of
	// 64); the rest are recurrent and contribute only to RecurrentStateMiB.
	// This 1-in-4 ratio is why a 27B model's cache costs what a 4B
	// full-attention model's would.
	FullAttentionLayers int
}

// PinnedMemoryProfile returns the measured profile of the pinned model. The
// on-disk size maps are DERIVED from `registry.go` at call time (a pin bump
// cannot leave a stale figure here), every other field is a measurement, and
// it fails closed: an unpinned packing yields an error, not a profile with a
// hole. See specs/domains/embedded-llm.md (Memory model).
func PinnedMemoryProfile() (ModelMemoryProfile, error) {
	packings := SupportedPackings()

	weights := make(map[Packing]int64, len(packings))
	for _, packing := range packings {
		asset, ok := ModelAsset(packing)
		if !ok {
			return ModelMemoryProfile{}, fmt.Errorf("%w: model packing %q has no size to derive",
				ErrArtifactNotPinned, packing)
		}
		weights[packing] = mibCeil(asset.SizeBytes)
	}

	mmproj := MMProjAsset()
	if mmproj.SizeBytes <= 0 {
		return ModelMemoryProfile{}, fmt.Errorf("%w: the vision projector has no size to derive",
			ErrArtifactNotPinned)
	}

	return ModelMemoryProfile{
		WeightsMiB: weights,
		MMProjMiB:  mibCeil(mmproj.SizeBytes),

		// MEASURED values; unmeasured packings are absent on purpose, so the
		// projections refuse instead of guessing.
		DeviceWeightsMiB:   map[Packing]int64{PackingPQ2_0: 6539, PackingPTQ1_0: 5395},
		HostWeightsMiB:     map[Packing]int64{PackingPQ2_0: 6865, PackingPTQ1_0: 5664},
		HostWeightSpillMiB: map[Packing]int64{PackingPQ2_0: 322, PackingPTQ1_0: 265},

		RecurrentStateMiB:      150,
		ComputeDeviceMiB:       1377,
		KVQuantComputeExtraMiB: 12,
		ComputeHostMiB:         276,
		ComputeHostCPUOnlyMiB:  390,
		MMProjReserveDeviceMiB: 849,
		MMProjReserveHostMiB:   25,
		KVBytesPerTokenF16:     65536,
		KVValuesPerLayerToken:  2048,
		KVDivisor:              kvDivisors(),
		MaxContext:             maxTrainingContext,
		LayerCount:             64,
		OffloadableLayers:      65,
		FullAttentionLayers:    16,
	}, nil
}

// kvDivisors derives the divisor table from the GGUF block geometry rather
// than writing the three ratios down.
func kvDivisors() map[KVType]float64 {
	return map[KVType]float64{
		KVTypeF16:  1,
		KVTypeQ8_0: kvDivisor(8),
		KVTypeQ4_0: kvDivisor(4),
	}
}

// kvDivisor returns the f16-to-quantised footprint ratio for a nominal bit
// width, accounting for the fp16 scale each block of kvBlockValues carries.
func kvDivisor(valueBits int) float64 {
	quantisedBitsPerBlock := kvBlockValues*valueBits + kvBlockScaleBits
	f16BitsPerBlock := kvBlockValues * kvF16ValueBits
	return float64(f16BitsPerBlock) / float64(quantisedBitsPerBlock)
}

// mibCeil converts an exact byte count to whole MiB, rounded UP: every use is
// a budget, and rounding a budget down is the unsafe direction.
func mibCeil(bytes int64) int64 {
	if bytes <= 0 {
		return 0
	}
	return (bytes + bytesPerMiB - 1) / bytesPerMiB
}

// ─────────────────────────────────────────────────────────────────────────────
// The CUDA (NVIDIA Blackwell) measured overlay — 2026-11-03
// ─────────────────────────────────────────────────────────────────────────────
//
// Everything in this section was MEASURED on 2026-11-03 with the SAME pinned
// fork release `prism-b10735-842b188` (`build 10735 (842b18804) with GNU
// 11.4.0 for Linux x86_64`), cuda-13.3 build (`CUDA ARCHS =
// 750,800,860,890,900,1200,1210`, `USE_GRAPHS = 1`,
// `BLACKWELL_NATIVE_FP4 = 1`), on an NVIDIA GeForce RTX 5060 Ti (Blackwell,
// arch 1200, 15938 MiB), against the pinned
// `Ternary-Bonsai-2-27B-PQ2_0.gguf` and its mmproj, at c0wrk's canonical
// launch shape (`-fit on -ngl 99 -fa on -np 1 --ctx-checkpoints 32
// --no-cache-idle-slots --mmproj ...`, all 65/65 layers offloaded).
//
// The evidence was the four full llama-server logs captured under
// `testdata/cuda-measured/` — LOCAL measurement artifacts that are NOT part
// of the repository (`*.log` is gitignored). The durable in-repo record of
// the numbers below is their transcription in
// `docs/development/embedded-llm-cuda-measurements.md` (methodology, the
// parsing grep commands, the SHA256 of every log, and
// `TestCUDAMeasuredProfileReplay`, which asserts every row). The four
// measured (context, KV precision) points are:
//
//	32768 / f16    `| - CUDA0 (RTX 5060 Ti) | 15938 = 4581 + (8907 = 6543 + 2197 + 166) + 2449 |`, `| - Host | 374 = 322 + 0 + 52 |`
//	65536 / f16    `| - CUDA0 (RTX 5060 Ti) | 15938 = 2479 + (10987 = 6543 + 4245 + 198) + 2471 |`, `| - Host | 406 = 322 + 0 + 84 |`
//	65536 / q8_0   `| - CUDA0 (RTX 5060 Ti) | 15938 = 4209 + (9279 = 6543 + 2325 + 410) + 2449 |`, `| - Host | 406 = 322 + 0 + 84 |`
//	131072 / q8_0  `| - CUDA0 (RTX 5060 Ti) | 15938 = 1702 + (11775 = 6543 + 4501 + 730) + 2460 |`, `| - Host | 470 = 322 + 0 + 148 |`
//
// The overlay reproduces the LOADED-PASS breakdown, not a dry-run fit
// projection: these logs carry no dry-run breakdown rows (`-fit on` sized
// only `-ngl`; the fork prints the breakdown once, at SIGTERM shutdown), so
// `self` is the tuple a CUDA projection reproduces. Two consequences a
// consumer must know:
//
//   - unlike the Metal dry-run figure, `self` EXCLUDES the unaccounted term
//     (~2449-2471 MiB in all four runs — the CUDA context, the cuBLAS/graph
//     workspace pools and the mmproj; constant with `-c`, a property of the
//     measurement environment rather than of the launch). A CUDA gate
//     compares this figure against a budget that must still absorb it — that
//     headroom comes from plan.go's budget margins and family allowance, not
//     from here.
//   - `self` rounds the exact sum once while flooring each column (the
//     131072/q8_0 row: 6543 + floor(4352.00 + 149.62) + 730 = 11775 printed;
//     the projection's 6543 + 4352 + 150 + 730 = 11775 lands on the same
//     total the same way the Metal pair does).
//
// The weights term (6543) is the loaded `model` COLUMN, not the raw buffer
// size: `load_tensors: CUDA0 model buffer size = 6539.67 MiB`, with ~3 MiB of
// loader overhead rounding into the column — the same loaded-pass shape the
// Metal profile models with HostWeightsMiB. The spill, the recurrent state
// and the KV cache are byte-identical to the Metal terms (322.07, 149.62, and
// the exact block-geometry sizes), so this overlay reuses them unchanged.
type cudaComputePoint struct {
	// ctx is the `-c` value the run launched with.
	ctx int
	// deviceMiB is `sched_reserve: CUDA0 compute buffer size` for the run,
	// floored to whole MiB exactly as llama.cpp floors the breakdown column
	// it prints.
	deviceMiB int64
	// hostMiB is the same run's `sched_reserve: CUDA_Host compute buffer
	// size`, floored the same way.
	hostMiB int64
}

// cudaComputeCurves is the measured compute-reserve table: per KV precision,
// the points above in ascending context order. Unlike Metal — where the
// reserve is one constant that SHRINKS with context — CUDA's reserve grows
// with the context and roughly doubles under a quantised cache (the
// dequantisation scratch the attention path needs), so a (kv, ctx) function
// replaces Metal's scalar + extra pair:
//
//	f16   166 @32,768 -> 198 @65,536
//	q8_0  410 @65,536 -> 730 @131,072
//
// The host half of each point is the same run's `CUDA_Host` reserve
// (52 / 84 / 84 / 148), which grows the same way for the same reason — the
// dequant-KV degassing on the CPU side of the graph.
//
// MEASURED 2026-11-03 (fork `prism-b10735-842b188`, cuda-13.3, RTX 5060 Ti);
// every value above quotes its `sched_reserve` line, and the fractional
// readings (166.02, 198.02, 410.28, 730.28 / 52.02, 84.02, 84.28, 148.28)
// floor to the stored values, matching the printed breakdown columns.
var cudaComputeCurves = map[KVType][]cudaComputePoint{
	KVTypeF16: {
		{ctx: 32768, deviceMiB: 166, hostMiB: 52},
		{ctx: 65536, deviceMiB: 198, hostMiB: 84},
	},
	KVTypeQ8_0: {
		{ctx: 65536, deviceMiB: 410, hostMiB: 84},
		{ctx: 131072, deviceMiB: 730, hostMiB: 148},
	},
}

// cudaDeviceWeightsMiB is the CUDA device residency per packing — the loaded
// breakdown's `model` column (the raw buffer plus loader overhead, see above).
// A packing with no entry is refused with ErrMemoryNotMeasured, exactly like
// the Metal map: PQ2_0 is the only packing measured on CUDA, and it is the
// packing `packingFor` selects on Blackwell anyway (the model card measures
// PQ2_0 faster there: RTX 5090 129.9 vs 120.5 TG128).
//
// MEASURED 2026-11-03 (fork `prism-b10735-842b188`, cuda-13.3, RTX 5060 Ti):
// `6543` in all four shutdown rows, e.g.
// `| - CUDA0 (RTX 5060 Ti) | 15938 = 1702 + (11775 = 6543 + 4501 + 730) + 2460 |`.
var cudaDeviceWeightsMiB = map[Packing]int64{PackingPQ2_0: 6543}

// cudaHostWeightSpillMiB is the CUDA host residency of the un-repackable
// weight spill at full offload.
//
// MEASURED 2026-11-03 (fork `prism-b10735-842b188`, cuda-13.3, RTX 5060 Ti):
// `load_tensors: CPU_Mapped model buffer size = 322.07 MiB` in all four logs,
// confirmed by every loaded Host row's `model` column
// (`| - Host | 470 = 322 + 0 + 148 |` and friends). It happens to equal the
// Metal PQ2_0 figure because it is the SAME tensors at the SAME bit width,
// but it is stored separately: the two are independent measurements, and a
// future re-measure of one must not silently move the other.
var cudaHostWeightSpillMiB = map[Packing]int64{PackingPQ2_0: 322}

// backendShape is the projection's view of one (backend, family) tuple: the
// per-packing residency maps and the two compute-reserve functions the
// projections sum. Everything it holds is a measurement cited above or in
// the profile fields it borrows; nothing here decides a context size or
// refuses an install — the projections keep that role.
type backendShape struct {
	// deviceWeights maps a packing to its accelerator residency; a missing
	// packing is ErrMemoryNotMeasured.
	deviceWeights map[Packing]int64
	// hostSpill maps a packing to its full-offload system-RAM residency; a
	// missing packing is ErrMemoryNotMeasured.
	hostSpill map[Packing]int64
	// computeDevice is the accelerator compute reserve at a (context,
	// precision) shape.
	computeDevice func(ctx int, kv KVType) (int64, error)
	// computeHost is the system-RAM compute reserve at full offload for the
	// same shape.
	computeHost func(ctx int, kv KVType) (int64, error)
}

// metalShape is the default: the 2026-09-25 Metal measurement exactly as the
// profile struct stores it, with the scalar compute reserve plus the scalar
// KV-quantisation extra. Every tuple the CUDA overlay does not cover is
// priced here — that is the documented estimate, not a hidden fallback.
func (p ModelMemoryProfile) metalShape() backendShape {
	return backendShape{
		deviceWeights: p.DeviceWeightsMiB,
		hostSpill:     p.HostWeightSpillMiB,
		computeDevice: func(_ int, kv KVType) (int64, error) {
			return p.ComputeDeviceMiB + p.kvQuantComputeExtra(kv), nil
		},
		computeHost: func(int, KVType) (int64, error) { return p.ComputeHostMiB, nil },
	}
}

// cudaShape reports the measured CUDA overlay for the ONE tuple the
// 2026-11-03 measurement covers — a CUDA backend (any pinned asset tag; the
// buffers are a property of the fork pin and the silicon, and the cuda-13.3
// build is the one actually measured) on NVIDIA Blackwell. Every other tuple
// reports false and stays on the Metal default.
func cudaShape(backend Backend, family GPUFamily) (backendShape, bool) {
	if !backend.IsCUDA() || family != GPUFamilyNVIDIABlackwell {
		return backendShape{}, false
	}
	return backendShape{
		deviceWeights: cudaDeviceWeightsMiB,
		hostSpill:     cudaHostWeightSpillMiB,
		computeDevice: func(ctx int, kv KVType) (int64, error) {
			device, _, err := cudaComputeAt(kv, ctx)
			return device, err
		},
		computeHost: func(ctx int, kv KVType) (int64, error) {
			_, host, err := cudaComputeAt(kv, ctx)
			return host, err
		},
	}, true
}

// shapeFor selects the measured shape the projections price a launch with:
// the CUDA overlay on the measured tuple, the Metal default everywhere else.
func (p ModelMemoryProfile) shapeFor(backend Backend, family GPUFamily) backendShape {
	if cuda, ok := cudaShape(backend, family); ok {
		return cuda
	}
	return p.metalShape()
}

// cudaComputeAt returns the device and host compute reserve at a context:
// linear interpolation between the measured knots of the precision's curve,
// CLAMPED to the end values outside the measured range. At every measured
// context the result is the exact stored knot.
//
// Clamp semantics, and the one direction that is NOT conservative: below the
// first knot the curve returns it (an over-count — safe for a gate); above
// the last knot it returns that too, which UNDER-counts should the reserve
// keep growing past the measured range (131,072/f16 was never measured on
// this card — the doc's own observation is that it would have swapped — and
// the resolver's ladder stops at 131,072 anyway; 262,144 is a projection
// ceiling, not a launch shape).
//
// q4_0 has NO CUDA measurement: it runs the q8_0 curve as a documented
// over-estimate. The reserve is the dequantisation scratch a quantised cache
// forces (both quantised types dequantise into the same f16 tiles, so the
// scratch is set by whether the cache is quantised, not by which width), and
// q4_0's smaller cache reads cannot cost more than q8_0's — using the larger
// measured neighbour keeps an unmeasured precision on measured numbers
// instead of falling back to the Metal scalar that is order-of-magnitude off
// on this silicon.
func cudaComputeAt(kv KVType, ctx int) (deviceMiB, hostMiB int64, err error) {
	curve, ok := cudaComputeCurves[kv]
	if !ok {
		curve, ok = cudaComputeCurves[KVTypeQ8_0]
	}
	if !ok || len(curve) == 0 {
		// Unreachable over the closed KVType set (f16 and q8_0 have curves),
		// but a guard keeps the function total rather than trusting that.
		return 0, 0, fmt.Errorf("%w: %q (no CUDA compute measurement)", ErrKVTypeUnsupported, kv)
	}

	first, last := curve[0], curve[len(curve)-1]
	if ctx <= first.ctx {
		return first.deviceMiB, first.hostMiB, nil
	}
	if ctx >= last.ctx {
		return last.deviceMiB, last.hostMiB, nil
	}
	for i := 1; i < len(curve); i++ {
		hi := curve[i]
		if ctx > hi.ctx {
			continue
		}
		lo := curve[i-1]
		fraction := float64(ctx-lo.ctx) / float64(hi.ctx-lo.ctx)
		device := float64(lo.deviceMiB) + fraction*float64(hi.deviceMiB-lo.deviceMiB)
		host := float64(lo.hostMiB) + fraction*float64(hi.hostMiB-lo.hostMiB)
		// Floored, not rounded: llama.cpp floors each breakdown column (and
		// the knots are exact regardless), and a floor stays within the
		// measured band it interpolates.
		return int64(math.Floor(device)), int64(math.Floor(host)), nil
	}
	// Unreachable: ctx < last.ctx found no segment. Treated as the top knot.
	return last.deviceMiB, last.hostMiB, nil
}

// KVCacheMiB projects the KV-cache footprint; EXACT for every modelled
// precision (the rounded projection equals the measured whole-MiB sizes).
// Out-of-range shapes are refused. See specs/domains/embedded-llm.md.
func (p ModelMemoryProfile) KVCacheMiB(ctx int, kv KVType) (int64, error) {
	if err := p.validateShape(ctx, kv); err != nil {
		return 0, err
	}
	divisor := p.KVDivisor[kv]

	// Rounded, not truncated: the quantised divisor is not exactly
	// representable in binary floating point, and rounding is safe because
	// the true sizes are whole MiB.
	bytes := float64(ctx) * float64(p.KVBytesPerTokenF16) / divisor
	return int64(math.Round(bytes / float64(bytesPerMiB))), nil
}

// ProjectDeviceMiB projects the ACCELERATOR footprint. backend and family
// select the measured shape: on the CUDA tuple the projection reproduces the
// loaded-pass breakdown `self` (TestCUDAMeasuredProfileReplay asserts every
// row), everywhere else the fork's own dry-run fit projection — the number
// `--fit` decides on — so a gate comparing against free VRAM compares like
// with like (TestProjectionMatchesMeasuredMatrix). 0 when nothing is
// offloaded. TEXT-ONLY — a gate must add MMProjReserveDeviceMiB for the
// projector c0wrk always passes. See specs/domains/embedded-llm.md.
func (p ModelMemoryProfile) ProjectDeviceMiB(backend Backend, family GPUFamily, packing Packing, ctx int, kv KVType, offloaded bool) (int64, error) {
	if err := p.validateShape(ctx, kv); err != nil {
		return 0, err
	}
	if !offloaded {
		return 0, nil
	}
	shape := p.shapeFor(backend, family)
	weights, ok := shape.deviceWeights[packing]
	if !ok {
		return 0, fmt.Errorf("%w: device residency of packing %q", ErrMemoryNotMeasured, packing)
	}
	cache, err := p.KVCacheMiB(ctx, kv)
	if err != nil {
		return 0, err
	}
	compute, err := shape.computeDevice(ctx, kv)
	if err != nil {
		return 0, err
	}
	return weights + cache + p.RecurrentStateMiB + compute, nil
}

// ProjectHostMiB projects the SYSTEM-RAM footprint: at full offload the
// loaded pass's host row (weight spill + one compute buffer — no KV cache or
// recurrent state is host-resident), at `-ngl 0` the whole model. backend
// and family select the shape exactly as ProjectDeviceMiB documents. The
// quantised-cache rows of the CPU-only branch are projections, not
// measurements — that compute delta is unmeasured and left out rather than
// guessed. TEXT-ONLY — a gate must add MMProjReserveHostMiB. See
// specs/domains/embedded-llm.md.
func (p ModelMemoryProfile) ProjectHostMiB(backend Backend, family GPUFamily, packing Packing, ctx int, kv KVType, offloaded bool) (int64, error) {
	if err := p.validateShape(ctx, kv); err != nil {
		return 0, err
	}
	if offloaded {
		shape := p.shapeFor(backend, family)
		spill, ok := shape.hostSpill[packing]
		if !ok {
			return 0, fmt.Errorf("%w: host weight spill of packing %q", ErrMemoryNotMeasured, packing)
		}
		compute, err := shape.computeHost(ctx, kv)
		if err != nil {
			return 0, err
		}
		// Neither the KV cache nor the recurrent state is host-resident once
		// every layer is offloaded: the measured Host row carries a 0 context
		// column in all three precisions.
		return spill + compute, nil
	}
	weights, ok := p.HostWeightsMiB[packing]
	if !ok {
		return 0, fmt.Errorf("%w: host residency of packing %q", ErrMemoryNotMeasured, packing)
	}
	cache, err := p.KVCacheMiB(ctx, kv)
	if err != nil {
		return 0, err
	}
	return weights + cache + p.RecurrentStateMiB + p.ComputeHostCPUOnlyMiB, nil
}

// validateShape rejects a (context, precision) pair the profile cannot
// project; both projections run it on EVERY call, including the branches whose
// total ignores the pair, so no number comes back for a shape the model
// refuses to describe.
func (p ModelMemoryProfile) validateShape(ctx int, kv KVType) error {
	if ctx <= 0 || ctx > p.MaxContext {
		return fmt.Errorf("%w: %d (modelled: 1..%d)", ErrContextOutOfRange, ctx, p.MaxContext)
	}
	divisor, ok := p.KVDivisor[kv]
	if !ok || divisor <= 0 || !kv.Valid() {
		return fmt.Errorf("%w: %q", ErrKVTypeUnsupported, kv)
	}
	return nil
}

// kvQuantComputeExtra is the quantised-cache device compute delta, keyed off
// the precision rather than folded into KVDivisor because it is a COMPUTE
// cost, not a cache-size cost: mixing it in would corrupt the exact cache
// figures. The CUDA shape never calls it — its quantised-cache cost lives
// inside the measured curves.
func (p ModelMemoryProfile) kvQuantComputeExtra(kv KVType) int64 {
	if kv == KVTypeF16 {
		return 0
	}
	return p.KVQuantComputeExtraMiB
}
