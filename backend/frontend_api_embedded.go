package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// The RPC surface of the embedded local model (see
// specs/domains/embedded-llm.md and specs/contracts/desktop-frontend.md).
//
// Ownership: this file owns the FrontendAPI-side wiring of
// core/embeddedllm — the supervisor, the installer, the config sink and the
// two global events. It owns NO policy: the command line, the resolution, the
// state machine and the idle budget all live in core.
//
// The conventions the surface follows:
//
//   - GetEmbeddedLLMStatus is a read-only getter and therefore returns no
//     error (desktop-frontend.md "RPC Surface"): an unavailable subsystem
//     reports the not-installed state instead of failing.
//   - every mutating method returns an error, and an error it returns is
//     actionable — it names the refused operation and the reason.
//   - the multi-gigabyte install runs in the BACKGROUND: InstallEmbeddedLLM
//     performs only the synchronous gates (single-run, hardware probe, the
//     combined memory refusal) and then returns, so the RPC never blocks on a
//     download. Progress arrives through embedded_llm:install_progress and a
//     background failure through the runtime_error toast.
//   - startup performs no network I/O and never loads the model
//     (initEmbeddedLLM); it only restores the state from manifest.json.

// Budgets of the synchronous parts of this surface. The install itself is
// unbounded (it is a multi-gigabyte resumable download on a background
// goroutine); these bound only what an RPC waits for.
const (
	// embeddedProbeTimeout bounds the hardware probe the install RPC runs
	// before it commits to a background download. Every external probe command
	// is already bounded inside core (2s each), so this is the outer belt: a
	// machine with every probe helper installed and wedged must still return
	// an actionable error instead of hanging the settings dialog.
	embeddedProbeTimeout = 30 * time.Second
	// embeddedStopTimeout bounds the shutdown stop. The supervisor's own
	// graceful window is DefaultStopTimeout (10s) followed by a kill and a
	// bounded post-kill wait, so this only covers a wedged terminate path —
	// quitting must not hang on a model that refuses to die.
	embeddedStopTimeout = 30 * time.Second
)

// runtime_error codes of this subsystem (the payload's error_code field).
const (
	embeddedErrCodeInstall = "embedded_llm_install_failed"
	embeddedErrCodeRemove  = "embedded_llm_remove_failed"
)

// ---------------------------------------------------------------------------
// DTOs
// ---------------------------------------------------------------------------

// EmbeddedLLMStatus is the payload of GetEmbeddedLLMStatus: the supervision
// state plus the install record the Settings page and the status bar render.
// Every field is always present (no omitempty) so the frontend never has to
// distinguish "absent" from "zero".
//
// Fields are ADDITIVE at this boundary and stay that way: the hand-written
// frontend guard (isEmbeddedLLMStatus in frontend/src/api/embedded.ts) checks
// the presence and type of the fields IT knows, so a payload from a newer
// backend still validates and a renderer that has not caught up simply ignores
// what it does not read. The two composite additions follow the same rule from
// the other side — Devices and Plan.Notes are always arrays and Plan is a value
// carrying its own Recorded flag — so there is no null to distinguish from an
// empty one anywhere in the payload.
type EmbeddedLLMStatus struct {
	// State is the raw supervision state: not_installed | installed | loading
	// | loaded | unloading | error.
	State string `json:"state"`
	// Installed reports that the runtime and the weights are on disk and
	// verified (manifest.json restored). It does NOT imply resident.
	Installed bool `json:"installed"`
	// Installing reports a background install run in flight.
	Installing bool `json:"installing"`
	// Loading reports a weight load in progress (the process is up, /v1/models
	// has not answered yet).
	Loading bool `json:"loading"`
	// Loaded reports a serving model: /v1/models answered with a non-empty
	// model list.
	Loaded bool `json:"loaded"`
	// Packing is the ternary quantization on disk ("PQ2_0" | "PTQ1_0").
	Packing string `json:"packing"`
	// Backend is the accelerator the runtime was provisioned for.
	Backend string `json:"backend"`
	// Port is the persisted loopback port (0 when nothing is installed).
	Port int `json:"port"`
	// ContextSize is the LAST KNOWN EFFECTIVE context of the installation (0
	// when nothing is installed): the planner's figure at install time, corrected
	// by every successful load with the value the server itself reported through
	// /props. It is the same figure the tier-1
	// llm.models."Bonsai 2 27B".context_window override carries.
	ContextSize int `json:"context_size"`
	// AutoUnloadEnabled is the resolved idle-timer master switch.
	AutoUnloadEnabled bool `json:"auto_unload_enabled"`
	// AutoUnloadMinutes is the resolved idle budget in minutes.
	AutoUnloadMinutes int `json:"auto_unload_minutes"`
	// IdleRemainingSeconds is the idle budget left before the process is
	// stopped, 0 when no timer is armed (the model is not loaded or the
	// auto-unload is off).
	IdleRemainingSeconds int64 `json:"idle_remaining_seconds"`
	// BaseURL is the OpenAI-compatible endpoint derived from the port, empty
	// when nothing is installed.
	BaseURL string `json:"base_url"`
	// ModelID is the composite provider/model id the router exposes
	// ("embedded/Bonsai 2 27B"), empty when nothing is installed.
	ModelID string `json:"model_id"`
	// ModelName is the bare model name of the generated provider record.
	ModelName string `json:"model_name"`
	// RuntimeVersion is the pinned fork release the runtime came from.
	RuntimeVersion string `json:"runtime_version"`
	// InstalledAt is the RFC 3339 install timestamp.
	InstalledAt string `json:"installed_at"`
	// ModelFile is the absolute path of the GGUF weights.
	ModelFile string `json:"model_file"`
	// PackingReason says why the installed packing is what it is: "default", or
	// the typed cause of a downgrade ("no_pq2_0_kernels",
	// "avx512_pq2_0_segfault", "pq2_0_does_not_fit",
	// "gpu_generation_decode"). Empty on an install recorded before the field
	// existed, which readers must treat as unknown rather than as "default".
	PackingReason string `json:"packing_reason"`
	// GPUFamily is the accelerator generation the install was planned for,
	// empty when no device probe answered.
	GPUFamily string `json:"gpu_family"`
	// Guards are the backend compatibility decisions this install was planned
	// under, each with its typed reason, its severity, its upstream issue
	// citation and an Applied flag saying whether the plan actually changed
	// because of it. This is how a DEGRADED install becomes visible instead of
	// silent: a machine whose runtime is documented to hang, abort or garble
	// output says so here, whether or not c0wrk could act on it. Empty on a
	// machine no documented failure covers, which is the healthy common case.
	//
	// Always an array, never null (an unguarded install carries an empty one),
	// so a renderer has one code path. The hand-written mirror in
	// frontend/src/api/embedded.ts does not carry these three fields yet: the
	// Settings surface that renders the degradation record is a separate change,
	// and until it lands the fields are simply unused on the wire.
	Guards []EmbeddedLLMGuard `json:"guards"`
	// Devices is the accelerator inventory of the RECORDED topology — the
	// snapshot the recorded plan was made from, as the provisioned runtime
	// reported it at provision time. Always an array, never null: an empty one
	// means either "no accelerator this build can use" or "no probe ever
	// answered", and TopologyProbedAt says which (empty = never). Call
	// ProbeEmbeddedLLMDevices for a measurement of THIS instant.
	Devices []EmbeddedLLMDevice `json:"devices"`
	// Unified reports whether the device pool and host RAM are the SAME memory
	// on the recorded topology. It is the one fact a reader must trust over any
	// OS intuition: when it is true, DeviceBudgetMiB and HostBudgetMiB are two
	// views of one pool and must never be added together. false on a topology no
	// probe answered, where it means "unknown" rather than "discrete".
	Unified bool `json:"unified"`
	// HostRAMGiB is the total system RAM of the recorded topology, 0 when no
	// probe ever answered.
	HostRAMGiB float64 `json:"host_ram_gib"`
	// DeviceBudgetMiB and HostBudgetMiB are the two budgets the recorded plan
	// was gated against, 0 when no probe answered (where 0 means UNREADABLE, not
	// "no memory" — the distinction the combined memory gate exists to keep).
	DeviceBudgetMiB int64 `json:"device_budget_mib"`
	HostBudgetMiB   int64 `json:"host_budget_mib"`
	// TopologyProbedAt is the RFC 3339 UTC stamp of the recorded topology. EMPTY
	// MEANS NO PROBE EVER ANSWERED, which is the only way to tell an empty
	// Devices array that means "CPU-only machine" from one that means "unknown".
	TopologyProbedAt string `json:"topology_probed_at"`
	// Plan is the launch shape LAST APPLIED to this installation — every
	// flag-bearing value the supervisor renders, the two footprints it expects
	// and the Notes saying why each non-default decision was made. Read
	// Plan.Recorded first: a manifest written before the field existed carries
	// no plan, and every other Plan field is then the zero value rather than a
	// decision. This is the OUTCOME of the planner over the operator's tuning;
	// the tuning itself is GetEmbeddedLLMTuning.
	Plan EmbeddedLLMPlan `json:"plan"`
	// ReloadRequired reports that the model is RESIDENT and was launched with
	// memory-plan overrides the operator has since changed — i.e. the persisted
	// tuning takes effect on the NEXT load, not on the running process. Every
	// tuning knob is a launch flag (`-c`, `-ngl`, `-ctk`, `-fit`, …), so only a
	// fresh process can pick one up; see SetEmbeddedLLMTuning for why this
	// surface reports instead of restarting. Always false while nothing is
	// resident.
	ReloadRequired bool `json:"reload_required"`
	// Pid is the OS process id of the supervised server, 0 when no process is
	// running.
	Pid int `json:"pid"`
	// FitWarning is the fit-contract finding of the last failed launch: the
	// fork's "failed to fit params to free device memory" complaint, scanned
	// from the dead run's bounded output tail by the supervisor and carried
	// here so the install record can show it. A launch that becomes ready
	// clears it. Empty when no launch has failed that way — the healthy
	// common case.
	FitWarning string `json:"fit_warning,omitempty"`
	// Error is a human-readable cause: the supervisor's message while State is
	// "error", otherwise the last failed install or removal. Empty when nothing
	// failed since the last successful operation.
	Error string `json:"error"`
	// Available reports whether the subsystem could be constructed at all
	// (false only when the agent directory is unset, i.e. before startup).
	Available bool `json:"available"`
}

// EmbeddedLLMGuard is the frontend-facing shape of one
// core/embeddedllm.GuardDecision: a backend compatibility decision derived from
// the pinned model's KNOWN_ISSUES, recorded at install time and replayed here so
// a degraded install is visible in Settings rather than silent.
//
// The string fields are the core enum values verbatim (snake_case), so the UI can
// switch on them without this layer inventing a second vocabulary. The
// frontend mirror is pending: see EmbeddedLLMStatus.Guards.
type EmbeddedLLMGuard struct {
	// Guard is the stable id of the guard that fired, e.g. "cuda-13.3-crash".
	Guard string `json:"guard"`
	// Action is what the guard asked for: prefer_backend | prefer_packing |
	// advisory.
	Action string `json:"action"`
	// Reason is the typed cause: crash_on_load | process_abort |
	// garbled_output | hang | fails_to_start.
	Reason string `json:"reason"`
	// Severity ranks the guarded failure: critical | warning.
	Severity string `json:"severity"`
	// Issue is the upstream citation, "<repo>#<number>".
	Issue string `json:"issue"`
	// Applied reports whether the install actually changed because of this
	// decision. false is not "nothing happened": it is "we know about this and
	// could not, or chose not to, act on it" — and Guidance says which.
	Applied bool `json:"applied"`
	// Backend is the substituted backend, empty unless Action is prefer_backend.
	Backend string `json:"backend,omitempty"`
	// Packing is the substituted packing, empty unless Action is prefer_packing.
	Packing string `json:"packing,omitempty"`
	// Guidance is the user-facing sentence: what is documented, what c0wrk did
	// or could not do, and what the upstream workaround is.
	Guidance string `json:"guidance"`
}

// embeddedGuardIDs renders the recorded compatibility decisions for one log
// line, marking the ones the install could not act on. A degradation that only
// ever reached a log is still better than one that reached nothing, and this is
// the operator-facing half of the same record the status DTO carries.
func embeddedGuardIDs(decisions []embeddedllm.GuardDecision) string {
	if len(decisions) == 0 {
		return "none"
	}
	ids := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		if decision.Applied {
			ids = append(ids, string(decision.Guard))
			continue
		}
		ids = append(ids, string(decision.Guard)+"(unapplied)")
	}
	return strings.Join(ids, ",")
}

// embeddedGuardsDTO maps the persisted guard decisions onto the status DTO. It
// returns an empty slice rather than nil so the boundary always carries an array
// and the frontend never has to distinguish "no guards" from "no field".
func embeddedGuardsDTO(decisions []embeddedllm.GuardDecision) []EmbeddedLLMGuard {
	guards := make([]EmbeddedLLMGuard, 0, len(decisions))
	for _, decision := range decisions {
		guards = append(guards, EmbeddedLLMGuard{
			Guard:    string(decision.Guard),
			Action:   string(decision.Action),
			Reason:   string(decision.Reason),
			Severity: string(decision.Severity),
			Issue:    decision.Issue,
			Applied:  decision.Applied,
			Backend:  string(decision.Backend),
			Packing:  string(decision.Packing),
			Guidance: decision.Guidance,
		})
	}
	return guards
}

// EmbeddedLLMStateData is the payload of the global embedded_llm:state event.
// It is deliberately narrower than EmbeddedLLMStatus: it carries exactly what a
// status indicator needs, so a transition event stays cheap. Mirrors
// EmbeddedLLMStateData in frontend/src/types/events.ts.
type EmbeddedLLMStateData struct {
	Installed         bool   `json:"installed"`
	Loading           bool   `json:"loading"`
	Loaded            bool   `json:"loaded"`
	Packing           string `json:"packing"`
	Backend           string `json:"backend"`
	Port              int    `json:"port"`
	ContextSize       int    `json:"context_size"`
	AutoUnloadMinutes int    `json:"auto_unload_minutes"`
	Error             string `json:"error"`
}

// EmbeddedLLMProgressData is the payload of the global
// embedded_llm:install_progress event: one component's one stage. It is the
// frontend-facing shape of core/embeddedllm.Progress (snake_case JSON keys,
// mirroring tool_manager:progress). Mirrors EmbeddedLLMInstallProgressData in
// frontend/src/types/events.ts.
type EmbeddedLLMProgressData struct {
	// Component is the artifact being worked on: runtime | cudart | model |
	// mmproj.
	Component string `json:"component"`
	// Stage is downloading | verifying | extracting | signing | done.
	Stage string `json:"stage"`
	// BytesDone and BytesTotal describe the component's own transfer. Both are
	// 0 for the non-transfer stages (verifying, extracting, signing, done),
	// where a byte count would be a lie.
	BytesDone  int64 `json:"bytes_done"`
	BytesTotal int64 `json:"bytes_total"`
}

