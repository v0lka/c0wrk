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
