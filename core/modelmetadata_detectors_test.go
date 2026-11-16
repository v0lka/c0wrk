package core

import (
	"testing"

	"github.com/v0lka/sp4rk/llm"
)

// aggregatorListingEntry is the real-world aggregator /models listing entry
// shape (limit/capabilities/modalities objects) the detector targets.
const aggregatorListingEntry = `{
	"id": "opencode/deepseek-v4.1-flash-free",
	"object": "model",
	"created": 1792195200,
	"owned_by": "opencode",
	"type": "chat",
	"name": "DeepSeek V4.1 Flash Free",
	"description": "October 17 update of DeepSeek V4.1 Flash with improved reasoning, coding, agentic tasks, and visual understanding",
	"open_weights": true,
	"modalities": {
		"input": [
			"text",
			"image/jpeg",
			"image/png",
			"image/gif",
			"image/webp"
		],
		"output": [
			"text"
		]
	},
	"capabilities": {
		"tools": {
			"supported": true
		},
		"reasoning": {
			"supported": true,
			"toggle": true,
			"effort": [
				"low",
				"high",
				"max"
			]
		},
		"structured_output": true,
		"temperature": true,
		"top_p": true,
		"top_k": true,
		"system_updates": true,
		"effort_updates": false
	},
	"limit": {
		"context": 200000,
		"output": 32000
	}
}`

func mustDetect(t *testing.T, entry string) (llm.ModelMetadata, bool) {
	t.Helper()
	meta, ok := detectModelMetadata([]byte(entry))
	return meta, ok
}

func TestDetectAggregatorMetadata_FullDeclaration(t *testing.T) {
	// The full aggregator shape declares every capability dimension
	// (capabilities + modalities) → the detector emits an authoritative set.
	meta, ok := mustDetect(t, aggregatorListingEntry)
	if !ok {
		t.Fatal("aggregator entry must match the aggregator detector")
	}
	if meta.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", meta.ContextWindow)
	}
	if meta.OutputLimit != 32000 {
		t.Errorf("OutputLimit = %d, want 32000 (real limit, not the window/4 heuristic)", meta.OutputLimit)
	}
	if meta.Capabilities == nil {
		t.Fatal("Capabilities must be non-nil for a full declaration")
	}
	if !meta.Capabilities.Attachment {
		t.Error("Attachment = false, want true (image/* input modalities)")
	}
	if !meta.Capabilities.Reasoning {
		t.Error("Reasoning = false, want true")
	}
	if !meta.Capabilities.Temperature {
		t.Error("Temperature = false, want true")
	}
	if !meta.Capabilities.ToolCall {
		t.Error("ToolCall = false, want true")
	}
	// Detectors never pin identity dimensions: they must inherit from the
	// registry's lower tiers.
	if meta.Family != "" || meta.TokenizerType != "" || meta.Protocol != "" {
		t.Errorf("identity fields must stay unset, got family=%q tokenizer=%q protocol=%q",
			meta.Family, meta.TokenizerType, meta.Protocol)
	}
}

func TestDetectAggregatorMetadata_CapabilitiesWithoutModalities(t *testing.T) {
	// capabilities present, modalities absent → partial declaration → nil
	// Capabilities (inherit): nothing may be disabled by omission.
	entry := `{"id":"m","capabilities":{"tools":{"supported":true}},"limit":{"context":100000,"output":16000}}`
	meta, ok := mustDetect(t, entry)
	if !ok {
		t.Fatal("entry with limit must match")
	}
	if meta.ContextWindow != 100000 || meta.OutputLimit != 16000 {
		t.Errorf("windows = %d/%d, want 100000/16000", meta.ContextWindow, meta.OutputLimit)
	}
	if meta.Capabilities != nil {
		t.Errorf("Capabilities must be nil for a partial declaration, got %+v", *meta.Capabilities)
	}
}

