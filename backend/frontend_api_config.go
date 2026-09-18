package backend

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/llmtls"
	"github.com/v0lka/c0wrk/core/modelprofiles"
	"github.com/v0lka/c0wrk/core/proxy"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// maskedAPIKey is the placeholder returned for configured API keys in the UI.
const maskedAPIKey = "***configured***"

// GetConfig returns the current configuration (sanitized, no raw API keys).
func (f *FrontendAPI) GetConfig() ConfigResponse {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		return ConfigResponse{Loaded: false}
	}

	resp := ConfigResponse{
		Loaded:       true,
		LogLevel:     f.config.LogLevel,
		ConfigErrors: nonNilStringSlice(f.configLoadErrors),
		LLM:          f.buildLLMResponse(),
		Search: ConfigSearchResp{
			Provider: f.config.Search.Provider,
			APIKey:   maskAPIKey(f.config.Search.APIKey),
		},
		VectorIndex: VectorIndexSettingsResponse{
			ExecutionProvider: f.config.VectorIndex.ExecutionProvider,
			DeviceID:          f.config.VectorIndex.DeviceID,
		},
		Proxy: ProxySettingsResponse{
			Enabled:    f.config.Proxy.Enabled,
			URL:        proxy.MaskURL(f.config.Proxy.URL),
			BypassList: nonNilStringSlice(f.config.Proxy.BypassList),
			TLSCertDir: f.config.Proxy.TLSCertDir,
		},
		Experimental: ExperimentalSettingsResponse{
			Enabled: f.config.Experimental.Enabled,
		},
		// The EFFECTIVE Model Profiles gate (the master toggle resolved against
		// the active profile), precomputed by
		// refreshModelProfilesGateLocked so this read path performs NO disk I/O — see the
		// GUARANTEE on collectAllModels.
		ModelProfiles: f.modelProfilesGateResp,
	}

	// Populate AllModels: flat list of all enabled models.
	// Always build from the per-provider config lists so the frontend sees
	// configured models immediately, even before the async ModelRegistry
	// finishes initializing. When the registry is ready, enrich with family
	// and reasoning metadata.
	b := f.builder()
	var reg *llm.ModelRegistry
	if b != nil {
		reg = b.ModelRegistry()
	}
	resp.LLM.AllModels = f.collectAllModels(reg)
	resp.LLM.ModelsReady = reg != nil

	return resp
}

// HasDefaultModel reports whether a default LLM model is configured. It is a
// cheap probe for UI flows (e.g. the settings close check) that only need this
// single fact and must not pay for a full GetConfig response.
func (f *FrontendAPI) HasDefaultModel() bool {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		return false
	}

	return f.config.LLM.DefaultModel != ""
}

// experimentalFeaturesEnabled reports whether experimental features are
// currently enabled. It returns false when the config is not yet initialized
// (fail-closed: gated features stay hidden/off).
func (f *FrontendAPI) experimentalFeaturesEnabled() bool {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	return f.config != nil && f.config.Experimental.Enabled
}

// buildLLMResponse constructs the sanitized ConfigLLMResponse from config.
func (f *FrontendAPI) buildLLMResponse() ConfigLLMResponse {
	resp := ConfigLLMResponse{
		DefaultModel: f.config.LLM.DefaultModel,
		Anthropic: ConfigProviderFull{
			APIKey: maskAPIKey(f.config.LLM.Anthropic.APIKey),
			Models: f.config.LLM.Anthropic.Models,
		},
		ChatGPT: ConfigProviderFull{
			APIKey: maskAPIKey(f.config.LLM.ChatGPT.APIKey),
			Models: f.config.LLM.ChatGPT.Models,
		},
		OpenAICompatible:    make(map[string]ConfigProviderFull, len(f.config.LLM.OpenAICompatible)),
		AnthropicCompatible: make(map[string]ConfigProviderFull, len(f.config.LLM.AnthropicCompatible)),
	}
	for name, cfg := range f.config.LLM.OpenAICompatible {
		resp.OpenAICompatible[name] = ConfigProviderFull{
			APIKey:         maskAPIKey(cfg.APIKey),
			BaseURL:        cfg.BaseURL,
			Models:         cfg.Models,
			TLSFingerprint: cfg.TLSFingerprint,
		}
	}
	for name, cfg := range f.config.LLM.AnthropicCompatible {
		resp.AnthropicCompatible[name] = ConfigProviderFull{
			APIKey:         maskAPIKey(cfg.APIKey),
			BaseURL:        cfg.BaseURL,
			Models:         cfg.Models,
			TLSFingerprint: cfg.TLSFingerprint,
		}
	}
	return resp
}

// collectAllModels iterates all enabled provider models and builds ModelInfo
// entries. When reg is non-nil, family and reasoning metadata are resolved
// from the registry. When reg is nil (registry not yet initialized), models
// are returned without metadata so the frontend still sees configured models.
//
// GUARANTEE: collectAllModels is network-free — it resolves every model via
// ModelRegistry.ResolveLocal, which serves overrides, built-ins, fuzzy
// matches, and cached entries purely from memory and returns fallback
// defaults for unknown models. No HTTP probes, no registered sources, no
// blocking I/O. GetConfig must remain a pure in-memory read: it runs on every
// settings open, and a model list containing an unknown model must not stall
// the UI behind a network timeout. The only disk-backed datum GetConfig reports
// — the effective Model Profiles gate (`ConfigResponse.model_profiles`) — is precomputed by
// refreshModelProfilesGateLocked at construction and on each Model Profiles mutation
// (under configMu), so the read path never reads the profile catalog.
//
// Entries are keyed by composite (provider, model) so that two providers
// exposing the same bare model name both appear — the frontend uses the
// composite "provider/name" value to select a specific provider while
// displaying the bare model name.
func (f *FrontendAPI) collectAllModels(reg *llm.ModelRegistry) []ModelInfo {
	// GetAllProviderConfigs returns providers in deterministic order
	// (anthropic, chatgpt, then sorted openai_compatible, then sorted anthropic_compatible).
	providers := f.config.LLM.GetAllProviderConfigs()

	seen := make(map[string]bool) // dedupe by composite "provider/model"
	var result []ModelInfo
	for _, p := range providers {
		for _, modelName := range p.Models {
			compositeID := llm.CompositeModelID(p.Name, modelName)
			if seen[compositeID] {
				continue
			}
			seen[compositeID] = true

			var family string
			var vision bool
			if reg != nil {
				meta, _ := reg.ResolveLocal(modelName)
				family = meta.Family
				// ResolveLocal output is enriched (non-nil capabilities), but
				// guard anyway: a raw/partial record must not panic the listing.
				vision = meta.Capabilities != nil && meta.Capabilities.Attachment
			}

			info := ModelInfo{
				Name:     modelName,
				Provider: p.Name,
				Family:   family,
				Vision:   vision,
			}

			if family != "" {
				if opts, def, ok := llm.ModelReasoningOptions(family, modelName); ok {
					info.Reasoning = &ReasoningInfo{
						Options: opts,
						Default: def,
					}
				}
			}

			result = append(result, info)
		}
	}
	return result
}

