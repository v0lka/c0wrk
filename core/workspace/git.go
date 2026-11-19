package workspace

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/v0lka/c0wrk/core/gittrust"
	"github.com/v0lka/c0wrk/internal/sysproc"
)

// unquoteGitPath strips surrounding double-quotes and unescapes C-style escapes
// from a git output path (git quotes paths when core.quotePath is on); unquoted paths pass through.
func unquoteGitPath(path string) string {
	if len(path) >= 2 && path[0] == '"' && path[len(path)-1] == '"' {
		if unquoted, err := strconv.Unquote(path); err == nil {
			return unquoted
		}
	}
	return path
}

// errNotGitRepo reports whether a git failure means "not a repository"
// (stderr match) rather than a real operational error.
func errNotGitRepo(err error, stderr string) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	s := strings.ToLower(stderr)
	return strings.Contains(s, "not a git repository")
}

// GitCmdInRepo returns a hardened git [exec.Cmd] for `git <args...>`: the
// repo-scoped layer on [sysproc.GitCmd] — it scans the repository config fresh
// per invocation, prepends the scan's neutralizing `-c key=value` overrides,
// pins the remote-side transport program on fetch/pull/push/ls-remote spawns,
// and fails closed on unscannable config; cmd.Dir defaults to repoPath. See
// specs/domains/workspace.md (Git Subprocess Hardening) and
// specs/decisions/033-git-subprocess-hardening.md (operation-local gitScanMemo).
func GitCmdInRepo(ctx context.Context, repoPath string, args ...string) (*exec.Cmd, error) {
	scan := newGitScanMemo(repoPath)
	scan.logger, _ = ctx.Value(gitScanLoggerKey{}).(*slog.Logger)
	return gitCmdInRepoScanned(ctx, scan, args...)
}

// gitScanLoggerKey carries an operation-local scanner logger via context.
type gitScanLoggerKey struct{}

// gitScanMemo carries ONE ScanGitConfig through a single logical operation —
// the sanctioned freshness-per-operation narrowing of the no-cache rule (review
// [15]; specs/decisions/033-git-subprocess-hardening.md). Not concurrent-safe.
type gitScanMemo struct {
	logger *slog.Logger
	path   string
	done   bool
	info   *GitConfigInfo
	err    error
}

func newGitScanMemo(repoPath string) *gitScanMemo {
	return &gitScanMemo{path: repoPath}
}

// scanInfo returns the memoized scan, taken at most once per operation — the
// overlay gate and the hardened path's argv derivation share it.
func (m *gitScanMemo) scanInfo() (*GitConfigInfo, error) {
	if !m.done {
		if m.logger != nil {
			m.info, m.err = ScanGitConfig(m.path, m.logger)
		} else {
			m.info, m.err = scanGitConfigFn(m.path)
		}
		if m.err != nil {
			m.err = fmt.Errorf("scanning git config for repo %s (fail closed): %w", m.path, m.err)
		}
		m.done = true
	}
	return m.info, m.err
}

// scanGitConfigFn is the scan seam gitScanMemo goes through; in-package
// tests swap it to observe scan counts. Production always uses ScanGitConfig.
var scanGitConfigFn = func(repoRoot string) (*GitConfigInfo, error) {
	return ScanGitConfig(repoRoot)
}

