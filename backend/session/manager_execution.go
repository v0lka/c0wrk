package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	goalpkg "github.com/v0lka/c0wrk/core/goal"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/ignore"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/pathutil"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ErrNoActiveTask is returned by CancelTask when the session has no task
// currently running. Callers that don't care whether a task was actually
// cancelled can treat this as non-fatal via errors.Is.
var ErrNoActiveTask = errors.New("no active task to cancel")

// ErrSessionArchived is returned by SendMessage/ResumeTask when the target
// session is archived. An archived session is read-only: no new task may be
// launched or resumed on it. The UI hides the input shell for archived
// sessions, but this guard is the single server-side choke point that also
// defends inline action panels (e.g. the failed-task Resume button) and any
// other caller of the manager. Restore the session to clear the flag.
var ErrSessionArchived = errors.New("session is archived: restore it before sending messages or resuming tasks")

// ErrPausePending is returned by SendMessage when the session's running task
// has been signalled to pause but the cooperative pause has not landed yet
// (the executor stops at its next step boundary). Sends are rejected in that
// window to avoid racing the pause→paused transition: once session_paused is
// emitted, a message becomes a nudge-resume instead. The UI locks the input
// for the window; this is the server-side backstop.
var ErrPausePending = errors.New("session is pausing — send again once the pause completes")

// finishLiveLeftover launches the follow-up task for live messages that were
// queued but never delivered before the request finished (liveActionFollowUp).
// leftover is nil for every other epilogue action. The messages were already
// persisted and rendered at send time, so the follow-up re-enters sendMessage
// as a presented relaunch (no duplicate message_received, no title regen). A
// fresh context is used: the original task's cancel func must not govern the
// follow-up.
func (m *Manager) finishLiveLeftover(_ context.Context, id string, _ *Session, leftover []string) {
	if len(leftover) == 0 {
		return
	}
	joined := strings.Join(leftover, "\n\n")
	m.log().Info("launching follow-up task for undelivered live messages", "session_id", id, "count", len(leftover))
	if _, err := m.sendMessage(ContextWithSessionID(context.Background(), id), id, joined, nil, nil, "", "", false, "", false, true); err != nil {
		m.log().Error("failed to launch follow-up task for live messages", "session_id", id, "error", err)
		m.emitFunc(Event{
			SessionID: id,
			Type:      "error",
			Data:      ErrorData{SessionID: id, Error: "queued message could not start a follow-up task: " + err.Error()},
		})
	}
}

// deactivateSessionTask flips the session back to idle and, for a follow-up
// action, drains the queued live messages under the same lock hold. It returns
// the drained leftovers (nil for any other action). Terminal events must be
// emitted AFTER this call: a consumer reacting to task_complete/session_paused
// treats the session as free to resume/send, so it must already observe
// active == false — otherwise a fast follow-up hits a spurious
// "session is already processing a task" error.
func (m *Manager) deactivateSessionTask(session *Session, action liveAction) []string {
	session.mu.Lock()
	defer session.mu.Unlock()

	session.active = false
	session.cancel = nil
	session.done = nil
	session.pausing = false

	if session.orchestrator == nil {
		return nil
	}
	switch action {
	case liveActionFollowUp:
		return session.orchestrator.TakeLiveUserMessages()
	case liveActionDiscard:
		session.orchestrator.DiscardLiveUserMessages()
	}
	return nil
}

// loadWorkDirectories fetches project-scoped and session-scoped auxiliary work
// directories for the given session. It is best-effort: nil stores or listing
// errors are logged and skipped. Project-scoped entries come first, then
// session-scoped entries. Loaded fresh on every call so mid-session additions
// take effect on the next task.
func (m *Manager) loadWorkDirectories(session *Session) []core.WorkDirectory {
	m.mu.RLock()
	projStore := m.projectStore
	sessStore := m.sessionStore
	m.mu.RUnlock()

	var dirs []core.WorkDirectory
	if session.ProjectID != project.NoProjectID && projStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		recs, err := projStore.ListProjectWorkDirs(ctx, session.ProjectID)
		cancel()
		if err != nil {
			m.log().Warn("failed to list project work directories", "project", session.ProjectID, "error", err)
		} else {
			for _, rec := range recs {
				dirs = append(dirs, core.WorkDirectory{Path: rec.Path, Description: rec.Description})
			}
		}
	}
	if sessStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		recs, err := sessStore.ListSessionWorkDirs(ctx, session.ID)
		cancel()
		if err != nil {
			m.log().Warn("failed to list session work directories", "session", session.ID, "error", err)
		} else {
			for _, rec := range recs {
				dirs = append(dirs, core.WorkDirectory{Path: rec.Path, Description: rec.Description})
			}
		}
	}
	return dirs
}

// implicitTempRoots computes the host OS temporary-directory roots that are
// always allowed for task execution, even when no auxiliary work directories
// are configured and even in CHAT mode (No Project). Agent-authored shell
// commands and tool paths routinely reference the OS temp tree (mktemp
// scratch, downloaded artifacts, scratch files of managed CLI tools), so those
// roots are injected as implicit containment peers of the workspace. They
// never surface in the system prompt or the UI — they are security roots
// only, set via sdktools.WithAllowedRoots.
//
// goos is the runtime.GOOS value (parameterised for testability), tempDir the
// os.TempDir() value, and systemRootEnv the %SystemRoot% value (Windows only,
// e.g. "C:\Windows"). On Windows the candidates are tempDir and
// %SystemRoot%\Temp (the classic inherited-TMP location); everywhere else
// they are "/tmp" and tempDir. Candidates are validated and normalized with
// host-independent string analysis (NOT filepath.Clean/Join, whose separator
// semantics depend on the host OS — the branches must behave identically on
// every CI runner): trailing separators are trimmed, duplicates dropped, and
// empty, relative, or drive-relative inputs skipped. The containment API
// requires roots to be absolute paths.
func implicitTempRoots(goos, tempDir, systemRootEnv string) []string {
	var candidates []string
	if goos == "windows" {
		candidates = append(candidates, tempDir)
		if systemRootEnv != "" {
			candidates = append(candidates, strings.TrimRight(systemRootEnv, "\\/")+"\\Temp")
		}
	} else {
		candidates = append(candidates, "/tmp", tempDir)
	}

	seen := make(map[string]struct{}, len(candidates))
	roots := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if !isAbsForGOOS(goos, c) {
			continue
		}
		norm := trimTrailingSeps(goos, c)
		if _, dup := seen[norm]; dup {
			continue
		}
		seen[norm] = struct{}{}
		roots = append(roots, norm)
	}
	return roots
}

// isAbsForGOOS reports whether p is absolute for the given GOOS, using pure
// string analysis so a windows branch evaluated on a POSIX host (and vice
// versa) yields the same verdict as on its native host. Windows absolute
// forms: volume letter + separator (e.g. "C:\Temp") or a UNC path
// ("\\server\share"). Drive-relative paths ("C:foo") are NOT absolute.
func isAbsForGOOS(goos, p string) bool {
	if goos == "windows" {
		if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') && isVolumeLetter(p[0]) {
			return true
		}
		return strings.HasPrefix(p, `\\`)
	}
	return strings.HasPrefix(p, "/")
}

// isVolumeLetter reports whether c is a Windows drive letter (A-Z, a-z).
func isVolumeLetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// trimTrailingSeps removes trailing path separators for the given GOOS while
// preserving the minimal absolute form ("/" on POSIX, the volume root "C:\"
// on Windows). Host-independent: a trailing backslash is a separator only
// for the windows GOOS, where on POSIX it is a legal filename character.
func trimTrailingSeps(goos, p string) string {
	minLen := 1
	if goos == "windows" {
		minLen = 3
	}
	for len(p) > minLen && (p[len(p)-1] == '/' || (goos == "windows" && p[len(p)-1] == '\\')) {
		p = p[:len(p)-1]
	}
	return p
}

// injectWorkDirectories injects the session's auxiliary work directories into
// the context as both allowed roots (security containment) and the
// prompt-facing directory list. dirs must be loaded by the caller (shared with
// injectIgnoreChecker so loadWorkDirectories runs once per task rather than
// twice). Allowed roots are ALWAYS set: the work-directory paths plus the
// implicit host temp roots from implicitTempRoots, so tasks with no configured
// directories (including CHAT-mode No Project sessions) still operate freely
// inside the OS temp tree. The prompt-facing core.WithWorkDirectories is
// applied only when directories are configured — implicit temp roots never
// reach the system prompt or the UI.
func (m *Manager) injectWorkDirectories(ctx context.Context, dirs []core.WorkDirectory) context.Context {
	paths := make([]string, 0, len(dirs))
	for i := range dirs {
		paths = append(paths, dirs[i].Path)
	}
	paths = append(paths, implicitTempRoots(runtime.GOOS, os.TempDir(), os.Getenv("SystemRoot"))...)
	ctx = sdktools.WithAllowedRoots(ctx, paths)
	if len(dirs) == 0 {
		return ctx
	}
	return core.WithWorkDirectories(ctx, dirs)
}

// researchProjectInfo reports whether the session's project has RESEARCH mode
// active and, when it does, returns the research-root path. RESEARCH is
// active: a real project (not No Project) with a non-empty research root. It
// loads the project from the store, so callers MUST NOT hold the session lock.
// Returns ("", false) for No Project sessions, when no project store is
// configured, when the project is missing, or on load errors (logged
// best-effort).
func (m *Manager) researchProjectInfo(projectID string) (string, bool) {
	if projectID == project.NoProjectID {
		return "", false
	}
	m.mu.RLock()
	store := m.projectStore
	m.mu.RUnlock()
	if store == nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proj, err := store.LoadProject(ctx, projectID)
	if err != nil {
		m.log().Warn("failed to load project for research check", "project", projectID, "error", err)
		return "", false
	}
	if proj == nil {
		return "", false
	}
	return proj.ResearchRoot, proj.ResearchRoot != ""
}

