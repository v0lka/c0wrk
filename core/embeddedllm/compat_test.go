package embeddedllm

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// packingForBackend is the shorthand the older fixtures use: the packing for a
// backend alone, with every refinement unmeasured. Unknown is the conservative
// side of all three refinements (no CPU-feature probe, no device probe, no fit
// gate), so this is exactly what the pre-guard resolver computed.
func packingForBackend(backend Backend) Packing {
	return packingFor(backend, GPUFamilyUnknown, FitUnknown, HostCaps{})
}

// resolveMachineWithTable is resolveWith for the machine-only convenience form:
// a registry stub plus the three machine facts, with no device topology and no
// tuning, so the plan takes the RAM-only launch shape and the statically
// decidable guards. It lives in a test file because tests are its only caller.
func resolveMachineWithTable(table AssetTable, platform string, backend Backend, ramGiB float64) (Resolution, error) {
	return resolveWith(table, ResolveInput{MachineProfile: MachineProfile{
		Platform: platform,
		Backend:  backend,
		RAMGiB:   ramGiB,
	}})
}

// ── the packing table ──

// TestDecidePackingTable is the whole packing matrix: every axis the decision
// reads, one case per documented cause, plus the precedence between them. The
// expectations are the upstream facts, not a restatement of the code — each
// case names the document it comes from.
func TestDecidePackingTable(t *testing.T) {
	t.Parallel()

	// fixedPin is a build that demonstrably contains PR #245 (the current pin),
	// brokenPin one that demonstrably predates it (b10709, published 2026-09-18,
	// against a fix merged 2026-09-23).
	const fixedPin = minBuildWithAVX512PQ2_0Fix
	const brokenPin = 10709

	avx512 := HostCaps{AVX512: CPUFeaturePresent}
	noAVX512 := HostCaps{AVX512: CPUFeatureAbsent}
	unknownCPU := HostCaps{} // the zero value: no probe answered

	cases := []struct {
		name       string
		in         PackingInput
		want       Packing
		wantReason PackingReason
	}{
		{
			// The default: a backend with PQ2_0 kernels, no CPU or capacity
			// problem, and a GPU generation the model card measures as
			// PQ2_0-faster.
			name:       "default CUDA on Blackwell is PQ2_0",
			in:         PackingInput{Backend: BackendCUDA128, GPU: GPUFamilyNVIDIABlackwell, RuntimeBuild: fixedPin},
			want:       PackingPQ2_0,
			wantReason: PackingReasonDefault,
		},
		{
			name:       "default Metal is PQ2_0",
			in:         PackingInput{Backend: BackendMetal, GPU: GPUFamilyAppleSilicon, RuntimeBuild: fixedPin},
			want:       PackingPQ2_0,
			wantReason: PackingReasonDefault,
		},
		{
			name:       "default CPU-only is PQ2_0 on a non-AVX-512 host",
			in:         PackingInput{Backend: BackendCPU, Host: noAVX512, RuntimeBuild: fixedPin},
			want:       PackingPQ2_0,
			wantReason: PackingReasonDefault,
		},
		{
			// Rule 1: Vulkan has no PQ2_0 (group-128) kernels at all.
			name:       "Vulkan is PTQ1_0 because it has no PQ2_0 kernels",
			in:         PackingInput{Backend: BackendVulkan, RuntimeBuild: fixedPin},
			want:       PackingPTQ1_0,
			wantReason: PackingReasonNoPQ2_0Kernels,
		},
		{
			// Vulkan outranks every other rule: even a machine that would
			// otherwise want PQ2_0 cannot have it there.
			name: "Vulkan outranks a measured-sufficient budget and a PQ2_0-faster GPU",
			in: PackingInput{Backend: BackendVulkan, GPU: GPUFamilyNVIDIAHopper,
				FitsPQ2_0: FitSufficient, Host: noAVX512, RuntimeBuild: fixedPin},
			want:       PackingPTQ1_0,
			wantReason: PackingReasonNoPQ2_0Kernels,
		},
		{
			// Rule 2: KNOWN_ISSUES "CPU crash on load (AVX-512 CPUs)" —
			// PQ2_0 segfaults at load on an AVX-512 host, on a pin without
			// PR #245.
			name:       "AVX-512 host on a pin predating #245 is PTQ1_0",
			in:         PackingInput{Backend: BackendCUDA128, Host: avx512, RuntimeBuild: brokenPin},
			want:       PackingPTQ1_0,
			wantReason: PackingReasonAVX512PQ2_0Segfault,
		},
		{
			// The segfault happens on the CPU repack path even with every
			// layer offloaded, so a GPU-backed plan is not exempt.
			name:       "AVX-512 host on a pin predating #245 is PTQ1_0 even fully offloaded",
			in:         PackingInput{Backend: BackendMetal, GPU: GPUFamilyAppleSilicon, Host: avx512, RuntimeBuild: brokenPin},
			want:       PackingPTQ1_0,
			wantReason: PackingReasonAVX512PQ2_0Segfault,
		},
		{
			// An UNPROBED host is treated as AVX-512-capable on an unfixed
			// pin: Unknown resolves to the side that cannot segfault.
			name:       "unprobed CPU on a pin predating #245 is PTQ1_0",
			in:         PackingInput{Backend: BackendCUDA128, Host: unknownCPU, RuntimeBuild: brokenPin},
			want:       PackingPTQ1_0,
			wantReason: PackingReasonAVX512PQ2_0Segfault,
		},
		{
			// The same machine on the fixed pin keeps the default: the guard
			// is pin-aware, so it stops firing when the pin does.
			name:       "AVX-512 host on a pin containing #245 keeps PQ2_0",
			in:         PackingInput{Backend: BackendCUDA128, Host: avx512, RuntimeBuild: fixedPin},
			want:       PackingPQ2_0,
			wantReason: PackingReasonDefault,
		},
		{
			// A build number of 0 means "this tag is not a shape we
			// recognize", which must behave like an unfixed pin.
			name:       "unrecognized pin build is treated as predating #245",
			in:         PackingInput{Backend: BackendCUDA128, Host: avx512, RuntimeBuild: 0},
			want:       PackingPTQ1_0,
			wantReason: PackingReasonAVX512PQ2_0Segfault,
		},
		{
			name:       "probed non-AVX-512 host on a pin predating #245 keeps PQ2_0",
			in:         PackingInput{Backend: BackendCUDA128, Host: noAVX512, RuntimeBuild: brokenPin},
			want:       PackingPQ2_0,
			wantReason: PackingReasonDefault,
		},
		{
			// Rule 3: a measured budget PQ2_0 does not fit. This is the
			// "memory is tightest" case the model card describes — and it is
			// driven by a MEASUREMENT, never by installed RAM.
			name:       "PQ2_0 that does not fit the device budget is PTQ1_0",
			in:         PackingInput{Backend: BackendCUDA128, GPU: GPUFamilyNVIDIABlackwell, FitsPQ2_0: FitInsufficient, RuntimeBuild: fixedPin},
			want:       PackingPTQ1_0,
			wantReason: PackingReasonPQ2_0DoesNotFit,
		},
		{
			// An unmeasured budget must not downgrade anything: the zero
			// value of the fit verdict is Unknown, not Insufficient.
			name:       "unmeasured budget keeps PQ2_0",
			in:         PackingInput{Backend: BackendCUDA128, GPU: GPUFamilyNVIDIABlackwell, FitsPQ2_0: FitUnknown, RuntimeBuild: fixedPin},
			want:       PackingPQ2_0,
			wantReason: PackingReasonDefault,
		},
		{
			name:       "measured-sufficient budget keeps PQ2_0",
			in:         PackingInput{Backend: BackendCUDA128, GPU: GPUFamilyNVIDIABlackwell, FitsPQ2_0: FitSufficient, RuntimeBuild: fixedPin},
			want:       PackingPQ2_0,
			wantReason: PackingReasonDefault,
		},
		{
			// Rule 4: the model card measures PTQ1_0 as the faster DECODE on
			// Ada-generation cards and the L4.
			name:       "Ada GPU is PTQ1_0",
			in:         PackingInput{Backend: BackendCUDA128, GPU: GPUFamilyNVIDIAAda, RuntimeBuild: fixedPin},
			want:       PackingPTQ1_0,
			wantReason: PackingReasonGPUGenerationDecode,
		},
		{
			// The generations the card measures as PQ2_0-faster keep it.
			name:       "Hopper GPU keeps PQ2_0",
			in:         PackingInput{Backend: BackendCUDA128, GPU: GPUFamilyNVIDIAHopper, RuntimeBuild: fixedPin},
			want:       PackingPQ2_0,
			wantReason: PackingReasonDefault,
		},
		{
			name:       "Ampere GPU keeps PQ2_0",
			in:         PackingInput{Backend: BackendCUDA128, GPU: GPUFamilyNVIDIAAmpere, RuntimeBuild: fixedPin},
			want:       PackingPQ2_0,
			wantReason: PackingReasonDefault,
		},
		{
			name:       "unknown GPU keeps PQ2_0",
			in:         PackingInput{Backend: BackendCUDA128, GPU: GPUFamilyUnknown, RuntimeBuild: fixedPin},
			want:       PackingPQ2_0,
			wantReason: PackingReasonDefault,
		},
		{
			// Precedence: a crash outranks a capacity argument, which
			// outranks a throughput one. All three want PTQ1_0, so only the
			// recorded reason distinguishes them — and the reason is what the
			// user reads.
			name: "AVX-512 crash outranks does-not-fit and Ada",
			in: PackingInput{Backend: BackendCUDA128, GPU: GPUFamilyNVIDIAAda,
				FitsPQ2_0: FitInsufficient, Host: avx512, RuntimeBuild: brokenPin},
			want:       PackingPTQ1_0,
			wantReason: PackingReasonAVX512PQ2_0Segfault,
		},
		{
			name:       "does-not-fit outranks the Ada throughput preference",
			in:         PackingInput{Backend: BackendCUDA128, GPU: GPUFamilyNVIDIAAda, FitsPQ2_0: FitInsufficient, RuntimeBuild: fixedPin},
			want:       PackingPTQ1_0,
			wantReason: PackingReasonPQ2_0DoesNotFit,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := decidePacking(tc.in)
			if got.Packing != tc.want {
				t.Errorf("decidePacking().Packing = %q, want %q", got.Packing, tc.want)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("decidePacking().Reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if got.Reason == "" {
				t.Error("decidePacking() returned an empty reason: a packing choice must always carry one")
			}
		})
	}
}