// EmbeddedLLMDevice is one accelerator as the provisioned runtime reported it:
// the frontend-facing shape of core/embeddedllm.DeviceMemory. The name is the
// runtime's own device id ("MTL0", "CUDA0", "Vulkan0", "HIP0") and the
// description its human label ("Apple M4 Max", "NVIDIA GeForce RTX 4090").
//
// FreeMiB is a snapshot of the instant the probe ran and is informational: the
// two BUDGETS a fit decision spends are derived from TotalMiB, because a
// capacity plan that moved with whatever else happened to be running would not
// be reproducible.
type EmbeddedLLMDevice struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	TotalMiB    int64  `json:"total_mib"`
	FreeMiB     int64  `json:"free_mib"`
}

// EmbeddedLLMPlan is the effective launch shape: the frontend-facing view of
// core/embeddedllm.MemoryPlan, i.e. every flag-bearing value the supervisor
// renders into a llama-server argv, the two footprints that shape expects, the
// budgets it was gated against, and the human-readable reason for each
// non-default decision.
//
// It is a VALUE with a Recorded flag rather than a pointer, so the status DTO
// keeps its "every field is always present" contract: a renderer has one code
// path and never has to distinguish an absent plan from an empty one.
type EmbeddedLLMPlan struct {
	// Recorded reports that a plan exists at all. false means the installation
	// carries no recorded launch shape — a manifest written before the field
	// existed — and every other field below is then the zero value, NOT a
	// decision. A reader must treat false as unknown.
	Recorded bool `json:"recorded"`
	// Packing is the weights quantization the plan was projected for
	// ("PQ2_0" | "PTQ1_0").
	Packing string `json:"packing"`
	// KVType is the resolved `-ctk`/`-ctv` precision ("f16" | "q8_0" |
	// "q4_0"). Never empty on a recorded plan: `auto` is resolved to a
	// concrete precision and the escalation that got there is in Notes.
	KVType string `json:"kv_type"`
	// ContextSize is `-c`. ZERO ON A FIT-SIZED PLAN IS NOT A ZERO CONTEXT: it
	// means the runtime's own fit pass chooses the window at launch, held to
	// FitMinContext as its floor. Fit says which of the two readings applies.
	ContextSize int `json:"context_size"`
	// Fit reports whether the runtime's `--fit` pass sizes the layer count and
	// the context. When true, Layers is nil and ContextSize is 0 — the
	// exclusivity rule: `-fit on` and an explicit `-ngl` abort the launch.
	Fit bool `json:"fit"`
	// FitArg is the literal `-fit` value ("on" | "off"), echoed so a renderer
	// shows the flag as the runtime receives it instead of re-deriving it from
	// a boolean.
	FitArg string `json:"fit_arg"`
	// FitTargetMiB is `-fitt`, the per-device margin fit leaves free. 0 omits
	// the flag and keeps the runtime's own 1024 MiB default. Only meaningful
	// when Fit is true.
	FitTargetMiB int `json:"fit_target_mib"`
	// FitMinContext is `-fitc`, the floor fit is held to. Only meaningful when
	// Fit is true.
	FitMinContext int `json:"fit_min_context"`
	// OffloadMode renders the EFFECTIVE `-ngl` decision in the operator's own
	// vocabulary: "auto" (fit sizes it, or the flag is omitted), "cpu" (nothing
	// offloaded) or "layers" (an explicit count, in Layers). Derived from Fit +
	// Layers, so it always agrees with the number beside it. "all" is not a
	// separate label here — see embeddedPlanDTO for why an every-layer offload
	// renders as a count instead.
	OffloadMode string `json:"offload_mode"`
	// Layers is the resolved `-ngl` count. -1 means the flag is OMITTED (fit
	// chooses the count), which is a different fact from 0 ("nothing
	// offloaded") and is why the sentinel is negative rather than a second
	// boolean.
	Layers int `json:"layers"`
	// KVOffload false means `-nkvo`: the KV cache stays in system RAM.
	KVOffload bool `json:"kv_offload"`
	// MMProjOffload false means `--no-mmproj-offload`: the vision projector's
	// reserve stays in system RAM.
	MMProjOffload bool `json:"mmproj_offload"`
	// Parallel is `-np`, the slot count.
	Parallel int `json:"parallel"`
	// CacheRAMMiB is `-cram`, the prompt-cache ceiling. -1 means the flag is
	// omitted (the runtime's own default); 0 is a real value that DISABLES the
	// cache, so the two must stay distinguishable.
	CacheRAMMiB int `json:"cache_ram_mib"`
	// GPUFamily is the accelerator generation the plan was classified as, empty
	// when no device probe answered. It is what priced the split allowance in
	// the two budgets below.
	GPUFamily string `json:"gpu_family"`
	// DeviceBudgetMiB and HostBudgetMiB are the budgets the plan was gated
	// against. On a UNIFIED machine these are the same physical bytes viewed
	// from two sides and must never be added together — the clamp lives in the
	// device term precisely so the pair cannot describe one pool twice.
	DeviceBudgetMiB int64 `json:"device_budget_mib"`
	HostBudgetMiB   int64 `json:"host_budget_mib"`
	// ExpectedDeviceMiB and ExpectedHostMiB are the projected footprints of
	// this shape, INCLUDING the vision projector's reserve. When the offload is
	// a PARTIAL layer count neither figure is exact: device is the full-offload
	// upper bound and host the CPU-only upper bound, and Notes says so.
	ExpectedDeviceMiB int64 `json:"expected_device_mib"`
	ExpectedHostMiB   int64 `json:"expected_host_mib"`
	// Notes is the human-readable "why", one entry per non-default decision,
	// rendered verbatim. Always an array, never null.
	Notes []string `json:"notes"`
}

// EmbeddedLLMDevicesDTO is the payload of ProbeEmbeddedLLMDevices and the
// topology half of EmbeddedLLMStatus: the measured device-memory shape of this
// machine plus the two budgets a fit decision may spend.
type EmbeddedLLMDevicesDTO struct {
	// Devices is the accelerator inventory in the order the runtime printed it,
	// without the entries that report no memory of their own and without
	// duplicates. Always an array, never null: an EMPTY one is a real answer
	// from a machine with no accelerator this build can use, not a failed probe
	// (a failed probe is an error, never a DTO).
	Devices []EmbeddedLLMDevice `json:"devices"`
	// Unified reports whether the device pool and host RAM are the SAME memory.
	// It is DETECTED, never assumed from the OS: unified pools exist well
	// beyond macOS (AMD APUs, Intel iGPUs, Jetson/Grace-Hopper class SoCs) and
	// a discrete GPU on a Mac would be the mirror-image mistake. When the
	// evidence is inconclusive core falls back to true, because treating a
	// discrete card as unified can only shrink its budget while the reverse
	// overcounts capacity that does not exist.
	Unified bool `json:"unified"`
	// HostRAMGiB is total system RAM, from the same probe that reports the
	// devices, so the two can never disagree about the host.
	HostRAMGiB float64 `json:"host_ram_gib"`
	// DeviceBudgetMiB and HostBudgetMiB are the two spendable budgets after the
	// accelerator margin and the OS reserve. See EmbeddedLLMPlan for why they
	// must not be summed on a unified machine.
	DeviceBudgetMiB int64 `json:"device_budget_mib"`
	HostBudgetMiB   int64 `json:"host_budget_mib"`
	// ProbedAt stamps the snapshot in RFC 3339 UTC. A topology is a snapshot:
	// free memory and even the device list change when a driver or a build
	// changes, which is why an explicit re-probe exists.
	ProbedAt string `json:"probed_at"`
}

// EmbeddedLLMContextTuningDTO is the `tuning.context` knob: a mode plus, for
// `mode: exact` only, the token count. Mirrors config.EmbeddedLLMContextConfig.
//
// Both fields are NULLABLE and nil is load-bearing: an absent mode means "the
// operator never wrote this", which is NOT the same value as an explicit
// "auto" — the config section's whole reason for keeping pointers. A DTO that
// collapsed the two could not round-trip the section it mirrors.
type EmbeddedLLMContextTuningDTO struct {
	// Mode is nil (unset), "auto" or "exact".
	Mode *string `json:"mode"`
	// Tokens is the pinned `-c`, nil when the operator never wrote one.
	Tokens *int `json:"tokens"`
}

// EmbeddedLLMOffloadTuningDTO is the `tuning.offload` knob: a mode plus, for
// `mode: layers` only, the layer count. Mirrors config.EmbeddedLLMOffloadConfig.
type EmbeddedLLMOffloadTuningDTO struct {
	// Mode is nil (unset), "auto", "all", "cpu" or "layers".
	Mode *string `json:"mode"`
	// Layers is the explicit `-ngl` count, nil when the operator never wrote
	// one.
	Layers *int `json:"layers"`
}

// EmbeddedLLMTuningDTO is the payload of GetEmbeddedLLMTuning: the operator's
// persisted memory-plan overrides (embedded_llm.tuning), field for field.
//
// EVERY field is nullable and nil means "unset — the planner decides", which is
// a different value from an explicit "auto": the planner treats them the same,
// but the persisted section does not, and this DTO mirrors the section rather
// than the plan. Read the effective, resolved decision from
// EmbeddedLLMStatus.Plan instead; this is the OVERRIDE, not the outcome.
//
// The legal spellings are not enumerated here on purpose. They live in exactly
// one place (config.tuningChoice's closed sets, derived from core's vocabulary)
// and a rejected SetEmbeddedLLMTuning names the offending key and lists every
// legal spelling, so an editor learns them from the refusal instead of from a
// second copy that can drift.
type EmbeddedLLMTuningDTO struct {
	// Context is the `-c` knob: nil mode = unset, "auto" = the planner sizes it,
	// "exact" = Tokens pins it.
	Context EmbeddedLLMContextTuningDTO `json:"context"`
	// KVCacheType overrides BOTH `-ctk` and `-ctv` (one value for both: a mixed
	// pair silently drops to CPU flash attention). nil = unset, "auto" = the
	// adaptive escalation f16 -> q8_0 -> q4_0 until the target context fits.
	KVCacheType *string `json:"kv_cache_type"`
	// Offload is the `-ngl` knob: nil mode = unset, "auto" = fit sizes it,
	// "all"/"cpu"/"layers" pin it.
	Offload EmbeddedLLMOffloadTuningDTO `json:"offload"`
	// Fit overrides `-fit`. nil lets the exclusivity rule decide; an explicit
	// false forces `-fit off` and hands context sizing back to the planner; an
	// explicit true beside an explicit offload loses to that rule (and the
	// planner records the loss in the plan's Notes).
	Fit *bool `json:"fit"`
	// FitTargetMiB overrides `-fitt`, the per-device margin fit leaves free. nil
	// keeps the runtime's own 1024 MiB; an explicit 0 also omits the flag.
	FitTargetMiB *int `json:"fit_target_mib"`
	// FitMinContext overrides `-fitc`, the smallest context fit may settle on.
	// nil means c0wrk's own floor, deliberately NOT the runtime's 4096.
	FitMinContext *int `json:"fit_min_context"`
	// KVOffload is `-kvo`/`-nkvo`. nil and an explicit true keep the KV cache on
	// the device; false leaves it in system RAM.
	KVOffload *bool `json:"kv_offload"`
	// MMProjOffload is `--mmproj-offload`/`--no-mmproj-offload`. nil and an
	// explicit true keep the vision projector's reserve on the device.
	MMProjOffload *bool `json:"mmproj_offload"`
	// Packing overrides the weights quantization. nil or "auto" keeps the
	// packing the hardware probe resolved (reported separately as
	// EmbeddedLLMStatus.Packing); any other spelling must be one the registry
	// pins AND the memory model has measured residency for.
	Packing *string `json:"packing"`
	// Parallel overrides `-np`, the slot count. nil means 1: c0wrk serves one
	// agent loop over one loopback socket and issues one request at a time.
	Parallel *int `json:"parallel"`
	// CacheRAMMiB overrides `-cram`, the prompt-cache ceiling. nil omits the
	// flag; an explicit 0 DISABLES the cache and is passed through verbatim,
	// because disabling it is a legitimate choice.
	CacheRAMMiB *int `json:"cache_ram_mib"`
	// HostReserveGiB overrides the RAM kept out of the host budget. nil keeps
	// the topology's own derivation. It is a PLANNER-side budget knob, not a
	// runtime flag -- the pinned fork has no `--host-reserve`.
	HostReserveGiB *float64 `json:"host_reserve_gib"`
}

// EmbeddedLLMContextTuningRequest is the `context` knob of a tuning patch. Its
// presence in the request (a non-nil EmbeddedLLMTuningRequest.Context) is what
// says "replace this knob"; the fields inside then REPLACE it wholesale, so
// `{mode: "exact", tokens: null}` is a validation error rather than a silent
// half-update.
type EmbeddedLLMContextTuningRequest struct {
	Mode   *string `json:"mode"`
	Tokens *int    `json:"tokens"`
}

// EmbeddedLLMOffloadTuningRequest is the `offload` knob of a tuning patch, with
// the same whole-knob replacement semantics as the context one.
type EmbeddedLLMOffloadTuningRequest struct {
	Mode   *string `json:"mode"`
	Layers *int    `json:"layers"`
}

// EmbeddedLLMTuningRequest is the payload of SetEmbeddedLLMTuning: a PARTIAL
// update of embedded_llm.tuning, mirroring ModelProfileUpdateRequest's "nil
// keeps the stored value" contract.
//
// Three states per knob, because the section this writes has three:
//
//   - field NIL                    -> keep the stored value, untouched
//   - field PRESENT                -> store it verbatim (an explicit "auto"
//     spelling included — it is a real value, not a synonym for unset)
//   - knob named in Reset          -> clear it back to unset, so the planner
//     decides again
//
// Reset is what makes the third state expressible at all. Every knob here is a
// pointer whose nil already means "absent from this request", so nil cannot
// ALSO mean "clear the override" — and for the bool/int knobs there is no
// "auto" spelling to send instead. Naming the keys is the one encoding that
// covers all twelve knobs uniformly, and it uses the config-file vocabulary the
// operator already reads in config.example.yaml and in every validation error.
//
// Resetting and setting the same knob in one request is a contradiction and is
// refused before anything is written.
type EmbeddedLLMTuningRequest struct {
	// Reset names the knobs to clear back to unset. Keys are the
	// embedded_llm.tuning YAML keys (see embeddedTuningKnobs); an unknown key
	// is refused without a write.
	Reset []string `json:"reset"`

	Context        *EmbeddedLLMContextTuningRequest `json:"context"`
	KVCacheType    *string                          `json:"kv_cache_type"`
	Offload        *EmbeddedLLMOffloadTuningRequest `json:"offload"`
	Fit            *bool                            `json:"fit"`
	FitTargetMiB   *int                             `json:"fit_target_mib"`
	FitMinContext  *int                             `json:"fit_min_context"`
	KVOffload      *bool                            `json:"kv_offload"`
	MMProjOffload  *bool                            `json:"mmproj_offload"`
	Packing        *string                          `json:"packing"`
	Parallel       *int                             `json:"parallel"`
	CacheRAMMiB    *int                             `json:"cache_ram_mib"`
	HostReserveGiB *float64                         `json:"host_reserve_gib"`
}

