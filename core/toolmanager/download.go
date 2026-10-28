package toolmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/v0lka/sp4rk/safeio"
)

// DownloadResult reports the outcome of a tool archive download.
type DownloadResult struct {
	ToolName     string // tool name from the spec
	ArchivePath  string // absolute path to the downloaded archive in cache
	Downloaded   bool   // false if cached and checksum matched (no re-download)
	ArchiveBytes int64  // size of the downloaded archive
}

// progressWriter flushes progress at ~100ms intervals so fast downloads
// don't flood the event channel.
type progressWriter struct {
	writer    io.Writer
	progress  func(done, total int64)
	total     int64
	written   int64
	lastFlush time.Time
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.writer.Write(p)
	pw.written += int64(n)
	if now := time.Now(); now.Sub(pw.lastFlush) >= 100*time.Millisecond {
		pw.progress(pw.written, pw.total)
		pw.lastFlush = now
	}
	return n, err
}

// DownloadMode tells Download whether it may reach the network.
type DownloadMode int

const (
	// Network allowed; the cache is consulted first — a verified hit skips
	// the request entirely.
	DownloadOnline DownloadMode = iota
	// Local disk only: a SHA256-verified cached archive or a fast
	// ErrCacheUnavailable failure — never any network I/O.
	DownloadCacheOnly
)

// ErrCacheUnavailable: DownloadCacheOnly found no verifiable cached archive.
// errors.Is it to tell "offline and not cached" from transient failures.
var ErrCacheUnavailable = errors.New("cached archive unavailable")

// Downloader fetches tool archives with checksum verification and cache
// support; progress may be nil. An interface so tests can substitute a mock.
type Downloader interface {
	Download(ctx context.Context, tool ToolSpec, cacheDir string, mode DownloadMode, progress func(int64, int64)) (*DownloadResult, error)
}

// HTTPDownloader is the production Downloader: public URLs + SHA256 checks.
type HTTPDownloader struct {
	Client *http.Client
}

// NewHTTPDownloader; a nil client becomes a 5-minute-timeout client.
func NewHTTPDownloader(client *http.Client) *HTTPDownloader {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &HTTPDownloader{Client: client}
}

// Download fetches tool.URLs[platform] into cacheDir/<ArchiveName>, returning
// a verified cache hit when one exists. Mode and progress semantics are
// documented on DownloadMode; a CacheOnly miss wraps ErrCacheUnavailable.
func (d *HTTPDownloader) Download(ctx context.Context, tool ToolSpec, cacheDir string, mode DownloadMode, progress func(int64, int64)) (*DownloadResult, error) {
	platform := Platform()
	url, ok := tool.URLs[platform]
	if !ok || url == "" {
		return nil, fmt.Errorf("tool %q: no download URL for platform %q", tool.Name, platform)
	}

	archivePath := filepath.Join(cacheDir, tool.ArchiveName)

	// The cache check is Lstat-based and refuses non-regular entries: a
	// dangling symlink planted at this deterministic path once reported
	// "absent" under os.Stat and a bare create wrote through the link; any
	// non-regular plant is removed (never its target) and re-downloaded.
	if info, statErr := os.Lstat(archivePath); statErr == nil {
		if info.Mode().IsRegular() && d.verifyChecksum(archivePath, tool, platform) {
			return &DownloadResult{
				ToolName:     tool.Name,
				ArchivePath:  archivePath,
				Downloaded:   false,
				ArchiveBytes: 0,
			}, nil
		}
		_ = os.Remove(archivePath)
	}

	// CacheOnly stops here: nothing verifiable on disk, network off-limits —
	// an unverified cache is never trusted.
	if mode == DownloadCacheOnly {
		return nil, fmt.Errorf("tool %q: %w", tool.Name, ErrCacheUnavailable)
	}

	// Download.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("tool %q: creating request: %w", tool.Name, err)
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tool %q: download failed: %w", tool.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tool %q: HTTP %d from %s", tool.Name, resp.StatusCode, url)
	}

	// Download into a uniquely named temp and rename into place: the fixed
	// archivePath is pre-plantable with a dangling symlink a bare os.Create
	// would follow; the rename replaces a plant instead of writing through it.
	tmp, err := os.CreateTemp(cacheDir, tool.ArchiveName+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("tool %q: creating archive temp file: %w", tool.Name, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	var writer io.Writer = tmp
	if resp.ContentLength > 0 && progress != nil {
		pw := &progressWriter{
			writer:    tmp,
			progress:  progress,
			total:     resp.ContentLength,
			lastFlush: time.Now(),
		}
		writer = pw
		progress(0, resp.ContentLength)
	}
	n, err := io.Copy(writer, io.LimitReader(resp.Body, maxDownloadBytes+1))
	// Final progress callback to guarantee 100% is reported.
	if resp.ContentLength > 0 && progress != nil {
		progress(n, resp.ContentLength)
	}
	if err != nil {
		return nil, fmt.Errorf("tool %q: writing archive: %w", tool.Name, err)
	}
	if n > maxDownloadBytes {
		return nil, fmt.Errorf("tool %q: download exceeds max size %d bytes", tool.Name, maxDownloadBytes)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("tool %q: writing archive: %w", tool.Name, err)
	}

	// Verify checksum before the download is published under its final name.
	if !d.verifyChecksum(tmpName, tool, platform) {
		return nil, fmt.Errorf("tool %q: checksum verification failed after download", tool.Name)
	}
	if err := os.Rename(tmpName, archivePath); err != nil {
		return nil, fmt.Errorf("tool %q: publishing archive: %w", tool.Name, err)
	}

	return &DownloadResult{
		ToolName:     tool.Name,
		ArchivePath:  archivePath,
		Downloaded:   true,
		ArchiveBytes: n,
	}, nil
}

// verifyChecksum compares the file's SHA256 with the spec checksum; a
// missing platform registration fails closed (see ToolSpec.Checksums).
func (d *HTTPDownloader) verifyChecksum(path string, tool ToolSpec, platform string) bool {
	expected := tool.Checksums[platform]
	if expected == "" {
		return false
	}

	f, err := safeio.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	actual := hex.EncodeToString(h.Sum(nil))
	return actual == expected
}

// DownloadFunc is a function adapter that implements Downloader.
type DownloadFunc func(ctx context.Context, tool ToolSpec, cacheDir string, mode DownloadMode, progress func(int64, int64)) (*DownloadResult, error)

// Download implements Downloader.
func (f DownloadFunc) Download(ctx context.Context, tool ToolSpec, cacheDir string, mode DownloadMode, progress func(int64, int64)) (*DownloadResult, error) {
	return f(ctx, tool, cacheDir, mode, progress)
}

// Compile-time check that DownloadFunc implements Downloader.
var _ Downloader = DownloadFunc(nil)
