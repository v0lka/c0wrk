package embeddedllm

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// ramTiers are the RAM sizes Resolve can actually be called with, given the
// MinRAMGiB gate. Every entry is at or above the gate; the sub-16 GiB refusals
// and the unreachable smallest context tier are covered separately by
// TestResolveRAMGate and TestContextSizeTiers.
var ramTiers = []struct {
	ramGiB      float64
	wantContext int
}{
	// The gate boundary itself.
	{ramGiB: MinRAMGiB, wantContext: 16384},
	{ramGiB: 16.5, wantContext: 16384},
	{ramGiB: 23, wantContext: 16384},
	// A Linux machine advertised as 24 GB reports MemTotal slightly below the
	// nominal size; flooring keeps it in the 16384 tier, matching the demo.
	{ramGiB: 23.4, wantContext: 16384},
	{ramGiB: 24, wantContext: 32768},
	{ramGiB: 32, wantContext: 32768},
	{ramGiB: 35, wantContext: 32768},
	{ramGiB: 36, wantContext: 65536},
	{ramGiB: 48, wantContext: 65536},
	{ramGiB: 64, wantContext: 65536},
	{ramGiB: 71, wantContext: 65536},
	{ramGiB: 72, wantContext: contextTierTop},
	{ramGiB: 128, wantContext: contextTierTop},
	{ramGiB: 512, wantContext: contextTierTop},
}

// matrixComponents are the two possible install sets, in download order.
var (
	componentsPlain  = []Component{ComponentRuntime, ComponentModel, ComponentMMProj}
	componentsCudart = []Component{ComponentRuntime, ComponentCudart, ComponentModel, ComponentMMProj}
)

// matrixCase is one (platform, probed backend) cell of the resolution matrix
// with the expectations that do NOT depend on RAM. Context size is orthogonal
// and comes from ramTiers, so the two tables are crossed in
// TestResolveFullMatrix instead of being multiplied out by hand.
type matrixCase struct {
	name string
	// platform is the "<goos>-<goarch>" key.
	platform string
	// probed is what the hardware probe reported.
	probed Backend
	// wantBackend is what the artifacts are actually resolved for, after the
	// architecture and pin-availability rules.
	wantBackend Backend
	// wantArchive is the runtime archive name, which pins down exactly which
	// build was selected — the strongest available check that a degradation
	// really happened.
	wantArchive string

	wantPacking     Packing
	wantLayers      int
	wantImageTokens int
	wantCudart      bool
	wantComponents  []Component
}

