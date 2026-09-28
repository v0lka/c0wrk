package embeddedllm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/v0lka/sp4rk/sysproc"
)

// This file supervises the one long-lived process the embedded LLM runs on:
// llama-server, bound strictly to loopback, with its built-in Web UI disabled
// (ADR-066 D12). Everything the command line depends on comes from the install
// record (manifest.json) plus the pure launch policy in resolve.go — the
// supervisor never invents a flag value. It MAY re-measure the hardware at load
// time, but only as a fail-soft refinement of the recorded plan: a probe that
// wedges, answers nothing or re-plans into a refusal leaves the install's
// decision in place, so a load can never fail because a driver query did.
//
// Two properties are load-bearing and easy to lose:
//
//   - Readiness is a successful GET /v1/models, NEVER /health. The fork's
//     server opens its listening socket and answers /health long before the
//     weights are in memory, so a /health-based Load would report "loaded"
//     for a model that then refuses or hangs the first request.
//   - A Load is idempotent and single-instance: at most one llama-server per
//     install, and a Load against an already-serving model is a no-op.
//
// Lifetime is owned here, not by a context: the spawned process deliberately
// does NOT use exec.CommandContext, because it must outlive the Load call that
// started it. It stops on Unload/Stop (graceful signal, then kill), on the
// idle timer (idle.go), or on app shutdown.

// LoopbackHost is the only address the embedded server is ever bound to. A
// non-loopback bind would expose an unauthenticated OpenAI-compatible endpoint
// (and, with the Web UI, a second agent) to the local network.
const LoopbackHost = "127.0.0.1"

// Supervision budgets. The weight load dominates: 6.7 GiB read from disk into
// RAM/VRAM takes tens of seconds on an NVMe machine and several minutes on a
// slow one, so the ready budget is generous and the poll interval is coarse —
// probing more often would not make the load faster.
const (
	// DefaultReadyTimeout bounds one Load, from spawn to the first /v1/models
	// answer. Exceeding it is a failure, not a longer wait: a server that has
	// not loaded the weights in this long is wedged.
	DefaultReadyTimeout = 15 * time.Minute
	// DefaultReadyPollInterval is the gap between readiness probes.
	DefaultReadyPollInterval = 500 * time.Millisecond
	// DefaultProbeTimeout bounds a SINGLE readiness probe, so one hung
	// connection cannot consume the whole ready budget.
	DefaultProbeTimeout = 10 * time.Second
	// DefaultStopTimeout is the graceful window between the termination signal
	// and the kill.
	DefaultStopTimeout = 10 * time.Second
	// DefaultLoadProbeTimeout bounds the OPTIONAL device probe a Load runs
	// before it decides the launch shape. It is deliberately short — an order
	// of magnitude below DefaultReadyTimeout — because the probe is a
	// refinement and never a precondition: whatever it costs is charged to
	// every cold start, including the one a first request waits on through the
	// ensure-loaded transport. `ProbeDevices` bounds its own spawn at
	// `probeCommandTimeout` (2 s); this is the outer cap that also covers a
	// caller-substituted probe.
	DefaultLoadProbeTimeout = 10 * time.Second
	// maxPropsBodyLen caps the /props readback. The response is a small JSON
	// object of server properties; the cap is the same posture the readiness
	// probe takes (maxModelsBodyLen) — a local server is trusted, but an
	// unbounded read from a socket is still an unbounded read.
	maxPropsBodyLen = 1 << 20
	// defaultKillWait bounds the wait after a kill. It should never be needed —
	// SIGKILL is unconditional — but a process stuck in an uninterruptible syscall
	// must not wedge shutdown forever.
	defaultKillWait = 5 * time.Second
)

// Output handling limits. The server's own log is diagnostic, not a data
// channel: lines are capped so one enormous line cannot exhaust memory, and
// only a short tail is retained for error messages.
const (
	maxLogLineBytes  = 64 << 10
	tailLines        = 24
	maxModelsBodyLen = 1 << 20
)

// fitFailureMarker is the fit contract's failure signature: the complaint the
// pinned fork's `--fit` pass logs (common_fit_params, fit.cpp) when it cannot
// size the launch inside the free device memory it measured. Its success
// sibling — "common_fit_params: successfully fit params to free device
// memory" — closes every fit trace captured from the pin (2026-09-26,
// `prism-b10735-842b188`, darwin-arm64: projected 24450 MiB vs 109950 MiB
// free, no changes needed), and this is the same sentence in the failure
// spelling the abort path emits before the process dies. Because the planner
// DELEGATES the launch shape to `--fit` whenever the operator pinned nothing
// (ADR-067 D2), a pin bump that changes fit's contract can turn a planned
// launch into an abort whose only trace is this line — so the tail is scanned
// for it and the finding surfaced (Status.FitWarning) instead of silently
// dropped with the dead run.
const fitFailureMarker = "failed to fit params to free device memory"

// Fixed launch policy (ADR-066, "Sampling flags"): the fork's demo scripts run
// the model with exactly these values, and they are deliberately not
// configurable — a knob here would silently move the model off the
// configuration its packing was validated with. Reasoning effort is NOT set
// (D7): the server default applies and the user changes it through the
// existing mechanism.
const (
	samplingTemp   = "1.0"
	samplingTopP   = "0.95"
	samplingTopK   = "20"
	flashAttention = "on"
)

// State is the supervision state machine.
//
//	not_installed ─Install→ installed ─Load→ loading ─/v1/models→ loaded
//	                       ▲                                        │
//	                       └────── Unload / idle ── unloading ───────┘
//	any spawn/load/process failure ─────────────────────────────→ error
type State string

const (
	// StateNotInstalled means no manifest: nothing is on disk to run.
	StateNotInstalled State = "not_installed"
	// StateInstalled means the bytes are on disk and the server is NOT
	// running. This is the "unloaded" state of the UI vocabulary.
	StateInstalled State = "installed"
	// StateLoading means a process is up and the weights are being read.
	StateLoading State = "loading"
	// StateLoaded means /v1/models answered: the model can serve requests.
	StateLoaded State = "loaded"
	// StateUnloading means a stop was requested and the process is still
	// draining its graceful window.
	StateUnloading State = "unloading"
	// StateError means the last operation failed, or the process died on its
	// own. A dead server is never reported as loaded.
	StateError State = "error"
)

// Running reports whether the state implies a live llama-server process.
func (st State) Running() bool {
	return st == StateLoading || st == StateLoaded || st == StateUnloading
}

// StateEvent is one state transition, in the shape the backend forwards as the
// global `embedded_llm:state` event. Core never imports backend, so the
// emission is a callback seam (Server.OnState).
type StateEvent struct {
	State State `json:"state"`
	// Port is the loopback port the server is bound (or was bound) to. Zero
	// when no install record was read yet.
	Port int `json:"port"`
	// Message carries the human-readable cause for StateError, and is empty
	// for every other transition.
	Message string `json:"message,omitempty"`
}

// Status is a snapshot of the supervision state, safe to hand to the UI.
type Status struct {
	State State
	Port  int
	// Pid is the OS process id of the supervised server, or 0 when no process
	// is running.
	Pid int
	// Since is when the current state was entered.
	Since time.Time
	// Message is the StateError cause, empty otherwise.
	Message string
	// FitWarning is the fit-contract finding of the last failed launch: the
	// fork's "failed to fit params to free device memory" complaint, scanned
	// out of the dead run's bounded output tail and rendered as a sentence an
	// operator can act on. Empty when no run failed that way, and cleared by
	// the next launch that becomes ready. It outlives the failed run so the
	// Settings install record can show it — a plan/pin mismatch must be
	// REPORTED, not silently retried away.
	FitWarning string
}

// Supervisor refusals. All are sentinels so callers can branch with errors.Is
// and surface an actionable message.
var (
	// ErrServerDied reports that the llama-server process exited. When it
	// happens during a Load, the wrapped detail carries the exit status and the
	// last lines of the server's own log, which is where the real cause (a
	// missing CUDA library, an out-of-memory kill, a Gatekeeper block) lives.
	ErrServerDied = errors.New("the embedded LLM server process exited")
	// ErrLoadTimeout reports that the weights did not finish loading inside
	// ReadyTimeout.
	ErrLoadTimeout = errors.New("the embedded LLM server did not become ready")
	// ErrLaunchSpecInvalid reports a launch specification that must not be
	// spawned: no binary, no model, an unusable port, or a context size outside
	// the resolved RAM tiers.
	ErrLaunchSpecInvalid = errors.New("invalid embedded-LLM launch specification")
	// ErrServerBusy reports an operation refused because a server process is
	// live and its state is not the caller's to overwrite.
	ErrServerBusy = errors.New("the embedded LLM server is running")
)

// errStoppedDuringLoad is Load's diagnosis for a readiness wait that succeeded
// against a process the supervisor had already stopped and given up ownership
// of — the ready-race half of a force stop (see forceUnload and the ownership
// re-check in Load). Unexported: no caller outside this package branches on it,
// and the backend surfaces Load's error text verbatim to the request that
// triggered the load, which is the whole point of naming what happened.
var errStoppedDuringLoad = errors.New(
	"the embedded LLM was stopped while it was loading the model, so it is not resident; ask again to reload it")

// layerKind is LayerMode's private discriminator. It is unexported so that no
// caller outside this package can build a fifth answer, and so that the mapping
// from a kind to an argv element stays the single switch in LayerMode.arg.
type layerKind uint8

const (
	// layerKindAuto omits `-ngl` entirely.
	layerKindAuto layerKind = iota
	// layerKindAll renders `-ngl all`.
	layerKindAll
	// layerKindCPU renders `-ngl 0`.
	layerKindCPU
	// layerKindExact renders `-ngl <count>`.
	layerKindExact
)

// LayerMode is the `-ngl` half of a LaunchSpec: how many layers the launch puts
// on the accelerator. It has FOUR answers, not three, and the fourth is the
// absence of an answer:
//
//	LayerAuto()     OMIT the flag
//	LayerAll()      -ngl all
//	LayerCPU()      -ngl 0
//	LayerCount(n)   -ngl n
//
// LayerAuto is deliberately NOT `-ngl auto`, even though the pinned fork
// accepts that spelling and even defaults to it
// (`-ngl, --gpu-layers, --n-gpu-layers N   max. number of layers to store in
// VRAM, either an exact number, 'auto', or 'all' (default: auto)`). `--fit`
// adjusts only UNSET arguments, and the fork's fit.cpp THROWS on the ambiguous
// shape rather than degrading it — the throw sites at `common/fit.cpp:183` and
// the cluster at `:462`, `:466`, `:472`, `:477`, `:480`, `:483` are all "an
// argument --fit would have chosen is already set", confirmed empirically
// against the pinned runtime: `-ngl 99` beside `--fit on` ABORTS the launch.
// Passing `-ngl auto` would still be passing a value, so a fit-sized launch
// must carry no `-ngl` element at all. That is plan.go's exclusivity rule, and
// this type is how the launcher expresses it.
//
// The zero value is LayerAuto, which is what lets a MemoryPlan's nil `Layers`
// map onto a spec with no translation step and no sentinel to forget. The
// discriminator and the count are private, so nothing free-form can reach argv
// through this field (SECURITY.md ASI05).
type LayerMode struct {
	kind   layerKind
	layers int
}

// LayerAuto is "the runtime's fit pass chooses the offload": `-ngl` is omitted
// from the command line. It is only valid together with Fit.
func LayerAuto() LayerMode { return LayerMode{kind: layerKindAuto} }

// LayerAll renders `-ngl all`: every offloadable layer on the accelerator.
func LayerAll() LayerMode { return LayerMode{kind: layerKindAll} }

// LayerCPU renders `-ngl 0`: the model stays in system RAM.
func LayerCPU() LayerMode { return LayerMode{kind: layerKindCPU} }

// LayerCount renders `-ngl n` for an exact layer count. A negative count is
// representable and is refused by Validate rather than clamped here: clamping
// would hide a caller's mistake behind a launch that offloads something the
// caller never asked for. The runtime itself treats any count at or above the
// model's offloadable layer count as "all", so a large n loses nothing.
func LayerCount(n int) LayerMode { return LayerMode{kind: layerKindExact, layers: n} }

// EmitsFlag reports whether Args renders `-ngl` at all. It is the LayerMode
// spelling of MemoryPlan.EmitsLayers, and it is what makes the exclusivity rule
// checkable exactly as it is stated — "`-fit on` and no `-ngl`" — rather than
// through a number the reader has to re-interpret.
func (m LayerMode) EmitsFlag() bool { return m.kind != layerKindAuto }

// arg renders the `-ngl` value. ok is false for LayerAuto, whose flag is
// omitted rather than given a value; Args is its only caller.
func (m LayerMode) arg() (value string, ok bool) {
	switch m.kind {
	case layerKindAll:
		return "all", true
	case layerKindCPU:
		return strconv.Itoa(nglCPUOnly), true
	case layerKindExact:
		return strconv.Itoa(m.layers), true
	case layerKindAuto:
	}
	return "", false
}

// String is for diagnostics and error text only. It is never an argv element —
// Args renders through arg(), and this spelling names the mode rather than the
// flag value so a log line distinguishes an omitted `-ngl` from `-ngl auto`.
func (m LayerMode) String() string {
	value, ok := m.arg()
	if !ok {
		return "auto (flag omitted)"
	}
	return value
}

// cacheRAMNoLimit is the pinned fork's own `--cache-ram` spelling of "no
// ceiling" (`-cram, --cache-ram N   set the maximum cache size in MiB
// (default: 8192, -1 - no limit, 0 - disable)`). It is the only negative value
// Validate accepts for that field.
const cacheRAMNoLimit = -1

