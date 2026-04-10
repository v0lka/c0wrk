package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/user/agent/sdk/tools"
)

// mockDispatcher is a test double for the toolDispatcher interface.
type mockDispatcher struct {
	handler func(ctx context.Context, name string, input json.RawMessage) (tools.ToolResult, error)
}

func (m *mockDispatcher) Execute(ctx context.Context, name string, input json.RawMessage) (tools.ToolResult, error) {
	return m.handler(ctx, name, input)
}

func newBatchInput(t *testing.T, calls []BatchCall) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(BatchInput{Calls: calls})
	if err != nil {
		t.Fatalf("failed to marshal batch input: %v", err)
	}
	return data
}

func TestBatchTool_Name(t *testing.T) {
	bt := NewBatchTool(&mockDispatcher{})
	if bt.Name() != "batch" {
		t.Errorf("expected Name() = %q, got %q", "batch", bt.Name())
	}
}

func TestBatchTool_DefaultPolicy(t *testing.T) {
	bt := NewBatchTool(&mockDispatcher{})
	if bt.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected PolicyAlwaysAllow, got %v", bt.DefaultPolicy())
	}
}

func TestBatchTool_ParallelExecution(t *testing.T) {
	d := &mockDispatcher{handler: func(_ context.Context, name string, _ json.RawMessage) (tools.ToolResult, error) {
		return tools.ToolResult{Content: "result-" + name}, nil
	}}
	bt := NewBatchTool(d)

	input := newBatchInput(t, []BatchCall{
		{Tool: "a", Input: json.RawMessage(`{}`)},
		{Tool: "b", Input: json.RawMessage(`{}`)},
		{Tool: "c", Input: json.RawMessage(`{}`)},
	})

	result, err := bt.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected IsError=false, got content: %s", result.Content)
	}

	var out BatchOutput
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}
	if len(out.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(out.Results))
	}
	for _, r := range out.Results {
		if !r.Success {
			t.Errorf("expected success for tool %q, got error: %s", r.Tool, r.Error)
		}
		if r.Output != "result-"+r.Tool {
			t.Errorf("expected output %q, got %q", "result-"+r.Tool, r.Output)
		}
	}
}

func TestBatchTool_OrderPreserved(t *testing.T) {
	d := &mockDispatcher{handler: func(_ context.Context, name string, _ json.RawMessage) (tools.ToolResult, error) {
		// Add small random-ish delay to encourage goroutine reordering.
		if name == "first" {
			time.Sleep(20 * time.Millisecond)
		}
		return tools.ToolResult{Content: "out-" + name}, nil
	}}
	bt := NewBatchTool(d)

	input := newBatchInput(t, []BatchCall{
		{Tool: "first", Input: json.RawMessage(`{}`)},
		{Tool: "second", Input: json.RawMessage(`{}`)},
		{Tool: "third", Input: json.RawMessage(`{}`)},
	})

	result, err := bt.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out BatchOutput
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	expected := []string{"first", "second", "third"}
	for i, want := range expected {
		if out.Results[i].Tool != want {
			t.Errorf("results[%d].Tool = %q, want %q", i, out.Results[i].Tool, want)
		}
	}
}

func TestBatchTool_RecursionGuard(t *testing.T) {
	d := &mockDispatcher{handler: func(context.Context, string, json.RawMessage) (tools.ToolResult, error) {
		t.Fatal("dispatcher should not be called for recursive batch")
		return tools.ToolResult{}, nil
	}}
	bt := NewBatchTool(d)

	input := newBatchInput(t, []BatchCall{
		{Tool: "safe", Input: json.RawMessage(`{}`)},
		{Tool: "batch", Input: json.RawMessage(`{}`)},
	})

	result, err := bt.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for recursive batch, got content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "recursive") {
		t.Errorf("expected error message about recursion, got: %s", result.Content)
	}
}

func TestBatchTool_PartialFailure(t *testing.T) {
	d := &mockDispatcher{handler: func(_ context.Context, name string, _ json.RawMessage) (tools.ToolResult, error) {
		if name == "fail" {
			return tools.ToolResult{}, fmt.Errorf("tool %q exploded", name)
		}
		return tools.ToolResult{Content: "ok-" + name}, nil
	}}
	bt := NewBatchTool(d)

	input := newBatchInput(t, []BatchCall{
		{Tool: "good1", Input: json.RawMessage(`{}`)},
		{Tool: "fail", Input: json.RawMessage(`{}`)},
		{Tool: "good2", Input: json.RawMessage(`{}`)},
	})

	result, err := bt.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Batch result itself should NOT be an error.
	if result.IsError {
		t.Fatalf("expected batch IsError=false, got content: %s", result.Content)
	}

	var out BatchOutput
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	if !out.Results[0].Success || out.Results[0].Output != "ok-good1" {
		t.Errorf("results[0] unexpected: %+v", out.Results[0])
	}
	if out.Results[1].Success {
		t.Errorf("results[1] should be failure: %+v", out.Results[1])
	}
	if out.Results[1].Error == "" {
		t.Errorf("results[1] should have error message")
	}
	if !out.Results[2].Success || out.Results[2].Output != "ok-good2" {
		t.Errorf("results[2] unexpected: %+v", out.Results[2])
	}
}