// UpdateLLMConfig updates the full LLM configuration atomically:
// default model, each provider's models list, and API keys.
//
// Locking layout: the whole update runs under saveMu so debounced saves apply
// strictly in submission order (mutation → persist → rebuild never interleave
// between two calls). configMu protects candidate validation, the atomic YAML
// commit/rollback, and the capture of the committed default. The expensive
// follow-up work (No-Project provisioning) runs with configMu released, and
// the judge/router rebuilds run under a shared configMu.RLock with a freshly
// re-snapshotted config: RLock readers are never convoyed behind a rebuild,
// yet writers are excluded between snapshot and rebuild so the router can
// never be rolled back to a snapshot that predates a concurrent config
// writer's changes.
func (f *FrontendAPI) UpdateLLMConfig(req LLMFullConfigRequest) error {
	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}

	// Build the proposed LLM state separately. A rejected request must not
	// leak partial provider mutations to GetConfig, YAML, or the router.
	previous := f.config.LLM
	candidate := previous

	// An empty default_model means "leave it unchanged": debounced partial
	// updates use that sentinel while the initial setup has no default yet.
	if req.DefaultModel != "" {
		candidate.DefaultModel = req.DefaultModel
	}

	if req.Anthropic != nil {
		if req.Anthropic.Models != nil {
			candidate.Anthropic.Models = req.Anthropic.Models
		}
		if req.Anthropic.APIKey != "" && req.Anthropic.APIKey != maskedAPIKey {
			candidate.Anthropic.APIKey = req.Anthropic.APIKey
		}
	}
	if req.OpenAICompatible != nil {
		newMap := make(map[string]config.OpenAICompatibleConfig, len(req.OpenAICompatible))
		for name, ocReq := range req.OpenAICompatible {
			apiKey := ocReq.APIKey
			outputReserve := 0
			existing, exists := candidate.OpenAICompatible[name]
			if exists {
				if apiKey == maskedAPIKey || apiKey == "" {
					apiKey = existing.APIKey
				}
				outputReserve = existing.OutputTokenReserve
			}
			newMap[name] = config.OpenAICompatibleConfig{
				APIKey:             apiKey,
				BaseURL:            ocReq.BaseURL,
				Models:             ocReq.Models,
				TLSFingerprint:     resolveTLSFingerprint(ocReq.TLSFingerprint, existing.TLSFingerprint, exists),
				OutputTokenReserve: outputReserve,
			}
		}
		candidate.OpenAICompatible = newMap
	}
	if req.AnthropicCompatible != nil {
		newMap := make(map[string]config.AnthropicCompatibleConfig, len(req.AnthropicCompatible))
		for name, acReq := range req.AnthropicCompatible {
			apiKey := acReq.APIKey
			outputReserve := 0
			existing, exists := candidate.AnthropicCompatible[name]
			if exists {
				if apiKey == maskedAPIKey || apiKey == "" {
					apiKey = existing.APIKey
				}
				outputReserve = existing.OutputTokenReserve
			}
			newMap[name] = config.AnthropicCompatibleConfig{
				APIKey:             apiKey,
				BaseURL:            acReq.BaseURL,
				Models:             acReq.Models,
				TLSFingerprint:     resolveTLSFingerprint(acReq.TLSFingerprint, existing.TLSFingerprint, exists),
				OutputTokenReserve: outputReserve,
			}
		}
		candidate.AnthropicCompatible = newMap
	}
	if req.ChatGPT != nil {
		if req.ChatGPT.Models != nil {
			candidate.ChatGPT.Models = req.ChatGPT.Models
		}
		if req.ChatGPT.APIKey != "" && req.ChatGPT.APIKey != maskedAPIKey {
			candidate.ChatGPT.APIKey = req.ChatGPT.APIKey
		}
	}

	// A first-run config intentionally has no default until setup finishes.
	// Once a default exists, however, every candidate must still resolve after
	// all requested provider/model replacements have been applied. Validate
	// before committing so a dangling replacement cannot change any state.
	if candidate.DefaultModel != "" {
		if _, _, err := candidate.ResolveDefaultModelProvider(); err != nil {
			f.configMu.Unlock()
			return fmt.Errorf("invalid LLM configuration: default_model would be unresolved: %w", err)
		}
	}

	f.config.LLM = candidate

	// Persist while configMu is held so a failed disk write can restore the
	// exact prior LLM state before any reader, rebuild, or frontend RPC result
	// observes the candidate. Defer the config-updated event until the write
	// succeeds: a failed update must be indistinguishable from a rejected
	// request to consumers.
	if err := config.Save(f.config, f.configPath); err != nil {
		f.config.LLM = previous
		f.configMu.Unlock()
		return fmt.Errorf("failed to persist LLM config: %w", err)
	}

	// Capture the provisioning guard here — after the unlock, f.config must
	// only be touched under configMu again.
	defaultModel := f.config.LLM.DefaultModel
	f.configMu.Unlock()
	f.emitConfigUpdated()

	// --- Heavy work below runs OUTSIDE configMu (readers stay responsive) ---
	// saveMu is still held, so concurrent UpdateLLMConfig calls are serialized.

	// Clear any config load errors since settings are now valid
	f.configMu.Lock()
	f.configLoadErrors = nil
	f.configMu.Unlock()

	// Ensure No Project exists now that the app is usable.
	// On a clean first run this is the first time the pseudo-project
	// is created — it was deferred during startup to avoid provisioning
	// infrastructure before configuration validation.
	//
	// Guard on a non-empty default model: during initial LLM setup the
	// frontend may debounce-save partial edits before the user has selected
	// a model. Creating No Project and switching on every keystroke would
	// disrupt the file panel while the settings dialog is still open.
	if f.projectManager != nil && defaultModel != "" {
		created, err := f.projectManager.EnsureNoProject()
		if err != nil {
			f.log().Warn("failed to ensure No Project after config update", "error", err)
		}
		// Only refresh the project list when No Project was just created
		// on first-run setup. The frontend's loadAndActivate auto-selects
		// the first project only when no project is active, so emitting
		// backend:ready here lands the user in CHAT mode on first run
		// without disrupting an already-active project/session on
		// mid-session config edits.
		if err == nil && created {
			if projects, pErr := f.projectManager.ListProjects(); pErr == nil {
				f.emitEvent(EventBackendReady, projects)
			} else {
				f.log().Warn("failed to list projects after config update", "error", pErr)
			}
		}
	}

	// Rebuild judge and LLM router via the backend builder so new sessions
	// use the updated provider immediately. The snapshot is taken fresh here
	// rather than carried over from the mutation phase, and configMu.RLock is
	// held across snapshot + rebuild: config writers that mutate and rebuild
	// under configMu.Lock (UpdateModelProfile, SetModelConfig) cannot run
	// in between, so this rebuild can never apply a snapshot that predates
	// their changes and roll the router back. RLock stays shared with
	// readers, so GetConfig is still never convoyed behind the rebuild, and
	// RebuildJudge/RebuildRouter are core calls that never re-enter
	// FrontendAPI, so holding the RLock across them cannot deadlock.
	if b := f.builder(); b != nil {
		f.configMu.RLock()
		fresh := ToBuilderConfig(f.config, f.modelProfilesCatalog())
		b.RebuildJudge(fresh)
		rebuildErr := b.RebuildRouter(fresh)
		f.configMu.RUnlock()
		if rebuildErr != nil {
			f.log().Warn("failed to rebuild LLM router after config update", "error", rebuildErr)
		}
	}

	return nil
}

// UpdateSearchSettings updates search configuration.
func (f *FrontendAPI) UpdateSearchSettings(settings SearchSettingsRequest) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	f.config.Search.Provider = settings.Provider
	// Only update API key if it's not the masked placeholder
	if settings.APIKey != "" && settings.APIKey != maskedAPIKey {
		f.config.Search.APIKey = settings.APIKey
	}

	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist search settings", "error", err)
	}

	// Rebuild web search tool via the backend builder.
	if b := f.builder(); b != nil {
		b.UpdateSearchTool(ToBuilderConfig(f.config, f.modelProfilesCatalog()))
	}

	return nil
}

// UpdateVectorIndexSettings updates the vector-index embedding settings
// (ONNX Runtime execution provider + GPU device id). It deliberately has NO
// hot application: the embedder is created once per process (after
// EventBackendReady), so the new provider/device only takes effect after an
// app restart. This RPC validates, mutates, and persists — nothing else.
func (f *FrontendAPI) UpdateVectorIndexSettings(settings VectorIndexSettingsResponse) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	// Validate BEFORE any mutation: a rejected request must leave both the
	// in-memory config and the YAML file untouched (the file stays loadable,
	// so a restart cannot brick the app on a bad value). The error text
	// mirrors the load-time validate() wording so the user gets the same
	// actionable guidance in the settings UI as on startup.
	switch settings.ExecutionProvider {
	case config.VectorIndexProviderAuto, config.VectorIndexProviderCPU, config.VectorIndexProviderCUDA:
		// valid
	default:
		return fmt.Errorf(
			"vector_index.execution_provider %q is not valid; must be one of: %s, %s, %s",
			settings.ExecutionProvider,
			config.VectorIndexProviderAuto, config.VectorIndexProviderCPU, config.VectorIndexProviderCUDA,
		)
	}
	if settings.DeviceID < 0 {
		return fmt.Errorf(
			"vector_index.device_id %d is not valid; must be >= 0",
			settings.DeviceID,
		)
	}

	f.config.VectorIndex.ExecutionProvider = settings.ExecutionProvider
	f.config.VectorIndex.DeviceID = settings.DeviceID

	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist vector index settings", "error", err)
		return fmt.Errorf("failed to persist vector index settings: %w", err)
	}

	return nil
}

// UpdateProxySettings updates proxy configuration at runtime and propagates
// the change to all subsystems (LLM providers, web tools, MCP, child processes).
//
// Locking contract (mirrors UpdateLLMConfig): saveMu serializes whole save
// sequences, while configMu is held only for the field mutation and the disk
// write — never across the propagation below. RebuildProxy restarts the MCP
// gateway and rebuilds the router and judge, each waiting on builder
// readiness with its own 30-second budget; holding the configMu WRITE lock
// across that blocks every concurrent GetConfig and, because Go's RWMutex
// stops admitting readers once a writer is queued, freezes the whole
// settings dialog for as long as the rebuild takes. The disk write stays
// inside the lock — it is a bounded local atomic rewrite, and keeping it
// there keeps the in-memory state and the persisted file consistent for
// concurrent readers.
func (f *FrontendAPI) UpdateProxySettings(settings ProxySettingsRequest) error {
	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}

	f.config.Proxy.Enabled = settings.Enabled
	// Preserve the existing URL when the incoming value is the masked form
	// returned by GetConfig's proxy section (proxy.MaskURL replaces the
	// password with "***"). The frontend round-trips the displayed (masked)
	// URL verbatim when
	// only another field (enabled/bypass/cert-dir) is edited, so without this
	// guard the real password would be silently overwritten with "***" and the
	// next proxy connection would fail to authenticate. Mirrors the
	// maskedAPIKey preserve guard used for API keys above.
	if settings.URL != "" && settings.URL != proxy.MaskURL(f.config.Proxy.URL) {
		f.config.Proxy.URL = settings.URL
	}
	if settings.BypassList != nil {
		f.config.Proxy.BypassList = settings.BypassList
	}
	f.config.Proxy.TLSCertDir = settings.TLSCertDir

	// Persist (and announce) while configMu is still held: the write is a
	// bounded local atomic rewrite, and keeping it inside keeps the
	// in-memory state and the file consistent for readers. A failure is
	// warned about and the change stays live in memory — the pre-existing
	// contract of this RPC, unchanged here.
	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist proxy settings", "error", err)
	}

	// Snapshot what the rebuild needs while the lock is still held; after the
	// unlock f.config must only be touched under configMu again.
	b := f.builder()
	bcfg := ToBuilderConfig(f.config, f.modelProfilesCatalog())
	f.configMu.Unlock()

	// --- Heavy work below runs OUTSIDE configMu (readers stay responsive) ---
	// saveMu is still held, so concurrent UpdateProxySettings calls are
	// serialized and a later save never propagates before an earlier one.
	if b != nil {
		if err := b.RebuildProxy(context.Background(), bcfg); err != nil {
			f.log().Warn("failed to rebuild proxy after settings update", "error", err)
			return fmt.Errorf("proxy rebuild failed: %w", err)
		}
	}

	return nil
}

