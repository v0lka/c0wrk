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

// ramTiers are the RAM sizes Resolve can actually be called with on a machine
// the memory gate admits. Every entry clears it; the refusals and the
// unreachable smallest context tier are covered separately by
// TestResolveMemoryGate and TestContextSizeTiers.
var ramTiers = []struct {
	ramGiB      float64
	wantContext int
}{
	// The smallest tier the RAM ladder itself reaches.
	{ramGiB: 16, wantContext: 16384},
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
	// KNOWN_ISSUES #222: "CUDA 13.3 builds crash on some systems. On Linux this
	// is a segfault; on Windows the server prints its banner and exits without a
	// message." Upstream's workaround is the 12.8 build on Linux and the 12.4
	// build on Windows, and a newer driver still runs an older-toolkit build —
	// the same backwards compatibility clampCUDABackend relies on. So a probed
	// 13.3 resolves to the fallback and the compatibility guard records why.
	{
		name: "linux x64 cuda 13.3 is guarded down to 12.8", platform: PlatformLinuxAMD64, probed: BackendCUDA133,
		wantBackend: BackendCUDA128, wantArchive: "llama-" + RuntimeTag + "-bin-linux-cuda-12.8-x64.tar.gz",
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
	// #222 on Windows: the documented fallback is the 12.4 build, which this pin
	// ships for Windows (it ships no Windows 12.8 archive at all).
	{
		name: "windows x64 cuda 13.3 is guarded down to 12.4", platform: PlatformWindowsAMD64, probed: BackendCUDA133,
		wantBackend: BackendCUDA124, wantArchive: "llama-" + RuntimeTag + "-bin-win-cuda-12.4-x64.zip",
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

				got, err := ResolveMachine(mc.platform, mc.probed, tier.ramGiB)
				if err != nil {
					t.Fatalf("ResolveMachine(%q, %q, %v) error = %v, want success",
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

			got, err := ResolveMachine(platform, BackendVulkan, 32)
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
		if got := packingForBackend(backend); got != PackingPQ2_0 {
			t.Errorf("packingForBackend(%q) = %q, want %q", backend, got, PackingPQ2_0)
		}
	}
}

// TestResolveMemoryGate covers the gate that replaced ADR-066 D6's flat 16 GiB
// floor. D6 measured ONE pool and was wrong in both directions at once, so the
// cases here are the two directions plus the fail-closed edges:
//
//   - a small-RAM machine with a big accelerator is ADMITTED (the floor refused
//     it on a number that described neither pool);
//   - a big-RAM machine with a small accelerator gets a plan that FITS (the
//     floor admitted it and then OOMed at load);
//   - a small-RAM machine with no accelerator is still REFUSED, now with the
//     arithmetic in the message rather than a threshold.
//
// The refusals are asserted on the typed error, not on a RAM size, because
// there is no RAM size to assert on any more: viability is a property of both
// pools and of the model's measured footprint.
func TestResolveMemoryGate(t *testing.T) {
	t.Parallel()

	// ── admitted: the accelerator carries the model ──
	t.Run("8 GiB RAM beside a 32 GiB accelerator is admitted", func(t *testing.T) {
		t.Parallel()

		topology := probedTopology(t, PlatformLinuxAMD64, 8,
			DeviceMemory{Name: "CUDA0", Description: "NVIDIA GeForce RTX 4090",
				TotalMiB: 32 * 1024, FreeMiB: 32 * 1024})

		res, err := Resolve(ResolveInput{
			MachineProfile: MachineProfile{
				Platform: PlatformLinuxAMD64, Backend: BackendCUDA128, RAMGiB: 8,
			},
			Topology: &topology,
		})
		if err != nil {
			t.Fatalf("Resolve error = %v, want a viable resolution", err)
		}
		if !res.Memory.OffloadsToDevice() {
			t.Errorf("the plan is not device-resident (fit=%v, layers=%v, device budget %d)",
				res.Memory.Fit, deref(res.Memory.Layers), res.Memory.DeviceBudgetMiB)
		}
		// The point of the whole exercise: the host side is the measured spill
		// plus one CPU compute buffer, not the weights.
		if res.Memory.ExpectedHostMiB >= res.Memory.ExpectedDeviceMiB {
			t.Errorf("host footprint %d MiB is not below the device footprint %d MiB; "+
				"the model is not really on the card",
				res.Memory.ExpectedHostMiB, res.Memory.ExpectedDeviceMiB)
		}
	})

	// ── admitted with a plan that fits: the accelerator cannot carry it ──
	t.Run("32 GiB RAM beside an 8 GiB accelerator gets a plan that fits", func(t *testing.T) {
		t.Parallel()

		topology := probedTopology(t, PlatformLinuxAMD64, 32,
			DeviceMemory{Name: "CUDA0", Description: "NVIDIA GeForce RTX 4060",
				TotalMiB: 8 * 1024, FreeMiB: 8 * 1024})

		res, err := Resolve(ResolveInput{
			MachineProfile: MachineProfile{
				Platform: PlatformLinuxAMD64, Backend: BackendCUDA128, RAMGiB: 32,
			},
			Topology: &topology,
		})
		if err != nil {
			t.Fatalf("Resolve error = %v, want a degraded plan rather than a refusal", err)
		}
		plan := res.Memory
		// The invariant that matters: the plan the installer persists fits the
		// budgets it was gated on, in BOTH pools. An at-load OOM is a plan whose
		// expected footprint nobody compared to the machine.
		if plan.ExpectedDeviceMiB > plan.DeviceBudgetMiB {
			t.Errorf("expected device footprint %d MiB exceeds the %d MiB budget it was gated on",
				plan.ExpectedDeviceMiB, plan.DeviceBudgetMiB)
		}
		if plan.ExpectedHostMiB > plan.HostBudgetMiB {
			t.Errorf("expected host footprint %d MiB exceeds the %d MiB budget it was gated on",
				plan.ExpectedHostMiB, plan.HostBudgetMiB)
		}
		if plan.OffloadsToDevice() {
			t.Error("the plan still offloads to an accelerator that cannot hold the weights")
		}
		// A degradation this large must be legible in the record.
		if !noteContaining(plan.Notes, "host residency") {
			t.Errorf("Notes = %v, want one recording the degradation to host residency", plan.Notes)
		}
	})

	// ── refused: no accelerator, and the host pool cannot hold the model ──
	refuseRAM := []float64{0.5, 1, 4, 8, 12}
	for _, ram := range refuseRAM {
		t.Run(fmt.Sprintf("%.1f GiB of RAM with no accelerator is refused", ram), func(t *testing.T) {
			t.Parallel()

			topology := probedTopology(t, PlatformLinuxAMD64, ram)
			got, err := Resolve(ResolveInput{
				MachineProfile: MachineProfile{
					Platform: PlatformLinuxAMD64, Backend: BackendCPU, RAMGiB: ram,
				},
				Topology: &topology,
			})
			if !errors.Is(err, ErrInsufficientMemory) {
				t.Fatalf("Resolve error = %v, want it to wrap ErrInsufficientMemory", err)
			}
			if got.Assets != nil {
				t.Errorf("a refused resolution still planned %d assets, want none", len(got.Assets))
			}
			// A host-pool refusal still matches the deprecated sentinel, so a
			// caller that only cares about system RAM keeps working.
			if !errors.Is(err, ErrInsufficientRAM) {
				t.Errorf("error %v does not unwrap to the deprecated ErrInsufficientRAM", err)
			}
		})
	}

	// ── the refusal names BOTH pools with BOTH numbers ──
	t.Run("the refusal message names both pools", func(t *testing.T) {
		t.Parallel()

		topology := probedTopology(t, PlatformLinuxAMD64, 8)
		_, err := Resolve(ResolveInput{
			MachineProfile: MachineProfile{
				Platform: PlatformLinuxAMD64, Backend: BackendCPU, RAMGiB: 8,
			},
			Topology: &topology,
		})
		if err == nil {
			t.Fatal("Resolve error = nil, want a refusal")
		}
		var typed *InsufficientMemoryError
		if !errors.As(err, &typed) {
			t.Fatalf("error %v is not an *InsufficientMemoryError", err)
		}
		message := err.Error()
		for _, want := range []string{
			"device memory", "host RAM", "GiB available", "reserve", "installed",
		} {
			if !strings.Contains(message, want) {
				t.Errorf("refusal %q does not name %q", message, want)
			}
		}
		// Both NUMBERS, not just both pool names: a message that said "not
		// enough memory" would name the pools too.
		if typed.HostNeedMiB <= 0 || typed.HostHaveMiB < 0 {
			t.Errorf("host figures are %d needed / %d available, want both populated",
				typed.HostNeedMiB, typed.HostHaveMiB)
		}
		if typed.HostReserveMiB <= 0 {
			t.Errorf("reserve = %d MiB, want the derivation the host budget came from",
				typed.HostReserveMiB)
		}
		// A refusal is diagnosable without re-running the probe: the installed
		// total is in the message.
		if !strings.Contains(message, "8.0 GiB installed") {
			t.Errorf("refusal %q does not report the installed RAM", message)
		}
	})

	// ── fail-closed on an unreadable host total ──
	t.Run("an unreadable RAM total still refuses", func(t *testing.T) {
		t.Parallel()

		_, err := ResolveMachine(PlatformDarwinARM64, BackendMetal, 0)
		if !errors.Is(err, ErrRAMUnknown) {
			t.Fatalf("ResolveMachine(ram=0) error = %v, want it to wrap ErrRAMUnknown", err)
		}
		if errors.Is(err, ErrInsufficientMemory) {
			t.Error("an UNKNOWN RAM total reported itself as insufficient memory; " +
				"unknown is not the same fact as too small")
		}
	})

	// ── an unreadable DEVICE budget degrades instead of refusing ──
	t.Run("an unmeasured accelerator degrades rather than refuses", func(t *testing.T) {
		t.Parallel()

		// No topology at all — the first-install shape, where there is no
		// provisioned runtime to ask. A discrete CUDA card on amd64 has memory
		// independent of host RAM, so 8 GiB of RAM is NOT evidence against it.
		res, err := Resolve(ResolveInput{MachineProfile: MachineProfile{
			Platform: PlatformLinuxAMD64, Backend: BackendCUDA128, RAMGiB: 8,
		}})
		if err != nil {
			t.Fatalf("Resolve error = %v, want a degraded resolution rather than a refusal", err)
		}
		if !noteContaining(res.Memory.Notes, "could not be measured") {
			t.Errorf("Notes = %v, want one recording that the device budget was not measured",
				res.Memory.Notes)
		}
	})

	t.Run("an unmeasured UNIFIED accelerator does not get the benefit of the doubt", func(t *testing.T) {
		t.Parallel()

		// Same RAM, but Apple Silicon: the accelerator's pool IS the host pool,
		// so the RAM probe measured both and 8 GiB really is the whole budget.
		_, err := ResolveMachine(PlatformDarwinARM64, BackendMetal, 8)
		if !errors.Is(err, ErrInsufficientMemory) {
			t.Fatalf("ResolveMachine(darwin-arm64, metal, 8 GiB) error = %v, "+
				"want it to wrap ErrInsufficientMemory", err)
		}
	})

	// ── the admitted band now ends where the measurements say it does ──
	accept := []float64{16, 17, 24, 32, 64, 128}
	for _, ram := range accept {
		t.Run(fmt.Sprintf("accept %.0f GiB of unified RAM", ram), func(t *testing.T) {
			t.Parallel()

			if _, err := ResolveMachine(PlatformDarwinARM64, BackendMetal, ram); err != nil {
				t.Fatalf("ResolveMachine(ram=%v) error = %v, want success", ram, err)
			}
		})
	}
}

// TestResolveMemoryGateIsCheckedBeforeArtifacts proves the refusal happens even
// when nothing could be provisioned anyway, i.e. the memory gate is the FIRST
// check and not a consequence of a missing pin.
func TestResolveMemoryGateIsCheckedBeforeArtifacts(t *testing.T) {
	t.Parallel()

	// A registry where nothing at all is pinned.
	empty := stubAssetTable{setErr: fmt.Errorf("%w: nothing pinned", ErrArtifactNotPinned)}

	_, err := resolveMachineWithTable(empty, PlatformLinuxAMD64, BackendCPU, 8)
	if !errors.Is(err, ErrInsufficientMemory) {
		t.Fatalf("error = %v, want ErrInsufficientMemory to win over the missing pin", err)
	}
	if errors.Is(err, ErrArtifactNotPinned) {
		t.Error("the artifact error masked the memory refusal")
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

			got, err := ResolveMachine(PlatformWindowsAMD64, backend, 64)
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
			got, err := ResolveMachine(PlatformLinuxAMD64, backend, 64)
			if err != nil {
				t.Fatalf("ResolveMachine(%q) error = %v, want success", backend, err)
			}
			if got.NeedsCudart {
				t.Errorf("ResolveMachine(%q).NeedsCudart = true, want false on Linux", backend)
			}
			assertComponents(t, got, componentsPlain)
		}
	})

	// A non-CUDA Windows build never carries the DLL archive.
	t.Run("windows non-cuda has no cudart", func(t *testing.T) {
		t.Parallel()

		for _, backend := range []Backend{BackendCPU, BackendVulkan, BackendROCm} {
			got, err := ResolveMachine(PlatformWindowsAMD64, backend, 64)
			if err != nil {
				t.Fatalf("ResolveMachine(%q) error = %v, want success", backend, err)
			}
			if got.NeedsCudart {
				t.Errorf("ResolveMachine(%q).NeedsCudart = true, want false for a non-CUDA backend", backend)
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

			got, err := ResolveMachine(PlatformLinuxARM64, probed, 32)
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

		got, err := ResolveMachine(PlatformDarwinARM64, BackendCUDA124, 32)
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

	got, err := ResolveMachine(PlatformWindowsAMD64, BackendCUDA128, 64)
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
		got, err := ResolveMachine(PlatformDarwinAMD64, probed, 32)
		if err != nil {
			t.Fatalf("ResolveMachine(%q) error = %v, want success", probed, err)
		}
		if got.Layers != 0 {
			t.Errorf("ResolveMachine(%q).Layers = %d, want 0 on an Intel Mac", probed, got.Layers)
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
		got, err := ResolveMachine(mc.platform, mc.probed, 32)
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

		got, err := resolveMachineWithTable(table, PlatformLinuxAMD64, BackendCUDA133, 64)
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

		got, err := resolveMachineWithTable(table, "plan9-mips", BackendCPU, 64)
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
			got, err := resolveMachineWithTable(table, PlatformLinuxAMD64, probed, 64)
			if err != nil {
				t.Fatalf("ResolveMachine(%q) error = %v, want the CPU fallback", probed, err)
			}
			if got.Backend != BackendCPU {
				t.Errorf("ResolveMachine(%q).Backend = %q, want %q", probed, got.Backend, BackendCPU)
			}
			if got.NeedsCudart {
				t.Errorf("ResolveMachine(%q).NeedsCudart = true, want false on the CPU fallback", probed)
			}
			// The packing follows the degraded backend, not the probed one.
			if got.Packing != PackingPQ2_0 {
				t.Errorf("ResolveMachine(%q).Packing = %q, want %q on the CPU fallback",
					probed, got.Packing, PackingPQ2_0)
			}
		}
	})

	t.Run("cudart presence drives NeedsCudart", func(t *testing.T) {
		t.Parallel()

		runtimes := map[string]map[Backend]bool{
			PlatformWindowsAMD64: {BackendCUDA124: true},
		}

		withCudart, err := resolveMachineWithTable(stubAssetTable{runtimes: runtimes, cudart: true},
			PlatformWindowsAMD64, BackendCUDA124, 64)
		if err != nil {
			t.Fatalf("Resolve error = %v, want success", err)
		}
		if !withCudart.NeedsCudart {
			t.Error("NeedsCudart = false, want true when the set carries a cudart component")
		}
		assertComponents(t, withCudart, componentsCudart)

		without, err := resolveMachineWithTable(stubAssetTable{runtimes: runtimes},
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

	first, err := ResolveMachine(PlatformWindowsAMD64, BackendCUDA128, 23.4)
	if err != nil {
		t.Fatalf("Resolve error = %v, want success", err)
	}
	for i := range 5 {
		again, err := ResolveMachine(PlatformWindowsAMD64, BackendCUDA128, 23.4)
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

// TestMemoryGateUnifiedFloorIsDerived pins the number the retired 16 GiB
// constant was replaced with — and pins that it is DERIVED rather than chosen.
//
// On a unified machine the accelerator's pool IS system RAM, so the RAM probe
// prices both and the floor is wherever the smallest modelled shape stops
// clearing `RAM − max(4 GiB, RAM/8)`. That is the 8230 MiB host-resident
// PTQ1_0 shape at the 65536-token context floor, which puts the boundary just
// above 12 GiB: a machine 38 MiB short of it is refused, and one 38 MiB over is
// admitted. A test that asserts a round 16 here would be asserting the constant
// this gate exists to remove; if a pin bump moves the weights, this test moves
// with the measurements and the spec's Configuration table must be re-read.
func TestMemoryGateUnifiedFloorIsDerived(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)
	ctx := DefaultFitMinContext

	// The shape the boundary is made of, priced from memory.go's own API rather
	// than from the gate under test, so the expectation is not circular.
	smallest, err := profile.ProjectHostMiB(PackingPTQ1_0, ctx, KVTypeQ4_0, false)
	if err != nil {
		t.Fatalf("ProjectHostMiB: %v", err)
	}
	smallest += profile.MMProjReserveDeviceMiB + profile.MMProjReserveHostMiB

	const refused, admitted = 12.0, 12.1
	for _, tc := range []struct {
		ramGiB float64
		wantOK bool
	}{
		{refused, false},
		{admitted, true},
	} {
		t.Run(fmt.Sprintf("%.1f GiB", tc.ramGiB), func(t *testing.T) {
			t.Parallel()

			_, err := ResolveMachine(PlatformDarwinARM64, BackendMetal, tc.ramGiB)
			if tc.wantOK && err != nil {
				t.Fatalf("ResolveMachine(%.1f GiB) error = %v, want it admitted", tc.ramGiB, err)
			}
			if !tc.wantOK && !errors.Is(err, ErrInsufficientMemory) {
				t.Fatalf("ResolveMachine(%.1f GiB) error = %v, want ErrInsufficientMemory",
					tc.ramGiB, err)
			}

			// The boundary really is the derivation: the budget at the refused
			// size is below the smallest shape and the one just above it is not.
			ramBytes := int64(tc.ramGiB * gibibyte)
			budget := ramBytes - hostReserveBytes(ramBytes)
			if got := budget >= smallest*bytesPerMiB; got != tc.wantOK {
				t.Errorf("%.1f GiB leaves a %d-byte host budget against a %d MiB smallest shape "+
					"(fits = %v), but Resolve said %v — the gate is not pricing the shape the "+
					"boundary is derived from", tc.ramGiB, budget, smallest, got, tc.wantOK)
			}
		})
	}

	// And the floor is not a round number anyone picked: it is strictly between
	// 12 and 13 GiB, which is what "derived" has to mean here.
	if _, err := ResolveMachine(PlatformDarwinARM64, BackendMetal, 13); err != nil {
		t.Errorf("13 GiB of unified RAM error = %v, want the derived floor to sit below it", err)
	}
}

// TestDefaultHostReserveGiBMatchesTheTopologyDerivation pins the two callers of
// the reserve to one another: the gate that has no topology and the probe that
// has one must hold back the same RAM, or a machine is admitted by the
// synchronous RPC and refused by the install (or the reverse).
func TestDefaultHostReserveGiBMatchesTheTopologyDerivation(t *testing.T) {
	t.Parallel()

	for _, ramGiB := range []float64{8, 12, 16, 32, 64, 128} {
		derived := DefaultHostReserveGiB(ramGiB)
		probed := probedTopology(t, PlatformLinuxAMD64, ramGiB)
		want := float64(hostReserveBytes(int64(ramGiB*gibibyte))) / gibibyte

		if derived != want {
			t.Errorf("DefaultHostReserveGiB(%v) = %v, want %v", ramGiB, derived, want)
		}
		// The topology's host budget is the same RAM minus the same reserve.
		if got := probed.HostRAMGiB - float64(probed.HostBudgetBytes)/gibibyte; got != want {
			t.Errorf("a probed %v GiB machine held back %v GiB, want %v", ramGiB, got, want)
		}
		if derived <= 0 {
			t.Errorf("DefaultHostReserveGiB(%v) = %v, want a positive reserve", ramGiB, derived)
		}
	}

	// The reserve has a floor, so a small machine does not end up handing
	// nearly all of its RAM to the model.
	if got := DefaultHostReserveGiB(8); got != 4 {
		t.Errorf("DefaultHostReserveGiB(8) = %v, want the 4 GiB floor", got)
	}
	// And it scales above the floor.
	if got := DefaultHostReserveGiB(128); got != 16 {
		t.Errorf("DefaultHostReserveGiB(128) = %v, want 1/8 of RAM", got)
	}
	if got := DefaultHostReserveGiB(0); got != 0 {
		t.Errorf("DefaultHostReserveGiB(0) = %v, want 0 for an unreadable total", got)
	}
}

// TestResolveNeverEmitsAPlanThatOverflowsItsOwnBudget is the property the whole
// gate exists to establish, asserted over the reachable machine matrix rather
// than over one anecdote per bug: **whatever `Resolve` returns must fit the
// budgets it reports**. A refusal is fine — a plan that does not fit is the
// at-load out-of-memory this subsystem is not allowed to produce.
//
// It is the regression test for a hole the gate itself opened. Admitting a
// machine because SOME modelled shape fits is only half the job: the RAM-only
// planner used to emit its platform defaults anyway (`-ngl 99` on Apple Silicon,
// an f16 cache), so a 13 GiB unified machine was admitted on a host-resident
// PTQ1_0 shape and then handed a device-resident PQ2_0 plan 1.3 GiB too large.
// The plan now yields to the gate's verdict, and this test is what stops the two
// from drifting apart again.
//
// The footprints are re-projected from memory.go's own API rather than read off
// the plan, so the assertion is not circular with the planner under test.
func TestResolveNeverEmitsAPlanThatOverflowsItsOwnBudget(t *testing.T) {
	t.Parallel()

	profile := profileOrFail(t)

	machines := []struct {
		platform  string
		backend   Backend
		ramGiB    float64
		deviceMiB int64 // 0 = no accelerator; negative = do not probe at all
	}{
		// No topology at all — the first-install shape, where the budgets are
		// derived from the RAM probe and the accelerator axis is static.
		{PlatformDarwinARM64, BackendMetal, 13, -1},
		{PlatformDarwinARM64, BackendMetal, 14, -1},
		{PlatformDarwinARM64, BackendMetal, 16, -1},
		{PlatformDarwinARM64, BackendMetal, 24, -1},
		{PlatformDarwinARM64, BackendMetal, 64, -1},
		{PlatformDarwinARM64, BackendMetal, 128, -1},
		{PlatformDarwinARM64, BackendCPU, 32, -1},
		{PlatformDarwinAMD64, BackendCPU, 32, -1},
		{PlatformLinuxAMD64, BackendCPU, 32, -1},
		{PlatformLinuxAMD64, BackendCUDA128, 8, -1},
		{PlatformLinuxAMD64, BackendVulkan, 16, -1},
		{PlatformLinuxAMD64, BackendVulkan, 32, -1},
		// Probed topologies: unified and discrete.
		{PlatformDarwinARM64, BackendMetal, 16, 16 * 1024},
		{PlatformDarwinARM64, BackendMetal, 128, 110100},
		{PlatformLinuxAMD64, BackendCUDA128, 8, 32 * 1024},
		{PlatformLinuxAMD64, BackendCUDA128, 32, 8 * 1024},
		{PlatformLinuxAMD64, BackendCUDA128, 64, 24 * 1024},
		{PlatformLinuxAMD64, BackendVulkan, 32, 12 * 1024},
		{PlatformLinuxAMD64, BackendCPU, 8, 0},
		{PlatformLinuxAMD64, BackendCPU, 64, 0},
	}

	for _, m := range machines {
		name := fmt.Sprintf("%s/%s/ram%.0f/vram%d", m.platform, m.backend, m.ramGiB, max(m.deviceMiB, -1))
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			in := ResolveInput{MachineProfile: MachineProfile{
				Platform: m.platform, Backend: m.backend, RAMGiB: m.ramGiB,
			}}
			if m.deviceMiB >= 0 {
				var devices []DeviceMemory
				if m.deviceMiB > 0 {
					devices = []DeviceMemory{{
						Name: "DEV0", Description: "NVIDIA GeForce RTX 4090",
						TotalMiB: m.deviceMiB, FreeMiB: m.deviceMiB,
					}}
					if m.platform == PlatformDarwinARM64 {
						devices[0] = DeviceMemory{
							Name: "MTL0", Description: "Apple M4 Max",
							TotalMiB: m.deviceMiB, FreeMiB: m.deviceMiB,
						}
					}
				}
				topology := probedTopology(t, m.platform, m.ramGiB, devices...)
				in.Topology = &topology
				in.GPU = ClassifyGPUs(topology.Devices)
			}

			res, err := Resolve(in)
			if err != nil {
				// A refusal is a correct outcome; this test is about the plans
				// that are emitted.
				if !errors.Is(err, ErrInsufficientMemory) {
					t.Fatalf("Resolve error = %v, want success or ErrInsufficientMemory", err)
				}
				return
			}

			plan := res.Memory
			ctx := plan.ContextSize
			if ctx == 0 {
				ctx = plan.FitMinContext // a fit-sized plan is held to its floor
			}
			offloaded := plan.OffloadsToDevice()
			deviceMiB, hostMiB, _, ferr := footprint(profile, res.Packing, ctx, plan.KVType, offloaded,
				false, plan.KVOffload, plan.MMProjOffload)
			if ferr != nil {
				t.Fatalf("footprint(%s, %d, %s, %v): %v", res.Packing, ctx, plan.KVType, offloaded, ferr)
			}

			// An accelerator nobody measured cannot be checked, and the plan
			// says so — that is the one axis this test cannot assert on.
			deviceUnmeasured := noteContaining(plan.Notes, "could not be measured")

			if offloaded && !deviceUnmeasured && deviceMiB > plan.DeviceBudgetMiB {
				t.Errorf("plan needs %d MiB on the accelerator against a %d MiB budget "+
					"(%s, %d-token %s, -ngl %d)",
					deviceMiB, plan.DeviceBudgetMiB, res.Packing, ctx, plan.KVType, deref(plan.Layers))
			}
			if hostMiB > plan.HostBudgetMiB {
				t.Errorf("plan needs %d MiB of host RAM against a %d MiB budget "+
					"(%s, %d-token %s, -ngl %d)",
					hostMiB, plan.HostBudgetMiB, res.Packing, ctx, plan.KVType, deref(plan.Layers))
			}
			// On a unified machine the two footprints come out of ONE pool, so
			// the sum is the number that has to fit — checking each against its
			// own budget would pass a launch needing twice the machine's memory.
			if plan.GPUFamily == GPUFamilyAppleSilicon || (m.deviceMiB < 0 && m.platform == PlatformDarwinARM64) {
				if total := deviceMiB + hostMiB; !deviceUnmeasured && total > plan.HostBudgetMiB {
					t.Errorf("unified pool: plan needs %d + %d = %d MiB against a %d MiB budget",
						deviceMiB, hostMiB, total, plan.HostBudgetMiB)
				}
			}
		})
	}
}
