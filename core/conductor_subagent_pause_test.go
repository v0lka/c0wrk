package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
)

// TestIsPaused verifies the cooperative-pause detection tolerates both the
// in-memory sentinel and the string-reconstructed error produced by a
// persistence round-trip (LoadTaskState rebuilds errors via errors.New).
func TestIsPaused(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sentinel", agent.ErrPaused, true},
		{"persisted form", errors.New(agent.ErrPaused.Error()), true},
		{"unrelated", errors.New("boom"), false},
		{"wrapped sentinel", errors.Join(errors.New("context"), agent.ErrPaused), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPaused(tt.err); got != tt.want {
				t.Fatalf("isPaused(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestPausedCheckpoint verifies the blackboard lookup returns a step's partial
// trajectory only when the step carries a paused checkpoint.
func TestPausedCheckpoint(t *testing.T) {
	bb := orchestration.NewMapBlackboard()
	steps := []agent.Step{{Thought: "prior", Action: llm.ToolCall{ID: "c1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "o1"}}
	bb.SetStepResult("paused", "", agent.ErrPaused, steps)
	bb.SetStepResult("failed", "", errors.New("boom"), nil)
	bb.SetStepResult("done", "ok", nil, nil)

	l := &conductorLauncher{bb: bb}

	t.Run("paused checkpoint", func(t *testing.T) {
		sr, ok := l.pausedCheckpoint("paused")
		if !ok {
			t.Fatal("expected a paused checkpoint")
		}
		if len(sr.Steps) != 1 || sr.Steps[0].Thought != "prior" {
			t.Fatalf("unexpected checkpoint steps: %+v", sr.Steps)
		}
	})

	t.Run("failed is not paused", func(t *testing.T) {
		if _, ok := l.pausedCheckpoint("failed"); ok {
			t.Fatal("failed step must not be treated as a paused checkpoint")
		}
	})

	t.Run("completed is not paused", func(t *testing.T) {
		if _, ok := l.pausedCheckpoint("done"); ok {
			t.Fatal("completed step must not be treated as a paused checkpoint")
		}
	})

	t.Run("missing is not paused", func(t *testing.T) {
		if _, ok := l.pausedCheckpoint("missing"); ok {
			t.Fatal("missing step must not be treated as a paused checkpoint")
		}
	})

	t.Run("nil blackboard", func(t *testing.T) {
		nilBB := &conductorLauncher{}
		if _, ok := nilBB.pausedCheckpoint("paused"); ok {
			t.Fatal("nil blackboard must never yield a paused checkpoint")
		}
	})
}

// TestConfigureExecutor_WiresPauseChecker verifies subagent executors receive
// the same cooperative pause signal as the Conductor: a true pause-checker
// stops the executor at its first step boundary with ErrPaused.
func TestConfigureExecutor_WiresPauseChecker(t *testing.T) {
	l := &conductorLauncher{deps: conductorDeps{
		pauseChecker: func(context.Context) bool { return true },
	}}
	executor := agent.NewExecutor(
		&mockLLMCaller{},
		&mockToolExecutor{},
		5,
		agent.WithTokenCounter(llm.NewSimpleTokenCounter()),
	)
	l.configureExecutor(executor)

	_, err := executor.Run(context.Background(), nil, &mockContextManager{})
	if !errors.Is(err, agent.ErrPaused) {
		t.Fatalf("expected ErrPaused from a pausing subagent executor, got %v", err)
	}
}

// TestBuildSubAgentTask_ResumesFromPausedCheckpoint verifies that a subagent
// whose step carries a paused checkpoint is resumed: the partial trajectory is
// seeded into the ContextManager and into the executor (its step counter
// continues from len(checkpoint)+1).
func TestBuildSubAgentTask_ResumesFromPausedCheckpoint(t *testing.T) {
	checkpoint := []agent.Step{{Thought: "prior work", Action: llm.ToolCall{ID: "c1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "o1"}}
	bb := orchestration.NewMapBlackboard()
	bb.SetStepResult("d1", "", agent.ErrPaused, checkpoint)

	var captured *seedableRecordingCM
	cf := func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) ContextManager {
		captured = &seedableRecordingCM{}
		captured.systemPrompt = systemPrompt
		return captured
	}

	l := &conductorLauncher{
		bb: bb,
		deps: conductorDeps{
			contextFactory: cf,
			llm:            &mockLLMCaller{responses: []*llm.ChatResponse{executorFinishResponse("done after resume")}},
			toolExec:       &mockToolExecutor{},
			tokenCounter:   llm.NewSimpleTokenCounter(),
		},
	}
	registry := tools.NewDelegationRegistry()
	ctx := WithComplexity(context.Background(), 1)
	st, err := l.buildSubAgentTask(ctx, tools.DelegationTask{ID: "d1", Summary: "s", Task: "do work"}, registry, &agent.NoopEvents{})
	if err != nil {
		t.Fatalf("buildSubAgentTask: %v", err)
	}

	if got := len(captured.SeededSteps()); got != len(checkpoint) {
		t.Fatalf("ContextManager seeded %d steps, want %d", got, len(checkpoint))
	}

	// Run the executor: the LLM finishes immediately, so the returned steps
	// must be the seeded checkpoint plus the fresh finish step.
	result, err := st.Executor.Run(ctx, st.TaskTools, st.CM)
	if err != nil {
		t.Fatalf("resumed executor run failed: %v", err)
	}
	if result == nil || !result.Finished {
		t.Fatalf("expected resumed executor to finish, got %+v", result)
	}
	if want := len(checkpoint) + 1; len(result.Steps) != want {
		t.Fatalf("resumed executor steps = %d, want %d (checkpoint + finish)", len(result.Steps), want)
	}
	if result.Steps[0].Thought != "prior work" {
		t.Fatalf("first resumed step = %+v, want the checkpoint's first step", result.Steps[0])
	}
}

// TestBuildSubAgentTask_ResumeFailsFastWithoutStepSeedable verifies the
// fail-fast contract: a paused checkpoint against a ContextManager that does
// not implement StepSeedable is an error, not a silent incoherent resume.
func TestBuildSubAgentTask_ResumeFailsFastWithoutStepSeedable(t *testing.T) {
	checkpoint := []agent.Step{{Thought: "prior", Action: llm.ToolCall{ID: "c1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "o1"}}
	bb := orchestration.NewMapBlackboard()
	bb.SetStepResult("d1", "", agent.ErrPaused, checkpoint)

	l := &conductorLauncher{
		bb: bb,
		deps: conductorDeps{
			contextFactory: func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) ContextManager {
				return &mockContextManager{systemPrompt: systemPrompt}
			},
			llm:          &mockLLMCaller{},
			toolExec:     &mockToolExecutor{},
			tokenCounter: llm.NewSimpleTokenCounter(),
		},
	}
	_, err := l.buildSubAgentTask(context.Background(), tools.DelegationTask{ID: "d1", Summary: "s", Task: "do work"}, tools.NewDelegationRegistry(), &agent.NoopEvents{})
	if err == nil {
		t.Fatal("expected an error when resuming with a non-StepSeedable ContextManager")
	}
}

// TestBuildSubAgentTask_FreshStartDoesNotSeed verifies the non-resume path is
// unchanged: without a paused checkpoint, no SeedSteps call happens.
func TestBuildSubAgentTask_FreshStartDoesNotSeed(t *testing.T) {
	var captured *seedableRecordingCM
	cf := func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) ContextManager {
		captured = &seedableRecordingCM{}
		captured.systemPrompt = systemPrompt
		return captured
	}
	l := &conductorLauncher{
		bb: orchestration.NewMapBlackboard(),
		deps: conductorDeps{
			contextFactory: cf,
			llm:            &mockLLMCaller{},
			toolExec:       &mockToolExecutor{},
			tokenCounter:   llm.NewSimpleTokenCounter(),
		},
	}
	if _, err := l.buildSubAgentTask(context.Background(), tools.DelegationTask{ID: "d1", Summary: "s", Task: "do work"}, tools.NewDelegationRegistry(), &agent.NoopEvents{}); err != nil {
		t.Fatalf("buildSubAgentTask: %v", err)
	}
	if got := captured.SeededSteps(); got != nil {
		t.Fatalf("fresh start must not seed steps, got %d", len(got))
	}
}
