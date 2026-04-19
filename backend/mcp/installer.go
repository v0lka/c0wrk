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
func InstallCodebaseMemoryMCP(progress ProgressFunc, logger *slog.Logger) (string, error) {
	if logger == nil {
		logger = slog.Default()
	}
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

	logger.Info("downloading codebase-memory-mcp", "url", url)
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
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.Debug("failed to close file during extraction", "error", closeErr)
		}
	}()

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
	if closeErr := out.Close(); closeErr != nil {
		logger.Debug("failed to close file during extraction", "error", closeErr)
	}
	if err != nil {
		progress("error")
		return "", fmt.Errorf("failed to save download: %w", err)
	}

	logger.Info("extracting codebase-memory-mcp")
	progress("installing")

	// Extract archive
	extractDir := filepath.Join(tempDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		progress("error")
		return "", fmt.Errorf("failed to create extract directory: %w", err)
	}

	if goos == "windows" {
		if err := ExtractZip(archivePath, extractDir, logger); err != nil {
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
	logger.Info("running codebase-memory-mcp installer")
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

	logger.Info("codebase-memory-mcp installed", "path", installPath)
	return installPath, nil
}

// EnsureAutoIndex checks the codebase-memory-mcp configuration and enables
// auto_index if it is currently disabled. If the binary is not found, it
// returns (nil, nil) (graceful skip). When auto_index is changed, a restore
// closure is returned that reverts the setting to its original value.
func EnsureAutoIndex(ctx context.Context, loggers ...*slog.Logger) (restore func(), err error) {
	logger := slog.Default()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}

	status := CheckCodebaseMemoryMCP()
	if !status.Installed {
		return nil, nil // not installed, skip gracefully
	}

	binaryPath := status.Path

	// Run "config list" to read current settings
	cmd := exec.CommandContext(ctx, binaryPath, "config", "list")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to read codebase-memory-mcp config: %w", err)
	}

	// Parse the output to find auto_index value
	originalValue := ""
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "auto_index ") && !strings.HasPrefix(line, "auto_index_") {
			// Line format: "auto_index                = false"
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				originalValue = strings.TrimSpace(parts[1])
				if originalValue == "true" {
					logger.Debug("codebase-memory-mcp auto_index already enabled")
					return nil, nil
				}
			}
		}
	}

	// auto_index is not true — enable it
	logger.Info("enabling codebase-memory-mcp auto_index")
	setCmd := exec.CommandContext(ctx, binaryPath, "config", "set", "auto_index", "true")
	if out, err := setCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to enable auto_index: %w, output: %s", err, string(out))
	}

	// Determine the value to restore: use "false" if original was empty or unparseable
	restoreValue := originalValue
	if restoreValue == "" {
		restoreValue = "false"
	}

	logger.Info("codebase-memory-mcp auto_index enabled")
	return func() {
		logger.Info("restoring codebase-memory-mcp auto_index", "value", restoreValue)
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		restoreCmd := exec.CommandContext(rctx, binaryPath, "config", "set", "auto_index", restoreValue)
		if out, err := restoreCmd.CombinedOutput(); err != nil {
			logger.Warn("failed to restore auto_index", "error", err, "output", string(out))
		}
	}, nil
}

// ExtractZip extracts a zip file to the specified directory.
func ExtractZip(src, dest string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := r.Close(); closeErr != nil {
			logger.Debug("failed to close file during extraction", "error", closeErr)
		}
	}()

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
			if closeErr := outFile.Close(); closeErr != nil {
				logger.Debug("failed to close file during extraction", "error", closeErr)
			}
			return err
		}

		_, err = io.Copy(outFile, rc)
		if closeErr := outFile.Close(); closeErr != nil {
			logger.Debug("failed to close file during extraction", "error", closeErr)
		}
		if closeErr := rc.Close(); closeErr != nil {
			logger.Debug("failed to close file during extraction", "error", closeErr)
		}

		if err != nil {
			return err
		}
	}
	return nil
}
