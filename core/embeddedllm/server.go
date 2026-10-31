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

// Supervisor of the one long-lived embedded-LLM process: readiness, the
// single-instance idempotent load (GET /v1/models, never /health), forced
// teardown and launch derivation. Contract: specs/domains/embedded-llm.md
// (Load / serve / unload); the process outlives the Load that started it.

// LoopbackHost is the only address the embedded server is ever bound to; a
// non-loopback bind is an unauthenticated LAN endpoint (ADR-066 D12).
const LoopbackHost = "127.0.0.1"

// Supervision budgets. The weight load dominates: the ready budget is generous
// and the poll interval is coarse — probing more often does not load faster.
const (
	// DefaultReadyTimeout bounds one Load, spawn to first /v1/models answer.
	// Exceeding it is a failure, not a longer wait: the load is wedged.
	DefaultReadyTimeout = 15 * time.Minute
	// DefaultReadyPollInterval is the gap between readiness probes.
	DefaultReadyPollInterval = 500 * time.Millisecond
	// DefaultProbeTimeout bounds a SINGLE readiness probe, so one hung
	// connection cannot consume the whole ready budget.
	DefaultProbeTimeout = 10 * time.Second
	// DefaultStopTimeout is the graceful window between the termination signal
	// and the kill.
	DefaultStopTimeout = 10 * time.Second
	// DefaultLoadProbeTimeout bounds the OPTIONAL device probe a Load runs —
	// an order of magnitude below DefaultReadyTimeout: a refinement, never a
	// precondition, charged to every cold start. Outer cap; also covers a
	// caller-substituted probe (the probe's own spawn is bounded at 2 s).
	DefaultLoadProbeTimeout = 10 * time.Second
	// maxPropsBodyLen caps the /props readback — same posture as the readiness
	// probe (maxModelsBodyLen): trusted local server, bounded socket read.
	maxPropsBodyLen = 1 << 20
	// defaultKillWait bounds the wait after a kill — SIGKILL is unconditional,
	// but a process stuck in an uninterruptible syscall must not wedge shutdown.
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

// fitFailureMarker is the fit contract's failure signature: the pinned fork's
// `--fit` abort complaint. A pin bump that changes fit's contract turns a
// planned launch into an abort whose only trace is this line — scanned out
// into Status.FitWarning (specs/domains/embedded-llm.md, server.go).
const fitFailureMarker = "failed to fit params to free device memory"

// Fixed launch policy (ADR-066): the fork's demo-script sampling values,
// deliberately not configurable — a knob would silently move the model off
// the configuration its packing was validated with. Reasoning effort is NOT
// set (ADR-066 D7); see specs/decisions/066-embedded-llm-runtime.md.
const (
	samplingTemp   = "1.0"
	samplingTopP   = "0.95"
	samplingTopK   = "20"
	flashAttention = "on"
)

// State is the supervision state machine; the transition diagram is canonical
// in specs/domains/embedded-llm.md (Load / serve / unload).
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
	// FitWarning is the fit-contract finding of the last failed launch
	// (scanFitFailure). It outlives the failed run — a plan/pin mismatch is
	// REPORTED, not retried away — and is cleared by the next launch that
	// becomes ready.
	FitWarning string
}

// Supervisor refusals. All are sentinels so callers can branch with errors.Is
// and surface an actionable message.
var (
	// ErrServerDied reports that the llama-server process exited; during a
	// Load the wrapped detail carries the exit status and the log tail, where
	// the real cause (missing CUDA library, OOM kill, Gatekeeper block) lives.
	ErrServerDied = errors.New("the embedded LLM server process exited")
	// ErrLoadTimeout reports that the weights did not finish loading inside
	// ReadyTimeout.
	ErrLoadTimeout = errors.New("the embedded LLM server did not become ready")
	// ErrLaunchSpecInvalid reports a launch specification that must not be
	// spawned (no binary/model, unusable port, context outside the RAM tiers).
	ErrLaunchSpecInvalid = errors.New("invalid embedded-LLM launch specification")
	// ErrServerBusy reports an operation refused because a server process is
	// live and its state is not the caller's to overwrite.
	ErrServerBusy = errors.New("the embedded LLM server is running")
)

// errStoppedDuringLoad is Load's diagnosis for a readiness wait that succeeded
// against a process a force stop had already taken
// (specs/domains/embedded-llm.md, Unload flow). Unexported — nothing outside
// the package branches on it — yet named: the backend surfaces it verbatim.
var errStoppedDuringLoad = errors.New(
	"the embedded LLM was stopped while it was loading the model, so it is not resident; ask again to reload it")

// layerKind is LayerMode's private discriminator: nothing outside the package
// can build a fifth answer, and arg() stays the single kind→argv mapping.
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

// LayerMode is the `-ngl` half of a LaunchSpec: four answers, one being the
// absence of one (LayerAuto omits the flag). LayerAuto is deliberately not
// the fork's `-ngl auto` spelling: `--fit` adjusts only UNSET arguments and
// the fork's fit.cpp aborts when an argument it would choose is already set,
// so a fit-sized launch carries no `-ngl` element at all — plan.go's
// exclusivity rule (specs/domains/embedded-llm.md, Memory plan). The zero
// value is LayerAuto, so a nil MemoryPlan.Layers maps with no sentinel; the
// fields are private, so nothing free-form reaches argv (SECURITY.md ASI05).
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

// LayerCount renders `-ngl n`. Negative is refused by Validate, not clamped —
// clamping would hide a caller's mistake; an over-large n is safe (the
// runtime treats any count ≥ the offloadable total as "all").
func LayerCount(n int) LayerMode { return LayerMode{kind: layerKindExact, layers: n} }

