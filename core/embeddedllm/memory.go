package embeddedllm

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

// bytesPerMiB is the unit every figure in this file is expressed in.
// llama.cpp's own memory breakdown is printed in MiB, so the profile matches
// it rather than converting twice.
const bytesPerMiB = int64(1) << 20

// maxTrainingContext is the pinned model's training context, 262,144 tokens.
//
// It is written as a shift for the same reason
// `TestPackageSourcesNeverEmitBannedContext` constructs its own pattern rather
// than spelling it out: that guard bans the bare decimal literal from every
// non-test source in this package, because the resolver must never pass it as
// `-c`. Here it is a CEILING the projections validate against, not a launch
// value — `resolve.go` still stops at `contextTierTop`, half of this.
//
// The same guard is why the numbers in this file's doc comments carry
// thousands separators ("262,144", never the bare form) even inside quoted log
// lines, which the runtime itself prints unseparated.
const maxTrainingContext = 1 << 18

// ─────────────────────────────────────────────────────────────────────────────
// Measurement provenance
// ─────────────────────────────────────────────────────────────────────────────
//
// Every number in this file was MEASURED on 2026-09-25 with the pinned fork
// release `prism-b10735-842b188` (`llama-server --version` reports
// "0.2.0-dev (build 10735, commit 842b18804)") on an Apple M4 Max (Mac16,6,
// 128 GiB unified memory; the Metal device `MTL0` reports 110100 MiB), against
// the pinned weights `Ternary-Bonsai-2-27B-PQ2_0.gguf` and
// `Ternary-Bonsai-2-27B-PTQ1_0.gguf` (`ModelRevision` 6ed5e12b; the registry's
// `ModelAsset` supplies the exact byte counts, and both files were re-verified
// against their pinned SHA256 before measuring). The
// runtime archive was re-downloaded from the registry URL and its SHA256
// verified before measuring, so the figures belong to the pin this package
// actually ships.
//
// The reference command line is c0wrk's own launch shape (see `LaunchSpec.Args`)
// minus the flags the memory model does not depend on:
//
//	llama-server -v -m Ternary-Bonsai-2-27B-PQ2_0.gguf --host 127.0.0.1 \
//	  -ngl 99 -fa on -c 262,144 -np 1 --no-webui [-ctk T -ctv T]
//
// The cited log lines are llama.cpp's own:
//
//	print_info:              n_ctx_train = 262,144 / n_layer = 64 /
//	                         n_embd_k_gqa = n_embd_v_gqa = 1024
//	llama_kv_cache:          size = 16384.00 MiB (262,144 cells, 16 layers, 1/1 seqs),
//	                         K (f16): 8192.00 MiB, V (f16): 8192.00 MiB
//	llama_memory_recurrent:  size = 149.62 MiB (1 cells, 64 layers, 1 seqs 0 rs_seq),
//	                         R (f32): 5.62 MiB, S (f32): 144.00 MiB
//	sched_reserve:           MTL0 compute buffer size = 1377.52 MiB
//	sched_reserve:           CPU  compute buffer size =  276.02 MiB
//	load_tensors:            CPU_Mapped model buffer size = 322.07 MiB
//	common_memory_breakdown_print / common_params_fit_impl — see below.
//
// TWO DIFFERENT TOTALS appear in one run and they are NOT interchangeable:
//
//   - the DRY-RUN "fit" pass, which prints
//     `| - MTL0 (Apple M4 Max) | 110100 = 109950 + (24450 = 6539 + 16533 + 1377) + -24300 |`
//     followed by `projected to use 24450 MiB of device memory`. This is the
//     number `--fit` itself decides on, it is available BEFORE the model is
//     loaded, and it splits device from host cleanly. `ProjectDeviceMiB`
//     reproduces it.
//   - the LOADED pass printed at shutdown, e.g.
//     `| - Host | 598 = 322 + 0 + 276 |`. This is what the running process
//     actually holds. `ProjectHostMiB` reproduces it.
//
// The dry-run `Host` row is NOT the real host footprint: the reserve pass runs
// twice, so the breakdown sums two identical CPU compute buffers
// (`874 = 322 + 0 + 552`, and 552 = 2 x 276.02). The loaded pass reports the
// single buffer the process keeps (`~llama_context: CPU compute buffer size is
// 276.0176 MiB, matches expectation`), which is why the host projection is
// modelled on the loaded pass instead.
//
// All figures are TEXT-ONLY: `llama-fit-params` rejects `--mmproj`, and the
// vision projector is accounted for separately by `MMProjReserveDeviceMiB` /
// `MMProjReserveHostMiB`. c0wrk always passes `--mmproj`, so a gate that only
// reads the two projections under-counts by the reserve.
//
// KNOWN LIMITATION — ONE BACKEND. Everything here was measured on Metal. The
// weight, cache, recurrent-state and compute terms are properties of the model
// and the runtime, but the device/host SPLIT is a property of the backend's
// repack support, so the figures for PTQ1_0 — the packing `packingFor` selects
// only for Vulkan — were measured on a backend c0wrk does not pair them with.
// CUDA, ROCm, Vulkan and CPU residency is unmeasured; a gate that runs on them
// is using a Metal measurement as its estimate.

