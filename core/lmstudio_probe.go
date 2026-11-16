package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/v0lka/sp4rk/llm"
)

// rawModelEntry is one /models listing entry, kept as the server sent it:
// the detector chain (modelmetadata_detectors.go) owns the field-level parse.
type rawModelEntry struct {
	ID   string
	JSON []byte
}

// Per-leg listing timeouts, chosen by SURFACE not by strictness. The lazy
// probe is best-effort latency insurance on the session path — 3s per leg
// keeps session creation and model switches unblocked even against an
// unreachable endpoint. The settings Fetch Models click is user-attended and
// carries the 10s ceiling the SDK-based fetch had before the shared listing
// layer — a cold vLLM or slow gateway answering within 10s must not fail
// there where it did not before
// (specs/domains/llm-providers.md, "Latency & safety").
const (
	probeListingTimeout    = 3 * time.Second
	settingsListingTimeout = 10 * time.Second
)

// probeLMStudioEntries queries the native LM Studio REST API
// (GET {base}/api/v0/models) and returns the raw listing entries. URL
// normalization, the 404-silent / 5xx-error response split, and the fallback
// chaining are specified in specs/domains/llm-providers.md ("Lazy
// local-model probe"). httpClient defaults to http.DefaultClient when nil.
// timeout bounds this leg (probeListingTimeout on the session path,
// settingsListingTimeout on the settings surface).
func probeLMStudioEntries(ctx context.Context, baseURL, apiKey string, httpClient *http.Client, timeout time.Duration) ([]rawModelEntry, error) {
	endpoint := strings.TrimRight(baseURL, "/")
	endpoint = strings.TrimSuffix(endpoint, "/v1")
	endpoint += "/api/v0/models"

	body, err := fetchModelListing(ctx, endpoint, apiKey, httpClient, "LM Studio", false, timeout)
	if err != nil {
		return nil, err
	}
	return decodeModelListing(body, "LM Studio")
}

// probeOpenAIEntries queries the standard OpenAI-compatible model listing
// (GET {base}/v1/models — the /v1 suffix is appended when the base_url omits
// it, as on the chat path's best-effort legs) and returns the raw listing
// entries; self-hosted servers (vLLM/TGI/Ollama) extend entries with window
// fields a cloud listing omits. Error classes mirror probeLMStudioEntries:
// 4xx stays a silent no-op, 5xx an error — this leg is best-effort only.
// timeout bounds this leg (probeListingTimeout on the session path,
// settingsListingTimeout on the settings surface).
// Semantics: specs/domains/llm-providers.md ("Lazy local-model probe").
func probeOpenAIEntries(ctx context.Context, baseURL, apiKey string, httpClient *http.Client, timeout time.Duration) ([]rawModelEntry, error) {
	endpoint := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(endpoint, "/v1") {
		endpoint += "/v1"
	}
	endpoint += "/models"

	body, err := fetchModelListing(ctx, endpoint, apiKey, httpClient, "openai", false, timeout)
	if err != nil {
		return nil, err
	}
	return decodeModelListing(body, "openai")
}

// strictOpenAIEntries is the settings-fetch twin of probeOpenAIEntries: same
// decode, but the endpoint is the base_url VERBATIM ({base}/models — the
// exact URL contract the chat path derives from the same base_url) and 4xx
// responses surface as errors — on the Fetch Models surface a wrong URL or
// revoked API key must be actionable, not an empty list (the lazy probe
// stays best-effort and silent). No /v1 suffix is invented: gateways mounting
// their OpenAI-compatible surface under another path (…/v1beta/openai) list
// in Fetch Models exactly where they chat. A base_url without /v1 (bare LM
// Studio host) is covered by the native leg, so the strict leg's 404 there
// degrades to the native listing (Warn, nil error). timeout bounds this leg
// (settingsListingTimeout — the surface that is the only strict4xx caller).
func strictOpenAIEntries(ctx context.Context, baseURL, apiKey string, httpClient *http.Client, timeout time.Duration) ([]rawModelEntry, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/models"

	body, err := fetchModelListing(ctx, endpoint, apiKey, httpClient, "openai", true, timeout)
	if err != nil {
		return nil, err
	}
	return decodeModelListing(body, "openai")
}

