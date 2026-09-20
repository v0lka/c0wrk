package workspace

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// isolateHome redirects HOME (and the derived global git config locations)
// to a scratch directory so assertions on signingGlobal never observe the
// developer's real ~/.gitconfig, and so armed global fixtures are only
// visible to the test that plants them.
//
// It also unsets the environment overrides git (and the detector) steer the
// global config by — XDG_CONFIG_HOME, GIT_CONFIG_GLOBAL, GIT_CONFIG_SYSTEM:
// CI runners export XDG_CONFIG_HOME, which would point the XDG location
// outside the scratch HOME and defeat the isolation. Tests that exercise
// the overrides re-set them with t.Setenv AFTER calling this helper.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{"XDG_CONFIG_HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		if prev, ok := os.LookupEnv(key); ok {
			t.Cleanup(func() { _ = os.Setenv(key, prev) })
			_ = os.Unsetenv(key)
		}
	}
	return home
}

// writeCommitHook plants a hook file with the given mode in hooksDir.
func writeCommitHook(t *testing.T, hooksDir, name string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, name), []byte("#!/bin/sh\ntrue\n"), mode); err != nil {
		t.Fatal(err)
	}
}

func assertHooks(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hooks = %v, want %v", got, want)
	}
}

// TestDetectCommitSuppression_CleanRepo pins the empty baseline: a freshly
// initialized repository has no commit hooks to report — git init seeds the
// hooks directory with executable *.sample templates, which must never
// count — no repo signing (the fixture pins commit.gpgsign=false, exactly
// what c0wrk's own baseline writes), and a clean global config.
func TestDetectCommitSuppression_CleanRepo(t *testing.T) {
	isolateHome(t)
	repo := gittest.InitRepo(t, filepath.Join(t.TempDir(), "repo"), "hello\n")

	hooks, signingRepo, signingGlobal, err := DetectCommitSuppression(repo.Root)
	if err != nil {
		t.Fatalf("DetectCommitSuppression: %v", err)
	}
	assertHooks(t, hooks, nil)
	if signingRepo {
		t.Error("signingRepo = true on a repo with commit.gpgsign=false, want false")
	}
	if signingGlobal {
		t.Error("signingGlobal = true with a clean scratch HOME, want false")
	}

	// The *.sample exclusion is not vacuous: git init really seeds them.
	sample := filepath.Join(repo.GitDir(), "hooks", "pre-commit.sample")
	if _, statErr := os.Stat(sample); statErr != nil {
		t.Skipf("git did not seed %s on this platform; sample exclusion covered by name matching", sample)
	}
}

// TestDetectCommitSuppression_InstalledHooks pins hook detection through
// the default hooks directory: executable commit-family hooks are listed
// sorted, a hook without the exec bit does not count (POSIX-only — Windows
// has no exec bit and detects by presence), and non-commit hooks are out
// of scope.
func TestDetectCommitSuppression_InstalledHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec-bit semantics are POSIX-only; Windows detects by file presence")
	}
	isolateHome(t)
	// Each subtest builds a fresh repository: WriteFile does not change the
	// permissions of an existing file, so sharing one hooks directory would
	// couple the subtests through the modes earlier subtests set.
	newRepo := func(t *testing.T) (root, hooksDir string) {
		t.Helper()
		r := gittest.InitRepo(t, filepath.Join(t.TempDir(), "repo"), "hello\n")
		return r.Root, filepath.Join(r.GitDir(), "hooks")
	}

	t.Run("executable pre-commit detected", func(t *testing.T) {
		root, hooksDir := newRepo(t)
		writeCommitHook(t, hooksDir, "pre-commit", 0o755)
		hooks, _, _, err := DetectCommitSuppression(root)
		if err != nil {
			t.Fatalf("DetectCommitSuppression: %v", err)
		}
		assertHooks(t, hooks, []string{"pre-commit"})
	})

	t.Run("whole commit family sorted", func(t *testing.T) {
		root, hooksDir := newRepo(t)
		writeCommitHook(t, hooksDir, "pre-commit", 0o755)
		writeCommitHook(t, hooksDir, "commit-msg", 0o755)
		writeCommitHook(t, hooksDir, "post-commit", 0o700)
		writeCommitHook(t, hooksDir, "prepare-commit-msg", 0o555)
		hooks, _, _, err := DetectCommitSuppression(root)
		if err != nil {
			t.Fatalf("DetectCommitSuppression: %v", err)
		}
		assertHooks(t, hooks, []string{"commit-msg", "post-commit", "pre-commit", "prepare-commit-msg"})
	})

	t.Run("no exec bit not detected", func(t *testing.T) {
		root, hooksDir := newRepo(t)
		writeCommitHook(t, hooksDir, "commit-msg", 0o644)
		hooks, _, _, err := DetectCommitSuppression(root)
		if err != nil {
			t.Fatalf("DetectCommitSuppression: %v", err)
		}
		assertHooks(t, hooks, nil)
	})

	t.Run("non-commit hook ignored", func(t *testing.T) {
		root, hooksDir := newRepo(t)
		writeCommitHook(t, hooksDir, "pre-push", 0o755)
		hooks, _, _, err := DetectCommitSuppression(root)
		if err != nil {
			t.Fatalf("DetectCommitSuppression: %v", err)
		}
		assertHooks(t, hooks, nil)
	})
}

