package embeddedllm

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ResolveInput is everything a resolution is derived from: the machine facts
// (`MachineProfile`, sufficient alone as the DERIVED-BUDGET view) plus the two
// memory-aware refinements that turn it into a MEASURED one. See
// specs/domains/embedded-llm.md (Hardware probe → resolution).
type ResolveInput struct {
	// MachineProfile carries the probed machine facts: platform, backend, RAM,
	// GPU generation, host CPU capabilities, any fit verdict.
	MachineProfile
	// Topology is the post-install device-memory probe result. NIL is "not
	// probed" — not the same fact as a zero topology reporting no devices; the
	// distinction decides whether the accelerator budget is unreadable or
	// measured-absent. See specs/domains/embedded-llm.md (Memory plan).
	Topology *MemoryTopology
	// Tuning is the operator override vocabulary (plan.go); its zero value is
	// the all-Auto plan.
	Tuning Tuning
	// Engine selects the runtime family this resolution is for. The ZERO value
	// is the llama family — every pre-existing caller and test keeps its
	// meaning — while the config layer resolves `embedded_llm.engine: auto`
	// through ResolveEngine BEFORE calling Resolve. EngineMTPLX switches the
	// whole flow to the MLX pack (mlxplan.go).
	Engine Engine
}

// Resolution is the pure output of a ResolveInput: everything the installer
// downloads and everything the supervisor passes to llama-server. See
// specs/domains/embedded-llm.md (Hardware probe → resolution).
type Resolution struct {
	// Assets is the ordered install set (runtime, cudart on Windows CUDA,
	// model, mmproj); the order is the download and progress order.
	Assets []Asset
	// Backend is what the artifacts were ACTUALLY resolved for,
	// post-degradation — the value the manifest and the Settings label must
	// record. See specs/domains/embedded-llm.md.
	Backend Backend
	// Packing is the weights quantization to download.
	Packing Packing
	// PackingReason is why that packing was chosen: every non-default value is
	// a recorded degradation, never inferred from a file size. See
	// specs/domains/embedded-llm.md.
	PackingReason PackingReason
	// GPU is the accelerator generation the plan was made for
	// (GPUFamilyUnknown when no probe answered); the packing and the guards
	// read it, and the post-staging refinement must know whether the plan
	// already had one.
	GPU GPUFamily
	// Guards are the compatibility decisions in force (compat.go's table),
	// each with an Applied flag saying whether the plan changed because of it.
	// Empty on a machine no documented failure covers — the common case.
	Guards []GuardDecision
	// Layers is the -ngl value; it mirrors Memory.Layers and reads 0 under a
	// fit-sized plan, so the launcher MUST consult Memory.EmitsLayers(). See
	// specs/domains/embedded-llm.md.
	Layers int
	// ContextSize is the -c value; it mirrors Memory.ContextSize and reads 0
	// under a fit-sized plan. A consumer persisting it (the manifest, and
	// through it the llm.models context_window override) must treat that as
	// "no concrete context to record", not as "context 0". See
	// specs/domains/embedded-llm.md.
	ContextSize int
	// Memory is the resolved launch shape and the arithmetic behind it;
	// Layers, ContextSize and Packing above are projections of it. See
	// specs/domains/embedded-llm.md.
	Memory MemoryPlan
	// ImageMaxTokens is --image-max-tokens: capped on Metal/Vulkan/CPU,
	// uncapped (flag omitted entirely) on CUDA/ROCm. See
	// specs/domains/embedded-llm.md.
	ImageMaxTokens int
	// NeedsCudart reports the paired Windows-CUDA DLL archive, which the
	// installer surfaces as its own component.
	NeedsCudart bool
	// Engine is the provisioned runtime family; the manifest and the Settings
	// label record it next to Backend, which under the mtplx engine stays
	// BackendMetal (the accelerator MLX actually runs on) while Engine carries
	// the difference a reader needs first.
	Engine Engine
}

