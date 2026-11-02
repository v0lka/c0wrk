package toolmanager

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/v0lka/c0wrk/internal/sysproc"
	"github.com/v0lka/sp4rk/pathutil"

	"github.com/v0lka/sp4rk/safeio"
)

// InstallResult reports the outcome of a tool installation step.
type InstallResult struct {
	ToolName  string // tool name from the spec
	BinPath   string // absolute path to the installed binary
	Installed bool   // false if already up-to-date (version matched)
}

// Installer extracts/installs tool archives and bootstraps Python
// environments; an interface so tests can substitute a mock.
type Installer interface {
	InstallStaticBinary(archivePath string, tool ToolSpec, binDir string) (*InstallResult, error)
	InstallPythonPackage(ctx context.Context, tool ToolSpec, toolsDir string, binDir string) (*InstallResult, error)
}

// FSInstaller is the production Installer that operates on the local filesystem.
type FSInstaller struct{}

func NewFSInstaller() *FSInstaller {
	return &FSInstaller{}
}

// InstallStaticBinary extracts archivePath and places the binary identified
// by tool.BinPathInArchive at binDir/<tool.BinName> (.tar.gz / .zip).
func (i *FSInstaller) InstallStaticBinary(archivePath string, tool ToolSpec, binDir string) (*InstallResult, error) {
	dst := filepath.Join(binDir, tool.BinName)
	if runtime.GOOS == "windows" {
		dst += ".exe"
	}

	// The destination-exists short-circuit: an existing binary → no reinstall.
	if _, err := os.Stat(dst); err == nil {
		return &InstallResult{ToolName: tool.Name, BinPath: dst, Installed: false}, nil
	}

	tmpDir, err := os.MkdirTemp("", "c0wrk-tool-"+tool.Name+"-*")
	if err != nil {
		return nil, fmt.Errorf("tool %q: creating temp dir: %w", tool.Name, err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	switch {
	case strings.HasSuffix(archivePath, ".tar.gz"), strings.HasSuffix(archivePath, ".tgz"):
		if err := extractTarGz(archivePath, tmpDir); err != nil {
			return nil, fmt.Errorf("tool %q: %w", tool.Name, err)
		}
	case strings.HasSuffix(archivePath, ".zip"):
		if err := extractZip(archivePath, tmpDir); err != nil {
			return nil, fmt.Errorf("tool %q: %w", tool.Name, err)
		}
	default:
		return nil, fmt.Errorf("tool %q: unsupported archive format: %s", tool.Name, filepath.Ext(archivePath))
	}

	src, ok := resolveBinaryInTree(tmpDir, tool.BinPathInArchive, tool.BinName, runtime.GOOS)
	if !ok {
		return nil, fmt.Errorf("tool %q: binary %q not found in archive (looked for %s)", tool.Name, tool.BinName, tool.BinPathInArchive)
	}

	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return nil, fmt.Errorf("tool %q: creating bin dir: %w", tool.Name, err)
	}

	if err := copyFile(src, dst, 0o755); err != nil {
		return nil, fmt.Errorf("tool %q: copying binary: %w", tool.Name, err)
	}

	return &InstallResult{ToolName: tool.Name, BinPath: dst, Installed: true}, nil
}

// InstallPythonPackage bootstraps the managed Python via uv (install → venv
// → pip) and writes the wrapper; uv must already exist in binDir.
func (i *FSInstaller) InstallPythonPackage(ctx context.Context, tool ToolSpec, toolsDir, binDir string) (*InstallResult, error) {
	uvBin := filepath.Join(binDir, "uv")
	if runtime.GOOS == "windows" {
		uvBin += ".exe"
	}
	if _, err := os.Stat(uvBin); os.IsNotExist(err) {
		return nil, errors.New("uv is not installed; uv must be bootstrapped before Python packages")
	}

	wrapperName := tool.BinName
	if runtime.GOOS == "windows" {
		wrapperName += ".cmd"
	}
	wrapperPath := filepath.Join(binDir, wrapperName)
	if _, err := os.Stat(wrapperPath); err == nil {
		return &InstallResult{ToolName: tool.Name, BinPath: wrapperPath, Installed: false}, nil
	}

	pythonDir := filepath.Join(toolsDir, "python")
	installDir := filepath.Join(pythonDir, "install")
	venvDir := filepath.Join(pythonDir, "venv")

	// Step 1: Install portable Python via uv.
	installCmd := exec.CommandContext(ctx, uvBin, "python", "install", tool.PythonVersion,
		"--install-dir", installDir)
	sysproc.HideConsole(installCmd) // avoid flashing console windows on Windows (GUI app)
	if out, err := installCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("tool %q: uv python install failed: %w\n%s", tool.Name, err, out)
	}

	// Step 2: Create a virtual environment.
	pythonBin := findPythonInDir(installDir, tool.PythonVersion, runtime.GOOS)
	if pythonBin == "" {
		return nil, fmt.Errorf("tool %q: python binary not found in %s after uv install", tool.Name, installDir)
	}
	venvCmd := exec.CommandContext(ctx, uvBin, "venv", venvDir, "--python", pythonBin)
	sysproc.HideConsole(venvCmd) // avoid flashing console windows on Windows (GUI app)
	if out, err := venvCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("tool %q: uv venv failed: %w\n%s", tool.Name, err, out)
	}

	// Step 3: Install the package.
	venvPython := findPythonInDir(venvDir, tool.PythonVersion, runtime.GOOS)
	if venvPython == "" {
		return nil, fmt.Errorf("tool %q: python binary not found in venv %s", tool.Name, venvDir)
	}
	lockPath, err := writeRequirementsLock(tool, toolsDir)
	if err != nil {
		return nil, err
	}
	pipCmd := exec.CommandContext(ctx, uvBin, pipInstallArgs(venvPython, lockPath, tool.PipSpec)...)
	sysproc.HideConsole(pipCmd) // avoid flashing console windows on Windows (GUI app)
	if out, err := pipCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("tool %q: uv pip install failed: %w\n%s", tool.Name, err, out)
	}

	// Step 4: Create a wrapper script in binDir.
	if err := createWrapper(wrapperPath, venvPython, tool.Name); err != nil {
		return nil, fmt.Errorf("tool %q: creating wrapper: %w", tool.Name, err)
	}

	return &InstallResult{ToolName: tool.Name, BinPath: wrapperPath, Installed: true}, nil
}