// LaunchSpec is everything one llama-server invocation is derived from. It is
// built from the install record plus the pure launch policy in resolve.go — or,
// for a memory-aware launch, from a MemoryPlan through ApplyMemoryPlan — so the
// flags always match what this machine was provisioned for.
//
// Args is the ONLY place a llama-server command line exists, and every element
// it renders comes from a field below. That is a security property and not just
// a tidiness one (SECURITY.md ASI05): there is no free-form flag, argument or
// command-line string anywhere in this struct, so nothing an operator or a
// config file says can become an argv element. The four string-typed fields
// that do reach argv are each pinned by Validate — Host to the loopback
// constant, KVType and SplitMode to closed enums, and Devices token by token —
// and the three path fields come from the install record and the layout, not
// from config. TestLaunchSpecArgsRenderTypedValuesOnly enforces this at the
// syntax-tree level, so widening it is a deliberate, reviewed act.
type LaunchSpec struct {
	// ServerBinary is the absolute path of llama-server inside the installed
	// runtime tree.
	ServerBinary string
	// ModelFile is the GGUF passed to -m.
	ModelFile string
	// MMProjFile is the vision projector passed to --mmproj. Empty omits the
	// flag (text-only serving), which is only acceptable when the projector is
	// genuinely absent.
	MMProjFile string
	// Host is the bind address. Always LoopbackHost.
	Host string
	// Port is the persisted loopback port.
	Port int
	// Layers is -ngl. LayerAuto omits the flag, which is the only shape a
	// fit-sized launch may carry; see LayerMode.
	Layers LayerMode
	// ContextSize is -c. Zero means "the runtime's fit pass sizes it" and is
	// rendered as an explicit zero, which the fork reads as its own default
	// (`-c, --ctx-size N   size of the prompt context (default: 0, 0 = loaded
	// from model)`) and --fit then adjusts; it is valid only together with Fit.
	// With Fit false the planner has computed the context and it is always
	// positive. Either way it never exceeds the model's own training context —
	// an unspecified one lets the server fall back to that training context,
	// which is memory-unaware and OOMs a constrained machine once -ngl offloads
	// the KV cache.
	ContextSize int
	// ImageMaxTokens is --image-max-tokens. ImageMaxTokensUncapped omits the
	// flag entirely (CUDA/ROCm run uncapped). Validate bounds it on both ends:
	// a negative cap is refused, and so is one above the model's own training
	// context (maxTrainingContext) — the ceiling the resolver's derived values
	// sit far below, since imageMaxTokensFor yields either the uncapped zero or
	// the 1,024-token vision cap.
	ImageMaxTokens int

	// ── the memory-plan surface ──
	//
	// Everything below is a typed projection of a MemoryPlan (plan.go). Omission
	// is expressed the way that struct expresses it — a nil pointer, a zero, an
	// empty slice, the enum's Auto member — so a plan maps onto a spec field for
	// field with no sentinel to invent.

	// Fit is `-fit`, and it is the one memory flag Args renders
	// UNCONDITIONALLY. The fork's own default is 'on' (`-fit, --fit [on|off]
	// whether to adjust unset arguments to fit in device memory ('on' or 'off',
	// default: 'on')`), and this subsystem does not rely on it: `--fit` is an
	// undocumented fork contract — stock llama.cpp has no such switch — so a pin
	// bump could flip or drop the default, and a flipped default would silently
	// turn every explicit-offload launch into a fit.cpp abort. Rendering it
	// always means the launch shape is a property of this struct rather than of
	// whichever binary happens to be pinned.
	Fit bool
	// FitTargetMiB is `-fitt`, the per-device margin fit leaves free. Zero omits
	// the flag and keeps the runtime's own 1024 MiB default. Rendered only under
	// Fit, since with fit off nothing reads it.
	FitTargetMiB int
	// FitMinContext is `-fitc`, the smallest context fit may settle on.
	// Rendered only under Fit, where Validate requires it to be positive:
	// letting the fork's own 4096 default apply reproduces the pinned model's
	// most-reported failure, which is what DefaultFitMinContext exists to
	// prevent. A fit-OFF plan still carries the planner's floor in this field
	// (plan.go sets it unconditionally); Args omits it and Validate accepts it,
	// because there it is a record of the gate the planner ran and not a
	// decision being dropped.
	FitMinContext int
	// KVType is `-ctk`/`-ctv`, one precision for both because memory.go
	// documents that a mixed pair silently drops to CPU until the target context
	// fits. Empty omits both flags and leaves the runtime's own f16 default;
	// any other value must be a member of memory.go's closed three-precision
	// set, so an unmodelled precision is a refusal rather than a fallback.
	KVType KVType
	// KVOffload nil omits `-kvo`/`-nkvo` and keeps the runtime's default
	// (enabled). An explicit false renders `-nkvo`: the KV cache stays in system
	// RAM, which trades device memory for host memory and attention bandwidth.
	KVOffload *bool
	// MMProjOffload nil omits `--mmproj-offload`/`--no-mmproj-offload` and keeps
	// the runtime's default (enabled). An explicit false renders
	// `--no-mmproj-offload`: the vision projector's worst-case reserve stays in
	// system RAM.
	MMProjOffload *bool
	// Parallel is `-np`, and like Fit it is ALWAYS rendered. The fork's default
	// is `-1` (auto), which is unsafe here for two measured reasons: auto
	// inflates fit's own compute reserve per stream (24450 MiB → 77297 MiB at 4
	// slots), and `-np` SPLITS `-c` across slots, so `-c 8192 -np 4` yields
	// `n_ctx_slot 2048`. See DefaultParallel. Validate requires at least 1.
	Parallel int
	// CacheRAMMiB is `--cache-ram` (the fork also accepts the short alias
	// `-cram`; the long spelling is used here so the flag is greppable in a
	// process listing). nil omits the flag and keeps the runtime's own default;
	// a non-nil value is passed through verbatim, including cacheRAMNoLimit for
	// "no ceiling" and 0 to disable the prompt cache, because disabling it is a
	// legitimate choice.
	CacheRAMMiB *int
	// Devices is `-dev`, the comma-separated offload target list. Empty omits
	// the flag. Any entry pins the offload, so a non-empty list is incompatible
	// with Fit. Each name is validated as a single separator-free, dash-free
	// token before it can reach argv — see validateDeviceName.
	Devices []string
	// SplitMode is `-sm`. SplitModeAuto (the zero value) omits the flag; any
	// other member of the runtime's closed set pins the split and is
	// incompatible with Fit.
	SplitMode SplitMode
}

// Validate refuses a specification that must never reach exec: spawning it
// would either fail obscurely, abort inside the fork's own fit pass, or — worse
// — start a server whose offload and context nobody decided.
//
// Every argv-bound numeric field is bounded on BOTH ends. The floors are launch
// semantics. The ceilings are limits.go's overflow and absurdity guards for the
// operator-tunable knobs — enforced here as well as in backend/config because a
// bound that exists at only one of the two layers is a bound an operator can walk
// around by hand-editing config.yaml — and the model's own training context
// (memory.go's maxTrainingContext) for the three context-derived fields, `-c`,
// `-fitc` and `--image-max-tokens`. Either way the value that reaches this gate is
// the one that becomes a command line, whichever path it arrived by.
func (spec LaunchSpec) Validate() error {
	if spec.ServerBinary == "" {
		return fmt.Errorf("%w: no llama-server binary", ErrLaunchSpecInvalid)
	}
	if spec.ModelFile == "" {
		return fmt.Errorf("%w: no model file", ErrLaunchSpecInvalid)
	}
	if spec.Host != LoopbackHost {
		return fmt.Errorf("%w: host %q is not the loopback bind %q",
			ErrLaunchSpecInvalid, spec.Host, LoopbackHost)
	}
	if !usablePort(spec.Port) {
		return fmt.Errorf("%w: port %d is not a usable TCP port", ErrLaunchSpecInvalid, spec.Port)
	}
	if spec.Layers.kind == layerKindExact && (spec.Layers.layers < 0 || spec.Layers.layers > MaxTuningLayers) {
		return fmt.Errorf("%w: -ngl %d is outside 0..%d", ErrLaunchSpecInvalid, spec.Layers.layers, MaxTuningLayers)
	}
	if err := spec.validateFitExclusivity(); err != nil {
		return err
	}
	if err := spec.validateContext(); err != nil {
		return err
	}
	if spec.Fit && (spec.FitMinContext <= 0 || spec.FitMinContext > maxTrainingContext) {
		return fmt.Errorf("%w: -fit on needs a -fitc floor in 1..%d, got %d — the runtime's own default of %d is the truncated-answer failure DefaultFitMinContext exists to prevent",
			ErrLaunchSpecInvalid, maxTrainingContext, spec.FitMinContext, upstreamFitMinContext)
	}
	if spec.FitTargetMiB < 0 || spec.FitTargetMiB > MaxTuningMiB {
		return fmt.Errorf("%w: -fitt %d MiB is outside 0..%d",
			ErrLaunchSpecInvalid, spec.FitTargetMiB, MaxTuningMiB)
	}
	if spec.KVType != "" && !spec.KVType.Valid() {
		return fmt.Errorf("%w: -ctk/-ctv %q is outside the modelled set (%s)",
			ErrLaunchSpecInvalid, string(spec.KVType), strings.Join(kvTypeNames(), ", "))
	}
	if spec.Parallel < 1 {
		return fmt.Errorf("%w: -np %d is below the single slot this subsystem serves; the runtime's own auto default splits -c across slots",
			ErrLaunchSpecInvalid, spec.Parallel)
	}
	if spec.Parallel > MaxTuningParallel {
		return fmt.Errorf("%w: -np %d exceeds the %d-slot ceiling; slots split -c and each one carries its own KV cache",
			ErrLaunchSpecInvalid, spec.Parallel, MaxTuningParallel)
	}
	if spec.CacheRAMMiB != nil && *spec.CacheRAMMiB < cacheRAMNoLimit {
		return fmt.Errorf("%w: --cache-ram %d MiB is below %d, the runtime's own spelling of no limit",
			ErrLaunchSpecInvalid, *spec.CacheRAMMiB, cacheRAMNoLimit)
	}
	if spec.CacheRAMMiB != nil && *spec.CacheRAMMiB > MaxTuningMiB {
		return fmt.Errorf("%w: --cache-ram %d MiB exceeds the %d MiB ceiling",
			ErrLaunchSpecInvalid, *spec.CacheRAMMiB, MaxTuningMiB)
	}
	if !splitModeIsKnown(spec.SplitMode) {
		return fmt.Errorf("%w: -sm %q is not one of the runtime's split modes (%s)",
			ErrLaunchSpecInvalid, string(spec.SplitMode), strings.Join(splitModeNames(), ", "))
	}
	for _, device := range spec.Devices {
		if err := validateDeviceName(device); err != nil {
			return err
		}
	}
	if spec.ImageMaxTokens < 0 {
		return fmt.Errorf("%w: --image-max-tokens %d is negative",
			ErrLaunchSpecInvalid, spec.ImageMaxTokens)
	}
	if spec.ImageMaxTokens > maxTrainingContext {
		return fmt.Errorf("%w: --image-max-tokens %d exceeds the model's own %d-token training context",
			ErrLaunchSpecInvalid, spec.ImageMaxTokens, maxTrainingContext)
	}
	return nil
}

// validateFitExclusivity is plan.go's exclusivity rule enforced at the last gate
// before exec. Exactly one of {this spec, the runtime's fit pass} decides the
// offload, and the fork refuses the ambiguous shape rather than picking a
// winner — so an explicit -ngl, -dev or -sm beside `-fit on` is a spec error
// here, not a spawn-time abort and not a silently ignored decision.
//
// The converse is enforced too: with `-fit off` an omitted -ngl leaves the
// offload to the runtime's own `auto` default, which is decided by the model
// file rather than by any memory measurement. That is precisely the
// memory-unaware launch this subsystem exists to prevent, so a fit-off spec must
// pin the layer count.
func (spec LaunchSpec) validateFitExclusivity() error {
	if !spec.Fit {
		if !spec.Layers.EmitsFlag() {
			return fmt.Errorf("%w: -fit off needs an explicit -ngl, because with fit off nothing else sizes the offload",
				ErrLaunchSpecInvalid)
		}
		return nil
	}
	switch {
	case spec.Layers.EmitsFlag():
		return fmt.Errorf("%w: -fit on cannot size a launch whose -ngl is already pinned to %s; the fork's fit pass aborts on that combination",
			ErrLaunchSpecInvalid, spec.Layers)
	case len(spec.Devices) > 0:
		return fmt.Errorf("%w: -fit on cannot size a launch whose -dev is already pinned to %s",
			ErrLaunchSpecInvalid, strings.Join(spec.Devices, ","))
	case spec.SplitMode != SplitModeAuto:
		return fmt.Errorf("%w: -fit on cannot size a launch whose -sm is already pinned to %q",
			ErrLaunchSpecInvalid, string(spec.SplitMode))
	}
	return nil
}

// validateContext is the `-c` rule. The ceiling is the model's own training
// context — memory.go's maxTrainingContext, the same figure
// ModelMemoryProfile.MaxContext reports — because nothing above it is
// representable and a fit-sized plan may legitimately be held to a floor at the
// top of the modelled range. The floor moved with the memory planner: a zero
// context is no longer "unspecified", it is "fit decides", and it is accepted
// only when fit is actually on to decide it. A negative one is refused outright.
func (spec LaunchSpec) validateContext() error {
	switch {
	case spec.ContextSize < 0:
		return fmt.Errorf("%w: context size %d is negative", ErrLaunchSpecInvalid, spec.ContextSize)
	case spec.ContextSize == 0 && !spec.Fit:
		return fmt.Errorf("%w: context size 0 means --fit sizes the context, but -fit is off and nothing else would",
			ErrLaunchSpecInvalid)
	case spec.ContextSize > maxTrainingContext:
		return fmt.Errorf("%w: context size %d exceeds the model's own %d-token training context",
			ErrLaunchSpecInvalid, spec.ContextSize, maxTrainingContext)
	}
	return nil
}

// splitModeIsKnown reports whether the mode is a member of the closed set the
// pinned fork accepts (`-sm, --split-mode {none,layer,row,tensor}`, plus
// SplitModeAuto for "omit the flag"). An unknown spelling is refused rather than
// passed through, so a typo cannot become an argv element.
func splitModeIsKnown(mode SplitMode) bool {
	switch mode {
	case SplitModeAuto, SplitModeNone, SplitModeLayer, SplitModeRow, SplitModeTensor:
		return true
	default:
		return false
	}
}

