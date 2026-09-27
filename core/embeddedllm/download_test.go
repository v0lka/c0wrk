package embeddedllm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// dlRangeServer is an HTTPS test server that serves a fixed body either with
// real Range/206 support or by ignoring Range entirely (200), plus a mode that
// rejects every ranged request with 416. It records the Range headers it saw so
// a test can assert what the downloader actually asked for.
//
// HTTPS (not plain HTTP) because validateAsset refuses non-HTTPS URLs — the
// supply-chain gate is exercised by every test here rather than bypassed.
type dlRangeServer struct {
	*httptest.Server

	mode dlServerMode
	body []byte

	// writeDelay > 0 makes the server stream in chunkSize pieces with a pause
	// and a flush between them, so cancellation lands mid-transfer.
	writeDelay time.Duration
	chunkSize  int

	mu       sync.Mutex
	requests int
	ranges   []string
	statuses []int
}

type dlServerMode int

const (
	dlServeRange   dlServerMode = iota // honours Range with 206, like HF and GitHub
	dlIgnoreRange                      // always 200 with the full body
	dlRejectRange                      // 416 for any ranged request, 200 otherwise
	dlAlwaysReject                     // 416 for every request, ranged or not
	// dlTruncateBody answers 200 with NO Content-Length and a body that ends
	// cleanly at half the artifact — the shape a proxy that strips
	// Content-Length produces, where io.Copy reports a short count and a nil
	// error rather than a transport failure.
	dlTruncateBody
)

func newDLRangeServer(t *testing.T, body []byte, mode dlServerMode) *dlRangeServer {
	t.Helper()
	s := &dlRangeServer{body: body, mode: mode}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

func (s *dlRangeServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests++
	rangeHeader := r.Header.Get("Range")
	s.ranges = append(s.ranges, rangeHeader)
	s.mu.Unlock()

	total := len(s.body)
	if s.mode == dlAlwaysReject || (rangeHeader != "" && s.mode == dlRejectRange) {
		s.record(http.StatusRequestedRangeNotSatisfiable)
		w.Header().Set("Content-Range", "bytes */"+strconv.Itoa(total))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}

	start := -1
	if rangeHeader != "" && s.mode == dlServeRange {
		parsed, ok := dlParseTestRange(rangeHeader, total)
		if !ok {
			s.record(http.StatusRequestedRangeNotSatisfiable)
			w.Header().Set("Content-Range", "bytes */"+strconv.Itoa(total))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		start = parsed
	}

	if start >= 0 {
		chunk := s.body[start:]
		s.record(http.StatusPartialContent)
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, total-1, total))
		w.Header().Set("Content-Length", strconv.Itoa(len(chunk)))
		w.WriteHeader(http.StatusPartialContent)
		s.writeBody(w, chunk)
		return
	}

	if s.mode == dlTruncateBody {
		s.record(http.StatusOK)
		w.Header().Set("Accept-Ranges", "none")
		// Deliberately no Content-Length: the transfer is chunked, so the client
		// sees a clean EOF after half the artifact instead of a transport error.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(s.body[:len(s.body)/2])
		return
	}

	s.record(http.StatusOK)
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Content-Length", strconv.Itoa(total))
	w.WriteHeader(http.StatusOK)
	s.writeBody(w, s.body)
}

// writeBody streams the body, optionally in delayed chunks so a test can
// cancel a transfer that is genuinely still in flight rather than racing a
// loopback write that completes before the callback runs.
func (s *dlRangeServer) writeBody(w http.ResponseWriter, chunk []byte) {
	if s.writeDelay <= 0 {
		_, _ = w.Write(chunk)
		return
	}
	size := s.chunkSize
	if size <= 0 {
		size = 32 * 1024
	}
	flusher, _ := w.(http.Flusher)
	for off := 0; off < len(chunk); off += size {
		end := min(off+size, len(chunk))
		if _, err := w.Write(chunk[off:end]); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(s.writeDelay)
	}
}

func (s *dlRangeServer) record(status int) {
	s.mu.Lock()
	s.statuses = append(s.statuses, status)
	s.mu.Unlock()
}

func (s *dlRangeServer) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *dlRangeServer) rangeHeaders() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ranges...)
}

func (s *dlRangeServer) statusCodes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.statuses...)
}

// dlParseTestRange parses "bytes=N-" against a body length. ok is false when
// the offset is beyond EOF, which is what makes a server answer 416.
func dlParseTestRange(header string, total int) (int, bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if !found {
		return 0, false
	}
	startStr, _, found := strings.Cut(spec, "-")
	if !found {
		return 0, false
	}
	start, err := strconv.Atoi(strings.TrimSpace(startStr))
	if err != nil || start < 0 || start >= total {
		return 0, false
	}
	return start, true
}

// dlAssetFor builds a correctly pinned Asset for a test server's body.
func dlAssetFor(url string, body []byte) Asset {
	sum := sha256.Sum256(body)
	return Asset{
		Component:   ComponentModel,
		URL:         url,
		SHA256:      hex.EncodeToString(sum[:]),
		SizeBytes:   int64(len(body)),
		ArchiveName: "fixture.gguf",
	}
}

// dlBody returns a deterministic non-trivial byte pattern of length n.
func dlBody(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i/251)
	}
	return b
}

func dlDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newDLTestDownloader returns a Downloader wired to a test server's TLS client.
func newDLTestDownloader(srv *httptest.Server) *Downloader {
	return NewDownloader(srv.Client(), dlDiscardLogger())
}

// dlProgressRecorder captures every (done, total) pair a callback receives.
type dlProgressRecorder struct {
	mu    sync.Mutex
	calls [][2]int64
}

func (r *dlProgressRecorder) fn(done, total int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, [2]int64{done, total})
}

func (r *dlProgressRecorder) snapshot() [][2]int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][2]int64(nil), r.calls...)
}

