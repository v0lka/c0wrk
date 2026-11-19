package backend

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
)

// --- helpers ---

// newFsopsTestAPI builds a bare FrontendAPI keyed to the given project, the
// same construction the WriteFile admission tests use.
func newFsopsTestAPI(t *testing.T, agentDir, projectID, projectPath string) *FrontendAPI {
	t.Helper()
	f := &FrontendAPI{agentDir: agentDir}
	f.seedPublished.Store(true)
	f.activeProjectMu.Lock()
	f.activeProjectID = projectID
	f.activeProjectPath = projectPath
	f.activeProjectMu.Unlock()
	return f
}

// recordingReveal captures the launcher calls a reveal RPC makes; err, when
// set, is returned to the RPC to exercise failure paths.
type recordingReveal struct {
	mu    sync.Mutex
	calls []revealCall
	err   error
}

type revealCall struct {
	revealDir  string
	selectPath string
}

func (r *recordingReveal) open(revealDir, selectPath string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, revealCall{revealDir: revealDir, selectPath: selectPath})
	return r.err
}

func (r *recordingReveal) last() revealCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return revealCall{}
	}
	return r.calls[len(r.calls)-1]
}

func (r *recordingReveal) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// --- CreateFile ---

func TestCreateFile_Success(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFsopsTestAPI(t, base, "proj-1", ws)

	p := filepath.Join(ws, "missing", "parents", "new.txt")
	if err := f.CreateFile("sess-1", p); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("created path is not a regular file: %v", info.Mode())
	}
}

func TestCreateFile_AlreadyExists(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFsopsTestAPI(t, base, "proj-1", ws)

	p := filepath.Join(ws, "exists.txt")
	if err := os.WriteFile(p, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateFile("sess-1", p); err == nil {
		t.Fatal("creating over an existing file must be rejected")
	}
	data, err := os.ReadFile(p)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing file was disturbed: %v (%q)", err, string(data))
	}
}

func TestCreateFile_ConcurrentWriters_ExclusiveCreate(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFsopsTestAPI(t, base, "proj-1", ws)

	// The exclusive create (O_CREATE|O_EXCL) is the kernel's atomic
	// check-and-create: whichever writer wins lands the single empty file,
	// every loser is refused with "already exists" — no writer can ever open
	// another's file through the truncating write-open. Without O_EXCL every
	// goroutine past the Lstat would succeed and silently truncate.
	p := filepath.Join(ws, "race.txt")
	const writers = 8
	var entered, release sync.WaitGroup
	entered.Add(writers)
	release.Add(1)
	errs := make([]error, writers)
	var done sync.WaitGroup
	done.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer done.Done()
			entered.Done()
			release.Wait() // entered/release barrier — no scheduler-guessing sleeps
			errs[i] = f.CreateFile("sess-1", p)
		}(i)
	}
	entered.Wait()
	release.Done()
	done.Wait()

	created := 0
	for i, err := range errs {
		if err == nil {
			created++
			continue
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("writer %d: unexpected error: %v", i, err)
		}
	}
	if created != 1 {
		t.Fatalf("exactly one writer must create the file, got %d (errs: %v)", created, errs)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("the created file must be empty, got %q", string(data))
	}
}

func TestCreateFile_OutsideWorkspace_Rejected(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFsopsTestAPI(t, base, "proj-1", ws)

	outside := filepath.Join(base, "outside.txt")
	if err := f.CreateFile("sess-1", outside); err == nil {
		t.Fatal("path outside the workspace must be rejected")
	}
	// filepath.Abs cleans .. before containment: the cleaned target lands
	// outside the root and is rejected the same way.
	traversal := filepath.Join(ws, "..", "sibling.txt")
	if err := f.CreateFile("sess-1", traversal); err == nil {
		t.Fatal("traversal out of the workspace must be rejected")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("outside path was created: %v", err)
	}
}

