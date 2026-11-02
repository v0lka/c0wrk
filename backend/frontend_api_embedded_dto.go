package backend

import (
	"slices"
	"strings"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// The wire shapes of the embedded local model RPC surface and the mappers
// that build them (specs/contracts/desktop-frontend.md,
// specs/domains/embedded-llm.md). This file owns ONLY boundary data — DTOs,
// their request counterparts, and the pure mappers; no policy, state or I/O.
//
// Conventions: fields are ADDITIVE (hand-written frontend guards check only
// the fields they know); a nullable knob stays a pointer ("unset — the
// planner decides" ≠ "explicitly set to the planner's own answer"); arrays
// are empty slices, never nil.

// EmbeddedLLMStatus is the GetEmbeddedLLMStatus payload: the supervision
// state plus the install record. Every field is always present (no
// omitempty) — the additivity/null rules are the file header's conventions.
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
	// Engine is the runtime family on disk: "" (a pre-engine install, llama),
	// "llama", or "mtplx"; the Settings label renders it first.
	Engine string `json:"engine"`
	// PythonVersion is the managed CPython under the mtplx engine ("" on the
	// llama family; keys are always on the wire — no omitempty here).
	PythonVersion string `json:"python_version"`
	// PackRevision is the pinned Hugging Face revision of the mtplx weight
	// pack ("" on the llama family).
	PackRevision string `json:"pack_revision"`
	// Packing is the ternary quantization on disk ("PQ2_0" | "PTQ1_0"; empty
	// under the mtplx engine, whose pack ships one quantization).
	Packing string `json:"packing"`
	// Backend is the accelerator the runtime was provisioned for.
	Backend string `json:"backend"`
	// Port is the persisted loopback port (0 when nothing is installed).
	Port int `json:"port"`
	// ContextSize is the last known effective context (0 when nothing is
	// installed): the planner's install-time figure corrected by each load's
	// /props readback; the tier-1 llm.models override carries the same value.
	ContextSize int `json:"context_size"`
	// AutoUnloadEnabled is the resolved idle-timer master switch.
	AutoUnloadEnabled bool `json:"auto_unload_enabled"`
	// AutoUnloadMinutes is the resolved idle budget in minutes.
	AutoUnloadMinutes int `json:"auto_unload_minutes"`
	// IdleRemainingSeconds is the idle budget left, 0 when no timer is armed.
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
	// PackingReason says why the installed packing is what it is ("default"
	// or a typed downgrade cause). Empty means unknown, not "default".
	PackingReason string `json:"packing_reason"`
	// GPUFamily is the accelerator generation the install was planned for,
	// empty when no device probe answered.
	GPUFamily string `json:"gpu_family"`
	// Guards are the compatibility decisions this install was planned under
	// (see EmbeddedLLMGuard); empty = no documented failure covers it.
	Guards []EmbeddedLLMGuard `json:"guards"`
	// Devices is the accelerator inventory of the RECORDED topology, not a
	// live measurement; empty + empty TopologyProbedAt = never probed.
	Devices []EmbeddedLLMDevice `json:"devices"`
	// Unified: device pool and host RAM are one memory — never add the two
	// budgets. false on an unprobed topology means "unknown", not discrete.
	Unified bool `json:"unified"`
	// HostRAMGiB is the total system RAM of the recorded topology, 0 when no
	// probe ever answered.
	HostRAMGiB float64 `json:"host_ram_gib"`
	// DeviceBudgetMiB / HostBudgetMiB are the gated budgets; 0 on an
	// unprobed topology means UNREADABLE, not "no memory".
	DeviceBudgetMiB int64 `json:"device_budget_mib"`
	HostBudgetMiB   int64 `json:"host_budget_mib"`
	// TopologyProbedAt stamps the topology; empty means no probe ever
	// answered (the "unknown" marker).
	TopologyProbedAt string `json:"topology_probed_at"`
	// Plan is the launch shape LAST APPLIED — the planner's OUTCOME over the
	// tuning (the tuning itself is GetEmbeddedLLMTuning); read Plan.Recorded
	// first, false = every field unset, not a decision.
	Plan EmbeddedLLMPlan `json:"plan"`
	// ReloadRequired: the resident model predates a tuning change — every
	// knob is a launch flag, so only the next load picks it up.
	ReloadRequired bool `json:"reload_required"`
	// Pid is the OS process id of the supervised server, 0 when no process is
	// running.
	Pid int `json:"pid"`
	// FitWarning is the fork's fit failure scanned from the last failed
	// launch; a launch that becomes ready clears it.
	FitWarning string `json:"fit_warning,omitempty"`
	// Error is the supervisor's message while State is "error" — install
	// failures deliberately live in InstallError instead, so a retrying
	// background download never paints the status bar.
	Error string `json:"error"`
	// InstallError is the last FAILED install's cause (fatal failures only —
	// resumable transfers retry silently inside core).
	InstallError string `json:"install_error"`
	// Available reports whether the subsystem could be constructed at all
	// (false only when the agent directory is unset, i.e. before startup).
	Available bool `json:"available"`
	// LeftoverRuntime / LeftoverWeights / LeftoverProjection: artifacts on
	// disk right now — what a scoped removal left behind (re-verifiable
	// cache or reclaimable residue). All false when the subsystem is absent.
	LeftoverRuntime    bool `json:"leftover_runtime"`
	LeftoverWeights    bool `json:"leftover_weights"`
	LeftoverProjection bool `json:"leftover_projection"`
}

