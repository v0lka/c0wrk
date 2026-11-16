package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// lmStudioModel mirrors the subset of the LM Studio /api/v0/models payload we
// care about, so tests can build responses programmatically.
type lmStudioModel struct {
	ID                  string `json:"id"`
	MaxContextLength    int    `json:"max_context_length"`
	LoadedContextLength int    `json:"loaded_context_length"`
	LoadedInstances     []struct {
		Config struct {
			ContextLength int `json:"context_length"`
		} `json:"config"`
	} `json:"loaded_instances"`
}

// newLMStudioServer returns an httptest.Server whose handler emits the given
// status code and body. It also records the last request path/method via the
// returned pointers, so URL-normalization tests can assert routing.
func newLMStudioServer(t *testing.T, status int, body []byte) (srv *httptest.Server, lastPath, lastMethod *string) {
	t.Helper()
	var path, method string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		method = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := w.Write(body); err != nil {
			t.Errorf("write body: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &path, &method
}

// listingWindow resolves one model's context window through the detector
// chain; 0 when the listing omits the model or no detector matches.
func listingWindow(t *testing.T, entries []rawModelEntry, id string) int {
	t.Helper()
	for _, e := range entries {
		if e.ID == id {
			meta, ok := detectModelMetadata(e.JSON)
			if !ok {
				return 0
			}
			return meta.ContextWindow
		}
	}
	return 0
}

func TestProbeLMStudioModels_CapacityOnly(t *testing.T) {
	// Model not loaded: loaded_instances is empty → must return capacity (262144).
	resp := struct {
		Data []lmStudioModel `json:"data"`
	}{
		Data: []lmStudioModel{
			{ID: "qwen2.5-coder-7b", MaxContextLength: 262144},
		},
	}
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	got, err := probeLMStudioEntries(context.Background(), srv.URL, "", nil, probeListingTimeout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 model, got %d: %v", len(got), got)
	}
	if w := listingWindow(t, got, "qwen2.5-coder-7b"); w != 262144 {
		t.Errorf("capacity: got %d, want 262144", w)
	}
}

func TestProbeLMStudioModels_RuntimePriority(t *testing.T) {
	// Model is loaded with runtime context_length 16384, even though capacity
	// is 262144 → runtime must win.
	loaded := lmStudioModel{
		ID:               "llama-3.1-8b",
		MaxContextLength: 262144,
	}
	loaded.LoadedInstances = append(loaded.LoadedInstances, struct {
		Config struct {
			ContextLength int `json:"context_length"`
		} `json:"config"`
	}{})
	loaded.LoadedInstances[0].Config.ContextLength = 16384

	body, err := json.Marshal(struct {
		Data []lmStudioModel `json:"data"`
	}{Data: []lmStudioModel{loaded}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	got, err := probeLMStudioEntries(context.Background(), srv.URL, "secret-key", nil, probeListingTimeout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w := listingWindow(t, got, "llama-3.1-8b"); w != 16384 {
		t.Errorf("runtime window: got %d, want 16384", w)
	}
}

func TestProbeLMStudioModels_LoadedContextLengthTopLevel(t *testing.T) {
	// Mirrors the REAL LM Studio /api/v0/models payload for a loaded model:
	// state="loaded", top-level "loaded_context_length" present, NO
	// "loaded_instances" array. The runtime value (16384) must win over the
	// advertised capacity (262144).
	resp := struct {
		Data []lmStudioModel `json:"data"`
	}{
		Data: []lmStudioModel{
			{
				ID:                  "qwen/qwen3.6-35b-a3b",
				MaxContextLength:    262144,
				LoadedContextLength: 16384,
			},
		},
	}
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	got, err := probeLMStudioEntries(context.Background(), srv.URL, "", nil, probeListingTimeout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w := listingWindow(t, got, "qwen/qwen3.6-35b-a3b"); w != 16384 {
		t.Errorf("loaded_context_length (top-level): got %d, want 16384", w)
	}
}

func TestProbeLMStudioModels_NotFound(t *testing.T) {
	// 404 (real OpenAI/vLLM server) → no entries + nil error (silent no-op).
	srv, _, _ := newLMStudioServer(t, http.StatusNotFound, []byte(`{"error":"not found"}`))

	got, err := probeLMStudioEntries(context.Background(), srv.URL, "", nil, probeListingTimeout)
	if err != nil {
		t.Fatalf("expected nil error on 404, got: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no entries on 404, got %d entries: %v", len(got), got)
	}
}

func TestProbeLMStudioModels_ServerErrorIsNonNil(t *testing.T) {
	// A transient 5xx (momentarily-unwell LM Studio) must surface as a non-nil
	// error so the caller can log a distinct, debuggable message instead of
	// silently dropping the model to the static default. Status codes < 500
	// remain a silent no-op (covered by TestProbeLMStudioModels_NotFound).
	for _, code := range []int{
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		srv, _, _ := newLMStudioServer(t, code, []byte(`{"error":"boom"}`))

		got, err := probeLMStudioEntries(context.Background(), srv.URL, "", nil, probeListingTimeout)
		if err == nil {
			t.Errorf("status %d: expected non-nil error, got nil", code)
		}
		if len(got) != 0 {
			t.Errorf("status %d: expected no entries, got %d entries: %v", code, len(got), got)
		}
	}
}

func TestProbeLMStudioModels_InvalidJSON(t *testing.T) {
	// Garbage body → no entries + non-nil error (non-fatal, logged by caller).
	srv, _, _ := newLMStudioServer(t, http.StatusOK, []byte("<<<not json>>>"))

	got, err := probeLMStudioEntries(context.Background(), srv.URL, "", nil, probeListingTimeout)
	if err == nil {
		t.Fatal("expected error on invalid JSON, got nil")
	}
	if len(got) != 0 {
		t.Errorf("expected no entries on invalid JSON, got %d entries: %v", len(got), got)
	}
}

func TestProbeLMStudioModels_URLNormalization(t *testing.T) {
	// All of these base URLs must resolve to the same server path "/api/v0/models".
	body := []byte(`{"data":[{"id":"m","max_context_length":8192}]}`)
	srv, lastPath, lastMethod := newLMStudioServer(t, http.StatusOK, body)

	cases := []string{
		srv.URL,          // http://127.0.0.1:PORT        (no /v1, no trailing slash)
		srv.URL + "/",    // http://127.0.0.1:PORT/       (trailing slash)
		srv.URL + "/v1",  // http://127.0.0.1:PORT/v1     (/v1 suffix)
		srv.URL + "/v1/", // http://127.0.0.1:PORT/v1/    (/v1 + trailing slash)
	}

	for _, base := range cases {
		*lastPath, *lastMethod = "", ""
		got, err := probeLMStudioEntries(context.Background(), base, "", nil, probeListingTimeout)
		if err != nil {
			t.Errorf("base %q: unexpected error: %v", base, err)
			continue
		}
		if *lastPath != "/api/v0/models" {
			t.Errorf("base %q: request path = %q, want /api/v0/models", base, *lastPath)
		}
		if *lastMethod != http.MethodGet {
			t.Errorf("base %q: request method = %q, want GET", base, *lastMethod)
		}
		if w := listingWindow(t, got, "m"); w != 8192 {
			t.Errorf("base %q: window = %d, want 8192", base, w)
		}
	}
}

func TestProbeLMStudioModels_EmptyData(t *testing.T) {
	// Valid payload with no models → no entries, nil error.
	srv, _, _ := newLMStudioServer(t, http.StatusOK, []byte(`{"data":[]}`))

	got, err := probeLMStudioEntries(context.Background(), srv.URL, "", nil, probeListingTimeout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no entries, got %d entries: %v", len(got), got)
	}
}

func TestProbeLMStudioModels_NilHTTPClientDefaults(t *testing.T) {
	// A nil httpClient must fall back to http.DefaultClient and still succeed.
	body := []byte(`{"data":[{"id":"m","max_context_length":4096}]}`)
	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	got, err := probeLMStudioEntries(context.Background(), srv.URL, "", nil, probeListingTimeout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w := listingWindow(t, got, "m"); w != 4096 {
		t.Errorf("window = %d, want 4096", w)
	}
}

// openAIModelEntry mirrors the subset of the /v1/models payload the OpenAI
// probe reads, so tests can build responses programmatically across the
// ecosystem field spellings (max_model_len / max_context_length /
// context_length).
type openAIModelEntry struct {
	ID            string `json:"id"`
	MaxModelLen   int    `json:"max_model_len"`
	MaxContextLen int    `json:"max_context_length"`
	ContextLen    int    `json:"context_length"`
}

func TestProbeOpenAIModels_MaxModelLen(t *testing.T) {
	// vLLM-style payload: the authoritative runtime limit lives in
	// "max_model_len".
	body, err := json.Marshal(struct {
		Data []openAIModelEntry `json:"data"`
	}{
		Data: []openAIModelEntry{
			{ID: "Qwen/Qwen2.5-Coder-32B-Instruct", MaxModelLen: 131072},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	got, err := probeOpenAIEntries(context.Background(), srv.URL, "", nil, probeListingTimeout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w := listingWindow(t, got, "Qwen/Qwen2.5-Coder-32B-Instruct"); w != 131072 {
		t.Errorf("max_model_len: got %d, want 131072", w)
	}
}

func TestProbeOpenAIModels_FieldPriorityAndFallbacks(t *testing.T) {
	// First non-zero field wins in priority order: max_model_len beats
	// max_context_length beats context_length. And entries reporting none of
	// them (the real OpenAI API shape) match nothing.
	body, err := json.Marshal(struct {
		Data []openAIModelEntry `json:"data"`
	}{
		Data: []openAIModelEntry{
			{ID: "alt-spelling", MaxContextLen: 32768}, // max_context_length only
			{ID: "tgi-style", ContextLen: 8192},        // context_length only
			{ID: "gpt-4o"},                             // none → not matched
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	got, err := probeOpenAIEntries(context.Background(), srv.URL, "", nil, probeListingTimeout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w := listingWindow(t, got, "alt-spelling"); w != 32768 {
		t.Errorf("max_context_length fallback: got %d, want 32768", w)
	}
	if w := listingWindow(t, got, "tgi-style"); w != 8192 {
		t.Errorf("context_length fallback: got %d, want 8192", w)
	}
	if w := listingWindow(t, got, "gpt-4o"); w != 0 {
		t.Errorf("model with no window field must not match, got %d", w)
	}
}

func TestProbeOpenAIModels_ClientErrorIsSilent(t *testing.T) {
	// A genuine cloud server returning 401/404 for the listing (or one that
	// simply omits the per-model window) is a silent no-op.
	for _, code := range []int{http.StatusUnauthorized, http.StatusNotFound} {
		srv, _, _ := newLMStudioServer(t, code, []byte(`{"error":"unauthorized"}`))
		got, err := probeOpenAIEntries(context.Background(), srv.URL, "", nil, probeListingTimeout)
		if err != nil {
			t.Errorf("status %d: expected nil error, got %v", code, err)
		}
		if len(got) != 0 {
			t.Errorf("status %d: expected no entries, got %d entries", code, len(got))
		}
	}
}

func TestProbeOpenAIModels_ServerErrorIsNonNil(t *testing.T) {
	srv, _, _ := newLMStudioServer(t, http.StatusBadGateway, []byte(`{"error":"boom"}`))
	got, err := probeOpenAIEntries(context.Background(), srv.URL, "", nil, probeListingTimeout)
	if err == nil {
		t.Errorf("expected non-nil error on 5xx, got nil")
	}
	if len(got) != 0 {
		t.Errorf("expected no entries on 5xx, got %d entries", len(got))
	}
}

func TestProbeOpenAIModels_URLNormalization(t *testing.T) {
	// Both bare host and /v1-suffixed bases must resolve to "/v1/models".
	body, err := json.Marshal(struct {
		Data []openAIModelEntry `json:"data"`
	}{
		Data: []openAIModelEntry{{ID: "m", MaxModelLen: 4096}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	srv, lastPath, lastMethod := newLMStudioServer(t, http.StatusOK, body)

	for _, base := range []string{
		srv.URL,         // http://127.0.0.1:PORT   → /v1/models appended
		srv.URL + "/v1", // http://127.0.0.1:PORT/v1 → /models appended
	} {
		*lastPath, *lastMethod = "", ""
		got, err := probeOpenAIEntries(context.Background(), base, "", nil, probeListingTimeout)
		if err != nil {
			t.Errorf("base %q: unexpected error: %v", base, err)
			continue
		}
		if *lastPath != "/v1/models" {
			t.Errorf("base %q: request path = %q, want /v1/models", base, *lastPath)
		}
		if *lastMethod != http.MethodGet {
			t.Errorf("base %q: method = %q, want GET", base, *lastMethod)
		}
		if w := listingWindow(t, got, "m"); w != 4096 {
			t.Errorf("base %q: window = %d, want 4096", base, w)
		}
	}
}

func TestProbeOpenAIModels_APIKeyHeader(t *testing.T) {
	// A non-empty apiKey must be sent as a Bearer header.
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	if _, err := probeOpenAIEntries(context.Background(), srv.URL, "tok-123", nil, probeListingTimeout); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok-123")
	}
}

// TestStrictOpenAIEntries_VerbatimURL pins the settings leg's URL contract:
// the base_url is used verbatim ({base}/models — the same URL the chat path
// derives from it). No /v1 suffix is invented, so a gateway mounting its
// OpenAI-compatible surface under another path (…/v1beta/openai) lists in
// Fetch Models exactly where it chats — the regression this guards against
// appended /v1 and turned a working endpoint into a strict-leg 404.
func TestStrictOpenAIEntries_VerbatimURL(t *testing.T) {
	body, err := json.Marshal(struct {
		Data []openAIModelEntry `json:"data"`
	}{
		Data: []openAIModelEntry{{ID: "m", MaxModelLen: 4096}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	srv, lastPath, _ := newLMStudioServer(t, http.StatusOK, body)

	for _, tc := range []struct{ base, wantPath string }{
		{srv.URL, "/models"},
		{srv.URL + "/v1", "/v1/models"},
		{srv.URL + "/v1beta/openai", "/v1beta/openai/models"},
	} {
		*lastPath = ""
		got, err := strictOpenAIEntries(context.Background(), tc.base, "", nil, settingsListingTimeout)
		if err != nil {
			t.Errorf("base %q: unexpected error: %v", tc.base, err)
			continue
		}
		if *lastPath != tc.wantPath {
			t.Errorf("base %q: request path = %q, want %q (base_url verbatim, no invented /v1)", tc.base, *lastPath, tc.wantPath)
		}
		if w := listingWindow(t, got, "m"); w != 4096 {
			t.Errorf("base %q: window = %d, want 4096", tc.base, w)
		}
	}
}

// TestStrictOpenAIEntries_ClientErrorSurfaces pins the strict half of the
// twin contract at the unit level: a 4xx on the settings leg is an actionable
// error (the best-effort probe twin swallows it — TestProbeOpenAIModels_
// ClientErrorIsSilent).
func TestStrictOpenAIEntries_ClientErrorSurfaces(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusNotFound} {
		srv, _, _ := newLMStudioServer(t, code, []byte(`{"error":"unauthorized"}`))
		got, err := strictOpenAIEntries(context.Background(), srv.URL, "", nil, settingsListingTimeout)
		if err == nil {
			t.Errorf("status %d: expected an error on the strict settings leg, got nil", code)
		}
		if len(got) != 0 {
			t.Errorf("status %d: expected no entries, got %d entries", code, len(got))
		}
	}
}

func TestProbeSelfHostedMetadata_LMStudioWins(t *testing.T) {
	// The native listing resolves the model → the /v1 fallback leg is never
	// consulted, even though the same server's /v1 listing would report a
	// different window.
	lmBody := lmStudioModelsPayload(t, lmStudioModel{ID: "m", MaxContextLength: 16384})
	var v1Hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v0/models" {
			_, _ = w.Write(lmBody)
			return
		}
		if r.URL.Path == "/v1/models" {
			v1Hit = true
			_, _ = w.Write([]byte(`{"data":[{"id":"m","max_model_len":99999}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	metadata, err := probeSelfHostedMetadata(context.Background(), srv.URL, "", "m", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v1Hit {
		t.Error("fallback /v1/models consulted although the native listing resolved the model")
	}
	if w := metadata["m"].ContextWindow; w != 16384 {
		t.Errorf("native window: got %d, want 16384", w)
	}
}

func TestProbeSelfHostedMetadata_FallsBackToOpenAI(t *testing.T) {
	// LM Studio returns 404 (it is a vLLM server, not LM Studio) → the probe
	// falls back to /v1/models and discovers max_model_len.
	oaiBody, _ := json.Marshal(struct {
		Data []openAIModelEntry `json:"data"`
	}{
		Data: []openAIModelEntry{{ID: "m", MaxModelLen: 262144}},
	})
	// A single server that 404s on /api/v0/models (LM Studio path) but serves
	// /v1/models. Build a discriminating handler.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v0/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(oaiBody)
	}))
	t.Cleanup(srv.Close)

	metadata, err := probeSelfHostedMetadata(context.Background(), srv.URL, "", "m", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w := metadata["m"].ContextWindow; w != 262144 {
		t.Errorf("fallback window: got %d, want 262144", w)
	}
}

func TestProbeSelfHostedMetadata_BothMissReturnsEmpty(t *testing.T) {
	// A genuine cloud provider: LM Studio path 404s, /v1/models serves a
	// listing WITHOUT a window field → empty metadata, nil error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v0/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o","object":"model"}]}`))
	}))
	t.Cleanup(srv.Close)

	metadata, err := probeSelfHostedMetadata(context.Background(), srv.URL, "", "gpt-4o", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(metadata) != 0 {
		t.Errorf("expected empty metadata when no source reports a window, got %v", metadata)
	}
}

func TestProbeSelfHostedMetadata_NoOverwriteByFallback(t *testing.T) {
	// The fallback leg must never overwrite an entry the native listing
	// resolved: SetRuntimeMetadata replaces wholesale, so the /v1 capacity
	// would trade away the native runtime window.
	lmBody, _ := json.Marshal(struct {
		Data []lmStudioModel `json:"data"`
	}{
		Data: []lmStudioModel{{
			ID:                  "a",
			MaxContextLength:    262144,
			LoadedContextLength: 16384,
		}},
	})
	oaiBody, _ := json.Marshal(struct {
		Data []openAIModelEntry `json:"data"`
	}{
		Data: []openAIModelEntry{
			{ID: "a", MaxContextLen: 262144}, // capacity spelling — must lose
			{ID: "b", MaxModelLen: 99999},
		},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v0/models" {
			_, _ = w.Write(lmBody)
			return
		}
		_, _ = w.Write(oaiBody)
	}))
	t.Cleanup(srv.Close)

	// Active model "b" is absent from the native listing → the fallback leg
	// runs for it and must not clobber "a".
	metadata, err := probeSelfHostedMetadata(context.Background(), srv.URL, "", "b", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w := metadata["a"].ContextWindow; w != 16384 {
		t.Errorf("native runtime window overwritten by fallback: got %d, want 16384", w)
	}
	if w := metadata["b"].ContextWindow; w != 99999 {
		t.Errorf("fallback-discovered model: got %d, want 99999", w)
	}
}

func TestProbeSelfHostedMetadata_FallbackErrorKeepsDiscovered(t *testing.T) {
	// A failing fallback leg reports its error but must not discard the
	// metadata the native listing already discovered — the caller writes it
	// and surfaces the error once.
	lmBody := lmStudioModelsPayload(t, lmStudioModel{ID: "a", MaxContextLength: 16384})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v0/models" {
			_, _ = w.Write(lmBody)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	t.Cleanup(srv.Close)

	metadata, err := probeSelfHostedMetadata(context.Background(), srv.URL, "", "b", nil, nil)
	if err == nil {
		t.Fatal("expected the fallback leg's error, got nil")
	}
	if w := metadata["a"].ContextWindow; w != 16384 {
		t.Errorf("discovered metadata dropped on fallback error: got %d, want 16384", w)
	}
	if _, ok := metadata["b"]; ok {
		t.Error("active model must stay absent when every source missed it")
	}
}
