package core

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/oneshot"
)

// containsAll reports whether s contains every needle substring.
func containsAll(s string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(s, n) {
			return false
		}
	}
	return true
}

// capturingLLMCaller records every incoming request so tests can assert on the
// exact request shape the wrapper produced; resp passes through verbatim.
type capturingLLMCaller struct {
	mu   sync.Mutex
	reqs []llm.ChatRequest
	resp *llm.ChatResponse
}

func (c *capturingLLMCaller) Call(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, req)
	return c.resp, nil
}

func (c *capturingLLMCaller) captured() []llm.ChatRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]llm.ChatRequest(nil), c.reqs...)
}

// warnAttrs flattens a captured record's attrs into a string-keyed map (single
// value per key is enough for these assertions).
func warnAttrs(r slog.Record) map[string]string {
	attrs := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	return attrs
}

// warnRecords returns only the WARN-level records from a capture.
func warnRecords(capture *recordCapture) []slog.Record {
	var warns []slog.Record
	for _, r := range capture.snapshot() {
		if r.Level >= slog.LevelWarn {
			warns = append(warns, r)
		}
	}
	return warns
}

// TestResolveServiceModelOverrides_NilRouterOrNoConfigIsNoOp verifies the
// no-op paths: nil router, absent section, empty section — all yield a nil set,
// and a nil/empty set reports misses (nil/пустой набор = no-op).
func TestResolveServiceModelOverrides_NilRouterOrNoConfigIsNoOp(t *testing.T) {
	router := modelOverrideValidatorTestRouter(t)

	if got := resolveServiceModelOverrides(nil, map[string]string{"title": "openai/gpt-4o"}, nil); got != nil {
		t.Errorf("nil router must resolve to nil, got %v", got)
	}
	if got := resolveServiceModelOverrides(router, nil, nil); got != nil {
		t.Errorf("nil configured map must resolve to nil, got %v", got)
	}
	if got := resolveServiceModelOverrides(router, map[string]string{}, nil); got != nil {
		t.Errorf("empty configured map must resolve to nil, got %v", got)
	}

	var empty *ServiceModelOverrides
	if _, ok := empty.Lookup("title"); ok {
		t.Error("nil set Lookup must miss")
	}
	if !empty.IsEmpty() {
		t.Error("nil set must be empty")
	}
	if !(&ServiceModelOverrides{}).IsEmpty() {
		t.Error("zero-value set must be empty")
	}
}

// TestResolveServiceModelOverrides_SplitsCompositeOnFirstSlash verifies a
// nested-slash model (HF-style id) resolves with the bare model intact: the id
// splits on the FIRST "/" only.
func TestResolveServiceModelOverrides_SplitsCompositeOnFirstSlash(t *testing.T) {
	router := modelOverrideValidatorTestRouter(t)
	capture := &recordCapture{}

	got := resolveServiceModelOverrides(router, map[string]string{
		"judge": "local-llm/qwen/qwen3-coder-30b",
	}, slog.New(capture))
	if got == nil {
		t.Fatal("known provider must resolve")
	}
	entry, ok := got.Lookup("judge")
	if !ok {
		t.Fatal("judge key must resolve")
	}
	if entry.Provider != "local-llm" {
		t.Errorf("provider = %q, want local-llm", entry.Provider)
	}
	if entry.Model != "qwen/qwen3-coder-30b" {
		t.Errorf("model = %q, want qwen/qwen3-coder-30b (split on first \"/\" only)", entry.Model)
	}
	if entry.Composite != "local-llm/qwen/qwen3-coder-30b" {
		t.Errorf("composite = %q, want local-llm/qwen/qwen3-coder-30b", entry.Composite)
	}
	if warns := warnRecords(capture); len(warns) != 0 {
		t.Errorf("known provider must not warn, got %d warn records", len(warns))
	}
}

