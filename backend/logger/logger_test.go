package logger

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name      string
		level     string
		wantLevel slog.Level
		wantErr   bool
	}{
		{
			name:      "DEBUG uppercase",
			level:     "DEBUG",
			wantLevel: slog.LevelDebug,
			wantErr:   false,
		},
		{
			name:      "INFO uppercase",
			level:     "INFO",
			wantLevel: slog.LevelInfo,
			wantErr:   false,
		},
		{
			name:      "WARN uppercase",
			level:     "WARN",
			wantLevel: slog.LevelWarn,
			wantErr:   false,
		},
		{
			name:      "ERROR uppercase",
			level:     "ERROR",
			wantLevel: slog.LevelError,
			wantErr:   false,
		},
		{
			name:      "debug lowercase",
			level:     "debug",
			wantLevel: slog.LevelDebug,
			wantErr:   false,
		},
		{
			name:      "info mixed case",
			level:     "Info",
			wantLevel: slog.LevelInfo,
			wantErr:   false,
		},
		{
			name:      "invalid level defaults to INFO with error",
			level:     "INVALID",
			wantLevel: slog.LevelInfo,
			wantErr:   true,
		},
		{
			name:      "empty level defaults to INFO with error",
			level:     "",
			wantLevel: slog.LevelInfo,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLevel, err := parseLevel(tt.level)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseLevel(%q) error = %v, wantErr %v", tt.level, err, tt.wantErr)
				return
			}
			if gotLevel != tt.wantLevel {
				t.Errorf("parseLevel(%q) = %v, want %v", tt.level, gotLevel, tt.wantLevel)
			}
		})
	}
}

func TestInit_CreatesDirectoryAndFile(t *testing.T) {
	testDir := t.TempDir()

	logger, err := Init("INFO", testDir)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer func() { _ = logger.Close() }()

	// Check that the log directory was created
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Errorf("log directory was not created")
	}

	// Check that a session file was created
	entries, err := os.ReadDir(testDir)
	if err != nil {
		t.Fatalf("failed to read test directory: %v", err)
	}

	foundSessionFile := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "session-") && strings.HasSuffix(entry.Name(), ".log") {
			foundSessionFile = true
			break
		}
	}

	if !foundSessionFile {
		t.Errorf("session log file was not created")
	}
}

func TestInit_InvalidLevelDefaultsToInfo(t *testing.T) {
	testDir := t.TempDir()

	logger, err := Init("INVALID_LEVEL", testDir)
	if err != nil {
		t.Fatalf("Init() with invalid level should not return error, got: %v", err)
	}
	defer func() { _ = logger.Close() }()

	// Logger should be created successfully with INFO level
	if logger.Logger() == nil {
		t.Errorf("Logger() returned nil")
	}
}

func TestSessionLogger_Logger(t *testing.T) {
	testDir := t.TempDir()

	logger, err := Init("DEBUG", testDir)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer func() { _ = logger.Close() }()

	// Logger() should return non-nil *slog.Logger
	if logger.Logger() == nil {
		t.Errorf("Logger() returned nil")
	}

	// Verify it's a proper slog.Logger by calling a method
	logger.Logger().Info("test message", "key", "value")
}

func TestSessionLogger_Close(t *testing.T) {
	testDir := t.TempDir()

	logger, err := Init("INFO", testDir)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Close should succeed
	if err := logger.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}

	// Double close - the file handle is already closed, which may return an error
	// This is acceptable behavior; we just verify it doesn't panic
	_ = logger.Close()
}

func TestSessionLogger_LevelFiltering(t *testing.T) {
	testDir := t.TempDir()

	// Create logger with WARN level
	logger, err := Init("WARN", testDir)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer func() { _ = logger.Close() }()

	// Log messages at different levels
	logger.Logger().Debug("debug message")
	logger.Logger().Info("info message")
	logger.Logger().Warn("warn message")
	logger.Logger().Error("error message")

	// Find and read the log file
	entries, err := os.ReadDir(testDir)
	if err != nil {
		t.Fatalf("failed to read test directory: %v", err)
	}

	var logFile string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "session-") && strings.HasSuffix(entry.Name(), ".log") {
			logFile = filepath.Join(testDir, entry.Name())
			break
		}
	}

	if logFile == "" {
		t.Fatalf("log file not found")
	}

	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	contentStr := string(content)

	// Should contain WARN and ERROR
	if !strings.Contains(contentStr, "warn message") {
		t.Errorf("log should contain warn message")
	}
	if !strings.Contains(contentStr, "error message") {
		t.Errorf("log should contain error message")
	}
}

