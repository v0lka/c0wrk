// Memory-aware launch planning: the pure decision layer between "what memory
// this machine has" (topology.go) and "what memory this model needs"
// (memory.go). Plan is PURE — no I/O, no probing, no clock, no package state
// (TestPlanImportsNoIOPackage, TestPlanIsPure, which also pins that
// MemoryTopology.ProbedAt is ignored); the fit-exclusivity rule this file is
// organised around is specified in specs/domains/embedded-llm.md (Memory plan).
package embeddedllm

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

// DefaultFitMinContext is the `-fitc` floor c0wrk passes when fit sizes the
// context: 65536, deliberately NOT the fork's own default of 4096. The
// measurement-forced divergence (the truncated-answer failure a 4096 floor
// reproduces, and the ladder-tier agreement) is specified in
// specs/domains/embedded-llm.md (Memory plan).
const DefaultFitMinContext = 65536

// upstreamFitMinContext is the fork's own `-fitc` default, kept so the
// divergence above is quotable and testable rather than a remembered number.
const upstreamFitMinContext = 4096

// DefaultParallel is the `-np` value every plan emits: 1, not the fork's auto
// default — the divergence measurements (fit's projection inflation, per-slot
// context splitting) are in specs/domains/embedded-llm.md (Memory plan).
const DefaultParallel = 1

// DefaultCtxCheckpoints is the `--ctx-checkpoints` value every plan emits
// unless overridden: 32, the fork's own figure — pinned here rather than
// diverged, because the flag is rendered unconditionally and an inherited
// LLAMA_ARG_CTX_CHECKPOINTS env override must not decide a memory-bearing
// setting no c0wrk layer chose. specs/domains/embedded-llm.md (Memory plan).
const DefaultCtxCheckpoints = 32

// runtimeCacheRAMDefaultMiB is the pinned fork's own `--cache-ram` default,
// kept rather than diverged from — and the FLOOR of the derived ceiling
// (planPromptCacheCeiling): the planner only ever raises the cache above it,
// never lowers it below it. Why a flat default turns long agent steps back
// into cold starts is in specs/domains/embedded-llm.md (Memory plan).
const runtimeCacheRAMDefaultMiB = 8192

// Policy margins for the device/host SPLIT: memory.go's split was measured on
// Metal only, and the division of the model+runtime terms between device and
// host is a property of each backend's repack support. These allowances are
// POLICY, not measurement — the only numbers in this file not derived from a
// cited figure (specs/domains/embedded-llm.md, Memory plan).
const (
	// unmeasuredSplitAllowanceMiB is subtracted from the device budget on a
	// recognized GPU family whose split has not been measured.
	unmeasuredSplitAllowanceMiB int64 = 1024
	// unknownGPUAllowanceMiB is the larger deduction for a device the
	// classifier does not recognize at all — unknown in both directions.
	unknownGPUAllowanceMiB int64 = 2048
)

// ErrMemoryPlanInfeasible reports a launch the measured budgets cannot serve
// at any modelled KV precision; the typed refusal carries the arithmetic —
// see InsufficientMemoryError.
var ErrMemoryPlanInfeasible = errors.New("the embedded LLM does not fit this machine's measured memory budgets")

// ErrTuningInvalid reports an override the planner cannot honour. It is
// checked BEFORE any feasibility arithmetic, so a typo is reported as a typo
// and never as a machine that is too small.
var ErrTuningInvalid = errors.New("invalid embedded-LLM memory tuning")

// ─────────────────────────────────────────────────────────────────────────────
// The combined memory gate: prices the launch against BOTH pools — one shared
// pool when MemoryTopology.Unified, two otherwise — replacing ADR-066 D6's flat
// 16 GiB floor. specs/domains/embedded-llm.md (The combined memory gate).

// ErrInsufficientMemory is the typed refusal of the combined gate; every
// refusal is an *InsufficientMemoryError wrapping it, so a caller can match
// the sentinel without parsing a message and still show the arithmetic.
var ErrInsufficientMemory = errors.New("the embedded LLM does not fit this machine's memory")

// InsufficientMemoryError carries the arithmetic: what the model needs and
// what the machine has, for both pools. It unwraps to ErrInsufficientMemory and
// ErrMemoryPlanInfeasible always, and to the deprecated ErrInsufficientRAM only
// when the HOST pool overflowed (specs/domains/embedded-llm.md, Invariants).
type InsufficientMemoryError struct {
	// Packing, Context, KVType and Offloaded describe the shape the numbers
	// belong to: the closest-fitting one this machine was offered.
	Packing   Packing
	Context   int
	KVType    KVType
	Offloaded bool

	// DeviceNeedMiB and DeviceHaveMiB are the accelerator side; both are 0
	// for a machine with no accelerator, which the message says in words.
	DeviceNeedMiB int64
	DeviceHaveMiB int64
	// Device is the tri-state of the accelerator axis — see deviceAxisState.
	Device deviceAxisState

	// HostNeedMiB and HostHaveMiB are the system-RAM side; HostReserveMiB is
	// the reserve HostHaveMiB was derived with, so the message can state it.
	HostNeedMiB    int64
	HostHaveMiB    int64
	HostReserveMiB int64
	HostRAMGiB     float64

	// Unified reports whether the two pools are the same bytes: on a unified
	// machine the two figures are views of one pool, never additive.
	Unified bool
}

// Error renders both pools with both numbers. A unified machine whose
// accelerator footprint is non-zero gets one extra clause stating the SUM and
// the shortfall: its two figures are drawn from the same bytes, so a reader
// comparing the rendered pairs independently would conclude the launch fits
// when the pool is over. A machine with no accelerator gets no such clause.
func (e *InsufficientMemoryError) Error() string {
	var b strings.Builder
	b.WriteString(ErrInsufficientMemory.Error())
	b.WriteString(": ")
	b.WriteString(e.deviceClause())
	b.WriteString("; ")
	b.WriteString(e.hostClause())

	short := int64(0)
	switch {
	case e.Unified && e.DeviceNeedMiB > 0:
		total := e.DeviceNeedMiB + e.HostNeedMiB
		short = total - e.HostHaveMiB
		if short > 0 {
			fmt.Fprintf(&b, "; those two are the SAME pool on this machine, so together they need %s of the %s available",
				mibGiB(total), mibGiB(e.HostHaveMiB))
		}
	default:
		if e.Device == deviceKnown {
			short = max(short, e.DeviceNeedMiB-e.DeviceHaveMiB)
		}
		short = max(short, e.HostNeedMiB-e.HostHaveMiB)
	}
	if short > 0 {
		fmt.Fprintf(&b, " — %s short", shortfall(short))
	}
	fmt.Fprintf(&b, " (the smallest shape priced was %s weights with a %d-token %s KV cache, %s)",
		e.Packing, e.Context, e.KVType, e.residencyWords())
	return b.String()
}

// residencyWords renders which side of the machine the priced shape sat on.
func (e *InsufficientMemoryError) residencyWords() string {
	if e.Offloaded {
		return "offloaded to the accelerator"
	}
	return "host-resident"
}

// shortfall renders an overflow: under 1 GiB in MiB, because "needs 8.0 GiB,
// 8.0 GiB available" hides the difference that is the whole point of the
// refusal.
func shortfall(mib int64) string {
	if mib < mibPerGiBInt {
		return fmt.Sprintf("%d MiB", mib)
	}
	return mibGiB(mib)
}

// mibPerGiBInt is mibPerGiB as an integer, for the comparison shortfall makes.
const mibPerGiBInt = int64(mibPerGiB)

// deviceClause renders the accelerator half of the message.
func (e *InsufficientMemoryError) deviceClause() string {
	switch e.Device {
	case deviceUnreadable:
		return "the accelerator's memory could not be measured before planning, " +
			"so only the host pool was gated"
	case deviceAbsent:
		return fmt.Sprintf(
			"needs %s of device memory, %s available (no accelerator was detected, "+
				"so the whole model has to be host-resident)",
			mibGiB(e.DeviceNeedMiB), mibGiB(e.DeviceHaveMiB))
	default:
		return fmt.Sprintf("needs %s of device memory, %s available",
			mibGiB(e.DeviceNeedMiB), mibGiB(e.DeviceHaveMiB))
	}
}

// hostClause renders the system-RAM half of the message, naming the reserve the
// available figure was derived with.
func (e *InsufficientMemoryError) hostClause() string {
	return fmt.Sprintf(
		"needs %s of host RAM, %s available after the %s reserve (%s installed)",
		mibGiB(e.HostNeedMiB), mibGiB(e.HostHaveMiB), mibGiB(e.HostReserveMiB),
		gibString(e.HostRAMGiB))
}

// Unwrap returns the sentinel chain — see the type's doc comment for why
// ErrInsufficientRAM is conditional.
func (e *InsufficientMemoryError) Unwrap() []error {
	chain := []error{ErrInsufficientMemory, ErrMemoryPlanInfeasible}
	if e.HostNeedMiB > e.HostHaveMiB {
		chain = append(chain, ErrInsufficientRAM)
	}
	return chain
}

// mibGiB renders a whole-MiB figure as GiB with one decimal — a figure a user
// can compare against the "8 GB" on a box, which 9623 MiB is not.
func mibGiB(mib int64) string { return gibString(float64(mib) / mibPerGiB) }

// gibString renders a GiB figure with one decimal.
func gibString(gib float64) string { return fmt.Sprintf("%.1f GiB", gib) }

