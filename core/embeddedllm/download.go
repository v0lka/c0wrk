package embeddedllm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

// Range support — experimentally confirmed against both pinned hosts.
//
// Resume is the normal path, not an optimistic guess. Probed live (2026-09-23)
// with `curl -L -H "Range: bytes=<offset>-"`:
//
//	Hugging Face weights
//	  GET https://huggingface.co/prism-ml/Ternary-Bonsai-2-27B-gguf/resolve/<rev>/
//	      Ternary-Bonsai-2-27B-mmproj-Q8_0.gguf          Range: bytes=629246000-
//	  → 302  accept-ranges: bytes
//	         x-linked-size: 629246976
//	         x-linked-etag: "6807ede61d570bb86ba34b756a0fa109edc33668604de867c6ea6d8f1d631903"
//	         location: https://us.aws.cdn.hf.co/xet-bridge-us/… (signed, expiring)
//	  → 206  accept-ranges: bytes
//	         content-length: 976
//	         content-range: bytes 629246000-629246975/629246976
//	  → 416  for a Range starting beyond EOF
//
//	GitHub release assets (runtime + cudart)
//	  GET …/releases/download/prism-b10709-9a9394a/llama-…-bin-win-cpu-x64.zip
//	                                                   Range: bytes=18785000-
//	  → 302 → 206  accept-ranges: bytes
//	               content-length: 841
//	               content-range: bytes 18785000-18785840/18785841
//	  → 416  content-range: bytes */18785841 for a Range beyond EOF
//
// Both hosts serve the bytes through a redirect to a signed CDN URL. net/http
// copies the Range header across a redirect — only Authorization, Cookie,
// Cookie2 and WWW-Authenticate are domain-gated — so one ranged request
// resumes correctly without any redirect handling here.
//
// Fallbacks, both explicit and never silent:
//
//   - A host that answers 200 to a ranged request ignored Range. The transfer
//     restarts from byte 0 using that same response body: the partial is
//     truncated, the hasher is reset, a warning is logged, and Result carries
//     Restarted + RestartReason. Appending a full body to a partial would
//     silently corrupt the artifact, so this is the only safe reading of 200.
//   - A host that answers 416 cannot be reconciled with the partial on disk
//     (the object shrank, or the partial is longer than the pin). The partial
//     is discarded and the download restarts once from byte 0.
//
// A transfer is never left "silently partial": every byte that reaches the
// destination path has been SHA256-verified against the compile-time pin.
const (
	// PartialSuffix marks an in-flight transfer. A file at the destination
	// path WITHOUT this suffix has passed SHA256 verification.
	PartialSuffix = ".part"

	// DefaultProgressInterval is the progress-callback throttle: fast enough to
	// feel live, slow enough not to flood the Wails event channel on a 10 Gb/s
	// link where a 6.7 GiB transfer produces hundreds of thousands of writes.
	DefaultProgressInterval = 100 * time.Millisecond

	// responseHeaderTimeout bounds the wait for response headers only. There is
	// deliberately no bound on the body transfer.
	responseHeaderTimeout = 60 * time.Second
)

// DefaultDiskHeadroom is free space required ON TOP of the bytes being
// written. It absorbs the runtime archive's extracted contents and the
// transient doubling while a partial is promoted, so a nearly-full volume
// cannot end in ENOSPC halfway through a 6.7 GiB write.
const DefaultDiskHeadroom int64 = 2 << 30 // 2 GiB

// maxTransferAttempts bounds the 416-restart loop: one resume attempt plus one
// from-scratch attempt. A host that keeps rejecting ranges is a hard error, not
// a retry loop.
const maxTransferAttempts = 2

// ProgressFunc reports transfer progress as (bytesDone, bytesTotal). It is
// called immediately at the start (with the resume offset, so the UI shows a
// resumed transfer starting partway), at throttled intervals during the
// transfer, and always once more at completion with done == total.
type ProgressFunc func(done, total int64)

