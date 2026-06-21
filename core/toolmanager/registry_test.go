package toolmanager

import (
	"testing"
)

func TestManagedTools_Count(t *testing.T) {
	tools := ManagedTools()
	if len(tools) != 4 {
		t.Errorf("ManagedTools() returned %d tools, want 4", len(tools))
	}
}

func TestManagedTools_Order(t *testing.T) {
	tools := ManagedTools()
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
	tools := ManagedTools()
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
	tools := ManagedTools()
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
	tools := ManagedTools()
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