// deviceAxisState is the tri-state of the gate's accelerator side: "no
// accelerator" and "an accelerator whose memory nobody measured" are different
// facts with OPPOSITE consequences — the first refuses, the second must not —
// so a bool cannot carry it (the three consequences are tabled in
// specs/domains/embedded-llm.md, The combined memory gate).
type deviceAxisState int

const (
	// deviceAbsent means there is no accelerator memory to offload to, so the
	// model has to be host-resident. Known, and gateable.
	deviceAbsent deviceAxisState = iota
	// deviceKnown means the device budget is a number this gate may spend:
	// either a probed one, or a unified pool that IS the host budget.
	deviceKnown
	// deviceUnreadable means an accelerator with INDEPENDENT memory exists but
	// nothing measured its size. Deliberately NOT fatal: refusing here would
	// turn away the small-RAM machine with a big card the gate exists to
	// admit; the host pool is priced alone, the degradation noted.
	deviceUnreadable
)

// gateBudgets is the capacity side of one gate decision: the two budgets, the
// reserve the host one was derived with, and what is known about the accelerator.
type gateBudgets struct {
	device       deviceAxisState
	deviceMiB    int64
	hostMiB      int64
	reserveMiB   int64
	ramGiB       float64
	unified      bool
	degradedNote string
}

// DefaultHostReserveGiB returns the system RAM the gate keeps out of the host
// budget (GiB): the same derivation topology.go applies to a probed machine,
// exposed so the no-topology refusal and a probed one cannot disagree. The
// reserve's three named tenants are specified in
// specs/domains/embedded-llm.md (Device memory topology probe).
func DefaultHostReserveGiB(ramGiB float64) float64 {
	if ramGiB <= 0 {
		return 0
	}
	reserve := hostReserveBytes(int64(ramGiB * gibibyte))
	return float64(reserve) / gibibyte
}

// gateBudgetsFor derives the two budgets the gate spends: the topology's own
// numbers minus the family's split allowance with a probe (as planBudgets does,
// so gate and planner price the same machine), otherwise RAM-probe-derived
// budgets with a statically classified accelerator axis — why an unprobed
// machine is not simply refused is in specs/domains/embedded-llm.md
// (The combined memory gate).
func gateBudgetsFor(in ResolveInput, backend Backend, family GPUFamily) (gateBudgets, error) {
	// A set override REPLACES the derived reserve, and the conversion is
	// range-checked (hostReserveBytesFromGiB) — see hostReserveMiB for why.
	var overrideBytes int64
	hasOverride := false
	if override := in.Tuning.HostReserveGiB; override != nil {
		reserve, err := hostReserveBytesFromGiB(*override)
		if err != nil {
			return gateBudgets{}, err
		}
		overrideBytes, hasOverride = reserve, true
	}

	if in.Topology != nil {
		// The topology's HostRAMGiB is documented as the SAME figure the RAM
		// probe carries, so the profile's copy is not a second opinion.
		topology := normalizeTopology(*in.Topology, in.RAMGiB)
		ramBytes := int64(topology.HostRAMGiB * gibibyte)

		hostBytes := topology.HostBudgetBytes
		if hasOverride {
			hostBytes = max(ramBytes-overrideBytes, 0)
		}
		reserveBytes := max(ramBytes-hostBytes, 0)

		device := max(topology.DeviceBudgetMiB()-splitAllowanceMiB(family), 0)

		axis := deviceKnown
		if len(topology.Devices) == 0 {
			// The runtime itself reported no accelerator. That is an answer,
			// not a failed probe: there is nothing to offload to.
			axis = deviceAbsent
		}
		return gateBudgets{
			device:     axis,
			deviceMiB:  device,
			hostMiB:    hostBytes / bytesPerMiB,
			reserveMiB: reserveBytes / bytesPerMiB,
			ramGiB:     topology.HostRAMGiB,
			unified:    topology.Unified,
		}, nil
	}

	// No probe answered. The host side is still derivable — the RAM probe is a
	// hard input and `resolveWith` has already refused an unreadable one.
	ramGiB := in.RAMGiB
	ramBytes := int64(ramGiB * gibibyte)
	reserve := hostReserveBytes(ramBytes)
	if hasOverride {
		reserve = overrideBytes
	}
	host := ramBytes - reserve
	if host < 0 {
		host = 0
	}

	g := gateBudgets{
		hostMiB:    host / bytesPerMiB,
		reserveMiB: reserve / bytesPerMiB,
		ramGiB:     ramGiB,
	}
	// The axis follows `layersFor` — the plan's OWN offload policy — because
	// the gate must price the shape the plan will actually emit. That matters
	// on Apple Silicon, where the arm64 archive IS the Metal build even when
	// the effective backend came out as `cpu` and nothing is offloaded.
	switch {
	case layersFor(in.Platform, backend) == nglCPUOnly:
		// The CPU build, and an Intel Mac (no Metal compute path for this
		// runtime): there is genuinely nothing to offload to.
		g.device = deviceAbsent
	case backendHasIndependentVRAM(in.Platform, backend):
		// An accelerator with its own memory, whose size nothing measured.
		g.device = deviceUnreadable
		g.degradedNote = fmt.Sprintf(
			"the accelerator's memory could not be measured before planning (no provisioned runtime was there to ask), "+
				"so this plan was gated on system RAM alone: %s available after the %s reserve. "+
				"The runtime's own --fit pass sizes the offload against the card it finds at launch",
			mibGiB(g.hostMiB), mibGiB(g.reserveMiB))
	default:
		// The plan offloads and the accelerator's memory is NOT independent of
		// host RAM (Apple Silicon; Vulkan, which classifyUnified's rule 6 also
		// settles as unified when unsure) — the RAM probe measured both pools,
		// so the device budget IS the host budget.
		g.device = deviceKnown
		g.unified = true
		g.deviceMiB = g.hostMiB
	}
	return g, nil
}

// backendHasIndependentVRAM reports whether a platform+backend pair implies an
// accelerator whose memory is NOT host RAM — the only shape where a small RAM
// total can coexist with a model that does not fit in it; the static
// classification and its Vulkan exclusion are specified in
// specs/domains/embedded-llm.md (The combined memory gate).
func backendHasIndependentVRAM(platform string, backend Backend) bool {
	if platformArch(platform) != "amd64" {
		return false
	}
	return backend.IsCUDA() || backend == BackendROCm
}

// gateOverflow prices one (device, host) footprint pair against the budgets and
// returns how far over it is, in MiB; zero means the shape fits. The unified
// additivity rule (and why the device term is still checked on its own) is
// specified in specs/domains/embedded-llm.md (The combined memory gate).
func (g gateBudgets) overflow(deviceMiB, hostMiB int64) int64 {
	over := int64(0)
	if g.device == deviceKnown && deviceMiB > g.deviceMiB {
		over += deviceMiB - g.deviceMiB
	}
	if g.unified {
		if total := deviceMiB + hostMiB; total > g.hostMiB {
			over += total - g.hostMiB
		}
		return over
	}
	if hostMiB > g.hostMiB {
		over += hostMiB - g.hostMiB
	}
	return over
}

// gateVerdict is what the gate decided, for the planner and the Notes trail.
type gateVerdict struct {
	budgets gateBudgets
	// viable is the cheapest shape that fit, and the one the planner should
	// expect to land on.
	viable gateAttempt
	// notes carries the degradations the gate accepted, operator-facing.
	notes []string
}

// gateAttempt is one priced shape: a packing, a KV precision, a residency
// extreme and the two footprints that came out of `memory.go`'s projections.
type gateAttempt struct {
	packing   Packing
	kv        KVType
	offloaded bool
	deviceMiB int64
	hostMiB   int64
	overflow  int64
}

// gateContext is the context the gate prices: the operator's exact override
// when there is one, otherwise the `-fitc` floor — never the RAM ladder's
// larger tiers, because the gate answers "can this machine serve the model at
// all", and the smallest context c0wrk serves IS the floor (DefaultFitMinContext).
func gateContext(tuning Tuning) int {
	if tuning.Context.Mode == ContextExact && tuning.Context.Tokens > 0 {
		return tuning.Context.Tokens
	}
	if tuning.FitMinContext != nil && *tuning.FitMinContext > 0 {
		return *tuning.FitMinContext
	}
	return DefaultFitMinContext
}

// gatePackings is the packing set the gate prices: the machine's own decision
// plus PTQ1_0 when that is not already it, mirroring planMemory's single
// bounded downgrade so the gate never admits a machine on a packing the
// planner cannot reach. An operator-pinned packing is the whole set.
func gatePackings(in ResolveInput, backend Backend, family GPUFamily) []Packing {
	if in.Tuning.Packing != "" {
		return []Packing{in.Tuning.Packing}
	}
	base := packingFor(backend, family, in.FitsPQ2_0, in.Host)
	if base == PackingPTQ1_0 {
		return []Packing{base}
	}
	return []Packing{base, PackingPTQ1_0}
}

// gateKVTypes is the precision set the gate prices: the pinned one, or the
// whole ladder the planner is allowed to descend.
func gateKVTypes(tuning Tuning) []KVType {
	if tuning.KVType != "" {
		return []KVType{tuning.KVType}
	}
	return kvTypes
}