// splitModeNames renders the closed set for diagnostics.
func splitModeNames() []string {
	return []string{
		string(SplitModeNone), string(SplitModeLayer),
		string(SplitModeRow), string(SplitModeTensor),
	}
}

// validateDeviceName is the typed-value guarantee for the one argv element Args
// builds by joining strings. A device name is a probe result rather than config,
// but it is still a string, so it is checked instead of trusted: the joined list
// must stay ONE argv element that the runtime parses as a device list and as
// nothing else.
//
// What this does not have to defend against is command injection — argv goes to
// exec directly and never through a shell, so no value here can introduce a
// command. What a malformed name could do is smuggle a second device past the
// planner's device decision, or present the runtime's parser with something that
// reads as a flag.
func validateDeviceName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: -dev carries an empty device name", ErrLaunchSpecInvalid)
	case strings.ContainsAny(name, ", \t\n\r"):
		return fmt.Errorf("%w: -dev device name %q contains a separator, so it would smuggle a second device into the list",
			ErrLaunchSpecInvalid, name)
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("%w: -dev device name %q reads as a flag", ErrLaunchSpecInvalid, name)
	}
	return nil
}

// fitArg renders the literal `-fit` value. It mirrors MemoryPlan.FitArg so the
// exclusivity rule reads identically on both sides of the bridge, and it is a
// two-branch choice between fixed literals: no value from outside this package
// can reach the flag.
func (spec LaunchSpec) fitArg() string {
	if spec.Fit {
		return "on"
	}
	return "off"
}

// Args renders the command line. This function is the only place a llama-server
// argv exists; the flag order is fixed, and every value is either a literal in
// this file or an integer/enum rendering of a typed LaunchSpec field. The Web UI
// is always off and the bind is always loopback.
//
// The order groups by concern so a diff reads as a decision: identity and
// socket, then the fit switch that decides who sizes the launch, then the
// offload shape, then the context and its cache, then the slot and prompt-cache
// budgets, then the fixed sampling policy, the UI kill switch, and finally the
// vision flags.
func (spec LaunchSpec) Args() []string {
	args := make([]string, 0, 34)
	args = append(args,
		"-m", spec.ModelFile,
		"--host", spec.Host,
		"--port", strconv.Itoa(spec.Port),
		// Always explicit — see LaunchSpec.Fit.
		"-fit", spec.fitArg(),
	)
	// The two fit knobs are rendered only under fit: with `-fit off` the
	// runtime never reads them, and passing them would advertise a decision
	// nobody made.
	if spec.Fit && spec.FitTargetMiB > 0 {
		args = append(args, "-fitt", strconv.Itoa(spec.FitTargetMiB))
	}
	if spec.Fit && spec.FitMinContext > 0 {
		args = append(args, "-fitc", strconv.Itoa(spec.FitMinContext))
	}
	// LayerAuto omits the element entirely — see LayerMode.
	if layers, ok := spec.Layers.arg(); ok {
		args = append(args, "-ngl", layers)
	}
	if spec.SplitMode != SplitModeAuto {
		args = append(args, "-sm", string(spec.SplitMode))
	}
	if len(spec.Devices) > 0 {
		args = append(args, "-dev", strings.Join(spec.Devices, ","))
	}
	args = append(args,
		"-fa", flashAttention,
		"-c", strconv.Itoa(spec.ContextSize),
	)
	if spec.KVType != "" {
		// One precision for both halves: memory.go documents that a mixed pair
		// silently drops to CPU until the target context fits.
		precision := string(spec.KVType)
		args = append(args, "-ctk", precision, "-ctv", precision)
	}
	if spec.KVOffload != nil && !*spec.KVOffload {
		args = append(args, "-nkvo")
	}
	args = append(args, "-np", strconv.Itoa(spec.Parallel))
	if spec.CacheRAMMiB != nil {
		args = append(args, "--cache-ram", strconv.Itoa(*spec.CacheRAMMiB))
	}
	args = append(args,
		"--temp", samplingTemp,
		"--top-p", samplingTopP,
		"--top-k", samplingTopK,
		"--jinja",
		// D12: the built-in Web UI ships its own MCP client and agentic loop.
		// It is disabled unconditionally — this process is an inference
		// endpoint for c0wrk and nothing else.
		//
		// `--no-ui` is the canonical spelling: the pinned fork registers the
		// switch as the alias pair {"--ui", "--webui"} / {"--no-ui",
		// "--no-webui"} (common/arg.cpp), and `--ui`-first is the vocabulary
		// every related flag uses (--ui-config, --ui-mcp-proxy). The legacy
		// `--no-webui` is still accepted as a trailing alias, so this is a
		// spelling migration rather than a behavior change — and because the
		// runtime is a compile-time pin whose arg.cpp was verified to accept
		// `--no-ui`, no rejected-spelling fallback is warranted: a fallback
		// would be dead code guarding a binary this build cannot spawn.
		"--no-ui",
	)
	if spec.MMProjFile != "" {
		args = append(args, "--mmproj", spec.MMProjFile)
	}
	if spec.MMProjOffload != nil && !*spec.MMProjOffload {
		args = append(args, "--no-mmproj-offload")
	}
	if spec.ImageMaxTokens != ImageMaxTokensUncapped {
		args = append(args, "--image-max-tokens", strconv.Itoa(spec.ImageMaxTokens))
	}
	return args
}

// ApplyMemoryPlan returns a copy of the spec with every memory-derived flag
// taken from plan. It is the one bridge between the pure planner and the
// command line, and it is deliberately total: every field of a MemoryPlan that
// names a runtime flag is copied here, so a plan cannot be half-applied and a
// new planner knob cannot be silently dropped on the way to argv.
//
// It copies rather than mutates so a caller can build the identity half (binary,
// model, projector, socket) from the install record and the shape half from the
// plan without either being able to overwrite the other. It does NOT call
// Validate: the exclusivity rule, the KV allow-list and the context bounds are
// the caller's gate, and ApplyMemoryPlan's own output satisfies them for any
// plan that Plan returned.
func (spec LaunchSpec) ApplyMemoryPlan(plan MemoryPlan) LaunchSpec {
	spec.Fit = plan.Fit
	spec.FitTargetMiB = plan.FitTargetMiB
	spec.FitMinContext = plan.FitMinContext
	if plan.Layers == nil {
		spec.Layers = LayerAuto()
	} else {
		// The count is passed through exactly as the planner decided it. It is
		// NOT re-read as "all" or "cpu" when it happens to equal nglAllGPU or
		// nglCPUOnly: the launcher renders a decision, it does not make one.
		spec.Layers = LayerCount(*plan.Layers)
	}
	spec.ContextSize = plan.ContextSize
	spec.KVType = plan.KVType
	kvOffload := plan.KVOffload
	spec.KVOffload = &kvOffload
	mmprojOffload := plan.MMProjOffload
	spec.MMProjOffload = &mmprojOffload
	spec.Parallel = plan.Parallel
	spec.CacheRAMMiB = nil
	if plan.CacheRAMMiB != nil {
		cacheRAM := *plan.CacheRAMMiB
		spec.CacheRAMMiB = &cacheRAM
	}
	spec.Devices = slices.Clone(plan.Devices)
	spec.SplitMode = plan.SplitMode
	return spec
}

// ModelsURL is the readiness endpoint: the OpenAI-compatible model list.
func (spec LaunchSpec) ModelsURL() string {
	return baseURL(spec.Port) + "/models"
}

// usablePort reports whether a port can be bound. The bounds are the
// subsystem's own MinLoopbackPort/MaxLoopbackPort, which mirror what
// backend/config's validate() enforces on the persisted value; this is the last
// gate before exec.
func usablePort(port int) bool {
	return port >= MinLoopbackPort && port <= MaxLoopbackPort
}

// baseURL renders the OpenAI-compatible base URL of a server on the given
// loopback port. It must stay byte-identical to the provider base_url that
// backend/config generates from embedded_llm.port.
func baseURL(port int) string {
	return "http://" + LoopbackHost + ":" + strconv.Itoa(port) + "/v1"
}

// LaunchCommand is the fully resolved process invocation handed to SpawnFunc.
// Args and Env are built by production code, so a substituted spawner still
// exercises (and can assert) the exact command line and environment.
type LaunchCommand struct {
	Binary string
	Args   []string
	Env    []string
	Dir    string
}

// Process is the supervisor's handle on one spawned llama-server. It is the
// seam every supervision test drives: the real implementation wraps
// *exec.Cmd, and a test double can start a fake endpoint, die on cue, or
// ignore a graceful signal.
type Process interface {
	// Wait blocks until the process has exited AND its output streams have been
	// drained, then returns the exit error (nil on a clean exit).
	Wait() error
	// Pid is the OS process id, for logs and for the kill fallback message.
	Pid() int
	// Signal asks the process to terminate gracefully. It returns an error when
	// the platform has no graceful signal for a child process (Windows) or the
	// process is already gone — both mean "kill it instead".
	Signal(sig os.Signal) error
	// Kill terminates the process immediately.
	Kill() error
	// Stdout and Stderr are the process's output streams, or nil when the
	// implementation does not capture them. The supervisor drains both into
	// slog; Wait must not return before that drain completes.
	Stdout() io.Reader
	Stderr() io.Reader
}

// SpawnFunc starts the process described by cmd. The context bounds the START,
// not the process lifetime: the returned Process outlives it.
type SpawnFunc func(ctx context.Context, cmd LaunchCommand) (Process, error)

// Server supervises one llama-server process for one installation.
//
// The zero value is not usable; build one with NewServer. As with Installer,
// every external effect is an injectable field, so the whole state machine —
// including a multi-minute weight load, a crash and the idle unload — is
// testable without a runtime, without weights and without waiting.
type Server struct {
	// Layout locates the installed runtime tree and weights. Required.
	Layout Layout
	// Logger receives the subsystem's diagnostics AND the server's own
	// stdout/stderr. nil → a discard logger (never the global slog.Default),
	// matching the package's own no-global-logging rule.
	Logger *slog.Logger
	// AutoUnload is the operator's idle policy (embedded_llm.auto_unload).
	// Change it at runtime with SetAutoUnload, not by writing this field.
	AutoUnload AutoUnload
	// Spawn starts the process. nil → spawnOSServer, the real llama-server.
	Spawn SpawnFunc
	// EnsurePort re-checks that the persisted port is still free immediately
	// before the spawn, and may return a replacement (which it also persists).
	// PRODUCTION ALWAYS WIRES IT: backend.embeddedEnsurePort scans upward with
	// SearchFreePort and writes a moved port back to embedded_llm.port, so the
	// generated provider base_url keeps matching the socket the server actually
	// bound. nil → the persisted port is trusted as-is, which is only correct
	// for a caller that has no config to keep in sync (tests, a core-only
	// embedding); it deliberately performs no probing of its own so a unit test
	// never binds a real port.
	//
	// The re-check is what makes the FIRST readiness probe safe: it runs
	// immediately after the spawn, so a server left behind on the same port by a
	// crashed previous run — or any other local process — would answer it and be
	// mistaken for the new one.
	EnsurePort func(ctx context.Context, port int) (int, error)
	// OnState receives one StateEvent per observable transition. May be nil.
	// It is called from the goroutine that made the transition and must not
	// call back into the Server: it runs outside the state lock, but a reentrant
	// Load/Unload from it would deadlock on the single-instance gate.
	OnState func(StateEvent)
	// HTTPClient performs the readiness probes. nil → http.DefaultClient with a
	// per-probe timeout (ProbeTimeout).
	HTTPClient *http.Client
	// ProbeDevices re-measures the accelerator inventory immediately before a
	// launch, so a load can notice that a GPU appeared, disappeared, or got a
	// new driver since the install recorded its topology. PRODUCTION ALWAYS
	// WIRES IT (backend.embeddedDeviceProbe); nil → NO load-time probe, and the
	// manifest's stored plan is launched exactly as recorded.
	//
	// It is opt-in rather than defaulted to the package function because the
	// pre-existing contract of this path was "no hardware probe runs at load
	// time", and a caller that does not wire it must get that behaviour rather
	// than an exec it did not ask for. When wired, it is FAIL-SOFT in every
	// direction — see effectivePlan — so wiring it can never make a load fail.
	ProbeDevices func(ctx context.Context, binaryPath string, logger *slog.Logger) (MemoryTopology, bool)
	// Tuning resolves the operator's memory-plan overrides
	// (`embedded_llm.tuning`) at launch time. It is a FUNCTION, not a value,
	// because a load must plan with the tuning in force when it runs: the
	// supervisor is built once and cached, while a settings save can change the
	// tuning at any point in between. nil → the zero Tuning, which IS the
	// documented all-Auto plan.
	//
	// It is read on the load path only, and it must not block: the production
	// implementation takes configMu.RLock, which is already established as safe
	// inside Load (EnsurePort → persistEmbeddedPort does the same).
	Tuning func() Tuning
	// PersistContext writes the effective context a READY server reported back
	// into durable config state — the `llm.models."Bonsai 2 27B".context_window`
	// override the tier-1 config lookup reads. PRODUCTION ALWAYS WIRES IT
	// (backend.persistEmbeddedContext); nil → the manifest is still updated, but
	// config.yaml keeps the install's estimate.
	//
	// It is called inside Load, after readiness and while the single-instance
	// gate is held, so an implementation must not call back into the Server.
	// Failures are logged and otherwise ignored: a load that served the model
	// successfully must not be reported as failed over an administrative write.
	PersistContext func(ctx context.Context, contextSize int) error
	// LoadProbeTimeout bounds the ProbeDevices call. <= 0 →
	// DefaultLoadProbeTimeout.
	LoadProbeTimeout time.Duration
	// Now stamps state transitions and idle bookkeeping. nil → time.Now.
	Now func() time.Time
	// HostOS overrides the OS the binary name and the library-path policy key
	// off. Empty → runtime.GOOS.
	HostOS string
	// Platform overrides the "<goos>-<goarch>" key the -ngl policy reads.
	// Empty → the host platform. Production leaves it empty; the value that
	// matters (the effective backend) comes from the manifest.
	Platform string
	// ReadyTimeout bounds one Load. <= 0 → DefaultReadyTimeout.
	ReadyTimeout time.Duration
	// ReadyPollInterval is the readiness poll gap. <= 0 → DefaultReadyPollInterval.
	ReadyPollInterval time.Duration
	// ProbeTimeout bounds a single readiness probe. <= 0 → DefaultProbeTimeout.
	ProbeTimeout time.Duration
	// StopTimeout is the graceful window before a kill. <= 0 → DefaultStopTimeout.
	StopTimeout time.Duration

	// killWaitFor shortens the post-kill wait. Unexported: it is a recovery
	// timeout with no operator meaning, and only a test needs it small.
	killWaitFor time.Duration

	// drainWaitFor shortens the post-exit output drain. Unexported for the same
	// reason as killWaitFor: it is a recovery timeout, and only a test that
	// stages a pump that never ends needs it small.
	drainWaitFor time.Duration

	mu      sync.Mutex
	state   State
	message string
	port    int
	since   time.Time
	run     *processRun
	// fitWarning is the fit-contract finding of the last failed run — the
	// sentence scanFitFailure produced from the dead run's output tail, or ""
	// when no run has complained (or the complaint was followed by a
	// successful load, which clears it). Guarded by mu.
	fitWarning string
	// gate is the single-instance lock: a buffered-1 channel used as a token,
	// so a waiter can abandon it through its context instead of blocking for
	// the whole weight load. Lazily created, which keeps the zero value from
	// deadlocking.
	gate chan struct{}
	idle idleTimer
	// inFlight counts the requests the ensure-loaded transport currently has
	// open (BeginRequest/EndRequest). Atomic rather than mu-guarded: the
	// transport touches it on every request, and the idle path reads it right
	// after releasing mu.
	inFlight atomic.Int64
	// loadsInFlight counts the Load calls that are inside the part of their
	// sequence which OWNS a terminal state transition — from just before they
	// report StateLoading until they return. Guarded by mu, because its only
	// reader is forceUnload, which decides under that same lock whether the
	// terminal transition is its own to make or the interrupted load's.
	//
	// A Load that finds the model already resident returns without transitioning
	// anything, so it is deliberately NOT counted: counting it would leave the
	// state wherever forceUnload put it, with nobody to move it on.
	loadsInFlight int
}

