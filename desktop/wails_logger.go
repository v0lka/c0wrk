package desktop

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	wailsLogger "github.com/wailsapp/wails/v2/pkg/logger"
)

// wailsLogAdapter bridges Wails' logger.Logger interface to an slog.Logger
// backed by a persistent wails.log file so that Wails-internal fatal errors
// (e.g. RPC serialization failures) are captured on disk before the process
// exits. A nil delegate (before session logger init) is tolerated — messages
// are written to the fallback file only.
type wailsLogAdapter struct {
	logger    *slog.Logger
	file      *os.File
	delegate  *slog.Logger
}

// NewWailsLogger creates a Wails Logger that writes to <logDir>/wails.log.
// An optional delegate is used for duplicate delivery once a session logger
// is available (messages go to both the persistent file and the delegate).
func NewWailsLogger(logDir string) (*wailsLogAdapter, error) {
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating wails log directory: %w", err)
	}
	logPath := filepath.Join(logDir, "wails.log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("opening wails log file: %w", err)
	}
	handler := slog.NewJSONHandler(file, &slog.HandlerOptions{Level: slog.LevelDebug})
	slogLogger := slog.New(handler)
	return &wailsLogAdapter{logger: slogLogger, file: file}, nil
}

// SetDelegate sets an optional slog.Logger that also receives messages.
// When set, Wails internal log messages (including fatal errors) are
// duplicated to both the persistent wails.log file and the delegate.
func (w *wailsLogAdapter) SetDelegate(l *slog.Logger) {
	w.delegate = l
}

func (w *wailsLogAdapter) write(level slog.Level, msg string) {
	w.logger.Log(nil, level, msg) //nolint:staticcheck // nil ctx acceptable for this adapter
	if w.delegate != nil {
		w.delegate.Log(nil, level, msg) //nolint:staticcheck // nil ctx acceptable for this adapter
	}
}

func (w *wailsLogAdapter) Print(message string)      { w.write(slog.LevelInfo, strings.TrimRight(message, "\n")) }
func (w *wailsLogAdapter) Trace(message string)      { w.write(slog.LevelDebug-4, strings.TrimRight(message, "\n")) } //nolint:mnd // trace level 4 below debug
func (w *wailsLogAdapter) Debug(message string)      { w.write(slog.LevelDebug, strings.TrimRight(message, "\n")) }
func (w *wailsLogAdapter) Info(message string)       { w.write(slog.LevelInfo, strings.TrimRight(message, "\n")) }
func (w *wailsLogAdapter) Warning(message string)    { w.write(slog.LevelWarn, strings.TrimRight(message, "\n")) }
func (w *wailsLogAdapter) Error(message string)      { w.write(slog.LevelError, strings.TrimRight(message, "\n")) }
func (w *wailsLogAdapter) Fatal(message string)      { w.write(slog.LevelError, strings.TrimRight(message, "\n")) }

func (w *wailsLogAdapter) Close() error {
	if w.file != nil {
		f := w.file
		w.file = nil
		return f.Close()
	}
	return nil
}

var _ wailsLogger.Logger = (*wailsLogAdapter)(nil)
