package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"

	coretools "github.com/v0lka/c0wrk/core/tools"
)

// capturingJudgeProvider is a fake llm.Provider that records the model name of
// the last chat completion request and answers with a strict-judge ALLOW.
type capturingJudgeProvider struct {
	name     string
	gotModel string
}

func (p *capturingJudgeProvider) ChatCompletion(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	p.gotModel = req.Model
	return &llm.ChatResponse{
		Message:    llm.Message{Role: "assistant", Content: "VERDICT: ALLOW\nREASON: benign test command"},
		StopReason: "end_turn",
	}, nil
}

func (p *capturingJudgeProvider) Name() string { return p.name }

// judgeTestRouter builds a two-provider router. No endpoint is dialed at
// construction; the distinct BaseURLs keep the providers distinguishable.
func judgeTestRouter(t *testing.T) *llm.Router {
	t.Helper()
	router, err := llm.NewRouter(context.Background(), llm.RouterConfig{
		Providers: []llm.ProviderEntry{
			{Name: "provA", ProviderType: "openai", BaseURL: "http://127.0.0.1:1", Models: []string{"model-a"}},
			{Name: "provB", ProviderType: "openai", BaseURL: "http://127.0.0.1:2", Models: []string{"model-b"}},
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

// TestNewJudgeForProvider_BindsModelToGivenProvider verifies the judge
// constructor binds the judge to the GIVEN provider and model: without a
// judge-model pin the judge sends the bare active model; with
// security.judge.model set the pin wins; a nil provider yields no judge.
func TestNewJudgeForProvider_BindsModelToGivenProvider(t *testing.T) {
	b := &OrchestratorBuilder{registry: coretools.NewToolRegistry()}

	// No pin: judge model = bare active model.
	prov := &capturingJudgeProvider{name: "provA"}
	judge := b.newJudgeForProvider(&BuilderConfig{
		Orchestration: BuilderOrchestrationConfig{MaxJudgeCacheSize: 8},
	}, prov, "provA", "provA/model-a")
	if judge == nil {
		t.Fatal("expected non-nil judge for non-nil provider")
	}
	verdict, _, err := judge.JudgeStrict(context.Background(), sdktools.StrictJudgeRequest{
		ToolName:    "bash_exec",
		Input:       json.RawMessage(`{"command":"ls"}`),
		TaskContext: "test task context",
	})
	if err != nil {
		t.Fatalf("JudgeStrict failed: %v", err)
	}
	if verdict != sdktools.VerdictAllow {
		t.Fatalf("verdict = %v, want VerdictAllow", verdict)
	}
	if prov.gotModel != "model-a" {
		t.Errorf("judge provider received model %q, want bare \"model-a\"", prov.gotModel)
	}

	// security.judge.model pin wins over the active model.
	pinned := &capturingJudgeProvider{name: "provA"}
	pinnedJudge := b.newJudgeForProvider(&BuilderConfig{
		Security:      BuilderSecurityConfig{JudgeModel: "provA/pinned-judge"},
		Orchestration: BuilderOrchestrationConfig{MaxJudgeCacheSize: 8},
	}, pinned, "provA", "provA/model-a")
	if pinnedJudge == nil {
		t.Fatal("expected non-nil pinned judge")
	}
	if _, _, err := pinnedJudge.JudgeStrict(context.Background(), sdktools.StrictJudgeRequest{
		ToolName:    "bash_exec",
		Input:       json.RawMessage(`{"command":"ls"}`),
		TaskContext: "test task context",
	}); err != nil {
		t.Fatalf("JudgeStrict (pinned) failed: %v", err)
	}
	if pinned.gotModel != "pinned-judge" {
		t.Errorf("pinned judge provider received model %q, want \"pinned-judge\"", pinned.gotModel)
	}

	// Nil provider → no judge (caller keeps the previous binding).
	if got := b.newJudgeForProvider(&BuilderConfig{}, nil, "provA", "provA/model-a"); got != nil {
		t.Errorf("expected nil judge for nil provider, got %v", got)
	}
}

// TestSessionJudgeSyncer_BindsToSessionRouterOnly verifies the session-pinning
// invariant: the session judge is bound to the SESSION's own router, a global
// judge rebuild (default-model change elsewhere) never leaks into the live
// session registry, and only the session's own model switch re-binds it.
func TestSessionJudgeSyncer_BindsToSessionRouterOnly(t *testing.T) {
	b := &OrchestratorBuilder{registry: coretools.NewToolRegistry()}
	router := judgeTestRouter(t)

	sessionRegistry := coretools.NewToolRegistry()
	cfg := &BuilderConfig{Orchestration: BuilderOrchestrationConfig{MaxJudgeCacheSize: 8}}

	// Record which provider each judge binding resolves to. ToolJudge does
	// not expose its provider, so without the hook the binding could only be
	// asserted by pointer identity — which cannot prove the judge actually
	// points at the session router's active provider.
	var boundProviders []string
	cfg.JudgeProviderHook = func(p llm.Provider) llm.Provider {
		if p != nil {
			boundProviders = append(boundProviders, p.Name())
		}
		return p
	}

	sync := b.sessionJudgeSyncer(cfg, router, sessionRegistry)
	sync()
	sessionJudge := sessionRegistry.GetJudge()
	if sessionJudge == nil {
		t.Fatal("expected session judge bound at sync time")
	}
	if len(boundProviders) != 1 || boundProviders[0] != "provA" {
		t.Errorf("sessionJudgeSyncer initial sync bound providers %v, want [provA] (the router's initial active provider)", boundProviders)
	}

	// A global default-model change rebuilds the SHARED registry's judge only.
	// The live session registry must keep its own judge instance.
	globalJudge := sdktools.NewToolJudge(&capturingJudgeProvider{name: "provA"}, "global-model", 8, nil)
	b.registry.SetJudge(globalJudge)
	if got := sessionRegistry.GetJudge(); got != sessionJudge {
		t.Fatal("global judge rebuild leaked into the live session registry")
	}

	// A freshly cloned session registry (what a LATER Build would produce)
	// inherits the new global judge — future sessions follow the new default.
	freshClone := b.registry.Clone()
	if freshClone.GetJudge() != globalJudge {
		t.Fatal("fresh session clone should inherit the rebuilt global judge")
	}

	// The session's OWN model switch re-binds its judge to the new provider.
	if err := router.SetModel(context.Background(), "provB/model-b"); err != nil {
		t.Fatalf("SetModel failed: %v", err)
	}
	sync()
	rebound := sessionRegistry.GetJudge()
	if rebound == nil {
		t.Fatal("expected re-bound session judge after model switch")
	}
	if rebound == sessionJudge {
		t.Fatal("expected a NEW judge instance after the session's model switch")
	}
	// The stale global judge must not have been installed by the re-sync.
	if rebound == globalJudge {
		t.Fatal("session judge re-sync must not fall back to the global judge")
	}
	// The re-bound judge is built from the SESSION's new provider (provB) —
	// not merely a fresh instance on the old provider. This is the substance
	// of the pinning invariant the pointer checks above only approximate.
	if len(boundProviders) != 2 || boundProviders[1] != "provB" {
		t.Errorf("sessionJudgeSyncer post-switch sync bound providers %v, want [provA provB] (the session's own switched provider)", boundProviders)
	}
}

// TestApplyRequestOverrides_ResyncsSessionJudgeOnModelSwitch verifies the
// orchestrator invokes JudgeSync after a SUCCESSFUL in-session model switch,
// and never after a failed one (the model — and therefore the judge — stays
// unchanged when SetModel rejects the override).
func TestApplyRequestOverrides_ResyncsSessionJudgeOnModelSwitch(t *testing.T) {
	router := judgeTestRouter(t)

	syncCalls := 0
	orch := NewOrchestrator(OrchestratorConfig{Model: "model-a"}, OrchestratorDeps{
		ModelSwitcher: router,
		JudgeSync:     func() { syncCalls++ },
	})

	// Successful switch → judge re-synced exactly once.
	orch.ApplyRequestOverrides(context.Background(), "provB/model-b", "")
	if syncCalls != 1 {
		t.Errorf("JudgeSync calls after successful switch = %d, want 1", syncCalls)
	}
	if orch.config.Model != "model-b" {
		t.Errorf("config.Model = %q, want \"model-b\"", orch.config.Model)
	}

	// Failed switch (unknown model) → judge must NOT be re-synced.
	orch.ApplyRequestOverrides(context.Background(), "provB/unknown-model", "")
	if syncCalls != 1 {
		t.Errorf("JudgeSync calls after failed switch = %d, want 1 (no re-sync on failure)", syncCalls)
	}

	// Empty override → no-op, judge untouched.
	orch.ApplyRequestOverrides(context.Background(), "", "high")
	if syncCalls != 1 {
		t.Errorf("JudgeSync calls after empty override = %d, want 1", syncCalls)
	}
}
