package core

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/embeddedllm"
	"github.com/v0lka/sp4rk/llm"
)

// This file pins the guarantee the user asked for in so many words: when the
// embedded model is selected, NO LLM request may start before the model is
// loaded and ready to answer.
//
// The hole it closes was structural, not local. A BuilderConfig is converted in
// more than one place, and the one that builds the PER-SESSION router — the
// orchestrator factory closed over inside backend.NewApplication — converts the
// live config directly, with no path to the supervisor. Injecting the seam only
// at the backend's conversion sites therefore left every session router without
// the ensure-loaded transport: a chat request to a cold model was dispatched
// straight to a loopback socket nothing was listening on. The fix is a
// builder-level default (SetEmbeddedLLM) resolved inside buildRouter, which is
// the one place every router is constructed — so it cannot be missed by a new
// conversion site.

// embeddedModelName is the model the backend-owned provider entry advertises
// (config.EmbeddedLLMModelName; core deliberately does not import backend).
const embeddedModelName = "Bonsai 2 27B"

// orderingLoader records the sequence number of each Load so a test can assert
// the load happened BEFORE the endpoint saw the request — which is the whole
// requirement, stated as an ordering.
type orderingLoader struct {
	seq    *atomic.Int64
	loaded atomic.Int64
	delay  time.Duration
	err    error
}

func (l *orderingLoader) Load(context.Context) error {
	if l.delay > 0 {
		time.Sleep(l.delay)
	}
	l.loaded.Store(l.seq.Add(1))
	return l.err
}

func (l *orderingLoader) MarkActivity() {}