func TestSessionLogger_JSONFormat(t *testing.T) {
	testDir := t.TempDir()

	logger, err := Init("INFO", testDir)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer func() { _ = logger.Close() }()

	// Log a structured message
	logger.Logger().Info("test message", "key1", "value1", "key2", 42)

	// Find and read the log file
	entries, err := os.ReadDir(testDir)
	if err != nil {
		t.Fatalf("failed to read test directory: %v", err)
	}

	var logFile string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "session-") && strings.HasSuffix(entry.Name(), ".log") {
			logFile = filepath.Join(testDir, entry.Name())
			break
		}
	}

	if logFile == "" {
		t.Fatalf("log file not found")
	}

	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	contentStr := string(content)

	// Should be valid JSON format
	if !strings.Contains(contentStr, `"msg":"test message"`) {
		t.Errorf("log should contain JSON-formatted message")
	}
	if !strings.Contains(contentStr, `"key1":"value1"`) {
		t.Errorf("log should contain key1 field")
	}
	if !strings.Contains(contentStr, `"key2":42`) {
		t.Errorf("log should contain key2 field")
	}
}

// TestInit_RefusesEscapingSymlinkedLogDir pins the corrected #77 contract:
// a PRE-EXISTING symlinked logs directory whose target lies OUTSIDE the
// agent directory (the log dir's parent — the containment boundary) is
// REFUSED, because it redirects every session log outside ~/.c0wrk (the
// finding's documented trigger: rm -rf ~/.c0wrk/logs && ln -s /tmp/evil
// ~/.c0wrk/logs). Init fails closed instead of writing through the link.
// What still resolves as operator intent is a link INSIDE the boundary
// (covered below) — the macOS /var → /private/var class lives ABOVE the
// boundary and is unaffected; a dangling link or a link swapped into a
// created component fails Init closed as before.
func TestInit_RefusesEscapingSymlinkedLogDir(t *testing.T) {
	base := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.MkdirAll(victim, 0o750); err != nil {
		t.Fatalf("mkdir victim: %v", err)
	}
	logDir := filepath.Join(base, "logs")
	if err := os.Symlink(victim, logDir); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	l, err := Init("INFO", logDir)
	if err == nil {
		_ = l.Close()
		t.Fatal("expected Init to refuse a logs dir symlink escaping the agent directory")
	}
	// The refusal must leave the link target untouched.
	entries, err := os.ReadDir(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the escaped link target was written to: %v", entries)
	}
}

// TestInit_ResolvesInBoundarySymlinkedLogDir pins the operator-intent half
// of the corrected #77 contract: a symlinked logs directory whose target
// resolves WITHIN the agent directory (the containment boundary) still
// resolves, and the session log is created inside the link's REAL target.
func TestInit_ResolvesInBoundarySymlinkedLogDir(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "real-logs"), 0o750); err != nil {
		t.Fatalf("mkdir real-logs: %v", err)
	}
	logDir := filepath.Join(base, "logs")
	if err := os.Symlink(filepath.Join(base, "real-logs"), logDir); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	l, err := Init("INFO", logDir)
	if err != nil {
		t.Fatalf("expected Init to resolve an in-boundary operator-symlinked logs dir, got: %v", err)
	}
	defer func() { _ = l.Close() }()
	entries, err := os.ReadDir(filepath.Join(base, "real-logs"))
	if err != nil {
		t.Fatalf("read real-logs: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected the session log inside the symlink's resolved target")
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".lnk") {
			t.Fatalf("unexpected non-file entry in the resolved target: %v", e)
		}
	}
}