// trustedRootRawEligible gates a trusted spawn on everything a
// repository-level trust decision can never cover on its own:
//
//   - a plain checkout's config may have drifted since the decision: the
//     spawn re-scans fresh per operation and requires the semantic
//     fingerprint recorded at trust time to still match — any drift evicts
//     the trust (the backend-registered handler removes the config record
//     and re-warns) and the spawn takes the hardened baseline below; a
//     fingerprint-less entry (legacy record awaiting its next-open
//     migration) never spawns raw, and a failing scan refuses the spawn
//     exactly like an untrusted repository. This closes the check-then-act
//     hole where raw git executed a config edited after the trust decision —
//     through an include target the snapshot covered — with no recheck
//     between decision and spawn: intake was the only path that rechecked,
//     so auto-fetch, panel refreshes, and every other GitCmdInRepo caller
//     ran whatever stood in the repository at that moment.
//
//   - a linked worktree's own config.worktree overlay, plantable after the
//     trust decision. Raw git runs from the worktree only while a fresh scan
//     shows the overlay contributing nothing dangerous; anything else — a
//     failed scan included — takes the hardened baseline below. The overlay
//     stays on its own scan gate rather than the fingerprint: the recorded
//     identity covers the MAIN checkout's scan, so comparing it against a
//     linked worktree's source set would evict on every benign per-worktree
//     overlay.
//
// Lives below the commit gate (prompting here cannot re-key the trust
// entry). See specs/decisions/034-git-trust-opt-out.md and
// specs/decisions/070-git-semantic-trust.md.
func trustedRootRawEligible(scan *gitScanMemo, trustedRoot string) bool {
	if ResolveGitTrustRoot(scan.path) == ResolveWorkTreeRoot(scan.path) {
		return plainCheckoutRawEligible(scan, trustedRoot)
	}
	info, err := scan.scanInfo()
	if err != nil {
		return false
	}
	return info.WorktreeOverlayClean()
}

// plainCheckoutRawEligible binds a plain-checkout trusted spawn to the exact
// semantic identity the trust decision recorded: a fresh scan plus include
// resolution — the same pipeline TrustGitRepo ran — must reproduce the
// stored fingerprint, else the trust is evicted and the spawn hardens.
func plainCheckoutRawEligible(scan *gitScanMemo, trustedRoot string) bool {
	info, err := scan.scanInfo()
	if err != nil {
		// Fail closed: the hardened path re-derives the same memoized error
		// and refuses the spawn, exactly as for untrusted repositories.
		return false
	}
	stored, ok := gittrust.SemanticFingerprint(trustedRoot)
	if !ok || stored == "" {
		// Registered without a verifiable identity: the decision cannot be
		// checked against the live config — keep the hardened path.
		return false
	}
	// The identity covers the include targets resolved at trust time, so the
	// same resolution here reads an edited target as drift. ResolveIncludes
	// only appends include sources — findings, records, and the Includes
	// directive list are untouched — so the hardened path's override
	// derivation, which reuses this memoized scan on the mismatch path,
	// derives the same overrides it would from a fresh scan.
	logger := scan.logger
	if logger == nil {
		logger = slog.Default()
	}
	info.ResolveIncludes(logger)
	if current := info.SemanticFingerprint(); current != stored {
		gittrust.EvictDrifted(trustedRoot)
		return false
	}
	return true
}

// remoteTransportPin returns the argv pin that forces git's DEFAULT
// remote-side transport program for the given subcommand, or nil for
// non-transport commands. remote.<n>.uploadpack (fetch/pull/ls-remote) and
// remote.<n>.receivepack (push) are command-bearing config keys the
// neutralizing-argv machinery cannot blanket-cover: there is no
// name-independent -c key, and an include-hidden config may arm a remote
// name the scan never sees (specs/decisions/033-git-subprocess-hardening.md).
// A command-line option beats file config regardless of remote name and
// config layer (verified on git 2.56.0: armed uploadpack/receivepack values
// execute on a local-path remote, and the pins suppress execution), so every
// c0wrk remote-op spawn carries the pin — trust-independently, on both the
// trusted-raw and hardened paths. A caller that explicitly passes its own
// --upload-pack/--receive-pack later in args overrides the pin (git's
// last-option-wins), preserving an escape hatch; the collateral is that a
// trusted repository's custom remote.<n>.uploadpack/receivepack no longer
// applies to c0wrk-issued remote operations.
func remoteTransportPin(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "fetch", "pull", "ls-remote":
		return []string{"--upload-pack=git-upload-pack"}
	case "push":
		return []string{"--receive-pack=git-receive-pack"}
	}
	return nil
}

