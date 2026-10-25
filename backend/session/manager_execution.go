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
	"sort"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	e2spkg "github.com/v0lka/c0wrk/core/e2s"
	goalpkg "github.com/v0lka/c0wrk/core/goal"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/units"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/ignore"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/pathutil"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ErrNoActiveTask is returned by CancelTask when no task is running; callers
// that don't care treat it as non-fatal via errors.Is.
var ErrNoActiveTask = errors.New("no active task to cancel")

// ErrSessionArchived is returned by SendMessage/ResumeTask for an archived
// session (read-only: no new/resumed tasks). The UI hides the input shell,
// but this guard is the single server-side choke point — it also covers
// inline action panels and any other manager caller. Restore the session to
// clear the flag.
var ErrSessionArchived = errors.New("session is archived: restore it before sending messages or resuming tasks")

// ErrPausePending is returned by SendMessage while the running task has been
// signalled to pause but the cooperative pause has not landed (the executor
// stops at its next step boundary). Sends are rejected in that window to
// avoid racing pause→paused: once session_paused is emitted, a message
// becomes a nudge-resume instead. The UI locks the input; this is the
// server-side backstop.
var ErrPausePending = errors.New("session is pausing — send again once the pause completes")

// finishLiveLeftover launches the follow-up task for live messages queued but
// never delivered before the request finished (leftover is nil for every
// other epilogue action). The messages were persisted and rendered at send
// time, so the follow-up re-enters sendMessage as a presented relaunch (no
// duplicate message_received, no title regen) on a FRESH context — the
// original task's cancel func must not govern it. When a manual compaction
// owns the session (the follow-up send hits the compacting guard), the
// drained messages are re-queued on the orchestrator instead of lost: live
// text reaches the model only through that queue, and the first epilogue
// after the compaction releases the session drains it again.
func (m *Manager) finishLiveLeftover(_ context.Context, id string, session *Session, leftover []string) {
	if len(leftover) == 0 {
		return
	}
	joined := strings.Join(leftover, "\n\n")
	m.log().Info("launching follow-up task for undelivered live messages", "session_id", id, "count", len(leftover))
	if _, err := m.sendMessage(ContextWithSessionID(context.Background(), id), id, joined, joined, "", nil, nil, nil, "", "", false, "", false, false, true); err != nil {
		if errors.Is(err, ErrSessionCompacting) && m.requeueLiveMessages(session, joined) {
			m.log().Info("follow-up deferred by manual compaction: live messages re-queued", "session_id", id)
			m.emitFunc(Event{
				SessionID: id,
				Type:      "service",
				Data: map[string]any{
					"content": "Queued message is held while the session compacts context; it will be delivered when compaction finishes.",
					"phase":   "orchestration",
				},
			})
			return
		}
		m.log().Error("failed to launch follow-up task for live messages", "session_id", id, "error", err)
		// A service notice, not an error: this failure does not settle the
		// just-finished task, and `error` is reserved for terminal events
		// the UI treats as "the run ended" (clears task/streaming state).
		m.emitFunc(Event{
			SessionID: id,
			Type:      "service",
			Data: map[string]any{
				"content": "Queued message could not start a follow-up task: " + err.Error(),
				"phase":   "orchestration",
			},
		})
	}
}

// requeueLiveMessages puts live-message text back on the session's
// orchestrator live queue after a follow-up send was rejected by the
// compaction guard. Returns false when the session or its orchestrator is
// gone (e.g. the session was deleted mid-flight) — the caller then falls
// back to the plain error notice.
func (m *Manager) requeueLiveMessages(session *Session, joined string) bool {
	if session == nil {
		return false
	}
	session.mu.Lock()
	orch := session.orchestrator
	session.mu.Unlock()
	if orch == nil {
		return false
	}
	orch.QueueLiveUserMessage(joined)
	return true
}

// deactivateSessionTask flips the session back to idle and, for a follow-up
// action, drains the queued live messages under the same lock hold (returning
// the drained leftovers; nil for any other action). Terminal events MUST be
// emitted AFTER this call: a consumer reacting to task_complete/session_paused
// treats the session as free to resume/send, so it must already observe
// active == false — otherwise a fast follow-up hits a spurious "already
// processing" error.
func (m *Manager) deactivateSessionTask(session *Session, action liveAction) []string {
	session.mu.Lock()
	defer session.mu.Unlock()

	session.active = false
	session.cancel = nil
	session.done = nil
	session.pausing = false
	session.stopRequestedAt = time.Time{}
	forceTerminated := session.forceTerminated
	session.forceTerminated = false

	if session.orchestrator == nil {
		return nil
	}
	switch action {
	case liveActionFollowUp:
		return session.orchestrator.TakeLiveUserMessages()
	case liveActionDiscard:
		// A force-terminated run already emitted its terminal event while this
		// goroutine was still stuck, so a live message queued since then
		// arrived AFTER the cancel was reported and belongs to the NEXT task:
		// keep it queued (liveActionNone semantics) instead of discarding it
		// with the dead run — the user's follow-up must not silently vanish.
		if forceTerminated {
			return nil
		}
		session.orchestrator.DiscardLiveUserMessages()
	}
	return nil
}

// loadWorkDirectories fetches project-scoped then session-scoped auxiliary
// work directories (best-effort: nil stores / listing errors logged and
// skipped). Loaded fresh every call so mid-session additions take effect on
// the next task.
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

// implicitTempRoots computes the host OS temp-directory roots always allowed
// for task execution — even with no auxiliary work directories and in CHAT
// mode — because agent-authored shell commands routinely reference the OS
// temp tree. They are security roots only (sdktools.WithAllowedRoots), never
// surfaced in the prompt or UI.
//
// goos/tempDir/systemRootEnv are parameterized for testability. Windows
// candidates: tempDir and %SystemRoot%\Temp; elsewhere "/tmp" and tempDir.
// Candidates are validated with host-independent string analysis (NOT
// filepath.Clean/Join, whose separator semantics depend on the host — the
// branches must behave identically on every CI runner): trailing separators
// trimmed, duplicates dropped, empty/relative/drive-relative skipped. The
// containment API requires absolute roots.
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

// isAbsForGOOS reports whether p is absolute for the given GOOS using pure
// string analysis, so a windows branch evaluated on a POSIX host (and vice
// versa) yields the same verdict as on its native host. Windows absolute:
// volume letter + separator ("C:\Temp") or UNC ("\\server\share");
// drive-relative ("C:foo") is NOT absolute.
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

// trimTrailingSeps removes trailing separators for the given GOOS while
// preserving the minimal absolute form ("/" on POSIX, "C:\" on Windows).
// Host-independent: a trailing backslash is a separator only under the
// windows GOOS — on POSIX it is a legal filename character.
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

// injectWorkDirectories injects the session's auxiliary work directories as
// both allowed roots (security containment) and the prompt-facing list. dirs
// is loaded by the caller (shared with injectIgnoreChecker so
// loadWorkDirectories runs once per task). Allowed roots are ALWAYS set —
// the work dirs plus implicitTempRoots — so even No Project sessions operate
// freely inside the OS temp tree; the prompt-facing WithWorkDirectories
// applies only when directories are configured.
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

// researchProjectInfo reports whether the session's project has RESEARCH
// active and returns the research root: the SESSION's canonical
// <workspace>/.research derived from its own execution workspace (checkout or
// managed worktree), so concurrent sessions of one project never share
// research state. Reads only immutable session fields — callers MUST NOT
// hold the session lock. ("", false) for No Project / no workspace.
func (m *Manager) researchProjectInfo(sess *Session) (string, bool) {
	if sess == nil || sess.ProjectID == project.NoProjectID || sess.WorkspacePath == "" {
		return "", false
	}
	return config.ProjectResearchPath(sess.WorkspacePath), true
}

