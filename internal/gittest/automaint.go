package gittest

import "os"

// SuppressGitAutoMaintenance makes every git process a test spawns — and
// anything those processes spawn — opt out of git's AUTOMATIC maintenance,
// by injecting `maintenance.auto=false` and `gc.auto=0` through git's
// environment configuration (GIT_CONFIG_COUNT / GIT_CONFIG_KEY_n /
// GIT_CONFIG_VALUE_n).
//
// # Why this exists
//
// Many tests build disposable repositories and then run `git commit` (via
// gittest.Repo.Git, GitCmdInRepo, or a package-local raw helper). Since the
// maintenance-based handoff, git runs `git maintenance run --auto` after
// commit / fetch / merge / rebase, and — because maintenance.autoDetach
// (legacy gc.autoDetach) defaults to true — it DETACHES the child with
// `--detach`. The detached child outlives the command that spawned it and
// writes under the repository's .git/objects: its `gc` task repacks into
// .git/objects/pack/*.{pack,idx,rev} and prunes loose objects, its
// commit-graph task writes .git/objects/info/.
//
// A test's t.TempDir cleanup then races that writer and flakily fails with
//
//	TempDir RemoveAll cleanup: unlinkat .../.git/objects/pack: directory not empty
//
// (observed on Linux CI). The window is short — for a tiny fixture the
// maintenance usually finds nothing to do — so the failure is rare and easy
// to mistake for a scanner/assertion bug; it is a genuine filesystem race
// against a detached process the test never waits for. Disposable test
// repositories never need background maintenance, so the whole test binary
// opts out once, from TestMain (right after MaybeRecordFakeGit):
//
//	func TestMain(m *testing.M) {
//		gittest.MaybeRecordFakeGit()
//		gittest.SuppressGitAutoMaintenance()
//		os.Exit(m.Run())
//	}
//
// Supplying the keys through the ENVIRONMENT — rather than writing them into
// each fixture's .git/config — keeps fixture configs pristine for the
// ScanGitConfig assertions, and still reaches the detached child: the commit
// process reads maintenance.auto before deciding to spawn anything, so with
// it false no maintenance child is ever started. Both keys are inert for the
// rest of the suite (nothing asserts on gc.* / maintenance.*), and both only
// suppress maintenance; nothing is enabled.
func SuppressGitAutoMaintenance() {
	// git's env-config protocol: a COUNT, then KEY_<i>/VALUE_<i> pairs from 0.
	env := []string{
		"GIT_CONFIG_COUNT", "2",
		"GIT_CONFIG_KEY_0", "maintenance.auto",
		"GIT_CONFIG_VALUE_0", "false",
		"GIT_CONFIG_KEY_1", "gc.auto",
		"GIT_CONFIG_VALUE_1", "0",
	}
	for i := 0; i < len(env); i += 2 {
		_ = os.Setenv(env[i], env[i+1])
	}
}