// The `reset` vocabulary: the embedded_llm.tuning YAML key of every knob, so a
// request names an override the way config.example.yaml and every validation
// error already name it. `context` and `offload` are included even though they
// are composite — one uniform clear mechanism for all twelve knobs beats two.
const (
	embeddedTuningKnobContext        = "context"
	embeddedTuningKnobKVCacheType    = "kv_cache_type"
	embeddedTuningKnobOffload        = "offload"
	embeddedTuningKnobFit            = "fit"
	embeddedTuningKnobFitTargetMiB   = "fit_target_mib"
	embeddedTuningKnobFitMinContext  = "fit_min_context"
	embeddedTuningKnobKVOffload      = "kv_offload"
	embeddedTuningKnobMMProjOffload  = "mmproj_offload"
	embeddedTuningKnobPacking        = "packing"
	embeddedTuningKnobParallel       = "parallel"
	embeddedTuningKnobCacheRAMMiB    = "cache_ram_mib"
	embeddedTuningKnobHostReserveGiB = "host_reserve_gib"
)

// embeddedTuningKnobs is every legal `reset` key, in the order
// config.TuningConfig declares them. It is the authority behind both the
// unknown-key refusal and that refusal's message, so the list a caller is told
// about is the list that is actually checked.
var embeddedTuningKnobs = []string{
	embeddedTuningKnobContext,
	embeddedTuningKnobKVCacheType,
	embeddedTuningKnobOffload,
	embeddedTuningKnobFit,
	embeddedTuningKnobFitTargetMiB,
	embeddedTuningKnobFitMinContext,
	embeddedTuningKnobKVOffload,
	embeddedTuningKnobMMProjOffload,
	embeddedTuningKnobPacking,
	embeddedTuningKnobParallel,
	embeddedTuningKnobCacheRAMMiB,
	embeddedTuningKnobHostReserveGiB,
}

// embeddedDevicesDTO maps a measured topology onto the wire shape. It returns
// an empty slice rather than nil so the boundary always carries an array.
func embeddedDevicesDTO(topology embeddedllm.MemoryTopology) EmbeddedLLMDevicesDTO {
	devices := make([]EmbeddedLLMDevice, 0, len(topology.Devices))
	for _, device := range topology.Devices {
		devices = append(devices, EmbeddedLLMDevice{
			Name:        device.Name,
			Description: device.Description,
			TotalMiB:    device.TotalMiB,
			FreeMiB:     device.FreeMiB,
		})
	}
	return EmbeddedLLMDevicesDTO{
		Devices:         devices,
		Unified:         topology.Unified,
		HostRAMGiB:      topology.HostRAMGiB,
		DeviceBudgetMiB: topology.DeviceBudgetMiB(),
		HostBudgetMiB:   topology.HostBudgetMiB(),
		ProbedAt:        topology.ProbedAt,
	}
}

// embeddedPlanDTO renders a recorded launch shape. A nil plan (a manifest
// written before the field existed) yields Recorded=false with every other
// field zero, which is the DTO's "unknown" — never a plan of zeros that a
// renderer would read as a decision to run nothing offloaded with no context.
func embeddedPlanDTO(plan *embeddedllm.MemoryPlan) EmbeddedLLMPlan {
	dto := EmbeddedLLMPlan{
		Layers:      -1,
		CacheRAMMiB: -1,
		OffloadMode: config.EmbeddedLLMTuningAuto,
		Notes:       []string{},
	}
	if plan == nil {
		return dto
	}

	dto.Recorded = true
	dto.Packing = string(plan.Packing)
	dto.KVType = string(plan.KVType)
	dto.ContextSize = plan.ContextSize
	dto.Fit = plan.Fit
	dto.FitArg = plan.FitArg()
	dto.FitTargetMiB = plan.FitTargetMiB
	dto.FitMinContext = plan.FitMinContext
	dto.KVOffload = plan.KVOffload
	dto.MMProjOffload = plan.MMProjOffload
	dto.Parallel = plan.Parallel
	dto.GPUFamily = string(plan.GPUFamily)
	dto.DeviceBudgetMiB = plan.DeviceBudgetMiB
	dto.HostBudgetMiB = plan.HostBudgetMiB
	dto.ExpectedDeviceMiB = plan.ExpectedDeviceMiB
	dto.ExpectedHostMiB = plan.ExpectedHostMiB
	if plan.CacheRAMMiB != nil {
		dto.CacheRAMMiB = *plan.CacheRAMMiB
	}
	if plan.Layers != nil {
		dto.Layers = *plan.Layers
	}
	if notes := plan.Notes; len(notes) > 0 {
		dto.Notes = slices.Clone(notes)
	}

	// The offload mode is DERIVED, not copied: MemoryPlan carries the resolved
	// `-ngl` (nil = omit the flag), and the operator's vocabulary is the one the
	// tuning knob uses. Deriving it here keeps the badge beside the number from
	// ever disagreeing with it.
	//
	// `all` deliberately has no label of its own at this boundary. Core spells it
	// as its own `-ngl` ceiling, a sentinel this layer must not transcribe — so
	// an offload of every layer renders as `layers` with the count beside it,
	// which is exactly the flag the runtime receives. Nothing is lost: the
	// operator's OWN choice is read from GetEmbeddedLLMTuning, where `all` is
	// spelled `all`. This DTO describes the OUTCOME, and the outcome is a count.
	switch {
	case plan.Fit || plan.Layers == nil:
		dto.OffloadMode = config.EmbeddedLLMTuningAuto
	case *plan.Layers == 0:
		dto.OffloadMode = config.EmbeddedLLMOffloadCPU
	default:
		dto.OffloadMode = config.EmbeddedLLMOffloadLayers
	}
	return dto
}

// embeddedTuningDTO mirrors the persisted override surface field for field. The
// pointers are copied rather than shared, so the returned DTO never aliases the
// live config — a caller may hold it across a config reload without watching
// its contents change underneath.
func embeddedTuningDTO(tuning config.TuningConfig) EmbeddedLLMTuningDTO {
	dto := EmbeddedLLMTuningDTO{
		Context: EmbeddedLLMContextTuningDTO{
			Mode:   copyStringPtr(tuning.Context.Mode),
			Tokens: copyIntPtr(tuning.Context.Tokens),
		},
		KVCacheType: copyStringPtr(tuning.KVCacheType),
		Offload: EmbeddedLLMOffloadTuningDTO{
			Mode:   copyStringPtr(tuning.Offload.Mode),
			Layers: copyIntPtr(tuning.Offload.Layers),
		},
		Fit:            copyBoolPtr(tuning.Fit),
		FitTargetMiB:   copyIntPtr(tuning.FitTargetMiB),
		FitMinContext:  copyIntPtr(tuning.FitMinContext),
		KVOffload:      copyBoolPtr(tuning.KVOffload),
		MMProjOffload:  copyBoolPtr(tuning.MMProjOffload),
		Packing:        copyStringPtr(tuning.Packing),
		Parallel:       copyIntPtr(tuning.Parallel),
		CacheRAMMiB:    copyIntPtr(tuning.CacheRAMMiB),
		HostReserveGiB: copyFloat64Ptr(tuning.HostReserveGiB),
	}
	return dto
}

