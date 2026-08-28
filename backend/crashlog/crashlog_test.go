package crashlog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// deadPID spawns a sacrificial child process and waits for it to exit,
// returning a pid that is guaranteed not to be running (bar theoretical OS
// pid reuse) so liveness-dependent tests never flake on a busy machine.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "true")
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(t.Context(), "cmd", "/c", "exit 0")
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn sacrificial process: %v", err)
	}
	return cmd.Process.Pid
}

// restoreThreshold keeps the rotation-size override local to the rotation
// test so a failing assert cannot leak a tiny threshold into other tests.
func rotateTest(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	old := maxStderrLogBytes
	maxStderrLogBytes = 16
	t.Cleanup(func() { maxStderrLogBytes = old })
	return dir
}

func TestRotateOversizedLog_RenamesOversizedFile(t *testing.T) {
	dir := rotateTest(t)
	if err := os.WriteFile(filepath.Join(dir, stderrLogName), []byte(strings.Repeat("x", 1024)), 0o640); err != nil {
		t.Fatalf("write stderr.log: %v", err)
	}

	if err := rotateOversizedLog(dir); err != nil {
		t.Fatalf("rotateOversizedLog: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, stderrLogName)); !os.IsNotExist(err) {
		t.Errorf("oversized stderr.log still present after rotation")
	}
	if _, err := os.Stat(filepath.Join(dir, rotatedStderrLogName)); err != nil {
		t.Errorf("rotated stderr.old.log missing: %v", err)
	}
}

func TestRotateOversizedLog_KeepsSmallFile(t *testing.T) {
	dir := rotateTest(t)
	small := []byte("tiny")
	if err := os.WriteFile(filepath.Join(dir, stderrLogName), small, 0o640); err != nil {
		t.Fatalf("write stderr.log: %v", err)
	}

	if err := rotateOversizedLog(dir); err != nil {
		t.Fatalf("rotateOversizedLog: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, stderrLogName))
	if err != nil {
		t.Fatalf("stderr.log should not have been rotated: %v", err)
	}
	if !bytes.Equal(got, small) {
		t.Errorf("content changed: got %q, want %q", got, small)
	}
}

func TestInstallWritesBannerAndMarker(t *testing.T) {
	dir := t.TempDir()

	c, err := install(dir, false)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	t.Cleanup(func() { _ = c.file.Close() })

	stderr, err := os.ReadFile(filepath.Join(dir, stderrLogName))
	if err != nil {
		t.Fatalf("read stderr.log: %v", err)
	}
	banner := string(stderr)
	if !strings.Contains(banner, "pid=") || !strings.Contains(banner, "start=") {
		t.Errorf("banner missing pid/start markers: %q", banner)
	}

	data, err := os.ReadFile(filepath.Join(dir, markerName))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	var rec runRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("unmarshal marker: %v", err)
	}
	if rec.PID != os.Getpid() {
		t.Errorf("marker pid = %d, want %d", rec.PID, os.Getpid())
	}
	if rec.StderrLog != stderrLogName {
		t.Errorf("marker stderr_log = %q, want %q", rec.StderrLog, stderrLogName)
	}
}

func TestInstallStashesPreviousMarker(t *testing.T) {
	dir := t.TempDir()
	stale := []byte(`{"pid":4242,"version":"v0.4.2","commit":"deadbeef","started_at":"2026-08-28T10:00:00+03:00","stderr_log":"stderr.log"}`)
	if err := os.WriteFile(filepath.Join(dir, markerName), stale, 0o640); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}

	c, err := install(dir, false)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	t.Cleanup(func() { _ = c.file.Close() })

	prev, err := os.ReadFile(filepath.Join(dir, prevMarkerName))
	if err != nil {
		t.Fatalf("previous marker not stashed: %v", err)
	}
	if !bytes.Equal(prev, stale) {
		t.Errorf("stashed marker = %q, want %q", prev, stale)
	}
	if rec := readMarkerPID(t, filepath.Join(dir, markerName)); rec != os.Getpid() {
		t.Errorf("fresh marker pid = %d, want %d", rec, os.Getpid())
	}
}

func TestRemoveMarkerDeletesAndToleratesNil(t *testing.T) {
	dir := t.TempDir()
	c, err := install(dir, false)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	t.Cleanup(func() { _ = c.file.Close() })

	c.RemoveMarker()
	if _, err := os.Stat(filepath.Join(dir, markerName)); !os.IsNotExist(err) {
		t.Errorf("marker still present after RemoveMarker")
	}
	c.RemoveMarker() // idempotent
	var nilCapture *Capture
	nilCapture.RemoveMarker() // nil-tolerant, must not panic
}

func TestRemoveMarkerKeepsForeignMarker(t *testing.T) {
	dir := t.TempDir()
	c, err := install(dir, false)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	t.Cleanup(func() { _ = c.file.Close() })
	marker := filepath.Join(dir, markerName)

	// A concurrent instance replaced the marker with its own pid: a clean
	// exit of THIS instance must not erase that instance's evidence.
	foreign := fmt.Sprintf(`{"pid":%d,"version":"v0.4.2","commit":"deadbeef","started_at":"2026-08-28T22:23:16+03:00","stderr_log":"stderr.log"}`, os.Getpid()+1)
	if err := os.WriteFile(marker, []byte(foreign), 0o640); err != nil {
		t.Fatalf("write foreign marker: %v", err)
	}
	c.RemoveMarker()
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Errorf("marker owned by another instance was deleted by this instance's clean exit")
	}

	// Own marker is removed as before.
	own := fmt.Sprintf(`{"pid":%d,"version":"v0.4.2","commit":"deadbeef","started_at":"2026-08-28T22:23:16+03:00","stderr_log":"stderr.log"}`, os.Getpid())
	if err := os.WriteFile(marker, []byte(own), 0o640); err != nil {
		t.Fatalf("write own marker: %v", err)
	}
	c.RemoveMarker()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("own marker still present after RemoveMarker")
	}

	// An unparsable marker cannot be attributed to anyone; it is removed on
	// clean exit (pre-existing fail-open behavior).
	if err := os.WriteFile(marker, []byte("not json"), 0o640); err != nil {
		t.Fatalf("write garbage marker: %v", err)
	}
	c.RemoveMarker()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("unparsable marker still present after RemoveMarker")
	}
}

