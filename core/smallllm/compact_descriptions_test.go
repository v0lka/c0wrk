package smallllm_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/smallllm"
	tools2 "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// realBuiltinDescriptors builds descriptors for the subset of builtins that
// can be instantiated without runtime dependencies. Their descriptions are the
// real full rubric texts the compact set is measured against.
func realBuiltinDescriptors(t *testing.T) []tools.ToolDescriptor {
	t.Helper()

	bashTool, err := builtins.NewBashExecTool(nil)
	if err != nil {
		t.Fatalf("NewBashExecTool: %v", err)
	}

	impls := []tools.Tool{
		bashTool,
		builtins.NewReadFileTool(),
		builtins.NewWriteFileTool(),
		builtins.NewEditFileTool(),
		builtins.NewListDirectoryTool(),
		builtins.NewCreateDirectoryTool(),
		builtins.NewDeleteFileTool(),
		builtins.NewDeleteDirectoryTool(),
		builtins.NewGlobTool(),
		builtins.NewRipgrepTool(),
		builtins.NewBatchTool(),
		builtins.NewToolResultReadTool(),
		builtins.NewReadStepOutputTool(),
		builtins.NewListStepOutputsTool(),
		builtins.NewReadFinalResultTool(),
		builtins.NewUpdateChecklistTool(),
		builtins.NewStoreFactTool(),
		builtins.NewSearchFactsTool(),
		builtins.NewReadAttachmentTool(),
	}

	out := make([]tools.ToolDescriptor, 0, len(impls))
	for _, tool := range impls {
		out = append(out, tools.ToolDescriptor{
			Name:        tool.Name(),
			Description: tool.Description(),
		})
	}
	return out
}

// c0wrkToolImpls returns the c0wrk orchestration tool implementations (all
// instantiable without runtime dependencies), exposing both descriptions and
// input schemas.
func c0wrkToolImpls(t *testing.T) []tools.Tool {
	t.Helper()

	return []tools.Tool{
		tools2.NewAskUserTool(nil),
		tools2.NewExecutePlanTool(),
		tools2.NewReflectTool(),
		tools2.NewDelegateTool(),
		tools2.NewCancelDelegationTool(),
		tools2.NewDeclarePlanTool(nil),
		tools2.NewDeclareStepCompleteTool(),
		tools2.NewDeclareVerificationTool(),
		tools2.NewProposeGoalTool(),
		tools2.NewDeclareGoalStatusTool(),
	}
}

// c0wrkBuiltinDescriptors adds the c0wrk orchestration tools (all
// instantiable without runtime dependencies) to the real-descriptor set.
func c0wrkBuiltinDescriptors(t *testing.T) []tools.ToolDescriptor {
	t.Helper()

	impls := c0wrkToolImpls(t)

	out := make([]tools.ToolDescriptor, 0, len(impls))
	for _, tool := range impls {
		out = append(out, tools.ToolDescriptor{
			Name:        tool.Name(),
			Description: tool.Description(),
		})
	}
	return out
}

// allRealBuiltinDescriptors is the union of sp4rk builtins and c0wrk
// orchestration tools.
func allRealBuiltinDescriptors(t *testing.T) []tools.ToolDescriptor {
	t.Helper()
	return append(realBuiltinDescriptors(t), c0wrkBuiltinDescriptors(t)...)
}

// TestCompactDescriptionsShorterThanFull: with the mode enabled, every known
// builtin's compact description must be strictly shorter than its real full
// rubric description.
func TestCompactDescriptionsShorterThanFull(t *testing.T) {
	in := allRealBuiltinDescriptors(t)
	if len(in) < 25 {
		t.Fatalf("expected at least 15 real builtin descriptors, got %d", len(in))
	}

	out := smallllm.ApplyCompactDescriptions(in)
	compacted := 0
	for i, desc := range out {
		full := in[i].Description
		if out[i].Description != full {
			compacted++
			if len(desc.Description) >= len(full) {
				t.Errorf("tool %s: compact description (%d chars) is not shorter than full (%d chars)",
					desc.Name, len(desc.Description), len(full))
			}
		}
	}
	if compacted != len(in) {
		t.Errorf("expected all %d real builtins to be compacted, got %d — missing compact one-liners?",
			len(in), compacted)
	}
}