// Downloader fetches one pinned artifact to disk with resume, progress and
// fail-closed verification.
//
// It is deliberately NOT core/toolmanager.Download. That downloader bounds a
// startup-critical archive: maxDownloadBytes (1 GiB), a 5-minute whole-transfer
// client timeout and a fixed 200 MiB disk guard — every one of which is wrong
// for a 6.7 GiB user-initiated download that may span hours and must resume
// across restarts (ADR-066, "Not a tool-manager extension"). What is reused is
// the tool-manager's *pattern*: compile-time pins, fail-closed SHA256, and
// securing the new bytes before destroying anything old.
//
// The zero value is usable; NewDownloader applies the documented defaults.
type Downloader struct {
	// Client performs the transfer. nil → defaultTransferClient(), which has
	// no overall timeout.
	Client *http.Client
	// Logger receives resume/restart/fallback diagnostics. nil → slog.Default().
	Logger *slog.Logger
	// ProgressInterval throttles progress callbacks. <= 0 → DefaultProgressInterval.
	ProgressInterval time.Duration
	// DiskHeadroom is the free space required beyond the artifact size.
	// <= 0 → DefaultDiskHeadroom.
	DiskHeadroom int64
	// FreeSpace reports bytes available on the volume containing a path.
	// nil → platformFreeSpace. Injectable so the guard is testable without
	// filling a disk.
	FreeSpace func(path string) (int64, error)
}

// Result reports the outcome of a verified download.
type Result struct {
	// Path is the verified destination file (never the .part).
	Path string
	// SizeBytes is the artifact size, equal to the pinned size on success.
	SizeBytes int64
	// BytesWritten is the number of bytes this call transferred; 0 on a cache
	// hit and less than SizeBytes when the transfer resumed.
	BytesWritten int64
	// SHA256 is the verified digest.
	SHA256 string
	// Resumed is true when the transfer continued from a pre-existing partial.
	Resumed bool
	// Cached is true when no bytes were transferred because the destination
	// already existed and verified.
	Cached bool
	// Restarted is true when a Range fallback discarded the resume offset and
	// re-fetched from byte 0; RestartReason carries the user-facing message.
	Restarted     bool
	RestartReason string
	// Verified is always true when Download returns a nil error: fail-closed
	// means there is no code path that hands back unverified bytes.
	Verified bool
}

// Downloader refusals. Each is a sentinel so callers can branch with errors.Is
// and surface an actionable message instead of a generic failure.
var (
	// ErrMissingChecksum is the fail-closed refusal for an artifact with no
	// usable pinned digest. An empty checksum means REFUSE — it is never read
	// as "skip verification" (ASI04).
	ErrMissingChecksum = errors.New("no pinned sha256 for this artifact")
	// ErrChecksumMismatch reports downloaded bytes whose digest differs from
	// the pin. The partial file is deleted; the bytes are never accepted.
	ErrChecksumMismatch = errors.New("sha256 mismatch")
	// ErrInsufficientDisk reports that the volume cannot hold the artifact.
	ErrInsufficientDisk = errors.New("insufficient free disk space")
	// ErrArtifactTooLarge reports a server that offered more bytes than the
	// pin allows — a wrong object, a lying mirror or a captive portal.
	ErrArtifactTooLarge = errors.New("transfer exceeded the pinned artifact size")
	// ErrIncompleteTransfer reports a transfer that stopped early. The partial
	// is KEPT so the next attempt resumes instead of restarting.
	ErrIncompleteTransfer = errors.New("transfer stopped before the pinned artifact size")
	// errStalePartial signals that the partial cannot be resumed (server
	// answered 416) and the download must restart from byte 0.
	errStalePartial = errors.New("resume offset rejected by the server")
)

// NewDownloader returns a Downloader with the documented defaults: no overall
// transfer timeout, ~100 ms progress throttle and a 2 GiB disk headroom.
func NewDownloader(client *http.Client, logger *slog.Logger) *Downloader {
	return &Downloader{
		Client:           client,
		Logger:           logger,
		ProgressInterval: DefaultProgressInterval,
		DiskHeadroom:     DefaultDiskHeadroom,
	}
}

