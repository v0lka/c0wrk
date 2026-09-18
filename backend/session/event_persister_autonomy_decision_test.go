package session

import (
	"encoding/json"
	"strings"
	"testing"

	coretools "github.com/v0lka/c0wrk/core/tools"
)

// TestEventPersister_AutonomyDecisionIsPersisted pins that an automatic
// (no-human) decision is DURABLE. Unlike the strict-judge phase telemetry
// (transient), the audit trail of a gate a human would otherwise have answered
// must survive a reload — the trajectory must stay reconstructable (ASI10).
// The `mode` field names the autonomy posture (assisted|silent) and `policy`
// the sub-policy that decided, so a reloaded row keeps both the posture and
// the mechanism on record.
func TestEventPersister_AutonomyDecisionIsPersisted(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	p.Persist(Event{SessionID: "s1", Type: EventAutonomyDecision, Data: AutonomyDecisionData{
		Kind:          "tool_confirm",
		Mode:          coretools.AutonomyModeSilent,
		Policy:        coretools.SilentToolConfirmJudge,
		Verdict:       "deny",
		Tool:          "bash_exec",
		Source:        "core",
		Reason:        "runs a shell command",
		Justification: "ASI05: unverified download",
	}})

	rows := store.snapshot()
	if len(rows) != 1 {
		t.Fatalf("autonomy_decision must be persisted, got %d rows: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.Role != EventAutonomyDecision {
		t.Errorf("role = %q, want %q", row.Role, EventAutonomyDecision)
	}
	if row.SessionID != "s1" {
		t.Errorf("session = %q, want s1", row.SessionID)
	}

	// The full payload must ride in metadata so the frontend can rebuild the
	// identical notice on reload (reconstructContent reads these keys).
	var meta map[string]any
	if err := json.Unmarshal(row.Metadata, &meta); err != nil {
		t.Fatalf("metadata is not valid JSON (%q): %v", row.Metadata, err)
	}
	for k, want := range map[string]string{
		"kind":          "tool_confirm",
		"mode":          coretools.AutonomyModeSilent,
		"policy":        coretools.SilentToolConfirmJudge,
		"verdict":       "deny",
		"tool":          "bash_exec",
		"source":        "core",
		"reason":        "runs a shell command",
		"justification": "ASI05: unverified download",
	} {
		if got, _ := meta[k].(string); got != want {
			t.Errorf("metadata[%q] = %q, want %q", k, got, want)
		}
	}

	// The persister writes the raw payload JSON into content for non-assistant
	// roles; it must not be empty (the frontend reconstructs from metadata).
	if strings.TrimSpace(row.Content) == "" {
		t.Error("content must carry the payload JSON, not be empty")
	}
}

// TestEventPersister_AutonomyDecisionStepLimitShape pins that a step-limit
// decision persists its numeric boundary fields too, so the reloaded notice can
// render "at step N/M".
func TestEventPersister_AutonomyDecisionStepLimitShape(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	p.Persist(Event{SessionID: "s1", Type: EventAutonomyDecision, Data: AutonomyDecisionData{
		Kind:          "step_limit",
		Mode:          coretools.AutonomyModeSilent,
		Policy:        "auto",
		Verdict:       "allow_more",
		Justification: "fresh budget granted",
		Category:      "budget",
		CurrentStep:   20,
		MaxSteps:      20,
	}})

	rows := store.snapshot()
	if len(rows) != 1 {
		t.Fatalf("step_limit autonomy_decision must be persisted, got %d rows", len(rows))
	}
	var meta map[string]any
	if err := json.Unmarshal(rows[0].Metadata, &meta); err != nil {
		t.Fatalf("metadata is not valid JSON: %v", err)
	}
	if got, _ := meta["kind"].(string); got != "step_limit" {
		t.Errorf("kind = %q, want step_limit", got)
	}
	if got, _ := meta["mode"].(string); got != coretools.AutonomyModeSilent {
		t.Errorf("mode = %q, want %q", got, coretools.AutonomyModeSilent)
	}
	if got, _ := meta["verdict"].(string); got != "allow_more" {
		t.Errorf("verdict = %q, want allow_more", got)
	}
	if got, _ := meta["current_step"].(float64); got != 20 {
		t.Errorf("current_step = %v, want 20", meta["current_step"])
	}
	if got, _ := meta["max_steps"].(float64); got != 20 {
		t.Errorf("max_steps = %v, want 20", meta["max_steps"])
	}
}
