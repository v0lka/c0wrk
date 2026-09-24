// Package embeddedllm implements c0wrk's embedded local-LLM subsystem: a
// pinned inference runtime plus pinned Ternary-Bonsai-2-27B weights, downloaded
// on an explicit user action, verified fail-closed, and supervised as a
// loopback OpenAI-compatible server.
//
// Supply chain (ADR-066 D11, ASI04): every artifact in this package is a
// compile-time pin. The subsystem performs no upstream version queries and no
// automatic updates; a pin advances only when a developer raises it after a CVE
// review. Runtime checksums come from the GitHub REST per-asset `digest` field,
// model checksums from the Hugging Face LFS OID (which *is* the SHA256).
//
// See specs/domains/embedded-llm.md and specs/decisions/066-embedded-llm-runtime.md.
package embeddedllm

import (
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// RuntimeTag is the pinned PrismML-Eng/llama.cpp fork release.
//
// It is a fork, never upstream llama.cpp: stock rejects the PQ2_0/PTQ1_0
// ternary packings outright and silently produces garbage on plain Q2_0
// (ADR-066 D11). Bumping this constant requires updating every runtime and
// cudart URL/checksum below and a CVE review of the old→new range.
const RuntimeTag = "prism-b10709-9a9394a"

// ModelRevision is the pinned Hugging Face revision of the weights repository.
//
// The resolve URLs are built from this immutable commit SHA rather than a
// floating branch name, so "the pin" cannot move under a rebuild: a branch
// would silently re-point at whatever upstream published last.
const (
	modelRepoOwner = "prism-ml"
	modelRepoName  = "Ternary-Bonsai-2-27B-gguf"
	ModelRevision  = "6ed5e12bf84b7a63069882c91dd9e9218647d17b"
)

const (
	runtimeReleaseBase = "https://github.com/PrismML-Eng/llama.cpp/releases/download/" + RuntimeTag + "/"
	modelResolveBase   = "https://huggingface.co/" + modelRepoOwner + "/" + modelRepoName + "/resolve/" + ModelRevision + "/"
)

// Backend (the accelerator vocabulary the artifact tables below are keyed by)
// is declared in hardware.go, together with its IsCUDA/x64Only/gpuAccelerated
// predicates. The registry consumes it; it does not redefine it.

// Packing is the ternary quantization of the weights on disk (ADR-066 D2).
type Packing string

const (
	// PackingPQ2_0 is the default 6.71 GiB packing.
	PackingPQ2_0 Packing = "PQ2_0"
	// PackingPTQ1_0 is the 5.54 GiB packing used on Vulkan (which has no
	// PQ2_0 kernels) and whenever memory is short.
	PackingPTQ1_0 Packing = "PTQ1_0"
)

// Component identifies one downloadable artifact for progress reporting.
type Component string

const (
	ComponentRuntime Component = "runtime"
	ComponentCudart  Component = "cudart" // Windows CUDA only: the paired DLL archive
	ComponentModel   Component = "model"
	ComponentMMProj  Component = "mmproj"
)

// Platform keys use the toolmanager.Platform() shape, "<goos>-<goarch>".
// windows-arm64 is deliberately absent: it is out of scope (ADR-066 D9), so
// every lookup for it fails closed instead of resolving to a wrong artifact.
const (
	PlatformDarwinAMD64  = "darwin-amd64"
	PlatformDarwinARM64  = "darwin-arm64"
	PlatformLinuxAMD64   = "linux-amd64"
	PlatformLinuxARM64   = "linux-arm64"
	PlatformWindowsAMD64 = "windows-amd64"
)

// supportedPlatforms is the exhaustive set of platform keys this registry pins
// artifacts for. Kept sorted for deterministic diagnostics and tests.
var supportedPlatforms = []string{
	PlatformDarwinAMD64,
	PlatformDarwinARM64,
	PlatformLinuxAMD64,
	PlatformLinuxARM64,
	PlatformWindowsAMD64,
}

// Asset is one downloadable component with its integrity pin.
//
// SHA256 is fail-closed: an empty value means REFUSE the download, never
// "skip verification" (see Downloader.Download). SizeBytes is the exact
// expected size — it drives the disk guard, the progress total and the
// transfer ceiling.
type Asset struct {
	Component   Component
	URL         string
	SHA256      string
	SizeBytes   int64
	ArchiveName string // on-disk file name in the download cache
}

// ErrArtifactNotPinned reports that the registry has no artifact for a
// (platform, backend, packing) combination. It is a refusal, not a fallback:
// callers must not substitute a neighboring platform's or backend's bytes.
var ErrArtifactNotPinned = errors.New("no pinned embedded-LLM artifact")

// ModelPQ2_0 and friends are the pinned weights. Sizes and SHA256 values are
// the Hugging Face LFS metadata for ModelRevision, captured verbatim.
const (
	modelFilePQ2_0  = "Ternary-Bonsai-2-27B-PQ2_0.gguf"
	modelFilePTQ1_0 = "Ternary-Bonsai-2-27B-PTQ1_0.gguf"
	mmprojFileQ8_0  = "Ternary-Bonsai-2-27B-mmproj-Q8_0.gguf"
)

// modelAssets maps each packing to its pinned GGUF.
var modelAssets = map[Packing]Asset{
	PackingPQ2_0: newModelAsset(modelFilePQ2_0,
		"3907dc1658db1f78a9826bf8d5bcb8dc65db0d466388937af57f2294fae62ec1", 7206168928),
	PackingPTQ1_0: newModelAsset(modelFilePTQ1_0,
		"53107f530aa52eb00912263ab1ee29bd199261c87cd7b4ad4ca1318c1fe33ee3", 5946648928),
}

// mmprojAsset is the vision projector. It is installed for EVERY backend and
// packing (ADR-066 D2), so image input works regardless of what was resolved.
var mmprojAsset = Asset{
	Component:   ComponentMMProj,
	URL:         modelResolveBase + mmprojFileQ8_0,
	SHA256:      "6807ede61d570bb86ba34b756a0fa109edc33668604de867c6ea6d8f1d631903",
	SizeBytes:   629246976,
	ArchiveName: mmprojFileQ8_0,
}

// runtimeAssets maps platform → backend → the pinned llama-server archive.
//
// Windows CUDA gap (verified against the release's 23 assets): this pin ships
// win-cuda-12.4-x64, win-cuda-13.3-x64 and win-cuda-13.4-arm64 — there is NO
// windows cuda-12.8 archive, and no cuda-12.8 cudart either (linux does have
// bin-linux-cuda-12.8-x64). windows-amd64 + BackendCUDA128 therefore has no
// entry and fails closed with ErrArtifactNotPinned. Resolution must not select
// that pair on Windows; it has to clamp to a pinned CUDA tag (12.4 or 13.3) or
// fall back to another backend.
//
// ROCm on Windows is the HIP/Radeon build; ROCm and CUDA are x64-only, so
// linux-arm64 pins CPU and Vulkan only.
var runtimeAssets = map[string]map[Backend]Asset{
	PlatformDarwinAMD64: {
		// Intel Mac: no usable GPU backend, so only the CPU build is pinned.
		// Metal is deliberately absent here — the probe only yields Metal on
		// darwin/arm64, and an Intel Mac runs with -ngl 0.
		BackendCPU: newRuntimeAsset("llama-"+RuntimeTag+"-bin-macos-x64.tar.gz",
			"a4417a984fa92f72f01e40dfb8fb471a16357c08df62e72e41b8a190a29d916d", 11515388),
	},
	PlatformDarwinARM64: {
		BackendMetal: newRuntimeAsset("llama-"+RuntimeTag+"-bin-macos-arm64.tar.gz",
			"f9cdf245fb7b832f1996dd776b321d4ae1f23b6d88c380100f636742c3a980ff", 11500187),
		// Same archive: the macOS arm64 build is the Metal build, and it is
		// also what a CPU-only run on Apple Silicon uses.
		BackendCPU: newRuntimeAsset("llama-"+RuntimeTag+"-bin-macos-arm64.tar.gz",
			"f9cdf245fb7b832f1996dd776b321d4ae1f23b6d88c380100f636742c3a980ff", 11500187),
	},
	PlatformLinuxAMD64: {
		BackendCPU: newRuntimeAsset("llama-"+RuntimeTag+"-bin-ubuntu-x64.tar.gz",
			"48b487f00fd2b27bc3ef77c701b43c1c23a4af484d2a203ae87d0efc41506728", 17108139),
		BackendVulkan: newRuntimeAsset("llama-"+RuntimeTag+"-bin-ubuntu-vulkan-x64.tar.gz",
			"4d7f858539d0207cf64e90beb83fcb7e076580d52856580f223cbecdd3ef6d03", 34248149),
		BackendROCm: newRuntimeAsset("llama-"+RuntimeTag+"-bin-ubuntu-rocm-7.2-x64.tar.gz",
			"230f879d538bb9f794d25c908bc8c0f676774c41c3e70ea719131c86d899841d", 139908720),
		BackendCUDA124: newRuntimeAsset("llama-"+RuntimeTag+"-bin-linux-cuda-12.4-x64.tar.gz",
			"f542fdcc818562359e947db65e0b11c4658dd5ca3bd240490448252e817d8e7a", 260869644),
		BackendCUDA128: newRuntimeAsset("llama-"+RuntimeTag+"-bin-linux-cuda-12.8-x64.tar.gz",
			"8aec67eb023b251712c7e6490f367b5671bf587eced1436a9b85f4a90c3b7d3d", 167241119),
		BackendCUDA133: newRuntimeAsset("llama-"+RuntimeTag+"-bin-linux-cuda-13.3-x64.tar.gz",
			"7e01a434e513b373026c347cd008502ab04f6307d1cab71fcd4cea212b4fdbb0", 146381070),
	},
	PlatformLinuxARM64: {
		BackendCPU: newRuntimeAsset("llama-"+RuntimeTag+"-bin-ubuntu-arm64.tar.gz",
			"f97eeed89b61786c41aba66caa5d39aa17e9435754ee19ad36a7de8ca3f32f49", 13753343),
		BackendVulkan: newRuntimeAsset("llama-"+RuntimeTag+"-bin-ubuntu-vulkan-arm64.tar.gz",
			"d8108de54933bc8212f31a4d13d3f6d517820467837a6dce866b847506ceacc0", 28002242),
	},
	PlatformWindowsAMD64: {
		BackendCPU: newRuntimeAsset("llama-"+RuntimeTag+"-bin-win-cpu-x64.zip",
			"92cd4d1cee11107593ff87d77eb57b02d804c86dd4b13224e18ba963a4271ad8", 18785841),
		BackendVulkan: newRuntimeAsset("llama-"+RuntimeTag+"-bin-win-vulkan-x64.zip",
			"fabef609b588cbbed85b5f10b45809c46088f0a63caca7976054034e24b40836", 29908460),
		BackendROCm: newRuntimeAsset("llama-"+RuntimeTag+"-bin-win-hip-radeon-x64.zip",
			"86792e1590232e4ac702df2fe961fe196552ad7642658b5347834f539a1525b6", 321932526),
		BackendCUDA124: newRuntimeAsset("llama-"+RuntimeTag+"-bin-win-cuda-12.4-x64.zip",
			"f565c8428c1f108311f65ed97f02425188b3aa3c745c2bc597521bbd24bcbbc9", 253442371),
		BackendCUDA133: newRuntimeAsset("llama-"+RuntimeTag+"-bin-win-cuda-13.3-x64.zip",
			"d656f217172c489706df40951e46bef647eb1a81eb8eeb1398c8f38bd7fa9725", 145031500),
		// BackendCUDA128 intentionally absent — see the gap note above.
	},
}

// cudartAssets maps platform → backend → the paired CUDA runtime DLL archive.
// Windows CUDA builds do not bundle cudart, so it is a second component with
// its own progress bar and its own checksum (ADR-066 D9). Linux CUDA links
// against the system CUDA installation and needs no companion archive.
var cudartAssets = map[string]map[Backend]Asset{
	PlatformWindowsAMD64: {
		BackendCUDA124: newCudartAsset("cudart-llama-bin-win-cuda-12.4-x64.zip",
			"8c79a9b226de4b3cacfd1f83d24f962d0773be79f1e7b75c6af4ded7e32ae1d6", 391443627),
		BackendCUDA133: newCudartAsset("cudart-llama-bin-win-cuda-13.3-x64.zip",
			"1462a050eb4c684921ba51dcc4cc488a036674c3e73e9945ee705b854808d03e", 390970417),
	},
}

func newRuntimeAsset(name, sha256Hex string, size int64) Asset {
	return Asset{
		Component:   ComponentRuntime,
		URL:         runtimeReleaseBase + name,
		SHA256:      sha256Hex,
		SizeBytes:   size,
		ArchiveName: name,
	}
}

func newCudartAsset(name, sha256Hex string, size int64) Asset {
	a := newRuntimeAsset(name, sha256Hex, size)
	a.Component = ComponentCudart
	return a
}

func newModelAsset(name, sha256Hex string, size int64) Asset {
	return Asset{
		Component:   ComponentModel,
		URL:         modelResolveBase + name,
		SHA256:      sha256Hex,
		SizeBytes:   size,
		ArchiveName: name,
	}
}

// SupportedPlatforms returns the platform keys this registry pins artifacts
// for, in stable order.
func SupportedPlatforms() []string {
	return slices.Clone(supportedPlatforms)
}

// IsSupportedPlatform reports whether platform is one of the pinned keys.
func IsSupportedPlatform(platform string) bool {
	return slices.Contains(supportedPlatforms, platform)
}

// RuntimeAsset returns the pinned llama-server archive for a platform and
// backend. The boolean is false — a fail-closed refusal — when the pair is not
// pinned; callers must not substitute a different backend's bytes.
func RuntimeAsset(platform string, backend Backend) (Asset, bool) {
	a, ok := runtimeAssets[platform][backend]
	if !ok || a.SHA256 == "" || a.URL == "" {
		return Asset{}, false
	}
	return a, true
}

// CudartAsset returns the paired CUDA runtime archive for a platform and
// backend. ok is false for every non-Windows-CUDA pair, which is the normal
// case and not an error.
func CudartAsset(platform string, backend Backend) (Asset, bool) {
	a, ok := cudartAssets[platform][backend]
	if !ok || a.SHA256 == "" || a.URL == "" {
		return Asset{}, false
	}
	return a, true
}

// ModelAsset returns the pinned GGUF for a packing.
func ModelAsset(packing Packing) (Asset, bool) {
	a, ok := modelAssets[packing]
	if !ok || a.SHA256 == "" || a.URL == "" {
		return Asset{}, false
	}
	return a, true
}

// MMProjAsset returns the pinned vision projector. It is unconditional: every
// install gets it regardless of backend or packing.
func MMProjAsset() Asset { return mmprojAsset }

// SupportedPackings returns every packing the registry pins, in stable order.
func SupportedPackings() []Packing {
	return []Packing{PackingPQ2_0, PackingPTQ1_0}
}

// ArtifactSet resolves the complete, ordered download set for a machine:
// runtime, then the paired cudart when the platform/backend needs one, then
// the model weights, then the vision projector.
//
// It is fail-closed: a missing pin for any required component returns an error
// wrapping ErrArtifactNotPinned rather than a partial set, because a partial
// set would install a runtime that cannot serve the model.
func ArtifactSet(platform string, backend Backend, packing Packing) ([]Asset, error) {
	if !IsSupportedPlatform(platform) {
		return nil, fmt.Errorf("%w: platform %q (supported: %s)",
			ErrArtifactNotPinned, platform, strings.Join(supportedPlatforms, ", "))
	}
	runtime, ok := RuntimeAsset(platform, backend)
	if !ok {
		return nil, fmt.Errorf("%w: runtime for platform %q backend %q",
			ErrArtifactNotPinned, platform, backend)
	}
	model, ok := ModelAsset(packing)
	if !ok {
		return nil, fmt.Errorf("%w: model packing %q", ErrArtifactNotPinned, packing)
	}

	set := make([]Asset, 0, 4)
	set = append(set, runtime)
	if cudart, ok := CudartAsset(platform, backend); ok {
		set = append(set, cudart)
	}
	set = append(set, model, MMProjAsset())
	return set, nil
}

// TotalBytes sums the exact pinned sizes of a set of assets.
func TotalBytes(assets []Asset) int64 {
	var total int64
	for _, a := range assets {
		total += a.SizeBytes
	}
	return total
}

// ValidateRegistry self-checks every pinned asset. It is the guard that turns
// a hand-edited pin table into a compile-adjacent failure instead of a runtime
// supply-chain hole: every asset must carry a URL, a well-formed lowercase
// hex SHA256, a positive exact size and a file name.
func ValidateRegistry() error {
	return validateTables(runtimeAssets, cudartAssets, modelAssets, mmprojAsset, supportedPlatforms)
}

// validateTables is ValidateRegistry over explicit tables rather than the
// package-level ones, so a test can prove the validator has teeth without
// mutating shared registry state (which would leak into every later test in
// the package).
func validateTables(runtimes map[string]map[Backend]Asset, cudarts map[string]map[Backend]Asset,
	models map[Packing]Asset, mmproj Asset, platforms []string,
) error {
	var errs []error
	check := func(where string, a Asset) {
		if a.URL == "" {
			errs = append(errs, fmt.Errorf("%s: empty URL", where))
		}
		if !validSHA256Hex(a.SHA256) {
			errs = append(errs, fmt.Errorf("%s: invalid sha256 %q", where, a.SHA256))
		}
		if a.SizeBytes <= 0 {
			errs = append(errs, fmt.Errorf("%s: non-positive size %d", where, a.SizeBytes))
		}
		if a.ArchiveName == "" {
			errs = append(errs, fmt.Errorf("%s: empty archive name", where))
		}
		if strings.Contains(a.ArchiveName, "/") || strings.Contains(a.ArchiveName, "\\") {
			errs = append(errs, fmt.Errorf("%s: archive name %q is not a plain file name", where, a.ArchiveName))
		}
	}

	packings := make([]Packing, 0, len(models))
	for packing := range models {
		packings = append(packings, packing)
	}
	slices.Sort(packings)

	for _, platform := range platforms {
		for backend, a := range runtimes[platform] {
			where := fmt.Sprintf("runtime %s/%s", platform, backend)
			check(where, a)
			if a.Component != ComponentRuntime {
				errs = append(errs, fmt.Errorf("%s: component is %q, want %q", where, a.Component, ComponentRuntime))
			}
			if !strings.HasPrefix(a.URL, runtimeReleaseBase) {
				errs = append(errs, fmt.Errorf("%s: URL is not under the pinned release", where))
			}
			if want := runtimeReleaseBase + a.ArchiveName; a.URL != want {
				errs = append(errs, fmt.Errorf("%s: URL %q does not match archive name", where, a.URL))
			}
		}
		for backend, a := range cudarts[platform] {
			where := fmt.Sprintf("cudart %s/%s", platform, backend)
			check(where, a)
			if a.Component != ComponentCudart {
				errs = append(errs, fmt.Errorf("%s: component is %q, want %q", where, a.Component, ComponentCudart))
			}
			// A cudart may only exist where its runtime exists, otherwise the
			// set would fetch a DLL bundle for a server it cannot run.
			if _, ok := runtimes[platform][backend]; !ok {
				errs = append(errs, fmt.Errorf("%s: cudart pinned without a runtime", where))
			}
		}
		// Every supported platform must pin its guaranteed fallback backend,
		// otherwise a probe miss has nowhere to land.
		if _, ok := runtimes[platform][BackendCPU]; !ok {
			errs = append(errs, fmt.Errorf("platform %s: no CPU fallback runtime pinned", platform))
		}
	}

	for _, packing := range packings {
		a := models[packing]
		where := fmt.Sprintf("model %s", packing)
		check(where, a)
		if a.Component != ComponentModel {
			errs = append(errs, fmt.Errorf("%s: component is %q, want %q", where, a.Component, ComponentModel))
		}
		if !strings.HasPrefix(a.URL, modelResolveBase) {
			errs = append(errs, fmt.Errorf("%s: URL is not under the pinned HF revision", where))
		}
		if want := modelResolveBase + a.ArchiveName; a.URL != want {
			errs = append(errs, fmt.Errorf("%s: URL %q does not match archive name", where, a.URL))
		}
	}

	check("mmproj", mmproj)
	if mmproj.Component != ComponentMMProj {
		errs = append(errs, fmt.Errorf("mmproj: component is %q, want %q", mmproj.Component, ComponentMMProj))
	}
	if !strings.HasPrefix(mmproj.URL, modelResolveBase) {
		errs = append(errs, errors.New("mmproj: URL is not under the pinned HF revision"))
	}

	return errors.Join(errs...)
}

// sha256HexLen is the byte length of a SHA-256 digest.
const sha256HexLen = 32

// validSHA256Hex reports whether s is a 64-character lowercase hex digest.
// Uppercase is rejected on purpose: the pins are copied verbatim from GitHub
// `digest` and HF LFS OID, both lowercase, and a mixed-case table entry is a
// transcription bug worth failing on.
func validSHA256Hex(s string) bool {
	if len(s) != 2*sha256HexLen {
		return false
	}
	if s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