// TestMaybeCompactDisabledByteIdentical: with the mode disabled the input
// slice must be returned untouched — byte-identical descriptions, same
// backing array.
func TestMaybeCompactDisabledByteIdentical(t *testing.T) {
	in := realBuiltinDescriptors(t)

	out := smallllm.MaybeCompactDescriptions(in, false)

	if len(out) != len(in) {
		t.Fatalf("disabled compact changed tool count: %d -> %d", len(in), len(out))
	}
	if &out[0] != &in[0] {
		t.Error("disabled compact must return the input slice itself, not a copy")
	}
	for i := range in {
		if in[i].Description != out[i].Description {
			t.Errorf("tool %s: description changed while compact mode is disabled", in[i].Name)
		}
	}
}

// TestMaybeCompactEnabledApplies: with the mode enabled the output equals
// ApplyCompactDescriptions on the same input.
func TestMaybeCompactEnabledApplies(t *testing.T) {
	in := realBuiltinDescriptors(t)

	enabled := smallllm.MaybeCompactDescriptions(in, true)
	direct := smallllm.ApplyCompactDescriptions(in)

	if len(enabled) != len(direct) {
		t.Fatalf("length mismatch: %d vs %d", len(enabled), len(direct))
	}
	for i := range enabled {
		if enabled[i].Name != direct[i].Name || enabled[i].Description != direct[i].Description {
			t.Errorf("tool %s: MaybeCompact(enabled) differs from ApplyCompact", enabled[i].Name)
		}
	}
}

// TestApplyCompactDescriptionsUnknownToolsUntouched: descriptors of tools
// without a compact one-liner (e.g. MCP-sourced) must pass through with their
// original description, and the input slice must never be mutated.
func TestApplyCompactDescriptionsUnknownToolsUntouched(t *testing.T) {
	unknownFull := "MCP tool description that must survive untouched " + strings.Repeat("x", 400)
	in := []tools.ToolDescriptor{
		{Name: "read_file", Description: strings.Repeat("f", 600)},
		{Name: "mcp_weather_get", Description: unknownFull},
	}

	out := smallllm.ApplyCompactDescriptions(in)

	if out[0].Description == in[0].Description {
		t.Error("known builtin read_file was not compacted")
	}
	if out[1].Description != unknownFull {
		t.Error("unknown tool's description was modified")
	}
	if in[0].Description != strings.Repeat("f", 600) {
		t.Error("input slice was mutated — compact must copy")
	}
}

// TestCompactOneLinersBounded: every compact one-liner must stay under the
// compact bound so the set cannot regress to full-length prose.
func TestCompactOneLinersBounded(t *testing.T) {
	for _, name := range compactNames(t) {
		compact := smallllm.CompactDescription(name)
		if compact == "" {
			t.Fatalf("tool %s: CompactDescription returned empty", name)
		}
		if len(compact) > smallllm.MaxCompactDescriptionLength {
			t.Errorf("tool %s: compact description is %d chars, exceeds %d-char bound", name, len(compact), smallllm.MaxCompactDescriptionLength)
		}
	}
}

// compactNames derives the compact-set tool names without exporting the map:
// feed stub descriptors named after the real builtins and see which change.
func compactNames(t *testing.T) []string {
	t.Helper()
	all := allRealBuiltinDescriptors(t)
	stubs := make([]tools.ToolDescriptor, 0, len(all))
	for _, d := range all {
		stubs = append(stubs, tools.ToolDescriptor{Name: d.Name, Description: "stub"})
	}
	out := smallllm.ApplyCompactDescriptions(stubs)
	found := make([]string, 0, len(out))
	for i := range out {
		if out[i].Description != "stub" {
			found = append(found, out[i].Name)
		}
	}
	if len(found) == 0 {
		t.Fatal("no compact names discovered")
	}
	return found
}

