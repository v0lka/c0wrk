// Package crashlog guarantees that the fact and the cause of ANY application
// termination end up on disk as evidence: an append-only stderr.log receives
// fd 1/2, and a liveness marker removed only on clean exit proves abnormal
// termination at the next start. Coverage matrix, mechanism and invariants:
// specs/domains/crash-logging.md.
package crashlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/v0lka/c0wrk/core/version"

	"github.com/v0lka/sp4rk/safeio"
)

const (
	// stderrLogName is the append-only capture file for fd 1/2 output.
	stderrLogName = "stderr.log"
	// rotatedStderrLogName receives stderr.log when it exceeds
	// maxStderrLogBytes at startup (previous evidence is never truncated).
	rotatedStderrLogName = "stderr.old.log"
	// markerName is the liveness marker: present while the app runs, removed
	// only on a clean exit.
	markerName = "app.running.json"
	// prevMarkerName stashes the previous run's marker at Install time so
	// ReportUncleanShutdown can consume it later.
	prevMarkerName = "app.running.prev.json"
)

// maxStderrLogBytes is the rotation threshold for stderr.log; a var so tests
// can shrink it. 4 MiB keeps several panic dumps plus chatty native output.
var maxStderrLogBytes int64 = 4 << 20

// currentCapture is the process-wide capture installed by install(); the
// desktop shutdown watchdog's ForceExit runs the clean-exit hooks on it.
// Nil until install() succeeds (or capture is disabled).
var currentCapture atomic.Pointer[Capture]