// TestPackingForMatchesThePinnedBuild pins the wrapper to the pure decision:
// packingFor is decidePacking with RuntimeBuild taken from registry.go, and
// nothing else.
func TestPackingForMatchesThePinnedBuild(t *testing.T) {
	t.Parallel()

	build := pinnedRuntimeBuild()
	if build == 0 {
		t.Fatalf("pinnedRuntimeBuild() = 0: RuntimeTag %q is not a recognized fork tag", RuntimeTag)
	}

	inputs := []PackingInput{
		{Backend: BackendVulkan},
		{Backend: BackendCUDA128, GPU: GPUFamilyNVIDIAAda},
		{Backend: BackendCUDA128, FitsPQ2_0: FitInsufficient},
		{Backend: BackendCUDA128, Host: HostCaps{AVX512: CPUFeaturePresent}},
		{Backend: BackendMetal, GPU: GPUFamilyAppleSilicon},
	}
	for _, in := range inputs {
		in.RuntimeBuild = build
		want := decidePacking(in).Packing
		if got := packingFor(in.Backend, in.GPU, in.FitsPQ2_0, in.Host); got != want {
			t.Errorf("packingFor(%q, %q, %v, %+v) = %q, want %q (the pinned build %d)",
				in.Backend, in.GPU, in.FitsPQ2_0, in.Host, got, want, build)
		}
	}
}

