package embeddedllm

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ResolveInput is everything a resolution is derived from: the machine facts
// (`MachineProfile`, which is sufficient on its own) plus the two memory-aware
// refinements the planner needs.
//
// An input carrying only the embedded profile is the DERIVED-BUDGET view: the
// gate still runs, but it prices the host pool from the RAM probe and classifies
// the accelerator axis statically, because nothing measured it (see
// gateBudgetsFor and deviceAxisState). It keeps the RAM-tiered context ladder
// rather than delegating to the runtime's fit pass, so the plan still carries a
// concrete context to persist.
//
// `Topology` is what turns the resolution into a MEASURED one. When it is present
// the gate and the planner spend the machine's real device and host budgets,
// which is the whole point of the probe — an 8 GiB laptop with a 24 GiB card and
// a 128 GiB unified machine stop being ranked by a number that describes neither
// of them.
type ResolveInput struct {
	// MachineProfile carries Platform, Backend, RAMGiB, the GPU generation, the
	// host CPU capabilities and any fit verdict the caller already has.
	MachineProfile
	// Topology is the post-install device-memory probe result. NIL means "not
	// probed", which is not the same as a topology reporting no devices: the
	// first is an UNREADABLE accelerator budget (the gate degrades to the host
	// pool and says so), the second is a measured answer that there is nothing
	// to offload to (the gate prices the host-resident shape and refuses a host
	// pool too small for it). Passing the zero MemoryTopology would report the
	// second fact about a machine that only answered the first.
	Topology *MemoryTopology
	// Tuning is the operator override vocabulary (plan.go). Its zero value is
	// the all-Auto plan: the runtime's fit pass sizes layers and context, the
	// KV precision escalates to whatever fits, and one slot is served.
	Tuning Tuning
}

// Resolution is the pure output of a ResolveInput: everything the installer
// downloads and everything the supervisor passes to llama-server.
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
	// PackingReason is WHY that packing was chosen. It is recorded because
	// every non-default value is a degradation of some kind — a backend with no
	// PQ2_0 kernels, a pin that predates an AVX-512 fix, a GPU generation that
	// decodes PTQ1_0 faster, or a budget PQ2_0 does not fit — and a degraded
	// install must state its reason rather than leave the user to infer it from
	// a file size. PackingReasonDefault means nothing fired.
	PackingReason PackingReason
	// GPU is the accelerator generation the plan was made for, or
	// GPUFamilyUnknown when no device probe answered. It is part of the
	// resolution because the packing and the compatibility guards both read it,
	// and because an installer that learns it only after staging a runtime needs
	// to know whether the plan already had it.
	GPU GPUFamily
	// Guards are the backend compatibility decisions in force for this machine
	// (compat.go's table, derived from the pinned model's KNOWN_ISSUES), each
	// with its typed reason, upstream issue citation and an Applied flag saying
	// whether the plan actually changed because of it. Empty on a machine no
	// documented failure covers — the common case.
	Guards []GuardDecision
	// Layers is the -ngl value: 0 on an Intel Mac and on the CPU build,
	// nglAllGPU on every GPU-backed build.
	//
	// It mirrors Memory.Layers, and under a fit-sized plan (Memory.Fit) the
	// runtime chooses the layer count itself, so this field reads 0 and the
	// launcher MUST consult Memory.EmitsLayers() rather than passing it.
	Layers int
	// ContextSize is the -c value: RAM-tiered, always positive, and never the
	// model's full training context (which is memory-unaware and OOMs a
	// constrained machine once -ngl offloads the KV cache).
	//
	// It mirrors Memory.ContextSize, and under a fit-sized plan it is 0 — the
	// runtime sizes the context and Memory.FitMinContext is the floor it is
	// held to. A consumer that persists this value (the manifest, and through
	// it the llm.models context_window override) must therefore treat a
	// fit-sized plan as "no concrete context to record", not as "context 0".
	ContextSize int
	// Memory is the resolved launch shape and the arithmetic behind it: the
	// fit-exclusivity decision, the KV precision (possibly escalated), the
	// projected device and host footprints, the budgets they were gated
	// against, and the human-readable Notes. Layers, ContextSize and Packing
	// above are projections of it, kept for the callers that predate the
	// memory model.
	Memory MemoryPlan
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