// memoryGate is the combined, unified-aware feasibility gate. It runs FIRST in
// `resolveWith` — before any asset is planned or directory created — so an
// undersized machine downloads nothing. It prices the modelled shape space at
// the smallest context c0wrk serves and admits the machine when ANY shape fits
// (specs/domains/embedded-llm.md, The combined memory gate). An unreadable
// device budget is NOT a refusal — see deviceUnreadable.
func memoryGate(in ResolveInput, backend Backend, family GPUFamily, profile ModelMemoryProfile) (gateVerdict, error) {
	g, err := gateBudgetsFor(in, backend, family)
	if err != nil {
		// An override no budget can be derived from (NaN, infinite, above the
		// ceiling); refusing here makes every later conversion total.
		return gateVerdict{}, err
	}

	ctx := gateContext(in.Tuning)
	packings := gatePackings(in, backend, family)
	kvSet := gateKVTypes(in.Tuning)

	// The residency extremes worth pricing: deviceAbsent prices host only, and
	// an unmeasured accelerator is priced as device-resident (what --fit will
	// make it) and gated on the host pool alone.
	offloadShapes := []bool{true, false}
	if g.device == deviceAbsent {
		offloadShapes = []bool{false}
	}

	var notes []string
	if g.degradedNote != "" {
		notes = append(notes, g.degradedNote)
	}

	best, ok := closestAttempt(profile, packings, kvSet, ctx, offloadShapes, g.overflow,
		in.Tuning.KVOffload == nil || *in.Tuning.KVOffload,
		in.Tuning.MMProjOffload == nil || *in.Tuning.MMProjOffload)
	if !ok {
		// Nothing was priced at all: every candidate was an unmeasured packing
		// or an unsupported precision. Report it as the planner does.
		return gateVerdict{}, fmt.Errorf("%w: no modelled shape could be priced for this machine",
			ErrMemoryNotMeasured)
	}
	if best.overflow == 0 {
		return gateVerdict{budgets: g, viable: best, notes: notes}, nil
	}
	return gateVerdict{}, insufficientMemory(g, best, ctx)
}

// closestAttempt prices every shape in the modelled space and returns the one
// that overflowed the budgets by the least; the space is walked least-lossy
// first, so the first fit found is the best one. An unmeasured shape is
// SKIPPED, not refused (Plan refuses an operator-PINNED unmeasured packing).
func closestAttempt(
	profile ModelMemoryProfile,
	packings []Packing,
	kvSet []KVType,
	ctx int,
	shapes []bool,
	overflow func(deviceMiB, hostMiB int64) int64,
	kvOffload bool,
	mmprojOffload bool,
) (gateAttempt, bool) {
	var best gateAttempt
	found := false
	for _, packing := range packings {
		for _, offloaded := range shapes {
			for _, kv := range kvSet {
				deviceMiB, hostMiB, _, err := footprint(profile, packing, ctx, kv, offloaded, false,
					kvOffload, mmprojOffload)
				if err != nil {
					continue
				}
				attempt := gateAttempt{
					packing:   packing,
					kv:        kv,
					offloaded: offloaded,
					deviceMiB: deviceMiB,
					hostMiB:   hostMiB,
					overflow:  overflow(deviceMiB, hostMiB),
				}
				if attempt.overflow == 0 {
					return attempt, true
				}
				if !found || attempt.overflow < best.overflow {
					best, found = attempt, true
				}
			}
		}
	}
	return best, found
}

// insufficientMemory is the typed refusal for a budget pair and the attempt
// that came closest to satisfying it.
func insufficientMemory(g gateBudgets, best gateAttempt, ctx int) *InsufficientMemoryError {
	return &InsufficientMemoryError{
		Packing:        best.packing,
		Context:        ctx,
		KVType:         best.kv,
		Offloaded:      best.offloaded,
		DeviceNeedMiB:  best.deviceMiB,
		DeviceHaveMiB:  g.deviceMiB,
		Device:         g.device,
		HostNeedMiB:    best.hostMiB,
		HostHaveMiB:    g.hostMiB,
		HostReserveMiB: g.reserveMiB,
		HostRAMGiB:     g.ramGiB,
		Unified:        g.unified,
	}
}

