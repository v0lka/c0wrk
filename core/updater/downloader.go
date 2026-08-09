package updater

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// errNonHTTPS is returned when a download URL is not HTTPS-only. The update
// channel must be end-to-end encrypted: a compromised API response or a MITM
// proxy must not be able to downgrade the archive/checksum fetch to plain HTTP.
// (The fail-closed SHA256 verification remains the real integrity gate; this
// is defense-in-depth that matches the SECURITY.md claim.)
var errNonHTTPS = errors.New("update download URL must use HTTPS")

// requireHTTPS rejects any URL whose scheme is not https, with a single
// exception for loopback addresses (127.0.0.1, ::1, localhost). Loopback
// traffic never traverses the network, so the downgrade/MITM threat the HTTPS
// rule defends against does not apply — and this keeps the package testable
// with httptest.NewServer. The fail-closed SHA256 verification remains the
// real integrity gate; this check is defense-in-depth matching the SECURITY.md
// claim of HTTPS-only asset/checksum URLs.
func requireHTTPS(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url %q: %w", rawURL, err)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && isLoopbackHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%w: %q", errNonHTTPS, rawURL)
}

// isLoopbackHost reports whether host is a loopback address or the "localhost"
// name (which always resolves to loopback per RFC 6761).
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// progressInterval is the minimum interval between progress callbacks. It keeps
// the event channel from being flooded during fast downloads (mirrors
// core/toolmanager/download.go).
const progressInterval = 100 * time.Millisecond

// DownloadResult reports the outcome of a staged download.
type DownloadResult struct {
	AssetName   string // name of the downloaded asset (basename of assetURL)
	ArchivePath string // absolute path to the verified archive in staging
	SumsPath    string // absolute path to the SHA256SUMS file in staging
	Bytes       int64  // size of the downloaded archive in bytes
}

// Downloader fetches a release asset and its SHA256SUMS file into a staging
// directory and verifies the archive's integrity before declaring success.
// Every failure path is fail-closed: a checksum error removes the archive so a
// partial/corrupt artifact is never left behind to be applied.
type Downloader struct {
	Client   *http.Client // HTTP client used for both fetches
	Verifier Verifier     // integrity verifier; defaults to SHA256Verifier
}

// NewDownloader creates a Downloader. If client is nil a client with a
// generous timeout is used. If verifier is nil SHA256Verifier is used.
func NewDownloader(client *http.Client, verifier Verifier) *Downloader {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	if verifier == nil {
		verifier = SHA256Verifier{}
	}
	return &Downloader{Client: client, Verifier: verifier}
}

// Download fetches the asset at assetURL and the checksums at sumsURL into
// stagingDir, then verifies the asset. On success the archive lives at
// stagingDir/<assetName> and the sums at stagingDir/SHA256SUMS. The progress
// callback receives (bytesDone, bytesTotal) for the asset download at ~100ms
// intervals; it may be nil.
//
// Context cancellation aborts in-flight HTTP reads cleanly: the request is
// created with the context, so a cancelled context surfaces as an error from
// the body read and the partial tmp file is removed.
func (d *Downloader) Download(ctx context.Context, assetURL, sumsURL, assetName, stagingDir string, progress func(done, total int64)) (*DownloadResult, error) {
	// Enforce HTTPS-only on both the asset and checksum URLs before any network
	// I/O: a compromised API response or MITM proxy must not downgrade the
	// fetch to plain HTTP (defense-in-depth alongside the fail-closed SHA256).
	if err := requireHTTPS(assetURL); err != nil {
		return nil, err
	}
	if err := requireHTTPS(sumsURL); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating staging dir %q: %w", stagingDir, err)
	}

	archivePath := filepath.Join(stagingDir, assetName)
	sumsPath := filepath.Join(stagingDir, sumsFilename)

	// Download both files atomically (tmp + rename). The asset is fetched
	// first so its progress is reported; the sums file is small.
	n, err := d.fetchFile(ctx, assetURL, archivePath, progress)
	if err != nil {
		return nil, err
	}
	if _, err := d.fetchFile(ctx, sumsURL, sumsPath, nil); err != nil {
		// Remove the asset so a dangling, unverified artifact is never left.
		_ = os.Remove(archivePath)
		return nil, err
	}

	// Fail-closed verification: on any verification failure, delete the
	// archive and report the error.
	if err := d.Verifier.Verify(archivePath, assetName, sumsPath); err != nil {
		_ = os.Remove(archivePath)
		return nil, fmt.Errorf("verifying asset %q: %w", assetName, err)
	}

	return &DownloadResult{
		AssetName:   assetName,
		ArchivePath: archivePath,
		SumsPath:    sumsPath,
		Bytes:       n,
	}, nil
}

// fetchFile downloads url into destPath using an atomic tmp-file + rename so a
// crash or cancellation never leaves a half-written file at the final path.
// progress is throttled at progressInterval; it may be nil.
func (d *Downloader) fetchFile(ctx context.Context, rawURL, destPath string, progress func(int64, int64)) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("creating request for %s: %w", rawURL, err)
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("downloading %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("downloading %s: HTTP %d", rawURL, resp.StatusCode)
	}

	// Write to a sibling tmp file so the rename is atomic on the same
	// filesystem. A random suffix avoids collisions across concurrent fetches.
	tmpPath, err := tmpSiblingPath(destPath)
	if err != nil {
		return 0, fmt.Errorf("staging tmp file for %s: %w", destPath, err)
	}

	f, err := os.Create(tmpPath)
	if err != nil {
		return 0, fmt.Errorf("creating tmp file %q: %w", tmpPath, err)
	}

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

	n, copyErr := io.Copy(writer, io.LimitReader(resp.Body, maxDownloadBytes+1))
	if closeErr := f.Close(); closeErr != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("closing tmp file %q: %w", tmpPath, closeErr)
	}
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("writing %s: %w", rawURL, copyErr)
	}
	if n > maxDownloadBytes {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("downloading %s: exceeds max size %d bytes", rawURL, maxDownloadBytes)
	}
	if resp.ContentLength > 0 && progress != nil {
		progress(n, resp.ContentLength)
	}

	// Atomic publish into place.
	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("publishing %s into place: %w", destPath, err)
	}
	return n, nil
}

// tmpSiblingPath returns a path next to destPath (same directory, so the
// rename is atomic) with a random suffix.
func tmpSiblingPath(destPath string) (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	dir, base := filepath.Split(destPath)
	return filepath.Join(dir, "."+base+"."+hex.EncodeToString(buf[:])+".tmp"), nil
}

// progressWriter wraps an io.Writer and emits progress callbacks at
// ~100ms intervals to avoid flooding the event channel during fast downloads.
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
	if now := time.Now(); now.Sub(pw.lastFlush) >= progressInterval {
		pw.progress(pw.written, pw.total)
		pw.lastFlush = now
	}
	return n, err
}
