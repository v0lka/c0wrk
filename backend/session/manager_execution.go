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
// (read-only) session; restoring the session clears the flag. See
// specs/domains/session-lifecycle.md.
var ErrSessionArchived = errors.New("session is archived: restore it before sending messages or resuming tasks")

// ErrPausePending is returned by SendMessage inside the pausing window (pause
// signalled, cooperative pause not yet landed). See
// specs/domains/session-lifecycle.md.
var ErrPausePending = errors.New("session is pausing — send again once the pause completes")

// finishLiveLeftover launches the follow-up task for live messages queued but
// never delivered before the request finished (leftover is nil for every other
// epilogue action): a presented relaunch on a FRESH context — the original
// task's cancel func must not govern it. See specs/domains/session-lifecycle.md.
// When a manual compaction owns the session (the follow-up send hits the
// compacting guard), the drained messages are re-queued on the orchestrator
// instead of lost: live text reaches the model only through that queue, and
// the first epilogue after the compaction releases the session drains it.
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
		// A service notice, not an `error`: that type is reserved for terminal
		// events, and this failure does not settle the just-finished task.
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

// requeueLiveMessages puts live-message text back on the session's orchestrator
// live queue after the compaction guard rejected the follow-up send. Returns
// false when the session or its orchestrator is gone (deleted mid-flight) —
// the caller falls back to the plain error notice.
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
// emitted AFTER this call: consumers reacting to task_complete/session_paused
// treat the session as free, so they must already observe active == false.
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
		// A force-terminated run's terminal event already fired while the
		// goroutine was stuck: a message queued since then belongs to the
		// NEXT task — keep it queued. See specs/domains/session-lifecycle.md.
		if forceTerminated {
			return nil
		}
		session.orchestrator.DiscardLiveUserMessages()
	}
	return nil
}

// loadWorkDirectories fetches project-scoped then session-scoped auxiliary work
// directories, loaded fresh every call so mid-session additions take effect on
// the next task (best-effort: errors logged, entries skipped).
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
// for task execution — security roots only (sdktools.WithAllowedRoots), never
// surfaced in the prompt or UI. See specs/architecture/security-model.md.
//
// goos/tempDir/systemRootEnv are parameterized for testability. Windows
// candidates: tempDir and %SystemRoot%\Temp; elsewhere "/tmp" and tempDir.
// Validation is host-independent string analysis (NOT filepath.Clean/Join,
// whose separator semantics depend on the host — every branch must behave
// identically on any CI runner): trailing separators trimmed, duplicates
// dropped, relative/drive-relative skipped. The containment API requires
// absolute roots.
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

// isAbsForGOOS reports whether p is absolute for the given GOOS via pure
// string analysis, so a windows branch evaluated on a POSIX host (and vice
// versa) yields the native verdict. Drive-relative ("C:foo") is NOT absolute.
func isAbsForGOOS(goos, p string) bool {
	if goos == "windows" {
		if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') && isVolumeLetter(p[0]) {
			return true
		}
		return strings.HasPrefix(p, `\\`)
	}
	return strings.HasPrefix(p, "/")
}

// isVolumeLetter reports whether c is a Windows drive letter.
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
// is caller-loaded (shared with injectIgnoreChecker — one
// loadWorkDirectories per task); the prompt-facing WithWorkDirectories
// applies only when directories are configured. See
// specs/architecture/security-model.md.
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

// researchProjectInfo reports whether RESEARCH is active for the session's
// project and returns its root, derived from the session's own execution
// workspace. See specs/decisions/099-research-root-follows-active-session.md.
// Reads only immutable session fields — callers MUST NOT hold the session
// lock. ("", false) for No Project / no workspace.
func (m *Manager) researchProjectInfo(sess *Session) (string, bool) {
	if sess == nil || sess.ProjectID == project.NoProjectID || sess.WorkspacePath == "" {
		return "", false
	}
	return config.ProjectResearchPath(sess.WorkspacePath), true
}

// injectIgnoreChecker builds a multi-root ignore resolver (workspace + work
// directories) and attaches it via sdktools.WithIgnoreChecker; glob/ripgrep
// consult it to honour each root's own .gitignore/.aiignore (read_file stays
// unrestricted). dirs is caller-loaded (shared with injectWorkDirectories);
// no roots → ctx unchanged. Resolvers are built PER ROOT with skip-and-log,
// so one bad root only drops itself, and survivors combine via
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

	// Uncached roots build asynchronously: the resolver walk is expensive, and
	// blocking SendMessage on a huge directory (the Go module cache) causes
	// multi-minute hangs. The first message after startup may run without
	// ignore filtering; later messages pick up the finished walk's resolver.
	resolvers := make([]*ignore.Resolver, 0, len(roots))
	for _, root := range roots {
		if cached, loaded := m.ignoreCache.Load(root); loaded {
			r, ok := cached.(*ignore.Resolver)
			if !ok {
				continue
			}
			resolvers = append(resolvers, r)
			continue
		}
		// Kick off the async build (deduplicated inside startIgnoreBuild).
		m.startIgnoreBuild(root)
	}
	if len(resolvers) == 0 {
		return ctx
	}
	checker := ignore.NewMultiFromResolvers(m.log(), resolvers...)
	return sdktools.WithIgnoreChecker(ctx, checker)
}

// caseInsensitiveProbe carries one case-sensitivity probe result, published
// by closing done; race losers block on done and reuse it. See
// specs/architecture/security-model.md.
type caseInsensitiveProbe struct {
	done chan struct{}
	ci   bool
}

// defaultDetectCaseInsensitive is the production probe wired into
// Manager.detectCaseInsensitiveFn at construction; tests override the
// per-instance field, not this value.
var defaultDetectCaseInsensitive = pathutil.DetectCaseInsensitive

// caseInsensitiveCachedValue reads a cache entry's resolved bool, blocking
// until its probe completes.
func caseInsensitiveCachedValue(v any) (ci, ok bool) {
	p, okp := v.(*caseInsensitiveProbe)
	if !okp {
		return false, false
	}
	<-p.done
	return p.ci, true
}

