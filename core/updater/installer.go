// Package updater implements the atomic self-update machinery for c0wrk-desktop.
//
// The self-update protocol is a two-process dance designed so the running
// application never replaces its own executable while executing it:
//
//  1. The running app calls PrepareSelfUpdate, which copies the current binary
//     into a staging directory and returns the path to the staging "updater".
//  2. The running app re-executes that staging updater copy with the flags
//     --self-update --pid <PID> --stage <dir> --target <installdir>, then exits.
//  3. On the second invocation main.go detects --self-update and calls
//     ApplySelfUpdate BEFORE starting the Wails loop: it waits for the parent
//     PID to die, extracts the staged update archive, performs an atomic swap
//     (current install tree → <root>.old backup, new tree → install root),
//     relaunches the new app, and cleans up. The Wails lifecycle never runs.
//
// The .old backup is retained after a successful swap so a user can roll back
// manually if the new version fails to start.
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

	"github.com/v0lka/sp4rk/pathutil"
)

// ErrNonStandardLocation is returned by DiscoverInstallRoot when the running
// binary lives in a location that is unsafe to update in place (a temporary
// directory, a Downloads folder, or a read-only path).
var ErrNonStandardLocation = errors.New("running from a non-standard location; install the app properly before updating")

// SelfUpdateOptions carries the parsed --self-update arguments.
type SelfUpdateOptions struct {
	// PID is the process id of the parent (old) app that launched the updater.
	// ApplySelfUpdate waits for this PID to exit before swapping files.
	PID int
	// StageDir is the staging directory that holds the update archive (and the
	// updater binary itself).
	StageDir string
	// TargetDir is the install root to replace (the .app bundle on macOS, or the
	// binary directory on Linux/Windows).
	TargetDir string
}

// shutdownTimeout is how long ApplySelfUpdate waits for the parent PID to exit
// before giving up. It is a var so tests can shorten it.
var shutdownTimeout = 60 * time.Second

// pollInterval is how often the PID-death waiter re-checks the parent.
var pollInterval = 500 * time.Millisecond

// maxDownloadBytes caps the size of a downloaded update archive or checksum
// file as defense-in-depth against disk exhaustion if a TLS path (e.g. an
// opted-in corporate proxy) is compromised. Downloads are SHA256-verified
// after the copy, so this cap only bounds the bytes written to disk before
// verification rejects a substituted-but-unforged response. Generous enough to
// accept the largest legitimate release asset.
const maxDownloadBytes int64 = 1 << 30 // 1 GiB

// maxExtractEntryBytes caps the decompressed size of a single archive entry as
// defense-in-depth against zip/tar bombs, mirroring core/toolmanager.
const maxExtractEntryBytes int64 = 512 << 20 // 512 MiB

// relaunchFn launches the freshly installed app. It is a package-level
// indirection so tests can substitute a no-op recorder instead of actually
// starting a GUI process.
var relaunchFn = relaunchApp

// DiscoverInstallRoot resolves the install root for the running binary and
// validates that it is a standard, writable location.
//
// On macOS the root is the enclosing .app bundle; on Linux/Windows it is the
// directory containing the binary. A non-standard location (temp, Downloads,
// read-only) yields ErrNonStandardLocation so the in-place install is left
// untouched.
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
	// Reject read-only locations: the swap renames entries in BOTH the install
	// root and its parent, so both must be writable. Probing the parent is the
	// critical check (e.g. on macOS /Applications may be admin-only even when
	// the .app bundle interior is user-writable); the self-check additionally
	// catches a genuinely read-only install dir.
	if !isWritable(abs) {
		return fmt.Errorf("%w: %s is not writable", ErrNonStandardLocation, abs)
	}
	parent := filepath.Dir(abs)
	if !isWritable(parent) {
		return fmt.Errorf("%w: %s is not writable (cannot rename install tree)", ErrNonStandardLocation, parent)
	}
	return nil
}

// hasDownloadsComponent reports whether any path element equals "downloads".
// It accepts both '/' and '\' as path separators so the check is portable
// across platforms and robust to paths that have not been normalized to the
// OS-native separator (e.g. forward-slash paths on Windows).
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
// equal to it), comparing cleaned absolute paths. Both are assumed already
// cleaned. Containment uses the centralized pathutil.IsWithinPath; equality is
// decided on the symlink-resolved prefixes so a candidate that is the same
// location as base (including through a symlink) is not "contained".
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

// isWritable reports whether the given directory is writable by attempting to
// create (and immediately remove) a probe file. The root must exist.
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
// directory and returns the path to the staging "updater" binary. The caller is
// expected to launch that binary with --self-update flags and then exit.
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