// TestPinnedRuntimeBuildIsNotKnownBrokenOnAVX512 asserts the pin c0wrk ships
// is at or above the #245 threshold, i.e. the AVX-512 downgrade is inert
// today. When a future bump LOWERS the build number (a rollback to an older
// tag) this test fails on purpose: the resolver would then be downgrading
// every AVX-512 host, and that is a fact a reader of the pin should see.
func TestPinnedRuntimeBuildIsNotKnownBrokenOnAVX512(t *testing.T) {
	t.Parallel()

	build := pinnedRuntimeBuild()
	if build < minBuildWithAVX512PQ2_0Fix {
		t.Fatalf("RuntimeTag %q is build %d, below the first build proven to contain PR #245 (%d): "+
			"every AVX-512 host would be downgraded to PTQ1_0", RuntimeTag, build, minBuildWithAVX512PQ2_0Fix)
	}
	if got := decidePacking(PackingInput{
		Backend: BackendCUDA128, Host: HostCaps{AVX512: CPUFeaturePresent}, RuntimeBuild: build,
	}); got.Packing != PackingPQ2_0 {
		t.Errorf("an AVX-512 host on the current pin resolved %q, want %q", got.Packing, PackingPQ2_0)
	}
}

// ── the GPU classifier ──

// TestClassifyGPU covers the description table. The inputs are the shapes the
// pinned runtime prints in its own device inventory (`--list-devices` and the
// `-lv 4` parameter dump) and the shapes the vendor tools print, plus the
// negative cases that must NOT be recognized.
func TestClassifyGPU(t *testing.T) {
	t.Parallel()

	cases := []struct {
		description string
		want        GPUFamily
	}{
		// Apple.
		{"Apple M4 Max", GPUFamilyAppleSilicon},
		{"Apple M1 Ultra", GPUFamilyAppleSilicon},
		{"Apple M5 Pro", GPUFamilyAppleSilicon},

		// Intel Arc (#192).
		{"Intel(R) Arc(TM) B390 Graphics", GPUFamilyIntelArc},
		{"Intel Arc A770", GPUFamilyIntelArc},
		{"Arc B580", GPUFamilyIntelArc},

		// AMD gfx1151 / Strix Halo (#223): recognized only when the string
		// carries the gfx target or the product name, which is the documented
		// limitation on the constant.
		{"gfx1151", GPUFamilyAMDGFX1151},
		{"AMD Radeon(TM) Graphics (gfx1151)", GPUFamilyAMDGFX1151},
		{"AMD Strix Halo", GPUFamilyAMDGFX1151},
		{"AMD Ryzen AI Max+ 395", GPUFamilyAMDGFX1151},

		// AMD RDNA2 (Bonsai-demo #197).
		{"AMD Radeon RX 6800 XT", GPUFamilyAMDRDNA2},
		{"Radeon RX 6600", GPUFamilyAMDRDNA2},
		{"gfx1030", GPUFamilyAMDRDNA2},
		{"AMD Radeon Pro W6900X", GPUFamilyAMDRDNA2},

		// NVIDIA Ada — the PTQ1_0-decode generation.
		{"NVIDIA GeForce RTX 4090", GPUFamilyNVIDIAAda},
		{"NVIDIA RTX 4060 Ti", GPUFamilyNVIDIAAda},
		{"NVIDIA RTX 6000 Ada Generation", GPUFamilyNVIDIAAda},
		{"NVIDIA L4", GPUFamilyNVIDIAAda},
		{"NVIDIA L40S", GPUFamilyNVIDIAAda},
		// The explicit Ada token must beat the 50xx Blackwell digit rule:
		// "RTX 5000 Ada" is an Ada workstation card.
		{"NVIDIA RTX 5000 Ada Generation", GPUFamilyNVIDIAAda},

		// NVIDIA Blackwell — PQ2_0-decode.
		{"NVIDIA GeForce RTX 5090", GPUFamilyNVIDIABlackwell},
		{"NVIDIA RTX PRO 6000 Blackwell", GPUFamilyNVIDIABlackwell},
		{"NVIDIA B200", GPUFamilyNVIDIABlackwell},

		// NVIDIA Hopper — PQ2_0-decode.
		{"NVIDIA H100 SXM", GPUFamilyNVIDIAHopper},
		{"NVIDIA H200", GPUFamilyNVIDIAHopper},

		// NVIDIA Ampere — PQ2_0-decode (A100 is named in the model card).
		{"NVIDIA A100-SXM4-80GB", GPUFamilyNVIDIAAmpere},
		{"NVIDIA GeForce RTX 3090", GPUFamilyNVIDIAAmpere},

		// Not recognized: no decision keys on these, so Unknown is the honest
		// answer rather than a guess at a generation.
		{"", GPUFamilyUnknown},
		{"   ", GPUFamilyUnknown},
		{"Accelerate", GPUFamilyUnknown}, // the measured Metal CPU/BLAS pseudo-device
		{"AMD Radeon RX 7900 XTX", GPUFamilyAMDRDNA3},
		{"gfx1100", GPUFamilyAMDRDNA3},
		{"AMD Radeon RX 7600", GPUFamilyAMDRDNA3},
		{"NVIDIA GeForce RTX 2080 Ti", GPUFamilyUnknown},
		{"Some Vendor Accelerator 9000", GPUFamilyUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()

			if got := ClassifyGPU(tc.description); got != tc.want {
				t.Errorf("ClassifyGPU(%q) = %q, want %q", tc.description, got, tc.want)
			}
		})
	}
}

