package toolmanager

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveBinaryInTree_WindowsExe is the regression test for the second
// half of the Windows-only install failure: after the archive format bug was
// fixed, the binary lookup still could not find the executable because Windows
// archives ship "uv.exe"/"rg.exe" (with the ".exe" suffix) while BinPathInArchive
// was declared without it. The verified upstream layouts are:
//
//	uv  -> flat:            uv.exe
//	rg  -> subdir:          ripgrep-14.1.1-x86_64-pc-windows-msvc/rg.exe
//	rtk -> flat:            rtk.exe
//
// resolveBinaryInTree must locate the ".exe" variant on Windows regardless of
// whether the archive uses a flat or subdirectory layout.
func TestResolveBinaryInTree_WindowsExe(t *testing.T) {
	tmp := t.TempDir()

	cases := []struct {
		name              string
		binPathInArchive  string
		binName           string
		relBinaryInArchive string // path of the file to create under tmp
		wantSuffix        string
	}{
		{
			name:               "uv flat uv.exe",
			binPathInArchive:   "uv-x86_64-pc-windows-msvc/uv",
			binName:            "uv",
			relBinaryInArchive: "uv.exe", // flat at archive root
			wantSuffix:         "uv.exe",
		},
		{
			name:               "rg subdir rg.exe",
			binPathInArchive:   "ripgrep-14.1.1-x86_64-pc-windows-msvc/rg",
			binName:            "rg",
			relBinaryInArchive: "ripgrep-14.1.1-x86_64-pc-windows-msvc/rg.exe",
			wantSuffix:         "rg.exe",
		},
		{
			name:               "rtk flat rtk.exe",
			binPathInArchive:   "rtk",
			binName:            "rtk",
			relBinaryInArchive: "rtk.exe",
			wantSuffix:         "rtk.exe",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			full := filepath.Join(dir, tc.relBinaryInArchive)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("fake"), 0o755); err != nil {
				t.Fatal(err)
			}

			got, ok := resolveBinaryInTree(dir, tc.binPathInArchive, tc.binName, "windows")
			if !ok {
				t.Fatalf("resolveBinaryInTree(windows) did not find binary; want %q", tc.wantSuffix)
			}
			if filepath.Base(got) != filepath.Base(tc.wantSuffix) {
				t.Errorf("resolveBinaryInTree(windows) = %q, want base %q", got, tc.wantSuffix)
			}
		})
	}

	// Sanity: the helper must NOT append ".exe" on a non-Windows target.
	t.Run("unix no exe suffix", func(t *testing.T) {
		full := filepath.Join(tmp, "rg")
		if err := os.WriteFile(full, []byte("fake"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, ok := resolveBinaryInTree(tmp, "rg-subdir/rg", "rg", "linux")
		if !ok {
			t.Fatalf("resolveBinaryInTree(linux) did not find binary")
		}
		if filepath.Base(got) != "rg" {
			t.Errorf("resolveBinaryInTree(linux) = %q, want base %q", got, "rg")
		}
	})
}

// TestResolveBinaryInTree_NotFound asserts a clear negative result when the
// binary is absent from every candidate path.
func TestResolveBinaryInTree_NotFound(t *testing.T) {
	dir := t.TempDir()
	if _, ok := resolveBinaryInTree(dir, "uv-x86_64-pc-windows-msvc/uv", "uv", "windows"); ok {
		t.Error("expected resolveBinaryInTree to report not-found for an empty tree")
	}
}

// TestFindPythonInDir covers the four real on-disk layouts uv produces. The
// Windows managed-install case is the regression for the bug where
// findPythonInDir searched only under "bin/" — but python-build-standalone
// places "python.exe" at the version-dir root (no "bin" subdirectory), so
// markitdown install failed on Windows with "python binary not found in
// ...python\install after uv install". Confirmed by uv's
// executable_path_from_base (managed.rs): base/python.exe on Windows vs
// base/bin/python3 on Unix.
func TestFindPythonInDir(t *testing.T) {
	type layout struct {
		// path segments relative to the search dir, joined to build the fake file
		relFile string
		// whether to create a "bin" variant alongside (to ensure the correct one wins)
		wantBase string // expected basename of the returned path
	}

	cases := []struct {
		name           string
		goos           string
		pythonVersion  string
		layout         layout
		wantFound      bool
		wantFileSuffix string // basename that must be returned
	}{
		{
			name:          "unix managed install cpython-3.12-*/bin/python3",
			goos:          "linux",
			pythonVersion: "3.12",
			layout: layout{
				relFile:  filepath.Join("cpython-3.12.1-linux-x86_64-gnu", "bin", "python3"),
				wantBase: "python3",
			},
			wantFound:      true,
			wantFileSuffix: "python3",
		},
		{
			name:          "unix venv bin/python3 (version-agnostic)",
			goos:          "darwin",
			pythonVersion: "",
			layout: layout{
				relFile:  filepath.Join("bin", "python3"),
				wantBase: "python3",
			},
			wantFound:      true,
			wantFileSuffix: "python3",
		},
		{
			// Regression: Windows managed install places python.exe at the
			// version-dir ROOT, not under bin/. The old code only looked in
			// bin/ and returned "".
			name:          "windows managed install cpython-3.12-*/python.exe (no bin)",
			goos:          "windows",
			pythonVersion: "3.12",
			layout: layout{
				relFile:  filepath.Join("cpython-3.12.1-windows-x86_64-none", "python.exe"),
				wantBase: "python.exe",
			},
			wantFound:      true,
			wantFileSuffix: "python.exe",
		},
		{
			name:          "windows venv Scripts/python.exe",
			goos:          "windows",
			pythonVersion: "",
			layout: layout{
				relFile:  filepath.Join("Scripts", "python.exe"),
				wantBase: "python.exe",
			},
			wantFound:      true,
			wantFileSuffix: "python.exe",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			full := filepath.Join(dir, tc.layout.relFile)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("fake"), 0o755); err != nil {
				t.Fatal(err)
			}

			got := findPythonInDir(dir, tc.pythonVersion, tc.goos)
			if tc.wantFound {
				if got == "" {
					t.Fatalf("findPythonInDir(goos=%q) returned empty; want %q", tc.goos, tc.wantFileSuffix)
				}
				if filepath.Base(got) != tc.wantFileSuffix {
					t.Errorf("findPythonInDir(goos=%q) = %q, want base %q", tc.goos, got, tc.wantFileSuffix)
				}
			} else if got != "" {
				t.Errorf("findPythonInDir(goos=%q) = %q, want empty", tc.goos, got)
			}
		})
	}
}

