package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sort"
	"sync"
	"time"
)

// SamplingFunc returns a default temperature for the given model family.
// Return nil to use the provider's built-in default (no temperature parameter sent).
type SamplingFunc func(family string) *float64

// RouterConfig configures the LLM router.
// All values must be pre-resolved by the caller (env vars expanded, durations parsed).
//
// MaxRetries defaults to 3 when unset (zero); a negative value means
// explicitly 0 (retries disabled). With retries enabled, transient errors
// (HTTP 429, 502, 503, 529, network blips) recover automatically with
// exponential backoff (1s → 2s → 4s, capped at MaxBackoff). This adds up to
// ~7s of latency on the worst-case retry path. Callers that rely on error
// propagation for compaction timing, circuit-breaker resets, or budget
// control should account for this default, or disable retries with a
// negative MaxRetries. InitialBackoff and MaxBackoff follow the same
// convention: 0 → default (1s / 30s), negative → explicitly 0.
type RouterConfig struct {
	Providers           []ProviderEntry // all enabled providers (at least one required)
	MaxRetries          int             // Max retry attempts on retryable errors
	InitialBackoff      time.Duration   // Already parsed initial backoff duration
	MaxBackoff          time.Duration   // Already parsed max backoff duration
	SafetyMarginPercent int             // Percentage of context window reserved as safety margin (default: 5)
	OutputTokenReserve  int             // Default output token reserve when model metadata doesn't specify (default: 4096)
	HTTPClient          *http.Client    // Optional proxy-configured HTTP client (nil = default)
	SamplingFunc        SamplingFunc    // Optional family-aware temperature defaults; nil = no default (provider decides)
	Logger              *slog.Logger    // Optional logger for ambiguity warnings (nil = silent)
}

// ProviderEntry describes a single LLM provider with its enabled models.
type ProviderEntry struct {
	Name         string   // logical name ("anthropic", "openai_compatible", …)
	ProviderType string   // provider type: "openai", "anthropic"
	APIKey       string   // already-expanded API key
	BaseURL      string   // already-expanded base URL
	Models       []string // enabled model names for this provider
}

// Router routes LLM calls to the active provider.
//
// Concurrency: Router is safe for concurrent use from multiple goroutines.
// SetModel takes a write lock; all other methods take read locks. Call
// snapshots the active provider and model under a read lock, then releases it
// before the retry loop so SetModel is not blocked by backoff sleeps.
type Router struct {
	mu                 sync.RWMutex
	providers          map[string]Provider
	modelToProvider    map[string]string // composite model ID ("provider/model") → provider name
	activeProvider     Provider
	activeModel        string // composite model ID ("provider/model") — the selector
	activeBareModel    string // bare model name sent to the LLM API / used for metadata
	activeProviderName string
	// Retry configuration
	maxRetries     int
	initialBackoff time.Duration
	maxBackoff     time.Duration
	// Pre-call context window validation
	registry            *ModelRegistry
	tokenCounter        TokenCounter
	sampling            SamplingFunc
	safetyMarginPercent int // percentage of context window reserved as safety margin (default: 5)
	outputTokenReserve  int // default output token reserve when model metadata doesn't specify (default: 4096)
	logger              *slog.Logger
}