// chatCompletionBody is a minimal well-formed /chat/completions response.
const chatCompletionBody = `{"id":"1","object":"chat.completion","created":1700000000,` +
	`"model":"` + embeddedModelName + `","choices":[{"index":0,` +
	`"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// newEmbeddedRouterServer serves /chat/completions and records the sequence
// number the request arrived at, so the caller can compare it with the load's.
func newEmbeddedRouterServer(t *testing.T, seq, arrived *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived.Store(seq.Add(1))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionBody))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// embeddedRouterCfg is the shape the session factory produces: a plain
// conversion of the live config, carrying NO embedded seam at all.
func embeddedRouterCfg(baseURL string) *BuilderConfig {
	return &BuilderConfig{
		LLM: BuilderLLMConfig{
			DefaultModel: "embedded/" + embeddedModelName,
			ProviderConfigs: map[string]BuilderProviderConfig{
				"embedded": {
					ProviderType: "openai",
					BaseURL:      baseURL + "/v1",
					Models:       []string{embeddedModelName},
				},
			},
		},
		Timeouts:      BuilderTimeoutsConfig{LLMRequestTimeout: 60},
		ExpandEnvVars: identityExpand,
	}
}

func newEmbeddedRouterBuilder(t *testing.T) *OrchestratorBuilder {
	t.Helper()
	return &OrchestratorBuilder{
		logger:  slog.New(slog.DiscardHandler),
		goAsync: func(fn func()) { fn() },
	}
}

// callEmbeddedOnce builds a router from cfg and issues one chat request through
// it, returning the response.
func callEmbeddedOnce(t *testing.T, b *OrchestratorBuilder, cfg *BuilderConfig) (*llm.ChatResponse, error) {
	t.Helper()
	router, _, err := b.buildRouter(t.Context(), cfg)
	if err != nil {
		t.Fatalf("buildRouter: %v", err)
	}
	if got := router.ActiveProviderName(); got != "embedded" {
		t.Fatalf("active provider = %q, want %q", got, "embedded")
	}
	return router.Call(t.Context(), llm.ChatRequest{
		Messages:  []llm.Message{{Role: "user", Content: "ping"}},
		MaxTokens: 8,
	})
}

// TestBuildRouterGatesTheRequestWhenOnlyTheBuilderSeamIsSet is the regression
// for the per-session hole: the BuilderConfig carries no seam — exactly what the
// session factory produces — and the request must still wait for the load.
func TestBuildRouterGatesTheRequestWhenOnlyTheBuilderSeamIsSet(t *testing.T) {
	var seq, arrived atomic.Int64
	loader := &orderingLoader{seq: &seq}
	srv := newEmbeddedRouterServer(t, &seq, &arrived)

	b := newEmbeddedRouterBuilder(t)
	b.SetEmbeddedLLM(BuilderEmbeddedLLMConfig{ProviderName: "embedded", Loader: loader})

	resp, err := callEmbeddedOnce(t, b, embeddedRouterCfg(srv.URL))
	if err != nil {
		t.Fatalf("the request through a router built without a per-config seam: %v", err)
	}
	if resp == nil || resp.Message.Content != "ok" {
		t.Fatalf("response = %+v, want the endpoint's reply", resp)
	}

	gotLoaded, gotArrived := loader.loaded.Load(), arrived.Load()
	if gotLoaded == 0 {
		t.Fatal("the model was never loaded: the router carries no ensure-loaded " +
			"transport, so a cold request would be dispatched to a dead socket")
	}
	if gotArrived == 0 {
		t.Fatal("the endpoint never saw the request")
	}
	if gotLoaded >= gotArrived {
		t.Errorf("the request reached the endpoint at seq %d, the load completed at %d: "+
			"an LLM request started before the model was ready", gotArrived, gotLoaded)
	}
}

// TestBuildRouterDoesNotGateWithoutAnySeam is the other half of the same
// guarantee: with neither a per-config nor a builder-level seam, nothing is
// loaded — the transport must not appear out of nowhere and hijack a provider
// the user configured themselves.
func TestBuildRouterDoesNotGateWithoutAnySeam(t *testing.T) {
	var seq, arrived atomic.Int64
	srv := newEmbeddedRouterServer(t, &seq, &arrived)

	b := newEmbeddedRouterBuilder(t)
	if _, err := callEmbeddedOnce(t, b, embeddedRouterCfg(srv.URL)); err != nil {
		t.Fatalf("the request must succeed without a seam: %v", err)
	}
	if arrived.Load() == 0 {
		t.Error("the endpoint never saw the request")
	}
}

// TestEmbeddedSeamPrecedence pins the resolution rule: an explicit per-build
// Loader wins, and the builder-level default fills in only when the config
// carries none. A half-populated per-config seam (a name with no supervisor)
// must NOT shadow the builder default — that is the state applyEmbeddedLoader
// leaves when nothing is installed.
func TestEmbeddedSeamPrecedence(t *testing.T) {
	perBuild := &orderingLoader{seq: &atomic.Int64{}}
	builderLevel := &orderingLoader{seq: &atomic.Int64{}}

	b := newEmbeddedRouterBuilder(t)
	b.SetEmbeddedLLM(BuilderEmbeddedLLMConfig{ProviderName: "embedded", Loader: builderLevel})

	t.Run("per-config loader wins", func(t *testing.T) {
		cfg := embeddedRouterCfg("http://127.0.0.1:1")
		cfg.EmbeddedLLM = BuilderEmbeddedLLMConfig{ProviderName: "embedded", Loader: perBuild}
		if got := b.embeddedSeam(cfg); got.Loader != embeddedllm.Loader(perBuild) {
			t.Errorf("seam loader = %v, want the per-config one %p", got.Loader, perBuild)
		}
	})

	t.Run("builder default fills the gap", func(t *testing.T) {
		if got := b.embeddedSeam(embeddedRouterCfg("http://127.0.0.1:1")); got.Loader == nil {
			t.Error("seam loader = nil, want the builder-level default")
		}
	})

	t.Run("a name without a loader does not shadow the default", func(t *testing.T) {
		cfg := embeddedRouterCfg("http://127.0.0.1:1")
		cfg.EmbeddedLLM = BuilderEmbeddedLLMConfig{ProviderName: "embedded"}
		if got := b.embeddedSeam(cfg); got.Loader == nil {
			t.Error("seam loader = nil; a half-populated per-config seam must fall " +
				"back to the builder default rather than guard nothing")
		}
	})

	t.Run("withdrawn default guards nothing", func(t *testing.T) {
		b.SetEmbeddedLLM(BuilderEmbeddedLLMConfig{})
		if got := b.embeddedSeam(embeddedRouterCfg("http://127.0.0.1:1")); got.Loader != nil {
			t.Errorf("seam = %+v after the default was withdrawn; a removal must "+
				"stop guarding the name", got)
		}
	})
}
