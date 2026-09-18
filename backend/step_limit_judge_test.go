package backend

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/session"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// stepLimitFakeProvider is a scripted llm.Provider for the loop judge. It
// captures the last request so tests can assert the judge actually received
// the trajectory context.
type stepLimitFakeProvider struct {
	response string
	err      error
	calls    int
	lastReq  llm.ChatRequest
}

func (p *stepLimitFakeProvider) ChatCompletion(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls++
	p.lastReq = req
	if p.err != nil {
		return nil, p.err
	}
	return &llm.ChatResponse{Message: llm.Message{Content: p.response}}, nil
}

func (p *stepLimitFakeProvider) Name() string { return "step-limit-fake" }

func newStepLimitJudge(p llm.Provider) *sdktools.ToolJudge {
	return sdktools.NewToolJudge(p, "judge-model", 0, nil)
}

func autoSilentMode() coretools.SilentModeState {
	return coretools.SilentModeState{StepLimit: config.SilentStepLimitAuto}
}

// --- gate: non-autonomous boundaries must fall back to the interactive path ---

func TestResolveSilentStepLimitDecision_NotHandledWhenSilentDisabled(t *testing.T) {
	sm := coretools.SilentModeState{StepLimit: config.SilentStepLimitAuto}
	resp, _, handled := resolveSilentStepLimitDecision(context.Background(), coretools.AutonomyModeStandard, sm,
		newStepLimitJudge(&stepLimitFakeProvider{response: "VERDICT: ALLOW_ALWAYS"}), sdktools.StepLimitJudgeRequest{})
	if handled {
		t.Fatalf("silent mode disabled must NOT be handled autonomously (got resp %v)", resp)
	}
}

func TestResolveSilentStepLimitDecision_NotHandledWhenSubPolicyStop(t *testing.T) {
	sm := coretools.SilentModeState{StepLimit: config.SilentStepLimitStop}
	_, _, handled := resolveSilentStepLimitDecision(context.Background(), coretools.AutonomyModeSilent, sm,
		newStepLimitJudge(&stepLimitFakeProvider{response: "VERDICT: ALLOW_ALWAYS"}), sdktools.StepLimitJudgeRequest{})
	if handled {
		t.Fatal("step_limit=stop must NOT be handled autonomously (non-silent behavior preserved)")
	}
}

// --- fixed sub-policies resolve deterministically without the judge ---

func TestResolveSilentStepLimitDecision_FixedModes(t *testing.T) {
	cases := []struct {
		mode string
		want agent.StepLimitResponse
	}{
		{config.SilentStepLimitAllowOnce, agent.StepLimitAllowOnce},
		{config.SilentStepLimitAllowMore, agent.StepLimitAllowMore},
		{config.SilentStepLimitAllowAlways, agent.StepLimitAllowAlways},
		{config.SilentStepLimitDeny, agent.StepLimitDeny},
	}
	for _, tc := range cases {
		// A judge is present but MUST NOT be consulted for a fixed mode.
		provider := &stepLimitFakeProvider{response: "VERDICT: DENY\nREASON: ignored"}
		sm := coretools.SilentModeState{StepLimit: tc.mode}
		resp, reason, handled := resolveSilentStepLimitDecision(context.Background(), coretools.AutonomyModeSilent, sm, newStepLimitJudge(provider), sdktools.StepLimitJudgeRequest{})
		if !handled {
			t.Fatalf("%s: a fixed mode must be handled (no card)", tc.mode)
		}
		if resp != tc.want {
			t.Errorf("%s: resp = %v, want %v", tc.mode, resp, tc.want)
		}
		if reason == "" {
			t.Errorf("%s: expected a reasoning string for the audit trail", tc.mode)
		}
		if provider.calls != 0 {
			t.Errorf("%s: a fixed mode must not consult the judge (calls=%d)", tc.mode, provider.calls)
		}
	}
}

// --- fail-closed: silent+auto with a missing or failing judge must DENY ---

func TestResolveSilentStepLimitDecision_MissingJudgeFailsClosed(t *testing.T) {
	resp, reason, handled := resolveSilentStepLimitDecision(context.Background(), coretools.AutonomyModeSilent, autoSilentMode(), nil, sdktools.StepLimitJudgeRequest{})
	if !handled {
		t.Fatal("silent+auto must be handled (autonomous)")
	}
	if resp != agent.StepLimitDeny {
		t.Errorf("missing judge: resp = %v, want StepLimitDeny (fail-closed)", resp)
	}
	if reason == "" {
		t.Error("expected an explanatory reason for the fail-closed deny")
	}
}

