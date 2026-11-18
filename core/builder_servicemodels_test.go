package core

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/reflector"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
)

// coreAgentsUnderTest is what buildCoreAgents wires, with the concrete sp4rk
// consumer types the tests drive.
type coreAgentsUnderTest struct {
	router    *router.Router
	reflector *reflector.Reflector
}

// buildCoreAgentsUnderTest wires buildCoreAgents with a capturing caller and
// the given resolved overrides; the returned router/reflector issue their LLM
// calls straight into the capture (through the production logging/dump
// layers), so tests assert the exact wire shape each consumer produced. The
// canned response parses as both a RoutingDecision and a Reflection.
func buildCoreAgentsUnderTest(t *testing.T, overrides *ServiceModelOverrides) (*capturingLLMCaller, coreAgentsUnderTest) {
	t.Helper()
	capture := &capturingLLMCaller{
		resp: &llm.ChatResponse{
			Message: llm.Message{Role: "assistant", Content: `{"domain":"general","complexity":2,"summary":"ok"}`},
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := &OrchestratorBuilder{logger: logger}
	cfg := &BuilderConfig{}
	coreRouter, coreReflector, err := b.buildCoreAgents(capture, cfg, nil, logger, io.Discard, overrides)
	if err != nil {
		t.Fatalf("buildCoreAgents failed: %v", err)
	}
	if coreRouter == nil || coreReflector == nil {
		t.Fatal("buildCoreAgents returned nil router/reflector for a non-nil caller")
	}
	return capture, coreAgentsUnderTest{router: coreRouter, reflector: coreReflector}
}

// lastCaptured returns the most recent captured request; ok=false when none.
func lastCaptured(capture *capturingLLMCaller) (llm.ChatRequest, bool) {
	reqs := capture.captured()
	if len(reqs) == 0 {
		return llm.ChatRequest{}, false
	}
	return reqs[len(reqs)-1], true
}

// TestBuildCoreAgents_RouteRequestsCarryOverridePin verifies routing calls go
// out pinned to the configured provider/model: every Route request carries the
// override pin plus the routing budget class, and the decision still parses.
func TestBuildCoreAgents_RouteRequestsCarryOverridePin(t *testing.T) {
	overrides := resolveServiceModelOverrides(modelOverrideValidatorTestRouter(t), map[string]string{
		serviceModelKeyRouter: "local-llm/qwen3:8b",
	}, nil)
	if overrides == nil {
		t.Fatal("router override must resolve")
	}
	capture, agents := buildCoreAgentsUnderTest(t, overrides)

	decision, err := agents.router.Route(context.Background(), "classify me", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route failed: %v", err)
	}
	if decision == nil {
		t.Fatal("Route returned a nil decision for a parseable response")
	}

	req, ok := lastCaptured(capture)
	if !ok {
		t.Fatal("Route issued no LLM request")
	}
	if req.Provider != "local-llm" {
		t.Errorf("Route request Provider = %q, want pinned %q", req.Provider, "local-llm")
	}
	if req.Model != "qwen3:8b" {
		t.Errorf("Route request Model = %q, want pinned %q", req.Model, "qwen3:8b")
	}
	if req.CallPurpose != llm.CallPurposeRouting {
		t.Errorf("Route request CallPurpose = %q, want %q preserved", req.CallPurpose, llm.CallPurposeRouting)
	}
}

// TestBuildCoreAgents_ReflectorRequestsCarryOverridePin verifies reflection
// calls go out pinned to their own configured provider/model, independent of
// the router pin, with the routing budget class preserved.
func TestBuildCoreAgents_ReflectorRequestsCarryOverridePin(t *testing.T) {
	overrides := resolveServiceModelOverrides(modelOverrideValidatorTestRouter(t), map[string]string{
		serviceModelKeyReflector: "openai/gpt-4o",
	}, nil)
	if overrides == nil {
		t.Fatal("reflector override must resolve")
	}
	capture, agents := buildCoreAgentsUnderTest(t, overrides)

	if _, err := agents.reflector.Reflect(context.Background(), nil, nil, nil); err != nil {
		t.Fatalf("Reflect failed: %v", err)
	}

	req, ok := lastCaptured(capture)
	if !ok {
		t.Fatal("Reflect issued no LLM request")
	}
	if req.Provider != "openai" {
		t.Errorf("Reflect request Provider = %q, want pinned %q", req.Provider, "openai")
	}
	if req.Model != "gpt-4o" {
		t.Errorf("Reflect request Model = %q, want pinned %q", req.Model, "gpt-4o")
	}
	if req.CallPurpose != llm.CallPurposeRouting {
		t.Errorf("Reflect request CallPurpose = %q, want %q preserved", req.CallPurpose, llm.CallPurposeRouting)
	}
}

// TestBuildCoreAgents_PerConsumerPinsAreIndependent verifies two consumers
// configured with different pins each carry their OWN pin on the wire — the
// wrap is per consumer, never shared through one common caller.
func TestBuildCoreAgents_PerConsumerPinsAreIndependent(t *testing.T) {
	overrides := resolveServiceModelOverrides(modelOverrideValidatorTestRouter(t), map[string]string{
		serviceModelKeyRouter:    "local-llm/qwen3:8b",
		serviceModelKeyReflector: "openai/gpt-4o",
	}, nil)
	if overrides == nil {
		t.Fatal("overrides must resolve")
	}
	capture, agents := buildCoreAgentsUnderTest(t, overrides)

	if _, err := agents.router.Route(context.Background(), "classify me", nil, nil, nil); err != nil {
		t.Fatalf("Route failed: %v", err)
	}
	if _, err := agents.reflector.Reflect(context.Background(), nil, nil, nil); err != nil {
		t.Fatalf("Reflect failed: %v", err)
	}

	reqs := capture.captured()
	if len(reqs) != 2 {
		t.Fatalf("expected exactly 2 captured requests, got %d", len(reqs))
	}
	if reqs[0].Provider != "local-llm" || reqs[0].Model != "qwen3:8b" {
		t.Errorf("router request = %s/%s, want local-llm/qwen3:8b", reqs[0].Provider, reqs[0].Model)
	}
	if reqs[1].Provider != "openai" || reqs[1].Model != "gpt-4o" {
		t.Errorf("reflector request = %s/%s, want openai/gpt-4o", reqs[1].Provider, reqs[1].Model)
	}
}

// TestBuildCoreAgents_WithoutOverrideRequestsKeepPriorShape verifies the
// no-override path is byte-identical to the pre-override wiring: requests go
// out with empty Provider/Model (the router resolves the active model), and
// overrideWrap passes the caller through as the SAME instance — no extra layer.
func TestBuildCoreAgents_WithoutOverrideRequestsKeepPriorShape(t *testing.T) {
	capture, agents := buildCoreAgentsUnderTest(t, nil)

	if _, err := agents.router.Route(context.Background(), "classify me", nil, nil, nil); err != nil {
		t.Fatalf("Route failed: %v", err)
	}
	if _, err := agents.reflector.Reflect(context.Background(), nil, nil, nil); err != nil {
		t.Fatalf("Reflect failed: %v", err)
	}

	for i, req := range capture.captured() {
		if req.Provider != "" || req.Model != "" {
			t.Errorf("request %d carried Provider=%q Model=%q; without an override both must stay empty", i, req.Provider, req.Model)
		}
	}

	// Identity passthrough: overrideWrap without a resolved pin must return
	// the caller itself, not a wrapper around it.
	probe := &capturingLLMCaller{}
	if got := overrideWrap(probe, nil, serviceModelKeyRouter); got != agent.LLMCaller(probe) {
		t.Error("overrideWrap with a nil set must return the caller unchanged")
	}
	if got := overrideWrap(probe, &ServiceModelOverrides{}, serviceModelKeyReflector); got != agent.LLMCaller(probe) {
		t.Error("overrideWrap with an empty set must return the caller unchanged")
	}
	// ...and with a resolved pin it must NOT be the same instance (a wrap happened).
	overrides := resolveServiceModelOverrides(modelOverrideValidatorTestRouter(t), map[string]string{
		serviceModelKeyRouter: "local-llm/qwen3:8b",
	}, nil)
	if wrapped := overrideWrap(probe, overrides, serviceModelKeyRouter); wrapped == agent.LLMCaller(probe) {
		t.Error("overrideWrap with a resolved pin must wrap the caller")
	}
}
