// Package rtk provides RTK CLI tool integration for compressing command output for LLM agents.
package rtk

import (
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

	"github.com/user/agent/backend/mcp"
)

// ProgressFunc is called with status strings during installation
// (e.g. "downloading", "installing", "done", "error").
type ProgressFunc func(status string)

// RtkStatus represents the installation status of the rtk CLI tool.
type RtkStatus struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path"`
	Version   string `json:"version"`
}

// CheckRtk checks if the rtk CLI tool is installed and returns its status.
func CheckRtk() RtkStatus {
	// Try exec.LookPath first
	if path, err := exec.LookPath("rtk"); err == nil {
		version := captureVersion(path)
		return RtkStatus{Installed: true, Path: path, Version: version}
	}
	// Check common install location
	home, _ := os.UserHomeDir()
	localPath := filepath.Join(home, ".local", "bin", "rtk")
	if _, err := os.Stat(localPath); err == nil {
		version := captureVersion(localPath)
		return RtkStatus{Installed: true, Path: localPath, Version: version}
	}
	return RtkStatus{}
}

// captureVersion runs `<path> --version` with a 2s timeout and returns the parsed version string.
func captureVersion(binPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "--version")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// InstallRtk downloads and installs the rtk CLI binary.
// The progress callback is called with status updates; it may be nil.
// Returns the install path on success.
func InstallRtk(progress ProgressFunc) (string, error) {
	if progress == nil {
		progress = func(string) {} // noop
	}

	url, ext, err := buildDownloadURL(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		progress("error")
		return "", err
	}

	slog.Info("downloading rtk", "url", url)
	progress("downloading")

	// Create temp directory
	tempDir, err := os.MkdirTemp("", "rtk-*")
	if err != nil {
		progress("error")
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	// Temp dir removal error is non-critical; safe to ignore.
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
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Debug("failed to close file during extraction", "error", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		progress("error")
		return "", fmt.Errorf("download failed with status: %s", resp.Status)
	}

	// Save to temp file
	archivePath := filepath.Join(tempDir, "rtk."+ext)
	out, err := os.Create(archivePath)
	if err != nil {
		progress("error")
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	_, err = io.Copy(out, resp.Body)
	if closeErr := out.Close(); closeErr != nil {
		slog.Debug("failed to close file during extraction", "error", closeErr)
	}
	if err != nil {
		progress("error")
		return "", fmt.Errorf("failed to save download: %w", err)
	}

	slog.Info("extracting rtk")
	progress("installing")

	// Extract archive
	extractDir := filepath.Join(tempDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		progress("error")
		return "", fmt.Errorf("failed to create extract directory: %w", err)
	}

	if ext == "zip" {
		if err := mcp.ExtractZip(archivePath, extractDir); err != nil {
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

	// Find the rtk binary in the extracted directory
	binaryName := "rtk"
	if runtime.GOOS == "windows" {
		binaryName = "rtk.exe"
	}
	srcBinary, err := findBinary(extractDir, binaryName)
	if err != nil {
		progress("error")
		return "", fmt.Errorf("failed to find rtk binary in archive: %w", err)
	}

	// Copy binary to ~/.local/bin/rtk
	home, err := os.UserHomeDir()
	if err != nil {
		progress("error")
		return "", fmt.Errorf("failed to determine home directory: %w", err)
	}
	destDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		progress("error")
		return "", fmt.Errorf("failed to create install directory: %w", err)
	}
	destPath := filepath.Join(destDir, binaryName)

	if err := copyFile(srcBinary, destPath); err != nil {
		progress("error")
		return "", fmt.Errorf("failed to copy rtk binary: %w", err)
	}
	if err := os.Chmod(destPath, 0o755); err != nil {
		progress("error")
		return "", fmt.Errorf("failed to set executable permission: %w", err)
	}

	// Verify installation
	var installPath string
	if path, err := exec.LookPath("rtk"); err == nil {
		installPath = path
	} else if _, err := os.Stat(destPath); err == nil {
		installPath = destPath
	} else {
		progress("error")
		return "", errors.New("installation verification failed: binary not found after install")
	}

	slog.Info("rtk installed", "path", installPath)
	return installPath, nil
}

// buildDownloadURL maps OS/arch to the RTK release download URL.
// Returns the URL, file extension, and any error.
func buildDownloadURL(goos, goarch string) (url, ext string, err error) {
	// Map Go arch to release arch
	arch := goarch
	switch goarch {
	case "arm64":
		arch = "aarch64"
	case "amd64":
		arch = "x86_64"
	}

	// Determine target triple
	var target string
	switch {
	case goos == "darwin" && arch == "aarch64":
		target = "aarch64-apple-darwin"
	case goos == "darwin" && arch == "x86_64":
		target = "x86_64-apple-darwin"
	case goos == "linux" && arch == "x86_64":
		target = "x86_64-unknown-linux-musl"
	case goos == "linux" && arch == "aarch64":
		target = "aarch64-unknown-linux-gnu"
	case goos == "windows" && arch == "x86_64":
		target = "x86_64-pc-windows-msvc"
	default:
		return "", "", fmt.Errorf("unsupported platform: %s/%s", goos, goarch)
	}

	ext = "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}

	url = fmt.Sprintf("https://github.com/rtk-ai/rtk/releases/latest/download/rtk-%s.%s", target, ext)
	return url, ext, nil
}

// findBinary walks the directory tree to find the named binary.
func findBinary(dir, name string) (string, error) {
	var found string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.Name() == name {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("binary %q not found in %s", name, dir)
	}
	return found, nil
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := in.Close(); closeErr != nil {
			slog.Debug("failed to close file during extraction", "error", closeErr)
		}
	}()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); closeErr != nil {
			slog.Debug("failed to close file during extraction", "error", closeErr)
		}
	}()

	_, err = io.Copy(out, in)
	return err
}