// fetchModelListing is the shared HTTP leg of a listing fetch: caller-chosen
// per-leg timeout (probeListingTimeout / settingsListingTimeout), bearer
// auth, bounded 1 MiB read, and the 4xx-silent/5xx-error split
// (specs/domains/llm-providers.md, "Latency & safety"). strict4xx reports
// non-200 non-5xx statuses as errors instead of swallowing them.
// label names the endpoint in error messages.
func fetchModelListing(ctx context.Context, endpoint, apiKey string, httpClient *http.Client, label string, strict4xx bool, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to build %s models request: %w", label, err)
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
		return nil, fmt.Errorf("failed to query %s models: %w", label, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 500 && resp.StatusCode != http.StatusOK {
		if strict4xx {
			return nil, fmt.Errorf("%s models endpoint returned status %d", label, resp.StatusCode)
		}
		return nil, nil
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("%s models endpoint returned status %d", label, resp.StatusCode)
	}

	// Bounded read: a hostile local server must not balloon memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read %s models response: %w", label, err)
	}
	return body, nil
}

// decodeModelListing extracts the id-keyed raw entries from a listing body;
// entries without an id are skipped.
func decodeModelListing(body []byte, label string) ([]rawModelEntry, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var payload struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("failed to parse %s models response: %w", label, err)
	}
	entries := make([]rawModelEntry, 0, len(payload.Data))
	for _, raw := range payload.Data {
		var idOnly struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &idOnly); err != nil || idOnly.ID == "" {
			continue
		}
		entries = append(entries, rawModelEntry{ID: idOnly.ID, JSON: raw})
	}
	return entries, nil
}

// fetchListingEntries runs the two-leg listing chain over an OpenAI-compatible
// endpoint: the LM Studio native listing first, the standard /models listing
// second (the base_url VERBATIM on the strict settings leg, /v1-normalized on
// the best-effort probe leg — see strictOpenAIEntries/probeOpenAIEntries).
// It is the shared raw layer under BOTH consumers of the chain — the
// lazy session probe (specs/domains/llm-providers.md, "Lazy local-model
// probe") and the settings Fetch Models listing. The fallback leg is skipped
// when target already carries detector metadata from the native listing (a
// bare "" target — the settings listing — never short-circuits, so both legs
// always merge there; being independent, they run concurrently there, so a
// dead endpoint fails after one leg's timeout instead of the sum of both),
// and fallback entries never overwrite an id the native leg produced — its
// wholesale replace would trade the runtime (loaded) window for the
// advertised capacity. strictFallback makes the fallback leg surface 4xx
// errors (a bad key or URL on the settings surface must be actionable)
// instead of silent-empty. A non-nil error reports only the fallback leg —
// native entries come back alongside it and must still be used by the
// caller. The returned metadata map is the detector chain's parse of the
// returned entries, keyed by lowercased model id — callers never re-parse
// (the native parse also feeds the target-skip check). log may be nil.
// timeout bounds EACH leg at the surface's choice (probeListingTimeout on
// the session path, settingsListingTimeout on the settings surface); with
// the legs concurrent (target == "") it stays a per-leg bound, not a shared
// deadline.
func fetchListingEntries(ctx context.Context, baseURL, apiKey, target string, httpClient *http.Client, log *slog.Logger, strictFallback bool, timeout time.Duration) ([]rawModelEntry, map[string]llm.ModelMetadata, error) {
	var lmEntries, oaiEntries []rawModelEntry
	var lmErr, oaiErr error
	runFallback := func() {
		if strictFallback {
			oaiEntries, oaiErr = strictOpenAIEntries(ctx, baseURL, apiKey, httpClient, timeout)
		} else {
			oaiEntries, oaiErr = probeOpenAIEntries(ctx, baseURL, apiKey, httpClient, timeout)
		}
	}
	var lmMeta map[string]llm.ModelMetadata
	if target == "" {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			lmEntries, lmErr = probeLMStudioEntries(ctx, baseURL, apiKey, httpClient, timeout)
		}()
		go func() {
			defer wg.Done()
			runFallback()
		}()
		wg.Wait()
	} else {
		lmEntries, lmErr = probeLMStudioEntries(ctx, baseURL, apiKey, httpClient, timeout)
		if lmErr == nil {
			lmMeta = detectListingMetadata(lmEntries)
			if _, found := lmMeta[strings.ToLower(target)]; found {
				return lmEntries, lmMeta, nil
			}
		}
		runFallback()
	}
	if lmErr != nil {
		// The silent split keeps 404-class native responses quiet, so any
		// error here is a real native-leg failure the fallback papers over —
		// keep it visible at Debug.
		if log != nil {
			log.Debug("LM Studio native listing unavailable; falling back to /v1/models", "error", lmErr)
		}
	}
	if oaiErr != nil {
		// The entries served alongside the error are the native ones; their
		// parse is already in hand unless only the target-less parallel path
		// ran (which never needed it for the skip check).
		if lmErr == nil && lmMeta == nil {
			lmMeta = detectListingMetadata(lmEntries)
		}
		return lmEntries, lmMeta, oaiErr
	}
	merged := mergeListingEntries(lmEntries, oaiEntries)
	return merged, detectListingMetadata(merged), nil
}

