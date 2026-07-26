package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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

// isLocalBaseURL reports whether the host part of baseURL refers to the local
// machine or a node on an attached local network. It is the gate for lazy LM
// Studio context-window probing: only models served from a local/LAN endpoint
// are probed at session start, so remote providers are never hit with the extra
// HTTP request that used to run eagerly for every OpenAI-compatible provider.
//
// Recognized as local:
//   - The "localhost" name or any "*.localhost" suffix.
//   - A ".local" mDNS suffix (e.g. "macbook.local").
//   - An IPv4/IPv6 host that is a loopback, private (RFC 1918/4193), or
//     link-local address. The Go stdlib's net.IP.IsPrivate covers 10/8,
//     172.16/12, 192.168/16, and fc00::/7; IsLoopback and IsLinkLocalUnicast
//     add 127.0.0.0/8, ::1, 169.254/16, and fe80::/10.
//
// Anything else (a public DNS name or global IP) is treated as remote and
// returns false. Parse/lookup failures are conservative: a URL whose host
// cannot be resolved to an IP is considered remote unless it matches the name
// heuristics above.
func isLocalBaseURL(baseURL string) bool {
	if baseURL == "" {
		return false
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "" {
		return false
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return true
	}
	if strings.HasSuffix(lower, ".local") {
		return true
	}
	// Try to interpret the host as a literal IP first (covers 127.0.0.1,
	// ::1, 10.0.0.5, 169.254.x.x, fc00::, fe80::, …) without a DNS lookup.
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	// A non-IP hostname that is not localhost/.local is assumed to be a
	// public DNS name — do not probe it.
	return false
}
