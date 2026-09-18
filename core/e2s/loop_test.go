package e2s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// scriptedCaller returns canned responses in order and records every request.
type scriptedCaller struct {
	mu        sync.Mutex
	responses []*llm.ChatResponse
	errs      []error
	requests  []llm.ChatRequest
}

func (c *scriptedCaller) Call(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, req)
	idx := len(c.requests) - 1
	if idx < len(c.errs) && c.errs[idx] != nil {
		return nil, c.errs[idx]
	}
	if idx >= len(c.responses) {
		return nil, fmt.Errorf("scriptedCaller: unexpected call #%d (no response scripted)", idx+1)
	}
	return c.responses[idx], nil
}

func (c *scriptedCaller) request(i int) llm.ChatRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests[i]
}

func (c *scriptedCaller) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

// stepResponse builds a ChatResponse carrying one e2s_step tool call.
func stepResponse(patch, tool, args string) *llm.ChatResponse {
	input := fmt.Sprintf(`{"state_patch":%s,"action":{"tool":%q,"args":%s}}`, patch, tool, args)
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:      "assistant",
			ToolCalls: []llm.ToolCall{{ID: "call_1", Name: StepToolName, Input: json.RawMessage(input)}},
		},
		Reasoning: "thinking about it",
	}
}

// rawToolResponse builds a ChatResponse whose assistant calls an arbitrary tool.
func rawToolResponse(name, input string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:      "assistant",
			ToolCalls: []llm.ToolCall{{ID: "call_1", Name: name, Input: json.RawMessage(input)}},
		},
	}
}

// mockRegistry records Execute dispatches and returns canned results.
type mockRegistry struct {
	mu          sync.Mutex
	descriptors []sdktools.ToolDescriptor
	dispatches  []dispatchRecord
	untrusted   map[string]bool
	// results optionally overrides the canned Execute content per tool name.
	results map[string]string
}

type dispatchRecord struct {
	name string
	args json.RawMessage
}

func (r *mockRegistry) List() []sdktools.ToolDescriptor { return r.descriptors }

func (r *mockRegistry) Execute(_ context.Context, name string, input json.RawMessage) (sdktools.ToolResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dispatches = append(r.dispatches, dispatchRecord{name: name, args: input})
	if name == "boom" {
		return sdktools.ToolResult{}, errors.New("registry exploded")
	}
	if name == "bad_tool" {
		return sdktools.ToolResult{Content: "tool failed", IsError: true}, nil
	}
	if content, ok := r.results[name]; ok {
		return sdktools.ToolResult{Content: content, IsError: false}, nil
	}
	return sdktools.ToolResult{Content: "ok result for " + name, IsError: false}, nil
}

func (r *mockRegistry) IsToolUntrusted(name string) bool { return r.untrusted[name] }

func (r *mockRegistry) ToolSource(string) string { return "core" }

func (r *mockRegistry) dispatched() []dispatchRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]dispatchRecord, len(r.dispatches))
	copy(out, r.dispatches)
	return out
}

// recordingEmitter captures every emitted event, including the optional
// e2s_state capability.
type recordingEmitter struct {
	mu            sync.Mutex
	events        []string
	states        []map[string]any
	thoughts      []string
	toolPreviews  []string
	toolResultLen []int
}

func (e *recordingEmitter) record(kind string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, kind)
}

func (e *recordingEmitter) StepStart(int) { e.record("StepStart") }
func (e *recordingEmitter) Thought(_ int, content, _ string) {
	e.mu.Lock()
	e.thoughts = append(e.thoughts, content)
	e.mu.Unlock()
}
func (e *recordingEmitter) ToolCall(_, _ int, toolName, _, _ string) {
	e.record("ToolCall:" + toolName)
}
func (e *recordingEmitter) ToolResult(_, _, resultLen int, preview string, _ bool) {
	e.mu.Lock()
	e.toolPreviews = append(e.toolPreviews, preview)
	e.toolResultLen = append(e.toolResultLen, resultLen)
	e.mu.Unlock()
	e.record("ToolResult")
}
func (e *recordingEmitter) StepComplete(int, time.Duration) { e.record("StepComplete") }
func (e *recordingEmitter) AssistantChunk(string)           { e.record("AssistantChunk") }
func (e *recordingEmitter) AssistantDone(string, int, int)  { e.record("AssistantDone") }
func (e *recordingEmitter) ContextFill(float64, int, int, string, string) {
	e.record("ContextFill")
}
func (e *recordingEmitter) Finishing(int, string) { e.record("Finishing") }
func (e *recordingEmitter) ExecutorDiagnostic(_ int, event string, _ map[string]any) {
	e.record("Diagnostic:" + event)
}

// E2SState implements the optional StateEmitter capability.
func (e *recordingEmitter) E2SState(data map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.states = append(e.states, data)
}

func (e *recordingEmitter) eventList() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.events))
	copy(out, e.events)
	return out
}

func (e *recordingEmitter) stateCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.states)
}

func (e *recordingEmitter) stateAt(i int) map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.states[i]
}

// memTrajectory is an in-memory agent.TrajectoryStore.
type memTrajectory struct {
	mu    sync.Mutex
	steps []agent.Step
}

func (t *memTrajectory) Sync(steps []agent.Step) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.steps = append([]agent.Step(nil), steps...)
}

func (t *memTrajectory) Steps() []agent.Step {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.steps
}

func testConfig() Config {
	return Config{
		Model:  "test-model",
		Task:   "count to three",
		Logger: slogDiscard(),
	}
}

// ---------------------------------------------------------------------------
// Acceptance: one LLM call per step; no history leaks between steps
// ---------------------------------------------------------------------------

