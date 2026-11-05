package backend

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/prompts"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/memory"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/skills"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// The chat's Goal badge renders from the persisted user row's metadata
// `goal: true`. Badge parity: a message that arms goal mode through a leading
// "/goal"/"/goal:N" prefix (no toggle flag) must carry the same stamp a
// toggled send gets — specs/domains/goal-mode.md. The FrontendAPI
// PendingMessageMetadata call receives the ARMED signal, not the raw toggle
// flag; these tests pin that wiring end-to-end through a real goal loop.

// goalCapableTestProposer auto-approves every proposal (the desktop approval
// flow's stand-in; the orchestrator factory does not wire one).
type goalCapableTestProposer struct{}

func (goalCapableTestProposer) Propose(_ context.Context, p coretools.GoalProposal) (coretools.GoalProposalResponse, error) {
	return coretools.GoalProposalResponse{Decision: "approve", Condition: p.Condition, Verify: p.Verify}, nil
}

// goalBadgeTestFactory builds a real routed orchestrator with the goal-loop
// tools REGISTERED (a scripted derivation must execute propose_goal; the
// empty default registry fails the call and the loop exits before
// activation) and the auto-approving proposer injected.
func goalBadgeTestFactory(caller agent.LLMCaller) session.OrchestratorFactory {
	return func(emitter core.Emitter, _ *slog.Logger, _ string, bbFactory core.BlackboardFactory, _ io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		registry := sdktools.NewToolRegistry()
		registry.Register(coretools.NewProposeGoalTool())
		registry.Register(coretools.NewDeclareGoalStatusTool())
		registry.Register(coretools.NewDeclareVerificationTool())
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
		orch := core.NewOrchestrator(core.OrchestratorConfig{}, core.OrchestratorDeps{
			LLM:            caller,
			Router:         rtr,
			BBFactory:      bbFactory,
			ToolExec:       registry,
			ToolRegistry:   registry,
			TokenCounter:   llm.NewSimpleTokenCounter(),
			ContextFactory: cf,
			Emitter:        emitter,
			CircuitBreaker: agent.CircuitBreakerConfig{RepeatNudgeThreshold: 3, RepeatAbortThreshold: 4},
		})
		orch.SetGoalProposer(goalCapableTestProposer{})
		return orch, nil
	}
}

// proposeGoalTestResponse builds the derivation-turn response that calls
// propose_goal with a minimal {condition, verify} pair.
func proposeGoalTestResponse(condition, verify string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:    "pg1",
				Name:  "propose_goal",
				Input: json.RawMessage(`{"condition":"` + condition + `","verify":"` + verify + `"}`),
			}},
		},
		StopReason: "tool_use",
	}
}

// declareGoalMetTestResponse builds a goal-turn response declaring the goal
// met.
func declareGoalMetTestResponse() *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:    "dgs1",
				Name:  "declare_goal_status",
				Input: json.RawMessage(`{"status":"met","reason":"done","evidence":[{"type":"qualitative","ref":"probe","summary":"verified"}]}`),
			}},
		},
		StopReason: "tool_use",
	}
}

// declareVerificationConfirmedTestResponse builds the verifier pass's
// response confirming the "met" verdict.
func declareVerificationConfirmedTestResponse() *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:    "dv1",
				Name:  "declare_verification",
				Input: json.RawMessage(`{"confirmed":true,"reason":"re-checked","evidence":[{"type":"qualitative","ref":"probe","summary":"verified"}]}`),
			}},
		},
		StopReason: "tool_use",
	}
}

// armGoalLoopScript arms the derivation → activation → met → verified script
// (the same shape the session-level budget tests use).
func armGoalLoopScript(h *nudgeStampHarness) {
	h.caller.setScript(
		routingTestJSONResponse("general", 1),
		proposeGoalTestResponse("review all code", "no issues found"),
		finishTestResponse("derivation complete"),
		declareGoalMetTestResponse(),
		declareVerificationConfirmedTestResponse(),
	)
}