// injectIgnoreChecker builds a multi-root ignore resolver from the session's
// workspace path plus its auxiliary work directories and attaches it to the
// context via sdktools.WithIgnoreChecker. Read-style search/listing tools
// (glob, ripgrep) consult it to honour each root's own .gitignore + .aiignore;
// read_file remains unrestricted.
//
// dirs is the work-directory slice the caller already loaded (shared with
// injectWorkDirectories) so loadWorkDirectories runs once per task rather than
// twice. For No Project sessions (no workspace and no work directories) ctx is
// returned unchanged, so behaviour is identical to before.
//
// Each root is symlink-resolved before being handed to the resolver so the
// roots match the (symlink-resolved) form that glob/ripgrep query paths in. A
// resolver is then built PER ROOT with skip-and-log on failure: a single bad
// root (e.g. a vanished or unreadable work-directory entry) only drops that
// root and must not disable ignore filtering for the healthy roots. The
// survivors are combined via ignore.NewMultiFromResolvers.
func (m *Manager) injectIgnoreChecker(ctx context.Context, session *Session, dirs []core.WorkDirectory) context.Context {
	rawRoots := make([]string, 0, 1+len(dirs))
	if session.WorkspacePath != "" {
		rawRoots = append(rawRoots, session.WorkspacePath)
	}
	for _, d := range dirs {
		if d.Path != "" {
			rawRoots = append(rawRoots, d.Path)
		}
	}
	if len(rawRoots) == 0 {
		return ctx
	}

	// Symlink-resolve + dedupe roots so they match the (resolved) form
	// glob/ripgrep query paths in.
	roots := make([]string, 0, len(rawRoots))
	seen := make(map[string]struct{}, len(rawRoots))
	for _, r := range rawRoots {
		resolved, err := filepath.EvalSymlinks(r)
		if err != nil {
			m.log().Debug("ignore checker: root symlink resolution failed, using raw path", "path", r, "error", err)
			resolved = r
		}
		if _, dup := seen[resolved]; dup {
			continue
		}
		seen[resolved] = struct{}{}
		roots = append(roots, resolved)
	}

	// Build a resolver per root from the cache. Roots that are not yet cached
	// are built asynchronously — the resolver is expensive (it walks the
	// entire directory tree for .gitignore/.aiignore files) and blocking
	// SendMessage on a large directory (e.g. the Go module cache with tens of
	// thousands of entries) causes multi-minute hangs. On the first message
	// after startup the checker may be incomplete; subsequent messages pick up
	// the cached resolver once the background walk finishes.
	resolvers := make([]*ignore.Resolver, 0, len(roots))
	for _, root := range roots {
		if cached, loaded := m.ignoreCache.Load(root); loaded {
			// The cache only ever stores *ignore.Resolver (a real resolver or
			// the building sentinel), but assert with the comma-ok form so an
			// unexpected type is skipped instead of panicking.
			r, ok := cached.(*ignore.Resolver)
			if !ok {
				continue
			}
			resolvers = append(resolvers, r)
			continue
		}
		// Kick off async build — deduplicated via LoadOrStore of a sentinel
		// so concurrent SendMessage calls don't launch duplicate walks.
		m.startIgnoreBuild(root)
	}
	if len(resolvers) == 0 {
		return ctx
	}
	checker := ignore.NewMultiFromResolvers(m.log(), resolvers...)
	return sdktools.WithIgnoreChecker(ctx, checker)
}

// caseInsensitiveProbe carries the single result of a case-sensitivity probe.
// The first call to miss the cache stores a fresh probe via LoadOrStore and
// performs the actual filesystem probe, then publishes the result by closing
// done. Any concurrent call that loses the LoadOrStore race observes the
// in-flight probe and blocks on its done channel, reusing the same result
// rather than re-probing. This guarantees the CaseSense-*.probe file is
// created exactly once per resolved root even under concurrent messages.
type caseInsensitiveProbe struct {
	done chan struct{}
	ci   bool
}

// defaultDetectCaseInsensitive is the production case-sensitivity probe wired
// into Manager.detectCaseInsensitiveFn at construction. Tests override the
// per-instance field (not this value) to assert call counts.
var defaultDetectCaseInsensitive = pathutil.DetectCaseInsensitive

// caseInsensitiveCachedValue reads the resolved bool from a cache entry. It
// blocks until the entry's probe has completed (the winning goroutine closes
// done), then returns (value, true). ok is false only if the entry is not a
// *caseInsensitiveProbe, which cannot happen by construction; it exists to
// satisfy errcheck on the type assertion.
func caseInsensitiveCachedValue(v any) (ci, ok bool) {
	p, okp := v.(*caseInsensitiveProbe)
	if !okp {
		return false, false
	}
	<-p.done
	return p.ci, true
}

// detectCaseInsensitive returns the cached case-sensitivity of the filesystem
// at path, probing exactly once per resolved root and reusing the result on
// every subsequent call. Case-sensitivity is a mount-level property that does
// not change during a session, so the probe — which creates and deletes a
// temporary CaseSense-*.probe file — must run at most once per root rather
// than on every SendMessage/ResumeSession. LoadOrStore plus the probe's done
// channel guarantee a single probe per root even under concurrent calls: the
// goroutine that wins the race performs it, every loser waits for its result.
//
// When path is empty (e.g. a No-Project session) it returns false, the
// fail-safe case-sensitive default, without touching the cache.
func (m *Manager) detectCaseInsensitive(path string) bool {
	if path == "" {
		return false
	}
	// Resolve symlinks so the cache key matches the physical root even when
	// the workspace is reached through a symlink (consistent with ignoreCache).
	// A resolution failure (e.g. a not-yet-created workspace) falls back to
	// the raw path; DetectCaseInsensitive itself climbs to the nearest existing
	// ancestor, so the probe still yields the correct filesystem answer.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	probe := &caseInsensitiveProbe{done: make(chan struct{})}
	if actual, loaded := m.caseInsensitiveCache.LoadOrStore(resolved, probe); loaded {
		// Another call already won the race for this root. Reuse its in-flight
		// (or already-completed) probe result instead of re-probing.
		existing, ok := actual.(*caseInsensitiveProbe)
		if !ok {
			// Unreachable by construction; the cache only stores
			// *caseInsensitiveProbe. Defensive to satisfy errcheck.
			return false
		}
		<-existing.done
		return existing.ci
	}
	// We won the race: perform the single probe for this root and publish it.
	probe.ci = m.detectCaseInsensitiveFn(resolved)
	close(probe.done)
	return probe.ci
}

// startIgnoreBuild launches a background goroutine to walk root and cache its
// ignore.Resolver. It is deduplicated: if another goroutine has already started
// (or completed) a build for the same root, this is a no-op.
func (m *Manager) startIgnoreBuild(root string) {
	// Deduplicate via a sentinel "building" marker stored in the cache map.
	// sync.Map.LoadOrStore guarantees exactly one goroutine wins the race.
	sentinel := &ignore.Resolver{}
	if actual, loaded := m.ignoreCache.LoadOrStore(root, sentinel); loaded {
		// Already cached (real resolver or in-flight sentinel) — nothing to do.
		_ = actual
		return
	}

	go func() {
		r, err := ignore.NewResolver(root)
		if err != nil {
			m.log().Debug("ignore checker: background resolver build failed", "root", root, "error", err)
			// Remove the sentinel so a future call can retry.
			m.ignoreCache.Delete(root)
			return
		}
		m.ignoreCache.Store(root, r)
	}()
}

// ignoreFileNames are the files whose change invalidates a cached resolver.
// ignore.NewResolver walks the whole root collecting every occurrence of these,
// so a change to any one of them — anywhere under a root — makes that root's
// cached resolver stale.
var ignoreFileNames = map[string]struct{}{
	".gitignore": {},
	".aiignore":  {},
	".ignore":    {}, // .ignore is honoured by ripgrep and some tools
}

// InvalidateIgnoreCache evicts cached ignore resolvers whose root contains one
// of changedPaths when that path is an ignore-rule file (.gitignore/.aiignore/
// .ignore). It is the invalidation half of the async ignore cache: without it,
// edits to ignore files would be invisible until the app restarts.
//
// It only DELETEs cache entries — it never rebuilds synchronously. The rebuild
// is triggered lazily and asynchronously by the next injectIgnoreChecker call:
// startIgnoreBuild launches a background goroutine and deduplicates concurrent
// builds via a sentinel. Because the rebuild never blocks SendMessage, this
// cannot regress the multi-minute-build case on roots with hundreds of
// thousands of files — at worst the first message after invalidation runs
// without ignore filtering (the no-checker / sentinel path), exactly like the
// first message after startup, until the background walk completes.
//
// changedPaths are expected to be absolute filesystem paths (as reported by the
// workspace watcher / fsnotify); the cache is keyed by symlink-resolved roots.
// pathutil.IsWithinPath resolves symlinks on both sides, so the match is
// correct even when the workspace lives behind an OS symlink.
func (m *Manager) InvalidateIgnoreCache(changedPaths []string) {
	affected := make(map[string]struct{})
	for _, p := range changedPaths {
		base := filepath.Base(p)
		if _, isIgnoreFile := ignoreFileNames[base]; isIgnoreFile {
			affected[p] = struct{}{}
		}
	}
	if len(affected) == 0 {
		return
	}

	// For each affected ignore file, evict every cached root that contains it.
	// The cache typically holds only a handful of roots (workspace + a few
	// auxiliary work directories), so a full Range per affected file is cheap.
	m.ignoreCache.Range(func(key, _ any) bool {
		root, ok := key.(string)
		if !ok {
			return true
		}
		for ignoreFile := range affected {
			within, err := pathutil.IsWithinPath(root, ignoreFile)
			if err != nil {
				// Resolve failure (e.g. vanished root) — evict to be safe; the
				// lazy rebuild will re-add it (or skip it) on next use.
				m.ignoreCache.Delete(root)
				m.log().Debug("ignore checker: evicted root on resolve error", "root", root, "error", err)
				continue
			}
			if within {
				m.ignoreCache.Delete(root)
				m.log().Debug("ignore checker: invalidated root after ignore-file change", "root", root, "trigger", ignoreFile)
			}
		}
		return true
	})
}

// Runs in a goroutine, results come via events.
// reviewMode, when true, marks the message as carrying code review feedback
// the agent must address (see core HandleOptions.ReviewMode).
//
// This is the single-return convenience form of SendMessageClassified for
// callers that do not need the authoritative send classification.
func (m *Manager) SendMessage(ctx context.Context, id, text string, activeSkills, activeAgents []string, modelOverride, reasoningEffort string, goal bool, goalBudget string, reviewMode bool) error {
	_, err := m.SendMessageClassified(ctx, id, text, activeSkills, activeAgents, modelOverride, reasoningEffort, goal, goalBudget, reviewMode)
	return err
}

// SendClassification describes how a user message was dispatched relative to
// the session's running/paused state. It is the authoritative result of the
// session-locked decision in sendMessage, so callers can persist the correct
// is_nudge metadata: a nudge-resume and a live interjection both carry the
// badge, while a fresh task (or a continuation of a completed task) does not.
type SendClassification int

const (
	// SendFresh means no task was running or paused when the message arrived —
	// it started a new task (or, on a continuation, re-entered the prior
	// completed task).
	SendFresh SendClassification = iota
	// SendNudgeResume means the message resumed a paused task and is injected
	// as a trailing user nudge.
	SendNudgeResume
	// SendLive means the message was queued into a running task as a live
	// interjection.
	SendLive
)

// SendMessageClassified runs a user message and returns the authoritative send
// classification (fresh / nudge-resume / live) so the caller can persist the
// correct is_nudge metadata after the decision is made under the session lock.
// The task itself runs in a goroutine and reports via events.
func (m *Manager) SendMessageClassified(ctx context.Context, id, text string, activeSkills, activeAgents []string, modelOverride, reasoningEffort string, goal bool, goalBudget string, reviewMode bool) (SendClassification, error) {
	return m.sendMessage(ctx, id, text, activeSkills, activeAgents, modelOverride, reasoningEffort, goal, goalBudget, reviewMode, false)
}

// liveAction tells the request epilogue what to do with live user messages
// that were queued but not delivered before the request finished.
type liveAction int

const (
	// liveActionNone keeps the queue: the messages remain queued and a later
	// request (resume, follow-up, or a fresh task) delivers them. Used for
	// paused and failed/resumable outcomes — the next entry point drains the
	// queue at its first step boundary.
	liveActionNone liveAction = iota
	// liveActionFollowUp takes the leftovers and launches a continuation task
	// carrying them (they were already persisted + rendered, so the follow-up
	// skips re-presentation). Used when the task completed successfully
	// without delivering them.
	liveActionFollowUp
	// liveActionDiscard drops the queue: the user stopped the run, and an
	// undelivered message in a cancelled exchange must not leak into a future
	// request. Used for user-initiated cancellation.
	liveActionDiscard
)

