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

// The operator tuning surface of the embedded local model — the OVERRIDE, not
// the outcome. Policy lives in core/embeddedllm, wire shapes in
// frontend_api_embedded_dto.go, supervisor wiring in frontend_api_embedded.go.
// See specs/domains/embedded-llm.md and
// specs/decisions/067-memory-aware-embedded-llm-provisioning.md.

// The `reset` vocabulary: the embedded_llm.tuning YAML key of every
// UI-exposed knob. The two YAML-only checkpoint-policy knobs
// (`ctx_checkpoints`, `cache_idle_slots`) are deliberately absent — no DTO
// field, editable only through config.yaml.
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

// embeddedTuningKnobs is every legal `reset` key in config.TuningConfig's
// declaration order — the list behind the unknown-key refusal and its message.
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
// returns it WITHOUT writing — validation is the caller's next step. A knob
// named in both Reset and the setters is refused, not resolved by precedence:
// "clear this" and "set this" in one request is a caller bug.
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

	// The knobs this request also SETS, checked against the reset list before
	// anything is folded so the refusal names every contradiction at once.
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

// embeddedTuningFingerprint renders a tuning section as a comparable string
// over the TRANSLATED planner vocabulary, so equivalent closed-set spellings
// (absent vs explicit `auto`, untrimmed vs canonical) do not raise a spurious
// reload_required; pointer knobs are cloned verbatim, so an explicit pin still
// reads as a distinct operator statement. In-memory comparison key only. See
// specs/domains/embedded-llm.md.
func (f *FrontendAPI) embeddedTuningFingerprint() string {
	tuning := f.embeddedTuning()
	encoded, err := json.Marshal(tuning)
	if err != nil {
		// Unreachable for a struct of scalars, pointers and string slices; the
		// stable fallback keeps the comparison total on the status path.
		f.log().Debug("the embedded LLM tuning fingerprint could not be rendered", "error", err)
		return "unrenderable"
	}
	return string(encoded)
}

// noteEmbeddedLaunchTuning records which overrides the process behind a
// supervision transition was launched with. Keyed off `loading`: launchSpec —
// which reads the tuning through the Server.Tuning seam — runs BEFORE that
// transition inside Server.Load, so the fingerprint taken here is the one the
// spawned argv was built from. Every non-resident state clears the record.
func (f *FrontendAPI) noteEmbeddedLaunchTuning(state embeddedllm.State) {
	st := &f.embedded
	if state != embeddedllm.StateLoading && state != embeddedllm.StateLoaded {
		st.setLaunchedTuning("", false)
		return
	}
	if state != embeddedllm.StateLoading {
		// `loaded` follows `loading` in the same run: keep the fingerprint that
		// run recorded rather than re-reading a mid-load config edit.
		return
	}
	// Config first, then the record: embeddedTuningFingerprint takes configMu
	// internally, and infoMu must never be held across it — the lock order is
	// one-directional, (st.mu | st.infoMu) → configMu.
	st.setLaunchedTuning(f.embeddedTuningFingerprint(), true)
}

// embeddedTuningReloadRequired reports whether a resident model was launched
// with overrides the operator has since changed (the persisted tuning takes
// effect on the NEXT load). A missing fingerprint answers false — "unknown"
// must not surface as a demand to reload. A hint for the operator, never a
// gate: the launch always reads the live config.
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

// GetEmbeddedLLMTuning returns the persisted embedded_llm.tuning overrides,
// nil meaning "unset — the planner decides". It reports an error before
// startup instead of an all-nil DTO (see specs/domains/embedded-llm.md): that
// answer is indistinguishable from "nothing overridden" and an editor would
// wipe the tuning on Save. No I/O, no probe, no translation — the resolved
// shape is EmbeddedLLMStatus.Plan.
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

