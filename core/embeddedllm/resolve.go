package embeddedllm

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Resolution is the pure output of (platform, backend, ramGiB): everything the
// installer downloads and everything the supervisor passes to llama-server.
type Resolution struct {
	// Assets is the ordered install set: runtime, cudart (Windows CUDA only),
	// model, mmproj. The order is the download order and the order progress is
	// reported in.
	Assets []Asset
	// Backend is the backend the artifacts were ACTUALLY resolved for, after
	// the architecture and pin-availability rules in effectiveBackend. It can
	// differ from the probed Hardware.Backend: an Intel Mac, a non-x64 machine
	// that reported CUDA, and a Windows machine whose driver maps to a CUDA tag
	// this pin has no archive for all resolve to something else. This is the
	// value the manifest and the informational Settings label must record —
	// recording the probed backend would describe an install that is not on
	// disk.
	Backend Backend
	// Packing is the weights quantization to download.
	Packing Packing
	// Layers is the -ngl value: 0 on an Intel Mac and on the CPU build,
	// nglAllGPU on every GPU-backed build.
	Layers int
	// ContextSize is the -c value: RAM-tiered, always positive, and never the
	// model's full training context (which is memory-unaware and OOMs a
	// constrained machine once -ngl offloads the KV cache).
	ContextSize int
	// ImageMaxTokens is --image-max-tokens: imageMaxTokensCapped on
	// Metal/Vulkan/CPU to keep vision prefill latency sane, and
	// ImageMaxTokensUncapped on CUDA/ROCm. Uncapped means the flag is omitted
	// entirely.
	ImageMaxTokens int
	// NeedsCudart reports whether the set carries the paired CUDA runtime DLL
	// archive (Windows CUDA only), so the installer can surface it as its own
	// component with its own progress bar.
	NeedsCudart bool
}

// Launch-flag policy derived from a Resolution. Every value mirrors the fork's
// demo scripts (scripts/common.sh), so c0wrk provisions and launches exactly
// what upstream would have on the same machine.
const (
	// MinRAMGiB is the hard refusal threshold (ADR-066 D6). There is no
	// reduced-experience band between 8 and 16 GiB: below this the install is
	// refused outright, before anything is downloaded.
	MinRAMGiB = 16.0

	// nglAllGPU offloads every layer to the GPU.
	nglAllGPU = 99
	// nglCPUOnly keeps the model in system RAM.
	nglCPUOnly = 0

	// imageMaxTokensCapped downscales large images to roughly this many vision
	// tokens. A 12 MP photo is ~4000 vision tokens, and prefilling that on
	// Metal/Vulkan/CPU costs far more than the fine detail is worth.
	imageMaxTokensCapped = 1024
	// ImageMaxTokensUncapped means "do not pass --image-max-tokens at all".
	// CUDA and ROCm run uncapped, and an explicit 0 override means the same.
	ImageMaxTokensUncapped = 0

	// contextTierTop is the largest context this resolver ever emits, reached
	// only above 71 GiB of RAM. The model's own training context is roughly
	// twice this and is deliberately unreachable: asking for it is
	// memory-unaware and OOMs constrained machines.
	contextTierTop = 131072
)

// ErrInsufficientRAM is the typed refusal for a machine below MinRAMGiB. It is
// returned before anything is downloaded, so an undersized machine never ends
// up with a multi-gigabyte partial install.
var ErrInsufficientRAM = errors.New("embedded LLM requires at least 16 GiB of system RAM")

// cudaTagsNewestFirst lists the pinned CUDA asset tags newest first. When a
// platform has no archive for the probed tag, Resolve clamps DOWN this list: a
// binary built against an older CUDA runs on a newer driver, never the reverse.
var cudaTagsNewestFirst = []Backend{BackendCUDA133, BackendCUDA128, BackendCUDA124}

// AssetTable is the artifact-registry seam Resolve consults. The default
// implementation delegates to the compile-time pinned registry in registry.go;
// tests substitute a fake to exercise the fail-closed paths without touching
// the pins. Resolution itself performs no I/O either way — the registry is a
// table of constants.
type AssetTable interface {
	// RuntimeAsset reports whether a llama-server archive is pinned for a
	// platform+backend pair. Resolve uses it to decide whether a probed backend
	// is provisionable at all.
	RuntimeAsset(platform string, backend Backend) (Asset, bool)
	// ArtifactSet returns the complete ordered install set (runtime, cudart
	// when the pair needs one, model, mmproj), or an error wrapping
	// ErrArtifactNotPinned rather than a partial set.
	ArtifactSet(platform string, backend Backend, packing Packing) ([]Asset, error)
}

