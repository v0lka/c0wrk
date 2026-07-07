package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/v0lka/sp4rk/llm"
)

func TestLoggingCaller_CallSuccess_LogsTokenUsage(t *testing.T) {
	inner := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{Role: "assistant", Content: "hi"},
				Usage:   llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			},
		},
	}

	// Capture slog output.
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	origLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(origLogger)

	caller := NewLoggingLLMCaller(inner, "openai", slog.New(handler))
	req := llm.ChatRequest{Model: "gpt-4o"}
	resp, err := caller.Call(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage.InputTokens != 100 || resp.Usage.OutputTokens != 50 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}

	logged := buf.String()
	for _, want := range []string{"llm: token usage", "provider=openai", "model=gpt-4o", "input_tokens=100", "output_tokens=50", "total_tokens=150", "stopReason", "toolCallCount"} {
		if !bytes.Contains([]byte(logged), []byte(want)) {
			t.Errorf("log output missing %q; got: %s", want, logged)
		}
	}
}

func TestLoggingCaller_CallError_NoLog(t *testing.T) {
	inner := &mockLLMCaller{errors: []error{errors.New("provider down")}}

	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	origLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(origLogger)

	caller := NewLoggingLLMCaller(inner, "anthropic", slog.New(handler))
	_, err := caller.Call(context.Background(), llm.ChatRequest{Model: "claude-3"})

	if err == nil {
		t.Fatal("expected error")
	}

	// With the new implementation, a "llm: request" and "llm: call failed" line are logged.
	// The old test expected zero output on error, but now we log pre-call + error.
	// We verify the error is logged.
	logged := buf.String()
	for _, want := range []string{"llm: request", "llm: call failed", "provider=anthropic"} {
		if !bytes.Contains([]byte(logged), []byte(want)) {
			t.Errorf("log output missing %q; got: %s", want, logged)
		}
	}
}

func TestLoggingCaller_DelegatesToInner(t *testing.T) {
	wantResp := &llm.ChatResponse{
		Message:    llm.Message{Role: "assistant", Content: "result"},
		Usage:      llm.TokenUsage{InputTokens: 10, OutputTokens: 20},
		StopReason: "end_turn",
	}
	inner := &mockLLMCaller{responses: []*llm.ChatResponse{wantResp}}

	// Suppress log output for this test.
	handler := slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug})
	origLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(origLogger)

	caller := NewLoggingLLMCaller(inner, "test", slog.New(handler))
	req := llm.ChatRequest{Model: "m1", Messages: []llm.Message{{Role: "user", Content: "hello"}}}
	resp, err := caller.Call(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != wantResp {
		t.Errorf("expected response to be delegated; got %+v", resp)
	}
	if len(inner.calls) != 1 {
		t.Fatalf("expected 1 call; got %d", len(inner.calls))
	}
	if inner.calls[0].Model != "m1" {
		t.Errorf("expected model m1; got %s", inner.calls[0].Model)
	}
}
