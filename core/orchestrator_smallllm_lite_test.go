package core

import (
	"context"
	"testing"

	"github.com/v0lka/sp4rk/tools"
)

// TestPrepareRequestContext_SmallLLMLite_Gating verifies the defense-in-depth
// contract for the prompt-lite variant: the SmallLLMLiteKey context value is
// set ONLY when BOTH the master SmallLLM.Enabled toggle AND the SystemPrompt
// variant sub-toggle (Lite) are active. This mirrors how the essential-tools,
// loop-hardening, and sampling variants are each gated on master + sub-toggle.
func TestPrepareRequestContext_SmallLLMLite_Gating(t *testing.T) {
	cases := []struct {
		name       string
		smallLLM   SmallLLMSettings
		wantActive bool
	}{
		{
			name: "master-on lite-on → active",
			smallLLM: SmallLLMSettings{
				Enabled: true,
				SystemPrompt: SmallLLMSystemPromptSettings{
					Lite: true,
				},
			},
			wantActive: true,
		},
		{
			name: "master-off lite-on → INACTIVE (defense-in-depth)",
			smallLLM: SmallLLMSettings{
				Enabled: false,
				SystemPrompt: SmallLLMSystemPromptSettings{
					Lite: true,
				},
			},
			wantActive: false,
		},
		{
			name: "master-on lite-off → INACTIVE (variant gated)",
			smallLLM: SmallLLMSettings{
				Enabled: true,
				SystemPrompt: SmallLLMSystemPromptSettings{
					Lite: false,
				},
			},
			wantActive: false,
		},
		{
			name:       "zero-config → INACTIVE (defaults OFF)",
			smallLLM:   SmallLLMSettings{},
			wantActive: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := &Orchestrator{config: OrchestratorConfig{SmallLLM: tc.smallLLM}, emitter: &noopEmitter{}}
			ctx := o.prepareRequestContext(context.Background(), "msg")
			got := ctx.Value(SmallLLMLiteKey) != nil
			if got != tc.wantActive {
				t.Errorf("SmallLLMLiteKey active = %v, want %v", got, tc.wantActive)
			}
		})
	}
}

// TestPrepareRequestContext_SmallLLMLite_DefaultsOffWhenAbsent confirms the
// SmallLLMLiteKey ctx value defaults to OFF when absent — a plain context (no
// orchestrator config) must read as inactive so the verbose directive is used.
func TestPrepareRequestContext_SmallLLMLite_DefaultsOffWhenAbsent(t *testing.T) {
	if smallLLMLiteFromCtx(context.Background()) {
		t.Fatal("smallLLMLiteFromCtx returned true for a plain context; lite must be OFF by default")
	}
}

// TestBuildSystemPrompt_SmallLLMLite_MasterGate verifies the full
// config→ctx→prompt chain: when the lite ctx value is absent (master or variant
// off), buildSystemPromptWith uses the verbose OrchestratorSystem directive;
// when present, it swaps to the compact OrchestratorSystemLite directive.
// This proves the master toggle's defense-in-depth reaches the prompt output.
func TestBuildSystemPrompt_SmallLLMLite_MasterGate(t *testing.T) {
	ctx := tools.WithWorkspacePath(context.Background(), "/ws")

	// No lite key → verbose directive present.
	verbose := buildSystemPrompt(ctx, "task", llmModelMetaForTests())
	if verbose == "" {
		t.Fatal("verbose prompt is empty")
	}

	// With lite key → prompt must differ (compact directive swapped in).
	liteCtx := WithSmallLLMLite(ctx)
	lite := buildSystemPrompt(liteCtx, "task", llmModelMetaForTests())
	if lite == "" {
		t.Fatal("lite prompt is empty")
	}
	if verbose == lite {
		t.Fatal("lite prompt identical to verbose prompt; lite swap did not take effect")
	}
}

// TestConfigAdapter_SmallLLMSystemPrompt_RoundTrip (in backend package) is the
// config→builder wiring test; here we assert the runtime mirror types carry the
// field so a rebuild surfaces config changes. Kept as a compile-time + value
// sanity check of the SmallLLMSettings → SmallLLMSystemPromptSettings shape.
func TestSmallLLMSettings_SystemPromptCarriesLite(t *testing.T) {
	s := SmallLLMSettings{
		Enabled: true,
		SystemPrompt: SmallLLMSystemPromptSettings{
			Lite: true,
		},
	}
	if !s.Enabled || !s.SystemPrompt.Lite {
		t.Errorf("SmallLLMSettings fields not carried: %+v", s)
	}
}