// NewServer returns a Server for an installation described by layout. The state
// starts at not_installed; startup moves it to installed after restoring the
// manifest (SetInstalled).
func NewServer(layout Layout, logger *slog.Logger) *Server {
	return &Server{
		Layout:     layout,
		Logger:     logger,
		AutoUnload: DefaultAutoUnload(),
		gate:       make(chan struct{}, 1),
	}
}

// processRun is one supervised process lifetime.
type processRun struct {
	proc Process
	pid  int
	tail *lineTail

	// died is closed exactly once, by the supervise goroutine, AFTER exitErr has
	// been written. A reader must not touch exitErr before it observes the
	// close.
	died    chan struct{}
	exitErr error

	// Both flags are written under Server.mu before the exit is provoked, and
	// tell the supervisor how to report an exit it did not expect to see:
	//   expected  — an Unload asked for it → StateInstalled
	//   abandoned — a failed Load discarded it → the load error owns the state
	expected  bool
	abandoned bool
}

// Load starts the server and blocks until the model can actually answer.
//
// The sequence is fixed:
//
//  1. acquire the single-instance gate (already serving → no-op success);
//  2. read manifest.json — no manifest means not installed, and nothing is
//     spawned;
//  3. re-check the persisted port through the EnsurePort seam;
//  4. derive the LaunchSpec from the manifest plus the pure launch policy, and
//     refuse it if it is not launchable;
//  5. spawn with the runtime's own directory on the dynamic-library path, and
//     start draining its output into slog;
//  6. poll /v1/models until it answers with a non-empty model list, bounded by
//     ReadyTimeout, ctx, and the death of the process;
//  7. only now: state → loaded and the idle budget starts — and only while this
//     call still owns the process it published. A run detached during step 6 is
//     refused residency: a force stop is answered with state → installed, a child
//     that died right after answering the probe leaves the supervisor's crash
//     report in place, and either way the load returns an error naming what
//     happened instead of claiming a residency the supervisor no longer tracks.
//
// Step 7's position is the user-visible requirement behind idle.go: the weight
// load can take minutes, and that time must never be charged against the
// operator's inactivity budget.
//
// ctx bounds the whole operation. Cancelling it aborts the load and stops the
// half-started process, so callers that must survive a long weight load pass a
// context that is not tied to one RPC.
func (s *Server) Load(ctx context.Context) error {
	if s == nil {
		return errors.New("embeddedllm: nil Server")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	release, err := s.acquireGate(ctx)
	if err != nil {
		return fmt.Errorf("embeddedllm: waiting for the embedded LLM to become available: %w", err)
	}
	defer release()

	if s.State() == StateLoaded {
		// Idempotent: the model is already serving, so a Load is a no-op that
		// still counts as activity (the caller is about to use it). It performs
		// no state transition, which is why the count below starts AFTER this
		// return rather than at the gate.
		s.MarkActivity()
		return nil
	}

	// From here every exit path of this call writes a terminal transition —
	// StateError on any failure (a child that died after answering readiness
	// leaves the supervisor's own StateError standing rather than adding one),
	// StateLoaded on success, StateInstalled when the process was force-stopped
	// underneath a readiness wait that still succeeded — so a force stop that
	// finds this count positive and the state still StateLoading may leave that
	// transition to this call instead of making its own. See forceUnload.
	//
	// Registered after the gate release above, so LIFO runs this one FIRST: the
	// count is back to zero before the gate is free again, and forceUnload —
	// which only runs while the gate is held — can never see a count left behind
	// by a load that has already finished.
	s.mu.Lock()
	s.loadsInFlight++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.loadsInFlight--
		s.mu.Unlock()
	}()

	// A process can still be alive from an attempt whose stop timed out. Single
	// instance means at most one llama-server per install, so it is discarded
	// before another one is spawned. Taking the handle out of the Server first
	// also tells the supervisor that this exit is nobody's news: no state
	// transition is emitted for it.
	if stale := s.takeRun(); stale != nil {
		s.logger().Warn("stopping a leftover embedded LLM process before loading",
			"pid", stale.pid)
		s.discardRun(stale)
	}

	manifest, err := s.readManifest()
	if err != nil {
		if errors.Is(err, ErrNotInstalled) {
			s.transition(StateNotInstalled, "")
		} else {
			s.transition(StateError, err.Error())
		}
		return err
	}

	port := manifest.Port
	if s.EnsurePort != nil {
		port, err = s.EnsurePort(ctx, port)
		if err != nil {
			s.transition(StateError, err.Error())
			return fmt.Errorf("embeddedllm: preparing the loopback port: %w", err)
		}
	}

	launch, err := s.launchSpec(ctx, manifest, port)
	if err != nil {
		s.transition(StateError, err.Error())
		return err
	}
	spec := launch.Spec

	s.mu.Lock()
	s.port = spec.Port
	s.mu.Unlock()
	s.transition(StateLoading, "")

	run, err := s.spawn(ctx, spec)
	if err != nil {
		s.transition(StateError, err.Error())
		return err
	}

	s.mu.Lock()
	s.run = run
	s.mu.Unlock()
	go s.supervise(run)

	if err := s.waitReady(ctx, run, spec); err != nil {
		// The load failed, so the half-started process is discarded. Marking it
		// abandoned first keeps the supervisor from reporting a death THIS call is
		// about to explain as an unexplained crash. A process that died on its own
		// before the flag was set is reported twice — once by the supervisor and
		// once here — which is deliberate: suppressing the supervisor's report
		// instead would need a flag set before the load, and clearing it after
		// readiness opens a window where a death right after the last successful
		// probe is swallowed and a dead server keeps being reported as loaded.
		s.mu.Lock()
		run.abandoned = true
		// R1 guard: the fit contract's failure line must not die with the run.
		// Scanned here (not only in supervise) because an abandoned run's exit
		// is deliberately unreported by the supervisor — this branch is the one
		// place the failure is explained.
		s.fitWarning = scanFitFailure(run.tail.String())
		s.mu.Unlock()
		s.discardRun(run)
		s.transition(StateError, err.Error())
		s.logger().Error("the embedded LLM did not become ready",
			"port", spec.Port, "error", err, "last_output", run.tail.String())
		return err
	}

	s.mu.Lock()
	// Residency is re-validated against the PUBLISHED handle before it is
	// claimed. Two paths can detach a run this Load published while it was still
	// inside waitReady, and in both the process must not be reported as resident:
	//
	//   - a force stop (forceUnload) takes the handle with takeRun, and its
	//     snapshot of who owes the terminal transition is made BEFORE it signals
	//     the child — an emit (which runs the backend's synchronous OnState
	//     handler) and a Warn log sit in between — so a readiness poll landing in
	//     that window is answered by a server already on its way out and this Load
	//     takes its success path anyway. Such a run is marked `expected`: a stop
	//     was asked for.
	//   - the child died right after answering the readiness probe, and supervise
	//     detached the handle while reporting the crash. Such a run is NOT
	//     `expected`.
	//
	// Claiming StateLoaded in either case would report a process the supervisor no
	// longer owns as resident: s.run is nil, so Status().Pid is 0, the
	// ensure-loaded transport short-circuits on State() == StateLoaded, and every
	// later request goes to a dead loopback port and fails with a bare connection
	// error — exactly what StateError's "a dead server is never reported as
	// loaded" rule forbids. In the force-stop case NEITHER death reporter fires
	// either (supervise's `current := s.run == run` is false and run.expected is
	// set), so nothing would ever reconcile the claim.
	//
	// The diagnosis travels in the returned error rather than in a transition
	// message, which StateEvent documents as StateError-only. No idle budget is
	// stamped on either branch: there is nothing resident to be idle, and
	// transitionLocked disarms the timer for any state other than StateLoaded.
	//
	// One window is deliberately left alone: a force stop whose terminate did NOT
	// take RE-ATTACHES the handle (see forceUnload), so this check passes and the
	// load claims a residency that is real — the child is alive and just answered
	// the probe — replacing the StateError the force path wrote. Reporting
	// `installed` there instead would misreport a still-resident process, which is
	// the mistake the gated unload's own doc forbids, and the failed stop is
	// already reported to the caller that asked for it.
	if s.run != run {
		pid := run.pid
		if run.expected {
			// A stop was asked for, so the honest terminal state is StateInstalled
			// — the bytes are on disk, the model is not resident.
			event := s.transitionLocked(StateInstalled, "")
			s.mu.Unlock()
			s.emit(event)
			s.logger().Warn("the embedded LLM was stopped while its load was in flight; the weights are not resident",
				"pid", pid, "port", spec.Port)
			return fmt.Errorf("embeddedllm: %w", errStoppedDuringLoad)
		}
		// The child died and supervise already reported that crash, so its
		// StateError IS the honest terminal state: this call adds no transition of
		// its own and only returns the diagnosis. run.exitErr was written under
		// this same lock before the handle was detached, so it is safe to read.
		exitErr := run.exitErr
		s.mu.Unlock()
		s.logger().Error("the embedded LLM answered its readiness probe and then died before the load could claim it",
			"pid", pid, "port", spec.Port, "error", exitErr)
		return fmt.Errorf("embeddedllm: %w after it became ready (%s)",
			ErrServerDied, exitReason(exitErr))
	}
	// A launch that became ready proves the fit contract held; whatever the
	// previous run complained about no longer describes this installation.
	s.fitWarning = ""
	event := s.transitionLocked(StateLoaded, "")
	// The idle budget starts HERE, after the weights are in memory — see step 7.
	// Stamping it at the spawn instead would charge a multi-minute weight load
	// to the operator's inactivity budget.
	s.recordActivityLocked()
	s.mu.Unlock()
	s.emit(event)

	// The server is resident and answering, so it can now be ASKED what context
	// it actually came up with. This is the only moment that question has an
	// answer, and the only path allowed to make it: GetConfig stays
	// network-free, and the load path already spawned the process and waited
	// for it. Fail-soft — a readback that fails leaves the recorded value and
	// never fails a load that is already serving.
	effectiveContext := s.recordEffectiveContext(ctx, launch, manifest)

	s.logger().Info("embedded LLM ready",
		"port", spec.Port, "pid", run.pid, "backend", manifest.Backend,
		"context_size", effectiveContext, "launch_context", spec.ContextSize,
		"layers", spec.Layers, "fit", spec.Fit, "plan_refreshed", launch.Refreshed)
	return nil
}

// Unload stops the server, which deterministically returns its RAM and VRAM to
// the system (ADR-066 D5). It is a no-op when nothing is running.
//
// Termination is graceful first: the process is asked to exit, and killed only
// after StopTimeout. The resulting state is installed — the bytes stay on disk,
// the model is simply not resident.
//
// The caller's ctx bounds ONLY the wait for the single-instance gate. Once the
// gate is held, the stop runs on a detached context budgeted at
// StopTimeout + the kill wait, so a budget nearly spent behind a cold load still
// gets the full graceful window rather than being cut down to whatever was left
// and killing the child early.
//
// A caller whose ctx expires while the single-instance gate is held by an
// in-flight Load does NOT get a timeout error back: the live process is stopped
// anyway (forceUnload), because the alternative is an orphan the app can never
// identify again.
func (s *Server) Unload(ctx context.Context) error {
	return s.unload(ctx, nil, true)
}

// Stop is Unload under the name the shutdown path and Installer.Stop use: the
// signatures match, so wiring is `installer.Stop = server.Stop`.
//
// Shutdown is exactly the caller the force path exists for: it budgets 30 s
// against a Load that can hold the gate for DefaultReadyTimeout (15 min), and a
// Stop that returned "context deadline exceeded" there would leave llama-server
// running — detached from every caller context, unrecorded in the manifest, and
// routed around rather than adopted by the next launch's port scan — holding its
// gigabytes of RAM and VRAM until a manual kill or a reboot.
func (s *Server) Stop(ctx context.Context) error {
	return s.unload(ctx, nil, true)
}