// TestClassifyGPUFoldsAnInventory covers the multi-device fold: the first
// recognized device wins, memoryless pseudo-devices are skipped, and an
// inventory with nothing recognized stays Unknown.
func TestClassifyGPUFoldsAnInventory(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		devices []DeviceMemory
		want    GPUFamily
	}{
		{
			name:    "empty inventory",
			devices: nil,
			want:    GPUFamilyUnknown,
		},
		{
			name: "single device",
			devices: []DeviceMemory{
				{Name: "CUDA0", Description: "NVIDIA GeForce RTX 4090"},
			},
			want: GPUFamilyNVIDIAAda,
		},
		{
			name: "unrecognized devices before a recognized one",
			devices: []DeviceMemory{
				{Name: "BLAS", Description: "Accelerate"},
				{Name: "MTL0", Description: "Apple M4 Max"},
			},
			want: GPUFamilyAppleSilicon,
		},
		{
			name: "first recognized device wins over a later one",
			devices: []DeviceMemory{
				{Name: "Vulkan0", Description: "Intel Arc A770"},
				{Name: "Vulkan1", Description: "NVIDIA GeForce RTX 4090"},
			},
			want: GPUFamilyIntelArc,
		},
		{
			name: "nothing recognized",
			devices: []DeviceMemory{
				{Name: "X0", Description: "Mystery Accelerator"},
			},
			want: GPUFamilyUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := ClassifyGPUs(tc.devices); got != tc.want {
				t.Errorf("ClassifyGPUs(...) = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAdaIsTheOnlyPTQ1_0DecodeFamily pins the throughput-table reading: Ada
// (which is where the model card puts the L4 and L40S) is the ONLY family
// documented as PTQ1_0-faster for decode.
func TestAdaIsTheOnlyPTQ1_0DecodeFamily(t *testing.T) {
	t.Parallel()

	families := []GPUFamily{
		GPUFamilyUnknown, GPUFamilyAppleSilicon, GPUFamilyNVIDIAAda,
		GPUFamilyNVIDIABlackwell, GPUFamilyNVIDIAHopper, GPUFamilyNVIDIAAmpere,
		GPUFamilyAMDRDNA2, GPUFamilyAMDRDNA3, GPUFamilyAMDGFX1151, GPUFamilyIntelArc,
	}
	for _, family := range families {
		want := family == GPUFamilyNVIDIAAda
		if got := family.prefersPTQ1_0Decode(); got != want {
			t.Errorf("%q.prefersPTQ1_0Decode() = %v, want %v", family, got, want)
		}
	}
}

// ── the guard table ──

// TestCompatibilityGuardsTable is the guard matrix. Each expectation is a
// documented upstream failure with its issue citation; the negative cases are
// just as load-bearing, because a guard that fires on healthy hardware is a
// needless degradation.
func TestCompatibilityGuardsTable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		backend  Backend
		gpu      GPUFamily
		platform string
		want     []GuardID
	}{
		{
			// #222 — CUDA 13.3 crashes; upstream names the fallback per OS.
			name:     "CUDA 13.3 on Linux prefers 12.8",
			backend:  BackendCUDA133,
			platform: PlatformLinuxAMD64,
			want:     []GuardID{GuardCUDA133Crash},
		},
		{
			name:     "CUDA 13.3 on Windows prefers 12.4",
			backend:  BackendCUDA133,
			platform: PlatformWindowsAMD64,
			want:     []GuardID{GuardCUDA133Crash, GuardWindowsCUDANoStart},
		},
		{
			// The older CUDA tags are not implicated by #222.
			name:     "CUDA 12.8 on Linux is unguarded",
			backend:  BackendCUDA128,
			platform: PlatformLinuxAMD64,
			want:     nil,
		},
		{
			name:     "CUDA 12.4 on Windows carries only the advisory #241",
			backend:  BackendCUDA124,
			platform: PlatformWindowsAMD64,
			want:     []GuardID{GuardWindowsCUDANoStart},
		},
		{
			// Bonsai-demo #197 — ROCm aborts on consumer RDNA2.
			name:     "ROCm on RDNA2 prefers Vulkan",
			backend:  BackendROCm,
			gpu:      GPUFamilyAMDRDNA2,
			platform: PlatformLinuxAMD64,
			want:     []GuardID{GuardROCmRDNA2Abort},
		},
		{
			name:     "ROCm on RDNA2 on Windows prefers Vulkan and carries #241 only for CUDA",
			backend:  BackendROCm,
			gpu:      GPUFamilyAMDRDNA2,
			platform: PlatformWindowsAMD64,
			want:     []GuardID{GuardROCmRDNA2Abort},
		},
		{
			// A generation #197 does not name is not guarded.
			name:     "ROCm on an unclassified AMD GPU is unguarded",
			backend:  BackendROCm,
			gpu:      GPUFamilyUnknown,
			platform: PlatformLinuxAMD64,
			want:     nil,
		},
		{
			// #223 — garbled output, Windows HIP, gfx1151.
			name:     "Windows HIP on gfx1151 prefers Vulkan",
			backend:  BackendROCm,
			gpu:      GPUFamilyAMDGFX1151,
			platform: PlatformWindowsAMD64,
			want:     []GuardID{GuardWindowsHIPGFX1151Garbled},
		},
		{
			// The same iGPU on Linux is not covered: #223 names Windows HIP.
			name:     "Linux ROCm on gfx1151 is unguarded",
			backend:  BackendROCm,
			gpu:      GPUFamilyAMDGFX1151,
			platform: PlatformLinuxAMD64,
			want:     nil,
		},
		{
			// #192 — PTQ1_0 on Vulkan hangs Intel Arc.
			name:     "Vulkan on Intel Arc is advisory",
			backend:  BackendVulkan,
			gpu:      GPUFamilyIntelArc,
			platform: PlatformLinuxAMD64,
			want:     []GuardID{GuardVulkanIntelArcHang},
		},
		{
			name:     "Vulkan on Intel Arc on Windows is advisory",
			backend:  BackendVulkan,
			gpu:      GPUFamilyIntelArc,
			platform: PlatformWindowsAMD64,
			want:     []GuardID{GuardVulkanIntelArcHang},
		},
		{
			name:     "Vulkan on a non-Intel GPU is unguarded",
			backend:  BackendVulkan,
			gpu:      GPUFamilyAMDRDNA2,
			platform: PlatformLinuxAMD64,
			want:     nil,
		},
		{
			// Healthy machines: nothing documented, nothing fired.
			name:     "Metal on Apple Silicon is unguarded",
			backend:  BackendMetal,
			gpu:      GPUFamilyAppleSilicon,
			platform: PlatformDarwinARM64,
			want:     nil,
		},
		{
			name:     "CPU-only is unguarded",
			backend:  BackendCPU,
			gpu:      GPUFamilyUnknown,
			platform: PlatformLinuxAMD64,
			want:     nil,
		},
		{
			name:     "CUDA 12.8 on an Ada card is unguarded",
			backend:  BackendCUDA128,
			gpu:      GPUFamilyNVIDIAAda,
			platform: PlatformLinuxAMD64,
			want:     nil,
		},
		{
			name:     "Linux CUDA is not covered by the Windows-only #241",
			backend:  BackendCUDA128,
			platform: PlatformLinuxAMD64,
			want:     nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := CompatibilityGuards(tc.backend, tc.gpu, tc.platform)
			gotIDs := make([]GuardID, 0, len(got))
			for _, decision := range got {
				gotIDs = append(gotIDs, decision.Guard)
			}
			if !slices.Equal(gotIDs, tc.want) {
				t.Fatalf("CompatibilityGuards(%q, %q, %q) fired %v, want %v",
					tc.backend, tc.gpu, tc.platform, gotIDs, tc.want)
			}

			// Nothing the table returns is ever pre-marked as applied: that is
			// the consumer's statement, not the table's.
			for _, decision := range got {
				if decision.Applied {
					t.Errorf("guard %q came back Applied=true from the pure table", decision.Guard)
				}
			}
		})
	}
}

// TestGuardDecisionShape asserts the per-guard contract: a typed reason, a
// severity derived from it, an upstream issue citation, non-empty guidance, and
// a substitution target exactly when the action asks for one.
func TestGuardDecisionShape(t *testing.T) {
	t.Parallel()

	issueRE := regexp.MustCompile(`^(PrismML-Eng/(llama\.cpp|Bonsai-demo))#\d+$`)

	machines := []struct {
		backend  Backend
		gpu      GPUFamily
		platform string
	}{
		{BackendCUDA133, GPUFamilyUnknown, PlatformLinuxAMD64},
		{BackendCUDA133, GPUFamilyNVIDIAAda, PlatformWindowsAMD64},
		{BackendROCm, GPUFamilyAMDRDNA2, PlatformLinuxAMD64},
		{BackendROCm, GPUFamilyAMDGFX1151, PlatformWindowsAMD64},
		{BackendVulkan, GPUFamilyIntelArc, PlatformWindowsAMD64},
		{BackendCUDA124, GPUFamilyUnknown, PlatformWindowsAMD64},
	}

	seen := map[GuardID]bool{}
	for _, machine := range machines {
		for _, decision := range CompatibilityGuards(machine.backend, machine.gpu, machine.platform) {
			seen[decision.Guard] = true

			if decision.Reason == "" {
				t.Errorf("guard %q has no typed reason", decision.Guard)
			}
			if want := severityFor(decision.Reason); decision.Severity != want {
				t.Errorf("guard %q severity = %q, want %q (derived from reason %q)",
					decision.Guard, decision.Severity, want, decision.Reason)
			}
			if !issueRE.MatchString(decision.Issue) {
				t.Errorf("guard %q issue = %q, want a <repo>#<number> citation", decision.Guard, decision.Issue)
			}
			if strings.TrimSpace(decision.Guidance) == "" {
				t.Errorf("guard %q has no user-facing guidance", decision.Guard)
			}
			switch decision.Action {
			case GuardActionPreferBackend:
				if decision.Backend == "" {
					t.Errorf("guard %q prefers a backend but names none", decision.Guard)
				}
			case GuardActionPreferPacking:
				if decision.Packing == "" {
					t.Errorf("guard %q prefers a packing but names none", decision.Guard)
				}
			case GuardActionAdvisory:
				if decision.Backend != "" || decision.Packing != "" {
					t.Errorf("advisory guard %q must not carry a substitution target", decision.Guard)
				}
			default:
				t.Errorf("guard %q has an unknown action %q", decision.Guard, decision.Action)
			}
		}
	}

	// Every guard the package declares must have been exercised above, so a new
	// constant cannot land without a shape assertion.
	for _, id := range []GuardID{
		GuardCUDA133Crash, GuardROCmRDNA2Abort, GuardWindowsHIPGFX1151Garbled,
		GuardVulkanIntelArcHang, GuardWindowsCUDANoStart,
	} {
		if !seen[id] {
			t.Errorf("guard %q is declared but never fired by the machines above", id)
		}
	}
}

// TestEveryGuardConstantCitesItsUpstreamIssue scans compat.go itself: each
// GuardID constant must be introduced by a comment block that names an issue
// number. This is the mechanical half of the "cite the upstream issue" rule —
// the table test above asserts the runtime value, this asserts the source.
func TestEveryGuardConstantCitesItsUpstreamIssue(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("compat.go")
	if err != nil {
		t.Fatalf("reading compat.go: %v", err)
	}
	lines := strings.Split(string(source), "\n")

	declRE := regexp.MustCompile(`^\t(Guard[A-Za-z0-9_]+) GuardID = `)
	issueRE := regexp.MustCompile(`#\d+`)

	declared := map[string]bool{}
	for i, line := range lines {
		match := declRE.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		name := match[1]
		declared[name] = true

		// Walk back over the contiguous comment block that documents this
		// constant, and require an issue citation inside it.
		var comment []string
		for j := i - 1; j >= 0 && strings.HasPrefix(strings.TrimSpace(lines[j]), "//"); j-- {
			comment = append(comment, lines[j])
		}
		if len(comment) == 0 {
			t.Errorf("%s has no doc comment", name)
			continue
		}
		block := strings.Join(comment, "\n")
		if !issueRE.MatchString(block) {
			t.Errorf("%s doc comment cites no upstream issue number (want e.g. \"#222\")", name)
		}
	}

	for _, want := range []string{
		"GuardCUDA133Crash", "GuardROCmRDNA2Abort", "GuardWindowsHIPGFX1151Garbled",
		"GuardVulkanIntelArcHang", "GuardWindowsCUDANoStart",
	} {
		if !declared[want] {
			t.Errorf("compat.go declares no %s constant", want)
		}
	}
}

// TestSeverityForCoversEveryReason keeps the reason→severity mapping total: an
// unranked reason must never render as the mildest severity.
func TestSeverityForCoversEveryReason(t *testing.T) {
	t.Parallel()

	cases := map[GuardReason]GuardSeverity{
		GuardReasonCrashOnLoad:   GuardSeverityCritical,
		GuardReasonProcessAbort:  GuardSeverityCritical,
		GuardReasonGarbledOutput: GuardSeverityCritical,
		GuardReasonHang:          GuardSeverityWarning,
		GuardReasonFailsToStart:  GuardSeverityWarning,
		GuardReason("unranked"):  GuardSeverityCritical,
		GuardReason(""):          GuardSeverityCritical,
	}
	for reason, want := range cases {
		if got := severityFor(reason); got != want {
			t.Errorf("severityFor(%q) = %q, want %q", reason, got, want)
		}
	}
}

// TestMergeGuardDecisionsDedupes covers the two-pass record: a guard already in
// the base keeps its own Applied flag, and a new one is appended once.
func TestMergeGuardDecisionsDedupes(t *testing.T) {
	t.Parallel()

	base := []GuardDecision{
		{Guard: GuardCUDA133Crash, Action: GuardActionPreferBackend, Reason: GuardReasonCrashOnLoad, Applied: true},
		{Guard: GuardWindowsCUDANoStart, Action: GuardActionAdvisory, Reason: GuardReasonFailsToStart},
	}
	extra := []GuardDecision{
		{Guard: GuardWindowsCUDANoStart, Action: GuardActionAdvisory, Reason: GuardReasonFailsToStart, Applied: true},
		{Guard: GuardVulkanIntelArcHang, Action: GuardActionAdvisory, Reason: GuardReasonHang},
	}

	got := mergeGuardDecisions(base, extra)
	wantIDs := []GuardID{GuardCUDA133Crash, GuardWindowsCUDANoStart, GuardVulkanIntelArcHang}
	gotIDs := make([]GuardID, 0, len(got))
	for _, decision := range got {
		gotIDs = append(gotIDs, decision.Guard)
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("merged guards = %v, want %v", gotIDs, wantIDs)
	}
	if !got[0].Applied {
		t.Error("the base entry lost its Applied flag")
	}
	if got[1].Applied {
		t.Error("a duplicate extra entry overwrote the base's Applied=false")
	}

	// Merging into nil is the common first-install case and must not panic.
	if merged := mergeGuardDecisions(nil, extra); len(merged) != 2 {
		t.Errorf("mergeGuardDecisions(nil, 2 entries) = %d decisions, want 2", len(merged))
	}
	if merged := mergeGuardDecisions(nil, nil); merged != nil {
		t.Errorf("mergeGuardDecisions(nil, nil) = %v, want nil", merged)
	}
}

// ── pin parsing ──

// TestParseRuntimeBuild covers the tag shape the pin uses, and the shapes it
// must refuse rather than guess at.
func TestParseRuntimeBuild(t *testing.T) {
	t.Parallel()

	cases := []struct {
		tag    string
		want   int
		wantOK bool
	}{
		{tag: "prism-b10735-842b188", want: 10735, wantOK: true},
		{tag: "prism-b10709-9a9394a", want: 10709, wantOK: true},
		{tag: "prism-b10687-0000000", want: 10687, wantOK: true},
		{tag: " prism-b10735-842b188 ", want: 10735, wantOK: true},
		// Refused: not the fork's release-tag shape.
		{tag: "", wantOK: false},
		{tag: "prism-b10735", wantOK: false},
		{tag: "prism-b-842b188", wantOK: false},
		{tag: "prism-babc12-842b188", wantOK: false},
		{tag: "v10735-842b188", wantOK: false},
		{tag: "prism-b0-842b188", wantOK: false},
		{tag: "prism-b10735-ZZZZZZZ", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.tag, func(t *testing.T) {
			t.Parallel()

			got, ok := parseRuntimeBuild(tc.tag)
			if ok != tc.wantOK {
				t.Fatalf("parseRuntimeBuild(%q) ok = %v, want %v", tc.tag, ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("parseRuntimeBuild(%q) = %d, want %d", tc.tag, got, tc.want)
			}
		})
	}
}

// ── guards folded into a resolution ──

// TestResolveProfileAppliesBackendGuards checks the substitution half end to
// end: a guarded backend is replaced by a PINNED one, the decision is recorded
// as applied, and the packing follows the backend it ends up with.
func TestResolveProfileAppliesBackendGuards(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		profile     MachineProfile
		wantBackend Backend
		wantPacking Packing
		wantApplied GuardID
	}{
		{
			// #222: a Linux CUDA 13.3 machine gets the 12.8 build, which its
			// newer driver still runs — provided the CUDA 12.x userland the
			// 12.8 build links against is really there.
			name: "CUDA 13.3 on Linux resolves to 12.8",
			profile: MachineProfile{Platform: PlatformLinuxAMD64, Backend: BackendCUDA133,
				RAMGiB: 64, CUDA12Userland: CUDA12Present},
			wantBackend: BackendCUDA128,
			wantPacking: PackingPQ2_0,
			wantApplied: GuardCUDA133Crash,
		},
		{
			// The userland probe says the fallback could not load here: absent
			// — including the ".so.12"-onto-13 symlink trap — or unknown. The
			// probed 13.3 build stays, and the guard is recorded unapplied
			// with guidance explaining why.
			name: "CUDA 13.3 on Linux keeps 13.3 without a CUDA 12 userland",
			profile: MachineProfile{Platform: PlatformLinuxAMD64, Backend: BackendCUDA133,
				RAMGiB: 64, CUDA12Userland: CUDA12Absent},
			wantBackend: BackendCUDA133,
			wantPacking: PackingPQ2_0,
			wantApplied: "",
		},
		{
			// An unanswered probe is not evidence of safety either.
			name: "CUDA 13.3 on Linux keeps 13.3 on an unknown CUDA 12 userland",
			profile: MachineProfile{Platform: PlatformLinuxAMD64, Backend: BackendCUDA133,
				RAMGiB: 64, CUDA12Userland: CUDA12Unknown},
			wantBackend: BackendCUDA133,
			wantPacking: PackingPQ2_0,
			wantApplied: "",
		},
		{
			// #222: Windows has no pinned 12.8 archive, so the substitution is
			// 12.4 — and that pair needs its cudart companion.
			name:        "CUDA 13.3 on Windows resolves to 12.4 with cudart",
			profile:     MachineProfile{Platform: PlatformWindowsAMD64, Backend: BackendCUDA133, RAMGiB: 64},
			wantBackend: BackendCUDA124,
			wantPacking: PackingPQ2_0,
			wantApplied: GuardCUDA133Crash,
		},
		{
			// Bonsai-demo #197: RDNA2 on ROCm aborts, so the plan becomes the
			// Vulkan build — which then takes PTQ1_0, because Vulkan has no
			// PQ2_0 kernels. The guard changes the backend and the packing
			// follows it, which is exactly upstream's prescribed workaround.
			name: "ROCm on RDNA2 resolves to Vulkan with PTQ1_0",
			profile: MachineProfile{Platform: PlatformLinuxAMD64, Backend: BackendROCm,
				GPU: GPUFamilyAMDRDNA2, RAMGiB: 64},
			wantBackend: BackendVulkan,
			wantPacking: PackingPTQ1_0,
			wantApplied: GuardROCmRDNA2Abort,
		},
		{
			// #223: Windows HIP on gfx1151 garbles PQ2_0 output, so the plan
			// becomes Vulkan + PTQ1_0.
			name: "Windows HIP on gfx1151 resolves to Vulkan with PTQ1_0",
			profile: MachineProfile{Platform: PlatformWindowsAMD64, Backend: BackendROCm,
				GPU: GPUFamilyAMDGFX1151, RAMGiB: 64},
			wantBackend: BackendVulkan,
			wantPacking: PackingPTQ1_0,
			wantApplied: GuardWindowsHIPGFX1151Garbled,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ResolveProfile(tc.profile)
			if err != nil {
				t.Fatalf("ResolveProfile error = %v, want success", err)
			}
			if got.Backend != tc.wantBackend {
				t.Errorf("Backend = %q, want %q", got.Backend, tc.wantBackend)
			}
			if got.Packing != tc.wantPacking {
				t.Errorf("Packing = %q, want %q", got.Packing, tc.wantPacking)
			}
			if got.GPU != tc.profile.GPU {
				t.Errorf("GPU = %q, want the profile's %q", got.GPU, tc.profile.GPU)
			}

			applied := guardIDs(got.Guards, true)
			if tc.wantApplied == "" {
				// The guard fired in the table but must NOT have changed the
				// plan: no applied decision at all, and the recorded #222
				// decision carries its skipped-substitution guidance.
				if len(applied) != 0 {
					t.Errorf("applied guards = %v, want none", applied)
				}
				if decision := findGuard(t, got.Guards, GuardCUDA133Crash); !strings.Contains(decision.Guidance, "CUDA 12.x") {
					t.Errorf("#222 guidance does not say why the substitution was skipped: %q", decision.Guidance)
				}
			} else if !slices.Contains(applied, tc.wantApplied) {
				t.Errorf("applied guards = %v, want %q among them", applied, tc.wantApplied)
			}

			// The resolved artifact set must describe the substituted backend,
			// not the probed one: the manifest and the download both read it.
			runtime := componentAsset(t, got, ComponentRuntime)
			if !strings.Contains(runtime.ArchiveName, archiveTokenFor(tc.wantBackend)) {
				t.Errorf("runtime archive = %q, want one for backend %q", runtime.ArchiveName, tc.wantBackend)
			}
			model := componentAsset(t, got, ComponentModel)
			if !strings.Contains(model.ArchiveName, string(tc.wantPacking)) {
				t.Errorf("model archive = %q, want the %s weights", model.ArchiveName, tc.wantPacking)
			}
		})
	}
}