func TestDetectAggregatorMetadata_ModalitiesWithoutCapabilities(t *testing.T) {
	// modalities present, capabilities absent → also a partial declaration.
	entry := `{"id":"m","modalities":{"input":["text","image/png"]},"limit":{"context":100000}}`
	meta, ok := mustDetect(t, entry)
	if !ok {
		t.Fatal("entry with limit must match")
	}
	if meta.Capabilities != nil {
		t.Errorf("Capabilities must be nil when capabilities block is absent, got %+v", *meta.Capabilities)
	}
	if meta.OutputLimit != 0 {
		t.Errorf("OutputLimit = %d, want 0 (absent → inherit)", meta.OutputLimit)
	}
}

func TestDetectAggregatorMetadata_TextOnlyIsAuthoritative(t *testing.T) {
	// modalities PRESENT and declaring text-only input, with the capabilities
	// block carrying the tools/reasoning sub-objects (full declaration), is a
	// genuine text-only declaration → Attachment false, authoritative.
	entry := `{"id":"m","modalities":{"input":["text"]},"capabilities":{"tools":{"supported":true},"reasoning":{"supported":true},"temperature":true},"limit":{"context":8192}}`
	meta, ok := mustDetect(t, entry)
	if !ok {
		t.Fatal("entry with limit must match")
	}
	if meta.Capabilities == nil {
		t.Fatal("full declaration must emit non-nil Capabilities")
	}
	if meta.Capabilities.Attachment {
		t.Error("Attachment = true, want false: modalities explicitly declare text-only input")
	}
}

func TestDetectAggregatorMetadata_SparseCapabilitiesInherits(t *testing.T) {
	// A capabilities block WITHOUT the tools/reasoning sub-objects is sparse,
	// not silent: emitting an authoritative set would turn the omission of
	// tools/reasoning into an authoritative false and disable tool routing
	// for a capable model. Both blocks present but sub-objects missing →
	// partial → nil Capabilities (inherit), windows still written.
	entry := `{"id":"m","capabilities":{"temperature":true},"modalities":{"input":["text"]},"limit":{"context":128000,"output":8192}}`
	meta, ok := mustDetect(t, entry)
	if !ok {
		t.Fatal("entry with limit must match")
	}
	if meta.ContextWindow != 128000 || meta.OutputLimit != 8192 {
		t.Errorf("windows = %d/%d, want 128000/8192", meta.ContextWindow, meta.OutputLimit)
	}
	if meta.Capabilities != nil {
		t.Errorf("sparse capabilities must inherit, got authoritative %+v", *meta.Capabilities)
	}
	// Either sub-object missing alone is also partial.
	half := `{"id":"m","capabilities":{"tools":{"supported":true},"temperature":true},"modalities":{"input":["text"]},"limit":{"context":8192}}`
	if meta, ok = mustDetect(t, half); !ok || meta.Capabilities != nil {
		t.Errorf("capabilities without the reasoning sub-object: got %v/%+v, want true/nil", ok, meta.Capabilities)
	}
}

