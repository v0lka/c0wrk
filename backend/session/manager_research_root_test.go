package session

import (
	"io"
	"log/slog"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/orchestration"
)

// TestResearchProjectInfo_UsesSessionWorkspace pins the session-root
// contract for the execution-time RESEARCH root (ADR-080 workspace-coherence
// step): the root is derived from the SESSION's execution workspace, never
// from the project's registered checkout — a local session gets
// <checkout>/.research, a managed-worktree session its own
// <tree>/.research, so concurrently running sessions of one project never
// share (or overwrite) each other's research state. CHAT sessions and
// workspace-less sessions stay research-off.
func TestResearchProjectInfo_UsesSessionWorkspace(t *testing.T) {
	m := NewManager(
		func(core.Emitter, *slog.Logger, string, core.BlackboardFactory, io.Writer, *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
			return nil, nil
		},
		func(Event) {},
		t.TempDir(),
	)

	local := &Session{ProjectID: "p1", WorkspacePath: "/repo"}
	got, ok := m.researchProjectInfo(local)
	if !ok || got != config.ProjectResearchPath("/repo") {
		t.Fatalf("local session: got (%q, %v), want %q", got, ok, config.ProjectResearchPath("/repo"))
	}

	managed := &Session{ProjectID: "p1", WorkspacePath: "/repo/.worktrees/s-1"}
	got, ok = m.researchProjectInfo(managed)
	wantTree := config.ProjectResearchPath("/repo/.worktrees/s-1")
	if !ok || got != wantTree {
		t.Fatalf("managed session: got (%q, %v), want the session tree root %q", got, ok, wantTree)
	}
	if got == config.ProjectResearchPath("/repo") {
		t.Fatal("managed session must not inherit the checkout's research root")
	}

	chat := &Session{ProjectID: project.NoProjectID, WorkspacePath: "/somewhere"}
	if _, ok := m.researchProjectInfo(chat); ok {
		t.Fatal("CHAT session must be research-off")
	}
	if _, ok := m.researchProjectInfo(&Session{ProjectID: "p1"}); ok {
		t.Fatal("workspace-less session must be research-off (defensive)")
	}
	if _, ok := m.researchProjectInfo(nil); ok {
		t.Fatal("nil session must be research-off (defensive)")
	}
}
