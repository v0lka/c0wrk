package core

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/orchestration"
)

// spyEmitter records all method calls for assertion.
type spyEmitter struct {
	calls []spyCall
}

type spyCall struct {
	method string
	args   []any
}

func (s *spyEmitter) record(method string, args ...any) {
	s.calls = append(s.calls, spyCall{method: method, args: args})
}

func (s *spyEmitter) StepStart(n int)                      { s.record("StepStart", n) }
func (s *spyEmitter) Thought(n int, c, r string)           { s.record("Thought", n, c, r) }
func (s *spyEmitter) ToolCall(n, ci int, t, a, src string) { s.record("ToolCall", n, ci, t, a, src) }
func (s *spyEmitter) ToolResult(n, ci, l int, p string, e bool) {
	s.record("ToolResult", n, ci, l, p, e)
}
func (s *spyEmitter) StepComplete(n int, d time.Duration) { s.record("StepComplete", n, d) }
func (s *spyEmitter) SubAgentLaunch(id, desc string)      { s.record("SubAgentLaunch", id, desc) }
func (s *spyEmitter) SubAgentComplete(id string, ok bool, d time.Duration) {
	s.record("SubAgentComplete", id, ok, d)
}
func (s *spyEmitter) AssistantChunk(c string)             { s.record("AssistantChunk", c) }
func (s *spyEmitter) AssistantDone(c string, in, out int) { s.record("AssistantDone", c, in, out) }
func (s *spyEmitter) ContextFill(p float64, u, m int, st, id string) {
	s.record("ContextFill", p, u, m, st, id)
}
func (s *spyEmitter) ContextCompaction(before, after float64, id string) {
	s.record("ContextCompaction", before, after, id)
}
func (s *spyEmitter) Finishing(n int, summary string) { s.record("Finishing", n, summary) }
func (s *spyEmitter) ExecutorDiagnostic(n int, e string, d map[string]any) {
	s.record("ExecutorDiagnostic", n, e, d)
}
func (s *spyEmitter) Routing(m, d, c string) { s.record("Routing", m, d, c) }
func (s *spyEmitter) PlanGenerated(n int, steps []orchestration.PlanStepEvent) {
	s.record("PlanGenerated", n, steps)
}
func (s *spyEmitter) PlanStepStart(id, desc, summary string) {
	s.record("PlanStepStart", id, desc, summary)
}
func (s *spyEmitter) PlanStepComplete(id string, ok bool, d time.Duration, errMsg string) {
	s.record("PlanStepComplete", id, ok, d, errMsg)
}
func (s *spyEmitter) Reflection(r *orchestration.Reflection, a, m int) {
	s.record("Reflection", r, a, m)
}
func (s *spyEmitter) Retry(a, m int)                             { s.record("Retry", a, m) }
func (s *spyEmitter) StepRetry(id string, a, m int)              { s.record("StepRetry", id, a, m) }
func (s *spyEmitter) Service(c string)                           { s.record("Service", c) }
func (s *spyEmitter) ServiceWithMeta(c string, m map[string]any) { s.record("ServiceWithMeta", c, m) }
func (s *spyEmitter) GoalStatus(m map[string]any)                { s.record("GoalStatus", m) }
func (s *spyEmitter) GoalProgress(m map[string]any)              { s.record("GoalProgress", m) }
func (s *spyEmitter) ReplanFailed(e error)                       { s.record("ReplanFailed", e) }
func (s *spyEmitter) SkillsActivated(skills []string)            { s.record("SkillsActivated", skills) }
func (s *spyEmitter) EmitSessionTokens(totalIn, totalOut int, model, family string) {
	s.record("EmitSessionTokens", totalIn, totalOut, model, family)
}
func (s *spyEmitter) StepTodoUpdate(stepID string, items []agent.TodoItem) {
	s.record("StepTodoUpdate", stepID, items)
}
func (s *spyEmitter) MemoryRead(stepNum int, content string) {
	s.record("MemoryRead", stepNum, content)
}

// SetDisplayContextWindow records the call so tests can assert the orchestrator
// injected the model's advertised context window.
func (s *spyEmitter) SetDisplayContextWindow(window int) {
	s.record("SetDisplayContextWindow", window)
}