func TestRun_FreshOneShotDialogPerStep(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{"scratchpad":"turn one"}`, "alpha", `{"q":1}`),
		stepResponse(`{}`, "beta", `{"q":2}`),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	reg := &mockRegistry{}
	loop := New(caller, reg, nil, testConfig())

	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Finished || res.Status != RunStatusFinished {
		t.Fatalf("status = %s (%v), want finished", res.Status, res.Finished)
	}

	if got := caller.callCount(); got != 3 {
		t.Fatalf("LLM call count = %d, want 3 (one per step)", got)
	}

	// Every request is a fresh one-shot dialog: exactly [system, user], no
	// assistant/tool roles, no tool calls riding along.
	for i := 0; i < 3; i++ {
		req := caller.request(i)
		if len(req.Messages) != 2 {
			t.Fatalf("request %d has %d messages, want exactly 2", i+1, len(req.Messages))
		}
		if req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
			t.Errorf("request %d roles = %s/%s, want system/user", i+1, req.Messages[0].Role, req.Messages[1].Role)
		}
		for j, m := range req.Messages {
			if len(m.ToolCalls) != 0 {
				t.Errorf("request %d message %d carries tool calls from a previous step", i+1, j)
			}
		}
	}

	// The state Σ is the ONLY thing carried forward: step 2 sees the turn-1
	// patch (by design), but step 3 must NOT see step 1's observation —
	// only the latest O₃ survives (bounded O(1) context).
	step2User := caller.request(1).Messages[1].Content
	if !strings.Contains(step2User, `"scratchpad":"turn one"`) {
		t.Error("step 2 request missing the applied state (Σ must carry forward)")
	}
	if !strings.Contains(step2User, "ok result for alpha") {
		t.Error("step 2 request missing observation O₂ (alpha result)")
	}
	step3User := caller.request(2).Messages[1].Content
	if !strings.Contains(step3User, "ok result for beta") {
		t.Error("step 3 request missing observation O₃ (beta result)")
	}
	if strings.Contains(step3User, "ok result for alpha") {
		t.Error("step 1 observation leaked into step 3 request — context is not O(1)")
	}
	// Turn markers advance (budgeted form "[turn N of M]" — testConfig
	// defaults MaxSteps to 16).
	if !strings.Contains(step2User, "[turn 2 of 16]") || !strings.Contains(step3User, "[turn 3 of 16]") {
		t.Error("turn markers missing from user messages")
	}
	// The model's reasoning never rides along.
	for i := 0; i < 3; i++ {
		if strings.Contains(caller.request(i).Messages[1].Content, "thinking about it") {
			t.Errorf("request %d user message contains assistant reasoning", i+1)
		}
	}
	// Exactly one tool definition is offered: the e2s_step envelope.
	req := caller.request(0)
	if len(req.Tools) != 1 || req.Tools[0].Name != StepToolName {
		t.Errorf("tool definitions = %v, want exactly [e2s_step]", req.Tools)
	}
}

// TestRun_AttachesContentBlocksToEveryTurn pins the image-threading contract:
// configured ContentBlocks ride on EVERY turn's user message (each turn is a
// fresh dialog, so the model must keep seeing them), while the per-turn Σ +
// observation text stays in Content.
func TestRun_AttachesContentBlocksToEveryTurn(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "alpha", `{"q":1}`),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	reg := &mockRegistry{}
	cfg := testConfig()
	cfg.ContentBlocks = []llm.ContentBlock{{Type: "image", ImageB64: "AAAA", MediaType: "image/png"}}
	loop := New(caller, reg, nil, cfg)

	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if caller.callCount() != 2 {
		t.Fatalf("LLM call count = %d, want 2", caller.callCount())
	}
	for i := 0; i < 2; i++ {
		msg := caller.request(i).Messages[1]
		if len(msg.ContentBlocks) != 1 || msg.ContentBlocks[0].Type != "image" || msg.ContentBlocks[0].ImageB64 != "AAAA" {
			t.Errorf("request %d user ContentBlocks = %+v, want the staged image on every turn", i+1, msg.ContentBlocks)
		}
		if msg.Content == "" {
			t.Errorf("request %d user Content is empty; the turn text must remain", i+1)
		}
	}
}

// ---------------------------------------------------------------------------
// Acceptance: action dispatch goes through Registry.Execute
// ---------------------------------------------------------------------------

func TestRun_DispatchesActionThroughRegistry(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "read_file", `{"path":"a.txt"}`),
		stepResponse(`{}`, "finish", `{"answer":"read it"}`),
	}}
	reg := &mockRegistry{}
	loop := New(caller, reg, nil, testConfig())

	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	dispatches := reg.dispatched()
	if len(dispatches) != 1 {
		t.Fatalf("registry dispatches = %d, want 1", len(dispatches))
	}
	if dispatches[0].name != "read_file" {
		t.Errorf("dispatched tool = %q, want read_file", dispatches[0].name)
	}
	var args map[string]any
	if err := json.Unmarshal(dispatches[0].args, &args); err != nil {
		t.Fatalf("dispatch args not JSON: %v", err)
	}
	if args["path"] != "a.txt" {
		t.Errorf("dispatch args = %v, want path a.txt", args)
	}
}

// ---------------------------------------------------------------------------
// Acceptance: finish terminates the loop and carries the answer
// ---------------------------------------------------------------------------

func TestRun_FinishTerminatesWithAnswer(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{"findings":["f1"]}`, "finish", `{"answer":"the final answer"}`),
	}}
	em := &recordingEmitter{}
	loop := New(caller, &mockRegistry{}, em, testConfig())

	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Answer != "the final answer" {
		t.Errorf("answer = %q, want the final answer", res.Answer)
	}
	if !res.Finished {
		t.Error("Finished = false, want true")
	}
	if res.Turns != 1 {
		t.Errorf("turns = %d, want 1", res.Turns)
	}
	events := em.eventList()
	if !contains(events, "Finishing") {
		t.Errorf("Finishing event not emitted: %v", events)
	}
	if !contains(events, "AssistantChunk") || !contains(events, "AssistantDone") {
		t.Errorf("assistant events not emitted for finish answer: %v", events)
	}
	// The last trajectory step records the finish action with the answer.
	if n := len(res.Steps); n != 1 {
		t.Fatalf("trajectory steps = %d, want 1", n)
	}
	last := res.Steps[0]
	if last.Action.Name != "finish" {
		t.Errorf("last step action = %q, want finish", last.Action.Name)
	}
	if !strings.Contains(string(last.Action.Input), "the final answer") {
		t.Errorf("last step input = %s, want the answer embedded", last.Action.Input)
	}
	// The finish-turn patch was applied before finishing.
	if got := res.Snapshot.Sigma["findings"]; fmt.Sprint(got) != "[f1]" {
		t.Errorf("finish-turn patch not applied: findings = %v", got)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Acceptance: invalid patch → bounded retry → error observation, Σ intact
// ---------------------------------------------------------------------------

func TestRun_InvalidPatchRetriesOnceThenErrorObservation(t *testing.T) {
	// Turn 1: patch violates the domain schema (objective re-typed to a
	// number). Retry: still invalid. Then the turn's error becomes O₂.
	// Turn 2 (new step): valid finish.
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		rawStepResponse(`{"state_patch":{"objective":42},"action":{"tool":"probe","args":{}}}`),
		rawStepResponse(`{"state_patch":{"objective":43},"action":{"tool":"probe","args":{}}}`),
		stepResponse(`{}`, "finish", `{"answer":"recovered"}`),
	}}
	reg := &mockRegistry{}
	em := &recordingEmitter{}
	loop := New(caller, reg, em, testConfig())

	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != RunStatusFinished {
		t.Fatalf("status = %s, want finished (loop must survive invalid turns)", res.Status)
	}

	// Exactly one extra call (the bounded retry) — not more.
	if got := caller.callCount(); got != 3 {
		t.Fatalf("LLM calls = %d, want 3 (turn + one retry + final turn)", got)
	}
	// The retry request carries the correction tail.
	retryUser := caller.request(1).Messages[1].Content
	if !strings.Contains(retryUser, "<correction>") {
		t.Error("retry user message missing correction tail")
	}
	// The invalid action was never dispatched.
	if d := reg.dispatched(); len(d) != 0 {
		t.Errorf("invalid turn leaked into dispatch: %v", d)
	}
	// Σ was not damaged: objective keeps its seeded string value.
	if got, _ := res.Snapshot.Sigma[CoreKeyObjective].(string); got != "count to three" {
		t.Errorf("objective = %v, want untouched string", res.Snapshot.Sigma[CoreKeyObjective])
	}
	// The invalid turn surfaced as an error observation in the NEXT request.
	nextUser := caller.request(2).Messages[1].Content
	if !strings.Contains(nextUser, "invalid and was NOT applied") {
		t.Errorf("next user message missing error observation: %q", nextUser)
	}
	// Trajectory records the invalid turn with IsError.
	found := false
	for _, s := range res.Steps {
		if s.Action.Name == StepToolName && s.IsError {
			found = true
		}
	}
	if !found {
		t.Error("invalid turn not recorded as an error step in the trajectory")
	}
}