// EmbeddedLLMGuard is the frontend-facing shape of one
// core/embeddedllm.GuardDecision (specs/domains/embedded-llm.md): core enum
// values verbatim, so the UI never needs a second vocabulary.
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

// embeddedGuardIDs renders the recorded guard decisions for one log line —
// the log-side half of the record the status DTO carries.
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

// embeddedGuardsDTO maps persisted guard decisions onto the status DTO.
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

// EmbeddedLLMStateData is the embedded_llm:state payload — deliberately
// narrower than EmbeddedLLMStatus: just what a status indicator needs.
type EmbeddedLLMStateData struct {
	Installed         bool   `json:"installed"`
	Loading           bool   `json:"loading"`
	Loaded            bool   `json:"loaded"`
	Packing           string `json:"packing"`
	Backend           string `json:"backend"`
	Port              int    `json:"port"`
	ContextSize       int    `json:"context_size"`
	AutoUnloadMinutes int    `json:"auto_unload_minutes"`
	// Error / InstallError carry the same split as EmbeddedLLMStatus: the
	// indicator shows the first, never the second.
	Error        string `json:"error"`
	InstallError string `json:"install_error"`
}

// EmbeddedLLMProgressData is the embedded_llm:install_progress payload: one
// component's one stage (specs/contracts/event-catalog.md).
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

// EmbeddedLLMDevice is one accelerator as the provisioned runtime reported it
// (the frontend-facing shape of core/embeddedllm.DeviceMemory). FreeMiB is an
// informational snapshot — fit budgets derive from TotalMiB so a plan stays
// reproducible regardless of what else was running at probe time.
type EmbeddedLLMDevice struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	TotalMiB    int64  `json:"total_mib"`
	FreeMiB     int64  `json:"free_mib"`
}

