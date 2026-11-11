package core

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// --- Helpers ---

// captureMetaContextFactory records the llm.ModelMetadata the ContextManager
// was built with, so a test can assert which window a subagent CM was sized
// against.
func captureMetaContextFactory(meta *llm.ModelMetadata) ContextManagerFactory {
	return func(systemPrompt string, m llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) ContextManager {
		if meta != nil {
			*meta = m
		}
		return &mockContextManager{systemPrompt: systemPrompt}
	}
}

// recordingModelOverrideValidator is an accepting ModelOverrideValidator that
// records every (agentName, model) it validated.
type recordingModelOverrideValidator struct {
	calls [][2]string
}

func (v *recordingModelOverrideValidator) validate(agentName, model string) error {
	v.calls = append(v.calls, [2]string{agentName, model})
	return nil
}

// unknownProviderValidator mirrors the builder closure's failure shape: a
// composite naming an unknown provider fails with profile, model, and the
// known providers named.
func unknownProviderValidator(agentName, model string) error {
	provider, _, composite := llm.ParseCompositeModelID(model)
	if !composite {
		return nil
	}
	if provider != "local-llm" {
		return fmt.Errorf("profile %q: model override %q names unknown provider %q — known providers: local-llm, openai",
			agentName, model, provider)
	}
	return nil
}

// modelOverrideLauncher builds a launcher wired for buildSubAgentTask with the
// given context factory, validator, LLM caller mock, and model registry.
func modelOverrideLauncher(cf ContextManagerFactory, validator tools.ModelOverrideValidator, caller agent.LLMCaller, registry *llm.ModelRegistry) *conductorLauncher {
	return &conductorLauncher{deps: conductorDeps{
		contextFactory:        cf,
		toolRegistry:          newSubagentTestRegistry(subagentToolSet()),
		llm:                   caller,
		toolExec:              &mockToolExecutor{},
		tokenCounter:          llm.NewSimpleTokenCounter(),
		modelRegistry:         registry,
		model:                 "gpt-4o",
		validateModelOverride: validator,
	}}
}

// modelOverrideProfileCtx returns the minimal context buildSubAgentTask needs
// for a profile'd delegation, with the profile resolvable under agentName.
func modelOverrideProfileCtx(agentName string, profile *agents.Agent) context.Context {
	ctx := tools.WithAgentResolver(sdktools.WithWorkspacePath(context.Background(), "/ws"), func(name string) (*agents.Agent, bool) {
		if name == agentName {
			return profile, true
		}
		return nil, false
	})
	return WithComplexity(ctx, 1)
}

// --- Composite routing ---

// TestBuildSubAgentTask_CompositeModelRoutesCrossProvider verifies a profile
// with a composite "provider/model" override pins BOTH req.Provider and the
// bare req.Model on the subagent's LLM calls (sp4rk ADR-011): the request
// routes to the named provider, not the session's active one.
func TestBuildSubAgentTask_CompositeModelRoutesCrossProvider(t *testing.T) {
	profile := testAgentProfile("body", agents.AgentMetadata{Name: "reviewer", Model: "local-llm/qwen3:8b"})
	validator := &recordingModelOverrideValidator{}
	caller := &mockLLMCaller{responses: []*llm.ChatResponse{executorFinishResponse("done")}}

	l := modelOverrideLauncher(captureMetaContextFactory(nil), validator.validate, caller, nil)
	ctx := modelOverrideProfileCtx("reviewer", profile)

	st, err := l.buildSubAgentTask(ctx, tools.DelegationTask{ID: "d1", Summary: "s", Task: "do work", Agent: "reviewer"}, tools.NewDelegationRegistry(), &agent.NoopEvents{})
	if err != nil {
		t.Fatalf("buildSubAgentTask: %v", err)
	}
	if _, err := st.Executor.Run(ctx, st.TaskTools, st.CM); err != nil {
		t.Fatalf("executor run: %v", err)
	}

	if len(caller.calls) != 1 {
		t.Fatalf("caller saw %d requests, want 1", len(caller.calls))
	}
	if got := caller.calls[0].Provider; got != "local-llm" {
		t.Errorf("req.Provider = %q, want %q (composite must route cross-provider)", got, "local-llm")
	}
	if got := caller.calls[0].Model; got != "qwen3:8b" {
		t.Errorf("req.Model = %q, want the bare override model %q", got, "qwen3:8b")
	}
	// The validator saw the RAW profile value (agent name + full composite).
	if len(validator.calls) != 1 || validator.calls[0] != [2]string{"reviewer", "local-llm/qwen3:8b"} {
		t.Errorf("validator calls = %v, want [[reviewer local-llm/qwen3:8b]]", validator.calls)
	}
}