// NewRouter creates a new Router from the given configuration.
// The caller is responsible for resolving provider config, expanding env vars,
// and parsing durations before calling this function.
// If registry is provided, providers may register their metadata sources.
func NewRouter(ctx context.Context, cfg RouterConfig, registry *ModelRegistry) (*Router, error) {
	if len(cfg.Providers) == 0 {
		return nil, errors.New("no providers configured")
	}

	providers := make(map[string]Provider, len(cfg.Providers))
	modelToProvider := make(map[string]string)

	for _, entry := range cfg.Providers {
		if entry.ProviderType == "" {
			return nil, fmt.Errorf("provider %q has no type", entry.Name)
		}
		provider, err := createProviderFromConfig(ctx, entry.Name, entry.ProviderType, entry.APIKey, entry.BaseURL, cfg.HTTPClient)
		if err != nil {
			return nil, fmt.Errorf("failed to create provider %q: %w", entry.Name, err)
		}
		providers[entry.Name] = provider

		// Build reverse index: composite model ID ("provider/model") → provider
		// name. Composite keys disambiguate models that share the same bare name
		// across multiple providers.
		for _, m := range entry.Models {
			modelToProvider[CompositeModelID(entry.Name, m)] = entry.Name
		}
	}

	// Set initial active provider+model = first provider's first model
	first := cfg.Providers[0]
	if len(first.Models) == 0 {
		return nil, fmt.Errorf("provider %q has no enabled models", first.Name)
	}
	activeProvider := providers[first.Name]
	if activeProvider == nil {
		return nil, fmt.Errorf("provider %q not found", first.Name)
	}

	// Sentinel convention (see RouterConfig doc): 0 → default, negative →
	// explicitly 0/disabled.
	maxRetries := cfg.MaxRetries
	switch {
	case maxRetries == 0:
		// Default to 3 retries so transient errors (HTTP 429/502/503/529,
		// network blips) recover automatically.
		maxRetries = 3
	case maxRetries < 0:
		maxRetries = 0 // retries explicitly disabled
	}
	initialBackoff := cfg.InitialBackoff
	switch {
	case initialBackoff == 0:
		initialBackoff = 1 * time.Second
	case initialBackoff < 0:
		initialBackoff = 0
	}
	maxBackoff := cfg.MaxBackoff
	switch {
	case maxBackoff == 0:
		maxBackoff = 30 * time.Second
	case maxBackoff < 0:
		maxBackoff = 0
	}
	safetyMarginPercent := cfg.SafetyMarginPercent
	if safetyMarginPercent <= 0 {
		safetyMarginPercent = 5
	}
	outputTokenReserve := cfg.OutputTokenReserve
	if outputTokenReserve <= 0 {
		outputTokenReserve = 4096
	}

	return &Router{
		providers:           providers,
		modelToProvider:     modelToProvider,
		activeProvider:      activeProvider,
		activeModel:         CompositeModelID(first.Name, first.Models[0]),
		activeBareModel:     first.Models[0],
		activeProviderName:  first.Name,
		maxRetries:          maxRetries,
		initialBackoff:      initialBackoff,
		maxBackoff:          maxBackoff,
		registry:            registry,
		tokenCounter:        NewSimpleTokenCounter(),
		sampling:            cfg.SamplingFunc,
		safetyMarginPercent: safetyMarginPercent,
		outputTokenReserve:  outputTokenReserve,
		logger:              cfg.Logger,
	}, nil
}

// createProviderFromConfig creates a Provider based on the provider type.
// The caller must have already expanded environment variables.
// name is the logical provider name (config key) used for logging and error
// reporting; it is forwarded to the provider so named compatible providers
// (e.g. "lmstudio", "my-anthropic-proxy") report their real name, not a
// hardcoded family name.
func createProviderFromConfig(ctx context.Context, name, provType, apiKey, baseURL string, httpClient *http.Client) (Provider, error) {
	switch provType {
	case "openai":
		return NewOpenAIProvider(OpenAIProviderConfig{
			Name:       name,
			APIKey:     apiKey,
			BaseURL:    baseURL,
			HTTPClient: httpClient,
		})

	case "anthropic":
		return NewAnthropicProvider(AnthropicProviderConfig{
			Name:       name,
			APIKey:     apiKey,
			BaseURL:    baseURL,
			HTTPClient: httpClient,
		})

	default:
		return nil, fmt.Errorf("unknown provider type: %s", provType)
	}
}

