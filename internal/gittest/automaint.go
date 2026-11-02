package gittest

import "os"

// SuppressGitAutoMaintenance makes every spawned git process — and its
// children — opt out of git's automatic maintenance via env config
// (GIT_CONFIG_COUNT / GIT_CONFIG_KEY_n / GIT_CONFIG_VALUE_n). Since the
// maintenance-based handoff, commit/fetch/merge/rebase run
// `git maintenance run --auto`, and with autoDetach defaulting true the
// child DETACHES and writes under .git/objects, racing t.TempDir cleanup
// into flaky "directory not empty" failures (Linux CI; short window, easy
// to mistake for an assertion bug). Disposable repos never need background
// maintenance — call once from TestMain, right after MaybeRecordFakeGit.
// The keys ride the environment, not fixture .git/config, so configs stay
// pristine for ScanGitConfig assertions; the commit process reads
// maintenance.auto before spawning anything, and both keys only suppress
// maintenance.
// See specs/domains/testing.md.
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
