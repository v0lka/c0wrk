// Memory-aware launch planning: the pure decision layer between "what memory
// does this machine have" (topology.go), "what memory does this model need"
// (memory.go) and "what flags does the runtime get" (server.go's LaunchSpec).
//
// Before this file existed, `Resolve` answered a memory question it had no
// inputs for. Its whole signature was `(platform, backend, ramGiB)`, so the two
// things it emitted that decide whether a launch survives contact with the
// machine were derived from RAM alone:
//
//   - `-ngl` was a constant per platform/backend (`layersFor`: 99 or 0), and
//   - `-c` was a five-step RAM ladder (`contextSizeFor`).
//
// Neither is a memory decision. A 16 GiB laptop with a 24 GiB discrete card
// and a 128 GiB Apple Silicon machine sit in different ladder tiers and get
// the same `-ngl 99`, while the real question — does the weights + KV + compute
// footprint fit the pool it is about to be allocated in — was never asked.
// This file asks it, and it asks it with the two measured halves the package
// already had: `MemoryTopology`'s device/host budgets for capacity and
// `ModelMemoryProfile`'s projections for requirement.
//
// THE INVARIANT THIS FILE KEEPS: `Plan` is PURE. No I/O, no probing, no clock,
// no package state — every input arrives as a value and every output is a
// function of those values, exactly like `Resolve` and `buildTopology` before
// it. That is what keeps the whole
// (topology × profile × tuning × backend × GPU family) matrix table-testable
// without a runtime, a GPU or a filesystem. `TestPlanImportsNoIOPackage` and
// `TestPlanIsPure` assert it; the second half of the pair also pins that the
// plan ignores `MemoryTopology.ProbedAt`, the one clock-derived field in the
// inputs.
//
// ─────────────────────────────────────────────────────────────────────────────
// Fit exclusivity — the rule the whole planner is organised around
// ─────────────────────────────────────────────────────────────────────────────
//
// The pinned fork ships its own sizing pass, `--fit` (`common/fit.cpp`), which
// adjusts *unset* arguments so the launch fits the device memory it can see.
// It is ON by default, and it sizes both `-ngl` and `-c`. That makes it a
// direct competitor with this planner: if both decide, whichever loses is
// silently ignored — and the loser is not always the runtime.
//
// The fork resolves the competition by REFUSING the ambiguous shape. `fit.cpp`
// throws when `--fit` is asked to size a launch whose offload the caller has
// already pinned: the throw sites at `common/fit.cpp:183` and `:462`, `:466`,
// `:472`, `:477`, `:480`, `:483` are all "an argument --fit would have chosen
// is already set". Confirmed empirically against the pinned runtime: `-ngl 99`
// together with `--fit on` ABORTS the launch rather than degrading it. A
// planner that emitted both would turn every tuned launch into a crash.
//
// So exactly one of the two decides, and which one is a function of the
// override vocabulary alone:
//
//	Offload == Auto AND no Devices AND SplitMode == Auto
//	  → `-fit on`, OMIT `-ngl` entirely, pass a zero `-c`, and let fit size
//	    both the layer count and the context. c0wrk still chooses the KV
//	    precision (fit does not touch `-ctk`/`-ctv`) and still sets the floor
//	    fit must respect (`-fitc`).
//	ANY of {explicit Offload, Devices, SplitMode} set
//	  → `-fit off`, pass those values explicitly, and COMPUTE the context here
//	    from `ModelMemoryProfile` — because with fit off nothing else will.
//
// A `FitEnabled` override never breaks the rule: an explicit `false` forces the
// second branch, and an explicit `true` next to an explicit offload loses to
// the exclusivity rule and is recorded in `Notes` rather than silently dropped
// or spawned into a `fit.cpp` throw.
package embeddedllm

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

// DefaultFitMinContext is the `-fitc` floor c0wrk passes when fit sizes the
// context.
//
// It is **65536**, deliberately NOT the fork's own default of 4096
// (`-fitc, --fit-ctx N   minimum ctx size that can be set by --fit option,
// default: 4096`). The fork's floor is a "does not crash" floor, not a "is
// usable" one: the pinned model's own issue tracker reports empty and truncated
// answers as *the most common* complaint against it, and the documented
// workaround is `-n 16384` paired with "use `-c 65536`". Letting fit settle at
// 4096 would reproduce exactly that failure on every machine tight enough for
// fit to shrink the context — the agent would get a server that loads, answers
// and then returns nothing useful, which is worse than a refusal because
// nothing reports it.
//
// 65536 is also the tier `contextSizeFor` already grants a ≤71 GiB machine, so
// the fit floor and the explicit ladder agree at the middle of the range
// instead of the planner having two different ideas of "usable".
const DefaultFitMinContext = 65536

// upstreamFitMinContext is the fork's own `-fitc` default, kept only so the
// divergence above is quotable and testable rather than a remembered number.
const upstreamFitMinContext = 4096

// DefaultParallel is the `-np` value every plan emits unless overridden.
//
// It is **1**, not the fork's `-1` (= auto). Measured against the pinned fork:
//
//   - auto/4 inflates fit's own projection from **24450 MiB to 77297 MiB** —
//     `n_streams` becomes 4 when `kv_unified` is false, so the compute reserve
//     is sized per stream. On a machine whose budget sits between the two
//     numbers, `-np 4` is the difference between a launch and a `fit.cpp`
//     refusal.
//   - `-np` also SPLITS the context across slots: `-c 8192 -np 4` yields
//     `n_ctx_slot 2048`. A plan that reasoned about a 65536-token context
//     while passing `-np 4` would be provisioning four 16384-token slots —
//     i.e. the exact truncated-answer failure `DefaultFitMinContext` exists to
//     prevent.
//
// c0wrk serves one agent loop over one loopback socket and issues one request
// at a time per server, so a single slot is not a restriction here; it is the
// shape the memory model was measured in (`-np 1` in memory.go's reference
// command line).
const DefaultParallel = 1

// DefaultCtxCheckpoints is the `--ctx-checkpoints` value every plan emits
// unless overridden.
//
// It is **32**, which IS the fork's own default (`common/common.h`:
// `int32_t n_ctx_checkpoints = 32;`, rendered as `-ctxcp, --ctx-checkpoints N
// max number of context checkpoints to create per slot (default: 32)` at the
// pinned tag prism-b10735-842b188). Unlike DefaultFitMinContext and
// DefaultParallel this constant does not DIVERGE from the runtime — it pins
// the same figure in this struct, because the flag is rendered
// unconditionally: the runtime also reads the `LLAMA_ARG_CTX_CHECKPOINTS`
// environment variable, and an inherited env override on the fork's side of
// the boundary would otherwise decide a memory-bearing setting (each
// checkpoint can hold a saved KV prefix) that no c0wrk layer chose. Rendering
// the value explicitly makes the launch shape a property of this struct
// rather than of the process environment.
//
// 0 is a legitimate operator choice ("never snapshot the KV prefix") and is
// passed through verbatim; see Tuning.CtxCheckpoints.
const DefaultCtxCheckpoints = 32

