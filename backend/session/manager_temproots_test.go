package session

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/v0lka/c0wrk/core"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// containsRoot reports whether the cleaned want path is a member of roots.
func containsRoot(roots []string, want string) bool {
	want = filepath.Clean(want)
	for _, r := range roots {
		if filepath.Clean(r) == want {
			return true
		}
	}
	return false
}

// TestImplicitTempRoots_Posix verifies the non-Windows branch: the candidates
// are "/tmp" and the host temp dir, with trailing separators trimmed. Empty
// values are skipped, and the common TMPDIR="/tmp" (Linux CI) case
// deduplicates to a single entry.
func TestImplicitTempRoots_Posix(t *testing.T) {
	got := implicitTempRoots("darwin", "/var/folders/ab/T/", "")
	if len(got) != 2 || got[0] != "/tmp" || got[1] != "/var/folders/ab/T" {
		t.Fatalf("expected [/tmp /var/folders/ab/T], got %v", got)
	}

	// TMPDIR equal to /tmp collapses to one entry (dedup after trimming).
	got = implicitTempRoots("linux", "/tmp", "")
	if len(got) != 1 || got[0] != "/tmp" {
		t.Fatalf("expected deduplicated [/tmp], got %v", got)
	}

	// Empty temp dir is skipped rather than normalized into a root.
	got = implicitTempRoots("linux", "", "")
	if len(got) != 1 || got[0] != "/tmp" {
		t.Fatalf("expected [/tmp] with empty tempDir skipped, got %v", got)
	}
}

// TestImplicitTempRoots_Windows verifies the Windows branch: tempDir plus
// %SystemRoot%\Temp, with an unset SystemRoot leaving only tempDir.
func TestImplicitTempRoots_Windows(t *testing.T) {
	tempDir := `C:\Users\me\AppData\Local\Temp`
	sysTemp := `C:\Windows\Temp`
	got := implicitTempRoots("windows", tempDir, `C:\Windows`)
	if len(got) != 2 || got[0] != tempDir || got[1] != sysTemp {
		t.Fatalf("expected [%s %s], got %v", tempDir, sysTemp, got)
	}

	// Unset %SystemRoot% must not fabricate a relative "Temp" root.
	got = implicitTempRoots("windows", `D:\tmp`, "")
	if len(got) != 1 || got[0] != `D:\tmp` {
		t.Fatalf("expected [D:\\tmp] with empty SystemRoot skipped, got %v", got)
	}
}

