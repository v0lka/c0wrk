package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// The operator tuning surface of the embedded local model (see
// specs/domains/embedded-llm.md and
// specs/decisions/067-memory-aware-embedded-llm-provisioning.md).
//
// Ownership: this file owns the OVERRIDE, not the outcome — the memory-plan
// knobs under embedded_llm.tuning, their read/write RPCs, the on-demand device
// probe, and the launch-fingerprint bookkeeping that tells the UI whether a
// resident model was started with overrides the operator has since changed. It
// owns no policy of its own: the resolution that turns these knobs into a
// launch shape, and the guards that can refuse one, all live in
// core/embeddedllm. The wire shapes these functions return live in
// frontend_api_embedded_dto.go; the supervisor wiring and the operation gate
// they read through live in frontend_api_embedded.go.
//
// The conventions the surface follows:
//
//   - GetEmbeddedLLMTuning DOES return an error, unlike GetEmbeddedLLMStatus:
//     a getter that cannot read the truth must not fabricate one.
//   - every mutating method returns an error, and an error it returns is
//     actionable — it names the refused operation and the reason.
//   - a probe performs no network I/O beyond asking the already-provisioned
//     runtime binary, and it is bounded by embeddedProbeTimeout.

// The `reset` vocabulary: the embedded_llm.tuning YAML key of every
// UI-exposed knob, so a request names an override the way config.example.yaml
// and every validation error already name it. `context` and `offload` are
// included even though they are composite — one uniform clear mechanism for
// all twelve knobs beats two. The two YAML-only checkpoint-policy knobs
// (`ctx_checkpoints`, `cache_idle_slots`) are deliberately NOT part of this
// vocabulary: they have no tuning DTO field, no request spelling and no reset
// key, and are editable only through config.yaml (see
// specs/domains/embedded-llm.md).
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
// so two CLOSED-SET spellings that resolve to the same launch shape — an absent
// `kv_cache_type` and an explicit `auto`, an untrimmed spelling and a canonical
// one — are the same fingerprint and do not raise a spurious "reload required".
// The collapse covers only the closed-set knobs TuningConfig.ToTuning resolves
// through tuningChoice (`context.mode`, `offload.mode`, `kv_cache_type`,
// `packing`): the pointer knobs are cloned verbatim, so an explicit pin equal
// to the derived default (a `ctx_checkpoints: 32`, a `parallel: 1`) still
// fingerprints as a distinct operator statement. reload_required therefore errs
// toward suggesting a reload — a hint for the operator, never a gate.
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
// and is refused while any embedded operation is in flight, because a
// half-staged runtime tree (or one being deleted) has no trustworthy binary to
// ask.
func (f *FrontendAPI) ProbeEmbeddedLLMDevices() (EmbeddedLLMDevicesDTO, error) {
	server, _, err := f.embeddedBuild()
	if err != nil {
		return EmbeddedLLMDevicesDTO{}, err
	}
	if op := f.embeddedBusyOperation(); op != embeddedOpIdle {
		return EmbeddedLLMDevicesDTO{}, embeddedBusyRefusal(op, embeddedOpIdle, "probing the devices")
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