// runtimeCacheRAMDefaultMiB is the pinned fork's own `--cache-ram` default
// (`-cram, --cache-ram N   set the maximum cache size in MiB (default: 8192,
// -1 - no limit, 0 - disable)`). Unlike the two defaults above it is KEPT, not
// diverged from — but it stops being the answer the moment the measured
// budgets hold more than it: the prompt cache lives in memory the launch was
// already granted, and every prefix past the ceiling is re-prefilled from
// scratch, so a flat 8192 MiB on a machine with tens of GiB to spare turns
// each long agent step back into a cold start. It is the FLOOR of the derived
// ceiling (see planPromptCacheCeiling): the planner only ever raises the
// cache above it, never lowers it below it.
const runtimeCacheRAMDefaultMiB = 8192

// Policy margins for the device/host SPLIT, which memory.go documents as
// measured on **Metal only**. The weight, cache, recurrent-state and compute
// terms are model+runtime properties and travel across backends; the division
// of those terms between device and host is a property of each backend's repack
// support, and on a backend nobody measured it the projection is an estimate.
// These allowances are the planner's way of saying so: they are POLICY, not
// measurement, deliberately conservative, and they are the only numbers in this
// file that are not derived from a cited figure.
const (
	// unmeasuredSplitAllowanceMiB is subtracted from the device budget on a
	// recognized GPU family whose split has not been measured (every CUDA,
	// ROCm, Vulkan and Intel part).
	unmeasuredSplitAllowanceMiB int64 = 1024
	// unknownGPUAllowanceMiB is the larger deduction for a device the
	// classifier does not recognize at all: an unknown part is unknown in both
	// directions (split AND compute-reserve behaviour).
	unknownGPUAllowanceMiB int64 = 2048
)

// ErrMemoryPlanInfeasible reports a launch the measured budgets cannot serve at
// ANY modelled KV precision. It is a refusal with the arithmetic in it: the
// message names the footprint, the budget and the precision it was measured at,
// so the UI can show why instead of a bare "not enough memory".
var ErrMemoryPlanInfeasible = errors.New("the embedded LLM does not fit this machine's measured memory budgets")

// ErrTuningInvalid reports an override the planner cannot honour: a context
// outside the modelled range, a partial layer count that is negative, or a
// packing this model has no measured residency for. It is checked BEFORE any
// feasibility arithmetic, so a typo is reported as a typo and never as a
// machine that is too small.
var ErrTuningInvalid = errors.New("invalid embedded-LLM memory tuning")

// ─────────────────────────────────────────────────────────────────────────────
// The combined memory gate
// ─────────────────────────────────────────────────────────────────────────────
//
// ADR-066 D6 refused an install below a flat 16 GiB of SYSTEM RAM. That single
// number measured one pool, and it was wrong in both directions at once:
//
//   - 8 GiB of RAM beside a 32 GiB accelerator was refused outright, even
//     though the weights, the KV cache and the compute reserve all live in
//     VRAM and the host carries only the measured 598 MiB of spill plus one
//     CPU compute buffer;
//   - 32 GiB of RAM beside an 8 GiB accelerator was ADMITTED, then handed a
//     full-offload `-ngl 99` plan nobody had priced against the card — an
//     out-of-memory at load, with no retry.
//
// Both failures come from measuring one pool. The gate here prices the launch
// against BOTH and spends them the way the machine actually does: ONE shared
// pool when the accelerator's memory aliases system RAM (`MemoryTopology.
// Unified`), two independent budgets when it does not. It replaces the floor
// rather than sitting beside it, so there is no RAM threshold left to drift
// out of agreement with the measurements — the floor is now wherever the
// smallest shape `memory.go` has measured stops fitting, which is a fact about
// the model and the machine rather than a round number.

// ErrInsufficientMemory is the typed refusal of the combined gate: no shape
// `memory.go` has measured — neither residency extreme, nor any packing, nor
// any modelled KV precision — fits this machine's memory. Every refusal is an
// `*InsufficientMemoryError` wrapping it, so a caller can match the sentinel
// without parsing a message and still show the arithmetic.
var ErrInsufficientMemory = errors.New("the embedded LLM does not fit this machine's memory")

// InsufficientMemoryError is the refusal WITH the arithmetic in it. Its message
// names both pools and both numbers — what the model needs and what the machine
// has — because "not enough memory" is not actionable while "needs 9.4 GiB of
// device memory, 6.9 GiB available" tells a user which part to change.
//
// It unwraps to a chain rather than to one sentinel: `ErrInsufficientMemory`
// always, `ErrMemoryPlanInfeasible` always (the two name the same fact at
// different layers — the gate and the planner — and a caller may match either),
// and the deprecated `ErrInsufficientRAM` only when the HOST pool is one of the
// pools that overflowed. That last condition is what keeps the old sentinel
// honest: a refusal caused by a small accelerator must not report itself as
// insufficient system RAM.
type InsufficientMemoryError struct {
	// Packing, Context, KVType and Offloaded describe the shape the numbers
	// belong to: the closest-fitting one this machine was offered.
	Packing   Packing
	Context   int
	KVType    KVType
	Offloaded bool

	// DeviceNeedMiB and DeviceHaveMiB are the accelerator side. Both are 0 for
	// a machine with no accelerator, which the message says in words rather
	// than leaving as two zeroes to interpret.
	DeviceNeedMiB int64
	DeviceHaveMiB int64
	// Device is the tri-state of the accelerator axis — see deviceAxisState.
	Device deviceAxisState

	// HostNeedMiB and HostHaveMiB are the system-RAM side, and
	// HostReserveMiB is the reserve HostHaveMiB was derived with, so the
	// message can state it rather than imply it.
	HostNeedMiB    int64
	HostHaveMiB    int64
	HostReserveMiB int64
	HostRAMGiB     float64

	// Unified reports whether the two pools are the same bytes. It changes the
	// message, because on a unified machine a device figure and a host figure
	// are two views of one pool and reading them as additive would double it.
	Unified bool
}

// Error renders both pools with both numbers, in GiB, matching llama.cpp's own
// habit of reporting a memory breakdown the reader can check against a
// `--list-devices` line.
//
// A unified machine whose accelerator footprint is non-zero gets one extra
// clause. Its two footprints are drawn from the SAME bytes, so a reader who
// compared the two rendered pairs independently would conclude the launch fits
// when the pool is over — the double-counting `MemoryTopology`'s clamp exists
// to prevent, arriving from the other direction. The clause states the sum and
// the shortfall. A machine with NO accelerator gets no such clause: there is
// nothing to sum with.
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