// SetEmbeddedLLMTuning writes a PARTIAL update of embedded_llm.tuning: nil
// keeps, present replaces verbatim, Reset clears (EmbeddedLLMTuningRequest).
// An invalid patch is refused without a write — the fold-validate-write
// sequence runs under configMu through the same ToTuning translation a config
// load runs. THE RESIDENT MODEL IS NOT RESTARTED, BY DECISION: every knob is a
// launch flag, so the change lands on the next load — reload_required flags
// it, Unload/Load applies it now. Tuning is an operator SETTING, not install
// state. See specs/domains/embedded-llm.md.
func (f *FrontendAPI) SetEmbeddedLLMTuning(req EmbeddedLLMTuningRequest) error {
	server, _, err := f.embeddedBuild()
	if err != nil {
		// The subsystem being unavailable is not a reason to lose the tuning:
		// persist it and report the supervisor problem through the missing
		// state event (mirrors SetEmbeddedLLMAutoUnload).
		f.log().Warn("embedded LLM subsystem unavailable; persisting the tuning only", "error", err)
		server = nil
	}

	// saveMu FIRST (the documented saveMu → configMu order), serializing this
	// write against the install/remove sink writes and every whole-config save.
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
	// Translate BEFORE writing — the same check validate() runs on load — so a
	// patch that would make config.yaml unloadable is refused while intact.
	tuning, terr := next.ToTuning()
	if terr != nil {
		f.configMu.Unlock()
		return fmt.Errorf("invalid embedded LLM tuning: %w", terr)
	}
	// The OTHER half of the load-time check, against the PERSISTED engine: an
	// explicit mtplx engine cannot carry llama-only tuning (the load validator
	// refuses the pair), and the message is its verbatim so both surfaces speak
	// with one voice.
	if embeddedllm.NormalizeEngine(embeddedllm.Engine(f.config.EmbeddedLLM.Engine)) == embeddedllm.EngineMTPLX {
		if forbidden := embeddedllm.ForbiddenLlamaTuning(tuning); len(forbidden) > 0 {
			f.configMu.Unlock()
			return fmt.Errorf(
				"embedded_llm.tuning.%s does not apply to embedded_llm.engine: mtplx (no offload, no fit pass, no KV or packing choice); remove the key or set engine: llama",
				forbidden[0],
			)
		}
	}
	if reflect.DeepEqual(next, stored) {
		// Nothing moved: no rewrite, no config:updated, no router rebuild.
		f.configMu.Unlock()
		return nil
	}

	previousLLM := f.config.LLM
	previousEmbedded := f.config.EmbeddedLLM
	f.config.EmbeddedLLM.Tuning = next
	// saveOrRollback is called with configMu HELD and releases it: on success it
	// emits config:updated, on failure it restores both sections.
	if err := sink.saveOrRollback(previousLLM, previousEmbedded); err != nil {
		return err
	}

	f.rebuildAfterEmbeddedConfigChange()
	if server != nil {
		// One explicit snapshot so the UI re-reads status and picks up a
		// reload_required this write may have just set (the store invalidates
		// and re-reads; an event is enough).
		f.emitEmbeddedStateFrom(server)
	}
	return nil
}

// ProbeEmbeddedLLMDevices measures THIS machine's accelerator memory on
// demand: EmbeddedLLMStatus's topology is a recorded, staleable snapshot and
// core's re-measure is fail-soft and invisible, so an operator diagnosing a
// plan asks here — the one embedded-LLM read allowed to be slow and to fail,
// turning core's silent probe into an actionable error. Nothing persisted or
// re-planned; requires an installed runtime; refused while an embedded
// operation is in flight. See specs/domains/embedded-llm.md.
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
		// the probe; this deliberately does not guess which failure it was.
		logger.Warn("the embedded LLM device probe did not answer", "binary", binary)
		return EmbeddedLLMDevicesDTO{}, errors.New(
			"the device probe did not answer: the provisioned runtime reported no recognizable accelerator inventory (see the debug log for the cause)")
	}

	logger.Debug("embedded LLM device probe answered on demand",
		"devices", len(topology.Devices), "unified", topology.Unified,
		"device_budget_mib", topology.DeviceBudgetMiB(), "host_budget_mib", topology.HostBudgetMiB())
	return embeddedDevicesDTO(topology), nil
}
