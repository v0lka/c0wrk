package updater

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// sumsURLForTest mirrors backend.sumsURLFor: both the asset and SHA256SUMS
// live under the same …/releases/download/<tag>/ prefix, so the sums URL is
// the asset URL with its trailing segment replaced by "SHA256SUMS".
func sumsURLForTest(assetURL string) string {
	idx := lastIndexByte(assetURL, '/')
	if idx < 0 {
		return ""
	}
	return assetURL[:idx+1] + sumsFilename
}

// lastIndexByte returns the index of the last occurrence of b in s, or -1.
func lastIndexByte(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// This file holds the cross-component integration tests that exercise the
// end-to-end self-update pipeline (check → download → verify → stage → swap)
// against a single in-process HTTP server that impersonates both the GitHub
// releases/latest API and the asset/SHA256SUMS download host. No real network
// is touched and no GUI process is launched (relaunch is stubbed).
//
// These tests intentionally reuse the unit-test helpers (makeZip/makeTarGz,
// buildPlatformArchive, spawnDeadPID, releasePayload, sha256Hex, sumsLine)
// already defined in the package so the integration scenarios stay consistent
// with the component-level coverage.

// integrationServer is a single httptest server that serves, under one origin:
//
//   - GET /repos/v0lka/c0wrk/releases/latest → GitHub release JSON whose asset
//     URLs point back at this same server.
//   - GET /<assetName>                       → the release archive bytes.
//   - GET /SHA256SUMS                        → the checksums body.
//
// The archive is built in the format the *running* platform expects so the
// download → extract → swap chain can run for real.
func integrationServer(t *testing.T, assetName string) (srv *httptest.Server, markerRel string) {
	t.Helper()

	// 1. Build a real, platform-appropriate update archive into a scratch path
	// and read its bytes back so they can be served.
	scratch := t.TempDir()
	archivePath := filepath.Join(scratch, assetName)
	markerRel = buildPlatformArchive(t, archivePath)
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read built archive: %v", err)
	}

	// 2. Build the matching SHA256SUMS body (entry keyed by the asset basename).
	sumsBytes := []byte(sumsLine(sha256Hex(archiveBytes), assetName))

	// 3. Serve everything under one origin. The release JSON's asset URL is
	// constructed lazily inside the handler because the server URL is only
	// known after NewServer.
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/v0lka/c0wrk/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		// Defer building the payload until the server URL exists.
		srvBase := "http://" + srv.Listener.Addr().String() + "/"
		asset := ReleaseAsset{
			Name:               assetName,
			BrowserDownloadURL: srvBase + assetName,
			ContentType:        "application/octet-stream",
			Size:               int64(len(archiveBytes)),
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(releasePayload("v9.9.9", asset)))
	})
	mux.HandleFunc("/"+assetName, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(archiveBytes)))
		_, _ = w.Write(archiveBytes)
	})
	mux.HandleFunc("/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", strconv.Itoa(len(sumsBytes)))
		_, _ = w.Write(sumsBytes)
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, markerRel
}

// pipelineChecker builds a Checker whose API base + asset host both point at
// the integration server, fixed to the *running* platform so the selected
// asset matches the archive the server actually serves.
func pipelineChecker(t *testing.T, srv *httptest.Server, current string) *Checker {
	t.Helper()
	goos, goarch := CurrentPlatform()
	c := NewChecker(Config{CurrentVersion: current}, srv.Client(), nil)
	c.baseURL = srv.URL
	c.WithPlatform(goos, goarch)
	return c
}

