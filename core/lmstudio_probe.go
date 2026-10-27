package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// probeLMStudioModels queries the native LM Studio REST API
// (GET {base}/api/v0/models) and returns a map of model ID → context window:
// the runtime value when the model is loaded, its capacity otherwise. URL
// normalization, the loaded-window field spellings, the 404-silent / 5xx-error
// response split, and the fallback chaining are specified in
// specs/domains/llm-providers.md ("Lazy local-model probe"). httpClient
// defaults to http.DefaultClient when nil.
func probeLMStudioModels(ctx context.Context, baseURL, apiKey string, httpClient *http.Client) (map[string]int, error) {
	endpoint := strings.TrimRight(baseURL, "/")
	endpoint = strings.TrimSuffix(endpoint, "/v1")
	endpoint += "/api/v0/models"

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return make(map[string]int), fmt.Errorf("failed to build LM Studio models request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := httpClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return make(map[string]int), fmt.Errorf("failed to query LM Studio models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 500 && resp.StatusCode != http.StatusOK {
		return make(map[string]int), nil
	}
	if resp.StatusCode >= 500 {
		return make(map[string]int), fmt.Errorf("lm studio models endpoint returned status %d", resp.StatusCode)
	}

	// Bounded read: a hostile local server must not balloon memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return make(map[string]int), fmt.Errorf("failed to read LM Studio models response: %w", err)
	}

	var payload struct {
		Data []struct {
			ID                  string `json:"id"`
			MaxContextLength    int    `json:"max_context_length"`
			LoadedContextLength int    `json:"loaded_context_length"`
			LoadedInstances     []struct {
				Config struct {
					ContextLength int `json:"context_length"`
				} `json:"config"`
			} `json:"loaded_instances"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return make(map[string]int), fmt.Errorf("failed to parse LM Studio models response: %w", err)
	}

	result := make(map[string]int, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID == "" {
			continue
		}
		window := m.MaxContextLength
		if m.LoadedContextLength > 0 {
			window = m.LoadedContextLength
		}
		for _, li := range m.LoadedInstances {
			if li.Config.ContextLength > 0 {
				window = li.Config.ContextLength
				break
			}
		}
		result[m.ID] = window
	}
	return result, nil
}

// probeOpenAIModels queries the standard OpenAI-compatible model listing
// (GET {base}/v1/models) and returns a map of model ID → context window —
// the first non-zero of max_model_len / max_context_length / context_length
// that self-hosted servers extend the entry with; a cloud listing without
// them maps to nothing. Error classes and URL normalization mirror
// probeLMStudioModels; semantics: specs/domains/llm-providers.md
// ("Lazy local-model probe").
func probeOpenAIModels(ctx context.Context, baseURL, apiKey string, httpClient *http.Client) (map[string]int, error) {
	endpoint := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(endpoint, "/v1") {
		endpoint += "/v1"
	}
	endpoint += "/models"

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return make(map[string]int), fmt.Errorf("failed to build OpenAI models request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := httpClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return make(map[string]int), fmt.Errorf("failed to query OpenAI models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 500 && resp.StatusCode != http.StatusOK {
		return make(map[string]int), nil
	}
	if resp.StatusCode >= 500 {
		return make(map[string]int), fmt.Errorf("openai models endpoint returned status %d", resp.StatusCode)
	}

	// Bounded read: a hostile local server must not balloon memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return make(map[string]int), fmt.Errorf("failed to read OpenAI models response: %w", err)
	}

	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			MaxModelLen   int    `json:"max_model_len"`
			MaxContextLen int    `json:"max_context_length"`
			ContextLen    int    `json:"context_length"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return make(map[string]int), fmt.Errorf("failed to parse OpenAI models response: %w", err)
	}

	result := make(map[string]int, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID == "" {
			continue
		}
		window := m.MaxModelLen
		if window == 0 {
			window = m.MaxContextLen
		}
		if window == 0 {
			window = m.ContextLen
		}
		if window > 0 {
			result[m.ID] = window
		}
	}
	return result, nil
}

// probeSelfHostedContextWindow discovers the runtime context window for a
// single model id: LM Studio native endpoint first, standard /v1/models
// fallback second; 0 (with the registry's existing metadata kept) when no
// source reports. Error semantics and probe ordering:
// specs/domains/llm-providers.md ("Lazy local-model probe").
func probeSelfHostedContextWindow(ctx context.Context, baseURL, apiKey, model string, httpClient *http.Client) (int, error) {
	if lm, err := probeLMStudioModels(ctx, baseURL, apiKey, httpClient); err == nil {
		if w := lm[model]; w > 0 {
			return w, nil
		}
	}
	return probeOpenAIModelWindow(ctx, baseURL, apiKey, model, httpClient)
}

// probeOpenAIModelWindow is the /v1/models fallback for a single model id,
// factored out of probeSelfHostedContextWindow for independent testability;
// 0 when the listing omits the model.
func probeOpenAIModelWindow(ctx context.Context, baseURL, apiKey, model string, httpClient *http.Client) (int, error) {
	oai, err := probeOpenAIModels(ctx, baseURL, apiKey, httpClient)
	if err != nil {
		return 0, err
	}
	return oai[model], nil
}