// EmbeddedLLMPlan is the effective launch shape — the frontend-facing view
// of core/embeddedllm.MemoryPlan (ADR-067): a value with a Recorded flag, so
// a renderer has one code path.
type EmbeddedLLMPlan struct {
	// Recorded: a plan exists at all. false = a pre-field manifest; every
	// other field is at its UNSET value (see embeddedPlanDTO) = unknown.
	Recorded bool `json:"recorded"`
	// Packing is the weights quantization the plan was projected for
	// ("PQ2_0" | "PTQ1_0").
	Packing string `json:"packing"`
	// KVType is the resolved `-ctk`/`-ctv` precision; never empty on a
	// recorded plan (the escalation lives in Notes).
	KVType string `json:"kv_type"`
	// ContextSize is `-c`; zero on a fit-sized plan means the runtime's fit
	// pass chooses the window (floor FitMinContext), not "no context".
	ContextSize int `json:"context_size"`
	// Fit: the runtime's `--fit` pass sizes layers and context (Layers unset,
	// ContextSize 0) — `-fit on` plus an explicit `-ngl` abort the launch.
	Fit bool `json:"fit"`
	// FitArg is the literal `-fit` value, echoed as the runtime receives it.
	FitArg string `json:"fit_arg"`
	// FitTargetMiB is `-fitt`, the per-device margin; 0 omits the flag
	// (runtime default 1024 MiB). Meaningful only when Fit is true.
	FitTargetMiB int `json:"fit_target_mib"`
	// FitMinContext is `-fitc`, the floor fit is held to. Only meaningful when
	// Fit is true.
	FitMinContext int `json:"fit_min_context"`
	// OffloadMode renders the EFFECTIVE `-ngl` decision: "auto" | "cpu" |
	// "layers"; derived from Fit + Layers so it always agrees with the number
	// beside it (why `all` has no label: embeddedPlanDTO).
	OffloadMode string `json:"offload_mode"`
	// Layers is the resolved `-ngl`; -1 = flag omitted (fit chooses) — a
	// different fact from 0 ("nothing offloaded").
	Layers int `json:"layers"`
	// KVOffload false means `-nkvo`: the KV cache stays in system RAM.
	KVOffload bool `json:"kv_offload"`
	// MMProjOffload false means `--no-mmproj-offload`: the vision projector's
	// reserve stays in system RAM.
	MMProjOffload bool `json:"mmproj_offload"`
	// Parallel is `-np`, the slot count.
	Parallel int `json:"parallel"`
	// CacheRAMMiB is `-cram`; -1 = flag omitted, 0 = a real value that
	// disables the cache.
	CacheRAMMiB int `json:"cache_ram_mib"`
	// GPUFamily is the accelerator generation the plan was classified as, empty
	// when no device probe answered. It is what priced the split allowance in
	// the two budgets below.
	GPUFamily string `json:"gpu_family"`
	// DeviceBudgetMiB / HostBudgetMiB are the gated budgets; on a UNIFIED
	// machine they are one pool seen twice — never add them.
	DeviceBudgetMiB int64 `json:"device_budget_mib"`
	HostBudgetMiB   int64 `json:"host_budget_mib"`
	// ExpectedDeviceMiB / ExpectedHostMiB are the projected footprints
	// including the vision reserve; on a PARTIAL offload both are bounds
	// (full-offload / CPU-only), and Notes says so.
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
	// Devices is the inventory in runtime order; an EMPTY array is a real
	// CPU-only answer — a failed probe is an error, never a DTO.
	Devices []EmbeddedLLMDevice `json:"devices"`
	// Unified is DETECTED, never assumed from the OS; inconclusive evidence
	// falls back to true (treating discrete as unified only shrinks budget).
	Unified bool `json:"unified"`
	// HostRAMGiB is total system RAM, from the same probe as the devices.
	HostRAMGiB float64 `json:"host_ram_gib"`
	// DeviceBudgetMiB / HostBudgetMiB are the spendable budgets after the
	// accelerator margin and OS reserve (never summed when unified).
	DeviceBudgetMiB int64 `json:"device_budget_mib"`
	HostBudgetMiB   int64 `json:"host_budget_mib"`
	// ProbedAt stamps the snapshot (RFC 3339 UTC); topology facts age, hence
	// the explicit re-probe.
	ProbedAt string `json:"probed_at"`
}

// EmbeddedLLMContextTuningDTO is the `tuning.context` knob, mirroring
// config.EmbeddedLLMContextConfig; nil is load-bearing ("never written" ≠ an
// explicit "auto"), which is what lets the DTO round-trip the section.
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