// Launch-flag policy: every value mirrors the pinned fork's demo scripts
// (scripts/common.sh), so c0wrk provisions and launches what upstream would
// have on the same machine.
const (
	// nglAllGPU offloads every layer to the GPU.
	nglAllGPU = 99
	// nglCPUOnly keeps the model in system RAM.
	nglCPUOnly = 0

	// imageMaxTokensCapped bounds large images (~4000 vision tokens for a
	// 12 MP photo) to keep prefill affordable on Metal/Vulkan/CPU.
	imageMaxTokensCapped = 1024
	// ImageMaxTokensUncapped means "do not pass --image-max-tokens at all".
	// CUDA and ROCm run uncapped, and an explicit 0 override means the same.
	ImageMaxTokensUncapped = 0

	// contextTierTop is the largest context the resolver emits, reached only
	// above 71 GiB of RAM; the model's ~2x training context is deliberately
	// unreachable — memory-unaware, and an OOM on constrained machines.
	contextTierTop = 131072
)

// ErrInsufficientRAM is deprecated: ADR-067 D6's flat 16 GiB floor is gone,
// and the combined gate prices both pools instead. Kept so a caller matching
// only the host pool still works: every *InsufficientMemoryError whose HOST
// budget overflowed unwraps to it. See specs/domains/embedded-llm.md.
var ErrInsufficientRAM = errors.New("insufficient system RAM for the embedded LLM")

// cudaTagsNewestFirst is the pinned CUDA asset tags, newest first; Resolve
// clamps DOWN it when the probed tag has no archive for the platform — an
// older-toolkit binary runs on a newer driver, never the reverse. See
// specs/domains/embedded-llm.md.
var cudaTagsNewestFirst = []Backend{BackendCUDA133, BackendCUDA128, BackendCUDA124}

// AssetTable is the artifact-registry seam Resolve consults: production
// delegates to the compile-time pins in registry.go, tests substitute a fake.
// The registry is a table of constants — resolution performs no I/O either
// way. See specs/domains/embedded-llm.md.
type AssetTable interface {
	// RuntimeAsset reports whether a runtime archive is pinned for the
	// platform+backend pair — whether a probed backend is provisionable.
	RuntimeAsset(platform string, backend Backend) (Asset, bool)
	// ArtifactSet returns the complete ordered install set, or an error
	// wrapping ErrArtifactNotPinned — never a partial set.
	ArtifactSet(platform string, backend Backend, packing Packing) ([]Asset, error)
}

// assetTable is the pinned-registry default behind the exported Resolve;
// tests drive fail-closed branches through resolveWith without touching it.
var assetTable AssetTable = pinnedRegistry{}

// pinnedRegistry adapts the compile-time registry functions to AssetTable.
type pinnedRegistry struct{}

func (pinnedRegistry) RuntimeAsset(platform string, backend Backend) (Asset, bool) {
	return RuntimeAsset(platform, backend)
}

func (pinnedRegistry) ArtifactSet(platform string, backend Backend, packing Packing) ([]Asset, error) {
	return ArtifactSet(platform, backend, packing)
}

// Resolve derives the complete install and launch plan for a machine. It is a
// PURE function — no I/O, no probing, no clock — so the whole platform x
// backend x RAM x topology x tuning matrix is table-testable. Its refusal
// order and its degrade-never-refuse backend rule are specified in
// specs/domains/embedded-llm.md (Hardware probe → resolution).
func Resolve(in ResolveInput) (Resolution, error) {
	return resolveWith(assetTable, in)
}

// ResolveMachine is the three-input convenience form of Resolve: GPU
// generation, host capabilities, measured fit, topology and tuning all read as
// unset. Callers that can answer more use ResolveProfile or Resolve.
func ResolveMachine(platform string, backend Backend, ramGiB float64) (Resolution, error) {
	return ResolveProfile(MachineProfile{Platform: platform, Backend: backend, RAMGiB: ramGiB})
}

