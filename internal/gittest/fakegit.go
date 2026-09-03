package gittest

// Fake-git harness, shared by the hardening tests that pin the exact argv
// and environment a c0wrk git layer executes. The stand-in "git" is the test
// binary itself (the stdlib helper-process pattern): a copy of it placed on
// PATH re-enters the package's TestMain in fake mode when
// C0WRK_GITTEST_FAKE_GIT=1 is present in the environment (t.Setenv in the
// parent test is inherited by the child), appends its argv and environment
// to the log file named by C0WRK_GITTEST_FAKE_GIT_LOG, and exits 0. This
// works on every platform, including Windows where shell scripts are not
// executable.
//
// Usage per package (TestMain cannot be shared):
//
//	func TestMain(m *testing.M) {
//		gittest.MaybeRecordFakeGit()
//		os.Exit(m.Run())
//	}

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/internal/sysproc"
)

const (
	// FakeGitGateEnv switches the test binary into fake-git mode.
	FakeGitGateEnv = "C0WRK_GITTEST_FAKE_GIT"
	// FakeGitLogEnv names the file fake-git invocations are logged to.
	FakeGitLogEnv = "C0WRK_GITTEST_FAKE_GIT_LOG"
	// SentinelEnvName is the environment sentinel tests set to prove the
	// child inherited the parent environment through the code under test.
	SentinelEnvName = "C0WRK_GITTEST_SENTINEL"
)

// MaybeRecordFakeGit turns the test binary into the fake git recorder and
// exits when C0WRK_GITTEST_FAKE_GIT=1; otherwise it returns so the normal
// test run proceeds. Call it first in the package's TestMain.
func MaybeRecordFakeGit() {
	if os.Getenv(FakeGitGateEnv) != "1" {
		return
	}
	recordFakeGit()
	os.Exit(0)
}

// recordFakeGit appends this invocation's argv and environment to the log
// file named by FakeGitLogEnv, under ARGV and ENV section headers.
func recordFakeGit() {
	logPath := os.Getenv(FakeGitLogEnv)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(3)
	}
	defer func() { _ = f.Close() }() // best-effort diagnostic logging
	buf := &strings.Builder{}
	buf.WriteString("ARGV\n")
	// os.Args[0] is the resolved executable path; record its base name so
	// assertions can pin that the executed binary is "git".
	buf.WriteString(filepath.Base(os.Args[0]) + "\n")
	for _, a := range os.Args[1:] {
		buf.WriteString(a + "\n")
	}
	buf.WriteString("ENV\n")
	for _, e := range os.Environ() {
		buf.WriteString(e + "\n")
	}
	_, _ = f.WriteString(buf.String())
}

// InstallFakeGit puts the fake git stand-in on PATH for the duration of the
// test and returns a function that reads the recorded lines back.
func InstallFakeGit(t *testing.T) func() []string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "fakegit.log")
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("gittest: mkdir fake bin dir: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("gittest: locating test binary: %v", err)
	}
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("gittest: reading test binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, name), data, 0o755); err != nil {
		t.Fatalf("gittest: writing fake git: %v", err)
	}
	t.Setenv(FakeGitGateEnv, "1")
	t.Setenv(FakeGitLogEnv, logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("gittest: reading fake git log: %v", err)
		}
		return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
}

// SectionLines returns the lines recorded under the given header ("ARGV" or
// "ENV") by recordFakeGit.
func SectionLines(lines []string, header string) []string {
	var out []string
	in := false
	for _, l := range lines {
		switch {
		case l == header:
			in = true
		case in && (l == "ARGV" || l == "ENV"):
			return out
		case in:
			out = append(out, l)
		}
	}
	return out
}

// AssertHardenedInvocation pins a recorded invocation of a hardened git
// layer: the argv must be [git, safety overrides…, layerArgs…] with a valid
// safe hooks dir, and the environment must preserve the parent environment
// (the SentinelEnvName sentinel, which the test must set via t.Setenv) and
// carry GIT_EDITOR=true.
func AssertHardenedInvocation(t *testing.T, lines []string, layerArgs ...string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("gittest: resolving home dir: %v", err)
	}
	wantHooksDir := filepath.Join(home, sysproc.DefaultAgentDirName, filepath.FromSlash(sysproc.GitSafeHooksSegment))

	argv := SectionLines(lines, "ARGV")
	want := slices.Concat(
		[]string{"git", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + wantHooksDir, "-c", "commit.gpgsign=false"},
		layerArgs,
	)
	if !slices.Equal(argv, want) {
		t.Errorf("recorded argv = %q, want %q", argv, want)
	}
	if st, err := os.Stat(wantHooksDir); err != nil || !st.IsDir() {
		t.Errorf("hooksPath override %s is not an existing directory: %v", wantHooksDir, err)
	}

	env := SectionLines(lines, "ENV")
	if !slices.Contains(env, "GIT_EDITOR=true") {
		t.Error("recorded env missing GIT_EDITOR=true")
	}
	if !slices.Contains(env, SentinelEnvName+"=1") {
		t.Errorf("recorded env lost the parent environment (%s=1 missing)", SentinelEnvName)
	}
}