// copyStringPtr, copyIntPtr, copyBoolPtr and copyFloat64Ptr clone one nullable
// knob. A shared pointer would let the DTO and the live config alias the same
// cell, so a later write to one would silently appear in the other.
func copyStringPtr(v *string) *string {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func copyIntPtr(v *int) *int {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func copyBoolPtr(v *bool) *bool {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func copyFloat64Ptr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

// applyEmbeddedTuningRequest folds a partial patch onto the stored section and
// returns the result WITHOUT writing it. Validation is the caller's next step,
// so an invalid patch is rejected before a lock is taken on the write path and
// before any byte of config.yaml changes.
//
// The fold order is Reset-then-set, but a knob named in both is refused rather
// than resolved by precedence: "clear this" and "set this to that" in one
// request is a caller bug, and silently picking a winner would hide it.
func applyEmbeddedTuningRequest(stored config.TuningConfig, req EmbeddedLLMTuningRequest) (config.TuningConfig, error) {
	reset := make(map[string]bool, len(req.Reset))
	for _, key := range req.Reset {
		trimmed := strings.TrimSpace(key)
		if !slices.Contains(embeddedTuningKnobs, trimmed) {
			return config.TuningConfig{}, fmt.Errorf(
				"unknown embedded_llm.tuning knob %q in reset; the legal keys are %s",
				key, strings.Join(embeddedTuningKnobs, ", "))
		}
		if reset[trimmed] {
			continue
		}
		reset[trimmed] = true
	}

	// The knobs this request also SETS. Checked against the reset list before
	// anything is folded, so the refusal names every contradiction at once.
	set := map[string]bool{
		embeddedTuningKnobContext:        req.Context != nil,
		embeddedTuningKnobKVCacheType:    req.KVCacheType != nil,
		embeddedTuningKnobOffload:        req.Offload != nil,
		embeddedTuningKnobFit:            req.Fit != nil,
		embeddedTuningKnobFitTargetMiB:   req.FitTargetMiB != nil,
		embeddedTuningKnobFitMinContext:  req.FitMinContext != nil,
		embeddedTuningKnobKVOffload:      req.KVOffload != nil,
		embeddedTuningKnobMMProjOffload:  req.MMProjOffload != nil,
		embeddedTuningKnobPacking:        req.Packing != nil,
		embeddedTuningKnobParallel:       req.Parallel != nil,
		embeddedTuningKnobCacheRAMMiB:    req.CacheRAMMiB != nil,
		embeddedTuningKnobHostReserveGiB: req.HostReserveGiB != nil,
	}
	contradictions := make([]string, 0, len(reset))
	for key := range reset {
		if set[key] {
			contradictions = append(contradictions, key)
		}
	}
	if len(contradictions) > 0 {
		slices.Sort(contradictions)
		return config.TuningConfig{}, fmt.Errorf(
			"the request both resets and sets embedded_llm.tuning.%s; send one or the other",
			strings.Join(contradictions, ", embedded_llm.tuning."))
	}

	next := stored
	for key := range reset {
		switch key {
		case embeddedTuningKnobContext:
			next.Context = config.EmbeddedLLMContextConfig{}
		case embeddedTuningKnobKVCacheType:
			next.KVCacheType = nil
		case embeddedTuningKnobOffload:
			next.Offload = config.EmbeddedLLMOffloadConfig{}
		case embeddedTuningKnobFit:
			next.Fit = nil
		case embeddedTuningKnobFitTargetMiB:
			next.FitTargetMiB = nil
		case embeddedTuningKnobFitMinContext:
			next.FitMinContext = nil
		case embeddedTuningKnobKVOffload:
			next.KVOffload = nil
		case embeddedTuningKnobMMProjOffload:
			next.MMProjOffload = nil
		case embeddedTuningKnobPacking:
			next.Packing = nil
		case embeddedTuningKnobParallel:
			next.Parallel = nil
		case embeddedTuningKnobCacheRAMMiB:
			next.CacheRAMMiB = nil
		case embeddedTuningKnobHostReserveGiB:
			next.HostReserveGiB = nil
		}
	}

	if req.Context != nil {
		next.Context = config.EmbeddedLLMContextConfig{
			Mode:   copyStringPtr(req.Context.Mode),
			Tokens: copyIntPtr(req.Context.Tokens),
		}
	}
	if req.KVCacheType != nil {
		next.KVCacheType = copyStringPtr(req.KVCacheType)
	}
	if req.Offload != nil {
		next.Offload = config.EmbeddedLLMOffloadConfig{
			Mode:   copyStringPtr(req.Offload.Mode),
			Layers: copyIntPtr(req.Offload.Layers),
		}
	}
	if req.Fit != nil {
		next.Fit = copyBoolPtr(req.Fit)
	}
	if req.FitTargetMiB != nil {
		next.FitTargetMiB = copyIntPtr(req.FitTargetMiB)
	}
	if req.FitMinContext != nil {
		next.FitMinContext = copyIntPtr(req.FitMinContext)
	}
	if req.KVOffload != nil {
		next.KVOffload = copyBoolPtr(req.KVOffload)
	}
	if req.MMProjOffload != nil {
		next.MMProjOffload = copyBoolPtr(req.MMProjOffload)
	}
	if req.Packing != nil {
		next.Packing = copyStringPtr(req.Packing)
	}
	if req.Parallel != nil {
		next.Parallel = copyIntPtr(req.Parallel)
	}
	if req.CacheRAMMiB != nil {
		next.CacheRAMMiB = copyIntPtr(req.CacheRAMMiB)
	}
	if req.HostReserveGiB != nil {
		next.HostReserveGiB = copyFloat64Ptr(req.HostReserveGiB)
	}
	return next, nil
}

// embeddedTuningFingerprint renders a tuning section as a comparable string. It
// fingerprints the TRANSLATED planner vocabulary rather than the config section,
// so two spellings that resolve to the same launch shape — an absent
// `kv_cache_type` and an explicit `auto`, an untrimmed spelling and a canonical
// one — are the same fingerprint and do not raise a spurious "reload required".
//
// It is an in-memory comparison key only: never persisted, never sent over the
// wire, and never parsed back. A translation failure yields the fingerprint of
// the all-Auto plan, matching embeddedTuning's own fail-soft, so a section the
// planner cannot read is not reported as a change from itself.
func (f *FrontendAPI) embeddedTuningFingerprint() string {
	tuning := f.embeddedTuning()
	encoded, err := json.Marshal(tuning)
	if err != nil {
		// Unreachable for a struct of scalars, pointers and string slices. A
		// stable fallback keeps the comparison total rather than panicking on
		// the status path.
		f.log().Debug("the embedded LLM tuning fingerprint could not be rendered", "error", err)
		return "unrenderable"
	}
	return string(encoded)
}

// noteEmbeddedLaunchTuning records which overrides the process behind a
// supervision transition was launched with. Called from onEmbeddedLLMState, on
// the goroutine that made the transition.
//
// `loading` is the transition to key off, and the ordering inside Server.Load is
// what makes it correct: launchSpec — which reads the tuning through the
// Server.Tuning seam — runs BEFORE the loading transition, so the fingerprint
// taken here is the one the argv being spawned was built from. Every
// non-resident state clears the record, because after a stop or a removal there
// is no process to be out of date.
func (f *FrontendAPI) noteEmbeddedLaunchTuning(state embeddedllm.State) {
	st := &f.embedded
	if state != embeddedllm.StateLoading && state != embeddedllm.StateLoaded {
		st.setLaunchedTuning("", false)
		return
	}
	if state != embeddedllm.StateLoading {
		// `loaded` follows `loading` in the same run; keep the fingerprint that
		// run recorded rather than re-reading a config the operator may have
		// edited mid-load (which would hide the very staleness this exists to
		// report).
		return
	}
	// Config first, then the record: embeddedTuningFingerprint takes and
	// releases configMu internally, and infoMu must never be held across it —
	// the documented lock order is one-directional, (st.mu | st.infoMu) →
	// configMu.
	st.setLaunchedTuning(f.embeddedTuningFingerprint(), true)
}

// embeddedTuningReloadRequired reports whether a resident model was launched
// with overrides the operator has since changed, i.e. whether the persisted
// tuning only takes effect on the NEXT load.
//
// A missing fingerprint (no resident process, or one this app instance did not
// start) answers false rather than true: "unknown" must not surface as a demand
// to reload a model that may already be running exactly what was asked for. The
// flag is a hint for the operator, not a gate — the launch always reads the live
// config, so a missed hint costs a stale badge, never a wrong launch.
func (f *FrontendAPI) embeddedTuningReloadRequired(loading, loaded bool) bool {
	if !loading && !loaded {
		return false
	}
	launched, ok := f.embedded.launchedTuningFingerprint()
	if !ok {
		return false
	}
	return launched != f.embeddedTuningFingerprint()
}

// ---------------------------------------------------------------------------
// Subsystem state
// ---------------------------------------------------------------------------

// embeddedLLMState is the FrontendAPI-owned bookkeeping of the embedded local
// model: the storage layout, the supervisor, the installer, the cached install
// record and the in-flight-install flag. It lives on FrontendAPI as the value
// field `embedded`, so the zero value is usable and no construction call is
// needed before the first RPC.
//
// TWO mutexes, deliberately separate:
//
//   - mu guards construction and the install-run bookkeeping. It is NEVER held
//     across a call into the supervisor: core calls OnState synchronously from
//     the transitioning goroutine and that handler reads the cached install
//     record, so holding mu there would deadlock on the first transition.
//   - infoMu guards the cached manifest snapshot, the restore-in-progress flag
//     and the last background failure. It is likewise never held across a
//     supervisor call.
//
// Neither mutex is ever taken while configMu is held, which keeps the lock
// order one-directional: (st.mu | st.infoMu) → configMu.
type embeddedLLMState struct {
	mu        sync.Mutex
	built     bool
	layout    embeddedllm.Layout
	server    *embeddedllm.Server
	installer *embeddedllm.Installer
	// loader is a LOCK-FREE snapshot of server, published the moment the
	// supervisor is constructed. It exists so the router build can reach the
	// supervisor without taking mu: ToBuilderConfig runs while configMu is held
	// at every call site, and mu must never be taken under configMu (the lock
	// order is one-directional, (st.mu | st.infoMu) → configMu). Readers get the
	// current supervisor or nil. Only embeddedBuild writes it, and it writes the
	// winner's instance, so a concurrent build publishes one supervisor.
	loader atomic.Pointer[embeddedllm.Server]
	// installing is true while a background install run is in flight. It is
	// the single-run gate: a second InstallEmbeddedLLM is refused instead of
	// racing the first one for the same staging tree.
	installing bool

	infoMu sync.Mutex
	// manifest is the cached manifest.json snapshot (the durable install
	// record). hasManifest reports whether it describes a real installation;
	// without it the cached value is the zero Manifest.
	manifest    embeddedllm.Manifest
	hasManifest bool
	// muted suppresses the state event of a supervisor transition the caller
	// is about to provoke, because the caller emits ONE explicit snapshot
	// right afterwards (the startup restore, an install completion, a
	// removal). Without it those operations would emit the same state twice.
	// It is only ever set and cleared by the goroutine performing the
	// operation — core calls OnState synchronously — so the window is
	// microseconds wide and never spans another caller's transition.
	muted bool
	// lastError carries the cause of the last failed background operation
	// (install, removal). Cleared when the next one starts.
	lastError string
	// launchedTuning is the fingerprint of the memory-plan overrides the
	// CURRENTLY RESIDENT process was launched with, recorded on the
	// supervisor's `loading` transition and cleared on every non-resident one.
	// hasLaunchedTuning says whether a fingerprint was recorded at all.
	//
	// It exists for one question the RPC surface has to answer honestly: "the
	// operator just saved a tuning change — is the model running with it?" A
	// tuning knob is a LAUNCH flag (`-c`, `-ngl`, `-ctk`, `-fit`, …), so the
	// answer is only knowable by comparing the live config against what the
	// running process was actually started with, and nothing else records that.
	// It is deliberately runtime state and NOT part of manifest.json: tuning is
	// an operator setting the config sink carries verbatim, and a second
	// persisted copy would be a second source of truth going stale on the first
	// edit (the same reason `Manifest.Plan` records the plan and not the
	// tuning). It is therefore empty after a restart, which is correct — after
	// a restart nothing is resident either.
	launchedTuning    string
	hasLaunchedTuning bool

	// Test seams. All are nil in production, where the core defaults run
	// (ProbeHardware, spawnOSServer, the supervisor's own readiness client and
	// the real Installer.Install). They exist so this package's tests can drive
	// a full install/load/stop cycle without a network, without the pinned
	// multi-gigabyte artifacts (whose SHA256 pins no fake download can
	// satisfy) and without a real llama-server. Mirrors fetchPaperOriginalFn
	// and gitStatusFn.
	probeFn    func(ctx context.Context, logger *slog.Logger) (embeddedllm.Hardware, error)
	spawnFn    embeddedllm.SpawnFunc
	httpClient *http.Client
	installFn  func(ctx context.Context, in *embeddedllm.Installer, opts embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error)
	// portProbeFn substitutes the loopback port prober used by the pre-spawn
	// port scan, so a test can stage a collision without occupying a real port.
	portProbeFn embeddedllm.PortProber
	// deviceProbeFn substitutes the accelerator-memory probe behind
	// ProbeEmbeddedLLMDevices, so a test can answer (or refuse, or wedge)
	// without a provisioned runtime to spawn. nil in production, where
	// embeddedllm.ProbeDevices runs — the SAME function the supervisor's
	// load-time hook is wired to, so an explicit re-probe and a load-time
	// re-plan can never disagree about the machine.
	deviceProbeFn func(ctx context.Context, binaryPath string, logger *slog.Logger) (embeddedllm.MemoryTopology, bool)
}

// installRecord returns the cached manifest snapshot.
func (s *embeddedLLMState) installRecord() (embeddedllm.Manifest, bool) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	return s.manifest, s.hasManifest
}

// setInstallRecord replaces the cached manifest snapshot.
func (s *embeddedLLMState) setInstallRecord(m embeddedllm.Manifest, ok bool) {
	s.infoMu.Lock()
	s.manifest, s.hasManifest = m, ok
	s.infoMu.Unlock()
}

// muteStateEvent arms/clears the suppression of the next transition's event.
func (s *embeddedLLMState) muteStateEvent(v bool) {
	s.infoMu.Lock()
	s.muted = v
	s.infoMu.Unlock()
}

// stateEventMuted reports whether transition events are suppressed.
func (s *embeddedLLMState) stateEventMuted() bool {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	return s.muted
}

// setError records the cause of a failed background operation.
func (s *embeddedLLMState) setError(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	s.infoMu.Lock()
	s.lastError = msg
	s.infoMu.Unlock()
}

// lastFailure returns the recorded background failure ("" when none).
func (s *embeddedLLMState) lastFailure() string {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	return s.lastError
}

// setLaunchedTuning records the fingerprint of the overrides the process now
// starting was launched with. An empty fingerprint with ok=false clears the
// record, which is what every non-resident transition does.
func (s *embeddedLLMState) setLaunchedTuning(fingerprint string, ok bool) {
	s.infoMu.Lock()
	s.launchedTuning, s.hasLaunchedTuning = fingerprint, ok
	s.infoMu.Unlock()
}

// launchedTuningFingerprint returns the recorded launch fingerprint. ok is
// false when nothing was recorded — no resident process, or one this app
// instance did not start — and a caller MUST treat that as "unknown" rather
// than as "unchanged".
func (s *embeddedLLMState) launchedTuningFingerprint() (string, bool) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	return s.launchedTuning, s.hasLaunchedTuning
}

// deviceProber returns the accelerator-memory prober for an explicit re-probe:
// the test seam when one is installed, the core function otherwise. Read at
// call time, so a test may install it after construction.
func (s *embeddedLLMState) deviceProber() func(ctx context.Context, binaryPath string, logger *slog.Logger) (embeddedllm.MemoryTopology, bool) {
	if s.deviceProbeFn != nil {
		return s.deviceProbeFn
	}
	return embeddedllm.ProbeDevices
}

// ---------------------------------------------------------------------------
// Construction, restore and teardown
// ---------------------------------------------------------------------------

// embeddedBuild returns the supervisor and the installer, constructing them on
// first use and restoring the on-disk state. Construction and restore are pure
// local work: no network, no hardware probe, no process spawn — which is what
// lets startup call it on the critical path and lets an early RPC call it
// safely.
func (f *FrontendAPI) embeddedBuild() (*embeddedllm.Server, *embeddedllm.Installer, error) {
	st := &f.embedded

	st.mu.Lock()
	if st.built {
		server, installer := st.server, st.installer
		st.mu.Unlock()
		return server, installer, nil
	}
	st.mu.Unlock()

	if f.agentDir == "" {
		return nil, nil, errors.New("the embedded LLM is unavailable: no agent directory")
	}
	// Roots come from the centralized path API; core never re-derives them.
	layout, err := embeddedllm.NewLayout(config.RuntimesDir(f.agentDir), config.EmbeddedModelDir(f.agentDir))
	if err != nil {
		return nil, nil, fmt.Errorf("embedded LLM storage layout: %w", err)
	}

	logger := f.log().With("subsystem", "embedded_llm")
	server := embeddedllm.NewServer(layout, logger)
	server.AutoUnload = f.embeddedAutoUnloadPolicy()
	server.OnState = f.onEmbeddedLLMState
	// The persisted port is a preference, not a reservation: it is re-checked
	// before every spawn and walks upward to the next free one, and a move is
	// written back to config so the generated provider base_url keeps matching
	// the socket the server bound.
	server.EnsurePort = f.embeddedEnsurePort
	// A load re-measures the accelerator before it commits to a launch shape,
	// so a GPU that appeared, disappeared or got a new driver since the install
	// is priced at launch instead of being served a shape computed for a
	// machine that no longer exists. The hook is fail-soft in core (a wedged or
	// absent probe launches the recorded plan), so wiring it can never make a
	// load fail — see Server.effectivePlan.
	server.ProbeDevices = embeddedllm.ProbeDevices
	// The launch shape must be planned with the tuning in force WHEN IT RUNS,
	// and the supervisor is built once and cached, so this is a function rather
	// than a value: it reads the live config on every load.
	server.Tuning = f.embeddedTuning
	// The effective context a ready server reports is written back to the
	// tier-1 llm.models override, which otherwise stays frozen at the install's
	// estimate and — because a tier-1 override shadows the tier-1.5 lazy probe —
	// could never be corrected by anything else.
	server.PersistContext = f.persistEmbeddedContext
	installer := embeddedllm.NewInstaller(layout, logger)
	installer.Sink = embeddedConfigSink{f: f}
	// The supervisor owns the process, so the removal stop and the shutdown
	// stop are the same call — the signatures match by design.
	installer.Stop = server.Stop

	// Test seams override the core defaults; production leaves them nil.
	if st.probeFn != nil {
		installer.Probe = st.probeFn
	}
	if st.spawnFn != nil {
		server.Spawn = st.spawnFn
	}
	if st.httpClient != nil {
		server.HTTPClient = st.httpClient
	}

	f.embeddedRestore(server, layout, logger)

	st.mu.Lock()
	// A concurrent builder wins; both build the same thing from the same
	// agent dir, so discarding the loser is correct (and it never started a
	// process — construction is inert).
	if st.built {
		server, installer = st.server, st.installer
	} else {
		st.layout, st.server, st.installer, st.built = layout, server, installer, true
	}
	st.mu.Unlock()
	// Publish the winner's supervisor lock-free for the router build, which
	// runs under configMu and therefore must never take st.mu. Storing outside
	// the lock is deliberate: the value is already the settled winner, and a
	// concurrent identical store is idempotent.
	st.loader.Store(server)
	return server, installer, nil
}

// embeddedRestore reads manifest.json into memory and records whether the model
// is installed. This is the WHOLE of the startup work for this subsystem: it
// performs no download, no probe and no load, so startup neither depends on the
// network nor blocks on a multi-gigabyte weight load.
func (f *FrontendAPI) embeddedRestore(server *embeddedllm.Server, layout embeddedllm.Layout, logger *slog.Logger) {
	st := &f.embedded
	path, err := layout.ManifestPath()
	if err != nil {
		logger.Warn("embedded LLM state not restored: the manifest path is unusable", "error", err)
		st.setInstallRecord(embeddedllm.Manifest{}, false)
		return
	}
	manifest, err := embeddedllm.ReadManifest(path)
	if err != nil {
		if !errors.Is(err, embeddedllm.ErrNotInstalled) {
			logger.Warn("embedded LLM manifest is unreadable; treating the model as not installed",
				"path", path, "error", err)
		}
		st.setInstallRecord(embeddedllm.Manifest{}, false)
		if f.embeddedConfig().Installed {
			// The bytes are gone but config.yaml still claims an install. The
			// generated provider entry is reconciled away on the next config
			// load; report the mismatch instead of silently serving a model
			// that cannot start.
			logger.Warn("embedded_llm.installed is true but no manifest was found; the model needs a reinstall",
				"path", path)
		}
		return
	}

	st.setInstallRecord(manifest, true)
	// ONE event for the restore: suppress the SetInstalled transition and emit
	// an explicit snapshot in initEmbeddedLLM, so a startup neither double-
	// emits nor stays silent when nothing is installed.
	st.muteStateEvent(true)
	if err := server.SetInstalled(true); err != nil {
		logger.Warn("embedded LLM state restore refused", "error", err)
	}
	st.muteStateEvent(false)
	logger.Info("embedded LLM state restored from the manifest",
		"backend", manifest.Backend, "packing", manifest.Packing,
		"port", manifest.Port, "context_size", manifest.ContextSize,
		"runtime_version", manifest.RuntimeVersion)
}

// initEmbeddedLLM restores the embedded local-model state and emits the initial
// embedded_llm:state snapshot. Called from desktop/startup_phases.go on the
// startup path (InitEmbeddedLLM). It never downloads, probes hardware or loads
// the model.
func (f *FrontendAPI) initEmbeddedLLM() {
	server, _, err := f.embeddedBuild()
	if err != nil {
		// Report the unusable subsystem rather than leaving the UI without an
		// initial snapshot: not_installed is the truth when there is no
		// supervisor to ask.
		f.log().Warn("embedded LLM subsystem unavailable", "error", err)
		f.emitEmbeddedLLMState(embeddedllm.StateNotInstalled, 0, err.Error())
		return
	}
	// The policy is re-applied from the live config: construction read it too,
	// but a config reload between construction and startup must still win.
	server.SetAutoUnload(f.embeddedAutoUnloadPolicy())

	snapshot := server.Status()
	// The router was first built inside NewApplication, which has no
	// FrontendAPI and therefore no loader seam. Re-attaching it here — during
	// the startup restore, before backend:ready and so before any session can
	// issue a request — is what makes a cold request to an idle-unloaded model
	// load it transparently instead of failing to connect. Skipped when nothing
	// is installed: there is no embedded provider entry to guard then, and a
	// router rebuild must not become a cost every machine pays at startup.
	if snapshot.State != embeddedllm.StateNotInstalled {
		f.rebuildRouterForEmbeddedTransport()
	}
	f.emitEmbeddedLLMState(snapshot.State, snapshot.Port, snapshot.Message)
}

// stopEmbeddedLLM stops the supervised server if one is running (Shutdown). It
// is a no-op when the subsystem was never constructed — nothing can be running
// then — and idempotent, so calling it twice (or after Cleanup) is safe.
func (f *FrontendAPI) stopEmbeddedLLM(ctx context.Context) error {
	st := &f.embedded
	st.mu.Lock()
	server := st.server
	st.mu.Unlock()
	if server == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	stopCtx, cancel := context.WithTimeout(ctx, embeddedStopTimeout)
	defer cancel()
	if err := server.Stop(stopCtx); err != nil {
		f.log().Error("failed to stop the embedded LLM server", "error", err)
		return err
	}
	return nil
}

// embeddedAutoUnloadPolicy resolves the operator's idle policy from config.
func (f *FrontendAPI) embeddedAutoUnloadPolicy() embeddedllm.AutoUnload {
	cfg := f.embeddedConfig()
	return embeddedllm.NewAutoUnload(cfg.AutoUnload.IsEnabled(), cfg.AutoUnload.IdleMinutes())
}

// embeddedConfig returns a copy of the persisted embedded_llm section. A copy
// (not a pointer) so the caller never touches config state outside configMu.
func (f *FrontendAPI) embeddedConfig() config.EmbeddedLLMConfig {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	if f.config == nil {
		return config.EmbeddedLLMConfig{}
	}
	return f.config.EmbeddedLLM
}

// embeddedEnsurePort is the supervisor's pre-spawn port check
// (Server.EnsurePort): it returns the first free loopback port at or above the
// persisted one, scanning upward one port at a time, and persists a move so the
// generated provider base_url keeps matching the socket the server is about to
// bind.
//
// A persistence failure is deliberately NOT fatal. The server binds the free
// port either way and the ensure-loaded transport redirects every request to
// the live port, so the model stays usable while config.yaml simply keeps the
// previous value until the next successful write; failing here would make a
// multi-gigabyte weight load unusable over an administrative write.
func (f *FrontendAPI) embeddedEnsurePort(ctx context.Context, port int) (int, error) {
	free, err := embeddedllm.SearchFreePort(ctx, port, f.embeddedPortProber())
	if err != nil {
		return 0, err
	}
	if free == port {
		return free, nil
	}
	f.log().Warn("the persisted embedded LLM port is taken; moving to the next free one",
		"persisted", port, "port", free)
	if perr := f.persistEmbeddedPort(free); perr != nil {
		f.log().Warn("failed to persist the moved embedded LLM port",
			"port", free, "error", perr)
	}
	return free, nil
}

// embeddedPortProber returns the loopback prober for the port scan: the test
// seam when one is installed, nil otherwise (core resolves nil to the real bind
// probe). Read at call time, so a test may install it after construction.
func (f *FrontendAPI) embeddedPortProber() embeddedllm.PortProber {
	return f.embedded.portProbeFn
}

// persistEmbeddedPort writes a moved loopback port to embedded_llm.port and
// regenerates the backend-owned provider record from it, so the base_url every
// router build derives stays in agreement with the socket the server bound.
//
// manifest.json keeps the port the install allocated: that is the PREFERRED
// port and the next load re-scans from it, so a port that was only temporarily
// taken is reclaimed instead of drifting upward forever.
//
// It does NOT rebuild the router. This runs inside Server.Load — on the request
// path, while the supervisor's single-instance gate is held — and a rebuild
// there would swap the router underneath an in-flight request. None is needed:
// the entry's transport redirects to the live port, and the persisted value is
// what the next rebuild (a settings save, a profile change, a restart) picks up.
func (f *FrontendAPI) persistEmbeddedPort(port int) error {
	sink := embeddedConfigSink{f: f}

	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}
	if !f.config.EmbeddedLLM.Installed {
		// No generated provider record exists to keep in sync, and writing a
		// port into a section that reports "not installed" would read as
		// corrupt state to an operator. The load still proceeds on the free
		// port; only the config write is skipped.
		f.configMu.Unlock()
		return nil
	}
	previousLLM := f.config.LLM
	previousEmbedded := f.config.EmbeddedLLM

	f.config.EmbeddedLLM.Port = port
	// contextWindow 0: a port move knows nothing about the resolved RAM tier, so
	// the existing llm.models override is left exactly as the install wrote it.
	f.config.SyncEmbeddedLLMProvider(0)

	return sink.saveOrRollback(previousLLM, previousEmbedded)
}