// MachineProfile is everything c0wrk can know about a machine when it plans an
// install. Only the first three fields are mandatory; the rest are refinements
// whose zero values mean "not measured" and are handled conservatively.
type MachineProfile struct {
	// Platform is the "<goos>-<goarch>" key (Hardware.Platform).
	Platform string
	// Backend is the PROBED accelerator (Hardware.Backend), before the
	// degradation rules and the compatibility guards run.
	Backend Backend
	// RAMGiB is total system RAM in GiB (Hardware.RAMGiB).
	RAMGiB float64
	// GPU is the accelerator generation, from ClassifyGPU over a device probe;
	// GPUFamilyUnknown keeps the default packing and fires no GPU-specific
	// guard.
	GPU GPUFamily
	// Host carries the CPU capabilities the packing decision reads; the zero
	// value means "no CPU-feature probe answered".
	Host HostCaps
	// FitsPQ2_0 is the verdict of the device-memory fit gate. FitUnknown — the
	// zero value — means capacity was not measured and must not justify a
	// downgrade: the gate that projects the footprint against the probed
	// budget (memory.go over topology.go) owns the measurement.
	FitsPQ2_0 BudgetFit
	// CUDA12Userland is the probe verdict on the CUDA 12.x load-time libraries
	// (libcudart.so.12 + libcublas.so.12). It gates the Linux half of the #222
	// guard: present fires the substitution, absent and unknown both keep the
	// probed 13.3 backend and record the guard unapplied — the zero value is
	// unknown, the conservative side. Windows and every non-#222 guard are
	// decided without the verdict (Windows bundles its CUDA runtime in the
	// cudart companion archive). See specs/domains/embedded-llm.md.
	CUDA12Userland CUDA12Userland
}

// ResolveProfile is Resolve for a caller that knows the machine but has no
// device-memory topology to gate against and no tuning to apply.
func ResolveProfile(profile MachineProfile) (Resolution, error) {
	return Resolve(ResolveInput{MachineProfile: profile})
}

// resolveProfileWith is resolveWith for a profile-only input.
func resolveProfileWith(table AssetTable, profile MachineProfile) (Resolution, error) {
	return resolveWith(table, ResolveInput{MachineProfile: profile})
}

// resolveWith is Resolve against an explicit registry: the exported wrapper
// pins the compile-time table, tests pass a stub to drive the fail-closed
// branches without touching package state.
func resolveWith(table AssetTable, in ResolveInput) (Resolution, error) {
	// Total system RAM is a HARD input, and the check is first — unknown is
	// not "big enough". See specs/domains/embedded-llm.md.
	if in.RAMGiB <= 0 && (in.Topology == nil || in.Topology.HostRAMGiB <= 0) {
		return Resolution{}, fmt.Errorf("%w: the memory gate needs a total to derive a host budget from",
			ErrRAMUnknown)
	}

	// The mtplx engine branches here, before any llama machinery runs: its
	// artifact set, planner and gate are all different (mlxplan.go), and every
	// llama-side rule below is deliberately not consulted on this path.
	if in.Engine == EngineMTPLX {
		return resolveMLX(table, in)
	}

	effective := effectiveBackend(table, in.Platform, in.Backend)

	// The GPU generation is classified from the probed inventory when the
	// caller did not supply one, BEFORE the compatibility guards run: the
	// guards are about the silicon that answered the probe.
	gpu := in.GPU
	if gpu == GPUFamilyUnknown && in.Topology != nil {
		gpu = ClassifyGPUs(in.Topology.Devices)
	}
	effective, guards := applyCompatGuards(table, in.Platform, effective, gpu, in.CUDA12Userland)

	// Normalize ONCE, here, so the gate and the planner price the same machine.
	// `Plan` normalizes too, but it has no RAM total to fall back on.
	if in.Topology != nil {
		normalized := normalizeTopology(*in.Topology, in.RAMGiB)
		in.Topology = &normalized
	}

	// THE GATE — before a single asset is planned, and so before any caller
	// can create a directory or fetch a byte: the combined two-pool gate that
	// replaced ADR-067 D6's flat RAM floor. See specs/domains/embedded-llm.md
	// (The combined memory gate).
	profile, err := PinnedMemoryProfile()
	if err != nil {
		return Resolution{}, fmt.Errorf("embeddedllm: memory profile: %w", err)
	}
	gate, err := memoryGate(in, effective, gpu, profile)
	if err != nil {
		return Resolution{}, err
	}

	// The gate OWNS the fit measurement, so it supplies the verdict
	// `decidePacking`'s rule 3 is waiting for (`MachineProfile.FitsPQ2_0`):
	// without this a machine the gate only admitted at PTQ1_0 would download
	// PQ2_0 with nothing left to downgrade to. Applied only when it CHANGES
	// the answer, so a more specific PTQ1_0 trigger — Vulkan's missing
	// kernels, an AVX-512 host on a pre-#245 pin, an Ada card — keeps its own
	// reason instead of being relabelled as a capacity finding.
	if in.Tuning.Packing == "" && in.FitsPQ2_0 != FitInsufficient &&
		gate.viable.packing == PackingPTQ1_0 &&
		packingFor(effective, gpu, in.FitsPQ2_0, in.Host) == PackingPQ2_0 {
		in.FitsPQ2_0 = FitInsufficient
	}

	memory, decision, err := planMemory(in, effective, gpu, profile, gate)
	if err != nil {
		return Resolution{}, err
	}
	memory.Notes = append(gate.notes, memory.Notes...)

	assets, err := table.ArtifactSet(in.Platform, effective, memory.Packing)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolving artifacts for %s/%s: %w",
			in.Platform, effective, err)
	}

	return Resolution{
		Assets:         assets,
		Backend:        effective,
		Packing:        memory.Packing,
		PackingReason:  decision.Reason,
		GPU:            gpu,
		Guards:         guards,
		Layers:         deref(memory.Layers),
		ContextSize:    memory.ContextSize,
		ImageMaxTokens: imageMaxTokensFor(effective),
		NeedsCudart:    hasCudart(assets),
		Memory:         memory,
	}, nil
}

