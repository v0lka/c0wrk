package updater

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/v0lka/c0wrk/internal/securehttp"
	"github.com/v0lka/sp4rk/safeio"
)

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
// Every failure path is fail-closed: a checksum error removes the archive so
// a partial/corrupt artifact is never left behind to be applied
// (specs/decisions/023-auto-update.md).
type Downloader struct {
	Client   *http.Client // HTTP client used for both fetches
	Verifier Verifier     // integrity verifier; defaults to SHA256Verifier
}

// NewDownloader creates a Downloader. If client is nil a client with a
// generous timeout is used. If verifier is nil SHA256Verifier is used. The
// client (supplied or default) is shallow-copied and gets the per-hop
// HTTPS-only redirect policy (securehttp.SecureHTTPClient):
// securehttp.RequireHTTPS guards the initial URLs, and the policy guards
// every redirect hop after them.
func NewDownloader(client *http.Client, verifier Verifier) *Downloader {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	if verifier == nil {
		verifier = SHA256Verifier{}
	}
	return &Downloader{Client: securehttp.SecureHTTPClient(client), Verifier: verifier}
}

// Download fetches the asset at assetURL and the checksums at sumsURL into
// stagingDir, then verifies the asset. On success the archive lives at
// stagingDir/<assetName> and the sums at stagingDir/SHA256SUMS. The progress
// callback receives (bytesDone, bytesTotal) for the asset download at
// ~100ms intervals; it may be nil. Context cancellation aborts in-flight
// HTTP reads cleanly: the request is created with the context, so a
// cancelled context surfaces as an error from the body read and the partial
// tmp file is removed.
func (d *Downloader) Download(ctx context.Context, assetURL, sumsURL, assetName, stagingDir string, progress func(done, total int64)) (*DownloadResult, error) {
	// The asset name is attacker-influenceable data (it comes from the GitHub
	// release API, which a compromised release or a MITM proxy could control).
	// Never trust it as a path component: require a single plain filename so it
	// cannot escape stagingDir through "../" or host path separators.
	safeName := filepath.Base(assetName)
	if safeName != assetName || safeName == "." || safeName == ".." {
		return nil, fmt.Errorf("invalid asset name %q: must be a plain filename", assetName)
	}

	if err := securehttp.RequireHTTPS(assetURL); err != nil {
		return nil, err
	}
	if err := securehttp.RequireHTTPS(sumsURL); err != nil {
		return nil, err
	}

	// The staging root must be created as REAL directories: a link swapped
	// into a created component at the fixed ~/.c0wrk/update-staging path
	// would redirect the verified release archive and SHA256SUMS — written
	// below via tmp-file + rename — outside the agent dir. MkdirAllReal
	// refuses a dangling or swapped-in link (and any non-directory
	// component); a pre-existing operator-symlinked tree resolves as intent.
	if err := safeio.MkdirAllReal(stagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating staging dir %q: %w", stagingDir, err)
	}

	archivePath := filepath.Join(stagingDir, safeName)
	sumsPath := filepath.Join(stagingDir, sumsFilename)

	// The asset is fetched first so its progress is reported; the sums file
	// is small.
	n, err := d.fetchFile(ctx, assetURL, archivePath, progress)
	if err != nil {
		return nil, err
	}
	if _, err := d.fetchFile(ctx, sumsURL, sumsPath, nil); err != nil {
		// Remove the asset so a dangling, unverified artifact is never left.
		_ = os.Remove(archivePath)
		return nil, err
	}

	if err := d.Verifier.Verify(archivePath, safeName, sumsPath); err != nil {
		_ = os.Remove(archivePath)
		return nil, fmt.Errorf("verifying asset %q: %w", safeName, err)
	}

	return &DownloadResult{
		AssetName:   safeName,
		ArchivePath: archivePath,
		SumsPath:    sumsPath,
		Bytes:       n,
	}, nil
}

// fetchFile downloads url into destPath using an atomic sibling tmp-file +
// rename (same directory, so the rename stays on one filesystem) so a crash
// or cancellation never leaves a half-written file at the final path.
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
// rename is atomic) with a random suffix — no collisions across concurrent
// fetches.
func tmpSiblingPath(destPath string) (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	dir, base := filepath.Split(destPath)
	return filepath.Join(dir, "."+base+"."+hex.EncodeToString(buf[:])+".tmp"), nil
}

// progressWriter wraps an io.Writer, emitting progress callbacks at
// ~progressInterval to avoid flooding the event channel during fast
// downloads.
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
