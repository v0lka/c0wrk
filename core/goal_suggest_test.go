package core

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
)

// goalSuggestStubCaller is a scripted oneshot.Caller: it records the request
// and returns a canned response (or error) for every call.
type goalSuggestStubCaller struct {
	resp *llm.ChatResponse
	err  error

	gotRequests []llm.ChatRequest
}

func (c *goalSuggestStubCaller) Call(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	c.gotRequests = append(c.gotRequests, req)
	if c.err != nil {
		return nil, c.err
	}
	return c.resp, nil
}

func goalSuggestJSON(body string) *llm.ChatResponse {
	return &llm.ChatResponse{Message: llm.Message{Role: "assistant", Content: body}}
}

// TestParseGoalSuggestion covers the verdict parsing contract: valid JSON in
// both verdicts, reason trimming, the reason reset on suggest=false, the
// explicit_criteria pass-through (a missing field decodes to false so the
// gate fail-closes), and the fail-closed zero value for a model that answers
// garbage around the JSON.
func TestParseGoalSuggestion(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantOK       bool
		wantSugg     bool
		wantExplicit bool
		wantReason   string
	}{
		{
			name:         "suggest true with explicit criteria and reason",
			body:         `{"suggest": true, "explicit_criteria": true, "reason": "Запрос описывает задачу с проверяемым критерием"}`,
			wantOK:       true,
			wantSugg:     true,
			wantExplicit: true,
			wantReason:   "Запрос описывает задачу с проверяемым критерием",
		},
		{
			name:       "suggest false resets reason",
			body:       `{"suggest": false, "reason": "should be dropped"}`,
			wantOK:     true,
			wantSugg:   false,
			wantReason: "",
		},
		{
			name:         "suggest true but explicit_criteria false parses raw (the gate drops it)",
			body:         `{"suggest": true, "explicit_criteria": false, "reason": "implicit goal"}`,
			wantOK:       true,
			wantSugg:     true,
			wantExplicit: false,
			wantReason:   "implicit goal",
		},
		{
			name:         "suggest true without explicit_criteria fails closed to not-explicit",
			body:         `{"suggest": true, "reason": "multi-step objective"}`,
			wantOK:       true,
			wantSugg:     true,
			wantExplicit: false,
			wantReason:   "multi-step objective",
		},
		{
			name:         "suggest true reason is trimmed",
			body:         `{"suggest":true,"explicit_criteria":true,"reason":"  padded  "}`,
			wantOK:       true,
			wantSugg:     true,
			wantExplicit: true,
			wantReason:   "padded",
		},
		{
			name:     "garbage around json still parses (zero value is no-suggestion)",
			body:     "The answer is {\"suggest\": false, \"reason\": \"\"} thanks",
			wantOK:   true,
			wantSugg: false,
		},
		{
			name:   "no json at all is a parse error",
			body:   "I cannot answer that",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGoalSuggestion(goalSuggestJSON(tt.body))
			if tt.wantOK {
				if err != nil {
					t.Fatalf("parseGoalSuggestion() error = %v, want nil", err)
				}
				if got.Suggest != tt.wantSugg {
					t.Errorf("Suggest = %v, want %v", got.Suggest, tt.wantSugg)
				}
				if got.ExplicitCriteria != tt.wantExplicit {
					t.Errorf("ExplicitCriteria = %v, want %v", got.ExplicitCriteria, tt.wantExplicit)
				}
				if got.Reason != tt.wantReason {
					t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
				}
				return
			}
			if err == nil {
				t.Fatalf("parseGoalSuggestion() error = nil, want parse failure")
			}
		})
	}
}

// TestSuggestGoalModeWithCaller verifies the request shape (prompt, user
// message, advisory routing line, service policy sampling) and the verdict
// pass-through of the production classifier pass.
func TestSuggestGoalModeWithCaller(t *testing.T) {
	caller := &goalSuggestStubCaller{
		resp: goalSuggestJSON(`{"suggest": true, "explicit_criteria": true, "reason": "multi-step objective"}`),
	}
	routing := &router.RoutingDecision{Domain: router.DomainCode, Complexity: 4}

	got, err := suggestGoalModeWithCaller(
		context.Background(), caller, newServiceMetrics(), slog.New(slog.DiscardHandler), "test-model", nil,
		"fix the failing tests in package foo and make them pass",
		routing,
	)
	if err != nil {
		t.Fatalf("suggestGoalModeWithCaller() error = %v", err)
	}
	if !got.Suggest || !got.ExplicitCriteria || got.Reason != "multi-step objective" {
		t.Errorf("verdict = %+v, want suggest=true explicit_criteria=true reason=multi-step objective", got)
	}
	if len(caller.gotRequests) != 1 {
		t.Fatalf("got %d LLM calls, want 1", len(caller.gotRequests))
	}
	req := caller.gotRequests[0]
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v, want [system user]", req.Messages)
	}
	if !strings.Contains(req.Messages[0].Content, "OBVIOUSLY better suited to goal mode") {
		t.Errorf("system prompt does not carry the goal_suggest prompt")
	}
	if !strings.Contains(req.Messages[1].Content, "fix the failing tests") {
		t.Errorf("user message does not carry the request")
	}
	if !strings.Contains(req.Messages[1].Content, "domain=code") ||
		!strings.Contains(req.Messages[1].Content, "complexity=4/5") {
		t.Errorf("user message does not carry the advisory routing context: %q", req.Messages[1].Content)
	}
	if req.CallPurpose != llm.CallPurposeRouting {
		t.Errorf("CallPurpose = %q, want routing", req.CallPurpose)
	}
	if req.Temperature == nil || *req.Temperature != oneshotTempGoalSuggest {
		t.Errorf("Temperature = %v, want the policy-pinned goal-suggest value", req.Temperature)
	}
}

// TestSuggestGoalModeWithCaller_RoutingNil verifies the advisory line degrades
// gracefully when routing is unavailable.
func TestSuggestGoalModeWithCaller_RoutingNil(t *testing.T) {
	caller := &goalSuggestStubCaller{
		resp: goalSuggestJSON(`{"suggest": false, "reason": ""}`),
	}
	if _, err := suggestGoalModeWithCaller(
		context.Background(), caller, newServiceMetrics(), slog.New(slog.DiscardHandler), "m", nil, "hello", nil,
	); err != nil {
		t.Fatalf("suggestGoalModeWithCaller() error = %v", err)
	}
	if !strings.Contains(caller.gotRequests[0].Messages[1].Content, "unavailable") {
		t.Errorf("nil routing must render the unavailable advisory line")
	}
}

// TestOrchestratorSuggestGoalMode_SeamAndFailOpen verifies the orchestrator
// seam: the stub wins over the production path, and a nil caller fails open
// with an error the gate converts to no-suggestion.
func TestOrchestratorSuggestGoalMode_SeamAndFailOpen(t *testing.T) {
	o := &Orchestrator{}

	if _, err := o.SuggestGoalMode(context.Background(), "msg", nil); !errors.Is(err, errGoalSuggestNoCaller) {
		t.Fatalf("nil caller: error = %v, want errGoalSuggestNoCaller", err)
	}

	want := GoalSuggestion{Suggest: true, Reason: "stubbed"}
	o.goalSuggester = func(context.Context, string, *router.RoutingDecision) (GoalSuggestion, error) {
		return want, nil
	}
	got, err := o.SuggestGoalMode(context.Background(), "msg", nil)
	if err != nil || got != want {
		t.Fatalf("stubbed SuggestGoalMode = (%+v, %v), want (%+v, nil)", got, err, want)
	}
}