// liveSendRejectionLocked returns the error that must reject a live send (a
// message arriving while a task is running) for the given request, or nil when
// the send may queue. The caller must hold session.mu. The same conditions are
// checked by ValidateLiveSend before the frontend persists the message and by
// the live branch here under the lock (the authoritative gate).
func liveSendRejectionLocked(session *Session, goal bool, text string, activeSkills, activeAgents []string) error {
	// A leading "/goal" command selects goal mode even when the explicit goal
	// flag is absent. The fresh-task path detects it via
	// DetectAndStripGoalMode; the live-send gate must reject it identically
	// instead of queueing the raw text as a plain LLM interjection.
	_, isGoalPrefix := core.DetectAndStripGoalMode(text)
	goal = goal || isGoalPrefix
	switch {
	case session.compacting:
		return ErrSessionCompacting
	case session.pausing:
		return ErrPausePending
	case goal:
		return errors.New("goal requests cannot be sent while a task is running — pause or wait for completion first")
	case len(activeSkills) > 0 || len(activeAgents) > 0:
		return errors.New("skill or agent references cannot be sent while a task is running — pause or wait for completion first")
	case session.orchestrator == nil:
		return errors.New("session is already processing a task")
	}
	return nil
}

// sendMessage is the implementation behind SendMessage. presented marks a
// relaunch of an already-rendered message (the live-send follow-up): it skips
// the message_received emission and title generation because the UI and the
// message store already hold the message from the original send.
func (m *Manager) sendMessage(ctx context.Context, id, text string, activeSkills, activeAgents []string, modelOverride, reasoningEffort string, goal bool, goalBudget string, reviewMode, presented bool) (SendClassification, error) {
	session, err := m.getOrRestoreSession(id)
	if err != nil {
		return SendFresh, fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return SendFresh, fmt.Errorf("session not found: %s", id)
	}

	session.mu.Lock()
	// Archived sessions are read-only: reject before activating. This is the
	// server-side choke point that prevents any caller (not just the UI) from
	// launching a task on an archived session.
	if session.Archived {
		session.mu.Unlock()
		return SendFresh, ErrSessionArchived
	}
	// Live-send: a message arriving while a task is already running is queued
	// into the RUNNING request instead of starting a new one — the executor
	// drains the queue at its next step boundary and delivers the text to the
	// LLM as a user interjection (the same landing spot as a resume-with-
	// nudge). The queue-while-locked ordering pairs with the request epilogue
	// (which flips active=false and takes leftovers under the same lock), so a
	// message sent in the closing window of a finishing request is either
	// delivered by that request or becomes its follow-up task — never lost
	// and never duplicated.
	if session.active {
		if err := liveSendRejectionLocked(session, goal, text, activeSkills, activeAgents); err != nil {
			session.mu.Unlock()
			return SendFresh, err
		}
		session.orchestrator.QueueLiveUserMessage(text)
		session.mu.Unlock()
		// Emit message_received so non-optimistic listeners see the user
		// message (mirrors the normal path; the frontend renders its own
		// optimistic copy and ignores this event).
		m.emitFunc(Event{
			SessionID: id,
			Type:      "message_received",
			Data: MessageReceivedData{
				SessionID: id,
				Text:      text,
			},
		})
		return SendLive, nil
	}
	// Manual compaction owns the session from the moment it starts (it waits
	// for a running task to pause, then swaps the history): a fresh task in
	// that window would race the swap. The nudge-resume path below is covered
	// by ResumeTask's own compacting guard.
	if session.compacting {
		session.mu.Unlock()
		return SendFresh, ErrSessionCompacting
	}
	session.mu.Unlock()

	// Nudge-resume: if the session has a paused task, sending a message does
	// NOT start a new task — the message becomes a nudge that resumes the
	// paused task via ResumeSession (which re-activates the session and injects
	// the text as a trailing user turn). Goal requests are excluded: a /goal
	// message supersedes any paused task (abandonUnfinishedTaskForGoal handles
	// cleanup). Detected here before activation so ResumeTask's own activation
	// path is not tripped by a spurious "already processing" check.
	if !goal && m.hasPausedUnfinishedTask(id) {
		// Emit message_received so the UI shows the user message (mirrors the
		// normal SendMessage path's emission that the goroutine skips).
		m.emitFunc(Event{
			SessionID: id,
			Type:      "message_received",
			Data: MessageReceivedData{
				SessionID: id,
				Text:      text,
			},
		})
		return SendNudgeResume, m.ResumeSession(ctx, id, modelOverride, reasoningEffort, text)
	}

	// Determine RESEARCH mode from the project's research root (loaded before
	// the session lock to avoid a DB query while holding it).
	researchRoot, isResearch := m.researchProjectInfo(session.ProjectID)

	session.mu.Lock()

	// Set active and create cancellable context with session ID
	session.active = true
	doneCh := make(chan struct{})
	session.done = doneCh
	taskCtx, cancel := context.WithCancel(ContextWithSessionID(ctx, id))
	// Enrich context with session workspace path for tool security heuristics.
	// The case-folding flag is probed once per root (cached by the Manager)
	// rather than on every message, so no .probe file is created/deleted here.
	taskCtx = sdktools.WithWorkspacePathNoProbe(taskCtx, session.WorkspacePath)
	taskCtx = sdktools.WithCaseInsensitivePaths(taskCtx, m.detectCaseInsensitive(session.WorkspacePath))
	taskCtx = sdktools.WithTempDir(taskCtx, session.TempDir)
	taskCtx = sdktools.WithCoherence(taskCtx, m.fileTracker)
	if session.ProjectID == project.NoProjectID {
		taskCtx = coretools.WithNoProject(taskCtx)
	}
	if isResearch {
		taskCtx = coretools.WithResearch(taskCtx)
		taskCtx = coretools.WithResearchRoot(taskCtx, researchRoot)
	}
	session.cancel = cancel
	session.mu.Unlock()

	// Snapshot envInfo under read lock
	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo != nil {
		taskCtx = sdktools.WithEnvInfo(taskCtx, envInfo)
	}

	// Inject auxiliary work directories (allowed roots + prompt list), and
	// attach a multi-root ignore checker so glob/ripgrep honour each root's
	// own .gitignore + .aiignore. dirs is loaded once and shared so the
	// (DB-hitting) loadWorkDirectories runs a single time per task. Allowed
	// roots ALWAYS include the implicit host temp roots (see implicitTempRoots),
	// also for No Project sessions; the prompt-facing list and the ignore
	// checker remain no-ops when no directories are configured.
	dirs := m.loadWorkDirectories(session)
	taskCtx = m.injectWorkDirectories(taskCtx, dirs)
	taskCtx = m.injectIgnoreChecker(taskCtx, session, dirs)

	// Emit message received event. Skipped for a presented relaunch (the
	// live-send follow-up): the message was already emitted when it was
	// queued into the running task, and the frontend renders its own
	// optimistic copy.
	if !presented {
		m.emitFunc(Event{
			SessionID: id,
			Type:      "message_received",
			Data: MessageReceivedData{
				SessionID: id,
				Text:      text,
			},
		})
	}

	// Check if this is the first message (session has default name)
	// and spawn title generation in background.
	session.mu.Lock()
	sessionName := session.Name
	session.mu.Unlock()
	m.mu.RLock()
	titleGen := m.titleGen
	store := m.sessionStore
	serviceLLMTimeout := m.serviceLLMTimeout
	m.mu.RUnlock()
	if !presented && sessionName == "Session "+safeSessionPrefix(id) && titleGen != nil {
		dumpFile := session.DumpFile()
		go func() {
			if dumpFile != nil {
				defer func() { _ = dumpFile.Close() }()
			}
			ctx, cancel := context.WithTimeout(context.Background(), serviceLLMTimeout)
			defer cancel()
			if dumpFile != nil {
				ctx = agent.WithDumpWriter(ctx, dumpFile)
			}
			title := titleGen.Generate(ctx, text, activeSkills)
			if title == "" {
				return
			}
			if err := m.RenameSession(id, title); err != nil {
				m.log().Warn("failed to rename session with generated title", "session", id, "error", err)
				return
			}
			m.log().Info("session auto-named", "session", id, "title", title)
			// Persist rename to store
			if store != nil {
				if err := store.RenameSession(context.Background(), id, title); err != nil {
					m.log().Warn("failed to persist session title", "session", id, "error", err)
				}
			}
		}()
	}

	// Launch goroutine to handle the message
	go func(ctx context.Context, msg string, skills []string, agents []string) {
		// action tells the request epilogue what to do with queued-but-
		// undelivered live messages. The zero value (liveActionNone) keeps
		// them queued — the default for every path that either delegated to
		// another flow (tryContinueInterruptedTask's resumed run drains the
		// queue itself) or left the task resumable (paused/failed: the next
		// resume delivers them).
		action := liveActionNone
		finished := false
		defer close(doneCh)
		defer func() {
			if finished {
				return
			}
			leftover := m.deactivateSessionTask(session, action)
			m.finishLiveLeftover(ctx, id, session, leftover)
		}()

		// Snapshot pending attachments and clear them so they are flushed
		// exactly once into the blackboard for this SendMessage — regardless
		// of whether the message continues an interrupted task or starts a
		// fresh one. Done before the continue check so the resume path also
		// receives them (it bypasses HandleMessage, which would otherwise
		// flush them via setupBlackboard).
		session.mu.Lock()
		pendingAttachments := session.pendingAttachments
		session.pendingAttachments = nil
		pendingImages := session.pendingImageAttachments
		session.pendingImageAttachments = nil
		session.mu.Unlock()

		// Clear the pending-attachment chips now that the attachments have been
		// flushed into the blackboard for this task. Only emit when there were
		// pending attachments to avoid a spurious event on attachment-free sends.
		if len(pendingAttachments) > 0 || len(pendingImages) > 0 {
			m.emitAttachmentsChanged(id, []AttachmentInfo{}, nil)
		}

		// Convert staged image attachments into LLM image content blocks for
		// the context window. The blocks carry the base64 image data (held in
		// memory only until this snapshot); the on-disk copy at FilePath
		// persists for restart reconstruction via ChatMessage.Metadata.
		imageBlocks := imageAttachmentsToContentBlocks(pendingImages)

		// Detect goal mode (leading "/goal" command OR an explicit goal flag)
		// BEFORE the resume check. A goal request starts a fresh goal pursuit
		// rather than continuing an interrupted task, so the resume path is
		// skipped and any unfinished task is abandoned (see
		// abandonUnfinishedTaskForGoal). The two signals are OR-ed: a goal
		// flag enables goal mode regardless of the message prefix.
		goalMsg, isGoal := core.DetectAndStripGoalMode(msg)
		goalEnabled := isGoal || goal

		if goalEnabled {
			// Goal mode supersedes any interrupted task: cancel it so it does
			// not linger as resumable WIP across the new goal task, then fall
			// through to the goal dispatch below. Best-effort; a missing task
			// store or no unfinished task is a no-op.
			m.abandonUnfinishedTaskForGoal(id)
		} else if m.tryContinueInterruptedTask(ctx, id, session, msg, modelOverride, reasoningEffort, pendingAttachments) {
			// Continue an interrupted (unfinished) task if one exists: the new
			// user message is appended as a final user-nudge turn to the prior
			// trajectory and the ReAct cycle resumes — no routing, no new task,
			// no conversation-history pair. The function fully owns terminal
			// handling (deactivate + emit + follow-up) for every outcome, so
			// mark finished to skip the deferred epilogue's second deactivation.
			finished = true
			return
		}

		// No unfinished task took the resume path (or goal was requested): run
		// the normal route → plan → execute flow, or dispatch to the goal loop.
		// Get last completed task ID for continuation.
		session.mu.Lock()
		lastTaskID := session.lastCompletedTaskID
		session.mu.Unlock()

		// Snapshot the anchor's unfinished state BEFORE HandleMessage can
		// reactivate it. lastCompletedTaskID is restored without a status
		// check, so after an app restart it may point at a failed task whose
		// resume path (tryContinueInterruptedTask) then fell back here on a
		// restore error — such an anchor is unfinished BEFORE the send. The
		// snapshot lets shouldRetryContinuationFresh tell that case apart from
		// an anchor reactivated by this send's own execution. The
		// short-circuit keeps fresh sends (the common case) off the lookup.
		anchorWasUnfinished := lastTaskID != "" && m.unfinishedTaskID(id) == lastTaskID

		// On a continuation (lastTaskID != ""), goal mode runs ON the restored
		// blackboard of the prior completed task: the agent keeps the inherited
		// facts and history, and a fresh goal is derived from the new message.
		// When the goal request supplanted an interrupted task, that task was
		// already cancelled above and lastTaskID refers to the last COMPLETED
		// task (or "" for a fresh goal).

		// Parse the optional budget override (JSON or empty). Empty/invalid
		// → nil (unlimited). A parse error is logged but does not block the
		// send; the goal simply runs unlimited.
		var budgetOverride *goalpkg.GoalBudget
		if goalEnabled {
			budgetOverride = parseGoalBudget(goalBudget, m.log())
		}

		// When goal mode is enabled, the orchestrator receives the /goal-stripped
		// message; otherwise it receives the original (preprocessed) message.
		// goalMsg == msg when no /goal prefix is present, so this is safe even
		// when goal mode was enabled by the flag rather than the command.
		hmMsg := msg
		if goalEnabled {
			hmMsg = goalMsg
		}

		result, err := session.orchestrator.HandleMessage(ctx, hmMsg, id, core.HandleOptions{
			TaskID:             lastTaskID,
			UserSkills:         skills,
			UserAgents:         agents,
			ModelOverride:      modelOverride,
			ReasoningEffort:    reasoningEffort,
			SessionPlansDir:    config.SessionPlansDir(m.agentDir, session.ProjectID, id),
			PendingAttachments: pendingAttachments,
			PendingImages:      imageBlocks,
			Goal:               goalEnabled,
			GoalBudgetOverride: budgetOverride,
			ReviewMode:         reviewMode,
		})

		// Distinguish partial-success (incomplete plan) from total failure.
		incomplete := err != nil && errors.Is(err, orchestration.ErrExecutionIncomplete) && result != nil
		if incomplete {
			m.log().Warn("task completed with incomplete execution", "session_id", id, "task_id", lastTaskID, "error", err)
			err = nil
		}

		// Fallback: if the continuation failed BEFORE it started executing
		// (blackboard restore or routing error) and we had a TaskID, retry
		// fresh. shouldRetryContinuationFresh returns false only when this
		// send's continuation actually started executing — the anchor was
		// terminal before the send and its row was reactivated to
		// in_progress, so a fresh retry would orphan it. An anchor that was
		// ALREADY unfinished when the send began still falls back to fresh:
		// the resume path cannot restore it (same error every time), so the
		// banner would dead-end, and the stale-task sweep cancels the row
		// once the fresh run succeeds.
		// Preserves goal mode: a failed goal-on-continuation retries as a fresh
		// goal task rather than silently dropping the flag (and, when goal was
		// enabled via the "/goal" prefix, leaking the prefix into the fresh task
		// — hmMsg is already /goal-stripped, msg is not).
		if err != nil && m.shouldRetryContinuationFresh(id, lastTaskID, anchorWasUnfinished) {
			m.log().Warn("continuation failed, falling back to fresh workflow", "session_id", id, "task_id", lastTaskID, "error", err)
			session.mu.Lock()
			session.lastCompletedTaskID = ""
			session.mu.Unlock()
			result, err = session.orchestrator.HandleMessage(ctx, hmMsg, id, core.HandleOptions{
				TaskID:             "",
				UserSkills:         skills,
				UserAgents:         agents,
				ModelOverride:      modelOverride,
				ReasoningEffort:    reasoningEffort,
				SessionPlansDir:    config.SessionPlansDir(m.agentDir, session.ProjectID, id),
				PendingAttachments: pendingAttachments,
				PendingImages:      imageBlocks,
				Goal:               goalEnabled,
				GoalBudgetOverride: budgetOverride,
				ReviewMode:         reviewMode,
			})
			if err != nil && errors.Is(err, orchestration.ErrExecutionIncomplete) && result != nil {
				m.log().Warn("task completed with incomplete execution (after fallback)", "session_id", id, "error", err)
				err = nil
			}
		}

		if err != nil {
			// Check if it was a cancellation
			if ctx.Err() == context.Canceled {
				// On shutdown, leave the task in_progress so it can be
				// resumed after restart; only user-initiated cancels are
				// persisted as cancelled. The goal is abandoned (cancelled)
				// only on a user cancel, not on shutdown.
				if m.emitTaskCancelledUnlessShuttingDown(id) {
					action = liveActionDiscard
					m.abandonGoalIfUnfinished(id)
					m.persistCancellationIfUnfinished(id)
				}
				return
			}

			// Emit error event
			m.emitFunc(Event{
				SessionID: id,
				Type:      "error",
				Data: ErrorData{
					SessionID: id,
					Error:     err.Error(),
				},
			})
			m.emitAgentMetrics(id, "failed")
			m.emitResumableIfUnfinished(id, resumableReasonFromError(err))
			return
		}

		// Store the task ID for potential continuations
		if pbb, ok := result.Blackboard.(*PersistentBlackboard); ok {
			session.mu.Lock()
			session.lastCompletedTaskID = pbb.TaskID()
			session.mu.Unlock()
		}

		// Safety net: if context was cancelled but orchestrator returned no error,
		// still treat as cancellation — do not emit partial results as final.
		if ctx.Err() == context.Canceled {
			if m.emitTaskCancelledUnlessShuttingDown(id) {
				action = liveActionDiscard
				m.abandonGoalIfUnfinished(id)
				m.persistCancellationIfUnfinished(id)
			}
			return
		}

		// A cooperative pause is a clean checkpoint, not a degraded completion.
		// Emit session_paused so the UI shows a paused state (unlocked input,
		// Resume/Stop controls) instead of a misleading failed/resumable banner.
		// The task is persisted as paused (resumable); a later Resume re-enters.
		if result != nil && result.Status == orchestration.ExecutionStatusPaused {
			// Deactivate BEFORE the terminal event: a consumer reacting to
			// session_paused treats the session as free to resume/send, so it
			// must already observe active == false (see deactivateSessionTask).
			leftover := m.deactivateSessionTask(session, action)
			finished = true
			m.emitFunc(Event{SessionID: id, Type: "session_paused"})
			m.finishLiveLeftover(ctx, id, session, leftover)
			return
		}

		// Emit done event with result (carries the typed success contract;
		// degraded outcomes surface a resumable action or a fallback warning).
		action = liveActionFollowUp
		// Deactivate BEFORE emitting task_complete: the event is the frontend's
		// signal that the session is free for a Resume/Send, so a consumer
		// reacting to it must already observe active == false.
		leftover := m.deactivateSessionTask(session, action)
		finished = true
		m.emitTaskComplete(id, result, nil)
		m.finishLiveLeftover(ctx, id, session, leftover)
	}(taskCtx, text, activeSkills, activeAgents)

	return SendFresh, nil
}