// assetTable is the registry the exported Resolve consults. It is never nil:
// the pinned registry is the package-level default.
var assetTable AssetTable = pinnedRegistry{}

// pinnedRegistry adapts the compile-time registry functions to AssetTable.
type pinnedRegistry struct{}

// RuntimeAsset implements AssetTable.
func (pinnedRegistry) RuntimeAsset(platform string, backend Backend) (Asset, bool) {
	return RuntimeAsset(platform, backend)
}

// ArtifactSet implements AssetTable.
func (pinnedRegistry) ArtifactSet(platform string, backend Backend, packing Packing) ([]Asset, error) {
	return ArtifactSet(platform, backend, packing)
}

// Resolve derives the complete install and launch plan for a machine. It is a
// PURE function: no I/O, no probing, no clock. That is what makes the whole
// platform x backend x RAM matrix table-testable.
//
// platform is the "<goos>-<goarch>" key from Hardware.Platform, backend is the
// probed Hardware.Backend, and ramGiB is Hardware.RAMGiB.
//
// Refusals, in order:
//   - ramGiB below MinRAMGiB -> ErrInsufficientRAM, before anything is planned
//   - no pinned artifact for the platform at all -> ErrArtifactNotPinned
//
// A probed backend that this pin cannot serve is NOT a refusal: Resolve
// degrades to the best build the platform actually has (see effectiveBackend),
// because a working CPU install beats an actionable error on a machine that
// could have run the model.
func Resolve(platform string, backend Backend, ramGiB float64) (Resolution, error) {
	return resolveWith(assetTable, platform, backend, ramGiB)
}

// resolveWith is Resolve against an explicit registry. The exported wrapper
// pins the compile-time table; tests pass a stub to drive the fail-closed
// branches without touching package state.
func resolveWith(table AssetTable, platform string, backend Backend, ramGiB float64) (Resolution, error) {
	if ramGiB < MinRAMGiB {
		return Resolution{}, fmt.Errorf("%w (detected %.1f GiB)", ErrInsufficientRAM, ramGiB)
	}

	effective := effectiveBackend(table, platform, backend)
	packing := packingFor(effective)

	assets, err := table.ArtifactSet(platform, effective, packing)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolving artifacts for %s/%s: %w", platform, effective, err)
	}

	return Resolution{
		Assets:         assets,
		Backend:        effective,
		Packing:        packing,
		Layers:         layersFor(platform, effective),
		ContextSize:    contextSizeFor(ramGiB),
		ImageMaxTokens: imageMaxTokensFor(effective),
		NeedsCudart:    hasCudart(assets),
	}, nil
}

// effectiveBackend maps the PROBED backend onto the backend this machine can
// actually be provisioned with. Four rules, applied in order:
//
//  1. Metal exists only on Apple Silicon. An Intel Mac has no Metal compute
//     path for this runtime and gets the CPU build.
//  2. CUDA and ROCm archives are published for x64 only, so a non-amd64
//     platform that reported one of them gets the CPU build
//     (linux-arm64 + CUDA -> cpu).
//  3. A CUDA tag with no archive for this platform clamps DOWN to the nearest
//     older pinned tag. This pin has no Windows cuda-12.8 archive, so a Windows
//     machine whose driver reports 12.8 or 13.0-13.2 is provisioned with the
//     12.4 build, which its newer driver still runs.
//  4. Anything else without a pinned archive falls back to the CPU build,
//     which every supported platform pins.
//
// If even the CPU build is missing the platform is not supported at all, and
// ArtifactSet fails closed with ErrArtifactNotPinned.
func effectiveBackend(table AssetTable, platform string, backend Backend) Backend {
	if backend == BackendMetal && platform != PlatformDarwinARM64 {
		return BackendCPU
	}
	if backend.x64Only() && platformArch(platform) != "amd64" {
		return BackendCPU
	}
	if backend.IsCUDA() {
		return clampCUDABackend(table, platform, backend)
	}
	if _, ok := table.RuntimeAsset(platform, backend); !ok {
		return BackendCPU
	}
	return backend
}