// writeRequirementsLock materializes the repo-embedded
// ToolSpec.RequirementsLock (trusted, audited at review time — never network)
// as <toolsDir>/<name>-lock.txt for `uv pip install -r`; "" when the tool
// declares no lock (caller falls back to the floating PipSpec).
func writeRequirementsLock(tool ToolSpec, toolsDir string) (string, error) {
	if tool.RequirementsLock == "" {
		return "", nil
	}
	lockPath := filepath.Join(toolsDir, tool.Name+"-lock.txt")
	// Atomic + FIFO/symlink-safe: the fixed path is pre-plantable — a bare
	// os.WriteFile would block on a planted FIFO and write through a symlink.
	if err := safeio.WriteFileAtomic(lockPath, []byte(tool.RequirementsLock), 0o644); err != nil {
		return "", fmt.Errorf("tool %q: writing requirements lock: %w", tool.Name, err)
	}
	return lockPath, nil
}

// pipInstallArgs builds the `uv pip install` args: `-r <lockPath>` when
// lockPath is non-empty (pinned install), else the floating pipSpec.
func pipInstallArgs(venvPython, lockPath, pipSpec string) []string {
	args := []string{"pip", "install", "--python", venvPython}
	if lockPath != "" {
		return append(args, "-r", lockPath)
	}
	return append(args, pipSpec)
}