// resolveMLX is the mtplx engine's resolution path: the pinned engine stack
// (managed CPython + the closed wheel set) plus the pinned MLX pack, planned
// by PlanMLX against the measured single-pool budget. The AssetTable seam is
// not consulted — the engine tables are this package's own mlxpins.go — and
// the compatibility guards do not run: compat.go's table is derived from the
// pinned llama.cpp fork's KNOWN_ISSUES, none of which describes an MLX
// failure, so guards on an mtplx resolution would be a transcription error.
func resolveMLX(_ AssetTable, in ResolveInput) (Resolution, error) {
	if in.Platform != PlatformDarwinARM64 {
		return Resolution{}, fmt.Errorf("%w: engine %q needs %q, got %q (use engine %q)",
			ErrEngineUnsupportedPlatform, EngineMTPLX, PlatformDarwinARM64, in.Platform, EngineLlama)
	}
	terms, err := PinnedMLXMemoryTerms()
	if err != nil {
		return Resolution{}, err
	}
	plan, err := PlanMLX(in.RAMGiB, in.Tuning, terms)
	if err != nil {
		return Resolution{}, err
	}
	assets, err := EngineArtifactSet(in.Platform)
	if err != nil {
		return Resolution{}, err
	}
	return Resolution{
		Engine:        EngineMTPLX,
		Assets:        assets,
		Backend:       BackendMetal, // the accelerator MLX runs on; Engine carries the rest
		PackingReason: PackingReasonMLXPack,
		GPU:           GPUFamilyAppleSilicon,
		ContextSize:   plan.ContextSize,
		NeedsCudart:   false,
		Memory:        plan,
	}, nil
}

// planMemory resolves the launch shape and the packing decision TOGETHER,
// because the two are mutually dependent: the packing chooses the footprint,
// and a footprint that does not fit is precisely the measurement
// `decidePacking`'s rule 3 is waiting for. Resolution closes that loop with
// ONE bounded retry rather than a fixed point — plan the derived packing, and
// if the measured budgets refuse it, re-decide the packing with that refusal
// as the fit verdict and plan once more; a second refusal is final, PTQ1_0
// being the smallest packing this model ships. This is what makes ADR-066
// D2's "PTQ1_0 whenever memory is short" a measurement instead of a heuristic
// (that memory clause superseded by the measured gate, ADR-067 D6). An
// explicit `Tuning.Packing` short-circuits the loop: a budget that cannot
// serve it is reported as infeasible, never answered with a different packing.
func planMemory(in ResolveInput, effective Backend, gpu GPUFamily, profile ModelMemoryProfile, gate gateVerdict) (MemoryPlan, PackingDecision, error) {
	if in.Tuning.Packing != "" {
		decision := PackingDecision{Packing: in.Tuning.Packing, Reason: PackingReasonOperatorOverride}
		plan, err := planOnce(in, effective, gpu, decision, profile, gate)
		return plan, decision, err
	}

	decision := packingDecisionFor(effective, gpu, in.FitsPQ2_0, in.Host)
	plan, err := planOnce(in, effective, gpu, decision, profile, gate)
	switch {
	case err == nil:
		return plan, decision, nil
	case !errors.Is(err, ErrMemoryPlanInfeasible):
		return MemoryPlan{}, decision, err
	case decision.Packing != PackingPQ2_0 || in.FitsPQ2_0 == FitInsufficient:
		// Already the smallest packing, or already measured as not fitting.
		return MemoryPlan{}, decision, err
	}

	downgraded := packingDecisionFor(effective, gpu, FitInsufficient, in.Host)
	if downgraded.Packing == decision.Packing {
		return MemoryPlan{}, decision, err
	}
	retried, retryErr := planOnce(in, effective, gpu, downgraded, profile, gate)
	if retryErr != nil {
		return MemoryPlan{}, downgraded, retryErr
	}
	return retried, downgraded, nil
}

