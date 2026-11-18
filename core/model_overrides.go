package core

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/oneshot"
)

// Service-model override resolution (llm.service_models): the single seam that
// turns the configured "key → composite id" map into an immutable per-build set
// of pinned (provider, model) pairs, checked against the router being built.
// Keys are opaque strings here — the canonical service-key set lives in
// backend/config (it cannot be imported from core); the three keys core wires
// itself are the exported constants below, test-enforced in lockstep with
// config (backend's TestServiceModelKeysInLockstep).
//
// Resolution rules (fail-soft, mirroring the delegation composite rule of
// specs/domains/orchestration/delegation.md): a value must be a full
// "provider/model" composite (split on the FIRST "/" — the bare model may
// itself contain "/"), and its provider must be one the router configured.
// Provider identity is checked provider-name-only via
// Router.ProviderForModel — the same predicate modelOverrideValidator applies
// to composites; the bare model need not appear in the provider's list. A key
// that fails either check is absent from the resolved set, so its consumer
// falls back to that router's active model (the session's current model on
// session builders, the global default on the app-level builder); unknown
// providers produce ONE aggregated Warn naming every dropped key, its
// provider, and the known providers.

// Core-side service-model keys for the consumers core itself wires (judge,
// router, reflector). The canonical key set (with its config validation)
// lives in backend/config — core cannot import it, so these constants mirror
// those values; the lockstep is enforced by backend's
// TestServiceModelKeysInLockstep, because a typo on either side would
// silently drop the pin at resolution (backend/config/service_models.go,
// ADR-131).
const (
	ServiceModelKeyJudge     = "judge"
	ServiceModelKeyRouter    = "router"
	ServiceModelKeyReflector = "reflector"
)

// ServiceModelOverride is one resolved service-model pin.
type ServiceModelOverride struct {
	Provider  string // router-configured provider name
	Model     string // bare model name (may itself contain "/")
	Composite string // "provider/model" — the pin reported by ActiveModel
}

// ServiceModelOverrides is the immutable per-build resolution result. The zero
// value and nil both behave as an empty set, so an unconfigured section is a
// no-op for every consumer.
type ServiceModelOverrides struct {
	entries map[string]ServiceModelOverride
}

// Lookup returns the resolved pin for a service-model key; ok=false when the
// key was absent, unconfigured, or dropped at resolution (use the session
// model).
func (o *ServiceModelOverrides) Lookup(key string) (ServiceModelOverride, bool) {
	if o == nil {
		return ServiceModelOverride{}, false
	}
	entry, ok := o.entries[key]
	return entry, ok
}

// IsEmpty reports whether no key resolved.
func (o *ServiceModelOverrides) IsEmpty() bool {
	return o == nil || len(o.entries) == 0
}

// resolveServiceModelOverrides resolves a configured service-model map against
// llmRouter. Returns nil when there is nothing to resolve (nil router or
// empty/absent section). Malformed values are dropped with a per-key Warn
// (defensive — backend/config already rejects them at load); unknown providers
// are dropped with the single aggregated Warn. Keys are visited in sorted
// order so warnings and resolution are deterministic.
func resolveServiceModelOverrides(llmRouter *llm.Router, configured map[string]string, logger *slog.Logger) *ServiceModelOverrides {
	if llmRouter == nil || len(configured) == 0 {
		return nil
	}
	entries := make(map[string]ServiceModelOverride, len(configured))
	type droppedUnknown struct{ key, provider string }
	var unknowns []droppedUnknown
	for _, key := range slices.Sorted(maps.Keys(configured)) {
		value := strings.TrimSpace(configured[key])
		provider, model, composite := llm.ParseCompositeModelID(value)
		if !composite || provider == "" || model == "" {
			if logger != nil {
				logger.Warn("service model override is not a \"provider/model\" composite — key falls back to the router's active model",
					"key", key, "value", configured[key])
			}
			continue
		}
		// Provider-name-only membership: on a composite id
		// ProviderForModel checks the provider map only — the same
		// predicate modelOverrideValidator applies.
		if _, ok := llmRouter.ProviderForModel(value); !ok {
			unknowns = append(unknowns, droppedUnknown{key: key, provider: provider})
			continue
		}
		entries[key] = ServiceModelOverride{
			Provider:  provider,
			Model:     model,
			Composite: llm.CompositeModelID(provider, model),
		}
	}
	if len(unknowns) > 0 {
		if logger != nil {
			pairs := make([]string, 0, len(unknowns))
			for _, u := range unknowns {
				pairs = append(pairs, u.key+" → "+u.provider)
			}
			logger.Warn("service model overrides name unknown providers — keys fall back to the router's active model",
				"unknown", pairs,
				"known_providers", strings.Join(llmRouter.KnownProviders(), ", "))
		}
	}
	if len(entries) == 0 {
		return nil
	}
	return &ServiceModelOverrides{entries: entries}
}

