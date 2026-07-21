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