// packingDecisionFor is decidePacking with the pin's runtime build filled in.
func packingDecisionFor(backend Backend, gpu GPUFamily, fit BudgetFit, host HostCaps) PackingDecision {
	return decidePacking(PackingInput{
		Backend:      backend,
		GPU:          gpu,
		FitsPQ2_0:    fit,
		Host:         host,
		RuntimeBuild: pinnedRuntimeBuild(),
	})
}

// planOnce runs one planning pass for an already-decided packing.
func planOnce(in ResolveInput, effective Backend, gpu GPUFamily, decision PackingDecision, profile ModelMemoryProfile, gate gateVerdict) (MemoryPlan, error) {
	tuning := in.Tuning
	if tuning.Packing == "" {
		tuning.Packing = decision.Packing
	}
	if in.Topology == nil {
		return ramOnlyPlan(in, effective, gpu, tuning, gate)
	}
	return Plan(*in.Topology, profile, tuning, effective, gpu)
}

// ramOnlyPlan is the fallback for a caller that never ran the device probe:
// the RAM-tiered context, the platform+backend layer policy, and no
// feasibility gate. It deliberately does NOT hand sizing to the runtime's fit
// pass, even though fit needs no topology of c0wrk's: a fit-sized plan has no
// concrete context, and the resolved context is what the manifest records and
// what becomes the `llm.models` `context_window` override — deterministically,
// at config-read time, when the server is not running and cannot be asked. A
// RAM tier is a worse estimate than a measured budget but better than nothing.
// Explicit overrides are still honoured; the plan says in its Notes what is
// missing.
func ramOnlyPlan(in ResolveInput, effective Backend, gpu GPUFamily, tuning Tuning, gate gateVerdict) (MemoryPlan, error) {
	packing := tuning.Packing
	if packing == "" {
		packing = packingFor(effective, gpu, in.FitsPQ2_0, in.Host)
	}
	kv := KVTypeF16
	if tuning.KVType != "" {
		kv = tuning.KVType
	}

	layers := layersFor(in.Platform, effective)
	if tuning.Offload.explicit() {
		layers = explicitLayers(tuning.Offload)
	}

	contextSize := contextSizeFor(in.RAMGiB)
	if tuning.Context.Mode == ContextExact {
		if tuning.Context.Tokens <= 0 {
			return MemoryPlan{}, fmt.Errorf("%w: an exact context of %d tokens is not positive",
				ErrTuningInvalid, tuning.Context.Tokens)
		}
		contextSize = tuning.Context.Tokens
	}

	// The defaults above are PLATFORM-derived, not memory-derived. The gate
	// admitted this machine because SOME modelled shape fits; a plan that then
	// emits a different one has thrown the measurement away — exactly the
	// at-load failure the gate exists to prevent. So offload and KV yield to
	// the gate's verdict wherever the operator did not pin them.
	var degradeNotes []string
	if !tuning.Offload.explicit() && !gate.viable.offloaded && layers != nglCPUOnly {
		layers = nglCPUOnly
		degradeNotes = append(degradeNotes, fmt.Sprintf(
			"the derived %d GiB host budget cannot serve a device-resident launch at this platform's default "+
				"offload, so nothing is offloaded (-ngl 0) and the model runs entirely from system RAM",
			gate.budgets.hostMiB/1024))
	}
	if tuning.KVType == "" && kv != gate.viable.kv {
		degradeNotes = append(degradeNotes, fmt.Sprintf(
			"the KV cache was escalated to %s: the derived budget does not serve a %d-token f16 cache",
			gate.viable.kv, contextSize))
		kv = gate.viable.kv
	}

	parallel := DefaultParallel
	if tuning.Parallel != nil && *tuning.Parallel > 0 {
		parallel = *tuning.Parallel
	}
	fitMinContext := DefaultFitMinContext
	if tuning.FitMinContext != nil && *tuning.FitMinContext > 0 {
		fitMinContext = *tuning.FitMinContext
	}
	// The two checkpoint knobs resolve exactly as planShape resolves them, so
	// the fallback and a measured plan render the same flags; an unusable
	// figure is refused, not quietly defaulted.
	ctxCheckpoints, err := planCtxCheckpoints(tuning)
	if err != nil {
		return MemoryPlan{}, err
	}

	plan := MemoryPlan{
		Fit:            false,
		FitMinContext:  fitMinContext,
		Layers:         ptrInt(layers),
		ContextSize:    contextSize,
		KVType:         kv,
		Packing:        packing,
		KVOffload:      tuning.KVOffload == nil || *tuning.KVOffload,
		MMProjOffload:  tuning.MMProjOffload == nil || *tuning.MMProjOffload,
		Parallel:       parallel,
		CtxCheckpoints: ctxCheckpoints,
		CacheIdleSlots: tuning.CacheIdleSlots == nil || *tuning.CacheIdleSlots,
		Devices:        slices.Clone(tuning.Devices),
		SplitMode:      tuning.SplitMode,
		GPUFamily:      gpu,

		// The gate's (DERIVED, not measured) budgets, so the plan reports the
		// capacity it was actually weighed against rather than two zeroes.
		DeviceBudgetMiB: gate.budgets.deviceMiB,
		HostBudgetMiB:   gate.budgets.hostMiB,

		Notes: []string{fmt.Sprintf(
			"no device-memory topology was probed, so this plan is the RAM-tiered fallback (%.0f GiB of system RAM): "+
				"-ngl %d and a %d-token context, gated against budgets DERIVED from the RAM probe rather than "+
				"measured on the accelerator",
			in.RAMGiB, layers, contextSize)},
	}
	plan.Notes = append(plan.Notes, degradeNotes...)
	if tuning.CacheRAMMiB != nil {
		value := *tuning.CacheRAMMiB
		plan.CacheRAMMiB = &value
	}
	return plan, nil
}

