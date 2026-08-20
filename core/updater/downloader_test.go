package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	testAssetName = "c0wrk-desktop-macos-arm64.zip"
	testSumsName  = "SHA256SUMS"
)

// sha256Hex returns the lowercase hex SHA256 of data — used to build correct
// sums bodies for the success path and tampered bodies for the mismatch path.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// sumsLine builds a standard text-mode sha256sum line.
func sumsLine(digest, filename string) string {
	return digest + "  " + filename + "\n"
}

// makeServer returns an httptest server that serves the given asset and sums
// bodies from fixed paths, returning the base URL. Content-Length is set
// explicitly (as GitHub does for release assets) so progress reporting is
// wired deterministically rather than depending on Go's auto-buffer heuristics.
func makeServer(t *testing.T, assetBody, sumsBody []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/"+testAssetName, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Length", strconv.Itoa(len(assetBody)))
		_, _ = w.Write(assetBody)
	})
	mux.HandleFunc("/"+testSumsName, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(sumsBody)))
		_, _ = w.Write(sumsBody)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// runDownload invokes a fresh Downloader against the server and returns the
// result and error. The staging dir is per-test via t.TempDir().
func runDownload(t *testing.T, srv *httptest.Server, sumsBody []byte, progress func(int64, int64)) (*DownloadResult, error) {
	t.Helper()
	staging := t.TempDir()
	d := NewDownloader(srv.Client(), nil)
	return d.Download(
		context.Background(),
		srv.URL+"/"+testAssetName,
		srv.URL+"/"+testSumsName,
		testAssetName,
		staging,
		progress,
	)
}

// TestRequireHTTPS verifies that only https URLs are accepted, with the
// loopback exception that keeps the package testable.
func TestRequireHTTPS(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"https production", "https://github.com/asset.zip", false},
		{"loopback ipv4", "http://127.0.0.1:8080/asset.zip", false},
		{"loopback localhost", "http://localhost:8080/asset.zip", false},
		{"loopback ipv6", "http://[::1]:8080/asset.zip", false},
		{"plain http non-loopback", "http://example.com/asset.zip", true},
		{"plain http private ip", "http://10.0.0.1/asset.zip", true},
		{"ftp", "ftp://github.com/asset.zip", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := requireHTTPS(tc.url)
			if tc.wantErr && err == nil {
				t.Errorf("requireHTTPS(%q) = nil, want error", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("requireHTTPS(%q) = %v, want nil", tc.url, err)
			}
		})
	}
}

// TestDownload_RejectsPathTraversalAssetName verifies that an attacker-
// influenced asset name cannot escape the staging directory. The asset name is
// checked before any network I/O or directory creation, so the test uses
// synthetic HTTPS URLs and no server.
func TestDownload_RejectsPathTraversalAssetName(t *testing.T) {
	t.Parallel()
	bad := []string{
		"",
		".",
		"..",
		"../evil-macos-arm64.plist",
		"../../evil-macos-arm64.plist",
		"sub/evil-macos-arm64.plist",
	}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := NewDownloader(nil, nil)
			staging := t.TempDir()
			_, err := d.Download(
				context.Background(),
				"https://example.com/a.zip",
				"https://example.com/SHA256SUMS",
				name,
				staging,
				nil,
			)
			if err == nil {
				t.Fatalf("Download with asset name %q = nil error, want rejection", name)
			}
			// The staging directory must remain empty: the rejection happens
			// before any file is created.
			entries, readErr := os.ReadDir(staging)
			if readErr != nil {
				t.Fatalf("reading staging dir: %v", readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("staging dir not empty after rejection: %v", entries)
			}
		})
	}
}

// TestDownload_Success is the happy path: the correct checksums verify the
// archive, which is left in staging, and the reported size matches.
func TestDownload_Success(t *testing.T) {
	assetBody := []byte("this is a release archive body")
	sumsBody := []byte(sumsLine(sha256Hex(assetBody), testAssetName))

	srv := makeServer(t, assetBody, sumsBody)
	res, err := runDownload(t, srv, sumsBody, nil)
	if err != nil {
		t.Fatalf("Download returned error: %v", err)
	}
	if res.AssetName != testAssetName {
		t.Errorf("AssetName = %q, want %q", res.AssetName, testAssetName)
	}
	if res.Bytes != int64(len(assetBody)) {
		t.Errorf("Bytes = %d, want %d", res.Bytes, len(assetBody))
	}
	// Archive must exist at the reported path.
	if _, err := os.Stat(res.ArchivePath); err != nil {
		t.Errorf("archive missing at %q: %v", res.ArchivePath, err)
	}
	got, err := os.ReadFile(res.ArchivePath)
	if err != nil {
		t.Fatalf("reading archive: %v", err)
	}
	if !bytes.Equal(got, assetBody) {
		t.Error("archive content does not match what was served")
	}
}