// TestDetectCommitSuppression_ExecutableSampleIgnored pins the *.sample
// exclusion independently of git init's seeding: an executable file whose
// name merely ends in a commit hook name plus .sample never counts.
func TestDetectCommitSuppression_ExecutableSampleIgnored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture relies on exec bits")
	}
	isolateHome(t)
	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	writeCommitHook(t, filepath.Join(repo.GitDir(), "hooks"), "pre-commit.sample", 0o755)

	hooks, _, _, err := DetectCommitSuppression(root)
	if err != nil {
		t.Fatalf("DetectCommitSuppression: %v", err)
	}
	assertHooks(t, hooks, nil)
}

// TestDetectCommitSuppression_LinkedWorktree pins commondir resolution: a
// hook installed in the main repository's hooks directory is detected when
// scanning from the linked worktree's path, because git runs commit hooks
// from the shared common dir there.
func TestDetectCommitSuppression_LinkedWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture relies on exec bits")
	}
	isolateHome(t)
	repo, wt := worktreeFixture(t)
	writeCommitHook(t, filepath.Join(repo.GitDir(), "hooks"), "pre-commit", 0o755)

	hooks, signingRepo, _, err := DetectCommitSuppression(wt)
	if err != nil {
		t.Fatalf("DetectCommitSuppression(worktree): %v", err)
	}
	assertHooks(t, hooks, []string{"pre-commit"})
	if signingRepo {
		t.Error("signingRepo = true on a fixture repo with gpgsign=false, want false")
	}
}

// TestDetectCommitSuppression_HooksPathMarker pins the core.hooksPath
// contract: the redirect is reported as the single marker entry, the
// configured directory is never listed, and — because git would not read
// it — the repository's own hooks directory is not listed either, even
// with a live executable hook sitting in it.
func TestDetectCommitSuppression_HooksPathMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture relies on exec bits")
	}
	isolateHome(t)
	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	writeCommitHook(t, filepath.Join(repo.GitDir(), "hooks"), "pre-commit", 0o755)
	repo.AppendConfig(t, "[core]\n\thooksPath = /elsewhere/hooks")

	hooks, _, _, err := DetectCommitSuppression(root)
	if err != nil {
		t.Fatalf("DetectCommitSuppression: %v", err)
	}
	assertHooks(t, hooks, []string{CommitSuppressionHooksPathMarker})
}

// TestDetectCommitSuppression_RepoSigning pins the repo signing flag: an
// armed commit.gpgsign (true) raises it, the disabled value the baseline
// itself pins never does, and the sibling signing keys alone — without an
// armed gpgsign — stay inert (they cannot execute anything on commit).
func TestDetectCommitSuppression_RepoSigning(t *testing.T) {
	isolateHome(t)
	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")

	if _, signingRepo, _, err := DetectCommitSuppression(root); err != nil {
		t.Fatalf("DetectCommitSuppression(disabled): %v", err)
	} else if signingRepo {
		t.Error("signingRepo = true with commit.gpgsign=false, want false")
	}

	repo.AppendConfig(t, "[user]\n\tsigningkey = DEADBEEF\n[gpg]\n\tformat = ssh")
	if _, signingRepo, _, err := DetectCommitSuppression(root); err != nil {
		t.Fatalf("DetectCommitSuppression(sibling keys): %v", err)
	} else if signingRepo {
		t.Error("signingRepo = true with only inert sibling signing keys, want false")
	}

	repo.AppendConfig(t, "[commit]\n\tgpgsign = true")
	hooks, signingRepo, signingGlobal, err := DetectCommitSuppression(root)
	if err != nil {
		t.Fatalf("DetectCommitSuppression(armed): %v", err)
	}
	assertHooks(t, hooks, nil)
	if !signingRepo {
		t.Error("signingRepo = false with commit.gpgsign=true, want true")
	}
	if signingGlobal {
		t.Error("signingGlobal = true with a clean scratch HOME, want false")
	}
}