// TestResolveProfileRecordsAdvisoryGuardsUnapplied checks the disclosure half:
// an advisory guard is recorded with its reason and its guidance, and it
// changes nothing about the plan.
func TestResolveProfileRecordsAdvisoryGuardsUnapplied(t *testing.T) {
	t.Parallel()

	got, err := ResolveProfile(MachineProfile{
		Platform: PlatformWindowsAMD64, Backend: BackendVulkan,
		GPU: GPUFamilyIntelArc, RAMGiB: 32,
	})
	if err != nil {
		t.Fatalf("ResolveProfile error = %v, want success", err)
	}
	if got.Backend != BackendVulkan || got.Packing != PackingPTQ1_0 {
		t.Fatalf("plan changed under an advisory guard: backend %q packing %q", got.Backend, got.Packing)
	}

	hang := findGuard(t, got.Guards, GuardVulkanIntelArcHang)
	if hang.Applied {
		t.Error("the #192 advisory is marked Applied; it must not claim a plan change")
	}
	if hang.Reason != GuardReasonHang || hang.Severity != GuardSeverityWarning {
		t.Errorf("#192 reason/severity = %q/%q, want %q/%q",
			hang.Reason, hang.Severity, GuardReasonHang, GuardSeverityWarning)
	}
	if !strings.Contains(hang.Guidance, "1,900") {
		t.Errorf("#192 guidance lost its documented failure threshold: %q", hang.Guidance)
	}
}