// EmbeddedLLMTuningDTO is the GetEmbeddedLLMTuning payload: the persisted
// embedded_llm.tuning overrides for the twelve UI-exposed knobs
// (specs/domains/embedded-llm.md); the two YAML-only checkpoint knobs are
// config.yaml-only. EVERY field is nullable — nil = "unset — the planner
// decides", distinct from an explicit "auto"; this is the OVERRIDE, the
// outcome is EmbeddedLLMStatus.Plan. Legal spellings live only in
// config.tuningChoice; a rejected SetEmbeddedLLMTuning lists them.
type EmbeddedLLMTuningDTO struct {
	// Context is the `-c` knob: nil mode = unset, "auto" = the planner sizes it,
	// "exact" = Tokens pins it.
	Context EmbeddedLLMContextTuningDTO `json:"context"`
	// KVCacheType overrides BOTH `-ctk`/`-ctv` (a mixed pair silently drops
	// CPU flash attention); "auto" escalates f16→q8_0→q4_0 until it fits.
	KVCacheType *string `json:"kv_cache_type"`
	// Offload is the `-ngl` knob: nil mode = unset, "auto" = fit sizes it,
	// "all"/"cpu"/"layers" pin it.
	Offload EmbeddedLLMOffloadTuningDTO `json:"offload"`
	// Fit overrides `-fit`; nil defers to the exclusivity rule, an explicit
	// true beside an explicit offload loses to it (recorded in Notes).
	Fit *bool `json:"fit"`
	// FitTargetMiB overrides `-fitt`; nil keeps the runtime's 1024 MiB, an
	// explicit 0 also omits the flag.
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
	// Packing overrides the weights quantization; nil/"auto" keeps the
	// probe-resolved packing, anything else must be registry-pinned AND
	// measured by the memory model.
	Packing *string `json:"packing"`
	// Parallel overrides `-np`; nil means 1 (one agent loop, one request at
	// a time).
	Parallel *int `json:"parallel"`
	// CacheRAMMiB overrides `-cram`; an explicit 0 disables the cache
	// verbatim — a legitimate choice.
	CacheRAMMiB *int `json:"cache_ram_mib"`
	// HostReserveGiB overrides the RAM kept out of the host budget. nil keeps
	// the topology's own derivation. It is a PLANNER-side budget knob, not a
	// runtime flag -- the pinned fork has no `--host-reserve`.
	HostReserveGiB *float64 `json:"host_reserve_gib"`
}

// EmbeddedLLMContextTuningRequest is the `context` knob of a tuning patch:
// presence in the request says "replace this knob", wholesale — no silent
// half-updates.
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

// EmbeddedLLMTuningRequest is a PARTIAL SetEmbeddedLLMTuning patch with three
// states per knob: field nil keeps the stored value; field present stores it
// verbatim (an explicit "auto" is a real value, not unset); a knob named in
// Reset clears back to unset. Reset exists because nil already means "absent
// from this request" and cannot also mean "clear" — bool/int knobs have no
// "auto" spelling. Resetting and setting the same knob is refused.
type EmbeddedLLMTuningRequest struct {
	// Reset names knobs to clear (embedded_llm.tuning YAML keys; an unknown
	// key is refused without a write).
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

// embeddedDevicesDTO maps a measured topology onto the wire shape.
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

// embeddedPlanDTO renders a recorded launch shape. A nil plan (a pre-field
// manifest) yields Recorded=false and the UNSET sentinels below — "unknown",
// never a plan of zeros a renderer would read as a decision.
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

	// OffloadMode is DERIVED, not copied, so the badge always agrees with the
	// number. `all` deliberately has no label: core spells it as its own `-ngl`
	// ceiling, a sentinel this layer must not transcribe — an every-layer
	// offload renders as `layers` + count, the flag the runtime receives. The
	// operator's own `all` choice is read from GetEmbeddedLLMTuning.
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

// embeddedTuningDTO mirrors the persisted override surface across the twelve
// UI-exposed knobs, copying the pointers so the DTO never aliases the live
// config across a reload.
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

// copy*Ptr clone one nullable knob so the DTO never aliases the live config.
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
