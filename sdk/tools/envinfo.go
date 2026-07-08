package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// EnvInfo holds static environment information collected once at application startup.
// It is passed through context.Context and formatted into agent system prompts
// to reduce unnecessary tool calls for basic environment discovery.
type EnvInfo struct {
	OS            string // e.g., "macOS 15.4 (Darwin 24.4.0)" or "Linux 6.1.0" or "Windows 11"
	Arch          string // e.g., "arm64", "amd64"
	Shell         string // e.g., "/bin/zsh", "/bin/bash"
	HomeDir       string // e.g., "/Users/vkochetkov"
	GoVersion     string // e.g., "1.23.1" or "" if not installed
	NodeVersion   string // e.g., "22.5.0" or "" if not installed
	PythonVersion string // e.g., "3.12.4" or "" if not installed
	DotNetVersion string // e.g., "8.0.100" or "" if not installed
	JavaVersion   string // e.g., "21.0.1" or "" if not installed
	PhpVersion    string // e.g., "8.2.10" or "" if not installed
}

// CollectEnvInfo detects the current environment and returns a populated EnvInfo.
// Runtime version detection uses short timeouts (2s) and returns empty strings on failure.
//
// The external-process probes (OS version and per-runtime version commands)
// run concurrently — each writes to its own dedicated EnvInfo field, so the
// result is deterministic and no synchronization beyond the WaitGroup is
// needed. This bounds the worst-case latency to a single probe timeout (~2s)
// instead of the sum of all probes.
func CollectEnvInfo() *EnvInfo {
	info := &EnvInfo{
		Arch: runtime.GOARCH,
	}

	// Shell
	if runtime.GOOS == "windows" {
		info.Shell = os.Getenv("COMSPEC")
	} else {
		info.Shell = os.Getenv("SHELL")
	}

	// Home directory
	if home, err := os.UserHomeDir(); err == nil {
		info.HomeDir = home
	}

	// External-process probes, run in parallel. Each closure writes to a
	// distinct field of info.
	probes := []func(){
		func() {
			switch runtime.GOOS {
			case "darwin":
				info.OS = detectDarwinOS()
			case "linux":
				kernel := runVersionCmd("uname", "-r")
				if kernel != "" {
					info.OS = "Linux " + kernel
				} else {
					info.OS = "Linux"
				}
			default:
				info.OS = runtime.GOOS
			}
		},
		func() { info.GoVersion = detectGoVersion() },
		func() { info.NodeVersion = detectNodeVersion() },
		func() { info.PythonVersion = detectPythonVersion() },
		func() { info.DotNetVersion = detectDotNetVersion() },
		func() { info.JavaVersion = detectJavaVersion() },
		func() { info.PhpVersion = detectPhpVersion() },
	}

	var wg sync.WaitGroup
	wg.Add(len(probes))
	for _, probe := range probes {
		go func() {
			defer wg.Done()
			probe()
		}()
	}
	wg.Wait()

	return info
}

// detectDarwinOS builds a macOS version string like "macOS 15.4 (Darwin 24.4.0)".
func detectDarwinOS() string {
	productVer := runVersionCmd("sw_vers", "-productVersion")
	darwinVer := runVersionCmd("uname", "-r")

	switch {
	case productVer != "" && darwinVer != "":
		return fmt.Sprintf("macOS %s (Darwin %s)", productVer, darwinVer)
	case productVer != "":
		return "macOS " + productVer
	case darwinVer != "":
		return "Darwin " + darwinVer
	default:
		return "macOS"
	}
}

// detectGoVersion parses "go version go1.23.1 darwin/arm64" → "1.23.1".
func detectGoVersion() string {
	out := runVersionCmd("go", "version")
	if out == "" {
		return ""
	}
	// Expected format: "go version go1.23.1 darwin/arm64"
	parts := strings.Fields(out)
	for _, p := range parts {
		if strings.HasPrefix(p, "go") && len(p) > 2 && p[2] >= '0' && p[2] <= '9' {
			return p[2:] // strip "go" prefix
		}
	}
	return ""
}

// detectNodeVersion parses "v22.5.0" → "22.5.0".
func detectNodeVersion() string {
	out := runVersionCmd("node", "--version")
	if out == "" {
		return ""
	}
	return strings.TrimPrefix(out, "v")
}

// detectPythonVersion tries python3 first, then python, parsing "Python 3.12.4" → "3.12.4".
func detectPythonVersion() string {
	for _, cmd := range []string{"python3", "python"} {
		out := runVersionCmd(cmd, "--version")
		if out == "" {
			continue
		}
		// Expected: "Python 3.12.4"
		parts := strings.Fields(out)
		if len(parts) >= 2 {
			return parts[len(parts)-1]
		}
	}
	return ""
}

// detectDotNetVersion parses "8.0.100" from "dotnet --version" output.
func detectDotNetVersion() string {
	out := runVersionCmd("dotnet", "--version")
	if out == "" {
		return ""
	}
	return strings.TrimSpace(out)
}

// detectJavaVersion parses "21.0.1" from "java -version" output (first line).
// Example: "openjdk version \"21.0.1\" 2023-10-17" or "java version \"1.8.0_391\""
func detectJavaVersion() string {
	out := runVersionCmdCombined("java", "-version")
	if out == "" {
		return ""
	}
	// Take first line
	lines := strings.Split(out, "\n")
	firstLine := strings.TrimSpace(lines[0])
	// Look for version in quotes
	start := strings.Index(firstLine, "\"")
	if start == -1 {
		return ""
	}
	end := strings.Index(firstLine[start+1:], "\"")
	if end == -1 {
		return ""
	}
	version := firstLine[start+1 : start+1+end]
	// Normalize Java version (1.8.x → 8.x)
	version = strings.TrimPrefix(version, "1.")
	return version
}