// TestResolveProfileRecordsWindowsCUDAAdvisory covers #241, the guard that
// cannot be decided statically: it is recorded on every Windows CUDA plan so
// the CPU-build fallback is already in the install record when the documented
// start failure happens, and it changes nothing about the plan.
func TestResolveProfileRecordsWindowsCUDAAdvisory(t *testing.T) {
	t.Parallel()

	got, err := ResolveProfile(MachineProfile{
		Platform: PlatformWindowsAMD64, Backend: BackendCUDA124, RAMGiB: 32,
	})
	if err != nil {
		t.Fatalf("ResolveProfile error = %v, want success", err)
	}
	if got.Backend != BackendCUDA124 {
		t.Errorf("Backend = %q, want the plan untouched by an advisory", got.Backend)
	}
	if !got.NeedsCudart {
		t.Error("the Windows CUDA 12.4 plan lost its cudart companion")
	}

	decision := findGuard(t, got.Guards, GuardWindowsCUDANoStart)
	if decision.Applied {
		t.Error("the #241 advisory is marked Applied; it changes nothing")
	}
	if decision.Reason != GuardReasonFailsToStart {
		t.Errorf("#241 reason = %q, want %q", decision.Reason, GuardReasonFailsToStart)
	}
	if !strings.Contains(decision.Guidance, "CPU-only build") {
		t.Errorf("#241 guidance lost its documented workaround: %q", decision.Guidance)
	}
}