// withRemoteTransportPin inserts the transport pin right after the
// subcommand (git accepts options among the positionals); args flow through
// unchanged for every other subcommand.
func withRemoteTransportPin(args []string) []string {
	pin := remoteTransportPin(args)
	if pin == nil {
		return args
	}
	pinned := make([]string, 0, len(args)+len(pin))
	pinned = append(pinned, args[0])
	pinned = append(pinned, pin...)
	pinned = append(pinned, args[1:]...)
	return pinned
}

// gitCmdInRepoScanned is GitCmdInRepo over an operation-scoped scan memo;
// the memo's path is the command's working directory.
func gitCmdInRepoScanned(ctx context.Context, scan *gitScanMemo, args ...string) (*exec.Cmd, error) {
	args = withRemoteTransportPin(args)
	// Trusted repository: spawn raw git — no baseline, no neutralizing argv,
	// no GIT_EDITOR pin — so it behaves exactly as outside c0wrk, hooks/
	// filters/signing included. The comparison probes every trust-key form of
	// scan.path (TrustProbePaths: trust root, work-tree root, raw path, each
	// canonical) so subdirectory/root/worktree workspaces of a trusted repo agree
	// with the stored entry.
	trustedRoot := ""
	for _, root := range TrustProbePaths(scan.path) {
		if gittrust.IsTrusted(root) {
			trustedRoot = root
			break
		}
	}
	if trustedRoot != "" && trustedRootRawEligible(scan, trustedRoot) {
		cmd := sysproc.GitCmdRaw(ctx, args...)
		cmd.Dir = scan.path
		// GIT_OPTIONAL_LOCKS=0 is c0wrk's own watcher-loop prevention, not a
		// config neutralization, so it stays on the trusted path too: without
		// it every read-only status/diff refresh opportunistically rewrites
		// .git/index, which the watcher reports as workspace:tree_changed and
		// the UI answers with more git calls — a self-sustaining loop. Only
		// OPTIONAL locks are skipped; the pin layers over os.Environ().
		// See specs/domains/workspace.md (GIT_OPTIONAL_LOCKS=0 pin).
		cmd.Env = pinGitEnv(os.Environ(), "GIT_OPTIONAL_LOCKS", "0")
		return cmd, nil
	}

	info, err := scan.scanInfo()
	if err != nil {
		return nil, err
	}
	argv, err := info.NeutralizingArgvErr()
	if err != nil {
		// A key unrenderable as -c key=value (subsection containing '=') would
		// leave the repository's command armed — refuse the spawn (review [21]).
		return nil, fmt.Errorf("rendering neutralizing overrides for repo %s (fail closed): %w", scan.path, err)
	}
	if info != nil && len(info.Includes) > 0 {
		// Include-bearing configs lean on the attr.tree kill, which git only
		// honors since 2.45 — older git silently ignores it, leaving hidden
		// drivers live: fail closed (specs/domains/workspace.md; gitversion.go).
		if err := requireAttrTreeCapableGit(); err != nil {
			return nil, err
		}
	}
	all := make([]string, 0, len(argv)+len(args))
	all = append(all, argv...)
	all = append(all, args...)
	cmd, err := sysproc.GitCmd(ctx, all...)
	if err != nil {
		return nil, err
	}
	if info.NeedsWorkTreeEnvPin() {
		// core.worktree redirects where checkout/reset write tracked files,
		// and no -c form beats it — GIT_WORK_TREE is the one channel that outranks
		// the key. Pin the discovered work-tree root so writes stay where the user
		// pointed c0wrk; discovery failing with a worktree finding = chain changed
		// under us — fail closed.
		root := ResolveWorkTreeRoot(scan.path)
		if root == "" {
			// Relax ONLY for an include-derived pin with NO core.worktree finding
			// (the bare-repository layout review [95]); a real key never passes unpinned.
			hasWorkTreeFinding := false
			for i := range info.Findings {
				if info.Findings[i].Kind == GitConfigFindingWorkTree {
					hasWorkTreeFinding = true
					break
				}
			}
			if hasWorkTreeFinding || len(info.Includes) == 0 {
				return nil, fmt.Errorf(
					"pinning GIT_WORK_TREE for repo %s (fail closed): core.worktree is set but no work-tree root can be discovered",
					scan.path)
			}
			// Include-derived pin with no discoverable root: a bare repository
			// (review [95]/[148]) — nothing to pin; failing every git operation
			// would break the supported layout. Defensive today: a scanned config implies a discoverable root.
		} else {
			cmd.Env = pinGitEnv(cmd.Env, "GIT_WORK_TREE", root)
		}
	}
	// GIT_OPTIONAL_LOCKS=0: same watcher-loop prevention as the trusted path
	// above; pinGitEnv strips any inherited value first.
	cmd.Env = pinGitEnv(cmd.Env, "GIT_OPTIONAL_LOCKS", "0")
	cmd.Dir = scan.path
	return cmd, nil
}