func TestBatchTool_ToolNotFound(t *testing.T) {
	d := &mockDispatcher{handler: func(_ context.Context, name string, _ json.RawMessage) (tools.ToolResult, error) {
		return tools.ToolResult{}, fmt.Errorf("tool %q not found", name)
	}}
	bt := NewBatchTool(d)

	input := newBatchInput(t, []BatchCall{
		{Tool: "nonexistent", Input: json.RawMessage(`{}`)},
	})

	result, err := bt.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Batch-level should not be an error; individual call should have error.
	if result.IsError {
		t.Fatalf("expected batch IsError=false, got content: %s", result.Content)
	}

	var out BatchOutput
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}
	if out.Results[0].Success {
		t.Errorf("expected failure for nonexistent tool, got success")
	}
	if !strings.Contains(out.Results[0].Error, "not found") {
		t.Errorf("expected 'not found' in error, got: %s", out.Results[0].Error)
	}
}

func TestBatchTool_EmptyCalls(t *testing.T) {
	bt := NewBatchTool(&mockDispatcher{})

	input := newBatchInput(t, []BatchCall{})

	result, err := bt.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for empty calls, got content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "empty") {
		t.Errorf("expected 'empty' in error message, got: %s", result.Content)
	}
}

func TestBatchTool_InvalidJSON(t *testing.T) {
	bt := NewBatchTool(&mockDispatcher{})

	result, err := bt.Execute(context.Background(), json.RawMessage(`{invalid`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for invalid JSON")
	}
}

func TestBatchTool_ConcurrencyCap(t *testing.T) {
	const maxConc = 2
	var running int64
	var peak int64

	d := &mockDispatcher{handler: func(_ context.Context, _ string, _ json.RawMessage) (tools.ToolResult, error) {
		cur := atomic.AddInt64(&running, 1)
		// Track peak concurrency.
		for {
			old := atomic.LoadInt64(&peak)
			if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond) // ensure overlap window
		atomic.AddInt64(&running, -1)
		return tools.ToolResult{Content: "ok"}, nil
	}}
	bt := NewBatchTool(d, WithMaxConcurrency(maxConc))

	calls := make([]BatchCall, 6)
	for i := range calls {
		calls[i] = BatchCall{Tool: fmt.Sprintf("t%d", i), Input: json.RawMessage(`{}`)}
	}
	input := newBatchInput(t, calls)

	result, err := bt.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}

	observed := atomic.LoadInt64(&peak)
	if observed > int64(maxConc) {
		t.Errorf("peak concurrency %d exceeded max %d", observed, maxConc)
	}
	if observed < 2 {
		t.Logf("warning: peak concurrency was %d, expected 2 (may be flaky on slow CI)", observed)
	}
}

func TestBatchTool_ResultTruncation(t *testing.T) {
	longContent := strings.Repeat("x", 200)

	d := &mockDispatcher{handler: func(context.Context, string, json.RawMessage) (tools.ToolResult, error) {
		return tools.ToolResult{Content: longContent}, nil
	}}
	bt := NewBatchTool(d, WithMaxResultSize(100))

	input := newBatchInput(t, []BatchCall{
		{Tool: "a", Input: json.RawMessage(`{}`)},
		{Tool: "b", Input: json.RawMessage(`{}`)},
	})

	result, err := bt.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out BatchOutput
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	for i, r := range out.Results {
		if !r.Success {
			t.Errorf("results[%d] expected success, got error: %s", i, r.Error)
			continue
		}
		if !strings.Contains(r.Output, "...(truncated, original size: 200 chars)") {
			t.Errorf("results[%d] expected truncation marker, got: %s", i, r.Output)
		}
		// Budget per call = 100/2 = 50, so output should start with 50 x's.
		if !strings.HasPrefix(r.Output, strings.Repeat("x", 50)) {
			t.Errorf("results[%d] expected 50 'x' chars before truncation", i)
		}
	}
}

func TestBatchTool_ContextCancellation(t *testing.T) {
	started := make(chan struct{})
	d := &mockDispatcher{handler: func(ctx context.Context, _ string, _ json.RawMessage) (tools.ToolResult, error) {
		close(started)
		// Simulate work that respects context.
		select {
		case <-ctx.Done():
			return tools.ToolResult{}, ctx.Err()
		case <-time.After(5 * time.Second):
			return tools.ToolResult{Content: "should not reach"}, nil
		}
	}}
	bt := NewBatchTool(d)

	ctx, cancel := context.WithCancel(context.Background())

	input := newBatchInput(t, []BatchCall{
		{Tool: "slow", Input: json.RawMessage(`{}`)},
	})

	done := make(chan struct{})
	var result tools.ToolResult
	var execErr error
	go func() {
		result, execErr = bt.Execute(ctx, input)
		close(done)
	}()

	<-started
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not return after context cancellation")
	}

	if execErr != nil {
		t.Fatalf("unexpected Go error: %v", execErr)
	}

	var out BatchOutput
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}
	if out.Results[0].Success {
		t.Errorf("expected failure due to context cancellation")
	}
	if !strings.Contains(out.Results[0].Error, "context canceled") {
		t.Errorf("expected 'context canceled' in error, got: %s", out.Results[0].Error)
	}
}
