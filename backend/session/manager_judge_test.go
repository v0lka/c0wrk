package session

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// workDirsSessionStore wraps the restore mock to serve fixed session-scoped
// work directories from ListSessionWorkDirs.
type workDirsSessionStore struct {
	*mockSessionStoreForRestore
	dirs []project.WorkDirectoryRecord
}

func (s *workDirsSessionStore) ListSessionWorkDirs(_ context.Context, _ string) ([]project.WorkDirectoryRecord, error) {
	return s.dirs, nil
}

// TestJudgeContext_UnknownSession_ReturnsUnchanged verifies the graceful
// degradation path: a session that cannot be resolved leaves the context
// untouched (no workspace, no roots) instead of failing.
func TestJudgeContext_UnknownSession_ReturnsUnchanged(t *testing.T) {
	m, _, _ := testManager(t)

	ctx := m.JudgeContext(context.Background(), "missing-session")

	if got := sdktools.WorkspacePathFrom(ctx); got != "" {
		t.Errorf("WorkspacePathFrom = %q for unknown session; want empty", got)
	}
	if roots := sdktools.SessionRoots(ctx); len(roots) != 0 {
		t.Errorf("SessionRoots = %v for unknown session; want none", roots)
	}
}

// TestJudgeContext_SetsSessionScope verifies that the judge context carries
// the same security scope a live task gets: workspace path, session temp
// directory, EnvInfo, and the auxiliary work directories (explicit +
// implicit temp roots) as allowed roots, plus the prompt-facing directory
// list with descriptions.
func TestJudgeContext_SetsSessionScope(t *testing.T) {
	ws := testWorkspacePath(t)
	auxDir := "/aux/judge-scope"
	if runtime.GOOS == "windows" {
		auxDir = `C:\aux\judge-scope`
	}

	m, _, _ := testManager(t)
	m.SetSessionStore(&workDirsSessionStore{
		mockSessionStoreForRestore: newMockSessionStore(),
		dirs: []project.WorkDirectoryRecord{
			{ID: "wdir-1", Path: auxDir, Description: "shared SDK checkout"},
		},
	})
	m.SetEnvInfo(&sdktools.EnvInfo{OS: "TestOS", Arch: "testarch"})

	info, err := m.CreateSession(testProjectID, ws)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	sess, ok := m.GetSession(info.ID)
	if !ok {
		t.Fatal("GetSession failed after CreateSession")
	}

	ctx := m.JudgeContext(context.Background(), info.ID)

	if got := sdktools.WorkspacePathFrom(ctx); got != ws {
		t.Errorf("WorkspacePathFrom = %q; want %q", got, ws)
	}
	if got := sdktools.TempDirFrom(ctx); got != sess.TempDir {
		t.Errorf("TempDirFrom = %q; want %q", got, sess.TempDir)
	}
	if info := sdktools.EnvInfoFrom(ctx); info == nil || info.OS != "TestOS" {
		t.Errorf("EnvInfoFrom = %+v; want OS TestOS", info)
	}

	roots := sdktools.SessionRoots(ctx)
	if !containsRoot(roots, ws) {
		t.Errorf("session roots %v missing workspace %q", roots, ws)
	}
	if !containsRoot(roots, auxDir) {
		t.Errorf("session roots %v missing aux work dir %q", roots, auxDir)
	}
	for _, want := range []string{"/tmp", os.TempDir()} {
		if runtime.GOOS == "windows" && want == "/tmp" {
			continue
		}
		if !containsRoot(roots, want) {
			t.Errorf("session roots %v missing implicit temp root %q", roots, want)
		}
	}

	dirs := core.WorkDirectoriesFrom(ctx)
	if len(dirs) != 1 || dirs[0].Path != auxDir || dirs[0].Description != "shared SDK checkout" {
		t.Errorf("WorkDirectoriesFrom = %+v; want the aux dir with description", dirs)
	}
}

// TestEmitJudgePhase_RoutesThroughSessionEmitter verifies the strict-judge
// (Smart Approve) phase events flow through the live session's emitter — so
// they reach the UI AND the activity tracker that backs the runtime-status
// snapshot read on session switches.
func TestEmitJudgePhase_RoutesThroughSessionEmitter(t *testing.T) {
	manager, _, _ := testManager(t)

	var captured []Event
	emitter := NewEventEmitter("sess-judge", func(e Event) { captured = append(captured, e) })
	manager.mu.Lock()
	manager.sessions["sess-judge"] = &Session{ID: "sess-judge", emitter: emitter}
	manager.mu.Unlock()

	manager.EmitJudgePhase("sess-judge", true, "bash_exec")
	manager.EmitJudgePhase("sess-judge", false, "bash_exec")

	if len(captured) != 2 {
		t.Fatalf("expected 2 emitted events, got %d: %+v", len(captured), captured)
	}
	if captured[0].Type != "tool_judge_started" || captured[1].Type != "tool_judge_finished" {
		t.Errorf("event types = [%s %s], want [tool_judge_started tool_judge_finished]",
			captured[0].Type, captured[1].Type)
	}
	for _, evt := range captured {
		if tool, _ := evt.Data.(map[string]any)["tool"].(string); tool != "bash_exec" {
			t.Errorf("event %s tool = %q, want bash_exec", evt.Type, tool)
		}
	}
	// The emitter's activity tracker advanced through both phases.
	if got := emitter.LastActivity(); got != "Running tool: bash_exec..." {
		t.Errorf("LastActivity after finished phase = %q, want %q", got, "Running tool: bash_exec...")
	}
}

// TestEmitJudgePhase_FallsBackToRawPipeline verifies a session without a live
// emitter still emits through the raw pipeline (UI delivery without activity
// tracking) instead of silently dropping the phase event.
func TestEmitJudgePhase_FallsBackToRawPipeline(t *testing.T) {
	manager, eventChan, _ := testManager(t)

	manager.EmitJudgePhase("sess-unknown", true, "bash_exec")

	select {
	case evt := <-eventChan:
		if evt.Type != "tool_judge_started" {
			t.Errorf("fallback event type = %q, want tool_judge_started", evt.Type)
		}
		if evt.SessionID != "sess-unknown" {
			t.Errorf("fallback event session = %q, want sess-unknown", evt.SessionID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected the fallback event on the raw emit pipeline")
	}
}
