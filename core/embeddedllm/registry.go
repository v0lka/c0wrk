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

// RuntimeTag is the pinned PrismML-Eng/llama.cpp fork release — stock rejects
// PQ2_0/PTQ1_0 and corrupts plain Q2_0 (ADR-066, Forces #1). A bump
// re-records every URL+checksum below and needs a CVE review of the old→new
// range (ADR-066 D11).
const RuntimeTag = "prism-b12128-d7b2896"

// ModelRevision is the pinned Hugging Face revision of the weights repository.
// The resolve URLs are built from this immutable commit SHA, not a floating
// branch, so the pin cannot move under a rebuild.
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
	// PackingPTQ1_0 is the 5.54 GiB packing, chosen by decidePacking's four
	// triggers — installed RAM alone never selects it
	// (specs/domains/embedded-llm.md, Hardware probe → resolution).
	PackingPTQ1_0 Packing = "PTQ1_0"
)

// Component identifies one downloadable artifact for progress reporting.
type Component string

const (
	ComponentRuntime Component = "runtime"
	ComponentCudart  Component = "cudart" // Windows CUDA only: the paired DLL archive
	ComponentModel   Component = "model"
	ComponentMMProj  Component = "mmproj"
	// mtplx-engine components (ADR-092): managed CPython, closed wheel set,
	// MLX weight pack (vision tower and MTP head inside).
	ComponentPython       Component = "python"
	ComponentEngineWheels Component = "wheels"
	ComponentMLXPack      Component = "pack"
)

// Platform keys use the toolmanager.Platform() shape, "<goos>-<goarch>".
// windows-arm64 is deliberately absent (out of scope, ADR-066 D9), so every
// lookup for it fails closed.
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

// Asset is one downloadable component with its integrity pin: SHA256 is
// fail-closed (empty REFUSES, never skips verification), SizeBytes the exact
// expected size behind the disk guard, progress total and transfer ceiling.
type Asset struct {
	Component   Component
	URL         string
	SHA256      string
	SizeBytes   int64
	ArchiveName string // on-disk file name in the download cache
}

// ErrArtifactNotPinned reports that the registry has no artifact for a
// (platform, backend, packing) combination — a refusal, never a fallback to
// neighboring bytes.
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

// mmprojAsset is the vision projector, installed for EVERY backend and
// packing (ADR-066 D2) so image input always works.
var mmprojAsset = Asset{
	Component:   ComponentMMProj,
	URL:         modelResolveBase + mmprojFileQ8_0,
	SHA256:      "6807ede61d570bb86ba34b756a0fa109edc33668604de867c6ea6d8f1d631903",
	SizeBytes:   629246976,
	ArchiveName: mmprojFileQ8_0,
}