// userRowGoalStamp loads the persisted rows and reports the user row's
// metadata `goal` stamp (false when the row or the key is absent).
func userRowGoalStamp(t *testing.T, store *session.SQLiteSessionStore, sessionID, content string) bool {
	t.Helper()
	msgs, err := store.LoadMessages(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	for _, m := range msgs {
		if m.Role != "user" || m.Content != content {
			continue
		}
		var md map[string]any
		if len(m.Metadata) > 0 {
			if err := json.Unmarshal(m.Metadata, &md); err != nil {
				t.Fatalf("user row metadata: %v", err)
			}
		}
		v, _ := md["goal"].(bool)
		return v
	}
	t.Fatalf("no persisted user row with content %q", content)
	return false
}

// TestSendMessage_GoalPrefixPersistsGoalMetadata pins badge parity for the
// prefix entry point: a "/goal:N"-armed send (toggle flag false) persists the
// user row with metadata goal:true and the RAW text, exactly like a toggled
// send, while a plain send carries no goal stamp.
func TestSendMessage_GoalPrefixPersistsGoalMetadata(t *testing.T) {
	t.Run("budgeted prefix send stamps goal metadata", func(t *testing.T) {
		h := newNudgeStampHarnessWithFactory(t, false, func(_ session.OrchestratorFactory, caller agent.LLMCaller) session.OrchestratorFactory {
			return goalBadgeTestFactory(caller)
		})
		sessID := h.newSession(t, "goal-prefix")
		armGoalLoopScript(h)

		if err := h.api.SendMessage(sessID, "/goal:5 ship it", nil, nil, nil, "", "", false, "", false, false, "", nil); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		waitForEventType(t, h.events, "task_complete", 10*time.Second)

		if !userRowGoalStamp(t, h.store, sessID, "/goal:5 ship it") {
			t.Error("prefix-armed send must persist metadata goal:true (Goal badge parity)")
		}
	})

	t.Run("toggled send stamps goal metadata identically", func(t *testing.T) {
		h := newNudgeStampHarnessWithFactory(t, false, func(_ session.OrchestratorFactory, caller agent.LLMCaller) session.OrchestratorFactory {
			return goalBadgeTestFactory(caller)
		})
		sessID := h.newSession(t, "goal-toggle")
		armGoalLoopScript(h)

		if err := h.api.SendMessage(sessID, "ship it", nil, nil, nil, "", "", true, "", false, false, "", nil); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		waitForEventType(t, h.events, "task_complete", 10*time.Second)

		if !userRowGoalStamp(t, h.store, sessID, "ship it") {
			t.Error("toggled goal send must persist metadata goal:true")
		}
	})

	t.Run("plain send carries no goal stamp", func(t *testing.T) {
		h := newNudgeStampHarness(t, false)
		sessID := h.newSession(t, "plain-send")

		if err := h.api.SendMessage(sessID, "just a task", nil, nil, nil, "", "", false, "", false, false, "", nil); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		waitForEventType(t, h.events, "task_complete", 10*time.Second)

		if userRowGoalStamp(t, h.store, sessID, "just a task") {
			t.Error("plain send must not persist a goal stamp")
		}
	})
}

// TestSendMessage_GhostGoalThreading pins the backend defense-in-depth for
// the bare "/goal" command (ADR-110): stripMentionRefs strips by the
// CALLER-PROVIDED mention lists, so threading ["goal"] would eat the leading
// command prefix before DetectAndStripGoalMode runs — silently downgrading
// the send to a plain task. The frontend partition never threads "goal"
// when it names no catalog entry; a direct API caller (or a stale frontend)
// that threads it anyway must not disarm the command. When "goal" names a
// REAL catalog entry the threading stands — the documented shadowing
// configuration (ADR-110 Decision 5).
func TestSendMessage_GhostGoalThreading(t *testing.T) {
	t.Run("threaded [goal] naming no catalog entry still arms the goal loop", func(t *testing.T) {
		h := newNudgeStampHarnessWithFactory(t, false, func(_ session.OrchestratorFactory, caller agent.LLMCaller) session.OrchestratorFactory {
			return goalBadgeTestFactory(caller)
		})
		sessID := h.newSession(t, "ghost-goal-defense")
		armGoalLoopScript(h)

		if err := h.api.SendMessage(sessID, "/goal ship it", []string{"goal"}, nil, nil, "", "", false, "", false, false, "", nil); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		// The goal LOOP ran (not just the stamp): a goal_status event only
		// fires from the loop's state transitions — drained first, before the
		// completing event consumes the rest.
		waitForEventType(t, h.events, "goal_status", 10*time.Second)
		waitForEventType(t, h.events, "task_complete", 10*time.Second)

		if !userRowGoalStamp(t, h.store, sessID, "/goal ship it") {
			t.Error("a threaded [goal] list naming no catalog entry must not disarm the /goal command (goal stamp missing)")
		}
	})

	t.Run("a real skill named goal keeps the threading (shadowing preserved)", func(t *testing.T) {
		h := newNudgeStampHarness(t, false)
		h.api.builderOverride = &mockBuilder{getSkillDescriptorsRes: []skills.SkillDescriptor{{Name: "goal", Description: "shadowing skill"}}}
		sessID := h.newSession(t, "goal-shadowing")

		if err := h.api.SendMessage(sessID, "/goal ship it", []string{"goal"}, nil, nil, "", "", false, "", false, false, "", nil); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		waitForEventType(t, h.events, "task_complete", 10*time.Second)

		if userRowGoalStamp(t, h.store, sessID, "/goal ship it") {
			t.Error("a real skill named goal owns the plain spelling (ADR-110 Decision 5): the send must run as a plain mention task with no goal stamp")
		}
	})

	t.Run("threaded [goal] with no command in the text threads unchanged", func(t *testing.T) {
		// The defense never touches a threading that is not eating a command:
		// a mid-text "goal" mention with no leading prefix stays threaded.
		h := newNudgeStampHarness(t, false)
		sessID := h.newSession(t, "goal-mention-only")

		if err := h.api.SendMessage(sessID, "consult /goal docs", []string{"goal"}, nil, nil, "", "", false, "", false, false, "", nil); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		waitForEventType(t, h.events, "task_complete", 10*time.Second)

		if userRowGoalStamp(t, h.store, sessID, "consult /goal docs") {
			t.Error("a goal threading with no command prefix must not arm goal mode")
		}
	})
}
