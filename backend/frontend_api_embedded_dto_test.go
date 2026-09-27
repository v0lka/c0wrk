package backend

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// Tests of the embedded local model's WIRE SHAPES: the DTOs and their mappers
// in backend/frontend_api_embedded_dto.go — the two event payloads, the
// always-an-array rule, the guard disclosure shapes and the additive-on-the-wire
// guarantee the frontend type guards rely on.
//
// Scope: the boundary contract only. What a shape MEANS — how the planner
// derived a plan, how a tuning override resolves into a launch command — lives
// in core/embeddedllm and is covered there; the RPC behaviour that fills these
// shapes is covered in frontend_api_embedded_test.go and
// frontend_api_embedded_tuning_test.go.

// TestEmbeddedLLMEventPayloadsSerializeToTheCatalogShape pins the wire shape of
// both events: the JSON keys the frontend type guards and the event catalog
// depend on. A renamed Go field would otherwise silently break the UI.
func TestEmbeddedLLMEventPayloadsSerializeToTheCatalogShape(t *testing.T) {
	state := EmbeddedLLMStateData{
		Installed: true, Loading: false, Loaded: true,
		Packing: "PQ2_0", Backend: "metal", Port: 4321,
		ContextSize: 32768, AutoUnloadMinutes: 60, Error: "",
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling the state payload: %v", err)
	}
	var stateKeys map[string]any
	if err := json.Unmarshal(stateJSON, &stateKeys); err != nil {
		t.Fatalf("unmarshalling the state payload: %v", err)
	}
	for _, key := range []string{
		"installed", "loading", "loaded", "packing", "backend",
		"port", "context_size", "auto_unload_minutes", "error",
	} {
		if _, ok := stateKeys[key]; !ok {
			t.Errorf("the state payload is missing the %q key: %s", key, stateJSON)
		}
	}
	if len(stateKeys) != 9 {
		t.Errorf("the state payload carries %d keys (%s), want exactly the 9 documented ones",
			len(stateKeys), stateJSON)
	}

	progress := EmbeddedLLMProgressData{
		Component: "model", Stage: "downloading", BytesDone: 1, BytesTotal: 2,
	}
	progressJSON, err := json.Marshal(progress)
	if err != nil {
		t.Fatalf("marshalling the progress payload: %v", err)
	}
	if want := `{"component":"model","stage":"downloading","bytes_done":1,"bytes_total":2}`; string(progressJSON) != want {
		t.Errorf("progress payload = %s, want %s", progressJSON, want)
	}
}

// TestEmbeddedGuardIDsMarksUnappliedDecisions covers the log rendering: an
// unapplied guard must not read like an applied one, because the difference is
// whether the install is actually running the recommended build.
func TestEmbeddedGuardIDsMarksUnappliedDecisions(t *testing.T) {
	if got := embeddedGuardIDs(nil); got != "none" {
		t.Errorf("embeddedGuardIDs(nil) = %q, want %q", got, "none")
	}
	got := embeddedGuardIDs([]embeddedllm.GuardDecision{
		{Guard: embeddedllm.GuardCUDA133Crash, Applied: true},
		{Guard: embeddedllm.GuardVulkanIntelArcHang},
	})
	if got != "cuda-13.3-crash,vulkan-intel-arc-hang(unapplied)" {
		t.Errorf("embeddedGuardIDs = %q", got)
	}
}

// TestEmbeddedPlanDTORendersTheOffloadDecision pins the derived offload mode,
// including the two sentinels a renderer would otherwise have to guess at: a nil
// Layers is -1 ("the flag is omitted"), and an explicit zero cache ceiling is 0
// ("the cache is disabled"), which are different facts.
func TestEmbeddedPlanDTORendersTheOffloadDecision(t *testing.T) {
	cpuOnly := 0
	partial := 24
	every := 99

	cases := []struct {
		name      string
		plan      *embeddedllm.MemoryPlan
		wantMode  string
		wantLayer int
		wantCache int
	}{
		{"nil plan", nil, config.EmbeddedLLMTuningAuto, -1, -1},
		{"under fit the runtime sizes it", &embeddedllm.MemoryPlan{Fit: true},
			config.EmbeddedLLMTuningAuto, -1, -1},
		{"no -ngl at all", &embeddedllm.MemoryPlan{}, config.EmbeddedLLMTuningAuto, -1, -1},
		{"nothing offloaded", &embeddedllm.MemoryPlan{Layers: &cpuOnly},
			config.EmbeddedLLMOffloadCPU, 0, -1},
		{"a partial offload", &embeddedllm.MemoryPlan{Layers: &partial},
			config.EmbeddedLLMOffloadLayers, 24, -1},
		// Core spells "every layer" with its own ceiling sentinel, which this
		// layer must not transcribe: an every-layer offload renders as a count.
		{"every layer is a count, not a label", &embeddedllm.MemoryPlan{Layers: &every},
			config.EmbeddedLLMOffloadLayers, 99, -1},
		{"a disabled prompt cache is 0, not omitted", &embeddedllm.MemoryPlan{CacheRAMMiB: tuningIntPtr(0)},
			config.EmbeddedLLMTuningAuto, -1, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := embeddedPlanDTO(tc.plan)
			if got.OffloadMode != tc.wantMode {
				t.Errorf("OffloadMode = %q, want %q", got.OffloadMode, tc.wantMode)
			}
			if got.Layers != tc.wantLayer {
				t.Errorf("Layers = %d, want %d", got.Layers, tc.wantLayer)
			}
			if got.CacheRAMMiB != tc.wantCache {
				t.Errorf("CacheRAMMiB = %d, want %d", got.CacheRAMMiB, tc.wantCache)
			}
			if got.Notes == nil {
				t.Error("Notes is nil, want an array — the boundary never carries null")
			}
			if got.Recorded != (tc.plan != nil) {
				t.Errorf("Recorded = %v, want %v", got.Recorded, tc.plan != nil)
			}
		})
	}
}

