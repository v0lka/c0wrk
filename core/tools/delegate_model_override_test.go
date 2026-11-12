package tools

import (
	"context"
	"encoding/json"
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

func (v *knownProvidersValidator) validate(source, model string) error {
	v.calls = append(v.calls, [2]string{source, model})
	provider, _, composite := llm.ParseCompositeModelID(model)
	if !composite {
		return nil
	}
	if provider != "local-llm" {
		return fmt.Errorf("%s: model override %q names unknown provider %q — known providers: local-llm, openai",
			source, model, provider)
	}
	return nil
}

// mandatoryCompositeValidator mirrors the production builder closure's FULL
// acceptance rule: bare names and empty-bare composites ("provider/") are
// rejected outright; a composite must name the configured provider
// "local-llm".
func mandatoryCompositeValidator(source, model string) error {
	provider, bare, composite := llm.ParseCompositeModelID(model)
	if !composite {
		return fmt.Errorf("%s: model override %q must be a full \"provider/model\" identifier — bare model names are not accepted", source, model)
	}
	if bare == "" {
		return fmt.Errorf("%s: model override %q has an empty bare model — a composite id is \"provider/model\" and the part after \"/\" must name a model", source, model)
	}
	if provider != "local-llm" {
		return fmt.Errorf("%s: model override %q names unknown provider %q — known providers: local-llm, openai", source, model, provider)
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

// validateTaskModelTasks runs validateDelegationTasks over a single
// task-model-carrying task (no agent profile).
func validateTaskModelTasks(t *testing.T, id, model string, validator ModelOverrideValidator) error {
	t.Helper()
	tasks := []DelegationTask{{ID: id, Summary: "s", Task: "do work", Model: model}}
	return validateDelegationTasks(tasks, NewDelegationRegistry(), nil, validator)
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
	if len(validator.calls) != 1 || validator.calls[0] != [2]string{"profile reviewer", "ghost-llm/qwen3:8b"} {
		t.Errorf("validator calls = %v, want [[profile reviewer ghost-llm/qwen3:8b]]", validator.calls)
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
	if len(validator.calls) != 1 || validator.calls[0] != [2]string{"profile reviewer", "qwen3:8b"} {
		t.Errorf("validator calls = %v, want [[profile reviewer qwen3:8b]] (consulted, accepted by the stub)", validator.calls)
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
	} else if err := got("profile reviewer", "local-llm/m"); err != nil {
		t.Errorf("extracted validator failed: %v", err)
	}
}

// --- Task-level model overrides (ADR-123) ---

// TestValidateDelegationTasks_TaskModelUnknownProviderFails verifies an
// explicit task-level `model` naming an unknown provider fails the WHOLE
// delegate call before any launch, with the task id, the model, the offending
// provider, and the known providers named.
func TestValidateDelegationTasks_TaskModelUnknownProviderFails(t *testing.T) {
	validator := &knownProvidersValidator{}
	err := validateTaskModelTasks(t, "del_7", "ghost-llm/qwen3:8b", validator.validate)
	if err == nil {
		t.Fatal("expected fail-closed error for an unknown provider in a task-level model")
	}
	for _, want := range []string{"del_7", "ghost-llm/qwen3:8b", "ghost-llm", "local-llm, openai"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must name %q", err.Error(), want)
		}
	}
	if len(validator.calls) != 1 || validator.calls[0] != [2]string{"task del_7", "ghost-llm/qwen3:8b"} {
		t.Errorf("validator calls = %v, want [[task del_7 ghost-llm/qwen3:8b]]", validator.calls)
	}
}

// TestValidateDelegationTasks_TaskModelKnownProviderPasses verifies a task
// model naming a configured provider validates cleanly and consults the
// validator with the task source.
func TestValidateDelegationTasks_TaskModelKnownProviderPasses(t *testing.T) {
	validator := &knownProvidersValidator{}
	if err := validateTaskModelTasks(t, "del_1", "local-llm/qwen3:8b", validator.validate); err != nil {
		t.Fatalf("known provider must validate: %v", err)
	}
	if len(validator.calls) != 1 || validator.calls[0] != [2]string{"task del_1", "local-llm/qwen3:8b"} {
		t.Errorf("validator calls = %v, want exactly one [[task del_1 local-llm/qwen3:8b]]", validator.calls)
	}
}

// TestValidateDelegationTasks_TaskModelBareRejected verifies the
// mandatory-composite rule fires for a task-level model: a bare name is
// rejected and the whole call fails, with the task source in the error.
func TestValidateDelegationTasks_TaskModelBareRejected(t *testing.T) {
	err := validateTaskModelTasks(t, "del_1", "qwen3:8b", mandatoryCompositeValidator)
	if err == nil {
		t.Fatal("bare task model must be rejected")
	}
	for _, want := range []string{"task del_1", "qwen3:8b", `must be a full "provider/model" identifier`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must name %q", err.Error(), want)
		}
	}
}

// TestValidateDelegationTasks_TaskModelEmptyBareRejected verifies a composite
// with an empty model part ("provider/") is rejected for a task-level model.
func TestValidateDelegationTasks_TaskModelEmptyBareRejected(t *testing.T) {
	err := validateTaskModelTasks(t, "del_1", "local-llm/", mandatoryCompositeValidator)
	if err == nil {
		t.Fatal("empty-bare composite task model must be rejected")
	}
	for _, want := range []string{"task del_1", "local-llm/", "empty bare model"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must name %q", err.Error(), want)
		}
	}
}

// TestValidateDelegationTasks_TaskModelNilValidatorSkips verifies the nil-safe
// skip: without a wired validator a task-level model imposes no delegate-time
// rejection (buildSubAgentTask re-checks or warn-ignores at launch).
func TestValidateDelegationTasks_TaskModelNilValidatorSkips(t *testing.T) {
	if err := validateTaskModelTasks(t, "del_1", "ghost-llm/qwen3:8b", nil); err != nil {
		t.Fatalf("nil validator must skip task-model validation, got: %v", err)
	}
}

// TestValidateDelegationTasks_EmptyTaskModelNotValidated verifies a task
// without a model never reaches the validator — omitting `model` is the
// documented "profile pin, else session model" behavior.
func TestValidateDelegationTasks_EmptyTaskModelNotValidated(t *testing.T) {
	validator := &knownProvidersValidator{}
	if err := validateTaskModelTasks(t, "del_1", "", validator.validate); err != nil {
		t.Fatalf("empty task model must pass: %v", err)
	}
	if len(validator.calls) != 0 {
		t.Errorf("validator must not be consulted for an empty task model, got %v", validator.calls)
	}
}

// TestDelegationTaskModelSurvivesSpecRoundtrip pins the resume contract: the
// task-level `model` rides RegisterTask's durable DelegationSpec (the
// unit-ledger rebuild input), so a resume relaunches the subagent with the
// same override instead of silently falling back to the session model.
func TestDelegationTaskModelSurvivesSpecRoundtrip(t *testing.T) {
	spec := DelegationSpec{Task: DelegationTask{
		ID: "del_1", Summary: "s", Task: "do work", Mode: "blocking", Model: "local-llm/qwen3:8b",
	}}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	var got DelegationSpec
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}
	if got.Task.Model != "local-llm/qwen3:8b" {
		t.Errorf("spec task model = %q, want %q (resume must carry the task model)", got.Task.Model, "local-llm/qwen3:8b")
	}

	// A task without a model must not emit the key at all (omitempty).
	raw, err = json.Marshal(DelegationSpec{Task: DelegationTask{ID: "d", Summary: "s", Task: "t"}})
	if err != nil {
		t.Fatalf("marshal bare spec: %v", err)
	}
	if strings.Contains(string(raw), `"model"`) {
		t.Errorf("model key must be omitted when unset, got %s", raw)
	}
}
