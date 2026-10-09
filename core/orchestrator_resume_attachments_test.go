package core

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/goal"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/skills"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// The attachment-augmentation family (#79) plus the goal-branch task-message
// fix (#94): the "## Attached files" section produced by augmentWithAttachments
// is the only place the model learns an attachment's attachment_id (the
// read_attachment precondition), so EVERY conductor-entry path must carry it —
// fresh sends, plain Resume, and goal resumes alike. And the goal branch must
// derive from the RESOLVED task message (resolveTaskMessage), not the raw
// (possibly emptied-by-skill-stripping) message.

// testAttachments returns a single non-image attachment fixture.
func testAttachments() []orchestration.Attachment {
	return []orchestration.Attachment{{
		ID:           "att-1",
		OriginalName: "data.csv",
		Format:       "text/csv",
		SizeBytes:    128,
	}}
}

// TestResume_PlainConductorCarriesAttachmentAugmentation: a resumed task's
// conductor message must carry the "## Attached files" section even when there
// are no image blocks to re-inject.
func TestResume_PlainConductorCarriesAttachmentAugmentation(t *testing.T) {
	var mu sync.Mutex
	var conductorUserMessage string
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			mu.Lock()
			for _, m := range req.Messages {
				if m.Role == "user" {
					conductorUserMessage = m.Content
				}
			}
			mu.Unlock()
			return executorFinishResponse("Resumed and finished"), nil
		},
	}
	orchestrator := newHistoryTestOrchestrator(mockLLM)

	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("analyze the attached export")
	bb.SetAttachments(testAttachments())

	if _, err := orchestrator.Resume(context.Background(), bb, nil, "", nil, nil, ""); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}

	if !strings.Contains(conductorUserMessage, "## Attached files") {
		t.Errorf("resumed conductor message missing the attachment augmentation (read_attachment would be unreachable for att-1):\n%s", conductorUserMessage)
	}
	if !strings.Contains(conductorUserMessage, "attachment_id: att-1") {
		t.Errorf("resumed conductor message missing the attachment_id listing:\n%s", conductorUserMessage)
	}
}

// msgCapturingGoalTurnRunner records every turn's conductor message and ends
// the loop on the first turn with a "met" verdict.
type msgCapturingGoalTurnRunner struct {
	mu      sync.Mutex
	turnMsg string
}

func (r *msgCapturingGoalTurnRunner) run(
	ctx context.Context,
	turn int,
	msg string,
	_ orchestration.Blackboard,
	_ []sdktools.ToolDescriptor,
	_ string,
	_ []llm.Message,
	_ conductorDeps,
) (int, *orchestration.ExecutionResult, error) {
	r.mu.Lock()
	if r.turnMsg == "" {
		r.turnMsg = msg
	}
	r.mu.Unlock()
	if sink := coretools.GoalStatusSinkFrom(ctx); sink != nil {
		sink.Declare(*metVerdict("met on first turn"))
	}
	return 1, &orchestration.ExecutionResult{}, nil
}

// TestResumeGoalLoop_CarriesAttachmentAugmentation: a resumed goal task's turn
// messages must carry the attachment augmentation even with no image blocks —
// mirroring the fresh runGoalLoop, which passes the augmented message into
// runGoalTurns.
func TestResumeGoalLoop_CarriesAttachmentAugmentation(t *testing.T) {
	o := newGoalTestOrchestrator()
	recorder := &msgCapturingGoalTurnRunner{}
	o.goalTurnRunner = recorder.run

	pausedGS := &goal.GoalState{
		Condition:    "ship the feature",
		VerifyClause: "go test ./...",
		Budget:       goal.GoalBudget{MaxTurns: 10},
		TurnCount:    2,
		Status:       goal.StatusActive,
		CreatedAt:    time.Now(),
	}
	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("analyze the attached export")
	bb.SetAttachments(testAttachments())

	if _, err := o.resumeGoalLoop(
		context.Background(), "resume the goal", bb, nil, "", &router.RoutingDecision{Domain: "general", Complexity: 3},
		pausedGS, nil, "", "",
	); err != nil {
		t.Fatalf("resumeGoalLoop failed: %v", err)
	}

	if !strings.Contains(recorder.turnMsg, "## Attached files") {
		t.Errorf("resumed goal turn message missing the attachment augmentation:\n%s", recorder.turnMsg)
	}
	if !strings.Contains(recorder.turnMsg, "attachment_id: att-1") {
		t.Errorf("resumed goal turn message missing the attachment_id listing:\n%s", recorder.turnMsg)
	}
	// The resume-time additions stay part of the turn message.
	if !strings.Contains(recorder.turnMsg, "resume the goal") {
		t.Errorf("resumed goal turn message lost the base message:\n%s", recorder.turnMsg)
	}
}

