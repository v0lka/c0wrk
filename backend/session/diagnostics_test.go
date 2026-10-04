package session

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"testing"
)

// emptySessionStore supplies an empty restoration view to in-memory tests.
// Persistence and missing-dependency tests install their own store explicitly.
type emptySessionStore struct{ *mockSessionStoreForRestore }

func (*emptySessionStore) LoadSession(context.Context, string) (*SessionInfo, error) { return nil, nil }

type expectedDiagnostic struct {
	level   slog.Level
	message string
	attrs   map[string]string
}
type diagnosticRecords struct {
	mu   sync.Mutex
	want []expectedDiagnostic
	got  []expectedDiagnostic
}
type diagnosticHandler struct {
	fallback slog.Handler
	records  *diagnosticRecords
	attrs    map[string]string
	group    string
}

func (h *diagnosticHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn || h.fallback.Enabled(ctx, level)
}
func diagnosticAttrs(dst map[string]string, prefix string, attrs ...slog.Attr) {
	for _, a := range attrs {
		a.Value = a.Value.Resolve()
		if a.Value.Kind() == slog.KindGroup {
			next := prefix
			if a.Key != "" {
				next += a.Key + "."
			}
			diagnosticAttrs(dst, next, a.Value.Group()...)
		} else if a.Key != "" {
			dst[prefix+a.Key] = fmt.Sprint(a.Value.Any())
		}
	}
}
func (h *diagnosticHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < slog.LevelWarn {
		return h.fallback.Handle(ctx, r)
	}
	d := expectedDiagnostic{level: r.Level, message: r.Message, attrs: map[string]string{}}
	for k, v := range h.attrs {
		d.attrs[k] = v
	}
	r.Attrs(func(a slog.Attr) bool { diagnosticAttrs(d.attrs, h.group, a); return true })
	h.records.mu.Lock()
	h.records.got = append(h.records.got, d)
	matched := false
	for _, w := range h.records.want {
		if reflect.DeepEqual(d, w) {
			matched = true
			break
		}
	}
	h.records.mu.Unlock()
	if !matched {
		return h.fallback.Handle(ctx, r)
	}
	return nil
}
func (h *diagnosticHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cp := *h
	cp.fallback = h.fallback.WithAttrs(attrs)
	cp.attrs = map[string]string{}
	for k, v := range h.attrs {
		cp.attrs[k] = v
	}
	diagnosticAttrs(cp.attrs, h.group, attrs...)
	return &cp
}
func (h *diagnosticHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	cp := *h
	cp.fallback = h.fallback.WithGroup(name)
	cp.group += name + "."
	return &cp
}
func captureManagerDiagnostics(t *testing.T, m *Manager, want ...expectedDiagnostic) {
	t.Helper()
	old := m.log()
	records := &diagnosticRecords{want: want}
	m.SetLogger(slog.New(&diagnosticHandler{fallback: old.Handler(), records: records}))
	t.Cleanup(func() {
		m.Shutdown()
		m.SetLogger(old)
		records.mu.Lock()
		defer records.mu.Unlock()
		if !reflect.DeepEqual(records.got, records.want) {
			t.Errorf("Manager diagnostics(%s) = %#v, want %#v (severity, message, attrs and count)", t.Name(), records.got, records.want)
		}
	})
}

func warningDiagnostic(message string, attrs map[string]string) expectedDiagnostic {
	return expectedDiagnostic{slog.LevelWarn, message, attrs}
}
func errorDiagnostic(message string, attrs map[string]string) expectedDiagnostic {
	return expectedDiagnostic{slog.LevelError, message, attrs}
}
