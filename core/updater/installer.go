// Package updater implements the atomic self-update machinery for
// c0wrk-desktop: a two-process re-exec dance (the running app stages a copy
// of itself and quits; the staging updater waits for the parent to die,
// extracts the archive, swaps the install tree keeping a .old rollback
// backup, relaunches and cleans up) so the app never replaces its own
// executable while executing it. See specs/decisions/023-auto-update.md.
package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/core/cli"
	"github.com/v0lka/sp4rk/pathutil"

	"github.com/v0lka/sp4rk/safeio"
)

// ErrNonStandardLocation is returned by DiscoverInstallRoot when the running
// binary lives somewhere unsafe to update in place (temp dir, Downloads,
// read-only path) — the in-place install is left untouched.
var ErrNonStandardLocation = errors.New("running from a non-standard location; install the app properly before updating")

// StagedUpdaterShutdownWait is how long a launched staging updater waits for
// the parent process to exit before giving up; exported so other layers (the
// desktop close guard's update_pending flag) can reason about that window.
const StagedUpdaterShutdownWait = 60 * time.Second

// shutdownTimeout is how long ApplySelfUpdate waits for the parent PID to exit
// before giving up. It is a var so tests can shorten it.
var shutdownTimeout = StagedUpdaterShutdownWait

// pollInterval is how often the PID-death waiter re-checks the parent.
var pollInterval = 500 * time.Millisecond

// maxDownloadBytes caps a downloaded archive or checksum file as
// defense-in-depth against disk exhaustion on a compromised TLS path;
// downloads are SHA256-verified after the copy, so the cap only bounds the
// bytes written to disk before verification rejects the response.
const maxDownloadBytes int64 = 1 << 30 // 1 GiB

// maxExtractEntryBytes caps the decompressed size of a single archive entry as
// defense-in-depth against zip/tar bombs, mirroring core/toolmanager.
const maxExtractEntryBytes int64 = 512 << 20 // 512 MiB

// relaunchFn launches the freshly installed app. It is a package-level
// indirection so tests can substitute a no-op recorder instead of actually
// starting a GUI process.
var relaunchFn = relaunchApp

// DiscoverInstallRoot resolves the install root for the running binary (the
// enclosing .app bundle on macOS, the binary's directory elsewhere) and
// validates it is a standard, writable location; otherwise
// ErrNonStandardLocation leaves the in-place install untouched.
func DiscoverInstallRoot() (string, error) {
	root, err := findInstallRoot()
	if err != nil {
		return "", fmt.Errorf("discover install root: %w", err)
	}
	if err := validateStandardLocation(root); err != nil {
		return "", err
	}
	return root, nil
}

// validateStandardLocation rejects temp directories, Downloads folders, and
// read-only paths.
func validateStandardLocation(root string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	lower := strings.ToLower(abs)
	// Reject OS temporary directories.
	if tempRoot, terr := normalizedTempRoot(); terr == nil && pathContains(abs, tempRoot) {
		return fmt.Errorf("%w: %s is inside the temporary directory", ErrNonStandardLocation, abs)
	}
	// Reject Downloads folders (case-insensitive path component match).
	if hasDownloadsComponent(lower) {
		return fmt.Errorf("%w: %s is inside a Downloads folder", ErrNonStandardLocation, abs)
	}
	// Reject read-only locations: the swap renames entries in BOTH the
	// install root and its parent, so both must be writable (the parent is
	// the critical check — e.g. an admin-only /Applications on macOS); the
	// self-check catches a genuinely read-only install dir.
	if !isWritable(abs) {
		return fmt.Errorf("%w: %s is not writable", ErrNonStandardLocation, abs)
	}
	parent := filepath.Dir(abs)
	if !isWritable(parent) {
		return fmt.Errorf("%w: %s is not writable (cannot rename install tree)", ErrNonStandardLocation, parent)
	}
	// The target must actually be an install root, not merely a writable
	// directory: a bare dev binary in an arbitrary folder must not be
	// "validated" into swapping that folder out and later deleting its .old
	// backup.
	if !hasInstallMarker(abs) {
		return fmt.Errorf("%w: %s does not look like an install root", ErrNonStandardLocation, abs)
	}
	return nil
}

// hasDownloadsComponent reports whether any path element equals "downloads",
// accepting both separators so un-normalized paths (e.g. forward slashes on
// Windows) are covered too.
func hasDownloadsComponent(lowerPath string) bool {
	for _, part := range strings.FieldsFunc(lowerPath, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if part == "downloads" {
			return true
		}
	}
	return false
}