func rawStepResponse(input string) *llm.ChatResponse {
	return rawToolResponse(StepToolName, input)
}

func TestRun_InvalidThenRetryRecovers(t *testing.T) {
	// Turn 1 first attempt calls the WRONG tool name (the loop offers only
	// e2s_step); the corrective retry returns a valid probe call, which
	// dispatches normally — no error observation is needed.
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		rawToolResponse("probe", `{"q":1}`),
		stepResponse(`{}`, "probe", `{"q":1}`),
		stepResponse(`{}`, "finish", `{"answer":"ok"}`),
	}}
	reg := &mockRegistry{}
	loop := New(caller, reg, nil, testConfig())

	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != RunStatusFinished {
		t.Fatalf("status = %s, want finished", res.Status)
	}
	// First call was invalid (wrong tool name); the retry dispatched the
	// probe exactly once.
	if d := reg.dispatched(); len(d) != 1 || d[0].name != "probe" {
		t.Errorf("dispatches = %v, want one probe", d)
	}
}

// ---------------------------------------------------------------------------
// Acceptance: step limit stops the loop with a status
// ---------------------------------------------------------------------------

func TestRun_StepLimitStopsLoop(t *testing.T) {
	responses := make([]*llm.ChatResponse, 0, 10)
	for i := 0; i < 10; i++ {
		responses = append(responses, stepResponse(`{}`, "probe", fmt.Sprintf(`{"n":%d}`, i)))
	}
	caller := &scriptedCaller{responses: responses}
	cfg := testConfig()
	cfg.MaxSteps = 4
	loop := New(caller, &mockRegistry{}, nil, cfg)

	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != RunStatusStepLimit {
		t.Errorf("status = %s, want step_limit", res.Status)
	}
	if res.Finished {
		t.Error("Finished = true on step limit, want false")
	}
	if res.Turns != 4 {
		t.Errorf("turns = %d, want 4", res.Turns)
	}
	if got := caller.callCount(); got != 4 {
		t.Errorf("LLM calls = %d, want 4", got)
	}
}

// ---------------------------------------------------------------------------
// Acceptance: anti-spin nudges then stops the loop with a status
// ---------------------------------------------------------------------------

// TestRun_SpinDetectsShiftingRanges pins the SEMANTIC anti-spin: re-reading
// the same file with ever-shifting line ranges (the production-session
// failure mode — byte-exact fingerprints never matched) is detected as a
// spin: nudged, then aborted.
func TestRun_SpinDetectsShiftingRanges(t *testing.T) {
	ranges := []string{
		`{"path":"diff.txt","start_line":1,"end_line":320}`,
		`{"path":"diff.txt","start_line":321,"end_line":520}`,
		`{"path":"diff.txt","start_line":521,"end_line":660}`,
		`{"path":"diff.txt","start_line":661,"end_line":922}`,
		`{"path":"diff.txt","start_line":1,"end_line":200}`,
		`{"path":"diff.txt","start_line":201,"end_line":400}`,
	}
	responses := make([]*llm.ChatResponse, 0, len(ranges))
	for _, r := range ranges {
		responses = append(responses, stepResponse(`{}`, "read_file", r))
	}
	caller := &scriptedCaller{responses: responses}
	reg := newSchemaRegistry()
	em := &recordingEmitter{}
	loop := New(caller, reg, em, testConfig())

	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != RunStatusSpinStop {
		t.Errorf("status = %s, want spin_stop (shifting ranges on one target)", res.Status)
	}
	// Only the first two reads executed; the rest were nudged/aborted.
	if d := reg.dispatched(); len(d) != 2 {
		t.Errorf("dispatch count = %d, want 2", len(d))
	}
}

func TestRun_AntiSpinNudgesThenStops(t *testing.T) {
	// Eight identical probe calls; nudge at 3, abort at 5 (defaults).
	responses := make([]*llm.ChatResponse, 0, 8)
	for i := 0; i < 8; i++ {
		responses = append(responses, stepResponse(`{}`, "probe", `{"q":"same"}`))
	}
	caller := &scriptedCaller{responses: responses}
	reg := &mockRegistry{}
	em := &recordingEmitter{}
	loop := New(caller, reg, em, testConfig())

	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != RunStatusSpinStop {
		t.Errorf("status = %s, want spin_stop", res.Status)
	}

	events := em.eventList()
	nudges := 0
	for _, ev := range events {
		if ev == "Diagnostic:spin_nudge" {
			nudges++
		}
	}
	if nudges == 0 {
		t.Error("no spin_nudge diagnostic emitted before the stop")
	}

	// Redundant repeats were not re-dispatched: 2 executions (turns 1-2),
	// nudged from turn 3, aborted at turn 5.
	if d := reg.dispatched(); len(d) != 2 {
		t.Errorf("dispatch count = %d, want 2 (identical actions skipped after detection)", len(d))
	}
	// The nudge observation reached the model on the next turn.
	spinUser := caller.request(3).Messages[1].Content
	if !strings.Contains(spinUser, "repeated the identical action") {
		t.Errorf("turn 4 user message missing spin nudge: %q", spinUser)
	}
}

