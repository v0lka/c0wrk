package backend

import (
	"slices"
	"strings"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// The wire shapes of the embedded local model RPC surface and the mappers that
// build them (see specs/contracts/desktop-frontend.md and
// specs/domains/embedded-llm.md).
//
// Ownership: this file owns ONLY boundary data — every DTO that crosses the
// Wails binding, its request counterpart, and the pure functions that map a
// core or config value onto it. It owns no policy, no state and no I/O. The
// RPCs that return these shapes live in frontend_api_embedded.go, and the
// tuning patch fold that consumes the request shapes lives in
// frontend_api_embedded_tuning.go.
//
// The conventions the shapes follow:
//
//   - fields are ADDITIVE at this boundary: the hand-written frontend guards
//     check the presence and type of the fields THEY know, so a payload from a
//     newer backend still validates and a renderer that has not caught up
//     simply ignores what it does not read.
//   - a nullable knob stays nullable (a pointer), because "unset — the planner
//     decides" and "explicitly set to the planner's own answer" are different
//     operator statements and must not collapse into one.
//   - every array is an empty slice rather than nil, so there is no null to
//     distinguish from an empty one anywhere in the payload.

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
	// frontend/src/api/embedded.ts validates and renders these three fields
	// (EmbeddedLLMInstallRecord).
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
	// Error is a human-readable cause of the SUPERVISION state: the
	// supervisor's message while State is "error" (a launch that failed, a
	// resident process that died). Empty otherwise — an install failure is
	// deliberately NOT folded in here: it lives in InstallError, because the
	// status-bar indicator renders this field while install errors are a
	// Settings-only surface (a background download that is still retrying
	// silently must not paint the bar, and a fatal one belongs to the Settings
	// error line).
	Error string `json:"error"`
	// InstallError is the operator-friendly cause of the last FAILED install
	// run (fatal download failures only — a resumable transfer failure is
	// retried silently inside core and never reaches this field). Empty when
	// no install has failed since the last successful or cancelled run.
	InstallError string `json:"install_error"`
	// Available reports whether the subsystem could be constructed at all
	// (false only when the agent directory is unset, i.e. before startup).
	Available bool `json:"available"`
	// LeftoverRuntime / LeftoverWeights / LeftoverProjection report which
	// embedded-LLM artifacts are on disk RIGHT NOW: any "llama-*" runtime tree
	// (or the archive staging area), a pinned model GGUF, the vision projector
	// GGUF. While Installed is true these are simply the install's own bytes;
	// their purpose is the NOT-installed state, where they describe what a
	// scoped removal left behind — a cache the next install re-verifies without
	// re-downloading, or residue a further removal can reclaim. Computed with a
	// cheap existence scan (directory listing + per-file stats, never a walk
	// and never a hash); all three are false when the subsystem could not be
	// constructed.
	LeftoverRuntime    bool `json:"leftover_runtime"`
	LeftoverWeights    bool `json:"leftover_weights"`
	LeftoverProjection bool `json:"leftover_projection"`
}

// EmbeddedLLMGuard is the frontend-facing shape of one
// core/embeddedllm.GuardDecision: a backend compatibility decision derived from
// the pinned model's KNOWN_ISSUES, recorded at install time and replayed here so
// a degraded install is visible in Settings rather than silent.
//
// The string fields are the core enum values verbatim (snake_case), so the UI can
// switch on them without this layer inventing a second vocabulary. The
// frontend mirror in frontend/src/api/embedded.ts (EmbeddedLLMGuard) validates
// every field, including both substitutes.
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
	// Always present on the wire (empty string, never omitted) — the frontend
	// mirror validates both substitutes as plain strings.
	Backend string `json:"backend"`
	// Packing is the substituted packing, empty unless Action is prefer_packing.
	// Always present on the wire (empty string, never omitted) — the frontend
	// mirror validates both substitutes as plain strings.
	Packing string `json:"packing"`
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
	// Error is the supervisor's own message (state "error" only); InstallError
	// is the last failed install run — the same split the status DTO carries,
	// so the status-bar indicator can show the first and never the second.
	Error        string `json:"error"`
	InstallError string `json:"install_error"`
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
	// existed — and every other field below is then at its UNSET value, NOT a
	// decision: the zero value for most, with Layers and CacheRAMMiB at the −1
	// "flag omitted" sentinel and OffloadMode at "auto" (see embeddedPlanDTO).
	// A reader must treat false as unknown.
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
// persisted memory-plan overrides (embedded_llm.tuning), field for field for
// the twelve UI-exposed knobs. The two YAML-only checkpoint-policy knobs
// (`ctx_checkpoints`, `cache_idle_slots`) are deliberately absent here, from
// EmbeddedLLMTuningRequest and from the reset vocabulary — they are editable
// only through config.yaml and observable only through the launch command
// line (see specs/domains/embedded-llm.md).
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
// field at its UNSET value — Go's zero for most, plus the three explicit
// sentinels below (Layers and CacheRAMMiB at −1, OffloadMode at "auto") — which
// is the DTO's "unknown", never a plan of zeros that a renderer would read as
// a decision to run nothing offloaded with no context.
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

// embeddedTuningDTO mirrors the persisted override surface field for field
// across the twelve UI-exposed knobs; the two YAML-only checkpoint-policy
// knobs have no DTO representation by design (see EmbeddedLLMTuningDTO). The
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
