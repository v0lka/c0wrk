// Package gittest builds disposable git repositories seeded with canary
// scripts for the GitSpawn neutralization integration tests.
//
// The threat model under test: a repository that arrives as plain files with
// its .git directory intact (archive, shared drive, USB — `git clone` does
// not transfer .git/config but archive distribution does) can carry
// command-bearing config keys (fsmonitor, hooks, clean/smudge/process
// filters, merge drivers, textconv) that git executes on ordinary read-only
// operations such as status and diff.
//
// Each fixture plants a "canary" — a shell script that appends a line to a
// marker file and then behaves benignly (passthrough for pipeline filters,
// exit 0/1 for hooks and drivers). The tests assert two things for every
// scenario: the marker file never appears (no canary was ever executed) and
// the c0wrk command still returned a usable result (neutralization does not
// break legitimate operation).
//
// The fixture scripts are POSIX shell; every test built on this package
// skips on Windows via RequirePOSIXShell. The package shell-outs to the
// real `git` binary for repository setup only — setup always runs BEFORE any
// hostile config is planted, so no fixture setup can trigger a canary.
package gittest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Canary manages the marker file and executable canary scripts for one test.
type Canary struct {
	// dir holds the canary scripts, outside any repository worktree so a
	// firing canary can never pollute status/diff assertions.
	dir string
	// marker is the file every canary appends to when executed. Its
	// existence after a c0wrk operation means the canary ran.
	marker string
}

// Canary script bodies. Every body appends to the marker file (baked in via
// %s substitution) and then performs a benign role-appropriate action.
const (
	// FilterBody is for clean/smudge/process canaries: record and pass the
	// content through unchanged so an unarmed run would still succeed.
	FilterBody = `#!/bin/sh
printf 'FIRED\n' >> %q
exec cat
`
	// TextconvBody is for diff.textconv canaries: record and passthrough.
	TextconvBody = FilterBody

	// HookBody is for pre-commit-style canaries: record and succeed.
	HookBody = `#!/bin/sh
printf 'FIRED\n' >> %q
exit 0
`

	// MergeDriverBody is for merge.<n>.driver canaries: record and fail
	// (a failing driver makes git record a conflict — visible either way).
	MergeDriverBody = `#!/bin/sh
printf 'FIRED\n' >> %q
exit 1
`

	// FSMonitorBody is for core.fsmonitor canaries: record and exit 0.
	FSMonitorBody = HookBody

	// passthroughBody is a legit filter stand-in (LFS-like): it behaves
	// like a well-behaved identity filter but logs its invocations to its
	// own file so tests can also pin that c0wrk does not execute ANY
	// config-declared filter, legit or not.
	passthroughBody = `#!/bin/sh
printf 'RAN\n' >> %q
exec cat
`
)

// NewCanary creates the canary scaffold in a fresh temp directory. The
// marker file deliberately does not exist yet.
func NewCanary(t *testing.T) *Canary {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "canary")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("gittest: creating canary dir: %v", err)
	}
	return &Canary{dir: dir, marker: filepath.Join(dir, "fired")}
}

// Plant writes an executable canary script with the given role body and
// returns its absolute path (the value planted into .git/config). name also
// becomes the script file name.
func (c *Canary) Plant(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(c.dir, name)
	if err := os.WriteFile(path, []byte(fmt.Sprintf(body, c.marker)), 0o755); err != nil {
		t.Fatalf("gittest: writing canary %s: %v", name, err)
	}
	return path
}

// PlantPassthrough writes a legit (LFS-like) passthrough filter and returns
// its path together with the file its invocations are logged to.
func (c *Canary) PlantPassthrough(t *testing.T, name string) (path, log string) {
	t.Helper()
	path = filepath.Join(c.dir, name)
	log = filepath.Join(c.dir, name+".log")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(passthroughBody, log)), 0o755); err != nil {
		t.Fatalf("gittest: writing passthrough %s: %v", name, err)
	}
	return path, log
}

// Path returns the absolute path of a canary script previously planted via
// Plant (for config values referencing an already-planted script).
func (c *Canary) Path(name string) string {
	return filepath.Join(c.dir, name)
}