// UpdateExperimentalFeatures toggles the master experimental-features switch
// at runtime. It persists the change and rebuilds the LLM router so the
// gated features (the E2S execution mode) take effect for new sessions without
// an app restart. Model Profiles is not gated by this switch, so this method
// never touches its persisted master toggle (model_profiles.enabled).
func (f *FrontendAPI) UpdateExperimentalFeatures(enabled bool) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	f.config.Experimental.Enabled = enabled

	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist experimental features toggle", "error", err)
	}

	// Rebuild the LLM router so the E2S execution mode is applied or removed
	// immediately. The builder config carries the effective E2S settings,
	// reused below to refresh the live orchestrators. Model Profiles is not
	// gated by this switch, so no Model Profiles state is recomputed or pushed.
	builderCfg := ToBuilderConfig(f.config, f.modelProfilesCatalog())
	if b := f.builder(); b != nil {
		if err := b.RebuildRouter(builderCfg); err != nil {
			f.log().Warn("failed to rebuild LLM router after experimental-features toggle", "error", err)
		}
	}

	// Push the refreshed E2S gate onto already-built session orchestrators —
	// otherwise the mode stays disabled there (stale config.E2S) until an app
	// restart even though the live config now enables it.
	if app := f.app; app != nil {
		if mgr := app.Manager(); mgr != nil {
			mgr.SetE2SSettings(builderCfg.E2S)
		}
	}

	return nil
}

// GetSecuritySettings returns current security settings for the UI. The
// response is group-based: every configurable tool group (seven of them — the
// reserved "system" group is never configurable and never included) is
// returned with its policy and, for the execute group, its command blacklist.
func (f *FrontendAPI) GetSecuritySettings() SecuritySettingsResponse {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		// No config loaded yet: surface the canonical defaults so the UI has
		// a complete group set to render (ApplyDefaults is idempotent and
		// pure — the canonical source of the seven groups and their policies).
		var defaults config.Config
		config.ApplyDefaults(&defaults)
		return SecuritySettingsResponse{
			Groups:                   groupPoliciesToResponse(defaults.Security.Groups),
			ExecuteBlacklistDefaults: config.DefaultExecuteGroupBlacklist(),
		}
	}
	resp := SecuritySettingsResponse{
		Groups:                     groupPoliciesToResponse(f.config.Security.Groups),
		AutoApproveWorkspaceWrites: f.config.Security.AutoApproveWorkspaceWrites,
		SmartApprove:               f.config.Security.SmartApprove,
		ExecuteBlacklistDefaults:   config.DefaultExecuteGroupBlacklist(),
	}
	if b := f.builder(); b != nil {
		resp.JudgeAvailable = b.JudgeAvailable()
	}
	return resp
}

// groupPoliciesToResponse converts config group policies into the frontend
// response shape, deep-copying blacklist slices so the caller cannot mutate
// the live config through the returned map. The execute blacklist is
// reported as its EFFECTIVE value while preserving the nil-vs-empty
// distinction across the JSON boundary: nil (unset) means the shipped
// defaults are in force (ApplyDefaults and ToBuilderConfig derive them), so
// those are what the UI must show; an explicitly emptied list is reported
// as [] so a UI round trip (the settings tab saves exactly what it loaded)
// cannot resurrect the defaults over the user's choice.
func groupPoliciesToResponse(groups map[string]config.GroupPolicyConfig) map[string]GroupPolicyResponse {
	out := make(map[string]GroupPolicyResponse, len(groups))
	for name, g := range groups {
		entry := GroupPolicyResponse{Policy: g.Policy}
		if name == config.ToolGroupExecute {
			if g.Blacklist == nil {
				entry.Blacklist = config.DefaultExecuteGroupBlacklist()
			} else {
				entry.Blacklist = make([]string, len(g.Blacklist))
				copy(entry.Blacklist, g.Blacklist)
			}
		} else if len(g.Blacklist) > 0 {
			entry.Blacklist = make([]string, len(g.Blacklist))
			copy(entry.Blacklist, g.Blacklist)
		}
		out[name] = entry
	}
	return out
}

// effectiveExecuteBlacklist maps a stored execute blacklist to its effective
// value: nil (unset) means the shipped defaults are in force (mirroring
// ApplyDefaults and ToBuilderConfig); every other list — including an
// explicitly emptied one — is used as stored.
func effectiveExecuteBlacklist(blacklist []string) []string {
	if blacklist == nil {
		return config.DefaultExecuteGroupBlacklist()
	}
	return blacklist
}

// UpdateSecuritySettings updates security settings at runtime. The incoming
// groups map REPLACES the stored one and must be the COMPLETE set of the
// seven configurable groups — a partial payload is rejected (it would
// silently weaken security: an omitted group resolves fail-safe to
// user_confirm, weaker than a configured deny, and omitting execute would
// strip the live shell blacklist). Validation mirrors config file
// validation: only the fixed set of configurable groups is accepted, the
// reserved "system" group is rejected, policies must use the group enum, a
// blacklist is an execute-only feature, and blacklist patterns must compile.
// An invalid payload mutates nothing. A blacklist identical to the shipped
// defaults is stored as unset so future default improvements keep flowing;
// the effective list (nil ⇒ defaults) is what the shell tool registers. A
// changed effective execute-group blacklist re-registers the shell tool so
// the edit applies without an app restart; the re-registration runs first
// and is atomic, so its failure rolls the config back with no
// partially-applied state.
func (f *FrontendAPI) UpdateSecuritySettings(settings SecuritySettingsResponse) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	newGroups, err := responseToGroupPolicies(settings.Groups)
	if err != nil {
		return err
	}

	// Store-as-unset: a blacklist identical to the shipped defaults is
	// stored as UNSET (nil) so improved default lists keep flowing to
	// configs that never customized the list. Without this rule every UI
	// save would pin today's default patterns into the config file (the UI
	// echoes back everything GetSecuritySettings returned, which includes
	// the effective-default view). config.StoreDefaultBlacklistAsUnset is
	// the single implementation of the rule — config.Save applies it to
	// every persist path, so unrelated settings saves (LLM setup, MCP,
	// search, ...) cannot pin the defaults either. The effective blacklist
	// is unchanged: ToBuilderConfig and groupPoliciesToResponse re-derive
	// the defaults for a nil list, and the change detection below compares
	// effective lists. An explicitly emptied list ([]) does not match, so
	// clearing the editor stays an intentional choice.
	newGroups = config.StoreDefaultBlacklistAsUnset(newGroups)

	// Replace the full group set so config stays in sync with the registry.
	// prevSecurity snapshots the previous block so a failed shell-tool
	// re-registration below can roll the whole replacement back.
	prevSecurity := f.config.Security
	f.config.Security.Groups = newGroups
	f.config.Security.AutoApproveWorkspaceWrites = settings.AutoApproveWorkspaceWrites
	f.config.Security.SmartApprove = settings.SmartApprove

	// Apply policies to the shared tool registry via the backend builder.
	if b := f.builder(); b != nil {
		builderCfg := ToBuilderConfig(f.config, f.modelProfilesCatalog())
		// Re-register the shell tool FIRST: the blacklist is compiled into
		// the tool instance at registration, so runtime edits need it to
		// take effect without an app restart. The call is atomic (a compile
		// failure leaves the previously registered tool in place), so on
		// error the config is restored and no layer is left half-applied —
		// the old blacklist stays live and matches the rolled-back config.
		// The comparison uses effective lists: a nil (unset) list means the
		// shipped defaults are in force, so an unchanged-default save does
		// not re-register anything.
		if !slices.Equal(
			effectiveExecuteBlacklist(prevSecurity.Groups[config.ToolGroupExecute].Blacklist),
			effectiveExecuteBlacklist(newGroups[config.ToolGroupExecute].Blacklist),
		) {
			if err := b.UpdateShellBlacklist(builderCfg); err != nil {
				f.config.Security = prevSecurity
				return fmt.Errorf("failed to apply execute blacklist: %w", err)
			}
		}
		b.UpdateSecurityPolicies(builderCfg)
	}

	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist security settings", "error", err)
	}

	return nil
}

