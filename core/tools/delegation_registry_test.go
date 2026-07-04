package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/sdk/agent"
)

func TestDelegationRegistry_RegisterAndStart(t *testing.T) {
	r := NewDelegationRegistry()
	if err := r.Register("del_1", "test", nil, "blocking"); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if err := r.Register("del_1", "test", nil, "blocking"); err == nil {
		t.Error("expected error for duplicate ID")
	}

	r.Start("del_1", nil)
	d := r.Get("del_1")
	if d == nil {
		t.Fatal("expected delegation to exist")
	}
	if d.Status != DelegationStatusRunning {
		t.Errorf("expected running, got %s", d.Status)
	}
}

func TestDelegationRegistry_Complete(t *testing.T) {
	r := NewDelegationRegistry()
	_ = r.Register("del_1", "test", nil, "blocking")
	r.Start("del_1", nil)
	r.Complete("del_1", "output", nil, nil)

	d := r.Get("del_1")
	if d.Status != DelegationStatusCompleted {
		t.Errorf("expected completed, got %s", d.Status)
	}
	if d.Output != "output" {
		t.Errorf("expected output 'output', got %q", d.Output)
	}
}

func TestDelegationRegistry_CompleteWithError(t *testing.T) {
	r := NewDelegationRegistry()
	_ = r.Register("del_1", "test", nil, "blocking")
	r.Start("del_1", nil)
	r.Complete("del_1", "", errors.New("boom"), nil)

	d := r.Get("del_1")
	if d.Status != DelegationStatusFailed {
		t.Errorf("expected failed, got %s", d.Status)
	}
}

func TestDelegationRegistry_Cancel(t *testing.T) {
	r := NewDelegationRegistry()
	_ = r.Register("del_1", "test", nil, "async")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start("del_1", cancel)

	r.Cancel("del_1")
	d := r.Get("del_1")
	if d.Status != DelegationStatusCancelled {
		t.Errorf("expected cancelled, got %s", d.Status)
	}
}

func TestDelegationRegistry_CancelDoesNotOverwriteComplete(t *testing.T) {
	r := NewDelegationRegistry()
	_ = r.Register("del_1", "test", nil, "blocking")
	r.Start("del_1", nil)
	r.Complete("del_1", "done", nil, nil)

	r.Cancel("del_1")
	d := r.Get("del_1")
	if d.Status != DelegationStatusCompleted {
		t.Errorf("expected completed (cancel after complete is no-op), got %s", d.Status)
	}
}

func TestDelegationRegistry_CompleteDoesNotOverwriteCancelled(t *testing.T) {
	r := NewDelegationRegistry()
	_ = r.Register("del_1", "test", nil, "async")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start("del_1", cancel)

	r.Cancel("del_1")
	r.Complete("del_1", "", ctx.Err(), nil)

	d := r.Get("del_1")
	if d.Status != DelegationStatusCancelled {
		t.Errorf("expected cancelled (complete after cancel preserves cancelled), got %s", d.Status)
	}
}

func TestDelegationRegistry_ListPending(t *testing.T) {
	r := NewDelegationRegistry()
	_ = r.Register("del_1", "test", nil, "blocking")
	_ = r.Register("del_2", "test", nil, "blocking")
	r.Start("del_1", nil)
	r.Complete("del_1", "done", nil, nil)

	pending := r.ListPending()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pending))
	}
	if pending[0] != "del_2" {
		t.Errorf("expected del_2, got %s", pending[0])
	}
}

func TestDelegationRegistry_IsCompleted(t *testing.T) {
	r := NewDelegationRegistry()
	_ = r.Register("del_1", "test", nil, "blocking")
	if r.IsCompleted("del_1") {
		t.Error("expected not completed before Complete")
	}
	r.Complete("del_1", "", nil, nil)
	if !r.IsCompleted("del_1") {
		t.Error("expected completed after Complete")
	}
	if r.IsCompleted("nonexistent") {
		t.Error("expected false for nonexistent")
	}
}

