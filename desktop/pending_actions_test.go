package desktop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend/session"
	coretools "github.com/v0lka/c0wrk/core/tools"
)

// TestGetPendingActions_EmptyReturnsNonNilSlices guards against the Go
// nil-slice JSON gotcha: a nil slice marshals to `null`, which the frontend
// shape guard (isPendingActionsResponse) rejects — silently disabling HITL
// reconciliation on session switch. Every kind must serialize to `[]` (not
// null) even when no pending actions of that kind exist.
func TestGetPendingActions_EmptyReturnsNonNilSlices(t *testing.T) {
	a := &App{}

	resp, err := a.GetPendingActions("sess-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ToolConfirms == nil || resp.StepLimits == nil || resp.PlanApprovals == nil || resp.AskUser == nil || resp.GoalProposals == nil {
		t.Fatalf("expected all slices non-nil, got %+v", resp)
	}
	if len(resp.ToolConfirms) != 0 || len(resp.StepLimits) != 0 ||
		len(resp.PlanApprovals) != 0 || len(resp.AskUser) != 0 || len(resp.GoalProposals) != 0 {
		t.Fatalf("expected all slices empty, got %+v", resp)
	}

	// The critical assertion: empty kinds marshal to JSON `[]`, never `null`.
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	asString := string(raw)
	for _, want := range []string{`"tool_confirms":[]`, `"step_limits":[]`, `"plan_approvals":[]`, `"ask_user":[]`, `"goal_proposals":[]`} {
		if !strings.Contains(asString, want) {
			t.Errorf("expected %s in JSON, got %s", want, asString)
		}
	}
}

// TestGetPendingActions_PartialKeepsEmptyKindsNonNil verifies that a session
// with only one kind of pending action (the common case) still returns
// non-nil empty slices for the other kinds — otherwise the frontend would
// reject the whole response and drop the live pending prompt.
func TestGetPendingActions_PartialKeepsEmptyKindsNonNil(t *testing.T) {
	a := &App{}
	const sid = "sess-1"

	a.pendingAskUser.Store("req-1", &pendingAskUserEntry{
		ch:        make(chan coretools.AskUserResponse, 1),
		sessionID: sid,
		payload: session.AskUserPayload{
			RequestID: "req-1",
			Questions: []coretools.AskUserQuestion{
				{ID: "q1", Question: "Pick one", Options: []coretools.AskUserOption{{Label: "A", Value: "a"}}},
			},
		},
	})
	t.Cleanup(func() { a.pendingAskUser.Delete("req-1") })

	resp, err := a.GetPendingActions(sid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.AskUser) != 1 || resp.AskUser[0].RequestID != "req-1" {
		t.Fatalf("expected one ask_user (req-1), got %+v", resp.AskUser)
	}
	// The bug: these were nil (→ JSON null) when no entries of the kind exist.
	if resp.ToolConfirms == nil || resp.StepLimits == nil || resp.PlanApprovals == nil {
		t.Fatalf("expected empty (non-nil) slices for absent kinds, got %+v", resp)
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The empty kinds must serialize to `[]`, never `null` (the bug). Check
	// each absent kind explicitly rather than substring-matching "null", which
	// would falsely fail if a value (e.g. args/plan_content) legitimately
	// contained that substring.
	for _, want := range []string{`"tool_confirms":[]`, `"step_limits":[]`, `"plan_approvals":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("expected %s in JSON, got %s", want, raw)
		}
	}
}

// TestGetPendingActions_FiltersBySession ensures entries from other sessions
// are excluded.
func TestGetPendingActions_FiltersBySession(t *testing.T) {
	a := &App{}
	a.pendingAskUser.Store("req-other", &pendingAskUserEntry{
		ch:        make(chan coretools.AskUserResponse, 1),
		sessionID: "other-session",
		payload:   session.AskUserPayload{RequestID: "req-other"},
	})
	t.Cleanup(func() { a.pendingAskUser.Delete("req-other") })

	resp, err := a.GetPendingActions("this-session")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.AskUser) != 0 {
		t.Errorf("expected no ask_user for this-session, got %+v", resp.AskUser)
	}
}