func TestResolveSilentStepLimitDecision_JudgeErrorFailsClosed(t *testing.T) {
	judge := newStepLimitJudge(&stepLimitFakeProvider{err: errors.New("provider down")})
	resp, reason, handled := resolveSilentStepLimitDecision(context.Background(), coretools.AutonomyModeSilent, autoSilentMode(), judge, sdktools.StepLimitJudgeRequest{})
	if !handled || resp != agent.StepLimitDeny {
		t.Errorf("judge error: resp=%v handled=%v, want deny/true", resp, handled)
	}
	if reason == "" {
		t.Error("expected an explanatory reason for the fail-closed deny")
	}
}

func TestResolveSilentStepLimitDecision_UnparseableFailsClosed(t *testing.T) {
	judge := newStepLimitJudge(&stepLimitFakeProvider{response: "no idea"})
	resp, _, handled := resolveSilentStepLimitDecision(context.Background(), coretools.AutonomyModeSilent, autoSilentMode(), judge, sdktools.StepLimitJudgeRequest{})
	if !handled || resp != agent.StepLimitDeny {
		t.Errorf("unparseable verdict: resp=%v handled=%v, want deny/true", resp, handled)
	}
}

// --- verdict mapping ---

func TestResolveSilentStepLimitDecision_VerdictMapping(t *testing.T) {
	cases := []struct {
		content string
		want    agent.StepLimitResponse
	}{
		{"VERDICT: ALLOW_ONCE\nREASON: one step left", agent.StepLimitAllowOnce},
		{"VERDICT: ALLOW_MORE\nREASON: healthy", agent.StepLimitAllowMore},
		{"VERDICT: ALLOW_ALWAYS\nREASON: long but clean", agent.StepLimitAllowAlways},
		{"VERDICT: DENY\nREASON: stuck", agent.StepLimitDeny},
	}
	for _, tc := range cases {
		judge := newStepLimitJudge(&stepLimitFakeProvider{response: tc.content})
		resp, reason, handled := resolveSilentStepLimitDecision(context.Background(), coretools.AutonomyModeSilent, autoSilentMode(), judge, sdktools.StepLimitJudgeRequest{})
		if !handled {
			t.Fatalf("%q: expected handled", tc.content)
		}
		if resp != tc.want {
			t.Errorf("%q: resp = %v, want %v", tc.content, resp, tc.want)
		}
		if reason == "" {
			t.Errorf("%q: expected non-empty reasoning", tc.content)
		}
	}
}

// --- request assembly: budget vs circuit breaker + trajectory ---

func TestBuildStepLimitJudgeRequest_BudgetVsCircuitBreaker(t *testing.T) {
	budget := buildStepLimitJudgeRequest("task", session.ExecutionWindowSnapshot{}, 5, 5, "")
	if budget.AbortCategory != sdktools.LoopBoundaryBudget {
		t.Errorf("empty reason: category = %q, want %q", budget.AbortCategory, sdktools.LoopBoundaryBudget)
	}
	if budget.AbortReason != "" {
		t.Errorf("budget boundary must carry no abort reason, got %q", budget.AbortReason)
	}

	breaker := buildStepLimitJudgeRequest("task", session.ExecutionWindowSnapshot{}, 5, 5, "  repeated identical tool calls  ")
	if breaker.AbortCategory != sdktools.LoopBoundaryCircuitBreaker {
		t.Errorf("non-empty reason: category = %q, want %q", breaker.AbortCategory, sdktools.LoopBoundaryCircuitBreaker)
	}
	if strings.TrimSpace(breaker.AbortReason) == "" {
		t.Error("circuit-breaker boundary must carry the abort reason")
	}
}