// detectCaseInsensitive returns the cached case-sensitivity at path — a
// mount-level property, probed (CaseSense-*.probe create+delete) exactly once
// per resolved root, never on every SendMessage/ResumeSession. Empty path →
// the fail-safe case-sensitive false. See specs/architecture/security-model.md.
func (m *Manager) detectCaseInsensitive(path string) bool {
	if path == "" {
		return false
	}
	// Symlink-resolve so the cache key matches the physical root; on failure
	// (workspace not yet created) fall back to the raw path —
	// DetectCaseInsensitive climbs to the nearest existing ancestor.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	probe := &caseInsensitiveProbe{done: make(chan struct{})}
	if actual, loaded := m.caseInsensitiveCache.LoadOrStore(resolved, probe); loaded {
		existing, ok := actual.(*caseInsensitiveProbe)
		if !ok {
			return false
		}
		<-existing.done
		return existing.ci
	}
	probe.ci = m.detectCaseInsensitiveFn(resolved)
	close(probe.done)
	return probe.ci
}

// startIgnoreBuild launches a background walk of root and caches its
// ignore.Resolver, deduplicated via a LoadOrStore sentinel (a concurrent or
// completed build makes this a no-op). The goroutine is manager-tracked
// (background.go) so Shutdown joins it, and derives from the manager's
// shutdown context: on a huge root (module cache / vendor tree) the walk runs
// for minutes with no other cancellation, so shutdownCancel aborts it BEFORE
// stopBackground joins instead of burning stopTimeout on the quiescing main
// thread. Read-only walk: a straggler cannot add to a removed directory.
// See specs/domains/workspace.md (per-root ignore resolver cache).
func (m *Manager) startIgnoreBuild(root string) {
	sentinel := &ignore.Resolver{}
	if actual, loaded := m.ignoreCache.LoadOrStore(root, sentinel); loaded {
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
		// permanently "building" root.
		m.ignoreCache.Delete(root)
	}
}

// ignoreFileNames are the files whose change invalidates a cached resolver:
// NewResolver walks the whole root, so a change to any one of them anywhere
// under a root makes that root's resolver stale.
var ignoreFileNames = map[string]struct{}{
	".gitignore": {},
	".aiignore":  {},
	".ignore":    {}, // .ignore is honoured by ripgrep and some tools
}