// pinGitEnv strips any inherited NAME=... entry and appends name=value once:
// glibc's getenv resolves duplicates to the FIRST entry, so appending alone
// is silently void (same reasoning as sysproc's hardenedGitEnv).
func pinGitEnv(env []string, name, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, name+"=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, name+"="+value)
}

// IsGitRepo reports whether dir is inside a git work tree; results are not
// cached. An unscannable config reports "not a repo" (fail closed), but the
// fallbacks stay degraded, not git-free — every fallback still spawns through
// GitCmdInRepo's fresh scan, resurfacing the failure (specs/domains/workspace.md).
func IsGitRepo(ctx context.Context, dir string) bool {
	cmd, err := GitCmdInRepo(ctx, dir, "-C", dir, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// IsGitTracked reports whether relPath is tracked by git in dir.
// Unscannable config counts as not tracked (fail closed, see IsGitRepo).
func IsGitTracked(ctx context.Context, dir, relPath string) bool {
	cmd, err := GitCmdInRepo(ctx, dir, "ls-files", "--error-unmatch", "--", relPath)
	if err != nil {
		return false
	}
	cmd.Dir = dir
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// GitStatus runs git status --porcelain in repoPath and returns a map of
// absolute file paths to entries carrying both index and work-tree status;
// legacy Status/Staged prefer the index side when set.
func GitStatus(ctx context.Context, repoPath string) (map[string]GitStatusEntry, error) {
	return gitStatus(ctx, repoPath, "-uall")
}

// GitStatusLite is GitStatus with untracked directories left collapsed
// (`-unormal`): git never enumerates every untracked file beneath them — the
// untracked directory itself arrives as a `dir/` entry carrying '?' on the
// work-tree axis. This is the file tree's badge source (the panel keeps the
// full enumeration): the tree inherits the untracked verdict down to the
// entries it already lists.
func GitStatusLite(ctx context.Context, repoPath string) (map[string]GitStatusEntry, error) {
	return gitStatus(ctx, repoPath, "-unormal")
}

// gitStatus parses porcelain v1 output; untrackedMode selects the -u flag.
func gitStatus(ctx context.Context, repoPath, untrackedMode string) (map[string]GitStatusEntry, error) {
	// Porcelain v1 paths are relative to the REPOSITORY ROOT, so joining them
	// onto repoPath is only correct at the work-tree root — from a subdirectory
	// workspace entries would key onto non-existent paths and a colliding root-
	// level name would make DiscardChanges revert the WRONG file (review [99]);
	// run from — and key onto — the discovered work-tree root.
	root := repoPath
	if r := ResolveWorkTreeRoot(repoPath); r != "" {
		root = r
	}
	cmd, err := GitCmdInRepo(ctx, root, "status", "--porcelain", untrackedMode)
	if err != nil {
		return nil, err
	}
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errNotGitRepo(err, stderr.String()) {
			return map[string]GitStatusEntry{}, nil
		}
		return nil, fmt.Errorf("git status failed: %w", err)
	}

	result := make(map[string]GitStatusEntry)
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 3 {
			continue
		}
		x := line[0]
		y := line[1]

		rawPath := line[3:]
		if x == 'R' || y == 'R' || x == 'C' || y == 'C' {
			if idx := strings.LastIndex(rawPath, " -> "); idx >= 0 {
				rawPath = rawPath[idx+4:]
			}
		}

		xStatus := porcelainStatus(x)
		yStatus := porcelainStatus(y)

		if x == '?' && y == '?' {
			// Untracked file — not in index, present in work tree.
			path := filepath.Join(root, unquoteGitPath(rawPath))
			result[path] = GitStatusEntry{
				Status:         "A",
				Staged:         false,
				IndexStatus:    "",
				WorkTreeStatus: "?",
			}
			continue
		}

		legacyStatus := yStatus
		legacyStaged := false
		if xStatus != "" {
			legacyStatus = xStatus
			legacyStaged = true
		}

		if legacyStatus == "" {
			continue
		}

		path := filepath.Join(root, unquoteGitPath(rawPath))
		result[path] = GitStatusEntry{
			Status:         legacyStatus,
			Staged:         legacyStaged,
			IndexStatus:    xStatus,
			WorkTreeStatus: yStatus,
		}
	}

	if scanErr := scanner.Err(); scanErr != nil {
		return nil, fmt.Errorf("parsing git status output: %w", scanErr)
	}

	return result, nil
}

// porcelainStatus maps one porcelain status column to its letter string, empty
// for unmodified; 'D' is kept so deletions and DD conflicts reach consumers.
func porcelainStatus(c byte) string {
	switch c {
	case 'M', 'A', 'R', 'C', 'U', 'D':
		return string(c)
	case '?':
		return "?"
	default:
		return ""
	}
}

// GetFileDiff returns the unified diff of uncommitted changes for a file:
// staged + unstaged for tracked files, a --no-index full-file diff for
// untracked/non-git workspaces; prefer GetFileDiffInRepo/NoRepo if known.
func GetFileDiff(ctx context.Context, repoPath, relPath string) (string, error) {
	if !IsGitRepo(ctx, repoPath) {
		return GetFileDiffNoRepo(ctx, repoPath, relPath)
	}
	return GetFileDiffInRepo(ctx, repoPath, relPath)
}

// GetFileDiffInRepo returns the unified diff of uncommitted changes for a file;
// the caller must ensure repoPath is a git repository (untracked files get a
// full-file --no-index diff).
func GetFileDiffInRepo(ctx context.Context, repoPath, relPath string) (string, error) {
	var result strings.Builder

	// Fresh scan per invocation: the review [15] memo is scoped to
	// BuildReviewDiff's multi-spawn operation only.
	staged, err := runGitDiff(ctx, newGitScanMemo(repoPath), true, relPath)
	if err != nil {
		return "", fmt.Errorf("git diff --cached: %w", err)
	}
	result.WriteString(staged)

	unstaged, err := runGitDiff(ctx, newGitScanMemo(repoPath), false, relPath)
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	result.WriteString(unstaged)

	if result.Len() == 0 && !IsGitTracked(ctx, repoPath, relPath) {
		untrackedDiff, untrackedErr := runGitDiffNoIndex(ctx, newGitScanMemo(repoPath), relPath)
		if untrackedErr != nil {
			return "", fmt.Errorf("git diff --no-index: %w", untrackedErr)
		}
		result.WriteString(untrackedDiff)
	}

	return result.String(), nil
}

// GetFileDiffNoRepo produces a full-file diff via git diff --no-index for
// workspaces known not to be repositories; the caller owns that knowledge.
func GetFileDiffNoRepo(ctx context.Context, repoPath, relPath string) (string, error) {
	diff, err := runGitDiffNoIndex(ctx, newGitScanMemo(repoPath), relPath)
	if err != nil {
		return "", fmt.Errorf("git diff --no-index: %w", err)
	}
	return diff, nil
}

// BuildReviewDiff returns the combined unified diff of ALL uncommitted changes
// relative to HEAD — `git diff -U{contextLines} HEAD` reports staged and
// unstaged tracked changes together but omits untracked files (no index entry),
// so each untracked, non-ignored file is appended as a --no-index diff against
// /dev/null. Empty for a clean tree; caller ensures repoPath is a git repo.
func BuildReviewDiff(ctx context.Context, repoPath string, contextLines int) (string, error) {
	var result strings.Builder

	// One scan for the whole operation (review [15] — see
	// specs/decisions/033-git-subprocess-hardening.md); it dies with it.
	scan := newGitScanMemo(repoPath)

	tracked, err := runGitDiffHead(ctx, scan, contextLines)
	if err != nil {
		return "", fmt.Errorf("git diff HEAD: %w", err)
	}
	result.WriteString(tracked)

	untracked, err := listUntrackedFiles(ctx, scan)
	if err != nil {
		return "", fmt.Errorf("listing untracked files: %w", err)
	}
	for _, rel := range untracked {
		d, dErr := runGitDiffNoIndex(ctx, scan, rel)
		if dErr != nil {
			return "", fmt.Errorf("git diff --no-index %s: %w", rel, dErr)
		}
		result.WriteString(d)
	}

	return result.String(), nil
}

// runGitDiffHead runs `git diff --no-ext-diff -U{contextLines} HEAD`: staged
// and unstaged tracked changes in one invocation, untracked files excluded.
// --no-ext-diff keeps external drivers out of the patch output (every
// patch-producing call site passes it — specs/domains/workspace.md).
func runGitDiffHead(ctx context.Context, scan *gitScanMemo, contextLines int) (string, error) {
	cmd, err := gitCmdInRepoScanned(ctx, scan, "diff", "--no-ext-diff", "-U"+strconv.Itoa(contextLines), "HEAD")
	if err != nil {
		return "", err
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git diff HEAD: %w", err)
	}
	return stdout.String(), nil
}

// listUntrackedFiles returns repository-relative paths of untracked files
// (work tree, not index), honoring .gitignore; NUL-delimited, individual files.
func listUntrackedFiles(ctx context.Context, scan *gitScanMemo) ([]string, error) {
	cmd, err := gitCmdInRepoScanned(ctx, scan, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git ls-files failed: %w", err)
	}
	var files []string
	for _, entry := range bytes.Split(stdout.Bytes(), []byte{'\x00'}) {
		rel := string(entry)
		if rel == "" {
			continue
		}
		files = append(files, rel)
	}
	return files, nil
}

// runGitDiff executes `git diff [--cached] -- <path>`; --no-ext-diff keeps
// external diff drivers out of the patch output (see runGitDiffHead).
func runGitDiff(ctx context.Context, scan *gitScanMemo, cached bool, relPath string) (string, error) {
	args := []string{"diff", "--no-ext-diff"}
	if cached {
		args = append(args, "--cached")
	}
	args = append(args, "--", relPath)

	cmd, err := gitCmdInRepoScanned(ctx, scan, args...)
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return stdout.String(), nil
}

// runGitDiffNoIndex diffs an untracked file against /dev/null; exit code 1
// means "differences exist" and is success. --no-ext-diff stays: inside a
// repository --no-index still consults repo config (specs/domains/workspace.md).
func runGitDiffNoIndex(ctx context.Context, scan *gitScanMemo, relPath string) (string, error) {
	cmd, err := gitCmdInRepoScanned(ctx, scan, "diff", "--no-ext-diff", "--no-index", "--", os.DevNull, relPath)
	if err != nil {
		return "", err
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return stdout.String(), nil
		}
		return "", fmt.Errorf("git diff --no-index failed: %w", err)
	}
	return stdout.String(), nil
}