// TestResolveMachineKeepsTheStaticGuards pins the machine-only convenience
// form: no GPU information means no GPU-specific guard, and the Linux #222
// substitution — statically decidable only in the presence of a measured CUDA
// 12.x userland — is held back with the guard recorded unapplied, while a
// profile that DID probe the userland gets the substitution.
func TestResolveMachineKeepsTheStaticGuards(t *testing.T) {
	t.Parallel()

	got, err := ResolveMachine(PlatformLinuxAMD64, BackendCUDA133, 64)
	if err != nil {
		t.Fatalf("ResolveMachine error = %v, want success", err)
	}
	if got.Backend != BackendCUDA133 {
		t.Errorf("Backend = %q, want %q (an unanswered probe must not justify the substitution)",
			got.Backend, BackendCUDA133)
	}
	if got.GPU != GPUFamilyUnknown {
		t.Errorf("GPU = %q, want unknown", got.GPU)
	}
	unapplied := guardIDs(got.Guards, false)
	if !slices.Contains(unapplied, GuardCUDA133Crash) {
		t.Errorf("unapplied guards = %v, want %q among them", unapplied, GuardCUDA133Crash)
	}
	decision := findGuard(t, got.Guards, GuardCUDA133Crash)
	if !strings.Contains(decision.Guidance, "could not determine") {
		t.Errorf("#222 guidance does not say why the substitution was skipped: %q", decision.Guidance)
	}
	if got.PackingReason != PackingReasonDefault {
		t.Errorf("PackingReason = %q, want %q", got.PackingReason, PackingReasonDefault)
	}
}