// Fired reports whether any canary executed (marker file exists).
func (c *Canary) Fired(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(c.marker)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatalf("gittest: stat canary marker: %v", err)
	}
	return true
}

// Content returns the marker file contents (empty when not fired).
func (c *Canary) Content(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(c.marker)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatalf("gittest: read canary marker: %v", err)
	}
	return string(data)
}

// RequireNotFired fails the test when the marker file exists, including the
// recorded content in the failure message for diagnosis.
func (c *Canary) RequireNotFired(t *testing.T) {
	t.Helper()
	if c.Fired(t) {
		t.Fatalf("canary executed but must never run; marker content:\n%s", c.Content(t))
	}
}

// RequireArmed fails the test when the marker file exists BEFORE the tested
// operation begins — i.e. guards against vacuous fixtures where the canary
// already fired during setup.
func (c *Canary) RequireArmed(t *testing.T) {
	t.Helper()
	// Armed means: no marker yet, ready to detect an execution.
	if c.Fired(t) {
		t.Fatalf("canary fired before the operation under test; marker content:\n%s", c.Content(t))
	}
}

// RequirePOSIXShell skips the test on platforms where the POSIX-sh canary
// scripts cannot run (Windows has no executable bit and no /bin/sh).
func RequirePOSIXShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("canary fixtures use POSIX shell scripts; skipping on Windows")
	}
}

// RequireGit skips the test when no git binary is available.
func RequireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

// Repo is a disposable git repository fixture. All setup git calls run
// against a pristine config; hostile config is planted afterwards via
// AppendConfig.
type Repo struct {
	Root string
}

// InitRepo creates root, runs `git init -b main` in it, sets a fixed commit
// identity, and makes an initial commit containing a single tracked file
// "file.txt" with the given initial content. All of this happens before any
// hostile configuration exists, so setup itself can never fire a canary.
func InitRepo(t *testing.T, root, initialContent string) *Repo {
	t.Helper()
	RequireGit(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("gittest: creating repo dir: %v", err)
	}
	r := &Repo{Root: root}
	r.Git(t, "init", "-b", "main")
	r.Git(t, "config", "user.email", "c0wrk-fixture@example.invalid")
	r.Git(t, "config", "user.name", "c0wrk fixture")
	// Fixture determinism: a machine whose global config enables commit
	// signing (or any other environment-sensitive commit knob) must not
	// break fixture commits — repo-local keys beat the global ones. The
	// extra key is inert for the scanner (commit.* is not a command-bearing
	// section family it parses).
	r.Git(t, "config", "commit.gpgsign", "false")
	r.Write(t, "file.txt", initialContent)
	r.Git(t, "add", ".")
	r.Git(t, "commit", "-m", "initial")
	return r
}

// GitDir returns the repository's .git directory path.
func (r *Repo) GitDir() string {
	return filepath.Join(r.Root, ".git")
}

// GitDirFile returns the path of a file inside .git (config, hooks/...).
func (r *Repo) GitDirFile(rel string) string {
	return filepath.Join(r.GitDir(), filepath.FromSlash(rel))
}

// Write creates or overwrites a worktree file.
func (r *Repo) Write(t *testing.T, rel, content string) {
	t.Helper()
	path := filepath.Join(r.Root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("gittest: mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("gittest: writing %s: %v", rel, err)
	}
}

// AppendConfig appends an INI fragment to .git/config. This is the
// "config planting" primitive: call it only after repository setup is
// complete so setup commands never observe the hostile keys.
func (r *Repo) AppendConfig(t *testing.T, iniFragment string) {
	t.Helper()
	f, err := os.OpenFile(r.GitDirFile("config"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("gittest: opening .git/config: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString("\n" + strings.TrimSpace(iniFragment) + "\n"); err != nil {
		t.Fatalf("gittest: appending .git/config: %v", err)
	}
}

// Git runs a setup git command in the repository and fails the test on
// error. Setup-only: never call this after planting hostile config unless
// the command is intentionally exercised against it.
func (r *Repo) Git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = r.Root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("gittest: git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}
