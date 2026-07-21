package toolmanager

import (
	"path"
	"strings"
	"testing"
)

func TestManagedTools_Count(t *testing.T) {
	tools, err := ManagedTools()
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 4 {
		t.Errorf("ManagedTools() returned %d tools, want 4", len(tools))
	}
}

func TestManagedTools_Order(t *testing.T) {
	tools, err := ManagedTools()
	if err != nil {
		t.Fatal(err)
	}
	// uv must be first (bootstrapper for markitdown).
	if tools[0].Name != "uv" {
		t.Errorf("first tool is %q, want uv", tools[0].Name)
	}
	// markitdown must be last (depends on uv).
	if tools[len(tools)-1].Name != "markitdown" {
		t.Errorf("last tool is %q, want markitdown", tools[len(tools)-1].Name)
	}
}

func TestManagedTools_HasURLs(t *testing.T) {
	platform := Platform()
	tools, err := ManagedTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Type != StaticBinary {
			continue
		}
		url, ok := tool.URLs[platform]
		if !ok || url == "" {
			t.Errorf("tool %q has no URL for platform %q", tool.Name, platform)
		}
	}
}

func TestManagedTools_StaticBinariesHaveArchiveInfo(t *testing.T) {
	tools, err := ManagedTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Type != StaticBinary {
			continue
		}
		if tool.BinName == "" {
			t.Errorf("tool %q has empty BinName", tool.Name)
		}
		if tool.ArchiveName == "" {
			t.Errorf("tool %q has empty ArchiveName", tool.Name)
		}
		if tool.BinPathInArchive == "" {
			t.Errorf("tool %q has empty BinPathInArchive", tool.Name)
		}
	}
}

func TestManagedTools_MarkitdownIsPythonPackage(t *testing.T) {
	tools, err := ManagedTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Name == "markitdown" {
			if tool.Type != PythonPackage {
				t.Errorf("markitdown has type %s, want python_package", tool.Type)
			}
			if tool.PipSpec != "markitdown[all]" {
				t.Errorf("markitdown PipSpec = %q, want markitdown[all]", tool.PipSpec)
			}
			if tool.PythonVersion == "" {
				t.Error("markitdown has empty PythonVersion")
			}
			return
		}
	}
	t.Error("markitdown not found in ManagedTools()")
}

// TestManagedTools_ArchiveNameMatchesURL is the regression test for a
// Windows-only bug where ArchiveName was hardcoded to ".tar.gz" even though
// upstream ships ".zip" archives on Windows. The download saved zip bytes
// under a ".tar.gz" filename; the checksum passed (whole-file hash) but the
// installer then tried gzip extraction on a zip and failed with
// "gzip: invalid header". The ArchiveName must always match the real format
// advertised by the download URL for the current platform.
func TestManagedTools_ArchiveNameMatchesURL(t *testing.T) {
	platform := Platform()
	tools, err := ManagedTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Type != StaticBinary {
			continue
		}
		url, ok := tool.URLs[platform]
		if !ok || url == "" {
			continue // no URL for this platform — skip
		}
		want := path.Base(url)
		if tool.ArchiveName != want {
			t.Errorf("tool %q ArchiveName = %q, want %q (URL basename) for platform %q",
				tool.Name, tool.ArchiveName, want, platform)
		}
	}
}

// TestArchiveNameForPlatform_WindowsZip directly verifies the Windows fix on
// any host platform (CI runs Linux/macOS, never Windows, so a
// run-platform-only assertion could not catch this regression). The uv/rg/rtk
// Windows URLs serve ".zip" archives, so the resolved cache filename must end
// in ".zip" — never the ".tar.gz" that triggered "gzip: invalid header".
func TestArchiveNameForPlatform_WindowsZip(t *testing.T) {
	tools, err := ManagedTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Type != StaticBinary {
			continue
		}
		url := tool.URLs["windows-amd64"]
		if url == "" {
			continue
		}
		got := archiveNameForPlatform(tool, "windows-amd64")
		want := path.Base(url)
		if got != want {
			t.Errorf("tool %q windows ArchiveName = %q, want %q", tool.Name, got, want)
		}
		if !strings.HasSuffix(got, ".zip") {
			t.Errorf("tool %q windows ArchiveName %q must be a .zip archive", tool.Name, got)
		}
	}
}

// TestArchiveNameForPlatform_NonWindowsTarGz ensures the Unix/macOS behavior
// is unchanged: those URLs serve ".tar.gz", so the derived name keeps that
// extension.
func TestArchiveNameForPlatform_NonWindowsTarGz(t *testing.T) {
	tools, err := ManagedTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Type != StaticBinary {
			continue
		}
		for _, platform := range []string{"darwin-arm64", "linux-amd64"} {
			url := tool.URLs[platform]
			if url == "" {
				continue
			}
			got := archiveNameForPlatform(tool, platform)
			if !strings.HasSuffix(got, ".tar.gz") {
				t.Errorf("tool %q %s ArchiveName %q must be a .tar.gz archive", tool.Name, platform, got)
			}
		}
	}
}