// responseToGroupPolicies validates a frontend groups payload and converts it
// into config group policies, deep-copying blacklist slices. The rules mirror
// config.validate — the fixed set of configurable groups, the policy enum,
// execute-only blacklists, and blacklist pattern compilation — so a UI-sourced
// update can never store what the config loader would reject on the next
// start. The payload must carry the COMPLETE set of configurable groups: the
// result replaces the stored map wholesale, and a partial payload would
// silently weaken security (an omitted group resolves fail-safe to
// user_confirm — weaker than a configured deny; omitting execute strips the
// live shell blacklist).
func responseToGroupPolicies(groups map[string]GroupPolicyResponse) (map[string]config.GroupPolicyConfig, error) {
	out := make(map[string]config.GroupPolicyConfig, len(groups))
	for name, g := range groups {
		if name == config.ToolGroupSystem {
			return nil, fmt.Errorf(
				"security group %q is reserved for system tools and cannot be configured",
				config.ToolGroupSystem,
			)
		}
		if !config.IsConfigurableToolGroup(name) {
			return nil, fmt.Errorf(
				"unknown security group %q; must be one of: %s",
				name, strings.Join(config.SortedToolGroupNames(), ", "),
			)
		}
		switch g.Policy {
		case config.GroupPolicyAllow, config.GroupPolicyUserConfirm, config.GroupPolicyDeny:
		default:
			return nil, fmt.Errorf(
				"security group %q has invalid policy %q; must be one of: %s, %s, %s",
				name, g.Policy, config.GroupPolicyAllow, config.GroupPolicyUserConfirm, config.GroupPolicyDeny,
			)
		}
		if name != config.ToolGroupExecute && len(g.Blacklist) > 0 {
			return nil, fmt.Errorf(
				"security group %q does not support a blacklist; only %q does",
				name, config.ToolGroupExecute,
			)
		}
		for _, pattern := range g.Blacklist {
			if _, err := regexp.Compile(pattern); err != nil {
				return nil, fmt.Errorf(
					"security group %q blacklist pattern %q does not compile: %w",
					name, pattern, err,
				)
			}
		}
		entry := config.GroupPolicyConfig{Policy: g.Policy}
		// Preserve nil vs empty distinction: a missing blacklist means
		// "unset" (defaults apply), an explicit empty array means "no
		// patterns" (an intentional user choice that must not resurrect
		// the defaults).
		if g.Blacklist != nil {
			entry.Blacklist = make([]string, len(g.Blacklist))
			copy(entry.Blacklist, g.Blacklist)
		}
		out[name] = entry
	}
	// Completeness: the map replaces the stored one, so every configurable
	// group must be present — a partial payload is a fail-closed error, never
	// a silent weakening of omitted groups.
	if names := config.SortedToolGroupNames(); len(out) != len(names) {
		missing := make([]string, 0, len(names))
		for _, name := range names {
			if _, ok := out[name]; !ok {
				missing = append(missing, name)
			}
		}
		return nil, fmt.Errorf(
			"security groups payload must include all %d configurable groups; missing: %s",
			len(names), strings.Join(missing, ", "),
		)
	}
	return out, nil
}

// GetModelProfiles returns the model-profile profile catalog for the settings
// picker: every profile (predefined ∪ custom store under f.agentDir) with its
// 25 knob values, the stored active profile id, the suggested profile id
// (normalized default-model match against the predefined slugs; null when
// nothing matches), the read-only picker universe (builtin_tools /
// tool_groups) read from the live tool registry, and warnings — store-load
// warnings, resolver warnings (dangling active id → generic fallback) and
// one-shot notices such as "the active profile was deleted; switched to
// generic". When config is not yet initialized the profiles are still
// returned (the catalog is independent of config.yaml); active_id is empty
// and suggested_profile_id is null.
func (f *FrontendAPI) GetModelProfiles() ModelProfilesResponse {
	// Lock (not RLock): the one-shot notices are drained on read.
	f.configMu.Lock()
	defer f.configMu.Unlock()

	// Picker universe (built-in tools + workflow clusters) comes from the live
	// tool registry, not from config, so it is read here.
	builtinTools, toolGroups := f.modelProfilesPickerData()

	resp := ModelProfilesResponse{
		Profiles:       []ModelProfileDTO{},
		BuiltinTools:   builtinTools,
		ToolGroups:     toolGroups,
		ProtectedTools: nonNilStringSlice(modelprofiles.ProtectedToolNames()),
		Warnings:       []string{},
	}

	// Fresh store read so runtime-created/deleted custom profiles show up
	// without a restart; store warnings surface in the response. The catalog
	// (predefined ∪ custom) is independent of config.yaml, so it is returned
	// even before the config is initialized.
	catalog, storeWarnings := config.LoadModelProfilesCatalog(f.agentDir)
	for _, w := range storeWarnings {
		f.log().Warn("model-profile profile warning", "warning", w)
	}
	resp.Warnings = append(resp.Warnings, storeWarnings...)
	for _, p := range catalog {
		resp.Profiles = append(resp.Profiles, modelProfileToDTO(p))
	}

	if f.config == nil {
		return resp
	}

	// Global master toggle, reported verbatim (it is not a profile value).
	resp.Enabled = f.config.ModelProfiles.Enabled

	// Stored view, verbatim: a dangling id stays visible while the resolver
	// warning explains the generic fallback.
	resp.ActiveID = f.config.ModelProfiles.ActiveProfile
	_, resolveWarnings := config.ResolveModelProfilesConfig(f.config.ModelProfiles, catalog)
	for _, w := range resolveWarnings {
		f.log().Warn("model-profile profile warning", "warning", w)
	}
	resp.Warnings = append(resp.Warnings, resolveWarnings...)

	// One-shot notices (e.g. delete of the active profile) appear exactly in
	// the next Get.
	resp.Warnings = append(resp.Warnings, f.modelProfilesNotices...)
	f.modelProfilesNotices = nil

	if id := suggestModelProfileID(f.config.LLM.DefaultModel); id != "" {
		resp.SuggestedProfileID = &id
	}
	return resp
}

// modelProfilesSuggestSuffixTokens lists the trailing marketing/instruction suffixes
// stripped (with their "-" separator) from BOTH the model name and the
// predefined slug before comparison: "Qwen3.8-27B-Instruct" and the slug
// "qwen3.8-27b" normalize to the same token.
var modelProfilesSuggestSuffixTokens = []string{"instruct", "it", "latest", "free"}