// ---------------------------------------------------------------------------
// Acceptance: e2s_state emitted after every applied patch
// ---------------------------------------------------------------------------

func TestRun_EmitsStateAfterEachAppliedPatch(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{"scratch":"one"}`, "probe", `{}`),
		stepResponse(`{"notes":"two"}`, "probe", `{}`),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	em := &recordingEmitter{}
	loop := New(caller, &mockRegistry{}, em, testConfig())

	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Three turns, three applied patches (the finish turn's empty patch
	// still merges and bumps the domain turn count).
	if got := em.stateCount(); got != 3 {
		t.Fatalf("e2s_state emissions = %d, want 3", got)
	}
	first := em.stateAt(0)
	if first["turn"] != 1 {
		t.Errorf("first snapshot turn = %v, want 1", first["turn"])
	}
	if first["total_turns"] != 1 {
		t.Errorf("first snapshot total_turns = %v, want 1 (cumulative domain count)", first["total_turns"])
	}
	sigma, ok := first["state"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot state is %T, want map[string]any", first["state"])
	}
	if sigma["scratch"] != "one" {
		t.Errorf("snapshot Σ missing applied patch: %v", sigma)
	}
	// Snapshot turns are the domain TurnCount (patch counter).
	if em.stateAt(2)["turn"] != 3 {
		t.Errorf("last snapshot turn = %v, want 3", em.stateAt(2)["turn"])
	}
	if res.Snapshot.TurnCount != 3 {
		t.Errorf("final TurnCount = %d, want 3", res.Snapshot.TurnCount)
	}
}

// ---------------------------------------------------------------------------
// Pause, cancellation, fatal error
// ---------------------------------------------------------------------------

func TestRun_PauseCheckerStopsAtStepBoundary(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "probe", `{}`),
		stepResponse(`{}`, "finish", `{"answer":"never"}`),
	}}
	cfg := testConfig()
	cfg.PauseChecker = func(context.Context) bool { return true }
	loop := New(caller, &mockRegistry{}, nil, cfg)

	res, err := loop.Run(context.Background())
	if !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
	if res.Status != RunStatusPaused {
		t.Errorf("status = %s, want paused", res.Status)
	}
	if got := caller.callCount(); got != 0 {
		t.Errorf("a paused run consumed %d LLM calls, want 0", got)
	}
}

func TestRun_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	caller := &scriptedCaller{}
	loop := New(caller, &mockRegistry{}, nil, testConfig())

	res, err := loop.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.Status != RunStatusCanceled {
		t.Errorf("status = %s, want canceled", res.Status)
	}
}

// cancellingCaller cancels the task context from inside the call and returns
// the resulting cancellation error — modeling an app shutdown that lands while
// an LLM request is in flight.
type cancellingCaller struct{ cancel context.CancelFunc }

func (c *cancellingCaller) Call(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	c.cancel()
	return nil, ctx.Err()
}

// TestRun_CancellationDuringLLMCallStaysResumable pins the mid-call shutdown
// classification: a cancellation surfaced inside the LLM request must be a
// cancellation (non-terminal, resumable checkpoint), not a fatal failure.
func TestRun_CancellationDuringLLMCallStaysResumable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loop := New(&cancellingCaller{cancel: cancel}, &mockRegistry{}, nil, testConfig())

	res, err := loop.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.Status != RunStatusCanceled {
		t.Errorf("status = %s, want canceled — a mid-call shutdown must keep the checkpoint resumable, not terminal", res.Status)
	}
}

func TestRun_FatalLLMError(t *testing.T) {
	caller := &scriptedCaller{
		errs: []error{errors.New("provider down")},
	}
	loop := New(caller, &mockRegistry{}, nil, testConfig())

	res, err := loop.Run(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if res.Status != RunStatusFailed {
		t.Errorf("status = %s, want failed", res.Status)
	}
}

// ---------------------------------------------------------------------------
// Tool result plumbing
// ---------------------------------------------------------------------------

func TestRun_ToolErrorBecomesObservation(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "bad_tool", `{}`),
		stepResponse(`{}`, "boom", `{}`),
		stepResponse(`{}`, "finish", `{"answer":"survived"}`),
	}}
	loop := New(caller, &mockRegistry{}, nil, testConfig())

	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != RunStatusFinished {
		t.Fatalf("status = %s, want finished", res.Status)
	}
	// IsError tool result → error observation in the next request.
	if user := caller.request(1).Messages[1].Content; !strings.Contains(user, "tool failed") {
		t.Errorf("turn 2 missing error observation: %q", user)
	}
	// Registry-level error → error observation too.
	if user := caller.request(2).Messages[1].Content; !strings.Contains(user, "tool execution error") {
		t.Errorf("turn 3 missing registry-error observation: %q", user)
	}
	// Trajectory marks both as errors.
	errSteps := 0
	for _, s := range res.Steps {
		if s.IsError {
			errSteps++
		}
	}
	if errSteps != 2 {
		t.Errorf("error steps = %d, want 2", errSteps)
	}
}

func TestRun_UntrustedResultWrapped(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "web_fetch", `{}`),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	reg := &mockRegistry{untrusted: map[string]bool{"web_fetch": true}}
	loop := New(caller, reg, nil, testConfig())

	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	user := caller.request(1).Messages[1].Content
	if !strings.Contains(user, "<untrusted-content") {
		t.Errorf("untrusted observation not wrapped: %q", user)
	}
}

// TestRun_UntrustedErrorResultWrapped pins the error-recovery carve-out: an
// untrusted tool's ERROR diagnostic is attacker-influenceable and must be
// delivered inside the boundary exactly like successful output — wrapping is
// decided by tool class, not result type.
func TestRun_UntrustedErrorResultWrapped(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "bad_tool", `{}`),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	reg := &mockRegistry{untrusted: map[string]bool{"bad_tool": true}}
	loop := New(caller, reg, nil, testConfig())

	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	user := caller.request(1).Messages[1].Content
	if !strings.Contains(user, "<untrusted-content") || !strings.Contains(user, "tool failed") {
		t.Errorf("untrusted error observation not wrapped: %q", user)
	}
}

// schemaRegistry serves descriptors with closed-set schemas so the
// pre-dispatch action-args validation can be exercised end-to-end.
type schemaRegistry struct {
	mockRegistry
}

func newSchemaRegistry() *schemaRegistry {
	return &schemaRegistry{mockRegistry: mockRegistry{
		descriptors: []sdktools.ToolDescriptor{
			{Name: "read_file", Description: "read a file", InputSchema: json.RawMessage(readFileSchema)},
		},
	}}
}

// TestRun_InvalidActionArgsRejectedPreDispatch pins the pre-dispatch
// structural validation: wrong parameter names produce an actionable error
// observation (naming the valid parameters), the tool is NOT dispatched, and
// no patch-retry budget is consumed (the turn's patch was already applied —
// the next turn simply corrects the args).
func TestRun_InvalidActionArgsRejectedPreDispatch(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{"progress":"reading diff"}`, "read_file", `{"path":"diff.txt","offset":1,"limit":100}`),
		stepResponse(`{}`, "read_file", `{"path":"diff.txt","start_line":1,"end_line":100}`),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	reg := newSchemaRegistry()
	loop := New(caller, reg, nil, testConfig())

	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != RunStatusFinished {
		t.Fatalf("status = %s, want finished", res.Status)
	}

	// The invalid call must NOT reach the registry: only the corrected call
	// plus nothing else dispatched for read_file.
	dispatched := reg.dispatched()
	reads := 0
	for _, d := range dispatched {
		if d.name == "read_file" {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("read_file dispatched %d times, want 1 (invalid args must be rejected pre-dispatch)", reads)
	}

	// The turn-2 observation carries the actionable error with valid names.
	user := caller.request(1).Messages[1].Content
	if !strings.Contains(user, "action arguments rejected") || !strings.Contains(user, "unknown parameter") {
		t.Errorf("action-args error observation missing: %q", user)
	}
	for _, valid := range []string{"start_line", "end_line"} {
		if !strings.Contains(user, valid) {
			t.Errorf("error must name valid parameter %q: %q", valid, user)
		}
	}

	// The invalid turn consumed NO extra LLM call (no correction retry for
	// action errors — they are ordinary observations): 3 calls for 3 turns.
	if got := caller.callCount(); got != 3 {
		t.Errorf("llm calls = %d, want 3 (action-arg errors must not burn patch retries)", got)
	}
}

