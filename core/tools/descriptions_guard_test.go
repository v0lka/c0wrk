package tools

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/tools/builtins"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// maxDescriptionLength is the generous guard for builtin tool descriptions,
// mirroring the sp4rk tools/builtins guard.
const maxDescriptionLength = 1200

func builtinTools(t *testing.T) []sdktools.Tool {
	t.Helper()

	readFileDoc := NewReadFileDocTool(builtins.FileLimits{}, nil)

	tools := []sdktools.Tool{
		readFileDoc,
		NewAskUserTool(nil),
		NewExecutePlanTool(),
		NewReflectTool(),
		NewDelegateTool(),
		NewCancelDelegationTool(),
		NewDeclarePlanTool(nil),
		NewDeclareStepCompleteTool(),
		NewDeclareVerificationTool(),
		NewProposeGoalTool(),
		NewDeclareGoalStatusTool(),
	}

	names := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		names[tool.Name()] = struct{}{}
	}
	if len(names) != 11 {
		t.Fatalf("expected exactly 11 c0wrk builtin tools, got %d — registration drift?", len(names))
	}
	return tools
}

func TestBuiltinDescriptionsWithinGuardLimit(t *testing.T) {
	for _, tool := range builtinTools(t) {
		desc := tool.Description()
		if strings.TrimSpace(desc) == "" {
			t.Errorf("tool %s: description is empty", tool.Name())
		}
		if len(desc) > maxDescriptionLength {
			t.Errorf("tool %s: description is %d chars, exceeds guard limit %d — trim it to the rubric (purpose/when-to-use/inputs/outputs/example/anti-example)",
				tool.Name(), len(desc), maxDescriptionLength)
		}
	}
}

func TestBuiltinDescriptionsFollowRubric(t *testing.T) {
	for _, tool := range builtinTools(t) {
		desc := tool.Description()
		for _, section := range []string{"Purpose:", "Use when:", "Inputs:", "Anti-example:"} {
			if !strings.Contains(desc, section) {
				t.Errorf("tool %s: description lacks %q section", tool.Name(), section)
			}
		}
	}
}

// schemaIndex is a flattened view of a tool's JSON schema for the
// description-consistency guard: every property name at any nesting depth,
// the enum values per property name, and every required property name at any
// depth (top level and inside array items).
type schemaIndex struct {
	props    map[string]map[string]struct{} // property name -> enum values (empty set = unconstrained)
	required map[string]struct{}
}

func indexSchema(t *testing.T, toolName string, raw json.RawMessage) *schemaIndex {
	t.Helper()

	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("tool %s: schema is not valid JSON: %v", toolName, err)
	}
	idx := &schemaIndex{
		props:    make(map[string]map[string]struct{}),
		required: make(map[string]struct{}),
	}
	idx.walk(root)
	return idx
}

// walk recursively collects properties, per-property enums, and required
// names from a decoded schema node, descending into nested object properties
// and array item schemas.
func (idx *schemaIndex) walk(node any) {
	obj, ok := node.(map[string]any)
	if !ok {
		return
	}
	if props, ok := obj["properties"].(map[string]any); ok {
		for name, def := range props {
			enumSet, exists := idx.props[name]
			if !exists {
				enumSet = make(map[string]struct{})
				idx.props[name] = enumSet
			}
			defObj, ok := def.(map[string]any)
			if !ok {
				continue
			}
			if enum, ok := defObj["enum"].([]any); ok {
				for _, v := range enum {
					if s, ok := v.(string); ok {
						enumSet[s] = struct{}{}
					}
				}
			}
			idx.walk(defObj)
			if items, ok := defObj["items"].(map[string]any); ok {
				idx.walk(items)
			}
		}
	}
	if req, ok := obj["required"].([]any); ok {
		for _, v := range req {
			if s, ok := v.(string); ok {
				idx.required[s] = struct{}{}
			}
		}
	}
}

// known reports whether name is a schema property at any depth.
func (idx *schemaIndex) known(name string) bool {
	_, ok := idx.props[name]
	return ok
}

var inputsSectionRe = regexp.MustCompile(`(?s)Inputs:(.*?)(?:Outputs:|$)`)