// KVType is the precision of the K and V KV caches, i.e. the value c0wrk would
// pass to BOTH `-ctk`/`--cache-type-k` and `-ctv`/`--cache-type-v`.
//
// The type covers K and V together on purpose. PrismML-Eng/llama.cpp#267
// (opened 2026-09-24, open) reports that MIXED K/V cache types silently fall
// back to running flash attention on the CPU — "2x slower generation" — unless
// the build carries `GGML_CUDA_FA_ALL_QUANTS`. One value for both caches makes
// the mixed shape unrepresentable.
//
// Exactly three precisions are modelled, and the set is deliberately closed:
//
//   - `q5_0` is EXCLUDED on performance. PrismML-Eng/llama.cpp#191 ("Perf bug:
//     q5_0 KV cache ~8x slower than f16/q8_0/q4_0 on long-context decode",
//     opened 2026-09-18, still open as of 2026-09-24) measures q5_0 at
//     pp 11.4 / tg 4.0 t/s against f16 pp 343.5 / tg 31.8, q8_0 pp 342.0 /
//     tg 31.5 and q4_0 pp 342.6 / tg 31.3 on the same hardware, model, prompt
//     and flags — reproduced after a full machine reboot (pp 11.3 / tg 4.1).
//     It also buys no capacity: the q5_0 context ceiling on that card was
//     73728 against q4_0's 72960.
//   - `q4_1`, `iq4_nl` and `q5_1` are EXCLUDED as unverified: neither c0wrk
//     nor the fork's own docs (`Bonsai-demo/KV-CACHE.md` documents `q4_0`
//     only) have a measurement for them on this model.
//
// An unlisted precision is a refusal (`ErrKVTypeUnsupported`), never a silent
// fallback to `f16`.
type KVType string