// GitIgnoredPaths returns the absolute paths git ignores in dir. A non-repo
// directory (no filtering) or no ignored paths yields an empty map.
func GitIgnoredPaths(ctx context.Context, dir string) (map[string]bool, error) {
	cmd, err := GitCmdInRepo(ctx, dir, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return nil, err
	}
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errNotGitRepo(err, stderr.String()) {
			return map[string]bool{}, nil // non-git workspace: no filtering
		}
		return nil, fmt.Errorf("git ls-files failed: %w", err)
	}

	output := stdout.Bytes()
	if len(output) == 0 {
		return map[string]bool{}, nil
	}

	result := make(map[string]bool)
	for _, entry := range bytes.Split(output, []byte{'\x00'}) {
		rel := string(entry)
		if rel == "" {
			continue
		}
		rel = strings.TrimSuffix(rel, "/")
		result[filepath.Join(dir, rel)] = true
	}
	return result, nil
}

// FilterGitIgnoredPaths runs one batch `git check-ignore -z --stdin` over the
// given paths and reports which of them git's ignore rules exclude. The cost
// is O(len(paths)) — pure pattern evaluation, no worktree walk — which is
// what lets ListDirectory flag a handful of listed entries instead of paying
// the repo-wide enumeration GitIgnoredPaths performs. Paths are echoed back
// verbatim, so the returned set keys on the input strings. Unlike the
// ls-files enumeration this is pattern-only: a TRACKED path that matches an
// ignore rule is reported too (specs/domains/workspace.md, ignore flags).
// A non-repo root and a "none ignored" run (exit 1) both yield an empty map.
func FilterGitIgnoredPaths(ctx context.Context, root string, paths []string) (map[string]bool, error) {
	if len(paths) == 0 {
		return map[string]bool{}, nil
	}
	cmd, err := GitCmdInRepo(ctx, root, "check-ignore", "-z", "--stdin")
	if err != nil {
		return nil, err
	}
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = pathsReader(paths)

	runErr := cmd.Run()
	if runErr != nil && !isCheckIgnoreNoneIgnored(runErr) {
		if errNotGitRepo(runErr, stderr.String()) {
			return map[string]bool{}, nil // non-git workspace: no filtering
		}
		return nil, fmt.Errorf("git check-ignore failed: %w", runErr)
	}

	result := make(map[string]bool)
	for _, entry := range bytes.Split(stdout.Bytes(), []byte{'\x00'}) {
		if len(entry) > 0 {
			result[string(entry)] = true
		}
	}
	return result, nil
}