// EmitsFlag reports whether Args renders `-ngl` at all — the LayerMode spelling
// of MemoryPlan.EmitsLayers, making the exclusivity rule checkable as stated.
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

// String is diagnostics-only spelling, never an argv element (Args renders
// through arg()): "auto (flag omitted)" stays distinguishable from `-ngl auto`.
func (m LayerMode) String() string {
	value, ok := m.arg()
	if !ok {
		return "auto (flag omitted)"
	}
	return value
}

// cacheRAMNoLimit is the pinned fork's own `--cache-ram` spelling of "no
// ceiling" (its flag doc: "default: 8192, -1 - no limit, 0 - disable"); the
// only negative value Validate accepts.
const cacheRAMNoLimit = -1

// LaunchSpec is everything one llama-server invocation is derived from (the
// install record + resolve.go policy, or a MemoryPlan via ApplyMemoryPlan).
// Args is the ONLY place a llama-server argv exists and every element comes
// from the typed fields — no free-form string can become argv (SECURITY.md
// ASI05); widening past TestLaunchSpecArgsRenderTypedValuesOnly is reviewed.
// specs/domains/embedded-llm.md (server.go).
type LaunchSpec struct {
	// Engine selects which argv/validate/readback family this spec renders.
	// The ZERO value is the llama family; EngineMTPLX switches every engine-
	// shaped half to the mtplx variants (server_mlx.go).
	Engine Engine
	// ServerBinary is the absolute path of llama-server inside the installed
	// runtime tree (or, under EngineMTPLX, the provisioned interpreter).
	ServerBinary string
	// ModelFile is the GGUF passed to -m.
	ModelFile string
	// MMProjFile is the vision projector for --mmproj; empty omits the flag
	// (text-only serving), acceptable only when the projector is absent.
	MMProjFile string
	// Host is the bind address. Always LoopbackHost.
	Host string
	// Port is the persisted loopback port.
	Port int
	// Layers is -ngl. LayerAuto omits the flag, which is the only shape a
	// fit-sized launch may carry; see LayerMode.
	Layers LayerMode
	// ContextSize is -c. Zero means "the runtime's fit pass sizes it" (an
	// explicit zero, valid only under Fit); with Fit off the planner's figure
	// is always positive. Never above the model's training context — that
	// fallback is memory-unaware and OOMs once -ngl offloads the KV cache.
	ContextSize int
	// ImageMaxTokens is --image-max-tokens; ImageMaxTokensUncapped omits the
	// flag (CUDA/ROCm run uncapped). Validate refuses negative values and any
	// above the model's training context — a ceiling the derived values sit
	// far below (imageMaxTokensFor yields 0 or the 1,024-token vision cap).
	ImageMaxTokens int

	// ── the memory-plan surface ──
	//
	// A typed projection of a MemoryPlan (plan.go); omission uses that
	// struct's own vocabulary — no sentinel to invent.

	// Fit is `-fit`, the one memory flag Args renders UNCONDITIONALLY: `--fit`
	// is an undocumented fork contract, so a pin bump could flip or drop its
	// 'on' default and silently turn explicit-offload launches into fit.cpp
	// aborts. Always rendered: the shape is this struct's, not the binary's.
	Fit bool
	// FitTargetMiB is `-fitt`, the per-device margin fit leaves free; zero
	// keeps the runtime's 1024 MiB default. Rendered only under Fit.
	FitTargetMiB int
	// FitMinContext is `-fitc`, the smallest context fit may settle on,
	// positive under Fit: the fork's 4096 default reproduces the pinned
	// model's most-reported failure (DefaultFitMinContext). A fit-off plan
	// still records its floor here; Args omits it — a record, not a decision.
	FitMinContext int
	// KVType is `-ctk`/`-ctv`, one precision for both — a mixed pair silently
	// drops to CPU until the target context fits (memory.go). Empty keeps the
	// runtime's f16 default; anything else must be a member of the closed
	// three-precision set, so an unmodelled precision is a refusal.
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
	// Parallel is `-np`, ALWAYS rendered: the fork's `-1` (auto) default
	// inflates fit's own compute reserve per stream (measured 24450 → 77297 MiB
	// at 4 slots), and `-np` SPLITS `-c` across slots (`-c 8192 -np 4` yields
	// `n_ctx_slot 2048`). See DefaultParallel. Validate requires at least 1.
	Parallel int
	// CacheRAMMiB is `--cache-ram` (long spelling: greppable in a process
	// listing). nil keeps the runtime default; non-nil passes verbatim —
	// cacheRAMNoLimit and 0 (disable) are legitimate choices.
	CacheRAMMiB *int
	// CtxCheckpoints is `--ctx-checkpoints`, ALWAYS rendered: the fork also
	// reads LLAMA_ARG_CTX_CHECKPOINTS, so omission lets inherited env or pin
	// drift decide a memory-bearing setting no c0wrk layer chose. Bounds
	// 0..MaxTuningCtxCheckpoints; 0 verbatim disables the snapshots.
	CtxCheckpoints int
	// CacheIdleSlots is `--cache-idle-slots`/`--no-cache-idle-slots`, always
	// rendered against env drift like CtxCheckpoints. True (the runtime's
	// default) saves idle slots to the prompt cache; false passes --no-.
	CacheIdleSlots bool
	// Devices is `-dev`, the comma-separated offload list; empty omits the
	// flag. Any entry pins the offload (incompatible with Fit); names are
	// validated before they can reach argv — see validateDeviceName.
	Devices []string
	// SplitMode is `-sm`. SplitModeAuto (the zero value) omits the flag; any
	// other member of the runtime's closed set pins the split and is
	// incompatible with Fit.
	SplitMode SplitMode
}