// pathContains reports whether candidate is strictly nested under base (not
// equal), via pathutil.IsWithinPath; equality is decided on symlink-resolved
// prefixes so the same location through a symlink is not "contained".
func pathContains(candidate, base string) bool {
	if base == "" {
		return false
	}
	within, err := pathutil.IsWithinPath(base, candidate)
	if err != nil || !within {
		return false
	}
	return pathutil.ResolveExistingPrefix(filepath.Clean(candidate)) !=
		pathutil.ResolveExistingPrefix(filepath.Clean(base))
}

// isWritable probes dir by creating and immediately removing a temp file; the
// directory must exist.
func isWritable(dir string) bool {
	probe, err := os.CreateTemp(dir, ".c0wrk-wprobe-*")
	if err != nil {
		return false
	}
	probePath := probe.Name()
	_ = probe.Close()
	_ = os.Remove(probePath)
	return true
}

// PrepareSelfUpdate copies the current executable into a fresh staging
// directory and returns the staging "updater" path to launch with the
// --self-update flags before exiting (specs/decisions/023-auto-update.md).
func PrepareSelfUpdate() (stagingDir, updaterPath string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("resolve current executable: %w", err)
	}
	base := updaterStagingBase()
	stagingDir, err = os.MkdirTemp(base, "c0wrk-update-*")
	if err != nil {
		return "", "", fmt.Errorf("create staging dir: %w", err)
	}
	updaterPath, err = copyExecutable(exe, filepath.Join(stagingDir, stagingUpdaterName()))
	if err != nil {
		_ = os.RemoveAll(stagingDir)
		return "", "", err
	}
	return stagingDir, updaterPath, nil
}

// SelfUpdateArgs builds the command-line arguments for the staging updater
// invocation.
func SelfUpdateArgs(pid int, stageDir, targetDir string) []string {
	return []string{
		"--self-update",
		"--pid", strconv.Itoa(pid),
		"--stage", stageDir,
		"--target", targetDir,
	}
}

// ApplySelfUpdate executes the staged update: wait for the parent PID to
// exit, extract the staged archive, atomically swap the install tree
// (keeping a .old backup), relaunch the new app, and clean up staging.
// Runs from main.go before the Wails lifecycle starts
// (specs/decisions/023-auto-update.md); req is the parsed --self-update
// command line (core/cli).
func ApplySelfUpdate(req cli.SelfUpdateRequest, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	log.Info("self-update starting", "pid", req.PID, "stage", req.Stage, "target", req.Target)

	// Reject an obviously invalid target, then re-run the full
	// standard-location validation: ApplySelfUpdate must not trust a crafted
	// or buggy --target — temp dirs, Downloads, read-only paths and any
	// directory that does not look like an install root are all rejected, so
	// a `--target $HOME` cannot rename an unrelated directory and later
	// delete its .old backup.
	cleanTarget := filepath.Clean(req.Target)
	switch filepath.Base(cleanTarget) {
	case ".", "..", string(filepath.Separator):
		return errors.New("refusing to update into invalid target directory")
	}
	if err := validateStandardLocation(req.Target); err != nil {
		return err
	}

	if err := waitForProcessExit(req.PID, shutdownTimeout, log); err != nil {
		return err
	}

	// Locate the staged update archive.
	archivePath, err := findStagedArchive(req.Stage)
	if err != nil {
		return err
	}

	// Extract into a sibling extraction directory so the new tree is fully on
	// disk before any swap occurs.
	extractDir, err := os.MkdirTemp(req.Stage, "c0wrk-extract-*")
	if err != nil {
		return fmt.Errorf("create extraction dir: %w", err)
	}
	if err := extractArchive(archivePath, extractDir); err != nil {
		_ = os.RemoveAll(extractDir)
		return fmt.Errorf("extract update archive: %w", err)
	}

	// Resolve the staged "new install tree" inside the extraction dir.
	newTree, err := resolveNewTree(extractDir)
	if err != nil {
		_ = os.RemoveAll(extractDir)
		return err
	}

	// The stale .old backup is cleared only AFTER the new tree has been staged
	// beside the install root, so a failed staging copy keeps the
	// last-known-good rollback target intact.
	oldBackup := req.Target + ".old"

	if err := swapInstallTrees(req.Target, newTree, oldBackup); err != nil {
		return fmt.Errorf("swap install trees: %w", err)
	}

	log.Info("install tree swapped; backing up previous version", "backup", oldBackup)

	// Relaunch the freshly installed app from its new location.
	if err := relaunchFn(req.Target, log); err != nil {
		// Relaunch failure is non-fatal to the swap — the files are correctly
		// in place; keep .old and staging artifacts so the user (or a retry)
		// can recover.
		log.Warn("relaunch failed; .old backup retained for manual rollback", "error", err)
	}

	// Best-effort staging cleanup; on platforms where the running binary
	// cannot self-delete (Windows), the caller/main covers cleanup.
	cleanupStaging(req.Stage, log)

	log.Info("self-update complete")
	return nil
}

