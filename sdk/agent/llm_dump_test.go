package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/v0lka/sp4rk/llm"
)

func TestDumpCaller_Success_WritesBothEntries(t *testing.T) {
	resp := &llm.ChatResponse{
		Message: llm.Message{
			Role:    "assistant",
			Content: "hello world",
		},
		Usage:      llm.TokenUsage{InputTokens: 10, OutputTokens: 5},
		StopReason: "end_turn",
	}
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{resp},
	}

	var buf bytes.Buffer
	caller := NewDumpCaller(mock, &buf, nil)

	req := llm.ChatRequest{
		Model: "test-model",
		Messages: []llm.Message{
			{Role: "user", Content: "hi"},
		},
		Tools: []llm.ToolDefinition{
			{Name: "read_file", Description: "reads a file"},
		},
	}

	gotResp, err := caller.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotResp != resp {
		t.Fatal("response pointer should be the same as inner returned")
	}

	lines := nonEmptyLines(buf.String())
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), buf.String())
	}

	// Parse request entry.
	var reqEntry dumpEntry
	if err := json.Unmarshal([]byte(lines[0]), &reqEntry); err != nil {
		t.Fatalf("failed to parse request entry: %v", err)
	}
	if reqEntry.Direction != "request" {
		t.Errorf("expected direction=request, got %q", reqEntry.Direction)
	}
	if reqEntry.Timestamp == "" {
		t.Error("request entry has empty timestamp")
	}
	if reqEntry.Error != "" {
		t.Errorf("request entry should have no error, got %q", reqEntry.Error)
	}
	// Verify request data contains the full ChatRequest.
	var parsedReq llm.ChatRequest
	if err := json.Unmarshal(reqEntry.Data, &parsedReq); err != nil {
		t.Fatalf("failed to parse request data: %v", err)
	}
	if parsedReq.Model != "test-model" {
		t.Errorf("expected model=test-model, got %q", parsedReq.Model)
	}
	if len(parsedReq.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(parsedReq.Messages))
	}
	if len(parsedReq.Tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(parsedReq.Tools))
	}

	// Parse response entry.
	var respEntry dumpEntry
	if err := json.Unmarshal([]byte(lines[1]), &respEntry); err != nil {
		t.Fatalf("failed to parse response entry: %v", err)
	}
	if respEntry.Direction != "response" {
		t.Errorf("expected direction=response, got %q", respEntry.Direction)
	}
	if respEntry.Timestamp == "" {
		t.Error("response entry has empty timestamp")
	}
	if respEntry.Error != "" {
		t.Errorf("response entry should have no error, got %q", respEntry.Error)
	}
	// Verify response data contains the full ChatResponse.
	var parsedResp llm.ChatResponse
	if err := json.Unmarshal(respEntry.Data, &parsedResp); err != nil {
		t.Fatalf("failed to parse response data: %v", err)
	}
	if parsedResp.Message.Content != "hello world" {
		t.Errorf("expected content=hello world, got %q", parsedResp.Message.Content)
	}
	if parsedResp.Usage.InputTokens != 10 {
		t.Errorf("expected input_tokens=10, got %d", parsedResp.Usage.InputTokens)
	}
	if parsedResp.StopReason != "end_turn" {
		t.Errorf("expected stop_reason=end_turn, got %q", parsedResp.StopReason)
	}
}

func TestDumpCaller_Error_WritesErrorEntry(t *testing.T) {
	testErr := errors.New("provider timeout")
	mock := &mockLLMCaller{
		errors: []error{testErr},
	}

	var buf bytes.Buffer
	caller := NewDumpCaller(mock, &buf, nil)

	req := llm.ChatRequest{Model: "m"}
	_, gotErr := caller.Call(t.Context(), req)
	if gotErr == nil || gotErr.Error() != "provider timeout" {
		t.Fatalf("expected 'provider timeout', got %v", gotErr)
	}

	lines := nonEmptyLines(buf.String())
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	// Request entry should exist.
	var reqEntry dumpEntry
	if err := json.Unmarshal([]byte(lines[0]), &reqEntry); err != nil {
		t.Fatalf("failed to parse request entry: %v", err)
	}
	if reqEntry.Direction != "request" {
		t.Errorf("expected direction=request, got %q", reqEntry.Direction)
	}

	// Response entry with error.
	var respEntry dumpEntry
	if err := json.Unmarshal([]byte(lines[1]), &respEntry); err != nil {
		t.Fatalf("failed to parse response entry: %v", err)
	}
	if respEntry.Direction != "response" {
		t.Errorf("expected direction=response, got %q", respEntry.Direction)
	}
	if respEntry.Error != "provider timeout" {
		t.Errorf("expected error='provider timeout', got %q", respEntry.Error)
	}
	if string(respEntry.Data) != "null" {
		t.Errorf("expected data=null, got %s", respEntry.Data)
	}
}

func TestDumpCaller_NilWriter_ReturnsInner(t *testing.T) {
	mock := &mockLLMCaller{}
	got := NewDumpCaller(mock, nil, nil)
	if got != mock {
		t.Error("NewDumpCaller with nil writer should return inner unchanged")
	}
}

func TestDumpCaller_ConcurrentSafety(t *testing.T) {
	const goroutines = 10

	// Pre-populate enough responses for all goroutines.
	responses := make([]*llm.ChatResponse, goroutines)
	for i := range responses {
		responses[i] = &llm.ChatResponse{
			Message:    llm.Message{Role: "assistant", Content: "ok"},
			StopReason: "end_turn",
		}
	}
	mock := &mockLLMCaller{responses: responses}

	var buf bytes.Buffer
	caller := NewDumpCaller(mock, &buf, nil)
	req := llm.ChatRequest{Model: "m"}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = caller.Call(t.Context(), req)
		}()
	}
	wg.Wait()

	lines := nonEmptyLines(buf.String())
	if len(lines) != goroutines*2 {
		t.Fatalf("expected %d lines, got %d", goroutines*2, len(lines))
	}

	// Verify every line is valid JSON.
	for i, line := range lines {
		var entry dumpEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Errorf("line %d: invalid JSON: %v", i, err)
		}
	}
}

// nonEmptyLines splits s by newline and returns only non-empty lines.
func nonEmptyLines(s string) []string {
	raw := strings.Split(s, "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
