package core

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/v0lka/sp4rk/llm"
)

// Per-kind llm.service_models override coverage for the seven one-shot service
// call-sites (step 3 of the service-model overrides cycle): every test drives
// the site's own seam with a mock caller and asserts the four AC dimensions —
// the pin is stamped onto the request (req.Provider/req.Model), the reasoning
// effort is computed from the PIN model (never the session model — asserted
// via spellings that differ between the two), the unified telemetry log
// reports the pin's composite id, and without an entry the site behaves
// byte-for-byte as before (bare session model, no req.Model/req.Provider).

// serviceOverrideSetFor resolves a one-key llm.service_models section against
// the shared two-provider test router (local-llm, openai).
func serviceOverrideSetFor(t *testing.T, key, composite string) *ServiceModelOverrides {
	t.Helper()
	set := resolveServiceModelOverrides(modelOverrideValidatorTestRouter(t), map[string]string{key: composite}, nil)
	if set == nil {
		t.Fatalf("override %s=%q must resolve against the test router", key, composite)
	}
	return set
}

// serviceCallLogModels returns the "model" attribute of every unified
// service-call log record (Debug on ok outcomes, Warn on degraded ones).
func serviceCallLogModels(capture *recordCapture) []string {
	var models []string
	for _, r := range capture.snapshot() {
		if r.Message != "service call" {
			continue
		}
		models = append(models, warnAttrs(r)["model"])
	}
	return models
}

// assertSingleServiceLogModel pins the telemetry AC: exactly one service-call
// record whose model attr is the composite pin.
func assertSingleServiceLogModel(t *testing.T, capture *recordCapture, want string) {
	t.Helper()
	logs := serviceCallLogModels(capture)
	if len(logs) != 1 {
		t.Fatalf("service-call log records = %v, want exactly one", logs)
	}
	if logs[0] != want {
		t.Errorf("telemetry model = %q, want the composite pin %q", logs[0], want)
	}
}

// TestServiceModelOverride_PassthroughWithoutEntry verifies the free seam's
// no-op paths: without a resolved entry (nil set or zero value) the model
// string and the caller instance pass through unchanged — byte-for-byte the
// pre-override behavior.
func TestServiceModelOverride_PassthroughWithoutEntry(t *testing.T) {
	caller := &capturingLLMCaller{}
	for name, set := range map[string]*ServiceModelOverrides{"nil": nil, "zero": {}} {
		model, got := applyServiceModelOverride(set, ServiceKindTitle, "session-model", caller)
		if model != "session-model" {
			t.Errorf("%s set: model = %q, want the input unchanged", name, model)
		}
		if got != caller {
			t.Errorf("%s set: caller must pass through as the same instance", name)
		}
	}
}

// TestServiceModelOverride_PinRestampsIncomingRequest verifies the returned
// caller pins the resolved pair even when the incoming request already carries
// a model/provider (the router's SetModel must not redirect a pinned call).
func TestServiceModelOverride_PinRestampsIncomingRequest(t *testing.T) {
	set := serviceOverrideSetFor(t, "title", "openai/gpt-4o")
	inner := &capturingLLMCaller{}
	_, pinned := applyServiceModelOverride(set, ServiceKindTitle, "qwen3:8b", inner)

	_, _ = pinned.Call(context.Background(), llm.ChatRequest{Model: "sneaky", Provider: "other"})
	reqs := inner.captured()
	if len(reqs) != 1 {
		t.Fatalf("captured %d requests, want 1", len(reqs))
	}
	if reqs[0].Provider != "openai" || reqs[0].Model != "gpt-4o" {
		t.Errorf("pinned call carried provider=%q model=%q, want the openai/gpt-4o pin", reqs[0].Provider, reqs[0].Model)
	}
}

