package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// TestConfigureExecutor_WiresToolCallTimeout proves the configured
// timeouts.toolCallTimeout reaches a subagent executor through
// conductorDeps.toolCallTimeout: a tool that blocks forever no longer hangs
// the executor's ReAct loop — it returns ErrToolTimeout (naming the tool)
// instead. This is the subagent half of the wiring; the main executor receives
// the same value via orchestration.ConductorConfig.ToolCallTimeout.
func TestConfigureExecutor_WiresToolCallTimeout(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	blocking := &mockToolExecutor{
		executeFn: func(_ context.Context, _ string, _ json.RawMessage) (sdktools.ToolResult, error) {
			<-release // block until the test tears down
			return sdktools.ToolResult{Content: "late"}, nil
		},
	}
	caller := &mockLLMCaller{
		callFn: func(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
			// Always answer with a tool call so the executor dispatches the
			// blocking tool on its first step.
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:      "assistant",
					ToolCalls: []llm.ToolCall{{ID: "c1", Name: "search", Input: json.RawMessage(`{}`)}},
				},
				StopReason: "tool_use",
			}, nil
		},
	}

	executor := agent.NewExecutor(caller, blocking, 5, agent.WithTokenCounter(llm.NewSimpleTokenCounter()))
	l := &conductorLauncher{deps: conductorDeps{toolCallTimeout: 50 * time.Millisecond}}
	l.configureExecutor(executor)

	_, err := executor.Run(context.Background(), nil, &mockContextManager{})
	if !errors.Is(err, agent.ErrToolTimeout) {
		t.Fatalf("expected ErrToolTimeout from a subagent executor with a blocking tool, got %v", err)
	}
}