// batchArgs builds a batch action args object from sub-calls.
func batchArgs(subs ...string) string {
	return `{"calls":[` + strings.Join(subs, ",") + `]}`
}

// TestRun_BatchExecutesSubCallsAndJoins pins the E2S batch meta-tool: every
// sub-call dispatches through the registry in order and the observation
// joins the numbered results.
func TestRun_BatchExecutesSubCallsAndJoins(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "batch", batchArgs(
			`{"tool":"read_file","input":{"path":"a.go"}}`,
			`{"tool":"glob","input":{"pattern":"*.go"}}`,
		)),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	reg := &mockRegistry{}
	loop := New(caller, reg, nil, testConfig())

	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	dispatched := reg.dispatched()
	if len(dispatched) != 2 {
		t.Fatalf("sub-calls dispatched = %d, want 2: %+v", len(dispatched), dispatched)
	}
	if dispatched[0].name != "read_file" || dispatched[1].name != "glob" {
		t.Errorf("sub-calls out of order: %+v", dispatched)
	}

	user := caller.request(1).Messages[1].Content
	for _, want := range []string{"[batch result 1/2 — read_file]", "ok result for read_file", "[batch result 2/2 — glob]", "ok result for glob"} {
		if !strings.Contains(user, want) {
			t.Errorf("joined batch observation missing %q: %q", want, user)
		}
	}
}

// TestRun_BatchSubCallErrorDoesNotAbort pins the executor-mirroring
// semantics: a failing sub-call is captured per index, later sub-calls still
// run, and the batch observation carries the error.
func TestRun_BatchSubCallErrorDoesNotAbort(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "batch", batchArgs(
			`{"tool":"bad_tool","input":{}}`,
			`{"tool":"glob","input":{"pattern":"*.go"}}`,
		)),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	reg := &mockRegistry{}
	loop := New(caller, reg, nil, testConfig())

	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if dispatched := reg.dispatched(); len(dispatched) != 2 {
		t.Fatalf("both sub-calls must run (errors do not abort): %+v", dispatched)
	}
	user := caller.request(1).Messages[1].Content
	if !strings.Contains(user, "tool failed") || !strings.Contains(user, "ok result for glob") {
		t.Errorf("batch observation must contain the sub-error and the later success: %q", user)
	}
	if len(res.Steps) == 0 || !res.Steps[0].IsError {
		t.Error("batch step with a failing sub-call must be marked IsError")
	}
}

// TestRun_BatchRejectsNestedAndEnvelopeTargets pins the fail-closed rules:
// nested batch and the e2s_step/finish envelope targets are rejected inside
// calls (finish must stay a top-level action), and the legal sub-calls
// around them still execute.
func TestRun_BatchRejectsNestedAndEnvelopeTargets(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "batch", batchArgs(
			`{"tool":"batch","input":{"calls":[]}}`,
			`{"tool":"finish","input":{"answer":"nope"}}`,
			`{"tool":"e2s_step","input":{}}`,
			`{"tool":"glob","input":{"pattern":"*.go"}}`,
		)),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	reg := &mockRegistry{}
	loop := New(caller, reg, nil, testConfig())

	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != RunStatusFinished || res.Answer != "done" {
		t.Fatalf("nested finish must NOT terminate the run; status=%s answer=%q", res.Status, res.Answer)
	}
	// Only the legal sub-call reached the registry.
	dispatched := reg.dispatched()
	if len(dispatched) != 1 || dispatched[0].name != "glob" {
		t.Fatalf("only the legal sub-call may dispatch: %+v", dispatched)
	}
	user := caller.request(1).Messages[1].Content
	for _, want := range []string{"batch cannot be nested", `"finish" cannot be used inside a batch`, `"e2s_step" cannot be used inside a batch`} {
		if !strings.Contains(user, want) {
			t.Errorf("rejection message missing %q: %q", want, user)
		}
	}
}

