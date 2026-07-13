package desktop

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/session"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
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

// --- handleToolConfirmResponse ---

func TestHandleToolConfirmResponse_HappyPath(t *testing.T) {
	a := &App{}
	ch := make(chan sdktools.ConfirmationResponse, 1)
	a.pendingConfirmations.Store("rid-1", &pendingConfirmData{ch: ch, toolName: "bash_exec"})

	a.handleToolConfirmResponse(map[string]any{
		"confirm_id": "rid-1",
		"decision":   "allow_once",
	}, silentLogger())

	select {
	case got := <-ch:
		if got != sdktools.ConfirmAllowOnce {
			t.Errorf("got %v, want ConfirmAllowOnce", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no value sent on channel")
	}

	if _, ok := a.pendingConfirmations.Load("rid-1"); ok {
		t.Error("pending entry should be deleted after dispatch")
	}
}

func TestHandleToolConfirmResponse_MissingConfirmID(t *testing.T) {
	a := &App{}
	log, buf := captureLogger()
	a.handleToolConfirmResponse(map[string]any{"decision": "allow_once"}, log)
	if !strings.Contains(buf.String(), "missing confirm_id") {
		t.Errorf("expected warning about missing confirm_id, got %q", buf.String())
	}
}

func TestHandleToolConfirmResponse_NoPendingEntry(t *testing.T) {
	a := &App{}
	log, buf := captureLogger()
	a.handleToolConfirmResponse(map[string]any{
		"confirm_id": "rid-missing",
		"decision":   "deny",
	}, log)
	if !strings.Contains(buf.String(), "no pending confirmation") {
		t.Errorf("expected 'no pending confirmation' warning, got %q", buf.String())
	}
}

func TestHandleToolConfirmResponse_WrongChannelType(t *testing.T) {
	a := &App{}
	log, buf := captureLogger()
	// Store something that's NOT *pendingConfirmData
	a.pendingConfirmations.Store("rid-x", "garbage")

	a.handleToolConfirmResponse(map[string]any{
		"confirm_id": "rid-x",
		"decision":   "deny",
	}, log)

	if !strings.Contains(buf.String(), "wrong type") {
		t.Errorf("expected 'wrong type' warning, got %q", buf.String())
	}
	// Wrong-type entry must be cleaned up to avoid leaking
	if _, ok := a.pendingConfirmations.Load("rid-x"); ok {
		t.Error("wrong-type entry should be deleted")
	}
}

func TestHandleToolConfirmResponse_ChannelFull(t *testing.T) {
	a := &App{}
	// Unbuffered channel with no receiver simulates "channel full"
	ch := make(chan sdktools.ConfirmationResponse)
	a.pendingConfirmations.Store("rid-full", &pendingConfirmData{ch: ch, toolName: "edit_file"})

	log, buf := captureLogger()
	a.handleToolConfirmResponse(map[string]any{
		"confirm_id": "rid-full",
		"decision":   "deny",
	}, log)

	if !strings.Contains(buf.String(), "channel full") {
		t.Errorf("expected 'channel full' warning, got %q", buf.String())
	}
	if _, ok := a.pendingConfirmations.Load("rid-full"); ok {
		t.Error("pending entry should still be deleted on drop")
	}
}

// --- handleAskUserResponse ---

func TestHandleAskUserResponse_HappyPath(t *testing.T) {
	a := &App{}
	ch := make(chan coretools.AskUserResponse, 1)
	a.pendingAskUser.Store("au-1", &pendingAskUserEntry{ch: ch, sessionID: "s1"})

	a.handleAskUserResponse(map[string]any{
		"request_id": "au-1",
		"answers": []any{
			map[string]any{
				"id":          "q1",
				"selected":    []any{"opt-a", "opt-b"},
				"custom_text": "extra",
			},
		},
	}, silentLogger())

	select {
	case got := <-ch:
		if len(got.Answers) != 1 {
			t.Fatalf("got %d answers, want 1", len(got.Answers))
		}
		a := got.Answers[0]
		if a.ID != "q1" || len(a.Selected) != 2 || a.Selected[0] != "opt-a" || a.CustomText != "extra" {
			t.Errorf("answer mismatch: %+v", a)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no value sent on channel")
	}
}

func TestHandleAskUserResponse_NoAnswersField(t *testing.T) {
	a := &App{}
	ch := make(chan coretools.AskUserResponse, 1)
	a.pendingAskUser.Store("au-2", &pendingAskUserEntry{ch: ch, sessionID: "s2"})

	a.handleAskUserResponse(map[string]any{"request_id": "au-2"}, silentLogger())

	select {
	case got := <-ch:
		if len(got.Answers) != 0 {
			t.Errorf("expected empty answers, got %d", len(got.Answers))
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no value sent on channel")
	}
}

func TestHandleAskUserResponse_MissingRequestID(t *testing.T) {
	a := &App{}
	log, buf := captureLogger()
	a.handleAskUserResponse(map[string]any{"answers": []any{}}, log)
	if !strings.Contains(buf.String(), "missing request_id") {
		t.Errorf("expected 'missing request_id' warning, got %q", buf.String())
	}
}

func TestHandleAskUserResponse_MalformedAnswers(t *testing.T) {
	a := &App{}
	ch := make(chan coretools.AskUserResponse, 1)
	a.pendingAskUser.Store("au-3", &pendingAskUserEntry{ch: ch, sessionID: "s3"})

	// Each branch malformed in a different way; all should be silently skipped.
	a.handleAskUserResponse(map[string]any{
		"request_id": "au-3",
		"answers": []any{
			"not a map",
			map[string]any{"id": 42},                // wrong type for id
			map[string]any{"selected": "not slice"}, // wrong type for selected
			map[string]any{"id": "ok", "selected": []any{"keep", 99}, "custom_text": 7},
		},
	}, silentLogger())

	select {
	case got := <-ch:
		// "not a map" is silently skipped (continue). The remaining three
		// map entries each produce a partial answer.
		if len(got.Answers) != 3 {
			t.Fatalf("got %d answers, want 3 (1 non-map skipped, 3 maps accepted with defaults)", len(got.Answers))
		}
		// Last answer should have id=ok and selected containing only the string entry
		last := got.Answers[2]
		if last.ID != "ok" || len(last.Selected) != 1 || last.Selected[0] != "keep" || last.CustomText != "" {
			t.Errorf("last answer mismatch: %+v", last)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no value sent on channel")
	}
}

// --- handleStepLimitResponse ---

func TestHandleStepLimitResponse_HappyPath(t *testing.T) {
	// The handler casts the decision string to agent.StepLimitResponse without
	// rewriting it. These cases pin that contract: every valid decision value
	// passes through unchanged.
	tests := []struct {
		name     string
		response string
	}{
		{name: "allow once", response: "allow_once"},
		{name: "allow more", response: "allow_more"},
		{name: "allow always", response: "allow_always"},
		{name: "deny", response: "deny"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &App{}
			ch := make(chan agent.StepLimitResponse, 1)
			a.pendingStepLimit.Store("sl-1", &pendingStepLimitEntry{ch: ch, sessionID: "s1"})

			a.handleStepLimitResponse(map[string]any{
				"request_id": "sl-1",
				"response":   tt.response,
			}, silentLogger())

			select {
			case got := <-ch:
				if got != agent.StepLimitResponse(tt.response) {
					t.Errorf("got %v, want %v", got, tt.response)
				}
			case <-time.After(100 * time.Millisecond):
				t.Fatal("no value sent on channel")
			}
		})
	}
}

func TestHandleStepLimitResponse_MissingResponseField(t *testing.T) {
	a := &App{}
	log, buf := captureLogger()
	a.handleStepLimitResponse(map[string]any{"request_id": "sl-x"}, log)
	if !strings.Contains(buf.String(), "missing response field") {
		t.Errorf("expected 'missing response field' warning, got %q", buf.String())
	}
}

func TestHandleStepLimitResponse_WrongResponseType(t *testing.T) {
	a := &App{}
	log, buf := captureLogger()
	a.handleStepLimitResponse(map[string]any{
		"request_id": "sl-x",
		"response":   42,
	}, log)
	if !strings.Contains(buf.String(), "unsupported type") {
		t.Errorf("expected 'unsupported type' warning, got %q", buf.String())
	}
}

func TestHandleStepLimitResponse_NoAppContext(t *testing.T) {
	// Verify handler does not panic when a.app is nil (zero-value *App{}).
	// This covers the edge case where a step_limit response arrives before
	// the application is fully initialized.
	a := &App{} // a.app == nil
	ch := make(chan agent.StepLimitResponse, 1)
	a.pendingStepLimit.Store("sl-noapp", &pendingStepLimitEntry{ch: ch, sessionID: "s-na"})

	a.handleStepLimitResponse(map[string]any{
		"request_id": "sl-noapp",
		"response":   "deny",
	}, silentLogger())

	select {
	case got := <-ch:
		if got != agent.StepLimitResponse("deny") {
			t.Errorf("got %q, want deny", got)
		}
	default:
		t.Fatal("expected response on channel")
	}
}

// --- handleToolJudgeRequest (without app) ---

func TestHandleToolJudgeRequest_NoPendingConfirmation(t *testing.T) {
	a := &App{}
	log, buf := captureLogger()
	uiCalls := 0
	uiEmit := func(session.Event) { uiCalls++ }

	a.handleToolJudgeRequest(map[string]any{"confirm_id": "absent"}, uiEmit, log)

	if !strings.Contains(buf.String(), "no pending confirmation for judge request") {
		t.Errorf("expected warning, got %q", buf.String())
	}
	if uiCalls != 0 {
		t.Errorf("uiEmit should not be called when no pending confirmation, got %d", uiCalls)
	}
}

func TestHandleToolJudgeRequest_NoApplication(t *testing.T) {
	// runJudgeEvaluation guards against a.app == nil and emits an error.
	a := &App{}
	a.pendingConfirmations.Store("cid-1", &pendingConfirmData{
		ch:        make(chan sdktools.ConfirmationResponse, 1),
		toolName:  "bash_exec",
		input:     json.RawMessage(`{"command":"ls"}`),
		sessionID: "sess-1",
	})

	var mu sync.Mutex
	var emitted []session.Event
	uiEmit := func(e session.Event) {
		mu.Lock()
		emitted = append(emitted, e)
		mu.Unlock()
	}

	a.handleToolJudgeRequest(map[string]any{"confirm_id": "cid-1"}, uiEmit, silentLogger())

	// The handler spawns a goroutine; wait briefly for it to finish.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(emitted)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(emitted) != 1 {
		t.Fatalf("expected 1 emitted event (error response), got %d", len(emitted))
	}
	got := emitted[0]
	if got.Type != "tool_judge_response" {
		t.Errorf("event type = %q, want tool_judge_response", got.Type)
	}
	payload, ok := got.Data.(session.JudgeResponsePayload)
	if !ok {
		t.Fatalf("payload type = %T, want JudgeResponsePayload", got.Data)
	}
	if payload.Error == "" {
		t.Error("expected non-empty error in payload when app is nil")
	}

	// W4: Verify that the pending confirmation entry is cleaned up when a.app is nil.
	if _, ok := a.pendingConfirmations.Load("cid-1"); ok {
		t.Error("pending confirmation should be deleted when app is nil")
	}
}

func TestHandleToolJudgeRequest_MissingConfirmID(t *testing.T) {
	a := &App{}
	log, buf := captureLogger()
	uiEmit := func(session.Event) {}

	a.handleToolJudgeRequest(map[string]any{}, uiEmit, log)

	if !strings.Contains(buf.String(), "missing confirm_id") {
		t.Errorf("expected 'missing confirm_id' warning, got %q", buf.String())
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