// injectIgnoreChecker builds a multi-root ignore resolver (workspace + work
// directories) and attaches it via sdktools.WithIgnoreChecker; glob/ripgrep
// consult it to honour each root's own .gitignore/.aiignore (read_file stays
// unrestricted). dirs is the caller-loaded slice (shared with
// injectWorkDirectories — one loadWorkDirectories per task); No Project with
// no dirs returns ctx unchanged. Roots are symlink-resolved to match the form
// glob/ripgrep query paths in; a resolver is built PER ROOT with
// skip-and-log, so one bad root only drops itself, and survivors combine via
// ignore.NewMultiFromResolvers.
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

	// Build a resolver per root from the cache; uncached roots build
	// asynchronously — the resolver walk is expensive and blocking SendMessage
	// on a huge directory (the Go module cache) causes multi-minute hangs. On
	// the first message after startup the checker may be incomplete; later
	// messages pick up the cached resolver once the walk finishes.
	resolvers := make([]*ignore.Resolver, 0, len(roots))
	for _, root := range roots {
		if cached, loaded := m.ignoreCache.Load(root); loaded {
			// The cache stores only *ignore.Resolver (real or building
			// sentinel); assert comma-ok so an unexpected type is skipped,
			// not a panic.
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

// caseInsensitiveProbe carries one case-sensitivity probe result. The first
// caller to miss the cache stores a fresh probe via LoadOrStore, performs the
// filesystem probe, then publishes by closing done; race losers block on
// done and reuse the result — the CaseSense-*.probe file is created exactly
// once per resolved root even under concurrent messages.
type caseInsensitiveProbe struct {
	done chan struct{}
	ci   bool
}

// defaultDetectCaseInsensitive is the production probe wired into
// Manager.detectCaseInsensitiveFn at construction; tests override the
// per-instance field (not this value) to assert call counts.
var defaultDetectCaseInsensitive = pathutil.DetectCaseInsensitive

// caseInsensitiveCachedValue reads a cache entry's resolved bool, blocking
// until its probe completes. ok is false only for a non-*caseInsensitiveProbe
// entry (impossible by construction; satisfies errcheck).
func caseInsensitiveCachedValue(v any) (ci, ok bool) {
	p, okp := v.(*caseInsensitiveProbe)
	if !okp {
		return false, false
	}
	<-p.done
	return p.ci, true
}

// detectCaseInsensitive returns the cached case-sensitivity at path, probing
// exactly once per resolved root: it is a mount-level property that does not
// change mid-session, and the probe (create+delete of a CaseSense-*.probe
// file) must not run on every SendMessage/ResumeSession. LoadOrStore plus the
// done channel guarantee one probe per root under concurrency: the winner
// probes, losers wait for its result. Empty path (No Project) → false, the
// fail-safe case-sensitive default, cache untouched.
func (m *Manager) detectCaseInsensitive(path string) bool {
	if path == "" {
		return false
	}
	// Symlink-resolve so the cache key matches the physical root (consistent
	// with ignoreCache); a resolution failure (workspace not yet created)
	// falls back to the raw path — DetectCaseInsensitive climbs to the
	// nearest existing ancestor, so the probe still answers correctly.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	probe := &caseInsensitiveProbe{done: make(chan struct{})}
	if actual, loaded := m.caseInsensitiveCache.LoadOrStore(resolved, probe); loaded {
		// Another call won the race: reuse its in-flight (or completed)
		// result instead of re-probing.
		existing, ok := actual.(*caseInsensitiveProbe)
		if !ok {
			// Unreachable by construction; defensive for errcheck.
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

// startIgnoreBuild launches a background walk of root and caches its
// ignore.Resolver, deduplicated (a concurrent/completed build for the same
// root makes this a no-op).
//
// The goroutine is manager-tracked (background.go) so Shutdown joins it: a
// walk still running at teardown would otherwise keep touching the workspace
// after its temp dir was removed. It derives from the manager's shutdown
// context: on a huge root (module cache / vendor tree — hundreds of thousands
// of entries) it runs for minutes with no other cancellation, so
// shutdownCancel aborts it BEFORE stopBackground joins, and the join returns
// at the next walk entry instead of burning stopTimeout on the quiescing main
// thread. Read-only walk: a straggler cannot add to a directory being removed.
func (m *Manager) startIgnoreBuild(root string) {
	// Deduplicate via a sentinel "building" marker stored in the cache map.
	// sync.Map.LoadOrStore guarantees exactly one goroutine wins the race.
	sentinel := &ignore.Resolver{}
	if actual, loaded := m.ignoreCache.LoadOrStore(root, sentinel); loaded {
		// Already cached (real resolver or in-flight sentinel) — nothing to do.
		_ = actual
		return
	}

	if !m.spawnBackgroundNamed("ignore-resolver-walk root="+root, func() {
		r, err := m.ignoreResolverBuild(m.shutdownCtx, root)
		if err != nil {
			if errors.Is(err, context.Canceled) && m.shutdownCtx.Err() != nil {
				m.log().Debug("ignore checker: background resolver build aborted by shutdown", "root", root)
			} else {
				m.log().Debug("ignore checker: background resolver build failed", "root", root, "error", err)
			}
			// Remove the sentinel so a future call can retry.
			m.ignoreCache.Delete(root)
			return
		}
		m.ignoreCache.Store(root, r)
	}) {
		// Shutting down: drop the sentinel so the cache is not left holding a
		// permanently "building" root for a manager that will not run again.
		m.ignoreCache.Delete(root)
	}
}

// ignoreFileNames are the files whose change invalidates a cached resolver:
// NewResolver walks the whole root collecting every occurrence, so a change
// to any one — anywhere under a root — makes that root's resolver stale.
var ignoreFileNames = map[string]struct{}{
	".gitignore": {},
	".aiignore":  {},
	".ignore":    {}, // .ignore is honoured by ripgrep and some tools
}

// InvalidateIgnoreCache evicts cached resolvers whose root contains a changed
// ignore-rule file (.gitignore/.aiignore/.ignore) — the invalidation half of
// the async cache; without it ignore edits stay invisible until restart.
//
// It only DELETEs entries, never rebuilds synchronously: the next
// injectIgnoreChecker rebuilds lazily/asynchronously (startIgnoreBuild
// deduplicates via a sentinel), so this cannot regress the multi-minute-build
// case — at worst the first message after invalidation runs without ignore
// filtering, exactly like the first message after startup.
//
// changedPaths are absolute (workspace watcher / fsnotify form); the cache is
// keyed by symlink-resolved roots and pathutil.IsWithinPath resolves both
// sides, so the match is correct behind an OS symlink too.
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

	// For each affected ignore file, evict every cached root containing it —
	// the cache holds only a handful of roots, so a full Range is cheap.
	m.ignoreCache.Range(func(key, _ any) bool {
		root, ok := key.(string)
		if !ok {
			return true
		}
		for ignoreFile := range affected {
			within, err := pathutil.IsWithinPath(root, ignoreFile)
			if err != nil {
				// Resolve failure (vanished root) — evict to be safe; the
				// lazy rebuild re-adds (or skips) it on next use.
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

// Runs in a goroutine; results come via events. reviewMode marks the message
// as carrying code review feedback the agent must address (core
// HandleOptions.ReviewMode); e2s enables the E2S (explicit-state) execution
// mode — the mirror of the goal flag, exclusive with it (the manager rejects
// both set; the frontend store enforces the same rule).
//
// Single-return convenience form of SendMessageClassified for callers not
// needing the authoritative classification; options forwarded unchanged.
func (m *Manager) SendMessage(ctx context.Context, id, text string, activeSkills, activeAgents, activeMCPServers []string, modelOverride, reasoningEffort string, goal bool, goalBudget string, e2s, reviewMode bool, opts ...SendOption) error {
	_, err := m.SendMessageClassified(ctx, id, text, activeSkills, activeAgents, activeMCPServers, modelOverride, reasoningEffort, goal, goalBudget, e2s, reviewMode, opts...)
	return err
}

// SendClassification describes how a user message was dispatched relative to
// the session's running/paused state. It is the authoritative result of the
// session-locked decision in sendMessage, so callers can persist the correct
// is_nudge metadata: a nudge-resume and a live interjection both carry the
// badge, while a fresh task (or a continuation of a completed task) does not.
type SendClassification int

const (
	SendFresh       SendClassification = iota // no task running/paused: started a new task (or re-entered the prior completed one)
	SendNudgeResume                           // resumed a paused task, injected as a trailing user nudge
	SendLive                                  // queued into a running task as a live interjection
)

// SendOption customizes one SendMessage/SendMessageClassified dispatch without
// touching the positional parameter list shared by every caller (internal
// re-entries and the whole test corpus call the positional form).
type SendOption func(*sendOptions)

// sendOptions carries the resolved option values for one dispatch.
type sendOptions struct {
	echoText string
	// clientSendID is the sending surface's optimistic-card id, echoed back so
	// the sender dedups by identity instead of content+time-window: two
	// surfaces sending identical text inside the echo window stay two distinct
	// cards instead of one echo being swallowed (frontend useChatEvents).
	clientSendID string
}

// WithEchoText overrides the user-facing text carried by message_received —
// the UI's delivery notification, deduped by the sending surface against its
// optimistic card by send id (WithClientMessageID) or exact content. text is
// the POST-preprocessing form for the orchestrator, while the persisted row
// and the optimistic card hold the RAW input, so a caller that pre-processes
// must pass the raw input here: whenever preprocessing changed the text (a
// "/skill" mention, an @file ref), a processed echo would fail the dedup and
// render a second ref-less card. Without this option the echo carries text
// (the historical behavior every internal caller and test relies on).
func WithEchoText(raw string) SendOption {
	return func(o *sendOptions) { o.echoText = raw }
}

// WithClientMessageID attaches the sending surface's optimistic-card id to
// the echo, so another surface sending the same text inside the echo window
// renders its own card instead of being dropped as the sender's echo.
// Internal callers without an optimistic card omit it.
func WithClientMessageID(id string) SendOption {
	return func(o *sendOptions) { o.clientSendID = id }
}

// resolveSendOptions applies opts over the defaults and falls an empty echo
// back to text (an unset option and an explicit empty override are the same
// thing: there is no meaningful empty user-facing message).
func resolveSendOptions(text string, opts []SendOption) sendOptions {
	o := sendOptions{echoText: text}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if o.echoText == "" {
		o.echoText = text
	}
	return o
}

// SendMessageClassified runs a user message and returns the authoritative send
// classification (fresh / nudge-resume / live) so the caller can persist the
// correct is_nudge metadata after the decision is made under the session lock.
// The task itself runs in a goroutine and reports via events.
func (m *Manager) SendMessageClassified(ctx context.Context, id, text string, activeSkills, activeAgents, activeMCPServers []string, modelOverride, reasoningEffort string, goal bool, goalBudget string, e2s, reviewMode bool, opts ...SendOption) (SendClassification, error) {
	o := resolveSendOptions(text, opts)
	return m.sendMessage(ctx, id, text, o.echoText, o.clientSendID, activeSkills, activeAgents, activeMCPServers, modelOverride, reasoningEffort, goal, goalBudget, e2s, reviewMode, false)
}

// liveAction tells the request epilogue what to do with live user messages
// that were queued but not delivered before the request finished.
type liveAction int

const (
	liveActionNone     liveAction = iota // keep queued — a later request (resume/follow-up/fresh) delivers them; paused & failed/resumable outcomes
	liveActionFollowUp                   // take the leftovers into a continuation task (already persisted+rendered, no re-presentation); successful completion
	liveActionDiscard                    // drop the queue — a user-stopped run's undelivered message must not leak into a future request
)

// liveSendRejectionLocked returns the error rejecting a live send (message
// while a task runs) for the request, or nil when it may queue. Caller holds
// session.mu; the same conditions are checked by ValidateLiveSend before the
// frontend persists, and here under the lock (the authoritative gate).
func liveSendRejectionLocked(session *Session, goal, e2s bool, text string, activeSkills, activeAgents, activeMCPServers []string) error {
	// A leading "/goal" selects goal mode even without the explicit flag; the
	// fresh path detects it via DetectAndStripGoalMode, so the live gate must
	// reject it identically instead of queueing it as a plain interjection.
	_, isGoalPrefix := core.DetectAndStripGoalMode(text)
	goal = goal || isGoalPrefix
	switch {
	case session.compacting:
		return ErrSessionCompacting
	case session.pausing:
		return ErrPausePending
	case goal:
		return errors.New("goal requests cannot be sent while a task is running — pause or wait for completion first")
	case e2s:
		// E2S mirrors goal: the run owns the whole task lifecycle (Σ seeded
		// only at task start), so it can never join a running task.
		return errors.New("E2S requests cannot be sent while a task is running — pause or wait for completion first")
	case len(activeSkills) > 0 || len(activeAgents) > 0 || len(activeMCPServers) > 0:
		// Rejected for the same reason as skill/agent refs: the task's
		// per-server MCP gating was fixed at task start, so a mid-run mention
		// could never take effect — reject up front, never silently ignore.
		return errors.New("skill, agent, or MCP server references cannot be sent while a task is running — pause or wait for completion first")
	case session.orchestrator == nil:
		return errors.New("session is already processing a task")
	}
	return nil
}

// refreshAutonomyPosture re-pins the session registry's autonomy posture
// (mode + silent-mode sub-policies) from the shared builder registry at a
// task-launch boundary — fresh sends and every resume path. Receiving half of
// the per-task pinning contract (ToolRegistry.RefreshAutonomyPosture /
// applySecurityPolicies): a Settings save updates the shared registry
// immediately but never flips a RUNNING task's posture, so a task runs under
// exactly what was saved before it launched. Best-effort; nil orchestrator/
// registry (tests, CLI) is a no-op.
func (m *Manager) refreshAutonomyPosture(session *Session) {
	session.mu.RLock()
	orch := session.orchestrator
	session.mu.RUnlock()
	if orch == nil {
		return
	}
	if reg := orch.ToolRegistry(); reg != nil {
		reg.RefreshAutonomyPosture()
		// Scope the silent-mode judge memo to this task: the registry clone
		// outlives the task, so a previous task's verdict must not be
		// replayed for an identical effect in this one (ResetJudgeMemo).
		reg.ResetJudgeMemo()
	}
}

// sendMessage is the implementation behind SendMessage. presented marks a
// relaunch of an already-rendered message (the live-send follow-up): it skips
// the message_received emission and title generation — the UI and store
// already hold the message from the original send. text is the
// post-preprocessing form for the orchestrator; echoText the raw user-facing
// form message_received carries (WithEchoText) — they differ whenever
// preprocessing changed the input; clientSendID is the optimistic card's id
// (WithClientMessageID), empty when none was rendered.
func (m *Manager) sendMessage(ctx context.Context, id, text, echoText, clientSendID string, activeSkills, activeAgents, activeMCPServers []string, modelOverride, reasoningEffort string, goal bool, goalBudget string, e2s, reviewMode, presented bool) (SendClassification, error) {
	// No task may be launched once Shutdown has begun (see ResumeTask).
	if m.shuttingDown.Load() {
		return SendFresh, errors.New("session manager is shutting down")
	}
	session, err := m.getOrRestoreSession(id)
	if err != nil {
		return SendFresh, fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return SendFresh, fmt.Errorf("session not found: %s", id)
	}

	session.mu.Lock()
	// Archived sessions are read-only: reject before activating — the
	// server-side choke point covering every caller, not just the UI.
	if session.Archived {
		session.mu.Unlock()
		return SendFresh, ErrSessionArchived
	}
	// Same choke point for a session whose deletion has begun: DeleteSession
	// tears the orchestrator and log handles down after setting the flag, so a
	// message in that window must not launch a task against a session being
	// dismantled.
	if session.deleting {
		session.mu.Unlock()
		return SendFresh, fmt.Errorf("session %s is being deleted", id)
	}
	// Live-send: a message while a task runs is queued into the RUNNING
	// request — the executor drains at its next step boundary and delivers it
	// as a user interjection (the same landing spot as a resume-with-nudge).
	// Queue-while-locked pairs with the request epilogue (which flips
	// active=false and takes leftovers under the same lock), so a message in
	// the closing window of a finishing request is either delivered by it or
	// becomes its follow-up — never lost, never duplicated.
	if session.active {
		if err := liveSendRejectionLocked(session, goal, e2s, text, activeSkills, activeAgents, activeMCPServers); err != nil {
			session.mu.Unlock()
			return SendFresh, err
		}
		session.orchestrator.QueueLiveUserMessage(text)
		session.mu.Unlock()
		// Emit message_received for non-optimistic listeners (the frontend
		// renders its own optimistic copy and ignores this event); IsNudge
		// matches the classification the caller persists (SendLive), so other
		// surfaces show the Nudge badge immediately.
		m.emitFunc(Event{
			SessionID: id,
			Type:      "message_received",
			Data: MessageReceivedData{
				SessionID:    id,
				Text:         echoText,
				ClientSendID: clientSendID,
				IsNudge:      true,
			},
		})
		return SendLive, nil
	}
	// Manual compaction owns the session from the moment it starts (waits for
	// a running task to pause, then swaps the history): a fresh task in that
	// window would race the swap. The nudge-resume path is covered by
	// ResumeTask's own compacting guard.
	if session.compacting {
		session.mu.Unlock()
		return SendFresh, ErrSessionCompacting
	}
	session.mu.Unlock()

	// Nudge-resume: a message into a PAUSED session does not start a new task
	// — it resumes the paused one via ResumeSession (which re-activates and
	// injects the text as a trailing user turn). Goal/E2S requests are
	// excluded: they supersede any paused task (abandonUnfinishedTaskForGoal /
	// ...ForE2S handle cleanup). Detected before activation so ResumeTask's
	// own activation path is not tripped by a spurious "already processing".
	if !goal && !e2s && m.hasPausedUnfinishedTask(id) {
		resumeErr := m.ResumeSession(ctx, id, modelOverride, reasoningEffort, text)
		if resumeErr == nil {
			// Emit message_received only AFTER the resume commits (CS #66):
			// a failed resume makes the caller skip SaveMessage, and a
			// pre-dispatch echo would leave a card no persisted row backs —
			// vanishing on reload. IsNudge matches SendNudgeResume so other
			// surfaces show the Nudge badge immediately.
			m.emitFunc(Event{
				SessionID: id,
				Type:      "message_received",
				Data: MessageReceivedData{
					SessionID:    id,
					Text:         echoText,
					ClientSendID: clientSendID,
					IsNudge:      true,
				},
			})
		}
		return SendNudgeResume, resumeErr
	}

	// Determine RESEARCH mode from the session's own research root (the
	// session's execution workspace — derived before the session lock; the
	// fields read are immutable).
	researchRoot, isResearch := m.researchProjectInfo(session)

	session.mu.Lock()

	// Re-check the compacting window under the SAME hold as activation: the
	// guard above ran before the DB reads (nudge-resume lookup, research
	// info), and CompactSessionContext can have armed in that gap — it would
	// see active=false and swap the history while this task runs. ResumeTask
	// checks-and-activates in one hold for the same reason.
	if session.compacting {
		session.mu.Unlock()
		return SendFresh, ErrSessionCompacting
	}

	// Set active and create cancellable context with session ID. Launching a
	// task consumes any recorded pause owner: the run supersedes a previous
	// pause request (this is the resume/fresh-send intent).
	session.active = true
	session.pauseOwner = pauseOwnerNone
	session.stopRequestedAt = time.Time{}
	session.terminalEmitted = false
	session.forceTerminated = false
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

	// Activation committed: announce the task start. This single point covers
	// the fresh, goal, E2S, and continue-interrupted dispatches below; the
	// desktop layer mirrors it into the global sessions:changed push so every
	// surface shows the session running immediately. The resume path
	// announces itself via task_resumed + session_resumed — do NOT emit
	// task_started there too, or one resume pushes the list-change twice.
	m.emitFunc(Event{SessionID: id, Type: EventTaskStarted})

	// Task-launch boundary: pin the autonomy posture this task runs under
	// from the current Settings (all dispatches below run in this goroutine).
	m.refreshAutonomyPosture(session)

	// Snapshot envInfo under read lock
	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo != nil {
		taskCtx = sdktools.WithEnvInfo(taskCtx, envInfo)
	}

	// Inject auxiliary work directories (allowed roots + prompt list) and the
	// multi-root ignore checker. dirs is loaded once and shared so the
	// DB-hitting loadWorkDirectories runs a single time per task; allowed
	// roots ALWAYS include the implicit host temp roots (implicitTempRoots),
	// also for No Project; the prompt list and ignore checker stay no-ops
	// when no directories are configured.
	dirs := m.loadWorkDirectories(session)
	taskCtx = m.injectWorkDirectories(taskCtx, dirs)
	taskCtx = m.injectIgnoreChecker(taskCtx, session, dirs)

	// Emit message_received, skipped for a presented relaunch (already
	// emitted when queued into the running task; the frontend renders its own
	// optimistic copy). IsNudge stays absent: a fresh launch persists no
	// is_nudge stamp, and the event flag must agree with the DB row.
	if !presented {
		m.emitFunc(Event{
			SessionID: id,
			Type:      "message_received",
			Data: MessageReceivedData{
				SessionID:    id,
				Text:         echoText,
				ClientSendID: clientSendID,
			},
		})
	}

	// Auto-name the session from its first message (background).
	m.maybeSpawnTitleGeneration(session, id, text, presented, activeSkills)

	// Launch goroutine to handle the message
	go func(ctx context.Context, msg string, skills []string, agents []string, mcpServers []string) {
		// action tells the epilogue what to do with queued-but-undelivered
		// live messages; liveActionNone (zero) keeps them queued — the default
		// for paths that delegated elsewhere (the resumed run drains the queue
		// itself) or left the task resumable (paused/failed: the next resume
		// delivers them).
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

		// Snapshot pending attachments and clear them so they flush exactly
		// once into the blackboard for this SendMessage — whether the message
		// continues an interrupted task or starts fresh. Before the continue
		// check so the resume path receives them too (it bypasses
		// HandleMessage, which would otherwise flush via setupBlackboard).
		session.mu.Lock()
		pendingAttachments := session.pendingAttachments
		session.pendingAttachments = nil
		pendingImages := session.pendingImageAttachments
		session.pendingImageAttachments = nil
		session.mu.Unlock()

		// Clear the pending-attachment chips now that they are flushed; emit
		// only when there were any (no spurious event on attachment-free sends).
		if len(pendingAttachments) > 0 || len(pendingImages) > 0 {
			m.emitAttachmentsChanged(id, []AttachmentInfo{}, nil)
		}

		// Convert staged images into LLM image content blocks: base64 data is
		// held in memory only until this snapshot; the on-disk copy at
		// FilePath persists for restart reconstruction (ChatMessage.Metadata).
		imageBlocks := imageAttachmentsToContentBlocks(pendingImages)

		// Detect goal mode (leading "/goal" OR the explicit flag) BEFORE the
		// resume check: a goal request starts a fresh pursuit rather than
		// continuing an interrupted task, so the resume path is skipped and
		// any unfinished task is abandoned (abandonUnfinishedTaskForGoal).
		// The two signals are OR-ed.
		goalMsg, isGoal := core.DetectAndStripGoalMode(msg)
		goalEnabled := isGoal || goal

		switch {
		case goalEnabled:
			// Goal mode supersedes any interrupted task: cancel it so it does
			// not linger as resumable WIP across the new goal task, then fall
			// through to the goal dispatch. Best-effort no-op without a store
			// or unfinished task.
			m.abandonUnfinishedTaskForGoal(id)
		case e2s:
			// E2S mirrors goal: Σ is seeded only at task start, so it can
			// never continue an interrupted plain task — abandoned, fresh E2S
			// task starts below.
			m.abandonUnfinishedTaskForE2S(id)
		default:
			// Continue an interrupted (unfinished) task if one exists: the new
			// message becomes a final user-nudge turn on the prior trajectory
			// and the ReAct cycle resumes — no routing, no new task, no
			// history pair. The function owns terminal handling (deactivate +
			// emit + follow-up) for every outcome, so mark finished to skip
			// the deferred epilogue's second deactivation.
			if m.tryContinueInterruptedTask(ctx, id, session, msg, modelOverride, reasoningEffort, pendingAttachments) {
				finished = true
				return
			}
		}

		// No unfinished task took the resume path (or goal was requested): run
		// the normal route → plan → execute flow, or dispatch to the goal loop.
		// Get last completed task ID for continuation.
		session.mu.Lock()
		lastTaskID := session.lastCompletedTaskID
		session.mu.Unlock()

		// Snapshot the anchor's unfinished state BEFORE HandleMessage can
		// reactivate it. lastCompletedTaskID is restored without a status
		// check, so after a restart it may point at a failed task whose resume
		// path fell back here on a restore error — such an anchor was
		// unfinished BEFORE the send. The snapshot lets
		// shouldRetryContinuationFresh tell that case apart from an anchor
		// reactivated by this send's own execution; the short-circuit keeps
		// fresh sends (the common case) off the lookup.
		anchorWasUnfinished := lastTaskID != "" && m.unfinishedTaskID(id) == lastTaskID

		// On a continuation (lastTaskID != ""), goal mode runs ON the restored
		// blackboard of the prior completed task: inherited facts and history,
		// a fresh goal derived from the new message. When the goal request
		// supplanted an interrupted task, that task was cancelled above and
		// lastTaskID refers to the last COMPLETED task (or "" for fresh).

		// Parse the optional budget override (JSON or empty → nil unlimited);
		// a parse error is logged, never blocks — the goal runs unlimited.
		var budgetOverride *goalpkg.GoalBudget
		if goalEnabled {
			budgetOverride = parseGoalBudget(goalBudget, m.log())
		}

		// The orchestrator receives the /goal-stripped message when goal mode
		// is on, else the original (preprocessed) message; goalMsg == msg
		// without the prefix, so this is safe even for flag-enabled goal mode.
		hmMsg := msg
		if goalEnabled {
			hmMsg = goalMsg
		}

		result, err := session.orchestrator.HandleMessage(ctx, hmMsg, id, core.HandleOptions{
			TaskID:             lastTaskID,
			UserSkills:         skills,
			UserAgents:         agents,
			UserMCPServers:     mcpServers,
			ModelOverride:      modelOverride,
			ReasoningEffort:    reasoningEffort,
			SessionPlansDir:    config.SessionPlansDir(m.agentDir, session.ProjectID, id),
			PendingAttachments: pendingAttachments,
			PendingImages:      imageBlocks,
			Goal:               goalEnabled,
			GoalBudgetOverride: budgetOverride,
			E2S:                e2s,
			ReviewMode:         reviewMode,
		})

		// Distinguish partial-success (incomplete plan) from total failure.
		incomplete := err != nil && errors.Is(err, orchestration.ErrExecutionIncomplete) && result != nil
		if incomplete {
			m.log().Warn("task completed with incomplete execution", "session_id", id, "task_id", lastTaskID, "error", err)
			err = nil
		}

		// Fallback: a continuation that failed BEFORE executing (blackboard
		// restore or routing error) with a TaskID retries fresh.
		// shouldRetryContinuationFresh returns false only when this send's
		// continuation actually started executing — the anchor was terminal
		// before the send and its row was reactivated to in_progress, so a
		// fresh retry would orphan it. An anchor ALREADY unfinished when the
		// send began still falls back to fresh: the resume path cannot restore
		// it (same error every time), so the banner would dead-end, and the
		// stale-task sweep cancels the row once the fresh run succeeds.
		// Goal mode is preserved: a failed goal-on-continuation retries as a
		// fresh goal task rather than dropping the flag (or, for a "/goal"
		// prefix, leaking it into the fresh task — hmMsg is already stripped,
		// msg is not).
		if err != nil && !errors.Is(err, core.ErrMCPAuthorizationState) && m.shouldRetryContinuationFresh(id, lastTaskID, anchorWasUnfinished) {
			m.log().Warn("continuation failed, falling back to fresh workflow", "session_id", id, "task_id", lastTaskID, "error", err)
			session.mu.Lock()
			session.lastCompletedTaskID = ""
			session.mu.Unlock()
			result, err = session.orchestrator.HandleMessage(ctx, hmMsg, id, core.HandleOptions{
				TaskID:             "",
				UserSkills:         skills,
				UserAgents:         agents,
				UserMCPServers:     mcpServers,
				ModelOverride:      modelOverride,
				ReasoningEffort:    reasoningEffort,
				SessionPlansDir:    config.SessionPlansDir(m.agentDir, session.ProjectID, id),
				PendingAttachments: pendingAttachments,
				PendingImages:      imageBlocks,
				Goal:               goalEnabled,
				GoalBudgetOverride: budgetOverride,
				E2S:                e2s,
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
				// On shutdown leave the task in_progress so it can be resumed
				// after restart; only user-initiated cancels persist as
				// cancelled. Goal and E2S runs are abandoned only on a user
				// cancel, not on shutdown.
				if m.emitTaskCancelledUnlessShuttingDown(id) {
					action = liveActionDiscard
					m.abandonGoalIfUnfinished(id)
					m.abandonE2SIfUnfinished(id)
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
			m.emitResumableIfUnfinished(id, resumableReasonFromError(err), err)
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
				m.abandonE2SIfUnfinished(id)
				m.persistCancellationIfUnfinished(id)
			}
			return
		}

		// A cooperative pause is a clean checkpoint, not a degraded completion:
		// emit session_paused so the UI shows a paused state (unlocked input,
		// Resume/Stop) instead of a misleading failed/resumable banner; the
		// task persists as paused (resumable).
		if result != nil && result.Status == orchestration.ExecutionStatusPaused {
			// Deactivate BEFORE the terminal event: a consumer reacting to
			// session_paused treats the session as free (see
			// deactivateSessionTask).
			leftover := m.deactivateSessionTask(session, action)
			finished = true
			m.emitFunc(Event{SessionID: id, Type: "session_paused"})
			m.finishLiveLeftover(ctx, id, session, leftover)
			return
		}

		// Emit done (typed success contract; degraded outcomes surface a
		// resumable action or a fallback warning).
		action = liveActionFollowUp
		// Deactivate BEFORE task_complete: the event is the frontend's signal
		// that the session is free for Resume/Send.
		leftover := m.deactivateSessionTask(session, action)
		finished = true
		m.emitTaskComplete(id, result, nil)
		m.finishLiveLeftover(ctx, id, session, leftover)
	}(taskCtx, text, activeSkills, activeAgents, activeMCPServers)

	return SendFresh, nil
}

// parseGoalBudget parses a goal-budget override (JSON with the GoalBudget
// field, max_turns). Empty → nil (unlimited); unparseable is logged and nil —
// a malformed budget never blocks the send.
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

// tryContinueInterruptedTask continues an unfinished (interrupted) task by
// appending the user's new message as a final seeded user-nudge turn. Returns
// true when it took the resume path (caller must NOT run the normal
// HandleMessage flow), false when there is no unfinished task.
//
// The message renders as a {role:user} turn (Step.UserNudge /
// stepsToMessages) right after the prior trajectory. The task is NOT
// re-routed, no new task or history pair is created — the lifecycle simply
// continues (Resume's recordResumeOutcome records the assistant side only,
// mirroring the app-restart ResumeTask path).
//
// This path bypasses HandleMessage, so the per-request model/reasoning
// overrides are applied here (ApplyRequestOverrides) and pending attachments
// are flushed into the restored blackboard (mirroring setupBlackboard's
// continuation path); both no-ops when empty.
//
// message_received was already emitted by SendMessage; the resumed task
// emits its own lifecycle events on top.
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
	bb, err := m.restoreBlackboardTracked(taskID, id, adapter, nil)
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
	// Capture the pre-nudge trajectory length: empty means the original run
	// never executed a step (see the re-route arming below). The nudge step
	// appended next would otherwise mask that.
	hadTrajectory := len(resumeSteps) > 0
	resumeSteps = append(resumeSteps, agent.Step{UserNudge: message})

	// Resolve the persisted routing decision (optional). A task that was
	// actually routed keeps its decision; a task that never got past routing is
	// re-classified on this resume (see the arming below).
	var routing *router.RoutingDecision
	if state, stateErr := adapter.LoadTaskState(taskID); stateErr != nil {
		m.log().Warn("continue-interrupted-task: failed to load task state; resuming without routing", "session", id, "error", stateErr)
	} else if state != nil {
		routing = state.RoutingDecision
	}

	// A task whose original run never got past routing (no persisted routing
	// decision AND no execution state) is re-classified on this nudge-resume,
	// mirroring Manager.ResumeTask: without it the resumed run would default
	// to the "general" domain and skip routing. A routed task keeps its
	// persisted decision (a nudge-resume never re-classifies a continuation).
	if routing == nil && !hadTrajectory && bb.GetPlan() == nil {
		session.orchestrator.RequestResumeReroute()
	}

	// Load the persisted goal state. A NON-terminal goal (halted as a
	// resumable turn-error failure, or still active) must NOT continue as a
	// plain task: it re-enters the goal loop so the pursuit is preserved
	// (Issue #95 AC2). A nil state (ordinary paused/failed task) continues
	// plain. A TERMINAL goal (exhausted) is over by design — resumeGoalLoop
	// must not re-enter it (ResumeTask surfaces an explicit notice there) —
	// so it is dropped here too.
	goalState, gerr := adapter.LoadGoalState(taskID)
	if gerr != nil {
		m.log().Warn("continue-interrupted-task: failed to load goal state; continuing as a plain task", "session", id, "error", gerr)
		goalState = nil
	}
	// Model Profiles gate (mirrors Manager.ResumeTask): goal mode and the
	// essential-tools narrowing are mutually exclusive
	// (core.ErrGoalBlockedByModelProfiles) — refuse the goal continuation
	// under narrowing with a visible error instead of silently degrading to a
	// plain run. Checked BEFORE the run starts so the paused row is left
	// untouched and a later message re-enters this path.
	if goalState != nil && !goalState.Status.IsTerminal() &&
		session.orchestrator != nil && session.orchestrator.ModelProfilesNarrowingEnabled() {
		m.emitFunc(Event{
			SessionID: id,
			Type:      "error",
			Data: ErrorData{
				SessionID: id,
				Error:     core.ErrGoalBlockedByModelProfiles.Error(),
			},
		})
		m.deactivateSessionTask(session, liveActionNone)
		return true
	}

	// Resolve the prior task_failed_resumable banner so it does not linger
	// after the resumed execution finishes.
	m.resolveResumableTaskMessage(id, taskID, "resumed")

	result, err := session.orchestrator.Resume(ctx, bb, routing, config.SessionPlansDir(m.agentDir, session.ProjectID, id), resumeSteps, goalState, "")

	// Shared completion handling (mirrors ResumeTask's goroutine tail).
	if err != nil && errors.Is(err, orchestration.ErrExecutionIncomplete) && result != nil {
		m.log().Warn("continued task completed with incomplete execution", "session_id", id, "error", err)
		err = nil
	}

	if err != nil {
		if ctx.Err() == context.Canceled {
			if m.emitTaskCancelledUnlessShuttingDown(id) {
				m.abandonGoalIfUnfinished(id)
				m.abandonE2SIfUnfinished(id)
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
		m.emitResumableIfUnfinished(id, resumableReasonFromError(err), err)
		m.deactivateSessionTask(session, liveActionNone)
		return true
	}

	// A cooperative pause during resume is a clean checkpoint (see the
	// SendMessage and ResumeTask goroutines): session_paused instead of a
	// degraded task_complete. Checked before the task-ID store so a paused
	// result does not update lastCompletedTaskID (not completed). Deactivate
	// BEFORE the terminal event (see deactivateSessionTask).
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

	// Deactivate BEFORE task_complete (frontend free-for-Resume/Send signal).
	// The queued-live-messages follow-up is drained and launched here; the
	// caller marks the run finished so its deferred epilogue does not
	// deactivate a second time and clobber the just-launched follow-up.
	leftover := m.deactivateSessionTask(session, liveActionFollowUp)
	m.emitTaskComplete(id, result, nil)
	m.finishLiveLeftover(ctx, id, session, leftover)
	return true
}

// ResumeTask resumes an unfinished task: nil when none exists or the task
// store is not configured. Invoked by the manual Resume button (with the
// user's current model/reasoning selection) and on app restart for
// interrupted tasks. The optional modelOverride/reasoningEffort are applied
// like a fresh SendMessage so a pre-resume switch is honored instead of
// silently inherited; the optional nudge is injected as a trailing user
// message into the first resumed turn (one-shot) — the nudge-resume path.
func (m *Manager) ResumeTask(ctx context.Context, id, modelOverride, reasoningEffort, nudge string) error {
	// No task may launch once Shutdown has begun: teardown drains and joins
	// all task goroutines, so a resume racing it would spawn a goroutine
	// nobody waits for (LLM calls and tools against a closing backend).
	if m.shuttingDown.Load() {
		return errors.New("session manager is shutting down")
	}
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

	// Archived sessions are read-only: fail fast before touching the task
	// store or restoring a blackboard (mirrors the SendMessage guard). The
	// deleting check is the same class: the compaction flow's auto-resume
	// re-enters here after DeleteSession set the flag but before it removed
	// the session — without it the resume would launch a ghost task against
	// a session being torn down.
	session.mu.Lock()
	archived := session.Archived
	deleting := session.deleting
	session.mu.Unlock()
	if archived {
		return ErrSessionArchived
	}
	if deleting {
		return fmt.Errorf("session %s is being deleted", id)
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
	bb, err := m.restoreBlackboardTracked(taskID, id, adapter, nil)
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

	// A goal that ended TERMINALLY (turn budget exhausted) cannot be
	// re-entered — goal mode is over by design. Resuming such a task therefore
	// continues it as a REGULAR task; tell the user plainly (Issue #95 AC3)
	// instead of silently dropping the goal pursuit. goalModeOver drives a
	// visible notice at the committed point below; the terminal goal state is
	// dropped so Resume takes the plain Conductor path.
	goalModeOver := false
	if goalState != nil && goalState.Status.IsTerminal() {
		goalModeOver = true
		goalState = nil
	}

	// Determine RESEARCH mode from the session's own research root (the
	// session's execution workspace — derived before the session lock; the
	// fields read are immutable).
	researchRoot, isResearch := m.researchProjectInfo(session)

	session.mu.Lock()
	if session.active {
		session.mu.Unlock()
		return errors.New("session is already processing a task")
	}
	// Re-check deleting INSIDE the activation critical section: the snapshot
	// guard above ran before the task-store reads and blackboard restore, and
	// DeleteSession's whole teardown can serialize into that window — without
	// this re-check the resume would activate a task against the session being
	// dismantled (the ghost-task class the flag exists to kill).
	if session.deleting {
		session.mu.Unlock()
		return fmt.Errorf("session %s is being deleted", id)
	}
	// Manual compaction owns the session until it finishes (it swaps the
	// history while idle); resuming in that window would race the swap. The
	// compaction flow's own auto-resume runs after the flag clears, so it is
	// not blocked here.
	if session.compacting {
		session.mu.Unlock()
		return ErrSessionCompacting
	}
	// Model Profiles goal guard: a paused non-terminal goal must not resume
	// while the essential-tools narrowing is active (mutually exclusive,
	// core.ErrGoalBlockedByModelProfiles). Checked here, under the session
	// lock and BEFORE activation, so EVERY resume entry point funneling
	// through ResumeTask (ResumeTask/ResumeSession RPCs, the nudge-resume,
	// the compaction auto-resume) is refused without side effects: the row
	// stays "paused", so a later message re-enters the nudge-resume path
	// instead of degrading to a plain continuation. The orchestrator's own
	// guard (Resume / resumeGoalLoop) is the ultimate authority; the frontend
	// API pre-check gives the friendly message.
	if goalState != nil && !goalState.Status.IsTerminal() &&
		session.orchestrator != nil && session.orchestrator.ModelProfilesNarrowingEnabled() {
		session.mu.Unlock()
		return core.ErrGoalBlockedByModelProfiles
	}
	// Launching the resumed task consumes any recorded pause owner (the
	// resume supersedes a previous pause request — including the user pause
	// the compaction flow just honoured by NOT auto-resuming).
	session.active = true
	session.pauseOwner = pauseOwnerNone
	session.stopRequestedAt = time.Time{}
	session.terminalEmitted = false
	session.forceTerminated = false
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

	// Apply per-request model/reasoning overrides — this path bypasses
	// HandleMessage (step 0 would apply them), so the resumed task picks up
	// the user's selection instead of inheriting the interrupted task's;
	// no-op when both empty. After the active-check (never leak into a
	// concurrently running task) but before the Resume goroutine, so the
	// emitter's cached model is synchronized before the first context_fill.
	session.orchestrator.ApplyRequestOverrides(ctx, modelOverride, reasoningEffort)

	// Task-launch boundary (resume): pin the autonomy posture from current
	// Settings — pause→edit→resume behaves exactly like launching a new task
	// with those Settings; the interrupted task's own posture is discarded.
	m.refreshAutonomyPosture(session)

	// Snapshot envInfo under read lock
	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo != nil {
		taskCtx = sdktools.WithEnvInfo(taskCtx, envInfo)
	}

	// Inject auxiliary work directories + ignore checker (same contract as
	// the sendMessage path: one DB-hitting load per task; implicit temp roots
	// always in the allowed roots; prompt list/ignore checker no-ops without
	// configured directories).
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
	// session_paused): input re-locks, Pause/Stop controls show again.
	m.emitFunc(Event{SessionID: id, Type: "session_resumed"})

	// Goal-mode-over notice (Issue #95 AC3): the resumed task's goal ended
	// terminally (budget exhausted) and will NOT be re-armed — the user must
	// know the continuation is a regular run.
	if goalModeOver {
		m.emitFunc(Event{
			SessionID: id,
			Type:      "service",
			Data: map[string]any{
				"content": "The goal for this task is over — its turn budget was exhausted, so goal mode will not be resumed. The task continues as a regular run. Send a new /goal message to pursue a new goal.",
				"phase":   "orchestration",
			},
		})
	}

	// Mark the prior task_failed_resumable banner resolved so it does not
	// reappear as pending after the resume finishes — done here at the
	// committed point so a failed restore still leaves the banner actionable.
	m.resolveResumableTaskMessage(id, taskID, "resumed")

	// Flip the persisted task row back to in_progress for the resumed run. The
	// row was written 'paused' by the prior run's checkpoint
	// (PersistentBlackboard.PauseTask) and would otherwise stay 'paused' all
	// run — the paused-ghost bug: the store claims a paused task while it is
	// actively running. A cooperative pause mid-run rewrites 'paused' at the
	// next checkpoint (persistTaskOutcome), preserving the pause contract.
	// Best-effort like reactivateContinuationTask: a failed UPDATE is logged,
	// never fatal.
	if err := adapter.ReactivateTask(taskID); err != nil {
		m.log().Warn("failed to reactivate task row on resume", "session_id", id, "task_id", taskID, "error", err)
	}

	// A task whose original run never got past routing (no persisted routing
	// decision AND no execution state — empty trajectory, no plan) has nothing
	// to continue: typically a fresh send that failed while the router was
	// calling the LLM, leaving a resumable "failed" row with no routing
	// decision. Resuming without re-routing would silently default to the
	// "general" domain and skip routing. Arm the one-shot re-route so the
	// resumed run classifies the original request first; a task that WAS
	// routed or has any execution state keeps reuse-without-routing — a resume
	// never re-classifies a continuation.
	if routing == nil && len(resumeSteps) == 0 && bb.GetPlan() == nil {
		session.orchestrator.RequestResumeReroute()
	}

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
				// the task as cancelled. The goal and the E2S run are
				// abandoned (cancelled) only on a user cancel, not on
				// shutdown.
				if m.emitTaskCancelledUnlessShuttingDown(id) {
					action = liveActionDiscard
					m.abandonGoalIfUnfinished(id)
					m.abandonE2SIfUnfinished(id)
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
			m.emitResumableIfUnfinished(id, resumableReasonFromError(err), err)
			return
		}

		// Safety net: context cancelled but Resume returned no error (the
		// goal-loop path returns nil on cancel — resumeGoalLoop always does) —
		// still treat as cancellation; mirrors HandleMessage's safety net.
		if taskCtx.Err() == context.Canceled {
			if m.emitTaskCancelledUnlessShuttingDown(id) {
				action = liveActionDiscard
				m.abandonGoalIfUnfinished(id)
				m.abandonE2SIfUnfinished(id)
				bb.CancelTask()
			}
			return
		}

		// A cooperative pause during resume is a clean checkpoint (see the
		// HandleMessage goroutine): session_paused instead of a degraded
		// task_complete; the task stays resumable. Checked before the task-ID
		// store so a paused result does not update lastCompletedTaskID.
		if result != nil && result.Status == orchestration.ExecutionStatusPaused {
			// Deactivate BEFORE the terminal event (deactivateSessionTask).
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
		// Close the processing window BEFORE task_complete: the event is the
		// frontend's free-for-Resume/Send signal.
		leftover := m.deactivateSessionTask(session, action)
		finished = true
		m.emitTaskComplete(id, result, nil)
		m.finishLiveLeftover(ctx, id, session, leftover)
	}()

	return nil
}

// CancelUnfinishedTask discards any unfinished task by marking it cancelled
// in the task store; afterwards the session has no resumable task and
// emitResumableIfUnfinished will not emit for it. Any deferred
// resume-compaction armed for the task is discarded too — it must not outlive
// the task it was armed for. Nil when no store or no unfinished task.
func (m *Manager) CancelUnfinishedTask(sessionID string) error {
	m.clearResumeRequests(sessionID)
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

// PauseSession signals the running task (any mode) to pause at the next step
// boundary, delegating to the orchestrator's universal PauseSession: the
// executor checks the pause signal at every step boundary (normal path and
// goal-loop turns alike), stops with a paused checkpoint, and the request
// persists the task as paused + exits so ResumeSession/ResumeTask can
// re-enter. No-op with no request in flight or no orchestrator yet.
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
	// Flag the pausing window under the session lock: until the running
	// request's epilogue flips it off, live sends are rejected with
	// ErrPausePending (the UI mirrors this by locking the input). Set under
	// the lock BEFORE the pause signal flips (after unlock), so a send racing
	// the flip still observes pausing=true — the window stays closed for the
	// whole pause-in-flight period.
	//
	// The pause owner is recorded on EVERY call, including on an idle session:
	// inside the manual-compaction window (checkpoint landed, task paused) a
	// pause click means "keep it paused" — the compaction flow's auto-resume
	// checks the owner and must not override it.
	session.mu.Lock()
	session.pauseOwner = pauseOwnerUser
	if session.active {
		session.pausing = true
		// Deliberately do NOT record stopRequestedAt: a pause is a COOPERATIVE
		// signal answered at the next step boundary, and a long LLM call
		// routinely exceeds hungSessionThreshold — the executor checks the
		// pause checker only after the in-flight model request returns, and
		// the tool watchdog polls pause only while a TOOL is in flight.
		// Flagging a healthy session as "not responding to a stop request"
		// during that window is the false positive this avoids. Only a Stop
		// (CancelTask) records stopRequestedAt: it cancels the context, so an
		// unanswered Stop genuinely means a wedged goroutine.
	}
	session.mu.Unlock()
	orch.PauseSession()
	return nil
}

// ResumeSession re-enters the execution loop for a paused (or still-active,
// non-terminal) task via ResumeTask, which loads the unfinished task +
// persisted state and dispatches to the orchestrator's resume path. The
// optional nudge is injected as a one-shot trailing user message (the
// nudge-resume path); modelOverride/reasoningEffort are forwarded so a
// pre-resume switch is honored. Nil when there is nothing resumable.
func (m *Manager) ResumeSession(ctx context.Context, sessionID, modelOverride, reasoningEffort, nudge string) error {
	return m.ResumeTask(ctx, sessionID, modelOverride, reasoningEffort, nudge)
}

// hasPausedUnfinishedTask reports whether the resumable unfinished task is
// specifically paused — the SendMessage nudge-resume router's discriminator
// between resuming and starting fresh. False with no store or on lookup
// error (best-effort: falls through to the normal send path).
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
	// UnfinishedTaskStatus is the RAW persisted status of the resumable task
	// ("in_progress", "paused", "failed"), or "" when none — the frontend's
	// reconcile seeds its live overlay with the exact value instead of
	// collapsing every non-paused unfinished task to "failed" (an orphaned
	// in_progress would colour red on a visited session but green on an
	// unvisited one). Verbatim, no clamping; consult only while Active is
	// false, and apply your own policy for a status outside the unfinished
	// set (possible only if the task settles between the ID lookup and the
	// state load).
	UnfinishedTaskStatus string `json:"unfinished_task_status,omitempty"`
	// Paused is true when the resumable unfinished task is paused — a
	// cooperative checkpoint the user can resume (optionally with a nudge) or
	// send into (treated as a nudge-resume).
	Paused bool `json:"paused"`
	// Compacting is true while a manual context compaction is in flight; the
	// UI locks the input and swaps compact for cancel.
	Compacting bool `json:"compacting"`
	// CompactionAvailability is the per-strategy manual-compaction prediction
	// for the session's current history (Orchestrator.ManualCompactionAvailability):
	// whether each strategy would shrink the dialogue now, how many tokens it
	// would reclaim, and whether that is exact or a forecast. Fail-open:
	// empty whenever the session/orchestrator is not in memory — a status
	// poll must never guess "disabled"; a pointless click just reports the
	// existing nothing_compacted outcome. omitempty keeps the nil slice OFF
	// the wire (no null — indistinguishable from "no prediction" anyway).
	CompactionAvailability []core.CompactionAvailability `json:"compaction_availability,omitempty"`
	// Activity is the session's last user-facing activity label
	// ("Thinking...", "Routing request...", ...) tracked by the emitter, so
	// the frontend can replace the frozen activityStatus left over from
	// before a session/project switch. Authoritative only while Active is
	// true: terminal events clear the activity on the frontend.
	Activity string `json:"activity,omitempty"`
	// Streaming is true while an assistant stream is open (assistant_chunk
	// without the closing assistant_done); when false the frontend clears
	// stale streaming text — a stream that ended in the background would
	// otherwise render a frozen partial answer forever (the full answer
	// arrives via history reload).
	Streaming bool `json:"streaming"`
	// WorkUnits is the durable work-unit snapshot for the resumable task
	// (Manager.workUnitSnapshot): the frontend reconciles chat blocks against
	// it after a restart/session load, so a paused/interrupted delegate or
	// plan step renders as such instead of a misleading "running" (the
	// replayed history has no terminal event for it). Empty with no resumable
	// task or no durable unit storage.
	WorkUnits []WorkUnitStatus `json:"work_units,omitempty"`
}

// WorkUnitStatus is one durable execution unit as reported to the frontend for
// chat-block reconciliation after a restart or session load. StepID is the
// same id the subagent_launch/plan_step_start chat events carry, so the
// frontend maps it straight onto the block it names and aligns the rendered
// status.
type WorkUnitStatus struct {
	// StepID is the unit's id — for a delegation or plan step, the step id.
	StepID string `json:"step_id"`
	// Kind classifies the unit ("subagent", "plan_step", "goal_verification",
	// "task", "tool"); advisory metadata for the reader.
	Kind string `json:"kind,omitempty"`
	// Status is the effective lifecycle state ("pending", "running", "paused",
	// "completed", "failed", "interrupted").
	Status string `json:"status"`
	// ParentID is the unit's parent (empty for a top-level unit).
	ParentID string `json:"parent_id,omitempty"`
}

// GetSessionRuntimeStatus reports the session's live (in-memory) and
// persisted execution state so the frontend can reconstruct
// "running / resumable / paused" after a restart or session switch. It never
// RESTORES a session and never mutates session state; it is not entirely
// side-effect-free: it settles the task's abandoned in-flight units as
// `interrupted` (workUnitSnapshot) — a column-scoped, conditional, idempotent
// ledger write confined to a task neither executing nor paused.
func (m *Manager) GetSessionRuntimeStatus(sessionID string) (SessionRuntimeStatus, error) {
	var status SessionRuntimeStatus

	// Memory-only lookup: a session not in memory cannot be active, and
	// restoring it here would be an unwanted side effect for a status poll.
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess != nil {
		status.Active = sess.IsActive()
		status.Compacting = sess.IsCompacting()
		// Live activity/streaming from the emitter — the same signals the
		// (possibly unmounted) frontend listeners would have received as
		// events. Reading the emitter pointer under sess.mu keeps it stable
		// (the activity/token states have their own locks); the orchestrator
		// pointer is read under the same lock, its no-op predicate outside.
		sess.mu.Lock()
		emitter := sess.emitter
		orch := sess.orchestrator
		sess.mu.Unlock()
		if emitter != nil {
			status.Activity = emitter.LastActivity()
			status.Streaming = emitter.StreamingActive()
		}
		if orch != nil {
			status.CompactionAvailability = orch.ManualCompactionAvailability()
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
			// plain in-progress/failed task; a missing state is non-paused.
			if state, stateErr := adapter.LoadTaskState(taskID); stateErr == nil && state != nil {
				// Raw persisted status so the frontend seeds its live overlay
				// with the EXACT value (an orphaned 'in_progress' stays green,
				// matching the DB fallback) instead of collapsing to 'failed'.
				status.UnfinishedTaskStatus = state.Status
				// Invariant: {Active:true, Paused:true} must not exist —
				// pausing deactivates the session until session_paused lands,
				// so a "paused" status observed while a task runs in memory is
				// a stale snapshot (a resumed/new run racing the poll) and
				// must not surface Paused=true.
				status.Paused = state.Status == "paused" && !status.Active
			} else if stateErr != nil {
				m.log().Warn("get session runtime status: failed to load task state", "session", sessionID, "error", stateErr)
			}
		}
	}

	// Durable work-unit snapshot folded into the same status round-trip the
	// frontend already performs on session load — no extra RPC to reconcile
	// chat blocks. Units the resume funnel will not relaunch (abandoned in
	// flight by a crash/app exit) settle as interrupted here — never for a
	// cooperatively PAUSED task, whose untouched tail units are exactly what
	// Resume runs.
	if status.HasUnfinishedTask && status.UnfinishedTaskID != "" {
		status.WorkUnits = m.workUnitSnapshot(sessionID, status.UnfinishedTaskID, status.Active, status.Paused)
	}

	return status, nil
}

// workUnitSnapshot returns the durable work-unit ledger for a task as the
// frontend-facing snapshot used to reconcile chat blocks after a restart or
// session load.
//
// A unit left in flight by a task no longer executing was abandoned by a
// crash or app exit: the resume funnel does not relaunch it, so it settles
// explicitly as interrupted and a "work_unit_settled" event fires for any
// live view. The settle is a column-scoped transition conditional on the unit
// still being in flight — it can neither drop a spec/checkpoint another
// writer set nor double-emit when pollers race, and is idempotent (once
// settled, later polls neither write nor emit).
//
// active: the task is executing in memory — a live task's running units are
// genuinely running, never settled. pausedTask: a cooperatively PAUSED task
// is not executing but not abandoned either — its untouched tail units are
// the canonical resumable checkpoint Resume runs, so nothing settles. A
// paused unit is an explicit resumable checkpoint the funnel owns, untouched
// in every case. Container units (task root, goal-verification pass) are not
// relaunchable execution work — excluded exactly as the resume funnel
// excludes them. No durable unit storage (older store, test double) → empty.
func (m *Manager) workUnitSnapshot(sessionID, taskID string, active, pausedTask bool) []WorkUnitStatus {
	if taskID == "" {
		return nil
	}
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return nil
	}
	store := NewTaskStoreAdapter(ts).UnitStore()
	if store == nil {
		return nil // no durable unit storage — nothing to reconcile
	}
	recs, err := store.LoadUnits(taskID)
	if err != nil {
		m.log().Warn("failed to load work units", "session", sessionID, "task", taskID, "error", err)
		return nil
	}
	if len(recs) == 0 {
		return nil
	}

	out := make([]WorkUnitStatus, 0, len(recs))
	for _, rec := range recs {
		if rec.Kind.Container() {
			// A container is not execution work: surfacing it would hand the
			// frontend a block with no chat item, and settling it would
			// durably assert a finished pass was abandoned.
			continue
		}
		status := rec.Status
		// Abandoned in flight → settle as interrupted. A paused task's
		// in-flight units are NOT abandoned (Resume runs them), and a paused
		// unit is a resumable checkpoint the funnel owns — neither settles.
		if !active && !pausedTask && status.InFlight() {
			if changed, err := store.SettleUnitStatusIfInFlight(taskID, rec.Namespace, rec.ID, units.UnitStatusInterrupted); err != nil {
				m.log().Warn("failed to settle abandoned work unit", "session", sessionID, "unit", rec.ID, "error", err)
			} else {
				status = units.UnitStatusInterrupted
				if changed {
					m.EmitSessionEvent(sessionID, "work_unit_settled", WorkUnitSettledData{
						StepID: rec.ID,
						Status: string(units.UnitStatusInterrupted),
						Reason: "unit was abandoned before it settled",
					})
				}
			}
		}
		out = append(out, WorkUnitStatus{
			StepID:   rec.ID,
			Kind:     string(rec.Kind),
			Status:   string(status),
			ParentID: rec.ParentID,
		})
	}
	// Deterministic order (the store returns creation order); sort by status
	// then id so repeated polls produce a stable payload.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return out[i].Status < out[j].Status
		}
		return out[i].StepID < out[j].StepID
	})
	return out
}

// hungSessionThreshold is how long a STOP (CancelTask) may go unanswered
// before ActiveSessions reports the session as hung. CancelTask cancels the
// context and force-terminates after stopTimeout (10s); the cancellation
// aborts an in-flight LLM request and every in-flight tool, so a healthy task
// settles well within the threshold — a Stop still unanswered past it means a
// genuinely wedged goroutine, and the quit modal may honestly present "not
// responding" (the wire contract of ActiveSessionInfo.Hung). A cooperative
// PAUSE is deliberately excluded: it is answered at the next step boundary,
// which a long LLM call routinely delays past this threshold.
const hungSessionThreshold = 10 * time.Second

// ActiveSessionInfo identifies a session with live background work, for the
// desktop close-confirmation guard.
type ActiveSessionInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Compacting: the live work is an in-flight manual compaction, not a task.
	Compacting bool `json:"compacting"`
	// Hung: a STOP was issued but the task goroutine has not answered within
	// hungSessionThreshold — the quit modal flags a wedged session so
	// "quit anyway" is an informed choice. A cooperative pause never sets it.
	Hung bool `json:"hung"`
}

// ActiveSessions returns the in-memory sessions with live background work (a
// running task or an in-flight manual compaction); the desktop close guard
// uses it to decide whether quitting needs confirmation — this work dies with
// the process, unlike persisted resumable tasks. Memory-only by design: a
// session not in memory cannot have live work, and the check must never
// restore sessions as a side effect.
func (m *Manager) ActiveSessions() []ActiveSessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	active := make([]ActiveSessionInfo, 0)
	for _, s := range m.sessions {
		s.mu.Lock()
		running := s.active
		compacting := s.compacting
		name := s.Name
		stopRequestedAt := s.stopRequestedAt
		s.mu.Unlock()
		if !running && !compacting {
			continue
		}
		active = append(active, ActiveSessionInfo{
			ID:         s.ID,
			Name:       name,
			Compacting: compacting && !running,
			Hung:       !stopRequestedAt.IsZero() && time.Since(stopRequestedAt) >= hungSessionThreshold,
		})
	}

	// Deterministic order (map iteration is random) so repeated guard checks
	// produce a stable payload for the frontend dialog.
	sort.Slice(active, func(i, j int) bool { return active[i].ID < active[j].ID })
	return active
}

// LiveTokenSnapshot returns the in-memory token/fill state — the same values
// the emitter broadcasts via session_tokens/context_fill — so
// GetSessionTokens can serve live used/max (and fresh fill/model) for a
// mid-task session, values the persisted row only partially carries.
// Memory-only: no restore side effect; ok=false when not in memory (caller
// falls back to the store).
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

// ValidateLiveSend runs the live-send gate checks WITHOUT queuing: with a
// task running it returns the same rejections as sendMessage's live branch
// (pause window, goal, E2S, skill/agent/MCP references). The frontend API
// calls it BEFORE persisting the user message so a rejected live send never
// leaves a phantom persisted row. Memory-only (no restore side effect);
// no-op with no running task. The authoritative re-check still happens under
// the session lock in sendMessage — a message passing here that finds the
// task finished simply starts a normal task.
func (m *Manager) ValidateLiveSend(sessionID string, goal, e2s bool, text string, activeSkills, activeAgents, activeMCPServers []string) error {
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess == nil {
		return nil
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	// A compacting session rejects sends even when no task runs: the
	// compaction flow owns the idle window between the pause landing and the
	// auto-resume (mirrors sendMessage's guard).
	if sess.compacting {
		return ErrSessionCompacting
	}
	if !sess.active {
		return nil
	}
	return liveSendRejectionLocked(sess, goal, e2s, text, activeSkills, activeAgents, activeMCPServers)
}

// emitTaskCancelledUnlessShuttingDown emits task_cancelled unless the manager
// is shutting down. Returns true when the caller should persist the
// cancellation (user-initiated), false on shutdown — the task stays
// in_progress and resumable, so the caller skips persistence entirely.
//
// Also the single gate for the run's terminal emission: when
// forceTerminateStuckTask already claimed the run (a Stop that exhausted
// stopTimeout while the goroutine ignored cancellation), a settling goroutine
// finds the claim taken and skips its own task_cancelled + metrics — no
// duplicate terminal event with zeroed counters over the forced one. It still
// returns true so the caller's idempotent cleanup runs.
func (m *Manager) emitTaskCancelledUnlessShuttingDown(id string) bool {
	if m.shuttingDown.Load() {
		return false
	}
	if s, ok := m.sessionByID(id); ok && s != nil && !m.claimTaskTerminal(s) {
		return true
	}
	m.emitTaskCancelled(id)
	return true
}

// emitTaskCancelled emits the terminal "task_cancelled" event plus the run's
// agent metrics. It performs no once-per-run check — callers own that decision
// (emitTaskCancelledUnlessShuttingDown / forceTerminateStuckTask).
func (m *Manager) emitTaskCancelled(id string) {
	m.emitFunc(Event{
		SessionID: id,
		Type:      "task_cancelled",
		Data:      TaskCancelledData{SessionID: id},
	})
	m.emitAgentMetrics(id, "cancelled")
}

// sessionByID returns the live in-memory session for id WITHOUT the lazy
// restore GetSession performs. It backs the terminal-emission claim, which must
// never resurrect a session as a side effect.
func (m *Manager) sessionByID(id string) (*Session, bool) {
	m.mu.RLock()
	s := m.sessions[id]
	m.mu.RUnlock()
	return s, s != nil
}

// claimTaskTerminal atomically claims the run's single terminal emission on the
// session. It returns true for the first caller (which then owns the emission)
// and false when one has already been claimed — forceTerminateStuckTask for a
// stuck run, or the settling goroutine's own normal path.
func (m *Manager) claimTaskTerminal(s *Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminalEmitted {
		return false
	}
	s.terminalEmitted = true
	return true
}

// claimTaskTerminalForceTerminated atomically claims the run's single
// terminal emission AND marks the run force-terminated under one lock, for
// forceTerminateStuckTask. Combining both writes is what makes the
// force-terminate path race-free: a settling stuck goroutine's
// deactivateSessionTask reads forceTerminated under s.mu, so it must never
// see the emission claimed while forceTerminated is still false (which would
// discard a live message queued after the cancel was reported). True for the
// first caller, false when the emission was already claimed.
func (m *Manager) claimTaskTerminalForceTerminated(s *Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminalEmitted {
		return false
	}
	s.terminalEmitted = true
	s.forceTerminated = true
	return true
}

// emitAgentMetrics emits the aggregated agent quality metrics (parse errors,
// loop-detector nudges/aborts, steps, output tokens, active Model Profiles
// profile) for the run that just finished — complete, cancel or failure. The
// counters reset afterwards, so one agent_metrics event covers exactly one
// run. No-op when the session is no longer tracked.
//
// MEMORY-ONLY lookup (sessionByID, no lazy restore): this runs from
// task-terminal callbacks, including a wedged run settling after DeleteSession
// stopped waiting. GetSession would lazily RESTORE the just-deleted session
// (the store row still exists in that window), rebuilding an orchestrator and
// reopening log/dump handles nobody will ever close.
func (m *Manager) emitAgentMetrics(sessionID, finish string) {
	s, ok := m.sessionByID(sessionID)
	if !ok || s == nil || s.emitter == nil {
		return
	}
	s.emitter.EmitAgentMetrics(finish)
}

// ExecutionWindow returns the host-side recent-execution window (bounded
// trajectory + run counters + plan progress); ok=false when the session is
// unknown or has no emitter. It backs the silent-mode step-limit judge, which
// needs the trajectory, not just the boundary reason. Memory-only (see
// emitAgentMetrics): a judge callback must never lazily restore a session.
func (m *Manager) ExecutionWindow(sessionID string) (ExecutionWindowSnapshot, bool) {
	if sessionID == "" {
		return ExecutionWindowSnapshot{}, false
	}
	s, ok := m.sessionByID(sessionID)
	if !ok || s == nil || s.emitter == nil {
		return ExecutionWindowSnapshot{}, false
	}
	return s.emitter.ExecutionWindowSnapshot(), true
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

// persistPauseIfUnfinished marks the unfinished task paused in the task store
// — the graceful-shutdown counterpart of persistCancellationIfUnfinished:
// instead of cancelling (or leaving a stale in_progress), the task is
// checkpointed paused so it survives restart clearly resumable and
// SessionRuntimeStatus reports Paused=true. Only in_progress is paused.
// Best-effort: errors logged only.
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
	// Only in_progress is paused on shutdown: a task that already failed, was
	// cancelled, or was paused cooperatively keeps its real status — pausing
	// it would mask the actual outcome.
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
// cancelled on a user-initiated cancel, so a later resume does not re-enter
// the goal loop. On shutdown the goal stays active (non-terminal) and
// survives restart — this runs only inside the
// emitTaskCancelledUnlessShuttingDown == true branch. Must run BEFORE
// persistCancellationIfUnfinished/bb.CancelTask: a cancelled task is no longer
// "unfinished" and GetUnfinishedTaskID would not find it. Best-effort.
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

// abandonE2SIfUnfinished terminalizes the unfinished task's E2S state as
// cancelled on a user-initiated cancel, so a later resume does not re-enter
// the E2S loop with a stale Σ. Mirrors abandonGoalIfUnfinished: non-terminal
// on shutdown (survives restart), only inside the not-shutting-down branch,
// and BEFORE persistCancellationIfUnfinished/bb.CancelTask. Best-effort.
func (m *Manager) abandonE2SIfUnfinished(sessionID string) {
	tid := m.unfinishedTaskID(sessionID)
	if tid == "" {
		return
	}
	m.terminalizeE2SState(tid)
}

// terminalizeE2SState marks the given task's persisted E2S state as cancelled
// (terminal) so a later resume does not re-enter the E2S loop with a stale Σ.
// It is a no-op when there is no task store, no persisted state, or the state
// is already terminal. Best-effort: errors are logged only.
func (m *Manager) terminalizeE2SState(tid string) {
	if tid == "" {
		return
	}
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return
	}
	adapter := NewTaskStoreAdapter(ts)
	st, err := adapter.LoadE2SState(tid)
	if err != nil {
		m.log().Warn("failed to load e2s state to terminalize", "task", tid, "error", err)
		return
	}
	if st == nil || e2sStateTerminal(st.Status) {
		return
	}
	st.Status = e2spkg.StateStatusCancelled
	if st.Sigma != nil {
		st.Sigma[e2spkg.CoreKeyStatus] = string(e2spkg.StateStatusCancelled)
	}
	if err := adapter.PersistE2SState(tid, st); err != nil {
		m.log().Warn("failed to persist e2s cancellation", "task", tid, "error", err)
	}
}

// e2sStateTerminal reports whether the E2S lifecycle status is terminal — a
// resume must only re-enter the loop for a non-terminal (active/paused) run.
func e2sStateTerminal(s e2spkg.StateStatus) bool {
	switch s {
	case e2spkg.StateStatusMet, e2spkg.StateStatusFailed, e2spkg.StateStatusCancelled:
		return true
	default:
		return false
	}
}

// abandonUnfinishedTaskForGoal cancels the session's unfinished task so a
// goal request starts a fresh pursuit instead of resuming the interrupted
// task: persists the cancellation, resolves the pending resumable banner, and
// emits a service notice. Best-effort; no-op without a store/unfinished task.
func (m *Manager) abandonUnfinishedTaskForGoal(id string) {
	m.abandonUnfinishedTaskForMode(id, "abandoned_for_goal", "Interrupted task abandoned to start goal mode.")
}

// abandonUnfinishedTaskForE2S cancels the unfinished task so an E2S request
// starts a fresh run — the E2S mirror of abandonUnfinishedTaskForGoal.
//
// Unlike the goal takeover, the E2S takeover is not stateless: runE2SLoop
// resumes any non-terminal persisted Σ under the continuation anchor
// (core.loadE2SResumeState), and ResumeState takes precedence over the new
// message — a leftover active Σ or stale anchor would make the new E2S
// instruction a silent no-op. So the abandoned task's Σ is terminalized and
// the anchor dropped when it points at that task.
func (m *Manager) abandonUnfinishedTaskForE2S(id string) {
	tid := m.abandonUnfinishedTaskForMode(id, "abandoned_for_e2s", "Interrupted task abandoned to start E2S mode.")
	if tid == "" {
		return
	}
	// Drop the continuation anchor when it points at the abandoned task: the
	// anchor is restored status-agnostically (GetLatestTaskID) on session
	// restore, so after a restart it can equal the interrupted task; leaving
	// it would make the send restore the abandoned task's blackboard instead
	// of starting the fresh E2S task this takeover intends.
	m.mu.RLock()
	sess := m.sessions[id]
	m.mu.RUnlock()
	if sess == nil {
		return
	}
	sess.mu.Lock()
	if sess.lastCompletedTaskID == tid {
		sess.lastCompletedTaskID = ""
	}
	sess.mu.Unlock()
}

// abandonUnfinishedTaskForMode is the shared body behind the goal/E2S
// abandon-on-mode-takeover helpers: drops any armed deferred
// resume-compaction, persists the cancellation, resolves the lingering
// resumable banner, and emits the given service notice. Returns the abandoned
// task's ID ("" when none) so the E2S takeover can also clear the run's
// persisted state and continuation anchor; the goal takeover ignores it.
func (m *Manager) abandonUnfinishedTaskForMode(id, bannerReason, serviceContent string) string {
	// The abandoned task may carry an armed deferred resume-compaction (a
	// manual no-op compaction deferred to its resume) — drop it so the new
	// mode's loop (or any later task) does not inherit the forced compaction
	// chosen for the abandoned task.
	m.clearResumeRequests(id)
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return ""
	}
	adapter := NewTaskStoreAdapter(ts)
	tid, err := adapter.GetUnfinishedTaskID(id)
	if err != nil {
		m.log().Warn("mode takeover: failed to look up unfinished task to abandon", "session", id, "error", err)
		return ""
	}
	if tid == "" {
		return ""
	}
	if err := adapter.PersistCancellation(tid); err != nil {
		m.log().Warn("mode takeover: failed to cancel unfinished task", "task", tid, "error", err)
	}
	// A takeover abandons the task for good, so terminalize any persisted E2S
	// Σ too — otherwise a later resume still resolving the anchor could
	// re-enter the E2S loop with the abandoned run's state (no-op for a
	// non-E2S task).
	m.terminalizeE2SState(tid)
	m.resolveResumableTaskMessage(id, tid, bannerReason)
	m.emitFunc(Event{
		SessionID: id,
		Type:      "service",
		Data: map[string]any{
			"content": serviceContent,
			"phase":   "orchestration",
		},
	})
	return tid
}

// shouldRetryContinuationFresh reports whether a failed continuation attempt
// (HandleMessage with a TaskID) should retry fresh. anchorWasUnfinished is
// the caller's PRE-HandleMessage snapshot of whether the anchor was already
// the session's unfinished task when the send began; it disambiguates the two
// ways an unfinished lookup can return the anchor after a failure:
//
//   - false: a normal send only reaches HandleMessage with a TaskID when no
//     unfinished task existed at send time (an unfinished task goes through
//     tryContinueInterruptedTask), and the anchor is reactivated to
//     in_progress only once its execution actually starts (post-routing,
//     reactivateContinuationTask). So an unfinished lookup returning the
//     anchor means the continuation's own execution started and failed
//     mid-flight: retrying fresh would orphan the reactivated row — the
//     session would report has_unfinished_task=true forever and re-inject the
//     "Task failed / Resume" banner after every restart over an otherwise
//     completed session. Do NOT retry; the error falls through to the shared
//     error path, which emits the resumable banner instead.
//
//   - true: the anchor predates the send — lastCompletedTaskID was restored
//     without a status check (may point at a failed task after a restart) and
//     the resume path itself could not restore the blackboard/trajectory,
//     falling back to a continuation on a row that never was terminal. A
//     failure here must not dead-end on the banner (the resume path hits the
//     same restore error), so a fresh retry is allowed; if it succeeds,
//     sweepStaleUnfinishedTasks cancels the leftover anchor row.
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

// emitResumableIfUnfinished emits "task_failed_resumable" when the session
// has an unfinished task, so the frontend can offer Resume. Returns true when
// emitted, so callers that KNOW the execution was degraded can surface a
// fallback warning when the safety net is unavailable (nil store, lookup
// error, no unfinished record). reason is a concise contextual cause
// prepended to the banner and carried in the Reason field ("" = generic);
// cause, when non-nil, is inspected for the automatic-retry timer (a
// classified *llm.Error with a rate-limit status arms it and surfaces
// AutoRetryAt; nil never arms).
//
// Emits ONLY the banner: agent_metrics resets on every emission, so exactly
// one must fire per terminal path — emitting here would double-count on the
// emitTaskComplete path and zero out the real per-run report.
func (m *Manager) emitResumableIfUnfinished(sessionID, reason string, cause error) bool {
	taskID := m.unfinishedTaskID(sessionID)
	if taskID == "" {
		return false
	}

	message := "Plan execution failed. You can resume to retry from where it left off."
	if reason != "" {
		message = reason + " You can resume to retry from where it left off."
	}

	autoRetryAt := m.maybeAutoRetryAt(cause)

	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      "task_failed_resumable",
		Data: TaskFailedResumableData{
			Message:     message,
			TaskID:      taskID,
			Reason:      reason,
			AutoRetryAt: autoRetryAt,
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

// resolveResumableTaskMessage marks the persisted task_failed_resumable
// message resolved, so the Resume/Cancel banner does not reappear as pending
// on session reload after the user resumes or cancels. Best-effort — the
// in-memory UI is already updated optimistically and a missed persist
// self-heals via stale reconciliation on the next reload.
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
	// Bounded like every other direct store call on the send/terminal paths
	// (restoreDBReadTimeout): this shares the app's single SQLite pool — an
	// unbounded wait for a pooled connection under a write storm would hang
	// the settle path.
	resolveCtx, resolveCancel := context.WithTimeout(context.Background(), restoreDBReadTimeout)
	defer resolveCancel()
	if err := store.ResolvePendingMessage(resolveCtx, sessionID, "task_failed_resumable", "task_id", taskID, extra); err != nil {
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

	// Prompt suggestions (ADR-088): one fail-open generation pass after a
	// full or partial finish, on a tracked background goroutine. The gate
	// reads the TYPED status — Cancelled and Paused share the "partial"
	// string with degraded outcomes but finished nothing, so they never
	// trigger. Guards checked before the goroutine spawns.
	if promptSuggestOutcomeTriggers(result) {
		m.maybeSpawnPromptSuggestions(sessionID, result)
	}

	// On a genuinely successful completion, NEVER offer a resume — the result
	// is final. If the task just ran is still marked unfinished, the
	// completion write raced or was dropped: surface a persistence warning
	// instead of a misleading resume banner
	// (warnIfCompletionDidNotPersist). Any OTHER unfinished row is a stale
	// orphan of an abandoned continuation — cancel it so
	// has_unfinished_task finally clears (sweepStaleUnfinishedTasks).
	if success {
		m.warnIfCompletionDidNotPersist(sessionID, result)
		m.sweepStaleUnfinishedTasks(sessionID, result)
		return
	}

	// The typed degraded-completion cause (ADR-065 follow-up): a result whose
	// run collapsed its error into the output (the goal loop's errored-turn
	// halt) carries it on HandleResult.Err so the auto-retry classifier still
	// sees the *llm.Error chain. Non-degraded outcomes (plain
	// partial/aborted/cancelled — no typed cause) pass nil and never arm the
	// timer, exactly as before.
	cause := error(nil)
	if result != nil {
		cause = result.Err
	}
	resumableEmitted := m.emitResumableIfUnfinished(sessionID, completionReason(completion), cause)
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

// sweepStaleUnfinishedTasks cancels leftover unfinished (in_progress / paused
// / failed) rows after a successful completion and resolves their persisted
// resumable banners. Such rows are stale by construction: a session executes
// one task at a time and a fresh task starts only when no unfinished task
// exists, so any unfinished row other than the just-completed one is the
// orphan of an abandoned continuation (historically the
// reactivated-then-abandoned anchor of the fresh-workflow fallback). Left in
// place, the row keeps has_unfinished_task=true forever and every restart
// re-injects the "Task failed / Resume" banner over a completed session.
// Best-effort: errors logged only.
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
	// blackboard there is no ID identifying the just-completed task, so the
	// sweep cannot tell its (possibly racing) row apart from a stale orphan —
	// cancel nothing rather than risk discarding the live row.
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
		// Done when nothing is unfinished, or when the lookup reaches the
		// just-completed task's own row (its completion raced or was dropped —
		// warnIfCompletionDidNotPersist surfaced that). The lookup returns one
		// row at a time, so an older orphan behind it stays for the NEXT
		// successful completion — best-effort by design.
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
		// Nothing running, but a cooperatively paused (or otherwise
		// unfinished, resumable) task is still cancellable via Stop: discard
		// it and emit task_cancelled so the frontend leaves the paused state.
		// With no unfinished task, keep ErrNoActiveTask so callers can
		// distinguish "nothing running" from a successful cancellation.
		return m.cancelUnfinishedTask(id)
	}

	doneCh := session.done
	// Record when the stop was requested so a session that ignores it can be
	// flagged hung by ActiveSessions while CancelTask waits (keep the FIRST
	// timestamp on a repeat Stop — resetting it would hide the hang).
	if session.stopRequestedAt.IsZero() {
		session.stopRequestedAt = time.Now()
	}
	if session.cancel != nil {
		session.cancel()
	}
	session.mu.Unlock()

	// Wait so the task_cancelled event lands before this returns to the
	// frontend.
	if doneCh != nil {
		select {
		case <-doneCh:
		case <-time.After(m.stopTimeout):
			// Re-check done non-blockingly first: the goroutine can settle in
			// the instant between the timer firing and this arm running — a
			// teardown that actually completed must win, or we log a spurious
			// "forcing terminal state" and race the goroutine's own emission
			// (the claim prevents a duplicate, but there is nothing to force).
			select {
			case <-doneCh:
				break
			default:
				m.log().Warn("timed out waiting for task goroutine to stop on cancel; forcing terminal state", "session_id", id)
				m.forceTerminateStuckTask(id)
			}
		}
	}

	return nil
}

// forceTerminateStuckTask is CancelTask's escape hatch for a goroutine that
// ignored its cancellation for the whole stopTimeout — typically parked in a
// tool call that never returns. Returning nil silently would leave the UI on
// "Generating response…" forever (the terminal event is emitted by the stuck
// goroutine itself). Instead the cancellation is forced onto the durable
// state and the wire: the persisted task flips to cancelled (banner
// resolved), and the terminal task_cancelled + agent metrics are emitted
// here, so the frontend clears its running state.
//
// The session is deliberately NOT deactivated: the goroutine really is still
// running, so ActiveSessions keeps listing it and the close guard flags it
// hung — the honest picture on quit.
//
// Two guards make the forced path mirror (never double) the goroutine's own
// terminal path:
//   - Shutdown wins: a Stop racing shutdown leaves the task in_progress and
//     resumable, exactly like emitTaskCancelledUnlessShuttingDown — the
//     forced path persists and emits nothing while shutting down.
//   - The emission is claimed once per run (claimTaskTerminal): the stuck
//     goroutine, when it finally settles, finds the claim taken and skips its
//     own task_cancelled + zeroed metrics; a repeated Stop click is a no-op.
func (m *Manager) forceTerminateStuckTask(sessionID string) {
	// On shutdown the task must stay in_progress/resumable: neither persist
	// nor emit.
	if m.shuttingDown.Load() {
		return
	}
	// Claim the run's single terminal emission; already claimed (repeated
	// Stop, or the goroutine settled at the deadline and emitted its own) →
	// nothing left to force.
	if s, ok := m.sessionByID(sessionID); ok && s != nil {
		// Claim the emission AND mark force-terminated in ONE critical
		// section, so a settling stuck goroutine's deactivateSessionTask can
		// never observe the claim taken while forceTerminated is still false
		// — which would make it discard a live message the user queued after
		// this terminal was reported (see deactivateSessionTask).
		if !m.claimTaskTerminalForceTerminated(s) {
			return
		}
	}

	m.clearResumeRequests(sessionID)

	// Best-effort durable terminalization: without it the orphaned in_progress
	// row would survive as a stale has_unfinished_task after restart. The store
	// may be absent (no persistence configured).
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts != nil {
		adapter := NewTaskStoreAdapter(ts)
		taskID, err := adapter.GetUnfinishedTaskID(sessionID)
		if err != nil {
			m.log().Warn("stuck-cancel: failed to look up unfinished task", "session", sessionID, "error", err)
		} else if taskID != "" {
			// Terminalize the run's goal/E2S state BEFORE persisting the
			// cancellation, mirroring the settle paths' ordering (abandon →
			// persistCancellationIfUnfinished). Both abandon helpers resolve
			// the task via GetUnfinishedTaskID (in_progress/paused/failed
			// only) — after PersistCancellation flips the row to cancelled
			// they would no-op, leaving the goal state / E2S Σ non-terminal
			// while the row reads cancelled: on restore the continuation
			// anchor lands on the cancelled task (GetLatestTaskID is
			// status-agnostic) and the next send silently RESUMES a run the
			// user explicitly stopped instead of honoring the new instruction.
			m.abandonGoalIfUnfinished(sessionID)
			m.abandonE2SIfUnfinished(sessionID)
			if err := adapter.PersistCancellation(taskID); err != nil {
				m.log().Warn("stuck-cancel: failed to persist cancellation", "session", sessionID, "task", taskID, "error", err)
			}
			// Resolve the prior task_failed_resumable banner so it does not
			// reappear as pending on reload after the user stopped the task.
			m.resolveResumableTaskMessage(sessionID, taskID, "cancelled")
		}
	}

	// Emit the terminal event: this call owns the run's emission, and the stuck
	// goroutine (which owns the normal one) will never reach its own.
	m.emitTaskCancelled(sessionID)
}

// cancelUnfinishedTask discards the session's unfinished (e.g. cooperatively
// paused) task and emits task_cancelled so the frontend leaves the
// paused/resumable state — the non-active counterpart of CancelTask: the
// goroutine has already exited, so instead of signalling a context it flips
// the persisted task to cancelled and emits the terminal event directly. Any
// deferred resume-compaction armed for the task is discarded too (mirroring
// CancelUnfinishedTask): a Stop-button cancel of the paused task must not
// leave a one-shot flag a later, unrelated task's resume would consume.
// ErrNoActiveTask when there is nothing to cancel, preserving the sentinel.
func (m *Manager) cancelUnfinishedTask(sessionID string) error {
	m.clearResumeRequests(sessionID)
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
	// clears the paused state. A cooperatively paused run has not yet emitted
	// agent metrics (pause is a checkpoint, not a terminal state), so this is
	// the run's single terminal emission.
	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      "task_cancelled",
		Data:      TaskCancelledData{SessionID: sessionID},
	})
	m.emitAgentMetrics(sessionID, "cancelled")

	return nil
}

// GetBlackboardState returns the session's current blackboard state, using
// the in-memory lastCompletedTaskID when available, else the most recent task
// ID from the database (nil, nil when none).
//
// MEMORY-ONLY session lookup (no lazy restore): a read-only display RPC fired
// by the frontend's blackboard hook on every session switch — restoring a
// store-only session here would build a full orchestrator and open
// log/dump/step-dump handles nobody closes — a ghost session for one the user
// merely viewed. A session not in memory cannot carry an in-memory anchor, so
// the only difference is skipping the restore: the fallback queries the same
// GetLatestTaskID the restore anchors from, producing byte-identical output
// with none of the side effects.
func (m *Manager) GetBlackboardState(sessionID string) (*BlackboardState, error) {
	sess, _ := m.sessionByID(sessionID)

	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()

	if ts == nil {
		return nil, nil // no task persistence — no blackboard state
	}

	// Try in-memory lastCompletedTaskID first (only a live session has one).
	if sess != nil {
		sess.mu.Lock()
		taskID := sess.lastCompletedTaskID
		sess.mu.Unlock()
		if taskID != "" {
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
	}

	// Fallback: query the database for the latest task. Bounded like the
	// restore-path anchor read (restoreDBReadTimeout) — this runs on the
	// session-switch RPC and shares the app's single SQLite pool.
	dbCtx, dbCancel := context.WithTimeout(context.Background(), restoreDBReadTimeout)
	defer dbCancel()
	dbTaskID, dbErr := ts.GetLatestTaskID(dbCtx, sessionID)
	if dbErr != nil {
		return nil, fmt.Errorf("failed to get latest task ID: %w", dbErr)
	}
	if dbTaskID == "" {
		return nil, nil // no tasks for this session
	}

	adapter := NewTaskStoreAdapter(ts)
	state, err := adapter.LoadTaskState(dbTaskID)
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

// maybeSpawnTitleGeneration auto-names a session from its first message.
//
// The generation runs on a tracked background goroutine derived from the
// manager's shutdown context: it writes through the session dump file, so
// Shutdown must be able to abort it and wait before closing that handle.
//
// Only a session still carrying its generated placeholder name is renamed,
// and never a presented relaunch (the live-send follow-up), whose first
// message was already handled when the original task launched.
//
// The readiness gate runs BEFORE the service timeout is armed (serviceLLMGate):
// a gate failure skips the rename and leaves the placeholder — the session
// stays usable, and the run's own request reports the cause to the user.
func (m *Manager) maybeSpawnTitleGeneration(session *Session, id, text string, presented bool, activeSkills []string) {
	session.mu.Lock()
	sessionName := session.Name
	session.mu.Unlock()
	m.mu.RLock()
	titleGen := m.titleGen
	store := m.sessionStore
	serviceLLMTimeout := m.serviceLLMTimeout
	serviceLLMGate := m.serviceLLMGate
	m.mu.RUnlock()
	if presented || sessionName != "Session "+safeSessionPrefix(id) || titleGen == nil {
		return
	}
	dumpFile := session.DumpFile()
	// Tracked by the manager and derived from its shutdown context: the
	// goroutine writes through the session dump file, so Shutdown must be
	// able to abort it and wait for it before closing that handle.
	titleSpawned := m.spawnBackgroundNamed("session-title-generation session="+id, func() {
		if dumpFile != nil {
			defer func() { _ = dumpFile.Close() }()
		}
		// Readiness BEFORE the budget: the embedded model's cold load is
		// longer than the service timeout, so arming the timeout first would
		// spend it on the load and lose the title. A failure here skips the
		// rename (placeholder stays; the session remains usable).
		//
		// BY DESIGN the gate sits outside the serviceLLMTimeout envelope
		// (ADR-066 D13: no LLM request may start before the model is
		// resident; the load is never charged to the request's own budget).
		// The worst-case wait before this goroutine's budget starts is
		// embeddedllm.DefaultLoadWaitTimeout when the local model is the
		// default and cold (derivation stated on the constant). Nothing
		// user-facing blocks: tracked background goroutine — the only
		// observable effect is a late (or skipped) auto-name.
		if serviceLLMGate != nil {
			if err := serviceLLMGate(m.shutdownCtx); err != nil {
				m.log().Warn("skipping session title generation: the model is not ready",
					"session", id, "error", err)
				return
			}
		}
		ctx, cancel := context.WithTimeout(m.shutdownCtx, serviceLLMTimeout)
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
	})
	if !titleSpawned && dumpFile != nil {
		// Shutdown closed the tracker concurrently, so the closure never runs
		// and its deferred close never happens. DumpFile duplicated the
		// descriptor for the closure to own — close it here or it leaks until
		// process exit (and on Windows blocks the dump file's removal).
		_ = dumpFile.Close()
	}
}