// A symlinked directory component below the admitted root must be refused —
// every component below the root must be real (CheckRealDirsBelow).
func TestCreateFile_RefusesSymlinkedParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on windows")
	}
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(filepath.Join(ws, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFsopsTestAPI(t, base, "proj-1", ws)

	link := filepath.Join(ws, "real", "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateFile("sess-1", filepath.Join(link, "escape.txt")); err == nil {
		t.Fatal("creation through a symlinked parent must be rejected")
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("file escaped through the symlink: %v", err)
	}
}

// --- CreateDirectory ---

func TestCreateDirectory_Success(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFsopsTestAPI(t, base, "proj-1", ws)

	p := filepath.Join(ws, "a", "b", "c")
	if err := f.CreateDirectory("sess-1", p); err != nil {
		t.Fatalf("CreateDirectory: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil || !info.IsDir() {
		t.Fatalf("directory not created: %v", err)
	}
}

func TestCreateDirectory_AlreadyExists(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(filepath.Join(ws, "exists"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFsopsTestAPI(t, base, "proj-1", ws)

	if err := f.CreateDirectory("sess-1", filepath.Join(ws, "exists")); err == nil {
		t.Fatal("creating over an existing directory must be rejected")
	}
}

// --- RenameEntry ---

func TestRenameEntry_FileAndDir(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(filepath.Join(ws, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "old.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFsopsTestAPI(t, base, "proj-1", ws)

	if err := f.RenameEntry("sess-1", filepath.Join(ws, "old.txt"), filepath.Join(ws, "new.txt")); err != nil {
		t.Fatalf("rename file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "new.txt")); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("source still present: %v", err)
	}

	// Moving a directory into a sibling keeps its contents.
	if err := os.WriteFile(filepath.Join(ws, "dir", "inner.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.RenameEntry("sess-1", filepath.Join(ws, "dir"), filepath.Join(ws, "moved")); err != nil {
		t.Fatalf("rename dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "moved", "inner.txt")); err != nil {
		t.Fatalf("directory contents lost in rename: %v", err)
	}
}

func TestRenameEntry_RootRejected(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFsopsTestAPI(t, base, "proj-1", ws)

	if err := f.RenameEntry("sess-1", ws, filepath.Join(ws, "renamed")); err == nil {
		t.Fatal("renaming the workspace root must be rejected")
	}
}

func TestRenameEntry_DestinationExistsOrSourceMissing(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFsopsTestAPI(t, base, "proj-1", ws)

	if err := f.RenameEntry("sess-1", filepath.Join(ws, "a.txt"), filepath.Join(ws, "b.txt")); err == nil {
		t.Fatal("renaming onto an existing destination must be rejected")
	}
	if err := f.RenameEntry("sess-1", filepath.Join(ws, "missing.txt"), filepath.Join(ws, "c.txt")); err == nil {
		t.Fatal("renaming a missing source must be rejected")
	}
}

func TestRenameEntry_SameSourceAndDestination(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFsopsTestAPI(t, base, "proj-1", ws)

	p := filepath.Join(ws, "a.txt")
	if err := f.RenameEntry("sess-1", p, p); err == nil {
		t.Fatal("a no-op rename must be rejected, not silently succeed")
	}
}

// A rename whose endpoints resolve to DIFFERENT admitted roots (checkout vs
// session-infra plans/) is refused: no cross-container moves.
func TestRenameEntry_CrossRootRejected(t *testing.T) {
	base := canonicalTempDir(t)
	checkout := filepath.Join(base, "checkout")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	pid, sid := "proj-1", "sess-1"
	if err := os.WriteFile(filepath.Join(checkout, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFsopsTestAPI(t, base, pid, checkout)

	planDir := config.SessionPlansDir(base, pid, sid)
	if err := f.RenameEntry(sid, filepath.Join(checkout, "a.txt"), filepath.Join(planDir, "b.txt")); err == nil {
		t.Fatal("cross-root rename (workspace → session-infra) must be rejected")
	}
	if _, err := os.Stat(filepath.Join(planDir, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("file moved across roots: %v", err)
	}
}

// --- DeleteEntry ---

func TestDeleteEntry_FileAndTree(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(filepath.Join(ws, "tree", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "plain.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "tree", "nested", "deep.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFsopsTestAPI(t, base, "proj-1", ws)

	if err := f.DeleteEntry("sess-1", filepath.Join(ws, "plain.txt")); err != nil {
		t.Fatalf("delete file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "plain.txt")); !os.IsNotExist(err) {
		t.Fatalf("file still present: %v", err)
	}
	if err := f.DeleteEntry("sess-1", filepath.Join(ws, "tree")); err != nil {
		t.Fatalf("delete tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "tree")); !os.IsNotExist(err) {
		t.Fatalf("tree still present: %v", err)
	}
}

func TestDeleteEntry_RootRejected(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFsopsTestAPI(t, base, "proj-1", ws)

	if err := f.DeleteEntry("sess-1", ws); err == nil {
		t.Fatal("deleting the workspace root must be rejected")
	}
	if _, err := os.Stat(ws); err != nil {
		t.Fatalf("workspace root was removed: %v", err)
	}
}

func TestDeleteEntry_OutsideWorkspace_Rejected(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	outside := filepath.Join(base, "outside.txt")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFsopsTestAPI(t, base, "proj-1", ws)

	if err := f.DeleteEntry("sess-1", outside); err == nil {
		t.Fatal("deleting a path outside the workspace must be rejected")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file was removed: %v", err)
	}
}

// --- No Project isolation (mirrors the WriteFile tests) ---

func TestFsops_NoProject_CrossSessionRejected(t *testing.T) {
	base := canonicalTempDir(t)
	projectDir := config.ProjectDir(base, project.NoProjectID)
	if err := os.MkdirAll(filepath.Join(projectDir, "other-session", "workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "other-session", "workspace", "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFsopsTestAPI(t, base, project.NoProjectID, projectDir)

	foreign := filepath.Join(projectDir, "other-session", "workspace", "x.txt")
	if err := f.CreateFile("my-session", filepath.Join(projectDir, "other-session", "workspace", "y.txt")); err == nil {
		t.Fatal("cross-session create in No Project mode must be rejected")
	}
	if err := f.RenameEntry("my-session", foreign, filepath.Join(projectDir, "other-session", "workspace", "z.txt")); err == nil {
		t.Fatal("cross-session rename in No Project mode must be rejected")
	}
	if err := f.DeleteEntry("my-session", foreign); err == nil {
		t.Fatal("cross-session delete in No Project mode must be rejected")
	}

	own := filepath.Join(projectDir, "my-session", "workspace")
	if err := f.CreateDirectory("my-session", filepath.Join(own, "sub")); err != nil {
		t.Fatalf("own-workspace create rejected: %v", err)
	}
	if err := f.RenameEntry("my-session", filepath.Join(own, "sub"), filepath.Join(own, "sub2")); err != nil {
		t.Fatalf("own-workspace rename rejected: %v", err)
	}
	ownPlan := filepath.Join(projectDir, "my-session", "plans")
	if err := f.CreateFile("my-session", filepath.Join(ownPlan, "plan.md")); err != nil {
		t.Fatalf("own session-infra create rejected: %v", err)
	}
	if err := f.DeleteEntry("my-session", filepath.Join(ownPlan, "plan.md")); err != nil {
		t.Fatalf("own session-infra delete rejected: %v", err)
	}
}

// --- RevealInFileManager ---

func TestRevealInFileManager_UnknownTarget(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &recordingReveal{}
	f := newFsopsTestAPI(t, base, "proj-1", ws)
	f.revealOpener = rec.open

	if err := f.RevealInFileManager("sess-1", "desktop", ""); err == nil {
		t.Fatal("unknown reveal target must be rejected")
	}
	if rec.count() != 0 {
		t.Fatal("launcher must not run for an unknown target")
	}
}

func TestRevealInFileManager_WorkspaceRootAndEntries(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(filepath.Join(ws, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "sub", "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &recordingReveal{}
	f := newFsopsTestAPI(t, base, "proj-1", ws)
	f.revealOpener = rec.open

	// Empty entry: reveal the root itself, nothing to select.
	if err := f.RevealInFileManager("sess-1", RevealTargetWorkspace, ""); err != nil {
		t.Fatalf("reveal root: %v", err)
	}
	if got := rec.last(); got.revealDir != ws || got.selectPath != "" {
		t.Fatalf("root reveal = %+v, want dir %q with no selection", got, ws)
	}

	// The root named as the entry reveals the same way (the parent of the
	// root lies outside the workspace).
	if err := f.RevealInFileManager("sess-1", RevealTargetWorkspace, ws); err != nil {
		t.Fatalf("reveal root-as-entry: %v", err)
	}
	if got := rec.last(); got.revealDir != ws || got.selectPath != "" {
		t.Fatalf("root-as-entry reveal = %+v, want dir %q with no selection", got, ws)
	}

	// A file reveals its parent with the file selected.
	filePath := filepath.Join(ws, "sub", "file.txt")
	if err := f.RevealInFileManager("sess-1", RevealTargetWorkspace, filePath); err != nil {
		t.Fatalf("reveal file: %v", err)
	}
	if got := rec.last(); got.revealDir != filepath.Join(ws, "sub") || got.selectPath != filePath {
		t.Fatalf("file reveal = %+v, want dir %q selecting %q", got, filepath.Join(ws, "sub"), filePath)
	}

	// A directory reveals its parent with the directory selected.
	dirPath := filepath.Join(ws, "sub")
	if err := f.RevealInFileManager("sess-1", RevealTargetWorkspace, dirPath); err != nil {
		t.Fatalf("reveal dir: %v", err)
	}
	if got := rec.last(); got.revealDir != ws || got.selectPath != dirPath {
		t.Fatalf("dir reveal = %+v, want dir %q selecting %q", got, ws, dirPath)
	}
}

func TestRevealInFileManager_OutsideRootAndMissingRejected(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &recordingReveal{}
	f := newFsopsTestAPI(t, base, "proj-1", ws)
	f.revealOpener = rec.open

	outside := filepath.Join(base, "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.RevealInFileManager("sess-1", RevealTargetWorkspace, outside); err == nil {
		t.Fatal("revealing an out-of-workspace entry must be rejected")
	}
	if err := f.RevealInFileManager("sess-1", RevealTargetWorkspace, filepath.Join(ws, "missing.txt")); err == nil {
		t.Fatal("revealing a missing entry must be rejected")
	}
	if err := f.RevealInFileManager("sess-1", RevealTargetWorkspace, filepath.Join(ws, "..", "outside.txt")); err == nil {
		t.Fatal("traversal out of the workspace must be rejected")
	}
	if rec.count() != 0 {
		t.Fatalf("launcher must not run for rejected paths: %+v", rec.calls)
	}
}

// Reveal containment follows the SESSION root: a managed-worktree session's
// own tree is accepted while a checkout file is refused, even though both
// belong to the same project.
func TestRevealInFileManager_ManagedTreeIsTheBoundary(t *testing.T) {
	h := newWTHarness(t, wtFactory())

	managed := h.api.mustManagedSession(t, "", true, "")
	tree := managed.WorkspaceBinding.WorkspacePath
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	inTree := filepath.Join(tree, "in-tree.txt")
	if err := os.WriteFile(inTree, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	inCheckout := filepath.Join(h.project.WorkspacePath, "in-checkout.txt")
	if err := os.WriteFile(inCheckout, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := &recordingReveal{}
	h.api.revealOpener = rec.open

	if err := h.api.RevealInFileManager(managed.ID, RevealTargetWorkspace, inTree); err != nil {
		t.Fatalf("reveal inside the managed tree must be accepted: %v", err)
	}
	if got := rec.last(); got.selectPath != inTree {
		t.Fatalf("reveal = %+v, want selection %q", got, inTree)
	}
	if err := h.api.RevealInFileManager(managed.ID, RevealTargetWorkspace, inCheckout); err == nil {
		t.Fatal("a checkout file must be refused for a managed-tree session")
	}
	if rec.count() != 1 {
		t.Fatalf("launcher ran %d times, want 1", rec.count())
	}
}

// The temp target resolves the session temp dir under the project data dir
// server-side, creating it lazily, and refuses entries outside it.
func TestRevealInFileManager_TempTarget(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &recordingReveal{}
	f := newFsopsTestAPI(t, base, "proj-1", ws)
	f.revealOpener = rec.open

	// A bare FrontendAPI carries no store/manager: the membership lookup is
	// unanswered and falls through — the UI only ever holds active-project
	// sessions.
	if err := f.RevealInFileManager("sess-1", RevealTargetTemp, ""); err != nil {
		t.Fatalf("reveal temp: %v", err)
	}
	tempDir := config.SessionTempDir(base, "proj-1", "sess-1")
	if info, err := os.Stat(tempDir); err != nil || !info.IsDir() {
		t.Fatalf("temp dir not created: %v", err)
	}
	if got := rec.last(); got.revealDir != tempDir || got.selectPath != "" {
		t.Fatalf("temp reveal = %+v, want dir %q with no selection", got, tempDir)
	}

	// An entry inside temp reveals parent+selection.
	inTemp := filepath.Join(tempDir, "scratch.txt")
	if err := os.WriteFile(inTemp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.RevealInFileManager("sess-1", RevealTargetTemp, inTemp); err != nil {
		t.Fatalf("reveal temp entry: %v", err)
	}
	if got := rec.last(); got.revealDir != tempDir || got.selectPath != inTemp {
		t.Fatalf("temp entry reveal = %+v, want dir %q selecting %q", got, tempDir, inTemp)
	}

	// Outside the temp dir: refused.
	if err := f.RevealInFileManager("sess-1", RevealTargetTemp, filepath.Join(ws, "file.txt")); err == nil {
		t.Fatal("revealing a workspace entry under the temp target must be rejected")
	}
	if rec.count() != 2 {
		t.Fatalf("launcher ran %d times, want 2", rec.count())
	}
}

func TestRevealInFileManager_TempRequiresSession(t *testing.T) {
	base := canonicalTempDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &recordingReveal{}
	f := newFsopsTestAPI(t, base, "proj-1", ws)
	f.revealOpener = rec.open

	if err := f.RevealInFileManager("", RevealTargetTemp, ""); err == nil {
		t.Fatal("the temp target requires a session id")
	}
	if rec.count() != 0 {
		t.Fatal("launcher must not run without a session")
	}
}

// A session of a foreign project must not reveal its temp dir while another
// project is active: temp dirs live under the project data tree.
func TestRevealInFileManager_TempForeignSessionRejected(t *testing.T) {
	h := newWTHarness(t, wtFactory())

	sess := h.api.mustManagedSession(t, "", true, "")

	foreignPath := filepath.Join(t.TempDir(), "other-project")
	if err := os.MkdirAll(foreignPath, 0o755); err != nil {
		t.Fatal(err)
	}
	h.api.activeProjectMu.Lock()
	h.api.activeProjectID = "foreign-project"
	h.api.activeProjectPath = foreignPath
	h.api.activeProjectMu.Unlock()

	rec := &recordingReveal{}
	h.api.revealOpener = rec.open

	if err := h.api.RevealInFileManager(sess.ID, RevealTargetTemp, ""); err == nil {
		t.Fatal("a foreign session's temp dir must not be revealed")
	}
	if rec.count() != 0 {
		t.Fatal("launcher must not run for a foreign session")
	}
}
