package hitlwire

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// silentLogger returns a logger that discards all output. Tests assert behavior
// (channel sends, pending-map cleanup), not log lines.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

// captureLogger returns a logger writing to the returned buffer so tests can
// inspect warning lines.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// --- extractPayload ---

func TestExtractPayload_EmptyData(t *testing.T) {
	log, buf := captureLogger()
	_, ok := extractPayload("ev", nil, log)
	if ok {
		t.Fatal("expected ok=false for empty data")
	}
	if !strings.Contains(buf.String(), "missing payload") {
		t.Errorf("expected 'missing payload' warning, got %q", buf.String())
	}
}

func TestExtractPayload_WrongType(t *testing.T) {
	log, buf := captureLogger()
	_, ok := extractPayload("ev", []any{"not a map"}, log)
	if ok {
		t.Fatal("expected ok=false for wrong type")
	}
	if !strings.Contains(buf.String(), "unexpected payload type") {
		t.Errorf("expected 'unexpected payload type' warning, got %q", buf.String())
	}
}

func TestExtractPayload_Success(t *testing.T) {
	want := map[string]any{"k": "v"}
	got, ok := extractPayload("ev", []any{want}, silentLogger())
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got["k"] != "v" {
		t.Errorf("got %v, want %v", got, want)
	}
}

// --- parseConfirmDecision ---

func TestParseConfirmDecision_NonIntegralFloatRejected(t *testing.T) {
	log, buf := captureLogger()
	for _, decision := range []float64{0.5, -0.9, 1.9} {
		payload := map[string]any{"decision": decision}
		got, ok := parseConfirmDecision(payload, log)
		if ok {
			t.Errorf("decision %v: expected ok=false, got %v", decision, got)
		}
	}
	if !strings.Contains(buf.String(), "non-integral confirmation decision") {
		t.Errorf("expected 'non-integral confirmation decision' warning, got %q", buf.String())
	}
}

func TestParseConfirmDecision(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
		want    sdktools.ConfirmationResponse
		wantOK  bool
	}{
		{name: "missing field", payload: map[string]any{}, wantOK: false},
		{name: "string allow_once", payload: map[string]any{"decision": "allow_once"}, want: sdktools.ConfirmAllowOnce, wantOK: true},
		{name: "string deny", payload: map[string]any{"decision": "deny"}, want: sdktools.ConfirmDeny, wantOK: true},
		{name: "string stop", payload: map[string]any{"decision": "stop"}, want: sdktools.ConfirmDenyAndStop, wantOK: true},
		{name: "string deny_and_stop", payload: map[string]any{"decision": "deny_and_stop"}, want: sdktools.ConfirmDenyAndStop, wantOK: true},
		{name: "unknown string", payload: map[string]any{"decision": "approve_forever"}, wantOK: false},
		{name: "float64 (JSON number)", payload: map[string]any{"decision": float64(int(sdktools.ConfirmAllowOnce))}, want: sdktools.ConfirmAllowOnce, wantOK: true},
		{name: "int", payload: map[string]any{"decision": int(sdktools.ConfirmDeny)}, want: sdktools.ConfirmDeny, wantOK: true},
		{name: "unsupported type bool", payload: map[string]any{"decision": true}, wantOK: false},
		{name: "unsupported type nil", payload: map[string]any{"decision": nil}, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseConfirmDecision(tt.payload, silentLogger())
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// --- confirmDecisionString ---

func TestConfirmDecisionString(t *testing.T) {
	tests := []struct {
		name string
		resp sdktools.ConfirmationResponse
		want string
	}{
		{name: "allow_once", resp: sdktools.ConfirmAllowOnce, want: "allow_once"},
		{name: "deny", resp: sdktools.ConfirmDeny, want: "deny"},
		{name: "deny_and_stop", resp: sdktools.ConfirmDenyAndStop, want: "deny_and_stop"},
		{name: "out of range", resp: sdktools.ConfirmationResponse(99), want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := confirmDecisionString(tt.resp); got != tt.want {
				t.Errorf("confirmDecisionString(%v) = %q, want %q", tt.resp, got, tt.want)
			}
		})
	}
}

// --- parseAskUserAnswers (direct unit tests) ---

func TestParseAskUserAnswers_NoField(t *testing.T) {
	resp := parseAskUserAnswers(map[string]any{})
	if len(resp.Answers) != 0 {
		t.Errorf("got %d answers, want 0", len(resp.Answers))
	}
}

func TestParseAskUserAnswers_AnswersWrongType(t *testing.T) {
	resp := parseAskUserAnswers(map[string]any{"answers": "not a slice"})
	if len(resp.Answers) != 0 {
		t.Errorf("got %d answers, want 0", len(resp.Answers))
	}
}