const (
	// KVTypeF16 is the default: lossless, and the baseline every other
	// precision is measured against. MEASURED 2026-09-25 (fork
	// `prism-b10735-842b188`): `llama_kv_cache: size = 16384.00 MiB
	// (262,144 cells, 16 layers, 1/1 seqs), K (f16): 8192.00 MiB,
	// V (f16): 8192.00 MiB`.
	KVTypeF16 KVType = "f16"
	// KVTypeQ8_0 halves the cache to within 6% and measured identical
	// throughput to f16 in PrismML-Eng/llama.cpp#191 (pp 342.0 / tg 31.5
	// against f16's pp 343.5 / tg 31.8). MEASURED 2026-09-25 (fork
	// `prism-b10735-842b188`): `llama_kv_cache: size = 8704.00 MiB
	// (262,144 cells, 16 layers, 1/1 seqs), K (q8_0): 4352.00 MiB,
	// V (q8_0): 4352.00 MiB`.
	KVTypeQ8_0 KVType = "q8_0"
	// KVTypeQ4_0 is the long-context option the fork itself documents
	// (`Bonsai-demo/KV-CACHE.md`: `BONSAI_KV4=1` "cut[s] KV memory roughly
	// 3.5x: from 64 KiB per token to about 18 KiB per token on the 27B"),
	// and the one PrismML-Eng/llama.cpp#85 reports as mandatory for a 262k
	// context on a 16 GB card. Throughput matched f16 in #191 (pp 342.6 /
	// tg 31.3). MEASURED 2026-09-25 (fork `prism-b10735-842b188`):
	// `llama_kv_cache: size = 4608.00 MiB (262,144 cells, 16 layers,
	// 1/1 seqs), K (q4_0): 2304.00 MiB, V (q4_0): 2304.00 MiB`.
	KVTypeQ4_0 KVType = "q4_0"
)

// kvTypes is the exhaustive, ordered set of modelled precisions.
var kvTypes = []KVType{KVTypeF16, KVTypeQ8_0, KVTypeQ4_0}

// GGUF quantisation-block geometry, which is what makes the KV divisors
// derivable instead of magic. Both quantised cache types store one fp16 scale
// per block of 32 values, so a value costs MORE than its nominal bit width:
//
//	q8_0: (32 * 8 + 16) / 32 = 8.5 bits per value  ->  divisor 32/17 = 1.882353
//	q4_0: (32 * 4 + 16) / 32 = 4.5 bits per value  ->  divisor 32/9  = 3.555556
//
// MEASURED 2026-09-25: the two divisors reproduce the reported cache sizes
// exactly (8704.00 and 4608.00 MiB at 262,144 tokens against f16's
// 16384.00 MiB), which is the check `TestKVDivisorsMatchBlockGeometry` makes.
const (
	kvBlockValues    = 32
	kvBlockScaleBits = 16
	kvF16ValueBits   = 16
)

// ErrKVTypeUnsupported reports a KV-cache precision this model does not cover:
// either one deliberately excluded (see KVType) or one that does not exist.
var ErrKVTypeUnsupported = errors.New("unsupported embedded-LLM KV cache type")

// ErrMemoryNotMeasured reports a weights packing whose runtime residency has
// not been measured. It is a refusal, not a fallback: projecting an unmeasured
// packing from its file size would guess at the split between device and host,
// and a gate built on that guess could pass a configuration that does not fit.
var ErrMemoryNotMeasured = errors.New("no measured memory residency for this packing")

// ErrContextOutOfRange reports a context length the profile cannot project:
// non-positive, or above the model's own training context.
var ErrContextOutOfRange = errors.New("context size outside the modelled range")

// KVTypes returns the modelled KV-cache precisions in a stable order.
func KVTypes() []KVType { return slices.Clone(kvTypes) }

// ParseKVType maps a `--cache-type-k`/`--cache-type-v` spelling onto a KVType.
// Anything outside the closed set — including `q5_0` (excluded on the #191
// performance bug) and `q4_1`/`iq4_nl`/`q5_1` (unverified) — is refused with
// ErrKVTypeUnsupported rather than coerced to a default.
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