// embeddedTuning resolves the operator's memory-plan overrides
// (embedded_llm.tuning) for a launch. It is wired onto Server.Tuning, which
// calls it on the load path, so it reads the live config rather than a snapshot
// taken when the supervisor was built.
//
// A translation failure is fail-soft and yields the zero Tuning — which IS the
// documented all-Auto plan — because a load must not fail over a config surface
// it did not write. It should be unreachable: validate() checks the same section
// through the same ToTuning call, so a config that loaded translates. Reaching
// this branch means the in-memory config was mutated past validation, and the
// all-Auto plan is the safe answer for that, not a refusal.
func (f *FrontendAPI) embeddedTuning() embeddedllm.Tuning {
	tuning, err := f.embeddedConfig().Tuning.ToTuning()
	if err != nil {
		f.log().Debug("the embedded LLM tuning could not be translated; launching the all-Auto plan",
			"error", err)
		return embeddedllm.Tuning{}
	}
	return tuning
}

// persistEmbeddedContext writes the effective context a READY server reported
// back into the tier-1 llm.models."Bonsai 2 27B".context_window override. It is
// the context analogue of persistEmbeddedPort and runs in the same place: inside
// Server.Load, after readiness, while the supervisor's single-instance gate is
// held.
//
// Why the load path and not GetConfig: the override must stay honest, and
// llm-providers.md gives a tier-1 config override precedence over the tier-1.5
// lazy probe — so a stale override can never be corrected by probing the model
// later. The load path is the only one that already has a resident server to
// ask, and it keeps GetConfig network-free.
//
// It does NOT rebuild the router, for the reason persistEmbeddedPort gives: this
// runs inside Load, usually on behalf of an in-flight request the ensure-loaded
// transport is waiting on, and swapping the router underneath it would be worse
// than serving one session with the previous — still valid — window. The
// persisted value is what the next rebuild (a settings save, a profile change, a
// restart) picks up.
//
// A persistence failure is deliberately NOT fatal: it is returned to core, which
// logs it and keeps the load successful, because the model is resident and
// serving and the manifest already carries the corrected value, so the next load
// retries.
func (f *FrontendAPI) persistEmbeddedContext(_ context.Context, contextSize int) error {
	if contextSize <= 0 {
		// Core only calls this with a value it read off the server, but the
		// override treats <= 0 as "leave the existing one alone", and a
		// non-positive window would fail validate() on the next config load.
		return nil
	}

	st := &f.embedded
	// Mirror the corrected value into the cached install record: core already
	// rewrote manifest.json before calling, and GetEmbeddedLLMStatus answers
	// from this cache, so without the mirror the UI would keep reporting the
	// install's estimate beside a config that carries the measured one.
	if record, ok := st.installRecord(); ok && record.ContextSize != contextSize {
		record.ContextSize = contextSize
		st.setInstallRecord(record, true)
	}

	sink := embeddedConfigSink{f: f}

	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}
	if !f.config.EmbeddedLLM.Installed {
		// No generated provider record exists to keep in sync. The load still
		// proceeds; only the config write is skipped.
		f.configMu.Unlock()
		return nil
	}
	previousLLM := f.config.LLM
	previousEmbedded := f.config.EmbeddedLLM

	// The port is already authoritative in the section, so this regenerates the
	// provider record from it (a no-op when it agrees) and records the context
	// window. A false return means every backend-owned value already matches —
	// the common case on a steady machine — so a value that did not move
	// produces no write and no config:updated event.
	if !f.config.SyncEmbeddedLLMProvider(contextSize) {
		f.configMu.Unlock()
		return nil
	}

	return sink.saveOrRollback(previousLLM, previousEmbedded)
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// onEmbeddedLLMState is the core supervisor's StateEvent transport: it forwards
// every observable transition as the global embedded_llm:state event. It runs
// on the goroutine that made the transition and must never call back into the
// supervisor (core documents OnState as non-reentrant), which is why it only
// reads the cached install record and the config.
func (f *FrontendAPI) onEmbeddedLLMState(ev embeddedllm.StateEvent) {
	// Recorded BEFORE the mute check: muting suppresses an EVENT the caller is
	// about to replace with an explicit snapshot, not the bookkeeping behind
	// ReloadRequired. In production no muted transition is a load, so the order
	// only matters for the invariant, not for the outcome.
	f.noteEmbeddedLaunchTuning(ev.State)

	if f.embedded.stateEventMuted() {
		return
	}
	f.emitEmbeddedLLMState(ev.State, ev.Port, ev.Message)
}

// emitEmbeddedLLMState builds and emits the global embedded_llm:state payload.
// state/port/message come from the transition (or the startup snapshot); the
// install record and the auto-unload budget come from the cached manifest and
// the persisted config. Nil-guarded like every other emitter: most tests do not
// wire emitEvent.
func (f *FrontendAPI) emitEmbeddedLLMState(state embeddedllm.State, port int, message string) {
	if f.emitEvent == nil {
		return
	}
	f.emitEvent(EventEmbeddedLLMState, f.embeddedStatePayload(state, port, message))
}

// embeddedStatePayload merges a supervision state with the install record and
// the persisted config. Precedence is explicit: the live supervisor port wins
// over the persisted one, the manifest (what is actually on disk) wins over the
// informational config copy of packing/backend, and the error field carries the
// supervisor's cause only for the error state — otherwise the last background
// failure, so a failed install is visible in the same field the UI reads.
func (f *FrontendAPI) embeddedStatePayload(state embeddedllm.State, port int, message string) EmbeddedLLMStateData {
	manifest, hasManifest := f.embedded.installRecord()
	cfg := f.embeddedConfig()

	packing, backend := cfg.Packing, cfg.Backend
	contextSize := 0
	if hasManifest {
		packing = string(manifest.Packing)
		backend = string(manifest.Backend)
		contextSize = manifest.ContextSize
	}
	if port <= 0 {
		port = cfg.Port
	}
	if port <= 0 && hasManifest {
		port = manifest.Port
	}

	errText := message
	if state != embeddedllm.StateError {
		errText = f.embedded.lastFailure()
	}

	return EmbeddedLLMStateData{
		Installed:         state != embeddedllm.StateNotInstalled,
		Loading:           state == embeddedllm.StateLoading,
		Loaded:            state == embeddedllm.StateLoaded,
		Packing:           packing,
		Backend:           backend,
		Port:              port,
		ContextSize:       contextSize,
		AutoUnloadMinutes: cfg.AutoUnload.IdleMinutes(),
		Error:             errText,
	}
}

// emitEmbeddedInstallProgress forwards one core Progress update as the global
// embedded_llm:install_progress event. It is the InstallOptions.Progress
// callback of a background install run, so it is called from that goroutine and
// must not block: the emit is a synchronous Wails dispatch, and the core
// downloader already throttles the byte-level updates.
func (f *FrontendAPI) emitEmbeddedInstallProgress(p embeddedllm.Progress) {
	if f.emitEvent == nil {
		return
	}
	f.emitEvent(EventEmbeddedLLMInstallProgress, EmbeddedLLMProgressData{
		Component:  string(p.Component),
		Stage:      p.Stage,
		BytesDone:  p.BytesDone,
		BytesTotal: p.BytesTotal,
	})
}

// emitEmbeddedRuntimeError raises the user-visible toast for a background
// failure — the case where no RPC is left to carry the error. Mirrors the
// existing runtime_error emitters (id, message, error_code).
func (f *FrontendAPI) emitEmbeddedRuntimeError(code, message string) {
	if f.emitEvent == nil {
		return
	}
	f.emitEvent(EventRuntimeError, map[string]string{
		"id":         uuid.New().String(),
		"message":    message,
		"error_code": code,
	})
}

// ---------------------------------------------------------------------------
// RPC surface
// ---------------------------------------------------------------------------