// TestServiceModelOverride_BuilderMethod_ResolvesFromStoredSet verifies the
// builder method reads the per-build set stored on the builder (Build/RebuildRouter
// product) and degrades to the passthrough without one.
func TestServiceModelOverride_BuilderMethod_ResolvesFromStoredSet(t *testing.T) {
	set := serviceOverrideSetFor(t, "optimize_rewrite", "openai/gpt-4o")
	b := &OrchestratorBuilder{mu: sync.RWMutex{}, serviceModels: set}
	model, caller := b.applyServiceModelOverride(ServiceKindOptimizeRewrite, "session-model", &capturingLLMCaller{})
	if model != "openai/gpt-4o" {
		t.Errorf("model = %q, want the composite pin", model)
	}
	if _, ok := caller.(*pinnedModelCaller); !ok {
		t.Errorf("caller = %T, want the pinned wrapper", caller)
	}

	unconfigured := &OrchestratorBuilder{mu: sync.RWMutex{}}
	inner := &capturingLLMCaller{}
	model, caller = unconfigured.applyServiceModelOverride(ServiceKindOptimizeRewrite, "session-model", inner)
	if model != "session-model" || caller != inner {
		t.Errorf("unconfigured builder must pass through, got model=%q caller=%T", model, caller)
	}
}