func TestDetectAggregatorMetadata_TemperatureOmissionInherits(t *testing.T) {
	// A full structural declaration WITHOUT the temperature key is partial:
	// emitting an authoritative set would turn the omission into
	// Temperature=false and disable the temperature parameter's adaptation
	// for a model the gateway merely didn't mention — the same
	// omission→false hazard the tools/reasoning sub-objects are guarded
	// against.
	entry := `{"id":"m","modalities":{"input":["text"]},"capabilities":{"tools":{"supported":true},"reasoning":{"supported":true}},"limit":{"context":8192}}`
	meta, ok := mustDetect(t, entry)
	if !ok {
		t.Fatal("entry with limit must match")
	}
	if meta.Capabilities != nil {
		t.Errorf("capabilities without the temperature key must inherit, got authoritative %+v", *meta.Capabilities)
	}
	if meta.ContextWindow != 8192 {
		t.Errorf("ContextWindow = %d, want 8192 (windows still written)", meta.ContextWindow)
	}

	// An explicit temperature:false is a genuine declaration → an
	// authoritative set that really declares Temperature=false.
	entry = `{"id":"m","modalities":{"input":["text"]},"capabilities":{"tools":{"supported":true},"reasoning":{"supported":false},"temperature":false},"limit":{"context":8192}}`
	meta, ok = mustDetect(t, entry)
	if !ok {
		t.Fatal("entry with limit must match")
	}
	if meta.Capabilities == nil {
		t.Fatal("an explicit temperature:false is a full declaration, must emit")
	}
	if meta.Capabilities.Temperature {
		t.Error("Temperature = true, want false (explicitly declared)")
	}
	if !meta.Capabilities.ToolCall || meta.Capabilities.Reasoning {
		t.Errorf("ToolCall/Reasoning = %v/%v, want true/false", meta.Capabilities.ToolCall, meta.Capabilities.Reasoning)
	}

	// An explicit null expresses no value → counts as undeclared (inherit).
	entry = `{"id":"m","modalities":{"input":["text"]},"capabilities":{"tools":{"supported":true},"reasoning":{"supported":true},"temperature":null},"limit":{"context":8192}}`
	if meta, ok = mustDetect(t, entry); !ok || meta.Capabilities != nil {
		t.Errorf("temperature:null must inherit, got %v/%+v", ok, meta.Capabilities)
	}
}

func TestDetectAggregatorMetadata_ContradictoryOutputIgnored(t *testing.T) {
	// A declared output at or above the window drives EffectiveMax to zero
	// or negative and silently disables compaction, so contradictory listing
	// data (output >= context) is treated as undeclared and normalizes to 0
	// at the source — the same "contradictory data is ignored" stance as the
	// non-positive case. An output strictly below the window is a genuine
	// declaration and passes through unchanged — the wire contract is flatly
	// "0 == undeclared", so no consumer ever re-guards a negative (the write
	// loop's heuristic applies when 0).
	for _, tc := range []struct {
		entry string
		want  int
	}{
		{`{"id":"m","limit":{"context":8192,"output":32768}}`, 0},   // contradictory → undeclared
		{`{"id":"m","limit":{"context":8192,"output":8192}}`, 0},    // equal → contradictory → undeclared
		{`{"id":"m","limit":{"context":8192,"output":4096}}`, 4096}, // below → unchanged
		{`{"id":"m","limit":{"context":8192}}`, 0},                  // absent → undeclared
		{`{"id":"m","limit":{"context":8192,"output":-5}}`, 0},      // negative → undeclared
	} {
		meta, ok := mustDetect(t, tc.entry)
		if !ok {
			t.Fatalf("%s: entry with limit must match", tc.entry)
		}
		if meta.ContextWindow != 8192 {
			t.Errorf("%s: ContextWindow = %d, want 8192", tc.entry, meta.ContextWindow)
		}
		if meta.OutputLimit != tc.want {
			t.Errorf("%s: OutputLimit = %d, want %d", tc.entry, meta.OutputLimit, tc.want)
		}
	}
}

func TestDetectAggregatorMetadata_NoLimitNoMatch(t *testing.T) {
	// The limit block with a positive context is the shape's defining key;
	// without it (or with a non-positive context) the aggregator detector
	// does not match and the chain continues.
	for _, entry := range []string{
		`{"id":"m","modalities":{"input":["text"]}}`,
		`{"id":"m","limit":{"context":0,"output":4096}}`,
		`{"id":"m","limit":{"context":-5,"output":4096}}`,
		`{"id":"m","limit":{"output":4096}}`,
	} {
		if meta, ok := mustDetect(t, entry); ok {
			t.Errorf("entry %q must not match, got %+v", entry, meta)
		}
	}
}