// shortfall renders an overflow. Under 1 GiB it is rendered in MiB, because a
// refusal that reads "needs 8.0 GiB, 8.0 GiB available" tells the user nothing:
// the two figures differ by an amount one decimal place cannot show, and the
// difference is the whole reason for the refusal.
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

// mibGiB renders a whole-MiB figure as GiB with one decimal, the precision a
// capacity message needs and no more: 9.4 GiB is a figure a user can compare
// against the "8 GB" on a box, while 9623 MiB is not.
func mibGiB(mib int64) string { return gibString(float64(mib) / mibPerGiB) }

// gibString renders a GiB figure with one decimal.
func gibString(gib float64) string { return fmt.Sprintf("%.1f GiB", gib) }

// deviceAxisState is the tri-state of the gate's accelerator side. It is
// tri-state for the same reason `BudgetFit` and `CPUFeature` are: "there is no
// accelerator" and "there is an accelerator whose memory nobody measured" are
// different facts with OPPOSITE consequences — the first is a refusal, the
// second must not be — and a bool's zero value would assert one of them about
// every caller that measured nothing.
type deviceAxisState int

const (
	// deviceAbsent means there is no accelerator memory to offload to, so the
	// model has to be host-resident. Known, and gateable.
	deviceAbsent deviceAxisState = iota
	// deviceKnown means the device budget is a number this gate may spend:
	// either a probed one, or a unified pool that IS the host budget.
	deviceKnown
	// deviceUnreadable means an accelerator with INDEPENDENT memory exists but
	// nothing measured its size. This is deliberately NOT fatal: refusing here
	// would reproduce the exact failure the gate exists to remove (a small-RAM
	// machine with a big card turned away), so the gate prices the host pool
	// alone and records the degradation in Notes.
	deviceUnreadable
)

// gateBudgets is the capacity side of one gate decision: the two budgets, how
// much RAM was held back to derive the host one, and what is known about the
// accelerator.
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
// budget for a machine of this size, in GiB. It is the SAME derivation
// topology.go applies to a probed machine — the larger of a 4 GiB floor and
// 1/8 of RAM — exposed so a caller with no topology (the synchronous
// pre-install refusal) and a caller with one cannot disagree about the reserve.
//
// The reserve is not slack. It has three named tenants, and a launch that eats
// into it does not merely slow down:
//
//   - the OS and the window server, which is what the 4 GiB floor is sized for;
//   - c0wrk itself — the Wails webview, the Go heap, the PTYs and the session
//     store all live in this process while the model is resident;
//   - the VECTOR INDEX, which is the tenant that scales with the project: the
//     ONNX Runtime session, the embedding model and the in-memory index of a
//     large workspace are all host-resident, and they are loaded whether or not
//     the embedded LLM is.
//
// The 1/8 ratio above the floor is what keeps the three covered on a big
// machine, where "everything else" is bigger too. `Tuning.HostReserveGiB`
// overrides it — replacing the derivation, not stacking on it.
func DefaultHostReserveGiB(ramGiB float64) float64 {
	if ramGiB <= 0 {
		return 0
	}
	reserve := hostReserveBytes(int64(ramGiB * gibibyte))
	return float64(reserve) / gibibyte
}

// gateBudgetsFor derives the two budgets the gate spends.
//
// With a probed topology it uses the topology's own numbers (which already
// carry the reserve and the unified clamp) and takes the GPU family's split
// allowance off the device side, exactly as `planBudgets` does — the gate and
// the planner must price the same machine or one of them is lying.
//
// WITHOUT one it derives the host budget from the RAM probe with the same
// reserve policy, and classifies the accelerator axis statically. That
// classification is the whole reason an unprobed machine is not simply refused:
// a backend whose memory is INDEPENDENT of host RAM (a discrete CUDA or ROCm
// card on amd64) leaves the device axis unreadable, while one whose memory IS
// host RAM (Apple Silicon's Metal, and Vulkan — which `classifyUnified` also
// resolves to unified when unsure) can be priced from the RAM probe alone.
func gateBudgetsFor(in ResolveInput, backend Backend, family GPUFamily) (gateBudgets, error) {
	// A set override REPLACES the derived reserve; an unset one leaves it alone.
	// The conversion is range-checked (hostReserveBytesFromGiB) instead of relying
	// on Go's implementation-defined float→int behaviour, and NaN is refused here
	// rather than silently skipped by a `>= 0` test — an operator typo must not
	// vanish without a word.
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
		// probe carries, so passing the profile's copy as the fallback is not a
		// second opinion.
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
	// The axis follows `layersFor` — the plan's OWN offload policy — rather than
	// re-deriving one, because the gate must price the shape the plan will
	// actually emit. That coupling matters on Apple Silicon, where the arm64
	// archive IS the Metal build even when the effective backend came out as
	// `cpu`: a gate keyed on `backend.gpuAccelerated()` would see "no
	// accelerator" there and price a host-resident shape nobody launches.
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
		// host RAM — Apple Silicon, and Vulkan, which `classifyUnified`'s rule 6
		// also settles as unified when unsure (misclassifying a discrete card
		// only shrinks its budget, while misclassifying a unified pool invents
		// memory). The RAM probe therefore measured both pools, and the device
		// budget IS the host budget.
		g.device = deviceKnown
		g.unified = true
		g.deviceMiB = g.hostMiB
	}
	return g, nil
}

// backendHasIndependentVRAM reports whether a platform+backend pair implies an
// accelerator whose memory is NOT host RAM, which is the only shape where a
// small RAM total can coexist with a model that does not fit in it.
//
// It is the static half of `classifyUnified`: CUDA and ROCm archives are
// published for amd64 only, and `classifyUnified`'s rule 4 reads a CUDA or HIP
// inventory on any other architecture as a Jetson/Grace-Hopper class SoC whose
// GPU shares system memory. Vulkan is deliberately NOT here — it serves
// discrete cards and integrated GPUs alike, and `classifyUnified`'s rule 6
// already settles "unsure" as unified, because misclassifying a discrete card
// only shrinks its budget while misclassifying a unified pool invents memory.
func backendHasIndependentVRAM(platform string, backend Backend) bool {
	if platformArch(platform) != "amd64" {
		return false
	}
	return backend.IsCUDA() || backend == BackendROCm
}

// gateOverflow prices one (device, host) footprint pair against the budgets and
// returns how far over it is, in MiB. Zero means the shape fits.
//
// The unified case is the reason this is a function and not two comparisons:
// when the pools are the same bytes, the two footprints are ADDITIVE against
// one pool, and checking each against its own budget would pass a launch that
// needs twice the machine's memory. The device term is still checked on its
// own, because the topology's margin and the family's split allowance live in
// the device budget alone.
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
	// notes carries the degradations the gate had to accept, in
	// operator-facing prose.
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
// when there is one, otherwise the `-fitc` floor. It is never the RAM ladder's
// larger tiers, because the gate answers "can this machine serve the model at
// all", and the smallest context c0wrk is willing to serve IS the floor —
// `DefaultFitMinContext`'s doc comment records why going below it reproduces
// the pinned model's most-reported failure.
func gateContext(tuning Tuning) int {
	if tuning.Context.Mode == ContextExact && tuning.Context.Tokens > 0 {
		return tuning.Context.Tokens
	}
	if tuning.FitMinContext != nil && *tuning.FitMinContext > 0 {
		return *tuning.FitMinContext
	}
	return DefaultFitMinContext
}