// TestDetectCommitSuppression_GlobalSigning pins the global signing flag
// across both locations git reads (~/.gitconfig and the XDG
// ~/.config/git/config): an armed commit.gpgsign in either raises the flag
// from an otherwise clean repository; a disabled or malformed global
// config does not.
func TestDetectCommitSuppression_GlobalSigning(t *testing.T) {
	if runtime.GOOS == "windows" {
		// HOME is not how the Windows runtime resolves the user dir; the
		// scratch-home isolation this test relies on is POSIX-shaped.
		t.Skip("test isolates HOME, which does not steer os.UserHomeDir on Windows")
	}
	repo := gittest.InitRepo(t, filepath.Join(t.TempDir(), "repo"), "hello\n")

	plantGlobal := func(rel string, ini string) string {
		home := isolateHome(t)
		path := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(ini), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("home gitconfig armed", func(t *testing.T) {
		plantGlobal(".gitconfig", "[commit]\n\tgpgsign = true\n")
		hooks, signingRepo, signingGlobal, err := DetectCommitSuppression(repo.Root)
		if err != nil {
			t.Fatalf("DetectCommitSuppression: %v", err)
		}
		assertHooks(t, hooks, nil)
		if signingRepo {
			t.Error("signingRepo = true from a GLOBAL config key, want false")
		}
		if !signingGlobal {
			t.Error("signingGlobal = false with ~/.gitconfig commit.gpgsign=true, want true")
		}
	})

	t.Run("xdg config armed", func(t *testing.T) {
		plantGlobal(".config/git/config", "[commit]\n\tgpgsign = true\n")
		_, _, signingGlobal, err := DetectCommitSuppression(repo.Root)
		if err != nil {
			t.Fatalf("DetectCommitSuppression: %v", err)
		}
		if !signingGlobal {
			t.Error("signingGlobal = false with ~/.config/git/config commit.gpgsign=true, want true")
		}
	})

	t.Run("disabled global stays clean", func(t *testing.T) {
		plantGlobal(".gitconfig", "[commit]\n\tgpgsign = false\n")
		_, _, signingGlobal, err := DetectCommitSuppression(repo.Root)
		if err != nil {
			t.Fatalf("DetectCommitSuppression: %v", err)
		}
		if signingGlobal {
			t.Error("signingGlobal = true with commit.gpgsign=false, want false")
		}
	})

	t.Run("malformed global treated as clean", func(t *testing.T) {
		plantGlobal(".gitconfig", "[commit\nthis is not git config\n")
		_, _, signingGlobal, err := DetectCommitSuppression(repo.Root)
		if err != nil {
			t.Fatalf("DetectCommitSuppression: %v", err)
		}
		if signingGlobal {
			t.Error("signingGlobal = true with a malformed global config, want false (git itself refuses it)")
		}
	})
}

// TestDetectCommitSuppression_GlobalConfigEnv pins the environment-aware
// global-config resolution (review finding 2): GIT_CONFIG_GLOBAL replaces
// both default locations, XDG_CONFIG_HOME redirects the XDG location away
// from ~/.config, GIT_CONFIG_SYSTEM joins the scan (git reads it on every
// invocation), and an EMPTY GIT_CONFIG_GLOBAL disables the global config
// wholesale — mirroring git's own semantics.
func TestDetectCommitSuppression_GlobalConfigEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test isolates HOME/XDG paths, which do not steer os.UserHomeDir on Windows")
	}
	repo := gittest.InitRepo(t, filepath.Join(t.TempDir(), "repo"), "hello\n")

	plant := func(t *testing.T, path, ini string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(ini), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	scan := func(t *testing.T) bool {
		t.Helper()
		_, _, signingGlobal, err := DetectCommitSuppression(repo.Root)
		if err != nil {
			t.Fatalf("DetectCommitSuppression: %v", err)
		}
		return signingGlobal
	}

	t.Run("git_config_global replaces both defaults", func(t *testing.T) {
		home := isolateHome(t)
		// Both default locations armed: with GIT_CONFIG_GLOBAL set, git does
		// NOT read them, so the flag must stay down.
		plant(t, filepath.Join(home, ".gitconfig"), "[commit]\n\tgpgsign = true\n")
		plant(t, filepath.Join(home, ".config", "git", "config"), "[commit]\n\tgpgsign = true\n")
		custom := filepath.Join(t.TempDir(), "custom-global")
		plant(t, custom, "[commit]\n\tgpgsign = false\n")
		t.Setenv("GIT_CONFIG_GLOBAL", custom)
		if scan(t) {
			t.Error("signingGlobal = true with GIT_CONFIG_GLOBAL pointing at a clean file — the defaults must be ignored")
		}
		// Arming the GIT_CONFIG_GLOBAL file raises the flag.
		plant(t, custom, "[commit]\n\tgpgsign = true\n")
		if !scan(t) {
			t.Error("signingGlobal = false with armed GIT_CONFIG_GLOBAL file, want true")
		}
	})

	t.Run("empty git_config_global disables global config", func(t *testing.T) {
		home := isolateHome(t)
		plant(t, filepath.Join(home, ".gitconfig"), "[commit]\n\tgpgsign = true\n")
		t.Setenv("GIT_CONFIG_GLOBAL", "")
		if scan(t) {
			t.Error("signingGlobal = true with GIT_CONFIG_GLOBAL='' (global config disabled), want false")
		}
	})

	t.Run("xdg_config_home redirects the xdg location", func(t *testing.T) {
		home := isolateHome(t)
		xdgRoot := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", xdgRoot)
		// The redirected location is armed; ~/.config/git/config is NOT
		// read when XDG_CONFIG_HOME is set, and the stale default-location
		// file must not raise the flag on its own.
		plant(t, filepath.Join(xdgRoot, "git", "config"), "[commit]\n\tgpgsign = true\n")
		if !scan(t) {
			t.Error("signingGlobal = false with armed $XDG_CONFIG_HOME/git/config, want true")
		}
		// Moving the arm to the DEFAULT xdg location (which git no longer
		// reads) drops the flag again.
		if err := os.Remove(filepath.Join(xdgRoot, "git", "config")); err != nil {
			t.Fatal(err)
		}
		plant(t, filepath.Join(home, ".config", "git", "config"), "[commit]\n\tgpgsign = true\n")
		if scan(t) {
			t.Error("signingGlobal = true from ~/.config/git/config while XDG_CONFIG_HOME is set — git does not read that file")
		}
	})

	t.Run("git_config_system joins the scan", func(t *testing.T) {
		isolateHome(t)
		system := filepath.Join(t.TempDir(), "system-config")
		plant(t, system, "[commit]\n\tgpgsign = true\n")
		t.Setenv("GIT_CONFIG_SYSTEM", system)
		if !scan(t) {
			t.Error("signingGlobal = false with armed GIT_CONFIG_SYSTEM file, want true")
		}
	})
}