// defaultTransferClient builds the transfer client once.
//
// It has NO Client.Timeout on purpose. toolmanager's 5-minute cap exists to
// keep a hung startup tool download from blocking the app; here the same cap
// would abort every multi-gigabyte transfer on a slow link. Liveness is
// bounded instead by the dial and response-header timeouts plus ctx.
var defaultTransferClient = sync.OnceValue(func() *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   30 * time.Second,
			ResponseHeaderTimeout: responseHeaderTimeout,
			ExpectContinueTimeout: time.Second,
			MaxIdleConns:          4,
			IdleConnTimeout:       90 * time.Second,
		},
	}
})

// RequiredFreeBytes is the free space an artifact set of totalBytes needs,
// including the headroom. Install uses it to gate the WHOLE set before the
// first byte is fetched, so a 7.3 GiB install is never attempted on a volume
// that can only hold 3 GiB.
func RequiredFreeBytes(totalBytes int64) int64 {
	return totalBytes + DefaultDiskHeadroom
}

// Download fetches asset to dstPath, resuming from an existing partial, and
// returns only after the bytes on disk verify against the pin.
//
// Contract:
//   - an artifact with no usable pinned digest is refused before any I/O;
//   - a checksum mismatch deletes the partial and returns ErrChecksumMismatch;
//   - a transport failure keeps the partial so the next call resumes;
//   - cancellation through ctx aborts at any stage and keeps the partial;
//   - dstPath is only ever created after verification (it is renamed from the
//     partial), so an unverified file never occupies the destination path.
//
// progress may be nil.
func (d *Downloader) Download(ctx context.Context, asset Asset, dstPath string, progress ProgressFunc) (*Result, error) {
	if err := validateAsset(asset); err != nil {
		return nil, err
	}
	if dstPath == "" {
		return nil, errors.New("embeddedllm: empty destination path")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("embeddedllm: %s: %w", asset.Component, err)
	}

	dir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("embeddedllm: creating %q: %w", dir, err)
	}
	// Sized to THIS artifact, never a fixed constant.
	if err := d.checkDiskSpace(dir, asset.SizeBytes+d.headroom()); err != nil {
		return nil, err
	}

	// Verified-cache fast path: no network, no re-hash of a partial.
	if _, err := os.Stat(dstPath); err == nil {
		verr := VerifyFile(dstPath, asset)
		if verr == nil {
			if progress != nil {
				progress(asset.SizeBytes, asset.SizeBytes)
			}
			return &Result{
				Path:      dstPath,
				SizeBytes: asset.SizeBytes,
				SHA256:    strings.ToLower(asset.SHA256),
				Cached:    true,
				Verified:  true,
			}, nil
		}
		d.logger().Warn("cached artifact failed verification, re-downloading",
			"component", asset.Component, "path", dstPath, "error", verr)
		if rerr := os.Remove(dstPath); rerr != nil {
			return nil, fmt.Errorf("embeddedllm: removing unverified %q: %w", dstPath, rerr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("embeddedllm: stat %q: %w", dstPath, err)
	}

	partial := dstPath + PartialSuffix
	var lastErr error
	var restartReason string
	for attempt := 1; attempt <= maxTransferAttempts; attempt++ {
		res, err := d.transfer(ctx, asset, dstPath, partial, progress)
		if err == nil {
			// Carry the fallback reason out even though the retry succeeded:
			// the user is entitled to know the transfer restarted.
			if restartReason != "" {
				res.Restarted = true
				res.RestartReason = restartReason
			}
			return res, nil
		}
		if !errors.Is(err, errStalePartial) {
			return nil, err
		}
		// The partial is irreconcilable with the server object. Discard it and
		// restart once from byte 0 — explicitly, never silently.
		reason := fmt.Sprintf("the server rejected the resume offset (%v); the download restarted from the beginning", err)
		d.logger().Warn("discarding unresumable partial and restarting the download",
			"component", asset.Component, "url", asset.URL, "partial", partial, "error", err)
		if rerr := os.Remove(partial); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return nil, fmt.Errorf("embeddedllm: removing stale partial %q: %w", partial, rerr)
		}
		restartReason = reason
		lastErr = errors.New(reason)
	}
	return nil, fmt.Errorf("embeddedllm: %s: download failed after %d attempts: %w",
		asset.Component, maxTransferAttempts, lastErr)
}