// retryBackoff sleeps for the given duration with +/- 20% jitter, respecting context cancellation.
// Returns false if the context was cancelled during sleep.
func retryBackoff(ctx context.Context, backoff time.Duration) bool {
	// Add jitter: +/- 20%
	jitterFactor := 0.8 + 0.4*rand.Float64() // random factor between 0.8 and 1.2
	jitteredBackoff := float64(backoff) * jitterFactor
	jitter := time.Duration(jitteredBackoff)
	select {
	case <-time.After(jitter):
		return true
	case <-ctx.Done():
		return false
	}
}

// validateContextWindow checks whether the estimated token count of msgs fits
// within the model's context window minus output reserve. Returns nil when
// validation passes or should be skipped (unknown model, zero context window,
// nil registry).
//
// NOTE: This is a pre-submission guard to reject obviously oversized requests.
// It differs from ContextWindow.EffectiveMax() (which tracks ongoing fill during
// the agent loop). The two calculations are intentionally independent.
func (r *Router) validateContextWindow(ctx context.Context, model string, msgs []Message) error {
	if r.registry == nil || r.tokenCounter == nil {
		return nil
	}

	meta, _ := r.registry.Resolve(ctx, model)

	// Skip validation when metadata is a fallback or context window is 0
	if meta.ContextWindow == 0 {
		return nil
	}

	outputReserve := meta.OutputLimit
	if outputReserve <= 0 {
		outputReserve = r.outputTokenReserve
	}

	effectiveMax := meta.ContextWindow - outputReserve
	if effectiveMax <= 0 {
		return nil
	}

	// Apply safety margin to account for counting inaccuracy
	effectiveMax = int(float64(effectiveMax) * (1 - float64(r.safetyMarginPercent)/100.0))

	estimated := r.tokenCounter.CountMessages(msgs)
	if estimated > effectiveMax {
		return NewContextWindowError(model, estimated, effectiveMax, meta.ContextWindow, outputReserve)
	}

	return nil
}

// applyDefaultTemperature sets a family-aware temperature default on the request
// when no explicit temperature is provided. Skips models that don't support
// the temperature parameter (e.g. reasoning models like o1, o3).
func (r *Router) applyDefaultTemperature(ctx context.Context, req *ChatRequest) {
	if req.Temperature != nil {
		return // caller set explicit temperature — respect it
	}

	// Resolve model metadata for capability check and family
	var family string
	if r.registry != nil {
		meta, _ := r.registry.Resolve(ctx, req.Model)
		family = meta.Family
		if !meta.Capabilities.Temperature {
			return // model doesn't accept temperature (e.g. reasoning models)
		}
	}

	// Apply sampling function regardless of registry presence.
	// The sampling func is responsible for handling empty family
	// (e.g. falling back to a default family or passing through).
	if r.sampling != nil {
		req.Temperature = r.sampling(family)
		return
	}

	// Fallback: no sampling func — default to deterministic (0.0)
	temp := 0.0
	req.Temperature = &temp
}

// prepareRequest fills defaults (model, temperature) and validates the context
// window. Returns an error if the request would exceed the context window.
//
// The bare model name (without provider prefix) is what is sent to the LLM API
// and used for metadata lookups; the composite identifier is only the internal
// selector stored in activeModel.
//
// bareModel is the snapshot of the active bare model taken under the read lock
// by the caller (Call). prepareRequest does not read r.active* fields itself,
// so it does not require the caller to hold r.mu.
func (r *Router) prepareRequest(ctx context.Context, req *ChatRequest, bareModel string) error {
	if req.Model == "" {
		req.Model = bareModel
	}
	r.applyDefaultTemperature(ctx, req)
	return r.validateContextWindow(ctx, req.Model, req.Messages)
}

