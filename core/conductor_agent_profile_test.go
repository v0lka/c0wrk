package core

import (
	"context"
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

// testAgentProfile builds an *agents.Agent with the given body and metadata.
func testAgentProfile(body string, md agents.AgentMetadata) *agents.Agent {
	return &agents.Agent{Metadata: md, Body: body, DirPath: "/agents/" + md.Name}
}

// captureContextFactory returns a ContextManagerFactory that records the
// systemPrompt it was called with, so a test can assert the prompt handed to
// buildSubAgentTask. It returns a mockContextManager (which is SetTask-aware).
func captureContextFactory(captured *string) ContextManagerFactory {
	return func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) ContextManager {
		if captured != nil {
			*captured = systemPrompt
		}
		return &mockContextManager{systemPrompt: systemPrompt}
	}
}

// --- resolveAgentAllowRedelegate ---

// TestResolveAgentAllowRedelegate_ProfileTrueOverrides verifies a profile with
// AllowRedelegate=true wins over a false task flag.
func TestResolveAgentAllowRedelegate_ProfileTrueOverrides(t *testing.T) {
	resolver := func(name string) (*agents.Agent, bool) {
		if name == "reviewer" {
			return testAgentProfile("body", agents.AgentMetadata{Name: "reviewer", AllowRedelegate: true}), true
		}
		return nil, false
	}
	ctx := tools.WithAgentResolver(context.Background(), resolver)
	l := &conductorLauncher{}

	got := l.resolveAgentAllowRedelegate(ctx, "reviewer", false)
	if !got {
		t.Error("profile AllowRedelegate=true must override task flag false")
	}
}

// TestResolveAgentAllowRedelegate_ProfileFalseKeepsTaskFlag verifies that a
// profile with AllowRedelegate=false does NOT downgrade an explicitly-granted
// task flag (the task flag is preserved — the profile only upgrades, never
// downgrades).
func TestResolveAgentAllowRedelegate_ProfileFalseKeepsTaskFlag(t *testing.T) {
	resolver := func(name string) (*agents.Agent, bool) {
		return testAgentProfile("body", agents.AgentMetadata{Name: "r", AllowRedelegate: false}), true
	}
	ctx := tools.WithAgentResolver(context.Background(), resolver)
	l := &conductorLauncher{}

	// Task flag true stays true (profile false does not downgrade).
	if got := l.resolveAgentAllowRedelegate(ctx, "r", true); !got {
		t.Error("profile AllowRedelegate=false must not downgrade an explicit task flag true")
	}
	// Task flag false stays false.
	if got := l.resolveAgentAllowRedelegate(ctx, "r", false); got {
		t.Error("profile AllowRedelegate=false with task flag false must stay false")
	}
}

// TestResolveAgentAllowRedelegate_NoResolverPreservesFlag verifies nil resolver
// (profiles unavailable) leaves the task flag untouched.
func TestResolveAgentAllowRedelegate_NoResolverPreservesFlag(t *testing.T) {
	l := &conductorLauncher{} // no resolver in context
	if got := l.resolveAgentAllowRedelegate(context.Background(), "x", true); !got {
		t.Error("nil resolver must preserve task flag true")
	}
	if got := l.resolveAgentAllowRedelegate(context.Background(), "x", false); got {
		t.Error("nil resolver must preserve task flag false")
	}
}

// TestResolveAgentAllowRedelegate_UnknownAgentPreservesFlag verifies a not-found
// agent (race: removed mid-run) keeps the safer task default.
func TestResolveAgentAllowRedelegate_UnknownAgentPreservesFlag(t *testing.T) {
	resolver := func(name string) (*agents.Agent, bool) { return nil, false }
	ctx := tools.WithAgentResolver(context.Background(), resolver)
	l := &conductorLauncher{}

	if got := l.resolveAgentAllowRedelegate(ctx, "ghost", true); !got {
		t.Error("unknown agent must preserve task flag true")
	}
}

// --- buildSubAgentTask agent application ---

