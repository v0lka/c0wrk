package backend

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/review"
	"github.com/v0lka/c0wrk/backend/session"
)

// newForkTestAPI builds a minimal FrontendAPI with real session/review stores
// and a stub manager (orchestrator factory is never invoked by ForkSession).
// The shared db owns a project + session row so FK constraints hold.
func newForkTestAPI(t *testing.T) (api *FrontendAPI, sessionStore *session.SQLiteSessionStore, reviewStore *review.SQLiteReviewStore, db interface{ Close() error }) {
	t.Helper()
	ctx := context.Background()

	dbConn := openProjectSwitchTestDB(t)

	projectStore, err := project.NewSQLiteProjectStore(dbConn)
	if err != nil {
		_ = dbConn.Close()
		t.Fatalf("project store: %v", err)
	}
	sessionStore, err = session.NewSQLiteSessionStore(dbConn)
	if err != nil {
		_ = dbConn.Close()
		t.Fatalf("session store: %v", err)
	}
	reviewStore, err = review.NewSQLiteReviewStore(dbConn)
	if err != nil {
		_ = dbConn.Close()
		t.Fatalf("review store: %v", err)
	}

	agentDir := t.TempDir()
	projectManager := project.NewManager(projectStore, agentDir, nil)
	createdProject, err := projectManager.CreateProject("Fork Project", "")
	if err != nil {
		_ = dbConn.Close()
		t.Fatalf("create project: %v", err)
	}

	manager := session.NewManager(nil, func(session.Event) {}, agentDir)
	manager.SetSessionStore(sessionStore)
	manager.SetProjectResolver(func(projectID string) (string, error) {
		return createdProject.WorkspacePath, nil
	})

	api = &FrontendAPI{
		app:         &Application{manager: manager},
		store:       sessionStore,
		reviewStore: reviewStore,
	}

	// Seed a source session.
	if err := sessionStore.SaveSession(ctx, session.SessionInfo{
		ID: "fork-src", ProjectID: createdProject.ID, Name: "Source Session",
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		_ = dbConn.Close()
		t.Fatalf("save session: %v", err)
	}

	return api, sessionStore, reviewStore, dbConn
}

// mustListAPITaskIDs returns the task ids for a session, failing the test on error.
func mustListAPITaskIDs(ctx context.Context, t *testing.T, store *session.SQLiteSessionStore, sessionID string) []string {
	t.Helper()
	// Use GetLatestTaskID to confirm a task exists in the fork (no direct db
	// access across packages).
	latest, err := store.GetLatestTaskID(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetLatestTaskID: %v", err)
	}
	if latest == "" {
		return nil
	}
	return []string{latest}
}

func TestForkSessionRPC_GuardBlocksUnfinishedTask(t *testing.T) {
	api, sessionStore, _, db := newForkTestAPI(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	// Seed an in-progress task → fork must be rejected.
	if err := sessionStore.SaveTask(ctx, session.TaskRecord{
		ID: "task-run", SessionID: "fork-src", OriginalRequest: "running",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}

	fork, err := api.ForkSession("fork-src")
	if err == nil {
		t.Fatal("expected error when forking session with unfinished task")
	}
	if !strings.Contains(err.Error(), "unfinished task") {
		t.Errorf("unexpected error message: %v", err)
	}
	if fork != nil {
		t.Error("expected nil fork on guard failure")
	}
}

func TestForkSessionRPC_SuccessClonesSessionAndReview(t *testing.T) {
	api, sessionStore, reviewStore, db := newForkTestAPI(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	// Completed task + a message + a review comment.
	if err := sessionStore.SaveTask(ctx, session.TaskRecord{
		ID: "task-done", SessionID: "fork-src", OriginalRequest: "done",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "completed", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	if err := sessionStore.SaveMessage(ctx, session.ChatMessage{
		SessionID: "fork-src", Role: "user", Content: "hi", Metadata: json.RawMessage(`{}`),
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	if err := reviewStore.UpsertGeneralComment(ctx, "fork-src", "review note"); err != nil {
		t.Fatalf("UpsertGeneralComment: %v", err)
	}

	fork, err := api.ForkSession("fork-src")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if fork == nil || fork.ID == "fork-src" {
		t.Fatalf("invalid fork: %+v", fork)
	}
	if fork.Name != "Source Session (fork 1)" {
		t.Errorf("fork name=%q", fork.Name)
	}

	// Review was cloned (best-effort).
	rev, err := reviewStore.GetReview(ctx, fork.ID)
	if err != nil {
		t.Fatalf("GetReview fork: %v", err)
	}
	if rev.GeneralComment != "review note" {
		t.Errorf("review not cloned: general=%q", rev.GeneralComment)
	}

	// Message + task copied.
	msgs, _ := sessionStore.LoadMessages(ctx, fork.ID)
	if len(msgs) != 1 || msgs[0].Content != "hi" {
		t.Errorf("messages not copied: %+v", msgs)
	}
	tasks := mustListAPITaskIDs(ctx, t, sessionStore, fork.ID)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task in fork, got %d", len(tasks))
	}
}

// TestListAllSessions_ReturnsSessionsAcrossProjects verifies the RPC returns
// sessions of MULTIPLE projects in a single list, ordered by effective
// activity (newest first), regardless of the active project.
func TestListAllSessions_ReturnsSessionsAcrossProjects(t *testing.T) {
	ctx := context.Background()
	dbConn := openProjectSwitchTestDB(t)
	defer func() { _ = dbConn.Close() }()

	projectStore, err := project.NewSQLiteProjectStore(dbConn)
	if err != nil {
		t.Fatalf("project store: %v", err)
	}
	sessionStore, err := session.NewSQLiteSessionStore(dbConn)
	if err != nil {
		t.Fatalf("session store: %v", err)
	}

	agentDir := t.TempDir()
	projectManager := project.NewManager(projectStore, agentDir, nil)
	projA, err := projectManager.CreateProject("Project A", "")
	if err != nil {
		t.Fatalf("create project A: %v", err)
	}
	projB, err := projectManager.CreateProject("Project B", "")
	if err != nil {
		t.Fatalf("create project B: %v", err)
	}

	manager := session.NewManager(nil, func(session.Event) {}, agentDir)
	manager.SetSessionStore(sessionStore)

	api := &FrontendAPI{app: &Application{manager: manager}}

	// Seed one session per project with distinct activity; A is newer.
	if err := sessionStore.SaveSession(ctx, session.SessionInfo{
		ID: "b-old", ProjectID: projB.ID, Name: "B old",
		CreatedAt: "2024-01-01T00:00:00Z", LastActiveAt: "2024-01-01T10:00:00Z",
	}); err != nil {
		t.Fatalf("save b-old: %v", err)
	}
	if err := sessionStore.SaveSession(ctx, session.SessionInfo{
		ID: "a-new", ProjectID: projA.ID, Name: "A new",
		CreatedAt: "2024-01-01T00:00:00Z", LastActiveAt: "2024-01-02T10:00:00Z",
	}); err != nil {
		t.Fatalf("save a-new: %v", err)
	}

	got, err := api.ListAllSessions()
	if err != nil {
		t.Fatalf("ListAllSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 sessions across both projects, got %d", len(got))
	}
	if got[0].ID != "a-new" || got[1].ID != "b-old" {
		t.Errorf("expected activity order [a-new b-old], got [%s %s]", got[0].ID, got[1].ID)
	}
	if got[0].ProjectID != projA.ID || got[1].ProjectID != projB.ID {
		t.Errorf("project IDs mismatch: got %q, %q", got[0].ProjectID, got[1].ProjectID)
	}
}

// TestListAllSessions_NilManagerReturnsEmpty verifies the guard: without an
// initialized manager the RPC returns an empty slice, not an error, so the
// UI can call it unconditionally during startup.
func TestListAllSessions_NilManagerReturnsEmpty(t *testing.T) {
	f := &FrontendAPI{} // f.app == nil — mirrors early startup
	got, err := f.ListAllSessions()
	if err != nil {
		t.Fatalf("expected no error with nil manager, got %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(got))
	}
}

// TestSendMessage_E2SFailClosedWhenExperimentalDisabled verifies the
// fail-closed experimental gate on the E2S flag: while experimental features
// are disabled (including the nil-config startup state), an E2S send is
// rejected BEFORE any side effect — no message is persisted and no task is
// started.
func TestSendMessage_E2SFailClosedWhenExperimentalDisabled(t *testing.T) {
	api, sessionStore, _, db := newForkTestAPI(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	// The harness carries no runtime config → experimentalFeaturesEnabled()
	// reports false (fail-closed).
	err := api.SendMessage("fork-src", "do the thing", nil, nil, nil, "", "", false, "", true, false)
	if err == nil {
		t.Fatal("expected an error sending an E2S message while experimental features are disabled")
	}
	if !strings.Contains(err.Error(), "experimental") {
		t.Errorf("expected an experimental-gate rejection, got: %v", err)
	}

	// Fail-closed = no side effects: no user message persisted, no task row.
	msgs, mErr := sessionStore.LoadMessages(ctx, "fork-src")
	if mErr != nil {
		t.Fatalf("LoadMessages: %v", mErr)
	}
	if len(msgs) != 0 {
		t.Errorf("gated send must not persist a message, got %d", len(msgs))
	}
	if latest, lErr := sessionStore.GetLatestTaskID(ctx, "fork-src"); lErr != nil || latest != "" {
		t.Errorf("gated send must not start a task (latest=%q, err=%v)", latest, lErr)
	}
}

// TestSendMessage_E2SGatePassesWhenExperimentalEnabled verifies the gate
// opens only with the experimental switch: with the config present and
// Experimental.Enabled=true, an E2S send passes the gate and proceeds into
// the send pipeline (it fails later in this harness — on session restore
// with a nil orchestrator factory — which proves the gate itself did not
// reject it).
func TestSendMessage_E2SGatePassesWhenExperimentalEnabled(t *testing.T) {
	api, _, _, db := newForkTestAPI(t)
	defer func() { _ = db.Close() }()

	// Drop the app so the send — after passing the gate — stops at the
	// manager-initialized guard. Any error OTHER than the gate/exclusivity
	// rejections proves the gate itself was open.
	api.app = nil

	api.configMu.Lock()
	api.config = &config.Config{}
	api.config.Experimental.Enabled = true
	api.configMu.Unlock()

	err := api.SendMessage("fork-src", "do the thing", nil, nil, nil, "", "", false, "", true, false)
	if err == nil {
		t.Fatal("expected the send to stop at the manager-initialized guard, not succeed")
	}
	if strings.Contains(err.Error(), "experimental") || strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("gate must be open with experimental features enabled, got gate rejection: %v", err)
	}
	if !strings.Contains(err.Error(), "session manager not initialized") {
		t.Errorf("expected the manager-initialized rejection after the open gate, got: %v", err)
	}
}

// TestSendMessage_E2SAndGoalMutuallyExclusive verifies the server-side
// exclusivity defense: arming both mode flags is rejected outright.
func TestSendMessage_E2SAndGoalMutuallyExclusive(t *testing.T) {
	api, _, _, db := newForkTestAPI(t)
	defer func() { _ = db.Close() }()

	// The exclusivity guard runs before the experimental gate, so the test
	// needs no config: both flags set → exclusivity rejection.
	err := api.SendMessage("fork-src", "both modes", nil, nil, nil, "", "", true, "", true, false)
	if err == nil {
		t.Fatal("expected an error when both goal and E2S flags are set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected a mutual-exclusivity rejection, got: %v", err)
	}
}

// TestSendMessage_E2SRejectsGoalCommandPrefix verifies the "/goal" command
// prefix cannot slip past the E2S/goal exclusivity defense: a leading /goal
// arms goal mode inside the manager even without the explicit flag, so it must
// be rejected before any side effect rather than surfacing the core-level
// conflict error (and skipping the E2S takeover cleanup).
func TestSendMessage_E2SRejectsGoalCommandPrefix(t *testing.T) {
	api, sessionStore, _, db := newForkTestAPI(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	// No config needed: the prefix guard runs before the experimental gate.
	err := api.SendMessage("fork-src", "/goal do the thing", nil, nil, nil, "", "", false, "", true, false)
	if err == nil {
		t.Fatal("expected an error for an E2S message carrying a /goal command")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected a mutual-exclusivity rejection, got: %v", err)
	}

	// Rejected before any side effect: no message persisted, no task row.
	if msgs, mErr := sessionStore.LoadMessages(ctx, "fork-src"); mErr != nil {
		t.Fatalf("LoadMessages: %v", mErr)
	} else if len(msgs) != 0 {
		t.Errorf("gated send must not persist a message, got %d", len(msgs))
	}
	if latest, lErr := sessionStore.GetLatestTaskID(ctx, "fork-src"); lErr != nil || latest != "" {
		t.Errorf("gated send must not start a task (latest=%q, err=%v)", latest, lErr)
	}
}

// TestSendMessage_E2SRejectsGoalPrefixExposedByPreprocessing pins the
// post-preprocessing guard: PreprocessMessageText strips leading /-mention
// refs (skills and agents), which can EXPOSE a "/goal" prefix hidden behind them — the
// manager arms goal mode from the processed text, so the raw-text guard alone
// misses this form and core would reject the run only after side effects.
func TestSendMessage_E2SRejectsGoalPrefixExposedByPreprocessing(t *testing.T) {
	api, _, _, db := newForkTestAPI(t)
	defer func() { _ = db.Close() }()

	// "/realskill" is a known active skill, so preprocessing strips it and
	// leaves "/goal do x" as the leading command.
	err := api.SendMessage("fork-src", "/realskill /goal refactor the auth module", []string{"realskill"}, nil, nil, "", "", false, "", true, false)
	if err == nil {
		t.Fatal("expected an error: the stripped /skill ref exposes a /goal prefix on an E2S send")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected a mutual-exclusivity rejection, got: %v", err)
	}

	// A non-goal skill message under E2S must be unaffected by the PREFIX
	// guard: it proceeds past the exclusivity check (this harness has no
	// experimental config, so the send stops at the gate — which is exactly
	// the proof wanted: the rejection names the gate, not exclusivity).
	if err := api.SendMessage("fork-src", "/realskill please proceed", []string{"realskill"}, nil, nil, "", "", false, "", true, false); err == nil || strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("a plain skill-ref E2S send must pass the prefix guard, got: %v", err)
	}
}

// ----------------------------------------------------------------------------
// Model Profiles essential-tools narrowing × goal mode
// ----------------------------------------------------------------------------

// modelProfilesNarrowingConfig returns a runtime config with the Model Profiles
// master toggle ON, resolving to the model-agnostic "generic" profile (the
// narrowing-active shape). Model Profiles is not gated by the experimental-features
// switch, so the gate is left unset. id optionally overrides the active profile.
func modelProfilesNarrowingConfig(profileID string) *config.Config {
	if profileID == "" {
		profileID = config.ModelProfilesGenericProfileID
	}
	return &config.Config{
		ModelProfiles: config.ModelProfilesPersistConfig{Enabled: true, ActiveProfile: profileID},
	}
}

// TestSendMessage_GoalBlockedByModelProfiles verifies the frontend-layer guard: a goal
// request — armed by the explicit flag OR a leading /goal command — is refused
// while the Model Profiles essential-tools narrowing is active, BEFORE any side
// effect (no persisted message, no task row).
func TestSendMessage_GoalBlockedByModelProfiles(t *testing.T) {
	api, sessionStore, _, db := newForkTestAPI(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	api.config = modelProfilesNarrowingConfig("")

	cases := []struct {
		name string
		text string
		goal bool
	}{
		{"explicit flag", "do the thing", true},
		{"/goal prefix", "/goal do the thing", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := api.SendMessage("fork-src", tc.text, nil, nil, nil, "", "", tc.goal, "", false, false)
			if err == nil {
				t.Fatal("expected an error for a goal send under the Model Profiles essential-tools profile")
			}
			if !strings.Contains(err.Error(), "Model Profiles") {
				t.Errorf("expected a Model Profiles rejection, got: %v", err)
			}
			// Rejected before any side effect: no message persisted, no task row.
			if msgs, mErr := sessionStore.LoadMessages(ctx, "fork-src"); mErr != nil {
				t.Fatalf("LoadMessages: %v", mErr)
			} else if len(msgs) != 0 {
				t.Errorf("gated send must not persist a message, got %d", len(msgs))
			}
			if latest, lErr := sessionStore.GetLatestTaskID(ctx, "fork-src"); lErr != nil || latest != "" {
				t.Errorf("gated send must not start a task (latest=%q, err=%v)", latest, lErr)
			}
		})
	}
}

// TestModelProfilesGoalBlocked_Combinations pins the guard predicate: only master-on AND
// the active profile's essential-tools variant-on blocks goal mode; a nil config
// never blocks. Model Profiles is not gated by the experimental-features switch,
// so the gate plays no part.
func TestModelProfilesGoalBlocked_Combinations(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{"nil config (fail-open)", nil, false},
		{
			"master off",
			&config.Config{
				ModelProfiles: config.ModelProfilesPersistConfig{Enabled: false, ActiveProfile: config.ModelProfilesGenericProfileID},
			},
			false,
		},
		{"variant off (qwen3.8-27b)", modelProfilesNarrowingConfig("qwen3.8-27b"), false},
		{"narrowing active (generic)", modelProfilesNarrowingConfig(""), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &FrontendAPI{config: tc.cfg}
			if got := api.modelProfilesGoalBlocked(); got != tc.want {
				t.Errorf("modelProfilesGoalBlocked() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestResumeTask_GoalBlockedByModelProfiles verifies the paused-goal resume guard: a
// paused task carrying a NON-terminal goal state is rejected while the
// narrowing is active, while a paused non-goal (or terminal-goal) task is
// unaffected.
func TestResumeTask_GoalBlockedByModelProfiles(t *testing.T) {
	api, sessionStore, _, db := newForkTestAPI(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	api.config = modelProfilesNarrowingConfig("")

	// A paused task with no goal state is NOT a paused goal: the guard must
	// leave it alone (the harness manager has no task store, so a permitted
	// resume returns nil without side effects).
	if err := sessionStore.SaveTask(ctx, session.TaskRecord{
		ID: "task-resume", SessionID: "fork-src", OriginalRequest: "plain paused task",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "paused", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	if err := api.ResumeTask("fork-src", "", ""); err != nil {
		t.Errorf("a paused non-goal task must be unaffected, got: %v", err)
	}

	// Attach an ACTIVE goal state → the same paused task is now a paused goal
	// and must be rejected.
	if err := sessionStore.SaveGoalState(ctx, "task-resume", json.RawMessage(`{"condition":"x","verify_clause":"y","status":"active"}`)); err != nil {
		t.Fatalf("SaveGoalState(active): %v", err)
	}
	if err := api.ResumeTask("fork-src", "", ""); err == nil {
		t.Fatal("expected a paused goal resume to be rejected under the narrowing")
	} else if !strings.Contains(err.Error(), "Model Profiles") {
		t.Errorf("expected a Model Profiles rejection, got: %v", err)
	}

	// A TERMINAL goal state is not a resumable goal (resume runs the plain
	// path), so it is unaffected too.
	if err := sessionStore.SaveGoalState(ctx, "task-resume", json.RawMessage(`{"condition":"x","status":"met"}`)); err != nil {
		t.Fatalf("SaveGoalState(met): %v", err)
	}
	if err := api.ResumeTask("fork-src", "", ""); err != nil {
		t.Errorf("a terminal-goal (plain-path) resume must be unaffected, got: %v", err)
	}
}

// TestModelProfilesGoalBlocked_ConcurrentWithMutation pins the synchronization fix: the
// effective-profile resolve runs under configMu.RLock, so it cannot race the
// setters' in-place config writes. Before the fix, modelProfilesGoalBlocked read cfg.ModelProfiles /
// cfg.Experimental from a detached pointer outside the lock; under
// `go test -race` this test would flag that race. It passes silently when run
// without -race, so the invariant is asserted in CI's race build.
func TestModelProfilesGoalBlocked_ConcurrentWithMutation(t *testing.T) {
	api := &FrontendAPI{config: modelProfilesNarrowingConfig("")}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				// Simulate a setter's in-place mutation under the write lock.
				api.configMu.Lock()
				api.config.Experimental.Enabled = !api.config.Experimental.Enabled
				api.configMu.Unlock()
			}
		}
	}()

	for i := 0; i < 1000; i++ {
		_ = api.modelProfilesGoalBlocked()
	}
	close(stop)
	wg.Wait()
}
