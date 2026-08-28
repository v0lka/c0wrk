// Package crashlog guarantees that the fact and the cause of ANY application
// termination end up on disk.
//
// Coverage matrix (what is captured where):
//
//	Cause                                   | Evidence written
//	----------------------------------------+-------------------------------------------
//	Panic in any goroutine                  | Go runtime dump -> stderr fd -> stderr.log
//	Runtime fatal error (OOM, deadlock)     | Go runtime dump -> stderr fd -> stderr.log
//	SIGSEGV/SIGQUIT/SIGABRT (Go code)       | runtime dump + OS crash report (.ips)
//	Native crash (WebKit/ONNX, cgo)         | OS crash report (.ips) only; next-start
//	                                        | marker warning points at it
//	SIGTERM/SIGINT/SIGHUP                   | signal line in stderr.log before re-raise
//	SIGKILL / OS OOM-kill (uncatchable)     | marker survives -> warning at next start
//	Clean quit (window close, updater quit) | Shutdown INFO logs + exit-code banner line
//
// The mechanism is deliberately low-tech: a single append-only stderr.log in
// the app logs directory receives fd 1 and fd 2 (dup2 on Unix), so everything
// the Go runtime and native libraries write to stderr — including panic
// dumps that bypass os.Stderr — is persisted even for Finder-launched GUI
// processes whose stderr would otherwise go to /dev/null.
//
// A liveness marker (app.running.json) is written at startup and removed only
// on a clean exit. Its survival proves the previous instance terminated
// abnormally; Startup reports that fact via ReportUncleanShutdown.
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
	"syscall"
	"time"

	"github.com/v0lka/c0wrk/core/version"
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
	// ReportUncleanShutdown can consume it later (Startup logs the warning
	// once the session logger exists).
	prevMarkerName = "app.running.prev.json"
)

// maxStderrLogBytes is the rotation threshold for stderr.log. A var (not a
// const) so tests can shrink it; panic dumps are a few hundred KiB at most,
// 4 MiB keeps several of them plus chatty native-library output.
var maxStderrLogBytes int64 = 4 << 20