// parseGoalBudget parses a goal-budget override string into a *goal.GoalBudget.
// The string is expected to be a JSON object with the goal.GoalBudget field
// (max_turns). An empty string returns nil (unlimited). An unparseable string
// is logged and returns nil so a malformed budget never blocks the send — the
// goal simply falls back to unlimited.
func parseGoalBudget(raw string, log *slog.Logger) *goalpkg.GoalBudget {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var b goalpkg.GoalBudget
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		log.Warn("goal budget override is not valid JSON; falling back to unlimited", "raw", raw, "error", err)
		return nil
	}
	return &b
}

// tryContinueInterruptedTask checks whether the session has an unfinished
// (interrupted) task and, if so, continues its ReAct cycle by appending the
// user's new message as a final seeded user-nudge turn. It returns true when
// it took the resume path (the caller must NOT proceed with the normal
// HandleMessage flow), and false when there is no unfinished task (the caller
// runs the normal route → plan → execute flow).
//
// The user message is rendered as a user turn in the LLM context (via the
// trajectory's Step.UserNudge, which stepsToMessages emits as a {role:user}
// message), so the agent sees the new instruction immediately after the prior
// trajectory. The task is NOT re-routed, NO new task is created, and NO new
// conversation-history pair is recorded — the task lifecycle simply continues.
// (Resume's recordResumeOutcome records the assistant side only, mirroring the
// app-restart ResumeTask path.)
//
// Because this path bypasses HandleMessage (step 0 there applies them), the
// per-request model/reasoning overrides are applied here via
// orchestrator.ApplyRequestOverrides, and pending attachments are flushed into
// the restored blackboard (mirroring HandleMessage's setupBlackboard
// continuation path). Both are no-ops when empty.
//
// The message_received event has already been emitted by SendMessage, so the
// UI shows the user message; the resumed task emits its own task lifecycle
// events (routing/context_fill/steps/task_complete) on top.
func (m *Manager) tryContinueInterruptedTask(
	ctx context.Context,
	id string,
	session *Session,
	message string,
	modelOverride, reasoningEffort string,
	pendingAttachments []orchestration.Attachment,
) bool {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return false
	}

	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(id)
	if err != nil {
		m.log().Warn("continue-interrupted-task: failed to look up unfinished task; falling back to fresh task", "session", id, "error", err)
		return false
	}
	if taskID == "" {
		return false
	}

	// Restore the blackboard (facts / step results) for the interrupted task.
	bb, err := RestoreBlackboard(taskID, id, adapter, nil)
	if err != nil {
		m.log().Warn("continue-interrupted-task: failed to restore blackboard; falling back to fresh task", "session", id, "error", err)
		return false
	}
	if bb == nil {
		return false
	}

	// Apply per-request model/reasoning overrides. This path bypasses
	// HandleMessage, where step 0 would otherwise apply them, so the resumed
	// task picks up the model/reasoning the user selected for this message
	// instead of silently inheriting the prior task's settings. No-op when
	// both overrides are empty.
	session.orchestrator.ApplyRequestOverrides(ctx, modelOverride, reasoningEffort)

	// Flush attachments staged by AttachFiles into the restored blackboard
	// (mirrors HandleMessage's setupBlackboard continuation path). bb is a
	// *PersistentBlackboard, so AddAttachment persists to the store.
	for _, a := range pendingAttachments {
		bb.AddAttachment(a)
	}

	// Load the persisted trajectory and append the user message as a final
	// nudge step so it renders as a {role:user} turn after the prior steps.
	resumeSteps, err := adapter.LoadTrajectory(taskID)
	if err != nil {
		m.log().Warn("continue-interrupted-task: failed to load trajectory; falling back to fresh task", "session", id, "error", err)
		return false
	}
	resumeSteps = append(resumeSteps, agent.Step{UserNudge: message})

	// Resolve the persisted routing decision (optional — Resume defaults to
	// the general domain when nil). The task is never re-routed.
	var routing *router.RoutingDecision
	if state, stateErr := adapter.LoadTaskState(taskID); stateErr != nil {
		m.log().Warn("continue-interrupted-task: failed to load task state; resuming without routing", "session", id, "error", stateErr)
	} else if state != nil {
		routing = state.RoutingDecision
	}

	// Resolve the prior task_failed_resumable banner so it does not linger
	// after the resumed execution finishes.
	m.resolveResumableTaskMessage(id, taskID, "resumed")

	result, err := session.orchestrator.Resume(ctx, bb, routing, config.SessionPlansDir(m.agentDir, session.ProjectID, id), resumeSteps, nil, "")

	// Shared completion handling (mirrors ResumeTask's goroutine tail).
	if err != nil && errors.Is(err, orchestration.ErrExecutionIncomplete) && result != nil {
		m.log().Warn("continued task completed with incomplete execution", "session_id", id, "error", err)
		err = nil
	}

	if err != nil {
		if ctx.Err() == context.Canceled {
			if m.emitTaskCancelledUnlessShuttingDown(id) {
				m.abandonGoalIfUnfinished(id)
				bb.CancelTask()
				m.deactivateSessionTask(session, liveActionDiscard)
				return true
			}
			m.deactivateSessionTask(session, liveActionNone)
			return true
		}
		m.emitFunc(Event{
			SessionID: id,
			Type:      "error",
			Data: ErrorData{
				SessionID: id,
				Error:     err.Error(),
			},
		})
		m.emitAgentMetrics(id, "failed")
		m.emitResumableIfUnfinished(id, resumableReasonFromError(err))
		m.deactivateSessionTask(session, liveActionNone)
		return true
	}

	// A cooperative pause during resume is a clean checkpoint (see the
	// SendMessage and ResumeTask goroutines): emit session_paused instead of a
	// degraded task_complete. Checked before the task-ID store below so a
	// paused result does not update lastCompletedTaskID (the task is not
	// completed). Deactivate BEFORE the terminal event so a consumer reacting
	// to session_paused already observes active == false (see
	// deactivateSessionTask).
	if result != nil && result.Status == orchestration.ExecutionStatusPaused {
		m.deactivateSessionTask(session, liveActionNone)
		m.emitFunc(Event{SessionID: id, Type: "session_paused"})
		return true
	}

	// Store the task ID for potential further continuations.
	if pbb, ok := result.Blackboard.(*PersistentBlackboard); ok {
		session.mu.Lock()
		session.lastCompletedTaskID = pbb.TaskID()
		session.mu.Unlock()
	}

	// Deactivate BEFORE emitting task_complete: the event is the frontend's
	// signal that the session is free for a Resume/Send, so a consumer reacting
	// to it must already observe active == false (see deactivateSessionTask).
	// The follow-up for queued live messages is drained and launched here; the
	// caller marks the run finished so its deferred epilogue does not
	// deactivate a second time and clobber the just-launched follow-up.
	leftover := m.deactivateSessionTask(session, liveActionFollowUp)
	m.emitTaskComplete(id, result, nil)
	m.finishLiveLeftover(ctx, id, session, leftover)
	return true
}

