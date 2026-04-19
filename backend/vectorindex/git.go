package vectorindex

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	git "github.com/go-git/go-git/v6"
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
	mu            sync.Mutex
	logger        *slog.Logger
}

// CurrentBranch returns the current git branch name using go-git.
// For non-git directories, it returns DefaultBranch.
func CurrentBranch(repoPath string) (string, error) {
	// Check if .git directory exists.
	gitDir := filepath.Join(repoPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return DefaultBranch, nil
	}

	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("opening git repo at %s: %w", repoPath, err)
	}

	headRef, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("getting HEAD reference: %w", err)
	}

	branch := headRef.Name().Short()
	if branch == "" {
		// Detached HEAD — use the hash prefix as identifier.
		branch = headRef.Hash().String()[:12]
	}

	return branch, nil
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

	branch, branchErr := CurrentBranch(repoPath)
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
		logger:        logger,
	}, nil
}

// Start begins watching for branch changes. It watches the .git/ directory
// for changes to the HEAD file.
func (m *GitMonitor) Start() error {
	gitDir := filepath.Join(m.repoPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		m.logger.Info("no .git directory found, git monitor not started", "path", m.repoPath)
		return nil
	}

	// Watch the .git directory (fsnotify can't watch individual files reliably
	// on all platforms, but watching the directory catches HEAD changes).
	if err := m.watcher.Add(gitDir); err != nil {
		return fmt.Errorf("watching .git directory: %w", err)
	}

	go m.eventLoop()
	m.logger.Info("git monitor started", "path", m.repoPath, "branch", m.currentBranch)
	return nil
}

// eventLoop reads fsnotify events and debounces branch change detection.
func (m *GitMonitor) eventLoop() {
	var timer *time.Timer

	for {
		select {
		case <-m.done:
			if timer != nil {
				timer.Stop()
			}
			return
		case event, ok := <-m.watcher.Events:
			if !ok {
				return
			}
			// Only react to changes to HEAD file.
			if filepath.Base(event.Name) != "HEAD" {
				continue
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(gitHeadDebounce, m.checkBranch)
		case watchErr, ok := <-m.watcher.Errors:
			if !ok {
				return
			}
			m.logger.Warn("git watcher error", "error", watchErr)
		}
	}
}

// checkBranch reads the current branch and calls onChange if it changed.
func (m *GitMonitor) checkBranch() {
	branch, err := CurrentBranch(m.repoPath)
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
	select {
	case <-m.done:
		return nil // already stopped
	default:
		close(m.done)
	}
	return m.watcher.Close()
}