// explicitLayers renders an explicit offload override as the -ngl value it
// asks for, clamped to the range the flag accepts; any value at or above the
// model's offloadable layer count means "all", so the clamp cannot lose an
// offload the operator asked for.
func explicitLayers(offload Offload) int {
	switch offload.Mode {
	case OffloadAll:
		return nglAllGPU
	case OffloadLayers:
		return min(max(offload.Layers, nglCPUOnly), nglAllGPU)
	case OffloadCPU:
		return nglCPUOnly
	case OffloadAuto:
	default:
		return nglCPUOnly
	}
	// OffloadAuto is the only mode that falls out of the switch, and it never
	// reaches this function: Offload.explicit() gates the call, and an Auto
	// offload is sized by planLayers rather than rendered here. The value is a
	// totality fallback, not a policy.
	return nglCPUOnly
}

// applyCompatGuards folds the guard table's backend substitutions into an
// already-degraded backend and sets the decisions' Applied flags — including
// on the ones that could NOT be applied, which stay in the record as guidance.
// A substitution must be pinned for the platform (an unpinned swap would turn
// a documented upstream failure into c0wrk's own ErrArtifactNotPinned), and
// the Linux #222 substitution additionally requires the probed CUDA12Userland
// verdict to be present — absent or unknown both keep the probed backend and
// record the decision unapplied. Packing is not folded here: decidePacking
// reads the substituted backend directly, so the packing follows the backend
// the plan ends up with. See specs/domains/embedded-llm.md (Backend
// compatibility guards).
func applyCompatGuards(table AssetTable, platform string, backend Backend, gpu GPUFamily, cu12 CUDA12Userland) (Backend, []GuardDecision) {
	decisions := CompatibilityGuards(backend, gpu, platform)
	effective := backend
	for i := range decisions {
		decision := &decisions[i]
		if decision.Action != GuardActionPreferBackend {
			continue
		}
		if platform == PlatformLinuxAMD64 && decision.Guard == GuardCUDA133Crash && cu12 != CUDA12Present {
			decision.Applied = false
			switch cu12 {
			case CUDA12Absent:
				decision.Guidance += " (c0wrk probed this system and found no usable CUDA 12.x " +
					"runtime libraries, so the " + string(decision.Backend) +
					" build could not be loaded here; this install keeps " + string(effective) +
					"; install the CUDA 12.x runtime libraries (e.g. via the nvidia driver's " +
					"cuda-12 package) and reinstall the runtime to get the " +
					string(decision.Backend) + " build)"
			default: // CUDA12Unknown — the zero value: not probed, or the probe could not decide.
				decision.Guidance += " (c0wrk could not determine whether this system has the CUDA 12.x " +
					"runtime libraries the " + string(decision.Backend) +
					" build needs; this install keeps " + string(effective) +
					"; install the CUDA 12.x runtime libraries and reinstall the runtime " +
					"to get the " + string(decision.Backend) + " build)"
			}
			continue
		}
		if _, pinned := table.RuntimeAsset(platform, decision.Backend); !pinned {
			decision.Applied = false
			decision.Guidance += " (c0wrk has no pinned " + string(decision.Backend) +
				" runtime for " + platform + ", so this install keeps " + string(effective) + ")"
			continue
		}
		decision.Applied = true
		effective = decision.Backend
	}
	return effective, decisions
}

