package backend

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/proxy"
	"github.com/v0lka/c0wrk/core/smallllm"
	"github.com/v0lka/sp4rk/llm"
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
		Proxy: ProxySettingsResponse{
			Enabled:    f.config.Proxy.Enabled,
			URL:        proxy.MaskURL(f.config.Proxy.URL),
			BypassList: nonNilStringSlice(f.config.Proxy.BypassList),
			TLSCertDir: f.config.Proxy.TLSCertDir,
		},
		Experimental: ExperimentalSettingsResponse{
			Enabled: f.config.Experimental.Enabled,
		},
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
			APIKey:  maskAPIKey(cfg.APIKey),
			BaseURL: cfg.BaseURL,
			Models:  cfg.Models,
		}
	}
	for name, cfg := range f.config.LLM.AnthropicCompatible {
		resp.AnthropicCompatible[name] = ConfigProviderFull{
			APIKey:  maskAPIKey(cfg.APIKey),
			BaseURL: cfg.BaseURL,
			Models:  cfg.Models,
		}
	}
	if f.subscriptions != nil {
		if catalog := f.subscriptions.catalog("chatgpt"); len(catalog) > 0 {
			resp.Subscriptions = []SubscriptionProviderModels{{
				Provider: "chatgpt_subscription",
				Models:   catalog,
				Enabled:  f.subscriptions.models("chatgpt"),
			}}
		}
	}
	return resp
}