// TestPipeline_CheckDownloadVerifyStage_E2E is the headline integration smoke
// test. It drives the full happy path against a mock host:
//
//  1. Check  → a strictly-newer release is detected for this platform.
//  2. Download → the asset + SHA256SUMS are fetched into a staging dir.
//  3. Verify  → the downloader's SHA256 check passes (fail-closed otherwise).
//  4. Stage/Apply → ApplySelfUpdate swaps the install tree from the staged
//     archive (dead parent PID, stubbed relaunch) and leaves a .old backup.
//
// A tampered-checksum variant (TestPipeline_TamperedChecksumAbortsBeforeStage)
// proves the verify step is a hard gate: no archive survives, and staging is
// never attempted.
func TestPipeline_CheckDownloadVerifyStage_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("integration smoke test in -short mode")
	}

	// Only platforms in the release matrix have a canonical asset to serve.
	goos, goarch := CurrentPlatform()
	assetName, err := AssetNameForPlatform(goos, goarch)
	if err != nil {
		t.Skipf("no canonical release asset for %s/%s: %v", goos, goarch, err)
	}

	srv, markerRel := integrationServer(t, assetName)

	// --- 1. CHECK -----------------------------------------------------------
	checker := pipelineChecker(t, srv, "v1.0.0")
	res, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: unexpected error: %v", err)
	}
	if !res.Available {
		t.Fatalf("Check: expected update available, got %+v", res)
	}
	if res.AssetName != assetName {
		t.Errorf("Check: asset = %q, want %q", res.AssetName, assetName)
	}
	if res.AssetURL == "" {
		t.Fatal("Check: AssetURL empty for an available update")
	}

	// --- 2+3. DOWNLOAD + VERIFY --------------------------------------------
	staging := t.TempDir()
	dl := NewDownloader(srv.Client(), nil)
	sumsURL := sumsURLForTest(res.AssetURL)
	progressCalls := 0
	dres, err := dl.Download(context.Background(), res.AssetURL, sumsURL, res.AssetName, staging, func(_, _ int64) {
		progressCalls++
	})
	if err != nil {
		t.Fatalf("Download: unexpected error: %v", err)
	}
	if dres.AssetName != assetName {
		t.Errorf("Download: asset = %q, want %q", dres.AssetName, assetName)
	}
	if dres.Bytes <= 0 {
		t.Errorf("Download: reported %d bytes, want > 0", dres.Bytes)
	}
	if progressCalls == 0 {
		t.Error("Download: progress callback was never invoked")
	}
	// The verified archive must be present in staging.
	if _, err := os.Stat(dres.ArchivePath); err != nil {
		t.Fatalf("Download: verified archive missing at %q: %v", dres.ArchivePath, err)
	}
	if _, err := os.Stat(dres.SumsPath); err != nil {
		t.Fatalf("Download: SHA256SUMS missing at %q: %v", dres.SumsPath, err)
	}

	// --- 4. STAGE / APPLY (swap) ------------------------------------------
	// Shorten the poll window so a stale PID resolves quickly.
	origTimeout := shutdownTimeout
	shutdownTimeout = 2 * time.Second
	t.Cleanup(func() { shutdownTimeout = origTimeout })

	dead := spawnDeadPID(t)

	root := newNonTempDir(t)
	target := filepath.Join(root, "install-target")
	writeMarker(t, target, "version.txt", "old")

	// Stub relaunch so no GUI process is started.
	relaunched := false
	origRelaunch := relaunchFn
	relaunchFn = func(string, *slog.Logger) error {
		relaunched = true
		return nil
	}
	t.Cleanup(func() { relaunchFn = origRelaunch })

	opts := SelfUpdateOptions{PID: dead, StageDir: staging, TargetDir: target}
	if err := ApplySelfUpdate(opts, slog.Default()); err != nil {
		t.Fatalf("ApplySelfUpdate: %v", err)
	}

	if !relaunched {
		t.Error("ApplySelfUpdate: relaunch was not invoked")
	}
	// New marker planted by the staged archive must be at the target root.
	got, err := os.ReadFile(filepath.Join(target, markerRel))
	if err != nil {
		t.Fatalf("new marker missing at target: %v", err)
	}
	if string(got) != "installed-via-swap" {
		t.Errorf("marker content = %q, want %q", string(got), "installed-via-swap")
	}
	// Previous version backed up for manual rollback.
	if _, err := os.Stat(target + ".old"); err != nil {
		t.Errorf(".old backup missing: %v", err)
	}
	// Staging directory cleaned up after a successful apply.
	if _, err := os.Stat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staging dir should be removed after apply, stat err = %v", err)
	}
}

