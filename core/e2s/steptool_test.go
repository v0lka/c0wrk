package e2s

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"
)

func TestStepTool_Metadata(t *testing.T) {
	tool := NewStepTool()
	if tool.Name() != "e2s_step" {
		t.Errorf("name = %q, want e2s_step", tool.Name())
	}
	if got := sdktools.ToolGroupOf(tool); got != sdktools.GroupSystem {
		t.Errorf("group = %q, want system (ADR-024)", got)
	}
	if tool.DefaultPolicy() != sdktools.PolicyAlwaysAllow {
		t.Errorf("policy = %v, want always allow (dispatch-only envelope)", tool.DefaultPolicy())
	}
	if tool.IsUntrusted() {
		t.Error("envelope tool must not be marked untrusted")
	}
	// The schema must declare the envelope contract.
	schema := string(tool.InputSchema())
	for _, want := range []string{`"state_patch"`, `"action"`, `"tool"`, `"args"`, `"required"`} {
		if !strings.Contains(schema, want) {
			t.Errorf("input schema missing %s", want)
		}
	}
	// The schema itself must be valid JSON.
	var probe map[string]any
	if err := json.Unmarshal(tool.InputSchema(), &probe); err != nil {
		t.Fatalf("input schema is not valid JSON: %v", err)
	}
}

func TestStepTool_ExecuteRefusesDirectDispatch(t *testing.T) {
	tool := NewStepTool()
	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute returned err: %v (must report via ToolResult)", err)
	}
	if !result.IsError {
		t.Error("Execute on the envelope tool must return an error result")
	}
	if !strings.Contains(result.Content, "must not be executed") {
		t.Errorf("unexpected content: %q", result.Content)
	}
}