// unloadIfIdle is Unload for the idle path, with two differences.
//
// It carries the activity generation the expiry decided on and abandons the
// unload if activity moved in the meantime — Unload has to re-acquire both the
// gate and Server.mu, and a request that finished exactly at the deadline must
// not have the model pulled out from under it.
//
// It never takes the force path: an idle unload that cannot get the gate is a
// load in progress, and killing that would trade a deferrable unload for a
// failed cold start.
func (s *Server) unloadIfIdle(ctx context.Context, generation uint64) error {
	return s.unload(ctx, &generation, false)
}

// unload is the shared teardown. idleGen non-nil enables the idle path's
// activity re-check; force enables the no-gate stop a caller with an expired
// budget gets.
func (s *Server) unload(ctx context.Context, idleGen *uint64, force bool) error {
	if s == nil {
		return errors.New("embeddedllm: nil Server")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	release, err := s.acquireGate(ctx)
	if err != nil {
		if !force {
			return fmt.Errorf("embeddedllm: waiting for the embedded LLM to become available: %w", err)
		}
		return s.forceUnload(ctx, err)
	}
	defer release()

	// The caller's budget bounds the GATE WAIT and nothing else — the contract
	// Unload/Stop document and the backend's embeddedStopTimeout is sized on. A
	// ctx that arrives here nearly spent, because a cold load held the gate until
	// just before the deadline, must not have terminate's graceful window cut down
	// to what is left of it: terminate's `case <-ctx.Done()` sits INSIDE that
	// window and falls straight through to Kill, so a late acquisition would
	// SIGKILL llama-server instead of giving it the full SIGTERM window. The stop
	// therefore gets its own budget on a detached context, derived exactly the way
	// forceUnload derives its own: the values the caller's context carries travel
	// with it, only its deadline does not.
	stopCtx, cancelStop := context.WithTimeout(context.WithoutCancel(ctx),
		s.stopTimeout()+s.killWait())
	defer cancelStop()

	if idleGen != nil {
		s.mu.Lock()
		moved := s.idle.generation() != *idleGen
		s.mu.Unlock()
		if moved {
			// Activity landed between the expiry's decision and this call: a
			// fresh budget is already armed and this unload is the stale one.
			s.logger().Debug("an idle unload was superseded by activity; keeping the model resident")
			return nil
		}
	}

	s.mu.Lock()
	run := s.run
	if run == nil {
		// Nothing is running. A stale error is cleared, because "no process" is
		// the truth this call was asked to establish; a not-installed state is
		// left alone, since Unload does not change what is on disk.
		var event *StateEvent
		if s.stateLocked() != StateNotInstalled {
			event = s.transitionLocked(StateInstalled, "")
		}
		s.mu.Unlock()
		s.emit(event)
		return nil
	}
	run.expected = true
	event := s.transitionLocked(StateUnloading, "")
	s.mu.Unlock()
	s.emit(event)

	err = s.terminate(stopCtx, run)

	// The supervisor normally records the final transition; do it here too so
	// Unload's caller can rely on the state when it returns. transitionLocked
	// de-duplicates, so whichever runs first wins and no double event escapes.
	//
	// A stop that did not take is reported as an error and KEEPS the run handle:
	// claiming "installed" while a process is still resident would misreport the
	// RAM, and dropping the handle would let the next Load start a second one.
	s.mu.Lock()
	var final *StateEvent
	if err != nil {
		final = s.transitionLocked(StateError, err.Error())
	} else {
		if s.run == run {
			s.run = nil
		}
		final = s.transitionLocked(StateInstalled, "")
	}
	s.mu.Unlock()
	s.emit(final)

	if err != nil {
		return err
	}
	s.logger().Info("embedded LLM unloaded", "port", s.Port())
	return nil
}

// forceUnload stops a live process WITHOUT the single-instance gate, which is
// what an in-flight Load holds for up to ReadyTimeout. It reuses the leftover-run
// machinery Load already has: takeRun detaches the handle — which also tells the
// supervisor that this exit is nobody's news — and terminate performs the same
// graceful-then-kill stop the gated path does.
//
// The detach is provisional: a stop that does NOT take re-attaches the handle
// before it reports the failure, so the invariant the gated unload documents and
// TestUnloadReportsAStopThatDidNotTake pins holds on this path too — a live child
// is always tracked, and the next attempt targets the same process instead of
// stacking a second one beside it.
//
// The caller's context is deliberately NOT the one bounding the stop: it has
// already expired, which is why this path is running at all. The stop gets its
// own budget, so a wedged child cannot outlast it either.
//
// A Load still in flight owns the terminal transition, and forceUnload leaves it
// there. Both sides wake on the same close(run.died) and then transition
// independently under s.mu, so a success transition made here could land second
// and erase the interrupted load's diagnosis. forceUnload therefore snapshots,
// under the same lock hold that marks the run expected, whether a Load is still
// inside the part of its sequence that owns a terminal transition
// (loadsInFlight positive with the state still the StateLoading that Load wrote
// before it spawned). When it is, the success transition is SKIPPED entirely.
// When no Load owes a transition, the model was merely resident and "installed"
// is the honest terminal state, exactly as before.
//
// What that snapshot does NOT guarantee is which transition the interrupted Load
// writes, because the snapshot is taken BEFORE terminate signals the child — an
// s.emit (which runs the backend's synchronous OnState handler) and a Warn log
// sit in between. A readiness poll that lands in that window answers against a
// server that is still serving, so waitReady can return NIL and the load can take
// its success path after the child has been asked to die. That branch is handled
// on the Load side, not here: Load re-validates that the run it published is
// still the published one before it claims residency, and reports StateInstalled
// with an error naming the interrupted load instead of StateLoaded for a process
// that no longer exists (see errStoppedDuringLoad). The invariant that survives
// every interleaving is therefore the one that matters — a dead child is never
// reported as loaded — and the two terminal outcomes are StateError carrying the
// load's own diagnosis (the death was observed first) or StateInstalled with the
// load returning that error (readiness won the race).
func (s *Server) forceUnload(ctx context.Context, gateErr error) error {
	run := s.takeRun()
	if run == nil {
		// Nothing to kill: the gate is held by a Load that has not spawned yet
		// (it is still reading the manifest or scanning for a port), so the
		// caller's timeout is the truth and no process is left behind.
		return fmt.Errorf("embeddedllm: waiting for the embedded LLM to become available: %w", gateErr)
	}

	s.mu.Lock()
	run.expected = true
	// Snapshotted BEFORE the transition below, which moves the state off
	// StateLoading and would otherwise hide the very fact being asked about.
	loadOwesTerminal := s.loadsInFlight > 0 && s.stateLocked() == StateLoading
	event := s.transitionLocked(StateUnloading, "")
	s.mu.Unlock()
	s.emit(event)

	s.logger().Warn("stopping the embedded LLM without the single-instance gate",
		"pid", run.pid, "gate", gateErr, "load_in_flight", loadOwesTerminal)

	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx),
		s.stopTimeout()+s.killWait()+time.Second)
	defer cancel()
	err := s.terminate(stopCtx, run)

	s.mu.Lock()
	var final *StateEvent
	leftToLoad := false
	switch {
	case err != nil:
		// A stop that did not take KEEPS the run handle, exactly as the gated
		// unload does. takeRun already detached it, and leaving it detached would
		// orphan a live child: supervise compares s.run against its own run and
		// reports nothing, a later Unload finds no handle and claims success while
		// the process keeps running — surviving app exit with its gigabytes and
		// its loopback port — and the next Load spawns a SECOND server on a
		// machine whose memory gate was priced for one. Re-attaching makes the
		// next attempt target the same process. `expected` stays set: this stop
		// WAS asked for, so an exit that lands later is not a crash to report.
		if s.run == nil {
			s.run = run
		}
		final = s.transitionLocked(StateError, err.Error())
	case loadOwesTerminal:
		// The interrupted Load writes the terminal transition — see the doc.
		leftToLoad = true
	default:
		final = s.transitionLocked(StateInstalled, "")
	}
	s.mu.Unlock()
	s.emit(final)
	if leftToLoad {
		s.logger().Debug("leaving the terminal state of the force-stopped embedded LLM to the interrupted load")
	}

	if err != nil {
		return err
	}
	s.logger().Info("embedded LLM stopped without the gate", "pid", run.pid)
	return nil
}

// SetInstalled records whether the weights and runtime are present, without
// touching any process. Startup calls it with true after restoring the
// manifest; Remove calls it with false.
//
// It is refused while a server is live: the state of a running process belongs
// to the supervisor, and silently rewriting it would let a caller report a
// running model as absent (or the reverse).
func (s *Server) SetInstalled(installed bool) error {
	if s == nil {
		return errors.New("embeddedllm: nil Server")
	}
	s.mu.Lock()
	if s.run != nil {
		state := s.stateLocked()
		s.mu.Unlock()
		return fmt.Errorf("%w (state %s)", ErrServerBusy, state)
	}
	next := StateNotInstalled
	if installed {
		next = StateInstalled
	}
	event := s.transitionLocked(next, "")
	s.mu.Unlock()
	s.emit(event)
	return nil
}