// gatePackings is the packing set the gate prices: the one the machine's own
// decision yields, plus PTQ1_0 when that is not already it. It mirrors
// `planMemory`'s single bounded downgrade exactly, so the gate never admits a
// machine on a packing the planner is unable to reach.
//
// An operator-pinned packing is the whole set: nothing is re-decided for a
// quantization the operator named.
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
// `resolveWith` — before any asset is planned and before any directory is
// created — so an undersized machine downloads nothing and leaves nothing
// behind.
//
// It searches the modelled shape space (packing × KV precision × residency
// extreme) at the smallest context c0wrk serves, and admits the machine when
// ANY shape fits. That is the right question: the gate decides whether a
// machine is viable at all, while `Plan` decides which viable shape is best.
//
// A refusal is an `*InsufficientMemoryError` naming both pools with both
// numbers. An unreadable device budget is NOT a refusal — see deviceUnreadable.
func memoryGate(in ResolveInput, backend Backend, family GPUFamily, profile ModelMemoryProfile) (gateVerdict, error) {
	g, err := gateBudgetsFor(in, backend, family)
	if err != nil {
		// An override no budget can be derived from (NaN, infinite, or above the
		// ceiling). Refusing here is what makes the conversion total downstream:
		// every later float→int of this knob is inside range by construction.
		return gateVerdict{}, err
	}

	ctx := gateContext(in.Tuning)
	packings := gatePackings(in, backend, family)
	kvSet := gateKVTypes(in.Tuning)

	// The residency extremes worth pricing. A machine with no accelerator
	// memory has only one, and a machine whose accelerator could not be
	// measured is priced as if it were device-resident (which is what --fit
	// will make it) and gated on the host pool alone.
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
		// or an unsupported precision. Report it as the planner does rather
		// than inventing numbers.
		return gateVerdict{}, fmt.Errorf("%w: no modelled shape could be priced for this machine",
			ErrMemoryNotMeasured)
	}
	if best.overflow == 0 {
		return gateVerdict{budgets: g, viable: best, notes: notes}, nil
	}
	return gateVerdict{}, insufficientMemory(g, best, ctx)
}

