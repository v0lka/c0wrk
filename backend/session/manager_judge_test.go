package session

import (
	"context"
	"os"
	"runtime"
	"testing"

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