// TestRun_BatchSubCallArgValidation pins that the pre-dispatch schema
// validation applies to each batch sub-call, not just top-level actions.
func TestRun_BatchSubCallArgValidation(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "batch", batchArgs(
			`{"tool":"read_file","input":{"path":"a.txt","offset":1}}`,
		)),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	reg := newSchemaRegistry()
	loop := New(caller, reg, nil, testConfig())

	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if dispatched := reg.dispatched(); len(dispatched) != 0 {
		t.Fatalf("invalid sub-call args must not dispatch: %+v", dispatched)
	}
	user := caller.request(1).Messages[1].Content
	if !strings.Contains(user, "unknown parameter") || !strings.Contains(user, "start_line") {
		t.Errorf("sub-call validation error missing: %q", user)
	}
}

// TestRun_LongBatchResultTruncatedWithNudge pins that the joined batch
// observation goes through the same cache-on-truncate path (the full joined
// result is recoverable via tool_result_read).
func TestRun_LongBatchResultTruncatedWithNudge(t *testing.T) {
	long := strings.Repeat("chunk\n", 5_000)
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "batch", batchArgs(`{"tool":"big","input":{}}`)),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	cache := agent.NewToolResultCache(time.Minute)
	cfg := testConfig()
	cfg.ToolCache = cache
	loop := New(caller, &bigResultRegistry{long: long}, nil, cfg)

	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	user := caller.request(1).Messages[1].Content
	if !strings.Contains(user, "This output was truncated") || !strings.Contains(user, "cached with hash: ") {
		t.Errorf("long batch observation must carry the truncation nudge: %q", user)
	}
	if cache.Len() != 1 {
		t.Errorf("cache entries = %d, want 1", cache.Len())
	}
}

func TestRun_ObservationTruncated(t *testing.T) {
	long := strings.Repeat("x", 10_000)
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		&llm.ChatResponse{Message: llm.Message{ToolCalls: []llm.ToolCall{{
			ID:    "c1",
			Name:  StepToolName,
			Input: json.RawMessage(`{"state_patch":{},"action":{"tool":"big","args":{}}}`),
		}}}},
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	reg := &bigResultRegistry{long: long}
	loop := New(caller, reg, nil, testConfig())

	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	user := caller.request(1).Messages[1].Content
	if strings.Contains(user, long) {
		t.Error("full 10k observation leaked into the request")
	}
	if !strings.Contains(user, "…") {
		t.Error("truncation marker missing")
	}
}

type bigResultRegistry struct{ long string }

func (r *bigResultRegistry) List() []sdktools.ToolDescriptor { return nil }

func (r *bigResultRegistry) Execute(_ context.Context, _ string, _ json.RawMessage) (sdktools.ToolResult, error) {
	return sdktools.ToolResult{Content: r.long}, nil
}

func (r *bigResultRegistry) IsToolUntrusted(string) bool { return false }

func (r *bigResultRegistry) ToolSource(string) string { return "core" }

// ctxCapturingRegistry records whether the dispatch context carries the
// tool-result cache (agent.ToolResultCacheFromContext).
type ctxCapturingRegistry struct {
	bigResultRegistry
	mu       sync.Mutex
	hasCache bool
	called   bool
}

func (r *ctxCapturingRegistry) Execute(ctx context.Context, name string, input json.RawMessage) (sdktools.ToolResult, error) {
	r.mu.Lock()
	r.called = true
	r.hasCache = agent.ToolResultCacheFromContext(ctx) != nil
	r.mu.Unlock()
	return r.bigResultRegistry.Execute(ctx, name, input)
}

// TestRun_TruncatedObservationCachedWithNudge pins cache-on-truncate: an
// observation truncated by MaxObservationChars is stored in full in the
// shared cache and the next request's observation ends with the standard
// fragmentation nudge carrying the cache hash.
func TestRun_TruncatedObservationCachedWithNudge(t *testing.T) {
	long := strings.Repeat("line of content\n", 2_000) // ~30k chars > any default cap
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "big", `{}`),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	reg := &ctxCapturingRegistry{bigResultRegistry: bigResultRegistry{long: long}}
	cache := agent.NewToolResultCache(time.Minute)
	cfg := testConfig()
	cfg.ToolCache = cache
	loop := New(caller, reg, nil, cfg)

	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The dispatch context must carry the cache so tool_result_read resolves.
	if !reg.called || !reg.hasCache {
		t.Fatalf("dispatch ctx must carry the tool-result cache (called=%v hasCache=%v)", reg.called, reg.hasCache)
	}

	user := caller.request(1).Messages[1].Content
	if strings.Contains(user, long) {
		t.Error("full 30k observation leaked into the request")
	}
	if !strings.Contains(user, "This output was truncated") || !strings.Contains(user, "cached with hash: ") {
		t.Errorf("truncation nudge with cache hash missing: %q", user)
	}
	if !strings.Contains(user, `tool_result_read(hash="`) {
		t.Errorf("nudge must point at tool_result_read: %q", user)
	}

	// The full result must be retrievable from the cache by the hash in the
	// nudge: extract it and Get() the entry.
	start := strings.Index(user, "cached with hash: ")
	if start < 0 {
		t.Fatal("hash marker missing")
	}
	hashRest := user[start+len("cached with hash: "):]
	end := strings.IndexAny(hashRest, ". ")
	if end < 0 {
		t.Fatal("hash terminator missing")
	}
	hash := hashRest[:end]
	entry, ok := cache.Get(hash)
	if !ok {
		t.Fatalf("cache entry %q not found", hash)
	}
	if entry.Content != long {
		t.Errorf("cached content length = %d, want %d", len(entry.Content), len(long))
	}
}

// TestRun_TruncationWithoutCacheDegrades pins the nil-cache degradation: no
// cache configured → plain truncation, no nudge, no panic.
func TestRun_TruncationWithoutCacheDegrades(t *testing.T) {
	long := strings.Repeat("y", 10_000)
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "big", `{}`),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	loop := New(caller, &bigResultRegistry{long: long}, nil, testConfig())

	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	user := caller.request(1).Messages[1].Content
	if strings.Contains(user, "cached with hash") {
		t.Error("nudge must not appear without a configured cache")
	}
}

// TestRun_SmallObservationNotCached pins that observations under the cap are
// not stored (cache stays empty — no eager caching in E2S).
func TestRun_SmallObservationNotCached(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "fine", `{}`),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	cache := agent.NewToolResultCache(time.Minute)
	cfg := testConfig()
	cfg.ToolCache = cache
	loop := New(caller, &mockRegistry{}, nil, cfg)

	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if cache.Len() != 0 {
		t.Errorf("cache entries = %d, want 0 for a small observation", cache.Len())
	}
}