// GetEmbeddedLLMStatus returns the embedded local-model status: the supervision
// state, the install record (including the RECORDED device topology, the launch
// plan it informed and its notes), the resolved auto-unload policy and whether a
// persisted tuning change is waiting for the next load.
//
// Read-only getter, so it returns no error (desktop-frontend.md convention):
// when the subsystem cannot be constructed (no agent directory — only possible
// before startup) it reports Available=false with the not-installed state
// instead of failing. It performs no network I/O and no hardware probe — the
// topology and the plan it reports are the ones the install RECORDED, and a
// measurement of this instant is ProbeEmbeddedLLMDevices. The only disk access
// is the manifest restore on the first call.
func (f *FrontendAPI) GetEmbeddedLLMStatus() EmbeddedLLMStatus {
	cfg := f.embeddedConfig()
	status := EmbeddedLLMStatus{
		State:             string(embeddedllm.StateNotInstalled),
		Installed:         cfg.Installed,
		Packing:           cfg.Packing,
		Backend:           cfg.Backend,
		Port:              cfg.Port,
		AutoUnloadEnabled: cfg.AutoUnload.IsEnabled(),
		AutoUnloadMinutes: cfg.AutoUnload.IdleMinutes(),
		RuntimeVersion:    cfg.RuntimeVersion,
		InstalledAt:       cfg.InstalledAt,
		ModelFile:         cfg.ModelFile,
		ModelName:         config.EmbeddedLLMModelName,
		Error:             f.embedded.lastFailure(),
		Guards:            []EmbeddedLLMGuard{},
		Devices:           []EmbeddedLLMDevice{},
		Plan:              embeddedPlanDTO(nil),
	}
	if manifest, ok := f.embedded.installRecord(); ok {
		// The manifest is what is actually on disk; the config copy of
		// packing/backend is informational. The port falls back to it as well,
		// so a status read still reports the endpoint the supervisor would
		// launch even before the config was synced.
		status.Packing = string(manifest.Packing)
		status.Backend = string(manifest.Backend)
		status.ContextSize = manifest.ContextSize
		// The degradation record travels with the manifest, not the config: it
		// describes the install that produced these bytes, and config.yaml is
		// not where a support bundle looks for it.
		status.PackingReason = string(manifest.PackingReason)
		status.GPUFamily = string(manifest.GPUFamily)
		status.Guards = embeddedGuardsDTO(manifest.Guards)
		// The measured topology and the launch shape it informed are recorded
		// beside the bytes they describe, for the same reason: they are facts
		// about THIS install, not tuning a config section carries. A nil
		// Topology (no probe ever answered) leaves the zero values in place,
		// which is why TopologyProbedAt — not the empty Devices array — is what
		// a reader checks for "unknown".
		if manifest.Topology != nil {
			topology := embeddedDevicesDTO(*manifest.Topology)
			status.Devices = topology.Devices
			status.Unified = topology.Unified
			status.HostRAMGiB = topology.HostRAMGiB
			status.DeviceBudgetMiB = topology.DeviceBudgetMiB
			status.HostBudgetMiB = topology.HostBudgetMiB
			status.TopologyProbedAt = topology.ProbedAt
		}
		status.Plan = embeddedPlanDTO(manifest.Plan)
		if status.Port <= 0 {
			status.Port = manifest.Port
		}
		if status.ModelFile == "" {
			status.ModelFile = manifest.ModelFile
		}
		if status.RuntimeVersion == "" {
			status.RuntimeVersion = embeddedllm.RuntimeTag
		}
		if status.InstalledAt == "" {
			status.InstalledAt = manifest.InstalledAt
		}
	}

	server, _, err := f.embeddedBuild()
	if err != nil {
		f.log().Debug("embedded LLM status without a supervisor", "error", err)
		if status.Installed && status.Port > 0 {
			status.BaseURL = embeddedBaseURL(status.Port)
			status.ModelID = embeddedCompositeModelID()
		}
		return status
	}
	status.Available = true

	snapshot := server.Status()
	status.State = string(snapshot.State)
	status.Installed = snapshot.State != embeddedllm.StateNotInstalled
	status.Loading = snapshot.State == embeddedllm.StateLoading
	status.Loaded = snapshot.State == embeddedllm.StateLoaded
	status.Pid = snapshot.Pid
	status.FitWarning = snapshot.FitWarning
	if snapshot.State == embeddedllm.StateError {
		status.Error = snapshot.Message
	}
	if snapshot.Port > 0 {
		status.Port = snapshot.Port
	}
	status.Installing = f.embeddedInstalling()
	if status.Installed && status.Port > 0 {
		status.BaseURL = embeddedBaseURL(status.Port)
	}
	if status.Installed {
		status.ModelID = embeddedCompositeModelID()
	} else {
		status.ModelID = ""
		status.BaseURL = ""
	}
	if remaining, armed := server.IdleRemaining(); armed && remaining > 0 {
		status.IdleRemainingSeconds = int64(remaining / time.Second)
	}
	// Computed LAST, from the live supervision state: a tuning change only ever
	// reaches a process that has not been spawned yet, so the question is
	// whether the resident one was started with the overrides now on disk.
	status.ReloadRequired = f.embeddedTuningReloadRequired(status.Loading, status.Loaded)
	return status
}

// InstallEmbeddedLLM provisions the pinned runtime and weights for this machine.
//
// The synchronous part is only the gates: one install at a time, a bounded
// hardware probe, and the combined memory refusal — so a machine whose memory
// cannot hold the model gets an actionable error from THIS call and not one
// byte is downloaded. Everything heavy (the multi-gigabyte resumable download,
// verification, extraction, the macOS provisioning and smoke test) then runs on
// a background goroutine: the RPC returns as soon as the run is started.
//
// Progress arrives as embedded_llm:install_progress (one event per component
// and stage) and the outcome as embedded_llm:state; a background failure
// additionally raises a runtime_error toast, because no RPC is left to carry
// it. The install is resumable: verified artifacts are kept as cache hits, so a
// retry continues where the previous attempt stopped.
func (f *FrontendAPI) InstallEmbeddedLLM() error {
	server, installer, err := f.embeddedBuild()
	if err != nil {
		return err
	}
	if !f.beginEmbeddedInstall() {
		return errors.New("an embedded LLM install is already running")
	}

	// The MEMORY GATE runs HERE, synchronously: a refusal the caller only
	// learns about from a toast ten minutes into a download is not actionable.
	// The probe is local and bounded (each external helper carries its own 2s
	// budget inside core). A synchronous refusal returns the error to the caller
	// and raises NO toast: the rejected promise is the report, and a toast on top
	// of it would say it twice.
	//
	// This is the SAME gate Resolve runs first, so the two cannot disagree — it
	// is exposed rather than re-derived here precisely so that a click and the
	// background install answer with one voice. It prices both memory pools
	// instead of a flat RAM floor: an unreadable ACCELERATOR budget degrades
	// rather than refuses, while an unreadable RAM total still does
	// (ErrRAMUnknown), because the host budget is derived from it on every path.
	probeCtx, cancel := context.WithTimeout(f.ctx(), embeddedProbeTimeout)
	defer cancel()
	hw, err := f.embeddedProbe(probeCtx)
	if err != nil {
		refusal := fmt.Errorf("the embedded LLM install was refused: %w", err)
		f.refuseEmbeddedInstall(refusal)
		return refusal
	}
	if err := embeddedllm.CheckMemoryBudget(embeddedllm.ResolveInput{
		MachineProfile: embeddedllm.MachineProfile{
			Platform: hw.Platform,
			Backend:  hw.Backend,
			RAMGiB:   hw.RAMGiB,
		},
	}); err != nil {
		refusal := fmt.Errorf("the embedded LLM install was refused: %w", err)
		f.refuseEmbeddedInstall(refusal)
		return refusal
	}

	go f.runEmbeddedInstall(server, installer, hw)
	return nil
}

// RemoveEmbeddedLLM stops a running server and removes the runtime, the weights
// and the manifest, then clears the backend-owned provider record (migrating
// llm.default_model off the embedded composite in the same operation).
//
// Blocking: the work is local file removal plus a config save. Verified
// artifacts of an unrelated component are never touched, and the flat
// embedding-model files sharing <agentDir>/models survive.
func (f *FrontendAPI) RemoveEmbeddedLLM() error {
	server, installer, err := f.embeddedBuild()
	if err != nil {
		return err
	}
	if f.embeddedInstalling() {
		return errors.New("an embedded LLM install is running; wait for it to finish before removing the model")
	}
	f.embedded.setError(nil)

	if err := installer.Remove(f.ctx()); err != nil {
		f.embedded.setError(err)
		f.log().Error("embedded LLM removal failed", "error", err)
		f.emitEmbeddedRuntimeError(embeddedErrCodeRemove,
			"The local model could not be removed: "+err.Error())
		f.emitEmbeddedStateFrom(server)
		return err
	}

	// The bytes and the manifest are gone; the supervisor must not keep
	// reporting an installation that no longer exists.
	f.embedded.setInstallRecord(embeddedllm.Manifest{}, false)
	f.embedded.muteStateEvent(true)
	err = server.SetInstalled(false)
	f.embedded.muteStateEvent(false)
	f.emitEmbeddedStateFrom(server)
	if err != nil {
		// Only reachable when a process is still live, which Remove's own stop
		// should have made impossible. Report it honestly rather than claiming
		// the model is gone while it holds gigabytes.
		f.embedded.setError(err)
		return fmt.Errorf("the local model was removed from disk but its server is still running: %w", err)
	}
	return nil
}

// LoadEmbeddedLLM starts the local server and blocks until the model can
// actually answer — readiness is a non-empty /v1/models response, never a
// health check. It is idempotent (a loaded model is a no-op) and single-
// instance: concurrent calls cannot start a second process.
//
// Blocking on purpose: a load is an explicit user action whose outcome the
// caller needs, and the weight load it waits for is bounded by the supervisor's
// ready budget. The embedded_llm:state events (loading → loaded | error) let
// the UI show progress while the promise is pending, and the idle budget starts
// only once the load completes, so a slow load never eats it.
func (f *FrontendAPI) LoadEmbeddedLLM() error {
	server, _, err := f.embeddedBuild()
	if err != nil {
		return err
	}
	if f.embeddedInstalling() {
		return errors.New("an embedded LLM install is running; wait for it to finish before loading the model")
	}
	f.embedded.setError(nil)

	if err := server.Load(f.ctx()); err != nil {
		if errors.Is(err, embeddedllm.ErrNotInstalled) {
			return errors.New("the embedded LLM is not installed — install it before loading it")
		}
		// The state event already carries the supervisor's cause (including the
		// tail of the server's own log); return the same detail to the caller.
		return err
	}
	return nil
}

// UnloadEmbeddedLLM stops the server process, deterministically returning its
// RAM/VRAM. The bytes stay on disk, so the state afterwards is "installed"
// (unloaded) and a later Load restarts it without a download.
//
// Blocking and idempotent: unloading a model that is not resident succeeds. A
// stop that did not take is reported as an error and keeps the state honest
// (error) instead of claiming the memory was released.
func (f *FrontendAPI) UnloadEmbeddedLLM() error {
	server, _, err := f.embeddedBuild()
	if err != nil {
		return err
	}
	return server.Unload(f.ctx())
}

// SetEmbeddedLLMAutoUnload persists the idle-unload policy
// (embedded_llm.auto_unload) and applies it to the supervisor immediately: the
// timer is re-armed against the existing activity stamp while the model is
// loaded, and disarmed when the policy is off.
//
// minutes must be at least 1 — the same rule config validation enforces, so a
// rejected value never reaches config.yaml. The setting is an operator
// preference, not install state: it survives a removal and applies to the next
// install.
func (f *FrontendAPI) SetEmbeddedLLMAutoUnload(enabled bool, minutes int) error {
	if minutes < 1 {
		return fmt.Errorf("the auto-unload budget must be at least 1 minute (got %d)", minutes)
	}

	server, _, err := f.embeddedBuild()
	if err != nil {
		// The subsystem being unavailable is not a reason to lose the
		// operator's setting: persist it, and report the supervisor problem.
		f.log().Warn("embedded LLM subsystem unavailable; persisting the auto-unload setting only", "error", err)
		server = nil
	}

	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}
	previous := f.config.EmbeddedLLM.AutoUnload
	enabledValue, minutesValue := enabled, minutes
	f.config.EmbeddedLLM.AutoUnload = config.AutoUnloadConfig{
		Enabled: &enabledValue,
		Minutes: &minutesValue,
	}
	// Persist under configMu so a failed write restores the exact prior value
	// before any reader observes the new one (the UpdateLLMConfig pattern).
	if err := config.Save(f.config, f.configPath); err != nil {
		f.config.EmbeddedLLM.AutoUnload = previous
		f.configMu.Unlock()
		return fmt.Errorf("failed to persist the auto-unload setting: %w", err)
	}
	f.configMu.Unlock()
	f.emitConfigUpdated()

	if server != nil {
		server.SetAutoUnload(embeddedllm.NewAutoUnload(enabled, minutes))
		f.emitEmbeddedStateFrom(server)
	}
	return nil
}

// GetEmbeddedLLMTuning returns the operator's persisted memory-plan overrides
// (embedded_llm.tuning), field for field, with nil meaning "unset — the planner
// decides".
//
// Unlike GetEmbeddedLLMStatus this getter DOES return an error, and the reason is
// the one case it has: before startup there is no config to read, and the
// fail-soft answer an error-free signature would force — an all-nil DTO — is
// indistinguishable from "the operator overrode nothing". An editor rendering
// that would then offer a Save button whose click WIPES the real tuning. A
// getter that cannot read the truth must not fabricate one, so the missing
// config is reported instead.
//
// It performs no network I/O, no hardware probe and no translation: this is the
// OVERRIDE, not the outcome. The resolved launch shape — what these knobs
// actually produced — is EmbeddedLLMStatus.Plan.
func (f *FrontendAPI) GetEmbeddedLLMTuning() (EmbeddedLLMTuningDTO, error) {
	f.configMu.RLock()
	if f.config == nil {
		f.configMu.RUnlock()
		return EmbeddedLLMTuningDTO{}, errors.New("config not initialized")
	}
	tuning := f.config.EmbeddedLLM.Tuning
	f.configMu.RUnlock()

	return embeddedTuningDTO(tuning), nil
}

