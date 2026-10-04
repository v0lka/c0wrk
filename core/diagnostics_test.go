package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type expectedCoreDiagnostic struct {
	level   string
	message string
	fields  map[string]string
}

type coreDiagnosticBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *coreDiagnosticBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

// expectedCoreLogger captures only this test's injected logger. Cleanup checks
// every warning/error, including unexpected diagnostics, without global state.
func expectedCoreLogger(t *testing.T, want ...expectedCoreDiagnostic) *slog.Logger {
	t.Helper()
	buf := &coreDiagnosticBuffer{}
	t.Cleanup(func() {
		buf.mu.Lock()
		defer buf.mu.Unlock()
		var got []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Errorf("expectedCoreLogger(%s): decode = %v, want valid record", t.Name(), err)
				continue
			}
			if record["level"] == "WARN" || record["level"] == "ERROR" {
				got = append(got, record)
			}
		}
		if len(got) != len(want) {
			t.Errorf("diagnostics(%s) = %v (%d records), want %v (%d records)", t.Name(), got, len(got), want, len(want))
		}
		for i := 0; i < len(got) && i < len(want); i++ {
			if got[i]["level"] != want[i].level || got[i]["msg"] != want[i].message {
				t.Errorf("diagnostics(%s)[%d] = %v, want severity %s message %q", t.Name(), i, got[i], want[i].level, want[i].message)
			}
			for key, value := range want[i].fields {
				if actual := fmt.Sprint(got[i][key]); actual != value {
					t.Errorf("diagnostics(%s)[%d].%s = %q, want %q", t.Name(), i, key, actual, value)
				}
			}
		}
	})
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func expectedServiceDiagnostic(kind, model, outcome string, attempts int) expectedCoreDiagnostic {
	return expectedCoreDiagnostic{"WARN", "service call", map[string]string{
		"service_kind": kind, "model": model, "outcome": outcome, "attempts": strconv.Itoa(attempts),
	}}
}

func expectedRewriteDiagnostic() expectedCoreDiagnostic {
	return expectedCoreDiagnostic{"WARN", "optimize prompt: rewrite produced no usable output", map[string]string{"has_reasoning": "false", "content_len": "0"}}
}

func expectedStartupDiagnostic() expectedCoreDiagnostic {
	return expectedCoreDiagnostic{"WARN", "failed to initialize LLM router at startup", map[string]string{"error": "no providers configured"}}
}