// TestBuildSubAgentTask_CompositeModelNilValidatorStillRoutes verifies the
// nil-safe skip: without a wired validator the composite override still wraps
// the caller (the router's per-request provider miss is the backstop).
func TestBuildSubAgentTask_CompositeModelNilValidatorStillRoutes(t *testing.T) {
	profile := testAgentProfile("body", agents.AgentMetadata{Name: "reviewer", Model: "local-llm/qwen3:8b"})
	caller := &mockLLMCaller{responses: []*llm.ChatResponse{executorFinishResponse("done")}}

	l := modelOverrideLauncher(captureMetaContextFactory(nil), nil, caller, nil)
	ctx := modelOverrideProfileCtx("reviewer", profile)

	st, err := l.buildSubAgentTask(ctx, tools.DelegationTask{ID: "d1", Summary: "s", Task: "do work", Agent: "reviewer"}, tools.NewDelegationRegistry(), &agent.NoopEvents{})
	if err != nil {
		t.Fatalf("buildSubAgentTask: %v (nil validator must skip, not fail)", err)
	}
	if _, err := st.Executor.Run(ctx, st.TaskTools, st.CM); err != nil {
		t.Fatalf("executor run: %v", err)
	}
	if len(caller.calls) != 1 || caller.calls[0].Provider != "local-llm" || caller.calls[0].Model != "qwen3:8b" {
		t.Errorf("calls = %+v, want one request pinned to (local-llm, qwen3:8b)", caller.calls)
	}
}

// --- Fail-closed before launch ---

// TestBuildSubAgentTask_CompositeModelUnknownProviderFailsClosed verifies the
// defensive launch-side re-validation: a composite override whose provider the
// router does not know fails buildSubAgentTask with an error naming the
// profile, the model, and the known providers — before any subagent runs.
func TestBuildSubAgentTask_CompositeModelUnknownProviderFailsClosed(t *testing.T) {
	profile := testAgentProfile("body", agents.AgentMetadata{Name: "reviewer", Model: "ghost-llm/qwen3:8b"})
	caller := &mockLLMCaller{responses: []*llm.ChatResponse{executorFinishResponse("must never run")}}

	l := modelOverrideLauncher(captureMetaContextFactory(nil), unknownProviderValidator, caller, nil)
	ctx := modelOverrideProfileCtx("reviewer", profile)

	_, err := l.buildSubAgentTask(ctx, tools.DelegationTask{ID: "d1", Summary: "s", Task: "do work", Agent: "reviewer"}, tools.NewDelegationRegistry(), &agent.NoopEvents{})
	if err == nil {
		t.Fatal("expected fail-closed error for an unknown provider in a composite override")
	}
	for _, want := range []string{"reviewer", "ghost-llm/qwen3:8b", "ghost-llm", "local-llm, openai"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must name %q", err.Error(), want)
		}
	}
	if len(caller.calls) != 0 {
		t.Errorf("caller saw %d requests; the subagent must never launch", len(caller.calls))
	}
}

// --- Bare passthrough ---

// TestBuildSubAgentTask_BareModelKeepsLegacyOverride verifies a bare (no
// "/") profile model keeps the legacy model-only override byte-identical:
// req.Model is the raw name, req.Provider stays empty (active provider), and
// the validator is never consulted.
func TestBuildSubAgentTask_BareModelKeepsLegacyOverride(t *testing.T) {
	profile := testAgentProfile("body", agents.AgentMetadata{Name: "reviewer", Model: "qwen3:8b"})
	validator := &recordingModelOverrideValidator{}
	caller := &mockLLMCaller{responses: []*llm.ChatResponse{executorFinishResponse("done")}}

	l := modelOverrideLauncher(captureMetaContextFactory(nil), validator.validate, caller, nil)
	ctx := modelOverrideProfileCtx("reviewer", profile)

	st, err := l.buildSubAgentTask(ctx, tools.DelegationTask{ID: "d1", Summary: "s", Task: "do work", Agent: "reviewer"}, tools.NewDelegationRegistry(), &agent.NoopEvents{})
	if err != nil {
		t.Fatalf("buildSubAgentTask: %v", err)
	}
	if _, err := st.Executor.Run(ctx, st.TaskTools, st.CM); err != nil {
		t.Fatalf("executor run: %v", err)
	}

	if len(caller.calls) != 1 {
		t.Fatalf("caller saw %d requests, want 1", len(caller.calls))
	}
	if got := caller.calls[0].Model; got != "qwen3:8b" {
		t.Errorf("req.Model = %q, want the bare name unchanged", got)
	}
	if got := caller.calls[0].Provider; got != "" {
		t.Errorf("req.Provider = %q, want empty (bare names never pin a provider)", got)
	}
	if len(validator.calls) != 0 {
		t.Errorf("validator must not be consulted for bare names, got %v", validator.calls)
	}
}