// transfer runs a single fetch: resume offset → ranged request → verified
// promotion. It never leaves an unverified file at dstPath.
func (d *Downloader) transfer(ctx context.Context, asset Asset, dstPath, partial string, progress ProgressFunc) (*Result, error) {
	total := asset.SizeBytes

	offset, err := d.resumeOffset(partial, asset)
	if err != nil {
		return nil, err
	}
	if offset == total {
		// A previous run wrote every byte but died before promotion. Verify
		// and promote without touching the network.
		verr := VerifyFile(partial, asset)
		if verr == nil {
			if perr := promote(partial, dstPath); perr != nil {
				return nil, perr
			}
			if progress != nil {
				progress(total, total)
			}
			return &Result{
				Path:      dstPath,
				SizeBytes: total,
				SHA256:    strings.ToLower(asset.SHA256),
				Resumed:   true,
				Cached:    true,
				Verified:  true,
			}, nil
		}
		d.logger().Warn("complete partial failed verification, restarting the download",
			"component", asset.Component, "partial", partial, "error", verr)
		if rerr := os.Remove(partial); rerr != nil {
			return nil, fmt.Errorf("embeddedllm: removing corrupt partial %q: %w", partial, rerr)
		}
		offset = 0
	}

	// Hash the bytes already on disk BEFORE requesting anything, so the final
	// digest covers the whole artifact without a second 6.7 GiB read after the
	// transfer. This is a read-only open: no file is created until the server
	// has actually answered, so a failed request leaves no partial behind.
	hasher := sha256.New()
	if offset > 0 {
		if herr := hashPrefix(hasher, partial, offset); herr != nil {
			return nil, herr
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("embeddedllm: creating request: %w", err)
	}
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	resp, err := d.client().Do(req)
	if err != nil {
		// Transport/context failure: the partial stays on disk for resume.
		return nil, fmt.Errorf("embeddedllm: %s: request failed: %w", asset.Component, err)
	}
	defer func() { _ = resp.Body.Close() }()

	res := &Result{Path: dstPath, SizeBytes: total, Resumed: offset > 0}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		start, complete, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != offset {
			return nil, fmt.Errorf("embeddedllm: %s: server resumed at the wrong offset (sent Range: bytes=%d-, got Content-Range: %q)",
				asset.Component, offset, resp.Header.Get("Content-Range"))
		}
		if complete > 0 && complete != total {
			return nil, fmt.Errorf("embeddedllm: %s: server reports %d bytes, the pin expects %d",
				asset.Component, complete, total)
		}
	case http.StatusOK:
		// Range ignored. Restart from byte 0 with THIS body rather than
		// appending a full object onto a partial (which would silently
		// corrupt the artifact).
		if offset > 0 {
			res.Restarted = true
			res.Resumed = false
			res.RestartReason = fmt.Sprintf(
				"the host answered 200 to a ranged request instead of 206, so it does not support resume; the %s download restarted from the beginning",
				asset.Component)
			d.logger().Warn("host ignored the Range header, restarting the transfer from byte 0",
				"component", asset.Component, "url", asset.URL, "discarded_offset", offset)
			hasher.Reset()
			offset = 0
		}
	case http.StatusRequestedRangeNotSatisfiable:
		return nil, fmt.Errorf("%w: Content-Range %q for offset %d of a %d-byte pin",
			errStalePartial, resp.Header.Get("Content-Range"), offset, total)
	default:
		return nil, fmt.Errorf("embeddedllm: %s: HTTP %d from %s", asset.Component, resp.StatusCode, asset.URL)
	}

	// The response is good, so now — and only now — create or open the partial
	// for writing and position it at the resume offset (0 after a fallback).
	f, err := os.OpenFile(partial, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("embeddedllm: opening partial %q: %w", partial, err)
	}
	defer func() { _ = f.Close() }()
	if terr := f.Truncate(offset); terr != nil {
		return nil, fmt.Errorf("embeddedllm: truncating partial to %d bytes: %w", offset, terr)
	}
	if _, serr := f.Seek(offset, io.SeekStart); serr != nil {
		return nil, fmt.Errorf("embeddedllm: seeking partial to %d: %w", offset, serr)
	}

	remaining := total - offset
	if progress != nil {
		// Immediate, unthrottled: the UI must show the resume point at once.
		progress(offset, total)
	}
	// The hasher must see BOTH the resumed prefix (fed above) and every byte
	// of this response, so the final digest covers the whole artifact. Writing
	// only to the file would verify nothing. io.MultiWriter returns an
	// io.Writer, so the progress wrapper can replace it below.
	writer := io.MultiWriter(f, hasher)
	if progress != nil {
		writer = &progressWriter{
			w:        writer,
			progress: progress,
			base:     offset,
			total:    total,
			interval: d.progressInterval(),
			last:     time.Now(),
		}
	}
	// remaining+1 so an oversized object is detected instead of truncated
	// silently. There is NO fixed byte ceiling here — the pin IS the ceiling.
	written, copyErr := io.Copy(writer, io.LimitReader(resp.Body, remaining+1))
	if written > remaining {
		_ = f.Close()
		if rerr := os.Remove(partial); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			d.logger().Warn("could not remove oversized partial", "partial", partial, "error", rerr)
		}
		return nil, fmt.Errorf("embeddedllm: %s: %w (pin expects %d bytes)", asset.Component, ErrArtifactTooLarge, total)
	}
	if copyErr != nil {
		// Keep the partial: this is exactly what resume is for. A cancelled
		// or dropped transfer costs at most the bytes since the last flush.
		// Both sentinels are wrapped so callers can branch on cancellation
		// (errors.Is(err, context.Canceled)) as well as on incompleteness.
		return nil, fmt.Errorf("embeddedllm: %s: %w after %d of %d bytes (partial kept at %s for resume): %w",
			asset.Component, ErrIncompleteTransfer, written, remaining, partial, copyErr)
	}
	// Sync before Close and before promotion, so a crash right after the
	// rename cannot leave a zero-length artifact at the destination path.
	if serr := f.Sync(); serr != nil {
		return nil, fmt.Errorf("embeddedllm: syncing partial %q: %w", partial, serr)
	}
	if cerr := f.Close(); cerr != nil {
		return nil, fmt.Errorf("embeddedllm: closing partial %q: %w", partial, cerr)
	}
	res.BytesWritten = written

	// Fail-closed verification BEFORE promotion: on mismatch the partial is
	// deleted and the destination path is never created.
	digest := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(digest, asset.SHA256) {
		if rerr := os.Remove(partial); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			d.logger().Warn("could not remove partial after checksum mismatch", "partial", partial, "error", rerr)
		}
		return nil, fmt.Errorf("embeddedllm: %s: %w: downloaded %s, the pin expects %s (the partial file was deleted)",
			asset.Component, ErrChecksumMismatch, digest, strings.ToLower(asset.SHA256))
	}

	if err := promote(partial, dstPath); err != nil {
		return nil, err
	}
	res.SHA256 = digest
	res.Verified = true
	if progress != nil {
		// Final callback, unthrottled: completion is always reported at 100%
		// even when the throttle swallowed the last intermediate update.
		progress(total, total)
	}
	return res, nil
}

