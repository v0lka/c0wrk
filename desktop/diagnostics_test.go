package desktop

import (
	"context"
	"log/slog"
	"reflect"
	"sync"
	"testing"
)

// diagnosticExpectation describes one deliberate failure-path receipt.
type diagnosticExpectation struct {
	level   slog.Level
	message string
	attrs   map[string]string
}

type diagnosticCapture struct {
	slog.Handler
	mu      sync.Mutex
	records []diagnosticExpectation
}

func (h *diagnosticCapture) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn || h.Handler.Enabled(ctx, level)
}

func (h *diagnosticCapture) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < slog.LevelWarn {
		return h.Handler.Handle(ctx, r)
	}
	attrs := make(map[string]string)
	r.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.Resolve().String(); return true })
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, diagnosticExpectation{r.Level, r.Message, attrs})
	return nil
}

// expectedDiagnostics is injected only into the test exercising the receipt.
// Cleanup rejects additional or missing diagnostics rather than hiding them.
func expectedDiagnostics(t *testing.T, want ...diagnosticExpectation) *slog.Logger {
	t.Helper()
	h := &diagnosticCapture{Handler: slog.Default().Handler()}
	t.Cleanup(func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.records) != len(want) {
			t.Errorf("diagnostics(%s) count = %d, want %d; got %+v", t.Name(), len(h.records), len(want), h.records)
		}
		for i := 0; i < len(h.records) && i < len(want); i++ {
			if !reflect.DeepEqual(h.records[i], want[i]) {
				t.Errorf("diagnostics(%s)[%d] = %+v, want %+v", t.Name(), i, h.records[i], want[i])
			}
		}
	})
	return slog.New(h)
}

func injectExpectedDiagnostics(t *testing.T, a *App, want ...diagnosticExpectation) {
	t.Helper()
	old := a.logger
	a.logger = expectedDiagnostics(t, want...)
	t.Cleanup(func() { a.logger = old })
}