// ParseSelfUpdateFlags inspects raw args and, if a --self-update flag is
// present, parses the companion flags into SelfUpdateOptions. The returned bool
// reports whether this process is a self-update invocation.
func ParseSelfUpdateFlags(args []string) (SelfUpdateOptions, bool, error) {
	var opts SelfUpdateOptions
	hasFlag := false
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--self-update":
			hasFlag = true
		case "--pid":
			val, ok := nextArg(args, &i)
			if !ok {
				return opts, hasFlag, errors.New("--pid requires a value")
			}
			p, perr := parseInt(val)
			if perr != nil || p <= 0 {
				return opts, hasFlag, fmt.Errorf("invalid --pid %q: %w", val, perr)
			}
			opts.PID = p
		case "--stage":
			val, ok := nextArg(args, &i)
			if !ok {
				return opts, hasFlag, errors.New("--stage requires a value")
			}
			opts.StageDir = val
		case "--target":
			val, ok := nextArg(args, &i)
			if !ok {
				return opts, hasFlag, errors.New("--target requires a value")
			}
			opts.TargetDir = val
		}
		i++
	}
	if !hasFlag {
		return opts, false, nil
	}
	if opts.PID <= 0 {
		return opts, hasFlag, errors.New("--self-update requires --pid")
	}
	if opts.StageDir == "" {
		return opts, hasFlag, errors.New("--self-update requires --stage")
	}
	if opts.TargetDir == "" {
		return opts, hasFlag, errors.New("--self-update requires --target")
	}
	return opts, true, nil
}

func nextArg(args []string, i *int) (string, bool) {
	if *i+1 >= len(args) {
		return "", false
	}
	*i++
	return args[*i], true
}

func parseInt(s string) (int, error) {
	if s == "" {
		return 0, errors.New("not a positive integer")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a positive integer")
		}
	}
	// strconv.Atoi rejects values outside the platform int range, closing the
	// overflow wrap that a hand-rolled accumulation would allow on long input.
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, errors.New("not a positive integer")
	}
	return n, nil
}

// ApplySelfUpdate executes the staged update: wait for the parent PID to exit,
// extract the staged archive, atomically swap the install tree (keeping a .old
// backup), relaunch the new app, and clean up staging artifacts. It is intended
// to run from main.go before the Wails lifecycle starts.
func ApplySelfUpdate(opts SelfUpdateOptions, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	log.Info("self-update starting", "pid", opts.PID, "stage", opts.StageDir, "target", opts.TargetDir)

	// Reject an obviously invalid target before any destructive step, then
	// re-run the full standard-location validation. The production caller
	// already computed the target via DiscoverInstallRoot, but ApplySelfUpdate
	// must not trust a crafted or buggy --target: re-checking here rejects temp
	// dirs, Downloads, and — most importantly for the data-loss case — any
	// target whose parent is not writable, which stops a `--target $HOME` from
	// renaming the entire home directory.
	cleanTarget := filepath.Clean(opts.TargetDir)
	switch filepath.Base(cleanTarget) {
	case ".", "..", string(filepath.Separator):
		return errors.New("refusing to update into invalid target directory")
	}
	if err := validateStandardLocation(opts.TargetDir); err != nil {
		return err
	}

	if err := waitForProcessExit(opts.PID, shutdownTimeout, log); err != nil {
		return err
	}

	// Locate the staged update archive.
	archivePath, err := findStagedArchive(opts.StageDir)
	if err != nil {
		return err
	}

	// Extract into a sibling extraction directory so the new tree is fully on
	// disk before any swap occurs.
	extractDir, err := os.MkdirTemp(opts.StageDir, "c0wrk-extract-*")
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

	// The .old path is handed to swapInstallTrees, which clears a stale backup
	// from a previous attempt only AFTER the new tree has been staged beside
	// the install root. This ordering keeps the previous last-known-good
	// rollback target intact if the staging copy fails (e.g. out-of-disk on a
	// cross-filesystem move).
	oldBackup := opts.TargetDir + ".old"

	if err := swapInstallTrees(opts.TargetDir, newTree, oldBackup); err != nil {
		return fmt.Errorf("swap install trees: %w", err)
	}

	log.Info("install tree swapped; backing up previous version", "backup", oldBackup)

	// Relaunch the freshly installed app from its new location.
	if err := relaunchFn(opts.TargetDir, log); err != nil {
		// Relaunch failed: keep the .old backup and staging artifacts so the
		// user (or a retry) can recover. Do not treat relaunch failure as fatal
		// to the swap itself — the files are correctly in place.
		log.Warn("relaunch failed; .old backup retained for manual rollback", "error", err)
	}

	// Clean up the staging directory (the updater binary + extracted archive).
	// On platforms where the running binary cannot self-delete (Windows), the
	// caller/main is responsible for best-effort cleanup; here we still attempt
	// it.
	cleanupStaging(opts.StageDir, log)

	log.Info("self-update complete")
	return nil
}