// normalizeModelProfilesModelToken canonicalizes a model name or profile slug for the
// suggestion match: lowercase, keep only the last path segment after "/"
// (provider prefixes such as "Qwen/" or "openrouter/qwen/"), drop any
// ":"-separated decoration inside that segment (openrouter's ":free"),
// strip trailing suffix tokens (see modelProfilesSuggestSuffixTokens), then drop every
// remaining non-alphanumeric character so separator styles ("qwen3.8-27b",
// "qwen3_8_27b") collapse together.
func normalizeModelProfilesModelToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	for changed := true; changed; {
		changed = false
		for _, suffix := range modelProfilesSuggestSuffixTokens {
			if strings.HasSuffix(s, "-"+suffix) {
				s = strings.TrimSuffix(s, "-"+suffix)
				changed = true
			}
		}
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// suggestModelProfileID returns the predefined profile whose slug best matches
// the configured default model name, or "" when nothing matches. "generic"
// is never suggested — it is the model-agnostic fallback the picker already
// offers. The match is containment on the normalized tokens (model name
// contains the slug), so vendor decorations ("Qwen/Qwen3.8-27B",
// "qwen3.8-27b-instruct-2507") still land on "qwen3.8-27b"; the longest
// matching slug wins so a more specific profile beats a shorter one.
func suggestModelProfileID(defaultModel string) string {
	norm := normalizeModelProfilesModelToken(defaultModel)
	if norm == "" {
		return ""
	}
	best := ""
	bestLen := 0
	for _, p := range config.PredefinedModelProfiles() {
		if p.ID == config.ModelProfilesGenericProfileID {
			continue
		}
		slug := normalizeModelProfilesModelToken(p.ID)
		if slug == "" {
			continue
		}
		if (norm == slug || strings.Contains(norm, slug)) && len(slug) > bestLen {
			best = p.ID
			bestLen = len(slug)
		}
	}
	return best
}

// modelProfilesCatalog loads the full model-profile profile catalog (predefined ∪ custom
// store under f.agentDir), logging store-level warnings. Reads are fresh so a
// custom profile saved at runtime applies on the next config conversion
// without a restart; the store file is tiny and conversions are not hot paths.
func (f *FrontendAPI) modelProfilesCatalog() []config.ModelProfile {
	return loadModelProfilesCatalog(f.agentDir, f.log())
}

// refreshModelProfilesGateLocked recomputes the cached effective Model Profiles gate reported
// as ConfigResponse.model_profiles. Callers must hold configMu for WRITING. The profile
// catalog (a disk read) is resolved only here — at construction and on the rare
// Model Profiles mutations — never on the GetConfig read path, which just
// serves the cached value (keeping GetConfig a pure in-memory read, per the
// GUARANTEE on collectAllModels).
func (f *FrontendAPI) refreshModelProfilesGateLocked() {
	if f.config == nil {
		f.modelProfilesGateResp = ModelProfilesSettingsResponse{}
		return
	}
	profile, _ := effectiveModelProfilesConfig(f.config, f.modelProfilesCatalog())
	f.modelProfilesGateResp = ModelProfilesSettingsResponse{
		Enabled:               profile.Enabled,
		EssentialToolsEnabled: profile.EssentialTools.Enabled,
	}
}

// modelProfilesGoalBlocked reports whether goal mode must be refused because the
// model-profile essential-tools narrowing is active: the master ModelProfiles toggle AND the
// active profile's essential-tools variant both on (Model Profiles is not gated
// by the experimental-features switch, so the master toggle alone decides). The
// profile is resolved against
// the live catalog so a runtime profile switch takes effect without a restart.
// Returns false when no config is loaded (fail-open, matching every other
// runtime config read): a not-yet-loaded config must never block a request on
// principle.
func (f *FrontendAPI) modelProfilesGoalBlocked() bool {
	// Cheap shunt under the read lock: no config, or the master toggle
	// persisted off, can never block — skip the catalog read too. The config
	// pointer is set once at construction and never swapped (only its fields
	// are mutated in place), so the short hold is safe.
	f.configMu.RLock()
	shunt := f.config == nil || !f.config.ModelProfiles.Enabled
	f.configMu.RUnlock()
	if shunt {
		return false
	}
	// modelProfilesCatalog() does file I/O (reads ~/.c0wrk/model-profiles.yaml) — load it
	// OUTSIDE the config lock, then resolve the effective profile UNDER the
	// read lock. Resolving under the lock is required for race-freedom:
	// effectiveModelProfilesConfig reads cfg.ModelProfiles, and the setters
	// (SetModelProfilesEnabled / SelectModelProfile) mutate that SAME field IN
	// PLACE under the write lock, so reading it from a detached pointer would be
	// a data race. (This is not a safe "config swap": the config is never
	// swapped, only mutated.)
	catalog := f.modelProfilesCatalog()
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	if f.config == nil {
		return false
	}
	profile, _ := effectiveModelProfilesConfig(f.config, catalog)
	return profile.Enabled && profile.EssentialTools.Enabled
}

// modelProfilesPickerData returns the read-only picker universe for the
// always-present picker: every registered built-in tool that is neither
// MCP-sourced nor goal-mode-only, with its description, plus the workflow
// clusters (each restricted to that universe). Both slices are non-nil, so JSON
// serializes them as []. Returns empty slices when the application is
// unavailable (before construction, or in unit tests built without an
// Application).
func (f *FrontendAPI) modelProfilesPickerData() ([]ModelProfilesBuiltinTool, []ModelProfilesToolGroup) {
	if f.app == nil {
		return []ModelProfilesBuiltinTool{}, []ModelProfilesToolGroup{}
	}
	tools := builtinToolInfos(f.app.ListTools())
	return tools, modelProfilesToolGroups(tools)
}

// builtinToolInfos extracts the picker universe from a descriptor list, sorted
// by name, each with its registry description. Two classes of built-in are
// excluded because pinning them would be inert:
//
//   - MCP tools — the essential-tools selection always keeps them implicitly,
//     so pinning one is a no-op;
//   - goal-mode-only tools (propose_goal / declare_goal_status /
//     declare_verification) — they are stripped from every non-goal run before
//     the selection runs (tools.StripGoalModeTools) and the selection is not
//     applied in goal mode at all, so their availability never depends on the
//     user's selection.
//
// Everything else is returned, including the reserved system/orchestration
// group and the protected tools (modelprofiles.ProtectedToolNames) — the latter are
// the selection's un-narrowable members, so the picker subtracts the
// already-allowed set before offering an entry (see ModelProfilesEssentialToolsResp).
// Deterministic (sorted) and freshly allocated so callers may mutate it.
//
// The description is the tool's registry text verbatim: every built-in — c0wrk
// and sp4rk alike — follows the purpose/when-to-use/inputs/outputs/example/
// anti-example rubric (guarded in core/tools and sp4rk/tools/builtins), so the
// UI can reformat it into markdown for the picker's hover tooltip.
func builtinToolInfos(descriptors []sdktools.ToolDescriptor) []ModelProfilesBuiltinTool {
	out := make([]ModelProfilesBuiltinTool, 0, len(descriptors))
	for _, d := range descriptors {
		if d.SourceCategory == sdktools.SourceCategoryMCP || coretools.IsGoalModeTool(d.Name) {
			continue
		}
		out = append(out, ModelProfilesBuiltinTool{Name: d.Name, Description: d.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// modelProfilesToolGroups projects the workflow-cluster catalog onto the picker
// universe: a cluster is kept when at least one of its members is in the
// universe (members are filtered to it), and a cluster with no surviving member
// is dropped. The result is freshly allocated.
func modelProfilesToolGroups(tools []ModelProfilesBuiltinTool) []ModelProfilesToolGroup {
	registered := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		registered[t.Name] = struct{}{}
	}
	catalog := modelprofiles.ToolGroupCatalog()
	out := make([]ModelProfilesToolGroup, 0, len(catalog))
	for _, g := range catalog {
		members := make([]string, 0, len(g.Tools))
		for _, name := range g.Tools {
			if _, ok := registered[name]; ok {
				members = append(members, name)
			}
		}
		if len(members) == 0 {
			continue
		}
		out = append(out, ModelProfilesToolGroup{
			ID:          g.ID,
			Title:       g.Title,
			Description: g.Description,
			Tools:       members,
		})
	}
	return out
}

// modelProfilesStore reads the custom profile store (fail-soft), logging store-level
// warnings. Mutations operate on this fresh snapshot.
func (f *FrontendAPI) modelProfilesStore() (custom []config.ModelProfile, storePath string) {
	storePath = config.ModelProfilesPath(f.agentDir)
	custom, warnings := config.LoadCustomModelProfiles(storePath)
	for _, w := range warnings {
		f.log().Warn("model-profile profile warning", "warning", w)
	}
	return custom, storePath
}

// applyModelProfilesChange is the uniform post-mutation tail shared by the profile
// CRUD/select methods. Callers must hold configMu and call it only after a
// successful write; it (1) clears the config load-warnings channel so a stale
// "damaged store"/"dangling active id" warning stops being served by GetConfig
// once the mutation fixed the condition, (2) announces the change
// (config:updated), (3) rebuilds the LLM router so the effective profile
// applies to new sessions without a restart, and (4) refreshes the session
// manager's Model Profiles snapshot so later agent_metrics events are annotated
// with the new profile.
func (f *FrontendAPI) applyModelProfilesChange() {
	f.configLoadErrors = nil
	f.emitConfigUpdated()
	// Recompute the cached effective gate that GetConfig serves (this method
	// runs under configMu.Lock), so the goal-mode block the settings UI mirrors
	// tracks the just-applied change.
	f.refreshModelProfilesGateLocked()
	builderCfg := ToBuilderConfig(f.config, f.modelProfilesCatalog())
	if b := f.builder(); b != nil {
		if err := b.RebuildRouter(builderCfg); err != nil {
			f.log().Warn("failed to rebuild LLM router after model-profile profile change", "error", err)
		}
	}
	if app := f.app; app != nil {
		if mgr := app.Manager(); mgr != nil {
			modelProfilesCatalog := f.modelProfilesCatalog()
			modelProfile, _ := effectiveModelProfilesConfig(f.config, modelProfilesCatalog)
			mgr.SetModelProfile(modelProfile, activeModelProfile(f.config.ModelProfiles, modelProfilesCatalog))
			// Push the refreshed ModelProfiles settings onto already-built orchestrators
			// so the runtime change takes effect there without an app restart —
			// otherwise the goal-mode guard (and the essential-tools filter)
			// would keep the stale build-time snapshot until the session is
			// rebuilt. Mirrors the E2S push.
			mgr.SetModelProfilesSettings(core.ModelProfilesSettingsFromBuilderConfig(builderCfg.ModelProfiles))
		}
	}
}

// CreateModelProfile duplicates the catalog profile baseID under a new display
// name as a custom profile and persists it to the custom store
// (~/.c0wrk/model-profiles.yaml). An empty baseID means the generic profile.
// The name must be non-empty and unique against every predefined and custom
// profile name; the id is derived from the name. The active profile is NOT
// changed (select explicitly afterwards). Returns the new profile id.
// Validation runs before any write; the store save is atomic, so a failure
// leaves nothing behind.
func (f *FrontendAPI) CreateModelProfile(baseID, name string) (string, error) {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return "", errors.New("config not initialized")
	}
	if baseID == "" {
		baseID = config.ModelProfilesGenericProfileID
	}
	base, ok := config.FindModelProfile(f.modelProfilesCatalog(), baseID)
	if !ok {
		return "", fmt.Errorf("base profile %q does not exist in the model-profile profile catalog", baseID)
	}

	custom, storePath := f.modelProfilesStore()
	created, err := config.CreateCustomModelProfile(name, base.Config, custom)
	if err != nil {
		return "", fmt.Errorf("invalid model-profile profile: %w", err)
	}
	if err := config.SaveCustomModelProfiles(storePath, slices.Concat(custom, []config.ModelProfile{created})); err != nil {
		return "", fmt.Errorf("failed to persist the model-profile profile store: %w", err)
	}
	f.applyModelProfilesChange()
	return created.ID, nil
}

// UpdateModelProfile updates the CUSTOM profile id. Only the two request-level
// fields are optional: a nil Name keeps the stored display name, a nil Config
// keeps the stored 25 knob values. A supplied Config is a WHOLE-VALUE
// replacement — all 25 knobs are overwritten by the request (the values DTO
// carries no per-leaf pointers, so there is no per-section merge). Predefined
// profiles are read-only (duplicate one to customize it) and unknown ids are
// rejected. Validation runs before any mutation, and the store save is an
// atomic full rewrite, so an invalid payload or a failed write leaves the
// stored state untouched.
func (f *FrontendAPI) UpdateModelProfile(id string, req ModelProfileUpdateRequest) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	if req.Name == nil && req.Config == nil {
		return nil // nothing requested — nothing to validate or persist
	}
	if _, isPredefined := config.FindPredefinedModelProfile(id); isPredefined {
		return fmt.Errorf("the model-profile profile %q is predefined and read-only; duplicate it as a custom profile to edit it", id)
	}

	custom, storePath := f.modelProfilesStore()
	idx := slices.IndexFunc(custom, func(p config.ModelProfile) bool { return p.ID == id })
	if idx < 0 {
		return fmt.Errorf("the model-profile profile %q does not exist in the profile catalog", id)
	}

	// Next name: trimmed, non-empty, unique against every OTHER profile name
	// (predefined ∪ custom, self excluded). The store save re-validates the
	// full set — including the name-length bound — before writing a byte.
	nextName := custom[idx].Name
	if req.Name != nil {
		nextName = strings.TrimSpace(*req.Name)
		if nextName == "" {
			return errors.New("model-profile profile name must not be empty")
		}
		for _, p := range config.PredefinedModelProfiles() {
			if p.Name == nextName {
				return fmt.Errorf("model-profile profile name %q collides with the predefined profile %q", nextName, p.ID)
			}
		}
		for i, p := range custom {
			if i != idx && p.Name == nextName {
				return fmt.Errorf("model-profile profile name %q is already used by custom profile %q", nextName, p.ID)
			}
		}
	}

	// Next values: the profile constructor re-validates everything
	// (config.ValidateModelProfileConfig semantics).
	nextCfg := custom[idx].Config
	if req.Config != nil {
		nextCfg = modelProfilesValuesToProfileConfig(*req.Config)
	}
	updated, err := config.NewModelProfile(custom[idx].ID, nextName, config.ModelProfileKindCustom, nextCfg)
	if err != nil {
		return fmt.Errorf("invalid model-profile profile payload: %w", err)
	}
	next := slices.Clone(custom)
	next[idx] = updated
	if err := config.SaveCustomModelProfiles(storePath, next); err != nil {
		return fmt.Errorf("failed to persist the model-profile profile: %w", err)
	}
	f.applyModelProfilesChange()
	return nil
}

// DeleteModelProfile removes the CUSTOM profile id from the store. Predefined
// profiles are undeletable and unknown ids are rejected. Deleting the ACTIVE
// profile first falls back to generic in config.yaml (persisted before the
// store write, rolled back if the config persist fails) and records a one-shot
// notice that the next GetModelProfiles reports as a warning alongside the
// switched active id.
func (f *FrontendAPI) DeleteModelProfile(id string) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	if _, isPredefined := config.FindPredefinedModelProfile(id); isPredefined {
		return fmt.Errorf("the model-profile profile %q is predefined and cannot be deleted", id)
	}

	custom, storePath := f.modelProfilesStore()
	if !slices.ContainsFunc(custom, func(p config.ModelProfile) bool { return p.ID == id }) {
		return fmt.Errorf("the model-profile profile %q does not exist in the profile catalog", id)
	}

	// Deleting the active profile switches config.yaml to generic FIRST: if
	// the config persist fails the in-memory id is restored and the store is
	// never touched; if the store delete then fails, config.yaml merely points
	// at generic while the profile still exists — a benign, retry-safe state.
	wasActive := f.config.ModelProfiles.ActiveProfile == id
	if wasActive {
		if f.configPath == "" {
			return errors.New("config path not set")
		}
		prev := f.config.ModelProfiles.ActiveProfile
		f.config.ModelProfiles.ActiveProfile = config.ModelProfilesGenericProfileID
		if err := config.Save(f.config, f.configPath); err != nil {
			f.config.ModelProfiles.ActiveProfile = prev
			return fmt.Errorf("failed to persist model-profile config: %w", err)
		}
	}

	if _, err := config.DeleteCustomModelProfile(storePath, id); err != nil {
		return fmt.Errorf("failed to delete the model-profile profile: %w", err)
	}
	if wasActive {
		f.modelProfilesNotices = append(f.modelProfilesNotices,
			fmt.Sprintf("the active model-profile profile %q was deleted; switched to the generic profile", id))
	}
	f.applyModelProfilesChange()
	return nil
}

// SelectModelProfile makes id the active model-profile profile and persists the
// choice to config.yaml (model_profiles.active_profile). The id must exist in the
// catalog (predefined ∪ custom); a failed disk write rolls the in-memory
// state back so the rejected selection is indistinguishable from a rejected
// request.
func (f *FrontendAPI) SelectModelProfile(id string) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	if id == "" {
		return errors.New("model-profile profile id must not be empty")
	}
	if f.configPath == "" {
		return errors.New("config path not set")
	}
	if _, ok := config.FindModelProfile(f.modelProfilesCatalog(), id); !ok {
		return fmt.Errorf("model-profile profile %q does not exist in the profile catalog", id)
	}
	if f.config.ModelProfiles.ActiveProfile == id {
		return nil // already active — no state change
	}

	prev := f.config.ModelProfiles.ActiveProfile
	f.config.ModelProfiles.ActiveProfile = id
	if err := config.Save(f.config, f.configPath); err != nil {
		f.config.ModelProfiles.ActiveProfile = prev
		return fmt.Errorf("failed to persist model-profile config: %w", err)
	}
	f.applyModelProfilesChange()
	return nil
}