func TestParseStepCall_ValidToolCall(t *testing.T) {
	calls := []llm.ToolCall{{
		ID:    "call_9",
		Name:  StepToolName,
		Input: json.RawMessage(`{"state_patch":{"findings":["f"]},"action":{"tool":"read_file","args":{"path":"x.go"}}}`),
	}}
	call, err := ParseStepCall(calls)
	if err != nil {
		t.Fatalf("ParseStepCall: %v", err)
	}
	if call.ID != "call_9" {
		t.Errorf("ID = %q, want call_9", call.ID)
	}
	if len(call.StatePatch) != 1 {
		t.Errorf("StatePatch = %v, want one key", call.StatePatch)
	}
	if call.Action.Tool != "read_file" {
		t.Errorf("action tool = %q", call.Action.Tool)
	}
	if string(call.Action.Args) != `{"path":"x.go"}` {
		t.Errorf("action args = %s (want compact form)", call.Action.Args)
	}
	if call.Action.IsFinish() {
		t.Error("IsFinish = true for a tool action")
	}
	if err := call.Action.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestParseStepCall_Finish(t *testing.T) {
	call, err := ParseStepCall([]llm.ToolCall{{
		ID:    "c1",
		Name:  StepToolName,
		Input: json.RawMessage(`{"state_patch":{},"action":{"tool":"finish","args":{"answer":"all done"}}}`),
	}})
	if err != nil {
		t.Fatalf("ParseStepCall: %v", err)
	}
	if !call.Action.IsFinish() {
		t.Fatal("IsFinish = false")
	}
	if call.Action.Answer != "all done" {
		t.Errorf("answer = %q", call.Action.Answer)
	}
}

func TestParseStepCall_Errors(t *testing.T) {
	tests := []struct {
		name      string
		calls     []llm.ToolCall
		wantErr   error
		wantTexts []string
	}{
		{
			name:      "no tool calls",
			calls:     nil,
			wantTexts: []string{"no tool call", "every turn must call e2s_step exactly once", stepEnvelope},
		},
		{
			name:      "wrong tool name",
			calls:     []llm.ToolCall{{ID: "c", Name: "read_file", Input: json.RawMessage(`{}`)}},
			wantTexts: []string{`unexpected tool "read_file"`, "the only tool you may call is e2s_step", stepEnvelope},
		},
		{
			name:      "malformed envelope JSON",
			calls:     []llm.ToolCall{{ID: "c", Name: StepToolName, Input: json.RawMessage(`{not json`)}},
			wantTexts: []string{"invalid e2s_step input JSON", stepEnvelope},
		},
		{
			name:      "missing action",
			calls:     []llm.ToolCall{{ID: "c", Name: StepToolName, Input: json.RawMessage(`{"state_patch":{}}`)}},
			wantTexts: []string{"e2s_step.action is required", stepEnvelope},
		},
		{
			name:      "action is explicit null",
			calls:     []llm.ToolCall{{ID: "c", Name: StepToolName, Input: json.RawMessage(`{"action":null}`)}},
			wantTexts: []string{"e2s_step.action is required", stepEnvelope},
		},
		{
			name:      "action is not an object",
			calls:     []llm.ToolCall{{ID: "c", Name: StepToolName, Input: json.RawMessage(`{"action":[1,2]}`)}},
			wantTexts: []string{"e2s_step.action must be an object", stepActionEnvelope},
		},
		{
			name:      "action without tool",
			calls:     []llm.ToolCall{{ID: "c", Name: StepToolName, Input: json.RawMessage(`{"action":{"args":{}}}`)}},
			wantErr:   ErrActionEmpty,
			wantTexts: []string{"e2s_step.action.tool is required", `"finish"`, stepActionEnvelope},
		},
		{
			name:      "empty tool",
			calls:     []llm.ToolCall{{ID: "c", Name: StepToolName, Input: json.RawMessage(`{"action":{"tool":"","args":{}}}`)}},
			wantErr:   ErrActionEmpty,
			wantTexts: []string{"e2s_step.action.tool is required", stepActionEnvelope},
		},
		{
			name:      "tool is not a string",
			calls:     []llm.ToolCall{{ID: "c", Name: StepToolName, Input: json.RawMessage(`{"action":{"tool":123,"args":{}}}`)}},
			wantTexts: []string{"e2s_step.action.tool must be a string", "got: 123"},
		},
		{
			name:      "args not an object",
			calls:     []llm.ToolCall{{ID: "c", Name: StepToolName, Input: json.RawMessage(`{"action":{"tool":"x","args":[1,2]}}`)}},
			wantTexts: []string{"e2s_step.action.args must be a JSON object", stepActionEnvelope, "[1,2]"},
		},
		{
			// Malformed nested JSON fails the whole-document unmarshal, so it
			// surfaces as an envelope-level error (still with the form shown).
			name:      "args malformed JSON",
			calls:     []llm.ToolCall{{ID: "c", Name: StepToolName, Input: json.RawMessage(`{"action":{"tool":"x","args":{"a":}}}`)}},
			wantTexts: []string{"invalid e2s_step input JSON", stepEnvelope},
		},
		{
			name:      "finish without answer field",
			calls:     []llm.ToolCall{{ID: "c", Name: StepToolName, Input: json.RawMessage(`{"action":{"tool":"finish","args":{}}}`)}},
			wantErr:   ErrActionNoAnswer,
			wantTexts: []string{"requires an answer"},
		},
		{
			name:      "finish with non-string answer",
			calls:     []llm.ToolCall{{ID: "c", Name: StepToolName, Input: json.RawMessage(`{"action":{"tool":"finish","args":{"answer":7}}}`)}},
			wantTexts: []string{"answer must be a string"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			call, err := ParseStepCall(tt.calls)
			if err == nil {
				t.Fatalf("ParseStepCall succeeded: %+v", call)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want wrapping %v", err, tt.wantErr)
			}
			for _, want := range tt.wantTexts {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want text %q", err, want)
				}
			}
		})
	}
}

func TestParseStepCall_AbsentStatePatchAndArgs(t *testing.T) {
	call, err := ParseStepCall([]llm.ToolCall{{
		ID:    "c",
		Name:  StepToolName,
		Input: json.RawMessage(`{"action":{"tool":"probe"}}`),
	}})
	if err != nil {
		t.Fatalf("ParseStepCall: %v (absent optional fields must default)", err)
	}
	if len(call.StatePatch) != 0 {
		t.Errorf("StatePatch = %v, want empty", call.StatePatch)
	}
	if string(call.Action.Args) != `{}` {
		t.Errorf("args = %s, want {}", call.Action.Args)
	}
}