// runtimeAssets maps platform → backend → the pinned llama-server archive.
// windows-amd64 + BackendCUDA128 has NO entry and fails closed (this pin ships
// no Windows cuda-12.8 archive — see specs/domains/embedded-llm.md). ROCm on
// Windows is the HIP/Radeon build; ROCm and CUDA are x64-only.
var runtimeAssets = map[string]map[Backend]Asset{
	PlatformDarwinAMD64: {
		// Intel Mac: no usable GPU backend, so only the CPU build is pinned
		// (the probe yields Metal on darwin/arm64 only).
		BackendCPU: newRuntimeAsset("llama-"+RuntimeTag+"-bin-macos-x64.tar.gz",
			"50aa0c304908a12a6ddea723c111bd6abb91fbd6a8ccbb573874f47fb801d14f", 12620946),
	},
	PlatformDarwinARM64: {
		BackendMetal: newRuntimeAsset("llama-"+RuntimeTag+"-bin-macos-arm64.tar.gz",
			"7072bcece7b2ae5cc97db85b65f4a57d96a4f1b926555bc97cf48d6357b3e532", 13172028),
		// Same archive: the arm64 build IS the Metal build, and what a CPU-only
		// run on Apple Silicon uses.
		BackendCPU: newRuntimeAsset("llama-"+RuntimeTag+"-bin-macos-arm64.tar.gz",
			"7072bcece7b2ae5cc97db85b65f4a57d96a4f1b926555bc97cf48d6357b3e532", 13172028),
	},
	PlatformLinuxAMD64: {
		BackendCPU: newRuntimeAsset("llama-"+RuntimeTag+"-bin-ubuntu-x64.tar.gz",
			"67bdd24e458715fd2fbb128297c44f480b3b4bbea254adf5e571feb3e82c5f53", 19765994),
		BackendVulkan: newRuntimeAsset("llama-"+RuntimeTag+"-bin-ubuntu-vulkan-x64.tar.gz",
			"a73a76f26b161e2fff3815cc3ae6f1daeee5991e1269982561aa620e8e07cca4", 33757914),
		BackendROCm: newRuntimeAsset("llama-"+RuntimeTag+"-bin-ubuntu-rocm-7.2-x64.tar.gz",
			"b77438468ae127dbf732cf84704a87cc97c3d58add80b157670c23efb802f435", 179147741),
		BackendCUDA124: newRuntimeAsset("llama-"+RuntimeTag+"-bin-linux-cuda-12.4-x64.tar.gz",
			"0d31cc673f400004533ff407a133d957f7fc322e67457958693f752c6d87d342", 369464173),
		BackendCUDA128: newRuntimeAsset("llama-"+RuntimeTag+"-bin-linux-cuda-12.8-x64.tar.gz",
			"d800a807295e73742d4dff7d3aa3a3438d6147882c9b609ef8bacba333bead01", 228535760),
		BackendCUDA133: newRuntimeAsset("llama-"+RuntimeTag+"-bin-linux-cuda-13.3-x64.tar.gz",
			"9f3a8d2de2b69936b8ec7c994579ad419cb2918785483d80091b6b2776b880bd", 204301954),
	},
	PlatformLinuxARM64: {
		BackendCPU: newRuntimeAsset("llama-"+RuntimeTag+"-bin-ubuntu-arm64.tar.gz",
			"2f292d018686ad5cbba5743dbbcaaf8999fa8acb20a8b376090d91c5e7e8c35c", 15023939),
		BackendVulkan: newRuntimeAsset("llama-"+RuntimeTag+"-bin-ubuntu-vulkan-arm64.tar.gz",
			"ae8ecf507d7544d489191d121887832f84387d966a37d42bc30106277dec60a4", 25969068),
	},
	PlatformWindowsAMD64: {
		BackendCPU: newRuntimeAsset("llama-"+RuntimeTag+"-bin-win-cpu-x64.zip",
			"a6e41db401ce93a28f4a2ed20fb7809455302231dd37037c9f1b4505d33fe00b", 21387846),
		BackendVulkan: newRuntimeAsset("llama-"+RuntimeTag+"-bin-win-vulkan-x64.zip",
			"ec1479514a34272c815b3d194046d27003867aa9c95069d9cd3031b5c31e01ff", 28692091),
		BackendROCm: newRuntimeAsset("llama-"+RuntimeTag+"-bin-win-hip-radeon-x64.zip",
			"6829fc192d2c77d11bbfbb02a198328d0026f25f0f8b60cf681eb348fae6e1ce", 360792610),
		BackendCUDA124: newRuntimeAsset("llama-"+RuntimeTag+"-bin-win-cuda-12.4-x64.zip",
			"14969ae6831abfc836295e8f4b8e09e686a063b559a8c974412001d9f5ce4f7f", 355011546),
		BackendCUDA133: newRuntimeAsset("llama-"+RuntimeTag+"-bin-win-cuda-13.3-x64.zip",
			"86c6546685ffe2f0954bac062d0bdb347694a2b5eee6f5ddeb1bbfe82d2be1b6", 198836745),
		// BackendCUDA128 intentionally absent — see the gap note above.
	},
}

// cudartAssets maps platform → backend → the paired CUDA runtime DLL archive:
// a second component with its own progress and checksum, Windows-only because
// the Windows CUDA builds do not bundle cudart (ADR-066 D9); Linux CUDA links
// the system installation.
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
// backend; false is the fail-closed refusal for an unpinned pair.
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

// ArtifactSet resolves the ordered download set: runtime, paired cudart when
// needed, model, projector. Fail-closed — a partial set would install a
// runtime that cannot serve the model.
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

// ValidateRegistry self-checks every pinned asset — the guard that turns a
// hand-edited pin table into a test-time failure instead of a runtime
// supply-chain hole. See specs/domains/embedded-llm.md (Key Files, registry.go).
func ValidateRegistry() error {
	return errors.Join(
		validateTables(runtimeAssets, cudartAssets, modelAssets, mmprojAsset, supportedPlatforms),
		ValidateMLXPins(),
	)
}

// validateTables is ValidateRegistry over explicit tables, so a test proves
// the validator has teeth without mutating shared registry state.
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
// Uppercase is rejected on purpose: the pins are copied verbatim from sources
// that are lowercase, and a mixed-case entry is a transcription bug.
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