// TestGuardSubstitutionRefusesAnUnpinnedBackend covers the fail-soft half of
// applyCompatGuards: a guard whose target backend this platform has no archive
// for is RECORDED as unapplied (with the reason in its guidance) instead of
// turning an upstream failure into c0wrk's own ErrArtifactNotPinned.
func TestGuardSubstitutionRefusesAnUnpinnedBackend(t *testing.T) {
	t.Parallel()

	// The guard's target must be pinned for the platform, so the uncovered
	// combination is a table that publishes ROCm and CPU but NOT Vulkan — the
	// shape a platform would have if c0wrk stopped shipping a Vulkan build.
	table := stubAssetTable{
		runtimes: map[string]map[Backend]bool{
			PlatformLinuxAMD64: {BackendROCm: true, BackendCPU: true},
		},
	}
	res, err := resolveProfileWith(table, MachineProfile{
		Platform: PlatformLinuxAMD64, Backend: BackendROCm, GPU: GPUFamilyAMDRDNA2, RAMGiB: 64,
	})
	if err != nil {
		t.Fatalf("resolveProfileWith error = %v, want success", err)
	}
	if res.Backend != BackendROCm {
		t.Errorf("Backend = %q, want the guard NOT to substitute an unpinned Vulkan build", res.Backend)
	}
	decision := findGuard(t, res.Guards, GuardROCmRDNA2Abort)
	if decision.Applied {
		t.Error("an unappliable substitution is marked Applied")
	}
	if !strings.Contains(decision.Guidance, "no pinned") {
		t.Errorf("guidance does not say why the substitution was skipped: %q", decision.Guidance)
	}
}

// ── helpers ──

// guardIDs lists the ids of the decisions whose Applied flag matches.
func guardIDs(decisions []GuardDecision, applied bool) []GuardID {
	ids := make([]GuardID, 0, len(decisions))
	for _, decision := range decisions {
		if decision.Applied == applied {
			ids = append(ids, decision.Guard)
		}
	}
	return ids
}

// findGuard returns the decision for an id, failing the test when absent.
func findGuard(t *testing.T, decisions []GuardDecision, id GuardID) GuardDecision {
	t.Helper()
	for _, decision := range decisions {
		if decision.Guard == id {
			return decision
		}
	}
	t.Fatalf("no %q decision among %v", id, guardIDs(decisions, false))
	return GuardDecision{}
}

// archiveTokenFor is the fragment of a pinned archive name that identifies a
// backend, used to assert the artifact set followed the substituted backend.
func archiveTokenFor(backend Backend) string {
	switch backend {
	case BackendCUDA124:
		return "cuda-12.4"
	case BackendCUDA128:
		return "cuda-12.8"
	case BackendCUDA133:
		return "cuda-13.3"
	case BackendVulkan:
		return "vulkan"
	case BackendROCm:
		return "rocm"
	case BackendMetal:
		return "macos-arm64"
	default:
		return string(backend)
	}
}