// SetModelProfilesEnabled toggles the global Model Profiles master switch (model_profiles.enabled) and
// persists it to config.yaml. The master toggle is the only switch — Model
// Profiles is not gated by the experimental-features switch, so both turning it
// on and off are always allowed. A request matching the stored value is a
// no-op, and a failed disk write rolls the in-memory value back so the rejected
// toggle is indistinguishable from a rejected request.
func (f *FrontendAPI) SetModelProfilesEnabled(enabled bool) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	if f.configPath == "" {
		return errors.New("config path not set")
	}
	if f.config.ModelProfiles.Enabled == enabled {
		return nil // already in the requested state — no state change
	}

	prev := f.config.ModelProfiles.Enabled
	f.config.ModelProfiles.Enabled = enabled
	if err := config.Save(f.config, f.configPath); err != nil {
		f.config.ModelProfiles.Enabled = prev
		return fmt.Errorf("failed to persist model-profile config: %w", err)
	}
	f.applyModelProfilesChange()
	return nil
}

// modelProfileToDTO converts one catalog profile into the picker DTO.
func modelProfileToDTO(p config.ModelProfile) ModelProfileDTO {
	return ModelProfileDTO{
		ID:     p.ID,
		Name:   p.Name,
		Kind:   string(p.Kind),
		Values: modelProfileConfigToValues(p.Config),
	}
}

// modelProfileConfigToValues converts the profile-level 25-knob struct into
// the JSON-tagged values DTO. AlwaysPresent is normalized to a non-nil slice
// so JSON serialization yields [] instead of null.
func modelProfileConfigToValues(c config.ModelProfileConfig) ModelProfileValues {
	return ModelProfileValues{
		EssentialTools: ModelProfilesEssentialToolsValues{
			Enabled:             c.EssentialTools.Enabled,
			AlwaysPresent:       nonNilStringSlice(c.EssentialTools.AlwaysPresent),
			CompactDescriptions: c.EssentialTools.CompactDescriptions,
		},
		SystemPrompt: ModelProfilesSystemPromptResp{
			Lite:              c.SystemPrompt.Lite,
			FewShot:           c.SystemPrompt.FewShot,
			ReasoningScaffold: c.SystemPrompt.ReasoningScaffold,
		},
		Sampling: ModelProfilesSamplingResp{
			Enabled:           c.Sampling.Enabled,
			Temperature:       c.Sampling.Temperature,
			TopP:              c.Sampling.TopP,
			TopK:              c.Sampling.TopK,
			RepetitionPenalty: c.Sampling.RepetitionPenalty,
			PresencePenalty:   c.Sampling.PresencePenalty,
			ReasoningEffort:   c.Sampling.ReasoningEffort,
		},
		LoopHardening: ModelProfilesLoopHardeningResp{
			Enabled:                      c.LoopHardening.Enabled,
			RepeatNudgeThreshold:         c.LoopHardening.RepeatNudgeThreshold,
			ParseErrorAbortThreshold:     c.LoopHardening.ParseErrorAbortThreshold,
			FruitlessNudgeThreshold:      c.LoopHardening.FruitlessNudgeThreshold,
			FruitlessAbortThreshold:      c.LoopHardening.FruitlessAbortThreshold,
			SameToolRepeatNudgeThreshold: c.LoopHardening.SameToolRepeatNudgeThreshold,
		},
		Context: ModelProfilesContextResp{
			Enabled: c.Context.Enabled,
			Compaction: ModelProfilesCompactionResp{
				KeepLast:       c.Context.Compaction.KeepLast,
				BlockSize:      c.Context.Compaction.BlockSize,
				TriggerPercent: c.Context.Compaction.TriggerPercent,
			},
			ToolOutputKeepLastN: c.Context.ToolOutputKeepLastN,
			OutputTokenReserve:  c.Context.OutputTokenReserve,
		},
	}
}

// modelProfilesValuesToProfileConfig converts the values DTO into the profile-level
// 25-knob struct for validation/persistence via config.NewModelProfile.
func modelProfilesValuesToProfileConfig(r ModelProfileValues) config.ModelProfileConfig {
	return config.ModelProfileConfig{
		EssentialTools: config.EssentialToolsConfig{
			Enabled:             r.EssentialTools.Enabled,
			AlwaysPresent:       r.EssentialTools.AlwaysPresent,
			CompactDescriptions: r.EssentialTools.CompactDescriptions,
		},
		SystemPrompt: config.SystemPromptConfig{
			Lite:              r.SystemPrompt.Lite,
			FewShot:           r.SystemPrompt.FewShot,
			ReasoningScaffold: r.SystemPrompt.ReasoningScaffold,
		},
		Sampling: config.ModelProfilesSamplingConfig{
			Enabled:           r.Sampling.Enabled,
			Temperature:       r.Sampling.Temperature,
			TopP:              r.Sampling.TopP,
			TopK:              r.Sampling.TopK,
			RepetitionPenalty: r.Sampling.RepetitionPenalty,
			PresencePenalty:   r.Sampling.PresencePenalty,
			ReasoningEffort:   r.Sampling.ReasoningEffort,
		},
		LoopHardening: config.LoopHardeningConfig{
			Enabled:                      r.LoopHardening.Enabled,
			RepeatNudgeThreshold:         r.LoopHardening.RepeatNudgeThreshold,
			ParseErrorAbortThreshold:     r.LoopHardening.ParseErrorAbortThreshold,
			FruitlessNudgeThreshold:      r.LoopHardening.FruitlessNudgeThreshold,
			FruitlessAbortThreshold:      r.LoopHardening.FruitlessAbortThreshold,
			SameToolRepeatNudgeThreshold: r.LoopHardening.SameToolRepeatNudgeThreshold,
		},
		Context: config.ModelProfilesContextConfig{
			Enabled: r.Context.Enabled,
			Compaction: config.ModelProfilesCompactionConfig{
				KeepLast:       r.Context.Compaction.KeepLast,
				BlockSize:      r.Context.Compaction.BlockSize,
				TriggerPercent: r.Context.Compaction.TriggerPercent,
			},
			ToolOutputKeepLastN: r.Context.ToolOutputKeepLastN,
			OutputTokenReserve:  r.Context.OutputTokenReserve,
		},
	}
}

