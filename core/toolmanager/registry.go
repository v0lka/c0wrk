package toolmanager

import (
	_ "embed" // for //go:embed markitdown_lock.txt
	"log/slog"
	"path"
)

// markitdownLock is the fully pinned requirements lock for the
// markitdown[all]==0.1.4 tree (regeneration command recorded in the file
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
			Version:          "0.12.1",
			Type:             StaticBinary,
			Description:      "Python package and project manager (bootstrapper for markitdown)",
			BinName:          "uv",
			ArchiveName:      "uv-" + triple + ".tar.gz",
			BinPathInArchive: "uv-" + triple + "/uv",
			URLs: map[string]string{
				"darwin-amd64":  "https://github.com/astral-sh/uv/releases/download/0.12.1/uv-x86_64-apple-darwin.tar.gz",
				"darwin-arm64":  "https://github.com/astral-sh/uv/releases/download/0.12.1/uv-aarch64-apple-darwin.tar.gz",
				"linux-amd64":   "https://github.com/astral-sh/uv/releases/download/0.12.1/uv-x86_64-unknown-linux-musl.tar.gz",
				"linux-arm64":   "https://github.com/astral-sh/uv/releases/download/0.12.1/uv-aarch64-unknown-linux-gnu.tar.gz",
				"windows-amd64": "https://github.com/astral-sh/uv/releases/download/0.12.1/uv-x86_64-pc-windows-msvc.zip",
			},
			Checksums: map[string]string{
				"darwin-amd64":  "69d9f9a00337f25a50dcb13882052da08b8469bac11091c98c5694c3c6721467",
				"darwin-arm64":  "77d2906988e8074fd43f2f329ec452ebbf9b0c257ba1c66451c71de70a6baf42",
				"linux-amd64":   "47823f814693bab8623308341369190de30b5c621eec5b1ee20352eae8c7982c",
				"linux-arm64":   "769d373e146692c639b5fbaae33b331c297a32e03d30448772051902df52bbf4",
				"windows-amd64": "8fcb0cb46e1229065e344758980924e569bef5882ef45f46fada8fb24e06b74a",
			},
		},
		{
			Name:             "rg",
			Version:          "14.1.1",
			Type:             StaticBinary,
			Description:      "Fast recursive regex content search (ripgrep)",
			BinName:          "rg",
			ArchiveName:      "ripgrep-14.1.1-" + triple + ".tar.gz",
			BinPathInArchive: "ripgrep-14.1.1-" + triple + "/rg",
			URLs: map[string]string{
				"darwin-amd64":  "https://github.com/BurntSushi/ripgrep/releases/download/14.1.1/ripgrep-14.1.1-x86_64-apple-darwin.tar.gz",
				"darwin-arm64":  "https://github.com/BurntSushi/ripgrep/releases/download/14.1.1/ripgrep-14.1.1-aarch64-apple-darwin.tar.gz",
				"linux-amd64":   "https://github.com/BurntSushi/ripgrep/releases/download/14.1.1/ripgrep-14.1.1-x86_64-unknown-linux-musl.tar.gz",
				"linux-arm64":   "https://github.com/BurntSushi/ripgrep/releases/download/14.1.1/ripgrep-14.1.1-aarch64-unknown-linux-gnu.tar.gz",
				"windows-amd64": "https://github.com/BurntSushi/ripgrep/releases/download/14.1.1/ripgrep-14.1.1-x86_64-pc-windows-msvc.zip",
			},
			Checksums: map[string]string{
				"darwin-amd64":  "fc87e78f7cb3fea12d69072e7ef3b21509754717b746368fd40d88963630e2b3",
				"darwin-arm64":  "24ad76777745fbff131c8fbc466742b011f925bfa4fffa2ded6def23b5b937be",
				"linux-amd64":   "4cf9f2741e6c465ffdb7c26f38056a59e2a2544b51f7cc128ef28337eeae4d8e",
				"linux-arm64":   "c827481c4ff4ea10c9dc7a4022c8de5db34a5737cb74484d62eb94a95841ab2f",
				"windows-amd64": "d0f534024c42afd6cb4d38907c25cd2b249b79bbe6cc1dbee8e3e37c2b6e25a1",
			},
		},
		{
			Name:             "markitdown",
			Version:          "0.1.4",
			Type:             PythonPackage,
			Description:      "Convert various file formats to Markdown (infrastructure-only: installed via uv for the document-conversion pipeline, invoked through VenvPythonPath; never model-invokable)",
			BinName:          "markitdown",
			PipSpec:          "markitdown[all]==0.1.4",
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