// closestAttempt prices every shape in the modelled space and returns the one
// that overflowed the budgets by the least. An overflow of 0 means that shape
// FITS, and because the space is walked least-lossy first (the packing set
// starts at the machine's own, the precision set at f16, the residency set at
// full offload) the first fit found is also the best one.
//
// A shape the profile has no measurement for is SKIPPED rather than refused:
// the gate is searching for something that works, and an unmeasured candidate
// is not this machine's problem. `Plan` still refuses an operator-PINNED
// unmeasured packing outright, which is a different question.
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
// planned and no launch shape resolved. It is the gate `Resolve` runs first,
// exposed for a caller that must refuse BEFORE it commits to an install — the
// desktop RPC, which refuses synchronously so a rejection is the answer to the
// user's click rather than a toast ten minutes into a download.
//
// It is pure like everything else in this file: no I/O, no probing, no clock.
// A nil `Topology` is legal and means "no device probe answered", which takes
// the derived-budget path rather than failing.
func CheckMemoryBudget(in ResolveInput) error {
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

// SplitMode is the `-sm`/`--split-mode` override. Its spellings are the
// runtime's own (`{-sm, --split-mode {none,layer,row,tensor}}`); the zero value
// means "unset", which is what leaves the fit-exclusivity rule free to hand
// sizing to the runtime.
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

// Tuning is the operator-facing override vocabulary for one memory plan.
//
// EVERY field distinguishes "unset" from "set to the default value", because
// the difference is load-bearing: an unset `-ngl` lets the runtime's fit pass
// size the launch, while an `-ngl` that happens to equal the value fit would
// have chosen turns fit OFF (see the exclusivity rule in this file's header).
// Fields with a natural zero sentinel use it (`KVType`, `Packing`, `SplitMode`,
// `Devices`, `Context`, `Offload`); the rest are pointers.
//
// The zero value is the all-Auto plan: fit sizes layers and context, the KV
// precision escalates to whatever fits, one slot, and every budget comes from
// the probed topology.
type Tuning struct {
	// Context overrides `-c`. ContextExact pins it; ContextAuto leaves it to
	// the planner (which passes a zero `-c` under fit, so the runtime sizes it).
	Context ContextTuning
	// KVType overrides BOTH `-ctk` and `-ctv` (one value for both, because
	// memory.go's KVType documents that a mixed pair silently drops to CPU
	// flash attention). The zero value is Auto: escalate f16 → q8_0 → q4_0
	// until the target context fits. An unmodelled precision is refused by
	// ParseKVType, so Auto can never be confused with a choice.
	KVType KVType
	// Offload overrides `-ngl`. See OffloadMode.
	Offload Offload
	// FitEnabled overrides `-fit`. nil lets the exclusivity rule decide; an
	// explicit false forces `-fit off` (and the planner computes the context);
	// an explicit true next to an explicit offload loses to the exclusivity
	// rule and is recorded in Notes.
	FitEnabled *bool
	// FitTargetMiB overrides `-fitt`, the per-device margin fit leaves free.
	// nil (and an explicit 0) omit the flag and keep the runtime's own default
	// of 1024 MiB. It is only emitted under fit, and it is bounded by
	// MaxTuningMiB.
	FitTargetMiB *int
	// FitMinContext overrides `-fitc`, the smallest context fit may settle on.
	// nil means DefaultFitMinContext (65536, NOT the runtime's 4096 — see that
	// constant). It is only emitted under fit, and it doubles as the context
	// the feasibility gate checks, because a fit run that cannot reach its own
	// floor aborts.
	FitMinContext *int
	// KVOffload is `-kvo`/`-nkvo`. nil and an explicit true keep the KV cache
	// on the device with the layers; false passes `-nkvo` and leaves the cache
	// in system RAM, which trades device memory for host memory and for
	// attention bandwidth.
	KVOffload *bool
	// MMProjOffload is `--mmproj-offload`/`--no-mmproj-offload`. nil and an
	// explicit true keep the vision projector's worst-case reserve on the
	// device; false moves it to system RAM.
	MMProjOffload *bool
	// Packing overrides the weights quantization. The zero value means "the
	// backend's packing" (`packingFor`), which is the resolution outcome
	// ADR-066 D2 describes; a set value must be one the profile has measured
	// residency for, or the plan is refused with ErrMemoryNotMeasured rather
	// than projected from a file size.
	Packing Packing
	// Parallel overrides `-np`. nil means DefaultParallel (1 — see that
	// constant for the two measurements behind it); a set value is bounded by
	// MaxTuningParallel.
	Parallel *int
	// CacheRAMMiB overrides `-cram`, the prompt cache ceiling. nil leaves the
	// ceiling to the planner: it keeps the runtime's own default unless the
	// measured budgets hold more than that beyond the expected footprint (see
	// planPromptCacheCeiling); 0 would disable the cache and is passed through
	// verbatim, because disabling it is a legitimate choice. A set value is
	// bounded by MaxTuningMiB.
	CacheRAMMiB *int
	// CtxCheckpoints overrides `--ctx-checkpoints`, the per-slot KV snapshot
	// count. nil means DefaultCtxCheckpoints (32, the runtime's own figure —
	// see that constant for why the pin exists); 0 disables the snapshots
	// verbatim, because disabling them is a legitimate choice. A set value is
	// bounded by MaxTuningCtxCheckpoints.
	CtxCheckpoints *int
	// CacheIdleSlots is `--cache-idle-slots`/`--no-cache-idle-slots`. nil and
	// an explicit true keep the runtime's default of saving idle slots to the
	// prompt cache on a new task; false passes `--no-cache-idle-slots`.
	CacheIdleSlots *bool
	// HostReserveGiB overrides the RAM kept out of the host budget. nil keeps
	// the topology's own derivation (the larger of a 4 GiB floor and 1/8 of
	// RAM). It is a planner-side budget knob, NOT a runtime flag: the pinned
	// fork has no `--host-reserve`.
	//
	// A SET value is always converted, and the conversion is range-checked:
	// NaN, an infinite or negative figure, and anything above
	// MaxTuningHostReserveGiB are refused as ErrTuningInvalid by both the gate
	// and the planner rather than silently ignored. The ceiling exists because a
	// float→int conversion the result type cannot represent is
	// implementation-defined in Go, and the amd64 result would fail the memory
	// gate OPEN — see hostReserveMiB.
	HostReserveGiB *float64
	// Devices overrides `-dev`, the comma-separated offload target list. Any
	// entry forces fit off: naming the devices is pinning the offload.
	Devices []string
	// SplitMode overrides `-sm`. Anything but SplitModeAuto forces fit off.
	SplitMode SplitMode
}

// explicitOffloadShape reports whether any override pins the offload, which is
// the condition that forces `-fit off` and hands context sizing back to this
// planner.
func (t Tuning) explicitOffloadShape() bool {
	return t.Offload.explicit() || len(t.Devices) > 0 || t.SplitMode != SplitModeAuto
}

// ─────────────────────────────────────────────────────────────────────────────
// GPU families
// ─────────────────────────────────────────────────────────────────────────────
//
// The GPUFamily vocabulary and its description classifier live in compat.go,
// which owns them for the packing decision and the compatibility guards. This
// file is a second CONSUMER of the same answer, and it consumes it for a
// different reason: memory.go's profile was measured on ONE family (Apple
// Silicon / Metal), so the family is what prices the uncertainty of a
// device/host split nobody measured — see splitAllowanceMiB. Resolve classifies
// a probed inventory with ClassifyGPUs before planning, so a plan and the
// packing decision can never disagree about which silicon they are for.

// splitAllowanceMiB is the device-budget deduction a family's unmeasured split
// costs. Apple Silicon is the measured family and pays nothing; a recognized
// family nobody measured pays unmeasuredSplitAllowanceMiB; an unrecognized part
// pays unknownGPUAllowanceMiB, because unknown means unknown in both
// directions (the split AND the compute-reserve behaviour).
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
// two footprints the plan expects to see, and the human-readable reason for
// each non-default decision.
//
// It is a description, not a command line. `LaunchSpec.Args` in server.go
// remains the only place a llama-server argv is assembled; this struct carries
// the values that argv is built from, with omission expressed as a nil pointer
// or a zero rather than as a pre-rendered flag.
type MemoryPlan struct {
	// Fit reports whether the runtime's own `--fit` pass sizes the layer count
	// and the context. When true, Layers is nil and ContextSize is 0, and the
	// launcher MUST omit `-ngl` and pass a zero `-c` — see the exclusivity
	// rule. FitArg renders the literal `-fit` value either way.
	Fit bool `json:"fit"`
	// FitTargetMiB is `-fitt`. Zero omits the flag and keeps the runtime's own
	// 1024 MiB default. Only meaningful when Fit is true.
	FitTargetMiB int `json:"fit_target_mib"`
	// FitMinContext is `-fitc`, the floor fit must respect. Always
	// DefaultFitMinContext or an explicit override — never the runtime's 4096.
	// Only meaningful when Fit is true.
	FitMinContext int `json:"fit_min_context"`
	// Layers is `-ngl`. NIL MEANS OMIT THE FLAG ENTIRELY, which is what lets
	// fit choose it; a non-nil value pins the offload and is accompanied by
	// Fit == false.
	Layers *int `json:"layers,omitempty"`
	// ContextSize is `-c`. Zero means "fit sizes it" and only ever occurs with
	// Fit == true; with Fit == false the planner has computed it and it is
	// always positive.
	ContextSize int `json:"context_size"`
	// KVType is the resolved `-ctk`/`-ctv` precision. Never empty: Auto is
	// resolved to a concrete member of memory.go's closed set, and the
	// escalation that got there is recorded in Notes.
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
	// CacheRAMMiB is `-cram`. A non-nil value — including 0, which disables
	// the prompt cache — is passed through verbatim. Nil omits the flag: the
	// planner emits a derived ceiling (planPromptCacheCeiling) only when the
	// measured budgets hold MORE than the runtime's own default beyond the
	// expected footprint, so an omitted flag always means "the runtime's own
	// number already covers the spare memory".
	CacheRAMMiB *int `json:"cache_ram_mib,omitempty"`
	// CtxCheckpoints is `--ctx-checkpoints`, the per-slot KV snapshot count.
	// Always DefaultCtxCheckpoints unless overridden — 0 (snapshots disabled)
	// included verbatim, because disabling them is a legitimate choice.
	CtxCheckpoints int `json:"ctx_checkpoints"`
	// CacheIdleSlots is the resolved `--cache-idle-slots`/
	// `--no-cache-idle-slots`. True unless explicitly disabled: idle slots are
	// saved to the prompt cache on a new task, the runtime's own default.
	CacheIdleSlots bool `json:"cache_idle_slots"`
	// Devices is `-dev`. Empty omits the flag.
	Devices []string `json:"devices,omitempty"`
	// SplitMode is `-sm`. SplitModeAuto omits the flag.
	SplitMode SplitMode `json:"split_mode,omitempty"`
	// GPUFamily is the family the plan was classified as, echoed so a support
	// bundle carries the reason behind the allowance in the budgets below.
	GPUFamily GPUFamily `json:"gpu_family,omitempty"`
	// DeviceBudgetMiB and HostBudgetMiB are the budgets the plan was gated
	// against: the topology's own, after the family's split allowance (device)
	// and after any HostReserveGiB override (host).
	DeviceBudgetMiB int64 `json:"device_budget_mib"`
	HostBudgetMiB   int64 `json:"host_budget_mib"`
	// ExpectedDeviceMiB and ExpectedHostMiB are the projected footprints of the
	// chosen shape, INCLUDING the vision projector's reserve (memory.go's two
	// projections are text-only, and c0wrk always passes `--mmproj`). They are
	// what a UI shows and what a later live check compares a real allocation
	// against. When the offload is a PARTIAL layer count neither figure is
	// exact: device is the full-offload upper bound and host the CPU-only upper
	// bound, and Notes says so.
	ExpectedDeviceMiB int64 `json:"expected_device_mib"`
	ExpectedHostMiB   int64 `json:"expected_host_mib"`
	// Notes is the human-readable "why", one entry per non-default decision.
	// It is surfaced in the UI verbatim, so every entry is a sentence an
	// operator can act on and none of them is a debug dump.
	Notes []string `json:"notes,omitempty"`
}

// FitArg renders the literal value of the `-fit` flag for this plan. It exists
// so the exclusivity rule is checkable as the rule is stated ("`-fit on` and no
// `-ngl`") rather than through a boolean the reader has to re-derive.
func (p MemoryPlan) FitArg() string {
	if p.Fit {
		return "on"
	}
	return "off"
}

// EmitsLayers reports whether the launcher must pass `-ngl` at all. Under fit
// it must not: an explicit `-ngl` beside `--fit on` is the ambiguous shape the
// fork's `fit.cpp` refuses.
func (p MemoryPlan) EmitsLayers() bool { return p.Layers != nil }

// OffloadsToDevice reports whether the plan puts any model layers on an
// accelerator. It is the boolean every projection needs, and under fit it is a
// PREDICTION (fit decides at launch) derived from whether the topology reported
// any usable device budget at all.
func (p MemoryPlan) OffloadsToDevice() bool {
	return p.Layers == nil && p.Fit && p.DeviceBudgetMiB > 0 ||
		p.Layers != nil && *p.Layers > 0
}

// Plan resolves a launch shape from measured inputs. It is PURE: no I/O, no
// probing, no clock, no package state (see this file's header).
//
// topology is the capacity side — normally the value `ProbeDevices` returned,
// whose zero value means "unknown" and must NOT be passed here (a caller with
// no topology takes the derived-budget path in `Resolve` instead). profile is
// the requirement side, normally `PinnedMemoryProfile()`. backend selects the
// default packing, and family prices the uncertainty of a split nobody measured.
//
// A shape that does not fit is DEGRADED before it is refused. `planShape` is
// the single-pass planner; Plan wraps it in a bounded ladder of relaxations,
// least-lossy first, so a machine that can serve the model only in a smaller
// shape gets that shape instead of an error. That ladder is what turns the old
// "32 GiB of RAM beside an 8 GiB accelerator is admitted and then OOMs at
// load" into a plan that fits: see relaxations.
//
// Refusals, in order:
//   - an override that cannot be honoured → ErrTuningInvalid
//   - a packing with no measured residency → ErrMemoryNotMeasured
//   - a footprint no relaxation brings under budget → ErrInsufficientMemory
//     (carrying both pools and both numbers), which also unwraps to
//     ErrMemoryPlanInfeasible
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
	// would change. Honouring the pin and reporting the refusal beats quietly
	// launching a different shape than the one asked for — but the refusal is
	// still the typed both-pools one, because "which pool is short" is a fact
	// about the machine and not about who chose the shape.
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
// fit. `apply` returns the Tuning to re-plan with, and `note` is the
// operator-facing sentence appended to the plan that won — a degraded install
// states its degradation rather than leaving the user to infer it from a
// smaller context.
type relaxation struct {
	apply func(Tuning) Tuning
	note  string
}

// relaxations is the degradation ladder, LEAST-LOSSY FIRST. Each rung trades
// something different, and the order is the order of what c0wrk would rather
// keep:
//
//  1. KV cache and the vision projector's reserve to host RAM (`-nkvo` +
//     `--no-mmproj-offload`). Costs attention bandwidth; keeps the weights, the
//     full context and f16 precision.
//  2. the context, down to `DefaultFitMinContext`. Costs reach; the floor is
//     never below 65536 because that is where the pinned model's most-reported
//     failure starts (see DefaultFitMinContext).
//  3. both.
//  4. host residency (`-ngl 0`, fit off). Costs the accelerator entirely — the
//     model runs from system RAM — but it is a MEASURED shape
//     (`HostWeightsMiB`, `ComputeHostCPUOnlyMiB`), whereas a PARTIAL offload is
//     not: the profile has no measurement for it, and pricing one would mean
//     guessing at a split. A slow launch that loads beats a fast plan that
//     cannot be verified to fit.
//  5. host residency with the reduced context.
//
// Every rung re-runs `planKVType`'s own precision ladder, so the rungs compose
// with the KV escalation rather than replacing it.
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

// spillKVToHost moves the KV cache and the vision projector's reserve off the
// accelerator. It leaves an operator-pinned half alone, which is why
// `relaxations` gates the rung on either knob still being unset.
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
// turns fit off and hands context sizing back to this planner.
//
// It deliberately leaves `KVOffload` and `MMProjOffload` alone: with nothing
// offloaded there is no device side for either to move away from, and pinning
// them would add two Notes asserting a relocation that did not happen.
func hostResident(tuning Tuning) Tuning {
	tuning.Offload = Offload{Mode: OffloadCPU}
	return tuning
}

// shrinkContextToFloor pins the context to DefaultFitMinContext: the `-fitc`
// floor under fit, and an exact `-c` on the planner-computed path. It never
// goes below the floor — see DefaultFitMinContext for the failure that lives
// down there.
func shrinkContextToFloor(tuning Tuning) Tuning {
	tuning.FitMinContext = ptrInt(DefaultFitMinContext)
	tuning.Context = ContextTuning{Mode: ContextExact, Tokens: DefaultFitMinContext}
	return tuning
}

// shrinkTarget is the context a relaxation would have to beat for the
// context rung to be worth trying: the plan's own target, derived the same way
// `planTargetContext` derives it.
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
// re-prices the modelled shape space against the planner's own budgets and
// reports the attempt that came CLOSEST to fitting, which is the one a user can
// act on: naming a shape that was never near the budget would only confuse.
//
// The search is the same one `memoryGate` runs, so a gate that admitted a
// machine and a planner that then refused it cannot disagree about what was
// tried — they disagree only about which budgets were spent, and that
// difference is exactly what a refusal should surface.
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
		// before it ever asks for a refusal message. Propagating it keeps this
		// builder total rather than inventing budgets to describe.
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
		// The operator pinned the residency the refusal is about, so pricing a
		// shape they ruled out would report numbers for a launch nobody asked
		// for.
		shapes = []bool{true}
	case tuning.Offload.Mode == OffloadCPU:
		shapes = []bool{false}
	case OffloadLayers == tuning.Offload.Mode:
		// A partial count is the one shape the profile has no measurement for;
		// price both extremes, which is the bound `footprint` already reports.
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
	// does not cover, and both consumers must agree on whether they are looking
	// at a measurement or at a bound.
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
	// (held to the `-fitc` floor — see planTargetContext), NOT "fit decides":
	// `--fit` adjusts only UNSET arguments, so a rendered `-c` is left alone
	// and fit sizes the offload (`-ngl`) — while a zero would hand the context
	// to the runtime, which sizes it up to the model's full training context
	// regardless of available memory. An exact operator pin is honoured as-is.
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
// unset, the operator's figure verbatim otherwise — including 0, which
// disables the per-slot KV snapshots. A value outside 0..MaxTuningCtxCheckpoints
// is refused as ErrTuningInvalid rather than clamped or silently defaulted: a
// negative or absurd count is a typo, and defaulting it would plan a different
// launch than the one written — the same reason checkHostReserve refuses
// instead of skipping an unusable figure.
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

// planPromptCacheCeiling derives the `--cache-ram` value an unset
// `cache_ram_mib` leaves the planner free to choose: the memory the measured
// budgets hold BEYOND the launch's expected footprint, so the prompt cache
// grows with the machine instead of sitting at the runtime's flat
// runtimeCacheRAMDefaultMiB while tens of GiB the launch was already granted
// sit idle — and every prefix past the ceiling is re-prefilled from scratch.
//
// The leftover is drawn from the HOST budget. On a unified machine both
// expected footprints spend the SAME bytes (budgets.fits' rule), so the device
// footprint is subtracted as well; on a discrete machine the device pool is
// the accelerator's own and the host leftover is the whole story.
//
// The ceiling is emitted only when it EXCEEDS the runtime's own default: this
// path can only RAISE the cache, never lower it — a smaller ceiling is a
// decision the operator did not make, and the flag stays omitted so the
// runtime's own number applies. The emitted value is clamped to MaxTuningMiB
// so a probe that reported an absurd host figure still renders a value
// Validate accepts.
func planPromptCacheCeiling(b budgets, plan MemoryPlan) (value int, note string, ok bool) {
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
	// (project PQ2_0, and on ErrMemoryPlanInfeasible re-plan with an explicit
	// `tuning.Packing` of PTQ1_0) — see decidePacking.
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
	// unified reports that the two pools are the SAME bytes, which makes the two
	// footprints additive against one pool — see fits.
	unified bool
}

// fits reports whether a (device, host) footprint pair fits these budgets.
//
// The unified case is why this is a method and not two comparisons: it is
// gateBudgets.overflow's rule on the planner's side of the same arithmetic. When
// the pools are the same bytes, a shape whose two footprints EACH fit a pool can
// still need twice the machine's memory in sum, and accepting it is what turned a
// narrow unified-memory configuration into a failed launch — the KV escalation
// loop settled on a precision the runtime's own `--fit` pass then could not fit
// even at the `-fitc` floor, so the load aborted with the FitWarning marker
// instead of escalating to q8_0 as the gate's verdict would have.
//
// The device term is checked on its own as well, because the topology's margin
// and the family's split allowance live in the device budget alone.
func (b budgets) fits(deviceMiB, hostMiB int64) bool {
	if deviceMiB > b.deviceMiB || hostMiB > b.hostMiB {
		return false
	}
	if b.unified {
		return deviceMiB+hostMiB <= b.hostMiB
	}
	return true
}

// planBudgets derives the two numbers the gate spends, from the topology's own
// budgets plus this plan's overrides.
//
// The device budget loses the GPU family's split allowance: the topology's
// 1536 MiB margin covers allocator bookkeeping on the backend the profile was
// measured on, and nothing covers a repack split nobody measured.
//
// The host budget is the topology's own unless HostReserveGiB overrides it, in
// which case it is re-derived from the topology's RAM total — the override
// replaces the reserve policy, it does not stack on top of it. A SET override is
// always converted, and the conversion is range-checked (hostReserveMiB) because
// a float→int whose value the result type cannot represent is
// implementation-defined in Go: an unusable override (NaN, negative, or above
// MaxTuningHostReserveGiB) is refused as ErrTuningInvalid here exactly as the gate
// refuses it, rather than being silently skipped — which is what a `>= 0` test did
// to a NaN, making an operator typo vanish without a word.
//
// The topology's Unified flag is carried through so the budgets can be spent the
// way the memory gate spends them — see budgets.fits.
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

// hostReserveMiB converts the operator's host-reserve override (GiB) into the MiB
// every budget in this file uses.
//
// The check is explicit because Go leaves a float→int conversion whose value the
// result type cannot represent IMPLEMENTATION-DEFINED: this host saturates it to
// MaxInt64 (which yields a zero host budget and a fail-closed plan), while amd64
// conventionally produces the negative "indefinite value" — which would make
// `host = ram − reserve` enormous and fail the memory gate OPEN on the very knob
// meant to shrink it. NaN is refused rather than silently ignored, which is what
// the `>= 0` guards at the call sites did to it before: an operator typo
// vanished without a word.
func hostReserveMiB(gib float64) (int64, error) {
	if err := checkHostReserve(gib); err != nil {
		return 0, err
	}
	return int64(gib * mibPerGiB), nil
}

// hostReserveBytesFromGiB is hostReserveMiB in the byte unit the gate's own
// derivation uses. (topology.go's hostReserveBytes takes the RAM total in bytes
// and applies the default reserve policy; this one converts an operator override.)
func hostReserveBytesFromGiB(gib float64) (int64, error) {
	if err := checkHostReserve(gib); err != nil {
		return 0, err
	}
	return int64(gib * gibibyte), nil
}

// checkHostReserve refuses a reserve override no total conversion can represent.
// The ceiling is limits.go's MaxTuningHostReserveGiB, which backend/config
// enforces on the persisted value as well: a bound that exists at only one of the
// two layers is a bound an operator can walk around by hand-editing config.yaml.
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
// `ProbeDevices` returned. `MemoryTopology`'s own contract is that its zero
// value means "unknown", never "no memory", and a zero `DeviceBudgetBytes`
// beside a 24 GiB inventory is exactly that ambiguity: read literally it says
// "no accelerator memory", and every consumer would degrade a machine that has
// plenty.
//
// It derives the figures the way `buildTopology` does — the same reserve policy,
// the same device margin, the same unified clamp — so a normalized topology and
// a probed one are priced identically, and the derivation lives in one place
// instead of being repeated by each consumer that wants to be robust.
//
// Two limits keep it from inventing memory:
//
//   - `Unified` is taken as given, never re-classified. It is the field
//     topology.go names as the only authority on the memory model, and
//     re-deriving it here would need a platform key `Plan` does not take.
//   - a device pool that fits INSIDE the margin is left at 0. That is a
//     measurement — a card too small for the runtime's own bookkeeping — not an
//     omission, and deriving a budget for it would fabricate one.
//
// ramGiBFallback supplies the host total when the topology left it unset; 0
// means "no fallback", which is what `Plan` passes because it has no other
// source for one.
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
// it. Under fit the flag is OMITTED, not set to a value fit would have picked:
// setting it is what turns fit off (and, with `--fit on`, what makes the fork
// abort).
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
		// Clamped to the range the flag itself accepts. The runtime treats any
		// value at or above the model's offloadable layer count as "all", so
		// clamping here cannot lose an offload the operator asked for.
		layers := min(max(tuning.Offload.Layers, nglCPUOnly), nglAllGPU)
		return ptrInt(layers), []string{fmt.Sprintf(
			"the offload was pinned to %d layers (-ngl %d)", layers, layers)}
	case OffloadAuto:
	default:
		return ptrInt(nglCPUOnly), []string{
			"an unrecognized offload override was ignored; nothing is offloaded",
		}
	}

	// Auto with fit forced off (an explicit --fit off, or an explicit device
	// list / split mode): offload everything when there is a device to offload
	// to, and nothing when there is not.
	if deviceBudgetMiB > 0 {
		return ptrInt(nglAllGPU), nil
	}
	return ptrInt(nglCPUOnly), []string{
		"no accelerator memory was probed, so the model runs entirely from system RAM",
	}
}