// ResumeTask checks for an unfinished task in the given session and resumes it.
// Returns nil if no unfinished task exists or if the task store is not configured.
// Invoked both by the manual Resume button (with the user's current model/reasoning
// selection) and on app restart to resume interrupted tasks. The optional
// modelOverride/reasoningEffort are applied (same as a fresh SendMessage) so a
// model/reasoning switch the user made before resuming is honored instead of
// silently inheriting the interrupted task's settings. The optional nudge is
// injected as a trailing user message into the first resumed turn (one-shot) —
// used by the nudge-resume path when a user sends a message into a paused session.
func (m *Manager) ResumeTask(ctx context.Context, id, modelOverride, reasoningEffort, nudge string) error {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()

	if ts == nil {
		return nil // no task persistence — nothing to resume
	}

	session, restoreErr := m.getOrRestoreSession(id)
	if restoreErr != nil {
		return fmt.Errorf("failed to restore session: %w", restoreErr)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", id)
	}

	// Archived sessions are read-only: fail fast before touching the task store
	// or restoring a blackboard. Mirrors the SendMessage guard.
	session.mu.Lock()
	archived := session.Archived
	session.mu.Unlock()
	if archived {
		return ErrSessionArchived
	}

	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(id)
	if err != nil {
		return fmt.Errorf("failed to check unfinished tasks: %w", err)
	}
	if taskID == "" {
		return nil // no unfinished task
	}

	// Load task state and restore blackboard.
	bb, err := RestoreBlackboard(taskID, id, adapter, nil)
	if err != nil {
		return fmt.Errorf("failed to restore blackboard: %w", err)
	}
	if bb == nil {
		return nil // task record not found (race condition or cleanup)
	}

	// Load task state. The routing decision and plan are OPTIONAL for resume:
	// a persisted routing decision is reused (the task is not re-routed), and
	// a plan is not required — the Conductor handles plan-less tasks via a
	// standalone checklist. state may be nil when no state was persisted.
	state, err := adapter.LoadTaskState(taskID)
	if err != nil {
		return fmt.Errorf("failed to load task state: %w", err)
	}

	// Resolve the persisted routing decision (may be nil — Resume defaults to
	// the general domain in that case) and original request (may be empty).
	var routing *router.RoutingDecision
	originalRequest := bb.GetOriginalRequest()
	if state != nil {
		routing = state.RoutingDecision
		if state.OriginalRequest != "" {
			originalRequest = state.OriginalRequest
		}
	}

	// Load the persisted trajectory so the resumed executor continues from the
	// checkpoint instead of starting fresh. An empty/nil trajectory is valid —
	// the Conductor simply starts a fresh ReAct loop.
	resumeSteps, err := adapter.LoadTrajectory(taskID)
	if err != nil {
		return fmt.Errorf("failed to load trajectory: %w", err)
	}

	// Load the persisted goal state. When present and non-terminal (paused or
	// still active), the orchestrator re-enters the goal loop instead of the
	// plain Conductor path. A nil goal state (non-goal task) resumes normally.
	goalState, err := adapter.LoadGoalState(taskID)
	if err != nil {
		return fmt.Errorf("failed to load goal state: %w", err)
	}

	// Determine RESEARCH mode from the project's research root (loaded before
	// the session lock to avoid a DB query while holding it).
	researchRoot, isResearch := m.researchProjectInfo(session.ProjectID)

	session.mu.Lock()
	if session.active {
		session.mu.Unlock()
		return errors.New("session is already processing a task")
	}
	// Manual compaction owns the session until it finishes (it swaps the
	// conversation history while the session is idle); resuming a task in
	// that window would race the swap. The compaction flow's own auto-resume
	// runs after the flag is cleared, so it is not blocked here.
	if session.compacting {
		session.mu.Unlock()
		return ErrSessionCompacting
	}
	session.active = true
	resumeDoneCh := make(chan struct{})
	session.done = resumeDoneCh
	taskCtx, cancel := context.WithCancel(ContextWithSessionID(ctx, id))
	taskCtx = sdktools.WithWorkspacePathNoProbe(taskCtx, session.WorkspacePath)
	taskCtx = sdktools.WithCaseInsensitivePaths(taskCtx, m.detectCaseInsensitive(session.WorkspacePath))
	taskCtx = sdktools.WithTempDir(taskCtx, session.TempDir)
	taskCtx = sdktools.WithCoherence(taskCtx, m.fileTracker)
	if session.ProjectID == project.NoProjectID {
		taskCtx = coretools.WithNoProject(taskCtx)
	}
	if isResearch {
		taskCtx = coretools.WithResearch(taskCtx)
		taskCtx = coretools.WithResearchRoot(taskCtx, researchRoot)
	}
	session.cancel = cancel
	session.mu.Unlock()

	// Apply per-request model/reasoning overrides. This path bypasses
	// HandleMessage, where step 0 would otherwise apply them, so the resumed
	// task picks up the model/reasoning the user selected instead of silently
	// inheriting the interrupted task's settings. No-op when both are empty.
	// Run after the active-check (so overrides never leak into a concurrently
	// running task) but before launching the Resume goroutine, so the emitter's
	// cached model is synchronized before the initial context_fill is emitted.
	session.orchestrator.ApplyRequestOverrides(ctx, modelOverride, reasoningEffort)

	// Snapshot envInfo under read lock
	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo != nil {
		taskCtx = sdktools.WithEnvInfo(taskCtx, envInfo)
	}

	// Inject auxiliary work directories (allowed roots + prompt list), and
	// attach a multi-root ignore checker so glob/ripgrep honour each root's
	// own .gitignore + .aiignore. dirs is loaded once and shared so the
	// (DB-hitting) loadWorkDirectories runs a single time per task. Allowed
	// roots ALWAYS include the implicit host temp roots (see implicitTempRoots),
	// also for No Project sessions; the prompt-facing list and the ignore
	// checker remain no-ops when no directories are configured.
	dirs := m.loadWorkDirectories(session)
	taskCtx = m.injectWorkDirectories(taskCtx, dirs)
	taskCtx = m.injectIgnoreChecker(taskCtx, session, dirs)

	// Emit resume event so the frontend knows a task is resuming.
	m.emitFunc(Event{
		SessionID: id,
		Type:      "task_resumed",
		Data: MessageReceivedData{
			SessionID: id,
			Text:      originalRequest,
		},
	})
	// session_resumed clears the UI's paused state (complementary to
	// session_paused) so the input re-locks and Pause/Stop controls show again.
	m.emitFunc(Event{SessionID: id, Type: "session_resumed"})

	// Mark the prior task_failed_resumable banner as resolved so it does not
	// reappear as pending after the resume goroutine finishes. Done here (at
	// the committed point, right before launching) so a failed restore still
	// leaves the banner actionable for a retry.
	m.resolveResumableTaskMessage(id, taskID, "resumed")

	// Launch goroutine (same pattern as SendMessage).
	go func() {
		action := liveActionNone
		finished := false
		defer close(resumeDoneCh)
		defer func() {
			if finished {
				return
			}
			leftover := m.deactivateSessionTask(session, action)
			m.finishLiveLeftover(ctx, id, session, leftover)
		}()

		result, err := session.orchestrator.Resume(taskCtx, bb, routing, config.SessionPlansDir(m.agentDir, session.ProjectID, id), resumeSteps, goalState, nudge)

		// Treat partial execution like the SendMessage path — deliver best-effort
		// output and rely on emitResumableIfUnfinished to expose resumability.
		if err != nil && errors.Is(err, orchestration.ErrExecutionIncomplete) && result != nil {
			m.log().Warn("resumed task completed with incomplete execution", "session_id", id, "error", err)
			err = nil
		}

		if err != nil {
			if taskCtx.Err() == context.Canceled {
				// On shutdown, leave the restored task in_progress so it can
				// be resumed after restart; only user-initiated cancels mark
				// the task as cancelled. The goal is abandoned (cancelled)
				// only on a user cancel, not on shutdown.
				if m.emitTaskCancelledUnlessShuttingDown(id) {
					action = liveActionDiscard
					m.abandonGoalIfUnfinished(id)
					bb.CancelTask()
				}
				return
			}

			m.emitFunc(Event{
				SessionID: id,
				Type:      "error",
				Data: ErrorData{
					SessionID: id,
					Error:     err.Error(),
				},
			})
			m.emitAgentMetrics(id, "failed")
			m.emitResumableIfUnfinished(id, resumableReasonFromError(err))
			return
		}

		// Safety net: if the context was cancelled but Resume returned no
		// error (the goal-loop path returns nil error on cancel because
		// resumeGoalLoop always returns nil), still treat it as a
		// cancellation — mark the task cancelled and return. This mirrors
		// the HandleMessage goroutine's safety net.
		if taskCtx.Err() == context.Canceled {
			if m.emitTaskCancelledUnlessShuttingDown(id) {
				action = liveActionDiscard
				m.abandonGoalIfUnfinished(id)
				bb.CancelTask()
			}
			return
		}

		// A cooperative pause during resume is a clean checkpoint (see the
		// HandleMessage goroutine): emit session_paused instead of a degraded
		// task_complete. The task stays resumable; a later Resume re-enters.
		// Checked before the task-ID store below so a paused result does not
		// update lastCompletedTaskID (the task is not completed).
		if result != nil && result.Status == orchestration.ExecutionStatusPaused {
			// Deactivate BEFORE the terminal event so a consumer reacting to
			// session_paused already observes active == false (see
			// deactivateSessionTask).
			leftover := m.deactivateSessionTask(session, action)
			finished = true
			m.emitFunc(Event{SessionID: id, Type: "session_paused"})
			m.finishLiveLeftover(ctx, id, session, leftover)
			return
		}

		// Store the task ID for potential continuations
		if pbb, ok := result.Blackboard.(*PersistentBlackboard); ok {
			session.mu.Lock()
			session.lastCompletedTaskID = pbb.TaskID()
			session.mu.Unlock()
		}

		action = liveActionFollowUp
		// Close the processing window BEFORE emitting task_complete: the event
		// is the frontend's signal that the session is free for a Resume/Send,
		// so a consumer reacting to it must already observe active == false.
		leftover := m.deactivateSessionTask(session, action)
		finished = true
		m.emitTaskComplete(id, result, nil)
		m.finishLiveLeftover(ctx, id, session, leftover)
	}()

	return nil
}