// GetLogLevel returns the current log level.
func (f *FrontendAPI) GetLogLevel() string {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	return f.logLevel
}

// SetLogLevel sets the log level dynamically.
func (f *FrontendAPI) SetLogLevel(level string) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	// Validate the level
	level = strings.ToUpper(level)
	switch level {
	case "DEBUG", "INFO", "WARN", "ERROR":
		f.logLevel = level
		if f.app != nil {
			f.app.Manager().SetLogLevel(level)
		}
		if f.config != nil {
			f.config.LogLevel = level
		}
		if err := f.persistConfig(); err != nil {
			f.log().Warn("failed to persist log level change", "error", err)
		}
		return nil
	default:
		return fmt.Errorf("invalid log level: %s", level)
	}
}

// ListProviderModels returns available model names for a given provider.
// For Anthropic: returns hardcoded list from model registry.
// For ChatGPT/OpenAI Compatible: fetches from the provider's API.
//
// Draft credentials on req (api_key, base_url, type) are merged into a
// throwaway BuilderConfig copy so the settings UI can list models for a
// compatible provider that is not yet persisted — e.g. first-run setup
// where saves are blocked until a default_model is chosen.
func (f *FrontendAPI) ListProviderModels(req ListProviderModelsRequest) ([]string, error) {
	if req.Provider == "" {
		return nil, errors.New("provider is required")
	}

	f.configMu.RLock()
	if f.config == nil {
		f.configMu.RUnlock()
		return nil, errors.New("config not initialized")
	}
	b := f.builder()
	if b == nil {
		f.configMu.RUnlock()
		return nil, errors.New("application not initialized")
	}
	cfg := ToBuilderConfig(f.config, f.modelProfilesCatalog())
	f.configMu.RUnlock()

	if err := applyListProviderModelsOverrides(cfg, req); err != nil {
		return nil, err
	}
	return b.ListProviderModels(context.Background(), req.Provider, cfg)
}

// GetProviderTLSCertificate connects to the provider's endpoint and returns
// the SPKI fingerprint of the certificate the server currently presents —
// the settings UI "Get" button (ADR-052). The connection performs only the
// TLS handshake: no HTTP request, no API key. Verification is deliberately
// skipped, because the fingerprint IS what is being fetched; the user pins
// the result afterwards.
//
// The call is UNCONDITIONAL with respect to any configured pin: the request
// carries no fingerprint, nothing here reads the provider's persisted pin,
// and the result is the same whether the provider is already pinned or not.
// Pressing the button always answers "what is this endpoint serving right
// now?".
//
// baseURL from the request (the draft form value) wins over the persisted
// provider base_url; ${VAR} is expanded for both, like every other dial
// path.
//
// The probe dials the endpoint DIRECTLY (it does not consult the configured
// HTTP proxy), so while a proxy is enabled the fetched pin would never be
// used: proxy wins over the pin on every provider dial path. The call is
// rejected up front with an actionable error instead of handing the user a
// pin that silently does nothing.
//
// Locking contract: the effective proxy state and the persisted base URL are
// snapshotted under configMu.RLock, and the lock is RELEASED before the
// handshake. Holding configMu across a network call (up to
// llmtls.FetchFingerprintTimeout) would block every concurrent GetConfig and
// freeze the settings dialog.
func (f *FrontendAPI) GetProviderTLSCertificate(req GetProviderTLSCertificateRequest) (TLSCertificateResponse, error) {
	if req.Provider == "" {
		return TLSCertificateResponse{}, errors.New("provider is required")
	}

	f.configMu.RLock()
	// Effective proxy state mirrors proxy.BuildTransport: enabled AND a URL
	// must both be set; an enabled-but-empty proxy dials directly, so the pin
	// stays meaningful there.
	proxyEnabled := f.config != nil && f.config.Proxy.Enabled && f.config.Proxy.URL != ""
	var persisted string
	if f.config != nil {
		// The canonical provider list carries the raw (env-var) base URL; the
		// expander resolves it below.
		for _, p := range f.config.LLM.GetAllProviderConfigs() {
			if p.Name == req.Provider {
				persisted = p.BaseURL
				break
			}
		}
	}
	f.configMu.RUnlock()
	// No lock is held from here on — the handshake below is a network call.

	if proxyEnabled {
		return TLSCertificateResponse{}, errors.New("TLS fingerprint fetching is unavailable while an HTTP proxy is enabled (Settings → General → HTTP Proxy): the pin does not apply to proxied connections. Disable the proxy, or add this host to the proxy bypass list, to pin this server's certificate")
	}

	raw := req.BaseURL
	if raw == "" {
		raw = persisted
	}
	if raw == "" {
		return TLSCertificateResponse{}, fmt.Errorf("provider %q has no base URL configured", req.Provider)
	}
	// Env-var expansion (${VAR}) — the same treatment every other dial path
	// gives the base URL; a persisted `${LLM_BASE_URL}` would otherwise be
	// parsed as a literal (and fail) by url.Parse inside FetchFingerprint.
	raw = config.ExpandEnvVars(raw)

	fp, err := llmtls.FetchFingerprint(context.Background(), raw, f.log())
	if err != nil {
		return TLSCertificateResponse{}, fmt.Errorf("fetching certificate fingerprint from %q: %w", raw, err)
	}
	return TLSCertificateResponse{Fingerprint: fp}, nil
}

// resolveTLSFingerprint applies the pointer sentinel for the per-provider
// TLS pin (ADR-052) on the API boundary: nil means "keep the persisted pin",
// which is what a debounced partial save from the settings dialog sends when
// only credentials or the model list changed — without this, every such save
// would silently clear the pin. A non-nil pointer applies verbatim, so an
// explicit empty string is the deliberate "clear the pin" signal (back to
// system CA verification).
func resolveTLSFingerprint(requested *string, persisted string, providerExists bool) string {
	if requested != nil {
		return *requested
	}
	if providerExists {
		return persisted
	}
	return ""
}

// applyListProviderModelsOverrides merges draft credentials from the settings
// UI into cfg so ListProviderModels can resolve providers that exist only in
// the frontend draft (not yet written to config.yaml). cfg must be a
// throwaway ToBuilderConfig result — this mutates its ProviderConfigs map.
func applyListProviderModelsOverrides(cfg *core.BuilderConfig, req ListProviderModelsRequest) error {
	existing, exists := cfg.LLM.ProviderConfigs[req.Provider]

	apiKey := req.APIKey
	if (apiKey == "" || apiKey == maskedAPIKey) && exists {
		apiKey = existing.APIKey
	}

	baseURL := req.BaseURL
	if baseURL == "" && exists {
		baseURL = existing.BaseURL
	}

	providerType := req.Type
	switch providerType {
	case "openai", "anthropic":
		// explicit draft transport
	case "":
		if exists {
			providerType = existing.ProviderType
		} else {
			// Unsaved compatible providers default to OpenAI Chat Completions.
			providerType = "openai"
		}
	default:
		return fmt.Errorf("unsupported provider type %q", providerType)
	}

	// Draft TLS pin (ADR-052): nil falls back to the saved value so a partial
	// draft (only credentials edited) does not silently drop the pin; a
	// non-nil value applies verbatim, so an explicit draft "" wins.
	tlsFingerprint := resolveTLSFingerprint(req.TLSFingerprint, existing.TLSFingerprint, exists)

	if !exists && baseURL == "" {
		// Fixed providers are always present in ToBuilderConfig; reaching here
		// means a named compatible provider that has not been saved yet.
		return fmt.Errorf("unknown provider: %s (set a base URL to fetch models before saving)", req.Provider)
	}

	cfg.LLM.ProviderConfigs[req.Provider] = core.BuilderProviderConfig{
		ProviderType:       providerType,
		APIKey:             apiKey,
		BaseURL:            baseURL,
		Models:             existing.Models,
		TLSFingerprint:     tlsFingerprint,
		OutputTokenReserve: existing.OutputTokenReserve,
	}
	return nil
}

// validTokenizerTypes enumerates the TokenizerType values the Configure dialog
// may select. It mirrors the switch in llm.NewTokenCounter (tokencount.go); the
// tiktoken/<encoding> forms are enumerated explicitly rather than accepting any
// "tiktoken/" prefix because the dialog offers a fixed, curated list (no free
// text). Keep in sync with NewTokenCounter if a new encoding is added.
var validTokenizerTypes = map[string]struct{}{
	"approximate":          {},
	"tiktoken/o200k_base":  {},
	"tiktoken/cl100k_base": {},
	"anthropic-api":        {},
}