func TestBuildStepLimitJudgeRequest_AssemblesTrajectory(t *testing.T) {
	win := session.ExecutionWindowSnapshot{
		Entries: []session.ExecutionWindowEntry{
			{Step: 1, Kind: session.ExecWindowToolCallKind, Tool: "read_file", Detail: `{"path":"a.go"}`},
			{Step: 1, Kind: session.ExecWindowToolResultKind, Detail: "ok"},
			{Step: 2, Kind: session.ExecWindowDiagnosticKind, Detail: "fruitless_abort"},
		},
		Counters:  session.ExecutionWindowCounters{Steps: 2, ToolCalls: 3, ToolErrors: 1, Nudges: 1, Aborts: 1, ParseErrors: 1, InvalidToolCalls: 2},
		PlanTotal: 4, PlanDone: 2, PlanStep: "s2",
	}
	req := buildStepLimitJudgeRequest("build the parser", win, 12, 12, "fruitless result circuit breaker")

	if req.PlanSnapshot != "2 of 4 plan steps complete; current step s2" {
		t.Errorf("plan snapshot = %q", req.PlanSnapshot)
	}
	if req.Metrics.ToolCalls != 3 || req.Metrics.Aborts != 1 || req.Metrics.InvalidToolCalls != 2 {
		t.Errorf("metrics not carried: %+v", req.Metrics)
	}
	if len(req.RecentSteps) != 3 {
		t.Fatalf("recent steps = %d, want 3", len(req.RecentSteps))
	}
	if req.RecentSteps[0].Tool != "read_file" || req.RecentSteps[1].Result != "ok" {
		t.Errorf("tool digests wrong: %+v", req.RecentSteps)
	}
	if !strings.Contains(req.RecentSteps[2].Result, "fruitless_abort") {
		t.Errorf("diagnostic digest must name the event, got %q", req.RecentSteps[2].Result)
	}
}

func TestBuildStepLimitJudgeRequest_NoPlanOmitsSnapshot(t *testing.T) {
	req := buildStepLimitJudgeRequest("t", session.ExecutionWindowSnapshot{}, 1, 1, "")
	if req.PlanSnapshot != "" {
		t.Errorf("plan snapshot must be empty without a plan, got %q", req.PlanSnapshot)
	}
	if req.RecentSteps != nil {
		t.Errorf("recent steps must be nil for an empty window, got %v", req.RecentSteps)
	}
}

// --- end-to-end: the judge receives the trajectory, not just the reason ---

func TestResolveSilentStepLimitDecision_JudgeReceivesTrajectory(t *testing.T) {
	win := session.ExecutionWindowSnapshot{
		Entries: []session.ExecutionWindowEntry{
			{Step: 1, Kind: session.ExecWindowToolCallKind, Tool: "read_file", Detail: `{"path":"a.go"}`},
			{Step: 2, Kind: session.ExecWindowToolCallKind, Tool: "bash", Detail: `{"command":"make build"}`},
			{Step: 2, Kind: session.ExecWindowToolResultKind, Detail: "boom", IsError: true},
		},
		Counters: session.ExecutionWindowCounters{Steps: 2, ToolCalls: 2, ToolErrors: 1, Aborts: 1},
	}
	req := buildStepLimitJudgeRequest("refactor the parser", win, 12, 12, "repeated identical tool calls")
	provider := &stepLimitFakeProvider{response: "VERDICT: ALLOW_ONCE\nREASON: one step from done"}

	resp, _, handled := resolveSilentStepLimitDecision(context.Background(), coretools.AutonomyModeSilent, autoSilentMode(), newStepLimitJudge(provider), req)
	if !handled || resp != agent.StepLimitAllowOnce {
		t.Fatalf("resp=%v handled=%v, want allow_once/true", resp, handled)
	}
	if len(provider.lastReq.Messages) != 2 {
		t.Fatalf("expected system+user messages, got %d", len(provider.lastReq.Messages))
	}
	user := provider.lastReq.Messages[1].Content
	for _, want := range []string{
		"trigger: circuit_breaker",
		"refactor the parser",
		"read_file",
		"make build",
		"tool_errors: 1",
		"hard_aborts: 1",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("judge user prompt missing %q\n---\n%s", want, user)
		}
	}
}

func TestRecentStepDigests_CapsToLimit(t *testing.T) {
	entries := make([]session.ExecutionWindowEntry, 30)
	for i := range entries {
		entries[i] = session.ExecutionWindowEntry{Step: i + 1, Kind: session.ExecWindowStepCompleteKind}
	}
	got := recentStepDigests(entries, 5)
	if len(got) != 5 {
		t.Fatalf("digests = %d, want 5", len(got))
	}
	if got[0].Step != 26 {
		t.Errorf("cap must keep the TRAILING entries, first step = %d, want 26", got[0].Step)
	}
	if recentStepDigests(nil, 5) != nil {
		t.Error("nil entries must yield nil digests")
	}
}

func TestStepLimitResponseFromVerdict_UnknownIsDeny(t *testing.T) {
	if got := stepLimitResponseFromVerdict(sdktools.LoopVerdict(99)); got != agent.StepLimitDeny {
		t.Errorf("unknown verdict maps to %v, want deny", got)
	}
}