// InvalidateIgnoreCache evicts cached resolvers whose root contains a changed
// ignore-rule file (.gitignore/.aiignore/.ignore) — the invalidation half of
// the async cache; without it ignore edits stay invisible until restart. It
// only DELETEs entries: the next injectIgnoreChecker rebuilds lazily, so at
// worst the first message after invalidation runs without ignore filtering.
// changedPaths are absolute (watcher form); pathutil.IsWithinPath resolves
// both sides against symlink-resolved roots, so the match holds behind a
// symlink too.
// See specs/domains/workspace.md (per-root ignore resolver cache).
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

	// Evict every cached root containing an affected file (a handful of
	// roots — a full Range is cheap).
	m.ignoreCache.Range(func(key, _ any) bool {
		root, ok := key.(string)
		if !ok {
			return true
		}
		for ignoreFile := range affected {
			within, err := pathutil.IsWithinPath(root, ignoreFile)
			if err != nil {
				// Vanished root — evict to be safe; the lazy rebuild re-adds
				// (or skips) it on next use.
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

// SendMessage runs a user message in a goroutine (results via events).
// reviewMode marks the message as carrying code-review feedback the agent
// must address; e2s selects the E2S (explicit-state) execution mode — the
// mirror of the goal flag, exclusive with it (both set is rejected). The
// single-return convenience form of SendMessageClassified; options forwarded
// unchanged. See specs/domains/session-lifecycle.md.
func (m *Manager) SendMessage(ctx context.Context, id, text string, activeSkills, activeAgents, activeMCPServers []string, modelOverride, reasoningEffort string, goal bool, goalBudget string, e2s, reviewMode bool, opts ...SendOption) error {
	_, err := m.SendMessageClassified(ctx, id, text, activeSkills, activeAgents, activeMCPServers, modelOverride, reasoningEffort, goal, goalBudget, e2s, reviewMode, opts...)
	return err
}

// SendClassification is the authoritative result of the session-locked send
// decision, so the caller can persist the matching is_nudge metadata: a
// nudge-resume and a live interjection carry it, a fresh task does not.
// See specs/domains/session-lifecycle.md.
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
	// the sender dedups the echo by identity, not content+time-window. See
	// specs/contracts/event-catalog.md.
	clientSendID string
}

// WithEchoText overrides the user-facing text carried by message_received.
// text is the POST-preprocessing form for the orchestrator; pass the RAW
// input here when preprocessing changed it (a "/skill" mention, an @file
// ref) — a processed echo would fail the sender's dedup and render a second
// ref-less card. Without the option the echo carries text (the historical
// behavior every internal caller and test relies on). See
// specs/contracts/event-catalog.md.
func WithEchoText(raw string) SendOption {
	return func(o *sendOptions) { o.echoText = raw }
}

// WithClientMessageID attaches the sending surface's optimistic-card id to
// the echo, so another surface sending identical text keeps its own card
// instead of being dropped as the sender's echo. See
// specs/contracts/event-catalog.md.
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

// SendMessageClassified runs a user message and returns its authoritative
// SendClassification; the task runs in a goroutine and reports via events.
// See specs/domains/session-lifecycle.md.
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
// while a task runs), or nil when it may queue. Caller holds session.mu —
// the authoritative gate; ValidateLiveSend pre-checks the same conditions
// before the frontend persists. See specs/domains/session-lifecycle.md.
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
		return errors.New("E2S requests cannot be sent while a task is running — pause or wait for completion first")
	case len(activeSkills) > 0 || len(activeAgents) > 0 || len(activeMCPServers) > 0:
		// Task-start concerns: skills/agents reshape the task context, and the
		// per-server MCP gate was fixed at task start — a mid-run mention
		// could never take effect, so reject up front, never silently ignore.
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
// the message_received emission and title generation. text is the
// post-preprocessing form for the orchestrator; echoText the raw user-facing
// form (WithEchoText); clientSendID the optimistic card's id
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
	if session.Archived {
		session.mu.Unlock()
		return SendFresh, ErrSessionArchived
	}
	// Same choke point for a session whose deletion has begun: DeleteSession
	// tears the orchestrator and log handles down after setting the flag.
	if session.deleting {
		session.mu.Unlock()
		return SendFresh, fmt.Errorf("session %s is being deleted", id)
	}
	// Live-send: a message while a task runs is queued into the RUNNING request
	// and delivered at the executor's next step boundary. Queue-while-locked
	// pairs with the epilogue's active=false flip under the same lock, so a
	// message in the closing window of a finishing request is either delivered
	// by it or becomes its follow-up — never lost, never duplicated. See
	// specs/domains/session-lifecycle.md.
	if session.active {
		if err := liveSendRejectionLocked(session, goal, e2s, text, activeSkills, activeAgents, activeMCPServers); err != nil {
			session.mu.Unlock()
			return SendFresh, err
		}
		session.orchestrator.QueueLiveUserMessage(text)
		session.mu.Unlock()
		// For non-optimistic listeners (the frontend renders its own optimistic
		// copy and ignores this event); IsNudge matches SendLive so other
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
	// Manual compaction owns the session until it finishes (it swaps the
	// history); a fresh task in that window would race the swap. The
	// nudge-resume path is covered by ResumeTask's own guard. See
	// specs/domains/memory/compaction.md.
	if session.compacting {
		session.mu.Unlock()
		return SendFresh, ErrSessionCompacting
	}
	session.mu.Unlock()

	// Nudge-resume: a message into a PAUSED session resumes the paused task via
	// ResumeSession (re-activate + inject the text as a trailing user turn)
	// instead of starting a new one. Goal/E2S requests are excluded — they
	// supersede any paused task. Detected before activation so ResumeTask's
	// own active-check is not tripped by a spurious "already processing".
	// See specs/domains/session-lifecycle.md.
	if !goal && !e2s && m.hasPausedUnfinishedTask(id) {
		resumeErr := m.ResumeSession(ctx, id, modelOverride, reasoningEffort, text)
		if resumeErr == nil {
			// Emit message_received only AFTER the resume commits (CS #66): a
			// failed resume makes the caller skip SaveMessage, and a
			// pre-dispatch echo would leave a card no persisted row backs.
			// IsNudge matches SendNudgeResume (Nudge badge on other surfaces).
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

	// RESEARCH mode from the session's own research root; the fields read are
	// immutable, so no lock is needed.
	researchRoot, isResearch := m.researchProjectInfo(session)

	session.mu.Lock()

	// Re-check the compacting window under the SAME hold as activation: the
	// guard above ran before the DB reads, and CompactSessionContext can have
	// armed in that gap — it would swap the history while this task runs.
	if session.compacting {
		session.mu.Unlock()
		return SendFresh, ErrSessionCompacting
	}

	// Set active and create the cancellable task context; launching consumes
	// any recorded pause owner (the run supersedes a previous pause request).
	session.active = true
	session.pauseOwner = pauseOwnerNone
	session.stopRequestedAt = time.Time{}
	session.terminalEmitted = false
	session.forceTerminated = false
	doneCh := make(chan struct{})
	session.done = doneCh
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

	// Activation committed: the single task_started point covers the fresh,
	// goal, E2S, and continue-interrupted dispatches; the resume path
	// announces itself via task_resumed + session_resumed. See
	// specs/domains/session-lifecycle.md.
	m.emitFunc(Event{SessionID: id, Type: EventTaskStarted})

	m.refreshAutonomyPosture(session)

	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo != nil {
		taskCtx = sdktools.WithEnvInfo(taskCtx, envInfo)
	}

	// One DB-hitting load per task, shared by both injectors; the implicit
	// temp roots are always in the allowed roots (also No Project).
	dirs := m.loadWorkDirectories(session)
	taskCtx = m.injectWorkDirectories(taskCtx, dirs)
	taskCtx = m.injectIgnoreChecker(taskCtx, session, dirs)

	// Skipped for a presented relaunch (already emitted when queued into the
	// running task); IsNudge stays absent — a fresh launch persists no
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
		// liveActionNone (zero) keeps undelivered live messages queued — the
		// default for paths that delegated elsewhere or left the task
		// resumable (the next resume delivers them).
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

		// Snapshot and clear pending attachments so they flush exactly once
		// into the blackboard; before the continue check so the resume path
		// (which bypasses HandleMessage) receives them too.
		session.mu.Lock()
		pendingAttachments := session.pendingAttachments
		session.pendingAttachments = nil
		pendingImages := session.pendingImageAttachments
		session.pendingImageAttachments = nil
		session.mu.Unlock()

		// Clear the pending-attachment chips; only emit when there were any.
		if len(pendingAttachments) > 0 || len(pendingImages) > 0 {
			m.emitAttachmentsChanged(id, []AttachmentInfo{}, nil)
		}

		// Staged images → LLM content blocks; the on-disk copies persist for
		// restart reconstruction (ChatMessage.Metadata).
		imageBlocks := imageAttachmentsToContentBlocks(pendingImages)

		// Detect goal mode (leading "/goal" OR the explicit flag) BEFORE the
		// resume check: a goal request starts a fresh pursuit, skipping the
		// resume path and abandoning any unfinished task.
		goalMsg, isGoal := core.DetectAndStripGoalMode(msg)
		goalEnabled := isGoal || goal

		switch {
		case goalEnabled:
			// Supersede any interrupted task so it cannot linger as resumable
			// WIP across the goal task (best-effort no-op without a store).
			m.abandonUnfinishedTaskForGoal(id)
		case e2s:
			// Σ is seeded only at task start, so E2S can never continue an
			// interrupted plain task — abandoned; a fresh E2S task starts below.
			m.abandonUnfinishedTaskForE2S(id)
		default:
			// Continue an interrupted task if one exists (see
			// tryContinueInterruptedTask); it owns terminal handling for every
			// outcome, so mark finished to skip the deferred epilogue's second
			// deactivation.
			if m.tryContinueInterruptedTask(ctx, id, session, msg, modelOverride, reasoningEffort, pendingAttachments) {
				finished = true
				return
			}
		}

		// No unfinished task took the resume path (or goal was requested): run
		// the normal route → plan → execute flow (or the goal loop).
		session.mu.Lock()
		lastTaskID := session.lastCompletedTaskID
		session.mu.Unlock()

		// Snapshot the anchor's unfinished state BEFORE HandleMessage can
		// reactivate it: the disambiguator shouldRetryContinuationFresh needs
		// (the short-circuit keeps fresh sends off the lookup). See
		// specs/domains/session-lifecycle.md.
		anchorWasUnfinished := lastTaskID != "" && m.unfinishedTaskID(id) == lastTaskID

		// On a continuation (lastTaskID != ""), goal mode runs ON the restored
		// blackboard of the prior completed task: inherited facts and history,
		// a fresh goal derived from the new message.

		var budgetOverride *goalpkg.GoalBudget
		if goalEnabled {
			budgetOverride = parseGoalBudget(goalBudget, m.log())
		}

		// The orchestrator receives the /goal-stripped message when goal mode
		// is on, else the original (preprocessed) message.
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

		incomplete := err != nil && errors.Is(err, orchestration.ErrExecutionIncomplete) && result != nil
		if incomplete {
			m.log().Warn("task completed with incomplete execution", "session_id", id, "task_id", lastTaskID, "error", err)
			err = nil
		}

		// Fallback: a continuation that failed BEFORE executing retries fresh
		// (see shouldRetryContinuationFresh). Goal mode is preserved — hmMsg is
		// already prefix-stripped, msg is not, so a "/goal" send would leak the
		// prefix into the fresh task if the flag were dropped.
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
				// On shutdown leave the task in_progress (resumable after
				// restart); only user-initiated cancels persist as cancelled —
				// goal/E2S runs are abandoned only on a user cancel.
				if m.emitTaskCancelledUnlessShuttingDown(id) {
					action = liveActionDiscard
					m.abandonGoalIfUnfinished(id)
					m.abandonE2SIfUnfinished(id)
					m.persistCancellationIfUnfinished(id)
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

		// Store the task ID for potential continuations
		if pbb, ok := result.Blackboard.(*PersistentBlackboard); ok {
			session.mu.Lock()
			session.lastCompletedTaskID = pbb.TaskID()
			session.mu.Unlock()
		}

		// Safety net: context cancelled but no error returned — still treat as
		// cancellation; never emit partial results as final.
		if ctx.Err() == context.Canceled {
			if m.emitTaskCancelledUnlessShuttingDown(id) {
				action = liveActionDiscard
				m.abandonGoalIfUnfinished(id)
				m.abandonE2SIfUnfinished(id)
				m.persistCancellationIfUnfinished(id)
			}
			return
		}

		// A cooperative pause is a clean checkpoint: session_paused (unlocked
		// input, Resume/Stop), not a failed/resumable banner; the task persists
		// as paused. Deactivate BEFORE the terminal event (deactivateSessionTask).
		if result != nil && result.Status == orchestration.ExecutionStatusPaused {
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
// appending the user's new message as a final user-nudge turn — no routing,
// no new task, no history pair. Returns true when it took the resume path
// (the caller must NOT run the normal HandleMessage flow). This path bypasses
// HandleMessage, so the per-request model/reasoning overrides are applied
// here and pending attachments are flushed into the restored blackboard.
// See specs/domains/session-lifecycle.md.
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

	// Apply per-request model/reasoning overrides — this path bypasses
	// HandleMessage, where step 0 would apply them. No-op when both are empty.
	session.orchestrator.ApplyRequestOverrides(ctx, modelOverride, reasoningEffort)

	// Flush staged attachments into the restored blackboard (mirrors
	// HandleMessage's setupBlackboard continuation path).
	for _, a := range pendingAttachments {
		bb.AddAttachment(a)
	}

	// Append the user message as a final nudge step ({role:user} turn).
	resumeSteps, err := adapter.LoadTrajectory(taskID)
	if err != nil {
		m.log().Warn("continue-interrupted-task: failed to load trajectory; falling back to fresh task", "session", id, "error", err)
		return false
	}
	// Pre-nudge length: empty means the original run never executed a step
	// (see the re-route arming below) — the nudge would otherwise mask that.
	hadTrajectory := len(resumeSteps) > 0
	resumeSteps = append(resumeSteps, agent.Step{UserNudge: message})

	// Persisted routing decision, if any (see the re-route arming below).
	var routing *router.RoutingDecision
	if state, stateErr := adapter.LoadTaskState(taskID); stateErr != nil {
		m.log().Warn("continue-interrupted-task: failed to load task state; resuming without routing", "session", id, "error", stateErr)
	} else if state != nil {
		routing = state.RoutingDecision
	}

	// A task never routed (no persisted decision AND no execution state) is
	// re-classified on this resume, mirroring Manager.ResumeTask. See
	// specs/domains/session-lifecycle.md.
	if routing == nil && !hadTrajectory && bb.GetPlan() == nil {
		session.orchestrator.RequestResumeReroute()
	}

	// A non-terminal goal re-enters the goal loop so the pursuit is preserved;
	// a terminal goal is over by design and is dropped here (ResumeTask
	// surfaces the explicit notice). See specs/domains/goal-mode.md.
	goalState, gerr := adapter.LoadGoalState(taskID)
	if gerr != nil {
		m.log().Warn("continue-interrupted-task: failed to load goal state; continuing as a plain task", "session", id, "error", gerr)
		goalState = nil
	}
	// Model Profiles gate (mirrors ResumeTask): the goal continuation is
	// refused with a visible error before the run starts, leaving the paused
	// row untouched. See specs/domains/goal-mode.md.
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

	// A cooperative pause during resume is a clean checkpoint: session_paused,
	// not a degraded completion. Checked before the task-ID store (a paused
	// result is not completed); deactivate BEFORE the terminal event.
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

	// Deactivate BEFORE task_complete; the follow-up drains here and the
	// caller marks the run finished, so the deferred epilogue does not
	// deactivate a second time and clobber the just-launched follow-up.
	leftover := m.deactivateSessionTask(session, liveActionFollowUp)
	m.emitTaskComplete(id, result, nil)
	m.finishLiveLeftover(ctx, id, session, leftover)
	return true
}

// ResumeTask resumes an unfinished task: nil when none exists or the task
// store is not configured. Invoked by the manual Resume button and on app
// restart for interrupted tasks. modelOverride/reasoningEffort are applied
// like a fresh SendMessage so a pre-resume switch is honored; nudge is
// injected as a one-shot trailing user message (the nudge-resume path).
// See specs/domains/session-lifecycle.md.
func (m *Manager) ResumeTask(ctx context.Context, id, modelOverride, reasoningEffort, nudge string) error {
	// No task may launch once Shutdown has begun (see sendMessage): teardown
	// drains and joins all task goroutines, so a resume racing it would spawn
	// a goroutine nobody waits for.
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

	// Archived sessions are read-only (ErrSessionArchived). The deleting check
	// is the same class: the compaction flow's auto-resume re-enters here
	// after DeleteSession set the flag but before it removed the session —
	// without it the resume would launch a ghost task against a session being
	// torn down.
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

	// Load task state. The routing decision and plan are OPTIONAL for resume
	// (routing is reused if persisted; a plan-less task runs the Conductor's
	// standalone checklist). state may be nil when no state was persisted.
	state, err := adapter.LoadTaskState(taskID)
	if err != nil {
		return fmt.Errorf("failed to load task state: %w", err)
	}

	// Routing (nil → Resume defaults to the general domain) and the original
	// request (may be empty; state carries a fresher copy).
	var routing *router.RoutingDecision
	originalRequest := bb.GetOriginalRequest()
	if state != nil {
		routing = state.RoutingDecision
		if state.OriginalRequest != "" {
			originalRequest = state.OriginalRequest
		}
	}

	// Persisted trajectory — the resumed executor continues from the
	// checkpoint; empty/nil is valid (the Conductor starts a fresh ReAct loop).
	resumeSteps, err := adapter.LoadTrajectory(taskID)
	if err != nil {
		return fmt.Errorf("failed to load trajectory: %w", err)
	}

	// Persisted goal state: a non-terminal goal re-enters the goal loop; nil
	// (non-goal task) resumes plain. See specs/domains/goal-mode.md.
	goalState, err := adapter.LoadGoalState(taskID)
	if err != nil {
		return fmt.Errorf("failed to load goal state: %w", err)
	}

	// A goal that ended TERMINALLY (turn budget exhausted) cannot be re-entered
	// — resuming continues as a REGULAR task, with a visible notice (see
	// specs/domains/goal-mode.md). The terminal state is dropped so Resume
	// takes the plain Conductor path.
	goalModeOver := false
	if goalState != nil && goalState.Status.IsTerminal() {
		goalModeOver = true
		goalState = nil
	}

	// RESEARCH mode from the session's own research root; the fields read are
	// immutable, so no lock is needed.
	researchRoot, isResearch := m.researchProjectInfo(session)

	session.mu.Lock()
	if session.active {
		session.mu.Unlock()
		return errors.New("session is already processing a task")
	}
	// Re-check deleting INSIDE the activation critical section: the snapshot
	// guard above ran before the task-store reads and blackboard restore, and
	// DeleteSession's whole teardown can serialize into that window.
	if session.deleting {
		session.mu.Unlock()
		return fmt.Errorf("session %s is being deleted", id)
	}
	// Manual compaction owns the session until it finishes; resuming in that
	// window would race the history swap. The compaction flow's own
	// auto-resume runs after the flag clears, so it is not blocked here. See
	// specs/domains/memory/compaction.md.
	if session.compacting {
		session.mu.Unlock()
		return ErrSessionCompacting
	}
	// Model Profiles goal guard: a paused non-terminal goal must not resume
	// while the essential-tools narrowing is active. Checked under the session
	// lock BEFORE activation so EVERY resume entry point funneling through
	// ResumeTask is refused without side effects — the row stays "paused" and
	// a later message re-enters the nudge-resume path. See
	// specs/domains/goal-mode.md.
	if goalState != nil && !goalState.Status.IsTerminal() &&
		session.orchestrator != nil && session.orchestrator.ModelProfilesNarrowingEnabled() {
		session.mu.Unlock()
		return core.ErrGoalBlockedByModelProfiles
	}
	// Launching the resumed task consumes any recorded pause owner.
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
	// HandleMessage (step 0 would apply them). After the active-check (never
	// leak into a concurrently running task) but before the Resume goroutine,
	// so the emitter's cached model is synchronized before the first
	// context_fill.
	session.orchestrator.ApplyRequestOverrides(ctx, modelOverride, reasoningEffort)

	m.refreshAutonomyPosture(session)

	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo != nil {
		taskCtx = sdktools.WithEnvInfo(taskCtx, envInfo)
	}

	// Same contract as the sendMessage path (one DB-hitting load per task).
	dirs := m.loadWorkDirectories(session)
	taskCtx = m.injectWorkDirectories(taskCtx, dirs)
	taskCtx = m.injectIgnoreChecker(taskCtx, session, dirs)

	// Resume event so the frontend knows a task is resuming.
	m.emitFunc(Event{
		SessionID: id,
		Type:      "task_resumed",
		Data: MessageReceivedData{
			SessionID: id,
			Text:      originalRequest,
		},
	})
	// session_resumed clears the UI's paused state (complementary to
	// session_paused).
	m.emitFunc(Event{SessionID: id, Type: "session_resumed"})

	// Goal-mode-over notice: the resumed task's goal ended terminally and will
	// NOT be re-armed — the user must know the continuation is a regular run.
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

	// Resolve the persisted task_failed_resumable banner at the committed
	// point, so a failed restore still leaves it actionable.
	m.resolveResumableTaskMessage(id, taskID, "resumed")

	// Flip the persisted task row back to in_progress for the resumed run: it
	// was written 'paused' by the prior run's checkpoint and would otherwise
	// stay 'paused' all run — the paused-ghost bug (the store claims a paused
	// task while it is actively running). A cooperative pause mid-run
	// rewrites 'paused' at the next checkpoint. Best-effort: a failed UPDATE
	// is logged, never fatal.
	if err := adapter.ReactivateTask(taskID); err != nil {
		m.log().Warn("failed to reactivate task row on resume", "session_id", id, "task_id", taskID, "error", err)
	}

	// A task whose original run never got past routing (no persisted routing
	// decision AND no execution state) has nothing to continue — arm the
	// one-shot re-route so the resumed run classifies the original request
	// first; a routed task keeps reuse-without-routing. See
	// specs/domains/session-lifecycle.md.
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
				// the task as cancelled (goal/E2S abandoned only on a user
				// cancel).
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
		// goal-loop path returns nil on cancel) — still treat as cancellation.
		if taskCtx.Err() == context.Canceled {
			if m.emitTaskCancelledUnlessShuttingDown(id) {
				action = liveActionDiscard
				m.abandonGoalIfUnfinished(id)
				m.abandonE2SIfUnfinished(id)
				bb.CancelTask()
			}
			return
		}

		// A cooperative pause during resume is a clean checkpoint:
		// session_paused, not a degraded task_complete; the task stays
		// resumable. Checked before the task-ID store (a paused result is not
		// completed); deactivate BEFORE the terminal event.
		if result != nil && result.Status == orchestration.ExecutionStatusPaused {
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
		// Close the processing window BEFORE task_complete (the frontend's
		// free-for-Resume/Send signal).
		leftover := m.deactivateSessionTask(session, action)
		finished = true
		m.emitTaskComplete(id, result, nil)
		m.finishLiveLeftover(ctx, id, session, leftover)
	}()

	return nil
}

// CancelUnfinishedTask discards any unfinished task by marking it cancelled
// in the task store; afterwards the session has no resumable task. Any
// deferred resume-compaction armed for the task is discarded too. Nil when no
// store or no unfinished task. See specs/domains/session-lifecycle.md.
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
	m.resolveResumableTaskMessage(sessionID, taskID, "cancelled")
	return nil
}

// PauseSession signals the running task (any mode) to pause at the next step
// boundary via the orchestrator's universal pause signal; the request
// persists the task as paused and exits, leaving it resumable. No-op with no
// request in flight or no orchestrator yet. See
// specs/domains/session-lifecycle.md.
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
	// Flag the pausing window under the session lock, BEFORE the pause signal
	// flips (after unlock): until the request epilogue clears it, live sends
	// are rejected with ErrPausePending and a send racing the flip still
	// observes pausing=true. The pause owner is recorded on EVERY call —
	// including idle, where inside the compaction window a pause click means
	// "keep it paused" and the flow's auto-resume must not override it. See
	// specs/domains/session-lifecycle.md.
	session.mu.Lock()
	session.pauseOwner = pauseOwnerUser
	if session.active {
		session.pausing = true
		// Deliberately do NOT record stopRequestedAt: a pause is cooperative
		// and a long LLM call routinely exceeds hungSessionThreshold, so only
		// a Stop (which cancels the context) may flag a session hung. See
		// specs/domains/session-lifecycle.md.
	}
	session.mu.Unlock()
	orch.PauseSession()
	return nil
}

// ResumeSession re-enters the execution loop for a paused (or still-active,
// non-terminal) task via ResumeTask; the optional nudge rides along as the
// one-shot trailing user message. See specs/domains/session-lifecycle.md.
func (m *Manager) ResumeSession(ctx context.Context, sessionID, modelOverride, reasoningEffort, nudge string) error {
	return m.ResumeTask(ctx, sessionID, modelOverride, reasoningEffort, nudge)
}

// hasPausedUnfinishedTask reports whether the resumable unfinished task is
// specifically paused — the nudge-resume router's discriminator between
// resuming and starting fresh. Best-effort: false on lookup error.
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
// session, so the frontend can reconstruct "running / resumable / paused"
// after a restart or session switch. See specs/contracts/desktop-frontend.md.
type SessionRuntimeStatus struct {
	Active            bool   `json:"active"`
	HasUnfinishedTask bool   `json:"has_unfinished_task"`
	UnfinishedTaskID  string `json:"unfinished_task_id,omitempty"`
	// UnfinishedTaskStatus is the RAW persisted status of the resumable task
	// ("in_progress", "paused", "failed"), or "" when none — the frontend
	// seeds its live overlay with the exact value instead of collapsing every
	// non-paused unfinished task to "failed". Verbatim; consult only while
	// Active is false.
	UnfinishedTaskStatus string `json:"unfinished_task_status,omitempty"`
	// Paused is true when the resumable unfinished task is paused — a
	// cooperative checkpoint the user can resume or send into.
	Paused bool `json:"paused"`
	// Compacting is true while a manual context compaction is in flight.
	Compacting bool `json:"compacting"`
	// CompactionAvailability is the per-strategy manual-compaction prediction
	// for the session's current history. Fail-open: empty whenever the
	// session/orchestrator is not in memory — a status poll must never guess
	// "disabled". omitempty keeps the nil slice OFF the wire.
	CompactionAvailability []core.CompactionAvailability `json:"compaction_availability,omitempty"`
	// Activity is the session's last user-facing activity label tracked by
	// the emitter; authoritative only while Active is true.
	Activity string `json:"activity,omitempty"`
	// Streaming is true while an assistant stream is open; when false the
	// frontend clears stale streaming text (a background-ended stream would
	// otherwise render a frozen partial answer forever).
	Streaming bool `json:"streaming"`
	// WorkUnits is the durable work-unit snapshot for the resumable task
	// (workUnitSnapshot), for chat-block reconciliation after a
	// restart/session load.
	WorkUnits []WorkUnitStatus `json:"work_units,omitempty"`
}

// WorkUnitStatus is one durable execution unit as reported to the frontend
// for chat-block reconciliation; StepID matches the subagent_launch /
// plan_step_start chat events so blocks align by id.
type WorkUnitStatus struct {
	// StepID is the unit's id — for a delegation or plan step, the step id.
	StepID string `json:"step_id"`
	// Kind classifies the unit; advisory metadata for the reader.
	Kind string `json:"kind,omitempty"`
	// Status is the effective lifecycle state.
	Status string `json:"status"`
	// ParentID is the unit's parent (empty for a top-level unit).
	ParentID string `json:"parent_id,omitempty"`
}

// GetSessionRuntimeStatus reports the session's live (in-memory) and
// persisted execution state for restart/session-switch reconciliation. It
// never RESTORES a session and never mutates session state, but it is not
// side-effect-free: it settles the task's abandoned in-flight units as
// `interrupted` (workUnitSnapshot). See specs/contracts/desktop-frontend.md.
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
		// Live activity/streaming from the emitter; reading the pointers
		// under sess.mu keeps them stable (the orchestrator's no-op predicate
		// runs outside the lock).
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
				status.UnfinishedTaskStatus = state.Status
				// Invariant: {Active:true, Paused:true} must not exist —
				// pausing deactivates the session until session_paused lands,
				// so a "paused" status under a running task is a stale
				// snapshot and must not surface Paused=true.
				status.Paused = state.Status == "paused" && !status.Active
			} else if stateErr != nil {
				m.log().Warn("get session runtime status: failed to load task state", "session", sessionID, "error", stateErr)
			}
		}
	}

	// Work-unit snapshot folded into the same status round-trip — no extra
	// RPC to reconcile chat blocks. See specs/decisions/048-unified-recovery-ledger.md.
	if status.HasUnfinishedTask && status.UnfinishedTaskID != "" {
		status.WorkUnits = m.workUnitSnapshot(sessionID, status.UnfinishedTaskID, status.Active, status.Paused)
	}

	return status, nil
}

// workUnitSnapshot returns the durable work-unit ledger for a task as the
// frontend-facing reconciliation snapshot: units abandoned in flight by a
// crash/app exit settle as interrupted, paused tasks and paused units settle
// nothing (they are Resume's checkpoint), and container units are excluded.
// See specs/decisions/048-unified-recovery-ledger.md.
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
			// Not execution work — see specs/decisions/048-unified-recovery-ledger.md.
			continue
		}
		status := rec.Status
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
// before ActiveSessions reports the session as hung — past it the goroutine
// is genuinely wedged and the quit modal may honestly say "not responding".
// A cooperative pause is deliberately excluded (a long LLM call can
// legitimately delay it past this). See specs/domains/session-lifecycle.md.
const hungSessionThreshold = 10 * time.Second

