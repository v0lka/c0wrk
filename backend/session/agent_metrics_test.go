package session

import (
	"reflect"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
)

// TestAgentMetrics_ExecutorDiagnosticsToPayload feeds the emitter with the
// executor diagnostic stream of a representative task run (all loop-detector
// nudges and aborts from the sp4rk agent.Executor, plus step/token/session
// events) and verifies the aggregated agent_metrics payload produced for the
// finish emission. This is the contract the "metrics" layer relies on:
// executor events → session aggregator → agent_metrics payload on finish.
func TestAgentMetrics_ExecutorDiagnosticsToPayload(t *testing.T) {
	var emitted []Event
	emitter := NewEventEmitter("sess-1", func(evt Event) { emitted = append(emitted, evt) })

	emitter.SetSmallLLMProfile(true, []string{"essential_tools", "system_prompt_lite"})

	// Executor loop-detector diagnostics (as emitted by sp4rk agent.Executor).
	emitter.ExecutorDiagnostic(1, "repeated_tool_call_nudge", nil)
	emitter.ExecutorDiagnostic(2, "same_tool_repeat_nudge", nil)
	emitter.ExecutorDiagnostic(3, "fruitless_nudge", nil)
	emitter.ExecutorDiagnostic(4, "parse_error_nudge", map[string]any{"tool": "finish"})
	emitter.ExecutorDiagnostic(5, "same_tool_repeat_abort", nil)
	emitter.ExecutorDiagnostic(6, "fruitless_abort", nil)
	emitter.ExecutorDiagnostic(7, "parse_error_abort", nil)
	// Informational diagnostics must not affect quality counters.
	emitter.ExecutorDiagnostic(8, "executor_finish_nudge", nil)
	emitter.ExecutorDiagnostic(9, "pre_compaction_nudge", nil)

	// Steps: two conductor steps plus one subagent step emitted through a
	// plan-scoped copy — the copy must share the session-level counters.
	emitter.StepStart(1)
	emitter.StepStart(2)
	emitter.WithPlanStepID("step-1").StepStart(1)

	// Output token accounting (as cached by EmitSessionTokens).
	emitter.EmitSessionTokens(500, 1200, "test-model", "test-family")

	want := AgentMetricsData{
		Finish:       "full",
		ParseErrors:  2,
		Nudges:       AgentMetricsCounters{Repeat: 1, SameTool: 1, Fruitless: 1, Parse: 1},
		Aborts:       AgentMetricsCounters{SameTool: 1, Fruitless: 1, Parse: 1},
		Steps:        3,
		OutputTokens: 1200,
		SmallLLM: SmallLLMMetaInfo{
			Enabled:  true,
			Variants: []string{"essential_tools", "system_prompt_lite"},
		},
	}
	got := emitter.EmitAgentMetrics("full")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agent_metrics payload mismatch:\n got: %+v\nwant: %+v", got, want)
	}

	// The agent_metrics event itself was emitted with the same payload.
	var metricsEvents []Event
	for _, evt := range emitted {
		if evt.Type == "agent_metrics" {
			metricsEvents = append(metricsEvents, evt)
		}
	}
	if len(metricsEvents) != 1 {
		t.Fatalf("expected exactly 1 agent_metrics event, got %d", len(metricsEvents))
	}
	if data, ok := metricsEvents[0].Data.(AgentMetricsData); !ok || !reflect.DeepEqual(data, want) {
		t.Fatalf("emitted agent_metrics event payload mismatch: %+v", metricsEvents[0].Data)
	}

	// Counters reset after emission: the next task run starts from zero
	// (session-level small-LLM profile and token totals persist).
	next := emitter.EmitAgentMetrics("failed")
	wantNext := AgentMetricsData{
		Finish:       "failed",
		OutputTokens: 1200,
		SmallLLM:     SmallLLMMetaInfo{Enabled: true, Variants: []string{"essential_tools", "system_prompt_lite"}},
	}
	if !reflect.DeepEqual(next, wantNext) {
		t.Fatalf("expected counters reset after emission:\n got: %+v\nwant: %+v", next, wantNext)
	}
}

