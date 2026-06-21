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

	// Locate the binary inside the extracted tree.
	src := filepath.Join(tmpDir, tool.BinPathInArchive)
	if _, err := os.Stat(src); os.IsNotExist(err) {
		// Try a flat extraction (binary directly in tmpDir).
		src = filepath.Join(tmpDir, tool.BinName)
		if _, err2 := os.Stat(src); os.IsNotExist(err2) {
			return nil, fmt.Errorf("tool %q: binary %q not found in archive (looked for %s)", tool.Name, tool.BinName, tool.BinPathInArchive)
		}
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
	if out, err := installCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("tool %q: uv python install failed: %w\n%s", tool.Name, err, out)
	}

	// Step 2: Create a virtual environment.
	pythonBin := findPythonInDir(installDir, tool.PythonVersion)
	if pythonBin == "" {
		return nil, fmt.Errorf("tool %q: python binary not found in %s after uv install", tool.Name, installDir)
	}
	venvCmd := exec.CommandContext(ctx, uvBin, "venv", venvDir, "--python", pythonBin)
	if out, err := venvCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("tool %q: uv venv failed: %w\n%s", tool.Name, err, out)
	}

	// Step 3: Install the package. Use findPythonInDir on the venv to get the
	// platform-correct python path (venv/bin/python3 on Unix, venv/Scripts/python.exe on Windows).
	venvPython := findPythonInDir(venvDir, tool.PythonVersion)
	if venvPython == "" {
		return nil, fmt.Errorf("tool %q: python binary not found in venv %s", tool.Name, venvDir)
	}
	pipCmd := exec.CommandContext(ctx, uvBin, "pip", "install", "--python", venvPython, tool.PipSpec)
	if out, err := pipCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("tool %q: uv pip install failed: %w\n%s", tool.Name, err, out)
	}

	// Step 4: Create a wrapper script in binDir.
	if err := createWrapper(wrapperPath, venvPython, tool.Name); err != nil {
		return nil, fmt.Errorf("tool %q: creating wrapper: %w", tool.Name, err)
	}

	return &InstallResult{ToolName: tool.Name, BinPath: wrapperPath, Installed: true}, nil
}

// findPythonInDir searches dir for a python3 or python binary, preferring
// an exact version match when pythonVersion is non-empty.
func findPythonInDir(dir, pythonVersion string) string {
	names := []string{"python3", "python"}
	if runtime.GOOS == "windows" {
		for i, n := range names {
			names[i] = n + ".exe"
		}
	}

	// Try version-specific path first: <dir>/cpython-<version>-*/bin/<name>
	if pythonVersion != "" {
		for _, name := range names {
			pattern := filepath.Join(dir, "cpython-"+pythonVersion+"-*", "bin", name)
			matches, _ := filepath.Glob(pattern)
			if len(matches) > 0 {
				return matches[0]
			}
		}
	}

	// Fallback: search any subdirectory.
	for _, name := range names {
		matches, err := filepath.Glob(filepath.Join(dir, "*", "bin", name))
		if err == nil && len(matches) > 0 {
			return matches[0]
		}
		// Also try direct bin/ under the dir.
		p := filepath.Join(dir, "bin", name)
		if _, err := os.Stat(p); err == nil {
			return p
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
		// Prevent path traversal.
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) {
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
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return fmt.Errorf("writing file: %w", err)
			}
			if err := out.Close(); err != nil {
				return fmt.Errorf("closing file: %w", err)
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
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) {
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
		_, err = io.Copy(out, rc)
		_ = rc.Close()
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("writing file: %w", err)
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
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}
