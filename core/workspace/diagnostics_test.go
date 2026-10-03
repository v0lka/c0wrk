package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type diagnosticExpectation struct {
	level   slog.Level
	message string
	attrs   map[string]string
}
type diagnosticRecords struct {
	mu   sync.Mutex
	want []diagnosticExpectation
	got  []diagnosticExpectation
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
	d := diagnosticExpectation{r.Level, r.Message, map[string]string{}}
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
func expectedDiagnostics(t *testing.T, want ...diagnosticExpectation) *slog.Logger {
	t.Helper()
	records := &diagnosticRecords{want: want}
	t.Cleanup(func() {
		records.mu.Lock()
		defer records.mu.Unlock()
		if !reflect.DeepEqual(records.got, records.want) {
			t.Errorf("diagnostics(%s) = %#v, want %#v (severity, message, attrs and count)", t.Name(), records.got, records.want)
		}
	})
	return slog.New(&diagnosticHandler{fallback: slog.Default().Handler(), records: records})
}

// plantedIncludeDiagnostic derives the exact line from fixture bytes, not the
// production parser or git's platform-dependent initial config layout.
func plantedIncludeDiagnostic(t *testing.T, root, path string) diagnosticExpectation {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	line, matches := 0, 0
	for i, text := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(text) == "path = "+path {
			line = i + 1
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("config include %q has %d literal matches, want 1", path, matches)
	}
	return ignoredIncludeDiagnostic(line, path)
}

func ignoredIncludeDiagnostic(line int, path string) diagnosticExpectation {
	return diagnosticExpectation{slog.LevelWarn, "git config include directive ignored (not followed); config is an incomplete view", map[string]string{"line": strconv.Itoa(line), "conditional": "false", "condition": "", "path": path}}
}
func unreadableIncludeDiagnostic(path string) diagnosticExpectation {
	return diagnosticExpectation{slog.LevelWarn, "git config include target could not be fingerprinted", map[string]string{"path": path, "error": path + " is not a regular file"}}
}

func gitDiagnosticContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, gitScanLoggerKey{}, logger)
}