// dlUnlimitedSpace is a FreeSpace stub that never trips the disk guard.
func dlUnlimitedSpace(string) (int64, error) { return int64(1) << 62, nil }

// ---------------------------------------------------------------------------
// Fail-closed verification
// ---------------------------------------------------------------------------

// TestDownload_ChecksumMismatchDeletesPartialAndRefusesBytes is the core
// ASI04 guarantee: bytes whose digest differs from the compile-time pin are
// never accepted, and the partial file is deleted rather than left behind for
// a later stage to mistake for good data.
func TestDownload_ChecksumMismatchDeletesPartialAndRefusesBytes(t *testing.T) {
	body := dlBody(512 * 1024)
	srv := newDLRangeServer(t, body, dlServeRange)
	d := newDLTestDownloader(srv.Server)
	d.FreeSpace = dlUnlimitedSpace

	// Same length, different content: the size gate passes so the failure has
	// to come from the digest comparison itself.
	tampered := dlBody(512 * 1024)
	tampered[0] ^= 0xFF
	asset := dlAssetFor(srv.URL+"/model.gguf", tampered)
	asset.SizeBytes = int64(len(body))

	dst := filepath.Join(t.TempDir(), "nested", "model.gguf")
	res, err := d.Download(context.Background(), asset, dst, nil)
	if res != nil {
		t.Fatalf("Download returned a non-nil result alongside err = %v", err)
	}
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch", err)
	}
	if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination %q exists after a checksum mismatch (stat err = %v); unverified bytes were accepted", dst, statErr)
	}
	if _, statErr := os.Stat(dst + PartialSuffix); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial %q survived a checksum mismatch; it must be deleted", dst+PartialSuffix)
	}
	// The parent directory is created by Download; nothing inside it may be.
	entries, readErr := os.ReadDir(filepath.Dir(dst))
	if readErr != nil {
		t.Fatalf("reading the destination directory: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("destination directory holds %d entr(ies) after a failed verification, want 0", len(entries))
	}
}

// TestDownload_MissingChecksumIsFailClosed covers the second half of the
// fail-closed rule: an empty, absent or malformed digest REFUSES the download.
// It must never be read as "no checksum published, skip verification". The
// request counter is the assertion — a refused artifact performs zero I/O.
func TestDownload_MissingChecksumIsFailClosed(t *testing.T) {
	body := dlBody(2048)
	srv := newDLRangeServer(t, body, dlServeRange)
	valid := dlAssetFor(srv.URL+"/model.gguf", body)

	cases := []struct {
		name   string
		mutate func(*Asset)
	}{
		{"empty digest", func(a *Asset) { a.SHA256 = "" }},
		{"whitespace digest", func(a *Asset) { a.SHA256 = "   " }},
		{"short digest", func(a *Asset) { a.SHA256 = "deadbeef" }},
		{"non-hex digest", func(a *Asset) { a.SHA256 = strings.Repeat("z", 64) }},
		{"uppercase digest", func(a *Asset) { a.SHA256 = strings.ToUpper(valid.SHA256) }},
		{"missing URL", func(a *Asset) { a.URL = "" }},
		{"non-HTTPS URL", func(a *Asset) { a.URL = "http://example.invalid/model.gguf" }},
		{"zero size", func(a *Asset) { a.SizeBytes = 0 }},
		{"negative size", func(a *Asset) { a.SizeBytes = -1 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := srv.hits()
			asset := valid
			tc.mutate(&asset)

			d := newDLTestDownloader(srv.Server)
			d.FreeSpace = dlUnlimitedSpace
			res, err := d.Download(context.Background(), asset, filepath.Join(t.TempDir(), "model.gguf"), nil)
			if res != nil {
				t.Fatalf("Download returned a result alongside err = %v", err)
			}
			if err == nil {
				t.Fatal("Download succeeded for an artifact with an unusable pin; it must refuse")
			}
			switch tc.name {
			case "missing URL", "non-HTTPS URL", "zero size", "negative size":
				// A pin-shape refusal; the digest sentinel does not apply.
			default:
				if !errors.Is(err, ErrMissingChecksum) {
					t.Fatalf("err = %v, want ErrMissingChecksum", err)
				}
			}
			if got := srv.hits() - before; got != 0 {
				t.Fatalf("a refused artifact performed %d network request(s); fail-closed must stop before I/O", got)
			}
		})
	}
}

// TestVerifyFile_FailClosed asserts the standalone verifier refuses an
// unpinned asset too, so no code path can verify against an empty digest.
func TestVerifyFile_FailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, digest := range []string{"", "nope", strings.Repeat("A", 64)} {
		asset := Asset{Component: ComponentModel, SHA256: digest}
		if err := VerifyFile(path, asset); !errors.Is(err, ErrMissingChecksum) {
			t.Fatalf("VerifyFile with digest %q: err = %v, want ErrMissingChecksum", digest, err)
		}
	}
}

