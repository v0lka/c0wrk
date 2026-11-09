package backend

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/v0lka/sp4rk/llm"
)

const (
	subscriptionModelsTimeout  = 15 * time.Second
	subscriptionModelsMaxBytes = 1 << 20
)

type subscriptionModelMetadata struct {
	contextWindow int
	outputLimit   int
	reasoning     bool
}

func resolveSubscriptionModelMetadata(name string, reg *llm.ModelRegistry) subscriptionModelMetadata {
	if reg == nil {
		return subscriptionModelMetadata{}
	}
	meta, ok := reg.ResolveLocal(name)
	if !ok {
		return subscriptionModelMetadata{}
	}
	return subscriptionModelMetadata{
		contextWindow: meta.ContextWindow,
		outputLimit:   meta.OutputLimit,
		reasoning:     meta.Capabilities != nil && meta.Capabilities.Reasoning,
	}
}

func (f *FrontendAPI) fetchSubscriptionCatalog(
	state *providerAuthState,
	desc providerAuthDescriptor,
	query string,
	followRedirects bool,
) ([]byte, error) {
	state.mu.Lock()
	m := state.manager
	baseURL := state.modelsBaseURLOverride
	if baseURL == "" {
		baseURL = state.modelsBaseURL
	}
	if m == nil {
		err := providerAuthNilManagerRefusalLocked(state, desc, "model list")
		state.mu.Unlock()
		return nil, err
	}
	state.mu.Unlock()
	if !m.Status().SignedIn {
		return nil, fmt.Errorf("fetching the %s model list requires a subscription sign-in — sign in with %s first", desc.displayName, desc.displayName)
	}
	if baseURL == "" {
		baseURL = desc.profile().APIBaseURL
	}

	ctx, cancel := context.WithTimeout(f.ctx(), subscriptionModelsTimeout)
	defer cancel()
	tok, err := m.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching the %s model list: resolving the access token: %w", desc.displayName, err)
	}
	catalogURL := strings.TrimRight(baseURL, "/") + "/models" + query
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, catalogURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("fetching the %s model list: %w", desc.displayName, err)
	}
	req.Header.Set("Accept", "application/json")
	if tok.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	}
	for k, v := range tok.ExtraHeaders {
		if v != "" {
			req.Header.Set(k, v)
		}
	}

	client := f.providerAuthOutboundHTTPClient(subscriptionModelsTimeout)
	if !followRedirects {
		bounded := *client
		bounded.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		client = &bounded
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching the %s model list: %w", desc.displayName, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, subscriptionModelsMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("fetching the %s model list: reading the response: %w", desc.displayName, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching the %s model list: the subscription backend answered %s: %s",
			desc.displayName, resp.Status, subscriptionModelsErrorSnippet(body))
	}
	return body, nil
}

func subscriptionModelsErrorSnippet(body []byte) string {
	snippet := strings.TrimSpace(string(body))
	if snippet == "" {
		return "(empty response body)"
	}
	if len(snippet) > 200 {
		return snippet[:200] + "…"
	}
	return snippet
}
