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

	got, err := probeLMStudioModels(context.Background(), srv.URL, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 model, got %d: %v", len(got), got)
	}
	if w := got["qwen2.5-coder-7b"]; w != 262144 {
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

	got, err := probeLMStudioModels(context.Background(), srv.URL, "secret-key", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w := got["llama-3.1-8b"]; w != 16384 {
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

	got, err := probeLMStudioModels(context.Background(), srv.URL, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w := got["qwen/qwen3.6-35b-a3b"]; w != 16384 {
		t.Errorf("loaded_context_length (top-level): got %d, want 16384", w)
	}
}

func TestProbeLMStudioModels_NotFound(t *testing.T) {
	// 404 (real OpenAI/vLLM server) → empty map + nil error (silent no-op).
	srv, _, _ := newLMStudioServer(t, http.StatusNotFound, []byte(`{"error":"not found"}`))

	got, err := probeLMStudioModels(context.Background(), srv.URL, "", nil)
	if err != nil {
		t.Fatalf("expected nil error on 404, got: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map on 404, got %d entries: %v", len(got), got)
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

		got, err := probeLMStudioModels(context.Background(), srv.URL, "", nil)
		if err == nil {
			t.Errorf("status %d: expected non-nil error, got nil", code)
		}
		if len(got) != 0 {
			t.Errorf("status %d: expected empty map, got %d entries: %v", code, len(got), got)
		}
	}
}

func TestProbeLMStudioModels_InvalidJSON(t *testing.T) {
	// Garbage body → empty map + non-nil error (non-fatal, logged by caller).
	srv, _, _ := newLMStudioServer(t, http.StatusOK, []byte("<<<not json>>>"))

	got, err := probeLMStudioModels(context.Background(), srv.URL, "", nil)
	if err == nil {
		t.Fatal("expected error on invalid JSON, got nil")
	}
	if len(got) != 0 {
		t.Errorf("expected empty map on invalid JSON, got %d entries: %v", len(got), got)
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
		got, err := probeLMStudioModels(context.Background(), base, "", nil)
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
		if w := got["m"]; w != 8192 {
			t.Errorf("base %q: window = %d, want 8192", base, w)
		}
	}
}

func TestProbeLMStudioModels_EmptyData(t *testing.T) {
	// Valid payload with no models → empty map, nil error.
	srv, _, _ := newLMStudioServer(t, http.StatusOK, []byte(`{"data":[]}`))

	got, err := probeLMStudioModels(context.Background(), srv.URL, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %d entries: %v", len(got), got)
	}
}

func TestProbeLMStudioModels_NilHTTPClientDefaults(t *testing.T) {
	// A nil httpClient must fall back to http.DefaultClient and still succeed.
	body := []byte(`{"data":[{"id":"m","max_context_length":4096}]}`)
	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	got, err := probeLMStudioModels(context.Background(), srv.URL, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w := got["m"]; w != 4096 {
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

	got, err := probeOpenAIModels(context.Background(), srv.URL, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w := got["Qwen/Qwen2.5-Coder-32B-Instruct"]; w != 131072 {
		t.Errorf("max_model_len: got %d, want 131072", w)
	}
}

func TestProbeOpenAIModels_FieldPriorityAndFallbacks(t *testing.T) {
	// First non-zero field wins in priority order: max_model_len beats
	// max_context_length beats context_length. And entries reporting none of
	// them (the real OpenAI API shape) map to nothing.
	body, err := json.Marshal(struct {
		Data []openAIModelEntry `json:"data"`
	}{
		Data: []openAIModelEntry{
			{ID: "alt-spelling", MaxContextLen: 32768}, // max_context_length only
			{ID: "tgi-style", ContextLen: 8192},        // context_length only
			{ID: "gpt-4o"},                             // none → skipped
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	got, err := probeOpenAIModels(context.Background(), srv.URL, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w := got["alt-spelling"]; w != 32768 {
		t.Errorf("max_context_length fallback: got %d, want 32768", w)
	}
	if w := got["tgi-style"]; w != 8192 {
		t.Errorf("context_length fallback: got %d, want 8192", w)
	}
	if _, ok := got["gpt-4o"]; ok {
		t.Errorf("model with no window field must be skipped, found gpt-4o")
	}
}

func TestProbeOpenAIModels_ClientErrorIsSilent(t *testing.T) {
	// A genuine cloud server returning 401/404 for the listing (or one that
	// simply omits the per-model window) is a silent no-op.
	for _, code := range []int{http.StatusUnauthorized, http.StatusNotFound} {
		srv, _, _ := newLMStudioServer(t, code, []byte(`{"error":"unauthorized"}`))
		got, err := probeOpenAIModels(context.Background(), srv.URL, "", nil)
		if err != nil {
			t.Errorf("status %d: expected nil error, got %v", code, err)
		}
		if len(got) != 0 {
			t.Errorf("status %d: expected empty map, got %d entries", code, len(got))
		}
	}
}

func TestProbeOpenAIModels_ServerErrorIsNonNil(t *testing.T) {
	srv, _, _ := newLMStudioServer(t, http.StatusBadGateway, []byte(`{"error":"boom"}`))
	got, err := probeOpenAIModels(context.Background(), srv.URL, "", nil)
	if err == nil {
		t.Errorf("expected non-nil error on 5xx, got nil")
	}
	if len(got) != 0 {
		t.Errorf("expected empty map on 5xx, got %d entries", len(got))
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
		got, err := probeOpenAIModels(context.Background(), base, "", nil)
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
		if w := got["m"]; w != 4096 {
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

	if _, err := probeOpenAIModels(context.Background(), srv.URL, "tok-123", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok-123")
	}
}

func TestProbeSelfHostedContextWindow_LMStudioWins(t *testing.T) {
	// The LM Studio endpoint reports the (runtime) window → it is returned
	// without touching /v1/models. Build an LM Studio server returning the
	// window, and an OpenAI server that would report a *different* window; the
	// LM Studio value must win.
	lmBody, _ := json.Marshal(struct {
		Data []lmStudioModel `json:"data"`
	}{
		Data: []lmStudioModel{{ID: "m", MaxContextLength: 16384}},
	})
	lmSrv, _, _ := newLMStudioServer(t, http.StatusOK, lmBody)

	// OpenAI server that would override to 99999 if consulted — proving it
	// was never reached.
	oaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m","max_model_len":99999}]}`))
	}))
	t.Cleanup(oaiSrv.Close)

	// Use the LM Studio server URL for the base so the probe hits it first.
	w, err := probeSelfHostedContextWindow(context.Background(), lmSrv.URL, "", "m", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 16384 {
		t.Errorf("expected LM Studio window 16384 to win, got %d", w)
	}
}

func TestProbeSelfHostedContextWindow_FallsBackToOpenAI(t *testing.T) {
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

	w, err := probeSelfHostedContextWindow(context.Background(), srv.URL, "", "m", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 262144 {
		t.Errorf("expected OpenAI fallback window 262144, got %d", w)
	}
}

func TestProbeSelfHostedContextWindow_BothMissReturnsZero(t *testing.T) {
	// A genuine cloud provider: LM Studio path 404s, /v1/models serves a
	// listing WITHOUT a window field → 0, nil (no useful result).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v0/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o","object":"model"}]}`))
	}))
	t.Cleanup(srv.Close)

	w, err := probeSelfHostedContextWindow(context.Background(), srv.URL, "", "gpt-4o", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 0 {
		t.Errorf("expected 0 when no source reports a window, got %d", w)
	}
}