// ---------------------------------------------------------------------------
// Trajectory store sync
// ---------------------------------------------------------------------------

func TestRun_SyncsTrajectoryStore(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "probe", `{"q":1}`),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	traj := &memTrajectory{}
	cfg := testConfig()
	cfg.Trajectory = traj
	loop := New(caller, &mockRegistry{}, nil, cfg)

	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := traj.Steps(); len(got) != len(res.Steps) {
		t.Errorf("trajectory store steps = %d, want %d", len(got), len(res.Steps))
	}
	// Steps carry the dispatched action and its observation.
	first := traj.Steps()[0]
	if first.Action.Name != "probe" {
		t.Errorf("trajectory action = %q, want probe", first.Action.Name)
	}
	if !strings.Contains(first.Observation, "ok result for probe") {
		t.Errorf("trajectory observation = %q, want tool result", first.Observation)
	}
	if first.Thought != "thinking about it" {
		t.Errorf("trajectory thought = %q, want response reasoning", first.Thought)
	}
}

// ---------------------------------------------------------------------------
// Prompt shape
// ---------------------------------------------------------------------------

func TestSystemPrompt_Sections(t *testing.T) {
	cfg := testConfig()
	cfg.WorkspacePath = "/ws/project"
	cfg.TempDir = "/ws/tmp"
	cfg.DelegateDirective = "Delegate via delegate(agent:...)."
	cfg.Skills = []SkillSection{{Name: "explore", Description: "think first", Body: "Body of skill."}}
	reg := &mockRegistry{descriptors: []sdktools.ToolDescriptor{
		{Name: "read_file", Description: "Read a file.\nSecond line.", InputSchema: json.RawMessage(readFileSchema)},
		{Name: "bash_exec", Description: "Run a shell command.", InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)},
		{Name: "no_schema", Description: "Schema-less tool."},
	}}

	prompt := BuildSystemPrompt(cfg, reg.List())

	for _, want := range []string{
		"E2S",          // core directive present
		"## Workspace", // workspace section
		"/ws/project",  // workspace path
		"/ws/tmp",      // temp dir
		"## Available Tools",
		"`read_file`",
		"`bash_exec`",
		"## Delegation",
		"## Active Skills",
		"Body of skill.",
		// Full input schemas render inline so the model knows parameter names.
		"args schema: {\"type\":\"object\",\"properties\":{\"path\"",
		`"start_line"`,
		"args schema: {\"type\":\"object\",\"properties\":{\"command\"",
		"MUST use exactly the parameter names",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
	// Only the first line of a description is inlined.
	if strings.Contains(prompt, "Second line.") {
		t.Error("tool description second line leaked into the catalog")
	}
	// A tool without a schema gets no schema line (no dangling marker).
	if strings.Contains(prompt, "`no_schema`: Schema-less tool.\n  args schema:") {
		t.Error("schema-less tool must not render an args-schema line")
	}
}

func TestUserMessage_Shape(t *testing.T) {
	state := map[string]any{"objective": "do it", "k": 1}

	// Budgeted run: "[turn N of M]" renders; the warning appears only in the
	// final window.
	msg := BuildUserMessage(state, "obs-text", 7, 50)
	for _, want := range []string{"[turn 7 of 50]", "<state>", `"objective":"do it"`, "<observation>", "obs-text"} {
		if !strings.Contains(msg, want) {
			t.Errorf("user message missing %q: %q", want, msg)
		}
	}
	if strings.Contains(msg, "turns remain") {
		t.Errorf("wrap-up warning must not render at turn 7 of 50: %q", msg)
	}

	// Inside the warning window (50 − max(3, 10) + 1 = turn 41): the
	// directive renders with the remaining count.
	warn := BuildUserMessage(state, "obs-text", 45, 50)
	if !strings.Contains(warn, "[turn 45 of 50 — 5 turns remain") || !strings.Contains(warn, "call finish") {
		t.Errorf("wrap-up warning missing at turn 45 of 50: %q", warn)
	}

	// Tiny budget: the window is the last 3 turns, so turn 1 of 5 stays
	// clean (the format is not confused by small budgets).
	tiny := BuildUserMessage(state, "obs-text", 1, 5)
	if !strings.Contains(tiny, "[turn 1 of 5]") || strings.Contains(tiny, "turns remain") {
		t.Errorf("turn 1 of 5 must be a plain header: %q", tiny)
	}
	tinyWarn := BuildUserMessage(state, "obs-text", 3, 5)
	if !strings.Contains(tinyWarn, "2 turns remain") {
		t.Errorf("turn 3 of 5 must carry the warning (2 remaining after this turn): %q", tinyWarn)
	}

	// Unbudgeted run: legacy bare header.
	legacy := BuildUserMessage(state, "obs-text", 7, 0)
	if !strings.Contains(legacy, "[turn 7]") || strings.Contains(legacy, "of 7") {
		t.Errorf("unbudgeted header must stay bare: %q", legacy)
	}
}

// ---------------------------------------------------------------------------
// Seed state
// ---------------------------------------------------------------------------

func TestSeedState_ObjectiveAndExtensions(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "finish", `{"answer":"x"}`),
	}}
	cfg := testConfig()
	cfg.Task = "the objective"
	cfg.InitialState = map[string]any{"context": "extra", CoreKeyObjective: "IGNORED"}
	loop := New(caller, &mockRegistry{}, nil, cfg)

	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	sigma := res.Snapshot.Sigma
	if sigma[CoreKeyObjective] != "the objective" {
		t.Errorf("objective = %v, want the objective (core keys are canonical)", sigma[CoreKeyObjective])
	}
	if sigma["context"] != "extra" {
		t.Errorf("extension seed missing: %v", sigma["context"])
	}
}

// slogDiscard returns a logger that drops output.
func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------------------------------------------------------------------------
// Review-fix regressions: seed limit, finish guard, verify-on-edit, batch
// raw preview, tool-call source
// ---------------------------------------------------------------------------