// TestPipeline_TamperedChecksumAbortsBeforeStage proves verification is a hard
// gate between download and staging: a SHA256 mismatch removes the archive and
// Download returns a wrapping error, so the install tree is never touched.
func TestPipeline_TamperedChecksumAbortsBeforeStage(t *testing.T) {
	if testing.Short() {
		t.Skip("integration smoke test in -short mode")
	}
	goos, goarch := CurrentPlatform()
	assetName, err := AssetNameForPlatform(goos, goarch)
	if err != nil {
		t.Skipf("no canonical release asset for %s/%s: %v", goos, goarch, err)
	}

	// Build the real archive bytes …
	scratch := t.TempDir()
	archivePath := filepath.Join(scratch, assetName)
	buildPlatformArchive(t, archivePath)
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read built archive: %v", err)
	}
	// … but ship a SHA256SUMS whose digest is deliberately wrong (one byte
	// flipped relative to the true digest) so verification must fail.
	trueDigest := sha256Hex(archiveBytes)
	tamperedDigest := flipDigestChar(trueDigest)
	sumsBytes := []byte(sumsLine(tamperedDigest, assetName))

	// srv is captured by the handler closures by reference and is only read
	// when a request arrives (well after assignment), so the lazy base URL is
	// resolved correctly — same pattern as integrationServer above.
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/v0lka/c0wrk/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		srvBase := "http://" + srv.Listener.Addr().String() + "/"
		asset := ReleaseAsset{
			Name:               assetName,
			BrowserDownloadURL: srvBase + assetName,
		}
		_, _ = w.Write([]byte(releasePayload("v9.9.9", asset)))
	})
	mux.HandleFunc("/"+assetName, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(archiveBytes)))
		_, _ = w.Write(archiveBytes)
	})
	mux.HandleFunc("/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(sumsBytes)))
		_, _ = w.Write(sumsBytes)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	checker := pipelineChecker(t, srv, "v1.0.0")
	res, cerr := checker.Check(context.Background())
	if cerr != nil {
		t.Fatalf("Check: unexpected error: %v", cerr)
	}
	if !res.Available {
		t.Fatalf("Check: expected update available, got %+v", res)
	}

	staging := t.TempDir()
	dl := NewDownloader(srv.Client(), nil)
	_, derr := dl.Download(context.Background(), res.AssetURL, sumsURLForTest(res.AssetURL), res.AssetName, staging, nil)
	if !errors.Is(derr, ErrChecksumMismatch) {
		t.Fatalf("Download: expected ErrChecksumMismatch, got %v", derr)
	}
	// Fail-closed: no archive may survive in staging.
	archivePath = filepath.Join(staging, assetName)
	if _, err := os.Stat(archivePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("tampered archive must be removed, stat err = %v", err)
	}
}

// flipDigestChar returns digest with its first hex digit changed to a
// different valid hex digit, guaranteed to differ from the original. Used only
// to construct a deliberately-mismatched checksum line.
func flipDigestChar(digest string) string {
	if digest == "" {
		return "0"
	}
	first := digest[0]
	// Map any hex digit to a different one by toggling its low bit's parity
	// in a rotation that always changes the character.
	const hexdigs = "0123456789abcdef"
	idx := -1
	for i := 0; i < len(hexdigs); i++ {
		if hexdigs[i] == first {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "0" + digest[1:]
	}
	next := hexdigs[(idx+1)%len(hexdigs)]
	return string(next) + digest[1:]
}

// TestPipeline_SkippedVersionSuppressesUpdate wires the checker+server as a
// mini-integration: when the latest tag equals the user's skipped version, the
// pipeline reports no update even though a newer release exists, so no
// download is ever staged.
func TestPipeline_SkippedVersionSuppressesUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("integration smoke test in -short mode")
	}
	goos, goarch := CurrentPlatform()
	assetName, err := AssetNameForPlatform(goos, goarch)
	if err != nil {
		t.Skipf("no canonical release asset for %s/%s: %v", goos, goarch, err)
	}

	srv, _ := integrationServer(t, assetName)

	// Current is old, but the latest tag (v9.9.9) is explicitly skipped.
	checker := NewChecker(Config{CurrentVersion: "v1.0.0", SkippedVersion: "v9.9.9"}, srv.Client(), nil)
	checker.baseURL = srv.URL
	checker.WithPlatform(goos, goarch)

	res, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: unexpected error: %v", err)
	}
	if res.Available {
		t.Fatalf("Check: expected no update for skipped version, got Available=true (asset=%q)", res.AssetName)
	}
}