// inputsSection returns the prose between the "Inputs:" and "Outputs:" rubric
// markers — the part of a description that names parameters.
func inputsSection(desc string) string {
	m := inputsSectionRe.FindStringSubmatch(desc)
	if m == nil {
		return ""
	}
	return m[1]
}

// identCallRe matches a lowercase identifier immediately followed by an open
// parenthesis — the "name (explanation)" pattern the Inputs section uses for
// every parameter it introduces.
var identCallRe = regexp.MustCompile(`([a-z_][a-z0-9_]*)\s*\(`)

// parenAltRe matches "name (a | b | c)" or "name (a/b/c)" groups whose
// content is a bare list of pipe- or slash-separated alternatives.
var parenAltRe = regexp.MustCompile(`([a-z_][a-z0-9_]*)\s*\(([^()]*[/|][^()]*)\)`)

// altTokenRe recognizes a single bare alternative value (optionally quoted).
var altTokenRe = regexp.MustCompile(`^"?([a-z_][a-z0-9_]*)"?$`)

// splitAlternatives splits a parenthetical on | and / and returns the bare
// tokens, or nil when the content is not a clean alternative list (prose
// explanations, multi-word parts).
func splitAlternatives(content string) []string {
	parts := regexp.MustCompile(`[/|]`).Split(content, -1)
	if len(parts) < 2 {
		return nil
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		m := altTokenRe.FindStringSubmatch(strings.TrimSpace(part))
		if m == nil {
			return nil
		}
		out = append(out, m[1])
	}
	return out
}

// wordBoundaryRe builds a whole-word matcher for a schema identifier so
// "plan_tasks" does not count as mentioning "tasks".
func wordBoundaryRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
}

// TestBuiltinDescriptionsMatchSchema guards the description↔schema contract
// for every c0wrk builtin. The LLM sees both the prose description and the
// JSON schema; when they disagree, a model that follows the prose produces
// inputs that fail validation on the first call. Three invariants:
//
//  1. Every "name (…)" parameter the Inputs section introduces must be a
//     real schema property (at any depth) — no phantom parameters.
//  2. Every schema-required property (at any depth) must be mentioned in the
//     Inputs section — no silent mandatory fields.
//  3. When a parameter's parenthetical lists bare alternatives ("a | b" or
//     "a/b") and the schema constrains that parameter to an enum, every
//     listed alternative must be an enum member — no invented values.
func TestBuiltinDescriptionsMatchSchema(t *testing.T) {
	for _, tool := range builtinTools(t) {
		idx := indexSchema(t, tool.Name(), tool.InputSchema())
		section := inputsSection(tool.Description())
		if section == "" {
			t.Errorf("tool %s: cannot extract Inputs section for schema check", tool.Name())
			continue
		}

		// 1. No phantom parameters.
		for _, m := range identCallRe.FindAllStringSubmatch(section, -1) {
			if !idx.known(m[1]) {
				t.Errorf("tool %s: Inputs mentions %q followed by '(', but the schema has no such property — align the description with the schema",
					tool.Name(), m[1])
			}
		}

		// 2. Required properties are documented.
		for name := range idx.required {
			if !wordBoundaryRe(name).MatchString(section) {
				t.Errorf("tool %s: schema requires %q but the Inputs section never mentions it",
					tool.Name(), name)
			}
		}

		// 3. Enum alternatives are real enum members.
		for _, m := range parenAltRe.FindAllStringSubmatch(section, -1) {
			alts := splitAlternatives(m[2])
			if alts == nil {
				continue
			}
			enumSet, hasEnum := idx.props[m[1]]
			if !hasEnum || len(enumSet) == 0 {
				// Unconstrained parameter (e.g. boolean described as
				// true | false) — nothing to validate against.
				continue
			}
			for _, alt := range alts {
				if _, ok := enumSet[alt]; !ok {
					t.Errorf("tool %s: Inputs lists %q as an alternative for %q, but the schema enum for %q is %v",
						tool.Name(), alt, m[1], m[1], enumValues(enumSet))
				}
			}
		}
	}
}

// enumValues renders an enum set as a sorted slice for failure messages.
func enumValues(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	// Small sets; simple insertion sort keeps the import list free of sort.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