// TestDetectCommitSuppression_NotARepo pins the no-repository contract: a
// path with no discoverable .git yields an empty result and a nil error,
// and the global config is not consulted then — without a repository there
// is no commit surface to suppress.
func TestDetectCommitSuppression_NotARepo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test isolates HOME, which does not steer os.UserHomeDir on Windows")
	}
	home := isolateHome(t)
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[commit]\n\tgpgsign = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hooks, signingRepo, signingGlobal, err := DetectCommitSuppression(t.TempDir())
	if err != nil {
		t.Fatalf("DetectCommitSuppression(not a repo): %v", err)
	}
	assertHooks(t, hooks, nil)
	if signingRepo {
		t.Error("signingRepo = true outside a repository, want false")
	}
	if signingGlobal {
		t.Error("signingGlobal = true outside a repository (global scan must not run), want false")
	}
}

// TestDetectCommitSuppression_EmptyPathErrors pins that an empty path is an
// error (the discovery helper's own contract), not an empty result.
func TestDetectCommitSuppression_EmptyPathErrors(t *testing.T) {
	isolateHome(t)
	if _, _, _, err := DetectCommitSuppression(""); err == nil {
		t.Error("DetectCommitSuppression(\"\") = nil error, want error")
	}
}

// TestDetectCommitSuppression_NoProcessExecution is the structural guard
// for the acceptance criterion: the detector's source must not reference
// process-spawning APIs. It reads the sibling source file as text and
// fails if any spawn entry point appears.
func TestDetectCommitSuppression_NoProcessExecution(t *testing.T) {
	src, err := os.ReadFile("gitcommitsuppress.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{
		`"os/exec"`, "exec.Command", "CommandContext", "cmd.Run", "cmd.Start", "CombinedOutput",
	} {
		if bytes.Contains(src, []byte(banned)) {
			t.Errorf("gitcommitsuppress.go references %q — the detector must never execute processes", banned)
		}
	}
}
