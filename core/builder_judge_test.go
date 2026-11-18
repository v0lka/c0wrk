package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"

	coretools "github.com/v0lka/c0wrk/core/tools"
)

// judgeCaptureServer serves a minimal OpenAI /chat/completions endpoint that
// records the model name of every request and answers with a strict-judge
// ALLOW. The judge under test rides a router whose provider points here, so
// the recorded model is exactly what the judge's call resolved to on the wire
// (the judge sends NO model; the router fills its active one).
type judgeCaptureServer struct {
	mu    sync.Mutex
	model string
	calls int
	srv   *httptest.Server
}

func newJudgeCaptureServer(t *testing.T) *judgeCaptureServer {
	t.Helper()
	c := &judgeCaptureServer{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode judge request body: %v", err)
		}
		c.mu.Lock()
		c.model = body.Model
		c.calls++
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion","created":1700000000,"model":"` + body.Model +
			`","choices":[{"index":0,"message":{"role":"assistant","content":"VERDICT: ALLOW\nREASON: benign test command"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *judgeCaptureServer) snapshot() (model string, calls int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.model, c.calls
}

// judgeTestRouter builds a two-provider router whose providers point at the
// given capture servers (one bare model each). Retries are disabled so a
// failed call surfaces immediately.
func judgeTestRouter(t *testing.T, a, b *judgeCaptureServer) *llm.Router {
	t.Helper()
	router, err := llm.NewRouter(context.Background(), llm.RouterConfig{
		Providers: []llm.ProviderEntry{
			{Name: "provA", ProviderType: "openai", BaseURL: a.srv.URL + "/v1", Models: []string{"model-a"}},
			{Name: "provB", ProviderType: "openai", BaseURL: b.srv.URL + "/v1", Models: []string{"model-b"}},
		},
		MaxRetries:     -1,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("failed to build llm router: %v", err)
	}
	return router
}

// judgeStubCaller is a minimal llm.Caller answering a strict-judge ALLOW. It
// backs the shared-registry global judge used only for pointer-identity checks.
type judgeStubCaller struct{}

func (judgeStubCaller) Call(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{
		Message:    llm.Message{Role: "assistant", Content: "VERDICT: ALLOW\nREASON: benign test command"},
		StopReason: "end_turn",
	}, nil
}

// judgeStrictCall runs one strict-judge evaluation with a command unique
// enough to never collide with the judge's decision cache across calls.
func judgeStrictCall(t *testing.T, judge *sdktools.ToolJudge, command string) sdktools.JudgeVerdict {
	t.Helper()
	verdict, _, err := judge.JudgeStrict(context.Background(), sdktools.StrictJudgeRequest{
		ToolName:    "bash_exec",
		Input:       json.RawMessage(`{"command":"` + command + `"}`),
		TaskContext: "test task context",
	})
	if err != nil {
		t.Fatalf("JudgeStrict(%q) failed: %v", command, err)
	}
	return verdict
}

// TestNewJudgeForRouter_RidesRouterActiveModel verifies the judge follows the
// router's ACTIVE model by construction: no model is pinned anywhere, the
// router fills its active model into the model-less judge request, and a
// Router.SetModel switch is picked up by the next judge call with NO re-bind.
func TestNewJudgeForRouter_RidesRouterActiveModel(t *testing.T) {
	b := &OrchestratorBuilder{registry: coretools.NewToolRegistry()}
	srvA := newJudgeCaptureServer(t)
	srvB := newJudgeCaptureServer(t)
	router := judgeTestRouter(t, srvA, srvB)

	judge := b.newJudgeForRouter(&BuilderConfig{
		Orchestration: BuilderOrchestrationConfig{MaxJudgeCacheSize: 8},
	}, router, nil, nil)
	if judge == nil {
		t.Fatal("expected non-nil judge for a non-nil router")
	}

	if verdict := judgeStrictCall(t, judge, "ls"); verdict != sdktools.VerdictAllow {
		t.Fatalf("verdict = %v, want VerdictAllow", verdict)
	}
	if model, calls := srvA.snapshot(); model != "model-a" || calls != 1 {
		t.Errorf("provider A got model %q after %d calls, want \"model-a\" after 1 (the router's active model, filled by the router)", model, calls)
	}
	if _, calls := srvB.snapshot(); calls != 0 {
		t.Errorf("provider B saw %d calls before the switch, want 0", calls)
	}

	// The session's model switch: NO re-bind happens — the SAME judge
	// instance resolves to the new active model on its next call (the test
	// performs no registry.SetJudge in between; Router.SetModel is the only
	// mutation).
	if err := router.SetModel(context.Background(), "provB/model-b"); err != nil {
		t.Fatalf("SetModel failed: %v", err)
	}
	if verdict := judgeStrictCall(t, judge, "cat notes.txt"); verdict != sdktools.VerdictAllow {
		t.Fatalf("verdict after switch = %v, want VerdictAllow", verdict)
	}
	if model, calls := srvB.snapshot(); model != "model-b" || calls != 1 {
		t.Errorf("provider B got model %q after %d calls, want \"model-b\" after 1 (the switched active model — no re-bind)", model, calls)
	}
}

// TestNewJudgeForRouter_NilRouterYieldsNoJudge pins the fail-safe: a nil
// router yields no judge, and callers treat nil as "keep the previous judge".
func TestNewJudgeForRouter_NilRouterYieldsNoJudge(t *testing.T) {
	b := &OrchestratorBuilder{registry: coretools.NewToolRegistry()}
	if got := b.newJudgeForRouter(&BuilderConfig{}, nil, nil, nil); got != nil {
		t.Errorf("expected nil judge for nil router, got %v", got)
	}
}

// TestBindSessionJudge_BindsSessionRouterOnceAndFollowsModelSwitch verifies
// the session-judge invariant after the judge became a router rider: the
// one-time bind installs the SESSION router's judge on the session registry
// (overriding the clone-inherited shared fallback), a global judge rebuild
// never leaks into the live session, and the session's own model switch is
// followed by the SAME judge instance with no re-bind at all.
func TestBindSessionJudge_BindsSessionRouterOnceAndFollowsModelSwitch(t *testing.T) {
	b := &OrchestratorBuilder{registry: coretools.NewToolRegistry()}
	srvA := newJudgeCaptureServer(t)
	srvB := newJudgeCaptureServer(t)
	router := judgeTestRouter(t, srvA, srvB)
	cfg := &BuilderConfig{Orchestration: BuilderOrchestrationConfig{MaxJudgeCacheSize: 8}}

	// Mirror Build: the session registry is a clone of the shared registry,
	// then bindSessionJudge installs the session judge.
	sessionRegistry := b.registry.Clone()
	b.bindSessionJudge(cfg, router, nil, sessionRegistry, nil)
	sessionJudge := sessionRegistry.GetJudge()
	if sessionJudge == nil {
		t.Fatal("expected session judge bound at bind time")
	}

	// The session judge rides the SESSION router's active model.
	if verdict := judgeStrictCall(t, sessionJudge, "ls"); verdict != sdktools.VerdictAllow {
		t.Fatalf("verdict = %v, want VerdictAllow", verdict)
	}
	if model, _ := srvA.snapshot(); model != "model-a" {
		t.Errorf("session judge evaluated on model %q, want \"model-a\" (the session router's active model)", model)
	}

	// A global default-model change rebuilds the SHARED registry's judge only.
	// The live session registry must keep its own judge instance.
	globalJudge := sdktools.NewToolJudge(judgeStubCaller{}, nil, 8, nil)
	b.registry.SetJudge(globalJudge)
	if got := sessionRegistry.GetJudge(); got != sessionJudge {
		t.Fatal("global judge rebuild leaked into the live session registry")
	}

	// A freshly cloned session registry (what a LATER Build would produce)
	// inherits the new global judge — future sessions follow the new default.
	if freshClone := b.registry.Clone(); freshClone.GetJudge() != globalJudge {
		t.Fatal("fresh session clone should inherit the rebuilt global judge")
	}

	// The session's OWN model switch needs NO re-bind: the same judge
	// instance follows the router's new active model on its next call.
	if err := router.SetModel(context.Background(), "provB/model-b"); err != nil {
		t.Fatalf("SetModel failed: %v", err)
	}
	if got := sessionRegistry.GetJudge(); got != sessionJudge {
		t.Fatal("the model switch must not re-bind the session judge (the judge rides the router)")
	}
	if verdict := judgeStrictCall(t, sessionJudge, "cat notes.txt"); verdict != sdktools.VerdictAllow {
		t.Fatalf("verdict after switch = %v, want VerdictAllow", verdict)
	}
	if model, _ := srvB.snapshot(); model != "model-b" {
		t.Errorf("session judge evaluated on model %q after the switch, want \"model-b\" (followed the session's model switch without re-bind)", model)
	}
}

// ---------------------------------------------------------------------------
// llm.service_models.judge pin (service-model override on the judge caller)
// ---------------------------------------------------------------------------

// judgeGarbageServer serves a minimal OpenAI /chat/completions endpoint whose
// answers contain no VERDICT at all, driving the judge through its parse-
// failure nudge loop to the fail-safe. Like judgeCaptureServer it records the
// model of every request.
type judgeGarbageServer struct {
	mu    sync.Mutex
	model string
	calls int
	srv   *httptest.Server
}

func newJudgeGarbageServer(t *testing.T) *judgeGarbageServer {
	t.Helper()
	c := &judgeGarbageServer{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode judge request body: %v", err)
		}
		c.mu.Lock()
		c.model = body.Model
		c.calls++
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion","created":1700000000,"model":"` + body.Model +
			`","choices":[{"index":0,"message":{"role":"assistant","content":"I cannot help with that."},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *judgeGarbageServer) snapshot() (model string, calls int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.model, c.calls
}

// judgeOverrideCfg is the BuilderConfig carrying an llm.service_models judge
// pin at pin ("provider/model").
func judgeOverrideCfg(pin string) *BuilderConfig {
	return &BuilderConfig{
		Orchestration: BuilderOrchestrationConfig{MaxJudgeCacheSize: 8},
		LLM:           BuilderLLMConfig{ServiceModels: map[string]string{ServiceModelKeyJudge: pin}},
	}
}

// TestNewJudgeForRouter_JudgeOverridePinsProviderModel verifies the
// llm.service_models.judge pin: with a pin valid for this router, every judge
// call rides the PINNED provider/model — not the router's active model — and
// the pin is immune to session model switches (under a pin the judge no
// longer follows Router.SetModel).
func TestNewJudgeForRouter_JudgeOverridePinsProviderModel(t *testing.T) {
	b := &OrchestratorBuilder{registry: coretools.NewToolRegistry()}
	srvA := newJudgeCaptureServer(t)
	srvB := newJudgeCaptureServer(t)
	router := judgeTestRouter(t, srvA, srvB)
	cfg := judgeOverrideCfg("provB/model-b")

	judge := b.newJudgeForRouter(cfg, router, resolveServiceModelOverrides(router, cfg.LLM.ServiceModels, nil), nil)
	if judge == nil {
		t.Fatal("expected non-nil judge for a valid judge pin")
	}

	// The router's active model is provA/model-a, but the pin forces provB/model-b.
	if verdict := judgeStrictCall(t, judge, "ls"); verdict != sdktools.VerdictAllow {
		t.Fatalf("verdict = %v, want VerdictAllow", verdict)
	}
	if model, calls := srvB.snapshot(); model != "model-b" || calls != 1 {
		t.Errorf("provider B got model %q after %d calls, want \"model-b\" after 1 (the judge pin, overriding the active model)", model, calls)
	}
	if _, calls := srvA.snapshot(); calls != 0 {
		t.Errorf("provider A saw %d calls while the pin points at B, want 0", calls)
	}

	// Session model switches must NOT move the judge: switch to the pin's own
	// model and back to A — every call still lands on the pin.
	if err := router.SetModel(context.Background(), "provB/model-b"); err != nil {
		t.Fatalf("SetModel failed: %v", err)
	}
	if verdict := judgeStrictCall(t, judge, "cat notes.txt"); verdict != sdktools.VerdictAllow {
		t.Fatalf("verdict after switch to the pinned model = %v, want VerdictAllow", verdict)
	}
	if err := router.SetModel(context.Background(), "provA/model-a"); err != nil {
		t.Fatalf("SetModel failed: %v", err)
	}
	if verdict := judgeStrictCall(t, judge, "cat todo.txt"); verdict != sdktools.VerdictAllow {
		t.Fatalf("verdict after switch back to A = %v, want VerdictAllow", verdict)
	}
	if model, calls := srvB.snapshot(); model != "model-b" || calls != 3 {
		t.Errorf("provider B got model %q after %d calls, want \"model-b\" after 3 (every judge call pinned)", model, calls)
	}
	if _, calls := srvA.snapshot(); calls != 0 {
		t.Errorf("provider A saw %d calls despite two session model switches, want 0 (the judge never follows SetModel under a pin)", calls)
	}
}

// TestNewJudgeForRouter_JudgeOverrideCacheKeyTracksPin verifies the advisory
// judge's decision cache keys off the PIN, not the session's active model:
// the same tool call evaluated before and after a Router.SetModel is a cache
// hit under the pin (the pin is constant within a session), where the
// unpinned judge would re-evaluate because its key follows the switched
// active model.
func TestNewJudgeForRouter_JudgeOverrideCacheKeyTracksPin(t *testing.T) {
	b := &OrchestratorBuilder{registry: coretools.NewToolRegistry()}
	srvA := newJudgeCaptureServer(t)
	srvB := newJudgeCaptureServer(t)
	router := judgeTestRouter(t, srvA, srvB)
	cfg := judgeOverrideCfg("provB/model-b")

	judge := b.newJudgeForRouter(cfg, router, resolveServiceModelOverrides(router, cfg.LLM.ServiceModels, nil), nil)
	if judge == nil {
		t.Fatal("expected non-nil judge for a valid judge pin")
	}

	input := json.RawMessage(`{"command":"echo cache-pin-stability"}`)
	judgeAdvisoryVerdict := func() sdktools.JudgeVerdict {
		t.Helper()
		verdict, _, err := judge.Judge(context.Background(), "bash_exec", input, "test task context")
		if err != nil {
			t.Fatalf("Judge failed: %v", err)
		}
		return verdict
	}

	if verdict := judgeAdvisoryVerdict(); verdict != sdktools.VerdictAllow {
		t.Fatalf("verdict = %v, want VerdictAllow", verdict)
	}
	if _, calls := srvB.snapshot(); calls != 1 {
		t.Fatalf("provider B saw %d calls after the first evaluation, want 1", calls)
	}

	// The session switches its active model; the PIN is constant, so the
	// cache key is unchanged and the identical call is served from cache.
	if err := router.SetModel(context.Background(), "provB/model-b"); err != nil {
		t.Fatalf("SetModel failed: %v", err)
	}
	if verdict := judgeAdvisoryVerdict(); verdict != sdktools.VerdictAllow {
		t.Fatalf("cached verdict = %v, want VerdictAllow", verdict)
	}
	if _, calls := srvB.snapshot(); calls != 1 {
		t.Errorf("provider B saw %d calls after the identical re-evaluation, want 1 (cache hit — the key tracks the pin, not the switched session model)", calls)
	}
	if _, calls := srvA.snapshot(); calls != 0 {
		t.Errorf("provider A saw %d calls, want 0", calls)
	}
}

// TestBindSessionJudgeOverride_SessionPinAndSharedFallbackResolveOwnRouters
// verifies both judge bind paths resolve the configured pin against their OWN
// router: the per-session bind (the resolution Build made against the session
// router) pins the session judge, and the shared-registry fallback
// (rebuildJudgeInternal resolving the same map against the builder-level
// router it hands the judge) pins the shared judge — each to its own router's
// providers.
func TestBindSessionJudgeOverride_SessionPinAndSharedFallbackResolveOwnRouters(t *testing.T) {
	b := &OrchestratorBuilder{registry: coretools.NewToolRegistry()}
	srvA1 := newJudgeCaptureServer(t)
	srvB1 := newJudgeCaptureServer(t)
	sessionRouter := judgeTestRouter(t, srvA1, srvB1)
	srvA2 := newJudgeCaptureServer(t)
	srvB2 := newJudgeCaptureServer(t)
	sharedRouter := judgeTestRouter(t, srvA2, srvB2)
	cfg := judgeOverrideCfg("provB/model-b")

	// Per-session path: Build resolves the map against the SESSION router and
	// bindSessionJudge installs the pinned judge on the session registry.
	sessionRegistry := b.registry.Clone()
	b.bindSessionJudge(cfg, sessionRouter, resolveServiceModelOverrides(sessionRouter, cfg.LLM.ServiceModels, nil), sessionRegistry, nil)
	sessionJudge := sessionRegistry.GetJudge()
	if sessionJudge == nil {
		t.Fatal("expected session judge bound at bind time")
	}
	if verdict := judgeStrictCall(t, sessionJudge, "ls"); verdict != sdktools.VerdictAllow {
		t.Fatalf("session judge verdict = %v, want VerdictAllow", verdict)
	}
	if model, _ := srvB1.snapshot(); model != "model-b" {
		t.Errorf("session judge evaluated on model %q, want \"model-b\" (the session router's pin)", model)
	}
	if _, calls := srvB2.snapshot(); calls != 0 {
		t.Errorf("shared router's provider B saw %d calls from the session judge, want 0", calls)
	}

	// Shared-fallback path: rebuildJudgeInternal resolves the SAME map against
	// the BUILDER-level router it hands the judge.
	b.rebuildJudgeInternal(cfg, sharedRouter)
	sharedJudge := b.registry.GetJudge()
	if sharedJudge == nil {
		t.Fatal("expected shared fallback judge rebuilt")
	}
	if got := sessionRegistry.GetJudge(); got != sessionJudge {
		t.Fatal("shared rebuild leaked into the live session registry")
	}
	if verdict := judgeStrictCall(t, sharedJudge, "cat notes.txt"); verdict != sdktools.VerdictAllow {
		t.Fatalf("shared judge verdict = %v, want VerdictAllow", verdict)
	}
	if model, _ := srvB2.snapshot(); model != "model-b" {
		t.Errorf("shared judge evaluated on model %q, want \"model-b\" (the builder-level router's pin)", model)
	}
	if _, calls := srvB1.snapshot(); calls != 1 {
		t.Errorf("session router's provider B saw %d calls, want exactly the session judge's 1", calls)
	}
}

// TestNewJudgeForRouter_JudgeOverrideUnknownProviderFallsBackToActiveModel
// pins the fail-soft resolution: a judge pin naming a provider this router
// does not configure is dropped at resolution (the aggregated warn is pinned
// by model_overrides_test.go; the nil logger here keeps the output clean), so
// the judge rides the ACTIVE model exactly as without any override.
func TestNewJudgeForRouter_JudgeOverrideUnknownProviderFallsBackToActiveModel(t *testing.T) {
	b := &OrchestratorBuilder{registry: coretools.NewToolRegistry()}
	srvA := newJudgeCaptureServer(t)
	srvB := newJudgeCaptureServer(t)
	router := judgeTestRouter(t, srvA, srvB)
	cfg := judgeOverrideCfg("ghost/model-x")

	judge := b.newJudgeForRouter(cfg, router, resolveServiceModelOverrides(router, cfg.LLM.ServiceModels, nil), nil)
	if judge == nil {
		t.Fatal("expected non-nil judge")
	}

	if verdict := judgeStrictCall(t, judge, "ls"); verdict != sdktools.VerdictAllow {
		t.Fatalf("verdict = %v, want VerdictAllow", verdict)
	}
	if model, calls := srvA.snapshot(); model != "model-a" || calls != 1 {
		t.Errorf("provider A got model %q after %d calls, want \"model-a\" after 1 (the dropped pin falls back to the active model)", model, calls)
	}
	if _, calls := srvB.snapshot(); calls != 0 {
		t.Errorf("provider B saw %d calls, want 0", calls)
	}
}

// TestNewJudgeForRouter_JudgeOverrideFailureStillConfirms verifies the
// fail-safe contract survives the pin: a pinned judge whose provider answers
// unparseable content still lands on CONFIRM (never fail-open), with every
// attempt riding the pinned provider/model.
func TestNewJudgeForRouter_JudgeOverrideFailureStillConfirms(t *testing.T) {
	b := &OrchestratorBuilder{registry: coretools.NewToolRegistry()}
	srvValid := newJudgeCaptureServer(t)
	srvGarbage := newJudgeGarbageServer(t)
	router, err := llm.NewRouter(context.Background(), llm.RouterConfig{
		Providers: []llm.ProviderEntry{
			{Name: "provA", ProviderType: "openai", BaseURL: srvValid.srv.URL + "/v1", Models: []string{"model-a"}},
			{Name: "provB", ProviderType: "openai", BaseURL: srvGarbage.srv.URL + "/v1", Models: []string{"model-b"}},
		},
		MaxRetries:     -1,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("failed to build llm router: %v", err)
	}
	cfg := judgeOverrideCfg("provB/model-b")

	judge := b.newJudgeForRouter(cfg, router, resolveServiceModelOverrides(router, cfg.LLM.ServiceModels, nil), nil)
	if judge == nil {
		t.Fatal("expected non-nil judge for a valid judge pin")
	}

	verdict, reason, err := judge.JudgeStrict(context.Background(), sdktools.StrictJudgeRequest{
		ToolName:    "bash_exec",
		Input:       json.RawMessage(`{"command":"rm -rf /tmp/judge-pin-garbage"}`),
		TaskContext: "test task context",
	})
	if err != nil {
		t.Fatalf("JudgeStrict failed: %v", err)
	}
	if verdict != sdktools.VerdictConfirm {
		t.Errorf("verdict = %v, want VerdictConfirm (fail-safe preserved under the pin)", verdict)
	}
	// Unparseable answers exhaust the nudge loop into the parse-failure
	// fail-safe (the transport-error reason is a different, judge-side path).
	if reason != "Unable to parse judge response; requiring manual confirmation for safety" {
		t.Errorf("reason = %q, want the judge-unparsed fail-safe reason", reason)
	}
	if model, calls := srvGarbage.snapshot(); model != "model-b" || calls == 0 {
		t.Errorf("garbage provider got model %q after %d calls, want \"model-b\" after >=1 (the pinned provider served the failing attempts)", model, calls)
	}
	if _, calls := srvValid.snapshot(); calls != 0 {
		t.Errorf("valid provider saw %d calls, want 0 (the pin never leaks to the active provider)", calls)
	}
}