// collectAllModels iterates all enabled provider models and builds ModelInfo
// entries. Managed subscription models are included only while their encrypted
// credential record is present; they are never copied into the API-key config.
// When reg is non-nil, family and reasoning metadata are resolved from the registry.
//
// GUARANTEE: collectAllModels is network-free — it resolves every model via
// ModelRegistry.ResolveLocal, which serves overrides, built-ins, fuzzy
// matches, and cached entries purely from memory and returns fallback
// defaults for unknown models. No HTTP probes, no registered sources, no
// blocking I/O. GetConfig must remain a pure in-memory read: it runs on every
// settings open, and a model list containing an unknown model must not stall
// the UI behind a network timeout.
//
// Entries are keyed by composite (provider, model) so that two providers
// exposing the same bare model name both appear — the frontend uses the
// composite "provider/name" value to select a specific provider while
// displaying the bare model name.
func (f *FrontendAPI) collectAllModels(reg *llm.ModelRegistry) []ModelInfo {
	// GetAllProviderConfigs returns providers in deterministic order
	// (anthropic, chatgpt, then sorted openai_compatible, then sorted anthropic_compatible).
	providers := f.config.LLM.GetAllProviderConfigs()
	if f.subscriptions != nil {
		if models := f.subscriptions.models("chatgpt"); len(models) > 0 {
			providers = append(providers, config.ProviderWithModels{Name: "chatgpt_subscription", Models: models})
		}
	}

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

func subscriptionModelEnabled(enabled, catalog []string, model string) bool {
	if enabled == nil {
		enabled = catalog
	}
	for _, configured := range enabled {
		if configured == model {
			return true
		}
	}
	return false
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
			if existing, ok := candidate.OpenAICompatible[name]; ok {
				if apiKey == maskedAPIKey || apiKey == "" {
					apiKey = existing.APIKey
				}
				outputReserve = existing.OutputTokenReserve
			}
			newMap[name] = config.OpenAICompatibleConfig{
				APIKey:             apiKey,
				BaseURL:            ocReq.BaseURL,
				Models:             ocReq.Models,
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
			if existing, ok := candidate.AnthropicCompatible[name]; ok {
				if apiKey == maskedAPIKey || apiKey == "" {
					apiKey = existing.APIKey
				}
				outputReserve = existing.OutputTokenReserve
			}
			newMap[name] = config.AnthropicCompatibleConfig{
				APIKey:             apiKey,
				BaseURL:            acReq.BaseURL,
				Models:             acReq.Models,
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

	if req.SubscriptionModels != nil {
		if enabled, ok := req.SubscriptionModels["chatgpt_subscription"]; ok {
			if f.subscriptions == nil {
				f.configMu.Unlock()
				return errors.New("subscription sign-in is unavailable")
			}
			catalog := make(map[string]struct{})
			for _, model := range f.subscriptions.catalog("chatgpt") {
				catalog[model] = struct{}{}
			}
			for _, model := range enabled {
				if _, known := catalog[model]; !known {
					f.configMu.Unlock()
					return fmt.Errorf("unknown ChatGPT subscription model %q", model)
				}
			}
			candidate.Subscriptions.ChatGPT.EnabledModels = slices.Compact(slices.Clone(enabled))
		}
	}

	// A first-run config intentionally has no default until setup finishes.
	// Once a default exists, however, every candidate must still resolve after
	// all requested provider/model replacements have been applied. Validate
	// before committing so a dangling replacement cannot change any state.
	if candidate.DefaultModel != "" {
		provider, model, managed := llm.ParseCompositeModelID(candidate.DefaultModel)
		// A connected managed subscription owns its models. Their credentials
		// intentionally live outside YAML, so config-only resolution cannot
		// validate them; the candidate's selected model list must contain the
		// model instead.
		managedValid := managed && provider == "chatgpt_subscription" && f.subscriptions != nil && f.subscriptions.connected("chatgpt") && subscriptionModelEnabled(candidate.Subscriptions.ChatGPT.EnabledModels, f.subscriptions.catalog("chatgpt"), model)
		if !managedValid {
			if _, _, err := candidate.ResolveDefaultModelProvider(); err != nil {
				f.configMu.Unlock()
				return fmt.Errorf("invalid LLM configuration: default_model would be unresolved: %w", err)
			}
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
	// under configMu.Lock (UpdateSmallLLMConfig, SetModelConfig) cannot run
	// in between, so this rebuild can never apply a snapshot that predates
	// their changes and roll the router back. RLock stays shared with
	// readers, so GetConfig is still never convoyed behind the rebuild, and
	// RebuildJudge/RebuildRouter are core calls that never re-enter
	// FrontendAPI, so holding the RLock across them cannot deadlock.
	if b := f.builder(); b != nil {
		f.configMu.RLock()
		fresh := ToBuilderConfig(f.config)
		if f.subscriptions != nil {
			f.subscriptions.augment(fresh)
		}
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
		b.UpdateSearchTool(ToBuilderConfig(f.config))
	}

	return nil
}

// UpdateProxySettings updates proxy configuration at runtime and propagates
// the change to all subsystems (LLM providers, web tools, MCP, child processes).
func (f *FrontendAPI) UpdateProxySettings(settings ProxySettingsRequest) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
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

	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist proxy settings", "error", err)
	}

	// Rebuild proxy transport and propagate to all subsystems.
	if b := f.builder(); b != nil {
		bcfg := ToBuilderConfig(f.config)
		if err := b.RebuildProxy(context.Background(), bcfg); err != nil {
			f.log().Warn("failed to rebuild proxy after settings update", "error", err)
			return fmt.Errorf("proxy rebuild failed: %w", err)
		}
	}

	return nil
}

// UpdateExperimentalFeatures toggles the master experimental-features switch
// at runtime. It persists the change and rebuilds the LLM router so the
// Small-LLM profile (one of the gated features) takes effect for new sessions
// without an app restart. RESEARCH mode is gated at its RPC boundary instead
// (EnableResearch / GetResearchStatus / GetResearchGraph), so no rebuild is
// needed for it here.
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

	// Rebuild the LLM router so the Small-LLM profile (sampling overrides,
	// tool matching, context management) is applied or removed immediately.
	if b := f.builder(); b != nil {
		routerCfg := ToBuilderConfig(f.config)
		if f.subscriptions != nil {
			f.subscriptions.augment(routerCfg)
		}
		if err := b.RebuildRouter(routerCfg); err != nil {
			f.log().Warn("failed to rebuild LLM router after experimental-features toggle", "error", err)
		}
	}

	// Keep the session manager's Small-LLM snapshot in sync so agent_metrics
	// events created afterwards are annotated with the effective profile.
	if app := f.app; app != nil {
		if mgr := app.Manager(); mgr != nil {
			mgr.SetSmallLLMProfile(effectiveSmallLLMConfig(f.config))
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
		builderCfg := ToBuilderConfig(f.config)
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

// GetSmallLLMConfig returns the current effective small-LLM profile
// configuration. Returns a zero value when config is not yet initialized.
func (f *FrontendAPI) GetSmallLLMConfig() SmallLLMConfigResponse {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		return SmallLLMConfigResponse{
			EssentialTools: SmallLLMEssentialToolsResp{AlwaysPresent: []string{}},
		}
	}
	return smallLLMToResponse(f.config.SmallLLM)
}

// UpdateSmallLLMConfig validates, persists, and applies a new small-LLM
// profile. Validation runs before any mutation so an invalid payload produces
// no partial write (the in-memory config and config.yaml are untouched). After
// a successful persist the LLM router is rebuilt so the new profile takes
// effect for new sessions without an app restart.
func (f *FrontendAPI) UpdateSmallLLMConfig(cfg SmallLLMConfigResponse) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	// Reconcile a stale slot budget BEFORE validation (systemic fix): a
	// config persisted by an older build can carry a max_tools below the
	// guaranteed set (always_present ∪ protected) — e.g. update_checklist
	// moved into the protected set and grew the union past an old cap, while
	// the stored always_present list kept stale entries. validateSmallLLMConfig
	// would then reject EVERY save from the settings panel — including a bare
	// master-toggle flip — with no UI-only remedy (the protected tools are
	// locked chips the user cannot un-pin), locking the profile behind a
	// hand-edited YAML. The guaranteed set is never trimmed at runtime
	// anyway, so raising the cap to its size only makes the stored budget
	// honest; the next successful persist writes the reconciled value.
	if r := reconcileSmallLLMCap(&cfg); r.from != r.to {
		f.log().Info("small-LLM: max_tools was below the guaranteed tool count; raised to the guaranteed set size (stale config reconciled)",
			"from", r.from, "to", r.to)
	}

	// Validate before mutation — a bad payload must not partially overwrite
	// the persisted config.
	if err := validateSmallLLMConfig(cfg); err != nil {
		return err
	}

	// Snapshot the previous profile so it can be restored if the persist
	// fails — otherwise the in-memory config would hold the unpersisted value
	// and the UI's revert-on-failure (GetSmallLLMConfig) would read it back,
	// silently keeping the rejected change.
	prev := f.config.SmallLLM
	f.config.SmallLLM = responseToSmallLLM(cfg)

	if err := f.persistConfig(); err != nil {
		f.config.SmallLLM = prev
		return fmt.Errorf("failed to persist small-LLM config: %w", err)
	}

	// Clear any config load errors since settings are now valid.
	f.configLoadErrors = nil

	// Rebuild the LLM router so the updated profile applies without restart.
	if b := f.builder(); b != nil {
		routerCfg := ToBuilderConfig(f.config)
		if f.subscriptions != nil {
			f.subscriptions.augment(routerCfg)
		}
		if err := b.RebuildRouter(routerCfg); err != nil {
			f.log().Warn("failed to rebuild LLM router after small-LLM config update", "error", err)
		}
	}

	// Keep the session manager's Small-LLM snapshot in sync so agent_metrics
	// events created afterwards are annotated with the updated profile.
	if app := f.app; app != nil {
		if mgr := app.Manager(); mgr != nil {
			mgr.SetSmallLLMProfile(f.config.SmallLLM)
		}
	}

	return nil
}

// validSmallLLMReasoningEfforts enumerates the accepted reasoning_effort
// values for the small-LLM sampling variant. Empty means "inherit the model's
// default" and is always allowed.
var validSmallLLMReasoningEfforts = map[string]struct{}{
	"":       {},
	"off":    {},
	"low":    {},
	"medium": {},
}

// validateSmallLLMConfig validates a small-LLM profile before it is persisted.
// Returns an error for any constraint violation; the caller must reject the
// update without mutating config when this returns non-nil.
func validateSmallLLMConfig(cfg SmallLLMConfigResponse) error {
	// Essential tools: a non-empty curated set and sane tool cap are required
	// when the variant is active.
	if cfg.EssentialTools.Enabled {
		// always_present may be empty: protected orchestration tools
		// (finish, fact memory, ask_user) and every MCP tool are always kept
		// implicitly by SelectTools, so an empty list is valid. max_tools is a
		// slot budget (0 = unlimited) and must simply be non-negative.
		if cfg.EssentialTools.MaxTools < 0 {
			return fmt.Errorf("small_llm.essential_tools.max_tools must be non-negative, got %d", cfg.EssentialTools.MaxTools)
		}
		// The guaranteed set (always_present ∪ protected orchestration tools;
		// MCP tools join at runtime) is never trimmed by SelectTools, so a cap
		// smaller than the guaranteed count would leave zero router-matched
		// slots and the result would silently exceed the budget. Reject up
		// front with an actionable message. Note: UpdateSmallLLMConfig
		// reconciles stale caps to the guaranteed count BEFORE calling this
		// validator (see reconcileSmallLLMCap), so the save path self-heals
		// instead of failing; this check remains the invariant's safety net.
		if cfg.EssentialTools.MaxTools > 0 {
			guaranteed := unionAlwaysPresent(cfg.EssentialTools.AlwaysPresent, smallllm.ProtectedToolNames())
			if len(guaranteed) > cfg.EssentialTools.MaxTools {
				return fmt.Errorf(
					"small_llm.essential_tools.max_tools (%d) is smaller than the guaranteed tool count (%d = always_present ∪ protected orchestration tools); guaranteed tools are never trimmed — raise max_tools, trim always_present, or set max_tools to 0 for unlimited",
					cfg.EssentialTools.MaxTools, len(guaranteed),
				)
			}
		}
	}

	// Sampling: each parameter uses zero as the "inherit the vendor preset"
	// sentinel, so zero is always valid. Any explicitly set (non-zero) value
	// must fall in its supported range — regardless of whether the variant is
	// currently enabled, so a stored out-of-range value cannot go live the
	// moment the toggle flips on.
	if cfg.Sampling.Temperature < 0 {
		return fmt.Errorf("small_llm.sampling.temperature must be > 0 when set, got %v (0 inherits the vendor preset)", cfg.Sampling.Temperature)
	}
	if cfg.Sampling.TopP < 0 || cfg.Sampling.TopP > 1 {
		return fmt.Errorf("small_llm.sampling.top_p must be in the range (0, 1] when set, got %v (0 inherits the vendor preset)", cfg.Sampling.TopP)
	}
	if cfg.Sampling.TopK < 0 {
		return fmt.Errorf("small_llm.sampling.top_k must be >= 1 when set, got %d (0 inherits the vendor preset)", cfg.Sampling.TopK)
	}
	if rp := cfg.Sampling.RepetitionPenalty; rp != 0 && (rp < 1 || rp > 2) {
		return fmt.Errorf("small_llm.sampling.repetition_penalty must be in the range [1, 2] when set, got %v (0 inherits the vendor preset)", rp)
	}
	// Qwen card: presence_penalty 0–2 is the sanctioned anti-repetition
	// lever (instruct default 1.5); values above 2 increase language mixing.
	if pp := cfg.Sampling.PresencePenalty; pp != 0 && (pp < 0 || pp > 2) {
		return fmt.Errorf("small_llm.sampling.presence_penalty must be in the range [0, 2] when set, got %v (0 inherits the vendor preset)", pp)
	}
	if _, ok := validSmallLLMReasoningEfforts[cfg.Sampling.ReasoningEffort]; !ok {
		return fmt.Errorf("small_llm.sampling.reasoning_effort %q is invalid (allowed: off, low, medium)", cfg.Sampling.ReasoningEffort)
	}

	// Loop hardening: every threshold must be non-negative.
	lh := cfg.LoopHardening
	thresholds := []int{
		lh.RepeatNudgeThreshold,
		lh.ParseErrorAbortThreshold,
		lh.FruitlessNudgeThreshold,
		lh.FruitlessAbortThreshold,
		lh.SameToolRepeatNudgeThreshold,
	}
	for _, t := range thresholds {
		if t < 0 {
			return errors.New("small_llm.loop_hardening thresholds must be non-negative")
		}
	}
	// When the variant is active, thresholds must be positive (>= 1).
	if lh.Enabled {
		for _, t := range thresholds {
			if t < 1 {
				return errors.New("small_llm.loop_hardening thresholds must be positive when loop_hardening is enabled")
			}
		}
	}

	// Context-management variant: non-negative always; sane tight ranges when
	// the variant is active.
	ctx := cfg.Context
	contextInts := []int{
		ctx.Compaction.KeepLast,
		ctx.Compaction.BlockSize,
		ctx.Compaction.TriggerPercent,
		ctx.ToolOutputKeepLastN,
		ctx.OutputTokenReserve,
	}
	for _, v := range contextInts {
		if v < 0 {
			return errors.New("small_llm.context values must be non-negative")
		}
	}
	if ctx.Enabled {
		if ctx.Compaction.KeepLast < 2 {
			return fmt.Errorf("small_llm.context.compaction.keep_last must be >= 2 when context is enabled, got %d", ctx.Compaction.KeepLast)
		}
		if ctx.Compaction.BlockSize < 2 {
			return fmt.Errorf("small_llm.context.compaction.block_size must be >= 2 when context is enabled, got %d", ctx.Compaction.BlockSize)
		}
		if ctx.Compaction.TriggerPercent < 1 || ctx.Compaction.TriggerPercent >= 100 {
			return fmt.Errorf("small_llm.context.compaction.trigger_percent must be in [1, 100) when context is enabled, got %d", ctx.Compaction.TriggerPercent)
		}
		if ctx.ToolOutputKeepLastN < 1 {
			return fmt.Errorf("small_llm.context.tool_output_keep_last_n must be >= 1 when context is enabled, got %d", ctx.ToolOutputKeepLastN)
		}
		if ctx.OutputTokenReserve < 1 {
			return fmt.Errorf("small_llm.context.output_token_reserve must be >= 1 when context is enabled, got %d", ctx.OutputTokenReserve)
		}
	}

	return nil
}

// smallLLMToResponse converts the config-level SmallLLMConfig into the
// JSON-tagged DTO returned to the UI. AlwaysPresent is normalized to a non-nil
// slice so JSON serialization yields [] instead of null. The protected
// orchestration tools (finish, fact memory, ask_user) are unioned into the
// response's always_present so the UI can render them as permanently present
// ("locked") — they are always kept by SelectTools regardless of the user's
// list.
func smallLLMToResponse(c config.SmallLLMConfig) SmallLLMConfigResponse {
	return SmallLLMConfigResponse{
		Enabled: c.Enabled,
		EssentialTools: SmallLLMEssentialToolsResp{
			Enabled:             c.EssentialTools.Enabled,
			AlwaysPresent:       nonNilStringSlice(unionAlwaysPresent(c.EssentialTools.AlwaysPresent, smallllm.ProtectedToolNames())),
			MaxTools:            c.EssentialTools.MaxTools,
			CompactDescriptions: c.EssentialTools.CompactDescriptions,
			// Read-only metadata so the UI can render protected tools as
			// locked chips without duplicating the backend list. Ignored on
			// write (responseToSmallLLM does not map it back).
			ProtectedTools: nonNilStringSlice(smallllm.ProtectedToolNames()),
		},
		SystemPrompt: SmallLLMSystemPromptResp{
			Lite:              c.SystemPrompt.Lite,
			FewShot:           c.SystemPrompt.FewShot,
			ReasoningScaffold: c.SystemPrompt.ReasoningScaffold,
		},
		Sampling: SmallLLMSamplingResp{
			Enabled:           c.Sampling.Enabled,
			Temperature:       c.Sampling.Temperature,
			TopP:              c.Sampling.TopP,
			TopK:              c.Sampling.TopK,
			RepetitionPenalty: c.Sampling.RepetitionPenalty,
			PresencePenalty:   c.Sampling.PresencePenalty,
			ReasoningEffort:   c.Sampling.ReasoningEffort,
		},
		LoopHardening: SmallLLMLoopHardeningResp{
			Enabled:                      c.LoopHardening.Enabled,
			RepeatNudgeThreshold:         c.LoopHardening.RepeatNudgeThreshold,
			ParseErrorAbortThreshold:     c.LoopHardening.ParseErrorAbortThreshold,
			FruitlessNudgeThreshold:      c.LoopHardening.FruitlessNudgeThreshold,
			FruitlessAbortThreshold:      c.LoopHardening.FruitlessAbortThreshold,
			SameToolRepeatNudgeThreshold: c.LoopHardening.SameToolRepeatNudgeThreshold,
		},
		Context: SmallLLMContextResp{
			Enabled: c.Context.Enabled,
			Compaction: SmallLLMCompactionResp{
				KeepLast:       c.Context.Compaction.KeepLast,
				BlockSize:      c.Context.Compaction.BlockSize,
				TriggerPercent: c.Context.Compaction.TriggerPercent,
			},
			ToolOutputKeepLastN: c.Context.ToolOutputKeepLastN,
			OutputTokenReserve:  c.Context.OutputTokenReserve,
		},
	}
}

// responseToSmallLLM converts the UI DTO back into the config-level struct so
// it can be persisted to config.yaml.
func responseToSmallLLM(r SmallLLMConfigResponse) config.SmallLLMConfig {
	return config.SmallLLMConfig{
		Enabled: r.Enabled,
		EssentialTools: config.EssentialToolsConfig{
			Enabled:             r.EssentialTools.Enabled,
			AlwaysPresent:       r.EssentialTools.AlwaysPresent,
			MaxTools:            r.EssentialTools.MaxTools,
			CompactDescriptions: r.EssentialTools.CompactDescriptions,
		},
		SystemPrompt: config.SystemPromptConfig{
			Lite:              r.SystemPrompt.Lite,
			FewShot:           r.SystemPrompt.FewShot,
			ReasoningScaffold: r.SystemPrompt.ReasoningScaffold,
		},
		Sampling: config.SmallLLMSamplingConfig{
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
		Context: config.SmallLLMContextConfig{
			Enabled: r.Context.Enabled,
			Compaction: config.SmallLLMCompactionConfig{
				KeepLast:       r.Context.Compaction.KeepLast,
				BlockSize:      r.Context.Compaction.BlockSize,
				TriggerPercent: r.Context.Compaction.TriggerPercent,
			},
			ToolOutputKeepLastN: r.Context.ToolOutputKeepLastN,
			OutputTokenReserve:  r.Context.OutputTokenReserve,
		},
	}
}

// capReconciliation reports a max_tools adjustment made by
// reconcileSmallLLMCap: from == to means no change was needed.
type capReconciliation struct {
	from int
	to   int
}

// reconcileSmallLLMCap raises EssentialTools.MaxTools to the guaranteed tool
// count (always_present ∪ protected orchestration tools) when a stale
// persisted cap sits below it. Guaranteed tools are never trimmed by
// SelectTools, so a cap below that count is unenforceable — it only breaks
// validation (see UpdateSmallLLMConfig). Negative caps and the unlimited
// sentinel (0) are passed through untouched: validation still rejects
// negatives and honors 0 as unlimited.
func reconcileSmallLLMCap(cfg *SmallLLMConfigResponse) capReconciliation {
	et := &cfg.EssentialTools
	if et.MaxTools <= 0 {
		return capReconciliation{from: et.MaxTools, to: et.MaxTools}
	}
	guaranteed := len(unionAlwaysPresent(et.AlwaysPresent, smallllm.ProtectedToolNames()))
	if guaranteed <= et.MaxTools {
		return capReconciliation{from: et.MaxTools, to: et.MaxTools}
	}
	from := et.MaxTools
	et.MaxTools = guaranteed
	return capReconciliation{from: from, to: guaranteed}
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
func (f *FrontendAPI) ListProviderModels(provider string) ([]string, error) {
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
	cfg := ToBuilderConfig(f.config)
	f.configMu.RUnlock()

	return b.ListProviderModels(context.Background(), provider, cfg)
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
		bcfg := ToBuilderConfig(f.config)
		if f.subscriptions != nil {
			f.subscriptions.augment(bcfg)
		}
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

// unionAlwaysPresent merges two tool-name lists, deduplicating by name. The
// primary list's order is preserved and protected names that are not already
// present are appended (so the UI can render them as locked, in a stable
// position). Used to surface the protected orchestration tools alongside the
// user's always-present list without storing them in config.
func unionAlwaysPresent(primary, extra []string) []string {
	if len(primary) == 0 && len(extra) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(primary)+len(extra))
	out := make([]string, 0, len(primary)+len(extra))
	for _, n := range primary {
		if _, ok := seen[n]; !ok {
			seen[n] = struct{}{}
			out = append(out, n)
		}
	}
	for _, n := range extra {
		if _, ok := seen[n]; !ok {
			seen[n] = struct{}{}
			out = append(out, n)
		}
	}
	return out
}
