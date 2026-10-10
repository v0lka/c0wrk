// Windows path-admission diagnostic for #146.
//
// The two WriteFile session-infra admission tests fail on the Windows CI leg
// even with a fully canonical (EvalSymlinks-resolved) base directory, while
// passing on Linux and macOS. Static analysis of pathutil.ResolveExistingPrefix
// (climb on ErrNotExist, rejoin raw tail) and of Go 1.27's Windows
// filepath.EvalSymlinks (walkSymlinks + toNorm/FindFirstFile, with
// ERROR_PATH_NOT_FOUND/ERROR_FILE_NOT_FOUND mapped to fs.ErrNotExist) says the
// parent-exists/child-partial-tail shape must resolve consistently — yet CI
// disagrees. This test runs the SAME call shapes at BOTH layers (pathutil
// primitives directly, then the full WriteFile flow) and traces every
// intermediate value, so one failing Windows CI run pins the diverging layer.
//
// Temporary diagnostics — removed once #146 is fixed and the two admission
// tests are unskipped on Windows.
package backend

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/sp4rk/pathutil"
)

// diagClimb walks the ResolveExistingPrefix candidate chain for path (raw,
// uncleaned, mirroring pathutil's rawParent) and traces EvalSymlinks at every
// step, annotating whether each error is treated as "keep climbing".
func diagClimb(t *testing.T, label, path string) {
	t.Helper()
	t.Logf("%s climb from %q", label, path)
	candidate := path
	for {
		resolved, err := filepath.EvalSymlinks(candidate)
		isNotExist := errors.Is(err, fs.ErrNotExist)
		t.Logf("%s   candidate=%q resolved=%q err=%v isErrNotExist=%v",
			label, candidate, resolved, err, isNotExist)
		if err == nil {
			remainder := ""
			if candidate != path {
				remainder = path[len(candidate):]
			}
			t.Logf("%s   -> ResolveExistingPrefix would return Join(%q, %q) = %q",
				label, resolved, remainder, filepath.Join(resolved, remainder))
			return
		}
		if !isNotExist {
			t.Logf("%s   -> NON-ErrNotExist error: ResolveExistingPrefix would return the path AS-IS (%q)",
				label, path)
			return
		}
		// Mirror pathutil.rawParent: cut at the last separator, collapse runs.
		i := -1
		for j := len(candidate) - 1; j >= 0; j-- {
			if os.IsPathSeparator(candidate[j]) {
				i = j
				break
			}
		}
		if i < 0 {
			t.Logf("%s   -> no separator left, would return %q", label, path)
			return
		}
		for i > 0 && os.IsPathSeparator(candidate[i-1]) {
			i--
		}
		if i == 0 {
			i = 1
		}
		candidate = candidate[:i]
	}
}

// diagTraceShape traces one containment shape end-to-end: the resolution of
// both sides, the volumes, and the case-folded raw comparison, then asserts
// the expected verdict so a divergence FAILS the test and lands in the CI log.
func diagTraceShape(t *testing.T, name string, wantWithin bool, parent, child string) {
	t.Helper()
	t.Logf("%s: parent=%q child=%q wantWithin=%v", name, parent, child, wantWithin)
	diagClimb(t, name+".parent", parent)
	diagClimb(t, name+".child", child)

	parentResolved := pathutil.ResolveExistingPrefix(filepath.Clean(parent))
	childResolved := pathutil.ResolveExistingPrefix(child)
	t.Logf("%s: parentResolved=%q childResolved=%q", name, parentResolved, childResolved)
	t.Logf("%s: volumes parent=%q child=%q equal=%v",
		name,
		filepath.VolumeName(parentResolved), filepath.VolumeName(childResolved),
		filepath.VolumeName(parentResolved) == filepath.VolumeName(childResolved))
	t.Logf("%s: EqualFold(parentResolved, childResolved)=%v prefixFold=%v",
		name,
		strings.EqualFold(parentResolved, childResolved),
		len(childResolved) >= len(parentResolved) &&
			strings.EqualFold(childResolved[:len(parentResolved)], parentResolved))

	got, err := pathutil.IsWithinPath(parent, child)
	t.Logf("%s: pathutil.IsWithinPath = %v, err = %v", name, got, err)
	if got != wantWithin {
		t.Errorf("%s: pathutil.IsWithinPath(%q, %q) = %v, want %v (err=%v)",
			name, parent, child, got, wantWithin, err)
	}
}

