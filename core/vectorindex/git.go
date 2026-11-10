package vectorindex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/v0lka/c0wrk/core/workspace"
)

const (
	// DefaultBranch is used when the directory is not a git repository.
	DefaultBranch = "default"

	// gitHeadDebounce is the debounce interval for .git/HEAD change events.
	// Git operations may touch HEAD multiple times in quick succession.
	gitHeadDebounce = 300 * time.Millisecond
)

// GitMonitor watches .git/HEAD for branch changes and calls onChange
// when the branch actually changes.
type GitMonitor struct {
	repoPath      string
	currentBranch string
	watcher       *fsnotify.Watcher
	onChange      func(newBranch string)
	done          chan struct{}
	// loopDone is closed by eventLoop on EVERY return path, so Stop can join
	// the loop instead of leaving a goroutine that may still arm the debounce
	// timer.
	loopDone chan struct{}
	// loopStarted records that Start actually spawned eventLoop: Stop joins
	// loopDone only then (a never-started monitor's loopDone would never
	// close).
	loopStarted atomic.Bool
	// debounceMu guards timer and stopped, and is deliberately NOT the
	// manager mutex: Stop joins loopDone while its CALLER holds the manager
	// mutex and the event loop arms the timer — sharing it would deadlock
	// that join against a timer arm.
	debounceMu sync.Mutex
	timer      *time.Timer
	stopped    bool
	mu         sync.Mutex
	logger     *slog.Logger
}

// CurrentBranch returns the current git branch name via the system git CLI,
// DefaultBranch for non-git directories. Unexpected git errors propagate —
// no silent fallback.
func CurrentBranch(ctx context.Context, repoPath string) (string, error) {
	// Fast-path: explicitly non-git directory. ErrNotExist alone is not
	// ".git absent": a repoPath that is a regular FILE stats as ENOTDIR on
	// POSIX but as ErrNotExist on Windows (see traversesNonDirectory), and
	// trusting the Windows shape would hand a broken workspace path a
	// DefaultBranch instead of the git failure the caller must surface.
	gitDir := filepath.Join(repoPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) && !traversesNonDirectory(err, gitDir) {
		return DefaultBranch, nil
	}

	// Normal branch HEAD — this is the common case.
	if out, err := runGit(ctx, repoPath, "symbolic-ref", "--short", "HEAD"); err == nil {
		return strings.TrimSpace(out), nil
	}

	// Detached HEAD: fall back to a short commit hash as branch identifier.
	out, err := runGit(ctx, repoPath, "rev-parse", "--short=12", "HEAD")
	if err != nil {
		return "", fmt.Errorf("detecting branch via git: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// CurrentCommit returns the commit OID prefix (16 hex chars) of repoPath's
// HEAD, or "" for a non-git directory. The OID — not the branch name — is
// the snapshot identity of the shared corpus (ADR-104 §1 L3): every branch
// and worktree checked out at the same commit maps onto one corpus, a
// detached HEAD is first-class, and branch-name sanitizer collisions cannot
// fork storage identity.
func CurrentCommit(ctx context.Context, repoPath string) (string, error) {
	gitDir := filepath.Join(repoPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return "", nil
	}
	out, err := runGit(ctx, repoPath, "rev-parse", "HEAD")
	if err != nil {
		// An unborn branch (git init, zero commits) resolves no HEAD object
		// yet while its symref is live: a normal working state the index
		// serves through the root-owned synthetic snapshot (oid == ""), so
		// it is "", nil — not a terminal error.
		if isUnbornHead(ctx, repoPath) {
			return "", nil
		}
		return "", fmt.Errorf("detecting commit via git: %w", err)
	}
	oid := strings.TrimSpace(out)
	if len(oid) > 16 {
		oid = oid[:16]
	}
	return oid, nil
}

// branchRefExists reports whether the named refs/heads/<branch> ref exists
// in ref storage (loose or packed), WITHOUT resolving its object: git's own
// ref-listing plumbing (show-ref --verify, for-each-ref) parses the object
// and fails on a damaged store — exactly the state the caller must
// distinguish from an absent ref. Branch refs live in the repository's
// COMMON git directory; a linked worktree's private dir names it in its
// `common-dir` file (relative paths resolve against the private dir).
// Reftable-backend repositories carry neither a loose file nor packed-refs;
// for the unborn question that reads as "ref absent" — conservative only in
// the already-failing state that reached this check.
func branchRefExists(repoPath, ref string) bool {
	gitDir, err := resolveGitDir(repoPath)
	if err != nil || gitDir == "" {
		return false
	}
	commonDir := gitDir
	if data, rerr := os.ReadFile(filepath.Join(gitDir, "common-dir")); rerr == nil {
		if cd := strings.TrimSpace(string(data)); cd != "" {
			if filepath.IsAbs(cd) {
				commonDir = cd
			} else {
				commonDir = filepath.Join(gitDir, cd)
			}
		}
	}
	name := strings.TrimPrefix(ref, "refs/heads/")
	if _, serr := os.Stat(filepath.Join(commonDir, "refs", "heads", filepath.FromSlash(name))); serr == nil {
		return true
	}
	data, perr := os.ReadFile(filepath.Join(commonDir, "packed-refs"))
	if perr != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "#") {
			continue // peeled/header comment lines
		}
		if strings.HasSuffix(line, " "+ref) {
			return true
		}
	}
	return false
}

// isUnbornHead reports whether repoPath's HEAD is an unborn branch: a
// symref whose target branch ref does not exist yet (git init with zero
// commits, a fresh orphan checkout). `rev-parse HEAD` fails on such a tree
// while `symbolic-ref HEAD` still succeeds AND the target ref is absent
// from ref storage — the ref-existence clause distinguishes "no commits
// yet" from a damaged clone (a ref that exists while its object is
// missing), which must stay on CurrentCommit's error path instead of
// silently degrading to the root-owned synthetic snapshot.
func isUnbornHead(ctx context.Context, repoPath string) bool {
	ref, err := runGit(ctx, repoPath, "symbolic-ref", "HEAD")
	if err != nil {
		return false
	}
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, "refs/heads/") {
		return false
	}
	return !branchRefExists(repoPath, ref)
}

