package toolmanager

import (
	_ "embed" // for //go:embed markitdown_lock.txt
	"log/slog"
	"path"
)

// markitdownLock is the fully pinned requirements lock for the
// markitdown[all]==0.1.9 tree (regeneration command recorded in the file
// header; OSV-reviewed), pinning every transitive dependency at the audited
// versions. See specs/domains/tool-manager.md.
//
//go:embed markitdown_lock.txt
var markitdownLock string

// ToolType classifies how a managed tool is installed and invoked.
type ToolType string

const (
	// StaticBinary is a self-contained executable downloaded as an archive.
	StaticBinary ToolType = "static_binary"
	// PythonPackage is a pip-installable package requiring a Python runtime.
	PythonPackage ToolType = "python_package"
)

// ToolSpec describes a managed external tool, including per-platform
// download URLs, checksums, and archive layout.
type ToolSpec struct {
	Name        string // unique name, e.g. "rg", "uv", "markitdown"
	Version     string // pinned upstream version
	Type        ToolType
	Description string
	BinName     string // executable name after install, e.g. "rg"

	// ArchiveName is the on-disk cache filename for the current platform
	// (triple-derived unless ArchiveNameOverride is set).
	ArchiveName string

	// ArchiveNameOverride, when non-empty, replaces the PlatformTriple-derived
	// ArchiveName for platforms whose upstream naming differs from the triple.
	ArchiveNameOverride string

	// BinPathInArchive is the binary's relative path inside the archive
	// (triple-derived unless BinPathInArchiveOverride is set).
	BinPathInArchive string

	// BinPathInArchiveOverride, when non-empty: see ArchiveNameOverride.
	BinPathInArchiveOverride string

	// PipSpec is the pip install spec for PythonPackage tools; the fallback
	// when RequirementsLock is empty.
	PipSpec string

	// RequirementsLock: embedded pinned lock (see markitdownLock); installed
	// via `uv pip install -r`, else PipSpec. See specs/domains/tool-manager.md.
	RequirementsLock string

	// PythonVersion is the Python version required for PythonPackage tools.
	PythonVersion string

	// URLs maps platform keys to download URLs.
	URLs map[string]string

	// Checksums maps platform keys to SHA256 hex strings; a missing/empty
	// checksum FAILS verification (fail-closed, ASI04-R2) — every supported
	// platform must declare one. See specs/domains/tool-manager.md.
	Checksums map[string]string
}

