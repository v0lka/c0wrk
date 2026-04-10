package core

import (
	"bytes"
	"log/slog"
	"testing"
	"time"
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

func (s *spyEmitter) StepStart(n int)                            { s.record("StepStart", n) }
func (s *spyEmitter) Thought(n int, c, r string)                 { s.record("Thought", n, c, r) }
func (s *spyEmitter) ToolCall(n int, t, a string)                { s.record("ToolCall", n, t, a) }
func (s *spyEmitter) ToolResult(n, l int, p string)              { s.record("ToolResult", n, l, p) }
func (s *spyEmitter) StepComplete(n int, d time.Duration)        { s.record("StepComplete", n, d) }
func (s *spyEmitter) SubAgentLaunch(id, desc string)             { s.record("SubAgentLaunch", id, desc) }
func (s *spyEmitter) SubAgentComplete(id string, ok bool, d time.Duration) {
	s.record("SubAgentComplete", id, ok, d)
}
func (s *spyEmitter) AssistantChunk(c string)                   { s.record("AssistantChunk", c) }
func (s *spyEmitter) AssistantDone(c string, in, out int)       { s.record("AssistantDone", c, in, out) }
func (s *spyEmitter) TokensUsed(in, out int)                    { s.record("TokensUsed", in, out) }
func (s *spyEmitter) ContextFill(p float64, u, m int, st, id string) {
	s.record("ContextFill", p, u, m, st, id)
}
func (s *spyEmitter) ExecutorDiagnostic(n int, e string, d map[string]any) {
	s.record("ExecutorDiagnostic", n, e, d)
}
func (s *spyEmitter) Routing(m, d, c string)                            { s.record("Routing", m, d, c) }
func (s *spyEmitter) PlanGenerated(n int, steps []PlanStepEvent)        { s.record("PlanGenerated", n, steps) }
func (s *spyEmitter) PlanStepStart(id, desc string)                     { s.record("PlanStepStart", id, desc) }
func (s *spyEmitter) PlanStepComplete(id string, ok bool, d time.Duration) {
	s.record("PlanStepComplete", id, ok, d)
}
func (s *spyEmitter) Evaluation(p, t int, c []EvalCriterionEvent)      { s.record("Evaluation", p, t, c) }
func (s *spyEmitter) Reflection(sum string, ins []string, a, m int)    { s.record("Reflection", sum, ins, a, m) }
func (s *spyEmitter) Retry(a, m int)                                   { s.record("Retry", a, m) }
func (s *spyEmitter) StepRetry(id string, a, m int)                    { s.record("StepRetry", id, a, m) }
func (s *spyEmitter) ACExtracted(n int, c []EvalCriterionEvent)        { s.record("ACExtracted", n, c) }
func (s *spyEmitter) Service(c string)                                 { s.record("Service", c) }
func (s *spyEmitter) ServiceWithMeta(c string, m map[string]any)       { s.record("ServiceWithMeta", c, m) }
func (s *spyEmitter) EvaluationError(e error)                          { s.record("EvaluationError", e) }
func (s *spyEmitter) ReplanFailed(e error)                             { s.record("ReplanFailed", e) }
func (s *spyEmitter) FileRollbackError(id string, e error)             { s.record("FileRollbackError", id, e) }
func (s *spyEmitter) EvalStepStart(id, desc string)                    { s.record("EvalStepStart", id, desc) }
func (s *spyEmitter) EvalStepComplete(id string, ok bool, d time.Duration) {
	s.record("EvalStepComplete", id, ok, d)
}

// scopableSpyEmitter extends spyEmitter with scoping support.
type scopableSpyEmitter struct {
	spyEmitter
}

func (s *scopableSpyEmitter) WithPlanStepID(id string) Emitter {
	return &scopableSpyEmitter{spyEmitter: spyEmitter{}}
}

func (s *scopableSpyEmitter) WithCriterionID(id string) Emitter {
	return &scopableSpyEmitter{spyEmitter: spyEmitter{}}
}

var _ Emitter = (*spyEmitter)(nil)
var _ Emitter = (*scopableSpyEmitter)(nil)
var _ PlanStepScopable = (*scopableSpyEmitter)(nil)
var _ CriterionScopable = (*scopableSpyEmitter)(nil)

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
	steps := []PlanStepEvent{{ID: "s1"}}
	criteria := []EvalCriterionEvent{{Name: "c1"}}

	tests := []struct {
		name   string
		invoke func(e Emitter)
		want   string
	}{
		{"StepStart", func(e Emitter) { e.StepStart(1) }, "StepStart"},
		{"Thought", func(e Emitter) { e.Thought(1, "c", "r") }, "Thought"},
		{"ToolCall", func(e Emitter) { e.ToolCall(1, "bash", "ls") }, "ToolCall"},
		{"ToolResult", func(e Emitter) { e.ToolResult(1, 100, "ok") }, "ToolResult"},
		{"StepComplete", func(e Emitter) { e.StepComplete(1, dur) }, "StepComplete"},
		{"SubAgentLaunch", func(e Emitter) { e.SubAgentLaunch("s1", "desc") }, "SubAgentLaunch"},
		{"SubAgentComplete", func(e Emitter) { e.SubAgentComplete("s1", true, dur) }, "SubAgentComplete"},
		{"AssistantChunk", func(e Emitter) { e.AssistantChunk("hi") }, "AssistantChunk"},
		{"AssistantDone", func(e Emitter) { e.AssistantDone("hi", 10, 20) }, "AssistantDone"},
		{"TokensUsed", func(e Emitter) { e.TokensUsed(10, 20) }, "TokensUsed"},
		{"ContextFill", func(e Emitter) { e.ContextFill(0.5, 500, 1000, "ok", "s1") }, "ContextFill"},
		{"ExecutorDiagnostic", func(e Emitter) { e.ExecutorDiagnostic(1, "nudge", details) }, "ExecutorDiagnostic"},
		{"Routing", func(e Emitter) { e.Routing("plan", "code", "3") }, "Routing"},
		{"PlanGenerated", func(e Emitter) { e.PlanGenerated(1, steps) }, "PlanGenerated"},
		{"PlanStepStart", func(e Emitter) { e.PlanStepStart("s1", "do it") }, "PlanStepStart"},
		{"PlanStepComplete", func(e Emitter) { e.PlanStepComplete("s1", true, dur) }, "PlanStepComplete"},
		{"Evaluation", func(e Emitter) { e.Evaluation(2, 3, criteria) }, "Evaluation"},
		{"Reflection", func(e Emitter) { e.Reflection("sum", []string{"a"}, 1, 3) }, "Reflection"},
		{"Retry", func(e Emitter) { e.Retry(1, 3) }, "Retry"},
		{"StepRetry", func(e Emitter) { e.StepRetry("s1", 1, 3) }, "StepRetry"},
		{"ACExtracted", func(e Emitter) { e.ACExtracted(2, criteria) }, "ACExtracted"},
		{"Service", func(e Emitter) { e.Service("msg") }, "Service"},
		{"ServiceWithMeta", func(e Emitter) { e.ServiceWithMeta("msg", meta) }, "ServiceWithMeta"},
		{"EvaluationError", func(e Emitter) { e.EvaluationError(nil) }, "EvaluationError"},
		{"ReplanFailed", func(e Emitter) { e.ReplanFailed(nil) }, "ReplanFailed"},
		{"FileRollbackError", func(e Emitter) { e.FileRollbackError("s1", nil) }, "FileRollbackError"},
		{"EvalStepStart", func(e Emitter) { e.EvalStepStart("c1", "check") }, "EvalStepStart"},
		{"EvalStepComplete", func(e Emitter) { e.EvalStepComplete("c1", true, dur) }, "EvalStepComplete"},
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

func TestLoggingEmitter_WithCriterionID_ScopesInner(t *testing.T) {
	inner := &scopableSpyEmitter{}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	le := NewLoggingEmitter(inner, logger)

	scoped, ok := le.(CriterionScopable)
	if !ok {
		t.Fatal("expected loggingEmitter to implement CriterionScopable")
	}
	scopedEmitter := scoped.WithCriterionID("crit_7")

	scopedLE, ok := scopedEmitter.(*loggingEmitter)
	if !ok {
		t.Fatal("expected scoped emitter to be *loggingEmitter")
	}
	if _, ok := scopedLE.inner.(*scopableSpyEmitter); !ok {
		t.Fatal("expected inner to be scoped spy emitter")
	}

	// Verify the logger has criterionID attribute
	scopedEmitter.Routing("plan", "code", "3")
	logged := buf.String()
	if !bytes.Contains([]byte(logged), []byte("criterionID=crit_7")) {
		t.Errorf("expected criterionID in log output; got: %s", logged)
	}
}
