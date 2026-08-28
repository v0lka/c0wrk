package session

import (
	"testing"

	"github.com/v0lka/sp4rk/orchestration"
)

// TestEventEmitterLastActivity_TracksLifecycleLabels verifies the activity
// tracker mirrors the labels the frontend's live handlers would set, so the
// runtime-status snapshot can replace a frozen activityStatus after a
// session/project switch (e.g. "Routing request..." stuck over a running
// ReAct loop).
func TestEventEmitterLastActivity_TracksLifecycleLabels(t *testing.T) {
	emitter := NewEventEmitter("test-session", func(Event) {})

	if got := emitter.LastActivity(); got != "" {
		t.Fatalf("fresh emitter activity = %q, want empty", got)
	}

	emitter.Routing("conductor", "code", "3")
	if got := emitter.LastActivity(); got != "Analyzing request..." {
		t.Errorf("after Routing: activity = %q, want %q", got, "Analyzing request...")
	}

	emitter.StepStart(1)
	if got := emitter.LastActivity(); got != "Thinking..." {
		t.Errorf("after StepStart: activity = %q, want %q", got, "Thinking...")
	}

	emitter.ServiceWithMeta("Routing request...", map[string]any{"phase": "orchestration"})
	if got := emitter.LastActivity(); got != "Routing request..." {
		t.Errorf("after ServiceWithMeta: activity = %q, want %q", got, "Routing request...")
	}

	emitter.Finishing(7, "done")
	if got := emitter.LastActivity(); got != "Finishing..." {
		t.Errorf("after Finishing: activity = %q, want %q", got, "Finishing...")
	}

	emitter.Retry(2, 3)
	if got := emitter.LastActivity(); got != "Retrying (attempt 2/3)..." {
		t.Errorf("after Retry: activity = %q, want %q", got, "Retrying (attempt 2/3)...")
	}

	emitter.Reflection(&orchestration.Reflection{Summary: "recovered"}, 1, 3)
	if got := emitter.LastActivity(); got != "Reflecting on results..." {
		t.Errorf("after Reflection: activity = %q, want %q", got, "Reflecting on results...")
	}
}

// TestEventEmitterActivity_TracksJudgePhases verifies the strict-judge (Smart
// Approve) phase events map to honest activity labels: the judge runs BEFORE
// any confirmation card exists, so the snapshot must say the judge is
// working, then restore the live tool-call convention when it finishes.
func TestEventEmitterActivity_TracksJudgePhases(t *testing.T) {
	emitter := NewEventEmitter("test-session", func(Event) {})

	emitter.StepStart(1)
	if got := emitter.LastActivity(); got != "Thinking..." {
		t.Errorf("after StepStart: activity = %q, want %q", got, "Thinking...")
	}

	emitter.JudgeStarted("bash_exec")
	if got := emitter.LastActivity(); got != "Safety judge evaluating..." {
		t.Errorf("after JudgeStarted: activity = %q, want %q", got, "Safety judge evaluating...")
	}

	emitter.JudgeFinished("bash_exec")
	if got := emitter.LastActivity(); got != "Running tool: bash_exec..." {
		t.Errorf("after JudgeFinished: activity = %q, want %q", got, "Running tool: bash_exec...")
	}

	// A CONFIRM verdict (or judge failure degrading to manual confirmation)
	// lands a tool_confirm right after: the snapshot must switch to the
	// honest waiting label for however long the user takes to respond,
	// instead of claiming the tool is executing.
	emitter.ToolConfirm(ToolConfirmPayload{ConfirmID: "c1", Tool: "bash_exec", Args: "{}"})
	if got := emitter.LastActivity(); got != "Awaiting confirmation..." {
		t.Errorf("after ToolConfirm: activity = %q, want %q", got, "Awaiting confirmation...")
	}
}

// TestEventEmitterActivity_TracksToolConfirm verifies a tool_confirm that
// arrives WITHOUT preceding judge phases (plain user_confirm policy — no
// Smart Approve) still flips the snapshot to the waiting label.
func TestEventEmitterActivity_TracksToolConfirm(t *testing.T) {
	emitter := NewEventEmitter("test-session", func(Event) {})

	emitter.ToolConfirm(ToolConfirmPayload{ConfirmID: "c1", Tool: "edit_file", Args: "{}"})
	if got := emitter.LastActivity(); got != "Awaiting confirmation..." {
		t.Errorf("after ToolConfirm: activity = %q, want %q", got, "Awaiting confirmation...")
	}
}

// TestEventEmitterStreamingFlag verifies the open-stream flag lifecycle:
// assistant_chunk opens it, assistant_done closes it. The flag lets the
// frontend clear frozen partial streaming text after switching back to a
// session whose stream already ended in the background.
func TestEventEmitterStreamingFlag(t *testing.T) {
	emitter := NewEventEmitter("test-session", func(Event) {})

	if emitter.StreamingActive() {
		t.Fatal("fresh emitter must not report streaming")
	}

	emitter.AssistantChunk("partial ")
	if !emitter.StreamingActive() {
		t.Fatal("after AssistantChunk: streaming must be true")
	}
	if got := emitter.LastActivity(); got != "Generating response..." {
		t.Errorf("after AssistantChunk: activity = %q, want %q", got, "Generating response...")
	}

	emitter.AssistantDone("partial answer", 10, 5)
	if emitter.StreamingActive() {
		t.Fatal("after AssistantDone: streaming must be false")
	}
	// assistant_done carries no activity label of its own: the last label
	// stays until the next tracked event.
	if got := emitter.LastActivity(); got != "Generating response..." {
		t.Errorf("after AssistantDone: activity = %q, want %q", got, "Generating response...")
	}
}

