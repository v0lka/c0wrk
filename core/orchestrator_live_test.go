package core

import (
	"context"
	"testing"
)

// The live user-message queue backs live-send: a message submitted while a
// request is executing is queued on the orchestrator and drained by the
// running request's executor at its next step boundary (via
// ConductorConfig.UserMessageSource). These tests cover the queue primitive
// itself (FIFO, one-message drain, atomic take, discard) and its wiring into
// conductorDeps.

func TestLiveUserMessageQueue_FIFO(t *testing.T) {
	o := newGoalTestOrchestrator()

	o.QueueLiveUserMessage("first")
	o.QueueLiveUserMessage("second")
	o.QueueLiveUserMessage("third")

	// Drain returns exactly one message per call, in FIFO order.
	if got := o.DrainLiveUserMessages(); got != "first" {
		t.Errorf("drain #1 = %q, want %q", got, "first")
	}
	if got := o.DrainLiveUserMessages(); got != "second" {
		t.Errorf("drain #2 = %q, want %q", got, "second")
	}
	if got := o.DrainLiveUserMessages(); got != "third" {
		t.Errorf("drain #3 = %q, want %q", got, "third")
	}
	// An empty queue drains to "" (the executor's no-op sentinel).
	if got := o.DrainLiveUserMessages(); got != "" {
		t.Errorf("drain on empty queue = %q, want empty", got)
	}
}

func TestLiveUserMessageQueue_EmptyMessageIsNoop(t *testing.T) {
	o := newGoalTestOrchestrator()

	o.QueueLiveUserMessage("")
	if got := o.DrainLiveUserMessages(); got != "" {
		t.Errorf("queue drained %q after QueueLiveUserMessage(\"\"), want empty", got)
	}
}

func TestLiveUserMessageQueue_TakeIsAtomicAndClearing(t *testing.T) {
	o := newGoalTestOrchestrator()

	o.QueueLiveUserMessage("a")
	o.QueueLiveUserMessage("b")

	msgs := o.TakeLiveUserMessages()
	if len(msgs) != 2 || msgs[0] != "a" || msgs[1] != "b" {
		t.Fatalf("take = %v, want [a b]", msgs)
	}
	if again := o.TakeLiveUserMessages(); again != nil {
		t.Errorf("second take = %v, want nil (queue cleared)", again)
	}
	if got := o.DrainLiveUserMessages(); got != "" {
		t.Errorf("drain after take = %q, want empty", got)
	}
}

func TestLiveUserMessageQueue_DiscardDropsAll(t *testing.T) {
	o := newGoalTestOrchestrator()

	o.QueueLiveUserMessage("a")
	o.QueueLiveUserMessage("b")
	o.DiscardLiveUserMessages()

	if got := o.DrainLiveUserMessages(); got != "" {
		t.Errorf("queue drained %q after discard, want empty", got)
	}
}

// TestBuildConductorDeps_UserMessageSourceWired verifies that
// buildConductorDeps wires the orchestrator's live-message drain into every
// conductor run (the normal path, resumes, and each goal-loop turn all build
// deps through it): the deps' source must drain the same FIFO queue, and a
// drained message must not be delivered twice.
func TestBuildConductorDeps_UserMessageSourceWired(t *testing.T) {
	o := newGoalTestOrchestrator()

	deps := o.buildConductorDeps(nil, nil)
	if deps.userMessageSource == nil {
		t.Fatal("buildConductorDeps left userMessageSource nil — live messages would never drain")
	}

	o.QueueLiveUserMessage("steer left")
	if got := deps.userMessageSource(context.Background()); got != "steer left" {
		t.Errorf("source returned %q, want %q", got, "steer left")
	}
	if got := deps.userMessageSource(context.Background()); got != "" {
		t.Errorf("source returned %q on second poll, want empty (drained)", got)
	}
}

// TestLiveQueueIsOrchestratorScopedNotRequestScoped documents the lifetime
// contract: the queue survives request boundaries so a message queued in the
// closing window of a finishing request is delivered by the request epilogue
// (follow-up task) or a later request — never silently lost. Clearing happens
// only via explicit Take/Discard by the owning flows.
func TestLiveQueueIsOrchestratorScopedNotRequestScoped(t *testing.T) {
	o := newGoalTestOrchestrator()

	// Simulate request entry/exit (installPauseSignal installs and clears the
	// pause signal per request): queueing before "exit", peeking after.
	release := o.installPauseSignal()
	o.QueueLiveUserMessage("queued mid-request")
	release()

	if msgs := o.TakeLiveUserMessages(); len(msgs) != 1 || msgs[0] != "queued mid-request" {
		t.Fatalf("queue = %v after request exit, want the message preserved", msgs)
	}
}