// TestVerifyFile_RejectsTruncatedFile guards the size half of verification: a
// short file with a matching prefix must not pass.
func TestVerifyFile_RejectsTruncatedFile(t *testing.T) {
	body := dlBody(4096)
	path := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(path, body[:len(body)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	asset := dlAssetFor("https://example.invalid/f.bin", body)
	err := VerifyFile(path, asset)
	if err == nil {
		t.Fatal("VerifyFile accepted a truncated file")
	}
	if errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("truncated file reported as a digest mismatch (%v); the size gate should fire first", err)
	}
}

// ---------------------------------------------------------------------------
// Resume
// ---------------------------------------------------------------------------

// TestDownload_ResumesFromPartialViaRange is the multi-gigabyte requirement in
// miniature: an existing partial is continued with "Range: bytes=N-" instead of
// being re-fetched, and the concatenated result verifies against the pin.
func TestDownload_ResumesFromPartialViaRange(t *testing.T) {
	body := dlBody(1024 * 1024)
	srv := newDLRangeServer(t, body, dlServeRange)
	d := newDLTestDownloader(srv.Server)
	d.FreeSpace = dlUnlimitedSpace

	asset := dlAssetFor(srv.URL+"/model.gguf", body)
	dst := filepath.Join(t.TempDir(), "model.gguf")

	offset := int64(len(body) * 2 / 5)
	if err := os.WriteFile(dst+PartialSuffix, body[:offset], 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := d.Download(context.Background(), asset, dst, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !res.Resumed {
		t.Error("Resumed = false, want true")
	}
	if res.Cached {
		t.Error("Cached = true on a resumed transfer, want false")
	}
	if !res.Verified {
		t.Error("Verified = false on a successful download")
	}
	if res.BytesWritten != int64(len(body))-offset {
		t.Errorf("BytesWritten = %d, want %d (only the tail may be transferred)", res.BytesWritten, int64(len(body))-offset)
	}
	if res.SizeBytes != int64(len(body)) {
		t.Errorf("SizeBytes = %d, want %d", res.SizeBytes, len(body))
	}

	headers := srv.rangeHeaders()
	if len(headers) != 1 {
		t.Fatalf("server saw %d request(s), want exactly 1", len(headers))
	}
	want := "bytes=" + strconv.FormatInt(offset, 10) + "-"
	if headers[0] != want {
		t.Errorf("Range header = %q, want %q", headers[0], want)
	}
	if codes := srv.statusCodes(); len(codes) != 1 || codes[0] != http.StatusPartialContent {
		t.Errorf("status codes = %v, want a single 206", codes)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading the promoted file: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("downloaded file differs from the source body (%d bytes got, %d want)", len(got), len(body))
	}
	if _, err := os.Stat(dst + PartialSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("partial %q was not promoted away", dst+PartialSuffix)
	}
	if err := VerifyFile(dst, asset); err != nil {
		t.Errorf("VerifyFile on the promoted file: %v", err)
	}
}

// TestDownload_CompletePartialPromotesWithoutNetwork covers a crash between the
// last byte and the rename: the partial already holds every byte, so it is
// verified and promoted with no request at all.
func TestDownload_CompletePartialPromotesWithoutNetwork(t *testing.T) {
	body := dlBody(64 * 1024)
	srv := newDLRangeServer(t, body, dlServeRange)
	d := newDLTestDownloader(srv.Server)
	d.FreeSpace = dlUnlimitedSpace

	asset := dlAssetFor(srv.URL+"/model.gguf", body)
	dst := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(dst+PartialSuffix, body, 0o600); err != nil {
		t.Fatal(err)
	}

	rec := &dlProgressRecorder{}
	res, err := d.Download(context.Background(), asset, dst, rec.fn)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if srv.hits() != 0 {
		t.Errorf("server saw %d request(s); a complete partial must not re-download", srv.hits())
	}
	if !res.Cached || !res.Verified || res.BytesWritten != 0 {
		t.Errorf("res = %+v, want Cached+Verified with BytesWritten 0", res)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("destination missing after promotion: %v", err)
	}
	calls := rec.snapshot()
	if len(calls) != 1 || calls[0] != [2]int64{int64(len(body)), int64(len(body))} {
		t.Errorf("progress calls = %v, want a single 100%% callback", calls)
	}
}

// TestDownload_OversizedPartialIsDiscarded covers a partial longer than the
// pin, which cannot be part of the artifact and must be thrown away rather
// than resumed.
func TestDownload_OversizedPartialIsDiscarded(t *testing.T) {
	body := dlBody(32 * 1024)
	srv := newDLRangeServer(t, body, dlServeRange)
	d := newDLTestDownloader(srv.Server)
	d.FreeSpace = dlUnlimitedSpace

	asset := dlAssetFor(srv.URL+"/model.gguf", body)
	dst := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(dst+PartialSuffix, append(dlBody(48*1024), body...), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := d.Download(context.Background(), asset, dst, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.Resumed {
		t.Error("Resumed = true for an oversized partial; it must restart from byte 0")
	}
	if res.BytesWritten != int64(len(body)) {
		t.Errorf("BytesWritten = %d, want the full %d", res.BytesWritten, len(body))
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Error("promoted file differs from the source body")
	}
}

// ---------------------------------------------------------------------------
// Range fallbacks
// ---------------------------------------------------------------------------

// TestDownload_RangeUnsupportedRestartsWithExplicitMessage is the documented
// fallback for a host that answers 200 to a ranged request. The partial must be
// discarded and the full body written — appending a complete object onto a
// partial would silently corrupt the artifact — and the restart must be
// explicit in both the log and the Result.
func TestDownload_RangeUnsupportedRestartsWithExplicitMessage(t *testing.T) {
	body := dlBody(256 * 1024)
	srv := newDLRangeServer(t, body, dlIgnoreRange)
	d := newDLTestDownloader(srv.Server)
	d.FreeSpace = dlUnlimitedSpace

	asset := dlAssetFor(srv.URL+"/model.gguf", body)
	dst := filepath.Join(t.TempDir(), "model.gguf")
	offset := int64(len(body) / 3)
	if err := os.WriteFile(dst+PartialSuffix, body[:offset], 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := d.Download(context.Background(), asset, dst, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !res.Restarted {
		t.Error("Restarted = false after the host ignored Range")
	}
	if res.Resumed {
		t.Error("Resumed = true after a restart, want false")
	}
	if res.RestartReason == "" {
		t.Error("RestartReason is empty; the fallback must carry an explicit message")
	}
	if !strings.Contains(res.RestartReason, "does not support resume") {
		t.Errorf("RestartReason = %q, want it to name the missing resume support", res.RestartReason)
	}
	if headers := srv.rangeHeaders(); len(headers) != 1 || headers[0] == "" {
		t.Errorf("Range headers = %v, want one non-empty Range attempt before the fallback", headers)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("file is %d bytes, want %d — the restart must not append to the partial", len(got), len(body))
	}
	if res.BytesWritten != int64(len(body)) {
		t.Errorf("BytesWritten = %d, want the full %d", res.BytesWritten, len(body))
	}
}

// TestDownload_RejectedResumeRestartsOnce covers the 416 path: the partial is
// irreconcilable with the server object, so it is discarded and the download
// restarts from byte 0 — exactly once, never in a loop.
func TestDownload_RejectedResumeRestartsOnce(t *testing.T) {
	body := dlBody(128 * 1024)
	srv := newDLRangeServer(t, body, dlRejectRange)
	d := newDLTestDownloader(srv.Server)
	d.FreeSpace = dlUnlimitedSpace

	asset := dlAssetFor(srv.URL+"/model.gguf", body)
	dst := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(dst+PartialSuffix, body[:len(body)/4], 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := d.Download(context.Background(), asset, dst, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !res.Restarted {
		t.Error("Restarted = false after a 416 rejection")
	}
	if !strings.Contains(res.RestartReason, "resume offset") {
		t.Errorf("RestartReason = %q, want it to name the rejected resume offset", res.RestartReason)
	}
	if got := srv.hits(); got != 2 {
		t.Errorf("server saw %d requests, want 2 (one rejected resume + one from scratch)", got)
	}
	if codes := srv.statusCodes(); len(codes) != 2 || codes[0] != http.StatusRequestedRangeNotSatisfiable || codes[1] != http.StatusOK {
		t.Errorf("status codes = %v, want [416 200]", codes)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Error("file differs from the source body after the restart")
	}
}

// TestDownload_RepeatedlyRejectedResumeGivesUp asserts the restart loop is
// bounded: a host that rejects every request with 416 yields an error after
// exactly maxTransferAttempts, not an infinite spin.
func TestDownload_RepeatedlyRejectedResumeGivesUp(t *testing.T) {
	body := dlBody(8 * 1024)
	srv := newDLRangeServer(t, body, dlAlwaysReject)
	d := newDLTestDownloader(srv.Server)
	d.FreeSpace = dlUnlimitedSpace

	dst := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(dst+PartialSuffix, body[:1024], 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := d.Download(context.Background(), dlAssetFor(srv.URL+"/model.gguf", body), dst, nil)
	if res != nil {
		t.Fatalf("Download returned a result alongside err = %v", err)
	}
	if err == nil {
		t.Fatal("Download succeeded against a server that rejects every range")
	}
	if got := srv.hits(); got != maxTransferAttempts {
		t.Errorf("server saw %d requests, want exactly %d (bounded restart loop)", got, maxTransferAttempts)
	}
	if !strings.Contains(err.Error(), "after 2 attempts") {
		t.Errorf("err = %v, want it to report the bounded attempt count", err)
	}
	if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("destination exists after a failed download")
	}
}

// ---------------------------------------------------------------------------
// Progress reporting
// ---------------------------------------------------------------------------

// TestDownload_ProgressThrottledAndAlwaysCompletes covers both halves of the
// progress contract: callbacks are throttled to roughly the configured
// interval, and a final unthrottled callback always reports 100% even when the
// throttle swallowed every intermediate update.
func TestDownload_ProgressThrottledAndAlwaysCompletes(t *testing.T) {
	body := dlBody(1024 * 1024)
	total := int64(len(body))

	t.Run("throttled to one start and one final callback", func(t *testing.T) {
		srv := newDLRangeServer(t, body, dlServeRange)
		d := newDLTestDownloader(srv.Server)
		d.FreeSpace = dlUnlimitedSpace
		d.ProgressInterval = time.Hour // no intermediate callback can pass the gate

		rec := &dlProgressRecorder{}
		if _, err := d.Download(context.Background(), dlAssetFor(srv.URL+"/m.gguf", body), filepath.Join(t.TempDir(), "m.gguf"), rec.fn); err != nil {
			t.Fatalf("Download: %v", err)
		}
		calls := rec.snapshot()
		if len(calls) != 2 {
			t.Fatalf("got %d progress callback(s) %v, want exactly 2 (start + final)", len(calls), calls)
		}
		if calls[0] != [2]int64{0, total} {
			t.Errorf("first callback = %v, want [0 %d]", calls[0], total)
		}
		if calls[1] != [2]int64{total, total} {
			t.Errorf("final callback = %v, want [%d %d] (100%%)", calls[1], total, total)
		}
	})

	t.Run("unthrottled reports many monotonic updates ending at 100%", func(t *testing.T) {
		srv := newDLRangeServer(t, body, dlServeRange)
		d := newDLTestDownloader(srv.Server)
		d.FreeSpace = dlUnlimitedSpace
		d.ProgressInterval = time.Nanosecond

		rec := &dlProgressRecorder{}
		if _, err := d.Download(context.Background(), dlAssetFor(srv.URL+"/m.gguf", body), filepath.Join(t.TempDir(), "m.gguf"), rec.fn); err != nil {
			t.Fatalf("Download: %v", err)
		}
		calls := rec.snapshot()
		if len(calls) <= 5 {
			t.Fatalf("got %d progress callback(s), want many more than the throttled case", len(calls))
		}
		for i, c := range calls {
			if c[1] != total {
				t.Fatalf("callback %d reported total %d, want %d", i, c[1], total)
			}
			if c[0] < 0 || c[0] > total {
				t.Fatalf("callback %d reported done %d outside [0,%d]", i, c[0], total)
			}
			if i > 0 && c[0] < calls[i-1][0] {
				t.Fatalf("callback %d went backwards: %d after %d", i, c[0], calls[i-1][0])
			}
		}
		if last := calls[len(calls)-1]; last != [2]int64{total, total} {
			t.Errorf("final callback = %v, want [%d %d] (100%%)", last, total, total)
		}
	})

	t.Run("resumed transfer starts at the resume offset", func(t *testing.T) {
		srv := newDLRangeServer(t, body, dlServeRange)
		d := newDLTestDownloader(srv.Server)
		d.FreeSpace = dlUnlimitedSpace
		d.ProgressInterval = time.Hour

		dst := filepath.Join(t.TempDir(), "m.gguf")
		offset := total / 2
		if err := os.WriteFile(dst+PartialSuffix, body[:offset], 0o600); err != nil {
			t.Fatal(err)
		}
		rec := &dlProgressRecorder{}
		if _, err := d.Download(context.Background(), dlAssetFor(srv.URL+"/m.gguf", body), dst, rec.fn); err != nil {
			t.Fatalf("Download: %v", err)
		}
		calls := rec.snapshot()
		if len(calls) != 2 {
			t.Fatalf("got %d callbacks %v, want 2", len(calls), calls)
		}
		if calls[0] != [2]int64{offset, total} {
			t.Errorf("first callback = %v, want [%d %d] so the UI shows the resume point", calls[0], offset, total)
		}
		if calls[1] != [2]int64{total, total} {
			t.Errorf("final callback = %v, want [%d %d]", calls[1], total, total)
		}
	})
}

// ---------------------------------------------------------------------------
// Disk guard
// ---------------------------------------------------------------------------

// TestDownload_DiskGuardRefusesRealSizedModel proves the guard is sized to the
// artifact rather than to a fixed constant: the real 6.71 GiB PQ2_0 pin is
// refused on a volume that cannot hold it plus headroom, before any request.
func TestDownload_DiskGuardRefusesRealSizedModel(t *testing.T) {
	model, ok := ModelAsset(PackingPQ2_0)
	if !ok {
		t.Fatal("PQ2_0 model asset is not pinned")
	}
	if model.SizeBytes != 7206168928 {
		t.Fatalf("PQ2_0 pinned size = %d, want 7206168928 (6.71 GiB)", model.SizeBytes)
	}

	body := dlBody(1024)
	srv := newDLRangeServer(t, body, dlServeRange)
	// Keep the real size and digest, but aim at the counting test server so a
	// guard failure is provable as "no request was made".
	asset := model
	asset.URL = srv.URL + "/never-fetched.gguf"

	cases := []struct {
		name string
		free int64
		want bool // true = must refuse
	}{
		{"nothing free", 0, true},
		{"200 MiB free (the tool-manager guard would have passed)", 200 << 20, true},
		{"artifact size exactly, no headroom", model.SizeBytes, true},
		{"6.7 GiB free", 7200 * 1024 * 1024, true},
		{"artifact plus headroom", model.SizeBytes + DefaultDiskHeadroom, false},
		{"plenty free", 64 << 30, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := srv.hits()
			free := tc.free
			d := newDLTestDownloader(srv.Server)
			d.FreeSpace = func(string) (int64, error) { return free, nil }

			dir := t.TempDir()
			dst := filepath.Join(dir, "model.gguf")
			_, err := d.Download(context.Background(), asset, dst, nil)
			refused := errors.Is(err, ErrInsufficientDisk)
			if refused != tc.want {
				t.Fatalf("refused = %v (err = %v), want %v for %d free bytes (need %d)",
					refused, err, tc.want, free, RequiredFreeBytes(model.SizeBytes))
			}
			if tc.want {
				if got := srv.hits() - before; got != 0 {
					t.Errorf("the disk guard refused but %d request(s) were still made", got)
				}
				entries, readErr := os.ReadDir(dir)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if len(entries) != 0 {
					t.Errorf("the disk guard refused but %d file(s) were created", len(entries))
				}
				if !strings.Contains(err.Error(), "headroom") {
					t.Errorf("refusal message %q does not explain the headroom requirement", err.Error())
				}
				return
			}
			// Not refused: the guard must let the transfer start, so the request
			// has to reach the server. (The transfer itself then fails on the
			// size/digest gate — the point here is that the GUARD passed.)
			if got := srv.hits() - before; got != 1 {
				t.Errorf("the guard passed but %d request(s) reached the server, want 1", got)
			}
			if errors.Is(err, ErrInsufficientDisk) {
				t.Errorf("err = %v, want the guard to pass with %d free bytes", err, free)
			}
		})
	}
}

// TestDownload_DiskGuardScalesWithArtifactSize is the direct contrast with the
// tool-manager's fixed 200 MiB guard: a 300 MiB artifact on a 250 MiB volume
// must be refused, and the same artifact on a roomy volume must proceed.
func TestDownload_DiskGuardScalesWithArtifactSize(t *testing.T) {
	body := dlBody(2048)
	srv := newDLRangeServer(t, body, dlServeRange)

	// An asset whose pinned size is 300 MiB while the served body is tiny: the
	// guard runs before the transfer, so the refusal is decided by the pin.
	bigAsset := dlAssetFor(srv.URL+"/m.gguf", body)
	bigAsset.SizeBytes = 300 << 20

	t.Run("300 MiB artifact on a 250 MiB volume is refused", func(t *testing.T) {
		d := newDLTestDownloader(srv.Server)
		d.DiskHeadroom = 1 // isolate the artifact size as the deciding factor
		d.FreeSpace = func(string) (int64, error) { return 250 << 20, nil }
		_, err := d.Download(context.Background(), bigAsset, filepath.Join(t.TempDir(), "m.gguf"), nil)
		if !errors.Is(err, ErrInsufficientDisk) {
			t.Fatalf("err = %v, want ErrInsufficientDisk", err)
		}
	})

	t.Run("a real download proceeds when the volume has room", func(t *testing.T) {
		d := newDLTestDownloader(srv.Server)
		d.FreeSpace = func(string) (int64, error) { return 1 << 40, nil }
		dst := filepath.Join(t.TempDir(), "m.gguf")
		if _, err := d.Download(context.Background(), dlAssetFor(srv.URL+"/m.gguf", body), dst, nil); err != nil {
			t.Fatalf("Download: %v", err)
		}
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, body) {
			t.Error("file differs from the served body")
		}
	})
}

// TestDownload_DiskGuardMeasurementFailureIsBestEffort documents the chosen
// posture: an unmeasurable volume logs and proceeds rather than making the
// feature unusable, because ENOSPC still fails safely and the digest gate still
// refuses unverified bytes.
func TestDownload_DiskGuardMeasurementFailureIsBestEffort(t *testing.T) {
	body := dlBody(4096)
	srv := newDLRangeServer(t, body, dlServeRange)
	d := newDLTestDownloader(srv.Server)
	d.FreeSpace = func(string) (int64, error) { return 0, errors.New("statfs: not supported") }

	dst := filepath.Join(t.TempDir(), "m.gguf")
	if _, err := d.Download(context.Background(), dlAssetFor(srv.URL+"/m.gguf", body), dst, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if err := VerifyFile(dst, dlAssetFor(srv.URL+"/m.gguf", body)); err != nil {
		t.Errorf("VerifyFile: %v", err)
	}
}

// TestRequiredFreeBytes pins the whole-set guard arithmetic install uses.
func TestRequiredFreeBytes(t *testing.T) {
	set, err := ArtifactSet(PlatformWindowsAMD64, BackendCUDA124, PackingPQ2_0)
	if err != nil {
		t.Fatal(err)
	}
	want := TotalBytes(set) + DefaultDiskHeadroom
	if got := RequiredFreeBytes(TotalBytes(set)); got != want {
		t.Errorf("RequiredFreeBytes(%d) = %d, want %d", TotalBytes(set), got, want)
	}
	// The Windows CUDA PQ2_0 set is the largest install, and its disk
	// requirement must be dominated by the artifacts themselves — a number no
	// fixed constant (the tool-manager's guard is 200 MiB) could produce.
	if TotalBytes(set) < 8_000_000_000 {
		t.Errorf("TotalBytes(windows cuda-12.4 PQ2_0 set) = %d, want > 8 GB", TotalBytes(set))
	}
	if RequiredFreeBytes(TotalBytes(set)) < 10_000_000_000 {
		t.Errorf("RequiredFreeBytes for the largest set = %d, want > 10 GB", RequiredFreeBytes(TotalBytes(set)))
	}
	if got, want := RequiredFreeBytes(0), DefaultDiskHeadroom; got != want {
		t.Errorf("RequiredFreeBytes(0) = %d, want the bare headroom %d", got, want)
	}
}

// ---------------------------------------------------------------------------
// Cancellation, oversize, cache
// ---------------------------------------------------------------------------

// TestDownload_CancellationKeepsPartialForResume asserts cancellation is
// honoured through the request context at every stage and that the partial
// survives, so a cancelled multi-gigabyte download is resumable rather than
// wasted.
func TestDownload_CancellationKeepsPartialForResume(t *testing.T) {
	body := dlBody(4 * 1024 * 1024)
	srv := newDLRangeServer(t, body, dlServeRange)
	// Stream slowly so the cancel provably lands mid-transfer instead of
	// racing a loopback write that finishes before the first callback.
	srv.chunkSize = 32 * 1024
	srv.writeDelay = time.Millisecond

	d := newDLTestDownloader(srv.Server)
	d.FreeSpace = dlUnlimitedSpace
	d.ProgressInterval = time.Nanosecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var once sync.Once
	rec := &dlProgressRecorder{}
	total := int64(len(body))
	progress := func(done, got int64) {
		rec.fn(done, got)
		if done > 0 && done < total {
			once.Do(cancel)
		}
	}

	dst := filepath.Join(t.TempDir(), "m.gguf")
	_, err := d.Download(ctx, dlAssetFor(srv.URL+"/m.gguf", body), dst, progress)
	if err == nil {
		t.Fatal("Download succeeded on a cancelled context")
	}
	if !errors.Is(err, ErrIncompleteTransfer) {
		t.Errorf("err = %v, want it to wrap ErrIncompleteTransfer", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to wrap context.Canceled so callers can tell a cancel from a network fault", err)
	}
	if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("destination exists after cancellation; only a verified file may occupy it")
	}
	fi, statErr := os.Stat(dst + PartialSuffix)
	if statErr != nil {
		t.Fatalf("partial was not kept for resume: %v", statErr)
	}
	if fi.Size() <= 0 || fi.Size() >= total {
		t.Errorf("partial is %d bytes, want a strictly partial prefix of %d", fi.Size(), total)
	}
	if !strings.Contains(err.Error(), "resume") {
		t.Errorf("error %q does not tell the user the partial is resumable", err.Error())
	}
}

// TestDownload_CancelledContextBeforeAnyIO covers cancellation that happens
// before the transfer starts.
func TestDownload_CancelledContextBeforeAnyIO(t *testing.T) {
	body := dlBody(1024)
	srv := newDLRangeServer(t, body, dlServeRange)
	d := newDLTestDownloader(srv.Server)
	d.FreeSpace = dlUnlimitedSpace

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.Download(ctx, dlAssetFor(srv.URL+"/m.gguf", body), filepath.Join(t.TempDir(), "m.gguf"), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := srv.hits(); got != 0 {
		t.Errorf("server saw %d request(s) on a pre-cancelled context", got)
	}
}

// TestDownload_OversizedResponseIsRefused covers a server (or a captive portal)
// that offers more bytes than the pin allows. There is no fixed byte ceiling in
// this downloader — the pin IS the ceiling.
func TestDownload_OversizedResponseIsRefused(t *testing.T) {
	body := dlBody(64 * 1024)
	srv := newDLRangeServer(t, body, dlServeRange)
	d := newDLTestDownloader(srv.Server)
	d.FreeSpace = dlUnlimitedSpace

	asset := dlAssetFor(srv.URL+"/m.gguf", body)
	asset.SizeBytes = int64(len(body) / 2) // pin smaller than what the server sends

	dst := filepath.Join(t.TempDir(), "m.gguf")
	_, err := d.Download(context.Background(), asset, dst, nil)
	if !errors.Is(err, ErrArtifactTooLarge) {
		t.Fatalf("err = %v, want ErrArtifactTooLarge", err)
	}
	if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("destination exists after an oversized response")
	}
	if _, statErr := os.Stat(dst + PartialSuffix); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("partial survived an oversized response; it cannot be resumed")
	}
}

// TestDownload_VerifiedCacheHitSkipsNetwork covers the re-entry path: an
// already-installed artifact costs no bandwidth and still reports 100%.
func TestDownload_VerifiedCacheHitSkipsNetwork(t *testing.T) {
	body := dlBody(16 * 1024)
	srv := newDLRangeServer(t, body, dlServeRange)
	d := newDLTestDownloader(srv.Server)
	d.FreeSpace = dlUnlimitedSpace

	asset := dlAssetFor(srv.URL+"/m.gguf", body)
	dst := filepath.Join(t.TempDir(), "m.gguf")
	if err := os.WriteFile(dst, body, 0o600); err != nil {
		t.Fatal(err)
	}

	rec := &dlProgressRecorder{}
	res, err := d.Download(context.Background(), asset, dst, rec.fn)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if srv.hits() != 0 {
		t.Errorf("server saw %d request(s) on a verified cache hit", srv.hits())
	}
	if !res.Cached || !res.Verified || res.BytesWritten != 0 {
		t.Errorf("res = %+v, want a cached+verified result with no bytes written", res)
	}
	if res.SHA256 != asset.SHA256 {
		t.Errorf("SHA256 = %q, want the pin %q", res.SHA256, asset.SHA256)
	}
	calls := rec.snapshot()
	if len(calls) != 1 || calls[0] != [2]int64{int64(len(body)), int64(len(body))} {
		t.Errorf("progress calls = %v, want a single 100%% callback", calls)
	}
}

// TestDownload_UnverifiedCacheIsNeverTrusted covers a destination file that is
// present but wrong: it must be removed and re-downloaded, not accepted.
func TestDownload_UnverifiedCacheIsNeverTrusted(t *testing.T) {
	body := dlBody(8 * 1024)
	srv := newDLRangeServer(t, body, dlServeRange)
	d := newDLTestDownloader(srv.Server)
	d.FreeSpace = dlUnlimitedSpace

	asset := dlAssetFor(srv.URL+"/m.gguf", body)
	dst := filepath.Join(t.TempDir(), "m.gguf")
	if err := os.WriteFile(dst, []byte("corrupted contents of the same length? no."), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := d.Download(context.Background(), asset, dst, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.Cached {
		t.Error("Cached = true for a file that failed verification")
	}
	if srv.hits() != 1 {
		t.Errorf("server saw %d request(s), want 1 re-download", srv.hits())
	}
	if err := VerifyFile(dst, asset); err != nil {
		t.Errorf("VerifyFile after re-download: %v", err)
	}
}

// TestDownload_HTTPErrorIsSurfaced covers a non-2xx, non-206/416 response.
func TestDownload_HTTPErrorIsSurfaced(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "gone", http.StatusGone)
	}))
	t.Cleanup(srv.Close)

	d := NewDownloader(srv.Client(), dlDiscardLogger())
	d.FreeSpace = dlUnlimitedSpace
	asset := dlAssetFor(srv.URL+"/m.gguf", dlBody(64))

	dst := filepath.Join(t.TempDir(), "m.gguf")
	_, err := d.Download(context.Background(), asset, dst, nil)
	if err == nil {
		t.Fatal("Download succeeded against a 410 response")
	}
	if !strings.Contains(err.Error(), "410") {
		t.Errorf("err = %v, want it to name the HTTP status", err)
	}
	if _, statErr := os.Stat(dst + PartialSuffix); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a failed request left a partial behind: %v", statErr)
	}
}

// TestDownload_EmptyDestinationRefused is a small API-contract guard.
func TestDownload_EmptyDestinationRefused(t *testing.T) {
	d := NewDownloader(nil, dlDiscardLogger())
	d.FreeSpace = dlUnlimitedSpace
	asset := dlAssetFor("https://example.invalid/m.gguf", dlBody(16))
	if _, err := d.Download(context.Background(), asset, "", nil); err == nil {
		t.Fatal("Download accepted an empty destination path")
	}
}

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

func TestParseContentRange(t *testing.T) {
	cases := []struct {
		in           string
		wantStart    int64
		wantComplete int64
		wantOK       bool
	}{
		{"bytes 0-1023/629246976", 0, 629246976, true},
		{"bytes 629246000-629246975/629246976", 629246000, 629246976, true},
		{"bytes 18785000-18785840/18785841", 18785000, 18785841, true},
		{"bytes */18785841", 0, 0, false}, // no start → unusable for resume
		{"bytes 0-1023/*", 0, 0, true},
		{"  bytes 0-9/10  ", 0, 10, true},
		{"", 0, 0, false},
		{"0-1023/629246976", 0, 0, false},
		{"bytes abc-1023/629246976", 0, 0, false},
		{"bytes 0-1023", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			start, complete, ok := parseContentRange(tc.in)
			if ok != tc.wantOK || start != tc.wantStart || complete != tc.wantComplete {
				t.Fatalf("parseContentRange(%q) = (%d, %d, %v), want (%d, %d, %v)",
					tc.in, start, complete, ok, tc.wantStart, tc.wantComplete, tc.wantOK)
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{200 << 20, "200.0 MiB"},
		{7206168928, "6.7 GiB"},
		{2 << 30, "2.0 GiB"},
	}
	for _, tc := range cases {
		if got := formatBytes(tc.in); got != tc.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewDownloaderDefaults(t *testing.T) {
	d := NewDownloader(nil, nil)
	if d.ProgressInterval != DefaultProgressInterval {
		t.Errorf("ProgressInterval = %v, want %v", d.ProgressInterval, DefaultProgressInterval)
	}
	if d.DiskHeadroom != DefaultDiskHeadroom {
		t.Errorf("DiskHeadroom = %d, want %d", d.DiskHeadroom, DefaultDiskHeadroom)
	}
	if d.progressInterval() != DefaultProgressInterval {
		t.Errorf("progressInterval() = %v, want the default", d.progressInterval())
	}
	if d.headroom() != DefaultDiskHeadroom {
		t.Errorf("headroom() = %d, want the default", d.headroom())
	}
	// The zero value must also fall back to the defaults rather than to 0.
	var zero Downloader
	if zero.progressInterval() != DefaultProgressInterval || zero.headroom() != DefaultDiskHeadroom {
		t.Error("the zero-value Downloader did not fall back to the documented defaults")
	}
	if zero.client() == nil {
		t.Error("the zero-value Downloader has no HTTP client")
	}
	if zero.logger() == nil {
		t.Error("the zero-value Downloader has no logger")
	}
}

// TestDefaultTransferClientHasNoOverallTimeout locks in the deliberate
// divergence from toolmanager: a multi-gigabyte transfer must not be bounded by
// a whole-request timeout, only by liveness timeouts and ctx.
func TestDefaultTransferClientHasNoOverallTimeout(t *testing.T) {
	client := defaultTransferClient()
	if client.Timeout != 0 {
		t.Errorf("Client.Timeout = %v, want 0 (a 6.7 GiB download may legitimately take hours)", client.Timeout)
	}
	if defaultTransferClient() != client {
		t.Error("defaultTransferClient is not memoized; each call would leak a transport")
	}
}

// TestPlatformFreeSpace exercises the real (non-stubbed) guard on this machine.
func TestPlatformFreeSpace(t *testing.T) {
	free, err := platformFreeSpace(t.TempDir())
	if err != nil {
		t.Fatalf("platformFreeSpace: %v", err)
	}
	if free <= 0 {
		t.Fatalf("platformFreeSpace = %d, want a positive byte count", free)
	}
	if _, err := platformFreeSpace(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("platformFreeSpace accepted a nonexistent path")
	}
}

// TestDownload_CleanlyShortBodyKeepsThePartialForResume covers a body that ends
// CLEANLY short of the pin — no transport error, just fewer bytes than the pin
// expects, which is what a proxy that strips Content-Length looks like from here.
// The bytes are unverified and must not be promoted, but they are also a valid
// resume prefix: falling through to the digest gate reported ErrChecksumMismatch
// and DELETED a multi-gigabyte partial that could simply have been continued.
func TestDownload_CleanlyShortBodyKeepsThePartialForResume(t *testing.T) {
	body := dlBody(64 * 1024)
	srv := newDLRangeServer(t, body, dlTruncateBody)
	d := newDLTestDownloader(srv.Server)
	d.FreeSpace = dlUnlimitedSpace

	dst := filepath.Join(t.TempDir(), "m.gguf")
	_, err := d.Download(context.Background(), dlAssetFor(srv.URL+"/m.gguf", body), dst, nil)
	if !errors.Is(err, ErrIncompleteTransfer) {
		t.Fatalf("err = %v, want ErrIncompleteTransfer", err)
	}
	if errors.Is(err, ErrChecksumMismatch) {
		t.Error("a cleanly short body was reported as a checksum mismatch")
	}

	partial := dst + PartialSuffix
	info, statErr := os.Stat(partial)
	if statErr != nil {
		t.Fatalf("the resumable partial was deleted: %v", statErr)
	}
	if want := int64(len(body) / 2); info.Size() != want {
		t.Errorf("the partial holds %d bytes, want the %d the body carried", info.Size(), want)
	}
	if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("unverified bytes reached the destination path")
	}
}

// TestDownload_RefusesToResumeThroughASymlinkedPartial covers the pre-planted
// partial: `os.OpenFile(partial, O_RDWR|O_CREATE, …)` FOLLOWS a symlink, so the
// truncate and every appended byte would land on the link's target — pinned
// artifact bytes written at an attacker-chosen offset into an arbitrary same-user
// path. The digest gate would still refuse to promote anything, but no
// verification can undo a write to the wrong file, so the link is discarded
// instead of written through.
func TestDownload_RefusesToResumeThroughASymlinkedPartial(t *testing.T) {
	body := dlBody(16 * 1024)
	srv := newDLRangeServer(t, body, dlServeRange)
	d := newDLTestDownloader(srv.Server)
	d.FreeSpace = dlUnlimitedSpace

	dir := t.TempDir()
	dst := filepath.Join(dir, "m.gguf")
	partial := dst + PartialSuffix

	target := filepath.Join(dir, "an-unrelated-file.txt")
	const untouched = "bytes this download has no business writing"
	if err := os.WriteFile(target, []byte(untouched), 0o600); err != nil {
		t.Fatalf("staging the symlink target: %v", err)
	}
	if err := os.Symlink(target, partial); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	res, err := d.Download(context.Background(), dlAssetFor(srv.URL+"/m.gguf", body), dst, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !res.Verified || res.SizeBytes != int64(len(body)) {
		t.Errorf("result = %+v, want a verified full download", res)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading the symlink target: %v", err)
	}
	if string(got) != untouched {
		t.Errorf("the symlink target was written through: %q", got)
	}
	// The link is gone and the artifact was promoted to a regular file.
	if info, err := os.Lstat(partial); err == nil && info.Mode()&os.ModeSymlink != 0 {
		t.Error("the symlinked partial survived the download")
	}
	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("the destination is missing: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("the destination is a %s, want a regular file", info.Mode())
	}
}