// TestFindPythonInDir_WindowsRootWinsOverBin ensures that when BOTH a root
// python.exe (the real layout) and a bin/python.exe exist, the Windows lookup
// resolves deterministically and does not silently miss the real interpreter.
func TestFindPythonInDir_WindowsRootWinsOverBin(t *testing.T) {
	dir := t.TempDir()
	versionDir := filepath.Join(dir, "cpython-3.12.1-windows-x86_64-none")
	if err := os.MkdirAll(filepath.Join(versionDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Create BOTH — the real interpreter is at the root; bin/ is a phantom.
	rootExe := filepath.Join(versionDir, "python.exe")
	binExe := filepath.Join(versionDir, "bin", "python.exe")
	if err := os.WriteFile(rootExe, []byte("real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binExe, []byte("phantom"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := findPythonInDir(dir, "3.12", "windows")
	if got == "" {
		t.Fatal("expected a Windows python.exe to be found")
	}
	if filepath.Base(got) != "python.exe" {
		t.Errorf("got base %q, want python.exe", filepath.Base(got))
	}
}

// TestFindPythonInDir_NotFound asserts a clear negative result.
func TestFindPythonInDir_NotFound(t *testing.T) {
	dir := t.TempDir()
	for _, goos := range []string{"linux", "darwin", "windows"} {
		if got := findPythonInDir(dir, "3.12", goos); got != "" {
			t.Errorf("findPythonInDir(goos=%q) = %q, want empty", goos, got)
		}
	}
}
