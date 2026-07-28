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
)

// InstallResult reports the outcome of a tool installation step.
type InstallResult struct {
	ToolName  string // tool name from the spec
	BinPath   string // absolute path to the installed binary
	Installed bool   // false if already up-to-date (version matched)
}

// Installer handles the extraction and installation of tool archives and the
// bootstrap of Python environments. The interface exists so tests can
// substitute a mock implementation.
type Installer interface {
	InstallStaticBinary(archivePath string, tool ToolSpec, binDir string) (*InstallResult, error)
	InstallPythonPackage(ctx context.Context, tool ToolSpec, toolsDir string, binDir string) (*InstallResult, error)
}

// FSInstaller is the production Installer that operates on the local filesystem.
type FSInstaller struct{}

// NewFSInstaller creates a new FSInstaller.
func NewFSInstaller() *FSInstaller {
	return &FSInstaller{}
}

// InstallStaticBinary extracts the archive at archivePath and places the
// binary (identified by tool.BinPathInArchive) into binDir/<tool.BinName>.
// Supports .tar.gz and .zip archives.
func (i *FSInstaller) InstallStaticBinary(archivePath string, tool ToolSpec, binDir string) (*InstallResult, error) {
	dst := filepath.Join(binDir, tool.BinName)
	if runtime.GOOS == "windows" {
		dst += ".exe"
	}

	// If the binary already exists at the destination, skip re-install.
	if _, err := os.Stat(dst); err == nil {
		return &InstallResult{ToolName: tool.Name, BinPath: dst, Installed: false}, nil
	}

	// Extract to a temp directory.
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

	// Locate the binary inside the extracted tree. Archives use different
	// layouts (subdir vs. flat) and Windows adds an ".exe" suffix, so try
	// the declared path and a flat fallback, each with/without ".exe".
	src, ok := resolveBinaryInTree(tmpDir, tool.BinPathInArchive, tool.BinName, runtime.GOOS)
	if !ok {
		return nil, fmt.Errorf("tool %q: binary %q not found in archive (looked for %s)", tool.Name, tool.BinName, tool.BinPathInArchive)
	}

	// Ensure binDir exists.
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return nil, fmt.Errorf("tool %q: creating bin dir: %w", tool.Name, err)
	}

	// Copy the binary to binDir.
	if err := copyFile(src, dst, 0o755); err != nil {
		return nil, fmt.Errorf("tool %q: copying binary: %w", tool.Name, err)
	}

	return &InstallResult{ToolName: tool.Name, BinPath: dst, Installed: true}, nil
}

// InstallPythonPackage bootstraps a Python environment and installs the
// package via uv. Requires the uv binary to already exist in binDir.
func (i *FSInstaller) InstallPythonPackage(ctx context.Context, tool ToolSpec, toolsDir, binDir string) (*InstallResult, error) {
	uvBin := filepath.Join(binDir, "uv")
	if runtime.GOOS == "windows" {
		uvBin += ".exe"
	}
	if _, err := os.Stat(uvBin); os.IsNotExist(err) {
		return nil, errors.New("uv is not installed; uv must be bootstrapped before Python packages")
	}

	// Check if markitdown wrapper already exists.
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

	// Step 3: Install the package. Use findPythonInDir on the venv to get the
	// platform-correct python path (venv/bin/python3 on Unix, venv/Scripts/python.exe on Windows).
	venvPython := findPythonInDir(venvDir, tool.PythonVersion, runtime.GOOS)
	if venvPython == "" {
		return nil, fmt.Errorf("tool %q: python binary not found in venv %s", tool.Name, venvDir)
	}
	pipCmd := exec.CommandContext(ctx, uvBin, "pip", "install", "--python", venvPython, tool.PipSpec)
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

// findPythonInDir searches dir for a Python interpreter binary, preferring an
// exact version match when pythonVersion is non-empty.
//
// The interpreter location depends on both the directory's provenance and the
// host platform:
//
//   - uv-managed install (uv python install --install-dir <dir>): the version
//     lives under <dir>/cpython-<version>-<platform>/. On Unix the interpreter
//     is at <version-dir>/bin/python3; on Windows python-build-standalone
//     places "python.exe" at the version-dir root — there is no "bin"
//     subdirectory (see uv's executable_path_from_base, which returns
//     base/python.exe on Windows vs base/bin/python3 on Unix).
//   - virtual environment (uv venv <dir>): <dir>/bin/python3 on Unix,
//     <dir>/Scripts/python.exe on Windows.
//
// The goos parameter ("windows" vs anything else) makes the lookup testable
// on any host platform, mirroring resolveBinaryInTree.
func findPythonInDir(dir, pythonVersion, goos string) string {
	var names []string
	var subDirs []string
	if goos == "windows" {
		// python-build-standalone ships only "python.exe" (never "python3.exe").
		// A managed install places it at the version-dir root; a venv places it
		// under "Scripts". "bin" is a defensive last resort.
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

// createWrapper creates a shell script that invokes the Python module.
// pythonBin must be the absolute path to the Python interpreter (already
// resolved by the caller via findPythonInDir).
func createWrapper(wrapperPath, pythonBin, moduleName string) error {
	if runtime.GOOS == "windows" {
		wrapperPath = strings.TrimSuffix(wrapperPath, ".cmd") + ".cmd"
		content := fmt.Sprintf("@echo off\r\n\"%s\" -m %s %%*\r\n", pythonBin, moduleName) //nolint:gocritic // batch file, not Go string
		return os.WriteFile(wrapperPath, []byte(content), 0o755)
	}
	content := fmt.Sprintf("#!/bin/sh\nexec \"%s\" -m %s \"$@\"\n", pythonBin, moduleName) //nolint:gocritic // shell script, not Go string
	return os.WriteFile(wrapperPath, []byte(content), 0o755)
}

// resolveBinaryInTree locates the extracted binary by trying the declared
// in-archive path and a flat fallback. On Windows (goos == "windows") it
// prefers the ".exe" variant because upstream archives ship executables
// with that suffix. The goos parameter makes the lookup testable on any
// host platform. Returns the resolved path and whether a match was found.
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

// maxExtractEntryBytes caps the decompressed size of a single archive entry as
// defense-in-depth against zip bombs. Downloads are SHA256-verified, but a
// checksum-valid-but-malicious archive could still exhaust disk on extraction.
const maxExtractEntryBytes = 512 << 20 // 512 MiB

// pathExists reports whether a file exists at path (file or otherwise).
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// extractTarGz extracts a .tar.gz archive to destDir.
func extractTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
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
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst) // don't leave a partial file that looks "installed"
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst) // don't leave a partial file that looks "installed"
		return err
	}
	return os.Chmod(dst, mode)
}
