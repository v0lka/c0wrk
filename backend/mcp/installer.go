// Package mcp provides MCP-related backend services.
package mcp

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ProgressFunc is called with status strings during installation
// (e.g. "downloading", "installing", "configuring", "done", "error").
type ProgressFunc func(status string)

// CodeMemoryStatus represents the installation status of codebase-memory-mcp.
type CodeMemoryStatus struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path"`
}

// CheckCodebaseMemoryMCP checks if codebase-memory-mcp is installed and returns its status.
func CheckCodebaseMemoryMCP() CodeMemoryStatus {
	// Try exec.LookPath first
	if path, err := exec.LookPath("codebase-memory-mcp"); err == nil {
		return CodeMemoryStatus{Installed: true, Path: path}
	}
	// Check common install location
	home, _ := os.UserHomeDir()
	localPath := filepath.Join(home, ".local", "bin", "codebase-memory-mcp")
	if _, err := os.Stat(localPath); err == nil {
		return CodeMemoryStatus{Installed: true, Path: localPath}
	}
	return CodeMemoryStatus{}
}

// InstallCodebaseMemoryMCP downloads and installs the codebase-memory-mcp binary.
// The progress callback is called with status updates; it may be nil.
// Returns the install path on success.
func InstallCodebaseMemoryMCP(progress ProgressFunc) (string, error) {
	if progress == nil {
		progress = func(string) {} // noop
	}

	// Determine OS/arch
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	// Map Go arch to release arch
	arch := goarch
	if goarch == "amd64" {
		arch = "x86_64"
	}

	// Determine file extension
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}

	// Build download URL
	url := fmt.Sprintf("https://github.com/DeusData/codebase-memory-mcp/releases/latest/download/codebase-memory-mcp-%s-%s.%s", goos, arch, ext)

	slog.Info("downloading codebase-memory-mcp", "url", url)
	progress("downloading")

	// Create temp directory
	tempDir, err := os.MkdirTemp("", "codebase-memory-mcp-*")
	if err != nil {
		progress("error")
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	// Download file
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if err != nil {
		progress("error")
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		progress("error")
		return "", fmt.Errorf("failed to download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		progress("error")
		return "", fmt.Errorf("download failed with status: %s", resp.Status)
	}

	// Save to temp file
	archivePath := filepath.Join(tempDir, "codebase-memory-mcp."+ext)
	out, err := os.Create(archivePath)
	if err != nil {
		progress("error")
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	_, err = io.Copy(out, resp.Body)
	_ = out.Close()
	if err != nil {
		progress("error")
		return "", fmt.Errorf("failed to save download: %w", err)
	}

	slog.Info("extracting codebase-memory-mcp")
	progress("installing")

	// Extract archive
	extractDir := filepath.Join(tempDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		progress("error")
		return "", fmt.Errorf("failed to create extract directory: %w", err)
	}

	if goos == "windows" {
		if err := ExtractZip(archivePath, extractDir); err != nil {
			progress("error")
			return "", fmt.Errorf("failed to extract zip: %w", err)
		}
	} else {
		cmd := exec.CommandContext(context.Background(), "tar", "xzf", archivePath, "-C", extractDir)
		if output, err := cmd.CombinedOutput(); err != nil {
			progress("error")
			return "", fmt.Errorf("failed to extract tar.gz: %w, output: %s", err, string(output))
		}
	}

	// Run installer
	slog.Info("running codebase-memory-mcp installer")
	var installCmd *exec.Cmd
	if goos == "windows" {
		installCmd = exec.CommandContext(context.Background(), "powershell", "-File", "install.ps1", "-SkipConfig")
	} else {
		installCmd = exec.CommandContext(context.Background(), "./install.sh", "--skip-config")
	}
	installCmd.Dir = extractDir
	if output, err := installCmd.CombinedOutput(); err != nil {
		progress("error")
		return "", fmt.Errorf("installer failed: %w, output: %s", err, string(output))
	}

	// Verify installation
	var installPath string
	if path, err := exec.LookPath("codebase-memory-mcp"); err == nil {
		installPath = path
	} else {
		home, _ := os.UserHomeDir()
		localPath := filepath.Join(home, ".local", "bin", "codebase-memory-mcp")
		if _, err := os.Stat(localPath); err == nil {
			installPath = localPath
		} else {
			progress("error")
			return "", errors.New("installation verification failed: binary not found after install")
		}
	}

	slog.Info("codebase-memory-mcp installed", "path", installPath)
	return installPath, nil
}

// ExtractZip extracts a zip file to the specified directory.
func ExtractZip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)

		// Check for ZipSlip
		if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path: %s", fpath)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(fpath, f.Mode()); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(fpath), 0o755); err != nil {
			return err
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			_ = outFile.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		_ = outFile.Close()
		_ = rc.Close()

		if err != nil {
			return err
		}
	}
	return nil
}