// resolveMatrix enumerates every supported platform against every backend the
// probe can report. Expectations are written out explicitly rather than
// recomputed from the production rules, so a rule change cannot silently
// rewrite its own test.
var resolveMatrix = []matrixCase{
	// ── darwin-amd64: Intel Mac. No Metal, so nothing offloads and every
	// probed accelerator degrades to the pinned CPU build.
	{
		name: "intel mac cpu", platform: PlatformDarwinAMD64, probed: BackendCPU,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-macos-x64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 0, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
	{
		name: "intel mac probed metal degrades to cpu", platform: PlatformDarwinAMD64, probed: BackendMetal,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-macos-x64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 0, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
	{
		name: "intel mac probed vulkan degrades to cpu", platform: PlatformDarwinAMD64, probed: BackendVulkan,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-macos-x64.tar.gz",
		// Packing follows the EFFECTIVE backend, so a Mac that happens to have
		// vulkaninfo installed still gets the faster PQ2_0 weights.
		wantPacking: PackingPQ2_0, wantLayers: 0, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
	{
		name: "intel mac probed cuda degrades to cpu", platform: PlatformDarwinAMD64, probed: BackendCUDA124,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-macos-x64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 0, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
	{
		name: "intel mac probed rocm degrades to cpu", platform: PlatformDarwinAMD64, probed: BackendROCm,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-macos-x64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 0, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},

	// ── darwin-arm64: Apple Silicon. The arm64 archive IS the Metal build, so
	// it always offloads every layer — even when the effective backend is CPU.
	{
		name: "apple silicon metal", platform: PlatformDarwinARM64, probed: BackendMetal,
		wantBackend: BackendMetal, wantArchive: "llama-" + RuntimeTag + "-bin-macos-arm64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 99, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
	{
		name: "apple silicon cpu still offloads", platform: PlatformDarwinARM64, probed: BackendCPU,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-macos-arm64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 99, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
	{
		name: "apple silicon probed vulkan degrades to cpu", platform: PlatformDarwinARM64, probed: BackendVulkan,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-macos-arm64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 99, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
	{
		name: "apple silicon probed cuda degrades to cpu", platform: PlatformDarwinARM64, probed: BackendCUDA133,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-macos-arm64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 99, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},

	// ── linux-amd64: the full backend matrix is pinned here.
	{
		name: "linux x64 cpu", platform: PlatformLinuxAMD64, probed: BackendCPU,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-ubuntu-x64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 0, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
	{
		name: "linux x64 vulkan", platform: PlatformLinuxAMD64, probed: BackendVulkan,
		wantBackend: BackendVulkan, wantArchive: "llama-" + RuntimeTag + "-bin-ubuntu-vulkan-x64.tar.gz",
		wantPacking: PackingPTQ1_0, wantLayers: 99, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
	{
		name: "linux x64 rocm", platform: PlatformLinuxAMD64, probed: BackendROCm,
		wantBackend: BackendROCm, wantArchive: "llama-" + RuntimeTag + "-bin-ubuntu-rocm-7.2-x64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 99, wantImageTokens: ImageMaxTokensUncapped,
		wantComponents: componentsPlain,
	},
	{
		name: "linux x64 cuda 12.4", platform: PlatformLinuxAMD64, probed: BackendCUDA124,
		wantBackend: BackendCUDA124, wantArchive: "llama-" + RuntimeTag + "-bin-linux-cuda-12.4-x64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 99, wantImageTokens: ImageMaxTokensUncapped,
		// Linux CUDA links against the system CUDA installation, so there is
		// no companion archive — that is a Windows-only requirement.
		wantComponents: componentsPlain,
	},
	{
		name: "linux x64 cuda 12.8", platform: PlatformLinuxAMD64, probed: BackendCUDA128,
		wantBackend: BackendCUDA128, wantArchive: "llama-" + RuntimeTag + "-bin-linux-cuda-12.8-x64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 99, wantImageTokens: ImageMaxTokensUncapped,
		wantComponents: componentsPlain,
	},
	{
		name: "linux x64 cuda 13.3", platform: PlatformLinuxAMD64, probed: BackendCUDA133,
		wantBackend: BackendCUDA133, wantArchive: "llama-" + RuntimeTag + "-bin-linux-cuda-13.3-x64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 99, wantImageTokens: ImageMaxTokensUncapped,
		wantComponents: componentsPlain,
	},
	{
		name: "linux x64 probed metal degrades to cpu", platform: PlatformLinuxAMD64, probed: BackendMetal,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-ubuntu-x64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 0, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},

	// ── linux-arm64: CPU and Vulkan only. CUDA and ROCm are x64-only, so a
	// probe that reported either must land on the CPU build.
	{
		name: "linux arm64 cpu", platform: PlatformLinuxARM64, probed: BackendCPU,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-ubuntu-arm64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 0, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
	{
		name: "linux arm64 vulkan", platform: PlatformLinuxARM64, probed: BackendVulkan,
		wantBackend: BackendVulkan, wantArchive: "llama-" + RuntimeTag + "-bin-ubuntu-vulkan-arm64.tar.gz",
		wantPacking: PackingPTQ1_0, wantLayers: 99, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
	{
		name: "linux arm64 cuda 12.4 falls back to cpu", platform: PlatformLinuxARM64, probed: BackendCUDA124,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-ubuntu-arm64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 0, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
	{
		name: "linux arm64 cuda 12.8 falls back to cpu", platform: PlatformLinuxARM64, probed: BackendCUDA128,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-ubuntu-arm64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 0, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
	{
		name: "linux arm64 cuda 13.3 falls back to cpu", platform: PlatformLinuxARM64, probed: BackendCUDA133,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-ubuntu-arm64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 0, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
	{
		name: "linux arm64 rocm falls back to cpu", platform: PlatformLinuxARM64, probed: BackendROCm,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-ubuntu-arm64.tar.gz",
		wantPacking: PackingPQ2_0, wantLayers: 0, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},

	// ── windows-amd64: CPU, Vulkan, ROCm/HIP and CUDA 12.4/13.3. Every CUDA
	// build carries the paired cudart DLL archive as a second component.
	{
		name: "windows x64 cpu", platform: PlatformWindowsAMD64, probed: BackendCPU,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-win-cpu-x64.zip",
		wantPacking: PackingPQ2_0, wantLayers: 0, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
	{
		name: "windows x64 vulkan", platform: PlatformWindowsAMD64, probed: BackendVulkan,
		wantBackend: BackendVulkan, wantArchive: "llama-" + RuntimeTag + "-bin-win-vulkan-x64.zip",
		wantPacking: PackingPTQ1_0, wantLayers: 99, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
	{
		name: "windows x64 rocm", platform: PlatformWindowsAMD64, probed: BackendROCm,
		wantBackend: BackendROCm, wantArchive: "llama-" + RuntimeTag + "-bin-win-hip-radeon-x64.zip",
		wantPacking: PackingPQ2_0, wantLayers: 99, wantImageTokens: ImageMaxTokensUncapped,
		wantComponents: componentsPlain,
	},
	{
		name: "windows x64 cuda 12.4", platform: PlatformWindowsAMD64, probed: BackendCUDA124,
		wantBackend: BackendCUDA124, wantArchive: "llama-" + RuntimeTag + "-bin-win-cuda-12.4-x64.zip",
		wantPacking: PackingPQ2_0, wantLayers: 99, wantImageTokens: ImageMaxTokensUncapped,
		wantCudart: true, wantComponents: componentsCudart,
	},
	{
		name: "windows x64 cuda 13.3", platform: PlatformWindowsAMD64, probed: BackendCUDA133,
		wantBackend: BackendCUDA133, wantArchive: "llama-" + RuntimeTag + "-bin-win-cuda-13.3-x64.zip",
		wantPacking: PackingPQ2_0, wantLayers: 99, wantImageTokens: ImageMaxTokensUncapped,
		wantCudart: true, wantComponents: componentsCudart,
	},
	// This pin ships no Windows cuda-12.8 archive and no cuda-12.8 cudart, so a
	// driver reporting 12.8 (or 13.0-13.2, which maps to the 12.8 tag) must
	// clamp DOWN to 12.4 rather than fail: a 12.4 build runs on a newer driver.
	{
		name: "windows x64 cuda 12.8 clamps down to 12.4", platform: PlatformWindowsAMD64, probed: BackendCUDA128,
		wantBackend: BackendCUDA124, wantArchive: "llama-" + RuntimeTag + "-bin-win-cuda-12.4-x64.zip",
		wantPacking: PackingPQ2_0, wantLayers: 99, wantImageTokens: ImageMaxTokensUncapped,
		wantCudart: true, wantComponents: componentsCudart,
	},
	{
		name: "windows x64 probed metal degrades to cpu", platform: PlatformWindowsAMD64, probed: BackendMetal,
		wantBackend: BackendCPU, wantArchive: "llama-" + RuntimeTag + "-bin-win-cpu-x64.zip",
		wantPacking: PackingPQ2_0, wantLayers: 0, wantImageTokens: 1024,
		wantComponents: componentsPlain,
	},
}

// TestResolveFullMatrix crosses every (platform, probed backend) cell with
// every reachable RAM tier and checks the whole Resolution: which archive was
// selected, the packing, -ngl, -c and --image-max-tokens.
func TestResolveFullMatrix(t *testing.T) {
	t.Parallel()

	for _, mc := range resolveMatrix {
		for _, tier := range ramTiers {
			name := fmt.Sprintf("%s/ram%.0fGiB", mc.name, tier.ramGiB)
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				got, err := Resolve(mc.platform, mc.probed, tier.ramGiB)
				if err != nil {
					t.Fatalf("Resolve(%q, %q, %v) error = %v, want success",
						mc.platform, mc.probed, tier.ramGiB, err)
				}

				if got.Backend != mc.wantBackend {
					t.Errorf("Backend = %q, want %q", got.Backend, mc.wantBackend)
				}
				if got.Packing != mc.wantPacking {
					t.Errorf("Packing = %q, want %q", got.Packing, mc.wantPacking)
				}
				if got.Layers != mc.wantLayers {
					t.Errorf("Layers (-ngl) = %d, want %d", got.Layers, mc.wantLayers)
				}
				if got.ContextSize != tier.wantContext {
					t.Errorf("ContextSize (-c) = %d, want %d at %.1f GiB",
						got.ContextSize, tier.wantContext, tier.ramGiB)
				}
				if got.ImageMaxTokens != mc.wantImageTokens {
					t.Errorf("ImageMaxTokens = %d, want %d", got.ImageMaxTokens, mc.wantImageTokens)
				}
				if got.NeedsCudart != mc.wantCudart {
					t.Errorf("NeedsCudart = %v, want %v", got.NeedsCudart, mc.wantCudart)
				}

				assertComponents(t, got, mc.wantComponents)
				assertRuntimeArchive(t, got, mc.wantArchive)
				assertModelMatchesPacking(t, got)
			})
		}
	}
}

// TestResolveVulkanSelectsPTQ1_0 pins the one packing exception: Vulkan has no
// PQ2_0 (fork group-128) decoder, so it must take the dense PTQ1_0 weights.
func TestResolveVulkanSelectsPTQ1_0(t *testing.T) {
	t.Parallel()

	for _, platform := range []string{PlatformLinuxAMD64, PlatformLinuxARM64, PlatformWindowsAMD64} {
		t.Run(platform, func(t *testing.T) {
			t.Parallel()

			got, err := Resolve(platform, BackendVulkan, 32)
			if err != nil {
				t.Fatalf("Resolve error = %v, want success", err)
			}
			if got.Packing != PackingPTQ1_0 {
				t.Fatalf("Packing = %q, want %q", got.Packing, PackingPTQ1_0)
			}
			if got.Packing == PackingPQ2_0 {
				t.Fatal("Vulkan resolved PQ2_0, which has no kernels on this backend")
			}

			model := componentAsset(t, got, ComponentModel)
			if !strings.Contains(model.ArchiveName, string(PackingPTQ1_0)) {
				t.Errorf("model archive = %q, want the %s weights", model.ArchiveName, PackingPTQ1_0)
			}
		})
	}

	// Every other backend keeps the faster-prefill default.
	for _, backend := range []Backend{BackendMetal, BackendCUDA124, BackendCUDA128, BackendCUDA133, BackendROCm, BackendCPU} {
		if got := packingFor(backend); got != PackingPQ2_0 {
			t.Errorf("packingFor(%q) = %q, want %q", backend, got, PackingPQ2_0)
		}
	}
}

// TestResolveRAMGate covers ADR-066 D6: below 16 GiB the install is refused
// with a typed error before anything is planned, and 16 GiB exactly succeeds.
// There is no reduced-experience band in between.
func TestResolveRAMGate(t *testing.T) {
	t.Parallel()

	refuse := []float64{0, 0.5, 1, 4, 8, 12, 15, 15.5, 15.9, MinRAMGiB - 0.001}
	for _, ram := range refuse {
		t.Run(fmt.Sprintf("refuse %.3f GiB", ram), func(t *testing.T) {
			t.Parallel()

			got, err := Resolve(PlatformDarwinARM64, BackendMetal, ram)
			if !errors.Is(err, ErrInsufficientRAM) {
				t.Fatalf("Resolve(ram=%v) error = %v, want it to wrap ErrInsufficientRAM", ram, err)
			}
			if got.Assets != nil {
				t.Errorf("refused resolution still planned %d assets, want none", len(got.Assets))
			}
			// The refusal has to be actionable: it names the threshold.
			if !strings.Contains(err.Error(), "at least 16 GiB") {
				t.Errorf("error %q does not state the required RAM threshold", err)
			}
		})
	}

	// The message also reports what the machine actually has, so a refusal is
	// diagnosable without re-running the probe.
	t.Run("message reports the detected size", func(t *testing.T) {
		t.Parallel()

		_, err := Resolve(PlatformDarwinARM64, BackendMetal, 12)
		if err == nil {
			t.Fatal("Resolve(ram=12) error = nil, want ErrInsufficientRAM")
		}
		if !strings.Contains(err.Error(), "detected 12.0 GiB") {
			t.Errorf("error %q does not report the detected RAM size", err)
		}
	})

	accept := []float64{MinRAMGiB, 16, 16.0, 17, 24, 128}
	for _, ram := range accept {
		t.Run(fmt.Sprintf("accept %.0f GiB", ram), func(t *testing.T) {
			t.Parallel()

			if _, err := Resolve(PlatformDarwinARM64, BackendMetal, ram); err != nil {
				t.Fatalf("Resolve(ram=%v) error = %v, want success", ram, err)
			}
		})
	}
}

// TestResolveRAMGateIsCheckedBeforeArtifacts proves the refusal happens even
// when nothing could be provisioned anyway, i.e. the RAM gate is the FIRST
// check and not a consequence of a missing pin.
func TestResolveRAMGateIsCheckedBeforeArtifacts(t *testing.T) {
	t.Parallel()

	// A registry where nothing at all is pinned.
	empty := stubAssetTable{setErr: fmt.Errorf("%w: nothing pinned", ErrArtifactNotPinned)}

	_, err := resolveWith(empty, PlatformLinuxAMD64, BackendCPU, 8)
	if !errors.Is(err, ErrInsufficientRAM) {
		t.Fatalf("error = %v, want ErrInsufficientRAM to win over the missing pin", err)
	}
	if errors.Is(err, ErrArtifactNotPinned) {
		t.Error("the artifact error masked the RAM refusal")
	}
}

// TestResolveWindowsCUDAHasTwoRuntimeComponents covers ADR-066 D9: a Windows
// CUDA install is TWO runtime components — the server archive and the paired
// cudart DLL archive — each with its own checksum and progress bar.
func TestResolveWindowsCUDAHasTwoRuntimeComponents(t *testing.T) {
	t.Parallel()

	for _, backend := range []Backend{BackendCUDA124, BackendCUDA133} {
		t.Run(string(backend), func(t *testing.T) {
			t.Parallel()

			got, err := Resolve(PlatformWindowsAMD64, backend, 64)
			if err != nil {
				t.Fatalf("Resolve error = %v, want success", err)
			}
			if !got.NeedsCudart {
				t.Fatal("NeedsCudart = false, want true for Windows CUDA")
			}
			if len(got.Assets) != 4 {
				t.Fatalf("got %d assets (%v), want 4: runtime + cudart + model + mmproj",
					len(got.Assets), componentNames(got.Assets))
			}

			runtime := componentAsset(t, got, ComponentRuntime)
			cudart := componentAsset(t, got, ComponentCudart)

			if !strings.HasPrefix(cudart.ArchiveName, "cudart-") {
				t.Errorf("cudart archive = %q, want a cudart-*.zip", cudart.ArchiveName)
			}
			if !strings.HasSuffix(cudart.ArchiveName, ".zip") {
				t.Errorf("cudart archive = %q, want a .zip DLL archive", cudart.ArchiveName)
			}
			if runtime.ArchiveName == cudart.ArchiveName {
				t.Errorf("runtime and cudart resolved to the same archive %q", runtime.ArchiveName)
			}
			if runtime.SHA256 == cudart.SHA256 {
				t.Error("runtime and cudart share a checksum; they must be separate pins")
			}
			// The cudart archive is the second component, so its progress bar
			// follows the runtime's.
			if got.Assets[1].Component != ComponentCudart {
				t.Errorf("asset[1] = %q, want %q", got.Assets[1].Component, ComponentCudart)
			}
		})
	}

	// Linux CUDA links against the system CUDA installation: one component.
	t.Run("linux cuda has no cudart", func(t *testing.T) {
		t.Parallel()

		for _, backend := range []Backend{BackendCUDA124, BackendCUDA128, BackendCUDA133} {
			got, err := Resolve(PlatformLinuxAMD64, backend, 64)
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v, want success", backend, err)
			}
			if got.NeedsCudart {
				t.Errorf("Resolve(%q).NeedsCudart = true, want false on Linux", backend)
			}
			assertComponents(t, got, componentsPlain)
		}
	})

	// A non-CUDA Windows build never carries the DLL archive.
	t.Run("windows non-cuda has no cudart", func(t *testing.T) {
		t.Parallel()

		for _, backend := range []Backend{BackendCPU, BackendVulkan, BackendROCm} {
			got, err := Resolve(PlatformWindowsAMD64, backend, 64)
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v, want success", backend, err)
			}
			if got.NeedsCudart {
				t.Errorf("Resolve(%q).NeedsCudart = true, want false for a non-CUDA backend", backend)
			}
		}
	})
}

// TestResolveNonX64CUDAFallsBackToCPU covers the x64-only rule: CUDA and ROCm
// archives are published for x64 alone, so a non-x64 platform that probed one
// of them is provisioned with the CPU build instead of failing.
func TestResolveNonX64CUDAFallsBackToCPU(t *testing.T) {
	t.Parallel()

	for _, probed := range []Backend{BackendCUDA124, BackendCUDA128, BackendCUDA133, BackendROCm} {
		t.Run(string(probed), func(t *testing.T) {
			t.Parallel()

			got, err := Resolve(PlatformLinuxARM64, probed, 32)
			if err != nil {
				t.Fatalf("Resolve error = %v, want the CPU fallback to succeed", err)
			}
			if got.Backend != BackendCPU {
				t.Errorf("Backend = %q, want %q (CUDA/ROCm are x64-only)", got.Backend, BackendCPU)
			}
			if got.Layers != 0 {
				t.Errorf("Layers = %d, want 0 for the CPU build", got.Layers)
			}
			if got.ImageMaxTokens != 1024 {
				t.Errorf("ImageMaxTokens = %d, want 1024 for the CPU build", got.ImageMaxTokens)
			}
			if got.NeedsCudart {
				t.Error("NeedsCudart = true, want false once the backend degraded to CPU")
			}
			// The bytes must be the arm64 CPU archive, not an x64 one.
			assertRuntimeArchive(t, got, "llama-"+RuntimeTag+"-bin-ubuntu-arm64.tar.gz")
			assertComponents(t, got, componentsPlain)
		})
	}

	// The same rule on Apple Silicon, where the fallback archive is the Metal
	// build and still offloads every layer.
	t.Run("darwin arm64 cuda falls back to cpu", func(t *testing.T) {
		t.Parallel()

		got, err := Resolve(PlatformDarwinARM64, BackendCUDA124, 32)
		if err != nil {
			t.Fatalf("Resolve error = %v, want success", err)
		}
		if got.Backend != BackendCPU {
			t.Errorf("Backend = %q, want %q", got.Backend, BackendCPU)
		}
		assertRuntimeArchive(t, got, "llama-"+RuntimeTag+"-bin-macos-arm64.tar.gz")
	})
}

// TestResolveWindowsCUDA128ClampsToPinnedTag covers the documented gap in this
// pin: there is no Windows cuda-12.8 archive, so resolution must clamp DOWN to
// a tag that is pinned rather than refuse or grab a newer one.
func TestResolveWindowsCUDA128ClampsToPinnedTag(t *testing.T) {
	t.Parallel()

	if _, ok := RuntimeAsset(PlatformWindowsAMD64, BackendCUDA128); ok {
		t.Skip("the registry now pins a Windows cuda-12.8 archive; the clamp case no longer applies")
	}

	got, err := Resolve(PlatformWindowsAMD64, BackendCUDA128, 64)
	if err != nil {
		t.Fatalf("Resolve error = %v, want the 12.4 clamp to succeed", err)
	}
	if got.Backend != BackendCUDA124 {
		t.Errorf("Backend = %q, want %q", got.Backend, BackendCUDA124)
	}
	if !got.NeedsCudart {
		t.Error("NeedsCudart = false, want true for the clamped Windows CUDA build")
	}
	assertRuntimeArchive(t, got, "llama-"+RuntimeTag+"-bin-win-cuda-12.4-x64.zip")
}

// TestClampCUDABackendNeverUpgrades proves the clamp direction. A binary built
// for a newer CUDA toolkit will not load on an older driver, so clamping up
// would produce a runtime that fails at load time.
func TestClampCUDABackendNeverUpgrades(t *testing.T) {
	t.Parallel()

	table := pinnedRegistry{}

	for _, platform := range SupportedPlatforms() {
		for _, probed := range []Backend{BackendCUDA124, BackendCUDA128, BackendCUDA133} {
			got := clampCUDABackend(table, platform, probed)
			if got == probed {
				continue
			}
			if !got.IsCUDA() {
				continue // degraded to CPU: always safe
			}
			if slices.Index(cudaTagsNewestFirst, got) < slices.Index(cudaTagsNewestFirst, probed) {
				t.Errorf("%s: probed %q clamped UP to %q", platform, probed, got)
			}
			if _, ok := RuntimeAsset(platform, got); !ok {
				t.Errorf("%s: clamped to %q, which has no pinned archive", platform, got)
			}
		}
	}

	// An exact hit is never rewritten.
	if got := clampCUDABackend(table, PlatformWindowsAMD64, BackendCUDA124); got != BackendCUDA124 {
		t.Errorf("windows cuda 12.4 clamped to %q, want it kept as-is", got)
	}
	if got := clampCUDABackend(table, PlatformLinuxAMD64, BackendCUDA128); got != BackendCUDA128 {
		t.Errorf("linux cuda 12.8 clamped to %q, want it kept as-is", got)
	}

	// A tag outside the pinned list has no position in the ordering at all, so
	// it degrades to the universally available CPU build rather than guessing a
	// neighbouring tag that may need a different driver.
	if got := clampCUDABackend(table, PlatformLinuxAMD64, Backend("cuda-99.0")); got != BackendCPU {
		t.Errorf("an unknown CUDA tag clamped to %q, want %q", got, BackendCPU)
	}
}

// TestResolveIntelMacNeverOffloads locks the Intel Mac rule: no Metal compute
// path, so -ngl is 0 no matter what the probe reported.
func TestResolveIntelMacNeverOffloads(t *testing.T) {
	t.Parallel()

	for _, probed := range []Backend{BackendCPU, BackendMetal, BackendVulkan, BackendROCm, BackendCUDA124, BackendCUDA133} {
		got, err := Resolve(PlatformDarwinAMD64, probed, 32)
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v, want success", probed, err)
		}
		if got.Layers != 0 {
			t.Errorf("Resolve(%q).Layers = %d, want 0 on an Intel Mac", probed, got.Layers)
		}
	}
}

// TestContextSizeTiers covers the whole tier table through the pure helper,
// including the smallest tier, which the 16 GiB gate makes unreachable through
// Resolve but which the documented table still defines.
func TestContextSizeTiers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		ramGiB float64
		want   int
	}{
		{ramGiB: 0, want: 8192},
		{ramGiB: 1, want: 8192},
		{ramGiB: 8, want: 8192},
		{ramGiB: 11, want: 8192},
		{ramGiB: 11.9, want: 8192},
		{ramGiB: 12, want: 16384},
		{ramGiB: 16, want: 16384},
		{ramGiB: 23, want: 16384},
		{ramGiB: 23.9, want: 16384},
		{ramGiB: 24, want: 32768},
		{ramGiB: 35, want: 32768},
		{ramGiB: 35.9, want: 32768},
		{ramGiB: 36, want: 65536},
		{ramGiB: 71, want: 65536},
		{ramGiB: 71.9, want: 65536},
		{ramGiB: 72, want: contextTierTop},
		{ramGiB: 128, want: contextTierTop},
		{ramGiB: 1024, want: contextTierTop},
	}

	for _, tc := range cases {
		if got := contextSizeFor(tc.ramGiB); got != tc.want {
			t.Errorf("contextSizeFor(%v) = %d, want %d", tc.ramGiB, got, tc.want)
		}
	}
}

// TestContextSizeIsAlwaysExplicitAndBounded is the "-c is never unspecified
// and never the model's own training context" invariant. An unbounded request
// is memory-unaware and OOMs a constrained machine once -ngl offloads the KV
// cache, which is exactly why the tiers exist.
func TestContextSizeIsAlwaysExplicitAndBounded(t *testing.T) {
	t.Parallel()

	allowed := map[int]bool{8192: true, 16384: true, 32768: true, 65536: true, contextTierTop: true}

	// Sweep a dense range of RAM sizes, including every tier boundary and the
	// fractional values a real MemTotal produces.
	for tenth := 0; tenth <= 3000; tenth++ {
		ram := float64(tenth) / 10
		got := contextSizeFor(ram)
		if got <= 0 {
			t.Fatalf("contextSizeFor(%v) = %d, want a positive explicit context", ram, got)
		}
		if !allowed[got] {
			t.Fatalf("contextSizeFor(%v) = %d, which is not one of the documented tiers", ram, got)
		}
		if got > contextTierTop {
			t.Fatalf("contextSizeFor(%v) = %d exceeds the top tier %d", ram, got, contextTierTop)
		}
	}
}

// TestResolveAssetIntegrity checks the supply-chain invariants on every
// resolved set: the projector is always present, and no asset may carry an
// empty checksum or URL, because an empty checksum means REFUSE rather than
// "skip verification".
func TestResolveAssetIntegrity(t *testing.T) {
	t.Parallel()

	for _, mc := range resolveMatrix {
		got, err := Resolve(mc.platform, mc.probed, 32)
		if err != nil {
			t.Fatalf("%s: Resolve error = %v, want success", mc.name, err)
		}

		seen := make(map[Component]bool, len(got.Assets))
		for _, a := range got.Assets {
			if seen[a.Component] {
				t.Errorf("%s: duplicate component %q in the set", mc.name, a.Component)
			}
			seen[a.Component] = true

			if a.URL == "" {
				t.Errorf("%s: %s has an empty URL", mc.name, a.Component)
			}
			if a.SHA256 == "" {
				t.Errorf("%s: %s has an empty SHA256 (must fail closed, never skip verification)",
					mc.name, a.Component)
			}
			if a.SizeBytes <= 0 {
				t.Errorf("%s: %s has a non-positive SizeBytes %d", mc.name, a.Component, a.SizeBytes)
			}
			if a.ArchiveName == "" {
				t.Errorf("%s: %s has an empty ArchiveName", mc.name, a.Component)
			}
		}

		if !seen[ComponentMMProj] {
			t.Errorf("%s: the vision projector is missing from the set", mc.name)
		}
		if !seen[ComponentModel] {
			t.Errorf("%s: the model weights are missing from the set", mc.name)
		}
		if !seen[ComponentRuntime] {
			t.Errorf("%s: the runtime is missing from the set", mc.name)
		}
		if seen[ComponentCudart] != mc.wantCudart {
			t.Errorf("%s: cudart present = %v, want %v", mc.name, seen[ComponentCudart], mc.wantCudart)
		}
	}
}

// TestResolveFailClosedWithoutPinnedArtifacts drives the registry seam: when
// no artifact can be resolved, Resolve must return an error wrapping
// ErrArtifactNotPinned rather than a partial or unpinned set. Each case builds
// its own stub table, so nothing mutates package state and every subtest can
// run in parallel.
func TestResolveFailClosedWithoutPinnedArtifacts(t *testing.T) {
	t.Parallel()

	t.Run("no runtime pinned at all", func(t *testing.T) {
		t.Parallel()

		table := stubAssetTable{
			setErr: fmt.Errorf("%w: platform %q", ErrArtifactNotPinned, PlatformLinuxAMD64),
		}

		got, err := resolveWith(table, PlatformLinuxAMD64, BackendCUDA133, 64)
		if !errors.Is(err, ErrArtifactNotPinned) {
			t.Fatalf("error = %v, want it to wrap ErrArtifactNotPinned", err)
		}
		if got.Assets != nil {
			t.Errorf("failed resolution still returned %d assets", len(got.Assets))
		}
	})

	t.Run("unknown platform", func(t *testing.T) {
		t.Parallel()

		table := stubAssetTable{
			setErr: fmt.Errorf("%w: platform %q", ErrArtifactNotPinned, "plan9-mips"),
		}

		got, err := resolveWith(table, "plan9-mips", BackendCPU, 64)
		if !errors.Is(err, ErrArtifactNotPinned) {
			t.Fatalf("error = %v, want it to wrap ErrArtifactNotPinned", err)
		}
		if got.Assets != nil {
			t.Errorf("failed resolution still returned %d assets", len(got.Assets))
		}
	})

	t.Run("degrades to cpu when nothing else is pinned", func(t *testing.T) {
		t.Parallel()

		// Only the CPU runtime is visible, so every accelerator probe has to
		// land on it instead of erroring out.
		table := stubAssetTable{
			runtimes: map[string]map[Backend]bool{
				PlatformLinuxAMD64: {BackendCPU: true},
			},
		}

		for _, probed := range []Backend{BackendCUDA133, BackendROCm, BackendVulkan, BackendMetal} {
			got, err := resolveWith(table, PlatformLinuxAMD64, probed, 64)
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v, want the CPU fallback", probed, err)
			}
			if got.Backend != BackendCPU {
				t.Errorf("Resolve(%q).Backend = %q, want %q", probed, got.Backend, BackendCPU)
			}
			if got.NeedsCudart {
				t.Errorf("Resolve(%q).NeedsCudart = true, want false on the CPU fallback", probed)
			}
			// The packing follows the degraded backend, not the probed one.
			if got.Packing != PackingPQ2_0 {
				t.Errorf("Resolve(%q).Packing = %q, want %q on the CPU fallback",
					probed, got.Packing, PackingPQ2_0)
			}
		}
	})

	t.Run("cudart presence drives NeedsCudart", func(t *testing.T) {
		t.Parallel()

		runtimes := map[string]map[Backend]bool{
			PlatformWindowsAMD64: {BackendCUDA124: true},
		}

		withCudart, err := resolveWith(stubAssetTable{runtimes: runtimes, cudart: true},
			PlatformWindowsAMD64, BackendCUDA124, 64)
		if err != nil {
			t.Fatalf("Resolve error = %v, want success", err)
		}
		if !withCudart.NeedsCudart {
			t.Error("NeedsCudart = false, want true when the set carries a cudart component")
		}
		assertComponents(t, withCudart, componentsCudart)

		without, err := resolveWith(stubAssetTable{runtimes: runtimes},
			PlatformWindowsAMD64, BackendCUDA124, 64)
		if err != nil {
			t.Fatalf("Resolve error = %v, want success", err)
		}
		if without.NeedsCudart {
			t.Error("NeedsCudart = true, want false when the set carries no cudart component")
		}
		assertComponents(t, without, componentsPlain)
	})
}

// TestResolveIsPure checks the property the whole matrix test relies on:
// Resolve performs no I/O and no allocation-dependent bookkeeping, so repeated
// calls with the same inputs are identical.
func TestResolveIsPure(t *testing.T) {
	t.Parallel()

	first, err := Resolve(PlatformWindowsAMD64, BackendCUDA128, 23.4)
	if err != nil {
		t.Fatalf("Resolve error = %v, want success", err)
	}
	for i := range 5 {
		again, err := Resolve(PlatformWindowsAMD64, BackendCUDA128, 23.4)
		if err != nil {
			t.Fatalf("call %d: Resolve error = %v, want success", i, err)
		}
		if again.Backend != first.Backend || again.Packing != first.Packing ||
			again.Layers != first.Layers || again.ContextSize != first.ContextSize ||
			again.ImageMaxTokens != first.ImageMaxTokens || again.NeedsCudart != first.NeedsCudart {
			t.Fatalf("call %d returned a different resolution: %+v vs %+v", i, again, first)
		}
		if !slices.EqualFunc(again.Assets, first.Assets, func(a, b Asset) bool {
			return a.Component == b.Component && a.URL == b.URL &&
				a.SHA256 == b.SHA256 && a.SizeBytes == b.SizeBytes &&
				a.ArchiveName == b.ArchiveName
		}) {
			t.Fatalf("call %d returned different assets", i)
		}
	}
}

// TestPackageSourcesNeverEmitBannedContext enforces the "-c is never
// unspecified and never the model's own training context" rule at the source
// level, so a future edit cannot reintroduce either form. Both patterns are
// constructed rather than written out, which keeps this guard from planting
// the very literals it forbids.
func TestPackageSourcesNeverEmitBannedContext(t *testing.T) {
	t.Parallel()

	// The model's full training context: memory-unaware, and it OOMs a
	// constrained machine once -ngl offloads the KV cache.
	bannedContext := strconv.Itoa(1 << 18)
	// The "just use whatever the model was trained with" flag form.
	bannedFlag := "-" + "c 0"

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		scanned++

		content := string(source)
		if strings.Contains(content, bannedContext) {
			t.Errorf("%s contains %s, the model's full training context, which must never be used as -c",
				name, bannedContext)
		}
		if strings.Contains(content, bannedFlag) {
			t.Errorf("%s contains the %q flag form, which means \"use the model's own context\" and is banned",
				name, bannedFlag)
		}
	}

	if scanned == 0 {
		t.Fatal("no package sources were scanned; the guard is vacuous")
	}
}

// ── helpers ──

// stubAssetTable is a controllable AssetTable for the fail-closed paths. An
// empty runtimes map means "nothing is pinned for anything".
type stubAssetTable struct {
	runtimes map[string]map[Backend]bool
	cudart   bool
	setErr   error
}

// RuntimeAsset implements AssetTable.
func (s stubAssetTable) RuntimeAsset(platform string, backend Backend) (Asset, bool) {
	return Asset{Component: ComponentRuntime, ArchiveName: "stub-runtime"}, s.runtimes[platform][backend]
}

// ArtifactSet implements AssetTable.
func (s stubAssetTable) ArtifactSet(_ string, _ Backend, _ Packing) ([]Asset, error) {
	if s.setErr != nil {
		return nil, s.setErr
	}
	set := make([]Asset, 0, 4)
	set = append(set, Asset{Component: ComponentRuntime, ArchiveName: "stub-runtime"})
	if s.cudart {
		set = append(set, Asset{Component: ComponentCudart, ArchiveName: "stub-cudart"})
	}
	set = append(set,
		Asset{Component: ComponentModel, ArchiveName: "stub-model"},
		Asset{Component: ComponentMMProj, ArchiveName: "stub-mmproj"},
	)
	return set, nil
}

// assertComponents checks the install set's components and their order, which
// is also the download and progress-reporting order.
func assertComponents(t *testing.T, got Resolution, want []Component) {
	t.Helper()

	if len(got.Assets) != len(want) {
		t.Fatalf("got %d assets (%v), want %d (%v)",
			len(got.Assets), componentNames(got.Assets), len(want), want)
	}
	for i, w := range want {
		if got.Assets[i].Component != w {
			t.Errorf("asset[%d] component = %q, want %q (full set: %v)",
				i, got.Assets[i].Component, w, componentNames(got.Assets))
		}
	}
}

// assertRuntimeArchive checks which build was actually selected.
func assertRuntimeArchive(t *testing.T, got Resolution, want string) {
	t.Helper()

	runtime := componentAsset(t, got, ComponentRuntime)
	if runtime.ArchiveName != want {
		t.Errorf("runtime archive = %q, want %q", runtime.ArchiveName, want)
	}
}

// assertModelMatchesPacking checks that the weights file on the plan is the one
// for the resolved packing — the pairing that a mixed install would break.
func assertModelMatchesPacking(t *testing.T, got Resolution) {
	t.Helper()

	model := componentAsset(t, got, ComponentModel)
	if !strings.Contains(model.ArchiveName, string(got.Packing)) {
		t.Errorf("model archive %q does not match the resolved packing %q",
			model.ArchiveName, got.Packing)
	}
}

// componentAsset returns the single asset of a component, failing the test when
// it is absent or duplicated.
func componentAsset(t *testing.T, got Resolution, want Component) Asset {
	t.Helper()

	var found Asset
	count := 0
	for _, a := range got.Assets {
		if a.Component == want {
			found = a
			count++
		}
	}
	switch count {
	case 0:
		t.Fatalf("no %q component in the set %v", want, componentNames(got.Assets))
	case 1:
		return found
	default:
		t.Fatalf("%d %q components in the set %v, want exactly 1", count, want, componentNames(got.Assets))
	}
	return found
}

// componentNames renders an asset set for failure messages.
func componentNames(assets []Asset) []Component {
	names := make([]Component, 0, len(assets))
	for _, a := range assets {
		names = append(names, a.Component)
	}
	return names
}