// ActiveSessionInfo identifies a session with live background work, for the
// desktop close-confirmation guard.
type ActiveSessionInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Compacting: the live work is an in-flight manual compaction, not a task.
	Compacting bool `json:"compacting"`
	// Hung: a STOP was issued but the task goroutine has not answered within
	// hungSessionThreshold, so the quit modal can flag a wedged session. A
	// cooperative pause never sets it.
	Hung bool `json:"hung"`
}

// ActiveSessions returns the in-memory sessions with live background work (a
// running task or an in-flight manual compaction) for the desktop close
// guard — this work dies with the process, unlike persisted resumable tasks.
// Memory-only (see sessionByID).
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

// LiveTokenSnapshot returns the in-memory token/fill state — the values the
// emitter broadcasts — so GetSessionTokens can serve live used/max for a
// mid-task session. Memory-only (see sessionByID); ok=false when not in
// memory (caller falls back to the store).
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

// ValidateLiveSend runs the live-send gate checks WITHOUT queuing, before the
// frontend persists the user message — a rejected live send never leaves a
// phantom persisted row. The authoritative re-check happens under the session
// lock in sendMessage; a message passing here that finds the task finished
// simply starts a normal task. See specs/domains/session-lifecycle.md.
func (m *Manager) ValidateLiveSend(sessionID string, goal, e2s bool, text string, activeSkills, activeAgents, activeMCPServers []string) error {
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess == nil {
		return nil
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	// A compacting session rejects sends even when no task runs: the flow
	// owns the idle window between the pause landing and the auto-resume.
	if sess.compacting {
		return ErrSessionCompacting
	}
	if !sess.active {
		return nil
	}
	return liveSendRejectionLocked(sess, goal, e2s, text, activeSkills, activeAgents, activeMCPServers)
}

// emitTaskCancelledUnlessShuttingDown emits task_cancelled unless the manager
// is shutting down (then the task stays in_progress/resumable and the caller
// skips persistence). Also the single gate for the run's terminal emission:
// once forceTerminateStuckTask claimed the run, a settling goroutine finds
// the claim taken and skips its own event + metrics — it still returns true
// so the caller's idempotent cleanup runs. See
// specs/domains/session-lifecycle.md.
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

// sessionByID returns the live in-memory session for id WITHOUT any lazy
// restore. It is the funnel for read paths that must never resurrect a
// session as a side effect: a store-only session restored here would rebuild
// an orchestrator and reopen log/dump handles nobody closes (a ghost for a
// session the user merely viewed, or one DeleteSession just removed).
// See specs/domains/session-lifecycle.md (§ Read-Path Restore Policy).
func (m *Manager) sessionByID(id string) (*Session, bool) {
	m.mu.RLock()
	s := m.sessions[id]
	m.mu.RUnlock()
	return s, s != nil
}

// claimTaskTerminal atomically claims the run's single terminal emission:
// true for the first caller, false when already claimed. See
// specs/domains/session-lifecycle.md.
func (m *Manager) claimTaskTerminal(s *Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminalEmitted {
		return false
	}
	s.terminalEmitted = true
	return true
}

// claimTaskTerminalForceTerminated claims the run's single terminal emission
// AND marks the run force-terminated under one lock. The combined write is
// what makes the force-terminate path race-free: deactivateSessionTask reads
// forceTerminated under s.mu, so it must never see the claim taken while
// forceTerminated is still false (it would discard a live message queued
// after the cancel was reported).
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
// profile) for the run that just finished. The counters reset afterwards, so
// one agent_metrics event covers exactly one run. Memory-only (see
// sessionByID).
func (m *Manager) emitAgentMetrics(sessionID, finish string) {
	s, ok := m.sessionByID(sessionID)
	if !ok || s == nil || s.emitter == nil {
		return
	}
	s.emitter.EmitAgentMetrics(finish)
}

// ExecutionWindow returns the host-side recent-execution window (bounded
// trajectory + run counters + plan progress) backing the silent-mode
// step-limit judge. Memory-only (see sessionByID).
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
// — the graceful-shutdown counterpart of persistCancellationIfUnfinished: the
// task survives restart clearly resumable (SessionRuntimeStatus reports
// Paused=true). Only in_progress is paused. Best-effort: errors logged only.
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
// the goal loop; on shutdown the goal stays non-terminal and survives
// restart. Must run BEFORE persistCancellationIfUnfinished/bb.CancelTask: a
// cancelled task is no longer "unfinished" and the lookup would miss it.
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
// cancelled on a user-initiated cancel — the E2S mirror of
// abandonGoalIfUnfinished (same shutdown/ordering contract).
func (m *Manager) abandonE2SIfUnfinished(sessionID string) {
	tid := m.unfinishedTaskID(sessionID)
	if tid == "" {
		return
	}
	m.terminalizeE2SState(tid)
}

// terminalizeE2SState marks the task's persisted E2S state as cancelled
// (terminal). No-op without a store, persisted state, or when already
// terminal; best-effort.
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
// task. See specs/domains/session-lifecycle.md.
func (m *Manager) abandonUnfinishedTaskForGoal(id string) {
	m.abandonUnfinishedTaskForMode(id, "abandoned_for_goal", "Interrupted task abandoned to start goal mode.")
}

// abandonUnfinishedTaskForE2S cancels the unfinished task so an E2S request
// starts a fresh run. Unlike the goal takeover it is not stateless: runE2SLoop
// resumes any non-terminal persisted Σ under the continuation anchor, and
// ResumeState takes precedence over the new message — so the abandoned task's
// Σ is terminalized and the anchor dropped when it points at that task.
// See specs/domains/e2s.md (Invariants: mode-takeover Σ terminalization).
func (m *Manager) abandonUnfinishedTaskForE2S(id string) {
	tid := m.abandonUnfinishedTaskForMode(id, "abandoned_for_e2s", "Interrupted task abandoned to start E2S mode.")
	if tid == "" {
		return
	}
	// The anchor is restored status-agnostically on session restore, so after
	// a restart it can equal the interrupted task — leaving it would make the
	// send restore the abandoned blackboard instead of starting fresh.
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
// abandon-on-takeover helpers: drops any armed deferred resume-compaction,
// persists the cancellation, resolves the lingering banner, emits the service
// notice. Returns the abandoned task's ID ("" when none) for the E2S
// takeover's anchor cleanup.
func (m *Manager) abandonUnfinishedTaskForMode(id, bannerReason, serviceContent string) string {
	// Drop any armed deferred resume-compaction so the new mode's loop does
	// not inherit the forced compaction chosen for the abandoned task. See
	// specs/domains/memory/compaction.md.
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
	// Terminalize any persisted E2S Σ too, or a later resume resolving the
	// anchor could re-enter the loop with the abandoned run's state.
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
// (HandleMessage with a TaskID) should retry fresh. anchorWasUnfinished — the
// caller's PRE-HandleMessage snapshot — disambiguates the two ways an
// unfinished lookup can return the anchor after a failure: a reactivated
// anchor whose own execution failed mid-flight must NOT retry fresh (the
// fresh task would orphan it), while an anchor already unfinished when the
// send began must (the banner would dead-end on the same restore error). See
// specs/domains/session-lifecycle.md.
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
// has an unfinished task; returns true when emitted, so callers that KNOW the
// execution was degraded can surface a fallback warning when the safety net
// is unavailable. reason is prepended to the banner ("" = generic); a
// non-nil cause is classified for the automatic-retry deadline. See
// specs/decisions/065-auto-resend-retryable-errors.md.
//
// Emits ONLY the banner: agent_metrics resets on every emission, so exactly
// one must fire per terminal path — emitting here would double-count on the
// emitTaskComplete path and zero the real per-run report.
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
// resume banner from a terminal execution error: trimmed to the first line,
// capitalized, capped for readability. "" when err is nil.
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
	// Bounded like every direct store call on the send/terminal paths
	// (restoreDBReadTimeout): the app's single SQLite pool — an unbounded
	// wait under a write storm would hang the settle path.
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
// contract and guarantees degraded-outcome surfacing: task_failed_resumable
// or a visible service warning — never a silent visual success. See
// specs/domains/session-lifecycle.md.
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
	// string but finished nothing, so they never trigger.
	if promptSuggestOutcomeTriggers(result) {
		m.maybeSpawnPromptSuggestions(sessionID, result)
	}

	// A successful completion is final — never offer a resume: warn if the
	// just-completed row still reads unfinished (warnIfCompletionDidNotPersist)
	// and cancel stale orphan rows (sweepStaleUnfinishedTasks). See
	// specs/domains/session-lifecycle.md.
	if success {
		m.warnIfCompletionDidNotPersist(sessionID, result)
		m.sweepStaleUnfinishedTasks(sessionID, result)
		return
	}

	// The typed degraded-completion cause (ADR-065 transport b): a run that
	// collapsed its error into the output carries it on HandleResult.Err so
	// the auto-retry classifier still sees the *llm.Error chain. See
	// specs/decisions/065-auto-resend-retryable-errors.md.
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

// warnIfCompletionDidNotPersist warns (service event) when the task that just
// completed successfully is still marked unfinished — the completion write
// raced or was dropped; the result above is valid and no resume banner is
// offered. A different (stale) unfinished row is ignored.
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

// sweepStaleUnfinishedTasks cancels leftover unfinished rows after a
// successful completion and resolves their persisted resumable banners: any
// unfinished row other than the just-completed task is by construction the
// orphan of an abandoned continuation, and left in place it keeps
// has_unfinished_task=true forever. Best-effort: errors logged only. See
// specs/domains/session-lifecycle.md.
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
	// Without a persistable blackboard the sweep cannot tell the
	// just-completed (possibly racing) row apart from a stale orphan — cancel
	// nothing rather than risk discarding the live row.
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
		// The lookup returns one row at a time, so an older orphan behind the
		// just-completed task's own row stays for the NEXT successful
		// completion — best-effort by design.
		if taskID == "" || taskID == completedTaskID {
			return
		}
		if err := adapter.PersistCancellation(taskID); err != nil {
			m.log().Warn("stale-task sweep: failed to cancel orphaned task", "session", sessionID, "task", taskID, "error", err)
			return
		}
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
		// Nothing running, but a paused/resumable task is still cancellable
		// via Stop; ErrNoActiveTask distinguishes "nothing running" from a
		// successful cancellation.
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
			// completed teardown must win over a spurious "forcing terminal
			// state" log.
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
// ignored its cancellation for the whole stopTimeout: the cancellation is
// forced onto the durable state and the wire (task flipped to cancelled,
// terminal task_cancelled + metrics emitted here), or the UI would wait on
// "Generating response…" forever. The session is deliberately NOT
// deactivated — the goroutine really is still running, so the close guard
// flags it hung. Shutdown wins, and the emission is claimed once per run.
// See specs/domains/session-lifecycle.md.
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
			// cancellation: both abandon helpers resolve the task via
			// GetUnfinishedTaskID, so after the flip they would no-op and the
			// next send would silently RESUME the stopped run via the
			// status-agnostic continuation anchor.
			m.abandonGoalIfUnfinished(sessionID)
			m.abandonE2SIfUnfinished(sessionID)
			if err := adapter.PersistCancellation(taskID); err != nil {
				m.log().Warn("stuck-cancel: failed to persist cancellation", "session", sessionID, "task", taskID, "error", err)
			}
			m.resolveResumableTaskMessage(sessionID, taskID, "cancelled")
		}
	}

	// Emit the terminal event: this call owns the run's emission, and the stuck
	// goroutine (which owns the normal one) will never reach its own.
	m.emitTaskCancelled(sessionID)
}