// CheckMemoryBudget runs the combined memory gate on its own, with no assets
// planned and no launch shape resolved — the gate `Resolve` runs first, exposed
// so the desktop RPC can refuse synchronously. A nil Topology is legal (derived
// budgets); specs/domains/embedded-llm.md (The combined memory gate).
func CheckMemoryBudget(in ResolveInput) error {
	// The mtplx engine gates through its own planner: one measured pool, the
	// pack's own terms, and the same first-check contract (mlxplan.go).
	if in.Engine == EngineMTPLX {
		terms, err := PinnedMLXMemoryTerms()
		if err != nil {
			return err
		}
		_, err = PlanMLX(in.RAMGiB, in.Tuning, terms)
		return err
	}
	profile, err := PinnedMemoryProfile()
	if err != nil {
		return fmt.Errorf("embeddedllm: memory profile: %w", err)
	}
	family := in.GPU
	if family == GPUFamilyUnknown && in.Topology != nil {
		family = ClassifyGPUs(in.Topology.Devices)
	}
	_, err = memoryGate(in, in.Backend, family, profile)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// The override vocabulary
// ─────────────────────────────────────────────────────────────────────────────

// OffloadMode is the `-ngl` half of a `Tuning`. Auto is the only value that
// leaves the fit-exclusivity rule free to hand sizing to the runtime.
type OffloadMode int

const (
	// OffloadAuto lets the planner decide: fit sizes the layer count when
	// nothing else is pinned, otherwise every offloadable layer goes to the
	// device when there is one and none when there is not.
	OffloadAuto OffloadMode = iota
	// OffloadAll is "every offloadable layer on the device" (`-ngl 99`).
	OffloadAll
	// OffloadCPU is "nothing offloaded" (`-ngl 0`): the model runs from system
	// RAM, which is the Intel-Mac and CPU-build shape.
	OffloadCPU
	// OffloadLayers is an explicit layer count (`-ngl n`), read from Layers.
	OffloadLayers
)

// Offload is an offload override: a mode plus, for OffloadLayers only, the
// layer count. The zero value is OffloadAuto with no count, i.e. "unset".
type Offload struct {
	Mode   OffloadMode
	Layers int
}

// explicit reports whether this override pins the offload, which is one of the
// three inputs that force fit off.
func (o Offload) explicit() bool { return o.Mode != OffloadAuto }

// ContextMode is the `-c` half of a `Tuning`.
type ContextMode int

const (
	// ContextAuto lets the planner compute the context: from the fit floor when
	// fit sizes it, otherwise from the RAM ladder.
	ContextAuto ContextMode = iota
	// ContextExact pins `-c` to Tokens.
	ContextExact
)

// ContextTuning is a context override. The zero value is ContextAuto, i.e.
// "unset".
type ContextTuning struct {
	Mode   ContextMode
	Tokens int
}

// SplitMode is the `-sm`/`--split-mode` override, with the runtime's own
// spellings. The zero value means "unset", which leaves the fit-exclusivity
// rule free to hand sizing to the runtime.
type SplitMode string

const (
	// SplitModeAuto omits `-sm` entirely and lets the runtime use its default.
	SplitModeAuto SplitMode = ""
	// SplitModeNone pins the model to one device.
	SplitModeNone SplitMode = "none"
	// SplitModeLayer splits layers and KV across devices (the runtime default).
	SplitModeLayer SplitMode = "layer"
	// SplitModeRow splits weights by rows across devices.
	SplitModeRow SplitMode = "row"
	// SplitModeTensor splits weights by tensors across devices.
	SplitModeTensor SplitMode = "tensor"
)

// Tuning is the operator-facing override vocabulary for one memory plan. EVERY
// field distinguishes "unset" from "set to the default value", because the
// difference is load-bearing under the fit-exclusivity rule (an unset `-ngl`
// lets fit size the launch; an `-ngl` equal to fit's choice turns fit OFF).
// Natural-zero-sentinel fields use it, the rest are pointers; the zero value is
// the all-Auto plan (specs/domains/embedded-llm.md, Memory plan).
type Tuning struct {
	// Context overrides `-c`. ContextExact pins it; ContextAuto leaves it to
	// the planner (which passes a zero `-c` under fit, so the runtime sizes it).
	Context ContextTuning
	// KVType overrides BOTH `-ctk` and `-ctv` (a mixed pair silently drops to
	// CPU flash attention); zero = Auto: f16 → q8_0 → q4_0 until the target fits.
	KVType KVType
	// Offload overrides `-ngl`. See OffloadMode.
	Offload Offload
	// FitEnabled overrides `-fit`: nil lets the exclusivity rule decide, an
	// explicit false forces `-fit off`, an explicit true beside an explicit
	// offload loses to the rule and is recorded in Notes.
	FitEnabled *bool
	// FitTargetMiB overrides `-fitt`, the per-device margin fit leaves free;
	// nil (and explicit 0) omit the flag. Only emitted under fit; bounded by
	// MaxTuningMiB.
	FitTargetMiB *int
	// FitMinContext overrides `-fitc` (nil means DefaultFitMinContext); only
	// emitted under fit, and it doubles as the context the feasibility gate
	// checks — a fit run that cannot reach its own floor aborts.
	FitMinContext *int
	// KVOffload is `-kvo`/`-nkvo`: nil and an explicit true keep the KV cache
	// with the layers, false leaves it in system RAM (attention-bandwidth cost).
	KVOffload *bool
	// MMProjOffload is `--mmproj-offload`/`--no-mmproj-offload`. nil and an
	// explicit true keep the projector's worst-case reserve on the device.
	MMProjOffload *bool
	// Packing overrides the weights quantization; zero = the backend's packing
	// (`packingFor`). Must be one the profile measured, else ErrMemoryNotMeasured.
	Packing Packing
	// Parallel overrides `-np`. nil means DefaultParallel; bounded by
	// MaxTuningParallel.
	Parallel *int
	// CacheRAMMiB overrides `-cram`, the prompt-cache ceiling: nil defers to
	// the planner (planPromptCacheCeiling); 0 disables; bounded MaxTuningMiB.
	CacheRAMMiB *int
	// CtxCheckpoints overrides `--ctx-checkpoints` (nil =
	// DefaultCtxCheckpoints; 0 disables verbatim); bounded
	// MaxTuningCtxCheckpoints.
	CtxCheckpoints *int
	// CacheIdleSlots is `--cache-idle-slots`/`--no-cache-idle-slots`. nil and
	// an explicit true keep the runtime's default of saving idle slots.
	CacheIdleSlots *bool
	// HostReserveGiB overrides the RAM kept out of the host budget: nil keeps
	// the topology's derivation, a SET value replaces it — a planner-side knob,
	// NOT a runtime flag; out-of-range figures are refused as ErrTuningInvalid
	// (see hostReserveMiB for the float→int hazard).
	HostReserveGiB *float64
	// Devices overrides `-dev`, the comma-separated offload target list. Any
	// entry forces fit off: naming the devices is pinning the offload.
	Devices []string
	// SplitMode overrides `-sm`. Anything but SplitModeAuto forces fit off.
	SplitMode SplitMode
}

// explicitOffloadShape reports whether any override pins the offload — the
// condition that forces `-fit off` (the fit-exclusivity rule).
func (t Tuning) explicitOffloadShape() bool {
	return t.Offload.explicit() || len(t.Devices) > 0 || t.SplitMode != SplitModeAuto
}

// ─────────────────────────────────────────────────────────────────────────────
// GPU families: the vocabulary and its classifier live in compat.go; this file
// is a second CONSUMER of the same answer — the family prices the uncertainty
// of a device/host split nobody measured (Resolve classifies before planning).

// splitAllowanceMiB is the device-budget deduction a family's unmeasured split
// costs: Apple Silicon (the measured family) pays nothing, a recognized family
// nobody measured pays unmeasuredSplitAllowanceMiB, an unrecognized part pays
// unknownGPUAllowanceMiB (specs/domains/embedded-llm.md, Memory plan).
func splitAllowanceMiB(family GPUFamily) int64 {
	switch family {
	case GPUFamilyAppleSilicon:
		return 0
	case GPUFamilyUnknown:
		return unknownGPUAllowanceMiB
	default:
		return unmeasuredSplitAllowanceMiB
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// The plan
// ─────────────────────────────────────────────────────────────────────────────

// MemoryPlan is the resolved launch shape: every value the runtime needs, the
// two footprints the plan expects to see, and the reason for each non-default
// decision. It is a description, not a command line — LaunchSpec.Args in
// server.go remains the only place a llama-server argv is assembled.
type MemoryPlan struct {
	// Fit reports whether the runtime's `--fit` pass sizes layers and context;
	// when true, Layers is nil and ContextSize is 0 — the exclusivity rule.
	Fit bool `json:"fit"`
	// FitTargetMiB is `-fitt`. Zero omits the flag and keeps the runtime's own
	// 1024 MiB default. Only meaningful when Fit is true.
	FitTargetMiB int `json:"fit_target_mib"`
	// FitMinContext is `-fitc`, the floor fit must respect. Always
	// DefaultFitMinContext or an explicit override — never the runtime's 4096.
	// Only meaningful when Fit is true.
	FitMinContext int `json:"fit_min_context"`
	// Layers is `-ngl`. NIL MEANS OMIT THE FLAG ENTIRELY (what lets fit choose
	// it); a non-nil value pins the offload and is accompanied by Fit == false.
	Layers *int `json:"layers,omitempty"`
	// ContextSize is `-c`. Zero means "fit sizes it" and only occurs with
	// Fit == true; with Fit == false it is always positive.
	ContextSize int `json:"context_size"`
	// KVType is the resolved `-ctk`/`-ctv` precision — never empty: Auto is
	// resolved to a concrete member, and the escalation is recorded in Notes.
	KVType KVType `json:"kv_type"`
	// Packing is the weights quantization the plan was projected for.
	Packing Packing `json:"packing"`
	// KVOffload false emits `-nkvo`: the KV cache stays in system RAM.
	KVOffload bool `json:"kv_offload"`
	// MMProjOffload false emits `--no-mmproj-offload`: the vision projector's
	// reserve stays in system RAM.
	MMProjOffload bool `json:"mmproj_offload"`
	// Parallel is `-np`. Always DefaultParallel unless overridden; see that
	// constant for why the runtime's own auto default is unsafe here.
	Parallel int `json:"parallel"`
	// CacheRAMMiB is `-cram`: non-nil (including 0, which disables the prompt
	// cache) passes through verbatim; nil omits the flag and lets the planner
	// emit a derived ceiling when budgets hold MORE spare with checkpoints
	// disabled (see planPromptCacheCeiling).
	CacheRAMMiB *int `json:"cache_ram_mib,omitempty"`
	// CtxCheckpoints is `--ctx-checkpoints`, always DefaultCtxCheckpoints
	// unless overridden — 0 (snapshots disabled) included verbatim.
	CtxCheckpoints int `json:"ctx_checkpoints"`
	// CacheIdleSlots is the resolved idle-slot caching spelling: true unless
	// explicitly disabled, the runtime's own default.
	CacheIdleSlots bool `json:"cache_idle_slots"`
	// Devices is `-dev`. Empty omits the flag.
	Devices []string `json:"devices,omitempty"`
	// SplitMode is `-sm`. SplitModeAuto omits the flag.
	SplitMode SplitMode `json:"split_mode,omitempty"`
	// GPUFamily is the family the plan was classified as, echoed so a support
	// bundle carries the reason behind the allowance in the budgets below.
	GPUFamily GPUFamily `json:"gpu_family,omitempty"`
	// DeviceBudgetMiB and HostBudgetMiB are the budgets the plan was gated
	// against: the topology's own, after the split allowance and any override.
	DeviceBudgetMiB int64 `json:"device_budget_mib"`
	HostBudgetMiB   int64 `json:"host_budget_mib"`
	// ExpectedDeviceMiB and ExpectedHostMiB are the projected footprints of the
	// chosen shape, INCLUDING the vision projector's reserve. When the offload
	// is a PARTIAL layer count neither figure is exact: device is the
	// full-offload bound and host the CPU-only bound, and Notes says so.
	ExpectedDeviceMiB int64 `json:"expected_device_mib"`
	ExpectedHostMiB   int64 `json:"expected_host_mib"`
	// Notes is the human-readable "why", one entry per non-default decision,
	// surfaced in the UI verbatim.
	Notes []string `json:"notes,omitempty"`
}

// FitArg renders the literal value of the `-fit` flag for this plan, so the
// exclusivity rule is checkable exactly as the rule is stated.
func (p MemoryPlan) FitArg() string {
	if p.Fit {
		return "on"
	}
	return "off"
}

// EmitsLayers reports whether the launcher must pass `-ngl` at all — under fit
// it must not: an explicit `-ngl` beside `--fit on` aborts the launch.
func (p MemoryPlan) EmitsLayers() bool { return p.Layers != nil }

// OffloadsToDevice reports whether the plan puts any model layers on an
// accelerator. Under fit it is a PREDICTION (fit decides at launch) derived
// from whether the topology reported any usable device budget at all.
func (p MemoryPlan) OffloadsToDevice() bool {
	return p.Layers == nil && p.Fit && p.DeviceBudgetMiB > 0 ||
		p.Layers != nil && *p.Layers > 0
}

// Plan resolves a launch shape from measured inputs. It is PURE (see this
// file's header). topology is the capacity side — its zero value means
// "unknown" and must NOT be passed here; profile is the requirement side. A
// shape that does not fit is DEGRADED before it is refused: planShape is the
// single pass, wrapped in Plan's bounded least-lossy-first relaxation ladder
// (specs/domains/embedded-llm.md, Memory plan). Refusals, in order: an
// unhonourable override → ErrTuningInvalid; an unmeasured packing →
// ErrMemoryNotMeasured; nothing fits after the ladder → ErrInsufficientMemory.
func Plan(
	topology MemoryTopology,
	profile ModelMemoryProfile,
	tuning Tuning,
	backend Backend,
	family GPUFamily,
) (MemoryPlan, error) {
	topology = normalizeTopology(topology, 0)
	// planShape returns a ZERO MemoryPlan when it refuses, so the packing a
	// refusal has to price is re-derived here rather than read off the plan.
	packing := tuning.Packing
	if packing == "" {
		packing = packingFor(backend, family, FitUnknown, HostCaps{})
	}

	plan, err := planShape(topology, profile, tuning, backend, family)
	if err == nil || !errors.Is(err, ErrMemoryPlanInfeasible) {
		return plan, err
	}
	// An operator who pinned the offload shape named the very thing the ladder
	// would change; honouring the pin beats quietly launching a different
	// shape than the one asked for. The refusal is still the typed both-pools
	// one — "which pool is short" is a fact about the machine.
	if tuning.explicitOffloadShape() {
		return plan, insufficientForPlan(topology, profile, tuning, family, packing)
	}

	for _, relax := range relaxations(tuning, topology, profile) {
		candidate := relax.apply(tuning)
		relaxed, relaxErr := planShape(topology, profile, candidate, backend, family)
		if relaxErr == nil {
			relaxed.Notes = append(relaxed.Notes, relax.note)
			return relaxed, nil
		}
		if !errors.Is(relaxErr, ErrMemoryPlanInfeasible) {
			// A different refusal (an invalid tuning, an unmeasured packing)
			// is not something another relaxation can fix.
			return relaxed, relaxErr
		}
	}
	return plan, insufficientForPlan(topology, profile, tuning, family, packing)
}

// relaxation is one shape degradation Plan tries when the base shape does not
// fit. `apply` returns the Tuning to re-plan with; `note` is the
// operator-facing sentence appended to the plan that won.
type relaxation struct {
	apply func(Tuning) Tuning
	note  string
}

// relaxations is the degradation ladder, LEAST-LOSSY FIRST: spill the KV cache
// and the projector's reserve to host RAM, shrink the context to the fit
// floor, both, host residency (a MEASURED shape — a partial offload is not,
// and pricing one would guess at a split), host residency with the reduced
// context. Each rung re-runs planKVType's own precision ladder rather than
// replacing it. The rung table is in specs/domains/embedded-llm.md (Memory plan).
func relaxations(tuning Tuning, topology MemoryTopology, profile ModelMemoryProfile) []relaxation {
	spillable := tuning.KVOffload == nil || tuning.MMProjOffload == nil
	shrinkable := shrinkTarget(tuning, topology, profile) > DefaultFitMinContext

	var out []relaxation
	if spillable {
		out = append(out, relaxation{apply: spillKVToHost,
			note: "the accelerator could not hold the KV cache and the vision projector's reserve, " +
				"so both were moved to system RAM (-nkvo, --no-mmproj-offload): the weights stay on the " +
				"accelerator and attention now crosses the bus"})
	}
	if shrinkable {
		out = append(out, relaxation{apply: shrinkContextToFloor,
			note: fmt.Sprintf("the context was reduced to %d tokens, the smallest this project will serve: "+
				"the larger one did not fit the measured budgets", DefaultFitMinContext)})
	}
	if spillable && shrinkable {
		out = append(out, relaxation{apply: func(t Tuning) Tuning { return shrinkContextToFloor(spillKVToHost(t)) },
			note: "the KV cache and the vision projector's reserve were moved to system RAM and the " +
				fmt.Sprintf("context was reduced to %d tokens to fit the measured budgets", DefaultFitMinContext)})
	}
	out = append(out,
		relaxation{apply: hostResident,
			note: "the plan degraded to host residency because the accelerator could not hold the weights at " +
				"ANY modelled KV precision. A PARTIAL offload was not considered: memory.go has no measurement " +
				"for one, and an unmeasured device/host split is a guess a capacity gate must not make"},
	)
	if shrinkable {
		out = append(out, relaxation{
			apply: func(t Tuning) Tuning { return shrinkContextToFloor(hostResident(t)) },
			note: "the model runs entirely from system RAM (-ngl 0) with the context reduced to " +
				fmt.Sprintf("%d tokens", DefaultFitMinContext),
		})
	}
	return out
}

// spillKVToHost moves the KV cache and the projector's reserve off the
// accelerator, leaving an operator-pinned half alone (which is why
// `relaxations` gates the rung on either knob still being unset).
func spillKVToHost(tuning Tuning) Tuning {
	if tuning.KVOffload == nil {
		tuning.KVOffload = ptrBool(false)
	}
	if tuning.MMProjOffload == nil {
		tuning.MMProjOffload = ptrBool(false)
	}
	return tuning
}

// hostResident pins the offload to nothing, which by the exclusivity rule also
// turns fit off. It deliberately leaves KVOffload/MMProjOffload alone: with
// nothing offloaded there is no device side to move away from, and pinning
// them would add two Notes asserting a relocation that did not happen.
func hostResident(tuning Tuning) Tuning {
	tuning.Offload = Offload{Mode: OffloadCPU}
	return tuning
}

// shrinkContextToFloor pins the context to DefaultFitMinContext: the `-fitc`
// floor under fit, an exact `-c` on the planner-computed path. It never goes
// below the floor.
func shrinkContextToFloor(tuning Tuning) Tuning {
	tuning.FitMinContext = ptrInt(DefaultFitMinContext)
	tuning.Context = ContextTuning{Mode: ContextExact, Tokens: DefaultFitMinContext}
	return tuning
}

// shrinkTarget is the context a relaxation would have to beat for the context
// rung to be worth trying: the plan's own target, derived the same way
// planTargetContext derives it.
func shrinkTarget(tuning Tuning, topology MemoryTopology, profile ModelMemoryProfile) int {
	if tuning.Context.Mode == ContextExact {
		return tuning.Context.Tokens
	}
	if !tuning.explicitOffloadShape() && tuning.FitEnabled == nil {
		floor := DefaultFitMinContext
		if tuning.FitMinContext != nil && *tuning.FitMinContext > 0 {
			floor = *tuning.FitMinContext
		}
		return floor
	}
	ladder := contextSizeFor(topology.HostRAMGiB)
	if profile.MaxContext > 0 && ladder > profile.MaxContext {
		ladder = profile.MaxContext
	}
	return ladder
}

// insufficientForPlan is the typed refusal a fully-degraded plan ends on. It
// re-prices the modelled shape space — the same search memoryGate runs, so a
// gate that admitted and a planner that refused cannot disagree about what was
// tried — and reports the attempt that came CLOSEST to fitting.
func insufficientForPlan(
	topology MemoryTopology,
	profile ModelMemoryProfile,
	tuning Tuning,
	family GPUFamily,
	packing Packing,
) error {
	b, err := planBudgets(topology, tuning, family)
	if err != nil {
		// Unreachable in practice: Plan returns a budget error from planShape
		// before it ever asks for a refusal message.
		return err
	}
	g := gateBudgets{
		device:     deviceKnown,
		deviceMiB:  b.deviceMiB,
		hostMiB:    b.hostMiB,
		reserveMiB: (int64(topology.HostRAMGiB*gibibyte) - b.hostMiB*bytesPerMiB) / bytesPerMiB,
		ramGiB:     topology.HostRAMGiB,
		unified:    topology.Unified,
	}
	if len(topology.Devices) == 0 {
		g.device = deviceAbsent
	}
	if g.reserveMiB < 0 {
		g.reserveMiB = 0
	}

	ctx := shrinkTarget(tuning, topology, profile)
	if ctx < DefaultFitMinContext {
		ctx = DefaultFitMinContext
	}
	packings := []Packing{packing}
	if packing == PackingPQ2_0 {
		packings = append(packings, PackingPTQ1_0)
	}
	shapes := []bool{true, false}
	switch {
	case g.device == deviceAbsent || b.deviceMiB <= 0:
		shapes = []bool{false}
	case tuning.Offload.Mode == OffloadAll:
		// The operator pinned the residency the refusal is about; pricing a
		// shape they ruled out would report numbers nobody asked for.
		shapes = []bool{true}
	case tuning.Offload.Mode == OffloadCPU:
		shapes = []bool{false}
	case OffloadLayers == tuning.Offload.Mode:
		// A partial count is unmeasured; price both extremes, the bound
		// `footprint` already reports.
	}

	best, ok := closestAttempt(profile, packings, gateKVTypes(tuning), ctx, shapes, g.overflow,
		tuning.KVOffload == nil || *tuning.KVOffload,
		tuning.MMProjOffload == nil || *tuning.MMProjOffload)
	if !ok {
		return fmt.Errorf("%w: no modelled shape could be priced for this machine", ErrMemoryNotMeasured)
	}
	return insufficientMemory(g, best, ctx)
}

// planShape is ONE planning pass over an already-fixed Tuning: no retries, no
// degradation. Plan owns the ladder; this owns the arithmetic.
func planShape(
	topology MemoryTopology,
	profile ModelMemoryProfile,
	tuning Tuning,
	backend Backend,
	family GPUFamily,
) (MemoryPlan, error) {
	if family == "" {
		family = GPUFamilyUnknown
	}

	packing, err := planPacking(tuning, backend, family, profile)
	if err != nil {
		return MemoryPlan{}, err
	}

	budgets, err := planBudgets(topology, tuning, family)
	if err != nil {
		return MemoryPlan{}, err
	}

	// Fit exclusivity: one decider, chosen by the override vocabulary alone.
	fit := !tuning.explicitOffloadShape()
	var notes []string
	if tuning.FitEnabled != nil {
		switch {
		case !*tuning.FitEnabled && fit:
			fit = false
			notes = append(notes, "--fit was disabled explicitly, so the layer count and the context are computed here instead of being sized by the runtime")
		case *tuning.FitEnabled && !fit:
			notes = append(notes, "--fit on was requested but an explicit offload, device list or split mode is set; the runtime refuses that combination, so --fit stays off")
		}
	}

	layers, layerNotes := planLayers(tuning, fit, budgets.deviceMiB)
	notes = append(notes, layerNotes...)

	fitMinContext := DefaultFitMinContext
	if tuning.FitMinContext != nil && *tuning.FitMinContext > 0 {
		fitMinContext = *tuning.FitMinContext
		if fitMinContext != DefaultFitMinContext {
			notes = append(notes, fmt.Sprintf(
				"the minimum context was overridden to %d tokens (the runtime's own default is %d, which is what produces this model's empty and truncated answers)",
				fitMinContext, upstreamFitMinContext))
		}
	}

	target, err := planTargetContext(tuning, fit, fitMinContext, topology, profile)
	if err != nil {
		return MemoryPlan{}, err
	}

	// One flag, computed once, shared by the KV gate and the expected-figure
	// projection: a partial layer count is the one shape the measured profile
	// does not cover, and both consumers must agree on measurement vs bound.
	partial := partialOffload(profile, layers, fit)

	kv, kvNotes, err := planKVType(tuning, profile, packing, target, budgets, layers, fit, partial)
	if err != nil {
		return MemoryPlan{}, err
	}
	notes = append(notes, kvNotes...)

	deviceMiB, hostMiB, bound, err := planFootprint(profile, packing, target, kv, layers, fit, partial, budgets, tuning)
	if err != nil {
		return MemoryPlan{}, err
	}
	if bound {
		notes = append(notes, fmt.Sprintf(
			"a partial offload of %d layers is not covered by the measured profile, so the expected figures are conservative bounds (full-offload device, CPU-only host) rather than measurements",
			deref(layers)))
	}

	parallel := DefaultParallel
	if tuning.Parallel != nil && *tuning.Parallel > 0 {
		parallel = *tuning.Parallel
		if parallel != DefaultParallel {
			notes = append(notes, fmt.Sprintf(
				"%d parallel slots were requested; the runtime splits -c across them, so each slot gets %d tokens, and it multiplies the compute reserve when the KV cache is not unified",
				parallel, target/max(parallel, 1)))
		}
	}

	ctxCheckpoints, err := planCtxCheckpoints(tuning)
	if err != nil {
		return MemoryPlan{}, err
	}

	plan := MemoryPlan{
		Fit:            fit,
		FitMinContext:  fitMinContext,
		Layers:         layers,
		KVType:         kv,
		Packing:        packing,
		KVOffload:      tuning.KVOffload == nil || *tuning.KVOffload,
		MMProjOffload:  tuning.MMProjOffload == nil || *tuning.MMProjOffload,
		Parallel:       parallel,
		CtxCheckpoints: ctxCheckpoints,
		CacheIdleSlots: tuning.CacheIdleSlots == nil || *tuning.CacheIdleSlots,
		Devices:        slices.Clone(tuning.Devices),
		SplitMode:      tuning.SplitMode,
		GPUFamily:      family,

		DeviceBudgetMiB:   budgets.deviceMiB,
		HostBudgetMiB:     budgets.hostMiB,
		ExpectedDeviceMiB: deviceMiB,
		ExpectedHostMiB:   hostMiB,
	}
	// The plan ALWAYS records a concrete `-c`. Under fit that is the RAM tier
	// held to the `-fitc` floor (see planTargetContext), NOT "fit decides":
	// `--fit` adjusts only UNSET arguments, so a rendered `-c` is left alone
	// and fit sizes only the offload — a zero would hand the context to the
	// runtime, which sizes it to the full training context.
	plan.ContextSize = target
	if fit {
		if tuning.Context.Mode == ContextExact {
			notes = append(notes, fmt.Sprintf(
				"the context was pinned to %d tokens, which the runtime leaves alone; only the layer count is sized by --fit",
				target))
		} else {
			notes = append(notes, fmt.Sprintf(
				"the context is the %d-token RAM tier (held to the %d-token fit floor), rendered as an explicit -c so the runtime's own fit pass never sizes it up to the model's training context; only the layer count is sized by --fit",
				target, fitMinContext))
		}
		if tuning.FitTargetMiB != nil && *tuning.FitTargetMiB > 0 {
			plan.FitTargetMiB = *tuning.FitTargetMiB
			notes = append(notes, fmt.Sprintf(
				"the per-device fit margin was overridden to %d MiB", plan.FitTargetMiB))
		}
	}
	if tuning.CacheRAMMiB != nil {
		value := *tuning.CacheRAMMiB
		plan.CacheRAMMiB = &value
		if value == 0 {
			notes = append(notes, "the prompt cache was disabled (-cram 0)")
		} else {
			notes = append(notes, fmt.Sprintf("the prompt cache was capped at %d MiB", value))
		}
	} else if cache, note, ok := planPromptCacheCeiling(budgets, plan); ok {
		plan.CacheRAMMiB = &cache
		notes = append(notes, note)
	}
	if tuning.CtxCheckpoints != nil && *tuning.CtxCheckpoints != DefaultCtxCheckpoints {
		if *tuning.CtxCheckpoints == 0 {
			notes = append(notes, "the per-slot context checkpoints were disabled (--ctx-checkpoints 0)")
		} else {
			notes = append(notes, fmt.Sprintf(
				"the per-slot context checkpoint count was overridden to %d (the runtime's own default is %d)",
				*tuning.CtxCheckpoints, DefaultCtxCheckpoints))
		}
	}

	notes = append(notes, shapeNotes(tuning, plan, profile, budgets, topology)...)
	plan.Notes = notes
	return plan, nil
}

// planCtxCheckpoints resolves `--ctx-checkpoints`: DefaultCtxCheckpoints when
// unset, the operator's figure verbatim otherwise (0 disables). A value
// outside 0..MaxTuningCtxCheckpoints is refused as ErrTuningInvalid rather
// than clamped or defaulted — a typo must not plan a different launch.
func planCtxCheckpoints(tuning Tuning) (int, error) {
	if tuning.CtxCheckpoints == nil {
		return DefaultCtxCheckpoints, nil
	}
	if *tuning.CtxCheckpoints < 0 || *tuning.CtxCheckpoints > MaxTuningCtxCheckpoints {
		return 0, fmt.Errorf("%w: ctx_checkpoints %d is outside 0..%d",
			ErrTuningInvalid, *tuning.CtxCheckpoints, MaxTuningCtxCheckpoints)
	}
	return *tuning.CtxCheckpoints, nil
}

// planPromptCacheCeiling derives the `--cache-ram` value an unset cache_ram_mib
// leaves the planner free to choose: the memory the measured budgets hold
// BEYOND the launch's expected footprint, emitted only when it EXCEEDS the
// runtime's own default — this path can only raise the cache, never lower it.
// While per-slot context checkpoints are enabled it derives nothing: the
// snapshots sit outside the `--cache-ram` accounting, so the spare is held for
// them. The full derivation (unified subtraction, checkpoint interplay, the
// MaxTuningMiB clamp) is specified in specs/domains/embedded-llm.md (Memory plan).
func planPromptCacheCeiling(b budgets, plan MemoryPlan) (value int, note string, ok bool) {
	if plan.CtxCheckpoints != 0 {
		return 0, "", false
	}
	leftover := b.hostMiB - plan.ExpectedHostMiB
	pool := "host"
	if b.unified {
		leftover -= plan.ExpectedDeviceMiB
		pool = "unified"
	}
	if leftover <= runtimeCacheRAMDefaultMiB {
		return 0, "", false
	}
	value = int(min(leftover, MaxTuningMiB))
	return value, fmt.Sprintf(
		"the prompt cache ceiling was raised to %d MiB — the memory the measured %s budget holds beyond the launch's expected footprint, above the runtime's own %d MiB default (a prefix past the ceiling is re-prefilled from scratch)",
		value, pool, runtimeCacheRAMDefaultMiB), true
}

// planPacking resolves the weights quantization: an explicit override, or the
// backend's own packing. Either way the profile must have MEASURED residency
// for it, so an unmeasured packing is refused here rather than projected from
// its file size — which would guess at the device/host split.
func planPacking(tuning Tuning, backend Backend, family GPUFamily, profile ModelMemoryProfile) (Packing, error) {
	// The memory-fit axis is FitUnknown here by construction: the packing
	// decides the footprint, so a single pass cannot know whether PQ2_0 fits
	// before it has picked one. A fit-driven downgrade is a two-pass business
	// — see decidePacking.
	packing := packingFor(backend, family, FitUnknown, HostCaps{})
	if tuning.Packing != "" {
		packing = tuning.Packing
	}
	if _, ok := profile.DeviceWeightsMiB[packing]; !ok {
		return "", fmt.Errorf("%w: device residency of packing %q", ErrMemoryNotMeasured, packing)
	}
	if _, ok := profile.HostWeightsMiB[packing]; !ok {
		return "", fmt.Errorf("%w: host residency of packing %q", ErrMemoryNotMeasured, packing)
	}
	return packing, nil
}

// budgets is the capacity side of one plan.
type budgets struct {
	deviceMiB int64
	hostMiB   int64
	// unified reports that the two pools are the SAME bytes, making the two
	// footprints additive — see fits.
	unified bool
}

// fits reports whether a (device, host) footprint pair fits these budgets —
// gateBudgets.overflow's rule on the planner's side. The unified case is why
// this is a method and not two comparisons: footprints that EACH fit can still
// need twice the machine's memory in sum (the device term is checked alone too,
// for the margin and split allowance that live in the device budget).
func (b budgets) fits(deviceMiB, hostMiB int64) bool {
	if deviceMiB > b.deviceMiB || hostMiB > b.hostMiB {
		return false
	}
	if b.unified {
		return deviceMiB+hostMiB <= b.hostMiB
	}
	return true
}

// planBudgets derives the two numbers the plan spends: the device budget loses
// the family's split allowance (nothing measured covers a repack split); the
// host budget is the topology's own unless HostReserveGiB replaces the reserve
// policy (conversion range-checked as the gate's — hostReserveMiB). Unified is
// carried through so the budgets are spent the way the gate spends them.
func planBudgets(topology MemoryTopology, tuning Tuning, family GPUFamily) (budgets, error) {
	device := topology.DeviceBudgetMiB() - splitAllowanceMiB(family)
	if device < 0 {
		device = 0
	}

	host := topology.HostBudgetMiB()
	if tuning.HostReserveGiB != nil {
		reserveMiB, err := hostReserveMiB(*tuning.HostReserveGiB)
		if err != nil {
			return budgets{}, err
		}
		host = int64(topology.HostRAMGiB*mibPerGiB) - reserveMiB
		if host < 0 {
			host = 0
		}
	}
	return budgets{deviceMiB: device, hostMiB: host, unified: topology.Unified}, nil
}

// hostReserveMiB converts the operator's host-reserve override (GiB) into the
// MiB every budget in this file uses. The check is explicit because a float→int
// conversion the result type cannot represent is IMPLEMENTATION-DEFINED in Go:
// this host saturates to MaxInt64 (a zero host budget, fail-closed), while
// amd64 conventionally yields a negative "indefinite value" that would make
// `host = ram − reserve` enormous and fail the gate OPEN on the very knob
// meant to shrink it — and NaN used to slip through a `>= 0` test in silence.
func hostReserveMiB(gib float64) (int64, error) {
	if err := checkHostReserve(gib); err != nil {
		return 0, err
	}
	return int64(gib * mibPerGiB), nil
}

// hostReserveBytesFromGiB is hostReserveMiB in the byte unit the gate's own
// derivation uses (topology.go's hostReserveBytes applies the default policy).
func hostReserveBytesFromGiB(gib float64) (int64, error) {
	if err := checkHostReserve(gib); err != nil {
		return 0, err
	}
	return int64(gib * gibibyte), nil
}

// checkHostReserve refuses a reserve override no total conversion can
// represent. The ceiling is shared with backend/config so a bound at only one
// layer is not a bound an operator can walk around by hand-editing config.yaml
// (specs/domains/embedded-llm.md, Key Files: limits.go).
func checkHostReserve(gib float64) error {
	if math.IsNaN(gib) {
		return fmt.Errorf("%w: host_reserve_gib is NaN, which no budget can be derived from", ErrTuningInvalid)
	}
	if math.IsInf(gib, 0) {
		return fmt.Errorf("%w: host_reserve_gib is infinite", ErrTuningInvalid)
	}
	if gib < 0 {
		return fmt.Errorf("%w: host_reserve_gib %s is negative", ErrTuningInvalid, gibString(gib))
	}
	if gib > MaxTuningHostReserveGiB {
		return fmt.Errorf("%w: host_reserve_gib %s exceeds the %d GiB ceiling",
			ErrTuningInvalid, gibString(gib), MaxTuningHostReserveGiB)
	}
	return nil
}

// mibPerGiB converts a GiB figure into the MiB every budget in this file uses.
const mibPerGiB = float64(1024)

// normalizeTopology fills in the DERIVED halves of a topology that carries an
// inventory but no budgets — one a caller assembled rather than one
// ProbeDevices returned — deriving the figures the way `buildTopology` does, so
// a normalized and a probed topology are priced identically. It never invents
// memory: `Unified` is taken as given (topology.go is the authority), and a
// pool that fits INSIDE the margin stays 0 — a measurement, not an omission.
// ramGiBFallback supplies the host total when unset; 0 means "no fallback".
func normalizeTopology(topology MemoryTopology, ramGiBFallback float64) MemoryTopology {
	if topology.HostRAMGiB <= 0 && ramGiBFallback > 0 {
		topology.HostRAMGiB = ramGiBFallback
	}
	ramBytes := int64(topology.HostRAMGiB * gibibyte)
	if topology.HostBudgetBytes <= 0 && ramBytes > 0 {
		topology.HostBudgetBytes = max(ramBytes-hostReserveBytes(ramBytes), 0)
	}
	if topology.DeviceBudgetBytes > 0 {
		return topology
	}
	pool := devicePoolMiB(topology.Devices, topology.Unified)
	if pool <= deviceMarginMiB {
		return topology
	}
	budget := (pool - deviceMarginMiB) * bytesPerMiB
	if topology.Unified {
		budget = min(budget, topology.HostBudgetBytes)
	}
	topology.DeviceBudgetBytes = max(budget, 0)
	return topology
}

// planLayers resolves `-ngl`, or nil when the runtime's fit pass must choose
// it: under fit the flag is OMITTED — setting it is what turns fit off and
// makes the fork abort.
func planLayers(tuning Tuning, fit bool, deviceBudgetMiB int64) (layers *int, notes []string) {
	if fit {
		return nil, nil
	}

	switch tuning.Offload.Mode {
	case OffloadAll:
		return ptrInt(nglAllGPU), []string{fmt.Sprintf(
			"every offloadable layer was pinned to the accelerator (-ngl %d), which is the shape the runtime's own sizing pass would otherwise choose",
			nglAllGPU)}
	case OffloadCPU:
		return ptrInt(nglCPUOnly), []string{
			"no layer was offloaded (-ngl 0); the model runs entirely from system RAM",
		}
	case OffloadLayers:
		// Clamped to the range the flag accepts; the runtime treats any value
		// at or above the offloadable count as "all", so no offload is lost.
		layers := min(max(tuning.Offload.Layers, nglCPUOnly), nglAllGPU)
		return ptrInt(layers), []string{fmt.Sprintf(
			"the offload was pinned to %d layers (-ngl %d)", layers, layers)}
	case OffloadAuto:
	default:
		return ptrInt(nglCPUOnly), []string{
			"an unrecognized offload override was ignored; nothing is offloaded",
		}
	}

	// Auto with fit forced off: offload everything when there is a device to
	// offload to, and nothing when there is not.
	if deviceBudgetMiB > 0 {
		return ptrInt(nglAllGPU), nil
	}
	return ptrInt(nglCPUOnly), []string{
		"no accelerator memory was probed, so the model runs entirely from system RAM",
	}
}

// planTargetContext resolves the context the plan must be able to SERVE: the
// floor fit is held to under fit (`-fitc` — a fit run that cannot reach its
// own minimum aborts), otherwise the override or the RAM ladder.
func planTargetContext(
	tuning Tuning,
	fit bool,
	fitMinContext int,
	topology MemoryTopology,
	profile ModelMemoryProfile,
) (int, error) {
	if tuning.Context.Mode == ContextExact {
		tokens := tuning.Context.Tokens
		if tokens <= 0 {
			return 0, fmt.Errorf("%w: an exact context of %d tokens is not positive", ErrTuningInvalid, tokens)
		}
		if profile.MaxContext > 0 && tokens > profile.MaxContext {
			return 0, fmt.Errorf("%w: a context of %d tokens exceeds the model's own %d-token training context",
				ErrTuningInvalid, tokens, profile.MaxContext)
		}
		return tokens, nil
	}
	if fit {
		// The fit path renders the SAME tier ladder the explicit path does,
		// held to the `-fitc` floor: fit sizes the offload, never the context
		// (without this floor a lower tier would reproduce the truncated-answer
		// failure DefaultFitMinContext exists to prevent).
		ladder := ladderContextFor(topology, profile)
		if fitMinContext > ladder {
			ladder = fitMinContext
		}
		return ladder, nil
	}
	return ladderContextFor(topology, profile), nil
}

// ladderContextFor is the RAM-tier context the explicit path has always used,
// clamped to the model's own training context. It is ALSO the fit path's `-c`
// (see planShape): `--fit` adjusts only UNSET arguments, so a rendered `-c` is
// left alone while an omitted (zero) one hands sizing to the runtime, which
// picks the full training context regardless of memory.
func ladderContextFor(topology MemoryTopology, profile ModelMemoryProfile) int {
	ladder := contextSizeFor(topology.HostRAMGiB)
	if profile.MaxContext > 0 && ladder > profile.MaxContext {
		ladder = profile.MaxContext
	}
	return ladder
}

// planKVType resolves `-ctk`/`-ctv`. An explicit override is honoured as given
// and validated against memory.go's closed set; Auto is the ADAPTIVE path —
// f16, then q8_0, then q4_0 (measured within 1% of f16 throughput), recording
// every escalation. "Fits" is budgets.fits, not two independent comparisons:
// on a unified machine the two footprints are spent from ONE pool.
func planKVType(
	tuning Tuning,
	profile ModelMemoryProfile,
	packing Packing,
	target int,
	budgets budgets,
	layers *int,
	fit bool,
	partial bool,
) (KVType, []string, error) {
	if tuning.KVType != "" {
		resolved, err := ParseKVType(string(tuning.KVType))
		if err != nil {
			// Both errors are wrapped, so a caller can match the tuning error
			// or the precision error without string matching.
			return "", nil, fmt.Errorf("%w: %w", ErrTuningInvalid, err)
		}
		return resolved, []string{fmt.Sprintf(
			"the KV cache precision was pinned to %s, so the planner did not escalate it", resolved)}, nil
	}

	offloaded := willOffload(layers, fit, budgets.deviceMiB)

	// Long contexts start the precision ladder at q8_0 rather than f16: the
	// KV cache dominates the footprint there, and halving it costs measured
	// ~1% of throughput while freeing device capacity. Below the threshold the
	// f16 cache is small enough to stay the lossless default.
	candidates := kvTypes
	if target > longContextKVThreshold {
		candidates = kvTypes[1:]
	}

	var lastDev, lastHost int64
	for _, candidate := range candidates {
		// The exactness flag is not consulted here: this loop only compares
		// footprints against budgets to pick a precision. Passing the real
		// `partial` still matters — a partial shape must be gated on the same
		// conservative bounds the plan will later report.
		deviceMiB, hostMiB, _, err := footprint(profile, packing, target, candidate, offloaded,
			partial, tuning.KVOffload == nil || *tuning.KVOffload,
			tuning.MMProjOffload == nil || *tuning.MMProjOffload)
		if err != nil {
			return "", nil, err
		}
		if budgets.fits(deviceMiB, hostMiB) {
			var notes []string
			if candidate != KVTypeF16 {
				notes = append(notes, fmt.Sprintf(
					"the KV cache was escalated to %s: a %d-token context needs %d MiB on the accelerator at f16 against a %d MiB budget, and %d MiB at %s",
					candidate, target, lastDev, budgets.deviceMiB, deviceMiB, candidate))
			}
			return candidate, notes, nil
		}
		lastDev, lastHost = deviceMiB, hostMiB
	}

	return "", nil, fmt.Errorf(
		"%w: a %d-token context in %s needs %d MiB on the accelerator and %d MiB in system RAM, against budgets of %d MiB and %d MiB",
		ErrMemoryPlanInfeasible, target, kvTypes[len(kvTypes)-1], lastDev, lastHost,
		budgets.deviceMiB, budgets.hostMiB)
}

// willOffload predicts whether any layer lands on an accelerator. Under fit the
// runtime decides at launch, so the prediction is "there is a device budget to
// decide with"; otherwise the pinned layer count decides.
func willOffload(layers *int, fit bool, deviceBudgetMiB int64) bool {
	if layers == nil {
		return fit && deviceBudgetMiB > 0
	}
	return *layers > 0
}

// partialOffload reports whether the plan pins a layer count that is neither
// "nothing" nor "everything" — the one shape memory.go's profile does not
// cover, its two measured residencies being the extremes. A plan under fit is
// never partial: the runtime chooses the count.
func partialOffload(profile ModelMemoryProfile, layers *int, fit bool) bool {
	if fit || layers == nil {
		return false
	}
	if *layers <= nglCPUOnly || *layers >= nglAllGPU {
		return false
	}
	return profile.OffloadableLayers <= 0 || *layers != profile.OffloadableLayers
}

// footprint projects the device and host memory one shape needs, in MiB,
// INCLUDING the vision projector's reserve (the projections are text-only and
// c0wrk always passes `--mmproj`). When the shape is a PARTIAL layer count the
// two figures are bounds — full-offload device, CPU-only host — both erring
// towards refusal, the safe direction for a gate.
func footprint(
	profile ModelMemoryProfile,
	packing Packing,
	ctx int,
	kv KVType,
	offloaded bool,
	partial bool,
	kvOffload bool,
	mmprojOffload bool,
) (deviceMiB, hostMiB int64, exact bool, err error) {
	deviceMiB, err = profile.ProjectDeviceMiB(packing, ctx, kv, offloaded)
	if err != nil {
		return 0, 0, false, err
	}
	hostMiB, err = profile.ProjectHostMiB(packing, ctx, kv, offloaded)
	if err != nil {
		return 0, 0, false, err
	}
	if partial {
		// Re-derive both sides as bounds: device from the full-offload
		// projection, host from the CPU-only one.
		deviceMiB, err = profile.ProjectDeviceMiB(packing, ctx, kv, true)
		if err != nil {
			return 0, 0, false, err
		}
		hostMiB, err = profile.ProjectHostMiB(packing, ctx, kv, false)
		if err != nil {
			return 0, 0, false, err
		}
	}

	cacheMiB, err := profile.KVCacheMiB(ctx, kv)
	if err != nil {
		return 0, 0, false, err
	}
	if offloaded && !kvOffload {
		// `-nkvo`: the cache leaves the accelerator for system RAM. The device
		// projection counted it, the host projection did not.
		deviceMiB -= cacheMiB
		hostMiB += cacheMiB
		if deviceMiB < 0 {
			deviceMiB = 0
		}
	}

	switch {
	case mmprojOffload && offloaded:
		deviceMiB += profile.MMProjReserveDeviceMiB
		hostMiB += profile.MMProjReserveHostMiB
	default:
		// Either the projector is not offloaded, or nothing is: its whole
		// worst-case reserve lands in system RAM.
		hostMiB += profile.MMProjReserveDeviceMiB + profile.MMProjReserveHostMiB
	}
	return deviceMiB, hostMiB, !partial, nil
}

// planFootprint re-projects the chosen shape so the plan carries the expected
// figures for the precision that actually won.
func planFootprint(
	profile ModelMemoryProfile,
	packing Packing,
	target int,
	kv KVType,
	layers *int,
	fit bool,
	partial bool,
	budgets budgets,
	tuning Tuning,
) (deviceMiB, hostMiB int64, bound bool, err error) {
	offloaded := willOffload(layers, fit, budgets.deviceMiB)
	deviceMiB, hostMiB, exact, err := footprint(profile, packing, target, kv, offloaded, partial,
		tuning.KVOffload == nil || *tuning.KVOffload,
		tuning.MMProjOffload == nil || *tuning.MMProjOffload)
	if err != nil {
		return 0, 0, false, err
	}
	return deviceMiB, hostMiB, !exact, nil
}

// shapeNotes records the non-default decisions the shape itself carries — the
// contract the UI relies on: a plan with nothing overridden explains nothing.
func shapeNotes(tuning Tuning, plan MemoryPlan, profile ModelMemoryProfile, b budgets, topology MemoryTopology) []string {
	var notes []string

	if plan.Fit {
		notes = append(notes, fmt.Sprintf(
			"the runtime sizes the layer count and the context itself (--fit on, no -ngl, a zero -c), held to a %d-token minimum; the plan expects at least %d MiB on the accelerator and %d MiB in system RAM",
			plan.FitMinContext, plan.ExpectedDeviceMiB, plan.ExpectedHostMiB))
	}
	if len(plan.Devices) > 0 {
		notes = append(notes, fmt.Sprintf(
			"the offload was pinned to %s (--device), which the runtime treats as a chosen offload and therefore sizes nothing itself",
			strings.Join(plan.Devices, ",")))
	}
	if plan.SplitMode != SplitModeAuto {
		notes = append(notes, fmt.Sprintf(
			"the multi-device split was pinned to %s (--split-mode), which the runtime treats as a chosen offload and therefore sizes nothing itself",
			plan.SplitMode))
	}
	if !plan.KVOffload {
		notes = append(notes, fmt.Sprintf(
			"the KV cache stays in system RAM (-nkvo), which moves %d MiB off the accelerator and onto the host",
			movedKVCacheMiB(profile, plan)))
	}
	if !plan.MMProjOffload {
		notes = append(notes, "--no-mmproj-offload keeps the vision projector's worst-case reserve in system RAM")
	}
	if allowance := splitAllowanceMiB(plan.GPUFamily); allowance > 0 {
		notes = append(notes, fmt.Sprintf(
			"the accelerator was classified as %s, whose device/host split this project has not measured, so %d MiB was held back from its %d MiB budget",
			plan.GPUFamily, allowance, b.deviceMiB+allowance))
	}
	if tuning.HostReserveGiB != nil {
		notes = append(notes, fmt.Sprintf(
			"the system-RAM reserve was overridden to %.1f GiB, leaving a %d MiB host budget out of %.1f GiB",
			*tuning.HostReserveGiB, b.hostMiB, topology.HostRAMGiB))
	}
	if tuning.Packing != "" {
		notes = append(notes, fmt.Sprintf("the weights packing was pinned to %s", tuning.Packing))
	}
	return notes
}

// movedKVCacheMiB is the cache size `-nkvo` relocates, re-derived from the
// plan's own precision and context (the profile is a pure function of both).
func movedKVCacheMiB(profile ModelMemoryProfile, plan MemoryPlan) int64 {
	ctx := plan.ContextSize
	if ctx == 0 {
		ctx = plan.FitMinContext
	}
	cache, err := profile.KVCacheMiB(ctx, plan.KVType)
	if err != nil {
		return 0
	}
	return cache
}

// ptrInt boxes an int, so a plan can distinguish "omit `-ngl`" (nil) from
// "`-ngl 0`" — the difference the fit-exclusivity rule turns on.
func ptrInt(v int) *int { return &v }

// ptrBool boxes a bool for the same reason: `Tuning.KVOffload == nil` means
// "unset", which is not the same as an explicit true.
func ptrBool(v bool) *bool { return &v }

// deref unboxes a layer count for a message. nil reads as 0, which is the
// honest rendering: under fit no layer count is emitted at all.
func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