// schemaVocabulary flattens a tool's JSON schema into the set of identifiers
// a compact one-liner may legitimately mention: every property name at any
// depth plus every enum member value.
func schemaVocabulary(t *testing.T, name string, raw json.RawMessage) map[string]struct{} {
	t.Helper()

	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("tool %s: schema is not valid JSON: %v", name, err)
	}
	vocab := make(map[string]struct{})
	var walk func(node any)
	walk = func(node any) {
		obj, ok := node.(map[string]any)
		if !ok {
			return
		}
		if props, ok := obj["properties"].(map[string]any); ok {
			for pname, def := range props {
				vocab[pname] = struct{}{}
				defObj, ok := def.(map[string]any)
				if !ok {
					continue
				}
				if enum, ok := defObj["enum"].([]any); ok {
					for _, v := range enum {
						if s, ok := v.(string); ok {
							vocab[s] = struct{}{}
						}
					}
				}
				walk(defObj)
				if items, ok := defObj["items"].(map[string]any); ok {
					walk(items)
				}
			}
		}
		if req, ok := obj["required"].([]any); ok {
			for _, v := range req {
				if s, ok := v.(string); ok {
					vocab[s] = struct{}{}
				}
			}
		}
	}
	walk(root)
	return vocab
}

var compactSnakeTokenRe = regexp.MustCompile(`[a-z]+(?:_[a-z]+)+`)
var compactParenAltRe = regexp.MustCompile(`\(([^()]*)\)`)
var compactAltTokenRe = regexp.MustCompile(`^"?([a-z_][a-z0-9_]*)"?$`)

// TestCompactDescriptionsMatchSchemas guards the compact one-liners against
// drift from the tools' real schemas. A compact description still names
// parameters and enum values; if it names one the schema does not know (or
// invents enum values), a small model following it fails validation on the
// first call — exactly the failure mode the full descriptions are guarded
// against in core/tools. Two invariants for every c0wrk orchestration tool:
//
//  1. Every snake_case token is a schema property or enum value (at any
//     depth) of that tool.
//  2. Every slash/pipe-separated list of bare tokens inside parentheses
//     consists of schema enum values.
func TestCompactDescriptionsMatchSchemas(t *testing.T) {
	for _, tool := range c0wrkToolImpls(t) {
		compact := smallllm.CompactDescription(tool.Name())
		if compact == "" {
			t.Fatalf("tool %s: no compact description for a c0wrk builtin — gap in the compact set?", tool.Name())
		}
		vocab := schemaVocabulary(t, tool.Name(), tool.InputSchema())

		for _, tok := range compactSnakeTokenRe.FindAllString(compact, -1) {
			if _, ok := vocab[tok]; !ok {
				t.Errorf("tool %s: compact description mentions %q, which is neither a schema property nor an enum value of this tool",
					tool.Name(), tok)
			}
		}

		for _, m := range compactParenAltRe.FindAllStringSubmatch(compact, -1) {
			parts := strings.Split(m[1], "/")
			if len(parts) < 2 {
				continue
			}
			toks := make([]string, 0, len(parts))
			clean := true
			for _, part := range parts {
				pm := compactAltTokenRe.FindStringSubmatch(strings.TrimSpace(part))
				if pm == nil {
					clean = false
					break
				}
				toks = append(toks, pm[1])
			}
			if !clean {
				continue
			}
			for _, tok := range toks {
				if _, ok := vocab[tok]; !ok {
					t.Errorf("tool %s: compact description lists %q as an alternative inside %q, but it is not a schema enum value",
						tool.Name(), tok, m[1])
				}
			}
		}
	}
}
