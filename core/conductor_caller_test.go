package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
)

// captureLogger builds a slog.Logger that writes DEBUG-and-above records to a
// buffer, returning the logger and using the buffer for assertions. DEBUG level
// is required because loggingCaller records request details ("llm: request") at
// DEBUG.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// loggingCallerTypeName is the concrete type produced by agent.NewLoggingLLMCaller
// (*agent.loggingCaller). It is unexported in the agent package, so tests detect
// the wrapper via reflection on the fully-qualified type name.
const loggingCallerTypeName = "*agent.loggingCaller"

// TestCallerForStep_LoggingIndependentOfStepDump is the regression test for the
// bug where callerForStep gated the LoggingLLMCaller on stepDumpTracker != nil.
// With step dumps disabled (stepDumpTracker == nil) but a provider name and
// logger configured, step/subagent LLM calls were silently unlogged. Logging
// must now be independent of step dumps (mirroring callerForConductor), with
// dumps still gated on stepDumpTracker.
//
// Acceptance criterion 1: when stepDumpTracker is nil but providerName/logger
// are set, a Call through the step caller still emits log records.
func TestCallerForStep_LoggingIndependentOfStepDump(t *testing.T) {
	var buf bytes.Buffer

	l := &conductorLauncher{deps: conductorDeps{
		trackingCaller: llm.NewTrackingCaller(&mockLLMCaller{}, llm.NewUsageTracker()),
		providerName:   "openai",
		logger:         captureLogger(&buf),
		// stepDumpTracker intentionally nil — the regression scenario.
	}}

	caller := l.callerForStep(&mockContextManager{}, "step_1")

	// The caller must be wrapped in loggingCaller even though stepDumpTracker
	// is nil — this is the core of the regression.
	if got := reflect.TypeOf(caller).String(); got != loggingCallerTypeName {
		t.Fatalf("expected %s wrapper, got %s", loggingCallerTypeName, got)
	}

	// Drive an actual LLM call through the produced caller. If loggingCaller
	// wraps it, a "llm: request" DEBUG record is emitted before delegation.
	if _, err := caller.Call(context.Background(), llm.ChatRequest{Model: "gpt-4o"}); err != nil {
		t.Fatalf("Call through step caller: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "llm: request") {
		t.Errorf("expected step LLM call to be logged even with nil stepDumpTracker; log output was:\n%s", out)
	}
	if !strings.Contains(out, "openai") {
		t.Errorf("expected provider name %q in log output; got:\n%s", "openai", out)
	}
}

// TestCallerForStep_LoggingGatedOnLogger documents the gating condition: the
// logging wrapper is added only when BOTH a provider name and a logger are
// present (matching callerForConductor). With no logger, the returned caller is
// the bare trackingCaller (never a loggingCaller), regardless of stepDumpTracker.
func TestCallerForStep_LoggingGatedOnLogger(t *testing.T) {
	t.Run("no logger means no logging wrapper", func(t *testing.T) {
		l := &conductorLauncher{deps: conductorDeps{
			trackingCaller: llm.NewTrackingCaller(&mockLLMCaller{}, llm.NewUsageTracker()),
			providerName:   "openai",
			logger:         nil, // no logger → no logging wrapper
		}}

		caller := l.callerForStep(&mockContextManager{}, "step_1")

		if got := reflect.TypeOf(caller).String(); got == loggingCallerTypeName {
			t.Errorf("must not add loggingCaller when logger is nil; got %s", got)
		}
	})

	t.Run("empty provider name means no logging wrapper", func(t *testing.T) {
		var buf bytes.Buffer
		l := &conductorLauncher{deps: conductorDeps{
			trackingCaller: llm.NewTrackingCaller(&mockLLMCaller{}, llm.NewUsageTracker()),
			providerName:   "", // empty → no logging wrapper
			logger:         captureLogger(&buf),
		}}

		caller := l.callerForStep(&mockContextManager{}, "step_1")

		if got := reflect.TypeOf(caller).String(); got == loggingCallerTypeName {
			t.Errorf("must not add loggingCaller when providerName is empty; got %s", got)
		}
	})
}

// dumpRecord mirrors agent.dumpEntry (unexported in the agent package) so tests
// can decode the JSONL records produced by agent.NewDumpCaller.
type dumpRecord struct {
	Direction string `json:"direction"`
	Error     string `json:"error,omitempty"`
}

// TestCallerForConductor_WritesSessionDumpOnError is the regression test for the
// bug where callerForConductor rebuilt the caller from trackingCaller +
// loggingCaller but DROPPED the session dump wrapper (NewDumpCaller). The main
// Conductor ReAct loop — the loop that carries the task message and any image /
// content blocks — uses this caller, so a failed LLM call in the main loop was
// never recorded in the session dump.
//
// callerForConductor must return deps.llm, which the orchestrator builder
// assembles as NewDumpCaller(NewLoggingLLMCaller(trackingCaller, ...), dumpWriter, ...).
//
// Acceptance: when the underlying LLM call fails, the session dump contains a
// "request" record AND a "response" record carrying the error — proving the
// dump wrapper is present on the main-loop caller.
func TestCallerForConductor_WritesSessionDumpOnError(t *testing.T) {
	// Replicate the caller stack assembled in builder.go (loggedLLM):
	// dump(logging(tracking(inner))).
	wantErr := errors.New("upstream llm failure")
	inner := &mockLLMCaller{err: wantErr}
	trackingCaller := llm.NewTrackingCaller(inner, llm.NewUsageTracker())

	var logBuf bytes.Buffer
	logger := captureLogger(&logBuf)

	var sessionDump bytes.Buffer
	loggedLLM := agent.NewLoggingLLMCaller(trackingCaller, "openai", logger)
	loggedLLM = agent.NewDumpCaller(loggedLLM, &sessionDump, logger)

	deps := conductorDeps{
		llm:            loggedLLM,
		trackingCaller: trackingCaller,
		providerName:   "openai",
		logger:         logger,
	}

	caller := callerForConductor(deps)

	// The returned caller must be the dump-wrapped caller (deps.llm), not a
	// rebuild that omits the dump layer.
	if got := reflect.TypeOf(caller).String(); got != "*agent.dumpCaller" {
		t.Fatalf("expected callerForConductor to return the dump-wrapped caller (*agent.dumpCaller); got %s", got)
	}

	// Drive a failing LLM call through the main-loop caller.
	_, err := caller.Call(context.Background(), llm.ChatRequest{Model: "gpt-4o"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected call to surface %v, got %v", wantErr, err)
	}

	// Decode the JSONL dump and assert a request record plus an error-bearing
	// response record were written.
	var records []dumpRecord
	dec := json.NewDecoder(&sessionDump)
	for {
		var rec dumpRecord
		if derr := dec.Decode(&rec); derr != nil {
			break
		}
		records = append(records, rec)
	}

	var hasRequest, hasErrResponse bool
	for _, rec := range records {
		switch rec.Direction {
		case "request":
			hasRequest = true
		case "response":
			if rec.Error == wantErr.Error() {
				hasErrResponse = true
			}
		}
	}
	if !hasRequest {
		t.Errorf("expected a request dump record for the main-loop LLM call; dump was:\n%s", sessionDump.String())
	}
	if !hasErrResponse {
		t.Errorf("expected a response dump record carrying the error %q; dump was:\n%s", wantErr.Error(), sessionDump.String())
	}
}

// TestCallerForConductor_PassesThroughDepsLLM asserts the minimal-invasive
// contract: callerForConductor returns deps.llm verbatim (the caller already
// carrying logging + the session dump writer), rather than reconstructing a
// logging-only chain that would drop the dump. This is referential identity,
// independent of how the caller stack is assembled.
func TestCallerForConductor_PassesThroughDepsLLM(t *testing.T) {
	inner := &mockLLMCaller{}
	loggedLLM := agent.NewLoggingLLMCaller(inner, "openai", slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	deps := conductorDeps{
		llm:            loggedLLM,
		trackingCaller: llm.NewTrackingCaller(inner, llm.NewUsageTracker()),
		providerName:   "openai",
		logger:         slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}

	if got := callerForConductor(deps); got != loggedLLM {
		t.Errorf("callerForConductor must return deps.llm verbatim; got a different caller %T", got)
	}
}