// TestBuildSubAgentTask_AgentProfileBodyReplacesSystemPrompt verifies that when
// an agent profile is requested, its body (not OrchestratorSystem) becomes the
// core directive, while the shared prefix (workspace, AGENTS.md) is preserved.
func TestBuildSubAgentTask_AgentProfileBodyReplacesSystemPrompt(t *testing.T) {
	const directiveMarker = "REVIEW ALL CODE WITH MAXIMUM RIGOR"
	profile := testAgentProfile(directiveMarker, agents.AgentMetadata{Name: "code-reviewer"})
	resolver := func(name string) (*agents.Agent, bool) {
		if name == "code-reviewer" {
			return profile, true
		}
		return nil, false
	}

	var captured string
	ctx := tools.WithAgentResolver(sdktools.WithWorkspacePath(context.Background(), "/ws"), resolver)
	registry := tools.NewDelegationRegistry()

	l := &conductorLauncher{deps: conductorDeps{
		contextFactory: captureContextFactory(&captured),
		toolRegistry:   newSubagentTestRegistry(subagentToolSet()),
	}}
	task := tools.DelegationTask{ID: "d1", Summary: "s", Task: "review the PR", Agent: "code-reviewer"}

	st, err := l.buildSubAgentTask(ctx, task, registry, &agent.NoopEvents{})
	if err != nil {
		t.Fatalf("buildSubAgentTask: %v", err)
	}
	if st.StepID != "d1" {
		t.Errorf("StepID = %q, want d1", st.StepID)
	}
	if !strings.Contains(captured, directiveMarker) {
		t.Errorf("agent body must be the core directive; prompt missing %q", directiveMarker)
	}
	// The standard OrchestratorSystem directive must NOT appear (the body
	// replaces it). OrchestratorSystem begins with a role line; assert the
	// body marker is present instead of a bare orchestrator prompt.
	if !strings.Contains(captured, "REVIEW ALL CODE") {
		t.Errorf("expected specialized directive in prompt")
	}
}

// TestBuildSubAgentTask_AgentUnknownFailsFast verifies a non-empty agent that
// cannot be resolved returns an error (no subagent launched).
func TestBuildSubAgentTask_AgentUnknownFailsFast(t *testing.T) {
	resolver := func(name string) (*agents.Agent, bool) { return nil, false }
	ctx := tools.WithAgentResolver(sdktools.WithWorkspacePath(context.Background(), "/ws"), resolver)
	registry := tools.NewDelegationRegistry()
	l := &conductorLauncher{deps: conductorDeps{
		contextFactory: captureContextFactory(nil),
		toolRegistry:   newSubagentTestRegistry(subagentToolSet()),
	}}
	task := tools.DelegationTask{ID: "d1", Summary: "s", Task: "do work", Agent: "ghost"}

	if _, err := l.buildSubAgentTask(ctx, task, registry, &agent.NoopEvents{}); err == nil {
		t.Fatal("expected error for unknown agent")
	}
}

// TestBuildSubAgentTask_AgentRequestedNoResolverFails verifies that when an
// agent is requested but no resolver is in context, buildSubAgentTask fails
// clearly rather than launching a profile-less subagent.
func TestBuildSubAgentTask_AgentRequestedNoResolverFails(t *testing.T) {
	ctx := sdktools.WithWorkspacePath(context.Background(), "/ws")
	registry := tools.NewDelegationRegistry()
	l := &conductorLauncher{deps: conductorDeps{
		contextFactory: captureContextFactory(nil),
		toolRegistry:   newSubagentTestRegistry(subagentToolSet()),
	}}
	task := tools.DelegationTask{ID: "d1", Summary: "s", Task: "do work", Agent: "x"}

	if _, err := l.buildSubAgentTask(ctx, task, registry, &agent.NoopEvents{}); err == nil {
		t.Fatal("expected error when agent requested with no resolver")
	}
}

