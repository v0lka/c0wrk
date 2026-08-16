package tools

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"

	"github.com/v0lka/sp4rk/llm"
)

// TestDelegateSchema_NoTypelessProperties is a regression test for the HTTP 400
// "schema must have a 'type' key" rejection from OpenAI (Responses API, strict
// mode) and OpenCode Zen. The delegate tool's `tasks[].tools` field is
// polymorphic (a string preset OR an array of tool names); it must be expressed
// with a valid JSON Schema combinator (anyOf) rather than left typeless.
//
// This test runs the real schema through the production sanitizer
// (SanitizeSchemaForOpenAI) and asserts that NO schema node anywhere in the
// tree is left without a `type` or a composition keyword (anyOf/oneOf/allOf) or
// `$ref`/`enum` — which is precisely what OpenAI strict validation demands.
func TestDelegateSchema_NoTypelessProperties(t *testing.T) {
	raw := NewDelegateTool().InputSchema()
	if len(raw) == 0 {
		t.Fatal("delegate tool schema is empty")
	}

	// The schema must be well-formed JSON to begin with.
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("delegate schema is not valid JSON: %v", err)
	}

	// Sanity: the `tools` field must carry anyOf before sanitization.
	items, _ := parsed["properties"].(map[string]any)["tasks"].(map[string]any)["items"].(map[string]any)
	props, _ := items["properties"].(map[string]any)
	toolsProp, ok := props["tools"].(map[string]any)
	if !ok {
		t.Fatal("tasks[].tools property missing from schema")
	}
	if _, ok := toolsProp["anyOf"].([]any); !ok {
		t.Fatalf("tasks[].tools must use anyOf (string-enum | string-array); got: %v", toolsProp)
	}

	// Run the production sanitizer — this is exactly what the OpenAI providers
	// apply before sending the schema to the API.
	sanitized := llm.SanitizeSchemaForOpenAI(raw)
	var s map[string]any
	if err := json.Unmarshal(sanitized, &s); err != nil {
		t.Fatalf("sanitized schema is not valid JSON: %v", err)
	}

	// Walk every schema node and assert none is typeless.
	var offenders []string
	var walk func(node map[string]any, path string)
	walk = func(node map[string]any, path string) {
		hasType := false
		for _, k := range []string{"type", "anyOf", "oneOf", "allOf", "$ref", "enum"} {
			if _, ok := node[k]; ok {
				hasType = true
				break
			}
		}
		if !hasType {
			offenders = append(offenders, path)
		}
		// Recurse into schema-bearing keys.
		if p, ok := node["properties"].(map[string]any); ok {
			for name, val := range p {
				if m, ok := val.(map[string]any); ok {
					walk(m, path+".properties."+name)
				}
			}
		}
		if it, ok := node["items"]; ok {
			switch v := it.(type) {
			case map[string]any:
				walk(v, path+".items")
			case []any:
				for i, item := range v {
					if m, ok := item.(map[string]any); ok {
						walk(m, path+".items["+strconv.Itoa(i)+"]")
					}
				}
			}
		}
		if ap, ok := node["additionalProperties"].(map[string]any); ok {
			walk(ap, path+".additionalProperties")
		}
		for _, key := range []string{"anyOf", "oneOf", "allOf"} {
			if arr, ok := node[key].([]any); ok {
				for i, item := range arr {
					if m, ok := item.(map[string]any); ok {
						walk(m, path+"."+key+"["+strconv.Itoa(i)+"]")
					}
				}
			}
		}
	}
	walk(s, "root")

	if len(offenders) > 0 {
		t.Fatalf("typeless schema nodes after sanitization (OpenAI strict mode rejects these with HTTP 400): %v", offenders)
	}
}

