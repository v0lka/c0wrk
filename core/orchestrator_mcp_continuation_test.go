package core

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
)

func TestHandleMessageMCP_RestartContinuationUnionAndFreshIsolation(t *testing.T) {
	store := newE2SCheckpointStore()
	boards := make(map[string]*delegSpecBB)
	var prompts []string
	caller := &mockLLMCaller{callFn: func(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
		var prompt strings.Builder
		for _, message := range req.Messages {
			if message.Role == "system" {
				prompt.WriteString(message.Content)
			}
		}
		prompts = append(prompts, prompt.String())
		return e2sFinishResponse("finish", "done"), nil
	}}
	build := func() *Orchestrator {
		o, _, _, _ := newFunnelOrchestrator(t, caller)
		o.SetTaskStore(store)
		o.config.E2S.Enabled = true
		o.bbFactory = func(id string) orchestration.Blackboard { bb := newDelegSpecBB(id, store); boards[id] = bb; return bb }
		o.SetBlackboardRestoreFunc(func(id, _ string, _ TaskPersistence, _ *slog.Logger, _ ...orchestration.MapBlackboardOption) (PersistableBlackboard, error) {
			return boards[id], nil
		})
		return o
	}
	o := build()
	first, err := o.HandleMessage(context.Background(), "work", "session", HandleOptions{E2S: true, UserMCPServers: []string{"a"}})
	if err != nil {
		t.Fatalf("HandleMessage(first) = %v, want nil", err)
	}
	pbb, ok := first.Blackboard.(PersistableBlackboard)
	if !ok {
		t.Fatal("HandleMessage(first) blackboard is not persistent")
	}
	id := pbb.TaskID()
	o = build() // fresh orchestrator, same durable task store, no mention cache.
	for _, extra := range [][]string{nil, {"b", "a", "b"}, nil} {
		if _, err := o.HandleMessage(context.Background(), "continue", "session", HandleOptions{TaskID: id, E2S: true, UserMCPServers: extra}); err != nil {
			t.Fatalf("HandleMessage(continuation %v) = %v, want nil", extra, err)
		}
	}
	got, err := store.LoadMCPMentions(context.Background(), id)
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("LoadMCPMentions(continued task) = (%v,%v), want a,b,nil", got, err)
	}
	for i, prompt := range prompts {
		if strings.Contains(prompt, "Requested MCP Servers") {
			t.Errorf("HandleMessage(entry %d) renders a server directive — E2S renders no server sections; the durable mention union lives in the task store and the entry gate", i)
		}
	}
	fresh, err := o.HandleMessage(WithUserMCPServers(context.Background(), []string{"foreign"}), "new work", "session", HandleOptions{E2S: true})
	if err != nil {
		t.Fatalf("HandleMessage(fresh) = %v, want nil", err)
	}
	freshBB, ok := fresh.Blackboard.(PersistableBlackboard)
	if !ok {
		t.Fatal("HandleMessage(fresh) blackboard is not persistent")
	}
	freshID := freshBB.TaskID()
	got, err = store.LoadMCPMentions(context.Background(), freshID)
	if err != nil || len(got) != 0 || freshID == id {
		t.Errorf("fresh task mentions = (%v,%v), want independent empty task", got, err)
	}
	if strings.Contains(prompts[len(prompts)-1], "Requested MCP Servers") {
		t.Error("HandleMessage(fresh) inherited another task's server directive")
	}
}