// effectiveBackend maps the PROBED backend onto the backend this machine can
// actually be provisioned with: Metal only on Apple Silicon, x64-only CUDA/ROCm
// archives elsewhere, CUDA tags clamped DOWN to the nearest older pinned tag,
// and the CPU build — which every supported platform pins — as the universal
// fallback. See specs/domains/embedded-llm.md.
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

// clampCUDABackend returns the newest pinned CUDA tag not newer than the
// probed one, or BackendCPU when the platform has no CUDA archive at all.
// Clamping only ever goes DOWN: an older-toolkit binary runs on a newer
// driver, a newer-toolkit binary does not load on an older driver. See
// specs/domains/embedded-llm.md.
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

// PackingReason is the typed cause of a packing choice: every non-default
// value records why the smaller, lower-precision PTQ1_0 was taken over PQ2_0,
// persisted to the manifest and mirrored into the Settings status DTO so a
// downgrade is legible instead of inferable from a file size. See
// specs/domains/embedded-llm.md.
type PackingReason string

const (
	// PackingReasonDefault is PQ2_0 with nothing against it.
	PackingReasonDefault PackingReason = "default"

	// PackingReasonNoPQ2_0Kernels is Vulkan, the one backend the fork ships
	// without PQ2_0 (group-128) kernels — a capability fact, not a trade-off;
	// PQ2_0 there would run dequantized (#238: silently on the CPU, "under 2
	// tokens per second").
	PackingReasonNoPQ2_0Kernels PackingReason = "no_pq2_0_kernels"

	// PackingReasonAVX512PQ2_0Segfault is an AVX-512 host on a pin predating
	// PR #245: PQ2_0 segfaults at load even with all layers offloaded
	// (reports #180, #204, #219, Bonsai-demo #182).
	PackingReasonAVX512PQ2_0Segfault PackingReason = "avx512_pq2_0_segfault"

	// PackingReasonPQ2_0DoesNotFit is a measured device budget PQ2_0 exceeds;
	// the model card names PTQ1_0 "the pick wherever memory is tightest"
	// (5.95 GB against 7.21 GB).
	PackingReasonPQ2_0DoesNotFit PackingReason = "pq2_0_does_not_fit"

	// PackingReasonGPUGenerationDecode is a generation the model card measures
	// as PTQ1_0-faster for DECODE (the Ada rows: 6000 Ada 90.4 vs 82.8 TG128,
	// 4090 91.1 vs 81.2, L40S 81.8 vs 74.4, L4 32.1 vs 29.8). Prompt
	// processing favours PQ2_0 everywhere, so this is a recorded trade — an
	// agent workload is decode-dominated, the side this rule optimizes.
	PackingReasonGPUGenerationDecode PackingReason = "gpu_generation_decode"

	// PackingReasonOperatorOverride is an explicit `Tuning.Packing`: the table
	// is not consulted for the value, but the memory gate must still be able
	// to project it (unmeasured is refused, never estimated).
	PackingReasonOperatorOverride PackingReason = "operator_override"

	// PackingReasonMLXPack is the mtplx engine: one quantization ships, so the
	// reason records that absence rather than a zero Packing (ADR-092).
	PackingReasonMLXPack PackingReason = "mlx_pack"
)