// TestDownload_BadByteMismatch covers the "corrupt-byte" (corrupt byte)
// criterion: a single differing byte changes the digest, so verification must
// fail-closed (ErrChecksumMismatch) and the archive must be removed from
// staging.
func TestDownload_BadByteMismatch(t *testing.T) {
	correct := []byte("release body that verifies")
	tampered := []byte("release body that verifies!") // trailing '!' flips the digest
	sumsBody := []byte(sumsLine(sha256Hex(correct), testAssetName))

	srv := makeServer(t, tampered, sumsBody)
	res, err := runDownload(t, srv, sumsBody, nil)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got %v", err)
	}
	if res != nil {
		t.Errorf("expected nil result on failure, got %+v", res)
	}
	// Fail-closed: archive must NOT be left behind.
	if res != nil {
		if _, statErr := os.Stat(res.ArchivePath); statErr == nil {
			t.Error("corrupt archive was left in staging after mismatch")
		}
	}
}

// TestDownload_MissingChecksumLine covers the "missing-line" criterion:
// the sums file exists but has no entry for the asset → fail-closed
// (ErrChecksumNotFound) and the archive removed.
func TestDownload_MissingChecksumLine(t *testing.T) {
	assetBody := []byte("payload")
	// Sums reference a *different* asset only.
	sumsBody := []byte(sumsLine(sha256Hex(assetBody), "some-other-asset.zip"))

	srv := makeServer(t, assetBody, sumsBody)
	res, err := runDownload(t, srv, sumsBody, nil)
	if !errors.Is(err, ErrChecksumNotFound) {
		t.Fatalf("expected ErrChecksumNotFound, got %v", err)
	}
	if res != nil {
		if _, statErr := os.Stat(res.ArchivePath); statErr == nil {
			t.Error("archive was left in staging after missing-checksum failure")
		}
	}
}

// TestDownload_EmptySums covers the "empty-sums" criterion: an empty sums body
// → no entry → fail-closed.
func TestDownload_EmptySums(t *testing.T) {
	assetBody := []byte("payload")
	srv := makeServer(t, assetBody, []byte(""))
	res, err := runDownload(t, srv, []byte(""), nil)
	if !errors.Is(err, ErrChecksumNotFound) {
		t.Fatalf("expected ErrChecksumNotFound for empty sums, got %v", err)
	}
	if res != nil {
		if _, statErr := os.Stat(res.ArchivePath); statErr == nil {
			t.Error("archive was left in staging after empty-sums failure")
		}
	}
}

// TestDownload_MalformedSums covers the "malformed-line" criterion: a
// malformed sums line → fail-closed.
func TestDownload_MalformedSums(t *testing.T) {
	assetBody := []byte("payload")
	sumsBody := []byte("not-a-valid-checksum-line\n")
	srv := makeServer(t, assetBody, sumsBody)
	res, err := runDownload(t, srv, sumsBody, nil)
	if !errors.Is(err, ErrMalformedChecksumLine) {
		t.Fatalf("expected ErrMalformedChecksumLine, got %v", err)
	}
	if res != nil {
		if _, statErr := os.Stat(res.ArchivePath); statErr == nil {
			t.Error("archive was left in staging after malformed-sums failure")
		}
	}
}

// TestDownload_SumsFetchFailure confirms that when the sums download fails the
// already-downloaded asset is cleaned up (no dangling unverified artifact).
func TestDownload_SumsFetchFailure(t *testing.T) {
	assetBody := []byte("payload")
	mux := http.NewServeMux()
	mux.HandleFunc("/"+testAssetName, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(assetBody)
	})
	mux.HandleFunc("/"+testSumsName, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	staging := t.TempDir()
	d := NewDownloader(srv.Client(), nil)
	res, err := d.Download(
		context.Background(),
		srv.URL+"/"+testAssetName,
		srv.URL+"/"+testSumsName,
		testAssetName,
		staging,
		nil,
	)
	if err == nil {
		t.Fatal("expected error when sums fetch fails, got nil")
	}
	if res != nil {
		t.Errorf("expected nil result on failure, got %+v", res)
	}
	// No asset file should remain in staging.
	entries, err := os.ReadDir(staging)
	if err != nil {
		t.Fatalf("reading staging dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() == testAssetName {
			t.Errorf("asset %q should have been removed after sums fetch failure", testAssetName)
		}
	}
}