// TestImplicitTempRoots_SkipsInvalidInputs verifies that empty and relative
// inputs never produce root elements: the containment API requires absolute
// roots, so a relative TMPDIR or SystemRoot must be dropped (fail-closed)
// rather than injected as an unusable root.
func TestImplicitTempRoots_SkipsInvalidInputs(t *testing.T) {
	tests := []struct {
		name          string
		goos          string
		tempDir       string
		systemRootEnv string
		want          []string
	}{
		{
			name:    "posix relative tempDir skipped",
			goos:    "linux",
			tempDir: "scratch",
			want:    []string{"/tmp"},
		},
		{
			name:    "posix nested relative tempDir skipped",
			goos:    "darwin",
			tempDir: "some/rel/tmp",
			want:    []string{"/tmp"},
		},
		{
			name:    "posix empty tempDir skipped",
			goos:    "linux",
			tempDir: "",
			want:    []string{"/tmp"},
		},
		{
			name:          "windows relative tempDir skipped",
			goos:          "windows",
			tempDir:       `Temp`,
			systemRootEnv: `C:\Windows`,
			want:          []string{`C:\Windows\Temp`},
		},
		{
			name:          "windows drive-relative tempDir skipped",
			goos:          "windows",
			tempDir:       `C:`,
			systemRootEnv: `C:\Windows`,
			want:          []string{`C:\Windows\Temp`},
		},
		{
			name:          "windows relative SystemRoot skipped",
			goos:          "windows",
			tempDir:       `D:\tmp`,
			systemRootEnv: `Windows`,
			want:          []string{`D:\tmp`},
		},
		{
			name:    "windows empty tempDir skipped",
			goos:    "windows",
			tempDir: "",
			want:    nil,
		},
		{
			name:    "windows trailing separators trimmed",
			goos:    "windows",
			tempDir: `E:\scratch\`,
			want:    []string{`E:\scratch`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := implicitTempRoots(tt.goos, tt.tempDir, tt.systemRootEnv)
			if len(got) != len(tt.want) {
				t.Fatalf("implicitTempRoots(%q, %q, %q) = %v; want %v", tt.goos, tt.tempDir, tt.systemRootEnv, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("implicitTempRoots(%q, %q, %q)[%d] = %q; want %q (full: %v)", tt.goos, tt.tempDir, tt.systemRootEnv, i, got[i], tt.want[i], got)
				}
			}
		})
	}
}

// TestImplicitTempRoots_MatchesHost verifies the real invocation parameters:
// on the running host the function returns the exact temp roots
// injectWorkDirectories must inject ("/tmp" plus os.TempDir() on POSIX,
// os.TempDir() plus %SystemRoot%\Temp on Windows).
func TestImplicitTempRoots_MatchesHost(t *testing.T) {
	want := []string{"/tmp", os.TempDir()}
	if runtime.GOOS == "windows" {
		want = []string{os.TempDir()}
		if sr := os.Getenv("SystemRoot"); sr != "" {
			want = append(want, filepath.Join(sr, "Temp"))
		}
	}
	got := implicitTempRoots(runtime.GOOS, os.TempDir(), os.Getenv("SystemRoot"))
	for _, w := range want {
		if !containsRoot(got, w) {
			t.Fatalf("host roots %v missing %q", got, w)
		}
	}
}

// TestInjectWorkDirectories_NoDirs_StillSetsTempRoots is the core CHAT-mode
// (No Project) regression: with zero work directories the context must still
// carry the implicit host temp roots as allowed roots, while the prompt-facing
// work-directory list stays unset.
func TestInjectWorkDirectories_NoDirs_StillSetsTempRoots(t *testing.T) {
	m, _, _ := testManager(t)
	ctx := m.injectWorkDirectories(context.Background(), nil)

	roots := sdktools.AllowedRootsFrom(ctx)
	if len(roots) == 0 {
		t.Fatal("AllowedRootsFrom is empty for a 0-workdir session; want implicit temp roots")
	}
	if runtime.GOOS != "windows" && !containsRoot(roots, "/tmp") {
		t.Fatalf("allowed roots %v missing /tmp", roots)
	}
	if !containsRoot(roots, os.TempDir()) {
		t.Fatalf("allowed roots %v missing os.TempDir()=%q", roots, os.TempDir())
	}

	// The prompt-facing list must remain unset: temp roots are security-only.
	if dirs := core.WorkDirectoriesFrom(ctx); dirs != nil {
		t.Fatalf("WorkDirectoriesFrom = %v for 0 dirs; want nil (no prompt leakage)", dirs)
	}
}

// TestInjectWorkDirectories_WithDirs_UnionInOneCall verifies that configured
// work directories and the implicit temp roots land in the SAME
// WithAllowedRoots call (a single AllowedRootsFrom result), and that the
// prompt-facing list is set unchanged. Fixtures are platform-aware so the
// test runs on every OS in the CI matrix (linux, macos, windows).
func TestInjectWorkDirectories_WithDirs_UnionInOneCall(t *testing.T) {
	auxA, auxB := "/aux/repo-a", "/aux/repo-b"
	wantTempRoots := []string{"/tmp", os.TempDir()}
	if runtime.GOOS == "windows" {
		auxA, auxB = `C:\aux\repo-a`, `C:\aux\repo-b`
		wantTempRoots = []string{os.TempDir()}
		if sr := os.Getenv("SystemRoot"); sr != "" {
			wantTempRoots = append(wantTempRoots, filepath.Join(sr, "Temp"))
		}
	}
	m, _, _ := testManager(t)
	dirs := []core.WorkDirectory{
		{Path: auxA, Description: "first aux repo"},
		{Path: auxB, Description: "second aux repo"},
	}
	ctx := m.injectWorkDirectories(context.Background(), dirs)

	roots := sdktools.AllowedRootsFrom(ctx)
	for _, want := range append([]string{auxA, auxB}, wantTempRoots...) {
		if !containsRoot(roots, want) {
			t.Fatalf("allowed roots %v missing %q", roots, want)
		}
	}

	got := core.WorkDirectoriesFrom(ctx)
	if len(got) != len(dirs) {
		t.Fatalf("WorkDirectoriesFrom = %v; want %v", got, dirs)
	}
	for i := range dirs {
		if got[i] != dirs[i] {
			t.Fatalf("WorkDirectoriesFrom[%d] = %+v; want %+v", i, got[i], dirs[i])
		}
	}
}

// TestInjectWorkDirectories_Containment exercises sdktools.IsWithinRoot
// against the produced context: temp paths (both the symlinked /tmp spelling
// and the resolved /private/tmp spelling on darwin) are contained, while a
// system file outside the temp tree is not. Fixtures are platform-aware so
// the test runs on every OS in the CI matrix.
func TestInjectWorkDirectories_Containment(t *testing.T) {
	root := "/tmp"
	scratch := "/tmp/c0wrk-scratch/file.txt"
	outside := "/etc/passwd"
	if runtime.GOOS == "windows" {
		root = os.TempDir()
		scratch = filepath.Join(root, "c0wrk-scratch", "file.txt")
		outside = `C:\Windows\System32\drivers\etc\hosts`
	}
	m, _, _ := testManager(t)
	ctx := m.injectWorkDirectories(context.Background(), nil)

	if !sdktools.IsWithinRoot(ctx, root, scratch) {
		t.Fatalf("IsWithinRoot(%q, %q) = false; want true", root, scratch)
	}
	if runtime.GOOS == "darwin" {
		// /tmp → /private/tmp symlink: containment must hold across both
		// spellings via symlink-resolving prefix resolution.
		if !sdktools.IsWithinRoot(ctx, "/tmp", "/private/tmp/c0wrk-scratch/file.txt") {
			t.Fatal("IsWithinRoot(/tmp, /private/tmp/...) = false; want true on darwin")
		}
	}
	if sdktools.IsWithinRoot(ctx, root, outside) {
		t.Fatalf("IsWithinRoot(%q, %q) = true; want false", root, outside)
	}
}
