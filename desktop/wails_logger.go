package desktop

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/v0lka/sp4rk/safeio"
	wailsLogger "github.com/wailsapp/wails/v2/pkg/logger"
)

// wailsLogAdapter bridges Wails' logger.Logger to a persistent wails.log,
// capturing Wails-internal fatal errors (specs/domains/crash-logging.md).
type wailsLogAdapter struct {
	logger *slog.Logger
	file   *os.File
	// delegate is written by SetDelegate during Startup and read by write
	// from Wails logger callbacks on any goroutine — hence atomic.
	delegate atomic.Pointer[slog.Logger]
}

// NewWailsLogger creates a Wails Logger writing to <logDir>/wails.log; the
// delegate arrives later via SetDelegate.
func NewWailsLogger(logDir string) (*wailsLogAdapter, error) {
	// MkdirAllReal + OpenFileNoFollow, not the symlink-following defaults: a
	// planted symlink at the wails.log file must not redirect the app's log
	// stream outside ~/.c0wrk (MkdirAllReal still treats an operator-symlinked
	// logs dir as intent). Failure fails closed to "no Wails log file".
	if err := safeio.MkdirAllReal(logDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating wails log directory: %w", err)
	}
	logPath := filepath.Join(logDir, "wails.log")
	file, err := safeio.OpenFileNoFollow(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("opening wails log file: %w", err)
	}
	handler := slog.NewJSONHandler(file, &slog.HandlerOptions{Level: slog.LevelDebug})
	slogLogger := slog.New(handler)
	return &wailsLogAdapter{logger: slogLogger, file: file}, nil
}

// SetDelegate arms duplicate delivery to the session logger.
func (w *wailsLogAdapter) SetDelegate(l *slog.Logger) {
	w.delegate.Store(l)
}

func (w *wailsLogAdapter) write(level slog.Level, msg string) {
	ctx := context.Background()
	w.logger.Log(ctx, level, msg)
	if d := w.delegate.Load(); d != nil {
		d.Log(ctx, level, msg)
	}
}

func (w *wailsLogAdapter) Print(message string) {
	w.write(slog.LevelInfo, strings.TrimRight(message, "\n"))
}
func (w *wailsLogAdapter) Trace(message string) {
	w.write(slog.LevelDebug-4, strings.TrimRight(message, "\n"))
} //nolint:mnd // trace level 4 below debug
func (w *wailsLogAdapter) Debug(message string) {
	w.write(slog.LevelDebug, strings.TrimRight(message, "\n"))
}
func (w *wailsLogAdapter) Info(message string) {
	w.write(slog.LevelInfo, strings.TrimRight(message, "\n"))
}
func (w *wailsLogAdapter) Warning(message string) {
	w.write(slog.LevelWarn, strings.TrimRight(message, "\n"))
}
func (w *wailsLogAdapter) Error(message string) {
	w.write(slog.LevelError, strings.TrimRight(message, "\n"))
}

// Fatal logs at Error but does NOT os.Exit (diverging from Wails' contract):
// a GUI app is better left alive; main.go's recover stays the last resort.
func (w *wailsLogAdapter) Fatal(message string) {
	w.write(slog.LevelError, strings.TrimRight(message, "\n"))
}

func (w *wailsLogAdapter) Close() error {
	if w.file != nil {
		f := w.file
		w.file = nil
		return f.Close()
	}
	return nil
}

var _ wailsLogger.Logger = (*wailsLogAdapter)(nil)