// ManagedTools returns the managed registry in dependency order: uv first
// (bootstraps markitdown), then rg, then markitdown. markitdown has no
// model-facing tool wrapper; backend layers consume it via VenvPythonPath
// for vision-assisted conversion. See specs/domains/tool-manager.md.
func ManagedTools(logger *slog.Logger) ([]ToolSpec, error) {
	triple := PlatformTriple(logger)
	tools := []ToolSpec{
		{
			Name:             "uv",
			Version:          "0.13.6",
			Type:             StaticBinary,
			Description:      "Python package and project manager (bootstrapper for markitdown)",
			BinName:          "uv",
			ArchiveName:      "uv-" + triple + ".tar.gz",
			BinPathInArchive: "uv-" + triple + "/uv",
			URLs: map[string]string{
				"darwin-amd64":  "https://github.com/astral-sh/uv/releases/download/0.13.6/uv-x86_64-apple-darwin.tar.gz",
				"darwin-arm64":  "https://github.com/astral-sh/uv/releases/download/0.13.6/uv-aarch64-apple-darwin.tar.gz",
				"linux-amd64":   "https://github.com/astral-sh/uv/releases/download/0.13.6/uv-x86_64-unknown-linux-musl.tar.gz",
				"linux-arm64":   "https://github.com/astral-sh/uv/releases/download/0.13.6/uv-aarch64-unknown-linux-gnu.tar.gz",
				"windows-amd64": "https://github.com/astral-sh/uv/releases/download/0.13.6/uv-x86_64-pc-windows-msvc.zip",
			},
			Checksums: map[string]string{
				"darwin-amd64":  "63ba17fdda55ad1a230d4b37fc6017ffa10c23688c83818bd3bfdf914c652a02",
				"darwin-arm64":  "60fbd5c8a1ed681845eea28d9d622fcde03c51e91a43465b64a1e17db1ed3472",
				"linux-amd64":   "5d61f26b6c7213779eda90edc6971ea6b0b796bfb8d49a574c8ad69b59838548",
				"linux-arm64":   "229e194255c7958d86d56e8790d969a6f74736592133f0dfd0076f754c642c1d",
				"windows-amd64": "a2a8820d5eea32f08ef467ef3b7f66883a9b076d5737a2fc2fbb5f458d1bb50f",
			},
		},
		{
			Name:             "rg",
			Version:          "15.2.0",
			Type:             StaticBinary,
			Description:      "Fast recursive regex content search (ripgrep)",
			BinName:          "rg",
			ArchiveName:      "ripgrep-15.2.0-" + triple + ".tar.gz",
			BinPathInArchive: "ripgrep-15.2.0-" + triple + "/rg",
			URLs: map[string]string{
				"darwin-amd64":  "https://github.com/BurntSushi/ripgrep/releases/download/15.2.0/ripgrep-15.2.0-x86_64-apple-darwin.tar.gz",
				"darwin-arm64":  "https://github.com/BurntSushi/ripgrep/releases/download/15.2.0/ripgrep-15.2.0-aarch64-apple-darwin.tar.gz",
				"linux-amd64":   "https://github.com/BurntSushi/ripgrep/releases/download/15.2.0/ripgrep-15.2.0-x86_64-unknown-linux-musl.tar.gz",
				"linux-arm64":   "https://github.com/BurntSushi/ripgrep/releases/download/15.2.0/ripgrep-15.2.0-aarch64-unknown-linux-gnu.tar.gz",
				"windows-amd64": "https://github.com/BurntSushi/ripgrep/releases/download/15.2.0/ripgrep-15.2.0-x86_64-pc-windows-msvc.zip",
			},
			Checksums: map[string]string{
				"darwin-amd64":  "af7825fcc69a2afc7a7aea55fc9af90e26421d8f20fe59df32e233c0b8a231c1",
				"darwin-arm64":  "3750b2e93f37e0c692657da574d7019a101c0084da05a790c83fd335bad973e4",
				"linux-amd64":   "33e15bcf1624b25cdd2a55813a47a2f95dbe126268203e76aa6a585d1e7b149c",
				"linux-arm64":   "a740b91c82eaf9914cfedd353572f2791cbe0162c84101ee0951058f4dcbc90d",
				"windows-amd64": "71b2fef860abe467217a538ff31de02f5258807c0129f771846f87bd029aafc5",
			},
		},
		{
			Name:             "markitdown",
			Version:          "0.1.9",
			Type:             PythonPackage,
			Description:      "Convert various file formats to Markdown (infrastructure-only: installed via uv for the document-conversion pipeline, invoked through VenvPythonPath; never model-invokable)",
			BinName:          "markitdown",
			PipSpec:          "markitdown[all]==0.1.9",
			RequirementsLock: markitdownLock,
			PythonVersion:    "3.12",
			// PythonPackage tools use uv for bootstrap; URLs/Checksums are nil.
			URLs:      nil,
			Checksums: nil,
		},
	}

	// Apply per-platform overrides where upstream naming differs from the triple.
	platform := Platform()
	for i := range tools {
		tools[i].ArchiveName = archiveNameForPlatform(tools[i], platform)
		if tools[i].BinPathInArchiveOverride != "" {
			tools[i].BinPathInArchive = tools[i].BinPathInArchiveOverride
		}
	}

	return tools, nil
}

// archiveNameForPlatform resolves the cache filename: the URL basename (the
// real archive format) unless ArchiveNameOverride wins; the triple-derived
// ArchiveName when there is no URL. See specs/domains/tool-manager.md.
func archiveNameForPlatform(tool ToolSpec, platform string) string {
	if tool.ArchiveNameOverride != "" {
		return tool.ArchiveNameOverride
	}
	if url, ok := tool.URLs[platform]; ok && url != "" {
		if base := path.Base(url); base != "" && base != "." && base != "/" {
			return base
		}
	}
	return tool.ArchiveName
}
