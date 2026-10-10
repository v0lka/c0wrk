package core

import (
	"bytes"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/orchestration"
)

// The bound-and-truncate family: loggingEmitter deliberately bounds every
// model/judge-authored field it forwards to the persistent session log
// (length + 200-rune preview, or shape-only for payload maps). These tests pin
// the policy on every handler the review flagged (#83/#102/#120/#170) — the
// same invariant TestLoggingEmitter_SubAgentComplete_BoundsErrMsgPreview
// established for SubAgentComplete.

// secretProse is long model-authored-looking text; if any handler logged its
// payload unbounded, this would land verbatim in the persisted session log.
func secretProse() string { return strings.Repeat("secret-bearing prose ", 200) } // 4200 bytes

func newBufLoggerEmitter(t *testing.T) (Emitter, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewLoggingEmitter(&spyEmitter{}, logger), &buf
}

// assertBounded asserts the shared invariant: the full payload never reaches
// the log, a <key>Len attribute is present, and the preview is truncated
// (ellipsis marker). inner must still receive the untruncated value.
func assertBounded(t *testing.T, buf *bytes.Buffer, lenKey, payload string) {
	t.Helper()
	logged := buf.String()
	if strings.Contains(logged, payload) {
		t.Fatalf("expected full payload NOT to be logged verbatim; log: %s", logged)
	}
	if want := lenKey + "=" + strconv.Itoa(len(payload)); !strings.Contains(logged, want) {
		t.Errorf("expected %s in log output; got: %s", want, logged)
	}
	if !strings.Contains(logged, "…") {
		t.Errorf("expected a truncated preview with ellipsis marker in log output; got: %s", logged)
	}
}

func TestLoggingEmitter_SubAgentLaunch_BoundsDescription(t *testing.T) {
	e, buf := newBufLoggerEmitter(t)
	desc := secretProse()

	e.SubAgentLaunch("step_1", desc)

	assertBounded(t, buf, "descriptionLen", desc)
}

func TestLoggingEmitter_PlanStepStart_BoundsDescriptionAndSummary(t *testing.T) {
	e, buf := newBufLoggerEmitter(t)
	desc := secretProse()
	summary := secretProse()

	e.PlanStepStart("step_1", desc, summary)

	assertBounded(t, buf, "descriptionLen", desc)
	assertBounded(t, buf, "summaryLen", summary)
}

func TestLoggingEmitter_PlanStepComplete_BoundsErrMsg(t *testing.T) {
	e, buf := newBufLoggerEmitter(t)
	errMsg := secretProse()

	e.PlanStepComplete("step_1", false, time.Second, errMsg)

	assertBounded(t, buf, "errMsgLen", errMsg)
}

func TestLoggingEmitter_PlanStepPaused_BoundsErrMsg(t *testing.T) {
	e, buf := newBufLoggerEmitter(t)
	errMsg := secretProse()

	e.PlanStepPaused("step_1", time.Second, errMsg)

	assertBounded(t, buf, "errMsgLen", errMsg)
}

func TestLoggingEmitter_Reflection_BoundsProse(t *testing.T) {
	e, buf := newBufLoggerEmitter(t)
	summary := secretProse()
	action := secretProse()
	cause := secretProse()

	e.Reflection(&orchestration.Reflection{Summary: summary, SuggestedAction: action, RootCause: cause}, 1, 3)

	assertBounded(t, buf, "summaryLen", summary)
	assertBounded(t, buf, "suggestedActionLen", action)
	assertBounded(t, buf, "rootCauseLen", cause)
}

func TestLoggingEmitter_Finishing_BoundsSummary(t *testing.T) {
	e, buf := newBufLoggerEmitter(t)
	summary := secretProse()

	e.Finishing(1, summary)

	assertBounded(t, buf, "summaryLen", summary)
}

func TestLoggingEmitter_Service_BoundsContent(t *testing.T) {
	e, buf := newBufLoggerEmitter(t)
	content := secretProse()

	e.Service(content)

	assertBounded(t, buf, "contentLen", content)
}