// planTargetContext resolves the context the plan must be able to SERVE. Under
// fit that is the floor fit is held to (`-fitc`), because a fit run that cannot
// reach its own minimum aborts rather than degrading; otherwise it is the
// override, or the RAM ladder the explicit path has always used.
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
		// The fit path renders the SAME tier ladder the explicit path does
		// (planShape writes it into the spec), held to the `-fitc` floor: fit
		// sizes the offload, never the context. Without this floor a tier
		// below 65536 would reproduce the truncated-answer failure
		// DefaultFitMinContext exists to prevent.
		ladder := ladderContextFor(topology, profile)
		if fitMinContext > ladder {
			ladder = fitMinContext
		}
		return ladder, nil
	}
	return ladderContextFor(topology, profile), nil
}

// ladderContextFor is the RAM-tier context the explicit path has always used:
// the ladder value clamped to the model's own training context. It is ALSO the
// fit path's `-c` (see planShape): `--fit` adjusts only UNSET arguments, so a
// rendered `-c` is left alone and fit sizes the offload (`-ngl`) — while an
// omitted (zero) context hands the sizing to the runtime, which picks the
// model's full training context regardless of available memory. That is the
// failure the vendor warns about explicitly and the shape that made every agent
// prompt a minutes-long prefill on memory-rich machines.
func ladderContextFor(topology MemoryTopology, profile ModelMemoryProfile) int {
	ladder := contextSizeFor(topology.HostRAMGiB)
	if profile.MaxContext > 0 && ladder > profile.MaxContext {
		ladder = profile.MaxContext
	}
	return ladder
}