// TestServiceModelOverride_Title_PinsRequestReasoningAndTelemetry drives the
// title seam with a title pin: session model qwen3:8b (tier-off spelling
// "Off"), pin openai/gpt-4o (no reasoning field) — the "" effort on the wire
// proves the tier resolved from the PIN.
func TestServiceModelOverride_Title_PinsRequestReasoningAndTelemetry(t *testing.T) {
	capture := &recordCapture{}
	set := serviceOverrideSetFor(t, "title", "openai/gpt-4o")
	mock := &mockLLMCaller{responses: []*llm.ChatResponse{{Message: llm.Message{Content: "Pinned Title"}}}}

	title, err := generateTitleWithCaller(context.Background(), mock, newServiceMetrics(), "qwen3:8b", set, slog.New(capture), "fix auth", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if title != "Pinned Title" {
		t.Errorf("title = %q, want the pinned model's content passed through", title)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("call count = %d, want 1", len(mock.calls))
	}
	req := mock.calls[0]
	if req.Provider != "openai" || req.Model != "gpt-4o" {
		t.Errorf("request carried provider=%q model=%q, want the pin", req.Provider, req.Model)
	}
	if req.ReasoningEffort != "" {
		t.Errorf("ReasoningEffort = %q, want \"\" (computed from the gpt-4o pin, not the qwen3:8b session model whose off spelling is %q)", req.ReasoningEffort, serviceReasoningEffort("qwen3:8b", oneshotTierTitle))
	}
	assertSingleServiceLogModel(t, capture, "openai/gpt-4o")
}

// TestServiceModelOverride_Title_NestedSlashPinKeepsBareModel pins an
// HF-style composite: the bare model (nested slashes kept whole) lands on the
// request and the composite on the telemetry log.
func TestServiceModelOverride_Title_NestedSlashPinKeepsBareModel(t *testing.T) {
	capture := &recordCapture{}
	set := serviceOverrideSetFor(t, "title", "local-llm/qwen/qwen3-coder-30b")
	mock := &mockLLMCaller{responses: []*llm.ChatResponse{{Message: llm.Message{Content: "T"}}}}

	if _, err := generateTitleWithCaller(context.Background(), mock, nil, "qwen3:8b", set, slog.New(capture), "fix auth", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req := mock.calls[0]
	if req.Provider != "local-llm" || req.Model != "qwen/qwen3-coder-30b" {
		t.Errorf("request carried provider=%q model=%q, want local-llm + the whole bare qwen/qwen3-coder-30b", req.Provider, req.Model)
	}
	assertSingleServiceLogModel(t, capture, "local-llm/qwen/qwen3-coder-30b")
}

// TestServiceModelOverride_Title_NoOverrideKeepsSessionModel pins the
// unchanged behavior: without a title entry the request carries no
// Model/Provider (the router applies the active model downstream) and the
// reasoning effort comes from the session model.
func TestServiceModelOverride_Title_NoOverrideKeepsSessionModel(t *testing.T) {
	capture := &recordCapture{}
	mock := &mockLLMCaller{responses: []*llm.ChatResponse{{Message: llm.Message{Content: "T"}}}}

	if _, err := generateTitleWithCaller(context.Background(), mock, nil, "qwen3:8b", nil, slog.New(capture), "fix auth", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req := mock.calls[0]
	if req.Model != "" || req.Provider != "" {
		t.Errorf("no-override request must stay pre-router shaped (empty model/provider), got provider=%q model=%q", req.Provider, req.Model)
	}
	if want := serviceReasoningEffort("qwen3:8b", oneshotTierTitle); req.ReasoningEffort != want {
		t.Errorf("ReasoningEffort = %q, want the session-model spelling %q", req.ReasoningEffort, want)
	}
	assertSingleServiceLogModel(t, capture, "qwen3:8b")
}

// TestServiceModelOverride_CommitMessage_PinsRequestReasoningAndTelemetry
// drives the commit-message method (the override applies inside it, before
// the request build).
func TestServiceModelOverride_CommitMessage_PinsRequestReasoningAndTelemetry(t *testing.T) {
	capture := &recordCapture{}
	b := &OrchestratorBuilder{mu: sync.RWMutex{}, logger: slog.New(capture), serviceMetrics: newServiceMetrics(), serviceModels: serviceOverrideSetFor(t, "commit_message", "openai/gpt-4o")}
	mock := &mockLLMCaller{responses: []*llm.ChatResponse{{Message: llm.Message{Content: "feat: route via pinned model"}}}}

	msg, err := b.generateCommitMessageWithCaller(context.Background(), mock, "session-provider", "qwen3:8b", "diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != "feat: route via pinned model" {
		t.Errorf("message = %q, want the pinned model's valid commit message", msg)
	}
	req := mock.calls[0]
	if req.Provider != "openai" || req.Model != "gpt-4o" {
		t.Errorf("request carried provider=%q model=%q, want the pin", req.Provider, req.Model)
	}
	if req.ReasoningEffort != "" {
		t.Errorf("ReasoningEffort = %q, want \"\" from the gpt-4o pin (session qwen3:8b would spell off as %q)", req.ReasoningEffort, serviceReasoningEffort("qwen3:8b", oneshotTierCommit))
	}
	assertSingleServiceLogModel(t, capture, "openai/gpt-4o")
}

// TestServiceModelOverride_OptimizeExtract_PinsRequestReasoningAndTelemetry
// drives the extract method with an extract pin.
func TestServiceModelOverride_OptimizeExtract_PinsRequestReasoningAndTelemetry(t *testing.T) {
	capture := &recordCapture{}
	b := &OrchestratorBuilder{mu: sync.RWMutex{}, logger: slog.New(capture), serviceMetrics: newServiceMetrics(), serviceModels: serviceOverrideSetFor(t, "optimize_extract", "openai/gpt-4o")}
	mock := &mockLLMCaller{responses: []*llm.ChatResponse{{Message: llm.Message{Content: `{"translated":"готово","keywords":["kw"]}`}}}}

	res, err := b.optimizeExtract(context.Background(), mock, "qwen3:8b", "prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Translated != "готово" || len(res.Keywords) != 1 {
		t.Errorf("extract result = %+v, want the pinned model's JSON parsed", res)
	}
	req := mock.calls[0]
	if req.Provider != "openai" || req.Model != "gpt-4o" {
		t.Errorf("request carried provider=%q model=%q, want the pin", req.Provider, req.Model)
	}
	if req.ReasoningEffort != "" {
		t.Errorf("ReasoningEffort = %q, want \"\" from the gpt-4o pin (session qwen3:8b spells minimal as %q)", req.ReasoningEffort, serviceReasoningEffort("qwen3:8b", oneshotTierOptimize))
	}
	if req.MaxTokens != 500 {
		t.Errorf("MaxTokens = %d, want the policy's 500 (no reasoning field on the pin → no budget raise)", req.MaxTokens)
	}
	assertSingleServiceLogModel(t, capture, "openai/gpt-4o")
}

// TestServiceModelOverride_OptimizeExtract_ReasoningPinRaisesBudget reverses
// the direction: a reasoning-capable pin over a non-reasoning session model —
// the "On" spelling must come from the pin AND feed the service policy's
// reasoning output budget (500 → 2048).
func TestServiceModelOverride_OptimizeExtract_ReasoningPinRaisesBudget(t *testing.T) {
	b := &OrchestratorBuilder{mu: sync.RWMutex{}, logger: slog.New(slog.DiscardHandler), serviceMetrics: newServiceMetrics(), serviceModels: serviceOverrideSetFor(t, "optimize_extract", "local-llm/qwen3:8b")}
	mock := &mockLLMCaller{responses: []*llm.ChatResponse{{Message: llm.Message{Content: `{}`}}}}

	if _, err := b.optimizeExtract(context.Background(), mock, "gpt-4o", "prompt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req := mock.calls[0]
	if req.Provider != "local-llm" || req.Model != "qwen3:8b" {
		t.Errorf("request carried provider=%q model=%q, want the pin", req.Provider, req.Model)
	}
	if want := serviceReasoningEffort("qwen3:8b", oneshotTierOptimize); req.ReasoningEffort != want {
		t.Errorf("ReasoningEffort = %q, want the pin spelling %q (session gpt-4o sends none)", req.ReasoningEffort, want)
	}
	if req.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %d, want 2048 (the pin's reasoning effort must raise the budget)", req.MaxTokens)
	}
}

// TestServiceModelOverride_OptimizeRewrite_PinsRequestReasoningAndTelemetry
// drives the rewrite method with a rewrite pin.
func TestServiceModelOverride_OptimizeRewrite_PinsRequestReasoningAndTelemetry(t *testing.T) {
	capture := &recordCapture{}
	b := &OrchestratorBuilder{mu: sync.RWMutex{}, logger: slog.New(capture), serviceMetrics: newServiceMetrics(), serviceModels: serviceOverrideSetFor(t, "optimize_rewrite", "openai/gpt-4o")}
	marked := "### OPTIMIZED_PROMPT_START\nBetter prompt\n### OPTIMIZED_PROMPT_END"
	mock := &mockLLMCaller{responses: []*llm.ChatResponse{{Message: llm.Message{Content: marked}}}}

	got, err := b.optimizeRewrite(context.Background(), mock, "qwen3:8b", "prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Better prompt" {
		t.Errorf("rewrite = %q, want the marker-extracted prompt", got)
	}
	req := mock.calls[0]
	if req.Provider != "openai" || req.Model != "gpt-4o" {
		t.Errorf("request carried provider=%q model=%q, want the pin", req.Provider, req.Model)
	}
	if req.ReasoningEffort != "" {
		t.Errorf("ReasoningEffort = %q, want \"\" from the gpt-4o pin", req.ReasoningEffort)
	}
	assertSingleServiceLogModel(t, capture, "openai/gpt-4o")
}

// TestServiceModelOverride_CompactionSummarize_PinsRequestReasoningAndTelemetry
// drives the shared compaction seam (both the builder context-factory closure
// and the orchestrator's manual-compaction deps delegate into it).
func TestServiceModelOverride_CompactionSummarize_PinsRequestReasoningAndTelemetry(t *testing.T) {
	capture := &recordCapture{}
	set := serviceOverrideSetFor(t, "compaction_summarize", "openai/gpt-4o")
	mock := &mockLLMCaller{responses: []*llm.ChatResponse{{Message: llm.Message{Content: "Summary text"}}}}

	summary, err := compactionSummarizeWithCaller(context.Background(), set, mock, newServiceMetrics(), slog.New(capture), "qwen3:8b", "block text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary != "Summary text" {
		t.Errorf("summary = %q, want the pinned model's content passed through", summary)
	}
	req := mock.calls[0]
	if req.Provider != "openai" || req.Model != "gpt-4o" {
		t.Errorf("request carried provider=%q model=%q, want the pin", req.Provider, req.Model)
	}
	if req.ReasoningEffort != "" {
		t.Errorf("ReasoningEffort = %q, want \"\" from the gpt-4o pin (session qwen3:8b spells minimal as %q)", req.ReasoningEffort, serviceReasoningEffort("qwen3:8b", oneshotTierCompaction))
	}
	assertSingleServiceLogModel(t, capture, "openai/gpt-4o")
}

// TestServiceModelOverride_CompactionOrchestrator_PinReachesSummarize proves
// the orchestrator-side wiring: manualCompactionDeps' Summarize closure passes
// o.serviceModels into the shared seam, so a reasoning-capable pin overrides a
// non-reasoning session model on the compaction path too.
func TestServiceModelOverride_CompactionOrchestrator_PinReachesSummarize(t *testing.T) {
	capture := &recordCapture{}
	set := serviceOverrideSetFor(t, "compaction_summarize", "local-llm/qwen3:8b")
	mock := &mockLLMCaller{responses: []*llm.ChatResponse{{Message: llm.Message{Content: "S"}}}}
	o := NewOrchestrator(OrchestratorConfig{Model: "gpt-4o"}, OrchestratorDeps{
		LLM:            mock,
		ModelSwitcher:  modelOverrideValidatorTestRouter(t),
		ServiceModels:  set,
		ServiceMetrics: newServiceMetrics(),
		Logger:         slog.New(capture),
	})

	deps := o.manualCompactionDeps()
	if _, err := deps.Summarize(context.Background(), "block"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req := mock.calls[0]
	if req.Provider != "local-llm" || req.Model != "qwen3:8b" {
		t.Errorf("request carried provider=%q model=%q, want the pin", req.Provider, req.Model)
	}
	if want := serviceReasoningEffort("qwen3:8b", oneshotTierCompaction); req.ReasoningEffort != want {
		t.Errorf("ReasoningEffort = %q, want the pin spelling %q (session gpt-4o sends none)", req.ReasoningEffort, want)
	}
	assertSingleServiceLogModel(t, capture, "local-llm/qwen3:8b")
}

// TestServiceModelOverride_GoalSuggest_PinsRequestReasoningAndTelemetry
// drives the goal-suggest seam with a goal_suggest pin.
func TestServiceModelOverride_GoalSuggest_PinsRequestReasoningAndTelemetry(t *testing.T) {
	capture := &recordCapture{}
	set := serviceOverrideSetFor(t, "goal_suggest", "openai/gpt-4o")
	mock := &mockLLMCaller{responses: []*llm.ChatResponse{{Message: llm.Message{Content: `{"suggest": true, "explicit_criteria": true, "reason": "pinned"}`}}}}

	verdict, err := suggestGoalModeWithCaller(context.Background(), mock, newServiceMetrics(), slog.New(capture), "qwen3:8b", set, "multi-step objective", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Suggest || verdict.Reason != "pinned" {
		t.Errorf("verdict = %+v, want the pinned model's JSON parsed", verdict)
	}
	req := mock.calls[0]
	if req.Provider != "openai" || req.Model != "gpt-4o" {
		t.Errorf("request carried provider=%q model=%q, want the pin", req.Provider, req.Model)
	}
	if req.ReasoningEffort != "" {
		t.Errorf("ReasoningEffort = %q, want \"\" from the gpt-4o pin (session qwen3:8b spells minimal as %q)", req.ReasoningEffort, serviceReasoningEffort("qwen3:8b", oneshotTierGoalSuggest))
	}
	assertSingleServiceLogModel(t, capture, "openai/gpt-4o")
}

// TestServiceModelOverride_SuggestGoalMode_AppliesSessionOverride proves the
// orchestrator-side wiring for the goal classifier: the public entry passes
// o.serviceModels into the seam (no goalSuggester stub — the production path).
func TestServiceModelOverride_SuggestGoalMode_AppliesSessionOverride(t *testing.T) {
	capture := &recordCapture{}
	set := serviceOverrideSetFor(t, "goal_suggest", "openai/gpt-4o")
	mock := &mockLLMCaller{responses: []*llm.ChatResponse{{Message: llm.Message{Content: `{"suggest": false}`}}}}
	o := NewOrchestrator(OrchestratorConfig{Model: "qwen3:8b"}, OrchestratorDeps{
		LLM:            mock,
		ModelSwitcher:  modelOverrideValidatorTestRouter(t),
		ServiceModels:  set,
		ServiceMetrics: newServiceMetrics(),
		Logger:         slog.New(capture),
	})

	verdict, err := o.SuggestGoalMode(context.Background(), "one clear objective", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Suggest {
		t.Errorf("verdict = %+v, want the pinned model's no-suggestion", verdict)
	}
	req := mock.calls[0]
	if req.Provider != "openai" || req.Model != "gpt-4o" {
		t.Errorf("request carried provider=%q model=%q, want the pin", req.Provider, req.Model)
	}
	assertSingleServiceLogModel(t, capture, "openai/gpt-4o")
}

// TestServiceModelOverride_PromptSuggest_PinsRequestReasoningAndTelemetry
// drives the prompt-suggest seam with a prompt_suggest pin.
func TestServiceModelOverride_PromptSuggest_PinsRequestReasoningAndTelemetry(t *testing.T) {
	capture := &recordCapture{}
	set := serviceOverrideSetFor(t, "prompt_suggest", "openai/gpt-4o")
	mock := &mockLLMCaller{responses: []*llm.ChatResponse{{Message: llm.Message{Content: "one\ntwo\nthree"}}}}
	in := PromptSuggestInput{Sessions: []PromptSuggestSession{promptSuggestSession("s", 1)}}

	got, err := suggestPromptsWithCaller(context.Background(), mock, newServiceMetrics(), "qwen3:8b", set, slog.New(capture), SuggestionKindNextTask, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 || !strings.Contains(got[0], "one") {
		t.Errorf("suggestions = %q, want the pinned model's three lines", got)
	}
	req := mock.calls[0]
	if req.Provider != "openai" || req.Model != "gpt-4o" {
		t.Errorf("request carried provider=%q model=%q, want the pin", req.Provider, req.Model)
	}
	if req.ReasoningEffort != "" {
		t.Errorf("ReasoningEffort = %q, want \"\" from the gpt-4o pin (session qwen3:8b spells minimal as %q)", req.ReasoningEffort, serviceReasoningEffort("qwen3:8b", oneshotTierPromptSuggest))
	}
	assertSingleServiceLogModel(t, capture, "openai/gpt-4o")
}

// TestOrchestratorBuilder_ServiceModelPin verifies the backend-facing accessor:
// it reads the SAME per-build set the call sites use (so the embedded service
// gates can key the advisory warm on the effective target) and answers
// ok=false — never a fabricated pin — for an unconfigured key, an
// unknown-provider key that resolution dropped, and a builder without a set.
func TestOrchestratorBuilder_ServiceModelPin(t *testing.T) {
	set := serviceOverrideSetFor(t, "commit_message", "openai/gpt-4o")
	b := &OrchestratorBuilder{mu: sync.RWMutex{}, serviceModels: set}

	pin, ok := b.ServiceModelPin("commit_message")
	if !ok {
		t.Fatal("ServiceModelPin(commit_message) = ok=false, want the resolved pin")
	}
	if pin.Provider != "openai" || pin.Model != "gpt-4o" || pin.Composite != "openai/gpt-4o" {
		t.Errorf("pin = %+v, want openai/gpt-4o", pin)
	}

	// An unconfigured key must report "no pin" (the default model serves), not
	// an empty entry that a caller could mistake for a real target.
	if _, ok := b.ServiceModelPin("title"); ok {
		t.Error("ServiceModelPin(title) = ok=true on an unconfigured key")
	}

	// A builder without a resolved set (pre-build, or nothing configured)
	// answers ok=false for everything.
	bare := &OrchestratorBuilder{mu: sync.RWMutex{}}
	if _, ok := bare.ServiceModelPin("commit_message"); ok {
		t.Error("ServiceModelPin on a builder with no set must be ok=false")
	}
}