// ErrInsufficientRAM is the typed refusal of ADR-065 D6's flat 16 GiB floor.
//
// Deprecated: the floor is gone. The gate that replaced it prices BOTH memory
// pools (see ErrInsufficientMemory and memoryGate in plan.go), because a floor
// keyed on system RAM alone refused an 8 GiB laptop with a 32 GiB accelerator
// and admitted a 32 GiB machine with an 8 GiB one. This sentinel is kept so a
// caller that only cares about the host pool can still match it: every
// *InsufficientMemoryError whose HOST budget overflowed unwraps to it, and one
// whose accelerator overflowed does not.
var ErrInsufficientRAM = errors.New("insufficient system RAM for the embedded LLM")

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
// platform x backend x RAM x topology x tuning matrix table-testable.
//
// Refusals, in order:
//
//   - an unreadable total system RAM -> ErrRAMUnknown (fail-closed: a capacity
//     gate must never pass on a guess)
//   - an override the planner cannot honour -> ErrTuningInvalid
//   - a packing with no measured residency -> ErrMemoryNotMeasured
//   - a machine whose memory no modelled shape fits -> ErrInsufficientMemory,
//     BEFORE any asset is planned. This is the combined, unified-aware gate
//     that replaced ADR-065 D6's flat RAM floor; see memoryGate.
//   - no pinned artifact for the platform at all -> ErrArtifactNotPinned
//
// A probed backend that this pin cannot serve is NOT a refusal: Resolve
// degrades to the best build the platform actually has (see effectiveBackend),
// because a working CPU install beats an actionable error on a machine that
// could have run the model.
func Resolve(in ResolveInput) (Resolution, error) {
	return resolveWith(assetTable, in)
}

// ResolveMachine is the three-input convenience form of Resolve: the machine's
// GPU generation, its CPU capabilities, its measured memory fit, its device
// topology and every tuning override all read as unset, so the plan carries no
// GPU-generation packing preference, only the statically decidable
// compatibility guards, and the derived-budget launch shape. Callers that can
// answer more — an installer with a device probe, a fit gate with a memory
// topology, an operator who pinned a context — use ResolveProfile or Resolve.
func ResolveMachine(platform string, backend Backend, ramGiB float64) (Resolution, error) {
	return ResolveProfile(MachineProfile{Platform: platform, Backend: backend, RAMGiB: ramGiB})
}

// MachineProfile is everything c0wrk can know about a machine when it plans an
// install. Only the first three fields are mandatory; the rest are refinements
// whose zero values mean "not measured" and are handled conservatively (see
// CPUFeature and BudgetFit, whose zero values are deliberately Unknown rather
// than a false assertion).
type MachineProfile struct {
	// Platform is the "<goos>-<goarch>" key (Hardware.Platform).
	Platform string
	// Backend is the PROBED accelerator (Hardware.Backend), before the
	// degradation rules and the compatibility guards run.
	Backend Backend
	// RAMGiB is total system RAM in GiB (Hardware.RAMGiB).
	RAMGiB float64
	// GPU is the accelerator generation, from ClassifyGPU over a device probe.
	// GPUFamilyUnknown keeps the default packing and fires no GPU-specific
	// guard.
	GPU GPUFamily
	// Host carries the CPU capabilities the packing decision reads. The zero
	// value means "no CPU-feature probe answered".
	Host HostCaps
	// FitsPQ2_0 is the verdict of the device-memory fit gate. FitUnknown — the
	// zero value — means capacity was not measured and must not justify a
	// downgrade. Resolution never guesses it: the gate that projects the
	// footprint against the probed budget (memory.go over topology.go) owns the
	// measurement and supplies the verdict.
	FitsPQ2_0 BudgetFit
}

// ResolveProfile is Resolve for a caller that knows the machine but has no
// device-memory topology to gate against and no tuning to apply. It is pure
// like Resolve: no I/O, no probing, no clock — the profile is a value the
// caller already has.
func ResolveProfile(profile MachineProfile) (Resolution, error) {
	return Resolve(ResolveInput{MachineProfile: profile})
}

// resolveProfileWith is resolveWith for a profile-only input.
func resolveProfileWith(table AssetTable, profile MachineProfile) (Resolution, error) {
	return resolveWith(table, ResolveInput{MachineProfile: profile})
}