// swapInstallTrees performs the atomic swap: the new tree is first
// materialized as a sibling of the install root (targetDir + ".new") so the
// only operations touching the live install path are single renames within
// one filesystem — each individually atomic, so a cross-filesystem copy
// failure can never leave the install tree half-overwritten. A stale .old
// backup is cleared only after the new tree has staged successfully, and any
// failure after the current tree moves to .old rolls the backup back (the
// target path is free, no ENOTEMPTY). See specs/decisions/023-auto-update.md.
func swapInstallTrees(targetDir, newTree, oldBackup string) (retErr error) {
	if _, err := os.Stat(newTree); err != nil {
		return fmt.Errorf("new tree missing: %w", err)
	}

	// A cross-fs staging copy can fail midway; only stagedNew is discarded.
	stagedNew := targetDir + ".new"
	_ = os.RemoveAll(stagedNew) // clear any leftover from a prior attempt
	if err := moveDir(newTree, stagedNew); err != nil {
		_ = os.RemoveAll(stagedNew)
		return fmt.Errorf("stage new tree beside install root: %w", err)
	}

	// Clear any stale .old backup — only now, after staging succeeded — then
	// move the current install tree aside to the backup.
	_ = os.RemoveAll(oldBackup)
	if err := os.Rename(targetDir, oldBackup); err != nil {
		_ = os.RemoveAll(stagedNew)
		return fmt.Errorf("rename current install to .old backup: %w", err)
	}
	// Ensure we roll back if anything below fails.
	defer func() {
		if retErr != nil {
			if rerr := os.Rename(oldBackup, targetDir); rerr != nil {
				retErr = fmt.Errorf("%w (and rollback failed: %w)", retErr, rerr)
			}
		}
	}()

	// stagedNew and targetDir are siblings: this rename is atomic on one fs.
	if err := os.Rename(stagedNew, targetDir); err != nil {
		return fmt.Errorf("move staged new tree into place: %w", err)
	}
	return nil
}

// moveDir moves src to dst, handling cross-filesystem renames by copying then
// deleting.
func moveDir(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Cross-device link: fall back to recursive copy + remove.
	if err := copyTree(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

// findStagedArchive returns the first update archive (zip or tar.gz) found in
// the staging directory.
func findStagedArchive(stageDir string) (string, error) {
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		return "", fmt.Errorf("read staging dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz") {
			return filepath.Join(stageDir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("no update archive (.zip/.tar.gz) found in staging dir %s", stageDir)
}

// resolveNewTree locates the installable tree inside an extraction
// directory: the top-level *.app bundle on macOS, the extraction directory
// itself on Linux/Windows (the archive root is the build output).
func resolveNewTree(extractDir string) (string, error) {
	return resolveNewTreePlatform(extractDir)
}

// extractArchive extracts a zip or tar.gz archive into destDir (which must
// exist). The format is inferred from the file extension.
func extractArchive(archivePath, destDir string) error {
	lower := strings.ToLower(archivePath)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(archivePath, destDir)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractTarGz(archivePath, destDir)
	default:
		return fmt.Errorf("unsupported archive format: %s", archivePath)
	}
}

// extractZip extracts a zip archive, defending against zip-slip by ensuring
// every destination path stays within destDir.
func extractZip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		target, err := safeJoin(dest, f.Name)
		if err != nil {
			return err
		}
		// Reproduce symlink entries as symlinks: the macOS archive is packaged
		// with `ditto -c -k --keepParent` and the .app bundle's framework
		// symlinks must stay symlinks — materializing them as regular files
		// installs an unlaunchable bundle.
		if f.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := readZipEntry(f)
			if err != nil {
				return err
			}
			if err := safeCreateSymlink(dest, target, linkTarget); err != nil {
				return err
			}
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		if err := copyZipFile(target, f); err != nil {
			return err
		}
		// Restore the executable bit when present (Unix).
		if f.Mode().Perm()&0o100 != 0 {
			_ = os.Chmod(target, 0o755)
		}
	}
	return nil
}

func copyZipFile(target string, f *zip.File) error {
	out, err := safeio.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	n, err := io.Copy(out, io.LimitReader(rc, maxExtractEntryBytes+1))
	if err != nil {
		return err
	}
	if n > maxExtractEntryBytes {
		return fmt.Errorf("zip entry %q exceeds max size %d bytes (possible zip bomb)", f.Name, maxExtractEntryBytes)
	}
	return nil
}

// readZipEntry reads a zip entry's contents as a string (the link target of a
// symlink entry), bounded to maxSymlinkTargetBytes.
func readZipEntry(f *zip.File) (string, error) {
	const maxSymlinkTargetBytes int64 = 4096
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(io.LimitReader(rc, maxSymlinkTargetBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxSymlinkTargetBytes {
		return "", fmt.Errorf("zip symlink target %q exceeds %d bytes", f.Name, maxSymlinkTargetBytes)
	}
	return string(data), nil
}

// safeCreateSymlink creates a symlink only after verifying the link resolves
// to a path inside dest — a symlink-flavored zip-slip guard. Absolute link
// targets are rejected unless they still resolve inside dest (effectively
// never for the archives this path consumes).
func safeCreateSymlink(dest, target, linkTarget string) error {
	resolved := linkTarget
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(target), resolved)
	}
	resolved = filepath.Clean(resolved)
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return fmt.Errorf("resolve destination path: %w", err)
	}
	absResolved, err := filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("resolve symlink target: %w", err)
	}
	within, err := pathutil.IsWithinPath(absDest, absResolved)
	if err != nil || !within {
		return fmt.Errorf("symlink %q points outside destination: %q", target, linkTarget)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	return os.Symlink(linkTarget, target)
}