// ListLocalBranches lists the repository's local branch names (refs/heads —
// the set every worktree of the clone shares, checked-out branches included).
// An empty result is valid: a repository without refs keeps nothing. It is
// the identity source of the unified store migration's branch resolution
// (layout.go): every legacy branch store must map back to a live ref before
// its commit OID — and therefore its snapshot corpus key — can be derived.
func ListLocalBranches(ctx context.Context, repoPath string) ([]string, error) {
	out, err := runGit(ctx, repoPath, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil, fmt.Errorf("listing local branches: %w", err)
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// CommitOfBranch resolves a local branch's current commit OID (16 hex
// chars), the key the unified store migration promotes a legacy branch
// store under (ADR-104 §1 L3: identity is the commit, never the branch
// name). A missing branch is an error, not "": callers distinguish
// structurally-absent branches from transient git failures by resolving the
// branch list first.
func CommitOfBranch(ctx context.Context, repoPath, branch string) (string, error) {
	out, err := runGit(ctx, repoPath, "rev-parse", "--verify", branch+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolving commit of branch %q: %w", branch, err)
	}
	oid := strings.TrimSpace(out)
	if len(oid) > 16 {
		oid = oid[:16]
	}
	return oid, nil
}

// runGit runs `git -C repoPath <args...>` and returns trimmed stdout. Every
// invocation is routed through workspace.GitCmdInRepo — the hardened choke
// point (fresh per-repo config scan + neutralizing -c overrides, fail-closed
// on an unscannable config): see specs/decisions/033-git-subprocess-hardening.md.
// Errors include stderr for diagnosability.
func runGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	full := make([]string, 0, len(args)+2)
	full = append(full, "-C", repoPath)
	full = append(full, args...)

	cmd, err := workspace.GitCmdInRepo(ctx, repoPath, full...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

// NewGitMonitor creates a monitor that watches .git/HEAD for branch changes.
// onChange is called with the new branch name when the branch changes.
func NewGitMonitor(repoPath string, onChange func(newBranch string), logger *slog.Logger) (*GitMonitor, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("creating fsnotify watcher: %w", err)
	}

	branch, branchErr := CurrentBranch(context.Background(), repoPath)
	if branchErr != nil {
		_ = fsw.Close()
		return nil, fmt.Errorf("detecting initial branch: %w", branchErr)
	}

	return &GitMonitor{
		repoPath:      repoPath,
		currentBranch: branch,
		watcher:       fsw,
		onChange:      onChange,
		done:          make(chan struct{}),
		loopDone:      make(chan struct{}),
		logger:        logger,
	}, nil
}

// gitDirPointerPrefix is the prefix of a linked-worktree `.git` file: in a
// linked worktree (git worktree add) `<root>/.git` is a regular file whose
// single line `gitdir: <path>` points at the worktree's private git
// directory (typically `<main-repo>/.git/worktrees/<name>`), where HEAD,
// the index and per-worktree refs live.
const gitDirPointerPrefix = "gitdir: "

// resolveGitDir resolves the directory that owns HEAD for the repository at
// repoPath: `<repoPath>/.git` for a normal checkout, the private git
// directory a linked worktree's `.git` pointer file names otherwise —
// watching the pointer FILE would never see a branch switch. See
// specs/domains/workspace.md (GitMonitor). Returns "" (nil error) when
// repoPath is not inside a git repository at all.
func resolveGitDir(repoPath string) (string, error) {
	dotGit := filepath.Join(repoPath, ".git")
	info, err := os.Stat(dotGit)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("stat .git: %w", err)
	}
	if info.IsDir() {
		return dotGit, nil
	}

	// Linked worktree: `.git` is a regular file carrying a gitdir pointer.
	// The read goes through the package's FIFO-safe primitive: a plain
	// os.ReadFile would block forever on a readerless FIFO planted at .git.
	// The bound is defensive — a real pointer line is one short path.
	data, err := readBounded(dotGit, 4096)
	if err != nil {
		return "", fmt.Errorf("reading .git pointer file: %w", err)
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, gitDirPointerPrefix) {
		return "", fmt.Errorf(".git at %s is a file without a %q pointer", dotGit, gitDirPointerPrefix)
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, gitDirPointerPrefix))
	if target == "" {
		return "", fmt.Errorf(".git pointer at %s names an empty gitdir", dotGit)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(repoPath, target)
	}
	target = filepath.Clean(target)
	gdInfo, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("resolving gitdir pointer target %s: %w", target, err)
	}
	if !gdInfo.IsDir() {
		return "", fmt.Errorf("gitdir pointer target %s is not a directory", target)
	}
	return target, nil
}

