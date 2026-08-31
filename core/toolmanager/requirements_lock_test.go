package toolmanager

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// pinLineRe matches a locked requirement line: "name==version" optionally
// followed by an environment marker (universal locks carry per-platform
// markers, e.g. "pyreadline3==3.5.6 ; sys_platform == 'win32'").
var pinLineRe = regexp.MustCompile(`^([A-Za-z0-9_.-]+)==([^\s;]+)`)

// TestMarkitdownLock_FullyPinned verifies the repo-committed requirements lock
// for markitdown: it must pin markitdown itself at exactly the registry
// version, pin at least the full audited dependency tree (46+ packages), and
// contain ONLY exact `==` pins — no floating ranges that could silently
// install unaudited versions.
func TestMarkitdownLock_FullyPinned(t *testing.T) {
	if markitdownLock == "" {
		t.Fatal("embedded markitdownLock is empty; is markitdown_lock.txt committed?")
	}

	pinned := 0
	sawMarkitdownPin := false
	for _, line := range strings.Split(markitdownLock, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.ContainsAny(line, "><~") {
			// Allow comparison operators inside the environment-marker half
			// only; the requirement half must be a pure name==version pin.
			if m := pinLineRe.FindString(line); m == "" {
				t.Errorf("lock line %q is not an exact == pin", line)
				continue
			}
		}
		m := pinLineRe.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("lock line %q is not a name==version pin", line)
			continue
		}
		pinned++
		if m[1] == "markitdown" {
			sawMarkitdownPin = true
			if m[2] != "0.1.4" {
				t.Errorf("lock pins markitdown==%s, want 0.1.4 (registry Version)", m[2])
			}
		}
	}

	if !sawMarkitdownPin {
		t.Error("lock does not pin markitdown itself")
	}
	if pinned < 46 {
		t.Errorf("lock pins only %d packages, want >= 46 (full audited tree)", pinned)
	}
}

// TestWriteRequirementsLock covers lock materialization: the embedded lock is
// written byte-exact to <toolsDir>/<name>-lock.txt, and a tool without a lock
// returns "" (PipSpec fallback) without touching the filesystem.
func TestWriteRequirementsLock(t *testing.T) {
	t.Run("writes lock content", func(t *testing.T) {
		toolsDir := t.TempDir()
		tool := ToolSpec{Name: "fake", RequirementsLock: "a==1.0.0\nb==2.0.0\n"}

		got, err := writeRequirementsLock(tool, toolsDir)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(toolsDir, "fake-lock.txt")
		if got != want {
			t.Fatalf("writeRequirementsLock = %q, want %q", got, want)
		}
		content, err := os.ReadFile(got)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != tool.RequirementsLock {
			t.Errorf("lock file content = %q, want %q", content, tool.RequirementsLock)
		}
	})

	t.Run("empty lock means fallback", func(t *testing.T) {
		toolsDir := t.TempDir()
		tool := ToolSpec{Name: "nolock", PipSpec: "nolock==1.0"}

		got, err := writeRequirementsLock(tool, toolsDir)
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Errorf("writeRequirementsLock = %q, want empty for a lock-less tool", got)
		}
		entries, err := os.ReadDir(toolsDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("toolsDir not empty after fallback: %d entries", len(entries))
		}
	})
}

// TestPipInstallArgs verifies the argument construction: a materialized lock
// installs via `-r <lock>` (exact audited pins); its absence falls back to the
// floating PipSpec.
func TestPipInstallArgs(t *testing.T) {
	t.Run("lock takes precedence", func(t *testing.T) {
		got := pipInstallArgs("/venv/bin/python3", "/tools/fake-lock.txt", "fake[all]==1.0")
		want := []string{"pip", "install", "--python", "/venv/bin/python3", "-r", "/tools/fake-lock.txt"}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("pipInstallArgs = %v, want %v", got, want)
		}
	})

	t.Run("falls back to PipSpec", func(t *testing.T) {
		got := pipInstallArgs("/venv/bin/python3", "", "fake[all]==1.0")
		want := []string{"pip", "install", "--python", "/venv/bin/python3", "fake[all]==1.0"}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("pipInstallArgs = %v, want %v", got, want)
		}
		for _, a := range got {
			if a == "-r" {
				t.Error("fallback args must not contain -r")
			}
		}
	})
}

// fakeUVScript emits a POSIX sh script that mimics the three uv invocations
// InstallPythonPackage performs: `uv python install`, `uv venv`, and
// `uv pip install`. Every invocation is appended (args, one line each) to the
// file named by $UV_CALL_LOG; the python/venv variants create the on-disk
// layout findPythonInDir expects for a managed install and a venv.
const fakeUVScript = `#!/bin/sh
printf '%s\n' "$*" >> "$UV_CALL_LOG"
if [ "$1" = "python" ]; then
  dir=""
  prev=""
  for a in "$@"; do
    if [ "$prev" = "--install-dir" ]; then dir="$a"; fi
    prev="$a"
  done
  mkdir -p "$dir/cpython-3.12.1-test/bin"
  printf '#!/bin/sh\nexit 0\n' > "$dir/cpython-3.12.1-test/bin/python3"
  chmod +x "$dir/cpython-3.12.1-test/bin/python3"
elif [ "$1" = "venv" ]; then
  venv="$2"
  mkdir -p "$venv/bin"
  printf '#!/bin/sh\nexit 0\n' > "$venv/bin/python3"
  chmod +x "$venv/bin/python3"
fi
exit 0
`