// --- CM window sizing ---

// TestBuildSubAgentTask_CompositeModelSizesCMByOverrideWindow verifies the CM
// is built with the OVERRIDE model's metadata window when the registry knows
// the bare model — the session model's window must not shadow it.
func TestBuildSubAgentTask_CompositeModelSizesCMByOverrideWindow(t *testing.T) {
	registry := llm.NewModelRegistry(map[string]llm.ModelMetadata{
		"gpt-4o":   {ContextWindow: 128000, OutputLimit: 16384, TokenizerType: "approximate"},
		"qwen3:8b": {ContextWindow: 32000, OutputLimit: 8000, TokenizerType: "approximate"},
	})
	profile := testAgentProfile("body", agents.AgentMetadata{Name: "reviewer", Model: "local-llm/qwen3:8b"})
	validator := &recordingModelOverrideValidator{}

	var meta llm.ModelMetadata
	l := modelOverrideLauncher(captureMetaContextFactory(&meta), validator.validate, &mockLLMCaller{responses: []*llm.ChatResponse{executorFinishResponse("done")}}, registry)
	ctx := modelOverrideProfileCtx("reviewer", profile)

	if _, err := l.buildSubAgentTask(ctx, tools.DelegationTask{ID: "d1", Summary: "s", Task: "do work", Agent: "reviewer"}, tools.NewDelegationRegistry(), &agent.NoopEvents{}); err != nil {
		t.Fatalf("buildSubAgentTask: %v", err)
	}
	if meta.ContextWindow != 32000 {
		t.Errorf("CM ContextWindow = %d, want the override model's 32000", meta.ContextWindow)
	}
}

// TestResolveModelMeta_EffectiveModelFallbackChain verifies resolveModelMeta
// resolves the effective (override) model first and falls through the
// existing chain on a miss: unknown override → session model, empty effective
// → session model (the pre-profile behavior).
func TestResolveModelMeta_EffectiveModelFallbackChain(t *testing.T) {
	registry := llm.NewModelRegistry(map[string]llm.ModelMetadata{
		"gpt-4o":    {ContextWindow: 128000, OutputLimit: 16384, TokenizerType: "approximate"},
		"known-slm": {ContextWindow: 8000, OutputLimit: 2000, TokenizerType: "approximate"},
	})
	l := &conductorLauncher{deps: conductorDeps{
		modelRegistry: registry,
		model:         "gpt-4o",
	}}
	ctx := context.Background()

	// Known effective model wins.
	if got := l.resolveModelMeta(ctx, "known-slm").ContextWindow; got != 8000 {
		t.Errorf("resolveModelMeta(known-slm) window = %d, want 8000", got)
	}
	// Unknown effective model falls through to the session model.
	if got := l.resolveModelMeta(ctx, "ghost-model").ContextWindow; got != 128000 {
		t.Errorf("resolveModelMeta(ghost-model) window = %d, want the session model's 128000", got)
	}
	// Empty effective model: the pre-profile chain (session model).
	if got := l.resolveModelMeta(ctx, "").ContextWindow; got != 128000 {
		t.Errorf("resolveModelMeta(\"\") window = %d, want the session model's 128000", got)
	}
	// No registry at all: static fallback keeps compaction alive.
	l2 := &conductorLauncher{deps: conductorDeps{model: "gpt-4o"}}
	if got := l2.resolveModelMeta(ctx, "known-slm").ContextWindow; got != 128000 {
		t.Errorf("no-registry window = %d, want the static fallback 128000", got)
	}
}