// validFamilies enumerates the ModelFamily values the Configure dialog may
// select. Built from the llm.Family* string constants so this stays in sync
// with DetectFamily automatically.
var validFamilies = map[string]struct{}{
	string(llm.FamilyAnthropic):      {},
	string(llm.FamilyOpenAIFlagship): {},
	string(llm.FamilyOpenAIStandard): {},
	string(llm.FamilyGoogle):         {},
	string(llm.FamilyMistral):        {},
	string(llm.FamilyDeepSeek):       {},
	string(llm.FamilyOpenAICodex):    {},
	string(llm.FamilyQwen):           {},
	string(llm.FamilyGLM):            {},
	string(llm.FamilyKimi):           {},
	string(llm.FamilyDefault):        {},
}

// validProtocols enumerates the APIProtocol values the Configure dialog may
// select. Built from the llm.Protocol* string constants so this stays in sync
// with DetectProtocol automatically.
var validProtocols = map[string]struct{}{
	string(llm.ProtocolChatCompletions): {},
	string(llm.ProtocolResponses):       {},
	string(llm.ProtocolAnthropic):       {},
	string(llm.ProtocolGoogle):          {},
}

// modelFamilyDefault and modelProtocolDefault were removed: ResolveBuiltInModel
// always resolves Family/Protocol (via resolveFamily/resolveProtocol in the
// SDK) for known, fuzzy-matched, and unknown models alike, so defMeta.Family
// and defMeta.Protocol are always populated directly. Callers use those fields
// inline.

// GetModelConfig returns a single model's configurable parameters: the
// currently-effective values (override value when set, otherwise the built-in
// default) and the built-in factory defaults. Used by the per-model Configure
// dialog to pre-fill inputs and show what would change.
//
// Effective-value rule: a config override field of 0/""/nil means "inherit
// default", so the effective value is the override's set value or, when unset,
// the built-in default resolved via llm.ResolveBuiltInModel (network-free:
// built-in catalog or the 128000/32768 fallback for unknown models).
// ResolveBuiltInModel always resolves Family/Protocol (including detected
// values for unknown models), so those fields are never empty.
func (f *FrontendAPI) GetModelConfig(model string) (ModelConfigResponse, error) {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		return ModelConfigResponse{}, errors.New("config not initialized")
	}

	defMeta, _ := llm.ResolveBuiltInModel(model)

	// defMeta.Capabilities is a pointer (nil = inherit); the response DTO
	// carries the effective value type. ResolveBuiltInModel always resolves
	// to non-nil capabilities (catalog or the optimistic fallback), but guard
	// so a future nil-returning path degrades to all-false instead of panicking.
	defCaps := llm.ModelCapabilities{}
	if defMeta.Capabilities != nil {
		defCaps = *defMeta.Capabilities
	}

	resp := ModelConfigResponse{
		Model:                model,
		ContextWindow:        defMeta.ContextWindow,
		OutputLimit:          defMeta.OutputLimit,
		TokenizerType:        defMeta.TokenizerType,
		Family:               defMeta.Family,
		Protocol:             string(defMeta.Protocol),
		Capabilities:         defCaps,
		DefaultContextWindow: defMeta.ContextWindow,
		DefaultOutputLimit:   defMeta.OutputLimit,
		DefaultTokenizerType: defMeta.TokenizerType,
		DefaultFamily:        defMeta.Family,
		DefaultProtocol:      string(defMeta.Protocol),
		DefaultCapabilities:  defCaps,
	}

	if override, ok := f.config.LLM.Models[model]; ok {
		resp.HasOverride = true
		if override.ContextWindow > 0 {
			resp.ContextWindow = override.ContextWindow
		}
		if override.OutputLimit > 0 {
			resp.OutputLimit = override.OutputLimit
		}
		if override.TokenizerType != "" {
			resp.TokenizerType = override.TokenizerType
		}
		if override.Family != "" {
			resp.Family = override.Family
		}
		if override.Protocol != "" {
			resp.Protocol = override.Protocol
		}
		if override.Capabilities != nil {
			resp.Capabilities = *override.Capabilities
		}
	}

	return resp, nil
}

// SetModelConfig persists per-model parameter overrides from the Configure
// dialog. Only fields that differ from the built-in default are stored (a field
// equal to the default is recorded as its "inherit" sentinel — 0 for ints, ""
// for strings, nil for the capabilities pointer); when every field matches the
// default the model's entry is removed entirely so config.yaml stays minimal.
// The change is persisted and the LLM router rebuilt so the new values take
// effect immediately.
//
// TokenizerType/Family/Protocol are validated against curated enum sets — the
// dialog offers a fixed dropdown list (no free text), and the backend is the
// enforcement boundary so a stale or tampered client cannot persist an invalid
// enum string.
func (f *FrontendAPI) SetModelConfig(model string, req ModelConfigRequest) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	// ContextWindow/OutputLimit must be positive integers. The Input has
	// min={1} but that is advisory only; a negative would otherwise be
	// persisted verbatim to config.yaml as nonsensical state.
	if req.ContextWindow < 1 || req.OutputLimit < 1 {
		return fmt.Errorf("context window and output limit must be positive integers, got %d/%d",
			req.ContextWindow, req.OutputLimit)
	}

	// TokenizerType/Family/Protocol are selected from fixed dropdown lists; the
	// backend is the enforcement boundary. Empty is allowed (means "inherit").
	if req.TokenizerType != "" {
		if _, ok := validTokenizerTypes[req.TokenizerType]; !ok {
			return fmt.Errorf("invalid tokenizer type %q", req.TokenizerType)
		}
	}
	if req.Family != "" {
		if _, ok := validFamilies[req.Family]; !ok {
			return fmt.Errorf("invalid family %q", req.Family)
		}
	}
	if req.Protocol != "" {
		if _, ok := validProtocols[req.Protocol]; !ok {
			return fmt.Errorf("invalid protocol %q", req.Protocol)
		}
	}

	defMeta, _ := llm.ResolveBuiltInModel(model)
	defCaps := llm.ModelCapabilities{}
	if defMeta.Capabilities != nil {
		defCaps = *defMeta.Capabilities
	}

	// Record the "inherit" sentinel for any field that matches the built-in
	// default — buildRouter seeds the override PARTIAL (only explicitly set
	// fields), and the registry treats a zero/empty sentinel as "inherit from
	// lower tiers", so storing the default value verbatim would be redundant.
	// Capabilities is compared by value (nil = inherit).
	var newCW, newOL int
	var newTok, newFam, newProto string
	var newCaps *llm.ModelCapabilities

	if req.ContextWindow != defMeta.ContextWindow {
		newCW = req.ContextWindow
	}
	if req.OutputLimit != defMeta.OutputLimit {
		newOL = req.OutputLimit
	}
	if req.TokenizerType != defMeta.TokenizerType {
		newTok = req.TokenizerType
	}
	if req.Family != defMeta.Family {
		newFam = req.Family
	}
	if req.Protocol != string(defMeta.Protocol) {
		newProto = req.Protocol
	}
	if req.Capabilities != nil && *req.Capabilities != defCaps {
		newCaps = req.Capabilities
	}

	if f.config.LLM.Models == nil {
		f.config.LLM.Models = make(map[string]config.ModelOverride)
	}

	if newCW == 0 && newOL == 0 && newTok == "" && newFam == "" && newProto == "" && newCaps == nil {
		// Everything matches the built-in default — drop the override so the
		// model resolves purely from the built-in catalog.
		delete(f.config.LLM.Models, model)
	} else {
		f.config.LLM.Models[model] = config.ModelOverride{
			ContextWindow: newCW,
			OutputLimit:   newOL,
			TokenizerType: newTok,
			Family:        newFam,
			Protocol:      newProto,
			Capabilities:  newCaps,
		}
	}

	if err := f.persistConfig(); err != nil {
		return fmt.Errorf("failed to persist model config: %w", err)
	}

	// Clear any config load errors since settings are now valid.
	f.configLoadErrors = nil

	// Rebuild the LLM router so the new override takes effect for new sessions.
	if b := f.builder(); b != nil {
		bcfg := ToBuilderConfig(f.config, f.modelProfilesCatalog())
		if err := b.RebuildRouter(bcfg); err != nil {
			f.log().Warn("failed to rebuild LLM router after model config update", "error", err)
		}
	}

	return nil
}

// persistConfig saves the current in-memory config to disk. Every successful
// config mutation funnels through here, so it is also the single point that
// announces config changes to the frontend (see emitConfigUpdated).
func (f *FrontendAPI) persistConfig() error {
	if f.configPath == "" || f.config == nil {
		return errors.New("config path or config not set")
	}
	err := config.Save(f.config, f.configPath)
	// Announce even when the disk write failed: the in-memory config (what
	// GetConfig serves) has already changed, so refetching consumers stay
	// consistent with what the backend will report.
	f.emitConfigUpdated()
	return err
}

// emitConfigUpdated notifies the frontend that the config was mutated via an
// Update* RPC. Dispatched on a fresh goroutine because persistConfig runs
// under whatever lock its caller holds (configMu.Lock for most setters), and
// emitEvent is a synchronous Wails webview dispatch — a config lock must
// never be held across it (readers such as GetConfig would convoy behind the
// event delivery). Nil-guarded: most tests exercise persistConfig without
// wiring emitEvent.
func (f *FrontendAPI) emitConfigUpdated() {
	if f.emitEvent == nil {
		return
	}
	go f.emitEvent(EventConfigUpdated)
}

// maskAPIKey returns a masked representation of an API key for display.
func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if strings.HasPrefix(key, "${") && strings.HasSuffix(key, "}") {
		return key
	}
	return maskedAPIKey
}

// nonNilStringSlice returns an empty slice if the input is nil,
// ensuring JSON serialization produces [] instead of null.
func nonNilStringSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