// TestDelegateToolsSchema_EnumListsGroups verifies the tasks[].tools array
// enum covers every group token the resolver accepts. The enum is generated
// from sdktools.AllToolGroups (see delegateGroupEnumJSON) — the same table
// parseToolGroupToken validates against — so this test derives its expected
// set from the SDK independently and compares set-wise: every declared group
// must appear in both spellings (underscore value and kebab-case twin), with
// no duplicates and no foreign tokens. A new SDK group without a schema enum
// entry would make schema-enforcing providers reject requests the resolver
// accepts.
func TestDelegateToolsSchema_EnumListsGroups(t *testing.T) {
	raw := NewDelegateTool().InputSchema()
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("delegate schema is not valid JSON: %v", err)
	}
	items, _ := parsed["properties"].(map[string]any)["tasks"].(map[string]any)["items"].(map[string]any)
	props, _ := items["properties"].(map[string]any)
	toolsProp, _ := props["tools"].(map[string]any)
	anyOf, _ := toolsProp["anyOf"].([]any)
	if len(anyOf) != 2 {
		t.Fatalf("tasks[].tools anyOf must have 2 branches (string enum + array), got %d", len(anyOf))
	}
	strBranch, _ := anyOf[0].(map[string]any)
	arrBranch, _ := anyOf[1].(map[string]any)

	strEnum, _ := strBranch["enum"].([]any)
	if len(strEnum) != 2 || strEnum[0] != "all" || strEnum[1] != "read-only" {
		t.Errorf("string enum must be [all read-only], got %v", strEnum)
	}

	arrItems, _ := arrBranch["items"].(map[string]any)
	enum, _ := arrItems["enum"].([]any)

	// Expected set, derived independently from the SDK group table.
	want := map[string]bool{}
	for _, g := range sdktools.AllToolGroups() {
		want[string(g)] = true
		if kebab := strings.ReplaceAll(string(g), "_", "-"); kebab != string(g) {
			want[kebab] = true
		}
	}
	got := map[string]bool{}
	for _, v := range enum {
		tok, _ := v.(string)
		if got[tok] {
			t.Errorf("duplicate enum token %q", tok)
		}
		got[tok] = true
	}
	if len(enum) != len(want) {
		t.Errorf("array enum must list all %d accepted tokens, got %d: %v", len(want), len(enum), enum)
	}
	for tok := range want {
		if !got[tok] {
			t.Errorf("array enum is missing accepted token %q — schema-enforcing providers would reject it", tok)
		}
	}
	for tok := range got {
		if !want[tok] {
			t.Errorf("array enum lists unknown token %q that the resolver would reject", tok)
		}
	}
}

// TestValidateDelegationTools verifies the `tools` field's runtime validation:
// nil/presets pass, group arrays (kebab and underscore) pass, and everything
// else fails closed with a message listing the valid values (acceptance
// criterion 4 for the delegate entry point). An empty array is REJECTED: it
// would otherwise resolve to a system-only toolset — a degenerate grant that
// strips every working tool from the subagent; ["system"] expresses that
// intent explicitly.
func TestValidateDelegationTools(t *testing.T) {
	valid := []any{
		nil,
		"",
		"all",
		"read-only",
		[]any{"local-read", "execute"},
		[]any{"local_read"}, // underscore spelling accepted
		[]string{"remote-read", "local-mcp"},
		[]any{"system"}, // explicit system-only grant is allowed
	}
	for _, v := range valid {
		if err := validateDelegationTools(v); err != nil {
			t.Errorf("validateDelegationTools(%#v) = %v, want nil", v, err)
		}
	}

	invalid := []struct {
		v       any
		wantMsg string
	}{
		{v: "edit_file", wantMsg: `invalid value "edit_file"`},
		{v: []any{"local-read", "edit_file"}, wantMsg: `unknown tool group "edit_file"`},
		{v: []any{"all", "local-read"}, wantMsg: `"all" must be passed as a plain string`},
		{v: []any{"read-only"}, wantMsg: `"read-only" must be passed as a plain string`},
		{v: []any{42}, wantMsg: "group names must be strings"},
		{v: 42, wantMsg: "unexpected type"},
		{v: []any{}, wantMsg: "empty array"},
		{v: []string{}, wantMsg: "empty array"},
	}
	for _, tt := range invalid {
		err := validateDelegationTools(tt.v)
		if err == nil {
			t.Errorf("validateDelegationTools(%#v) = nil, want error", tt.v)
			continue
		}
		if !strings.Contains(err.Error(), tt.wantMsg) {
			t.Errorf("validateDelegationTools(%#v) = %q, want message containing %q", tt.v, err.Error(), tt.wantMsg)
		}
	}
}

// TestValidateDelegationTasks_RejectsUnknownToolsField wires the tools
// validation into the batch validator: a delegate call carrying an unknown
// group must fail the WHOLE call before any subagent launches.
func TestValidateDelegationTasks_RejectsUnknownToolsField(t *testing.T) {
	registry := NewDelegationRegistry()
	tasks := []DelegationTask{
		{ID: "del_1", Summary: "s", Task: "t", Tools: []any{"local-read", "edit_file"}},
	}
	err := validateDelegationTasks(tasks, registry, nil)
	if err == nil {
		t.Fatal("expected validation error for unknown tool group, got nil")
	}
	if !strings.Contains(err.Error(), "del_1") {
		t.Errorf("error should reference the offending task id, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "local-mcp") {
		t.Errorf("error should list valid groups, got %q", err.Error())
	}
}