// Validate refuses a specification that must never reach exec: it would fail
// obscurely, abort inside the fork's fit pass, or start a server whose offload
// and context nobody decided. Every argv-bound numeric field is bounded both
// ends — limits.go ceilings (also enforced in backend/config, so a hand-edited
// config.yaml cannot walk around them) and maxTrainingContext.
// specs/domains/embedded-llm.md (Load / serve / unload, Validate).
func (spec LaunchSpec) Validate() error {
	if spec.Engine == EngineMTPLX {
		return spec.mlxValidate()
	}
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
	if spec.CtxCheckpoints < 0 || spec.CtxCheckpoints > MaxTuningCtxCheckpoints {
		return fmt.Errorf("%w: --ctx-checkpoints %d is outside 0..%d (0 disables the per-slot snapshots; the runtime's own default is %d)",
			ErrLaunchSpecInvalid, spec.CtxCheckpoints, MaxTuningCtxCheckpoints, DefaultCtxCheckpoints)
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

// validateFitExclusivity is plan.go's exclusivity rule at the last gate before
// exec, both directions: nothing pinned beside `-fit on` (the fork aborts),
// and no omitted -ngl beside `-fit off` (the runtime's model-file-decided
// `auto` default is the memory-unaware launch this subsystem prevents).
// specs/domains/embedded-llm.md (Memory plan).
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

// validateContext is the `-c` rule: ceiling maxTrainingContext (nothing above
// it is representable), zero means "fit decides" and is accepted only when fit
// is on, negative is refused. specs/domains/embedded-llm.md (Load / serve / unload).
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

// splitModeIsKnown reports membership in the closed set the pinned fork
// accepts (`-sm {none,layer,row,tensor}`, plus SplitModeAuto = omit); an
// unknown spelling is refused so a typo cannot become argv.
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

// validateDeviceName is the typed-value guarantee for the one argv element
// Args builds by joining strings: the list must stay ONE element the runtime
// parses as a device list and nothing else. Not an injection defense (no
// shell) — the threat is a smuggled second device or a flag-shaped name.
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

// fitArg renders the literal `-fit` value, mirroring MemoryPlan.FitArg so the
// exclusivity rule reads identically on both sides of the bridge; two-branch,
// fixed literals only.
func (spec LaunchSpec) fitArg() string {
	if spec.Fit {
		return "on"
	}
	return "off"
}

// Args renders the command line — the only place a llama-server argv exists,
// every value a literal or a typed-field rendering; the Web UI is always off
// and the bind always loopback. Flag order is fixed and groups by concern so a
// diff reads as a decision. specs/domains/embedded-llm.md (server.go).
func (spec LaunchSpec) Args() []string {
	args := make([]string, 0, 34)
	args = append(args,
		"-m", spec.ModelFile,
		"--host", spec.Host,
		"--port", strconv.Itoa(spec.Port),
		// Always explicit — see LaunchSpec.Fit.
		"-fit", spec.fitArg(),
	)
	// The two fit knobs render only under fit: with `-fit off` the runtime
	// never reads them, and passing them would advertise a decision nobody made.
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
	// The two checkpoint knobs are ALWAYS explicit — see their fields; the
	// spellings are the pinned fork's own, verified against its tagged arg.cpp.
	args = append(args, "--ctx-checkpoints", strconv.Itoa(spec.CtxCheckpoints))
	if spec.CacheIdleSlots {
		args = append(args, "--cache-idle-slots")
	} else {
		args = append(args, "--no-cache-idle-slots")
	}
	args = append(args,
		"--temp", samplingTemp,
		"--top-p", samplingTopP,
		"--top-k", samplingTopK,
		"--jinja",
		// D12: the Web UI ships its own MCP client and agentic loop; disabled
		// unconditionally — this process is an inference endpoint, nothing
		// else. `--no-ui` is the canonical spelling (the fork's `--ui`-first
		// alias vocabulary); the legacy `--no-webui` alias makes this a
		// spelling migration, and the compile-time pin needs no fallback.
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
// taken from plan — the one planner→argv bridge, total so a plan cannot be
// half-applied, copying so identity (install record) and shape (plan) cannot
// overwrite each other. It does not Validate; that is the caller's gate.
func (spec LaunchSpec) ApplyMemoryPlan(plan MemoryPlan) LaunchSpec {
	spec.Fit = plan.Fit
	spec.FitTargetMiB = plan.FitTargetMiB
	spec.FitMinContext = plan.FitMinContext
	if plan.Layers == nil {
		spec.Layers = LayerAuto()
	} else {
		// Passed through exactly as the planner decided — not re-read as "all"
		// or "cpu" when it happens to equal those counts: the launcher renders
		// a decision, it does not make one.
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
	spec.CtxCheckpoints = plan.CtxCheckpoints
	spec.CacheIdleSlots = plan.CacheIdleSlots
	spec.Devices = slices.Clone(plan.Devices)
	spec.SplitMode = plan.SplitMode
	return spec
}

// ModelsURL is the readiness endpoint: the OpenAI-compatible model list.
func (spec LaunchSpec) ModelsURL() string {
	return baseURL(spec.Port) + "/models"
}

// usablePort reports whether a port can be bound, mirroring backend/config's
// bounds on the persisted value; the last gate before exec.
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

// Process is the supervisor's handle on one spawned llama-server — the seam
// every supervision test drives; the real implementation wraps *exec.Cmd.
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

// Server supervises one llama-server process for one installation. The zero
// value is not usable — build one with NewServer; every external effect is an
// injectable field, so the whole state machine (weight load, crash, idle
// unload) is testable without a runtime. See specs/domains/embedded-llm.md
// (server.go).
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
	// before the spawn and may return a replacement (which it also persists).
	// PRODUCTION ALWAYS WIRES IT (backend.embeddedEnsurePort: SearchFreePort +
	// write-back so the generated base_url matches the bound socket); nil
	// trusts the port and deliberately performs no probing, so unit tests
	// never bind a real port. The re-check also keeps a leftover server from a
	// crashed previous run from answering the FIRST readiness probe.
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
	// launch (a GPU that appeared, disappeared or changed driver since the
	// install). PRODUCTION ALWAYS WIRES IT (backend.embeddedDeviceProbe);
	// nil → no load-time probe — opt-in so an unwired caller keeps the
	// original no-probe behaviour. When wired, FAIL-SOFT in every direction
	// (see effectivePlan): wiring it can never make a load fail.
	ProbeDevices func(ctx context.Context, binaryPath string, logger *slog.Logger) (MemoryTopology, bool)
	// Tuning resolves the operator's memory-plan overrides
	// (`embedded_llm.tuning`) at launch time — a FUNCTION, not a value: the
	// supervisor outlives settings saves. nil → the zero Tuning (all-Auto).
	// Load path only, must not block (production takes configMu.RLock).
	Tuning func() Tuning
	// PersistContext writes the effective context a READY server reported into
	// durable config — the tier-1 `llm.models` context_window override.
	// PRODUCTION ALWAYS WIRES IT (backend.persistEmbeddedContext); nil → only
	// the manifest updates. Called inside Load under the single-instance gate
	// (no call-backs into the Server); failures are logged and ignored — a
	// serving load is not failed over an administrative write.
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
	// fitWarning is the fit-contract finding of the last failed run
	// (scanFitFailure), or "" — a successful load clears it. Guarded by mu.
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
	// loadsInFlight counts Load calls inside the part of their sequence that
	// OWNS a terminal transition (just before StateLoading until return);
	// forceUnload reads it under mu to decide whose transition it is. An
	// already-resident Load transitions nothing and is NOT counted — counting
	// it would strand the state where forceUnload left it.
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

// Load starts the server and blocks until the model can actually answer. The
// fixed sequence and the force-stop interleavings are specified in
// specs/domains/embedded-llm.md (Load / serve / unload); weight-load time is
// never charged to the idle budget. ctx bounds the whole operation —
// cancelling aborts the load and stops the half-started process, so callers
// that must survive a long weight load pass a context not tied to one RPC.
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
		// Idempotent: already serving — a no-op that still counts as activity,
		// with no state transition; hence loadsInFlight counting starts AFTER
		// this return rather than at the gate.
		s.MarkActivity()
		return nil
	}

	// Every exit path from here writes a terminal transition, so forceUnload
	// may leave it to this call (see forceUnload). Registered after the gate
	// so LIFO unwinds the count before the gate frees — forceUnload (gate-held
	// only) never sees a stale count.
	s.mu.Lock()
	s.loadsInFlight++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.loadsInFlight--
		s.mu.Unlock()
	}()

	// A leftover process from an attempt whose stop timed out is discarded
	// before another spawn (single instance). takeRun first also tells the
	// supervisor this exit is nobody's news: no state transition is emitted.
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
		// The load failed, so the half-started process is discarded; abandoned
		// keeps the supervisor from reporting as a crash a death THIS call is
		// about to explain. A death landing before the flag is set is reported
		// twice, deliberately: clearing the flag after readiness would let a
		// death right after the last probe be swallowed while the server stays
		// "loaded".
		s.mu.Lock()
		run.abandoned = true
		// The fit-failure line must not die with the run: an abandoned run's
		// exit is unreported by the supervisor, so this branch is the one
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
	// Residency is re-validated against the PUBLISHED handle: a force stop or
	// crash may have detached the run while waitReady polled, and a dead
	// child is never reported as loaded (full interleaving analysis:
	// specs/domains/embedded-llm.md, Unload flow). The diagnosis travels in
	// the returned error, no idle budget is stamped, and a force stop whose
	// terminate did not take re-attaches — that residency is real.
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
		// supervise already reported this crash, so its StateError IS the
		// honest terminal state: this call adds no transition and only returns
		// the diagnosis. run.exitErr was written under this same lock before
		// the handle was detached — safe to read.
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
	// The idle budget starts HERE, after the weights are in memory — stamping
	// it at the spawn would charge a multi-minute weight load to the
	// operator's inactivity budget.
	s.recordActivityLocked()
	s.mu.Unlock()
	s.emit(event)

	// The server is resident and answering, so it can now be ASKED its real
	// context — the only path allowed to make that request, keeping GetConfig
	// network-free. Fail-soft: a readback that fails leaves the recorded value
	// and never fails a load that is already serving.
	effectiveContext := s.recordEffectiveContext(ctx, launch, manifest)

	s.logger().Info("embedded LLM ready",
		"port", spec.Port, "pid", run.pid, "backend", manifest.Backend,
		"context_size", effectiveContext, "launch_context", spec.ContextSize,
		"layers", spec.Layers, "fit", spec.Fit, "plan_refreshed", launch.Refreshed)
	return nil
}

// Unload stops the server, deterministically returning its RAM and VRAM to the
// system (ADR-066 D5); a no-op when nothing is running; graceful, then killed
// after StopTimeout, ending installed. The caller's ctx bounds ONLY the gate
// wait; an expiry behind an in-flight Load gets forceUnload, not an error.
// specs/domains/embedded-llm.md (Load / serve / unload).
func (s *Server) Unload(ctx context.Context) error {
	return s.unload(ctx, nil, true)
}

// Stop is Unload under the name the shutdown path and Installer.Stop use: the
// signatures match, so wiring is `installer.Stop = server.Stop`. Shutdown is
// the caller the force path exists for (specs/domains/embedded-llm.md).
func (s *Server) Stop(ctx context.Context) error {
	return s.unload(ctx, nil, true)
}

// unloadIfIdle is Unload for the idle path, with two deltas: it carries the
// activity generation the expiry decided on and abandons the unload if
// activity moved before the gate and mu were re-acquired; and it never takes
// the force path — an idle unload that cannot get the gate is a load in
// progress, and killing it trades a deferrable unload for a failed cold start.
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

	// The caller's budget bounds the GATE WAIT and nothing else: a nearly-
	// spent ctx must not cut terminate's graceful window to the remainder —
	// its Done case falls straight through to Kill. The stop gets its own
	// detached budget; the caller's values travel, its deadline does not.
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

	// The final transition is recorded here too so the caller can rely on the
	// state on return; transitionLocked de-duplicates. A stop that did not
	// take is an error and KEEPS the run handle: "installed" would misreport
	// resident RAM, and dropping the handle lets the next Load stack a second
	// server.
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

// forceUnload stops a live process WITHOUT the single-instance gate — the
// path for a caller whose ctx expired while an in-flight Load held the gate.
// It reuses Load's leftover-run machinery (takeRun detach, graceful-then-kill
// terminate); the detach is PROVISIONAL — a failed stop re-attaches — and a
// Load still in flight keeps the terminal transition. The caller's ctx is
// not the stop's budget: it already expired. Full interleavings:
// specs/domains/embedded-llm.md (Unload flow).
func (s *Server) forceUnload(ctx context.Context, gateErr error) error {
	run := s.takeRun()
	if run == nil {
		// Nothing to kill: the gate is held by a Load that has not spawned
		// yet (manifest read or port scan), so the timeout is the whole truth.
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
		// A stop that did not take KEEPS the run handle (re-attached), as the
		// gated unload does: detached, a live child would survive app exit
		// untracked and the next Load would stack a SECOND server. `expected`
		// stays set — this stop was asked for, so a later exit is no crash.
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
// touching any process (startup after restoring the manifest; Remove with
// false). Refused while a server is live: a running process's state belongs to
// the supervisor, and a silent rewrite could report a resident model absent.
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

// acquireGate takes the single-instance token, honouring ctx while waiting,
// and returns the release function (non-nil whenever the error is nil).
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

// resolvedLaunch is one Load's fully derived launch: the command line, the
// memory decision behind its shape, and the measurement that decision was made
// from — a load may REFINE the recorded decision, and a refinement not written
// back is one the next load has to rediscover.
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

// launchSpec turns the install record into a launch specification. IDENTITY
// half (binary, weights, projector, socket, --image-max-tokens): always from
// the manifest and layout, keyed on the recorded EFFECTIVE backend — bytes on
// disk. SHAPE half: Manifest.Plan, or the resolve.go policy for a pre-plan
// manifest, then OPTIONALLY refined by a bounded load-time device probe —
// FAIL-SOFT in every direction (a load must not fail because a driver query
// wedged). Full contract: specs/domains/embedded-llm.md (Load / serve / unload).
func (s *Server) launchSpec(ctx context.Context, manifest Manifest, port int) (resolvedLaunch, error) {
	if manifest.Engine == EngineMTPLX {
		return s.launchSpecMLX(manifest, port)
	}
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
		// The exclusivity rule's second branch: `-fit off` with an explicit
		// -ngl. Rendering the switch rather than inheriting the fork's 'on'
		// default keeps that pinned offload from aborting inside fit.cpp —
		// see LaunchSpec.Fit.
		spec.Fit = false
		spec.Layers = LayerCount(layersFor(platform, manifest.Backend))
		// The manifest's tier is the context. A zero one is only meaningful
		// under fit, and this path never runs fit, so Validate refuses it —
		// how TestLaunchSpecRefusesACorruptContext catches a lost tier.
		spec.ContextSize = manifest.ContextSize
		// One slot: one agent loop over one loopback socket, one request at a
		// time per server; the fork's auto default would split the context
		// across slots — see DefaultParallel.
		spec.Parallel = DefaultParallel
		// This policy path has no plan to take the checkpoint knobs from: the
		// defaults ARE the policy, pinned so an inherited LLAMA_ARG_* env
		// value cannot decide them (see DefaultCtxCheckpoints).
		spec.CtxCheckpoints = DefaultCtxCheckpoints
		spec.CacheIdleSlots = true
	}

	if err := spec.Validate(); err != nil {
		return resolvedLaunch{}, fmt.Errorf("embeddedllm: %w", err)
	}
	return resolvedLaunch{Spec: spec, Plan: plan, Topology: topology, Refreshed: refreshed}, nil
}

// launchIdentity resolves the half of a launch that describes what is ON DISK
// (binary, weights, optional projector, socket, image-token cap); no memory
// decision, so neither half can overwrite the other (ApplyMemoryPlan). Both
// path halves are containment-checked against the layout — a tampered record
// cannot aim the launch at outside bytes. specs/domains/embedded-llm.md.
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
		// Trusted only inside the model root (Layout.OwnsModel); a violation
		// falls back to the layout-derived path — the honest recovery, logged
		// because a record contradicting the layout is operator-visible.
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

// effectivePlan decides the shape a load launches: the recorded plan, refined
// by a fresh measurement when cheaply available, else unchanged. NEVER fails,
// never blocks beyond LoadProbeTimeout — every refinement failure keeps the
// stored decision (a measurement refines; the accept/reject gate ran at
// install, behind a user action). specs/domains/embedded-llm.md (Load / serve / unload).
func (s *Server) effectivePlan(ctx context.Context, manifest Manifest, binary string) (*MemoryPlan, *MemoryTopology) {
	stored := manifest.Plan
	snapshot := manifest.Topology

	if s.ProbeDevices == nil {
		// Not wired: the pre-existing contract, and the manifest is the whole
		// story.
		return stored, snapshot
	}
	if stored == nil {
		// Nothing to refine: a fresh measurement with no recorded plan would
		// have to invent a coarser shape from the pure policy, so the launch
		// stays legacy and no topology is recorded beside a plan ignoring it.
		return nil, nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, s.loadProbeTimeout())
	defer cancel()
	fresh, ok := s.ProbeDevices(probeCtx, binary, s.logger())
	if !ok {
		// THE fail-soft case the load-time probe exists to survive: a wedged
		// or absent driver query falls back to the recorded snapshot.
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

// replan re-derives the launch shape from a fresh measurement, holding the
// plan to the artifacts on disk. The packing is PINNED to the manifest's —
// the one input a re-plan must not re-decide: a different packing prices
// bytes that are not there (a packing change is an install decision).
// specs/domains/embedded-llm.md (Load / serve / unload).
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
		// An unrecognized inventory is not evidence the accelerator changed
		// generation; the install's classification beats "unknown".
		family = manifest.GPUFamily
	}
	return Plan(topology, profile, tuning, manifest.Backend, family)
}

// tuning resolves the operator's memory-plan overrides; nil → the zero Tuning
// (the all-Auto plan), so an unwired supervisor still gets a coherent decision.
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

// planChanged and topologyChanged compare a recorded decision against the one
// a load is about to apply, so the manifest is rewritten only when the load
// learned something; nil-vs-present counts as a change (first plan: record it).
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

// propsURL is the server's property endpoint — NOT under /v1: the fork
// exposes /props at the root, beside /health.
func propsURL(port int) string {
	return "http://" + LoopbackHost + ":" + strconv.Itoa(port) + "/props"
}

// propsPayload is the /props slice the readback needs: n_ctx is PER-SLOT (the
// fork splits `-c` across `-np` slots), total_slots the split count — the
// product is the honest effective total even if `-np` stops being 1.
// specs/domains/embedded-llm.md (Context readback).
type propsPayload struct {
	DefaultGenerationSettings struct {
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
	TotalSlots int `json:"total_slots"`
}

// readPropsContext asks a READY server its context. Fail-soft — (0, false)
// for every failure mode; absent total_slots means one slot. The ceiling is
// the point of the fail-softness: the only external-HTTP context figure, and
// it lands in two durable stores an absurd value would poison beyond repair.
// specs/domains/embedded-llm.md (Context readback).
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

// mulContext multiplies two context figures, reporting false on overflow; both
// operands come from an untrusted local socket, so the multiply is total rather
// than relying on a wrap a later `<= 0` check may miss.
func mulContext(perSlot, slots int) (int, bool) {
	if perSlot > 0 && slots > math.MaxInt/perSlot {
		return 0, false
	}
	return perSlot * slots, true
}

// recordEffectiveContext reads the served context back and makes it durable:
// the manifest's ContextSize and the tier-1 llm.models override (which
// shadows the lazy probe — this is its only correction), on the load path so
// GetConfig stays network-free. MERGES onto the current on-disk record, only
// the fields this load learned, abandoned on a different install
// (sameInstall); fail-soft. specs/domains/embedded-llm.md (Context readback).
func (s *Server) recordEffectiveContext(ctx context.Context, launch resolvedLaunch, manifest Manifest) int {
	recorded := manifest.ContextSize
	effective := recorded
	readCtx := func() (int, bool) {
		if launch.Spec.Engine == EngineMTPLX {
			return readHealthContext(ctx, s.httpClient(), healthURL(launch.Spec.Port), s.probeTimeout())
		}
		return readPropsContext(ctx, s.httpClient(), propsURL(launch.Spec.Port), s.probeTimeout())
	}
	if n, ok := readCtx(); ok {
		effective = n
		// The served window can never legitimately exceed what this launch
		// ordered; anything larger is a poisoned readback (squatted port,
		// stale fit-sized server). Clamping keeps both durable stores
		// RAM-tiered, so compaction scales from a window actually held.
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
		// No record to merge onto: removed (or corrupted) mid-load — writing
		// one back would resurrect a record the operator just deleted.
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

// sameInstall reports whether two records describe the SAME installed bytes.
// What a load refines (ContextSize, Plan, Topology) and the port are NOT
// identity; the rest is the set whose change makes a plan and a readback
// meaningless — different bytes priced, a replaced tree, a different GGUF.
func sameInstall(recorded, loaded Manifest) bool {
	return recorded.Engine == loaded.Engine &&
		recorded.Packing == loaded.Packing &&
		recorded.Backend == loaded.Backend &&
		recorded.RuntimeVersion == loaded.RuntimeVersion &&
		recorded.InstalledAt == loaded.InstalledAt &&
		recorded.ModelFile == loaded.ModelFile
}

// launchEnv builds the child environment: the parent's, with the runtime's
// binary directory prepended to the platform's dynamic-library path (ADR-066
// D3 — libraries ship beside the executable, nothing system-wide); without
// this a Linux CUDA/ROCm build fails at dlopen like a broken install.
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

// pathListSeparator is the target platform's list separator, derived from goos
// rather than the host so the policy stays table-testable from any CI machine.
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
	// The engine selects the renderer: Args stays the llama argv's only home,
	// mlxArgs the mtplx argv's (server_mlx.go). Engine itself never renders —
	// it is identity, like ServerBinary.
	args := spec.Args()
	if spec.Engine == EngineMTPLX {
		args = spec.mlxArgs()
	}
	env := launchEnv(binaryDir, s.hostOS(), os.Environ())
	if spec.Engine == EngineMTPLX {
		// The mtplx children run on a scrubbed, self-contained environment
		// (mlxChildEnv, server_mlx.go): pinned bin dir on PATH, no inherited
		// PIP_*/PYTHON*/MTPLX_* influence, engine state inside the c0wrk
		// layout — an operator's MTPLX install can never mix with this one.
		env = mlxChildEnv(os.Environ(), s.hostOS(), binaryDir)
	}
	cmd := LaunchCommand{
		Binary: spec.ServerBinary,
		Args:   args,
		Env:    env,
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
// supervision goroutine waiting after the process is gone: a grandchild that
// inherited the stdout write end keeps the pipe open, so EOF never arrives.
// It bounds two agreeing things — os/exec's copy into this package's relay
// (expiry closes the descriptor; Wait returns exec.ErrWaitDelay) and the
// pump drain in supervise. Seconds, not milliseconds: an early abandon would
// truncate the diagnostics a failed launch reports (crashMessage,
// scanFitFailure).
const serverWaitDelay = 5 * time.Second

// spawnOSServer starts the real llama-server. The context is detached
// (context.WithoutCancel): the server outlives the RPC or UI action that
// started the load, owned by Unload/Stop, the idle timer and shutdown. The
// io.Pipe relay (rather than StdoutPipe) is what gives WaitDelay something to
// bound — a caller-owned *os.File starts no copy goroutine, closes nothing,
// and a grandchild holding the write end wedges the pumps for the app's
// lifetime. cmd.Wait still drains to EOF, so the tail keeps every line (see
// serverWaitDelay).
func spawnOSServer(ctx context.Context, cmd LaunchCommand, logger *slog.Logger) (Process, error) {
	// The binary and every argument come from the pinned install record and the
	// pure launch policy — never from a model, a user string or the network.
	proc := exec.CommandContext(context.WithoutCancel(ctx), cmd.Binary, cmd.Args...)
	proc.Dir = cmd.Dir
	proc.Env = cmd.Env
	// Suppress the console window a GUI-subsystem host would allocate for the
	// child (CREATE_NO_WINDOW on Windows, a no-op elsewhere): this child runs
	// for hours, and a Windows user would otherwise get a terminal window they
	// cannot dismiss without killing the model.
	sysproc.HideConsole(proc)
	// Bound the output as well as the process: see serverWaitDelay.
	proc.WaitDelay = serverWaitDelay

	stdoutRead, stdoutWrite := io.Pipe()
	stderrRead, stderrWrite := io.Pipe()
	proc.Stdout = stdoutWrite
	proc.Stderr = stderrWrite

	// The child's stdin is a pipe THIS process holds open — never nil:
	// os/exec's null-device default lets a stdin-supervised build (a shape
	// llama-server has shipped) self-exit seconds after a clean launch.
	// Holding the write end also gives such a build a graceful stop trigger
	// (closed on Wait/Kill).
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		closeRelays([]*io.PipeWriter{stdoutWrite, stderrWrite})
		_ = stdoutRead.Close()
		_ = stderrRead.Close()
		return nil, err
	}
	proc.Stdin = stdinRead

	if err := proc.Start(); err != nil {
		_ = stdinWrite.Close()
		_ = stdinRead.Close()
		closeRelays([]*io.PipeWriter{stdoutWrite, stderrWrite})
		_ = stdoutRead.Close()
		_ = stderrRead.Close()
		return nil, err
	}
	// The parent's duplicate read end must go, or the child's reads never see
	// EOF once the write end closes.
	_ = stdinRead.Close()
	logger.Debug("embedded LLM server started", "pid", proc.Process.Pid, "binary", cmd.Binary)
	return &osProcess{
		cmd:        proc,
		stdout:     stdoutRead,
		stderr:     stderrRead,
		stdinWrite: stdinWrite,
		relay:      []*io.PipeWriter{stdoutWrite, stderrWrite},
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

	// stdinWrite is the parent's end of the child's stdin (see spawnOSServer),
	// closed exactly once — after exit is observed, or on kill — so the
	// descriptor does not leak and a stdin-supervised child gets its
	// EOF-based stop before the SIGKILL lands.
	stdinOnce  sync.Once
	stdinWrite *os.File
}

// Wait blocks until the child has exited AND os/exec has finished (or, at
// serverWaitDelay, abandoned) copying its output, then ends the relays.
func (p *osProcess) Wait() error {
	err := p.cmd.Wait()
	p.relayOnce.Do(func() { closeRelays(p.relay) })
	p.closeStdin()
	return err
}

// closeStdin releases the child's stdin write end exactly once — Wait and Kill
// can race (a discard alongside a supervised exit); the field is written only
// by the constructor, before the process is published.
func (p *osProcess) closeStdin() {
	if p.stdinWrite == nil {
		return
	}
	p.stdinOnce.Do(func() { _ = p.stdinWrite.Close() })
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
	p.closeStdin()
	return p.cmd.Process.Kill()
}

func (p *osProcess) Stdout() io.Reader { return p.stdout }

func (p *osProcess) Stderr() io.Reader { return p.stderr }

// gracefulSignal is what Unload sends before resorting to a kill; Windows has
// no graceful child-process signal, so Signal fails there and the caller falls
// back to Kill.
func gracefulSignal() os.Signal { return syscall.SIGTERM }

// ── output ──

// pumpOutput starts one drain goroutine per output stream, into slog and into
// the run's tail. wg reports when both have finished, which is what supervise
// waits on (bounded — see waitExit).
func (s *Server) pumpOutput(run *processRun, wg *sync.WaitGroup) {
	s.pump(run, wg, run.proc.Stdout(), "stdout")
	s.pump(run, wg, run.proc.Stderr(), "stderr")
}

// pump drains one output stream into slog and into the run's tail until it
// ends. An over-long line is SKIPPED, not fatal: a scanner stopping at
// bufio.ErrTooLong would leave the child with nothing draining its pipe — its
// writes would block and it would wedge mid-generation while the supervisor
// still reported "loaded". ReadSlice reports it as a full buffer instead.
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
				// A too-long line is dropped WHOLE: keeping the first chunk
				// would turn the tail's bound (tailLines × maxLogLineBytes)
				// into a megabyte-scale buffer for one line, and a partial
				// line is no more useful than the marker.
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

// lineTail keeps the last n lines of server output — a crash or failed load is
// only actionable with the server's own complaint: "the process exited" is not
// a diagnosis, "ggml_cuda_init: found 0 devices" is.
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

// supervise owns one process lifetime: observe the child's exit, let the
// output pumps finish within drainWait, then report it; the only goroutine
// that closes run.died. The exit comes FIRST, the drain bounded and
// abandonable (waitExit owns that ordering) — abandon a pump, never hang.
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
// bounded. The exit is observed FIRST, inverting the naive "drain, then Wait":
// cmd.Wait bounds and ends the output relay (serverWaitDelay), so waiting for
// EOF first would let a grandchild holding the stdout wedge this goroutine and
// its pumps for the app's lifetime, and every later Load would pay a killWait
// delay against the zombie run. It costs no diagnostics — cmd.Wait returns
// only at EOF, so the tail keeps every line. The bounded wait covers a
// Process not relayed through os/exec (a test double); abandoning is safe —
// lineTail is mutex-guarded, read only for diagnostics. exec.ErrWaitDelay is
// not a crash: it replaces a nil exit error when a successful exit's output
// was abandoned; passing it through would forge a spurious StateError.
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
// fork's fit-failure complaint and renders the Status.FitWarning sentence. The
// marker matches as a substring (timestamp/level prefixes cannot hide it); the
// SUCCESS spelling deliberately does not match.
func scanFitFailure(tail string) string {
	if msg := scanMLXFitFailure(tail); msg != "" {
		return msg
	}
	if strings.Contains(tail, fitFailureMarker) {
		return "the runtime's --fit pass aborted: " + strconv.Quote(fitFailureMarker) +
			" — the recorded memory plan and this pin's fit contract disagree;" +
			" re-plan or re-review the pin before reloading"
	}
	return ""
}

// takeRun detaches and returns the live process handle, if any; the supervisor
// then treats its exit as unremarkable — the caller owns the state from here.
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

// terminate stops the process gracefully first, killing after the graceful
// window, returning once the exit is observed. ctx is a BACKSTOP, not the
// window — its Done case falls straight through to the kill, so every caller
// derives a whole-stop budget (WithoutCancel + StopTimeout + kill wait).
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

// waitReady polls /v1/models until the model can serve, giving up on the ready
// budget, on ctx, and — immediately — on the death of the process, so a
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
		ready, err := probeModelsExt(probeCtx, client, modelsURL, spec.Engine == EngineMTPLX)
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
		ID            string `json:"id"`
		ContextLength *int   `json:"context_length"`
		MaxContextLen *int   `json:"max_context_length"`
		MaxModelLen   *int   `json:"max_model_len"`
	} `json:"data"`
}

// modelsReady applies the readiness predicate to one parsed model list. The
// llama contract is "a served model is listed" (llama never sends window
// fields). mtplx is STRICTER: its listing names the model before the backend
// resolved its context, and an earlier request would queue behind the load,
// so a window spelling MUST be present — any of the three, else keep polling.
func modelsReady(payload modelsPayload, requireWindow bool) bool {
	for _, m := range payload.Data {
		if m.ID == "" {
			continue
		}
		if !requireWindow {
			return true
		}
		for _, w := range []*int{m.ContextLength, m.MaxContextLen, m.MaxModelLen} {
			if w != nil && *w > 0 {
				return true
			}
		}
	}
	return false
}

// probeModels performs one readiness probe — more than "the socket is open": a
// 200 with an empty model list is a server still initialising (not ready), and
// a transport error is the normal state during the weight load, kept as
// diagnostic detail rather than a failure.
func probeModels(ctx context.Context, client *http.Client, url string) (bool, error) {
	return probeModelsExt(ctx, client, url, false)
}

// probeModelsExt is probeModels with the engine's window requirement.
func probeModelsExt(ctx context.Context, client *http.Client, url string, requireWindow bool) (bool, error) {
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
	return modelsReady(payload, requireWindow), nil
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

// transitionLocked records a state change and returns the event to emit, or
// nil when nothing observable changed — a failed load and the supervisor both
// observe the same death, and the UI needs one event, not two. s.mu must be
// held; the returned event is emitted after releasing it.
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