// pinnedModelCaller wraps an agent.LLMCaller so every Call pins the resolved
// provider+model while exposing the pin as the caller's active model. The
// pinning itself is sp4rk's modelProviderOverrideCaller contract (ADR-012
// per-request provider routing; the router fills req.Model only when empty, so
// a pre-set req.Model bypasses active-model selection) — this wrapper reuses
// agent.NewModelProviderOverrideCaller as its inner layer and adds the
// ActiveModel reporting the ToolJudge's activeModelSource probe looks for.
//
// The pin is immune to Router.SetModel: every Call re-stamps req.Model (and
// req.Provider), so a mid-task switch never redirects a pinned service call.
// When effortStamp is set (router/reflector wraps only — see overrideWrap)
// every Call also re-stamps req.ReasoningEffort with the pin's own effort,
// the same immunity model: no session-side re-stamp path can diverge it.
type pinnedModelCaller struct {
	inner     agent.LLMCaller
	composite string
	// effortStamp, when non-nil, is written into req.ReasoningEffort on every
	// Call before the request leaves the wrapper. Nil for the oneshot service
	// sites (they compute the kind's own tier into the request before calling)
	// and for the judge (it resolves effort from the ActiveModel probe);
	// set by overrideWrap for the router/reflector pins.
	effortStamp *string
}

// Interface conformance: the wrapper is usable wherever the raw caller was
// (oneshot service calls, executor callers) and answers the judge's structural
// ActiveModel() string probe.
var (
	_ agent.LLMCaller                   = (*pinnedModelCaller)(nil)
	_ oneshot.Caller                    = (*pinnedModelCaller)(nil)
	_ interface{ ActiveModel() string } = (*pinnedModelCaller)(nil)
)

// newPinnedModelCaller pins provider+model onto every Call of inner. With both
// empty it degrades to a plain ActiveModel-reporting wrapper (sp4rk returns
// inner unchanged) — the resolver never produces such an entry.
func newPinnedModelCaller(inner agent.LLMCaller, provider, model string) *pinnedModelCaller {
	composite := provider
	if model != "" {
		composite = llm.CompositeModelID(provider, model)
	}
	return &pinnedModelCaller{
		inner:     agent.NewModelProviderOverrideCaller(inner, provider, model),
		composite: composite,
	}
}

// Call delegates to the pinned inner layer, which stamps the override onto the
// request before it reaches the router; the effort stamp (router/reflector
// pins) overwrites whatever effort the requesting component derived from its
// session identity, so the wire carries the pin model's spelling.
func (c *pinnedModelCaller) Call(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	if c.effortStamp != nil {
		req.ReasoningEffort = *c.effortStamp
	}
	return c.inner.Call(ctx, req)
}

// ActiveModel returns the composite pin, bare-model-shaped for consumers like
// the judge (which applies llm.BareModel itself).
func (c *pinnedModelCaller) ActiveModel() string {
	return c.composite
}