// findPythonInDir locates a Python interpreter in dir, preferring an exact
// version match when pythonVersion is non-empty. Layout follows provenance
// and platform: uv-managed install under <dir>/cpython-<version>-<platform>/
// (bin/python3 on Unix; Windows python-build-standalone ships "python.exe"
// at the version-dir root, no "bin" — see uv's executable_path_from_base);
// a venv is bin/python3 / Scripts/python.exe. goos keeps it host-testable.
func findPythonInDir(dir, pythonVersion, goos string) string {
	var names []string
	var subDirs []string
	if goos == "windows" {
		// python-build-standalone ships only "python.exe": version-dir root
		// for managed installs, "Scripts" for venvs; "bin" defensive last resort.
		names = []string{"python.exe"}
		subDirs = []string{"", "Scripts", "bin"}
	} else {
		names = []string{"python3", "python"}
		subDirs = []string{"bin"}
	}

	// 1. Versioned managed install: <dir>/cpython-<version>-*/<sub>/<name>.
	if pythonVersion != "" {
		for _, sub := range subDirs {
			for _, name := range names {
				pattern := filepath.Join(dir, "cpython-"+pythonVersion+"-*", sub, name)
				if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
					return matches[0]
				}
			}
		}
	}

	// 2. Any version directory: <dir>/*/<sub>/<name>.
	for _, sub := range subDirs {
		for _, name := range names {
			if matches, err := filepath.Glob(filepath.Join(dir, "*", sub, name)); err == nil && len(matches) > 0 {
				return matches[0]
			}
		}
	}

	// 3. Directly under dir (e.g. a venv's Scripts): <dir>/<sub>/<name>.
	for _, sub := range subDirs {
		for _, name := range names {
			p := filepath.Join(dir, sub, name)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

// createWrapper writes the module-invoking wrapper script; pythonBin must be
// the absolute interpreter path (resolved by the caller via findPythonInDir).
func createWrapper(wrapperPath, pythonBin, moduleName string) error {
	// Atomic + FIFO/symlink-safe: the fixed wrapper path is pre-plantable — a
	// bare os.WriteFile would block on a planted FIFO and write through a link.
	write := func(content []byte) error {
		return safeio.WriteFileAtomic(wrapperPath, content, 0o755)
	}
	if runtime.GOOS == "windows" {
		wrapperPath = strings.TrimSuffix(wrapperPath, ".cmd") + ".cmd"
		content := fmt.Sprintf("@echo off\r\n\"%s\" -m %s %%*\r\n", pythonBin, moduleName) //nolint:gocritic // batch file, not Go string
		return write([]byte(content))
	}
	content := fmt.Sprintf("#!/bin/sh\nexec \"%s\" -m %s \"$@\"\n", pythonBin, moduleName) //nolint:gocritic // shell script, not Go string
	return write([]byte(content))
}

// resolveBinaryInTree locates the extracted binary: declared in-archive path
// first, then a flat fallback; ".exe" wins on Windows (upstream ships that
// suffix). Returns the path and whether a match was found.
func resolveBinaryInTree(tmpDir, binPathInArchive, binName, goos string) (string, bool) {
	bases := []string{
		filepath.Join(tmpDir, binPathInArchive),
		filepath.Join(tmpDir, binName),
	}
	wantExe := goos == "windows"
	for _, base := range bases {
		if wantExe {
			if exe := base + ".exe"; pathExists(exe) {
				return exe, true
			}
		}
		if pathExists(base) {
			return base, true
		}
	}
	return "", false
}

// maxExtractEntryBytes caps one decompressed entry (zip-bomb defense):
// SHA256 verifies bytes, not intent — a checksum-valid archive can still
// exhaust disk on extraction.
const maxExtractEntryBytes = 512 << 20 // 512 MiB

// maxDownloadBytes caps a downloaded archive, bounding the bytes written
// before SHA256 verification rejects a substituted-but-unforged response
// (e.g. over a compromised TLS path such as an opted-in corporate proxy).
const maxDownloadBytes int64 = 1 << 30 // 1 GiB

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// extractTarGz extracts a .tar.gz archive to destDir.
func extractTarGz(archivePath, destDir string) error {
	f, err := safeio.Open(archivePath)
	if err != nil {
		return fmt.Errorf("opening archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("creating gzip reader: %w", err)
	}
	defer func() { _ = gzr.Close() }()

	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar entry: %w", err)
		}

		target := filepath.Join(destDir, hdr.Name)
		// Prevent path traversal (tar-slip) via the centralized containment API.
		within, travErr := pathutil.IsWithinPath(destDir, target)
		if travErr != nil || !within {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("creating directory: %w", err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("creating parent dir: %w", err)
			}
			out, err := os.Create(target)
			if err != nil {
				return fmt.Errorf("creating file: %w", err)
			}
			n, copyErr := io.Copy(out, io.LimitReader(tr, maxExtractEntryBytes+1))
			if copyErr != nil {
				_ = out.Close()
				_ = os.Remove(target)
				return fmt.Errorf("writing file: %w", copyErr)
			}
			if err := out.Close(); err != nil {
				_ = os.Remove(target)
				return fmt.Errorf("closing file: %w", err)
			}
			if n > maxExtractEntryBytes {
				_ = os.Remove(target)
				return fmt.Errorf("archive entry exceeds max size %d bytes (possible zip bomb)", maxExtractEntryBytes)
			}
			if err := os.Chmod(target, os.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("chmod: %w", err)
			}
		}
	}
	return nil
}

// extractZip extracts a .zip archive to destDir.
func extractZip(archivePath, destDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("opening zip: %w", err)
	}
	defer func() { _ = r.Close() }()

	for _, f := range r.File {
		target := filepath.Join(destDir, f.Name)
		within, travErr := pathutil.IsWithinPath(destDir, target)
		if travErr != nil || !within {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("creating directory: %w", err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("creating parent dir: %w", err)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("opening zip entry: %w", err)
		}
		out, err := os.Create(target)
		if err != nil {
			_ = rc.Close()
			return fmt.Errorf("creating file: %w", err)
		}
		n, err := io.Copy(out, io.LimitReader(rc, maxExtractEntryBytes+1))
		_ = rc.Close()
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(target)
			return fmt.Errorf("writing file: %w", err)
		}
		if n > maxExtractEntryBytes {
			_ = os.Remove(target)
			return fmt.Errorf("archive entry exceeds max size %d bytes (possible zip bomb)", maxExtractEntryBytes)
		}
	}
	return nil
}

// copyFile copies src to dst with the given mode.
func copyFile(src, dst string, mode os.FileMode) (err error) {
	in, err := safeio.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := in.Close(); cerr != nil {
			// A Close error on the read side signals an FS anomaly; join with
			// any prior error.
			err = errors.Join(err, cerr)
		}
	}()

	// Stage into a uniquely named sibling temp and rename into place: the
	// fixed final path is pre-plantable with a dangling symlink a bare
	// os.Create would follow outside ~/.c0wrk (the upstream Lstat probe misses
	// dangling links); the rename replaces a plant, a failed copy keeps the old file.
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}