func TestLogExitWritesExitCode(t *testing.T) {
	dir := t.TempDir()
	c, err := install(dir, false)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	t.Cleanup(func() { _ = c.file.Close() })

	c.LogExit(3)

	stderr, err := os.ReadFile(filepath.Join(dir, stderrLogName))
	if err != nil {
		t.Fatalf("read stderr.log: %v", err)
	}
	if !strings.Contains(string(stderr), "exit code=3") {
		t.Errorf("exit banner missing exit code: %q", stderr)
	}
}

func TestReportUncleanShutdown_WarnsOnStashedMarker(t *testing.T) {
	dir := t.TempDir()
	pid := deadPID(t)
	stale := fmt.Sprintf(`{"pid":%d,"version":"v0.4.2","commit":"deadbeef","started_at":"2026-08-28T22:23:16+03:00","stderr_log":"stderr.log"}`, pid)
	if err := os.WriteFile(filepath.Join(dir, prevMarkerName), []byte(stale), 0o640); err != nil {
		t.Fatalf("write prev marker: %v", err)
	}

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	ReportUncleanShutdown(log, dir)

	out := buf.String()
	for _, want := range []string{
		"did not shut down cleanly",
		strconv.Itoa(pid),
		"v0.4.2",
		"stderr.old.log", // hint must point at the rotated copy too
	} {
		if !strings.Contains(out, want) {
			t.Errorf("warning output missing %q: %s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, prevMarkerName)); !os.IsNotExist(err) {
		t.Errorf("prev marker not consumed after reporting")
	}
}

func TestReportUncleanShutdown_SoftensWhenPIDAlive(t *testing.T) {
	dir := t.TempDir()
	// The test process itself is alive: a stashed marker referencing it must
	// not be reported as an unclean shutdown (overlapping-instance case).
	stale := fmt.Sprintf(`{"pid":%d,"version":"v0.4.2","commit":"deadbeef","started_at":"2026-08-28T22:23:16+03:00","stderr_log":"stderr.log"}`, os.Getpid())
	if err := os.WriteFile(filepath.Join(dir, prevMarkerName), []byte(stale), 0o640); err != nil {
		t.Fatalf("write prev marker: %v", err)
	}

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	ReportUncleanShutdown(log, dir)

	out := buf.String()
	if !strings.Contains(out, "live process") {
		t.Errorf("overlap note missing for live pid: %s", out)
	}
	if strings.Contains(out, "did not shut down cleanly") {
		t.Errorf("false unclean-shutdown warning for a live pid: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, prevMarkerName)); !os.IsNotExist(err) {
		t.Errorf("prev marker not consumed after reporting")
	}
}

func TestReportUncleanShutdown_SilentWhenClean(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	ReportUncleanShutdown(log, dir)

	if buf.Len() != 0 {
		t.Errorf("unexpected warning for clean previous shutdown: %s", buf.String())
	}
}

func readMarkerPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read marker %s: %v", path, err)
	}
	var rec runRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("unmarshal marker: %v", err)
	}
	return rec.PID
}
