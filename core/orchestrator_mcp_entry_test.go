package core

import (
	"context"
	"errors"
	"testing"

	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
)

func TestMCPModes_PublicationBeforeFailedReconciliationAndImmutableSnapshot(t *testing.T) {
	b := &OrchestratorBuilder{mcpDone: make(chan struct{})}
	o := &Orchestrator{config: OrchestratorConfig{MCPServerModesResolver: b.currentMCPServerModes}}
	bb := newDelegSpecBB("task", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, mode := range []string{"auto", "manual", "auto", "disabled", "manual"} {
		cfg := &BuilderConfig{MCP: BuilderMCPConfig{Servers: map[string]BuilderMCPServer{"srv": {Mode: mode}}}}
		if err := b.ReconfigureMCP(ctx, cfg); !errors.Is(err, context.Canceled) {
			t.Fatalf("ReconfigureMCP(%s,cancelled) = %v, want cancellation", mode, err)
		}
		cfg.MCP.Servers["srv"] = BuilderMCPServer{Mode: "auto"}
		snapshot := b.currentMCPServerModes()
		snapshot["srv"] = "auto"
		if got := b.currentMCPServerModes()["srv"]; got != mode {
			t.Errorf("currentMCPServerModes(%s) = %s, want independent %s", mode, got, mode)
		}
		prepared, err := o.prepareTaskMCP(context.Background(), bb, nil)
		if err != nil {
			t.Fatal(err)
		}

		if got := coretools.GatedMCPServersFromContext(prepared)["srv"]; got != (mode != "auto") {
			t.Errorf("published gate(%s) = %v, want %v", mode, got, mode != "auto")
		}
	}
}

func TestHandleMessageMCP_ReadAndWriteFailuresBeforeExecution(t *testing.T) {
	for _, failure := range []string{"read", "write"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			caller := &mockLLMCaller{callFn: func(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
				calls++
				return e2sFinishResponse("finish", "done"), nil
			}}
			o, _, _, _ := newFunnelOrchestrator(t, caller)
			store := &failingMCPStore{}
			if failure == "read" {
				store.readErr = errors.New("read unavailable")
			} else {
				store.writeErr = errors.New("write unavailable")
			}
			o.SetTaskStore(store)
			o.bbFactory = func(id string) orchestration.Blackboard { return newDelegSpecBB(id, store) }
			o.config.E2S.Enabled = true
			if _, err := o.HandleMessage(context.Background(), "work", "session", HandleOptions{E2S: true, UserMCPServers: []string{"srv"}}); !errors.Is(err, ErrMCPAuthorizationState) {
				t.Fatalf("HandleMessage(%s failure) = %v, want authorization error", failure, err)
			}
			if calls != 0 {
				t.Errorf("HandleMessage(%s failure) LLM calls=%d, want 0", failure, calls)
			}
			store.readErr = nil
			store.writeErr = nil
			if _, err := o.HandleMessage(context.Background(), "work", "session", HandleOptions{E2S: true, UserMCPServers: []string{"srv"}}); err != nil {
				t.Fatalf("HandleMessage(recovered) = %v, want nil", err)
			}
			if calls != 1 {
				t.Errorf("HandleMessage(recovered) LLM calls=%d, want 1", calls)
			}
		})
	}
}