// CancelUnfinishedTask discards any unfinished task in the given session by
// marking it as cancelled in the task store. After this returns successfully,
// the session no longer has a resumable task and emitResumableIfUnfinished
// will not emit a "task_failed_resumable" event for it. Any deferred
// resume-compaction armed for the task's future resume is discarded too — it
// must not outlive the task it was armed for.
// Returns nil if no task store is configured or no unfinished task exists.
func (m *Manager) CancelUnfinishedTask(sessionID string) error {
	m.clearResumeCompaction(sessionID)
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return nil
	}

	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		return fmt.Errorf("failed to look up unfinished task: %w", err)
	}
	if taskID == "" {
		return nil
	}
	if err := adapter.PersistCancellation(taskID); err != nil {
		return fmt.Errorf("failed to mark task as cancelled: %w", err)
	}
	// Mark the prior task_failed_resumable banner as resolved so it does not
	// reappear as pending on session reload after the user discards the task.
	m.resolveResumableTaskMessage(sessionID, taskID, "cancelled")
	return nil
}

// PauseSession signals the currently-running task (any mode) for the session to
// pause at the next step boundary. It delegates to the orchestrator's universal
// PauseSession: the conductor's executor checks the pause signal at every step
// boundary (normal path and every goal-loop turn alike), stops with a paused
// checkpoint, and the request persists the task as paused + exits so a later
// ResumeSession/ResumeTask can re-enter. It is a no-op when no request is in
// flight or the orchestrator is not yet built.
func (m *Manager) PauseSession(sessionID string) error {
	session, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	orch := session.GetOrchestrator()
	if orch == nil {
		return errors.New("orchestrator not initialized")
	}
	// Flag the pausing window under the session lock: from here until the
	// running request's epilogue flips it off, live sends are rejected with
	// ErrPausePending (the UI mirrors this by locking the input). The flag is
	// set under the lock BEFORE the pause signal is flipped (after unlock), so
	// a send that races the signal flip still observes pausing=true and is
	// rejected — the window stays closed for the whole pause-in-flight period.
	session.mu.Lock()
	if session.active {
		session.pausing = true
	}
	session.mu.Unlock()
	orch.PauseSession()
	return nil
}

// ResumeSession re-enters the execution loop for a paused (or still-active,
// non-terminal) task. It delegates to ResumeTask, which loads the unfinished
// task + persisted state (trajectory, goal state) and dispatches to the
// orchestrator's resume path. The optional nudge is injected as a trailing
// user message into the first resumed turn (one-shot) — the nudge-resume path
// when a user sends a message into a paused session. The optional
// modelOverride/reasoningEffort are forwarded so a model/reasoning switch made
// before resuming is honored. Returns nil if there is no resumable task.
func (m *Manager) ResumeSession(ctx context.Context, sessionID, modelOverride, reasoningEffort, nudge string) error {
	return m.ResumeTask(ctx, sessionID, modelOverride, reasoningEffort, nudge)
}

// hasPausedUnfinishedTask reports whether the session's resumable unfinished
// task is specifically in the paused status. Used by the SendMessage
// nudge-resume router to decide whether a new message resumes the paused task
// instead of starting a fresh one. Returns false when no task store is
// configured or on lookup error (best-effort: a failure falls through to the
// normal SendMessage path).
func (m *Manager) hasPausedUnfinishedTask(sessionID string) bool {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return false
	}
	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil || taskID == "" {
		return false
	}
	state, err := adapter.LoadTaskState(taskID)
	if err != nil || state == nil {
		return false
	}
	return state.Status == "paused"
}

// SessionRuntimeStatus describes the live and persisted execution state of a
// session, so the frontend can reconstruct "is something running / resumable /
// paused" after app restart or session switch instead of assuming idle.
type SessionRuntimeStatus struct {
	Active            bool   `json:"active"`
	HasUnfinishedTask bool   `json:"has_unfinished_task"`
	UnfinishedTaskID  string `json:"unfinished_task_id,omitempty"`
	// Paused is true when the resumable unfinished task is in the "paused"
	// status — a cooperative pause checkpoint that the user can resume (with
	// an optional nudge) or send a new message into (treated as a nudge-resume).
	Paused bool `json:"paused"`
	// Compacting is true while a manual context compaction is in flight. The
	// UI locks the input and swaps the compact button for a cancel button.
	Compacting bool `json:"compacting"`
	// Activity is the session's last user-facing activity label
	// ("Thinking...", "Routing request...", "Generating response...", ...)
	// tracked by the emitter. It lets the frontend replace the frozen
	// activityStatus left over from before a session/project switch — e.g. a
	// session that advanced past routing while unobserved must not keep
	// displaying "Routing request...". Authoritative only while Active is
	// true: terminal events (task_complete/error/cancel) are emitted by the
	// Manager outside the emitter and clear the activity on the frontend.
	Activity string `json:"activity,omitempty"`
	// Streaming is true while an assistant stream is open (assistant_chunk
	// emitted without the closing assistant_done). When false, the frontend
	// clears stale streaming text for the session — a stream that ended while
	// the session was in the background would otherwise render a frozen
	// partial answer forever (the full answer arrives via history reload).
	Streaming bool `json:"streaming"`
}

// GetSessionRuntimeStatus returns whether a task is currently running in the
// session (in-memory) and whether an unfinished (resumable) task is persisted
// in the task store. When a resumable task exists, Paused reports whether it is
// in the paused status. It never restores a session as a side effect.
func (m *Manager) GetSessionRuntimeStatus(sessionID string) (SessionRuntimeStatus, error) {
	var status SessionRuntimeStatus

	// Memory-only lookup: a session that is not in memory cannot be active,
	// and restoring it here would be an unwanted side effect for a status poll.
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess != nil {
		status.Active = sess.IsActive()
		status.Compacting = sess.IsCompacting()
		// Live activity/streaming from the session's emitter — the same
		// signals the (possibly unmounted) frontend listeners would have
		// received as events. Reading the emitter pointer under sess.mu keeps
		// the pointer stable; the activity/token states have their own locks.
		sess.mu.Lock()
		emitter := sess.emitter
		sess.mu.Unlock()
		if emitter != nil {
			status.Activity = emitter.LastActivity()
			status.Streaming = emitter.StreamingActive()
		}
	}

	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts != nil {
		adapter := NewTaskStoreAdapter(ts)
		taskID, err := adapter.GetUnfinishedTaskID(sessionID)
		if err != nil {
			return status, fmt.Errorf("failed to look up unfinished task: %w", err)
		}
		if taskID != "" {
			status.HasUnfinishedTask = true
			status.UnfinishedTaskID = taskID
			// Load the task record to distinguish a paused checkpoint from a
			// plain in-progress/failed task. LoadTaskState returns the status
			// field; a missing state is treated as non-paused.
			if state, stateErr := adapter.LoadTaskState(taskID); stateErr == nil && state != nil {
				status.Paused = state.Status == "paused"
			} else if stateErr != nil {
				m.log().Warn("get session runtime status: failed to load task state", "session", sessionID, "error", stateErr)
			}
		}
	}

	return status, nil
}

// LiveTokenSnapshot returns the in-memory token/fill state of a session — the
// same values the emitter broadcasts via session_tokens / context_fill. It
// exists so GetSessionTokens can serve live used/max tokens (and a fresh
// fill/model) for a session that is mid-task, values the persisted session
// row only partially carries. Memory-only: no session restore side effect;
// ok=false when the session is not in memory (caller falls back to the store).
func (m *Manager) LiveTokenSnapshot(sessionID string) (TokenSnapshot, bool) {
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess == nil {
		return TokenSnapshot{}, false
	}
	sess.mu.Lock()
	emitter := sess.emitter
	sess.mu.Unlock()
	if emitter == nil {
		return TokenSnapshot{}, false
	}
	return emitter.TokenSnapshot(), true
}