// swapInstallTrees performs the atomic swap. The new tree is first materialized
// as a sibling of the install root (targetDir + ".new") so that the only
// operations touching the live install path are single renames within one
// filesystem — each individually atomic. This keeps a cross-filesystem copy
// failure from ever leaving the install tree half-overwritten.
//
// Sequence:
//  1. moveDir newTree → stagedNew (sibling). On a single filesystem this is a
//     rename; across filesystems it is a copy+remove. If it fails, stagedNew is
//     discarded and the live install tree is untouched.
//  2. Clear any stale oldBackup (previous .old) — only now, so a failure in
//     step 1 leaves the last-known-good rollback target intact.
//  3. Rename the current install root → oldBackup (atomic).
//  4. Rename stagedNew → targetDir (atomic: both are siblings on one volume).
//
// A failure between 3 and 4 rolls oldBackup back into targetDir — which is safe
// because after step 3 the target path is free (no ENOTEMPTY).
func swapInstallTrees(targetDir, newTree, oldBackup string) (retErr error) {
	if _, err := os.Stat(newTree); err != nil {
		return fmt.Errorf("new tree missing: %w", err)
	}

	// Stage 1: materialize the new tree beside the install root. A cross-fs
	// copy can fail midway; if it does we only discard stagedNew.
	stagedNew := targetDir + ".new"
	_ = os.RemoveAll(stagedNew) // clear any leftover from a prior attempt
	if err := moveDir(newTree, stagedNew); err != nil {
		_ = os.RemoveAll(stagedNew)
		return fmt.Errorf("stage new tree beside install root: %w", err)
	}

	// Stage 2: clear any stale .old backup from a previous attempt, then move
	// the current install tree aside to the .old backup. The stale backup is
	// removed only now — after the staging copy succeeded — so a failed stage
	// (step 1) never destroys the user's last-known-good rollback target.
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

	// Stage 3: move the staged new tree into place. stagedNew and targetDir are
	// siblings, so this rename is atomic on a single filesystem.
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

// resolveNewTree locates the installable tree inside an extraction directory.
//
//   - macOS: the top-level *.app bundle.
//   - Linux/Windows: the extraction directory itself (the archive root is the
//     build/output directory).
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
		// Reproduce symlink entries as symlinks. The macOS release archive is
		// packaged with `ditto -c -k --keepParent` precisely because the .app
		// bundle contains framework symlinks; materializing them as regular
		// files (the previous behavior) installs a bundle that is not launchable.
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
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
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

// readZipEntry reads a zip entry's contents as a string, bounded to
// maxSymlinkTargetBytes. It is only used for symlink entries, whose content is
// the link target.
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

// safeCreateSymlink creates a symlink at target pointing at linkTarget, after
// verifying the link resolves to a path that stays inside dest. This prevents
// a crafted archive from smuggling an escape route via a symlink (a
// symlink-flavored zip-slip). Absolute link targets are rejected unless they
// still resolve inside dest (which is effectively never for the archives this
// path consumes).
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
	f, err := os.Open(src)
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
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777|0o600)
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

// safeJoin joins dest with name and ensures the result stays within dest,
// rejecting path-traversal (zip-slip / tar-slip) attempts.
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

// copyTree recursively copies src into dst (dst is created). Permissions and
// symlinks are preserved so a cross-filesystem move of a macOS .app bundle
// does not flatten its framework symlinks into regular files.
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
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	in, err := os.Open(src)
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

// CleanupStaleUpdaters removes any leftover updater artifacts from previous
// runs. On Windows the running updater .exe cannot self-delete, so this is the
// primary cleanup path and is invoked at normal startup.
func CleanupStaleUpdaters(log *slog.Logger) {
	cleanupStaleUpdatersPlatform(log)
}

// cleanupStaging removes the staging directory, ignoring errors (best effort).
func cleanupStaging(stageDir string, log *slog.Logger) {
	if err := os.RemoveAll(stageDir); err != nil {
		log.Debug("could not remove staging dir (best-effort)", "dir", stageDir, "error", err)
	}
}
