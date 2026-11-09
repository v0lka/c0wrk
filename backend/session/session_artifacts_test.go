package session

import (
	"compress/gzip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/sp4rk/appendcodec"
)

// zstdCLIAvailable reports whether the system zstd CLI is installed. The CLI
// decode test names its OS exception: Windows CI legs ship no zstd binary, so
// the integration test skips there — codec correctness is still covered on
// every leg by the stdlib klauspost decoder in the sp4rk appendcodec tests.
func zstdCLIAvailable() bool {
	_, err := exec.LookPath("zstd")
	return err == nil
}

// zstdCLIDecode decodes path with the SYSTEM zstd CLI and returns its output:
// the acceptance test proving new artifacts are externally decodable, not
// merely self-consistent with this build's encoder.
func zstdCLIDecode(t *testing.T, path string) string {
	t.Helper()
	if !zstdCLIAvailable() {
		t.Skip("system zstd CLI not installed (Windows CI leg); stdlib decoder coverage lives in the sp4rk appendcodec tests")
	}
	cmd := exec.CommandContext(context.Background(), "zstd", "-dc", path)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("zstd -dc %s failed: %v (stderr: %s)", path, err, stderr.String())
	}
	return string(out)
}

// gzipDecode reads a (possibly multi-member) gzip file with the stdlib
// reader — a clean ReadAll to EOF proves Close wrote the member trailers.
func gzipDecode(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	r, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader for %s: %v", path, err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("gzip decode %s: %v", path, err)
	}
	return string(data)
}

// TestSessionArtifacts_NewFilesUseZstdAndDecodeWithSystemCLI verifies the
// core AC: with the default (zstd) compression, NEW session artifacts carry
// the .zst extension, the plain names are not created, and the files decode
// with the system zstd CLI.
func TestSessionArtifacts_NewFilesUseZstdAndDecodeWithSystemCLI(t *testing.T) {
	manager, _, agentDir := testManager(t)
	sid := "artifacts-zstd-1"

	logger, logWC, err := manager.createSessionLogger(testProjectID, sid)
	if err != nil {
		t.Fatalf("createSessionLogger: %v", err)
	}
	logger.Info("session log record")
	if cerr := logWC.Close(); cerr != nil {
		t.Fatalf("close log wrapper: %v", cerr)
	}

	dumpWC, tracker := manager.openDumpArtifacts(testProjectID, sid)
	if dumpWC == nil || tracker == nil {
		t.Fatal("DEBUG-mode artifacts missing under the default DEBUG level")
	}
	if _, werr := dumpWC.Write([]byte(`{"dump":"record"}` + "\n")); werr != nil {
		t.Fatalf("write dump record: %v", werr)
	}
	if cerr := dumpWC.Close(); cerr != nil {
		t.Fatalf("close dump wrapper: %v", cerr)
	}

	logPath := config.SessionLogPathFor(agentDir, testProjectID, sid, config.DefaultSessionCompressionAlgorithm)
	dumpPath := config.SessionDumpPathFor(agentDir, testProjectID, sid, config.DefaultSessionCompressionAlgorithm)
	// The plain forms must NOT exist: new files are codec-named.
	for _, plain := range []string{
		config.SessionLogPath(agentDir, testProjectID, sid),
		config.SessionDumpPath(agentDir, testProjectID, sid),
	} {
		if _, serr := os.Stat(plain); !os.IsNotExist(serr) {
			t.Errorf("plain artifact exists beside codec-named file: %s (stat err=%v)", plain, serr)
		}
	}
	if got := zstdCLIDecode(t, logPath); !strings.Contains(got, "session log record") {
		t.Errorf("system CLI decode of %s misses the log record: %q", logPath, got)
	}
	if got := zstdCLIDecode(t, dumpPath); !strings.Contains(got, `"dump":"record"`) {
		t.Errorf("system CLI decode of %s misses the dump record: %q", dumpPath, got)
	}
}