// planKVType resolves `-ctk`/`-ctv`.
//
// An explicit override is honoured as given (and validated against memory.go's
// closed set, which refuses `q5_0` on the measured 8x long-context slowdown and
// the unverified `q4_1`/`iq4_nl`/`q5_1`). Auto is the ADAPTIVE path: try f16,
// and if the target context does not fit the device budget escalate to q8_0 and
// then q4_0, recording every escalation. The escalation is lossy in precision
// and free in throughput — the fork's own measurements put q8_0 and q4_0 within
// 1% of f16 on this model — so it is the right thing to spend first.
//
// When no precision fits, the refusal carries the arithmetic: which budget,
// which footprint, at the least-lossy precision that was tried.
//
// "Fits" is budgets.fits, not two independent comparisons: on a unified-memory
// machine the two footprints are spent from ONE pool, so gating each against its
// own budget would accept a precision whose sum exceeds the machine — the exact
// case the memory gate's additive rule exists to refuse, and one that turned a
// narrow unified configuration into a failed launch instead of a degraded one.
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
			// (an override the planner cannot honour) or the precision error
			// (a KV type this model does not cover) without string matching.
			return "", nil, fmt.Errorf("%w: %w", ErrTuningInvalid, err)
		}
		return resolved, []string{fmt.Sprintf(
			"the KV cache precision was pinned to %s, so the planner did not escalate it", resolved)}, nil
	}

	offloaded := willOffload(layers, fit, budgets.deviceMiB)

	// Long contexts start the precision ladder at q8_0 rather than f16. The KV
	// cache dominates the footprint there (at 64 KiB/token, 131072 tokens is
	// 8 GiB at f16 — half the weights), and halving it costs measured 1% of
	// throughput on this model while freeing device capacity for the offload
	// fit pass to keep. Below the threshold the f16 cache is small enough to
	// stay the lossless default.
	candidates := kvTypes
	if target > longContextKVThreshold {
		candidates = kvTypes[1:]
	}

	var lastDev, lastHost int64
	for _, candidate := range candidates {
		// The exactness flag is not consulted here: this loop only compares
		// footprints against budgets to pick a KV precision, and the bound a
		// partial offload produces is reported once, by planFootprint. Passing
		// the real `partial` matters even though the flag is ignored — a
		// partial shape must be gated on the same conservative bounds the plan
		// will later report, not on an exact figure it cannot produce.
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
// cover, because its two measured residency figures are the `-ngl 0` and
// full-offload extremes. A plan under fit is never partial: the runtime chooses
// the count, and the projection uses the extreme it will land on.
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
// INCLUDING the vision projector's reserve (memory.go's two projections are
// text-only and c0wrk always passes `--mmproj`).
//
// exact is false when the shape is a PARTIAL layer count, which the measured
// profile does not cover: the device figure is then the full-offload projection
// (an upper bound — fewer layers on the device cannot cost more device memory)
// and the host figure the CPU-only projection (an upper bound — some weights
// moving to the device cannot cost more host memory). Both bounds err towards
// refusal, which is the safe direction for a gate.
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

// shapeNotes records the non-default decisions the shape itself carries. Every
// entry is a deviation from the all-Auto plan, which is the contract the UI
// relies on: a plan with nothing overridden has nothing to explain.
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

// movedKVCacheMiB is the cache size `-nkvo` relocates, for the note above. It
// re-derives nothing: the plan already carries the precision and the context it
// was gated at, and the caller's profile is a pure function of both.
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
// "`-ngl 0`" (a pointer to zero) — the difference the fit-exclusivity rule
// turns on.
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
