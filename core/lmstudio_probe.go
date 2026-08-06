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
// (GET {base}/api/v0/models) and returns a map of model ID → context window.
//
// For each model the returned window is the *runtime* value when the model is
// loaded, otherwise the model's capacity (max_context_length). This lets
// callers size token budgets using the value LM Studio is actually running with
// rather than the static spec. The runtime value is read from whichever field
// LM Studio exposes for the loaded instance: recent versions report a top-level
// "loaded_context_length", while older/other versions nest it under
// "loaded_instances[].config.context_length". Both are honored; capacity is the
// fallback when neither is present (model not loaded).
//
// baseURL is normalized: trailing slashes are trimmed, an optional "/v1" suffix
// (used by OpenAI-compatible endpoints) is dropped, and "/api/v0/models" is
// appended. An empty apiKey omits the Authorization header.
//
// Non-200 responses are split into two classes:
//   - Client errors < 500 (most notably 404, which real OpenAI/vLLM servers
//     return for the LM-Studio-specific path) are a silent no-op: an empty map
//     and a nil error are returned, so callers can probe an endpoint without
//     distinguishing LM Studio from other servers.
//   - Server errors >= 500 (a momentarily-unwell LM Studio, gateway errors,
//     …) return an empty map together with a non-nil error so the caller can
//     log a distinct, debuggable message instead of silently dropping the
//     model to the static default.
//
// Note that when this function is called as the first leg of
// probeSelfHostedContextWindow, the 5xx error surfaced here is intentionally
// swallowed if the OpenAI /v1/models fallback succeeds — a useful result was
// still obtained, so a warn-log is only emitted when BOTH legs fail. Callers
// that invoke probeLMStudioModels directly will see the 5xx error as returned.
//
// Network, timeout, and parse failures return an empty map together with the
// error (non-fatal; callers log it). httpClient defaults to http.DefaultClient
// when nil.
func probeLMStudioModels(ctx context.Context, baseURL, apiKey string, httpClient *http.Client) (map[string]int, error) {
	// Normalize the URL: drop trailing slashes, strip an OpenAI-style "/v1"
	// suffix, then append the LM Studio native path.
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

	// 404 (and other < 500 client errors) means this is not an LM Studio
	// server — a silent no-op rather than a hard error.
	if resp.StatusCode < 500 && resp.StatusCode != http.StatusOK {
		return make(map[string]int), nil
	}
	// 5xx server errors (momentarily-unwell LM Studio, gateway errors, …) are
	// surfaced as a non-nil error so the caller logs a distinct, debuggable
	// message rather than silently treating the models as "not reported".
	if resp.StatusCode >= 500 {
		return make(map[string]int), fmt.Errorf("lm studio models endpoint returned status %d", resp.StatusCode)
	}

	// Cap the response at 1 MiB to avoid unbounded reads from a misbehaving or
	// hostile local server.
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
		// Prefer the runtime context window when the model is loaded; fall
		// back to the model's advertised capacity otherwise. Recent LM Studio
		// versions report the runtime window as a top-level
		// "loaded_context_length" field; older/other versions expose it under
		// "loaded_instances[].config.context_length". Both are honored.
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
// (GET {base}/v1/models) and returns a map of model ID → context window.
//
// While the OpenAI API itself does not expose a context window in this
// listing, self-hosted OpenAI-compatible servers extend the per-model entry
// with one. This honors the field names seen across the ecosystem, taking the
// first non-zero value in priority order:
//   - "max_model_len"      — vLLM (the authoritative runtime limit).
//   - "max_context_length" — an alternate spelling used by some forks.
//   - "context_length"     — TGI / Ollama-style fallback.
//
// Models that report none of these (notably the real OpenAI API, which only
// returns id/object/created/owned_by) simply map to 0 and are skipped, so
// probing a genuine cloud provider is a harmless no-op.
//
// baseURL is normalized: trailing slashes are trimmed and a missing "/v1"
// prefix is added before appending "/models" (so both "http://h:8000" and
// "http://h:8000/v1" resolve to the same endpoint). An empty apiKey omits the
// Authorization header.
//
// Non-200 responses use the same two-class split as probeLMStudioModels:
// client errors < 500 (e.g. 401/404 for a cloud server that does not expose a
// per-model window) are a silent no-op; server errors >= 500 return an empty
// map with a non-nil error for distinct logging. Network, timeout, and parse
// failures likewise return an empty map together with the error. httpClient
// defaults to http.DefaultClient when nil.
func probeOpenAIModels(ctx context.Context, baseURL, apiKey string, httpClient *http.Client) (map[string]int, error) {
	// Normalize: drop trailing slashes, ensure a "/v1" prefix, then append the
	// standard OpenAI models path. Unlike probeLMStudioModels we keep "/v1"
	// (it IS the path the standard listing lives under).
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

	// < 500 client errors (401/403/404) mean the server either does not expose
	// this endpoint or a per-model window — a silent no-op.
	if resp.StatusCode < 500 && resp.StatusCode != http.StatusOK {
		return make(map[string]int), nil
	}
	if resp.StatusCode >= 500 {
		return make(map[string]int), fmt.Errorf("openai models endpoint returned status %d", resp.StatusCode)
	}

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
		// First non-zero field wins, in ecosystem priority order.
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
// single model id served from an OpenAI-compatible base URL. It tries the LM
// Studio native endpoint first (which reports the runtime/loaded window when a
// model is hot) and falls back to the standard OpenAI /v1/models listing
// (which vLLM/TGI/Ollama extend with max_model_len). Returns 0 when no source
// reports a window — e.g. a genuine cloud provider whose listing omits the
// field, in which case the caller keeps the registry's existing metadata.
//
// A non-nil error is returned only when the LAST attempted probe failed with a
// server-side/network error; an earlier LM Studio failure is swallowed if the
// OpenAI fallback succeeds, since a useful result was still obtained.
func probeSelfHostedContextWindow(ctx context.Context, baseURL, apiKey, model string, httpClient *http.Client) (int, error) {
	if lm, err := probeLMStudioModels(ctx, baseURL, apiKey, httpClient); err == nil {
		if w := lm[model]; w > 0 {
			return w, nil
		}
	}
	return probeOpenAIModelWindow(ctx, baseURL, apiKey, model, httpClient)
}

// probeOpenAIModelWindow is the /v1/models fallback for a single model id,
// factored out of probeSelfHostedContextWindow so the fallback path is
// independently testable. It reports the discovered window (0 when absent) and
// the OpenAI probe's error (nil for a clean "not reported" result).
func probeOpenAIModelWindow(ctx context.Context, baseURL, apiKey, model string, httpClient *http.Client) (int, error) {
	oai, err := probeOpenAIModels(ctx, baseURL, apiKey, httpClient)
	if err != nil {
		return 0, err
	}
	return oai[model], nil
}