// SetEmbeddedLLMTuning writes a PARTIAL update of embedded_llm.tuning: a nil
// field keeps the stored value, a present field replaces it verbatim, and a knob
// named in Reset is cleared back to unset. See EmbeddedLLMTuningRequest for the
// three-state encoding and why Reset exists.
//
// An invalid patch is refused WITHOUT A WRITE. The whole fold-validate-write
// sequence runs under configMu, so the section it validated is byte-for-byte the
// section it persists — there is no window in which a concurrent writer could
// substitute a different stored tuning between the two. Validation is
// `TuningConfig.ToTuning`, the single translation config.validate() itself
// delegates to, so "the RPC accepted it" and "the next config load accepts it"
// are the same statement and a refused key can never reach config.yaml. The
// error names the offending key and lists every legal spelling.
//
// A failed persist rolls the in-memory state back through the shared
// save-or-rollback tail, so a rejected write and a failed write are
// indistinguishable from the caller's side. A patch that changes nothing is a
// success with no write at all: no config.yaml rewrite, no config:updated, no
// router rebuild — the same no-op-no-write rule the load-path context persist
// follows.
//
// THE RESIDENT MODEL IS NOT RESTARTED, AND THAT IS THE DECISION, NOT AN
// OMISSION. Every tuning knob is a launch flag (`-c`, `-ngl`, `-ctk`/`-ctv`,
// `-fit`, `-np`, `-cram`, `-kvo`, `--mmproj-offload`), so a change can only take
// effect on the NEXT load — and a load of a 6–8 GiB weight file takes minutes
// and drops every in-flight request. Restarting one because an operator moved a
// slider in Settings would be a multi-minute denial of service with no
// confirmation behind it, and doing it silently is worse than not doing it. So
// this method persists, reports, and leaves the timing to the operator:
// EmbeddedLLMStatus.reload_required says whether the running process predates
// the stored tuning, and UnloadEmbeddedLLM followed by LoadEmbeddedLLM (or the
// next launch, or the next app start) applies it. An operator who wants the
// change NOW has an explicit, already-bound two-click path for it.
//
// The write ends on the same tail as every other embedded-LLM config mutation:
// atomic save-or-rollback, config:updated, then the judge/router rebuild that
// keeps the in-memory router in step with the file just written. The rebuild is
// idempotent here — no tuning knob feeds the generated provider record — and is
// done anyway so this surface has ONE tail rather than a per-method variation on
// one.
//
// Tuning is an operator SETTING, not install state: it survives a removal,
// applies to the next install, and this method does not require the model to be
// installed.
func (f *FrontendAPI) SetEmbeddedLLMTuning(req EmbeddedLLMTuningRequest) error {
	server, _, err := f.embeddedBuild()
	if err != nil {
		// The subsystem being unavailable is not a reason to lose the
		// operator's tuning: persist it, and report the supervisor problem
		// through the missing state event rather than by dropping the write.
		// Mirrors SetEmbeddedLLMAutoUnload.
		f.log().Warn("embedded LLM subsystem unavailable; persisting the tuning only", "error", err)
		server = nil
	}

	// saveMu FIRST (the documented saveMu → configMu order), so this write is
	// serialized against the install/remove sink writes and against every other
	// whole-config save.
	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	sink := embeddedConfigSink{f: f}

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}

	stored := f.config.EmbeddedLLM.Tuning
	next, err := applyEmbeddedTuningRequest(stored, req)
	if err != nil {
		f.configMu.Unlock()
		return err
	}
	// Translate BEFORE writing: this is the same check validate() runs on load,
	// so a patch that would make config.yaml unloadable is refused while the
	// file is still intact.
	if _, terr := next.ToTuning(); terr != nil {
		f.configMu.Unlock()
		return fmt.Errorf("invalid embedded LLM tuning: %w", terr)
	}
	if reflect.DeepEqual(next, stored) {
		// Nothing moved. A no-op must not rewrite config.yaml, emit
		// config:updated or rebuild the router: all three would tell the app a
		// change happened that did not.
		f.configMu.Unlock()
		return nil
	}

	previousLLM := f.config.LLM
	previousEmbedded := f.config.EmbeddedLLM
	f.config.EmbeddedLLM.Tuning = next
	// saveOrRollback is called with configMu HELD and releases it: on success it
	// emits config:updated, on failure it restores both sections so a reader
	// never observes a tuning the file does not carry.
	if err := sink.saveOrRollback(previousLLM, previousEmbedded); err != nil {
		return err
	}

	f.rebuildAfterEmbeddedConfigChange()
	if server != nil {
		// One explicit snapshot so the UI re-reads status and picks up the
		// reload_required this write may have just set. The store's contract is
		// invalidate-and-re-read, not patch, so an event is enough.
		f.emitEmbeddedStateFrom(server)
	}
	return nil
}

// ProbeEmbeddedLLMDevices measures THIS machine's accelerator memory on demand
// and returns the topology: the device inventory, whether the pool is unified
// with host RAM, and the two budgets a fit decision may spend.
//
// It exists because the topology EmbeddedLLMStatus reports is a RECORDED
// snapshot taken at provision time, and a snapshot goes stale: a GPU appears or
// disappears, a driver updates, another process takes the memory. The supervisor
// already re-measures opportunistically on every load, but that measurement is
// fail-soft and invisible, so an operator diagnosing "why did it plan this
// shape?" needs a way to ask directly and to see a REFUSAL rather than a silent
// fallback.
//
// This is the one embedded-LLM read that is allowed to be slow and to fail, and
// therefore the one that returns an error: it spawns the provisioned
// llama-server with --list-devices, and a probe that does not answer is reported
// as the actionable failure it is instead of being rendered as a machine with no
// accelerator. Core's ProbeDevices is fail-soft by contract — a load must not
// fail because a driver query wedged — and this RPC converts exactly that
// (zero, false) into an error, because here nothing depends on the load and
// everything depends on the answer being real.
//
// It does NOT persist anything and does NOT re-plan: the measurement is returned
// to the caller and discarded. The install record keeps the snapshot the plan
// was actually made from, so a support bundle still describes the decision
// rather than the machine as it happens to look now.
//
// Requires an installed runtime — the probe asks the provisioned binary, which
// is the only source that agrees with the runtime's own allocation decisions —
// and is refused while an install is in flight, because a half-staged runtime
// tree has no trustworthy binary to ask.
func (f *FrontendAPI) ProbeEmbeddedLLMDevices() (EmbeddedLLMDevicesDTO, error) {
	server, _, err := f.embeddedBuild()
	if err != nil {
		return EmbeddedLLMDevicesDTO{}, err
	}
	if f.embeddedInstalling() {
		return EmbeddedLLMDevicesDTO{}, errors.New(
			"an embedded LLM install is running; wait for it to finish before probing the devices")
	}

	manifest, ok := f.embedded.installRecord()
	if !ok {
		return EmbeddedLLMDevicesDTO{}, errors.New(
			"the embedded LLM is not installed — the device probe asks the provisioned runtime, so install it first")
	}

	runtimeDir, err := server.Layout.RuntimeDir(manifest.Backend)
	if err != nil {
		return EmbeddedLLMDevicesDTO{}, fmt.Errorf("the embedded LLM runtime tree is unusable: %w", err)
	}
	// The supervisor's own HostOS override, so a test (or a future cross-host
	// probe) resolves the same binary name the launch would. Empty means host.
	binary, err := embeddedllm.ServerBinaryPath(runtimeDir, server.HostOS)
	if err != nil {
		return EmbeddedLLMDevicesDTO{}, fmt.Errorf("the embedded LLM runtime binary is missing: %w", err)
	}

	ctx, cancel := context.WithTimeout(f.ctx(), embeddedProbeTimeout)
	defer cancel()

	logger := f.log().With("subsystem", "embedded_llm")
	topology, ok := f.embedded.deviceProber()(ctx, binary, logger)
	if !ok {
		// Fail-soft in core, actionable here. The cause is already at Debug in
		// the probe; this is the operator-facing half, and it deliberately does
		// not guess which of "no binary", "wedged driver" or "unparseable
		// output" it was.
		logger.Warn("the embedded LLM device probe did not answer", "binary", binary)
		return EmbeddedLLMDevicesDTO{}, errors.New(
			"the device probe did not answer: the provisioned runtime reported no recognizable accelerator inventory (see the debug log for the cause)")
	}

	logger.Debug("embedded LLM device probe answered on demand",
		"devices", len(topology.Devices), "unified", topology.Unified,
		"device_budget_mib", topology.DeviceBudgetMiB(), "host_budget_mib", topology.HostBudgetMiB())
	return embeddedDevicesDTO(topology), nil
}

// ---------------------------------------------------------------------------
// Background install run
// ---------------------------------------------------------------------------

// beginEmbeddedInstall claims the single install slot. False means a run is
// already in flight.
func (f *FrontendAPI) beginEmbeddedInstall() bool {
	st := &f.embedded
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.installing {
		return false
	}
	st.installing = true
	return true
}

// embeddedInstalling reports whether a background install run is in flight.
func (f *FrontendAPI) embeddedInstalling() bool {
	st := &f.embedded
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.installing
}

// endEmbeddedInstall releases the install slot.
func (f *FrontendAPI) endEmbeddedInstall() {
	st := &f.embedded
	st.mu.Lock()
	st.installing = false
	st.mu.Unlock()
}

// embeddedProbe runs the hardware probe through the seam (production:
// core/embeddedllm.ProbeHardware).
func (f *FrontendAPI) embeddedProbe(ctx context.Context) (embeddedllm.Hardware, error) {
	if fn := f.embedded.probeFn; fn != nil {
		return fn(ctx, f.log())
	}
	return embeddedllm.ProbeHardware(ctx, f.log())
}

// runEmbeddedInstall performs the download/verify/extract/provision run on a
// background goroutine. ctx is the APPLICATION context, not the RPC's: the run
// must outlive the call that started it and stop only when the app quits.
func (f *FrontendAPI) runEmbeddedInstall(server *embeddedllm.Server, installer *embeddedllm.Installer, hw embeddedllm.Hardware) {
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("the embedded LLM install panicked: %v", r)
			f.log().Error("panic during the embedded LLM install", "panic", r)
			f.failEmbeddedInstall(err)
			f.emitEmbeddedStateFrom(server)
			f.endEmbeddedInstall()
		}
	}()

	ctx := f.ctx()
	if ctx == nil {
		ctx = context.Background()
	}

	// A per-run copy of the installer pins the probe result the RPC already
	// paid for, so the background run neither re-probes nor races a second
	// install over the shared field. Copying is safe: Installer is a plain
	// struct of injectable fields with no internal synchronization.
	run := *installer
	run.Probe = func(context.Context, *slog.Logger) (embeddedllm.Hardware, error) { return hw, nil }

	opts := embeddedllm.InstallOptions{
		Platform: hw.Platform,
		Backend:  hw.Backend,
		Progress: f.emitEmbeddedInstallProgress,
	}
	// A repair/reinstall keeps the persisted port, so the generated provider
	// base_url (always derived from it) does not churn.
	if cfg := f.embeddedConfig(); cfg.Port >= config.EmbeddedLLMMinPort && cfg.Port <= config.EmbeddedLLMMaxPort {
		opts.Port = cfg.Port
	}

	var report *embeddedllm.InstallReport
	var err error
	if fn := f.embedded.installFn; fn != nil {
		report, err = fn(ctx, &run, opts)
	} else {
		report, err = run.Install(ctx, opts)
	}
	if err == nil && report == nil {
		// Unreachable for the real Installer (a nil error always carries a
		// report); reported explicitly rather than recovered from a nil deref.
		err = errors.New("the embedded LLM install finished without reporting its result")
	}
	if err != nil {
		f.failEmbeddedInstall(err)
		f.emitEmbeddedStateFrom(server)
		f.endEmbeddedInstall()
		return
	}

	f.embedded.setError(nil)
	f.embedded.setInstallRecord(report.Manifest, true)
	// The sink already persisted the config and rebuilt the router; what is
	// left is the supervisor's view of the disk. ONE event for the whole
	// operation: the transition is muted and the snapshot below is emitted
	// unconditionally, so a repair install (whose state does not change) still
	// reports that it finished.
	f.embedded.muteStateEvent(true)
	if err := server.SetInstalled(true); err != nil {
		f.log().Warn("the embedded LLM supervisor refused the installed state", "error", err)
	}
	f.embedded.muteStateEvent(false)
	server.SetAutoUnload(f.embeddedAutoUnloadPolicy())
	f.log().Info("embedded LLM install complete",
		"backend", report.Manifest.Backend, "packing", report.Manifest.Packing,
		"packing_reason", report.PackingReason, "gpu_family", report.Manifest.GPUFamily,
		"guards", embeddedGuardIDs(report.Guards),
		"port", report.Manifest.Port, "context_size", report.Manifest.ContextSize)
	f.emitEmbeddedStateFrom(server)
	// The slot is released LAST: it is what an observer (the status RPC, the
	// UI) reads as "the run is over", so every effect of the run — the config
	// save, the supervisor state and the completion event — is already visible
	// through this mutex by the time it flips.
	f.endEmbeddedInstall()
}

// refuseEmbeddedInstall releases the install slot and records a SYNCHRONOUS
// refusal (the single-run gate, an unreadable hardware probe, the combined
// memory gate). It raises no toast: the RPC returns the error to its caller, which is
// the report, and a toast on top of it would say the same thing twice.
func (f *FrontendAPI) refuseEmbeddedInstall(err error) {
	f.endEmbeddedInstall()
	f.embedded.setError(err)
	f.log().Warn("embedded LLM install refused", "error", err)
}

// failEmbeddedInstall records a BACKGROUND failure: the cause is kept for the
// status/event error field, and a runtime_error toast is raised because no RPC
// is left to carry it. The caller releases the install slot afterwards, so the
// failure is fully reported before the run looks finished.
func (f *FrontendAPI) failEmbeddedInstall(err error) {
	f.embedded.setError(err)
	f.log().Error("embedded LLM install failed", "error", err)
	f.emitEmbeddedRuntimeError(embeddedErrCodeInstall,
		"The local model could not be installed: "+err.Error())
}

// embeddedBaseURL derives the OpenAI-compatible endpoint from a loopback port.
// Deriving (never storing) it is what keeps a port reallocation from leaving a
// stale endpoint behind — the same rule backend/config applies to the generated
// provider record.
func embeddedBaseURL(port int) string {
	if port <= 0 {
		return ""
	}
	return fmt.Sprintf("http://%s:%d/v1", config.EmbeddedLLMHost, port)
}

// embeddedCompositeModelID is the provider/model id the router exposes for the
// local model.
func embeddedCompositeModelID() string {
	return config.EmbeddedLLMProviderName + "/" + config.EmbeddedLLMModelName
}

// emitEmbeddedStateFrom emits a state snapshot taken from the supervisor.
func (f *FrontendAPI) emitEmbeddedStateFrom(server *embeddedllm.Server) {
	if server == nil {
		return
	}
	snapshot := server.Status()
	f.emitEmbeddedLLMState(snapshot.State, snapshot.Port, snapshot.Message)
}

// ---------------------------------------------------------------------------
// Config sink (core/embeddedllm.ConfigSink)
// ---------------------------------------------------------------------------

// embeddedConfigSink is the production half of the core → config boundary: the
// only channel through which the subsystem reaches config.yaml (core cannot
// import backend/config). Its state mutation is exactly the reference
// implementation pinned by backend/config/embedded_llm_sink_test.go; on top of
// it, this one persists the file and rebuilds the LLM router so a freshly
// installed model is usable without a restart.
type embeddedConfigSink struct{ f *FrontendAPI }