// TestResolveServiceModelOverrides_UnknownProviderWarnsOnceAndDrops verifies
// the fail-soft contract: unknown-provider keys are dropped (session-model
// fallback) and produce exactly ONE aggregated Warn naming each dropped key,
// its provider, and the sorted known providers — regardless of how many keys
// missed.
func TestResolveServiceModelOverrides_UnknownProviderWarnsOnceAndDrops(t *testing.T) {
	router := modelOverrideValidatorTestRouter(t)
	capture := &recordCapture{}

	got := resolveServiceModelOverrides(router, map[string]string{
		"title":  "ghost-llm/gpt-4o",
		"judge":  "phantom-ai/qwen3:8b",
		"router": "openai/gpt-4o",
	}, slog.New(capture))
	if got == nil {
		t.Fatal("the one known key must resolve")
	}
	if _, ok := got.Lookup("title"); ok {
		t.Error("unknown-provider key must fall back to the session model (absent from the set)")
	}
	if _, ok := got.Lookup("judge"); ok {
		t.Error("second unknown-provider key must fall back too")
	}
	entry, ok := got.Lookup("router")
	if !ok || entry.Provider != "openai" {
		t.Errorf("known key must survive; got entry=%+v ok=%v", entry, ok)
	}

	warns := warnRecords(capture)
	if len(warns) != 1 {
		t.Fatalf("exactly ONE aggregated warn expected, got %d", len(warns))
	}
	msg := warns[0].Message
	if !containsAll(msg, "unknown provider") {
		t.Errorf("warn message %q must name the unknown-provider condition", msg)
	}
	attrs := warnAttrs(warns[0])
	// Keys visited in sorted order: judge before title.
	if !containsAll(attrs["unknown"], "judge → phantom-ai", "title → ghost-llm") {
		t.Errorf("warn attrs %q must name every dropped key with its provider", attrs["unknown"])
	}
	if attrs["known_providers"] != "local-llm, openai" {
		t.Errorf("known_providers = %q, want the sorted \"local-llm, openai\"", attrs["known_providers"])
	}
}

// TestResolveServiceModelOverrides_MalformedValueDropped verifies the
// defensive load-bypass path: a value that is not a full composite (bare name
// or "provider/") is dropped with its own warn while valid siblings survive.
func TestResolveServiceModelOverrides_MalformedValueDropped(t *testing.T) {
	router := modelOverrideValidatorTestRouter(t)
	capture := &recordCapture{}

	got := resolveServiceModelOverrides(router, map[string]string{
		"title":          "bare-model-name",
		"commit_message": "openai/",
		"router":         "openai/gpt-4o",
	}, slog.New(capture))
	if got == nil {
		t.Fatal("the valid key must resolve")
	}
	if _, ok := got.Lookup("title"); ok {
		t.Error("bare value must be dropped")
	}
	if _, ok := got.Lookup("commit_message"); ok {
		t.Error("empty-bare composite must be dropped")
	}
	if _, ok := got.Lookup("router"); !ok {
		t.Error("valid sibling key must survive")
	}
	for _, r := range warnRecords(capture) {
		attrs := warnAttrs(r)
		if attrs["key"] != "title" && attrs["key"] != "commit_message" {
			t.Errorf("unexpected warn for key %q", attrs["key"])
		}
	}
}

// TestResolveServiceModelOverrides_AllUnknownYieldsNilSet verifies that a
// section whose every key missed still degrades to the no-op set (nil) — the
// warn already fired.
func TestResolveServiceModelOverrides_AllUnknownYieldsNilSet(t *testing.T) {
	router := modelOverrideValidatorTestRouter(t)
	capture := &recordCapture{}

	got := resolveServiceModelOverrides(router, map[string]string{
		"title": "ghost-llm/gpt-4o",
	}, slog.New(capture))
	if got != nil {
		t.Errorf("all-unknown section must resolve to nil, got %v", got)
	}
	if warns := warnRecords(capture); len(warns) != 1 {
		t.Errorf("the aggregated warn must still fire, got %d warns", len(warns))
	}
}

// TestResolveServiceModelOverrides_NilLoggerSafe verifies the resolver is
// callable with a nil logger (no panic).
func TestResolveServiceModelOverrides_NilLoggerSafe(t *testing.T) {
	router := modelOverrideValidatorTestRouter(t)
	got := resolveServiceModelOverrides(router, map[string]string{
		"title": "ghost-llm/gpt-4o",
		"judge": "openai/gpt-4o",
	}, nil)
	if got == nil {
		t.Fatal("the known key must resolve even without a logger")
	}
	if _, ok := got.Lookup("title"); ok {
		t.Error("unknown-provider key must still drop")
	}
}