// mergeListingEntries concatenates two legs' entries, dropping fallback
// entries whose lowercased id the primary leg already produced. First-wins
// metadata detection over the merged slice then preserves the native listing's
// richer (runtime) view.
func mergeListingEntries(primary, fallback []rawModelEntry) []rawModelEntry {
	if len(fallback) == 0 {
		return primary
	}
	seen := make(map[string]struct{}, len(primary))
	for _, e := range primary {
		seen[strings.ToLower(e.ID)] = struct{}{}
	}
	merged := make([]rawModelEntry, 0, len(primary)+len(fallback))
	merged = append(merged, primary...)
	for _, e := range fallback {
		if _, dup := seen[strings.ToLower(e.ID)]; dup {
			continue
		}
		merged = append(merged, e)
	}
	return merged
}

// probeSelfHostedMetadata discovers rich metadata for every model the server
// lists, through the detector chain over fetchListingEntries (native leg
// first, /v1 fallback; the target-skip and no-overwrite rules live there).
// Returned map is keyed by lowercased model id; entries no detector matches
// are dropped. Error semantics and probe ordering:
// specs/domains/llm-providers.md ("Lazy local-model probe"); a non-nil error
// reports only the fallback leg — already-discovered metadata is returned
// alongside it and must still be written by the caller. log may be nil.
func probeSelfHostedMetadata(ctx context.Context, baseURL, apiKey, model string, httpClient *http.Client, log *slog.Logger) (map[string]llm.ModelMetadata, error) {
	_, metadata, err := fetchListingEntries(ctx, baseURL, apiKey, model, httpClient, log, false, probeListingTimeout)
	return metadata, err
}

// detectListingMetadata runs the detector chain over a fetched listing,
// keyed by lowercased model id. First-wins per id: the native leg precedes
// the fallback in a merged listing and its view of a repeated id is the
// authoritative one.
func detectListingMetadata(entries []rawModelEntry) map[string]llm.ModelMetadata {
	metadata := make(map[string]llm.ModelMetadata, len(entries))
	for _, e := range entries {
		if meta, ok := detectModelMetadata(e.JSON); ok {
			key := strings.ToLower(e.ID)
			if _, dup := metadata[key]; !dup {
				metadata[key] = meta
			}
		}
	}
	return metadata
}