func TestDetectLMStudioMetadata_WindowPrecedence(t *testing.T) {
	// loaded_instances beats top-level loaded_context_length beats capacity —
	// the runtime (what the server actually loaded) is the ground truth.
	instances := `{"id":"a","max_context_length":262144,"loaded_context_length":16384,
		"loaded_instances":[{"config":{"context_length":8192}}]}`
	meta, ok := mustDetect(t, instances)
	if !ok {
		t.Fatal("LM Studio entry must match")
	}
	if meta.ContextWindow != 8192 {
		t.Errorf("loaded_instances window: got %d, want 8192", meta.ContextWindow)
	}

	topLevel := `{"id":"b","max_context_length":262144,"loaded_context_length":16384}`
	if meta, ok = mustDetect(t, topLevel); !ok || meta.ContextWindow != 16384 {
		t.Errorf("loaded_context_length top-level: got %d/%v, want 16384/true", meta.ContextWindow, ok)
	}

	// Capacity-only entries (no loaded-state field) are claimed by the LM
	// Studio detector itself — the same window the old native parse returned.
	capacity := `{"id":"c","max_context_length":262144}`
	if meta, ok = mustDetect(t, capacity); !ok || meta.ContextWindow != 262144 {
		t.Errorf("capacity: got %d/%v, want 262144/true", meta.ContextWindow, ok)
	}

	// A negative loaded_context_length is an unset runtime window, not a
	// declaration: the LM Studio detector still claims the entry through its
	// advertised-capacity branch (same window the old native parse returned)
	// instead of leaking it to the OpenAI-standard detector.
	negLoaded := `{"id":"e","loaded_context_length":-5,"max_context_length":262144}`
	if meta, ok = detectLMStudioMetadata([]byte(negLoaded)); !ok || meta.ContextWindow != 262144 {
		t.Errorf("negative loaded_context_length via capacity: got %d/%v, want 262144/true", meta.ContextWindow, ok)
	}
	if meta, ok = mustDetect(t, negLoaded); !ok || meta.ContextWindow != 262144 {
		t.Errorf("negative loaded_context_length chain: got %d/%v, want 262144/true", meta.ContextWindow, ok)
	}

	// With no other window field the entry is still no match.
	if meta, ok = mustDetect(t, `{"id":"z","loaded_context_length":-5}`); ok {
		t.Errorf("negative loaded_context_length alone must not match, got %+v", meta)
	}

	if meta, ok = mustDetect(t, `{"id":"d","object":"model"}`); ok {
		t.Errorf("LM Studio entry with no window fields must not match, got %+v", meta)
	}
}

func TestDetectLMStudioMetadata_MaxModelLenShield(t *testing.T) {
	// The capacity branch is shielded: with the vLLM spelling max_model_len
	// present, the advertised capacity belongs to the OpenAI-standard
	// detector's max_model_len-first priority, so the LM Studio detector
	// must decline the entry.
	shielded := `{"id":"s","max_model_len":131072,"max_context_length":999}`
	if meta, ok := detectLMStudioMetadata([]byte(shielded)); ok {
		t.Errorf("capacity must stay unclaimed under max_model_len, got %+v", meta)
	}
	// Through the chain the entry resolves via max_model_len instead.
	if meta, ok := mustDetect(t, shielded); !ok || meta.ContextWindow != 131072 {
		t.Errorf("chain resolution: got %d/%v, want 131072/true", meta.ContextWindow, ok)
	}

	// The shield guards the capacity branch only: a loaded runtime window is
	// still claimed even when max_model_len is present.
	loaded := `{"id":"l","max_model_len":131072,"loaded_context_length":16384}`
	if meta, ok := detectLMStudioMetadata([]byte(loaded)); !ok || meta.ContextWindow != 16384 {
		t.Errorf("loaded window under max_model_len: got %d/%v, want 16384/true", meta.ContextWindow, ok)
	}
}