// ValidateLiveSend performs the live-send gate checks without queuing: when a
// task is currently running, it returns the same rejection errors the live
// branch of sendMessage would (pause window, goal, skill/agent references).
// The frontend API calls this BEFORE persisting the user message so a rejected
// live send never leaves a phantom persisted message. It is a memory-only
// lookup (no session restore side effect); when no task is running it is a
// no-op. The authoritative re-check still happens under the session lock in
// sendMessage — a message that passes here but finds the task finished
// afterwards simply starts a normal task.
func (m *Manager) ValidateLiveSend(sessionID string, goal bool, text string, activeSkills, activeAgents []string) error {
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess == nil {
		return nil
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	// A compacting session rejects sends even when no task is running: the
	// compaction flow owns the idle window between the pause landing and the
	// auto-resume. Mirrors the authoritative guard in sendMessage.
	if sess.compacting {
		return ErrSessionCompacting
	}
	if !sess.active {
		return nil
	}
	return liveSendRejectionLocked(sess, goal, text, activeSkills, activeAgents)
}

// emitTaskCancelledUnlessShuttingDown emits the "task_cancelled" event for the
// session unless the manager is shutting down. It returns true when the caller
// should persist the cancellation (user-initiated cancel), and false when the
// manager is shutting down — in that case the task must stay in_progress so it
// remains resumable after restart, so the caller skips persistence entirely.
func (m *Manager) emitTaskCancelledUnlessShuttingDown(id string) bool {
	if m.shuttingDown.Load() {
		return false
	}
	m.emitFunc(Event{
		SessionID: id,
		Type:      "task_cancelled",
		Data:      TaskCancelledData{SessionID: id},
	})
	m.emitAgentMetrics(id, "cancelled")
	return true
}

// emitAgentMetrics emits the aggregated agent quality metrics (parse errors,
// loop-detector nudges/aborts, steps, output tokens and the active Small-LLM
// profile) for the task run that just finished — complete, cancel or failure.
// The counters reset afterwards, so one agent_metrics event covers exactly
// one task run. No-op when the session is no longer tracked.
func (m *Manager) emitAgentMetrics(sessionID, finish string) {
	s, ok := m.GetSession(sessionID)
	if !ok || s == nil || s.emitter == nil {
		return
	}
	s.emitter.EmitAgentMetrics(finish)
}

// persistCancellationIfUnfinished marks the session's unfinished task (if any)
// as cancelled in the task store. Best-effort: errors are logged only.
func (m *Manager) persistCancellationIfUnfinished(sessionID string) {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return
	}
	adapter := NewTaskStoreAdapter(ts)
	tid, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		m.log().Warn("failed to look up unfinished task on cancel", "session", sessionID, "error", err)
		return
	}
	if tid == "" {
		return
	}
	if err := adapter.PersistCancellation(tid); err != nil {
		m.log().Warn("failed to persist cancellation", "task", tid, "error", err)
	}
}

// persistPauseIfUnfinished marks the session's unfinished task as paused in
// the task store. It is the graceful-shutdown counterpart of
// persistCancellationIfUnfinished: instead of cancelling an active task (or
// leaving it as a stale in_progress), the task is checkpointed as paused so it
// survives app restart in a clearly resumable state and SessionRuntimeStatus
// reports Paused=true. Only an in_progress task is paused — a task that
// already completed/failed/was cancelled (or was paused cooperatively) keeps
// its existing status. Best-effort: errors are logged only.
func (m *Manager) persistPauseIfUnfinished(sessionID string) {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return
	}
	adapter := NewTaskStoreAdapter(ts)
	tid, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		m.log().Warn("failed to look up unfinished task on shutdown pause", "session", sessionID, "error", err)
		return
	}
	if tid == "" {
		return
	}
	// Only an in_progress task should be paused on shutdown. A task that
	// already failed, was cancelled, or was paused cooperatively must keep its
	// real status — pausing it would mask the actual outcome.
	state, err := adapter.LoadTaskState(tid)
	if err != nil {
		m.log().Warn("failed to load task state on shutdown pause", "task", tid, "error", err)
		return
	}
	if state == nil || state.Status != "in_progress" {
		return
	}
	if err := adapter.PersistPause(tid); err != nil {
		m.log().Warn("failed to persist pause on shutdown", "task", tid, "error", err)
	}
}

// abandonGoalIfUnfinished terminalizes the unfinished task's goal state as
// cancelled (terminal) on a user-initiated cancel so a later resume does not
// re-enter the goal loop. On shutdown the goal is left active (non-terminal)
// so it survives restart — this method is only called inside the
// emitTaskCancelledUnlessShuttingDown(id) == true branch (i.e. NOT shutting
// down). Must be called BEFORE persistCancellationIfUnfinished/bb.CancelTask
// because a cancelled task is no longer "unfinished" and GetUnfinishedTaskID
// would not find it. Best-effort: errors are logged only. No-op when there is
// no task store, no unfinished task, or no non-terminal goal state.
func (m *Manager) abandonGoalIfUnfinished(sessionID string) {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return
	}
	adapter := NewTaskStoreAdapter(ts)
	tid, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		m.log().Warn("failed to look up unfinished task on cancel (goal abandon)", "session", sessionID, "error", err)
		return
	}
	if tid == "" {
		return
	}
	gs, err := adapter.LoadGoalState(tid)
	if err != nil {
		m.log().Warn("failed to load goal state on cancel", "task", tid, "error", err)
		return
	}
	if gs == nil || gs.Status.IsTerminal() {
		return
	}
	gs.Status = goalpkg.StatusCancelled
	if err := adapter.PersistGoalState(tid, gs); err != nil {
		m.log().Warn("failed to persist goal cancellation", "task", tid, "error", err)
	}
}

// abandonUnfinishedTaskForGoal cancels the session's unfinished task (if any)
// so a goal request can start a fresh goal pursuit instead of resuming the
// interrupted task. It persists the cancellation, resolves any pending
// task_failed_resumable banner so it does not linger, and emits a service
// event so the user sees the interrupted task was abandoned for the goal.
// Best-effort: errors are logged only. No-op when there is no task store or
// no unfinished task.
func (m *Manager) abandonUnfinishedTaskForGoal(id string) {
	// The interrupted task being abandoned may carry an armed deferred
	// resume-compaction (a manual no-op compaction deferred to its resume) —
	// drop it so the goal loop (or any later task) does not inherit the
	// forced compaction chosen for the abandoned task.
	m.clearResumeCompaction(id)
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return
	}
	adapter := NewTaskStoreAdapter(ts)
	tid, err := adapter.GetUnfinishedTaskID(id)
	if err != nil {
		m.log().Warn("goal-on-resume: failed to look up unfinished task to abandon", "session", id, "error", err)
		return
	}
	if tid == "" {
		return
	}
	if err := adapter.PersistCancellation(tid); err != nil {
		m.log().Warn("goal-on-resume: failed to cancel unfinished task", "task", tid, "error", err)
	}
	m.resolveResumableTaskMessage(id, tid, "abandoned_for_goal")
	m.emitFunc(Event{
		SessionID: id,
		Type:      "service",
		Data: map[string]any{
			"content": "Interrupted task abandoned to start goal mode.",
			"phase":   "orchestration",
		},
	})
}

// shouldRetryContinuationFresh reports whether a failed continuation attempt
// (HandleMessage invoked with a TaskID) should be retried as a fresh task.
// anchorWasUnfinished is the caller's PRE-HandleMessage snapshot of whether
// the anchor was already the session's unfinished task when the send began
// (see SendMessage); it disambiguates the two ways an unfinished lookup can
// return the anchor after a failed attempt:
//
//   - anchorWasUnfinished=false: a normal send only reaches HandleMessage
//     with a TaskID when no unfinished task existed when the send began (an
//     unfinished task is continued via tryContinueInterruptedTask instead),
//     and the anchor is reactivated back to in_progress only once its
//     execution actually started (post-routing — see the orchestrator's
//     reactivateContinuationTask). An unfinished lookup returning the anchor
//     therefore means the continuation's own execution started and failed
//     mid-flight: retrying fresh would abandon the reactivated row as an
//     orphaned in_progress task — the session would then report
//     has_unfinished_task=true forever and re-inject the "Task failed /
//     Resume" banner after every restart over an otherwise completed
//     session. In that case the caller must NOT retry fresh; the error
//     falls through to the shared error path, which emits the resumable
//     banner for the anchor instead.
//
//   - anchorWasUnfinished=true: the anchor predates the send. This happens
//     when lastCompletedTaskID was restored without a status check (it may
//     point at a failed task after an app restart) and the resume path
//     itself could not restore the task's blackboard or trajectory, falling
//     back to a continuation attempt on a row that never was terminal. A
//     failed attempt here must NOT dead-end on the resumable banner — the
//     resume path would hit the same restore error — so a fresh retry is
//     allowed. Should the fresh run succeed, sweepStaleUnfinishedTasks
//     cancels the leftover anchor row, so no orphan remains.
func (m *Manager) shouldRetryContinuationFresh(sessionID, lastTaskID string, anchorWasUnfinished bool) bool {
	if lastTaskID == "" {
		return false
	}
	if anchorWasUnfinished {
		return true
	}
	return m.unfinishedTaskID(sessionID) != lastTaskID
}

// unfinishedTaskID returns the ID of the most recent unfinished (in_progress
// or failed) task for the session, or "" if there is none, no task store is
// configured, or the lookup errors. It is a pure lookup — it never emits.
func (m *Manager) unfinishedTaskID(sessionID string) string {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return ""
	}

	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		m.log().Warn("failed to get unfinished task ID", "session", sessionID, "error", err)
		return ""
	}
	return taskID
}

// emitResumableIfUnfinished checks whether the session has an unfinished task
// in the task store and, if so, emits a "task_failed_resumable" event so the
// frontend can offer a Resume button. It returns true when the event was
// emitted, so callers that KNOW the execution was degraded can surface a
// fallback warning when the resumable safety net is unavailable (nil task
// store, lookup error, or no unfinished record). reason is a concise,
// contextual cause prepended to the banner message and carried in the
// structured Reason field; pass "" for the generic message.
//
// This helper emits ONLY the banner. The "agent_metrics" counters reset on
// every emission, so exactly one agent_metrics event must fire per terminal
// path — emitting it here would double-count on the emitTaskComplete path
// (which already emits) and zero out the real per-run report in the UI.
func (m *Manager) emitResumableIfUnfinished(sessionID, reason string) bool {
	taskID := m.unfinishedTaskID(sessionID)
	if taskID == "" {
		return false
	}

	message := "Plan execution failed. You can resume to retry from where it left off."
	if reason != "" {
		message = reason + " You can resume to retry from where it left off."
	}

	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      "task_failed_resumable",
		Data: TaskFailedResumableData{
			Message: message,
			TaskID:  taskID,
			Reason:  reason,
		},
	})
	return true
}

// completionReason maps a completion outcome to a concise, readable reason
// sentence for the resume banner. Returns "" for a full success (the caller
// must not call emitResumableIfUnfinished for successes — see emitTaskComplete).
func completionReason(completion string) string {
	switch completion {
	case "partial":
		return "Execution reached a partial state."
	case "failed":
		return "Execution failed."
	case "aborted":
		return "Execution was aborted."
	default:
		return ""
	}
}

// resumableReasonFromError derives a concise, user-facing reason for the
// resume banner from a terminal execution error. It trims to the first line,
// capitalizes the first letter, and caps the length so the banner stays
// readable. Returns "" when err is nil, leaving the generic message intact.
func resumableReasonFromError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return ""
	}
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	const maxReasonRunes = 140
	if r := []rune(msg); len(r) > maxReasonRunes {
		msg = string(r[:maxReasonRunes-1]) + "…"
	}
	if r := []rune(msg); len(r) > 0 {
		msg = strings.ToUpper(string(r[0])) + string(r[1:])
	}
	return msg
}

