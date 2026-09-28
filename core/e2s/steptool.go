// The e2s_step meta-tool: the model's per-turn output envelope (the ONLY
// tool it may call). See loop.go for the driver and types.go/merge.go for
// the domain layer.
//
// Unlike the ReAct executor, which replays a growing message history, the E2S
// loop keeps the model's context bounded at O(1): every step is a FRESH
// one-shot dialog consisting of exactly [system, user] messages, where the
// user message carries the full working state Σₜ (compact JSON) and the
// latest observation Oₜ. The model responds with a single e2s_step meta-tool
// call that (a) patches the state and (b) names the next action; the loop
// validates and applies the patch, dispatches the action through the tool
// registry (inheriting every security gate: group policies, judge, HITL
// confirmation, verify-on-edit), and feeds the truncated result back as Oₜ₊₁.

package e2s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/strutil"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// StepToolName is the name of the E2S meta-tool offered to the model as the
// ONLY tool definition on every LLM call.
const StepToolName = "e2s_step"

// FinishActionName is the reserved action target that terminates the loop.
const FinishActionName = "finish"

// The canonical shapes quoted verbatim in every shape-violation error and in
// the correction suffix: an invalid move is retried with the form in view, so
// the retry can copy the envelope instead of re-guessing it (the analyzed
// production session lost ~12% of its turns to shape errors whose messages
// named the violation but never showed the expected envelope).
const (
	// stepEnvelope is the full e2s_step input: both keys, always.
	stepEnvelope = `{"state_patch": {...}, "action": {"tool": "<name>", "args": {...}}}`
	// stepActionEnvelope is the action object alone — the fragment relevant
	// to per-action shape errors.
	stepActionEnvelope = `{"tool": "<name>", "args": {...}}`
	// stepFinishEnvelope is the action that ends the task.
	stepFinishEnvelope = `{"tool": "finish", "args": {"answer": "..."}}`
)

const toolStepDescription = `Purpose: advance the E2S loop one step — patch your working state and name the next action.
Use when: on EVERY turn. This is the only tool you may call; a turn without an e2s_step call is invalid and will be retried once, then reported back as an error observation.
Inputs: state_patch (a JSON object shallow-merged into your working state Σ — record here everything the next turn needs: findings, decisions, file locations, partial results; keep it compact, the merged state is size-capped); action (object: tool = a name from the Available Tools list with args = that tool's arguments object, OR tool = "finish" with args = {"answer": "..."} when the task is complete).
Outputs: the action's result (truncated) comes back to you as the next observation; the merged state is echoed to the user as a live snapshot.
Example: {"state_patch": {"found": "auth lives in core/middleware.go"}, "action": {"tool": "read_file", "args": {"path": "core/middleware.go"}}}.
Anti-example: do not put the action inside state_patch, do not emit text instead of calling this tool, and do not call finish while work remains.`

// StepTool is the E2S meta-tool. It exists to give the LLM a tool definition
// to call; the E2S loop intercepts the call and never routes it through a
// registry. Executing it directly is a configuration error and returns an
// error result instead of performing anything.
//
// Group: GroupSystem (ADR-024). The tool has no filesystem or network side
// effect of its own — it only describes the state-patch + action envelope.
// The real policy gates apply to the TARGET tool when the loop dispatches it
// via ToolRegistry.Execute, so a malicious e2s_step envelope cannot bypass
// group policies, the judge, or HITL confirmation.
type StepTool struct {
	*sdktools.BaseTool
}

// NewStepTool creates the e2s_step meta-tool.
func NewStepTool() *StepTool {
	return &StepTool{BaseTool: &sdktools.BaseTool{
		ToolGroup:       sdktools.GroupSystem,
		ToolName:        StepToolName,
		ToolDescription: toolStepDescription,
		Schema:          json.RawMessage(stepToolSchema),
		Policy:          sdktools.PolicyAlwaysAllow,
		Untrusted:       false,
	}}
}