func TestDelegationRegistry_GetReturnsCopy(t *testing.T) {
	r := NewDelegationRegistry()
	_ = r.Register("del_1", "test", nil, "blocking")
	r.Complete("del_1", "original", nil, []agent.Step{{Thought: "step1"}})

	d1 := r.Get("del_1")
	d1.Output = "mutated"

	d2 := r.Get("del_1")
	if d2.Output != "original" {
		t.Errorf("Get returned a pointer to internal state — mutation leaked: got %q, want %q", d2.Output, "original")
	}
}

func TestDelegationRegistry_Depth(t *testing.T) {
	r := NewDelegationRegistry()
	if r.Depth() != 0 {
		t.Errorf("expected depth 0, got %d", r.Depth())
	}
	child := NewDelegationRegistryWithDepth(1)
	if child.Depth() != 1 {
		t.Errorf("expected depth 1, got %d", child.Depth())
	}
}

func TestValidateDelegationTasks_AsyncDep(t *testing.T) {
	r := NewDelegationRegistry()
	tasks := []DelegationTask{
		{ID: "A", Summary: "async task", Task: "do stuff", Mode: "async"},
		{ID: "B", Summary: "blocking dep", Task: "do more", Mode: "blocking", DependsOn: []string{"A"}},
	}
	err := validateDelegationTasks(tasks, r)
	if err == nil {
		t.Fatal("expected error for depending on async task")
	}
}

func TestValidateDelegationTasks_Cycle(t *testing.T) {
	r := NewDelegationRegistry()
	tasks := []DelegationTask{
		{ID: "A", Summary: "task A", Task: "do stuff", DependsOn: []string{"B"}},
		{ID: "B", Summary: "task B", Task: "do stuff", DependsOn: []string{"A"}},
	}
	err := validateDelegationTasks(tasks, r)
	if err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestDetectDelegationCycle_NoFalsePositive(t *testing.T) {
	r := NewDelegationRegistry()
	tasks := []DelegationTask{
		{ID: "A", Summary: "root", Task: "do stuff"},
		{ID: "B", Summary: "child", Task: "do stuff", DependsOn: []string{"A"}},
		{ID: "C", Summary: "child2", Task: "do stuff", DependsOn: []string{"A"}},
	}
	if cycle := detectDelegationCycle(tasks, r); cycle != "" {
		t.Errorf("expected no cycle, got %s", cycle)
	}
}

func TestDelegationRegistry_All(t *testing.T) {
	r := NewDelegationRegistry()
	_ = r.Register("del_1", "task 1", nil, "blocking")
	_ = r.Register("del_2", "task 2", nil, "blocking")
	all := r.All()
	if len(all) != 2 {
		t.Fatalf("expected 2, got %d", len(all))
	}
}

func TestDelegationRegistry_ContextInjection(t *testing.T) {
	r := NewDelegationRegistry()
	ctx := WithDelegationRegistry(context.Background(), r)
	if DelegationRegistryFrom(ctx) != r {
		t.Error("registry not found in context")
	}
	if DelegationRegistryFrom(context.Background()) != nil {
		t.Error("expected nil for context without registry")
	}
}

func TestDelegationRegistry_CancelNilCancelFunc(t *testing.T) {
	r := NewDelegationRegistry()
	_ = r.Register("del_1", "test", nil, "blocking")
	r.Start("del_1", nil)
	r.Cancel("del_1")
	d := r.Get("del_1")
	if d.Status != DelegationStatusCancelled {
		t.Errorf("expected cancelled, got %s", d.Status)
	}
}

func TestDelegationRegistry_StartedAt(t *testing.T) {
	r := NewDelegationRegistry()
	_ = r.Register("del_1", "test", nil, "blocking")
	r.Start("del_1", nil)
	d := r.Get("del_1")
	if d.StartedAt.IsZero() {
		t.Error("expected non-zero StartedAt")
	}
	r.Complete("del_1", "", nil, nil)
	d = r.Get("del_1")
	if d.CompletedAt.IsZero() {
		t.Error("expected non-zero CompletedAt")
	}
	_ = time.Now // keep import
}