// TestDiagnostic_WindowsPathAdmission reproduces the #146 admission shapes at
// the pathutil layer and at the full WriteFile layer, with full tracing.
// Windows-only: the divergence exists only under Windows EvalSymlinks
// semantics; on other GOOS it is a no-op skip.
func TestDiagnostic_WindowsPathAdmission(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only diagnostic for #146")
	}
	base := canonicalTempDir(t)
	t.Logf("os.TempDir()=%q", os.TempDir())
	t.Logf("t.TempDir() raw spelling shown via canonicalTempDir=%q", base)
	if resolved, err := filepath.EvalSymlinks(os.TempDir()); err != nil {
		t.Logf("EvalSymlinks(os.TempDir()) error=%v isErrNotExist=%v", err, errors.Is(err, fs.ErrNotExist))
	} else {
		t.Logf("EvalSymlinks(os.TempDir())=%q EqualFold=%v", resolved, strings.EqualFold(resolved, os.TempDir()))
	}

	// Shape 1 (TestWriteFile_SessionInfraPlanPath_Admitted): a regular
	// project. projDir exists (Workspace was created beneath it); the child's
	// sess-1 tree does not exist at admission time.
	pid, sid := "proj-1", "sess-1"
	ws := filepath.Join(base, "projects", pid, "Workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	projDir := filepath.Join(base, "projects", pid)
	planPath := filepath.Join(base, "projects", pid, sid, "plans", "plan_x.md")
	diagTraceShape(t, "shape1.workspace-vs-plan", false, ws, planPath)
	diagTraceShape(t, "shape1.projdir-vs-plan", true, projDir, planPath)

	// Shape 2 (TestWriteFile_NoProject_CrossSessionRejected): the No Project
	// project dir exists (another session's workspace was created beneath
	// it); the own session's tree does not exist at admission time.
	npDir := config.ProjectDir(base, project.NoProjectID)
	if err := os.MkdirAll(filepath.Join(npDir, "other-session", "workspace"), 0o755); err != nil {
		t.Fatalf("mkdir no-project tree: %v", err)
	}
	ownPlan := filepath.Join(npDir, "my-session", "plans", "plan.md")
	diagTraceShape(t, "shape2.npdir-vs-plan", true, npDir, ownPlan)
	diagTraceShape(t, "shape2.ownsession-vs-plan", true, filepath.Join(npDir, "my-session"), ownPlan)

	// Session-infra classification, the exact call resolveWorkspacePath makes.
	t.Logf("IsSessionInfraPath(shape1 projDir, plan) = %v",
		config.IsSessionInfraPath(projDir, planPath))
	t.Logf("IsSessionInfraPath(shape2 npDir, ownPlan) = %v",
		config.IsSessionInfraPath(npDir, ownPlan))

	// Full WriteFile flow for shape 1 — the exact failing call.
	f := newWriteFileTestAPI(t, base, pid, ws)
	if err := f.WriteFile(sid, planPath, "# plan"); err != nil {
		t.Errorf("shape1 full WriteFile flow rejected: %v", err)
	} else if data, rerr := os.ReadFile(planPath); rerr != nil || string(data) != "# plan" {
		t.Errorf("shape1 write not persisted: %v (%q)", rerr, string(data))
	}

	// Full WriteFile flow for shape 2 — own session-infra write.
	f2 := newWriteFileTestAPI(t, base, project.NoProjectID, npDir)
	if err := f2.WriteFile("my-session", ownPlan, "# plan"); err != nil {
		t.Errorf("shape2 full WriteFile flow rejected: %v", err)
	} else if data, rerr := os.ReadFile(ownPlan); rerr != nil || string(data) != "# plan" {
		t.Errorf("shape2 write not persisted: %v (%q)", rerr, string(data))
	}
}