// pathsReader feeds paths NUL-separated over stdin (check-ignore -z input
// convention, same as its output separator).
func pathsReader(paths []string) io.Reader {
	var buf bytes.Buffer
	for _, p := range paths {
		buf.WriteString(p)
		buf.WriteByte('\x00')
	}
	return bytes.NewReader(buf.Bytes())
}

// isCheckIgnoreNoneIgnored reports git check-ignore's "no path is ignored"
// exit code (1) — a successful answer, not a failure.
func isCheckIgnoreNoneIgnored(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

// reviewHunkHeaderRe matches a unified-diff hunk header, capturing the
// old/new start and (optional) count; omitted counts default to 1.
var reviewHunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// ParseReviewDiff parses a multi-file unified diff (e.g. `git diff -U5 HEAD`)
// into per-file groups delimited by "diff --git" header lines: the current
// path is the b/ side, OldPath is set only for renames/copies, paths are
// unquoted. Empty non-nil slice for an empty diff; hunk context counts are
// whatever git emitted (the caller's -U flag), not enforced here.
func ParseReviewDiff(diff string) []ReviewFileDiff {
	diff = strings.TrimSpace(diff)
	if diff == "" {
		return []ReviewFileDiff{}
	}

	lines := strings.Split(diff, "\n")
	// A real "diff --git " header has no leading space, distinguishing it
	// from an identical-looking context line inside a hunk body.
	var blockStarts []int
	for i, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			blockStarts = append(blockStarts, i)
		}
	}

	files := make([]ReviewFileDiff, 0, len(blockStarts))
	for idx, start := range blockStarts {
		end := len(lines)
		if idx+1 < len(blockStarts) {
			end = blockStarts[idx+1]
		}
		files = append(files, parseReviewFileBlock(lines[start:end]))
	}
	return files
}

