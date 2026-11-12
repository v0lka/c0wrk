package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/llm"
)

// modelOverrideProfile builds a Subagent Profile carrying a `model:` override.
func modelOverrideProfile(name, model string) *agents.Agent {
	return &agents.Agent{Metadata: agents.AgentMetadata{Name: name, Model: model}, Body: "b"}
}

// knownProvidersValidator mirrors the builder closure's shape: a composite
// must name a configured provider (here "local-llm"); bare names pass through
// unvalidated. Calls are recorded for no-call assertions.
type knownProvidersValidator struct {
	calls [][2]string
}

func (v *knownProvidersValidator) validate(agentName, model string) error {
	v.calls = append(v.calls, [2]string{agentName, model})
	provider, _, composite := llm.ParseCompositeModelID(model)
	if !composite {
		return nil
	}
	if provider != "local-llm" {
		return fmt.Errorf("profile %q: model override %q names unknown provider %q — known providers: local-llm, openai",
			agentName, model, provider)
	}
	return nil
}

// validateModelOverrideTasks runs validateDelegationTasks over a single
// agent-naming task with the given resolver and validator.
func validateModelOverrideTasks(t *testing.T, agentName, model string, validator ModelOverrideValidator) error {
	t.Helper()
	resolver := func(name string) (*agents.Agent, bool) {
		if name == agentName {
			return modelOverrideProfile(agentName, model), true
		}
		return nil, false
	}
	tasks := []DelegationTask{{ID: "d1", Summary: "s", Task: "do work", Agent: agentName}}
	return validateDelegationTasks(tasks, NewDelegationRegistry(), resolver, validator)
}

// TestValidateDelegationTasks_CompositeModelUnknownProviderFails verifies a
// composite override naming an unknown provider fails the WHOLE delegate call
// before any launch, with the task id prefix and the validator's
// profile/model/providers error.
func TestValidateDelegationTasks_CompositeModelUnknownProviderFails(t *testing.T) {
	validator := &knownProvidersValidator{}
	err := validateModelOverrideTasks(t, "reviewer", "ghost-llm/qwen3:8b", validator.validate)
	if err == nil {
		t.Fatal("expected fail-closed error for an unknown provider in a composite override")
	}
	for _, want := range []string{`task "d1"`, "reviewer", "ghost-llm/qwen3:8b", "ghost-llm", "local-llm, openai"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must name %q", err.Error(), want)
		}
	}
	if len(validator.calls) != 1 || validator.calls[0] != [2]string{"reviewer", "ghost-llm/qwen3:8b"} {
		t.Errorf("validator calls = %v, want [[reviewer ghost-llm/qwen3:8b]]", validator.calls)
	}
}

// TestValidateDelegationTasks_CompositeModelKnownProviderPasses verifies a
// composite naming a configured provider validates cleanly.
func TestValidateDelegationTasks_CompositeModelKnownProviderPasses(t *testing.T) {
	validator := &knownProvidersValidator{}
	if err := validateModelOverrideTasks(t, "reviewer", "local-llm/qwen3:8b", validator.validate); err != nil {
		t.Fatalf("known provider must validate: %v", err)
	}
	if len(validator.calls) != 1 {
		t.Errorf("validator calls = %v, want exactly one", validator.calls)
	}
}

// TestValidateDelegationTasks_BareModelConsultsValidator verifies a bare model
// name reaches the validator unchanged — this layer is policy-neutral, so the
// stub's acceptance is what lets the call pass. The production validator
// (core/builder.go, ADR-122) rejects bare names and fails the whole call.
func TestValidateDelegationTasks_BareModelConsultsValidator(t *testing.T) {
	validator := &knownProvidersValidator{}
	if err := validateModelOverrideTasks(t, "reviewer", "qwen3:8b", validator.validate); err != nil {
		t.Fatalf("bare model must consult the validator without failing here: %v", err)
	}
	if len(validator.calls) != 1 || validator.calls[0] != [2]string{"reviewer", "qwen3:8b"} {
		t.Errorf("validator calls = %v, want [[reviewer qwen3:8b]] (consulted, accepted by the stub)", validator.calls)
	}
}

// TestValidateDelegationTasks_EmptyModelNotValidated verifies a profile
// without a model override never reaches the validator.
func TestValidateDelegationTasks_EmptyModelNotValidated(t *testing.T) {
	validator := &knownProvidersValidator{}
	if err := validateModelOverrideTasks(t, "reviewer", "", validator.validate); err != nil {
		t.Fatalf("empty model must pass: %v", err)
	}
	if len(validator.calls) != 0 {
		t.Errorf("validator must not be consulted for an empty model, got %v", validator.calls)
	}
}

// TestValidateDelegationTasks_NilValidatorSkips verifies the nil-safe skip:
// without a wired validator a composite override imposes no delegate-time
// rejection (buildSubAgentTask re-checks defensively at launch).
func TestValidateDelegationTasks_NilValidatorSkips(t *testing.T) {
	if err := validateModelOverrideTasks(t, "reviewer", "ghost-llm/qwen3:8b", nil); err != nil {
		t.Fatalf("nil validator must skip model validation, got: %v", err)
	}
}

// TestModelOverrideValidatorContext verifies the context plumbing: injection,
// nil-safe extraction, and (like the agent resolver) survival is the caller's
// concern — the value itself round-trips.
func TestModelOverrideValidatorContext(t *testing.T) {
	if got := ModelOverrideValidatorFrom(context.Background()); got != nil {
		t.Error("absent validator must extract as nil")
	}
	// A stored nil must also extract as nil, so callers never see a typed-nil.
	ctx := WithModelOverrideValidator(context.Background(), nil)
	if got := ModelOverrideValidatorFrom(ctx); got != nil {
		t.Error("stored nil validator must extract as nil")
	}
	validator := &knownProvidersValidator{}
	ctx = WithModelOverrideValidator(ctx, validator.validate)
	if got := ModelOverrideValidatorFrom(ctx); got == nil {
		t.Fatal("injected validator must extract non-nil")
	} else if err := got("reviewer", "local-llm/m"); err != nil {
		t.Errorf("extracted validator failed: %v", err)
	}
}