func TestDetectOpenAIStandardMetadata_FirstNonZeroWins(t *testing.T) {
	// max_model_len (vLLM) beats max_context_length beats context_length
	// (Ollama); an entry reporting none is not a match.
	priority := `{"id":"a","max_model_len":131072,"max_context_length":999,"context_length":111}`
	meta, ok := mustDetect(t, priority)
	if !ok || meta.ContextWindow != 131072 {
		t.Errorf("max_model_len priority: got %d/%v, want 131072/true", meta.ContextWindow, ok)
	}

	for _, tc := range []struct {
		entry string
		want  int
	}{
		{`{"id":"b","max_context_length":32768}`, 32768},
		{`{"id":"c","context_length":8192}`, 8192},
	} {
		if meta, ok = mustDetect(t, tc.entry); !ok || meta.ContextWindow != tc.want {
			t.Errorf("%s: got %d/%v, want %d/true", tc.entry, meta.ContextWindow, ok, tc.want)
		}
	}

	if meta, ok = mustDetect(t, `{"id":"gpt-4o","object":"model"}`); ok {
		t.Errorf("cloud entry with no window field must not match, got %+v", meta)
	}

	// Negative fields decline instead of falling through: a negative
	// max_model_len must not silently promote context_length, and a
	// negative capacity is no match at all.
	if meta, ok = mustDetect(t, `{"id":"h","max_model_len":-5,"context_length":8192}`); ok {
		t.Errorf("negative max_model_len must not fall through to context_length, got %+v", meta)
	}
	if meta, ok = mustDetect(t, `{"id":"n","max_context_length":-5}`); ok {
		t.Errorf("negative capacity must not match, got %+v", meta)
	}
}

func TestDetectModelMetadata_ChainOrderAndUnknownShape(t *testing.T) {
	// A bare max_context_length entry is caught by the chain (LM Studio
	// detector runs first; same window an OpenAI-standard parse would give).
	meta, ok := mustDetect(t, `{"id":"m","max_context_length":4096}`)
	if !ok || meta.ContextWindow != 4096 {
		t.Errorf("bare max_context_length: got %d/%v, want 4096/true", meta.ContextWindow, ok)
	}

	// An aggregator entry is fully handled by the aggregator detector.
	if _, ok = mustDetect(t, aggregatorListingEntry); !ok {
		t.Fatal("aggregator entry must match the chain")
	}

	// Unknown shape: every detector declines.
	for _, entry := range []string{
		`{"id":"gpt-4o","object":"model"}`,
		`not json at all`,
		`{"id":"m","limit":"not-an-object"}`,
	} {
		if meta, ok := mustDetect(t, entry); ok {
			t.Errorf("entry %q must not match any detector, got %+v", entry, meta)
		}
	}
}

func TestDetectModelMetadata_AggregatorBeforeOpenAIStandard(t *testing.T) {
	// An entry matching BOTH shapes resolves through the aggregator detector:
	// the chain order puts it ahead of the OpenAI-standard detector, so the
	// limit block wins over max_model_len.
	entry := `{"id":"m","max_model_len":8192,"limit":{"context":200000,"output":32000}}`
	meta, ok := mustDetect(t, entry)
	if !ok {
		t.Fatal("mixed aggregator+OpenAI entry must match")
	}
	if meta.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000 (aggregator limit.context, not max_model_len 8192)", meta.ContextWindow)
	}
	if meta.OutputLimit != 32000 {
		t.Errorf("OutputLimit = %d, want 32000 (only the aggregator detector declares one)", meta.OutputLimit)
	}
}

func TestAggregatorDeclaresAttachment(t *testing.T) {
	for _, tc := range []struct {
		input []string
		want  bool
	}{
		{[]string{"text"}, false},
		{[]string{"text", "image/png"}, true},
		{[]string{"application/pdf"}, true},
		{[]string{"image/webp", "text"}, true},
		{nil, false},
	} {
		if got := aggregatorDeclaresAttachment(tc.input); got != tc.want {
			t.Errorf("aggregatorDeclaresAttachment(%v) = %v, want %v", tc.input, got, tc.want)
		}
	}
}