// Call sends a chat request to the active provider.
func (r *Router) Call(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	// Snapshot the active provider and bare model under the read lock, then
	// release the lock before the retry loop. The retry loop includes
	// exponential backoff sleeps (up to ~7s); holding the read lock for that
	// duration would block SetModel (write lock) and freeze model switching
	// during retry storms. applyDefaultTemperature and validateContextWindow
	// only read immutable fields (registry, tokenCounter, sampling, etc.), so
	// they are safe to call without the lock after the snapshot.
	r.mu.RLock()
	provider := r.activeProvider
	bareModel := r.activeBareModel
	r.mu.RUnlock()

	if err := r.prepareRequest(ctx, &req, bareModel); err != nil {
		return nil, err
	}

	var lastErr error
	backoff := r.initialBackoff

	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		resp, err := provider.ChatCompletion(ctx, req)
		if err == nil {
			// Ensure model is set in response
			if resp.Model == "" {
				resp.Model = req.Model
			}
			// Resolve family from model registry
			if r.registry != nil && resp.Family == "" {
				meta, _ := r.registry.Resolve(ctx, resp.Model)
				resp.Family = meta.Family
			}
			normalizeResponse(resp)
			return resp, nil
		}

		lastErr = err

		// Don't retry if not retryable or this was the last attempt
		if !IsRetryable(err) || attempt == r.maxRetries {
			return nil, err
		}

		// Sleep with jitter, respecting context cancellation
		if !retryBackoff(ctx, backoff) {
			return nil, lastErr
		}

		// Exponential backoff: double, capped at max
		backoff *= 2
		if backoff > r.maxBackoff {
			backoff = r.maxBackoff
		}
	}

	return nil, lastErr
}

// DefaultProvider returns the active provider.
// Returns nil if no provider is configured.
func (r *Router) DefaultProvider() Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.activeProvider
}

// ActiveProviderName returns the logical name of the active provider.
func (r *Router) ActiveProviderName() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.activeProviderName
}

// ActiveModel returns the composite model identifier ("provider/model") of the
// currently active model. Use BareModel(ActiveModel()) to obtain the bare model
// name shown to users / sent to the LLM API.
func (r *Router) ActiveModel() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.activeModel
}

// SetModel switches the active provider and model to the given model identifier.
//
// The identifier may be a composite "provider/model" (routes to the named
// provider, disambiguating models that share a bare name across providers) or a
// bare model name (resolved to the first matching provider for backward
// compatibility). When a bare name matches multiple providers, the first match
// (deterministic, sorted by composite ID) is selected and a warning is logged
// when a logger is configured.
func (r *Router) SetModel(ctx context.Context, model string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Composite identifier: direct lookup.
	if IsCompositeModelID(model) {
		providerName, ok := r.modelToProvider[model]
		if !ok {
			return fmt.Errorf("model %q is not enabled in any provider", model)
		}
		provider, ok := r.providers[providerName]
		if !ok {
			return fmt.Errorf("provider %q not found", providerName)
		}
		r.activeProvider = provider
		r.activeModel = model
		r.activeBareModel = BareModel(model)
		r.activeProviderName = providerName
		return nil
	}

	// Bare identifier: resolve to a composite ID via reverse scan of the index.
	var matches []string
	for id := range r.modelToProvider {
		if BareModel(id) == model {
			matches = append(matches, id)
		}
	}
	if len(matches) == 0 {
		return fmt.Errorf("model %q is not enabled in any provider", model)
	}
	sort.Strings(matches) // deterministic first-match when ambiguous
	compositeID := matches[0]
	if len(matches) > 1 && r.logger != nil {
		r.logger.Warn("model name is ambiguous across providers; selecting first match — use a composite \"provider/model\" identifier to disambiguate",
			"model", model, "selected", compositeID, "candidates", matches)
	}
	providerName := r.modelToProvider[compositeID]
	provider, ok := r.providers[providerName]
	if !ok {
		return fmt.Errorf("provider %q not found", providerName)
	}
	r.activeProvider = provider
	r.activeModel = compositeID
	r.activeBareModel = model
	r.activeProviderName = providerName
	return nil
}