func TestLoggingEmitter_ServiceWithMeta_BoundsContentAndMetaShape(t *testing.T) {
	e, buf := newBufLoggerEmitter(t)
	content := secretProse()

	e.ServiceWithMeta(content, map[string]any{"secret": "quoted-file-contents"})

	assertBounded(t, buf, "contentLen", content)
	logged := buf.String()
	if strings.Contains(logged, "quoted-file-contents") {
		t.Fatalf("expected meta values NOT to be logged verbatim; log: %s", logged)
	}
	if !strings.Contains(logged, "metaShape=secret(20)") {
		t.Errorf("expected the meta shape (key + byte length) in the log; got: %s", logged)
	}
}

func TestLoggingEmitter_GoalPayloads_LogMetadataAndShapeOnly(t *testing.T) {
	condition := "the whole test suite passes: " + secretProse()
	reason := secretProse()
	verdict := "met — " + secretProse() // verdict.Status is free-form model text
	data := map[string]any{
		"status":    "active",
		"turn":      3,
		"max_turns": 10,
		"condition": condition,
		"reason":    reason,
		"evidence":  []string{"e1"},
		"verdict":   verdict,
	}

	t.Run("goal_status", func(t *testing.T) {
		e, buf := newBufLoggerEmitter(t)
		e.GoalStatus(data)

		logged := buf.String()
		for _, secret := range []string{condition, reason, verdict} {
			if strings.Contains(logged, secret) {
				t.Fatalf("expected goal prose NOT to be logged verbatim; log: %s", logged)
			}
		}
		// Non-prose metadata stays visible for debugging.
		for _, want := range []string{"status=active", "turn=3", "max_turns=10"} {
			if !strings.Contains(logged, want) {
				t.Errorf("expected %s in the log; got: %s", want, logged)
			}
		}
		// The verdict appears only as a bounded preview (first 199 runes plus
		// the ellipsis marker), never the whole free-form model text.
		if !strings.Contains(logged, `verdict="`+verdict[:199]) {
			t.Errorf("expected a bounded verdict preview in the log; got: %s", logged)
		}
		// The prose payload appears only as shape (key + length/type).
		if !strings.Contains(logged, "condition("+strconv.Itoa(len(condition))+")") {
			t.Errorf("expected condition's length in payloadShape; got: %s", logged)
		}
		if !strings.Contains(logged, "evidence([]string)") {
			t.Errorf("expected non-string values as types in payloadShape; got: %s", logged)
		}
	})

	t.Run("goal_progress", func(t *testing.T) {
		e, buf := newBufLoggerEmitter(t)
		e.GoalProgress(data)

		logged := buf.String()
		if strings.Contains(logged, condition) || strings.Contains(logged, reason) {
			t.Fatalf("expected goal prose NOT to be logged verbatim; log: %s", logged)
		}
		if !strings.Contains(logged, "turn=3") {
			t.Errorf("expected turn metadata in the log; got: %s", logged)
		}
	})
}

func TestLoggingEmitter_ExecutorDiagnostic_LogsShapeOnly(t *testing.T) {
	e, buf := newBufLoggerEmitter(t)
	secret := secretProse()

	e.ExecutorDiagnostic(2, "nudge", map[string]any{"details": secret, "code": 7})

	logged := buf.String()
	if strings.Contains(logged, secret) {
		t.Fatalf("expected diagnostic values NOT to be logged verbatim; log: %s", logged)
	}
	if !strings.Contains(logged, "details("+strconv.Itoa(len(secret))+")") {
		t.Errorf("expected the details shape in the log; got: %s", logged)
	}
	if !strings.Contains(logged, "code(int)") {
		t.Errorf("expected non-string values as types in the log; got: %s", logged)
	}
}

func TestPayloadShape(t *testing.T) {
	if got := payloadShape(nil); got != "" {
		t.Errorf("payloadShape(nil) = %q, want empty", got)
	}
	if got := payloadShape(map[string]any{}); got != "" {
		t.Errorf("payloadShape(empty) = %q, want empty", got)
	}
	got := payloadShape(map[string]any{
		"condition": "abc",
		"turn":      3,
		"evidence":  []string{"x"},
	})
	// Sorted keys, string values as byte lengths, others as Go types.
	want := "condition(3), evidence([]string), turn(int)"
	if got != want {
		t.Errorf("payloadShape = %q, want %q", got, want)
	}
}