// SetLastModel records the call so tests can assert the orchestrator
// synchronized the emitter's cached model on a per-request override.
func (s *spyEmitter) SetLastModel(model, family string) {
	s.record("SetLastModel", model, family)
}

// scopableSpyEmitter extends spyEmitter with scoping support.
type scopableSpyEmitter struct {
	spyEmitter
}

func (s *scopableSpyEmitter) WithPlanStepID(id string) Emitter {
	return &scopableSpyEmitter{spyEmitter: spyEmitter{}}
}

func (s *scopableSpyEmitter) SetCurrentStepID(id string) {
	s.record("SetCurrentStepID", id)
}

var _ Emitter = (*spyEmitter)(nil)
var _ Emitter = (*scopableSpyEmitter)(nil)
var _ PlanStepScopable = (*scopableSpyEmitter)(nil)
var _ CurrentStepScopable = (*scopableSpyEmitter)(nil)
var _ DisplayContextWindowSetter = (*spyEmitter)(nil)
var _ LastModelSetter = (*spyEmitter)(nil)

func TestNewLoggingEmitter_NilLogger_ReturnsInner(t *testing.T) {
	inner := &spyEmitter{}
	got := NewLoggingEmitter(inner, nil)
	if got != inner {
		t.Fatal("expected NewLoggingEmitter with nil logger to return inner unchanged")
	}
}

func TestLoggingEmitter_DelegatesToInner(t *testing.T) {
	dur := 42 * time.Millisecond
	meta := map[string]any{"k": "v"}
	details := map[string]any{"d": 1}
	steps := []orchestration.PlanStepEvent{{ID: "s1"}}

	tests := []struct {
		name   string
		invoke func(e Emitter)
		want   string
	}{
		{"StepStart", func(e Emitter) { e.StepStart(1) }, "StepStart"},
		{"Thought", func(e Emitter) { e.Thought(1, "c", "r") }, "Thought"},
		{"ToolCall", func(e Emitter) { e.ToolCall(1, 0, "bash", "ls", "core") }, "ToolCall"},
		{"ToolResult", func(e Emitter) { e.ToolResult(1, 0, 100, "ok", false) }, "ToolResult"},
		{"StepComplete", func(e Emitter) { e.StepComplete(1, dur) }, "StepComplete"},
		{"SubAgentLaunch", func(e Emitter) { e.SubAgentLaunch("s1", "desc") }, "SubAgentLaunch"},
		{"SubAgentComplete", func(e Emitter) { e.SubAgentComplete("s1", true, dur) }, "SubAgentComplete"},
		{"AssistantChunk", func(e Emitter) { e.AssistantChunk("hi") }, "AssistantChunk"},
		{"AssistantDone", func(e Emitter) { e.AssistantDone("hi", 10, 20) }, "AssistantDone"},
		{"ContextFill", func(e Emitter) { e.ContextFill(0.5, 500, 1000, "ok", "s1") }, "ContextFill"},
		{"ContextCompaction", func(e Emitter) { e.ContextCompaction(85.0, 30.0, "s1") }, "ContextCompaction"},
		{"ExecutorDiagnostic", func(e Emitter) { e.ExecutorDiagnostic(1, "nudge", details) }, "ExecutorDiagnostic"},
		{"Finishing", func(e Emitter) { e.Finishing(1, "done") }, "Finishing"},
		{"Routing", func(e Emitter) { e.Routing("plan", "code", "3") }, "Routing"},
		{"PlanGenerated", func(e Emitter) { e.PlanGenerated(1, steps) }, "PlanGenerated"},
		{"PlanStepStart", func(e Emitter) { e.PlanStepStart("s1", "do it", "summary") }, "PlanStepStart"},
		{"PlanStepComplete", func(e Emitter) { e.PlanStepComplete("s1", true, dur, "") }, "PlanStepComplete"},
		{"Reflection", func(e Emitter) { e.Reflection(&orchestration.Reflection{Summary: "sum"}, 1, 3) }, "Reflection"},
		{"Retry", func(e Emitter) { e.Retry(1, 3) }, "Retry"},
		{"StepRetry", func(e Emitter) { e.StepRetry("s1", 1, 3) }, "StepRetry"},
		{"Service", func(e Emitter) { e.Service("msg") }, "Service"},
		{"ServiceWithMeta", func(e Emitter) { e.ServiceWithMeta("msg", meta) }, "ServiceWithMeta"},
		{"ReplanFailed", func(e Emitter) { e.ReplanFailed(nil) }, "ReplanFailed"},
		{"SkillsActivated", func(e Emitter) { e.SkillsActivated([]string{"pdf"}) }, "SkillsActivated"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &spyEmitter{}
			logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
			le := NewLoggingEmitter(spy, logger)

			tt.invoke(le)

			if len(spy.calls) != 1 {
				t.Fatalf("expected 1 call to inner, got %d", len(spy.calls))
			}
			if spy.calls[0].method != tt.want {
				t.Errorf("expected method %q, got %q", tt.want, spy.calls[0].method)
			}
		})
	}
}