// runRecord is the JSON payload of the liveness marker.
type runRecord struct {
	PID       int    `json:"pid"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	StartedAt string `json:"started_at"`
	StderrLog string `json:"stderr_log"`
}

// Capture owns the redirected stdio file and the liveness marker. Install
// creates it; all methods are nil-tolerant so callers can invoke them
// unconditionally when Install was skipped or failed.
type Capture struct {
	file       *os.File
	logDir     string
	markerPath string
	startedAt  time.Time
}

// Install arms process-wide crash capture in logDir: stash any leftover
// marker, rotate an oversized stderr.log, redirect fd 1/2 and the default
// log output into it, write banner + liveness marker, log catchable
// termination signals. Call exactly once, as early as possible in main; a
// returned error means capture is disabled for this run — continue without
// it. Mechanism and coverage: specs/domains/crash-logging.md.
func Install(logDir string) (*Capture, error) {
	return install(logDir, true)
}

// install is the testable core of Install. redirect=false skips the global
// side effects (fd redirect, logger rewiring, signal handling) in tests.
func install(logDir string, redirect bool) (*Capture, error) {
	// MkdirAllReal: only REAL directories are created — a planted or
	// swapped-in link fails capture closed for the run instead of pointing
	// fd 1/2 and the marker outside the operator's tree.
	// See specs/architecture/security-model.md (Fixed-Path ~/.c0wrk State
	// Hardening).
	if err := safeio.MkdirAllReal(logDir, 0o750); err != nil {
		return nil, fmt.Errorf("crashlog: creating log directory: %w", err)
	}

	// Stash (not overwrite) the previous run's marker: its survival is
	// exactly what ReportUncleanShutdown reports at Startup.
	if err := stashPreviousMarker(logDir); err != nil {
		// Non-fatal: losing the stash only silences the next-start warning.
		slog.Warn("crashlog: failed to stash previous liveness marker", "error", err)
	}

	if err := rotateOversizedLog(logDir); err != nil {
		// Non-fatal: an oversized file keeps growing until the next start.
		slog.Warn("crashlog: failed to rotate oversized stderr log", "error", err)
	}

	// OpenFileNoFollow: a planted symlink at the fixed stderr.log path must
	// not receive the fd 1/2 capture (the post-open fstat of plain
	// safeio.OpenFile would pass); ELOOP fails capture closed for the run.
	// On Windows a final symlink is still resolved (safeio parity note),
	// tempered by Windows requiring elevated/dev-mode rights to create
	// symlinks.
	// See specs/architecture/security-model.md (Fixed-Path ~/.c0wrk State
	// Hardening).
	file, err := safeio.OpenFileNoFollow(filepath.Join(logDir, stderrLogName),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("crashlog: opening stderr log: %w", err)
	}

	c := &Capture{
		file:       file,
		logDir:     logDir,
		markerPath: filepath.Join(logDir, markerName),
		startedAt:  time.Now(),
	}

	if redirect {
		// dup2: runtime panic dumps and native output bypass os.Stderr.
		if err := redirectStdio(file); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("crashlog: redirecting stdio: %w", err)
		}
		// The default log.Logger caches its writer at init; rewire it so
		// pre-Startup slog output is persisted too.
		log.SetOutput(file)
		watchTerminationSignals(c)
	}

	c.writeBanner()
	if err := c.writeMarker(); err != nil {
		slog.Warn("crashlog: failed to write liveness marker", "error", err)
	}
	currentCapture.Store(c)
	return c, nil
}

// RemoveMarker deletes the liveness marker. Only clean exit paths may call
// it — a surviving marker is what marks the previous run as abnormal. A
// marker overwritten by a concurrent instance (foreign pid) is left in
// place.
func (c *Capture) RemoveMarker() {
	if c == nil {
		return
	}
	if data, err := safeio.ReadFile(c.markerPath); err == nil {
		var rec runRecord
		if uerr := json.Unmarshal(data, &rec); uerr == nil && rec.PID != 0 && rec.PID != os.Getpid() {
			return
		}
	}
	_ = os.Remove(c.markerPath)
}

// Banner prefixes bracketing every run in stderr.log; exported so readers
// (the webview GPU post-mortem) can split the append-only file into runs.
const (
	// RunBannerPrefix opens a run's segment (written by Install).
	RunBannerPrefix = "=== c0wrk-desktop pid="
	// ExitBannerPrefix closes a run that reached a clean exit (LogExit).
	ExitBannerPrefix = "=== c0wrk-desktop exit code="
)

// LogExit writes the closing exit banner (exit code, uptime), pairing with
// the startup banner. Call it in main right before os.Exit. Nil-tolerant.
func (c *Capture) LogExit(code int) {
	if c == nil {
		return
	}
	c.writeLine(ExitBannerPrefix + strconv.Itoa(code) +
		" uptime=" + time.Since(c.startedAt).Round(time.Second).String() +
		" at=" + time.Now().Format(time.RFC3339) + " ===")
	_ = c.file.Sync()
}

// ForceExit runs the clean-exit hooks (RemoveMarker + LogExit) on the
// process-wide capture and then os.Exit(code) — the forced-exit path for
// callers that cannot wait for main's normal return (the desktop shutdown
// watchdog); without it a legitimate forced quit is indistinguishable from
// a crash at the next launch. Nil-tolerant: with no capture it just exits.
func ForceExit(code int) {
	if c := currentCapture.Load(); c != nil {
		c.RemoveMarker()
		c.LogExit(code)
	}
	os.Exit(code)
}

// writeBanner records the run header (pid, build) in the append-only file.
func (c *Capture) writeBanner() {
	c.writeLine(RunBannerPrefix + strconv.Itoa(os.Getpid()) +
		" version=" + version.Version + " commit=" + version.GitCommit +
		" start=" + c.startedAt.Format(time.RFC3339) + " ===")
}

// writeMarker persists the run record consumed by ReportUncleanShutdown.
func (c *Capture) writeMarker() error {
	rec := runRecord{
		PID:       os.Getpid(),
		Version:   version.Version,
		Commit:    version.GitCommit,
		StartedAt: c.startedAt.Format(time.RFC3339),
		StderrLog: stderrLogName,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("crashlog: encoding marker: %w", err)
	}
	// WriteFileAtomic: the rename REPLACES a planted symlink at the fixed
	// marker path (including a dangling one, which stashPreviousMarker does
	// not stash) instead of writing through it; the random-suffix staging
	// file leaves no plantable fixed temp path.
	// See specs/architecture/security-model.md (Fixed-Path ~/.c0wrk State
	// Hardening).
	if werr := safeio.WriteFileAtomic(c.markerPath, data, 0o640); werr != nil {
		return fmt.Errorf("crashlog: writing marker: %w", werr)
	}
	return nil
}

// writeLine appends one line in a single O_APPEND write, so the signal
// handler races other writers without interleaving bytes.
func (c *Capture) writeLine(line string) {
	_, _ = c.file.WriteString(line + "\n")
}

// watchTerminationSignals logs SIGTERM/SIGINT/SIGHUP before their default
// disposition. SIGQUIT is deliberately not intercepted: the runtime's
// default handler dumps every goroutine stack to stderr — the evidence.
func watchTerminationSignals(c *Capture) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		sig := <-ch
		c.writeLine("=== c0wrk-desktop received signal " + sig.String() +
			" at " + time.Now().Format(time.RFC3339) + "; terminating ===")
		_ = c.file.Sync()
		reraise(sig)
	}()
}

// stashPreviousMarker moves an existing liveness marker aside so Install can
// write a fresh one without destroying the evidence of an abnormal exit.
func stashPreviousMarker(logDir string) error {
	marker := filepath.Join(logDir, markerName)
	if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("crashlog: stat marker: %w", err)
	}
	if err := os.Rename(marker, filepath.Join(logDir, prevMarkerName)); err != nil {
		return fmt.Errorf("crashlog: renaming marker: %w", err)
	}
	return nil
}

// rotateOversizedLog renames an oversized stderr.log to stderr.old.log
// (replacing any previous rotation); content is only moved, never dropped.
func rotateOversizedLog(logDir string) error {
	path := filepath.Join(logDir, stderrLogName)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("crashlog: stat stderr log: %w", err)
	}
	if info.Size() <= maxStderrLogBytes {
		return nil
	}
	old := filepath.Join(logDir, rotatedStderrLogName)
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("crashlog: removing stale rotation: %w", err)
	}
	if err := os.Rename(path, old); err != nil {
		return fmt.Errorf("crashlog: rotating stderr log: %w", err)
	}
	return nil
}

// ReportUncleanShutdown consumes the stashed marker of a previous run: a
// dead pid warns unclean shutdown (pointing at stderr.log and the OS crash
// reports), a live pid warns a suspected concurrent instance instead. Call
// from Startup after the session logger exists so the report lands in the
// log the user reads; the marker is removed after reporting. Details:
// specs/domains/crash-logging.md.
func ReportUncleanShutdown(logger *slog.Logger, logDir string) {
	prev := filepath.Join(logDir, prevMarkerName)
	data, err := safeio.ReadFile(prev)
	if errors.Is(err, os.ErrNotExist) {
		return // previous run exited cleanly
	}
	if err != nil {
		logger.Warn("previous shutdown state could not be read", "path", prev, "error", err)
		return
	}
	var rec runRecord
	if uerr := json.Unmarshal(data, &rec); uerr != nil {
		logger.Warn("previous app instance did not shut down cleanly (unparsable marker)",
			"marker_path", prev, "error", uerr)
	} else if rec.PID != 0 && processAlive(rec.PID) {
		logger.Warn("stashed liveness marker references a live process; concurrent app instance suspected",
			"prev_pid", rec.PID,
			"prev_version", rec.Version,
			"prev_started_at", rec.StartedAt,
			"stderr_log", filepath.Join(logDir, stderrLogName))
	} else {
		logger.Warn("previous app instance did not shut down cleanly",
			"prev_pid", rec.PID,
			"prev_version", rec.Version,
			"prev_started_at", rec.StartedAt,
			"stderr_log", filepath.Join(logDir, stderrLogName),
			"hint", "check stderr.log (rotated evidence: stderr.old.log) for a panic dump and the OS crash-report directory (macOS: ~/Library/Logs/DiagnosticReports)")
	}
	_ = os.Remove(prev)
}

// StderrLogPath returns the absolute path of the crash-capture file for the
// given logs directory.
func StderrLogPath(logDir string) string {
	return filepath.Join(logDir, stderrLogName)
}