const stepToolSchema = `{
	"type": "object",
	"properties": {
		"state_patch": {
			"type": "object",
			"description": "Shallow-merge patch into the working state. Core keys keep their fixed types; extension keys add or update in place (delete with an explicit null). Omit keys you want to keep. Keep the merged state compact — oversized patches are rejected.",
			"additionalProperties": true
		},
		"action": {
			"type": "object",
			"description": "The next action: a target tool call, or finish to end the task.",
			"properties": {
				"tool": {
					"type": "string",
					"description": "Target tool name from the Available Tools list, or \"finish\" to deliver the final answer."
				},
				"args": {
					"type": "object",
					"description": "Arguments object for the target tool. For finish: {\"answer\": \"...\"}."
				}
			},
			"required": ["tool", "args"]
		}
	},
	"required": ["state_patch", "action"]
}`

// Execute is never called by the E2S loop (it intercepts e2s_step calls
// before dispatch). Registering the tool into a registry and executing it
// there is a wiring mistake — fail loudly instead of silently no-oping.
func (t *StepTool) Execute(_ context.Context, _ json.RawMessage) (sdktools.ToolResult, error) {
	return sdktools.ErrorResult(
		"%s is an E2S envelope tool: it is intercepted by the E2S loop and must not be executed through a registry",
		StepToolName,
	), nil
}

// StepAction is the validated action half of an e2s_step call.
type StepAction struct {
	// Tool is the target tool name ("finish" terminates the loop).
	Tool string
	// Args is the raw arguments object for the target tool (always a compact
	// JSON object).
	Args json.RawMessage
	// Answer is set when Tool == "finish" (parsed from args.answer).
	Answer string
}

// IsFinish reports whether the action terminates the loop.
func (a StepAction) IsFinish() bool { return a.Tool == FinishActionName }

// StepCall is a parsed and validated e2s_step tool call.
type StepCall struct {
	// ID is the LLM-assigned tool-call id, threaded into the synthesized
	// trajectory step.
	ID string
	// StatePatch holds the raw JSON values of the patch keys (nil for an
	// absent/empty patch). Values are kept raw so the merge round-trips
	// numbers, nested objects, and arrays without re-marshaling artifacts
	// until the state is rendered.
	StatePatch map[string]json.RawMessage
	// Action is the validated action.
	Action StepAction
}

// ParseStepCall extracts and validates the e2s_step tool call from an LLM
// response's tool calls. The first tool call must be e2s_step; a response
// with no tool calls, or whose first tool call names a different tool, is
// invalid (the loop offers exactly one tool definition, so any other name is
// a protocol violation).
func ParseStepCall(toolCalls []llm.ToolCall) (StepCall, error) {
	if len(toolCalls) == 0 {
		return StepCall{}, fmt.Errorf("response contains no tool call: every turn must call %s exactly once — %s", StepToolName, stepEnvelope)
	}
	first := toolCalls[0]
	if first.Name != StepToolName {
		return StepCall{}, fmt.Errorf("unexpected tool %q: the only tool you may call is %s — %s", first.Name, StepToolName, stepEnvelope)
	}
	call, err := parseStepInput(first.Input)
	if err != nil {
		return StepCall{}, err
	}
	call.ID = first.ID
	return call, nil
}