// TestPinnedModelCaller_ImplementsBothCallerInterfaces verifies the wrapper
// satisfies oneshot.Caller AND agent.LLMCaller (identical Call shape) and
// answers the judge's structural activeModelSource probe.
func TestPinnedModelCaller_ImplementsBothCallerInterfaces(t *testing.T) {
	inner := &capturingLLMCaller{resp: &llm.ChatResponse{}}
	pinned := newPinnedModelCaller(inner, "openai", "gpt-4o")

	// The assignments themselves are the interface conformance proof; drive
	// both views once so a mis-directed Call surfaces here too.
	var asOneshot oneshot.Caller = pinned
	var asAgent agent.LLMCaller = pinned
	_, _ = asOneshot.Call(context.Background(), llm.ChatRequest{})
	_, _ = asAgent.Call(context.Background(), llm.ChatRequest{})
	for i, req := range inner.captured() {
		if req.Provider != "openai" || req.Model != "gpt-4o" {
			t.Errorf("call %d through the interface: pin missing, got provider=%q model=%q", i+1, req.Provider, req.Model)
		}
	}

	src, ok := any(pinned).(interface{ ActiveModel() string })
	if !ok {
		t.Fatal("wrapper must answer the judge's structural ActiveModel() probe")
	}
	if got := src.ActiveModel(); got != "openai/gpt-4o" {
		t.Errorf("ActiveModel() = %q, want composite openai/gpt-4o", got)
	}
}

// TestPinnedModelCaller_PinImmuneToSetModel verifies the pin overwrites the
// request on EVERY call: even when the surrounding world switches the active
// model mid-task (simulated here by feeding each Call a different
// req.Model/req.Provider), the wrapper re-stamps the pinned pair both times.
func TestPinnedModelCaller_PinImmuneToSetModel(t *testing.T) {
	inner := &capturingLLMCaller{resp: &llm.ChatResponse{}}
	pinned := newPinnedModelCaller(inner, "local-llm", "qwen3:8b")

	// First call as the router would hand one over; second call after a
	// simulated Router.SetModel switched the active model.
	_, _ = pinned.Call(context.Background(), llm.ChatRequest{Model: "gpt-4o", Provider: "openai"})
	_, _ = pinned.Call(context.Background(), llm.ChatRequest{Model: "claude-3", Provider: "anthropic"})

	reqs := inner.captured()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 captured requests, got %d", len(reqs))
	}
	for i, req := range reqs {
		if req.Provider != "local-llm" {
			t.Errorf("call %d: req.Provider = %q, want the pin local-llm (rewritten every Call)", i+1, req.Provider)
		}
		if req.Model != "qwen3:8b" {
			t.Errorf("call %d: req.Model = %q, want the pin qwen3:8b (rewritten every Call)", i+1, req.Model)
		}
	}
}

// TestPinnedModelCaller_EmptyPinsDegradeToPassThrough verifies the defensive
// no-pin path: with both pins empty sp4rk returns inner unchanged, so requests
// pass through unmodified and ActiveModel degrades to "".
func TestPinnedModelCaller_EmptyPinsDegradeToPassThrough(t *testing.T) {
	inner := &capturingLLMCaller{resp: &llm.ChatResponse{}}
	pinned := newPinnedModelCaller(inner, "", "")

	_, _ = pinned.Call(context.Background(), llm.ChatRequest{Model: "inherit-me"})
	reqs := inner.captured()
	if len(reqs) != 1 || reqs[0].Model != "inherit-me" || reqs[0].Provider != "" {
		t.Errorf("no-pin wrapper must pass the request through untouched, got %+v", reqs)
	}
	if got := pinned.ActiveModel(); got != "" {
		t.Errorf("ActiveModel() = %q, want empty on the no-pin path", got)
	}
}

// TestOrchestratorStoresServiceModels verifies the build product carries the
// resolved set: deps → Orchestrator → ServiceModels() accessor (same pointer).
func TestOrchestratorStoresServiceModels(t *testing.T) {
	router := modelOverrideValidatorTestRouter(t)
	resolved := resolveServiceModelOverrides(router, map[string]string{"title": "openai/gpt-4o"}, nil)

	orch := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{ServiceModels: resolved})
	got := orch.ServiceModels()
	if got != resolved {
		t.Fatalf("ServiceModels() must return the resolved set from deps, got %v want %v", got, resolved)
	}
	entry, ok := got.Lookup("title")
	if !ok || entry.Composite != "openai/gpt-4o" {
		t.Errorf("accessor set Lookup(title) = %+v ok=%v, want the openai/gpt-4o pin", entry, ok)
	}

	var unconfigured *ServiceModelOverrides
	if orch := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{ServiceModels: unconfigured}); !orch.ServiceModels().IsEmpty() {
		t.Error("unconfigured deps must surface an empty set")
	}
}
