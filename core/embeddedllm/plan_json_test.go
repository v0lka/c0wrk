package embeddedllm

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestMemoryPlanJSONCheckpointDefaults(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		input string
		count int
		idle  bool
	}{
		{"legacy", `{"context_size":65536,"parallel":1}`, DefaultCtxCheckpoints, true},
		{"explicit disabled", `{"ctx_checkpoints":0,"cache_idle_slots":false}`, 0, false},
		{"count only", `{"ctx_checkpoints":8}`, 8, true},
		{"idle only", `{"cache_idle_slots":false}`, DefaultCtxCheckpoints, false},
		{"explicit enabled", `{"ctx_checkpoints":64,"cache_idle_slots":true}`, 64, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var manifest Manifest
			if err := json.Unmarshal([]byte(`{"plan":`+tc.input+`}`), &manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.Plan == nil {
				t.Fatal("missing plan")
			}
			if manifest.Plan.CtxCheckpoints != tc.count || manifest.Plan.CacheIdleSlots != tc.idle {
				t.Fatalf("checkpoint policy = %d/%v, want %d/%v", manifest.Plan.CtxCheckpoints, manifest.Plan.CacheIdleSlots, tc.count, tc.idle)
			}
			// The policy survives persistence, including explicit disable values.
			encoded, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			var restored Manifest
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(manifest, restored) {
				t.Fatalf("manifest did not round-trip: %s", encoded)
			}
		})
	}
}

func TestMemoryPlanJSONFreshDecodeAndFailureAtomicity(t *testing.T) {
	t.Parallel()
	plan := MemoryPlan{ContextSize: 123, CtxCheckpoints: 8, CacheIdleSlots: false}
	before := plan
	if err := json.Unmarshal([]byte(`{"context_size":"bad","ctx_checkpoints":64}`), &plan); err == nil {
		t.Fatal("invalid field type accepted")
	}
	if !reflect.DeepEqual(plan, before) {
		t.Fatal("failed decode partially changed the plan")
	}
	if err := json.Unmarshal([]byte(`{}`), &plan); err != nil {
		t.Fatal(err)
	}
	want := MemoryPlan{CtxCheckpoints: DefaultCtxCheckpoints, CacheIdleSlots: true}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("fresh decode = %+v, want %+v", plan, want)
	}
}
