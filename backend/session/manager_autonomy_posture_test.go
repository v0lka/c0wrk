package session

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/prompts"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/memory"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// posturePinningFactory builds a real orchestrator whose CoreToolRegistry is
// a clone of the shared registry — the production wiring Build performs via
// registerSessionRegistry — and captures the clone so a test can assert its
// pinned posture. A router is wired so the fresh-send path runs end-to-end;
// the resume path never routes, so the same factory serves both tests.
func posturePinningFactory(caller agent.LLMCaller, shared *coretools.ToolRegistry, out **coretools.ToolRegistry) OrchestratorFactory {
	return func(emitter core.Emitter, _ *slog.Logger, _ string, _ core.BlackboardFactory, _ io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		registry := sdktools.NewToolRegistry()
		coreReg := shared.Clone()
		if out != nil {
			*out = coreReg
		}
		cf := func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) core.ContextManager {
			cw := memory.NewContextWindow(memory.ContextWindowConfig{
				SystemPrompt: systemPrompt,
				ModelMeta:    llm.ModelMetadata{ContextWindow: 128000, OutputLimit: 4096},
			})
			return core.NewCoreContextManager(cw)
		}
		rtr := router.New(caller, router.Config{
			SystemPrompt:  prompts.RouterSystem,
			HistoryWindow: 5,
		})
		return core.NewOrchestrator(core.OrchestratorConfig{}, core.OrchestratorDeps{
			LLM:              caller,
			Router:           rtr,
			ToolExec:         registry,
			ToolRegistry:     registry,
			CoreToolRegistry: coreReg,
			TokenCounter:     llm.NewSimpleTokenCounter(),
			ContextFactory:   cf,
			Emitter:          emitter,
			CircuitBreaker:   agent.CircuitBreakerConfig{RepeatNudgeThreshold: 3, RepeatAbortThreshold: 4},
		}), nil
	}
}

// TestSendMessage_RepinsAutonomyPostureAtTaskLaunch covers the reported bug's
// fix from the session side: a session created under one autonomy posture must
// run each task under the posture current at that task's launch — a Settings
// save made AFTER the session was created (the reported scenario: silent
// enabled while a session started interactive is live) is picked up by the
// session's NEXT task, never mid-run.
func TestSendMessage_RepinsAutonomyPostureAtTaskLaunch(t *testing.T) {
	shared := coretools.NewToolRegistry()
	shared.ApplySecurityState(nil, false, coretools.AutonomyModeStandard, coretools.SilentModeState{})

	caller := &scriptedLLM{scripted: []*llm.ChatResponse{
		routingJSONResponse("general", 3),
		finishResponse("done"),
	}}
	var reg *coretools.ToolRegistry
	eventChan := make(chan Event, 100)
	mgr := NewManager(posturePinningFactory(caller, shared, &reg), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	t.Cleanup(mgr.Shutdown) // stop the manager before its temp dirs are removed

	info, err := mgr.CreateSession(testProjectID, runtimeTempDir(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if reg == nil {
		t.Fatal("factory did not capture the session registry clone")
	}
	// The clone inherited the standard posture at creation.
	if got := reg.AutonomyMode(); got != coretools.AutonomyModeStandard {
		t.Fatalf("post-creation clone autonomy mode = %q, want %q", got, coretools.AutonomyModeStandard)
	}

	// A Settings save AFTER session creation flips the shared registry to
	// silent (the exact reported scenario).
	on := coretools.SilentModeState{ToolConfirm: "judge", StepLimit: "auto", AskUser: "disable", ReviewPrompt: "suppress"}
	shared.ApplySecurityState(nil, false, coretools.AutonomyModeSilent, on)

	if err := mgr.SendMessage(context.Background(), info.ID, "do the thing", nil, nil, "", "", false, "", false, false); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if _, ok := waitForEvent(eventChan, "task_complete", 5*time.Second); !ok {
		t.Fatal("timeout waiting for task_complete event")
	}

	if got := reg.AutonomyMode(); got != coretools.AutonomyModeSilent {
		t.Errorf("task-launch clone autonomy mode = %q, want %q (re-pinned from the shared registry)", got, coretools.AutonomyModeSilent)
	}
	if got := reg.SilentMode(); got != on {
		t.Errorf("task-launch clone silent mode = %+v, want %+v", got, on)
	}
}

// TestResumeTask_RepinsAutonomyPostureAtTaskLaunch covers the pause→edit→
// resume flow: a paused task resumed after a Settings change must run under
// the posture the user currently sees in Settings — exactly like launching a
// new task with those Settings.
func TestResumeTask_RepinsAutonomyPostureAtTaskLaunch(t *testing.T) {
	shared := coretools.NewToolRegistry()
	shared.ApplySecurityState(nil, false, coretools.AutonomyModeStandard, coretools.SilentModeState{})

	caller := &scriptedLLM{scripted: []*llm.ChatResponse{
		finishResponse("resumed-and-finished"),
	}}
	var reg *coretools.ToolRegistry
	eventChan := make(chan Event, 100)
	mgr := NewManager(posturePinningFactory(caller, shared, &reg), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	t.Cleanup(mgr.Shutdown) // stop the manager before its temp dirs are removed

	// A genuinely paused task carries an execution trajectory. Without one the
	// resume path would classify it as a never-started (pre-routing) task and
	// re-run routing, which is not the behavior under test here.
	trajJSON, _ := json.Marshal([]agent.Step{
		{Thought: "prior reasoning", Action: llm.ToolCall{ID: "pc1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "PRIOR"},
	})
	store := &resumeTaskStore{
		task: &TaskRecord{
			ID: "task-posture-paused", SessionID: "ignored", OriginalRequest: "long running task",
			Status: "paused",
		},
		trajectory: trajJSON,
	}
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, runtimeTempDir(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if reg == nil {
		t.Fatal("factory did not capture the session registry clone")
	}
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	// The paused task started under standard; the user changes Settings while
	// the task is paused.
	on := coretools.SilentModeState{ToolConfirm: "deny", StepLimit: "stop", AskUser: "enable", ReviewPrompt: "allow"}
	shared.ApplySecurityState(nil, false, coretools.AutonomyModeSilent, on)

	if err := mgr.ResumeTask(context.Background(), info.ID, "", "", ""); err != nil {
		t.Fatalf("ResumeTask failed: %v", err)
	}
	if _, ok := waitForEvent(eventChan, "task_complete", 5*time.Second); !ok {
		t.Fatal("timeout waiting for task_complete event")
	}

	if got := reg.AutonomyMode(); got != coretools.AutonomyModeSilent {
		t.Errorf("resumed clone autonomy mode = %q, want %q (re-pinned at the resume boundary)", got, coretools.AutonomyModeSilent)
	}
	if got := reg.SilentMode(); got != on {
		t.Errorf("resumed clone silent mode = %+v, want %+v", got, on)
	}
}