// State reports the current supervision state.
func (s *Server) State() State {
	if s == nil {
		return StateNotInstalled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

// Status reports a snapshot for the UI.
func (s *Server) Status() Status {
	if s == nil {
		return Status{State: StateNotInstalled}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := Status{
		State:      s.stateLocked(),
		Port:       s.port,
		Since:      s.since,
		Message:    s.message,
		FitWarning: s.fitWarning,
	}
	if s.run != nil {
		snapshot.Pid = s.run.pid
	}
	return snapshot
}

// Port is the loopback port of the current (or most recent) server, or 0 when
// no install record has been read.
func (s *Server) Port() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// BaseURL is the OpenAI-compatible base URL of the supervised server. It is
// derived from the persisted port, never stored independently, and matches the
// provider base_url backend/config generates.
func (s *Server) BaseURL() string {
	return baseURL(s.Port())
}

// ── gate ──

// acquireGate takes the single-instance token, honouring ctx while waiting. It
// returns the release function; a nil error always comes with a non-nil
// release.
func (s *Server) acquireGate(ctx context.Context) (func(), error) {
	s.mu.Lock()
	if s.gate == nil {
		s.gate = make(chan struct{}, 1)
	}
	gate := s.gate
	s.mu.Unlock()

	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ── launch derivation ──

// resolvedLaunch is one Load's fully derived launch: the command line to run,
// plus the memory decision that produced its shape and the measurement that
// decision was made from. The extra fields exist because a load is allowed to
// REFINE the record — an opportunistic device probe may answer with a different
// accelerator than the install saw — and a refinement that is not written back
// is a refinement the next load has to rediscover.
type resolvedLaunch struct {
	// Spec is the validated launch specification.
	Spec LaunchSpec
	// Plan is the shape Spec renders, or nil for a manifest that recorded none
	// (a pre-plan install), where the shape came from the pure policy instead.
	Plan *MemoryPlan
	// Topology is the measurement Plan was made from — the fresh one when the
	// load-time probe answered, the manifest's snapshot when it did not, nil
	// when there never was one.
	Topology *MemoryTopology
	// Refreshed reports whether Plan or Topology differ from the manifest's, so
	// a load only rewrites the record when it actually learned something.
	Refreshed bool
}

// launchSpec turns the install record into a launch specification.
//
// The IDENTITY half — the binary, the weights, the projector, the loopback
// socket and `--image-max-tokens` — always comes from the manifest and the
// layout, keyed on the EFFECTIVE backend the manifest recorded, because those
// describe bytes that are on disk and a re-derivation could only disagree with
// them.
//
// The SHAPE half comes from `Manifest.Plan` when the install recorded one, and
// from the pure policy in resolve.go when it did not (a pre-plan manifest:
// explicit `-ngl`, `-fit off`, the recorded context tier, one slot, and the
// runtime's own defaults for everything else).
//
// A recorded plan is then OPTIONALLY refined by a load-time device probe,
// bounded by LoadProbeTimeout. The refinement is FAIL-SOFT in every direction —
// an unwired probe, a wedged or absent driver query, an unreadable memory
// profile and a re-plan that now refuses the machine all leave the recorded plan
// untouched and log at Debug — which replaces the older, absolute "no hardware
// probe runs at load time" rule with the property that rule existed to
// guarantee: A LOAD MUST NOT FAIL BECAUSE A DRIVER QUERY WEDGED. What the probe
// buys is freshness: a GPU that appeared, disappeared or got a new driver since
// the install is priced at launch instead of being served a shape computed for a
// machine that no longer exists.
func (s *Server) launchSpec(ctx context.Context, manifest Manifest, port int) (resolvedLaunch, error) {
	identity, err := s.launchIdentity(manifest, port)
	if err != nil {
		return resolvedLaunch{}, err
	}

	plan, topology := s.effectivePlan(ctx, manifest, identity.ServerBinary)
	refreshed := planChanged(manifest.Plan, plan) || topologyChanged(manifest.Topology, topology)

	var spec LaunchSpec
	if plan != nil {
		spec = identity.ApplyMemoryPlan(*plan)
	} else {
		// A manifest with no recorded plan: reproduce the shape this path has
		// always derived, from the pure policy and the recorded context tier.
		platform := s.Platform
		if platform == "" {
			platform = runtime.GOOS + "-" + runtime.GOARCH
		}
		spec = identity
		// The install record pins the offload, so this is the exclusivity
		// rule's second branch: `-fit off` with an explicit -ngl. Rendering the
		// switch rather than inheriting the fork's own 'on' default is what
		// keeps that pinned offload from aborting inside fit.cpp — see
		// LaunchSpec.Fit.
		spec.Fit = false
		spec.Layers = LayerCount(layersFor(platform, manifest.Backend))
		// The manifest's tier is the context. A zero one is only meaningful
		// under fit, and this path never runs fit, so Validate refuses it —
		// which is how TestLaunchSpecRefusesACorruptContext catches a manifest
		// whose tier was lost.
		spec.ContextSize = manifest.ContextSize
		// One slot: c0wrk serves one agent loop over one loopback socket and
		// issues one request at a time per server. The fork's auto default
		// would split the context across slots — see DefaultParallel.
		spec.Parallel = DefaultParallel
	}

	if err := spec.Validate(); err != nil {
		return resolvedLaunch{}, fmt.Errorf("embeddedllm: %w", err)
	}
	return resolvedLaunch{Spec: spec, Plan: plan, Topology: topology, Refreshed: refreshed}, nil
}

// launchIdentity resolves the half of a launch that describes what is ON DISK:
// the runtime binary, the recorded weights, the optional vision projector, the
// loopback socket and the backend-keyed image-token cap. It carries no memory
// decision, so neither half can overwrite the other — see ApplyMemoryPlan.
//
// Both path halves are containment-checked against the layout, so a tampered
// install record cannot aim the launch at bytes outside it: the binary is
// derived from the runtime tree and walked through ServerBinaryPath, and the
// recorded model file must lie inside the model root (Layout.OwnsModel).
func (s *Server) launchIdentity(manifest Manifest, port int) (LaunchSpec, error) {
	runtimeDir, err := s.Layout.RuntimeDir(manifest.Backend)
	if err != nil {
		return LaunchSpec{}, err
	}
	binary, err := ServerBinaryPath(runtimeDir, s.hostOS())
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("embeddedllm: %w", err)
	}

	modelFile := manifest.ModelFile
	if modelFile == "" || !s.Layout.OwnsModel(modelFile) {
		// The recorded path is trusted only inside the layout's model root (see
		// Layout.OwnsModel). A violation falls back to the layout-derived path for
		// the recorded packing rather than refusing: the derivation is what an
		// install writes, so it is also the honest recovery — and the divergence
		// is logged, because a record that does not describe the layout is a fact
		// an operator should see.
		derived, deriveErr := s.Layout.ModelFile(manifest.Packing)
		if deriveErr != nil {
			return LaunchSpec{}, deriveErr
		}
		if modelFile != "" {
			s.logger().Warn("the recorded model file is outside the model root; launching the layout-derived path instead",
				"recorded", modelFile, "derived", derived, "model_root", s.Layout.ModelRoot)
		}
		modelFile = derived
	}
	if !pathExists(modelFile) {
		return LaunchSpec{}, fmt.Errorf("%w: the recorded model file %q is missing — reinstall the model",
			ErrNotInstalled, modelFile)
	}

	// The projector is optional at launch: a missing one means no vision, which
	// beats refusing to serve text at all.
	mmproj := ""
	if asset := MMProjAsset(); asset.ArchiveName != "" {
		candidate, destErr := s.Layout.Destination(asset)
		switch {
		case destErr != nil:
			s.logger().Warn("the vision projector path could not be resolved",
				"error", destErr)
		case !pathExists(candidate):
			s.logger().Warn("the vision projector is missing; starting without image input",
				"path", candidate)
		default:
			mmproj = candidate
		}
	}

	return LaunchSpec{
		ServerBinary: binary,
		ModelFile:    modelFile,
		MMProjFile:   mmproj,
		Host:         LoopbackHost,
		Port:         port,
		// A pure function of the EFFECTIVE backend the manifest recorded, so it
		// is re-derived rather than stored: it is a property of the artifact
		// set, not of a memory measurement.
		ImageMaxTokens: imageMaxTokensFor(manifest.Backend),
		// Fit, Layers, ContextSize, Parallel and the whole memory-plan surface
		// are filled by the caller — from Manifest.Plan through
		// ApplyMemoryPlan, or from the pure policy for a plan-less manifest.
	}, nil
}

// effectivePlan decides the shape a load launches: the manifest's recorded plan,
// refined by a fresh measurement when one can be had cheaply, and unchanged
// otherwise.
//
// It NEVER fails and never blocks longer than LoadProbeTimeout. Every way the
// refinement can go wrong — no probe wired, no recorded plan to refine, a driver
// query that wedges or answers nothing, an unreadable memory profile, a planner
// that now refuses the machine — leaves the stored decision in place and logs at
// Debug. The reasoning is the one the install's own probes already follow: a
// measurement REFINES a decision, it is never a precondition of one. A load-time
// refusal would also be a new failure mode with no user action behind it — the
// memory gate that decides whether this machine may hold the model ran at
// install, when the bytes were chosen and the user accepted them.
func (s *Server) effectivePlan(ctx context.Context, manifest Manifest, binary string) (*MemoryPlan, *MemoryTopology) {
	stored := manifest.Plan
	snapshot := manifest.Topology

	if s.ProbeDevices == nil {
		// Not wired: the pre-existing contract, and the manifest is the whole
		// story.
		return stored, snapshot
	}
	if stored == nil {
		// Nothing to refine. A fresh measurement with no recorded plan would
		// have to invent a shape from the pure policy, which is a different and
		// coarser decision than the one this install made — so the launch stays
		// legacy and no topology is recorded beside a plan that ignores it.
		return nil, nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, s.loadProbeTimeout())
	defer cancel()
	fresh, ok := s.ProbeDevices(probeCtx, binary, s.logger())
	if !ok {
		// THE FAIL-SOFT CASE the load-time probe exists to survive: a wedged or
		// absent driver query falls back to the snapshot the install recorded,
		// and the load proceeds on the shape this machine was provisioned for.
		s.logger().Debug("the embedded LLM load-time device probe did not answer; launching the recorded plan")
		return stored, snapshot
	}

	replanned, err := s.replan(manifest, fresh)
	if err != nil {
		s.logger().Debug("the embedded LLM launch shape was not re-planned from the fresh topology",
			"error", err, "gpu_family", ClassifyGPUs(fresh.Devices))
		return stored, snapshot
	}

	measured := fresh
	if planChanged(stored, &replanned) {
		s.logger().Info("the embedded LLM launch shape changed after the load-time device probe",
			"gpu_family", replanned.GPUFamily, "fit", replanned.FitArg(),
			"from_context", stored.ContextSize, "to_context", replanned.ContextSize,
			"device_budget_mib", measured.DeviceBudgetMiB())
	}
	return &replanned, &measured
}

// replan re-derives the launch shape from a fresh measurement while holding the
// plan to the artifacts that are actually on disk.
//
// The packing is PINNED to the manifest's. That is the one input a load-time
// re-plan must not be allowed to re-decide: `Plan` derives a packing from the
// backend and GPU family whenever the tuning leaves it unset, and a different
// packing would price a footprint the installed GGUF does not have — so the
// context and offload it returned would be computed for bytes that are not
// there. A packing change is an install decision, because it changes which
// multi-gigabyte file gets downloaded.
func (s *Server) replan(manifest Manifest, topology MemoryTopology) (MemoryPlan, error) {
	profile, err := PinnedMemoryProfile()
	if err != nil {
		return MemoryPlan{}, fmt.Errorf("memory profile: %w", err)
	}
	tuning := s.tuning()
	if manifest.Packing != "" {
		tuning.Packing = manifest.Packing
	}
	family := ClassifyGPUs(topology.Devices)
	if family == GPUFamilyUnknown {
		// An inventory the classifier does not recognize is not evidence that the
		// accelerator changed generation; the install's classification is a
		// better answer than "unknown".
		family = manifest.GPUFamily
	}
	return Plan(topology, profile, tuning, manifest.Backend, family)
}

// tuning resolves the operator's memory-plan overrides for a launch. nil → the
// zero Tuning, which IS the all-Auto plan, so an unwired supervisor still gets a
// coherent decision rather than a nil dereference.
func (s *Server) tuning() Tuning {
	if s.Tuning == nil {
		return Tuning{}
	}
	return s.Tuning()
}

func (s *Server) loadProbeTimeout() time.Duration {
	if s.LoadProbeTimeout > 0 {
		return s.LoadProbeTimeout
	}
	return DefaultLoadProbeTimeout
}

// planChanged and topologyChanged compare a recorded decision against the one a
// load is about to apply, so a load rewrites the manifest only when it actually
// learned something: an unchanged record rewritten on every cold start is a disk
// write, a new mtime and a spurious change signal for nothing. Both count
// nil-vs-present as a change, which is the point — a legacy manifest gaining its
// first plan IS worth recording.
func planChanged(recorded, applied *MemoryPlan) bool {
	if recorded == nil || applied == nil {
		return recorded != applied
	}
	return !reflect.DeepEqual(*recorded, *applied)
}

func topologyChanged(recorded, measured *MemoryTopology) bool {
	if recorded == nil || measured == nil {
		return recorded != measured
	}
	return !reflect.DeepEqual(*recorded, *measured)
}

// ── post-ready context readback ──

// propsURL is the server's own property endpoint. It is NOT under /v1: the
// fork's server exposes /props at the root, beside /health, and only the
// OpenAI-compatible surface lives under /v1.
func propsURL(port int) string {
	return "http://" + LoopbackHost + ":" + strconv.Itoa(port) + "/props"
}

// propsPayload is the slice of the /props response the context readback needs.
//
// `default_generation_settings.n_ctx` is the PER-SLOT context — the fork splits
// the `-c` value across `-np` slots — and `total_slots` is how many it split it
// into, so the effective total the model can actually hold is their product.
// Reading only the per-slot figure would under-report a multi-slot server by a
// factor of `-np`; c0wrk launches `-np 1` (DefaultParallel), so today the two
// agree, and the multiplication is what keeps the recorded value honest if that
// ever changes.
type propsPayload struct {
	DefaultGenerationSettings struct {
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
	TotalSlots int `json:"total_slots"`
}

// readPropsContext asks a READY server what context it came up with.
//
// It is fail-soft and returns (0, false) for every way the question can go wrong
// — a non-200, an unreadable or unparseable body, a missing or non-positive
// `n_ctx`, a product that would overflow, and a product above the model's own
// training context — because the answer refines a recorded value and must never
// become the reason a load that is already serving gets reported as failed.
// `total_slots` is the one field allowed to be absent: a server that does not
// report it is serving one slot, and treating an absent count as zero would
// report a context of 0.
//
// The ceiling is not a refinement of that fail-softness but the point of it: this
// is the only context figure in the subsystem that arrives from an external
// process over HTTP, and it lands in two durable stores — the manifest and, via
// PersistContext, the tier-1 `llm.models` `context_window` override that shadows
// every later attempt to correct it. An absurd value therefore poisons the
// router's context accounting (a huge positive figure reports "ok" forever, so
// compaction never triggers) with no way for an operator to repair it: the
// readback re-runs on every load and clobbers a hand-edit. Every other context
// figure in the package is bounded (LaunchSpec.validateContext, the tuning range
// in config); this one is bounded here.
func readPropsContext(ctx context.Context, client *http.Client, url string, timeout time.Duration) (int, bool) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, false
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPropsBodyLen))
	if err != nil || resp.StatusCode != http.StatusOK {
		return 0, false
	}
	var payload propsPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, false
	}
	perSlot := payload.DefaultGenerationSettings.NCtx
	if perSlot <= 0 {
		return 0, false
	}
	slots := payload.TotalSlots
	if slots <= 0 {
		slots = 1
	}
	// Total AND bounded, for the two reasons the doc gives: an unchecked
	// `perSlot * slots` wraps (4611686018427387904 × 4 is exactly 0, which the
	// guards above would happily report as an answer), and a huge positive
	// product is worse still.
	total, ok := mulContext(perSlot, slots)
	if !ok || total > maxTrainingContext {
		return 0, false
	}
	return total, true
}

// mulContext multiplies two context figures, reporting false when the product
// would overflow an int. Both operands come from an untrusted local socket, so
// the multiplication has to be total rather than relying on a wrap that a later
// `<= 0` check may or may not catch.
func mulContext(perSlot, slots int) (int, bool) {
	if perSlot > 0 && slots > math.MaxInt/perSlot {
		return 0, false
	}
	return perSlot * slots, true
}