// TestSessionArtifacts_ConfigFlipKeepsExistingFileCodec pins the "для
// существующих сессий алгоритм не меняем" rule: after the config flips
// gzip → zstd, reopening the SAME session continues the existing .gz file
// (a fresh gzip member) and never creates a second file under the new
// extension.
func TestSessionArtifacts_ConfigFlipKeepsExistingFileCodec(t *testing.T) {
	manager, _, agentDir := testManager(t)
	sid := "artifacts-flip-1"

	manager.SetSessionCompression(config.SessionCompressionGzip, 0)
	logger, logWC, err := manager.createSessionLogger(testProjectID, sid)
	if err != nil {
		t.Fatalf("createSessionLogger (gzip): %v", err)
	}
	logger.Info("first open")
	if cerr := logWC.Close(); cerr != nil {
		t.Fatalf("close log wrapper (gzip): %v", cerr)
	}

	manager.SetSessionCompression(config.SessionCompressionZstd, config.DefaultSessionZstdWindowLog)
	logger2, logWC2, err := manager.createSessionLogger(testProjectID, sid)
	if err != nil {
		t.Fatalf("createSessionLogger (after flip): %v", err)
	}
	logger2.Info("second open")
	if cerr := logWC2.Close(); cerr != nil {
		t.Fatalf("close log wrapper (after flip): %v", cerr)
	}

	zstPath := config.SessionLogPathFor(agentDir, testProjectID, sid, config.SessionCompressionZstd)
	if _, serr := os.Stat(zstPath); !os.IsNotExist(serr) {
		t.Fatalf("config flip created %s; an existing file must keep its own codec", zstPath)
	}
	gzPath := config.SessionLogPathFor(agentDir, testProjectID, sid, config.SessionCompressionGzip)
	got := gzipDecode(t, gzPath)
	if !strings.Contains(got, "first open") || !strings.Contains(got, "second open") {
		t.Errorf("gzip continuation lost records after flip; decoded: %q", got)
	}
}

// TestSessionArtifacts_PlainFileStaysPlain pins the pre-codec era: a plain
// session log keeps receiving plain bytes even under zstd config — the sniff
// continues its codec, no compressed sibling appears, and the file stays
// readable verbatim.
func TestSessionArtifacts_PlainFileStaysPlain(t *testing.T) {
	manager, _, agentDir := testManager(t)
	sid := "artifacts-plain-1"

	plainPath := config.SessionLogPath(agentDir, testProjectID, sid)
	if mkErr := os.MkdirAll(filepath.Dir(plainPath), 0o755); mkErr != nil {
		t.Fatalf("mkdir log dir: %v", mkErr)
	}
	if werr := os.WriteFile(plainPath, []byte("{\"legacy\":true}\n"), 0o644); werr != nil {
		t.Fatalf("seed plain log: %v", werr)
	}

	logger, logWC, err := manager.createSessionLogger(testProjectID, sid)
	if err != nil {
		t.Fatalf("createSessionLogger: %v", err)
	}
	logger.Info("appended plain record")
	if cerr := logWC.Close(); cerr != nil {
		t.Fatalf("close log wrapper: %v", cerr)
	}

	zstPath := config.SessionLogPathFor(agentDir, testProjectID, sid, config.DefaultSessionCompressionAlgorithm)
	if _, serr := os.Stat(zstPath); !os.IsNotExist(serr) {
		t.Fatalf("compressed sibling %s created beside a plain file", zstPath)
	}
	data, rerr := os.ReadFile(plainPath)
	if rerr != nil {
		t.Fatalf("read plain log: %v", rerr)
	}
	combined := string(data)
	if !strings.Contains(combined, "{\"legacy\":true}\n") || !strings.Contains(combined, "appended plain record") {
		t.Errorf("plain file content corrupted or truncated: %q", combined)
	}
}

// TestSessionArtifacts_EmptyFileAdoptsExtensionCodec pins the empty-file
// edge: a zero-byte .zst file (created, then the process died before any
// write) is compatible with every codec; its NAME is the only signal of
// intent, so it is continued as zstd even while the config says gzip.
func TestSessionArtifacts_EmptyFileAdoptsExtensionCodec(t *testing.T) {
	manager, _, agentDir := testManager(t)
	sid := "artifacts-empty-1"

	zstPath := config.SessionLogPathFor(agentDir, testProjectID, sid, config.SessionCompressionZstd)
	if mkErr := os.MkdirAll(filepath.Dir(zstPath), 0o755); mkErr != nil {
		t.Fatalf("mkdir log dir: %v", mkErr)
	}
	if werr := os.WriteFile(zstPath, nil, 0o644); werr != nil {
		t.Fatalf("seed empty zst file: %v", werr)
	}

	manager.SetSessionCompression(config.SessionCompressionGzip, 0)
	logger, logWC, err := manager.createSessionLogger(testProjectID, sid)
	if err != nil {
		t.Fatalf("createSessionLogger: %v", err)
	}
	logger.Info("resumed zst")
	if cerr := logWC.Close(); cerr != nil {
		t.Fatalf("close log wrapper: %v", cerr)
	}

	gzPath := config.SessionLogPathFor(agentDir, testProjectID, sid, config.SessionCompressionGzip)
	if _, serr := os.Stat(gzPath); !os.IsNotExist(serr) {
		t.Fatalf("gzip sibling %s created beside an empty .zst file", gzPath)
	}
	if got := zstdCLIDecode(t, zstPath); !strings.Contains(got, "resumed zst") {
		t.Errorf("empty .zst file not continued as zstd; decoded: %q", got)
	}
}

