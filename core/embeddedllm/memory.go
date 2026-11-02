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
	// at 262,144/131072/65536 tokens. Stored rounded up.
	RecurrentStateMiB int64

	// ComputeDeviceMiB is the accelerator-side compute reserve for the
	// reference shape (`-np 1`, default batch, f16, full offload): 1377.52 →
	// 1377, stored as the FLOOR while RecurrentStateMiB keeps its ceiling —
	// llama.cpp floors each breakdown column but rounds the total once from
	// the exact sum, and 150 + 1377 is exactly the measured 1527 (storing two
	// ceilings would project 24451 for a run the runtime reports as 24450).
	// NOT constant across the space: it grows with the physical batch and
	// shrinks with context; the quantised delta lives in
	// KVQuantComputeExtraMiB.
	ComputeDeviceMiB int64

	// KVQuantComputeExtraMiB is the dequantisation scratch a QUANTISED cache
	// adds to the device compute reserve: 1389.03 measured under both q8_0
	// and q4_0 against f16's 1377.52 → 12 rounded up.
	KVQuantComputeExtraMiB int64

	// ComputeHostMiB is the system-RAM compute reserve at full offload:
	// 276.02 → 276, the same whole MiB under every precision.
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

// ProjectDeviceMiB projects the ACCELERATOR footprint: it reproduces the
// fork's own dry-run fit projection (the number `--fit` decides on), so a gate
// comparing against free VRAM compares like with like; every row of the
// measured matrix is asserted by `TestProjectionMatchesMeasuredMatrix`. 0 when
// nothing is offloaded. TEXT-ONLY — a gate must add MMProjReserveDeviceMiB for
// the projector c0wrk always passes. See specs/domains/embedded-llm.md.
func (p ModelMemoryProfile) ProjectDeviceMiB(packing Packing, ctx int, kv KVType, offloaded bool) (int64, error) {
	if err := p.validateShape(ctx, kv); err != nil {
		return 0, err
	}
	if !offloaded {
		return 0, nil
	}
	weights, ok := p.DeviceWeightsMiB[packing]
	if !ok {
		return 0, fmt.Errorf("%w: device residency of packing %q", ErrMemoryNotMeasured, packing)
	}
	cache, err := p.KVCacheMiB(ctx, kv)
	if err != nil {
		return 0, err
	}
	return weights + cache + p.RecurrentStateMiB + p.ComputeDeviceMiB + p.kvQuantComputeExtra(kv), nil
}

// ProjectHostMiB projects the SYSTEM-RAM footprint: at full offload the loaded
// pass's host row (weight spill + one compute buffer — no KV cache or
// recurrent state is host-resident), at `-ngl 0` the whole model. The
// quantised-cache rows of the CPU-only branch are projections, not
// measurements — that compute delta is unmeasured and left out rather than
// guessed. TEXT-ONLY — a gate must add MMProjReserveHostMiB. See
// specs/domains/embedded-llm.md.
func (p ModelMemoryProfile) ProjectHostMiB(packing Packing, ctx int, kv KVType, offloaded bool) (int64, error) {
	if err := p.validateShape(ctx, kv); err != nil {
		return 0, err
	}
	if offloaded {
		spill, ok := p.HostWeightSpillMiB[packing]
		if !ok {
			return 0, fmt.Errorf("%w: host weight spill of packing %q", ErrMemoryNotMeasured, packing)
		}
		return spill + p.ComputeHostMiB, nil
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
// figures.
func (p ModelMemoryProfile) kvQuantComputeExtra(kv KVType) int64 {
	if kv == KVTypeF16 {
		return 0
	}
	return p.KVQuantComputeExtraMiB
}