// recordEffectiveContext reads the served context back from a ready server and
// makes it durable: the manifest's `ContextSize` and — through PersistContext —
// the tier-1 `llm.models."Bonsai 2 27B".context_window` override.
//
// This is the correction the recorded value needs. `llm-providers.md` gives a
// tier-1 config override precedence over the tier-1.5 lazy probe, so an override
// written at install can never be refined by asking the model later: it shadows
// the answer. Reading the real window on the LOAD path — the one path that has
// already spawned the process and waited for it, so it costs no extra startup
// and leaves `GetConfig` network-free — is what keeps the override honest
// instead of frozen at an estimate.
//
// The write is a MERGE onto the record as it is NOW, not a whole-file rewrite of
// the snapshot this load started from. `Installer.Install` never takes the
// supervisor's single-instance gate, so a repair or reinstall can complete while
// a cold load — started from the OLD runtime by the ensure-loaded transport — is
// becoming ready; overwriting manifest.json from the stale snapshot would then
// silently lose the install's refreshed plan, topology, checksums and port, which
// is exactly the degraded-plan visibility this record exists to guarantee. Only
// the three fields this load actually learned (ContextSize, Plan, Topology) are
// written, and the write is abandoned entirely when the record now describes a
// DIFFERENT install (sameInstall) — those three fields price the old bytes, so
// merging them onto a new record would corrupt it.
//
// It returns the context now on record. Fail-soft throughout: a readback that
// does not answer keeps the previous value, and a manifest or config write that
// fails is logged and otherwise ignored, because the model is resident and
// serving and that is the fact this Load was asked to establish.
func (s *Server) recordEffectiveContext(ctx context.Context, launch resolvedLaunch, manifest Manifest) int {
	recorded := manifest.ContextSize
	effective := recorded
	if n, ok := readPropsContext(ctx, s.httpClient(), propsURL(launch.Spec.Port), s.probeTimeout()); ok {
		effective = n
		// The served window can never legitimately exceed what this launch
		// itself ordered (`-c`, the RAM tier in the auto path, the operator's
		// pinned figure in the exact path — the same value rendered in argv).
		// Anything larger is a poisoned readback (a squatted port answering
		// /props, or a stale fit-sized server from a previous build). Clamping
		// here keeps the invariant the config documents — the override is
		// RAM-tiered, never the fork's full training context — in BOTH durable
		// stores (the manifest below and, through PersistContext, the tier-1
		// `llm.models.context_window`), so compaction and the output budget
		// scale from the window the server actually holds instead of an
		// absurd one. Fail-soft by construction: the launch value is always
		// positive on every path ApplyMemoryPlan renders, and when it is not
		// the clamp simply does not apply.
		if launched := launch.Spec.ContextSize; launched > 0 && effective > launched {
			s.logger().Warn("the embedded LLM reported a context above the launched one; clamping the readback",
				"reported", effective, "launched", launched)
			effective = launched
		}
	} else {
		s.logger().Debug("the embedded LLM did not report its context; keeping the recorded value",
			"port", launch.Spec.Port, "context_size", recorded)
	}

	path, err := s.Layout.ManifestPath()
	if err != nil {
		s.logger().Debug("the embedded LLM context readback was not persisted",
			"error", err, "context_size", effective)
		return effective
	}
	current, err := ReadManifest(path)
	if err != nil {
		// No record to merge onto: the install was removed (or its manifest
		// corrupted) while the load was running. Writing one back would resurrect
		// a record the operator just deleted.
		s.logger().Warn("the embedded LLM install record could not be re-read for the context readback",
			"error", err, "context_size", effective)
		return effective
	}
	if !sameInstall(current, manifest) {
		s.logger().Warn("the embedded LLM install record changed during the load; the context readback was not persisted",
			"context_size", effective,
			"loaded_packing", manifest.Packing, "recorded_packing", current.Packing,
			"loaded_backend", manifest.Backend, "recorded_backend", current.Backend)
		return effective
	}
	recorded = current.ContextSize
	if effective == recorded && !launch.Refreshed {
		// Nothing this load learned differs from the record: no write, no new
		// mtime and no spurious change signal.
		return effective
	}

	updated := current
	updated.ContextSize = effective
	updated.Plan = launch.Plan
	updated.Topology = launch.Topology
	// The port is deliberately NOT the one this load bound: manifest.json keeps
	// the port the install allocated, which is the preference the next load
	// re-scans from — see backend.persistEmbeddedPort.

	if err := writeManifest(path, updated, s.logger()); err != nil {
		s.logger().Warn("failed to persist the embedded LLM context readback",
			"error", err, "context_size", effective)
		return effective
	}
	s.logger().Debug("the embedded LLM manifest was updated from the live server",
		"context_size", effective, "recorded", recorded, "plan_refreshed", launch.Refreshed)

	if effective == recorded || s.PersistContext == nil {
		return effective
	}
	if err := s.PersistContext(ctx, effective); err != nil {
		// The manifest is already correct; only the config mirror lagged. The
		// next successful load retries, and the override keeps its previous —
		// still valid — value in the meantime.
		s.logger().Warn("failed to persist the embedded LLM context window override",
			"context_size", effective, "error", err)
	}
	return effective
}

// sameInstall reports whether two manifest records describe the SAME installed
// bytes: the identity fields an install rewrites when it provisions a different
// artifact set.
//
// The fields a load legitimately refines (ContextSize, Plan, Topology) are
// deliberately NOT part of the identity, and neither is the port — a preference
// the load re-scans and the backend persists separately. What is left is the set
// that makes a plan and a readback meaningless when it changes: a different
// packing or backend prices different bytes, a different runtime version or
// install time means the tree was replaced, and a different model path means the
// GGUF is not the one this load measured.
func sameInstall(recorded, loaded Manifest) bool {
	return recorded.Packing == loaded.Packing &&
		recorded.Backend == loaded.Backend &&
		recorded.RuntimeVersion == loaded.RuntimeVersion &&
		recorded.InstalledAt == loaded.InstalledAt &&
		recorded.ModelFile == loaded.ModelFile
}

// launchEnv builds the child environment: the parent's, with the runtime's own
// binary directory prepended to the platform's dynamic-library search path.
//
// The pinned archives ship their GPU/CUDA libraries next to the executable, and
// nothing is installed into a system location (ADR-066 D3: never under the
// agent's PATH). Without this, a Linux CUDA or ROCm build fails at dlopen time
// with a library-not-found error that looks like a broken install.
func launchEnv(binaryDir, goos string, parent []string) []string {
	env := make([]string, 0, len(parent)+1)
	if binaryDir == "" {
		return append(env, parent...)
	}
	key := libraryPathVar(goos)
	if key == "" {
		return append(env, parent...)
	}
	replaced := false
	for _, entry := range parent {
		name, value, found := strings.Cut(entry, "=")
		if !found || !strings.EqualFold(name, key) {
			env = append(env, entry)
			continue
		}
		replaced = true
		if value == "" {
			env = append(env, key+"="+binaryDir)
			continue
		}
		env = append(env, key+"="+binaryDir+pathListSeparator(goos)+value)
	}
	if !replaced {
		env = append(env, key+"="+binaryDir)
	}
	return env
}

// pathListSeparator is the list separator of the target platform's environment,
// derived from goos rather than from the host: the policy is a pure function of
// the platform it is built for, which is what keeps it table-testable from any
// CI machine.
func pathListSeparator(goos string) string {
	if goos == "windows" {
		return ";"
	}
	return ":"
}

// libraryPathVar names the environment variable the platform's loader consults.
// Windows has no separate one, so the binary directory goes on PATH.
func libraryPathVar(goos string) string {
	switch goos {
	case "windows":
		return "PATH"
	case "darwin":
		return "DYLD_LIBRARY_PATH"
	default:
		return "LD_LIBRARY_PATH"
	}
}

// ── spawn ──

// spawn builds the LaunchCommand and hands it to the spawner.
func (s *Server) spawn(ctx context.Context, spec LaunchSpec) (*processRun, error) {
	binaryDir := filepath.Dir(spec.ServerBinary)
	cmd := LaunchCommand{
		Binary: spec.ServerBinary,
		Args:   spec.Args(),
		Env:    launchEnv(binaryDir, s.hostOS(), os.Environ()),
		Dir:    binaryDir,
	}
	s.logger().Debug("spawning the embedded LLM server",
		"binary", cmd.Binary, "args", strings.Join(cmd.Args, " "))

	proc, err := s.spawner()(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("embeddedllm: starting %s: %w", ServerBinaryName, err)
	}
	return &processRun{
		proc: proc,
		pid:  proc.Pid(),
		tail: newLineTail(tailLines),
		died: make(chan struct{}),
	}, nil
}

func (s *Server) spawner() SpawnFunc {
	if s.Spawn != nil {
		return s.Spawn
	}
	return func(ctx context.Context, cmd LaunchCommand) (Process, error) {
		return spawnOSServer(ctx, cmd, s.logger())
	}
}

// serverWaitDelay bounds how long the supervised server's output may keep the
// supervision goroutine waiting after the process itself is gone. It is the
// long-lived sibling of hardware.go's probeWaitDelay and exists for the same
// documented hazard: killing (or losing) the child is not enough to end the
// read, because a grandchild that inherited the write end of its stdout keeps
// the pipe open and EOF never arrives.
//
// It bounds two things, which must agree:
//
//   - os/exec's own copy of the child's output into this package's relay (see
//     spawnOSServer): when the delay expires, os/exec closes the descriptor the
//     copy is blocked on and Wait returns exec.ErrWaitDelay;
//   - the pump drain in supervise, so a Process implementation whose readers do
//     not reach EOF cannot wedge supervision either.
//
// It is seconds rather than milliseconds because the pump is draining the real
// server's log tail, and abandoning it early would truncate the diagnostics a
// failed launch is reported with (crashMessage, scanFitFailure).
const serverWaitDelay = 5 * time.Second

// spawnOSServer starts the real llama-server.
//
// The context is detached with context.WithoutCancel: the one that started a load
// is usually an RPC or a UI action, and the server must outlive both. Its lifetime
// is owned by Unload/Stop, the idle timer and app shutdown — never by the caller
// that happened to ask for the load.
//
// The child's stdout/stderr are relayed through io.Pipes this package owns
// rather than handed out by exec.Cmd.StdoutPipe, and WaitDelay is set on the
// command. The pairing is the point: with a caller-owned *os.File os/exec starts
// no copy goroutine, so WaitDelay has nothing to bound and the ONLY thing that
// ends the read is the child's own exit — a grandchild holding the write end
// wedges the pumps and their supervisor for the lifetime of the app. Relaying
// through an io.Writer makes os/exec own the descriptor, which is what lets the
// delay close it. Nothing is lost in exchange: cmd.Wait still drains the copy
// goroutines to EOF before it returns, so the log tail keeps every line the
// server wrote before it died.
func spawnOSServer(ctx context.Context, cmd LaunchCommand, logger *slog.Logger) (Process, error) {
	// The binary and every argument come from the pinned install record and the
	// pure launch policy — never from a model, a user string or the network.
	proc := exec.CommandContext(context.WithoutCancel(ctx), cmd.Binary, cmd.Args...)
	proc.Dir = cmd.Dir
	proc.Env = cmd.Env
	// Suppress the console window a GUI-subsystem host would otherwise allocate
	// for the child (CREATE_NO_WINDOW on Windows, a no-op elsewhere). This is
	// the one child that runs for HOURS — up to the whole idle budget — so it is
	// the spawn where an allocated console is most visible: a Windows user would
	// otherwise get a terminal window they cannot dismiss without killing the
	// model.
	sysproc.HideConsole(proc)
	// Bound the output as well as the process: see serverWaitDelay.
	proc.WaitDelay = serverWaitDelay

	stdoutRead, stdoutWrite := io.Pipe()
	stderrRead, stderrWrite := io.Pipe()
	proc.Stdout = stdoutWrite
	proc.Stderr = stderrWrite

	if err := proc.Start(); err != nil {
		closeRelays([]*io.PipeWriter{stdoutWrite, stderrWrite})
		_ = stdoutRead.Close()
		_ = stderrRead.Close()
		return nil, err
	}
	logger.Debug("embedded LLM server started", "pid", proc.Process.Pid, "binary", cmd.Binary)
	return &osProcess{
		cmd:    proc,
		stdout: stdoutRead,
		stderr: stderrRead,
		relay:  []*io.PipeWriter{stdoutWrite, stderrWrite},
	}, nil
}

// closeRelays closes the relay write ends, which is what turns the end of a
// copy into io.EOF for the pumps reading the other end. Errors are dropped: the
// only one possible is a double close, and the caller is already tearing down.
func closeRelays(relay []*io.PipeWriter) {
	for _, w := range relay {
		_ = w.Close()
	}
}

// osProcess adapts *exec.Cmd to Process. Stdout/Stderr hand out the read ends of
// the relays spawnOSServer installed; Wait closes their write ends once os/exec
// has finished copying, so the pumps always reach EOF.
type osProcess struct {
	cmd    *exec.Cmd
	stdout io.Reader
	stderr io.Reader

	relayOnce sync.Once
	relay     []*io.PipeWriter
}

// Wait blocks until the child has exited AND os/exec has finished (or, at
// serverWaitDelay, abandoned) copying its output, then ends the relays.
func (p *osProcess) Wait() error {
	err := p.cmd.Wait()
	p.relayOnce.Do(func() { closeRelays(p.relay) })
	return err
}

func (p *osProcess) Pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *osProcess) Signal(sig os.Signal) error {
	if p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return p.cmd.Process.Signal(sig)
}

func (p *osProcess) Kill() error {
	if p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return p.cmd.Process.Kill()
}

func (p *osProcess) Stdout() io.Reader { return p.stdout }

func (p *osProcess) Stderr() io.Reader { return p.stderr }

// gracefulSignal is what Unload sends before resorting to a kill. Windows has
// no graceful child-process signal, so Signal fails there and the caller falls
// back to Kill — which is exactly what this fallback ordering is for.
func gracefulSignal() os.Signal { return syscall.SIGTERM }

// ── output ──

// pumpOutput starts one drain goroutine per output stream, into slog and into
// the run's tail. wg reports when both have finished, which is what supervise
// waits on (bounded — see waitExit).
func (s *Server) pumpOutput(run *processRun, wg *sync.WaitGroup) {
	s.pump(run, wg, run.proc.Stdout(), "stdout")
	s.pump(run, wg, run.proc.Stderr(), "stderr")
}

// pump drains one output stream into slog and into the run's tail until it ends.
//
// An over-long line is SKIPPED, not fatal. bufio.Scanner stops for good at
// bufio.ErrTooLong, which would leave the child running with nothing draining
// its pipe: once the OS pipe buffer filled, the server's own writes would block
// and it would wedge mid-generation while the supervisor still reported
// "loaded". So the stream is read with ReadSlice, which reports a too-long line
// as a full buffer instead of an error, and the rest of that line is discarded
// before pumping continues.
func (s *Server) pump(run *processRun, wg *sync.WaitGroup, r io.Reader, stream string) {
	if r == nil {
		return
	}
	logger := s.logger()
	wg.Add(1)
	go func() {
		defer wg.Done()
		reader := bufio.NewReaderSize(r, maxLogLineBytes)
		for {
			chunk, readErr := reader.ReadSlice('\n')
			if errors.Is(readErr, bufio.ErrBufferFull) {
				// A line longer than the cap is dropped WHOLE: keeping the first
				// cap-sized chunk would turn the tail's bound (tailLines ×
				// maxLogLineBytes) into a megabyte-scale buffer for one line, and
				// a partial line is not a more useful diagnostic than the marker.
				skipped := len(chunk) + skipLine(reader)
				run.tail.add(fmt.Sprintf("[a log line longer than %d bytes was skipped: %d bytes]",
					maxLogLineBytes, skipped))
				logger.Debug("the embedded LLM emitted an over-long output line; it was skipped",
					"stream", stream, "pid", run.pid, "cap", maxLogLineBytes, "skipped", skipped)
				continue
			}
			if line := strings.TrimRight(string(chunk), "\r\n"); line != "" {
				run.tail.add(line)
				logger.Debug("embedded LLM server output",
					"stream", stream, "pid", run.pid, "line", line)
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					logger.Debug("the embedded LLM output stream ended early",
						"stream", stream, "pid", run.pid, "error", readErr)
				}
				return
			}
		}
	}()
}