// parseReviewFileBlock parses a single file's diff block (the "diff --git"
// header line plus its preamble and hunks) into a ReviewFileDiff.
func parseReviewFileBlock(lines []string) ReviewFileDiff {
	file := ReviewFileDiff{}
	if len(lines) > 0 {
		oldPath, newPath := parseDiffGitHeader(lines[0])
		file.Path = newPath
		if oldPath != "" && oldPath != newPath {
			file.OldPath = oldPath
		}
	}

	// Non-nil so a hunk-less block (empty file, mode-only change, pure rename)
	// serialises to JSON `[]`, not `null`: the frontend's Array.isArray guard
	// would discard the whole diff array, showing "no changes" spuriously.
	hunks := []ReviewHunk{}
	i := 0
	for i < len(lines) {
		if !strings.HasPrefix(lines[i], "@@") {
			i++
			continue
		}
		m := reviewHunkHeaderRe.FindStringSubmatch(lines[i])
		if m == nil {
			i++
			continue
		}
		oldStart, _ := strconv.Atoi(m[1])
		oldCount := 1
		if m[2] != "" {
			oldCount, _ = strconv.Atoi(m[2])
		}
		newStart, _ := strconv.Atoi(m[3])
		newCount := 1
		if m[4] != "" {
			newCount, _ = strconv.Atoi(m[4])
		}

		start := i
		i++
		for i < len(lines) && !strings.HasPrefix(lines[i], "@@") {
			i++
		}
		hunks = append(hunks, ReviewHunk{
			Raw:      strings.Join(lines[start:i], "\n"),
			OldStart: oldStart,
			OldCount: oldCount,
			NewStart: newStart,
			NewCount: newCount,
		})
	}
	file.Hunks = hunks
	return file
}