func TestActionFingerprint_Canonical(t *testing.T) {
	// Whitespace differences must not defeat the anti-spin detector.
	a := ActionFingerprint("probe", json.RawMessage(`{"q": 1,   "z": "s"}`))
	b := ActionFingerprint("probe", json.RawMessage(`{"q":1,"z":"s"}`))
	if a != b {
		t.Errorf("fingerprints differ for identical args:\n%s\n%s", a, b)
	}
	c := ActionFingerprint("probe", json.RawMessage(`{"q":2}`))
	if a == c {
		t.Error("fingerprints collide for different args")
	}
	d := ActionFingerprint("other", json.RawMessage(`{"q":1,"z":"s"}`))
	if a == d {
		t.Error("fingerprints collide for different tools")
	}
}

// TestActionFingerprint_SemanticAnchors pins the anchor-based spin identity:
// precision arguments (line ranges, limits) are ignored, the target anchor
// (path/pattern/command/…) is not — the exact re-reading spin the analyzed
// production session died of.
func TestActionFingerprint_SemanticAnchors(t *testing.T) {
	// Same file, shifting ranges → identical fingerprint (spin detectable).
	r1 := ActionFingerprint("read_file", json.RawMessage(`{"path":"diff.txt","start_line":1,"end_line":320}`))
	r2 := ActionFingerprint("read_file", json.RawMessage(`{"path":"diff.txt","start_line":321,"end_line":520}`))
	if r1 != r2 {
		t.Errorf("shifting ranges on one path must not change the fingerprint:\n%s\n%s", r1, r2)
	}

	// Different files → different fingerprints.
	other := ActionFingerprint("read_file", json.RawMessage(`{"path":"other.txt","start_line":1,"end_line":10}`))
	if r1 == other {
		t.Error("different paths must produce different fingerprints")
	}

	// ripgrep: same path, different pattern → different (the pattern IS the
	// operation); same pattern → identical.
	p1 := ActionFingerprint("ripgrep", json.RawMessage(`{"pattern":"func Test","path":"x.go"}`))
	p2 := ActionFingerprint("ripgrep", json.RawMessage(`{"pattern":"func Other","path":"x.go"}`))
	if p1 == p2 {
		t.Error("different patterns must produce different fingerprints")
	}
	p3 := ActionFingerprint("ripgrep", json.RawMessage(`{"pattern":"func Test","path":"x.go","context_lines":5}`))
	if p1 != p3 {
		t.Error("non-anchor precision args must not change the fingerprint")
	}

	// bash_exec: different commands → different; same command → identical.
	c1 := ActionFingerprint("bash_exec", json.RawMessage(`{"command":"ls -la"}`))
	c2 := ActionFingerprint("bash_exec", json.RawMessage(`{"command":"git status"}`))
	if c1 == c2 {
		t.Error("different commands must produce different fingerprints")
	}

	// tool_result_read: the hash anchor distinguishes recoveries.
	h1 := ActionFingerprint("tool_result_read", json.RawMessage(`{"hash":"abc123","start_line":1}`))
	h2 := ActionFingerprint("tool_result_read", json.RawMessage(`{"hash":"abc123","start_line":500}`))
	if h1 != h2 {
		t.Error("paging one cached result must keep a stable fingerprint")
	}
	h3 := ActionFingerprint("tool_result_read", json.RawMessage(`{"hash":"def456","start_line":1}`))
	if h1 == h3 {
		t.Error("different cache hashes must produce different fingerprints")
	}
}

// TestActionFingerprint_BatchPerSubCall pins batch spin identity: identical
// batch calls match; any changed sub-target breaks the match.
func TestActionFingerprint_BatchPerSubCall(t *testing.T) {
	b1 := ActionFingerprint("batch", json.RawMessage(`{"calls":[{"tool":"read_file","input":{"path":"a.go"}},{"tool":"glob","input":{"pattern":"*.go"}}]}`))
	b2 := ActionFingerprint("batch", json.RawMessage(`{"calls":[{"tool":"read_file","input":{"path":"a.go","start_line":10}},{"tool":"glob","input":{"pattern":"*.go"}}]}`))
	if b1 != b2 {
		t.Errorf("identical batch targets must match:\n%s\n%s", b1, b2)
	}
	b3 := ActionFingerprint("batch", json.RawMessage(`{"calls":[{"tool":"read_file","input":{"path":"b.go"}},{"tool":"glob","input":{"pattern":"*.go"}}]}`))
	if b1 == b3 {
		t.Error("a changed sub-call target must break the batch fingerprint")
	}
}