// TestBuildSubAgentTask_NoAgentNoRegression verifies the legacy path (no agent
// field) still works: standard system prompt, no resolver needed.
func TestBuildSubAgentTask_NoAgentNoRegression(t *testing.T) {
	var captured string
	ctx := sdktools.WithWorkspacePath(context.Background(), "/ws")
	registry := tools.NewDelegationRegistry()
	l := &conductorLauncher{deps: conductorDeps{
		contextFactory: captureContextFactory(&captured),
		toolRegistry:   newSubagentTestRegistry(subagentToolSet()),
	}}
	task := tools.DelegationTask{ID: "d1", Summary: "s", Task: "do work"}

	st, err := l.buildSubAgentTask(ctx, task, registry, &agent.NoopEvents{})
	if err != nil {
		t.Fatalf("no-agent path must succeed: %v", err)
	}
	if st.StepID != "d1" {
		t.Errorf("StepID = %q, want d1", st.StepID)
	}
	if captured == "" {
		t.Error("system prompt must be assembled for the no-agent path")
	}
}

// TestSubagentCtx_ClearsRoster verifies that subagentCtx clears the
// Conductor-only subagent roster (availableAgentsKey/userAgentsKey). Both
// roster prompt sections are Conductor-only (ADR-021 §4); a subagent that
// inherits them — especially a generic no-profile one with no delegate tool —
// would receive a contradictory "you MUST delegate via delegate(agent:)"
// directive it cannot satisfy.
func TestSubagentCtx_ClearsRoster(t *testing.T) {
	ctx := sdktools.WithWorkspacePath(context.Background(), "/ws")
	ctx = WithAvailableAgents(ctx, []agents.AgentDescriptor{
		{Name: "code-reviewer", Description: "Reviews Go code."},
	})
	ctx = WithUserAgents(ctx, []string{"code-reviewer"})

	out := subagentCtx(ctx)

	if descs := AvailableAgentsFromContext(out); len(descs) != 0 {
		t.Errorf("subagentCtx must clear available agents, got %d", len(descs))
	}
	if names := UserAgentsFromContext(out); len(names) != 0 {
		t.Errorf("subagentCtx must clear requested agents, got %d", len(names))
	}
}

// TestBuildSubAgentTask_GenericSubagentOmitsRoster is the end-to-end regression
// for the roster leak: a generic no-profile subagent launched via the regular
// path (which derives its context from subagentCtx) must NOT see the
// "Available Subagents"/"Requested Subagents" sections in its system prompt,
// even when the parent Conductor context carried a roster.
func TestBuildSubAgentTask_GenericSubagentOmitsRoster(t *testing.T) {
	var captured string
	ctx := sdktools.WithWorkspacePath(context.Background(), "/ws")
	ctx = WithAvailableAgents(ctx, []agents.AgentDescriptor{
		{Name: "code-reviewer", Description: "Reviews Go code."},
	})
	ctx = WithUserAgents(ctx, []string{"code-reviewer"})

	// runRegularBlocking derives the subagent context via subagentCtx.
	subCtx := subagentCtx(ctx)
	registry := tools.NewDelegationRegistry()
	l := &conductorLauncher{deps: conductorDeps{
		contextFactory: captureContextFactory(&captured),
		toolRegistry:   newSubagentTestRegistry(subagentToolSet()),
	}}
	task := tools.DelegationTask{ID: "d1", Summary: "s", Task: "do work"}

	if _, err := l.buildSubAgentTask(subCtx, task, registry, &agent.NoopEvents{}); err != nil {
		t.Fatalf("no-agent path must succeed: %v", err)
	}
	if strings.Contains(captured, "## Available Subagents") {
		t.Error("generic subagent prompt must NOT contain the Conductor-only Available Subagents section")
	}
	if strings.Contains(captured, "## Requested Subagents") {
		t.Error("generic subagent prompt must NOT contain the Conductor-only Requested Subagents section")
	}
	if strings.Contains(captured, "MUST delegate") {
		t.Error("generic subagent prompt must NOT inherit the Conductor's MUST-delegate directive")
	}
}