// parseDiffGitHeader extracts the a/ and b/ paths from a "diff --git" header
// line, stripping prefixes and unquoting; empty strings on a malformed line.
func parseDiffGitHeader(line string) (oldPath, newPath string) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "diff --git "))
	oldRaw, after, ok := nextDiffPath(rest)
	if !ok {
		return "", ""
	}
	newRaw, _, ok := nextDiffPath(after)
	if !ok {
		return stripDiffPrefix(oldRaw), ""
	}
	return stripDiffPrefix(oldRaw), stripDiffPrefix(newRaw)
}

// nextDiffPath extracts the first path token from s — a double-quoted
// (core.quotePath) or unquoted run up to the next space — returning the
// unquoted token, the rest, and whether one was found.
func nextDiffPath(s string) (token, rest string, ok bool) {
	s = strings.TrimLeft(s, " ")
	if s == "" {
		return "", "", false
	}
	if s[0] == '"' {
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' {
				i++ // skip the escaped character
				continue
			}
			if s[i] == '"' {
				return unquoteGitPath(s[:i+1]), strings.TrimLeft(s[i+1:], " "), true
			}
		}
		return unquoteGitPath(s), "", true
	}
	if idx := strings.IndexByte(s, ' '); idx >= 0 {
		return s[:idx], strings.TrimLeft(s[idx+1:], " "), true
	}
	return s, "", true
}

// stripDiffPrefix removes the leading "a/" or "b/" prefix that git adds to
// paths in the "diff --git" header.
func stripDiffPrefix(p string) string {
	p = strings.TrimPrefix(p, "a/")
	p = strings.TrimPrefix(p, "b/")
	return p
}
