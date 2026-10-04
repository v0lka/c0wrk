package backend

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// apiDiagnosticBuffer retains all levels; assertions reject additional diagnostics.
type apiDiagnosticBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *apiDiagnosticBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func expectAPIDiagnostic(t *testing.T, message, key, value string) *slog.Logger {
	t.Helper()
	b := &apiDiagnosticBuffer{}
	logger := slog.New(slog.NewJSONHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		decoder := json.NewDecoder(bytes.NewReader(b.Bytes()))
		count := 0
		for decoder.More() {
			var record map[string]any
			if err := decoder.Decode(&record); err != nil {
				t.Errorf("diagnostics(%q) decode error = %v, want nil", message, err)
				return
			}
			level, _ := record["level"].(string)
			if level != "WARN" && level != "ERROR" {
				continue
			}
			count++
			if level != "WARN" || record["msg"] != message {
				t.Errorf("diagnostics(%q) = %v, want one WARN with that message", message, record)
			}
			got, ok := record[key].(string)
			if !ok || !strings.Contains(got, value) {
				t.Errorf("diagnostics(%q)[%q] = %v, want string containing %q", message, key, record[key], value)
			}
		}
		if count != 1 {
			t.Errorf("diagnostics(%q) count = %d, want 1", message, count)
		}
	})
	return logger
}

func captureAPIDiagnostic(t *testing.T, f *FrontendAPI, message, key, value string) {
	t.Helper()
	old := f.logger
	f.logger = expectAPIDiagnostic(t, message, key, value)
	t.Cleanup(func() { f.logger = old })
}