// cancelUnfinishedTask discards the session's unfinished (e.g. cooperatively
// paused) task and emits task_cancelled — the non-active counterpart of
// CancelTask: the goroutine has already exited, so the persisted task flips
// to cancelled and the terminal event is emitted directly. Any deferred
// resume-compaction armed for the task is discarded too. ErrNoActiveTask
// when there is nothing to cancel. See specs/domains/session-lifecycle.md.
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
// ID from the database (nil, nil when none). Memory-only (see sessionByID):
// the fallback queries the same GetLatestTaskID the restore anchors from, so
// skipping the restore produces byte-identical output with no side effects.
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

// maybeSpawnTitleGeneration auto-names a session from its first message: a
// tracked background goroutine derived from the manager's shutdown context
// (it writes through the session dump file, so Shutdown must be able to abort
// it and join before closing that handle). Only a session still carrying its
// generated placeholder name is renamed, never a presented relaunch. The
// readiness gate runs BEFORE the service timeout is armed. See
// specs/domains/embedded-llm.md.
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
	titleSpawned := m.spawnBackgroundNamed("session-title-generation session="+id, func() {
		if dumpFile != nil {
			defer func() { _ = dumpFile.Close() }()
		}
		// Readiness BEFORE the budget: the embedded model's cold load is longer
		// than the service timeout, so arming the timeout first would spend it
		// on the load and lose the title. BY DESIGN the gate sits outside the
		// serviceLLMTimeout envelope (ADR-066 D13); nothing user-facing blocks —
		// the only observable effect is a late (or skipped) auto-name. See
		// specs/domains/embedded-llm.md.
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