// TestEmbeddedLLMStatusAdditionsAreAdditiveOnTheWire is the guard-degradation
// criterion expressed on this side of the boundary: the payload still serializes
// with every pre-existing key present and correctly typed, the new composite
// fields never serialize as null, and nothing was renamed or retyped.
func TestEmbeddedLLMStatusAdditionsAreAdditiveOnTheWire(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	manifest := embeddedTestManifest(4321, 32768)
	topology := embeddedTopologyFixture()
	manifest.Topology = &topology
	manifest.Plan = &embeddedllm.MemoryPlan{Packing: embeddedllm.PackingPQ2_0, Parallel: 1}
	writeEmbeddedInstallTree(t, f.agentDir, manifest)
	f.Lifecycle().InitEmbeddedLLM()

	encoded, err := json.Marshal(f.GetEmbeddedLLMStatus())
	if err != nil {
		t.Fatalf("marshalling the status: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshalling the status: %v", err)
	}

	// The 21 fields the existing frontend guard checks, with the type each
	// typeof-branch expects. A rename or a retype here is what would break an
	// older UI, so they are pinned explicitly.
	older := map[string]string{
		"state": "string", "installed": "boolean", "installing": "boolean",
		"loading": "boolean", "loaded": "boolean", "packing": "string",
		"backend": "string", "port": "number", "context_size": "number",
		"auto_unload_enabled": "boolean", "auto_unload_minutes": "number",
		"idle_remaining_seconds": "number", "base_url": "string",
		"model_id": "string", "model_name": "string", "runtime_version": "string",
		"installed_at": "string", "model_file": "string", "pid": "number",
		"error": "string", "available": "boolean",
	}
	for key, want := range older {
		value, ok := raw[key]
		if !ok {
			t.Errorf("the pre-existing field %q is missing from the payload", key)
			continue
		}
		if got := jsonKind(value); got != want {
			t.Errorf("the pre-existing field %q is a %s, want %s", key, got, want)
		}
	}

	// The additions.
	additions := map[string]string{
		"devices": "array", "unified": "boolean", "host_ram_gib": "number",
		"device_budget_mib": "number", "host_budget_mib": "number",
		"topology_probed_at": "string", "plan": "object",
		"reload_required": "boolean",
	}
	for key, want := range additions {
		value, ok := raw[key]
		if !ok {
			t.Errorf("the new field %q is missing from the payload", key)
			continue
		}
		if got := jsonKind(value); got != want {
			t.Errorf("the new field %q is a %s, want %s", key, got, want)
		}
	}

	// fit_warning is the one addition allowed to be ABSENT: omitempty keeps a
	// healthy status free of it, so the additive contract here is "absent or a
	// string", never null and never another type.
	if value, ok := raw["fit_warning"]; ok {
		if got := jsonKind(value); got != "string" {
			t.Errorf("fit_warning is a %s, want a string", got)
		}
	}

	// A null array is the one shape the "always present" contract forbids, so
	// check it on the unpopulated payload too.
	var plan struct {
		Notes    []string `json:"notes"`
		Recorded bool     `json:"recorded"`
	}
	if err := json.Unmarshal(raw["plan"], &plan); err != nil {
		t.Fatalf("unmarshalling plan: %v", err)
	}
	if plan.Notes == nil {
		t.Error("plan.notes serialized as null, want an array")
	}

	empty, err := json.Marshal(EmbeddedLLMStatus{Devices: []EmbeddedLLMDevice{}, Plan: embeddedPlanDTO(nil)})
	if err != nil {
		t.Fatalf("marshalling an empty status: %v", err)
	}
	if strings.Contains(string(empty), `"devices":null`) || strings.Contains(string(empty), `"notes":null`) {
		t.Errorf("an unpopulated status serialized a null array: %s", empty)
	}
}

// jsonKind reports the JSON typeof of a raw value, using the same vocabulary as
// the frontend guard's typeof checks.
func jsonKind(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	switch {
	case text == "null":
		return "null"
	case strings.HasPrefix(text, "\""):
		return "string"
	case strings.HasPrefix(text, "["):
		return "array"
	case strings.HasPrefix(text, "{"):
		return "object"
	case text == "true" || text == "false":
		return "boolean"
	default:
		return "number"
	}
}

// TestEmbeddedDevicesDTONeverCarriesANullArray pins the always-an-array rule on
// the probe's own DTO, which the status additions share.
func TestEmbeddedDevicesDTONeverCarriesANullArray(t *testing.T) {
	dto := embeddedDevicesDTO(embeddedllm.MemoryTopology{})
	if dto.Devices == nil {
		t.Fatal("Devices is nil for an empty topology, want an empty array")
	}
	encoded, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if strings.Contains(string(encoded), `"devices":null`) {
		t.Errorf("an empty topology serialized a null array: %s", encoded)
	}
}