// resumeOffset reports how many already-downloaded bytes of asset the partial
// file holds. A partial longer than the pin cannot be part of the artifact and
// is discarded; an empty or missing partial yields 0.
func (d *Downloader) resumeOffset(partial string, asset Asset) (int64, error) {
	fi, err := os.Stat(partial)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("embeddedllm: stat partial %q: %w", partial, err)
	}
	size := fi.Size()
	switch {
	case size <= 0:
		return 0, nil
	case size > asset.SizeBytes:
		d.logger().Warn("partial is longer than the pinned artifact, discarding it",
			"component", asset.Component, "partial", partial, "size", size, "pinned", asset.SizeBytes)
		if rerr := os.Remove(partial); rerr != nil {
			return 0, fmt.Errorf("embeddedllm: removing oversized partial %q: %w", partial, rerr)
		}
		return 0, nil
	default:
		return size, nil
	}
}

// VerifyFile checks an on-disk file against its pin, fail-closed: a missing or
// malformed digest is an error, a short file is an error, and a differing
// digest is ErrChecksumMismatch. It never reports success for unverified bytes.
func VerifyFile(path string, asset Asset) error {
	if !validSHA256Hex(asset.SHA256) {
		return fmt.Errorf("embeddedllm: %s: %w", asset.Component, ErrMissingChecksum)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("embeddedllm: stat %q: %w", path, err)
	}
	if asset.SizeBytes > 0 && fi.Size() != asset.SizeBytes {
		return fmt.Errorf("embeddedllm: %s: %s is %d bytes, the pin expects %d",
			asset.Component, path, fi.Size(), asset.SizeBytes)
	}
	digest, err := hashFile(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(digest, asset.SHA256) {
		return fmt.Errorf("embeddedllm: %s: %w: %s is %s, the pin expects %s",
			asset.Component, ErrChecksumMismatch, path, digest, strings.ToLower(asset.SHA256))
	}
	return nil
}

// hashPrefix feeds the first n bytes of an existing partial into hasher so a
// resumed transfer ends up with a digest over the whole artifact. The file is
// opened read-only: nothing is created or modified here.
func hashPrefix(hasher io.Writer, partial string, n int64) error {
	f, err := os.Open(partial)
	if err != nil {
		return fmt.Errorf("embeddedllm: opening partial %q to resume: %w", partial, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(hasher, io.NewSectionReader(f, 0, n)); err != nil {
		return fmt.Errorf("embeddedllm: hashing %d resumed bytes of %q: %w", n, partial, err)
	}
	return nil
}

// hashFile returns the lowercase hex SHA256 of a file's contents.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("embeddedllm: opening %q for verification: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("embeddedllm: hashing %q: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// promote moves a verified partial onto the destination path. The destination
// is removed first because os.Rename will not replace an existing file on
// Windows; removal is safe here since the replacement bytes are already
// verified on disk (secure-before-destroy).
func promote(partial, dstPath string) error {
	if err := os.Remove(dstPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("embeddedllm: removing %q before promotion: %w", dstPath, err)
	}
	if err := os.Rename(partial, dstPath); err != nil {
		return fmt.Errorf("embeddedllm: promoting %q to %q: %w", partial, dstPath, err)
	}
	return nil
}

// checkDiskSpace refuses a download when the volume holding dir cannot fit the
// artifact plus headroom. The guard is sized to the actual artifact, which is
// the whole reason this downloader is not the tool-manager's (whose guard is a
// fixed 200 MiB).
//
// Measurement failure is best-effort and non-fatal, matching
// toolmanager.checkDiskSpace: an unmeasurable volume should not make the
// feature unusable, and a genuinely full disk still fails safely — the write
// returns ENOSPC, the transfer keeps its partial, and the digest gate means
// unverified bytes are never accepted.
func (d *Downloader) checkDiskSpace(dir string, required int64) error {
	freeSpace := d.FreeSpace
	if freeSpace == nil {
		freeSpace = platformFreeSpace
	}
	free, err := freeSpace(dir)
	if err != nil {
		d.logger().Warn("disk space check unavailable, proceeding without the guard",
			"path", dir, "required", required, "error", err)
		return nil //nolint:nilerr // best-effort guard; the write fails safely on ENOSPC
	}
	if free < required {
		return fmt.Errorf("%w for %s: %s available, %s required (artifact %s + %s headroom)",
			ErrInsufficientDisk, dir,
			formatBytes(free), formatBytes(required),
			formatBytes(required-d.headroom()), formatBytes(d.headroom()))
	}
	return nil
}

// platformFreeSpace reports bytes available to an unprivileged writer on the
// volume containing path (statfs Bavail×Bsize on Unix, GetDiskFreeSpaceEx on
// Windows). gopsutil is already a direct dependency and provides the identical
// fact on every platform, so no build-tagged helper files are needed.
func platformFreeSpace(path string) (int64, error) {
	usage, err := disk.Usage(path)
	if err != nil {
		return 0, err
	}
	if usage.Free > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(usage.Free), nil
}

// validateAsset is the fail-closed gate in front of every transfer.
func validateAsset(asset Asset) error {
	// An empty (or malformed) digest means REFUSE. It is never interpreted as
	// "no checksum published, skip verification" — that reading is how an
	// unverified binary ends up being executed (ASI04).
	if !validSHA256Hex(asset.SHA256) {
		return fmt.Errorf("embeddedllm: %s: %w (refusing to download unverified bytes)",
			asset.Component, ErrMissingChecksum)
	}
	if asset.URL == "" {
		return fmt.Errorf("embeddedllm: %s: no download URL for this platform/backend", asset.Component)
	}
	if asset.SizeBytes <= 0 {
		return fmt.Errorf("embeddedllm: %s: pinned size must be positive, got %d", asset.Component, asset.SizeBytes)
	}
	if !strings.HasPrefix(asset.URL, "https://") {
		return fmt.Errorf("embeddedllm: %s: refusing non-HTTPS URL %q", asset.Component, asset.URL)
	}
	return nil
}

// parseContentRange parses "bytes <start>-<end>/<complete>" (or "bytes */<n>").
// complete is 0 when the server reports "*".
func parseContentRange(value string) (start, complete int64, ok bool) {
	const prefix = "bytes "
	rest, found := strings.CutPrefix(strings.TrimSpace(value), prefix)
	if !found {
		return 0, 0, false
	}
	rangePart, completePart, found := strings.Cut(rest, "/")
	if !found {
		return 0, 0, false
	}
	startPart, _, found := strings.Cut(rangePart, "-")
	if !found {
		return 0, 0, false
	}
	startValue, err := strconv.ParseInt(strings.TrimSpace(startPart), 10, 64)
	if err != nil {
		return 0, 0, false
	}
	completePart = strings.TrimSpace(completePart)
	if completePart == "*" || completePart == "" {
		return startValue, 0, true
	}
	completeValue, err := strconv.ParseInt(completePart, 10, 64)
	if err != nil {
		return startValue, 0, true
	}
	return startValue, completeValue, true
}

// progressWriter throttles progress callbacks to roughly one per interval.
type progressWriter struct {
	w        io.Writer
	progress ProgressFunc
	base     int64 // bytes already on disk when this transfer started
	total    int64
	written  int64
	interval time.Duration
	last     time.Time
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	pw.written += int64(n)
	if now := time.Now(); now.Sub(pw.last) >= pw.interval {
		pw.progress(pw.base+pw.written, pw.total)
		pw.last = now
	}
	return n, err
}

func (d *Downloader) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return defaultTransferClient()
}

func (d *Downloader) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

func (d *Downloader) progressInterval() time.Duration {
	if d.ProgressInterval > 0 {
		return d.ProgressInterval
	}
	return DefaultProgressInterval
}

func (d *Downloader) headroom() int64 {
	if d.DiskHeadroom > 0 {
		return d.DiskHeadroom
	}
	return DefaultDiskHeadroom
}

// formatBytes renders a byte count for user-facing disk-guard messages.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	value := float64(n)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		if value < unit {
			return strconv.FormatFloat(value, 'f', 1, 64) + " " + suffix
		}
	}
	return strconv.FormatFloat(value/unit, 'f', 1, 64) + " PiB"
}