func parseStepInput(raw json.RawMessage) (StepCall, error) {
	var input struct {
		StatePatch map[string]json.RawMessage `json:"state_patch"`
		Action     json.RawMessage            `json:"action"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return StepCall{}, fmt.Errorf("invalid %s input JSON: %w — the envelope is %s", StepToolName, err, stepEnvelope)
	}

	// Absent action (or an explicit null) is the most frequent invalid move:
	// name the whole envelope, not just the missing key.
	if trimmed := bytes.TrimSpace(input.Action); len(trimmed) == 0 || string(trimmed) == "null" {
		return StepCall{}, fmt.Errorf("%s.action is required — the envelope is %s", StepToolName, stepEnvelope)
	}

	// Parse the action generically (tool as raw JSON) so a non-string tool or
	// a non-object action produces an explicit shape error instead of a Go
	// reflection message buried under "invalid input JSON".
	var action struct {
		Tool json.RawMessage `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(input.Action, &action); err != nil {
		return StepCall{}, fmt.Errorf("%s.action must be an object — %s", StepToolName, stepActionEnvelope)
	}

	if trimmed := bytes.TrimSpace(action.Tool); len(trimmed) == 0 || string(trimmed) == "null" {
		return StepCall{}, fmt.Errorf("%s.action.tool is required (a name from the Available Tools list, or %q) — %s: %w",
			StepToolName, FinishActionName, stepActionEnvelope, ErrActionEmpty)
	}
	var tool string
	if err := json.Unmarshal(action.Tool, &tool); err != nil {
		return StepCall{}, fmt.Errorf("%s.action.tool must be a string (a name from the Available Tools list, or %q), got: %s",
			StepToolName, FinishActionName, truncateForError(action.Tool))
	}

	args, err := normalizeArgs(action.Args)
	if err != nil {
		return StepCall{}, fmt.Errorf("%s.action.args must be a JSON object — %s: %w", StepToolName, stepActionEnvelope, err)
	}

	call := StepCall{
		StatePatch: input.StatePatch,
		Action: StepAction{
			Tool: tool,
			Args: args,
		},
	}

	if call.Action.IsFinish() {
		answer, err := parseFinishAnswer(args)
		if err != nil {
			return StepCall{}, fmt.Errorf("%s.action.args: %w", StepToolName, err)
		}
		call.Action.Answer = answer
	}
	// Enforce the domain action contract (exactly-one-of tool call / finish
	// with an answer) on top of the envelope shape checks. An empty tool
	// (absent, null, or "") is re-reported with the action envelope in view.
	if err := call.Action.Validate(); err != nil {
		if errors.Is(err, ErrActionEmpty) {
			return StepCall{}, fmt.Errorf("%s.action.tool is required (a name from the Available Tools list, or %q) — %s: %w",
				StepToolName, FinishActionName, stepActionEnvelope, err)
		}
		return StepCall{}, err
	}
	return call, nil
}

// normalizeArgs validates that args is a JSON object (or absent → the empty
// object) and returns it in compact form so identical argument sets produce
// identical fingerprints. A non-object value (array, string, number) is a
// schema violation: every tool input in the registry is an object.
func normalizeArgs(raw json.RawMessage) (json.RawMessage, error) {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || string(trimmed) == "null" {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("invalid JSON: %s", truncateForError(raw))
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("must be a JSON object, got: %s", truncateForError(raw))
	}
	return json.RawMessage(compactJSON(raw)), nil
}

// parseFinishAnswer extracts the answer string from finish args. A missing
// or empty answer is left for the domain contract check (StepAction.Validate
// → ErrActionNoAnswer); a present-but-non-string value is a shape error.
func parseFinishAnswer(args json.RawMessage) (string, error) {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(args, &parsed); err != nil {
		return "", fmt.Errorf("invalid JSON: %w", err)
	}
	raw, ok := parsed["answer"]
	if !ok {
		return "", nil
	}
	var answer string
	if err := json.Unmarshal(raw, &answer); err != nil {
		return "", fmt.Errorf("answer must be a string: %w", err)
	}
	return answer, nil
}

// truncateForError renders a short preview of raw JSON for error messages.
// Rune-safe: byte slicing could split a multi-byte character (CJK, emoji,
// typographic quotes are common in pasted arguments) and inject invalid
// UTF-8 into the model-facing error text.
func truncateForError(raw json.RawMessage) string {
	return strutil.TruncateUTF8(string(raw), 120)
}

// compactJSON removes insignificant whitespace — a byte-level
// normalization only: it does NOT reorder keys, so argument objects that
// differ solely in key order render differently (and therefore fingerprint
// differently on the no-anchor fallback path; canonicalArgs exists for
// that). Falls back to the raw string for malformed JSON. Also used for the
// args preview (cosmetic).
func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

// canonicalArgs renders args as key-sorted JSON so semantically identical
// argument objects produce byte-identical strings regardless of key order
// (used by the anti-spin fingerprint's full-args fallback, making
// {"a":1,"b":2} and {"b":2,"a":1} the same action).
// Falls back to whitespace normalization for values the round-trip cannot
// represent (it never fails for valid JSON).
func canonicalArgs(raw json.RawMessage) string {
	// UseNumber decodes numbers as json.Number, so a large integer literal is
	// re-marshaled verbatim instead of through float64 (which would collapse
	// two distinct integers beyond 2^53 into the same fingerprint). Marshaling
	// a map sorts keys, giving the canonical order.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj any
	if err := dec.Decode(&obj); err != nil {
		return compactJSON(raw)
	}
	data, err := json.Marshal(obj)
	if err != nil {
		return compactJSON(raw)
	}
	return string(data)
}

// ActionFingerprint returns a canonical string identifying the action for
// the anti-spin detector. It is SEMANTIC, not byte-exact: the fingerprint
// anchors on the identity of the operation's TARGET — the tool name plus the
// "anchor" arguments that name what is acted on (path, pattern, command,
// query, url, name, skill, hash) — and deliberately ignores precision
// arguments such as line ranges or limits. Re-reading the same file with
// ever-shifting start_line/end_line (the classic small-model spin) produces
// a stable fingerprint and is caught; reading different files or searching
// different patterns does not.
//
// Content-bearing arguments (content, old_string, new_string — the payloads
// of write_file/edit_file) are folded into the fingerprint whenever present:
// for a mutating tool the payload IS part of the operation's identity, so
// three successive edits of the same file with different content are three
// DISTINCT actions (no spin), while re-writing the same content to the same
// path is a true repeat (spin). Without this, an anchor-only fingerprint
// would count every consecutive edit of one file as an identical action and
// silently drop the third edit at the nudge threshold (ADR-040 anti-spin
// semantics). A tool exposing no anchor argument falls back to the full
// canonical args (the legacy exact behavior), and a batch fingerprints per
// sub-call so a repeated identical batch still spins.
func ActionFingerprint(tool string, args json.RawMessage) string {
	if tool == sdktools.ToolBatch {
		return tool + ":" + batchAnchors(args)
	}
	return tool + ":" + actionAnchors(args)
}

// actionAnchors renders the per-action identity: the anchor key=value pairs
// when any anchor key carries a string value, plus the content-bearing
// arguments when present; the full canonical args otherwise.
func actionAnchors(args json.RawMessage) string {
	if anchors := argAnchors(args); anchors != "" {
		if content := contentArgs(args); content != "" {
			return anchors + "\x01" + content
		}
		return anchors
	}
	return canonicalArgs(args)
}

// anchorArgKeys are the argument names treated as operation-target anchors.
// Sorted for deterministic extraction.
var anchorArgKeys = []string{"command", "hash", "name", "path", "pattern", "query", "skill", "url"}

// contentArgKeys are the argument names whose values ARE the operation's
// payload (what gets written), not just its target. They join the
// fingerprint so that two mutating calls on the same target with different
// payloads are distinct actions. Sorted for deterministic extraction.
var contentArgKeys = []string{"content", "new_string", "old_string"}

// argAnchors extracts the anchor key=value pairs from an args object
// (string-valued anchors only), sorted by key. Empty string when no anchor
// key carries a string value.
func argAnchors(args json.RawMessage) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return ""
	}
	pairs := make([]string, 0, len(anchorArgKeys))
	for _, key := range anchorArgKeys {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		var val string
		if err := json.Unmarshal(raw, &val); err != nil {
			continue // non-string anchor (e.g. numeric name): not an anchor
		}
		pairs = append(pairs, key+"="+val)
	}
	if len(pairs) == 0 {
		return ""
	}
	return strings.Join(pairs, "\x00")
}