func TestLoggingEmitter_EmitSessionTokens_ForwardsToInner(t *testing.T) {
	spy := &spyEmitter{}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	le := NewLoggingEmitter(spy, logger)

	// The type assertion used in builder.go must succeed on the logging wrapper.
	type tokenEmitter interface {
		EmitSessionTokens(totalIn, totalOut int, model, family string)
	}
	te, ok := le.(tokenEmitter)
	if !ok {
		t.Fatal("expected loggingEmitter to satisfy EmitSessionTokens interface")
	}
	te.EmitSessionTokens(100, 50, "gpt-4o", "openai")

	if len(spy.calls) != 1 {
		t.Fatalf("expected 1 call to inner, got %d", len(spy.calls))
	}
	if spy.calls[0].method != "EmitSessionTokens" {
		t.Errorf("expected method EmitSessionTokens, got %q", spy.calls[0].method)
	}
}

func TestLoggingEmitter_WithPlanStepID_ScopesInner(t *testing.T) {
	inner := &scopableSpyEmitter{}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	le := NewLoggingEmitter(inner, logger)

	scoped, ok := le.(PlanStepScopable)
	if !ok {
		t.Fatal("expected loggingEmitter to implement PlanStepScopable")
	}
	scopedEmitter := scoped.WithPlanStepID("step_42")

	// Verify scoped emitter is a loggingEmitter wrapping the scoped inner
	scopedLE, ok := scopedEmitter.(*loggingEmitter)
	if !ok {
		t.Fatal("expected scoped emitter to be *loggingEmitter")
	}
	if _, ok := scopedLE.inner.(*scopableSpyEmitter); !ok {
		t.Fatal("expected inner to be scoped spy emitter")
	}

	// Verify the logger has planStepID attribute
	scopedEmitter.Routing("plan", "code", "3")
	logged := buf.String()
	if !bytes.Contains([]byte(logged), []byte("planStepID=step_42")) {
		t.Errorf("expected planStepID in log output; got: %s", logged)
	}
}

func TestLoggingEmitter_SetCurrentStepID_DelegatesToInner(t *testing.T) {
	inner := &scopableSpyEmitter{}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	le := NewLoggingEmitter(inner, logger)

	scoper, ok := le.(CurrentStepScopable)
	if !ok {
		t.Fatal("expected loggingEmitter to implement CurrentStepScopable")
	}
	scoper.SetCurrentStepID("step_inline")

	if len(inner.calls) != 1 {
		t.Fatalf("expected 1 call to inner, got %d", len(inner.calls))
	}
	if inner.calls[0].method != "SetCurrentStepID" {
		t.Errorf("expected method SetCurrentStepID, got %q", inner.calls[0].method)
	}
}

func TestLoggingEmitter_SetCurrentStepID_NoopWhenInnerNotScopable(t *testing.T) {
	// spyEmitter does NOT implement CurrentStepScopable — the call must be
	// a graceful no-op (no panic, no call recorded).
	inner := &spyEmitter{}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	le := NewLoggingEmitter(inner, logger)

	scoper, ok := le.(CurrentStepScopable)
	if !ok {
		t.Fatal("expected loggingEmitter to implement CurrentStepScopable")
	}
	scoper.SetCurrentStepID("step_inline") // must not panic

	if len(inner.calls) != 0 {
		t.Errorf("expected 0 calls to non-scopable inner, got %d", len(inner.calls))
	}
}