var _ embeddedllm.ConfigSink = embeddedConfigSink{}

// ApplyInstalled writes embedded_llm.* from the install record, fills the
// auto-unload defaults WITHOUT overwriting an explicit operator choice (both
// knobs are pointers, so nil is distinguishable from an explicit false), and
// regenerates the backend-owned provider entry plus the context-window override
// from the authoritative state. The tuning section is operator-owned as well,
// so it is carried through verbatim: installing the model never resets a tuned
// memory plan.
func (s embeddedConfigSink) ApplyInstalled(_ context.Context, state embeddedllm.InstallState) error {
	f := s.f
	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}
	previousLLM := f.config.LLM
	previousEmbedded := f.config.EmbeddedLLM

	autoUnload := f.config.EmbeddedLLM.AutoUnload
	tuning := f.config.EmbeddedLLM.Tuning
	f.config.EmbeddedLLM = config.EmbeddedLLMConfig{
		Installed:      true,
		Packing:        string(state.Packing),
		Backend:        string(state.Backend),
		Port:           state.Port,
		ModelFile:      state.ModelFile,
		RuntimeVersion: state.RuntimeVersion,
		InstalledAt:    state.InstalledAt,
		AutoUnload:     autoUnload,
		Tuning:         tuning,
	}
	if f.config.EmbeddedLLM.AutoUnload.Enabled == nil {
		enabled := state.AutoUnloadEnabled
		f.config.EmbeddedLLM.AutoUnload.Enabled = &enabled
	}
	if f.config.EmbeddedLLM.AutoUnload.Minutes == nil {
		minutes := state.AutoUnloadMinutes
		f.config.EmbeddedLLM.AutoUnload.Minutes = &minutes
	}
	// The install is the only caller that has resolved a context tier, so it is
	// the only one that writes the llm.models override.
	f.config.SyncEmbeddedLLMProvider(state.ContextSize)

	if err := s.saveOrRollback(previousLLM, previousEmbedded); err != nil {
		return err
	}
	f.rebuildAfterEmbeddedConfigChange()
	return nil
}

// ApplyRemoved clears the install state, migrates llm.default_model off the
// embedded composite (an empty default fails validation, so the composite must
// MOVE to another enabled model rather than simply be cleared) and drops the
// provider record. The auto-unload and tuning knobs are operator settings, not
// install state, so both survive.
func (s embeddedConfigSink) ApplyRemoved(_ context.Context) error {
	f := s.f
	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}
	previousLLM := f.config.LLM
	previousEmbedded := f.config.EmbeddedLLM

	composite := embeddedCompositeModelID()
	migrated := f.config.LLM.DefaultModel
	if migrated == composite {
		migrated = firstNonEmbeddedModelID(f.config, composite)
	}
	autoUnload := f.config.EmbeddedLLM.AutoUnload
	tuning := f.config.EmbeddedLLM.Tuning
	f.config.EmbeddedLLM = config.EmbeddedLLMConfig{AutoUnload: autoUnload, Tuning: tuning}
	f.config.LLM.DefaultModel = migrated
	// contextWindow 0: the override of a removed model is dropped with its
	// provider record, and no other model's override is touched.
	f.config.SyncEmbeddedLLMProvider(0)

	if err := s.saveOrRollback(previousLLM, previousEmbedded); err != nil {
		return err
	}
	f.rebuildAfterEmbeddedConfigChange()
	return nil
}

// saveOrRollback persists the mutated config and restores the exact prior state
// when the write fails, so a failed save is indistinguishable from a rejected
// one. Called with configMu HELD; it releases the lock before returning, and
// emits config:updated only after a successful write.
func (s embeddedConfigSink) saveOrRollback(previousLLM config.LLMConfig, previousEmbedded config.EmbeddedLLMConfig) error {
	f := s.f
	if err := config.Save(f.config, f.configPath); err != nil {
		f.config.LLM = previousLLM
		f.config.EmbeddedLLM = previousEmbedded
		f.configMu.Unlock()
		return fmt.Errorf("failed to persist the embedded LLM state: %w", err)
	}
	f.configLoadErrors = nil
	f.configMu.Unlock()
	f.emitConfigUpdated()
	return nil
}

// embeddedLoaderRef is the lock-free Loader seam the router entry drives.
//
// It exists because of an ordering fact, not for convenience: the router is
// first built inside NewApplication — before this subsystem has been
// constructed — and every ToBuilderConfig call site runs while configMu is
// held, where st.mu must never be taken (the lock order is one-directional,
// (st.mu | st.infoMu) → configMu). A direct *Server reference would therefore
// either be nil at the first build, or require inverting the lock order and
// risk a deadlock.
//
// The indirection resolves the supervisor at CALL time instead of at build
// time, so the transport can be attached before the supervisor exists and still
// reach the real one later. Reads are a single atomic load: no mutex, no
// configMu re-entry, no change to the lock order.
type embeddedLoaderRef struct {
	f *FrontendAPI
}

// Load makes the model resident, constructing the subsystem first when a
// request arrives before any lifecycle hook built it. That construction is pure
// local work and takes configMu.RLock internally, which is safe here: a request
// goroutine holds no configMu, so neither lock-order rule nor self-deadlock
// applies.
func (r embeddedLoaderRef) Load(ctx context.Context) error {
	server := r.f.embedded.loader.Load()
	if server == nil {
		built, _, err := r.f.embeddedBuild()
		if err != nil {
			return err
		}
		server = built
	}
	if server == nil {
		return errors.New("the embedded LLM supervisor is unavailable")
	}
	return server.Load(ctx)
}

// MarkActivity restarts the auto-unload budget. A response that completes
// before the subsystem exists has no budget to restart, so a nil snapshot is a
// no-op rather than an error path.
func (r embeddedLoaderRef) MarkActivity() {
	if server := r.f.embedded.loader.Load(); server != nil {
		server.MarkActivity()
	}
}

// The ref satisfies the core seam; the assertion keeps the wiring honest if
// either side drifts.
var _ embeddedllm.Loader = embeddedLoaderRef{}

// applyEmbeddedLoader attaches the ensure-loaded transport seam to a freshly
// converted BuilderConfig, so the embedded provider's router entry starts a
// cold model before its first request goes out and restarts the idle budget on
// every completed response.
//
// MUST be called with configMu held (it reads f.config) and MUST NOT take st.mu
// or call embeddedBuild — see embeddedLoaderRef for why the loader resolves
// lazily instead.
//
// The gate is the persisted install state, not the presence of a supervisor: it
// is true from the config load onwards, so the very first router build already
// carries the seam. It is also what leaves a user's own unrelated provider that
// happens to be named "embedded" untouched while the local model is not
// installed.
//
// LoadWaitTimeout deliberately stays zero so core applies
// embeddedllm.DefaultLoadWaitTimeout. Deriving it here would need
// embeddedAutoUnloadPolicy, which takes configMu.RLock and would self-deadlock
// under the caller's lock.
func (f *FrontendAPI) applyEmbeddedLoader(bc *core.BuilderConfig) {
	if bc == nil || f.config == nil || !f.config.EmbeddedLLM.Installed {
		return
	}
	bc.EmbeddedLLM = core.BuilderEmbeddedLLMConfig{
		ProviderName: config.EmbeddedLLMProviderName,
		Loader:       embeddedLoaderRef{f: f},
	}
}

// syncEmbeddedBuilderSeam mirrors the persisted install state into the BUILDER's
// default seam, the one every router build falls back to when its own
// BuilderConfig carries no Loader (core.OrchestratorBuilder.SetEmbeddedLLM).
//
// applyEmbeddedLoader decorates one BuilderConfig and therefore only reaches the
// routers built from it. The per-session orchestrator is not one of them: the
// session factory in backend/application.go converts the live config directly,
// because it was closed over inside NewApplication — before any FrontendAPI, and
// so before any supervisor, existed. Without this default the session router
// carries no ensure-loaded transport, and a chat request to a cold model is
// dispatched straight to a loopback socket nothing is listening on: the exact
// failure the transport exists to prevent, on the one path that matters most.
//
// MUST NOT be called with configMu held (embeddedConfig takes it for reading).
func (f *FrontendAPI) syncEmbeddedBuilderSeam() {
	b := f.builder()
	if b == nil {
		return
	}
	var seam core.BuilderEmbeddedLLMConfig
	if f.embeddedConfig().Installed {
		seam = core.BuilderEmbeddedLLMConfig{
			ProviderName: config.EmbeddedLLMProviderName,
			Loader:       embeddedLoaderRef{f: f},
		}
	}
	b.SetEmbeddedLLM(seam)
}

// ensureEmbeddedReadyForLLMRequest blocks until the embedded model can answer,
// and returns immediately when the request will not be served by it.
//
// It exists for the one thing the ensure-loaded transport cannot do. The
// transport gates inside http.Client.Do, so it protects the wire — but a caller
// that has ALREADY armed a short request budget gets that budget eaten by the
// wait: time the supervisor legitimately needs to map gigabytes of weights
// counts against a call configured for 2 minutes. So the short-budget paths call
// this FIRST, and create their timeout context only afterwards, which is what
// makes the requirement hold: no LLM request starts before the model is loaded
// and ready, and the load never shortens the request it precedes.
//
// The load runs under the app context, not the caller's, bounded by the
// supervisor's own wait budget: an expired service budget must not abort a load
// that is legitimately in progress (the transport detaches for the same
// reason), and a caller that walks away must not be able to cancel it either.
// Concurrent callers coalesce on the supervisor's single-instance gate.
func (f *FrontendAPI) ensureEmbeddedReadyForLLMRequest(ctx context.Context) error {
	if !f.activeModelIsEmbedded() {
		return nil
	}
	if ctx == nil {
		ctx = f.ctx()
	}
	loadCtx, cancel := context.WithTimeout(ctx, embeddedllm.DefaultLoadWaitTimeout)
	defer cancel()
	if err := (embeddedLoaderRef{f: f}).Load(loadCtx); err != nil {
		return fmt.Errorf("the embedded model could not be loaded: %w", err)
	}
	return nil
}

// activeModelIsEmbedded reports whether the model a one-shot service request
// resolves to is the backend-owned embedded entry.
//
// Service calls run on the cached router, whose active model is the configured
// default (buildRouter applies llm.default_model via SetModel), so resolving the
// default here answers the same question the router will. The install gate comes
// first: with nothing installed there is no embedded entry to resolve to, and a
// user's own provider that happens to be named "embedded" must not be treated as
// the local model.
func (f *FrontendAPI) activeModelIsEmbedded() bool {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	if f.config == nil || !f.config.EmbeddedLLM.Installed {
		return false
	}
	provider, _, err := f.config.LLM.ResolveDefaultModelProvider()
	if err != nil {
		return false
	}
	return provider.Name == config.EmbeddedLLMProviderName
}

// toBuilderConfigLocked converts the live config and attaches the embedded
// loader seam. Callers MUST hold configMu (Lock or RLock) — exactly the
// precondition every ToBuilderConfig call site already satisfies. Use this
// instead of calling ToBuilderConfig directly on a FrontendAPI path, or the
// embedded entry silently loses its transport.
func (f *FrontendAPI) toBuilderConfigLocked() *core.BuilderConfig {
	bc := ToBuilderConfig(f.config, f.modelProfilesCatalog())
	f.applyEmbeddedLoader(bc)
	return bc
}

// rebuildRouterForEmbeddedTransport re-attaches the ensure-loaded transport to
// the live router. The router is first built inside NewApplication, which has no
// FrontendAPI and therefore no loader seam; this is the one place that closes
// the gap, and it runs during the startup restore — before backend:ready, so
// before any session can issue a request. It takes saveMu because
// rebuildAfterEmbeddedConfigChange is contracted to run under it (the config
// writers it serializes against hold saveMu across their own rebuild).
func (f *FrontendAPI) rebuildRouterForEmbeddedTransport() {
	f.saveMu.Lock()
	defer f.saveMu.Unlock()
	f.rebuildAfterEmbeddedConfigChange()
}

// rebuildAfterEmbeddedConfigChange rebuilds the judge and the LLM router so the
// provider set change takes effect for new sessions without a restart. Runs
// OUTSIDE the config write lock (readers stay responsive) but still under
// saveMu, and takes configMu.RLock across snapshot + rebuild exactly like
// UpdateLLMConfig, so a concurrent profile/model writer cannot be rolled back.
// A rebuild failure is logged, not returned: the install itself succeeded and
// the config on disk is authoritative — the next load picks it up.
//
// The builder-level seam is refreshed FIRST, and it is refreshed here rather
// than at each of the three lifecycle points because this is the one place all
// three already meet (startup restore, completed install, removal): it keeps the
// default every FUTURE per-session router is built with in step with the
// persisted install state, so a removal cannot leave a session router guarding a
// provider the user then creates themselves under the same name.
func (f *FrontendAPI) rebuildAfterEmbeddedConfigChange() {
	f.syncEmbeddedBuilderSeam()
	b := f.builder()
	if b == nil {
		return
	}
	f.configMu.RLock()
	fresh := f.toBuilderConfigLocked()
	b.RebuildJudge(fresh)
	err := b.RebuildRouter(fresh)
	f.configMu.RUnlock()
	if err != nil {
		f.log().Warn("failed to rebuild the LLM router after an embedded LLM config change", "error", err)
	}
}

// firstNonEmbeddedModelID picks the migration target for llm.default_model when
// the embedded model is removed. Empty when it was the only enabled model:
// ApplyDefaults fills the first available model on the next load, and a config
// with no provider at all is already invalid independently of this subsystem.
func firstNonEmbeddedModelID(cfg *config.Config, composite string) string {
	for _, id := range cfg.LLM.AllModelIDs() {
		if id != composite {
			return id
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Lifecycle surface (NOT part of the Wails RPC surface: methods on
// FrontendAPILifecycle are never bound — see frontend_api.go)
// ---------------------------------------------------------------------------

// InitEmbeddedLLM restores the embedded local-model state from manifest.json
// and emits the initial embedded_llm:state snapshot. Called from
// desktop/startup_phases.go on the startup path.
//
// It performs NO network I/O, NO hardware probe and never loads the model:
// startup must neither depend on the network nor block on a multi-gigabyte
// weight load. The whole call is a manifest read plus one event.
func (l *FrontendAPILifecycle) InitEmbeddedLLM() {
	l.f.initEmbeddedLLM()
}

// StopEmbeddedLLM stops the supervised llama-server if one is running,
// releasing its RAM/VRAM. Called from desktop Shutdown; idempotent and a no-op
// when the subsystem was never constructed.
func (l *FrontendAPILifecycle) StopEmbeddedLLM(ctx context.Context) error {
	return l.f.stopEmbeddedLLM(ctx)
}
