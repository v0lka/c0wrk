package core

import (
	"context"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/tools"
)

// This file holds the SmallLLM INTEGRATION tests. Unlike the per-variant unit
// tests (orchestrator_smallllm_test.go, systemprompt_test.go) that drive each
// function in isolation with a hand-crafted context, these tests exercise the
// full composition through the orchestrator's own prepareRequestContext →
// buildSystemPrompt → applySmallLLMToolFilter chain, driven by a real
// OrchestratorConfig. They prove the variants COMPOSE correctly when the master
// toggle is ON (lite prompt + few-shot + injection-defense all present AND the
// tool set reduced in one coherent scenario) and that master OFF is a complete
// no-op (byte-identical prompt + untouched tool set vs. a zero-SmallLLM
// baseline).
//
// The integration value over the unit tests is that the unit tests set ctx keys
// directly (WithSmallLLMLite / InjectionDefenseKey), bypassing the orchestrator
// wiring. These tests let prepareRequestContext derive the ctx from config and
// then assert the downstream prompt + tool behaviors compose — closing the gap
// between "the config toggles are gated correctly" and "the gated toggles
// actually reach the prompt output and the tool set together".

// TestSmallLLM_Integration_MasterOn_ComposesLitePromptFewShotInjectionDefenseAndReducedTools
// is the end-to-end composition test for the master-ON case. With a full profile
// (master ON + essential-tools + prompt-lite sub-toggles + injection defense),
// the orchestrator's prepareRequestContext must set BOTH the lite key and the
// injection-defense key, buildSystemPrompt must then assemble a prompt carrying
// the lite directive + few-shot examples + the FULL injection-defense content
// (strict constraint: lite never strips injection defense), and
// applySmallLLMToolFilter must reduce the conductor's tool set.
func TestSmallLLM_Integration_MasterOn_ComposesLitePromptFewShotInjectionDefenseAndReducedTools(t *testing.T) {
	o := &Orchestrator{
		config: OrchestratorConfig{
			InjectionDefenseEnabled: true,
			SmallLLM: SmallLLMSettings{
				Enabled: true,
				EssentialTools: SmallLLMEssentialSettings{
					Enabled:       true,
					AlwaysPresent: []string{"store_fact"},
				},
				SystemPrompt: SmallLLMSystemPromptSettings{
					Lite:              true,
					FewShot:           true,
					ReasoningScaffold: true,
				},
			},
		},
		emitter: &noopEmitter{},
	}

	ctx := tools.WithWorkspacePath(context.Background(), "/ws")
	reqCtx := o.prepareRequestContext(ctx, "fix the bug in main.go")
	meta := llmModelMetaForTests()

	// Defense-in-depth wiring: both ctx keys set from config.
	if reqCtx.Value(SmallLLMLiteKey) == nil {
		t.Fatal("master ON: prepareRequestContext did not set SmallLLMLiteKey")
	}
	if reqCtx.Value(InjectionDefenseKey) == nil {
		t.Fatal("master ON: prepareRequestContext did not set InjectionDefenseKey")
	}

	// ── PROMPT: lite directive + few-shot + injection defense all present ──
	prompt := buildSystemPrompt(reqCtx, "fix the bug", meta)
	if !strings.Contains(prompt, "Thought Scaffold") {
		t.Error("integration: lite directive (Thought Scaffold) missing from master-ON prompt")
	}
	if !strings.Contains(prompt, "Worked Examples — Correct ReAct Cycles") {
		t.Error("integration: few-shot block missing from master-ON prompt")
	}
	if !strings.Contains(prompt, "Epistemic Discipline") {
		t.Error("integration: verification mandate (Epistemic Discipline) missing from master-ON prompt")
	}
	// STRICT CONSTRAINT: full injection-defense content present verbatim even
	// with the lite directive active — the lite swap never removes it.
	for _, marker := range []string{
		"Untrusted Content Policy",
		"Data Exfiltration Prevention",
		"indirect prompt injection",
	} {
		if !strings.Contains(prompt, marker) {
			t.Errorf("integration: injection-defense marker %q missing — strict constraint violated", marker)
		}
	}
	// The verbose directive that the lite swap REPLACES must be absent.
	if strings.Contains(prompt, "Progress Tracking — Three Levels") {
		t.Error("integration: verbose OrchestratorSystem leaked into the lite prompt")
	}

	// ── TOOLS: narrowed by router-matched + always-present + protected + MCP ──
	in := smallLLMTestTools()
	got := o.applySmallLLMToolFilter(in, &router.RoutingDecision{
		Domain:       router.DomainCode,
		MatchedTools: []string{"read_file", "write_file", "bash_exec"},
	})
	if len(got) >= len(in) {
		t.Errorf("integration: master-ON tool set not reduced: got %d tools, input %d", len(got), len(in))
	}
	set := make(map[string]struct{}, len(got))
	for _, n := range sortedToolNames(got) {
		set[n] = struct{}{}
	}
	// Matched code tools + always-present (store_fact) + protected (finish) +
	// MCP all survive SelectTools.
	for _, keep := range []string{"read_file", "write_file", "bash_exec", "finish", "store_fact", "mcp_linter"} {
		if _, ok := set[keep]; !ok {
			t.Errorf("integration: %q should survive selection; got %v", keep, sortedToolNames(got))
		}
	}
	// web_search is neither matched nor always-present nor protected nor MCP,
	// so SelectTools drops it.
	if _, ok := set["web_search"]; ok {
		t.Errorf("integration: web_search should be dropped (not matched/protected/MCP); got %v", sortedToolNames(got))
	}
}