// resolveWith is Resolve against an explicit registry. The exported wrapper
// pins the compile-time table; tests pass a stub to drive the fail-closed
// branches without touching package state.
func resolveWith(table AssetTable, in ResolveInput) (Resolution, error) {
	// Total system RAM is a HARD input, and the check is first: an unreadable
	// size refuses rather than letting a capacity gate pass on a guess. The
	// probe reports ErrRAMUnknown for it, and so does a caller that passed a
	// profile it never filled in — both are "unknown", and unknown is not
	// "big enough".
	if in.RAMGiB <= 0 && (in.Topology == nil || in.Topology.HostRAMGiB <= 0) {
		return Resolution{}, fmt.Errorf("%w: the memory gate needs a total to derive a host budget from",
			ErrRAMUnknown)
	}

	effective := effectiveBackend(table, in.Platform, in.Backend)

	// The GPU generation is classified from the probed inventory when the
	// caller did not supply one, BEFORE the compatibility guards run: the
	// guards are about the silicon that answered the probe, and a caller that
	// has a topology but no classification should still get them.
	gpu := in.GPU
	if gpu == GPUFamilyUnknown && in.Topology != nil {
		gpu = ClassifyGPUs(in.Topology.Devices)
	}
	effective, guards := applyCompatGuards(table, in.Platform, effective, gpu)

	// Normalize ONCE, here, so the gate and the planner price the same machine.
	// `Plan` normalizes too, but it has no RAM total to fall back on — a
	// topology carrying an inventory and no derived budgets is one a caller
	// assembled, and the profile's RAMGiB is the same figure its HostRAMGiB is
	// documented to carry.
	if in.Topology != nil {
		normalized := normalizeTopology(*in.Topology, in.RAMGiB)
		in.Topology = &normalized
	}

	// THE GATE — before a single asset is planned, and so before any caller can
	// create a directory or fetch a byte. It replaced ADR-065 D6's flat RAM
	// floor: see memoryGate for why measuring one pool was wrong in both
	// directions at once.
	profile, err := PinnedMemoryProfile()
	if err != nil {
		return Resolution{}, fmt.Errorf("embeddedllm: memory profile: %w", err)
	}
	gate, err := memoryGate(in, effective, gpu, profile)
	if err != nil {
		return Resolution{}, err
	}

	// The gate OWNS the fit measurement, so it supplies the verdict
	// `decidePacking`'s rule 3 is waiting for (`MachineProfile.FitsPQ2_0`).
	// Without this the planner picks PQ2_0 for a machine the gate only admitted
	// at PTQ1_0, downloads the larger weights, and then has no packing left to
	// downgrade to. It is applied only when it CHANGES the answer, so a more
	// specific trigger that already selected PTQ1_0 — Vulkan's missing kernels,
	// an AVX-512 host on a pre-#245 pin, an Ada card — keeps its own reason
	// instead of being relabelled as a capacity finding.
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

// planMemory resolves the launch shape and the packing decision TOGETHER,
// because the two are mutually dependent: the packing chooses the footprint,
// and a footprint that does not fit is precisely the measurement
// `decidePacking`'s rule 3 is waiting for (`FitsPQ2_0 == FitInsufficient`).
//
// Resolution closes that loop with ONE bounded retry rather than a fixed point:
// plan the derived packing, and if the measured budgets refuse it, re-decide
// the packing with that refusal as the fit verdict and plan once more. A second
// refusal is final — PTQ1_0 is the smallest packing this model ships, so there
// is nothing left to downgrade to. This is what makes ADR-065 D2's "PTQ1_0 when
// memory is short" a measurement instead of a heuristic.
//
// An explicit `Tuning.Packing` short-circuits the loop: the operator named a
// quantization, so nothing is re-decided, and a budget that cannot serve it is
// reported as infeasible rather than answered with a different packing.
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
		// Already the smallest packing, or the caller had already measured it
		// as not fitting: the refusal stands.
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

// ramOnlyPlan is the fallback for a caller that never ran the device probe: the
// RAM-tiered context, the platform+backend layer policy, and no feasibility
// gate, because there is no measured budget to gate against.
//
// It deliberately does NOT hand sizing to the runtime's fit pass, even though
// `Offload` is Auto and fit needs no topology of c0wrk's to do its job. The
// reason is the consumer, not the runtime: a fit-sized plan has no concrete
// context, and the resolved context is what the manifest records and what
// becomes the `llm.models` `context_window` override — deterministically, at
// config-read time, when the server is not running and cannot be asked. A RAM
// tier is a worse estimate than a measured budget but a better one than
// nothing, so an unmeasured machine keeps the estimate it always had.
//
// Explicit overrides are still honoured; only the gate and the fit delegation
// are missing, and the plan says so in its Notes.
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

	// The defaults above are PLATFORM-derived, not memory-derived: `layersFor`
	// offloads everything on Apple Silicon and on every GPU build, and the KV
	// cache starts at f16. On a machine whose pool cannot serve that shape they
	// are exactly the at-load failure the gate exists to prevent — the gate
	// admitted this machine because SOME modelled shape fits, and a plan that
	// then emits a different one has thrown the measurement away. So both yield
	// to the gate's verdict wherever the operator did not pin them.
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

	plan := MemoryPlan{
		Fit:           false,
		FitMinContext: fitMinContext,
		Layers:        ptrInt(layers),
		ContextSize:   contextSize,
		KVType:        kv,
		Packing:       packing,
		KVOffload:     tuning.KVOffload == nil || *tuning.KVOffload,
		MMProjOffload: tuning.MMProjOffload == nil || *tuning.MMProjOffload,
		Parallel:      parallel,
		Devices:       slices.Clone(tuning.Devices),
		SplitMode:     tuning.SplitMode,
		GPUFamily:     gpu,

		// The gate's budgets, so the plan reports the capacity it was actually
		// weighed against rather than two zeroes. They are DERIVED here, not
		// measured — that is what the Note below says — but a report with no
		// numbers at all invites a reader to assume none were computed.
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

// explicitLayers renders an explicit offload override as the -ngl value it asks
// for, clamped to the range the flag accepts. The runtime treats any value at
// or above the model's offloadable layer count as "all", so clamping here
// cannot lose an offload the operator asked for.
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

// applyCompatGuards folds the compatibility table's backend substitutions into
// an already-degraded backend, and returns the decisions with their Applied
// flags set — including the ones that could NOT be applied, which stay in the
// record as guidance.
//
// Only GuardActionPreferBackend is acted on here, and only when the pin
// actually publishes the substituted backend's runtime archive for this
// platform: a guard that swapped in an unpinned build would turn a documented
// upstream failure into c0wrk's own ErrArtifactNotPinned, which helps nobody.
// Packing substitutions are not folded here either — decidePacking reads the
// same GPU family and the substituted backend directly, so the packing follows
// the backend it ends up with (a ROCm→Vulkan substitution yields PTQ1_0
// because Vulkan has no PQ2_0 kernels, exactly as upstream's workaround for
// #223 prescribes).
func applyCompatGuards(table AssetTable, platform string, backend Backend, gpu GPUFamily) (Backend, []GuardDecision) {
	decisions := CompatibilityGuards(backend, gpu, platform)
	effective := backend
	for i := range decisions {
		decision := &decisions[i]
		if decision.Action != GuardActionPreferBackend {
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

// PackingReason is the typed cause of a packing choice. Every value except
// PackingReasonDefault describes a reason the smaller, lower-precision PTQ1_0
// was chosen over PQ2_0, and each one cites the fact that justifies it — a
// missing kernel, a documented segfault, a measured throughput table or a
// measured budget. It is persisted in the manifest and mirrored into the
// Settings status DTO, so a downgrade is legible to the user instead of
// inferable from a file size.
type PackingReason string

const (
	// PackingReasonDefault is PQ2_0 with nothing against it: the backend has
	// PQ2_0 kernels, the pin is not known to crash this CPU, the GPU generation
	// does not decode PTQ1_0 faster, and no measured budget was exceeded.
	PackingReasonDefault PackingReason = "default"

	// PackingReasonNoPQ2_0Kernels is Vulkan, the one backend the fork ships
	// without PQ2_0 (group-128) kernels. This is a capability fact, not a
	// trade-off: PQ2_0 on Vulkan would run dequantized, and KNOWN_ISSUES #238
	// documents exactly that failure mode as "silently runs on the CPU ... at
	// under 2 tokens per second" while the log still claims full offload.
	PackingReasonNoPQ2_0Kernels PackingReason = "no_pq2_0_kernels"

	// PackingReasonAVX512PQ2_0Segfault is an AVX-512 host on a pin that
	// predates PR #245. KNOWN_ISSUES "CPU crash on load (AVX-512 CPUs)":
	// "PQ2_0 segfaults while loading on CPUs with AVX-512, including AMD Zen 4
	// and Zen 5 ... It crashes even with all layers offloaded to a GPU"
	// (reports #180, #204, #219, Bonsai-demo #182; fixed in source by #245).
	PackingReasonAVX512PQ2_0Segfault PackingReason = "avx512_pq2_0_segfault"

	// PackingReasonPQ2_0DoesNotFit is a measured device budget that PQ2_0
	// exceeds. The model card names PTQ1_0 "the pick wherever memory is
	// tightest": 5.95 GB against PQ2_0's 7.21 GB, 17% less weight data.
	PackingReasonPQ2_0DoesNotFit PackingReason = "pq2_0_does_not_fit"

	// PackingReasonGPUGenerationDecode is an accelerator generation the model
	// card measures as PTQ1_0-faster for DECODE: "PTQ1_0 is the faster decode
	// on the Ada-generation cards and the L4", confirmed by every Ada row of
	// its Cross-Platform Throughput table (RTX 6000 Ada 90.4 vs 82.8 TG128,
	// RTX 4090 91.1 vs 81.2, L40S 81.8 vs 74.4, L4 32.1 vs 29.8). Prompt
	// processing still favours PQ2_0 everywhere, so this is a genuine trade and
	// is recorded as one — an agent workload is decode-dominated, which is the
	// side this rule optimizes.
	PackingReasonGPUGenerationDecode PackingReason = "gpu_generation_decode"

	// PackingReasonOperatorOverride is an explicit `Tuning.Packing`: the
	// operator named a quantization, so the table above is not consulted for
	// the value — though it still is for the record, and the memory gate still
	// has to be able to project the chosen packing (an unmeasured one is
	// refused with ErrMemoryNotMeasured rather than estimated from a file
	// size).
	PackingReasonOperatorOverride PackingReason = "operator_override"
)

// PackingInput is the packing decision's whole input: the backend the plan
// settled on, the accelerator generation, the memory-fit verdict, the host CPU
// capabilities and the build number of the pinned runtime.
//
// It is a struct rather than a parameter list because the decision has five
// axes and grows them slowly; a sixth axis must not silently reorder anyone's
// arguments. decidePacking is pure over it, which is what makes the whole
// matrix table-testable without a machine to test on.
type PackingInput struct {
	// Backend is the EFFECTIVE backend (after degradation and the compatibility
	// guards), since that is the build whose kernels will actually run.
	Backend Backend
	// GPU is the accelerator generation, GPUFamilyUnknown when unmeasured.
	GPU GPUFamily
	// FitsPQ2_0 is the fit gate's verdict, FitUnknown when unmeasured.
	FitsPQ2_0 BudgetFit
	// Host is the CPU capability set; the zero value means "not probed".
	Host HostCaps
	// RuntimeBuild is the pinned fork build (parseRuntimeBuild over RuntimeTag),
	// or 0 when the tag is not recognized. Zero compares below every fix
	// threshold, i.e. an unrecognizable pin is treated as unfixed.
	RuntimeBuild int
}

// PackingDecision is the chosen packing plus the single highest-precedence
// reason for it.
type PackingDecision struct {
	Packing Packing
	Reason  PackingReason
}

// decidePacking is the packing table. PTQ1_0 is chosen when ANY of four
// conditions holds, and the reason reported is the highest-precedence one that
// fired; otherwise PQ2_0, the default.
//
// Precedence, highest first:
//
//  1. Vulkan — no PQ2_0 kernels at all. A capability fact outranks every
//     trade-off, and no other rule can rescue it.
//  2. An AVX-512 host on a pin predating #245 — PQ2_0 SEGFAULTS at load. A
//     crash outranks a capacity or throughput argument because those are
//     degradations and this is a failure. The host verdict is tri-state and
//     Unknown counts as AVX-512-capable: an unprobed machine takes the packing
//     that cannot crash.
//  3. PQ2_0 does not fit the measured budget — capacity outranks throughput,
//     because a packing that does not fit cannot be slow.
//  4. The GPU generation decodes PTQ1_0 faster (Ada and the L4).
//
// Note what is NOT here: a RAM heuristic. Installed RAM never downgrades the
// packing on its own, because RAM is not the constraint that PQ2_0 can exceed
// — the accelerator budget is, and rule 3 is that constraint as measured by
// the fit gate rather than guessed from a system-memory size. A laptop at the
// bottom of the gate's viable range and a 128 GiB workstation with a small
// discrete GPU can both legitimately land on rule 3, and a 128 GiB machine with
// a 48 GB Ada card lands on rule 4 instead.
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

// packingFor selects the weights quantization for a machine, against the
// compile-time pin. It is decidePacking with the pin's build number filled in —
// the form the resolver calls, and the form whose four inputs name the four
// things upstream documents a packing decision for: the backend's kernels, the
// accelerator generation, the measured memory fit and the host CPU.
//
// There is no RAM-based downgrade in here, and that is a deliberate absence
// rather than an oversight: see decidePacking's closing note.
func packingFor(backend Backend, gpu GPUFamily, fitsPQ2_0 BudgetFit, host HostCaps) Packing {
	return decidePacking(PackingInput{
		Backend:      backend,
		GPU:          gpu,
		FitsPQ2_0:    fitsPQ2_0,
		Host:         host,
		RuntimeBuild: pinnedRuntimeBuild(),
	}).Packing
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