// TestSessionArtifacts_NonDebugDumpArtifactsDisabled pins that non-DEBUG
// behavior is unchanged: no dump file and no step dump tracker, while the
// session log — which is not DEBUG-gated — is still created.
func TestSessionArtifacts_NonDebugDumpArtifactsDisabled(t *testing.T) {
	manager, _, agentDir := testManager(t)
	sid := "artifacts-info-1"

	manager.SetLogLevel("WARN")
	dumpWC, tracker := manager.openDumpArtifacts(testProjectID, sid)
	if dumpWC != nil || tracker != nil {
		t.Fatal("non-DEBUG level must disable dump artifacts")
	}

	logger, logWC, err := manager.createSessionLogger(testProjectID, sid)
	if err != nil {
		t.Fatalf("createSessionLogger: %v", err)
	}
	logger.Info("info-level record") // dropped by the WARN handler; file still exists
	if cerr := logWC.Close(); cerr != nil {
		t.Fatalf("close log wrapper: %v", cerr)
	}
	logPath := config.SessionLogPathFor(agentDir, testProjectID, sid, config.DefaultSessionCompressionAlgorithm)
	if _, serr := os.Stat(logPath); serr != nil {
		t.Fatalf("session log should exist at non-DEBUG levels: %v", serr)
	}
}

// TestSessionArtifacts_SymlinkAtCodecPathRefused pins the link-safe model on
// the codec-named path: a symlink planted at the configured artifact path is
// refused (sniff open fails O_NOFOLLOW) with the same artifact-disabling
// warning as before, and nothing is created through it.
func TestSessionArtifacts_SymlinkAtCodecPathRefused(t *testing.T) {
	manager, _, agentDir := testManager(t)
	sid := "artifacts-symlink-1"

	capture := newCaptureLogger()
	manager.SetLogger(capture.logger())

	dumpsDir := filepath.Dir(config.SessionDumpPath(agentDir, testProjectID, sid))
	if mkErr := os.MkdirAll(dumpsDir, 0o755); mkErr != nil {
		t.Fatalf("mkdir dumps: %v", mkErr)
	}
	zstPath := config.SessionDumpPathFor(agentDir, testProjectID, sid, config.DefaultSessionCompressionAlgorithm)
	victim := filepath.Join(runtimeTempDir(t), "victim")
	if serr := os.Symlink(victim, zstPath); serr != nil {
		t.Fatalf("plant symlink: %v", serr)
	}

	dumpWC, tracker := manager.openDumpArtifacts(testProjectID, sid)
	if dumpWC != nil || tracker != nil {
		t.Fatal("dump file opened through a symlink")
	}
	if !capture.contains("failed to create LLM dump file") {
		t.Fatalf("expected a 'failed to create LLM dump file' warning, got: %s", capture.snapshot())
	}
	if _, serr := os.Lstat(zstPath); serr != nil {
		t.Fatalf("symlink itself should remain untouched: %v", serr)
	}
	if _, serr := os.Stat(victim); !os.IsNotExist(serr) {
		t.Fatalf("symlink target must stay untouched: %v", serr)
	}
}

// TestSessionArtifacts_StepDumpTrackerFollowsConfigCodec pins that the
// tracker compresses per-step dumps with the CONFIGURED algorithm — its file
// names carry the codec extension and decode with the system CLI.
func TestSessionArtifacts_StepDumpTrackerFollowsConfigCodec(t *testing.T) {
	manager, _, agentDir := testManager(t)
	sid := "artifacts-steps-1"

	_, tracker := manager.openDumpArtifacts(testProjectID, sid)
	if tracker == nil {
		t.Fatal("step dump tracker missing under DEBUG")
	}
	wc := tracker.OpenStepDump("1")
	if wc == nil {
		t.Fatal("OpenStepDump returned a nil writer")
	}
	if _, werr := io.WriteString(wc, "step record\n"); werr != nil {
		t.Fatalf("write step record: %v", werr)
	}
	// CloseAll writes the codec footer (the wrapper itself is io.Writer).
	if cerr := tracker.CloseAll(); cerr != nil {
		t.Fatalf("CloseAll: %v", cerr)
	}

	stepPath := filepath.Join(config.SessionStepDumpDir(agentDir, testProjectID, sid), "step_1.jsonl.zst")
	if _, serr := os.Stat(stepPath); serr != nil {
		t.Fatalf("codec-named step file missing: %v", serr)
	}
	if got := zstdCLIDecode(t, stepPath); !strings.Contains(got, "step record") {
		t.Errorf("system CLI decode of step dump misses the record: %q", got)
	}
}