// TestEventEmitterActivitySharedAcrossScopedCopies verifies scoped copies
// (WithPlanStepID / WithRetryAttempt) update the SAME session-level activity
// state — a subagent's step events must advance the session's activity label.
func TestEventEmitterActivitySharedAcrossScopedCopies(t *testing.T) {
	root := NewEventEmitter("test-session", func(Event) {})

	scoped, ok := root.WithPlanStepID("step_1").(*EventEmitter)
	if !ok {
		t.Fatalf("WithPlanStepID returned %T, want *EventEmitter", root.WithPlanStepID("step_1"))
	}
	scoped.StepStart(1)
	if got := root.LastActivity(); got != "Thinking..." {
		t.Errorf("root activity after scoped StepStart = %q, want %q", got, "Thinking...")
	}

	retryCopy, ok := root.WithRetryAttempt(2).(*EventEmitter)
	if !ok {
		t.Fatalf("WithRetryAttempt returned %T, want *EventEmitter", root.WithRetryAttempt(2))
	}
	retryCopy.StepRetry("step_1", 2, 3)
	if got := root.LastActivity(); got != "Retrying step 2/3..." {
		t.Errorf("root activity after scoped StepRetry = %q, want %q", got, "Retrying step 2/3...")
	}
}

// TestEventEmitterTokenSnapshot verifies the live token/fill snapshot read by
// GetSessionTokens: totals from EmitSessionTokens, window values from the
// session-root ContextFill.
func TestEventEmitterTokenSnapshot(t *testing.T) {
	emitter := NewEventEmitter("test-session", func(Event) {})

	empty := emitter.TokenSnapshot()
	if empty.UsedTokens != 0 || empty.MaxTokens != 0 || empty.FillPercent != 0 {
		t.Fatalf("fresh snapshot = %+v, want zero values", empty)
	}

	emitter.EmitSessionTokens(1200, 300, "test-model", "test-family")
	emitter.ContextFill(42.5, 42500, 100000, "ok", "")

	snap := emitter.TokenSnapshot()
	if snap.InputTokens != 1200 || snap.OutputTokens != 300 {
		t.Errorf("snapshot totals = %+v, want in=1200 out=300", snap)
	}
	if snap.Model != "test-model" || snap.Family != "test-family" {
		t.Errorf("snapshot model = %q/%q, want test-model/test-family", snap.Model, snap.Family)
	}
	if snap.UsedTokens != 42500 || snap.MaxTokens != 100000 {
		t.Errorf("snapshot window = %d/%d, want 42500/100000", snap.UsedTokens, snap.MaxTokens)
	}
	if snap.FillPercent != 42.5 {
		t.Errorf("snapshot fill = %v, want 42.5", snap.FillPercent)
	}
}

// TestManagerGetSessionRuntimeStatus_ActivityAndStreaming verifies the
// runtime-status snapshot surfaces the emitter's live activity label and
// streaming flag for an in-memory session, and leaves them empty for a
// session without an emitter (or an unknown session).
func TestManagerGetSessionRuntimeStatus_ActivityAndStreaming(t *testing.T) {
	manager, _, _ := testManager(t)

	emitter := NewEventEmitter("sess-activity", func(Event) {})
	emitter.StepStart(3)
	emitter.AssistantChunk("streaming...")

	manager.mu.Lock()
	manager.sessions["sess-activity"] = &Session{ID: "sess-activity", emitter: emitter}
	manager.mu.Unlock()

	status, err := manager.GetSessionRuntimeStatus("sess-activity")
	if err != nil {
		t.Fatalf("GetSessionRuntimeStatus failed: %v", err)
	}
	if status.Activity != "Generating response..." {
		t.Errorf("status.Activity = %q, want %q", status.Activity, "Generating response...")
	}
	if !status.Streaming {
		t.Error("status.Streaming = false, want true while a stream is open")
	}

	// A session with no emitter (nil in tests, or pre-emitter construction)
	// reports empty live fields rather than panicking.
	manager.mu.Lock()
	manager.sessions["sess-bare"] = &Session{ID: "sess-bare"}
	manager.mu.Unlock()
	status, err = manager.GetSessionRuntimeStatus("sess-bare")
	if err != nil {
		t.Fatalf("GetSessionRuntimeStatus failed: %v", err)
	}
	if status.Activity != "" || status.Streaming {
		t.Errorf("bare session live fields = %q/%v, want empty/false", status.Activity, status.Streaming)
	}
}

// TestManagerLiveTokenSnapshot verifies the memory-only token snapshot
// accessor used by GetSessionTokens: values from the live emitter, ok=false
// for unknown sessions.
func TestManagerLiveTokenSnapshot(t *testing.T) {
	manager, _, _ := testManager(t)

	if _, ok := manager.LiveTokenSnapshot("no-such-session"); ok {
		t.Fatal("LiveTokenSnapshot for unknown session must return ok=false")
	}

	emitter := NewEventEmitter("sess-tokens", func(Event) {})
	emitter.EmitSessionTokens(10, 20, "m", "f")
	emitter.ContextFill(5, 500, 10000, "ok", "")

	manager.mu.Lock()
	manager.sessions["sess-tokens"] = &Session{ID: "sess-tokens", emitter: emitter}
	manager.mu.Unlock()

	snap, ok := manager.LiveTokenSnapshot("sess-tokens")
	if !ok {
		t.Fatal("LiveTokenSnapshot for in-memory session must return ok=true")
	}
	if snap.InputTokens != 10 || snap.OutputTokens != 20 || snap.UsedTokens != 500 || snap.MaxTokens != 10000 {
		t.Errorf("snapshot = %+v, want in=10 out=20 used=500 max=10000", snap)
	}
}
