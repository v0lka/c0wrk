package session

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// TestLoadPlanTimeline_ReturnsOnlyPlanRolesAscending verifies that the
// plan-timeline read returns exactly the plan-lifecycle rows (declaration +
// step start/complete/paused) in ascending stream order, skipping the
// thousands of interleaved tool-call rows a plan's execution produces —
// which is what lets the panel be restored no matter how far back the
// declaration sits behind the newest history page.
func TestLoadPlanTimeline_ReturnsOnlyPlanRolesAscending(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	const sid = "plan-timeline"
	seedPagedSession(t, store, sid)

	rows := []struct {
		role string
		at   string
		meta string
	}{
		{"user", "2024-01-01T10:00:00Z", `{}`},
		{"plan", "2024-01-01T10:00:01Z", `{"steps":[{"id":"step_1"},{"id":"step_2"}]}`},
		{"tool_call", "2024-01-01T10:00:02Z", `{"tool":"read_file"}`},
		{"plan_step_start", "2024-01-01T10:00:03Z", `{"step_id":"step_1"}`},
		{"thinking", "2024-01-01T10:00:04Z", `{}`},
		{"step_todo_update", "2024-01-01T10:00:05Z", `{"step_id":"step_1","items":[]}`},
		{"plan_step_complete", "2024-01-01T10:00:06Z", `{"step_id":"step_1","success":true}`},
		{"plan_step_start", "2024-01-01T10:00:07Z", `{"step_id":"step_2"}`},
		{"plan_step_paused", "2024-01-01T10:00:08Z", `{"step_id":"step_2"}`},
		{"assistant", "2024-01-01T10:00:09Z", `{}`},
	}
	for _, r := range rows {
		if err := store.SaveMessage(context.Background(), ChatMessage{
			SessionID: sid,
			Role:      r.role,
			Content:   r.role,
			Metadata:  json.RawMessage(r.meta),
			CreatedAt: r.at,
		}); err != nil {
			t.Fatalf("SaveMessage(%s): %v", r.role, err)
		}
	}

	got, err := store.LoadPlanTimeline(context.Background(), sid)
	if err != nil {
		t.Fatalf("LoadPlanTimeline: %v", err)
	}

	wantRoles := []string{"plan", "plan_step_start", "plan_step_complete", "plan_step_start", "plan_step_paused"}
	if len(got) != len(wantRoles) {
		t.Fatalf("got %d rows, want %d: roles=%v", len(got), len(wantRoles), rolesOf(got))
	}
	for i, want := range wantRoles {
		if got[i].Role != want {
			t.Fatalf("row %d role = %q, want %q (full: %v)", i, got[i].Role, want, rolesOf(got))
		}
	}
	// Metadata must survive the round-trip (the frontend rebuild reads
	// steps / step_id / success out of it).
	if string(got[0].Metadata) != `{"steps":[{"id":"step_1"},{"id":"step_2"}]}` {
		t.Fatalf("plan row metadata = %s", got[0].Metadata)
	}
}

// TestLoadPlanTimeline_EmptyForPlanlessSession verifies a session with no
// plan rows yields an empty (non-nil) timeline and no error — the restore
// path must treat that as "nothing to rebuild", not as a failure.
func TestLoadPlanTimeline_EmptyForPlanlessSession(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	const sid = "plan-timeline-empty"
	seedPagedSession(t, store, sid)
	savePagedMessage(t, store, sid, "user", "hello", "2024-01-01T10:00:00Z")
	savePagedMessage(t, store, sid, "assistant", "hi", "2024-01-01T10:00:01Z")

	got, err := store.LoadPlanTimeline(context.Background(), sid)
	if err != nil {
		t.Fatalf("LoadPlanTimeline: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d rows for a plan-less session, want 0: %v", len(got), rolesOf(got))
	}

	// Session isolation: another session's plan rows must not leak in.
	const other = "plan-timeline-other"
	seedPagedSession(t, store, other)
	if err := store.SaveMessage(context.Background(), ChatMessage{
		SessionID: other,
		Role:      "plan",
		Content:   "",
		Metadata:  json.RawMessage(`{"steps":[{"id":"x"}]}`),
		CreatedAt: "2024-01-01T11:00:00Z",
	}); err != nil {
		t.Fatalf("SaveMessage(plan): %v", err)
	}
	got, err = store.LoadPlanTimeline(context.Background(), sid)
	if err != nil {
		t.Fatalf("LoadPlanTimeline after seeding other session: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("rows leaked across sessions: %v", rolesOf(got))
	}
}

func rolesOf(msgs []ChatMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Role)
	}
	return out
}

// TestLoadPlanTimeline_CapKeepsNewestRows pins the cap's truncation side:
// when a pathological session exceeds planTimelineLimit plan-lifecycle rows,
// the read must keep the NEWEST rows — the declaration and step rows of the
// plan currently in flight, exactly what this read path exists to recover.
// An ASC-first LIMIT kept the OLDEST rows instead and silently dropped the
// current plan.
func TestLoadPlanTimeline_CapKeepsNewestRows(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	const sid = "plan-timeline-cap"
	seedPagedSession(t, store, sid)

	total := planTimelineLimit + 3
	for i := 0; i < total; i++ {
		role := "plan_step_start"
		if i == 0 || i == total-3 {
			// The original (oldest) declaration and the current plan's
			// declaration, declared three rows before the end.
			role = "plan"
		}
		if err := store.SaveMessage(context.Background(), ChatMessage{
			SessionID: sid,
			Role:      role,
			Content:   fmt.Sprintf("row-%d", i),
			Metadata:  json.RawMessage(`{}`),
			// All rows share one created_at second: the id tiebreaker (not a
			// timestamp spread) must decide what survives the cap.
			CreatedAt: "2024-01-01T10:00:00Z",
		}); err != nil {
			t.Fatalf("SaveMessage(%d): %v", i, err)
		}
	}

	got, err := store.LoadPlanTimeline(context.Background(), sid)
	if err != nil {
		t.Fatalf("LoadPlanTimeline: %v", err)
	}
	if len(got) != planTimelineLimit {
		t.Fatalf("got %d rows, want exactly the %d-row cap", len(got), planTimelineLimit)
	}
	// Still ascending, with the truncation on the OLDEST side: the first
	// surviving row is row-3 and the newest row is last.
	if got[0].Content != "row-3" {
		t.Errorf("oldest surviving row = %q, want %q (the oldest rows must be the truncated ones)", got[0].Content, "row-3")
	}
	if got[len(got)-1].Content != fmt.Sprintf("row-%d", total-1) {
		t.Errorf("newest row = %q, want %q", got[len(got)-1].Content, fmt.Sprintf("row-%d", total-1))
	}
	// The current plan's declaration (three rows before the end) survived.
	currentDecl := got[len(got)-3]
	if currentDecl.Role != "plan" || currentDecl.Content != fmt.Sprintf("row-%d", total-3) {
		t.Errorf("current plan declaration lost to the cap: role=%q content=%q", currentDecl.Role, currentDecl.Content)
	}
	for _, m := range got {
		if m.Content == "row-0" || m.Content == "row-1" || m.Content == "row-2" {
			t.Fatalf("row %q must have been truncated, not returned", m.Content)
		}
	}
}
