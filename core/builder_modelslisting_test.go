package core

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// newDetectorListingServer is a two-leg listing endpoint: the native LM Studio
// path and the standard /v1/models path serve independent bodies (0 = leg
// absent → 404), so tests can exercise the merge/no-overwrite rules and the
// strict fallback-leg error policy of the settings Fetch Models surface.
func newDetectorListingServer(t *testing.T, nativeBody, v1Body string, v1Status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/models":
			if nativeBody == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(nativeBody))
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(v1Status)
			if v1Body != "" {
				_, _ = w.Write([]byte(v1Body))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestListProviderModels_OpenAIDetectorMetadata verifies that the settings
// Fetch Models listing carries the detector-chain view of the same raw
// entries it lists — the whole point of the feature: what the UI shows must
// be the endpoint's declared windows (the 512000-vs-128000 mismatch report),
// not the catalog fallback.
func TestListProviderModels_OpenAIDetectorMetadata(t *testing.T) {
	// deepseek-v4 is listed twice with different windows: metadata must be
	// first-wins (512000, not the later 999). The aggregator-shaped entry is
	// the real-world OpenRouter-style listing (aggregatorListingEntry, with
	// limit.context 200000 / limit.output 32000). plain-chat matches no
	// detector and must carry no metadata.
	v1Body := `{"data":[
		{"id":"deepseek-v4","object":"model","max_model_len":512000},
		{"id":"deepseek-v4","object":"model","max_model_len":999},
		` + aggregatorListingEntry + `,
		{"id":"plain-chat","object":"model"}
	]}`
	srv := newDetectorListingServer(t, "", v1Body, http.StatusOK)

	b := newListingBuilder(nil)
	cfg := listingConfig("local", srv.URL+"/v1", "", "openai")
	listing, err := b.ListProviderModels(context.Background(), "local", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantNames := []string{"deepseek-v4", "opencode/deepseek-v4.1-flash-free", "plain-chat"}
	if !reflect.DeepEqual(listing.Models, wantNames) {
		t.Errorf("models = %v, want %v (sorted, duplicates collapsed)", listing.Models, wantNames)
	}

	ds, ok := listing.Metadata["deepseek-v4"]
	if !ok {
		t.Fatalf("metadata[deepseek-v4] missing: %v", listing.Metadata)
	}
	if ds.ContextWindow != 512000 {
		t.Errorf("metadata[deepseek-v4].context_window = %d, want 512000 (first listing wins)", ds.ContextWindow)
	}
	if ds.OutputLimit != 0 {
		t.Errorf("metadata[deepseek-v4].output_limit = %d, want 0 (the listing declared none; the clamp lives in the probe caller)", ds.OutputLimit)
	}

	agg, ok := listing.Metadata["opencode/deepseek-v4.1-flash-free"]
	if !ok {
		t.Fatalf("metadata[aggregator entry] missing: %v", listing.Metadata)
	}
	if agg.ContextWindow != 200000 || agg.OutputLimit != 32000 {
		t.Errorf("aggregator metadata = %d/%d, want 200000/32000 (limit.context/limit.output)", agg.ContextWindow, agg.OutputLimit)
	}
	if agg.Capabilities == nil {
		t.Error("aggregator capabilities = nil, want the listing's full declaration")
	}
	if _, present := listing.Metadata["plain-chat"]; present {
		t.Error("metadata[plain-chat] present, want absent (no detector match)")
	}
}

// TestListProviderModels_NativeLegNoOverwrite verifies the two-leg merge on
// the settings surface: the native LM Studio listing's runtime view of an id
// wins over the /v1 fallback's advertised window, native-only and /v1-only
// models both land, and the merged metadata is complete.
func TestListProviderModels_NativeLegNoOverwrite(t *testing.T) {
	nativeBody := `{"data":[
		{"id":"lm-model","loaded_context_length":16384},
		{"id":"native-only","max_context_length":8192}
	]}`
	v1Body := `{"data":[
		{"id":"lm-model","max_model_len":131072},
		{"id":"v1-only","max_model_len":262144}
	]}`
	srv := newDetectorListingServer(t, nativeBody, v1Body, http.StatusOK)

	b := newListingBuilder(nil)
	cfg := listingConfig("local", srv.URL+"/v1", "", "openai")
	listing, err := b.fetchProviderModels(context.Background(), "local", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantNames := []string{"lm-model", "native-only", "v1-only"}
	if !reflect.DeepEqual(listing.Models, wantNames) {
		t.Errorf("models = %v, want %v (both legs merged)", listing.Models, wantNames)
	}
	if got := listing.Metadata["lm-model"].ContextWindow; got != 16384 {
		t.Errorf("metadata[lm-model].context_window = %d, want 16384 (native runtime view must not be overwritten by /v1's 131072)", got)
	}
	if got := listing.Metadata["native-only"].ContextWindow; got != 8192 {
		t.Errorf("metadata[native-only].context_window = %d, want 8192 (capacity branch)", got)
	}
	if got := listing.Metadata["v1-only"].ContextWindow; got != 262144 {
		t.Errorf("metadata[v1-only].context_window = %d, want 262144 (standard detector)", got)
	}
}

// TestListProviderModels_StrictFallbackSurfacesError verifies the error
// policy on the settings surface: a 401 on the /v1/models leg (revoked key,
// wrong URL) is an actionable error, not a silent empty list — unlike the
// best-effort lazy probe whose 4xx legs stay silent.
func TestListProviderModels_StrictFallbackSurfacesError(t *testing.T) {
	srv := newDetectorListingServer(t, "", "", http.StatusUnauthorized)

	b := newListingBuilder(nil)
	cfg := listingConfig("local", srv.URL+"/v1", "", "openai")
	listing, err := b.fetchProviderModels(context.Background(), "local", cfg)
	if err == nil {
		t.Fatal("expected the strict fallback leg to surface the 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %q, want mention of status 401", err)
	}
	if len(listing.Models) != 0 || len(listing.Metadata) != 0 {
		t.Errorf("listing = %+v, want empty on a failed listing", listing)
	}
}

// TestListProviderModels_FallbackLegVerbatimURL pins the settings listing's
// URL contract: the openai branch requests {base}/models — the base_url
// VERBATIM, the same URL the chat path derives from it. No /v1 suffix is
// invented, so a gateway mounting its OpenAI-compatible surface under another
// path (…/v1beta/openai) lists in Fetch Models exactly where it chats (the
// regression appended /v1 and turned a working endpoint into a 404 on the
// strict leg).
func TestListProviderModels_FallbackLegVerbatimURL(t *testing.T) {
	body := `{"data":[{"id":"m","max_model_len":4096}]}`
	var mu sync.Mutex
	paths := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	for _, tc := range []struct{ base, wantPath string }{
		{srv.URL, "/models"},
		{srv.URL + "/v1beta/openai", "/v1beta/openai/models"},
	} {
		mu.Lock()
		clear(paths)
		mu.Unlock()
		b := newListingBuilder(nil)
		cfg := listingConfig("local", tc.base, "", "openai")
		listing, err := b.fetchProviderModels(context.Background(), "local", cfg)
		if err != nil {
			t.Errorf("base %q: unexpected error: %v", tc.base, err)
			continue
		}
		mu.Lock()
		if paths[tc.wantPath] == 0 {
			t.Errorf("base %q: fallback leg never requested %q (requests: %v)", tc.base, tc.wantPath, paths)
		}
		if paths["/v1/models"] != 0 {
			t.Errorf("base %q: an invented /v1/models request was made (requests: %v); the settings leg must keep the base_url verbatim", tc.base, paths)
		}
		mu.Unlock()
		if !reflect.DeepEqual(listing.Models, []string{"m"}) {
			t.Errorf("base %q: models = %v, want [m]", tc.base, listing.Models)
		}
	}
}

// TestListProviderModels_NativeServedWhenFallbackLegFails verifies the other
// half of the error policy: when the native leg served entries and the strict
// fallback leg fails, the usable native listing is served (nil error) and the
// failed leg stays visible at Warn — intercepted here at the source.
func TestListProviderModels_NativeServedWhenFallbackLegFails(t *testing.T) {
	nativeBody := `{"data":[{"id":"lm-model","loaded_context_length":16384}]}`
	srv := newDetectorListingServer(t, nativeBody, "", http.StatusInternalServerError)

	capture := &recordCapture{}
	b := newListingBuilder(nil)
	b.logger = slog.New(capture)
	cfg := listingConfig("local", srv.URL+"/v1", "", "openai")
	listing, err := b.fetchProviderModels(context.Background(), "local", cfg)
	if err != nil {
		t.Fatalf("native entries must be served despite the failed fallback leg: %v", err)
	}
	if !reflect.DeepEqual(listing.Models, []string{"lm-model"}) {
		t.Errorf("models = %v, want [lm-model]", listing.Models)
	}
	if got := listing.Metadata["lm-model"].ContextWindow; got != 16384 {
		t.Errorf("metadata[lm-model].context_window = %d, want 16384", got)
	}

	// Expected diagnostics, intercepted at their source: exactly one Warn
	// naming the failed fallback leg and carrying the 500.
	var warns []slog.Record
	for _, r := range capture.snapshot() {
		if r.Level == slog.LevelWarn && strings.Contains(r.Message, "provider model listing fallback leg failed") {
			warns = append(warns, r)
		}
	}
	if len(warns) != 1 {
		t.Fatalf("fallback-leg failure warns = %d, want exactly 1 (records: %v)", len(warns), capture.snapshot())
	}
	assertRecordAttr(t, warns[0], "error", "status 500")
}

// assertRecordAttr asserts a captured slog record carries the expected
// key/value; values are matched as formatted strings ("status 500" arrives
// inside the wrapped error's text).
func assertRecordAttr(t *testing.T, r slog.Record, key, wantSubstr string) {
	t.Helper()
	found := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key != key {
			return true
		}
		found = true
		if !strings.Contains(a.Value.String(), wantSubstr) {
			t.Errorf("attr %s = %q, want substring %q", key, a.Value.String(), wantSubstr)
		}
		return false
	})
	if !found {
		t.Errorf("record %q missing attr %s", r.Message, key)
	}
}

// TestListProviderModels_AnthropicCompatibleNilMetadata documents the scope
// boundary: anthropic-compatible listings carry names only — the detector
// chain is an OpenAI-compatible surface.
func TestListProviderModels_AnthropicCompatibleNilMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-x"}]}`))
	}))
	t.Cleanup(srv.Close)

	b := newListingBuilder(nil)
	cfg := listingConfig("proxy", srv.URL, "", "anthropic")
	listing, err := b.fetchProviderModels(context.Background(), "proxy", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(listing.Models, []string{"claude-x"}) {
		t.Errorf("models = %v, want [claude-x]", listing.Models)
	}
	if listing.Metadata != nil {
		t.Errorf("metadata = %v, want nil", listing.Metadata)
	}
}