// clampCUDABackend returns the newest pinned CUDA tag that is not newer than
// the probed one, or BackendCPU when the platform has no CUDA archive at all.
//
// Clamping only ever goes DOWN: CUDA drivers are backwards compatible with
// binaries built against an older toolkit, while a binary built for a newer
// toolkit will not load on an older driver. Choosing 13.3 for a 12.8 driver
// would produce a runtime that fails at load time.
func clampCUDABackend(table AssetTable, platform string, backend Backend) Backend {
	if _, ok := table.RuntimeAsset(platform, backend); ok {
		return backend
	}
	probed := slices.Index(cudaTagsNewestFirst, backend)
	if probed < 0 {
		return BackendCPU
	}
	for _, candidate := range cudaTagsNewestFirst[probed+1:] {
		if _, ok := table.RuntimeAsset(platform, candidate); ok {
			return candidate
		}
	}
	return BackendCPU
}

// packingFor selects the weights quantization. Vulkan is the single backend
// without PQ2_0 (fork group-128) kernels, so it takes the dense PTQ1_0
// packing; every other backend takes PQ2_0, which has the faster prompt
// processing.
//
// There is deliberately NO RAM-based packing downgrade. With the MinRAMGiB gate
// at 16 GiB and the RAM-tiered context below it, PQ2_0 (6.70 GiB) plus the
// projector (0.63 GiB) plus the tier's KV cache fits every machine that is
// allowed to install at all; downgrading on a memory heuristic would trade
// away quality without buying headroom that is actually needed.
func packingFor(backend Backend) Packing {
	if backend == BackendVulkan {
		return PackingPTQ1_0
	}
	return PackingPQ2_0
}

// layersFor returns the -ngl value. The platform decides first, exactly in the
// order the demo's bonsai_llama_ngl does:
//
//   - An Intel Mac gets no offload at all — it has no Metal compute path for
//     this runtime.
//   - Apple Silicon always offloads every layer. Note this holds even when the
//     effective backend came out as CPU: the macOS arm64 archive IS the Metal
//     build (see the registry), so a CPU-labelled resolution on Apple Silicon
//     still runs on the GPU.
//
// Everywhere else the backend decides: all layers on a GPU-backed build, none
// on the CPU build.
func layersFor(platform string, backend Backend) int {
	switch {
	case platform == PlatformDarwinAMD64:
		return nglCPUOnly
	case platform == PlatformDarwinARM64:
		return nglAllGPU
	case backend.gpuAccelerated():
		return nglAllGPU
	default:
		return nglCPUOnly
	}
}

// imageMaxTokensFor returns the --image-max-tokens value. CUDA and ROCm run
// uncapped (fast datacenter GPUs absorb the vision prefill); Metal, Vulkan and
// CPU cap large images to keep latency reasonable.
func imageMaxTokensFor(backend Backend) int {
	if backend.IsCUDA() || backend == BackendROCm {
		return ImageMaxTokensUncapped
	}
	return imageMaxTokensCapped
}

// contextSizeFor returns the -c value for a RAM size in GiB.
//
// The context is never left unspecified and never set to the model's full
// training context: that request is memory-unaware, and with -ngl offloading
// the KV cache it picks the maximum and OOMs constrained machines. Instead the
// cap is sized to installed RAM, with this hybrid-attention model's FP16 KV
// cost at roughly 64 KiB per token:
//
//	RAM (GiB)   context
//	<= 11        8192
//	<= 23       16384
//	<= 35       32768
//	<= 71       65536
//	>  71      131072
//
// ramGiB is floored to a whole GiB before comparison, matching the integer
// arithmetic of the demo script these tiers come from. It matters on Linux,
// where MemTotal is reported slightly below the nominal size: a machine
// advertised as 24 GB reads ~23.x GiB and belongs to the 16384 tier, not the
// next one up. Flooring is also the conservative direction — it never grants a
// larger context than the demo would.
func contextSizeFor(ramGiB float64) int {
	wholeGiB := int(ramGiB)
	switch {
	case wholeGiB <= 11:
		return 8192
	case wholeGiB <= 23:
		return 16384
	case wholeGiB <= 35:
		return 32768
	case wholeGiB <= 71:
		return 65536
	default:
		return contextTierTop
	}
}

// hasCudart reports whether an install set carries the paired CUDA runtime DLL
// archive. It is derived from the set itself rather than recomputed from the
// platform and backend, so the flag can never disagree with what is downloaded.
func hasCudart(assets []Asset) bool {
	return slices.ContainsFunc(assets, func(a Asset) bool {
		return a.Component == ComponentCudart
	})
}

// platformArch returns the GOARCH half of a "<goos>-<goarch>" platform key.
func platformArch(platform string) string {
	_, arch, _ := strings.Cut(platform, "-")
	return arch
}