// ModelMemoryProfile is the measured memory model of the ONE model c0wrk pins
// (Ternary-Bonsai-2-27B). It replaces a single transcribed "~64 KiB per token"
// comment with per-term figures, each carrying its own measurement.
//
// It is a description, not a policy: nothing here decides a context size or
// refuses an install. `ProjectDeviceMiB` and `ProjectHostMiB` are pure
// projections a gate or a UI reads; the RAM-tiered context ladder in
// `resolve.go` is a separate concern.
//
// Every field is expressed in MiB, matching llama.cpp's own breakdown.
type ModelMemoryProfile struct {
	// WeightsMiB is the ON-DISK size of each pinned GGUF, derived from the
	// registry's exact `Asset.SizeBytes` (never re-typed here) and rounded UP
	// to a whole MiB. It answers "how much disk does the install need", and it
	// is deliberately NOT the residency term the projections use: the file
	// carries ~11 MiB of GGUF metadata and tensor-info that is never loaded
	// into a compute buffer.
	// DERIVED 2026-09-25 from `registry.go` (`ModelRevision` 6ed5e12b):
	// PQ2_0 = 6873 MiB and PTQ1_0 = 5671 MiB. The byte counts themselves are
	// read from `ModelAsset` at call time and are never written down here.
	WeightsMiB map[Packing]int64

	// MMProjMiB is the ON-DISK size of the pinned vision projector, derived
	// the same way from `MMProjAsset().SizeBytes`.
	// DERIVED 2026-09-25 from `registry.go`: 601 MiB; llama.cpp's own
	// `load_hparams: model size: 600.08 MiB` agrees.
	MMProjMiB int64

	// DeviceWeightsMiB is the weight residency the pinned runtime puts on the
	// ACCELERATOR when every offloadable layer is offloaded (`-ngl 99`, which
	// `-ngl 65` reproduces exactly — see OffloadableLayers).
	// MEASURED 2026-09-25 (fork `prism-b10735-842b188`, Apple M4 Max / Metal),
	// from the dry-run fit breakdown's `model` column:
	//
	//	PQ2_0   6539 MiB — `| - MTL0 | 110100 = 109950 + (24450 = 6539 + 16533 + 1377) + -24300 |`
	//	PTQ1_0  5395 MiB — `| - MTL0 | 110100 = 109950 + (23306 = 5395 + 16533 + 1377) + -23156 |`
	//
	// A packing with no entry is refused with ErrMemoryNotMeasured rather than
	// projected from its file size, which would guess at the device/host split.
	DeviceWeightsMiB map[Packing]int64

	// HostWeightsMiB is the weight residency held in SYSTEM RAM when nothing
	// is offloaded (`-ngl 0`, the Intel-Mac and CPU-build shape).
	// MEASURED 2026-09-25 (fork `prism-b10735-842b188`, `-ngl 0`, loaded pass
	// printed at shutdown), from the Host row's `model` column:
	//
	//	PQ2_0   6865 MiB — `| - Host | 23789 = 6865 + 16533 + 390 |`
	//	PTQ1_0  5664 MiB — `| - Host | 22588 = 5664 + 16533 + 390 |`
	//
	// Each is 4 MiB above DeviceWeightsMiB + HostWeightSpillMiB (6539 + 322 =
	// 6861, 5395 + 265 = 5660), the extra being the small host-side buffers
	// the loaded pass also counts (`llama_context: CPU output buffer size =
	// 0.95 MiB` and friends).
	HostWeightsMiB map[Packing]int64

	// HostWeightSpillMiB is the weight residency that stays in SYSTEM RAM even
	// at full offload — the tensors the runtime cannot repack onto the
	// accelerator (`create_tensor: tensor 'token_embd.weight' (pq2_0) ...
	// cannot be used with preferred buffer type CPU_REPACK, using CPU
	// instead`).
	// MEASURED 2026-09-25 (fork `prism-b10735-842b188`):
	// `load_tensors: CPU_Mapped model buffer size = 322.07 MiB` for PQ2_0 and
	// 265 MiB for PTQ1_0 (the dry-run Host `model` column, and
	// `CPU_Mapped model buffer size = 5660.56 MiB` at `-ngl 0`).
	//
	// It is per-packing BECAUSE it is the same tensors at a different bit
	// width: 2.13 bpw (PQ2_0) spills 322 MiB, 1.75 bpw (PTQ1_0) spills 265.
	// A single scalar would over-count PTQ1_0 by 57 MiB.
	HostWeightSpillMiB map[Packing]int64

	// RecurrentStateMiB is the SSM/GDN linear-attention state (`llama_memory_
	// recurrent`, the `R` and `S` buffers). It is an UNDOCUMENTED term: it
	// appears in no upstream table (`Bonsai-demo/README.md`'s peak-memory
	// table lists only "weights + activations + FP16 KV cache + ~1.2 GiB
	// overhead"), yet it is allocated on every run.
	//
	// It is CONTEXT-INDEPENDENT and KV-TYPE-INDEPENDENT: it is sized by the
	// state width, not by the number of tokens.
	// MEASURED 2026-09-25 (fork `prism-b10735-842b188`):
	// `llama_memory_recurrent: size = 149.62 MiB (1 cells, 64 layers, 1 seqs
	// 0 rs_seq), R (f32): 5.62 MiB, S (f32): 144.00 MiB` — byte-identical
	// under f16, q8_0 and q4_0 caches and at -c 262,144, 131072 and 65536.
	// Stored rounded UP (149.62 -> 150); see ComputeDeviceMiB for why the pair
	// still lands on the measured total.
	RecurrentStateMiB int64

	// ComputeDeviceMiB is the accelerator-side compute/activation reserve
	// (`sched_reserve`) for the reference shape: one parallel sequence
	// (`-np 1`), the default 2048/512 logical/physical batch, an f16 cache and
	// full offload.
	// MEASURED 2026-09-25 (fork `prism-b10735-842b188`):
	// `sched_reserve: MTL0 compute buffer size = 1377.52 MiB`.
	//
	// It is stored as the FLOOR (1377) while RecurrentStateMiB is stored as
	// the CEILING (150), because llama.cpp floors each breakdown column but
	// rounds the total once from the exact sum: the two fractional terms add
	// to 149.62 + 1377.52 = 1527.14, whose floor (1527) is exactly 150 + 1377.
	// Storing both ceilings would project 24451 for a run llama.cpp itself
	// reports as 24450.
	//
	// This term is NOT constant across the whole configuration space. It grows
	// with the physical batch (`-ub 256` measures 1259 MiB) and shrinks with
	// the context (`-c 131072` measures 737 MiB, `-c 65536` measures 579 MiB);
	// the value here is the reference shape only, and the KV-quantisation
	// delta lives in KVQuantComputeExtraMiB.
	ComputeDeviceMiB int64

	// KVQuantComputeExtraMiB is the extra accelerator compute reserve a
	// QUANTISED cache costs over f16 — the dequantisation scratch the
	// attention path needs.
	// MEASURED 2026-09-25 (fork `prism-b10735-842b188`):
	// `sched_reserve: MTL0 compute buffer size = 1389.03 MiB` under BOTH
	// `-ctk q8_0 -ctv q8_0` and `-ctk q4_0 -ctv q4_0`, against 1377.52 MiB for
	// f16; 1389.03 - 1377.52 = 11.51, stored rounded UP as 12.
	KVQuantComputeExtraMiB int64

	// ComputeHostMiB is the SYSTEM-RAM compute reserve at full offload.
	// MEASURED 2026-09-25 (fork `prism-b10735-842b188`):
	// `sched_reserve: CPU compute buffer size = 276.02 MiB` (f16) and 276.28
	// MiB (q8_0 and q4_0 — the same whole MiB), confirmed by the loaded
	// breakdown `| - Host | 598 = 322 + 0 + 276 |` and by the shutdown assert
	// `~llama_context: CPU compute buffer size is 276.0176 MiB, matches
	// expectation of 276.0176 MiB`.
	ComputeHostMiB int64

	// ComputeHostCPUOnlyMiB is the SYSTEM-RAM compute reserve when NOTHING is
	// offloaded (`-ngl 0`), where the whole graph is scheduled on the CPU and
	// the reserve is correspondingly larger.
	// MEASURED 2026-09-25 (fork `prism-b10735-842b188`, `-ngl 0`):
	// `sched_reserve: CPU compute buffer size = 390.02 MiB` in the dry run and
	// 390.0176 MiB in the loaded pass, confirmed by `~llama_context: CPU
	// compute buffer size is 390.0176 MiB, matches expectation`. The dry-run
	// Host row prints 776 for the same reason it prints 552 at full offload:
	// the reserve pass is summed twice.
	ComputeHostCPUOnlyMiB int64

	// MMProjReserveDeviceMiB is the accelerator memory the vision projector
	// reserves ON TOP of the text-only projection. c0wrk always passes
	// `--mmproj`, so a gate that reads only ProjectDeviceMiB under-counts by
	// this much.
	// MEASURED 2026-09-25 (fork `prism-b10735-842b188`, with `--mmproj
	// Ternary-Bonsai-2-27B-mmproj-Q8_0.gguf`):
	// `[mtmd] estimated worst-case memory usage of mmproj is 873.10 MiB`, of
	// which `[mtmd] adding 848.18 MiB to fit_params_target for device MTL0`.
	// Stored rounded UP (848.18 -> 849).
	MMProjReserveDeviceMiB int64

	// MMProjReserveHostMiB is the system-RAM half of the same worst-case
	// projector reserve.
	// MEASURED 2026-09-25 (fork `prism-b10735-842b188`):
	// `[mtmd] adding 24.93 MiB to fit_params_target for device CPU`, stored
	// rounded UP (24.93 -> 25).
	MMProjReserveHostMiB int64

	// KVBytesPerTokenF16 is the exact f16 KV-cache cost per token — the
	// "~64 KiB per token" the fork's docs quote for the 27B
	// (`Bonsai-demo/README.md`: "The FP16 KV cache costs 64 KiB per token
	// (~6.3 GiB at 100K)"; `Bonsai-demo/KV-CACHE.md`: "from 64 KiB per token
	// to about 18 KiB per token on the 27B"). It is NOT a family constant: the
	// full-attention 8B costs roughly 140 KiB per token, which is why the
	// profile is per-model.
	//
	// MEASURED EXACTLY 2026-09-25 (fork `prism-b10735-842b188`):
	// `llama_kv_cache: size = 16384.00 MiB (262,144 cells, 16 layers, 1/1
	// seqs), K (f16): 8192.00 MiB, V (f16): 8192.00 MiB` — 16384.00 MiB /
	// 262,144 tokens = 65536 bytes per token, with no remainder.
	//
	// It is also derivable from the model's own geometry, which
	// `TestKVBytesPerTokenIsDerivedFromGeometry` asserts:
	// FullAttentionLayers * KVValuesPerLayerToken * 2 bytes = 16 * 2048 * 2.
	KVBytesPerTokenF16 int64

	// KVValuesPerLayerToken is the number of cached values one full-attention
	// layer holds per token: `n_embd_k_gqa + n_embd_v_gqa`.
	// MEASURED 2026-09-25 (fork `prism-b10735-842b188`):
	// `print_info: n_embd_k_gqa = 1024` and `n_embd_v_gqa = 1024`
	// (n_head = 24, n_head_kv = 4, n_embd_head_k = n_embd_head_v = 256).
	KVValuesPerLayerToken int64

	// KVDivisor maps a cache precision to the factor its footprint shrinks by
	// against f16. Each entry is the block geometry above, and each reproduces
	// a measured cache size EXACTLY at 262,144 tokens:
	//
	//	f16   1        -> 16384.00 MiB (measured 16384.00)
	//	q8_0  32/17    ->  8704.00 MiB (measured  8704.00)
	//	q4_0  32/9     ->  4608.00 MiB (measured  4608.00)
	//
	// MEASURED 2026-09-25 (fork `prism-b10735-842b188`); the `llama_kv_cache:
	// size` lines are quoted on each KVType constant above.
	//
	// Note the q8_0 divisor is 32/17 = 1.882353 and NOT the nominal 2: a q8_0
	// block is 32 values plus one fp16 scale, so a value costs 8.5 bits rather
	// than 8. Using 2 would project 8192 MiB for a cache that measures
	// 8704.00 MiB — a 512 MiB (6.25%) UNDER-count at full context, which is
	// the unsafe direction for a gate.
	//
	// The same trap applies to q4_0: the fork's docs say "roughly 3.5x" and the
	// rounded 3.556 is close enough to read that way, but only the exact 32/9
	// reproduces 4608.00 MiB (3.556 projects 4607). Store the ratio, not a
	// rounding of it.
	KVDivisor map[KVType]float64

	// MaxContext is the model's training context, and the ceiling every
	// projection accepts.
	// MEASURED 2026-09-25 (fork `prism-b10735-842b188`):
	// `print_info: n_ctx_train = 262,144` (and `n_ctx_orig_yarn = 262,144`);
	// `Bonsai-demo/README.md`: "The 27B models support up to 262,144 tokens of
	// context".
	//
	// This is NOT the largest context c0wrk currently launches: the RAM-tiered
	// ladder in `resolve.go` stops at `contextTierTop` = 131072.
	MaxContext int

	// LayerCount is the model's layer count.
	// MEASURED 2026-09-25 (fork `prism-b10735-842b188`):
	// `print_info: n_layer = 64` and `n_layer_all = 64`; independently
	// confirmed by `llama_memory_recurrent: size = 149.62 MiB (1 cells,
	// 64 layers, ...)`.
	LayerCount int

	// OffloadableLayers is the `-ngl` value that offloads everything, i.e. the
	// count llama.cpp itself reports when c0wrk passes `-ngl 99`. It is
	// LayerCount + 1 because the output/embedding layer is offloadable too.
	// MEASURED 2026-09-25 (fork `prism-b10735-842b188`): `-ngl 65` and
	// `-ngl 99` produce byte-identical projections (`MTL0 6539 16533 1377`),
	// and PrismML-Eng/llama.cpp#191 describes the same model as "all 65/65
	// layers offloaded to GPU".
	OffloadableLayers int

	// FullAttentionLayers is how many of the LayerCount layers keep a KV cache
	// at all; the rest are recurrent (SSM/GDN) and contribute only to
	// RecurrentStateMiB. This 1-in-4 ratio is why a 27B model's cache costs
	// what a 4B full-attention model's would.
	// MEASURED 2026-09-25 (fork `prism-b10735-842b188`):
	// `llama_kv_cache: size = 16384.00 MiB (262,144 cells, 16 layers, 1/1
	// seqs)` — 16 of 64 — and the `llama_memory_recurrent, layer N` trace
	// keeps layers 0,1,2 then skips 3, keeps 4,5,6 then skips 7, and so on.
	FullAttentionLayers int
}

