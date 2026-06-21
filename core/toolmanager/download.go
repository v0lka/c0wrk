package toolmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// DownloadResult reports the outcome of a tool archive download.
type DownloadResult struct {
	ToolName     string // tool name from the spec
	ArchivePath  string // absolute path to the downloaded archive in cache
	Downloaded   bool   // false if cached and checksum matched (no re-download)
	ArchiveBytes int64  // size of the downloaded archive
}

// progressWriter wraps an io.Writer and calls a progress callback at ~100ms
// intervals to avoid flooding the event channel during fast downloads.
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

// Downloader handles HTTP downloads for tool archives with checksum verification
// and cache-resume support. The progress callback receives (bytesDone, bytesTotal)
// during the download; it may be nil if progress reporting is not needed.
// The interface exists so tests can substitute a mock implementation.
type Downloader interface {
	Download(ctx context.Context, tool ToolSpec, cacheDir string, progress func(int64, int64)) (*DownloadResult, error)
}

// HTTPDownloader is the production Downloader that fetches archives from
// public URLs and verifies their SHA256 checksums.
type HTTPDownloader struct {
	Client *http.Client
}

// NewHTTPDownloader creates an HTTPDownloader with the given client. If client
// is nil, a client with a 5-minute timeout is created to prevent hung startup.
func NewHTTPDownloader(client *http.Client) *HTTPDownloader {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &HTTPDownloader{Client: client}
}

// Download fetches the archive for the given tool and platform. It writes the
// archive to cacheDir/<ArchiveName>. If the file already exists and its
// checksum matches, the download is skipped (cache hit).
// The progress callback receives (bytesDone, bytesTotal) with throttle at
// ~100ms intervals; may be nil.
func (d *HTTPDownloader) Download(ctx context.Context, tool ToolSpec, cacheDir string, progress func(int64, int64)) (*DownloadResult, error) {
	platform := Platform()
	url, ok := tool.URLs[platform]
	if !ok || url == "" {
		return nil, fmt.Errorf("tool %q: no download URL for platform %q", tool.Name, platform)
	}

	archivePath := filepath.Join(cacheDir, tool.ArchiveName)

	// Check cache: if the file exists and checksum matches, skip download.
	if _, statErr := os.Stat(archivePath); statErr == nil {
		if d.verifyChecksum(archivePath, tool, platform) {
			return &DownloadResult{
				ToolName:     tool.Name,
				ArchivePath:  archivePath,
				Downloaded:   false,
				ArchiveBytes: 0,
			}, nil
		}
		// Checksum mismatch — delete and re-download.
		_ = os.Remove(archivePath)
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

	f, err := os.Create(archivePath)
	if err != nil {
		return nil, fmt.Errorf("tool %q: creating archive file: %w", tool.Name, err)
	}
	defer func() { _ = f.Close() }()

	// Wire up progress tracking if Content-Length is known.
	var writer io.Writer = f
	if resp.ContentLength > 0 && progress != nil {
		pw := &progressWriter{
			writer:    f,
			progress:  progress,
			total:     resp.ContentLength,
			lastFlush: time.Now(),
		}
		writer = pw
		progress(0, resp.ContentLength)
	}
	n, err := io.Copy(writer, resp.Body)
	// Final progress callback to guarantee 100% is reported.
	if resp.ContentLength > 0 && progress != nil {
		progress(n, resp.ContentLength)
	}
	if err != nil {
		_ = os.Remove(archivePath)
		return nil, fmt.Errorf("tool %q: writing archive: %w", tool.Name, err)
	}

	// Verify checksum after download.
	if !d.verifyChecksum(archivePath, tool, platform) {
		_ = os.Remove(archivePath)
		return nil, fmt.Errorf("tool %q: checksum verification failed after download", tool.Name)
	}

	return &DownloadResult{
		ToolName:     tool.Name,
		ArchivePath:  archivePath,
		Downloaded:   true,
		ArchiveBytes: n,
	}, nil
}

// verifyChecksum reads the file at path and compares its SHA256 against the
// expected checksum in the tool spec. If no checksum is registered for the
// platform, verification is skipped (returns true).
func (d *HTTPDownloader) verifyChecksum(path string, tool ToolSpec, platform string) bool {
	expected := tool.Checksums[platform]
	if expected == "" {
		return true // no checksum registered — skip verification
	}

	f, err := os.Open(path)
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
type DownloadFunc func(ctx context.Context, tool ToolSpec, cacheDir string, progress func(int64, int64)) (*DownloadResult, error)

// Download implements Downloader.
func (f DownloadFunc) Download(ctx context.Context, tool ToolSpec, cacheDir string, progress func(int64, int64)) (*DownloadResult, error) {
	return f(ctx, tool, cacheDir, progress)
}

// Compile-time check that DownloadFunc implements Downloader.
var _ Downloader = DownloadFunc(nil)