// zstdStdlibDecode decodes path with the klauspost stdlib reader — the
// cross-platform decode for tests whose assertions must run on every CI leg
// (the system zstd CLI is absent on Windows; zstdCLIDecode skips there).
func zstdStdlibDecode(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	r, err := zstd.NewReader(f)
	if err != nil {
		t.Fatalf("zstd reader for %s: %v", path, err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("zstd decode %s: %v", path, err)
	}
	return string(data)
}

// TestSessionArtifacts_PlainNameWithZstdContentContinuesAsZstd pins the
// plain-name-with-compressed-bytes shape an operator's manual in-place
// conversion can leave behind (ADR-116): the plain NAME survives while the
// bytes become zstd. Continuation must sniff the magic, not trust the
// extension: under a gzip config the file keeps growing as zstd and neither
// a .gz sibling nor a fresh .zst sibling appears.
func TestSessionArtifacts_PlainNameWithZstdContentContinuesAsZstd(t *testing.T) {
	manager, _, agentDir := testManager(t)
	sid := "artifacts-migrated-1"

	// Seed the converted shape: produce a real zstd artifact with the default
	// config, then rename it onto the plain name — byte-identical to what an
	// operator's manual in-place conversion leaves behind.
	logger, logWC, err := manager.createSessionLogger(testProjectID, sid)
	if err != nil {
		t.Fatalf("createSessionLogger (seed): %v", err)
	}
	logger.Info("pre-migration record")
	if cerr := logWC.Close(); cerr != nil {
		t.Fatalf("close log wrapper (seed): %v", cerr)
	}
	zstPath := config.SessionLogPathFor(agentDir, testProjectID, sid, config.SessionCompressionZstd)
	plainPath := config.SessionLogPath(agentDir, testProjectID, sid)
	if rerr := os.Rename(zstPath, plainPath); rerr != nil {
		t.Fatalf("rename migrated artifact onto plain name: %v", rerr)
	}

	// A config flip away from zstd must not matter: the magic wins.
	manager.SetSessionCompression(config.SessionCompressionGzip, 0)
	logger2, logWC2, err := manager.createSessionLogger(testProjectID, sid)
	if err != nil {
		t.Fatalf("createSessionLogger (post-migration): %v", err)
	}
	logger2.Info("post-migration record")
	if cerr := logWC2.Close(); cerr != nil {
		t.Fatalf("close log wrapper (post-migration): %v", cerr)
	}

	gzPath := config.SessionLogPathFor(agentDir, testProjectID, sid, config.SessionCompressionGzip)
	if _, serr := os.Stat(gzPath); !os.IsNotExist(serr) {
		t.Fatalf("gzip sibling %s created beside a migrated plain-named file", gzPath)
	}
	if _, serr := os.Stat(zstPath); !os.IsNotExist(serr) {
		t.Fatalf("fresh zstd sibling %s created beside a migrated plain-named file", zstPath)
	}
	if detected, derr := appendcodec.DetectMagic(plainPath, nil); derr != nil || detected != appendcodec.CodecZstd {
		t.Fatalf("migrated file not detected as zstd after continuation (codec=%v, err=%v)", detected, derr)
	}
	if got := zstdStdlibDecode(t, plainPath); !strings.Contains(got, "pre-migration record") || !strings.Contains(got, "post-migration record") {
		t.Errorf("migrated continuation lost records; decoded: %q", got)
	}
	// The external-decodability acceptance, Unix legs only (see
	// zstdCLIAvailable): the migrated file must stay decodable by plain zstd.
	if got := zstdCLIDecode(t, plainPath); !strings.Contains(got, "post-migration record") {
		t.Errorf("system CLI decode of migrated file misses the appended record: %q", got)
	}
}