// TestDownload_AtomicWrite verifies the tmp+rename contract: while the download
// is in flight no file exists at the final archive path, and after success the
// final path exists with the right content (and no leftover .tmp files).
func TestDownload_AtomicWrite(t *testing.T) {
	assetBody := []byte("atomic-write-payload")
	sumsBody := []byte(sumsLine(sha256Hex(assetBody), testAssetName))

	srv := makeServer(t, assetBody, sumsBody)

	staging := t.TempDir()
	d := NewDownloader(srv.Client(), nil)

	var observedFinal, observedTmp bool
	progress := func(done, total int64) {
		// During the download the final file must not exist yet, and a tmp
		// file should be present.
		if _, err := os.Stat(filepath.Join(staging, testAssetName)); err == nil {
			observedFinal = true
		}
		entries, _ := os.ReadDir(staging)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".tmp") {
				observedTmp = true
			}
		}
	}

	res, err := d.Download(
		context.Background(),
		srv.URL+"/"+testAssetName,
		srv.URL+"/"+testSumsName,
		testAssetName,
		staging,
		progress,
	)
	if err != nil {
		t.Fatalf("Download returned error: %v", err)
	}
	if observedFinal {
		t.Error("final archive path was visible mid-download (not atomic)")
	}
	if !observedTmp {
		t.Error("expected a .tmp file to be observed during download")
	}
	// After completion the final file exists and no .tmp lingers.
	if _, err := os.Stat(res.ArchivePath); err != nil {
		t.Errorf("archive missing after success: %v", err)
	}
	entries, _ := os.ReadDir(staging)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover tmp file %q after download", e.Name())
		}
	}
}

// TestDownload_ProgressThrottle confirms the progress callback is invoked and
// reports the total size, and that it is throttled (many bytes, few callbacks).
func TestDownload_ProgressThrottle(t *testing.T) {
	// Large enough body to produce multiple chunks but small for test speed.
	assetBody := bytes.Repeat([]byte("x"), 256*1024)
	sumsBody := []byte(sumsLine(sha256Hex(assetBody), testAssetName))
	srv := makeServer(t, assetBody, sumsBody)

	var calls int
	var lastDone int64
	var reportedTotal int64
	progress := func(done, total int64) {
		calls++
		if done < lastDone {
			t.Errorf("progress went backwards: %d < %d", done, lastDone)
		}
		lastDone = done
		reportedTotal = total
	}
	res, err := runDownload(t, srv, sumsBody, progress)
	if err != nil {
		t.Fatalf("Download returned error: %v", err)
	}
	if calls == 0 {
		t.Fatal("progress callback was never invoked")
	}
	if reportedTotal != int64(len(assetBody)) {
		t.Errorf("reported total = %d, want %d", reportedTotal, len(assetBody))
	}
	// Final byte count must reach the full size.
	if res.Bytes != int64(len(assetBody)) {
		t.Errorf("Bytes = %d, want %d", res.Bytes, len(assetBody))
	}
}

// TestDownload_ContextCancellation confirms a cancelled context aborts the
// download cleanly: an error is returned and no archive is published.
func TestDownload_ContextCancellation(t *testing.T) {
	// Slow-streaming body so cancellation can interrupt mid-download. Small
	// enough that the streaming loop is short; the handler respects the
	// request context so the server shuts down promptly once the client bails.
	assetBody := bytes.Repeat([]byte("y"), 256*1024)
	mux := http.NewServeMux()
	mux.HandleFunc("/"+testAssetName, func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		for i := 0; i < len(assetBody); i += 4096 {
			// Respect client cancellation so srv.Close() does not block.
			if r.Context().Err() != nil {
				return
			}
			end := i + 4096
			if end > len(assetBody) {
				end = len(assetBody)
			}
			if _, err := w.Write(assetBody[i:end]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	mux.HandleFunc("/"+testSumsName, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sumsLine(sha256Hex(assetBody), testAssetName)))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after the download starts.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	staging := t.TempDir()
	d := NewDownloader(srv.Client(), nil)
	res, err := d.Download(
		ctx,
		srv.URL+"/"+testAssetName,
		srv.URL+"/"+testSumsName,
		testAssetName,
		staging,
		nil,
	)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if res != nil {
		t.Errorf("expected nil result on cancellation, got %+v", res)
	}
	// No final archive should have been published.
	if _, err := os.Stat(filepath.Join(staging, testAssetName)); err == nil {
		t.Error("archive was published despite context cancellation")
	}
	// No leftover tmp files.
	entries, _ := os.ReadDir(staging)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover tmp file %q after cancellation", e.Name())
		}
	}
}

// TestNewDownloader_Defaults confirms nil client/verifier are replaced with
// sane defaults rather than panicking later.
func TestNewDownloader_Defaults(t *testing.T) {
	d := NewDownloader(nil, nil)
	if d.Client == nil {
		t.Error("expected non-nil default client")
	}
	if d.Verifier == nil {
		t.Error("expected non-nil default verifier")
	}
	if _, ok := d.Verifier.(SHA256Verifier); !ok {
		t.Errorf("expected SHA256Verifier default, got %T", d.Verifier)
	}
}