// Start begins watching the git directory that owns HEAD (see resolveGitDir)
// for changes to the HEAD file.
func (m *GitMonitor) Start() error {
	gitDir, err := resolveGitDir(m.repoPath)
	if err != nil {
		return fmt.Errorf("resolving git directory: %w", err)
	}
	if gitDir == "" {
		m.logger.Info("no .git directory found, git monitor not started", "path", m.repoPath)
		return nil
	}

	// Watch the git directory (fsnotify can't watch individual files reliably
	// on all platforms, but watching the directory catches HEAD changes).
	if err := m.watcher.Add(gitDir); err != nil {
		return fmt.Errorf("watching git directory %s: %w", gitDir, err)
	}

	m.loopStarted.Store(true)
	go m.eventLoop()
	m.logger.Info("git monitor started", "path", m.repoPath, "gitdir", gitDir, "branch", m.currentBranch)
	return nil
}

// eventLoop reads fsnotify events and debounces branch-change detection. It
// stops the debounce timer on EVERY return path — including closed watcher
// channels — so Stop, which joins this loop, guarantees no armed timer fires
// a stale checkBranch after the monitor is gone.
func (m *GitMonitor) eventLoop() {
	defer close(m.loopDone)
	defer m.stopDebounceTimer()

	for {
		select {
		case <-m.done:
			return
		case event, ok := <-m.watcher.Events:
			if !ok {
				return
			}
			// Only react to changes to HEAD file.
			if filepath.Base(event.Name) != "HEAD" {
				continue
			}
			m.armDebounce()
		case watchErr, ok := <-m.watcher.Errors:
			if !ok {
				return
			}
			m.logger.Warn("git watcher error", "error", watchErr)
		}
	}
}

// armDebounce (re)arms the HEAD-change debounce timer unless the monitor was
// already stopped, so a timer can never be armed across a Stop.
func (m *GitMonitor) armDebounce() {
	m.debounceMu.Lock()
	defer m.debounceMu.Unlock()
	if m.stopped {
		return
	}
	if m.timer != nil {
		m.timer.Stop()
	}
	m.timer = time.AfterFunc(gitHeadDebounce, m.checkBranch)
}

// stopDebounceTimer stops any armed debounce timer and marks the monitor
// stopped, so neither a pending nor a later-armed timer can fire a stale
// checkBranch.
func (m *GitMonitor) stopDebounceTimer() {
	m.debounceMu.Lock()
	defer m.debounceMu.Unlock()
	m.stopped = true
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
}

// checkBranch reads the current branch and calls onChange if it changed. A
// monitor stopped mid-read must not fire its callback: a stale onChange
// would mutate state that no longer belongs to this monitor.
func (m *GitMonitor) checkBranch() {
	m.debounceMu.Lock()
	stopped := m.stopped
	m.debounceMu.Unlock()
	if stopped {
		return
	}

	branch, err := CurrentBranch(context.Background(), m.repoPath)
	if err != nil {
		m.logger.Warn("failed to detect branch", "error", err)
		return
	}

	m.mu.Lock()
	prev := m.currentBranch
	m.currentBranch = branch
	m.mu.Unlock()

	if branch != prev {
		m.logger.Info("branch changed", "from", prev, "to", branch)
		m.onChange(branch)
	}
}

// CurrentBranchName returns the last known branch name.
func (m *GitMonitor) CurrentBranchName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.currentBranch
}

// Stop stops the monitor and releases resources.
func (m *GitMonitor) Stop() error {
	// Stop any armed debounce timer first: neither a pending nor a
	// later-armed timer may fire a stale checkBranch across this return.
	m.stopDebounceTimer()

	select {
	case <-m.done:
		// Already stopped: the first Stop closed done and joined below.
	default:
		close(m.done)
	}
	err := m.watcher.Close()
	if m.loopStarted.Load() {
		// Join the event loop so it can no longer arm a timer after this
		// return; it exits promptly and never blocks on a lock Stop's
		// caller holds — the debounce mutex is exclusive to this monitor.
		<-m.loopDone
	}
	return err
}