// contentArgs extracts the content-bearing key=value pairs from an args
// object (string-valued only), sorted by key. Empty string when none of the
// content keys is present with a string value.
func contentArgs(args json.RawMessage) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return ""
	}
	pairs := make([]string, 0, len(contentArgKeys))
	for _, key := range contentArgKeys {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		var val string
		if err := json.Unmarshal(raw, &val); err != nil {
			continue
		}
		pairs = append(pairs, key+"="+val)
	}
	if len(pairs) == 0 {
		return ""
	}
	return strings.Join(pairs, "\x00")
}

// batchAnchors fingerprints a batch action per sub-call: each sub-call's
// tool + anchors (plus content args, or the canonical-args fallback), joined
// in order. A repeated identical batch matches; any changed target or
// payload breaks the match.
func batchAnchors(args json.RawMessage) string {
	var input struct {
		Calls []struct {
			Tool  string          `json:"tool"`
			Input json.RawMessage `json:"input"`
		} `json:"calls"`
	}
	if err := json.Unmarshal(args, &input); err != nil || len(input.Calls) == 0 {
		return canonicalArgs(args)
	}
	parts := make([]string, 0, len(input.Calls))
	for _, sub := range input.Calls {
		parts = append(parts, sub.Tool+"="+actionAnchors(sub.Input))
	}
	return strings.Join(parts, "\x00")
}