// extractTarGz extracts a gzip-compressed tar archive, defending against
// path traversal.
func extractTarGz(src, dest string) error {
	f, err := safeio.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip reader: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)&0o777|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			out, err := safeio.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777|0o600)
			if err != nil {
				return err
			}
			n, err := io.Copy(out, io.LimitReader(tr, maxExtractEntryBytes+1))
			if err != nil {
				_ = out.Close()
				return err
			}
			if n > maxExtractEntryBytes {
				_ = out.Close()
				return fmt.Errorf("archive entry %q exceeds max size %d bytes (possible zip bomb)", hdr.Name, maxExtractEntryBytes)
			}
			_ = out.Close()
		case tar.TypeSymlink:
			// Skip symlinks for safety during an update extraction.
		}
	}
	return nil
}

// safeJoin joins dest with name and rejects path traversal (zip-slip /
// tar-slip) so the result stays within dest (specs/decisions/023-auto-update.md).
func safeJoin(dest, name string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("archive entry escapes destination: %q", name)
	}
	joined := filepath.Join(dest, cleaned)
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return "", fmt.Errorf("resolve destination path: %w", err)
	}
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", fmt.Errorf("resolve archive entry path: %w", err)
	}
	within, err := pathutil.IsWithinPath(absDest, absJoined)
	if err != nil || !within {
		return "", fmt.Errorf("archive entry escapes destination: %q", name)
	}
	return joined, nil
}

// copyTree recursively copies src into dst, preserving permissions and
// symlinks so a cross-filesystem .app bundle move keeps its framework
// symlinks instead of flattening them into regular files.
func copyTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return copySymlink(src, dst)
	}
	if info.IsDir() {
		return copyDirTree(src, dst, info)
	}
	return copyFileTree(src, dst, info)
}

// copySymlink recreates the symlink at src at the destination path.
func copySymlink(src, dst string) error {
	link, err := os.Readlink(src)
	if err != nil {
		return err
	}
	return os.Symlink(link, dst)
}

func copyDirTree(src, dst string, info os.FileInfo) error {
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFileTree(src, dst string, info os.FileInfo) error {
	mode := info.Mode().Perm()
	out, err := safeio.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	in, err := safeio.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// waitForProcessExit polls until the given PID is no longer running or the
// timeout elapses. A PID of 0 is treated as "already gone".
func waitForProcessExit(pid int, timeout time.Duration, log *slog.Logger) error {
	if pid <= 0 {
		return nil
	}
	deadline := time.Now().Add(timeout)
	for {
		if !processAlive(pid) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for parent pid %d to exit", timeout, pid)
		}
		log.Debug("waiting for parent process to exit", "pid", pid)
		time.Sleep(pollInterval)
	}
}

// stagingUpdaterName returns the filename for the staging updater copy.
func stagingUpdaterName() string {
	name := "c0wrk-updater"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// CleanupStaleUpdaters removes leftover updater artifacts from previous runs;
// the primary cleanup path on Windows, where the running updater .exe cannot
// self-delete, invoked at normal startup (specs/decisions/023-auto-update.md).
func CleanupStaleUpdaters(log *slog.Logger) {
	cleanupStaleUpdatersPlatform(log)
}

// cleanupStaging removes the staging directory, ignoring errors (best effort).
func cleanupStaging(stageDir string, log *slog.Logger) {
	if err := os.RemoveAll(stageDir); err != nil {
		log.Debug("could not remove staging dir (best-effort)", "dir", stageDir, "error", err)
	}
}
