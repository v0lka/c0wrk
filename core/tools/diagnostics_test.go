package tools

import (
	"context"
	"log/slog"
	"reflect"
	"sync"
	"testing"
)

type expectedToolDiagnostic struct {
	message string
	attrs   map[string]string
}

type toolDiagnosticCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *toolDiagnosticCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *toolDiagnosticCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *toolDiagnosticCapture) WithAttrs([]slog.Attr) slog.Handler {
	panic("unexpected WithAttrs in tool diagnostic test")
}
func (h *toolDiagnosticCapture) WithGroup(string) slog.Handler {
	panic("unexpected WithGroup in tool diagnostic test")
}

// captureToolDiagnostics is installed only by tests deliberately exercising
// security diagnostics. Every warning/error must match the explicit contract.
func captureToolDiagnostics(t *testing.T, registry *ToolRegistry, want ...expectedToolDiagnostic) {
	t.Helper()
	registry.SetLogger(newToolDiagnosticLogger(t, want...))
}

func newToolDiagnosticLogger(t *testing.T, want ...expectedToolDiagnostic) *slog.Logger {
	t.Helper()
	return newToolDiagnosticLoggerFor(t, t.Name(), want...)
}

func newToolDiagnosticLoggerFor(t *testing.T, label string, want ...expectedToolDiagnostic) *slog.Logger {
	t.Helper()
	h := &toolDiagnosticCapture{}
	logger := slog.New(h)
	t.Cleanup(func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		var got []slog.Record
		for _, r := range h.records {
			if r.Level >= slog.LevelWarn {
				got = append(got, r)
			}
		}
		if len(got) != len(want) {
			t.Errorf("ToolRegistry diagnostics(%s) count = %d, want %d", label, len(got), len(want))
		}
		for i, r := range got {
			attrs := map[string]string{}
			r.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.String(); return true })
			if i >= len(want) {
				t.Errorf("ToolRegistry diagnostics(%s) unexpected = %v %q %v, want none", label, r.Level, r.Message, attrs)
				continue
			}
			if r.Level != slog.LevelWarn || r.Message != want[i].message || !reflect.DeepEqual(attrs, want[i].attrs) {
				t.Errorf("ToolRegistry diagnostics(%s)[%d] = %v %q %v, want WARN %q %v", label, i, r.Level, r.Message, attrs, want[i].message, want[i].attrs)
			}
		}
	})
	return logger
}