// TestAgentMetrics_CollectedWithoutSmallLLMProfile verifies the acceptance
// criterion that metrics are collected even when the small-LLM profile is
// disabled: the aggregator is part of the common session layer, not gated by
// the profile.
func TestAgentMetrics_CollectedWithoutSmallLLMProfile(t *testing.T) {
	emitter := NewEventEmitter("sess-2", func(Event) {})

	// No SetSmallLLMProfile call — the profile was never enabled.
	emitter.ExecutorDiagnostic(1, "parse_error_nudge", nil)
	emitter.StepStart(1)

	got := emitter.EmitAgentMetrics("cancelled")
	if got.ParseErrors != 1 || got.Nudges.Parse != 1 || got.Steps != 1 {
		t.Fatalf("metrics must be collected with the small-LLM profile off: %+v", got)
	}
	if got.SmallLLM.Enabled {
		t.Fatalf("small_llm.enabled must be false when the profile is off: %+v", got.SmallLLM)
	}
	if len(got.SmallLLM.Variants) != 0 {
		t.Fatalf("small_llm.variants must be empty when the profile is off: %+v", got.SmallLLM)
	}
}

// TestSmallLLMProfileFromConfig verifies the config → metrics-meta mapping:
// every variant sub-toggle counts only when BOTH the master toggle and the
// sub-toggle are on (mirroring ApplySmallLLM semantics).
func TestSmallLLMProfileFromConfig(t *testing.T) {
	enabled := config.SmallLLMConfig{
		EssentialTools: config.EssentialToolsConfig{Enabled: true},
		SystemPrompt:   config.SystemPromptConfig{Lite: true, FewShot: true},
		Sampling:       config.SmallLLMSamplingConfig{Enabled: true},
	}

	// Master toggle off → whole profile reported as disabled, no variants.
	off := enabled
	off.Enabled = false
	if info := smallLLMProfileFromConfig(off); info.Enabled || len(info.Variants) != 0 {
		t.Fatalf("master toggle off must disable the whole profile: %+v", info)
	}

	// Master toggle on → only enabled sub-toggles are listed, in canonical order.
	on := enabled
	on.Enabled = true
	on.LoopHardening = config.LoopHardeningConfig{Enabled: true}
	on.Context = config.SmallLLMContextConfig{Enabled: true}
	want := []string{
		"essential_tools",
		"system_prompt_lite",
		"system_prompt_few_shot",
		"sampling",
		"loop_hardening",
		"context",
	}
	info := smallLLMProfileFromConfig(on)
	if !info.Enabled || !reflect.DeepEqual(info.Variants, want) {
		t.Fatalf("variant mapping mismatch:\n got: %+v\nwant: %v", info.Variants, want)
	}

	// ReasoningScaffold is reported only when its parent Lite variant is on.
	scaffoldOnly := config.SmallLLMConfig{
		Enabled:      true,
		SystemPrompt: config.SystemPromptConfig{ReasoningScaffold: true},
	}
	info = smallLLMProfileFromConfig(scaffoldOnly)
	if !reflect.DeepEqual(info.Variants, []string{}) {
		t.Fatalf("reasoning scaffold without lite must not be reported: %+v", info.Variants)
	}
}

// TestEventPersister_AgentMetricsPersistedAsStatus verifies the persistence
// contract: agent_metrics is persisted with role "status" and its payload
// round-trips through the metadata JSON, so per-run quality metrics survive
// session reloads.
func TestEventPersister_AgentMetricsPersistedAsStatus(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	p.Persist(Event{
		SessionID: "s1",
		Type:      "agent_metrics",
		Data: AgentMetricsData{
			Finish:       "full",
			ParseErrors:  1,
			Nudges:       AgentMetricsCounters{Parse: 1},
			Steps:        3,
			OutputTokens: 42,
			SmallLLM:     SmallLLMMetaInfo{Enabled: false, Variants: []string{}},
		},
	})

	rows := store.snapshot()
	if len(rows) != 1 {
		t.Fatalf("expected 1 persisted row, got %d: %+v", len(rows), rows)
	}
	if rows[0].Role != "status" {
		t.Fatalf("expected role %q, got %q", "status", rows[0].Role)
	}
	for _, want := range []string{`"finish":"full"`, `"parse_errors":1`, `"steps":3`, `"output_tokens":42`} {
		if !strings.Contains(string(rows[0].Metadata), want) {
			t.Errorf("metadata %s must contain %s", string(rows[0].Metadata), want)
		}
	}
}