// TestSmallLLM_Integration_MasterOff_ByteIdenticalPromptAndFullToolset is the
// master-OFF regression guard. With all variant sub-toggles ON but the master
// toggle OFF, defense-in-depth must gate every variant: prepareRequestContext
// sets NEITHER ctx key, the assembled prompt is BYTE-IDENTICAL to one built
// from a zero-SmallLLM config, and applySmallLLMToolFilter returns the full
// tool set untouched. This proves enabling variant sub-toggles (while the
// master is off) changes absolutely nothing about prompt output or tool set.
func TestSmallLLM_Integration_MasterOff_ByteIdenticalPromptAndFullToolset(t *testing.T) {
	// Variants ON, master OFF.
	oOff := &Orchestrator{
		config: OrchestratorConfig{
			InjectionDefenseEnabled: true,
			SmallLLM: SmallLLMSettings{
				Enabled: false, // master gate OFF
				EssentialTools: SmallLLMEssentialSettings{
					Enabled:       true,
					AlwaysPresent: []string{"read_file"},
				},
				SystemPrompt: SmallLLMSystemPromptSettings{Lite: true},
			},
		},
		emitter: &noopEmitter{},
	}
	// Pre-profile baseline: a zero-SmallLLM config.
	oBaseline := &Orchestrator{
		config:  OrchestratorConfig{InjectionDefenseEnabled: true},
		emitter: &noopEmitter{},
	}

	ctx := tools.WithWorkspacePath(context.Background(), "/ws")
	meta := llmModelMetaForTests()
	msg := "fix the bug"

	offCtx := oOff.prepareRequestContext(ctx, msg)
	baselineCtx := oBaseline.prepareRequestContext(ctx, msg)

	// Defense-in-depth: master OFF gates the lite key away.
	if offCtx.Value(SmallLLMLiteKey) != nil {
		t.Fatal("master OFF: prepareRequestContext set SmallLLMLiteKey — defense-in-depth broken")
	}
	// Injection defense is independent of SmallLLM, so it is set in both.
	if offCtx.Value(InjectionDefenseKey) == nil {
		t.Fatal("master OFF: InjectionDefenseKey should still be set (independent of SmallLLM)")
	}

	offPrompt := buildSystemPrompt(offCtx, msg, meta)
	baselinePrompt := buildSystemPrompt(baselineCtx, msg, meta)

	// BYTE-IDENTICAL regression guard.
	if offPrompt != baselinePrompt {
		t.Fatalf(
			"master OFF prompt diverges from zero-SmallLLM baseline: "+
				"off=%d bytes, baseline=%d bytes",
			len(offPrompt), len(baselinePrompt),
		)
	}
	// Verbose directive present, lite directive absent.
	if !strings.Contains(offPrompt, "Progress Tracking — Three Levels") {
		t.Error("master OFF prompt missing verbose OrchestratorSystem content")
	}
	if strings.Contains(offPrompt, "Thought Scaffold") {
		t.Error("master OFF prompt leaked the lite directive into the baseline")
	}
	if strings.Contains(offPrompt, "Worked Examples — Correct ReAct Cycles") {
		t.Error("master OFF prompt leaked the lite few-shot block into the baseline")
	}

	// TOOLS: full set returned untouched.
	in := smallLLMTestTools()
	got := oOff.applySmallLLMToolFilter(in, &router.RoutingDecision{Domain: router.DomainCode})
	if len(got) != len(in) {
		t.Errorf("master OFF: expected full tool set (%d tools untouched), got %d", len(in), len(got))
	}
}