// skipLine discards the remainder of an over-long line and reports how many
// bytes went. It stops at the newline that ends the line, at EOF, or at a read
// error — all of which mean the caller's next ReadSlice sees the same condition.
func skipLine(r *bufio.Reader) int {
	skipped := 0
	for {
		chunk, err := r.ReadSlice('\n')
		skipped += len(chunk)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return skipped
		}
	}
}

// lineTail keeps the last n lines of server output. A crash or a failed load is
// only actionable if the server's own complaint comes with it: "the process
// exited" is not a diagnosis, "ggml_cuda_init: found 0 devices" is.
type lineTail struct {
	mu    sync.Mutex
	limit int
	lines []string
}

func newLineTail(limit int) *lineTail {
	if limit <= 0 {
		limit = tailLines
	}
	return &lineTail{limit: limit}
}

func (t *lineTail) add(line string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if over := len(t.lines) - t.limit; over > 0 {
		t.lines = t.lines[over:]
	}
}

func (t *lineTail) String() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}

// ── supervision ──

// supervise owns one process lifetime: observe the child's exit, let the output
// pumps finish within drainWait, then report it. It is the only goroutine that
// closes run.died.
//
// The exit comes FIRST and the drain is bounded and abandonable — waitExit owns
// that ordering and its doc says why the naive "drain the pipes, then Wait"
// shape it inverts would let a grandchild holding the child's stdout wedge this
// goroutine for the lifetime of the app. Supervision may abandon a pump; it may
// never hang.
func (s *Server) supervise(run *processRun) {
	var wg sync.WaitGroup
	s.pumpOutput(run, &wg)
	pumpsDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(pumpsDone)
	}()
	exitErr := s.waitExit(run, pumpsDone)

	s.mu.Lock()
	run.exitErr = exitErr
	current := s.run == run
	expected := run.expected
	abandoned := run.abandoned
	if current {
		s.run = nil
	}
	close(run.died)
	var event *StateEvent
	if current && !abandoned {
		if expected {
			event = s.transitionLocked(StateInstalled, "")
		} else {
			s.fitWarning = scanFitFailure(run.tail.String())
			event = s.transitionLocked(StateError, s.crashMessage(run, exitErr))
		}
	}
	pid := run.pid
	s.mu.Unlock()

	if current && !abandoned && !expected {
		s.logger().Error("the embedded LLM server died",
			"pid", pid, "error", exitErr, "last_output", run.tail.String())
	}
	s.emit(event)
}

// waitExit observes the child's exit and then lets the output pumps finish,
// bounded.
//
// The exit is observed FIRST, which inverts the naive "drain the pipes, then
// Wait" ordering on purpose. cmd.Wait is what bounds the output relay
// (serverWaitDelay) and what ends it, so waiting for EOF first would leave a
// grandchild that inherited the child's stdout able to wedge this goroutine and
// its two pumps for the lifetime of the app — and every later Load would then pay
// a killWait discard delay against the zombie run. Ordering it this way costs
// nothing in diagnostics: cmd.Wait does not return until os/exec's copy has
// reached EOF, so the tail still holds every line the server wrote before it died.
//
// The bounded pump wait extends the same guarantee to a Process implementation
// that does not relay through os/exec (a test double whose readers never end):
// supervision may abandon a pump, but it may never hang. Abandoning is safe —
// lineTail is mutex-guarded and the tail is only ever read for diagnostics.
//
// exec.ErrWaitDelay is NOT a crash. os/exec returns it in place of a NIL exit
// error when the process exited successfully but its output had to be abandoned
// at the delay, so passing it through would turn a clean exit into a spurious
// StateError whose cause reads "exec: WaitDelay expired before I/O complete".
func (s *Server) waitExit(run *processRun, pumpsDone <-chan struct{}) error {
	exitErr := run.proc.Wait()

	grace := s.drainWait()
	drain := time.NewTimer(grace)
	defer drain.Stop()
	select {
	case <-pumpsDone:
	case <-drain.C:
		s.logger().Warn("the embedded LLM output pumps did not finish; abandoning them",
			"pid", run.pid, "grace", grace)
	}

	if errors.Is(exitErr, exec.ErrWaitDelay) {
		s.logger().Warn("the embedded LLM server's output outlived the process; it was abandoned",
			"pid", run.pid, "wait_delay", serverWaitDelay)
		return nil
	}
	return exitErr
}

// crashMessage renders the user-facing cause of an unexpected exit.
func (s *Server) crashMessage(run *processRun, exitErr error) string {
	message := fmt.Sprintf("%s (pid %d): %s", ServerBinaryName, run.pid, exitReason(exitErr))
	if tail := strings.TrimSpace(run.tail.String()); tail != "" {
		message += "\nlast server output:\n" + tail
	}
	return message
}

// scanFitFailure inspects a dead or failed run's bounded output tail for the
// fork's fit-failure complaint and renders it as the warning Status.FitWarning
// carries. The marker is matched as a substring so the fork's timestamp and
// level prefixes cannot hide it, and the SUCCESS spelling ("successfully fit
// params to free device memory", the line every captured trace ends with)
// deliberately does not match.
func scanFitFailure(tail string) string {
	if strings.Contains(tail, fitFailureMarker) {
		return "the runtime's --fit pass aborted: " + strconv.Quote(fitFailureMarker) +
			" — the recorded memory plan and this pin's fit contract disagree;" +
			" re-plan or re-review the pin before reloading"
	}
	return ""
}

// takeRun detaches and returns the live process handle, if any. Once detached,
// the supervisor treats its exit as unremarkable and reports no transition — the
// caller owns the state from here.
func (s *Server) takeRun() *processRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.run
	s.run = nil
	return run
}

// discardRun stops a run the supervisor no longer wants (a failed load) and
// waits, bounded, for the exit to be observed.
func (s *Server) discardRun(run *processRun) {
	if killErr := run.proc.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		s.logger().Debug("discarding the embedded LLM process failed",
			"pid", run.pid, "error", killErr)
	}
	timer := time.NewTimer(s.killWait())
	defer timer.Stop()
	select {
	case <-run.died:
	case <-timer.C:
		s.logger().Warn("the discarded embedded LLM process did not exit", "pid", run.pid)
	}
}

// terminate asks the process to stop, gracefully first, and kills it after the
// graceful window. It returns once the exit has been observed.
//
// ctx is a BACKSTOP, not the graceful window: a `case <-ctx.Done()` inside it
// falls straight through to the kill, so every caller derives one budgeted for
// the whole stop (unload and forceUnload both use
// `context.WithoutCancel(ctx)` + at least StopTimeout + the kill wait). Passing a
// caller's own nearly-spent budget here would silently shorten the window and
// SIGKILL a server that was about to exit on its own.
func (s *Server) terminate(ctx context.Context, run *processRun) error {
	if err := run.proc.Signal(gracefulSignal()); err != nil &&
		!errors.Is(err, os.ErrProcessDone) {
		// Not a failure: Windows has no graceful child signal, and a process
		// that is already gone needs no signal. Both fall through to the kill.
		s.logger().Debug("the graceful stop signal was not delivered",
			"pid", run.pid, "error", err)
	}

	grace := time.NewTimer(s.stopTimeout())
	defer grace.Stop()
	select {
	case <-run.died:
		return nil
	case <-grace.C:
	case <-ctx.Done():
		s.logger().Warn("the graceful stop was interrupted; killing the server",
			"pid", run.pid, "error", ctx.Err())
	}

	if err := run.proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		s.logger().Warn("killing the embedded LLM server failed", "pid", run.pid, "error", err)
	}
	hardWait := time.NewTimer(s.killWait())
	defer hardWait.Stop()
	select {
	case <-run.died:
		return nil
	case <-hardWait.C:
		// Only killWait was waited SINCE the kill — the graceful stopTimeout
		// was consumed before it — so the message must name killWait alone,
		// not the caller-facing stopTimeout+killWait total.
		return fmt.Errorf("embeddedllm: %s (pid %d) did not exit within %s of being killed",
			ServerBinaryName, run.pid, s.killWait())
	}
}

// ── readiness ──

// waitReady polls /v1/models until the model can serve. It gives up on the
// ready budget, on ctx, and — immediately — on the death of the process, so a
// server that segfaults during the weight load is reported in milliseconds
// instead of after a fifteen-minute timeout.
func (s *Server) waitReady(ctx context.Context, run *processRun, spec LaunchSpec) error {
	modelsURL := spec.ModelsURL()
	client := s.httpClient()
	probeTimeout := s.probeTimeout()

	var lastErr error
	probe := func() bool {
		probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		ready, err := probeModels(probeCtx, client, modelsURL)
		if err != nil {
			lastErr = err
			return false
		}
		return ready
	}

	if probe() {
		return nil
	}
	deadline := time.NewTimer(s.readyTimeout())
	defer deadline.Stop()
	ticker := time.NewTicker(s.pollInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("embeddedllm: waiting for %s to load the model: %w",
				ServerBinaryName, ctx.Err())
		case <-run.died:
			return fmt.Errorf("%w before it became ready (%s)\nlast server output:\n%s",
				ErrServerDied, exitReason(run.exitErr), strings.TrimSpace(run.tail.String()))
		case <-deadline.C:
			return fmt.Errorf("%w within %s (last probe: %s)",
				ErrLoadTimeout, s.readyTimeout(), probeDetail(lastErr))
		case <-ticker.C:
			if probe() {
				return nil
			}
		}
	}
}

// modelsPayload is the slice of the OpenAI model-list response readiness looks
// at. Only the presence of a served model matters.
type modelsPayload struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// probeModels performs one readiness probe.
//
// The answer must be more than "the socket is open": a 200 with an empty model
// list is a server that is still initialising, so it is treated as not ready
// and polling continues. A transport error is the normal state during the
// weight load (nothing is listening yet), so it is reported as an error the
// caller keeps as diagnostic detail rather than as a failure.
func probeModels(ctx context.Context, client *http.Client, url string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return false, fmt.Errorf("building the readiness probe: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsBodyLen))
	if err != nil {
		return false, fmt.Errorf("reading the readiness probe response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("GET %s answered %s", url, resp.Status)
	}
	var payload modelsPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		// A 200 that is not the model list is not readiness either.
		return false, fmt.Errorf("GET %s answered 200 but not a model list: %w", url, err)
	}
	if len(payload.Data) == 0 {
		return false, nil
	}
	return true, nil
}

// exitReason renders an exit error for a message. A clean exit is still a
// failure when it was not asked for, so nil gets its own wording instead of
// disappearing into the message.
func exitReason(err error) string {
	if err == nil {
		return "exited with status 0"
	}
	return err.Error()
}

// probeDetail renders the last readiness-probe outcome for a timeout message, so
// "it did not become ready" says whether nothing was listening or the model list
// stayed empty.
func probeDetail(err error) string {
	if err == nil {
		return "the model list stayed empty"
	}
	return err.Error()
}

// ── state plumbing ──

// stateLocked normalises the zero value. s.mu must be held.
func (s *Server) stateLocked() State {
	if s.state == "" {
		return StateNotInstalled
	}
	return s.state
}

// transitionLocked records a state change and returns the event to emit, or nil
// when nothing observable changed. De-duplication matters: a failed load and the
// supervisor both observe the same death, and the UI needs one event, not two.
// s.mu must be held, and the returned event must be emitted after releasing it.
func (s *Server) transitionLocked(next State, message string) *StateEvent {
	if s.stateLocked() == next && s.message == message {
		return nil
	}
	s.state = next
	s.message = message
	s.since = s.now()
	if next != StateLoaded {
		// The idle timer only ever runs against a loaded model.
		s.idle.disarm()
	}
	return &StateEvent{State: next, Port: s.port, Message: message}
}

// transition records a state change and emits its event.
func (s *Server) transition(next State, message string) {
	s.mu.Lock()
	event := s.transitionLocked(next, message)
	s.mu.Unlock()
	s.emit(event)
}

// emit delivers one transition to the callback seam, outside the state lock: an
// OnState implementation is allowed to read the server's state.
func (s *Server) emit(event *StateEvent) {
	if event == nil || s.OnState == nil {
		return
	}
	s.OnState(*event)
}

func (s *Server) readManifest() (Manifest, error) {
	path, err := s.Layout.ManifestPath()
	if err != nil {
		return Manifest{}, fmt.Errorf("embeddedllm: %w", err)
	}
	manifest, err := ReadManifest(path)
	if err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (s *Server) logger() *slog.Logger {
	if s != nil && s.Logger != nil {
		return s.Logger
	}
	return slog.New(slog.DiscardHandler)
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) hostOS() string {
	if s.HostOS != "" {
		return s.HostOS
	}
	return runtime.GOOS
}

func (s *Server) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	// No overall Client.Timeout: each probe carries its own ProbeTimeout
	// context, and the client is shared across a load that may last minutes.
	return &http.Client{}
}

func (s *Server) readyTimeout() time.Duration {
	if s.ReadyTimeout > 0 {
		return s.ReadyTimeout
	}
	return DefaultReadyTimeout
}

func (s *Server) pollInterval() time.Duration {
	if s.ReadyPollInterval > 0 {
		return s.ReadyPollInterval
	}
	return DefaultReadyPollInterval
}

func (s *Server) probeTimeout() time.Duration {
	if s.ProbeTimeout > 0 {
		return s.ProbeTimeout
	}
	return DefaultProbeTimeout
}

func (s *Server) stopTimeout() time.Duration {
	if s.StopTimeout > 0 {
		return s.StopTimeout
	}
	return DefaultStopTimeout
}

func (s *Server) killWait() time.Duration {
	if s.killWaitFor > 0 {
		return s.killWaitFor
	}
	return defaultKillWait
}

// drainWait is how long supervise gives the output pumps to finish after the
// child's exit has been observed. See serverWaitDelay.
func (s *Server) drainWait() time.Duration {
	if s.drainWaitFor > 0 {
		return s.drainWaitFor
	}
	return serverWaitDelay
}