func TestNormalizeArgs_RejectsInvalidJSON(t *testing.T) {
	if _, err := normalizeArgs(json.RawMessage(`{"broken":`)); err == nil {
		t.Error("expected error for malformed JSON")
	}
	got, err := normalizeArgs(json.RawMessage(`  {"a" : 1} `))
	if err != nil {
		t.Fatalf("normalizeArgs: %v", err)
	}
	if string(got) != `{"a":1}` {
		t.Errorf("normalized = %s, want compact {\"a\":1}", got)
	}
}

// TestActionFingerprint_ContentArgsDistinct pins the content-bearing half of
// the fingerprint: for mutating tools the payload IS
// part of the operation's identity — successive edits of the SAME path with
// different content are distinct actions (no false spin), while re-writing
// identical content is a true repeat.
func TestActionFingerprint_ContentArgsDistinct(t *testing.T) {
	e1 := ActionFingerprint("edit_file", json.RawMessage(`{"path":"a.go","old_string":"x","new_string":"y"}`))
	e2 := ActionFingerprint("edit_file", json.RawMessage(`{"path":"a.go","old_string":"x","new_string":"z"}`))
	if e1 == e2 {
		t.Error("edits of the same path with different payloads must NOT collide — the third consecutive edit would be silently dropped at the nudge threshold")
	}
	e1Again := ActionFingerprint("edit_file", json.RawMessage(`{"old_string":"x","path":"a.go","new_string":"y"}`))
	if e1 != e1Again {
		t.Error("key order must not change the fingerprint (canonical args)")
	}
	w1 := ActionFingerprint("write_file", json.RawMessage(`{"path":"a.txt","content":"one"}`))
	w2 := ActionFingerprint("write_file", json.RawMessage(`{"path":"a.txt","content":"two"}`))
	if w1 == w2 {
		t.Error("writes of the same path with different content must not collide")
	}
	w1b := ActionFingerprint("write_file", json.RawMessage(`{"path":"a.txt","content":"one"}`))
	if w1 != w1b {
		t.Error("identical write_file actions must share the fingerprint (true spin detectable)")
	}
}

// TestActionFingerprint_BatchContentSensitive extends the batch identity:
// per-sub-call content payloads participate, so a repeated identical batch
// still spins while a batch whose sub-call payload changed does not.
func TestActionFingerprint_BatchContentSensitive(t *testing.T) {
	b1 := ActionFingerprint("batch", json.RawMessage(`{"calls":[{"tool":"write_file","input":{"path":"a.txt","content":"one"}}]}`))
	b2 := ActionFingerprint("batch", json.RawMessage(`{"calls":[{"tool":"write_file","input":{"path":"a.txt","content":"two"}}]}`))
	if b1 == b2 {
		t.Error("batches differing only in sub-call content must not collide")
	}
	b1b := ActionFingerprint("batch", json.RawMessage(`{"calls":[{"tool":"write_file","input":{"content":"one","path":"a.txt"}}]}`))
	if b1 != b1b {
		t.Error("sub-call key order must not change the fingerprint")
	}
}

// TestCanonicalArgs_KeyOrderIndependence pins the canonical (key-sorted)
// fallback for anchor-less tools: argument objects differing only in key
// order are the same action.
func TestCanonicalArgs_KeyOrderIndependence(t *testing.T) {
	a := ActionFingerprint("probe", json.RawMessage(`{"a":1,"b":[2,3]}`))
	b := ActionFingerprint("probe", json.RawMessage(`{"b":[2,3],"a":1}`))
	if a != b {
		t.Errorf("key order must not defeat the fingerprint:\n%s\n%s", a, b)
	}
}