// PinnedMemoryProfile returns the measured profile of the model c0wrk pins.
//
// The two on-disk size maps are DERIVED from `registry.go` at call time —
// this file never re-types a byte count, so a pin bump that changes a size
// cannot leave a stale figure behind here. Every other field is a measurement
// whose source and date are quoted in its doc comment.
//
// It fails closed: a packing the registry does not pin yields an error rather
// than a profile with a hole in it.
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

		// MEASURED 2026-09-25 — see each field's doc comment for the log line
		// behind every value. A packing that has not been measured is absent on
		// purpose, so the projections refuse it instead of guessing.
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

// kvDivisors builds the divisor table from the GGUF block geometry, so the
// three values are derived from one stated fact (32 values plus one fp16 scale
// per block) rather than written down three times.
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

// KVCacheMiB projects the KV-cache footprint for a context length and cache
// precision. It is exact for every modelled precision: the measured sizes are
// whole MiB (16384.00, 8704.00 and 4608.00 at 262,144 tokens), so the rounded
// projection equals them rather than approximating them.
//
// It refuses a non-positive context, a context above MaxContext, and any
// precision outside the closed KVType set.
func (p ModelMemoryProfile) KVCacheMiB(ctx int, kv KVType) (int64, error) {
	if err := p.validateShape(ctx, kv); err != nil {
		return 0, err
	}
	divisor := p.KVDivisor[kv]

	// Rounded, not truncated: the divisor of a quantised type is not
	// representable exactly in binary floating point, so the quotient of an
	// otherwise-integral size can land a fraction below the integer it should
	// be. Rounding is safe here because the true sizes are whole MiB.
	bytes := float64(ctx) * float64(p.KVBytesPerTokenF16) / divisor
	return int64(math.Round(bytes / float64(bytesPerMiB))), nil
}