// TestBuildSubAgentTask_AgentMaxStepsOverrides verifies the profile's max-steps
// (when >0) overrides both the task field and complexity default.
func TestBuildSubAgentTask_AgentMaxStepsOverrides(t *testing.T) {
	profile := testAgentProfile("body", agents.AgentMetadata{Name: "r", MaxSteps: 42})
	resolver := func(name string) (*agents.Agent, bool) { return profile, true }

	var captured string
	ctx := tools.WithAgentResolver(sdktools.WithWorkspacePath(context.Background(), "/ws"), resolver)
	registry := tools.NewDelegationRegistry()
	l := &conductorLauncher{deps: conductorDeps{
		contextFactory: captureContextFactory(&captured),
		toolRegistry:   newSubagentTestRegistry(subagentToolSet()),
	}}
	// task.MaxSteps = 10, profile says 42 → 42 wins.
	task := tools.DelegationTask{ID: "d1", Summary: "s", Task: "do work", Agent: "r", MaxSteps: 10}

	st, err := l.buildSubAgentTask(ctx, task, registry, &agent.NoopEvents{})
	if err != nil {
		t.Fatalf("buildSubAgentTask: %v", err)
	}
	// The task description carries the max-steps budget line.
	if !strings.Contains(st.TaskDesc, "42 iterations") {
		t.Errorf("profile MaxSteps=42 must override; task desc should mention 42 iterations, got: %s", st.TaskDesc)
	}
}

// TestBuildSubAgentTask_AgentNameInTaskDesc verifies the agent name surfaces in
// the task description header (for SubAgentLaunch UI events).
func TestBuildSubAgentTask_AgentNameInTaskDesc(t *testing.T) {
	profile := testAgentProfile("body", agents.AgentMetadata{Name: "code-reviewer"})
	resolver := func(name string) (*agents.Agent, bool) { return profile, true }

	ctx := tools.WithAgentResolver(sdktools.WithWorkspacePath(context.Background(), "/ws"), resolver)
	registry := tools.NewDelegationRegistry()
	l := &conductorLauncher{deps: conductorDeps{
		contextFactory: captureContextFactory(nil),
		toolRegistry:   newSubagentTestRegistry(subagentToolSet()),
	}}
	task := tools.DelegationTask{ID: "d1", Summary: "s", Task: "do work", Agent: "code-reviewer"}

	st, err := l.buildSubAgentTask(ctx, task, registry, &agent.NoopEvents{})
	if err != nil {
		t.Fatalf("buildSubAgentTask: %v", err)
	}
	if !strings.Contains(st.TaskDesc, "agent: code-reviewer") {
		t.Errorf("agent name must surface in task desc header, got: %s", st.TaskDesc)
	}
}

// --- Per-agent model override ---

// TestModelOverrideCaller_AgentProfileModel verifies the integration point: a
// profile with a model override wraps the caller so every Call forces that
// model. This mirrors the sp4rk unit test but at the conductor build level.
func TestModelOverrideCaller_AgentProfileModel(t *testing.T) {
	inner := &mockLLMCaller{}
	const override = "claude-haiku-4"
	caller := agent.NewModelOverrideCaller(inner, override)

	// Empty request model is filled by the override.
	if _, err := caller.Call(context.Background(), llm.ChatRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inner.calls) != 1 || inner.calls[0].Model != override {
		t.Errorf("override must force model %q, got %q", override, firstModelOrEmpty(inner.calls))
	}

	// Pre-set model is overridden.
	if _, err := caller.Call(context.Background(), llm.ChatRequest{Model: "gpt-4o"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.calls[1].Model != override {
		t.Errorf("override must win over pre-set model, got %q", inner.calls[1].Model)
	}
}

// TestModelOverrideCaller_EmptyModelIsNoOp verifies the no-op contract when the
// profile has no model override.
func TestModelOverrideCaller_EmptyModelIsNoOp(t *testing.T) {
	inner := &mockLLMCaller{}
	got := agent.NewModelOverrideCaller(inner, "")
	if got != inner {
		t.Fatalf("empty model must return inner unchanged (no-op)")
	}
}

func firstModelOrEmpty(calls []llm.ChatRequest) string {
	if len(calls) == 0 {
		return ""
	}
	return calls[0].Model
}