// PackingInput is the packing decision's whole input — a struct, not a
// parameter list, so a sixth axis cannot silently reorder anyone's arguments;
// decidePacking is pure over it, which is what makes the whole matrix
// table-testable without a machine.
type PackingInput struct {
	// Backend is the EFFECTIVE backend — the build whose kernels will run.
	Backend Backend
	// GPU is the accelerator generation, GPUFamilyUnknown when unmeasured.
	GPU GPUFamily
	// FitsPQ2_0 is the fit gate's verdict, FitUnknown when unmeasured.
	FitsPQ2_0 BudgetFit
	// Host is the CPU capability set; the zero value means "not probed".
	Host HostCaps
	// RuntimeBuild is the pinned fork build, or 0 when the tag is not
	// recognized: zero compares below every fix threshold, i.e. an
	// unrecognizable pin is treated as unfixed.
	RuntimeBuild int
}

// PackingDecision is the chosen packing plus the single highest-precedence
// reason for it.
type PackingDecision struct {
	Packing Packing
	Reason  PackingReason
}

// decidePacking is the packing table: PTQ1_0 when ANY of four triggers fires,
// reported at the highest-precedence one that did — Vulkan's missing kernels,
// an AVX-512 host on a pre-#245 pin, a measured budget PQ2_0 does not fit, a
// generation that decodes PTQ1_0 faster. There is deliberately no RAM-based
// downgrade: RAM is not the constraint PQ2_0 can exceed, the accelerator
// budget is. See specs/domains/embedded-llm.md.
func decidePacking(in PackingInput) PackingDecision {
	switch {
	case in.Backend == BackendVulkan:
		return PackingDecision{Packing: PackingPTQ1_0, Reason: PackingReasonNoPQ2_0Kernels}

	case in.Host.hasAVX512() && in.RuntimeBuild < minBuildWithAVX512PQ2_0Fix:
		return PackingDecision{Packing: PackingPTQ1_0, Reason: PackingReasonAVX512PQ2_0Segfault}

	case in.FitsPQ2_0 == FitInsufficient:
		return PackingDecision{Packing: PackingPTQ1_0, Reason: PackingReasonPQ2_0DoesNotFit}

	case in.GPU.prefersPTQ1_0Decode():
		return PackingDecision{Packing: PackingPTQ1_0, Reason: PackingReasonGPUGenerationDecode}

	default:
		return PackingDecision{Packing: PackingPQ2_0, Reason: PackingReasonDefault}
	}
}

// packingFor is decidePacking with the pin's build number filled in — the
// resolver's form, whose four inputs are the four things upstream documents a
// packing decision for. No RAM-based downgrade, by design. See
// specs/domains/embedded-llm.md.
func packingFor(backend Backend, gpu GPUFamily, fitsPQ2_0 BudgetFit, host HostCaps) Packing {
	return decidePacking(PackingInput{
		Backend:      backend,
		GPU:          gpu,
		FitsPQ2_0:    fitsPQ2_0,
		Host:         host,
		RuntimeBuild: pinnedRuntimeBuild(),
	}).Packing
}

// layersFor returns the -ngl value, the platform deciding first exactly as the
// fork demo's bonsai_llama_ngl does. An Intel Mac gets no offload; Apple
// Silicon always offloads every layer — INCLUDING when the effective backend
// came out as CPU, because the macOS arm64 archive IS the Metal build, so a
// CPU-labelled resolution on Apple Silicon still runs on the GPU. Everywhere
// else the backend decides.
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

// imageMaxTokensFor returns the --image-max-tokens value: CUDA and ROCm run
// uncapped (fast datacenter GPUs absorb the vision prefill), everything else
// caps large images to keep latency reasonable.
func imageMaxTokensFor(backend Backend) int {
	if backend.IsCUDA() || backend == BackendROCm {
		return ImageMaxTokensUncapped
	}
	return imageMaxTokensCapped
}

// contextSizeFor returns the -c value for a RAM size in GiB: never left
// unspecified, and never the model's full training context — that request is
// memory-unaware and OOMs a constrained machine once -ngl offloads the KV
// cache. ramGiB is floored to a whole GiB before comparison, matching the
// demo script's integer arithmetic: a machine advertised as 24 GB reads
// ~23.x GiB on Linux and stays in the 16384 tier, and flooring never grants a
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

// hasCudart reports the paired CUDA runtime DLL archive, derived from the set
// itself rather than recomputed from platform and backend, so the flag can
// never disagree with what is downloaded.
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