// resolveResumableTaskMessage marks the persisted "task_failed_resumable"
// message for the given task as resolved, so the Resume/Cancel banner does
// not reappear as pending on session reload after the user resumes or cancels.
// Best-effort: errors are logged only — the in-memory UI is already updated
// optimistically by the frontend, and a missed persist is self-healing via
// stale reconciliation on the next reload.
func (m *Manager) resolveResumableTaskMessage(sessionID, taskID, decision string) {
	m.mu.RLock()
	store := m.sessionStore
	m.mu.RUnlock()
	if store == nil || taskID == "" {
		return
	}
	extra := map[string]any{"resolved": true}
	if decision != "" {
		extra["decision"] = decision
	}
	if err := store.ResolvePendingMessage(context.Background(), sessionID, "task_failed_resumable", "task_id", taskID, extra); err != nil {
		m.log().Warn("failed to resolve persisted task_failed_resumable message",
			"session", sessionID, "task", taskID, "decision", decision, "error", err)
	}
}

// taskCompletionInfo derives the frontend-facing success contract from the
// typed execution status on a HandleResult.
func taskCompletionInfo(result *core.HandleResult) (success bool, completion string) {
	if result == nil {
		return true, "full"
	}
	switch result.Status {
	case orchestration.ExecutionStatusPartial, orchestration.ExecutionStatusCancelled, orchestration.ExecutionStatusPaused:
		return false, "partial"
	case orchestration.ExecutionStatusFailed:
		return false, "failed"
	case orchestration.ExecutionStatusAborted:
		return false, "aborted"
	default:
		return true, "full"
	}
}

// emitTaskComplete emits the "task_complete" event with the typed success
// contract and guarantees the degraded-outcome surfacing: for non-successful
// completions it either emits "task_failed_resumable" or, when the resumable
// safety net cannot deliver (no task store, lookup failure, no unfinished
// record), a visible service warning — never a silent visual success.
func (m *Manager) emitTaskComplete(sessionID string, result *core.HandleResult, plan *orchestration.Plan) {
	success, completion := taskCompletionInfo(result)
	data := TaskCompleteData{
		SessionID:  sessionID,
		Success:    success,
		Completion: completion,
	}
	if result != nil {
		data.Output = result.Output
		data.RoutingDecision = result.RoutingDecision
		data.Plan = result.Plan
		data.Reflections = result.Reflections
	}
	if plan != nil {
		data.Plan = plan
	}
	m.emitFunc(Event{SessionID: sessionID, Type: "task_complete", Data: data})
	m.emitAgentMetrics(sessionID, completion)

	// On a genuinely successful completion, NEVER offer a resume action — the
	// result above is final. If the task that just ran is still marked
	// unfinished, the completion write raced or was dropped; surface a
	// persistence warning instead of a misleading "resume" banner (see
	// warnIfCompletionDidNotPersist). Any OTHER unfinished row in the session
	// is a stale orphan of an abandoned continuation — cancel it so the
	// session's has_unfinished_task flag finally clears (see
	// sweepStaleUnfinishedTasks).
	if success {
		m.warnIfCompletionDidNotPersist(sessionID, result)
		m.sweepStaleUnfinishedTasks(sessionID, result)
		return
	}

	resumableEmitted := m.emitResumableIfUnfinished(sessionID, completionReason(completion))
	if !resumableEmitted {
		m.log().Warn("degraded task completion without resumable safety net", "session", sessionID, "completion", completion)
		m.emitFunc(Event{
			SessionID: sessionID,
			Type:      "service",
			Data: map[string]any{
				"content": fmt.Sprintf("Task finished with %s execution, but it cannot be resumed. Review the output above.", completion),
				"phase":   "orchestration",
			},
		})
	}
}

// warnIfCompletionDidNotPersist checks whether the task that just completed
// successfully is still marked unfinished. When it is, the completion write
// (CompleteTask) raced or was dropped, so a service warning is emitted so the
// user knows a persistence issue occurred — but the result above is valid and
// no misleading "resume" banner is offered. A different (stale) unfinished
// task is ignored, as it predates this successful execution.
func (m *Manager) warnIfCompletionDidNotPersist(sessionID string, result *core.HandleResult) {
	var completedTaskID string
	if result != nil {
		if pbb, ok := result.Blackboard.(core.PersistableBlackboard); ok {
			completedTaskID = pbb.TaskID()
		}
	}
	if completedTaskID == "" {
		return
	}
	if unfinished := m.unfinishedTaskID(sessionID); unfinished == completedTaskID {
		m.log().Error("task succeeded but completion did not persist", "session", sessionID, "task", completedTaskID)
		m.emitFunc(Event{
			SessionID: sessionID,
			Type:      "service",
			Data: map[string]any{
				"content": "Task completed successfully, but a persistence issue was detected. The result above is valid.",
				"phase":   "persistence",
			},
		})
	}
}

// maxStaleTaskSweep bounds the stale-task sweep loop defensively: a store
// that keeps reporting the same unfinished row (e.g. a CancelTask write that
// silently fails) must not turn the sweep into an infinite loop.
const maxStaleTaskSweep = 8

// sweepStaleUnfinishedTasks cancels leftover unfinished (in_progress /
// paused / failed) task rows in the session after a successful completion and
// resolves their persisted task_failed_resumable banners. Such rows are stale
// by construction: a session executes one task at a time and a fresh task is
// only started when no unfinished task exists, so any unfinished row other
// than the just-completed one is the orphan of an abandoned continuation
// (historically the reactivated-then-abandoned anchor of the
// fresh-workflow fallback). Left in place, the row keeps the session's
// has_unfinished_task=true forever, so every app restart re-injects the
// "Task failed / Resume" banner over an otherwise successfully completed
// session. Best-effort: errors are logged only.
func (m *Manager) sweepStaleUnfinishedTasks(sessionID string, result *core.HandleResult) {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return
	}

	var completedTaskID string
	if result != nil {
		if pbb, ok := result.Blackboard.(core.PersistableBlackboard); ok {
			completedTaskID = pbb.TaskID()
		}
	}
	// Symmetric with warnIfCompletionDidNotPersist: without a persistable
	// blackboard there is no ID identifying the task that just completed, so
	// the sweep cannot tell that task's (possibly racing) unfinished row
	// apart from a stale orphan. Cancel nothing rather than risk discarding
	// the live row.
	if completedTaskID == "" {
		return
	}

	adapter := NewTaskStoreAdapter(ts)
	for range maxStaleTaskSweep {
		taskID, err := adapter.GetUnfinishedTaskID(sessionID)
		if err != nil {
			m.log().Warn("stale-task sweep: unfinished lookup failed", "session", sessionID, "error", err)
			return
		}
		// Done when nothing is unfinished, or when the lookup reaches the row
		// of the task that just completed (its own completion raced or was
		// dropped — warnIfCompletionDidNotPersist already surfaced that). The
		// lookup returns one row at a time, so any older orphan sitting behind
		// that row is invisible to this sweep and stays for the NEXT
		// successful completion to cancel — best-effort by design.
		if taskID == "" || taskID == completedTaskID {
			return
		}
		if err := adapter.PersistCancellation(taskID); err != nil {
			m.log().Warn("stale-task sweep: failed to cancel orphaned task", "session", sessionID, "task", taskID, "error", err)
			return
		}
		// Resolve the orphan's persisted resume banner so it does not
		// reappear as pending on the next session reload.
		m.resolveResumableTaskMessage(sessionID, taskID, "cancelled")
		m.log().Info("cancelled orphaned unfinished task superseded by a successful completion",
			"session", sessionID, "task", taskID)
	}
}

// CancelTask cancels the currently running task in a session.
// It signals cancellation and waits (with timeout) for the task goroutine to finish.
func (m *Manager) CancelTask(id string) error {
	session, err := m.getOrRestoreSession(id)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", id)
	}

	session.mu.Lock()

	if !session.active {
		session.mu.Unlock()
		// Nothing is running, but a cooperatively paused (or otherwise
		// unfinished, resumable) task is still cancellable via the Stop
		// button. Discard it and emit task_cancelled so the frontend leaves
		// the paused state. When there is no unfinished task to discard, keep
		// ErrNoActiveTask so callers can still distinguish "nothing running"
		// from a successful cancellation.
		return m.cancelUnfinishedTask(id)
	}

	doneCh := session.done
	if session.cancel != nil {
		session.cancel()
	}
	session.mu.Unlock()

	// Wait for the goroutine to finish so the task_cancelled event is emitted
	// before this method returns to the frontend.
	if doneCh != nil {
		select {
		case <-doneCh:
		case <-time.After(m.stopTimeout):
			m.log().Warn("timed out waiting for task goroutine to stop on cancel", "session_id", id)
		}
	}

	return nil
}

// cancelUnfinishedTask discards the session's unfinished (e.g. cooperatively
// paused) task and emits task_cancelled so the frontend leaves the
// paused/resumable state. It is the non-active counterpart of CancelTask: the
// running-task goroutine has already exited, so instead of signalling a
// context it flips the persisted task to cancelled and emits the terminal
// event directly. Returns ErrNoActiveTask when there is no unfinished task to
// cancel, preserving the sentinel callers rely on to distinguish "nothing
// running" from a successful cancellation.
func (m *Manager) cancelUnfinishedTask(sessionID string) error {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return ErrNoActiveTask
	}

	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		return fmt.Errorf("failed to look up unfinished task: %w", err)
	}
	if taskID == "" {
		return ErrNoActiveTask
	}
	if err := adapter.PersistCancellation(taskID); err != nil {
		return fmt.Errorf("failed to mark task as cancelled: %w", err)
	}
	// Mark the prior task_failed_resumable banner (if any) as resolved so it
	// does not reappear as pending on reload after the user cancels.
	m.resolveResumableTaskMessage(sessionID, taskID, "cancelled")

	// Emit the same terminal event the active-task cancel path emits so the UI
	// clears the paused state and shows "Task was cancelled". A cooperatively
	// paused run has not yet emitted agent metrics (pause is a checkpoint, not
	// a terminal state), so this is the single terminal emission for the run.
	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      "task_cancelled",
		Data:      TaskCancelledData{SessionID: sessionID},
	})
	m.emitAgentMetrics(sessionID, "cancelled")

	return nil
}

// GetBlackboardState returns the current blackboard state for a session.
// It uses the in-memory lastCompletedTaskID if available, otherwise falls back
// to the most recent task ID from the database.
// Returns nil, nil if no task state is available.
func (m *Manager) GetBlackboardState(sessionID string) (*BlackboardState, error) {
	sess, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to restore session: %w", err)
	}
	if sess == nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()

	if ts == nil {
		return nil, nil // no task persistence — no blackboard state
	}

	// Try in-memory lastCompletedTaskID first.
	sess.mu.Lock()
	taskID := sess.lastCompletedTaskID
	sess.mu.Unlock()

	// Fallback: query the database for the latest task.
	if taskID == "" {
		dbTaskID, dbErr := ts.GetLatestTaskID(context.Background(), sessionID)
		if dbErr != nil {
			return nil, fmt.Errorf("failed to get latest task ID: %w", dbErr)
		}
		if dbTaskID == "" {
			return nil, nil // no tasks for this session
		}
		taskID = dbTaskID
	}

	adapter := NewTaskStoreAdapter(ts)
	state, err := adapter.LoadTaskState(taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to load task state: %w", err)
	}
	if state == nil {
		return nil, nil
	}

	return &BlackboardState{TaskState: state}, nil
}

// BlackboardState wraps a core.TaskState for the GetBlackboardState API.
type BlackboardState struct {
	TaskState *core.TaskState
}