// detectPhpVersion parses "8.2.10" from "php --version" output (first line).
// Example: "PHP 8.2.10 (cli) (built: Aug 29 2023) ..."
func detectPhpVersion() string {
	out := runVersionCmd("php", "--version")
	if out == "" {
		return ""
	}
	// Take first line
	lines := strings.Split(out, "\n")
	firstLine := strings.TrimSpace(lines[0])
	// Expected: "PHP 8.2.10 ..."
	parts := strings.Fields(firstLine)
	if len(parts) >= 2 && strings.EqualFold(parts[0], "php") {
		return parts[1]
	}
	return ""
}

// runVersionCmd executes a command with a 2-second timeout and returns trimmed stdout.
// Returns "" on any error (not found, timeout, non-zero exit, etc.).
func runVersionCmd(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil

	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(stdout.String())
}

// runVersionCmdCombined executes a command with a 2-second timeout and returns trimmed
// combined stdout+stderr output. Needed for tools like `java -version` that write to stderr.
func runVersionCmdCombined(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}

// envInfoKey is the context key for EnvInfo.
type envInfoKey struct{}

// WithEnvInfo returns a new context with EnvInfo attached.
func WithEnvInfo(ctx context.Context, info *EnvInfo) context.Context {
	return context.WithValue(ctx, envInfoKey{}, info)
}

// EnvInfoFrom extracts EnvInfo from the context. Returns nil if not found.
func EnvInfoFrom(ctx context.Context) *EnvInfo {
	if v, ok := ctx.Value(envInfoKey{}).(*EnvInfo); ok {
		return v
	}
	return nil
}

// EnvFormatOptions controls the formatting of environment info blocks.
type EnvFormatOptions struct {
	// HideHomeDir suppresses the home directory line in the environment block.
	HideHomeDir bool
}

// FormatFullEnvBlock returns a detailed environment block for executor/planner prompts.
// Set opts.HideHomeDir to true to suppress the home directory line (e.g., in CHAT mode).
// Returns "" if info is nil.
func FormatFullEnvBlock(info *EnvInfo, opts EnvFormatOptions) string {
	if info == nil {
		return ""
	}

	now := time.Now()
	zone, offset := now.Zone()
	tzName := now.Location().String()
	tzLabel := formatTimezoneLabel(tzName, zone, offset)

	var b strings.Builder
	b.WriteString("## Environment\n")
	fmt.Fprintf(&b, "- OS: %s\n", info.OS)
	fmt.Fprintf(&b, "- Architecture: %s\n", info.Arch)
	fmt.Fprintf(&b, "- Shell: %s\n", info.Shell)
	if !opts.HideHomeDir {
		fmt.Fprintf(&b, "- Home directory: %s\n", info.HomeDir)
	}
	fmt.Fprintf(&b, "- Date: %s\n", now.Format("2006-01-02"))
	fmt.Fprintf(&b, "- Timezone: %s\n", tzLabel)
	fmt.Fprintf(&b, "- Node.js: %s\n", runtimeOrNotInstalled(info.NodeVersion))
	fmt.Fprintf(&b, "- Python: %s\n", runtimeOrNotInstalled(info.PythonVersion))
	fmt.Fprintf(&b, "- Go: %s\n", runtimeOrNotInstalled(info.GoVersion))
	fmt.Fprintf(&b, "- .NET: %s\n", runtimeOrNotInstalled(info.DotNetVersion))
	fmt.Fprintf(&b, "- Java: %s\n", runtimeOrNotInstalled(info.JavaVersion))
	fmt.Fprintf(&b, "- PHP: %s\n", runtimeOrNotInstalled(info.PhpVersion))

	return b.String()
}

// FormatCompactEnvBlock returns a minimal environment block for evaluator/judge/reflector prompts.
// Includes OS, date, and timezone. Returns "" if info is nil.
func FormatCompactEnvBlock(info *EnvInfo) string {
	if info == nil {
		return ""
	}

	now := time.Now()
	zone, offset := now.Zone()
	tzName := now.Location().String()
	tzLabel := formatTimezoneLabel(tzName, zone, offset)

	var b strings.Builder
	b.WriteString("## Environment\n")
	fmt.Fprintf(&b, "- OS: %s\n", info.OS)
	fmt.Fprintf(&b, "- Date: %s\n", now.Format("2006-01-02"))
	fmt.Fprintf(&b, "- Timezone: %s\n", tzLabel)

	return b.String()
}

// formatTimezoneLabel produces a label like "Europe/Moscow (UTC+3)" or "MST (UTC-7)".
// Offsets with non-zero minutes are rendered as "UTC+5:30" (e.g., India, Nepal).
func formatTimezoneLabel(locName, zone string, offsetSecs int) string {
	absOffset := offsetSecs
	sign := "+"
	if absOffset < 0 {
		sign = "-"
		absOffset = -absOffset
	}
	hours := absOffset / 3600
	minutes := (absOffset % 3600) / 60

	var utcLabel string
	if minutes > 0 {
		utcLabel = fmt.Sprintf("UTC%s%d:%02d", sign, hours, minutes)
	} else {
		utcLabel = fmt.Sprintf("UTC%s%d", sign, hours)
	}

	// If the location name is informative (e.g. "Europe/Moscow"), prefer it.
	// Otherwise fall back to the zone abbreviation (e.g. "MST").
	if locName != "" && locName != "Local" {
		return fmt.Sprintf("%s (%s)", locName, utcLabel)
	}
	return fmt.Sprintf("%s (%s)", zone, utcLabel)
}

// runtimeOrNotInstalled returns the version string or "not installed" if empty.
func runtimeOrNotInstalled(version string) string {
	if version == "" {
		return "not installed"
	}
	return version
}
