package llm

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"
)

// SamplingFunc returns a default temperature for the given model family.
// Return nil to use the provider's built-in default (no temperature parameter sent).
type SamplingFunc func(family string) *float64

// RouterConfig configures the LLM router.
// All values must be pre-resolved by the caller (env vars expanded, durations parsed).
type RouterConfig struct {
	ActiveProvider      string        // Logical name of the active provider (e.g. "openai", "lmstudio")
	ProviderType        string        // Provider type: "openai", "lmstudio", "anthropic", "gemini"
	APIKey              string        // Already-expanded API key
	BaseURL             string        // Already-expanded base URL
	Model               string        // Default model to use
	MaxRetries          int           // Max retry attempts on retryable errors
	InitialBackoff      time.Duration // Already parsed initial backoff duration
	MaxBackoff          time.Duration // Already parsed max backoff duration
	SafetyMarginPercent int           // Percentage of context window reserved as safety margin (default: 5)
	OutputTokenReserve  int           // Default output token reserve when model metadata doesn't specify (default: 4096)
	SamplingFunc        SamplingFunc  // Optional family-aware temperature defaults; nil = no default (provider decides)
}

// Router routes LLM calls to the active provider.
type Router struct {
	providers          map[string]Provider
	activeProvider     Provider
	activeModel        string
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
}

// NewRouter creates a new Router from the given configuration.
// The caller is responsible for resolving provider config, expanding env vars,
// and parsing durations before calling this function.
// If registry is provided, LM Studio providers will register their metadata sources.
func NewRouter(ctx context.Context, cfg RouterConfig, registry *ModelRegistry) (*Router, error) {
	if cfg.ProviderType == "" {
		return nil, errors.New("no active provider configured")
	}

	// Create the active provider — values are already expanded by the caller
	provider, err := createProviderFromConfig(ctx, cfg.ProviderType, cfg.APIKey, cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider %q: %w", cfg.ActiveProvider, err)
	}

	// Register LM Studio provider as metadata source
	if registry != nil {
		if lms, ok := provider.(*LMStudioProvider); ok {
			registry.RegisterSource(lms.MetadataSource())
		}
	}

	initialBackoff := cfg.InitialBackoff
	if initialBackoff == 0 {
		initialBackoff = 1 * time.Second
	}
	maxBackoff := cfg.MaxBackoff
	if maxBackoff == 0 {
		maxBackoff = 30 * time.Second
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
		providers: map[string]Provider{
			cfg.ActiveProvider: provider,
		},
		activeProvider:      provider,
		activeModel:         cfg.Model,
		activeProviderName:  cfg.ActiveProvider,
		maxRetries:          cfg.MaxRetries,
		initialBackoff:      initialBackoff,
		maxBackoff:          maxBackoff,
		registry:            registry,
		tokenCounter:        NewSimpleTokenCounter(),
		sampling:            cfg.SamplingFunc,
		safetyMarginPercent: safetyMarginPercent,
		outputTokenReserve:  outputTokenReserve,
	}, nil
}

// createProviderFromConfig creates a Provider based on the provider type.
// The caller must have already expanded environment variables.
func createProviderFromConfig(ctx context.Context, provType, apiKey, baseURL string) (Provider, error) {
	switch provType {
	case "openai":
		return NewOpenAIProvider(OpenAIProviderConfig{
			APIKey:  apiKey,
			BaseURL: baseURL,
		})

	case "lmstudio":
		return NewLMStudioProvider(LMStudioProviderConfig{
			APIKey:  apiKey,
			BaseURL: baseURL,
		})

	case "anthropic":
		return NewAnthropicProvider(AnthropicProviderConfig{
			APIKey: apiKey,
		})

	case "gemini":
		return NewGeminiProvider(ctx, GeminiProviderConfig{
			APIKey: apiKey,
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
func (r *Router) validateContextWindow(model string, msgs []Message) error {
	if r.registry == nil || r.tokenCounter == nil {
		return nil
	}

	meta, _ := r.registry.Resolve(model)

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
func (r *Router) applyDefaultTemperature(req *ChatRequest) {
	if req.Temperature != nil {
		return // caller set explicit temperature — respect it
	}

	// Resolve model metadata for capability check and family
	if r.registry != nil {
		meta, _ := r.registry.Resolve(req.Model)
		if !meta.Capabilities.Temperature {
			return // model doesn't accept temperature (e.g. reasoning models)
		}
		if r.sampling != nil {
			req.Temperature = r.sampling(meta.Family)
			return
		}
	}

	// Fallback: no registry or no sampling func — use 0.0 for backward compat
	temp := 0.0
	req.Temperature = &temp
}

// Call sends a chat request to the active provider.
func (r *Router) Call(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	// Set model if not specified
	if req.Model == "" {
		req.Model = r.activeModel
	}

	// Apply family-aware temperature default when not explicitly set
	r.applyDefaultTemperature(&req)

	// Pre-call context window validation
	if err := r.validateContextWindow(req.Model, req.Messages); err != nil {
		return nil, err
	}

	var lastErr error
	backoff := r.initialBackoff

	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		resp, err := r.activeProvider.ChatCompletion(ctx, req)
		if err == nil {
			// Ensure model is set in response
			if resp.Model == "" {
				resp.Model = req.Model
			}
			// Resolve family from model registry
			if r.registry != nil && resp.Family == "" {
				meta, _ := r.registry.Resolve(resp.Model)
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

		// Wait before retrying

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

// GetProvider returns a provider by name.
// Returns nil if the provider is not found.
func (r *Router) GetProvider(name string) Provider {
	provider, ok := r.providers[name]
	if !ok {
		return nil
	}
	return provider
}

// GetDefaultProvider returns the active provider.
// Returns nil if no provider is configured.
func (r *Router) GetDefaultProvider() Provider {
	return r.activeProvider
}

// Stream sends a streaming chat request to the active provider.
func (r *Router) Stream(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error) {
	// Set model if not specified
	if req.Model == "" {
		req.Model = r.activeModel
	}

	// Apply family-aware temperature default when not explicitly set
	r.applyDefaultTemperature(&req)

	// Pre-call context window validation
	if err := r.validateContextWindow(req.Model, req.Messages); err != nil {
		return nil, err
	}

	var lastErr error
	backoff := r.initialBackoff

	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		ch, err := r.activeProvider.StreamChatCompletion(ctx, req)
		if err == nil {
			return ch, nil
		}

		lastErr = err

		if !IsRetryable(err) || attempt == r.maxRetries {
			return nil, err
		}

		// Wait before retrying stream
		if !retryBackoff(ctx, backoff) {
			return nil, lastErr
		}

		backoff *= 2
		if backoff > r.maxBackoff {
			backoff = r.maxBackoff
		}
	}

	return nil, lastErr
}