// TestHandleMessage_GoalBranchUsesResolvedTaskMessage: a skill-only goal send
// ("/goal /skill" — the preprocessor strips the ref, leaving the raw message
// EMPTY) must derive the goal from the skill-resolved taskMessage, not from
// the empty raw message.
func TestHandleMessage_GoalBranchUsesResolvedTaskMessage(t *testing.T) {
	wsDir := t.TempDir()
	const skillName = "goal-taskmsg-skill"
	skillDir := t.TempDir()
	skillPath := skillDir + "/" + skillName
	if err := os.MkdirAll(skillPath, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	skillMD := "---\nname: " + skillName + "\ndescription: Skill exercising the goal task message\n---\nBody.\n"
	if err := os.WriteFile(skillPath+"/SKILL.md", []byte(skillMD), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	sm := skills.NewSkillManager([]string{skillDir}, nil)
	if err := sm.Scan(); err != nil {
		t.Fatalf("skill scan: %v", err)
	}

	// call 1 = router classification; call 2 = derivation conductor turn 1
	// (propose_goal) — its user message is the derivation task under test;
	// call >= 3 = finish.
	var mu sync.Mutex
	var derivationTaskMessage string
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			callIdx++
			switch callIdx {
			case 1:
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 4, "needs_clarification": false, "matched_skills": ["` + skillName + `"]}`,
					},
					StopReason: "end_turn",
				}, nil
			case 2:
				for _, m := range req.Messages {
					if m.Role == "user" {
						derivationTaskMessage = m.Content
					}
				}
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						ToolCalls: []llm.ToolCall{{
							ID:    "pg1",
							Name:  "propose_goal",
							Input: json.RawMessage(`{"condition":"ship the fix","verify":"go test ./core/... passes"}`),
						}},
					},
					StopReason: "tool_use",
				}, nil
			default:
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						ToolCalls: []llm.ToolCall{{
							ID:    "fn1",
							Name:  "finish",
							Input: json.RawMessage(`{"answer":"goal derived"}`),
						}},
					},
					StopReason: "tool_use",
				}, nil
			}
		},
	}

	registry := createTestRegistry()
	registry.Register(coretools.NewProposeGoalTool())

	orchestrator := NewOrchestrator(OrchestratorConfig{GoalLoop: GoalLoopSettings{Verification: "off"}}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: testContextFactory,
		Emitter:        &spyEmitter{},
		CircuitBreaker: defaultCircuitBreakerConfig,
		SkillManager:   sm,
	})
	orchestrator.SetGoalProposer(&mockProposer{
		response: coretools.GoalProposalResponse{
			Decision:  "approve",
			Condition: "ship the fix",
			Verify:    "go test ./core/... passes",
		},
	})
	orchestrator.goalTurnRunner = (&msgCapturingGoalTurnRunner{}).run

	// The raw message is EMPTY: "/goal /goal-taskmsg-skill" had both the goal
	// prefix and the skill ref stripped before HandleMessage. resolveTaskMessage
	// rebuilds a non-empty task — the goal branch must forward THAT.
	result, err := orchestrator.HandleMessage(sdktools.WithWorkspacePath(context.Background(), wsDir), "", "session-goal-taskmsg", HandleOptions{
		Goal:       true,
		UserSkills: []string{skillName},
	})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}
	_ = result

	if derivationTaskMessage == "" {
		t.Fatal("goal derivation ran with an EMPTY task message — the raw (skill-stripped) message reached runGoalLoop instead of the resolved taskMessage")
	}
	if !strings.Contains(derivationTaskMessage, "/"+skillName) {
		t.Errorf("goal derivation task missing the resolved skill context %q:\n%s", "/"+skillName, derivationTaskMessage)
	}
}