// runRecord is the JSON payload of the liveness marker.
type runRecord struct {
	PID       int    `json:"pid"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	StartedAt string `json:"started_at"`
	StderrLog string `json:"stderr_log"`
}

// Capture owns the process-wide crash-capture state: the redirected stdio
// file and the liveness marker. Install creates it; all methods are
// nil-tolerant so callers can invoke them unconditionally when Install was
// skipped (e.g. C0WRK_DISABLE_CRASH_CAPTURE set, or Install failed).
type Capture struct {
	file       *os.File
	logDir     string
	markerPath string
	startedAt  time.Time
}

// Install arms process-wide crash capture in logDir:
//
//  1. stash a leftover liveness marker from a previous abnormal exit,
//  2. rotate an oversized stderr.log,
//  3. open stderr.log, redirect fd 1/2 into it and route the default
//     log/slog output there as well,
//  4. write a startup banner and a fresh liveness marker,
//  5. log SIGTERM/SIGINT/SIGHUP before dying by their default disposition.
//
// Install must be called exactly once, as early as possible in main (before
// Wails and any goroutine that might panic). The returned error means
// capture is disabled for this run — the app must continue without it.
func Install(logDir string) (*Capture, error) {
	return install(logDir, true)
}

// install is the testable core of Install. redirect=false skips the global
// side effects (fd redirection, default logger rewiring, signal handling) so
// tests can exercise banner/marker/rotation logic inside the test process.
func install(logDir string, redirect bool) (*Capture, error) {
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		return nil, fmt.Errorf("crashlog: creating log directory: %w", err)
	}

	// Preserve the previous run's marker before overwriting it: if the last
	// instance died abnormally its marker is still here, and that fact is
	// exactly what ReportUncleanShutdown needs to report at Startup.
	if err := stashPreviousMarker(logDir); err != nil {
		// Non-fatal: losing the stash only silences the next-start warning.
		slog.Warn("crashlog: failed to stash previous liveness marker", "error", err)
	}

	if err := rotateOversizedLog(logDir); err != nil {
		// Non-fatal: an oversized file keeps growing until the next start.
		slog.Warn("crashlog: failed to rotate oversized stderr log", "error", err)
	}

	file, err := os.OpenFile(filepath.Join(logDir, stderrLogName),
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
		// dup2 the real descriptors: Go runtime panic/fatal dumps and native
		// library output go straight to fd 2, bypassing the os.Stderr value.
		if err := redirectStdio(file); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("crashlog: redirecting stdio: %w", err)
		}
		// The default log.Logger (and thus slog.Default()) caches its writer
		// at init; rewire it so pre-Startup slog output is persisted too.
		log.SetOutput(file)
		watchTerminationSignals(c)
	}

	c.writeBanner()
	if err := c.writeMarker(); err != nil {
		slog.Warn("crashlog: failed to write liveness marker", "error", err)
	}
	return c, nil
}

// RemoveMarker deletes the liveness marker. It must be called ONLY on clean
// exit paths (after wails.Run returns); a surviving marker is what marks the
// previous run as abnormally terminated. A marker whose pid belongs to
// another instance (a concurrent start overwrote ours) is left in place so
// that instance keeps its own liveness evidence. Nil-tolerant.
func (c *Capture) RemoveMarker() {
	if c == nil {
		return
	}
	if data, err := os.ReadFile(c.markerPath); err == nil {
		var rec runRecord
		if uerr := json.Unmarshal(data, &rec); uerr == nil && rec.PID != 0 && rec.PID != os.Getpid() {
			return
		}
	}
	_ = os.Remove(c.markerPath)
}

// LogExit writes the final exit banner with the process exit code and
// uptime, giving every run a closing bracket that pairs with the startup
// banner. Call it in main right before os.Exit. Nil-tolerant.
func (c *Capture) LogExit(code int) {
	if c == nil {
		return
	}
	c.writeLine("=== c0wrk-desktop exit code=" + strconv.Itoa(code) +
		" uptime=" + time.Since(c.startedAt).Round(time.Second).String() +
		" at=" + time.Now().Format(time.RFC3339) + " ===")
	_ = c.file.Sync()
}

// writeBanner records the run header: pid and build identify the process in
// OS crash reports and let multiple runs be told apart in the append-only
// file.
func (c *Capture) writeBanner() {
	c.writeLine("=== c0wrk-desktop pid=" + strconv.Itoa(os.Getpid()) +
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
	if werr := os.WriteFile(c.markerPath, data, 0o640); werr != nil {
		return fmt.Errorf("crashlog: writing marker: %w", werr)
	}
	return nil
}

// writeLine appends one line with a trailing newline in a single Write call
// (O_APPEND makes concurrent single writes atomic, so the signal handler can
// race other writers without interleaving bytes).
func (c *Capture) writeLine(line string) {
	_, _ = c.file.WriteString(line + "\n")
}

// watchTerminationSignals logs catchable termination signals before dying by
// their default disposition. SIGQUIT is intentionally NOT intercepted: the
// Go runtime's default SIGQUIT handler dumps every goroutine's stack to
// stderr (captured via the fd redirect) — that dump IS the crash evidence.
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

// rotateOversizedLog renames stderr.log to stderr.old.log (replacing any
// previous rotation) once it exceeds maxStderrLogBytes, so the file stays
// bounded across runs while its content is only ever moved, never dropped.
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

// ReportUncleanShutdown consumes the stashed marker of a previous run. If it
// exists, the previous instance never reached a clean exit (crash, kill,
// power loss) and a warning with its pid, version and start time is logged,
// pointing at stderr.log (panic dumps) and the OS crash-report directory.
// A stashed marker whose pid is still alive means this start overlapped a
// live instance (or the OS recycled the pid): an overlap note is logged
// instead of a false unclean-shutdown warning. Call it from Startup after
// the session logger exists (and after any logger reinit) so the report
// lands in the log the user actually reads. Idempotent: the marker is
// removed after reporting.
func ReportUncleanShutdown(logger *slog.Logger, logDir string) {
	prev := filepath.Join(logDir, prevMarkerName)
	data, err := os.ReadFile(prev)
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
		// The stashed pid still exists: two instances overlapped (or the pid
		// was recycled by the OS). A crash cannot be concluded, so report the
		// overlap instead of a false unclean shutdown.
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
// given logs directory. Useful for diagnostics surfaces that surface the
// file location to the user.
func StderrLogPath(logDir string) string {
	return filepath.Join(logDir, stderrLogName)
}