// applyServiceModelOverride resolves the llm.service_models pin for ONE
// service call: with a resolved entry the effective model becomes the pin's
// composite id — reported verbatim by the per-call telemetry log (logServiceCall
// "model") and fed to serviceReasoningEffort, which strips the provider prefix
// before family resolution — and the caller is wrapped in pinnedModelCaller so
// every request (each oneshot attempt included) re-stamps req.Provider/req.Model.
// Without an entry (unconfigured section, dropped key, nil set) both inputs pass
// through unchanged — byte-for-byte the pre-override behavior. Resolution MUST
// happen before the request (and its serviceReasoningEffort) is built.
//
// Builders resolve through their per-build set (applyServiceModelOverride on
// *OrchestratorBuilder, guarded by b.mu); Orchestrator-owned sites hand their
// o.serviceModels straight into the set-taking seams.
func applyServiceModelOverride(set *ServiceModelOverrides, kind ServiceKind, model string, caller oneshot.Caller) (string, oneshot.Caller) {
	entry, ok := set.Lookup(string(kind))
	if !ok {
		return model, caller
	}
	return entry.Composite, newPinnedModelCaller(caller, entry.Provider, entry.Model)
}

// applyServiceModelOverride resolves the builder's per-build llm.service_models
// pin for one builder-level service call (commit_message, optimize_extract,
// optimize_rewrite); title/prompt_suggest/compaction read the same set under
// b.mu where the free-function seam shape needs the set itself.
func (b *OrchestratorBuilder) applyServiceModelOverride(kind ServiceKind, model string, caller oneshot.Caller) (string, oneshot.Caller) {
	b.mu.RLock()
	set := b.serviceModels
	b.mu.RUnlock()
	return applyServiceModelOverride(set, kind, model, caller)
}

// ServiceModelPin resolves the builder-level llm.service_models pin for one
// service key against the cached router — the SAME resolveServiceModelOverrides
// resolution the call itself performs (same set, same fail-soft fallback: an
// unconfigured section, an unknown-provider key or a pre-build builder all
// answer ok=false, i.e. "the default model serves"). Exposed so the backend's
// embedded service gates can key the advisory warm on the EFFECTIVE call
// target (the pin when it resolves, the default otherwise) instead of
// re-implementing the resolution config-side and risking divergence.
func (b *OrchestratorBuilder) ServiceModelPin(kind string) (ServiceModelOverride, bool) {
	b.mu.RLock()
	set := b.serviceModels
	b.mu.RUnlock()
	return set.Lookup(kind)
}

// overrideWrap pins the resolved service-model override for key onto caller;
// without a resolved pin the caller passes through unchanged (the same
// instance — no extra layer), preserving the pre-override wiring exactly.
// Each consumer gets its own wrap so two consumers never share one pin.
//
// The router/reflector wraps also carry the pin's REASONING EFFORT: their
// requests serve the pin's model exclusively, so they must carry the pin's
// own tier-Off spelling (the component's one-shot policy re-resolved for the
// pin's family) — the session effort keys on the session model's family and
// is a foreign spelling for the pin (a qwen3 "off" sent to an OpenAI-family
// model is rejected or silently reinterpreted). Stamping per Call makes the
// effort immune to every session-side re-stamp path (build time,
// ApplyRequestOverrides, resume — ADR-131); "" (the pin's family unknown to
// the catalog) sends no reasoning field at all — the fail-closed
// service-policy spelling, where sp4rk's SetReasoningEffort("") would instead
// clear the override and fall back to the session identity's spelling. The
// oneshot service sites and the judge are NOT stamped here: they resolve
// their own kind tier from the pin before/at the call.
func overrideWrap(caller agent.LLMCaller, overrides *ServiceModelOverrides, key string) agent.LLMCaller {
	pin, ok := overrides.Lookup(key)
	if !ok {
		return caller
	}
	wrapped := newPinnedModelCaller(caller, pin.Provider, pin.Model)
	effort := serviceReasoningEffort(pin.Composite, llm.ReasoningTierOff)
	wrapped.effortStamp = &effort
	return wrapped
}
