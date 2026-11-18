package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/sp4rk/llm"
)

// promptSuggestStubCaller is a scripted oneshot.Caller: it records the
// requests and returns a canned response (or error) for every call.
type promptSuggestStubCaller struct {
	resp *llm.ChatResponse
	err  error

	gotRequests []llm.ChatRequest
}

func (c *promptSuggestStubCaller) Call(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	c.gotRequests = append(c.gotRequests, req)
	if c.err != nil {
		return nil, c.err
	}
	return c.resp, nil
}

func promptSuggestResp(body string) *llm.ChatResponse {
	return &llm.ChatResponse{Message: llm.Message{Role: "assistant", Content: body}}
}

// TestParsePromptSuggestions covers the fail-safe parsing contract: clean
// lines, the per-pass cap, decoration stripping (fences, preambles, list
// markers, ordinals, quotes), duplicate dropping, preamble/header-line
// skipping, and the parse error when nothing parseable survives.
func TestParsePromptSuggestions(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "clean three lines",
			body: "Fix the flaky auth test\nAdd table-driven cases to the parser tests\nDocument the retry policy in README",
			want: []string{
				"Fix the flaky auth test",
				"Add table-driven cases to the parser tests",
				"Document the retry policy in README",
			},
		},
		{
			name: "more than three lines caps at three",
			body: "one\ntwo\nthree\nfour\nfive",
			want: []string{"one", "two", "three"},
		},
		{
			name: "numbering and bullets stripped",
			body: "1. Fix the flaky auth test\n2) Add retry tests\n- Document the policy\n* Cover the edge case",
			want: []string{"Fix the flaky auth test", "Add retry tests", "Document the policy"},
		},
		{
			name: "quotes and emphasis stripped",
			body: "\"Fix the flaky auth test\"\n**Document the policy**\n`go test ./core`",
			want: []string{"Fix the flaky auth test", "Document the policy", "go test ./core"},
		},
		{
			name: "fenced body recovered",
			body: "```\nFix the flaky auth test\nAdd retry tests\n```",
			want: []string{"Fix the flaky auth test", "Add retry tests"},
		},
		{
			name: "preamble line skipped",
			body: "Here are three suggestions:\nFix the flaky auth test\nAdd retry tests",
			want: []string{"Fix the flaky auth test", "Add retry tests"},
		},
		{
			name: "conversational preamble stripped from the first line",
			body: "Sure, fix the flaky auth test\nAdd retry tests",
			want: []string{"fix the flaky auth test", "Add retry tests"},
		},
		{
			name: "empty lines skipped",
			body: "\nFix the flaky auth test\n\n\nAdd retry tests\n",
			want: []string{"Fix the flaky auth test", "Add retry tests"},
		},
		{
			name: "exact duplicates dropped",
			body: "Fix the flaky auth test\nFix the flaky auth test\nAdd retry tests",
			want: []string{"Fix the flaky auth test", "Add retry tests"},
		},
		{
			name: "over-long line kept whole",
			body: strings.Repeat("a", 300),
			want: []string{strings.Repeat("a", 300)},
		},
		{
			name: "multibyte line kept whole",
			body: strings.Repeat("ж", 300),
			want: []string{strings.Repeat("ж", 300)},
		},
		{
			name: "empty response is a parse error",
			body: "",
		},
		{
			name: "whitespace-only response is a parse error",
			body: "  \n\t\n ",
		},
		{
			name: "fence-only response is a parse error",
			body: "```\n```\n```",
		},
		{
			name: "decoration-only lines are a parse error",
			body: "1. \n- \n\"  \"\n**",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePromptSuggestions(promptSuggestResp(tt.body))
			if len(tt.want) == 0 {
				if err == nil {
					t.Fatalf("parsePromptSuggestions(%q) error = nil, want %v", tt.body, errPromptSuggestEmptyResponse)
				}
				if !errors.Is(err, errPromptSuggestEmptyResponse) {
					t.Fatalf("parsePromptSuggestions(%q) error = %v, want %v", tt.body, err, errPromptSuggestEmptyResponse)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePromptSuggestions(%q) error = %v, want nil", tt.body, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parsePromptSuggestions(%q) = %q, want %q", tt.body, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parsePromptSuggestions(%q)[%d] = %q, want %q", tt.body, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestPromptSuggestPairs covers the prompt→final-answer pairing contract:
// the final assistant message before the next user message wins, is_nudge
// user messages neither open nor close a pair (D5), and incomplete pairs are
// discarded.
func TestPromptSuggestPairs(t *testing.T) {
	tests := []struct {
		name string
		msgs []PromptSuggestMessage
		want []promptSuggestPair
	}{
		{
			name: "single pair",
			msgs: []PromptSuggestMessage{
				{Role: "user", Content: "add retries to the client"},
				{Role: "assistant", Content: "done, retries added"},
			},
			want: []promptSuggestPair{{prompt: "add retries to the client", answer: "done, retries added"}},
		},
		{
			name: "final assistant message wins",
			msgs: []PromptSuggestMessage{
				{Role: "user", Content: "add retries"},
				{Role: "assistant", Content: "working on it"},
				{Role: "assistant", Content: "done"},
			},
			want: []promptSuggestPair{{prompt: "add retries", answer: "done"}},
		},
		{
			name: "nudge does not close the pair — continuation joins the answer",
			msgs: []PromptSuggestMessage{
				{Role: "user", Content: "add retries"},
				{Role: "assistant", Content: "first part"},
				{Role: "user", Content: "continue", IsNudge: true},
				{Role: "assistant", Content: "first part and second part"},
			},
			want: []promptSuggestPair{{prompt: "add retries", answer: "first part and second part"}},
		},
		{
			name: "nudge before any pair is ignored",
			msgs: []PromptSuggestMessage{
				{Role: "user", Content: "продолжай", IsNudge: true},
				{Role: "assistant", Content: "orphan answer"},
				{Role: "user", Content: "add retries"},
				{Role: "assistant", Content: "done"},
			},
			want: []promptSuggestPair{{prompt: "add retries", answer: "done"}},
		},
		{
			name: "pair without an answer is dropped",
			msgs: []PromptSuggestMessage{
				{Role: "user", Content: "abandoned question"},
				{Role: "user", Content: "add retries"},
				{Role: "assistant", Content: "done"},
			},
			want: []promptSuggestPair{{prompt: "add retries", answer: "done"}},
		},
		{
			name: "empty and non-conversational roles ignored",
			msgs: []PromptSuggestMessage{
				{Role: "system", Content: "system noise"},
				{Role: "user", Content: "   "},
				{Role: "assistant", Content: "orphan"},
				{Role: "user", Content: "add retries"},
				{Role: "assistant", Content: "done"},
			},
			want: []promptSuggestPair{{prompt: "add retries", answer: "done"}},
		},
		{
			name: "message content capped to the per-message budget",
			msgs: []PromptSuggestMessage{
				{Role: "user", Content: strings.Repeat("в", promptSuggestMaxMessageChars+50)},
				{Role: "assistant", Content: strings.Repeat("b", promptSuggestMaxMessageChars+50)},
			},
			want: []promptSuggestPair{
				{
					prompt: strings.Repeat("в", promptSuggestMaxMessageChars),
					answer: strings.Repeat("b", promptSuggestMaxMessageChars),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := promptSuggestPairs(tt.msgs)
			if len(got) != len(tt.want) {
				t.Fatalf("promptSuggestPairs() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("promptSuggestPairs()[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestPromptSuggestTrimPairsLatestFirst covers the latest-priority trim: the
// most recent pairs win the budget, order stays chronological, the single
// most recent pair is never trimmed away, and degenerate inputs yield nil.
func TestPromptSuggestTrimPairsLatestFirst(t *testing.T) {
	pair := func(p, a string) promptSuggestPair { return promptSuggestPair{prompt: p, answer: a} }
	pairs := []promptSuggestPair{
		pair("much older and by far the longest prompt in this transcript", "a1"),
		pair("old2", "a2"),
		pair("new3", "a3"),
		pair("new4", "a4"),
	}

	tests := []struct {
		name   string
		pairs  []promptSuggestPair
		budget int
		want   []promptSuggestPair
	}{
		{
			name:   "everything fits",
			pairs:  pairs,
			budget: 1 << 20,
			want:   pairs,
		},
		{
			name:   "latest pairs win the budget",
			pairs:  pairs,
			budget: promptSuggestPairCost(pairs[3]) + promptSuggestPairCost(pairs[2]),
			want:   []promptSuggestPair{pairs[2], pairs[3]},
		},
		{
			name:  "older pairs never resurrect past a non-fitting newer one",
			pairs: pairs,
			// The second-latest pair does not fit (one byte short), while the
			// smaller older pairs would — they must stay excluded.
			budget: promptSuggestPairCost(pairs[3]) + promptSuggestPairCost(pairs[2]) - 1,
			want:   []promptSuggestPair{pairs[3]},
		},
		{
			name:   "the most recent pair is kept even when it alone exceeds the budget",
			pairs:  pairs,
			budget: 10,
			want:   []promptSuggestPair{pairs[3]},
		},
		{
			name:   "empty input",
			pairs:  nil,
			budget: 1000,
			want:   nil,
		},
		{
			name:   "zero budget",
			pairs:  pairs,
			budget: 0,
			want:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := promptSuggestTrimPairsLatestFirst(tt.pairs, tt.budget)
			if len(got) != len(tt.want) {
				t.Fatalf("promptSuggestTrimPairsLatestFirst() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("promptSuggestTrimPairsLatestFirst()[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// promptSuggestSession builds a session with n complete pairs; every pair's
// content carries its index, so assertions can name individual pairs.
func promptSuggestSession(id string, n int) PromptSuggestSession {
	s := PromptSuggestSession{ID: id}
	for i := 0; i < n; i++ {
		s.Messages = append(s.Messages,
			PromptSuggestMessage{Role: "user", Content: fmt.Sprintf("%s prompt %d %s", id, i, strings.Repeat("x", 60))},
			PromptSuggestMessage{Role: "assistant", Content: fmt.Sprintf("%s answer %d %s", id, i, strings.Repeat("y", 60))})
	}
	return s
}

// TestPromptSuggestUserContent_WindowCaps covers the window enforcement in
// the input assembly: at most the kind's session count is read (keeping the
// most recent ones), the per-session and global budgets trim latest-first,
// and an empty window is an error (D9).
func TestPromptSuggestUserContent_WindowCaps(t *testing.T) {
	t.Run("next_task keeps the last 10 of 12 sessions", func(t *testing.T) {
		sessions := make([]PromptSuggestSession, 0, 12)
		for i := 0; i < 12; i++ {
			sessions = append(sessions, promptSuggestSession("s"+string(rune('A'+i)), 1))
		}
		got, err := promptSuggestUserContent(SuggestionKindNextTask, PromptSuggestInput{Sessions: sessions})
		if err != nil {
			t.Fatalf("promptSuggestUserContent() error = %v", err)
		}
		if n := strings.Count(got, "[Session "); n != nextTaskWindowSessions {
			t.Fatalf("rendered %d sessions, want %d", n, nextTaskWindowSessions)
		}
		if !strings.Contains(got, "sL prompt") {
			t.Errorf("the most recent session is missing from the transcript")
		}
		if strings.Contains(got, "sA prompt") || strings.Contains(got, "sB prompt") {
			t.Errorf("the oldest sessions must be dropped by the window cap")
		}
	})

	t.Run("goal_advance keeps only the triggering session", func(t *testing.T) {
		sessions := make([]PromptSuggestSession, 0, 7)
		for i := 0; i < 7; i++ {
			sessions = append(sessions, promptSuggestSession("s"+string(rune('A'+i)), 1))
		}
		got, err := promptSuggestUserContent(SuggestionKindGoalAdvance, PromptSuggestInput{Sessions: sessions})
		if err != nil {
			t.Fatalf("promptSuggestUserContent() error = %v", err)
		}
		if n := strings.Count(got, "[Session "); n != 1 {
			t.Fatalf("rendered %d sessions, want 1 (the triggering session only in v1)", n)
		}
		if !strings.Contains(got, "sG prompt") {
			t.Errorf("the triggering (most recent) session is missing from the transcript")
		}
		if strings.Contains(got, "sA prompt") {
			t.Errorf("older sessions must be dropped by the goal_advance cap")
		}
	})

	t.Run("per-session budget keeps only the latest pairs", func(t *testing.T) {
		s := promptSuggestSession("s", 40) // 40 pairs of ~150 bytes ≈ 6 KB > budget
		got, err := promptSuggestUserContent(SuggestionKindNextTask, PromptSuggestInput{Sessions: []PromptSuggestSession{s}})
		if err != nil {
			t.Fatalf("promptSuggestUserContent() error = %v", err)
		}
		if n := strings.Count(got, "User: "); n == 40 {
			t.Fatalf("per-session budget did not trim the transcript")
		}
		if !strings.Contains(got, "s prompt 39") {
			t.Errorf("the most recent pair must survive the trim: %q", got)
		}
	})

	t.Run("nudge-only window is an error", func(t *testing.T) {
		s := PromptSuggestSession{ID: "s", Messages: []PromptSuggestMessage{
			{Role: "user", Content: "continue", IsNudge: true},
			{Role: "assistant", Content: "orphan"},
		}}
		if _, err := promptSuggestUserContent(SuggestionKindNextTask, PromptSuggestInput{Sessions: []PromptSuggestSession{s}}); !errors.Is(err, errPromptSuggestEmptyWindow) {
			t.Fatalf("promptSuggestUserContent() error = %v, want errPromptSuggestEmptyWindow", err)
		}
	})
}

// TestSuggestPromptsWithCaller_RequestShape verifies the request shape — the
// kind's system prompt, the assembled transcript, the service-policy sampling
// — and the suggestion pass-through of the production generation pass.
func TestSuggestPromptsWithCaller_RequestShape(t *testing.T) {
	caller := &promptSuggestStubCaller{
		resp: promptSuggestResp("Продолжи рефакторинг клиента\nДобавь тесты на retry\nОбнови README"),
	}
	in := PromptSuggestInput{
		Sessions: []PromptSuggestSession{promptSuggestSession("s", 2)},
	}
	got, err := suggestPromptsWithCaller(
		context.Background(), caller, newServiceMetrics(), "test-model", nil, slog.New(slog.DiscardHandler),
		SuggestionKindNextTask, in,
	)
	if err != nil {
		t.Fatalf("suggestPromptsWithCaller() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("suggestions = %q, want 3", got)
	}
	if len(caller.gotRequests) != 1 {
		t.Fatalf("got %d LLM calls, want 1", len(caller.gotRequests))
	}
	req := caller.gotRequests[0]
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v, want [system user]", req.Messages)
	}
	if req.Messages[0].Content != prompts.PromptSuggestNewSession {
		t.Errorf("next_task must ride the new-session prompt")
	}
	if !strings.Contains(req.Messages[1].Content, "s prompt 1") {
		t.Errorf("user message does not carry the assembled input: %q", req.Messages[1].Content)
	}
	if req.CallPurpose != llm.CallPurposeSummarization {
		t.Errorf("CallPurpose = %q, want summarization", req.CallPurpose)
	}
	if req.Temperature == nil || *req.Temperature != oneshotTempPromptSuggest {
		t.Errorf("Temperature = %v, want the policy-pinned prompt-suggest value", req.Temperature)
	}
	if req.MaxTokens != promptSuggestMaxTokens {
		t.Errorf("MaxTokens = %d, want %d", req.MaxTokens, promptSuggestMaxTokens)
	}
}

// TestSuggestPromptsWithCaller_GoalAdvancePrompt pins the kind→prompt mapping
// for the goal_advance kind.
func TestSuggestPromptsWithCaller_GoalAdvancePrompt(t *testing.T) {
	caller := &promptSuggestStubCaller{resp: promptSuggestResp("Добавь тест на недетерминизм")}
	if _, err := suggestPromptsWithCaller(
		context.Background(), caller, nil, "m", nil, slog.New(slog.DiscardHandler),
		SuggestionKindGoalAdvance, PromptSuggestInput{Sessions: []PromptSuggestSession{promptSuggestSession("s", 1)}},
	); err != nil {
		t.Fatalf("suggestPromptsWithCaller() error = %v", err)
	}
	if caller.gotRequests[0].Messages[0].Content != prompts.PromptSuggestContinuation {
		t.Errorf("goal_advance must ride the continuation prompt")
	}
}

// TestSuggestPromptsWithCaller_FailSafeFallback pins the fail-safe contract:
// a model that never produces a parseable line rides the client's nudge loop
// to exhaustion and then resolves to the typed fail-safe value — no
// suggestions, NO error (the caller's fail-open path sees a normal empty
// outcome, and the telemetry records a fallback).
func TestSuggestPromptsWithCaller_FailSafeFallback(t *testing.T) {
	caller := &promptSuggestStubCaller{resp: promptSuggestResp("```\n```\n```")}
	got, err := suggestPromptsWithCaller(
		context.Background(), caller, newServiceMetrics(), "m", nil, slog.New(slog.DiscardHandler),
		SuggestionKindNextTask, PromptSuggestInput{Sessions: []PromptSuggestSession{promptSuggestSession("s", 1)}},
	)
	if err != nil {
		t.Fatalf("suggestPromptsWithCaller() error = %v, want nil (fail-safe fallback)", err)
	}
	if got != nil {
		t.Fatalf("suggestions = %q, want nil", got)
	}
	if len(caller.gotRequests) != 3 {
		t.Fatalf("got %d LLM calls, want the full nudge loop (3)", len(caller.gotRequests))
	}
	// The nudge loop must have appended the corrective [System] turns.
	last := caller.gotRequests[2]
	if len(last.Messages) < 4 || last.Messages[len(last.Messages)-1].Role != "user" ||
		!strings.HasPrefix(last.Messages[len(last.Messages)-1].Content, "[System]") {
		t.Errorf("the final attempt must carry the [System] corrective nudge: %+v", last.Messages)
	}
}

// TestSuggestPromptsWithCaller_PreconditionErrors pins the no-LLM-call
// preconditions: an empty window and an unknown kind fail before the caller
// is invoked (D9: the feature's own gaps never reach the model).
func TestSuggestPromptsWithCaller_PreconditionErrors(t *testing.T) {
	t.Run("empty window", func(t *testing.T) {
		caller := &promptSuggestStubCaller{resp: promptSuggestResp("ok")}
		if _, err := suggestPromptsWithCaller(
			context.Background(), caller, nil, "m", nil, slog.New(slog.DiscardHandler),
			SuggestionKindNextTask, PromptSuggestInput{},
		); !errors.Is(err, errPromptSuggestEmptyWindow) {
			t.Fatalf("error = %v, want errPromptSuggestEmptyWindow", err)
		}
		if len(caller.gotRequests) != 0 {
			t.Fatalf("empty window must not reach the LLM, got %d calls", len(caller.gotRequests))
		}
	})
	t.Run("unknown kind", func(t *testing.T) {
		caller := &promptSuggestStubCaller{resp: promptSuggestResp("ok")}
		if _, err := suggestPromptsWithCaller(
			context.Background(), caller, nil, "m", nil, slog.New(slog.DiscardHandler),
			SuggestionKind("bogus"), PromptSuggestInput{Sessions: []PromptSuggestSession{promptSuggestSession("s", 1)}},
		); err == nil {
			t.Fatal("unknown kind must fail")
		} else if !strings.Contains(err.Error(), "bogus") {
			t.Fatalf("error = %v, want it to name the kind", err)
		}
		if len(caller.gotRequests) != 0 {
			t.Fatalf("unknown kind must not reach the LLM, got %d calls", len(caller.gotRequests))
		}
	})
}

// TestGeneratePromptSuggestions_NoRouter verifies the exported builder seam
// refuses to generate without an initialized router.
func TestGeneratePromptSuggestions_NoRouter(t *testing.T) {
	initDone := make(chan struct{})
	close(initDone)
	b := &OrchestratorBuilder{mu: sync.RWMutex{}, serviceMetrics: newServiceMetrics(), initDone: initDone}
	if _, err := b.GeneratePromptSuggestions(context.Background(), SuggestionKindNextTask, PromptSuggestInput{}); err == nil {
		t.Fatal("expected an error without an initialized router")
	}
}