// TestRun_SeedStateOverLimitFailsFast pins the seed-limit guard: an initial
// Σ larger than the byte limit fails fast with an actionable error instead
// of wedging every turn on ErrStateTooLarge until the budget runs out.
func TestRun_SeedStateOverLimitFailsFast(t *testing.T) {
	huge := strings.Repeat("x", 40*1024)
	loop := New(&scriptedCaller{responses: []*llm.ChatResponse{}}, nil, nil,
		Config{Model: "m", Task: huge, Logger: slogDiscard()})
	res, err := loop.Run(context.Background())
	if err == nil {
		t.Fatal("expected a fast failure error")
	}
	if !strings.Contains(err.Error(), "byte limit") {
		t.Errorf("error should name the byte limit: %v", err)
	}
	if res == nil || res.Status != RunStatusFailed {
		t.Fatalf("Status = %v, want failed", mustStatus(res))
	}
	if res.Turns != 0 {
		t.Errorf("Turns = %d, want 0 (no LLM call may run)", res.Turns)
	}
}

func mustStatus(res *Result) RunStatus {
	if res == nil {
		return ""
	}
	return res.Status
}

// TestRun_FinishGuardVetoContinues pins the finish-join guard: a finish
// while async delegations are pending is vetoed (the reason becomes the next
// observation, no answer is emitted), and a later finish after the blocker
// clears is accepted.
func TestRun_FinishGuardVetoContinues(t *testing.T) {
	pending := atomic.Bool{}
	pending.Store(true)
	vetoes := atomic.Int32{}
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "finish", `{"answer":"too early"}`),
		stepResponse(`{}`, "finish", `{"answer":"all clear"}`),
	}}
	em := &recordingEmitter{}
	loop := New(caller, nil, em, Config{
		Model: "m", Task: "t", Logger: slogDiscard(),
		FinishGuard: func(context.Context) error {
			// Veto exactly once — the second finish models the model having
			// resolved the blocker (delegation completed/cancelled).
			if pending.Load() {
				pending.Store(false)
				vetoes.Add(1)
				return errors.New("1 pending async delegation(s): del_1")
			}
			return nil
		},
	})
	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != RunStatusFinished || res.Answer != "all clear" {
		t.Fatalf("Status=%s Answer=%q, want finished with the second answer", res.Status, res.Answer)
	}
	// The vetoed finish must not surface as an assistant message: exactly
	// ONE answer emission is allowed — the accepted finish's. (The event
	// list interleaves both turns, so count rather than scan.)
	if cnt := strings.Count(strings.Join(em.eventList(), ","), "AssistantChunk"); cnt != 1 {
		t.Errorf("AssistantChunk count = %d, want 1 (only the accepted finish): %v", cnt, em.eventList())
	}
	if cnt := strings.Count(strings.Join(em.eventList(), ","), "AssistantDone"); cnt != 1 {
		t.Errorf("AssistantDone count = %d, want 1", cnt)
	}
	if !strings.Contains(res.Steps[0].Observation, "finish rejected") {
		t.Errorf("first step observation should carry the veto: %q", res.Steps[0].Observation)
	}
}

// TestRun_EditVerifyAppendedToObservation pins the verify-on-edit hook: a
// successful write_file arms the run, the note is appended to the
// observation, and a read-only action never arms it.
func TestRun_EditVerifyAppendedToObservation(t *testing.T) {
	var verifyRuns int32
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "write_file", `{"path":"a.txt","content":"hello"}`),
		stepResponse(`{}`, "read_file", `{"path":"a.txt"}`),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	reg := &mockRegistry{}
	loop := New(caller, reg, nil, Config{
		Model: "m", Task: "t", Logger: slogDiscard(),
		EditVerify: func(context.Context) agent.EditVerifyResult {
			atomic.AddInt32(&verifyRuns, 1)
			return agent.EditVerifyResult{Output: "tests passed", ExitCode: 0}
		},
	})
	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := atomic.LoadInt32(&verifyRuns); n != 1 {
		t.Fatalf("verify runs = %d, want exactly 1 (only after the edit)", n)
	}
	if !strings.Contains(res.Steps[0].Observation, "[verify_on_edit]") {
		t.Errorf("edit observation should carry the verify note: %q", res.Steps[0].Observation)
	}
	if strings.Contains(res.Steps[1].Observation, "[verify_on_edit]") {
		t.Errorf("read-only action must not trigger verification: %q", res.Steps[1].Observation)
	}
}

// TestRun_BatchPreviewStaysRaw pins the UI-preview contract for batch
// actions with untrusted tools: the MODEL observation wraps each sub-result
// in untrusted-content tags, the ToolResult event preview stays raw.
func TestRun_BatchPreviewStaysRaw(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "batch", `{"calls":[{"tool":"fetch_doc","input":{"url":"https://x"}}]}`),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	reg := &mockRegistry{
		descriptors: []sdktools.ToolDescriptor{{Name: "fetch_doc"}},
		untrusted:   map[string]bool{"fetch_doc": true},
	}
	em := &recordingEmitter{}
	loop := New(caller, reg, em, Config{Model: "m", Task: "t", Logger: slogDiscard()})
	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(em.toolPreviews[0], "<"+"untrusted-content") {
		t.Errorf("batch UI preview leaked boundary tags: %q", em.toolPreviews[0])
	}
	if !strings.Contains(res.Steps[0].Observation, "<"+"untrusted-content") {
		t.Errorf("model observation must wrap untrusted sub-results: %q", res.Steps[0].Observation)
	}
}

// TestRun_ToolResultPreviewNotCapped pins the ToolResult event contract for
// long observations: the preview must carry the FULL raw (pre-wrap) result —
// the same value the Conductor passes (executor_run) — so the frontend's
// result_len badge ("N.NK chars") and the preview body agree. The former
// 200-rune cap made the card self-contradictory (a 12.3K badge above a
// 200-char body) and left the tail unreachable ("Show more" revealed nothing).
func TestRun_ToolResultPreviewNotCapped(t *testing.T) {
	long := strings.Repeat("x", 600) + " tail marker"
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "read_file", `{"path":"big.txt"}`),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	reg := &mockRegistry{results: map[string]string{"read_file": long}}
	em := &recordingEmitter{}
	loop := New(caller, reg, em, Config{Model: "m", Task: "t", Logger: slogDiscard()})
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(em.toolPreviews) != 1 {
		t.Fatalf("toolPreviews = %d, want 1", len(em.toolPreviews))
	}
	if em.toolPreviews[0] != long {
		t.Errorf("preview length = %d, want %d (the full raw observation, uncapped — the badge describes bytes the card must actually show)",
			len(em.toolPreviews[0]), len(long))
	}
	if len(em.toolResultLen) != 1 || em.toolResultLen[0] != len(long) {
		t.Errorf("result_len = %v, want [%d] (agreeing with the preview)", em.toolResultLen, len(long))
	}
}