// setupFakeUV creates toolsDir/bin/uv as the fake script and returns the path
// of the call log it will append to.
func setupFakeUV(t *testing.T, toolsDir string) string {
	t.Helper()
	binDir := filepath.Join(toolsDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	uvBin := filepath.Join(binDir, "uv")
	if err := os.WriteFile(uvBin, []byte(fakeUVScript), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "uv-calls.log")
	t.Setenv("UV_CALL_LOG", logPath)
	return logPath
}

// TestInstallPythonPackage_UsesRequirementsLock exercises the real
// FSInstaller.InstallPythonPackage against a fake uv binary and asserts the
// lock install path end-to-end: the embedded lock is materialized at
// <toolsDir>/markitdown-lock.txt and the `uv pip install` invocation consumes
// it via `-r` instead of the floating PipSpec.
//
// The fake uv is a POSIX sh script, so this test is skipped on Windows (the
// production .cmd shim differs; CI exercises Linux/macOS only).
func TestInstallPythonPackage_UsesRequirementsLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake uv is a POSIX sh script; Windows would need a .cmd shim")
	}

	toolsDir := t.TempDir()
	logPath := setupFakeUV(t, toolsDir)
	binDir := filepath.Join(toolsDir, "bin")

	tools, err := ManagedTools(nil)
	if err != nil {
		t.Fatal(err)
	}
	var tool ToolSpec
	for _, ts := range tools {
		if ts.Name == "markitdown" {
			tool = ts
			break
		}
	}
	if tool.Name == "" {
		t.Fatal("markitdown not found in ManagedTools(nil)")
	}

	res, err := NewFSInstaller().InstallPythonPackage(context.Background(), tool, toolsDir, binDir)
	if err != nil {
		t.Fatalf("InstallPythonPackage: %v", err)
	}
	if !res.Installed {
		t.Fatal("expected Installed=true for a fresh install")
	}
	if _, err := os.Stat(res.BinPath); err != nil {
		t.Errorf("wrapper not created at %s: %v", res.BinPath, err)
	}

	// The lock must be materialized in toolsDir with byte-exact content.
	lockPath := filepath.Join(toolsDir, "markitdown-lock.txt")
	content, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("requirements lock not materialized: %v", err)
	}
	if string(content) != markitdownLock {
		t.Error("materialized lock content differs from the embedded lock")
	}

	// The `uv pip install` invocation (last logged call) must use -r <lock>.
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(calls) != 3 {
		t.Fatalf("expected 3 uv calls (python install, venv, pip install), got %d: %q", len(calls), calls)
	}
	pipCall := calls[2]
	if !strings.HasPrefix(pipCall, "pip install") {
		t.Fatalf("last call is not a pip install: %q", pipCall)
	}
	if !strings.Contains(pipCall, "-r "+lockPath) {
		t.Errorf("pip call does not install from the lock via -r: %q", pipCall)
	}
	if strings.Contains(pipCall, "markitdown[all]==0.1.4") {
		t.Errorf("pip call must not use the floating PipSpec when a lock exists: %q", pipCall)
	}
}

// TestInstallPythonPackage_FallsBackToPipSpec verifies the documented
// fallback: a PythonPackage tool with an empty RequirementsLock installs from
// its floating PipSpec (no -r, no lock file written).
func TestInstallPythonPackage_FallsBackToPipSpec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake uv is a POSIX sh script; Windows would need a .cmd shim")
	}

	toolsDir := t.TempDir()
	logPath := setupFakeUV(t, toolsDir)
	binDir := filepath.Join(toolsDir, "bin")

	tool := ToolSpec{
		Name:          "lockless",
		BinName:       "lockless",
		PipSpec:       "lockless==1.0",
		PythonVersion: "3.12",
	}

	res, err := NewFSInstaller().InstallPythonPackage(context.Background(), tool, toolsDir, binDir)
	if err != nil {
		t.Fatalf("InstallPythonPackage: %v", err)
	}
	if !res.Installed {
		t.Fatal("expected Installed=true for a fresh install")
	}
	if _, err := os.Stat(filepath.Join(toolsDir, "lockless-lock.txt")); err == nil {
		t.Error("no lock file should be written for a lock-less tool")
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(calls) != 3 {
		t.Fatalf("expected 3 uv calls, got %d: %q", len(calls), calls)
	}
	pipCall := calls[2]
	if !strings.Contains(pipCall, "lockless==1.0") {
		t.Errorf("pip call must fall back to the floating PipSpec: %q", pipCall)
	}
	if strings.Contains(pipCall, "-r") {
		t.Errorf("pip call must not use -r without a lock: %q", pipCall)
	}
}