// ProjectDeviceMiB projects the ACCELERATOR memory a launch needs, in MiB.
//
// It reproduces the pinned fork's own dry-run fit projection — the number
// `--fit` decides on and the one printed as `projected to use N MiB of device
// memory` — so a gate comparing against free VRAM is comparing like with like.
// MEASURED 2026-09-25 (fork `prism-b10735-842b188`, `-ngl 99 -fa on -c 262,144
// -np 1`): PQ2_0 projects 24450 MiB for f16, 16782 MiB for q8_0 and 12686 MiB
// for q4_0, and PTQ1_0 projects 23306 MiB for f16;
// `TestProjectionMatchesMeasuredMatrix` asserts every row.
//
// With offloaded == false the projection is 0: nothing is placed on the
// accelerator (`-ngl 0` measures `| - MTL0 | ... (0 = 0 + 0 + 0) ...`), and
// the whole footprint moves to ProjectHostMiB.
//
// The projection is TEXT-ONLY. c0wrk always passes `--mmproj`, so a gate must
// add MMProjReserveDeviceMiB (849 MiB measured) to this figure.
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

// ProjectHostMiB projects the SYSTEM-RAM memory a launch needs, in MiB.
//
// At full offload it reproduces the loaded pass's host row, which is what the
// running process actually holds: MEASURED 2026-09-25 (fork
// `prism-b10735-842b188`) `| - Host | 598 = 322 + 0 + 276 |` for PQ2_0,
// identical under f16, q8_0 and q4_0 — no part of the KV cache or the recurrent
// state lives in system RAM once the layers are offloaded, only the
// un-repackable weight spill and one CPU compute buffer.
//
// With offloaded == false the whole model runs from system RAM: MEASURED
// 2026-09-25 (`-ngl 0`, f16) `| - Host | 23789 = 6865 + 16533 + 390 |` for
// PQ2_0 and `| - Host | 22588 = 5664 + 16533 + 390 |` for PTQ1_0. The
// quantised-cache rows of that branch are projections, not measurements — the
// CPU-only compute delta of a quantised cache has not been measured, so it is
// left out rather than guessed at.
//
// The projection is TEXT-ONLY; a gate must add MMProjReserveHostMiB (25 MiB
// measured) for the vision projector c0wrk always loads.
func (p ModelMemoryProfile) ProjectHostMiB(packing Packing, ctx int, kv KVType, offloaded bool) (int64, error) {
	if err := p.validateShape(ctx, kv); err != nil {
		return 0, err
	}
	if offloaded {
		spill, ok := p.HostWeightSpillMiB[packing]
		if !ok {
			return 0, fmt.Errorf("%w: host weight spill of packing %q", ErrMemoryNotMeasured, packing)
		}
		// Neither the KV cache nor the recurrent state is host-resident once
		// every layer is offloaded: the measured Host row carries a 0 context
		// column in all three precisions.
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
// project. Both projections run it on EVERY call, including the branches whose
// total does not depend on the pair, so a caller cannot get a number back for
// a shape the model refuses to describe.
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

// kvQuantComputeExtra is the additional device compute reserve a quantised
// cache needs. It is keyed off the precision rather than folded into
// KVDivisor because it is a COMPUTE cost, not a cache-size cost: mixing it
// into the divisor would corrupt the exact cache figures.
func (p ModelMemoryProfile) kvQuantComputeExtra(kv KVType) int64 {
	if kv == KVTypeF16 {
		return 0
	}
	return p.KVQuantComputeExtraMiB
}
