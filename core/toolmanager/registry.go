package toolmanager

import (
	"path"
)

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
	Name        string // unique name, e.g. "rg", "rtk", "uv", "markitdown"
	Version     string // pinned upstream version
	Type        ToolType
	Description string
	BinName     string // executable name after install, e.g. "rg"

	// ArchiveName returns the archive file name for the current platform
	// (e.g. "ripgrep-14.1.1-x86_64-apple-darwin.tar.gz").
	// Constructed from PlatformTriple() unless ArchiveNameOverride is set.
	ArchiveName string

	// ArchiveNameOverride, when non-empty, replaces the PlatformTriple-derived
	// ArchiveName. Use this when an upstream project uses a different naming
	// convention than PlatformTriple for a specific platform (e.g. uv uses
	// "-gnu" on linux-amd64, but PlatformTriple returns "-musl").
	ArchiveNameOverride string

	// BinPathInArchive is the relative path to the binary inside the archive
	// (e.g. "ripgrep-14.1.1-x86_64-apple-darwin/rg").
	// Constructed from PlatformTriple() unless BinPathInArchiveOverride is set.
	BinPathInArchive string

	// BinPathInArchiveOverride, when non-empty, replaces the PlatformTriple-derived
	// BinPathInArchive. See ArchiveNameOverride for rationale.
	BinPathInArchiveOverride string

	// PipSpec is the pip install spec for PythonPackage tools (e.g. "markitdown[all]").
	PipSpec string

	// PythonVersion is the Python version required for PythonPackage tools.
	PythonVersion string

	// URLs maps platform keys to download URLs.
	URLs map[string]string

	// Checksums maps platform keys to SHA256 hex strings. An empty string
	// disables checksum verification for that platform.
	Checksums map[string]string
}

// ManagedTools returns the registry of all tools managed by the tool-manager
// in dependency order: uv first (needed to bootstrap markitdown), then static
// binaries (rg, rtk), then markitdown last.
//
// Note: rtk and markitdown are infrastructure-only at this stage — the
// tool-manager downloads and installs them, but no built-in tool wrappers
// consume them yet. Wrappers will be added in github.com/v0lka/sp4rk/tools/builtins/ when the
// agent is ready to invoke these tools.
func ManagedTools() ([]ToolSpec, error) {
	triple, err := PlatformTriple()
	if err != nil {
		return nil, err
	}
	tools := []ToolSpec{
		{
			Name:             "uv",
			Version:          "0.7.0",
			Type:             StaticBinary,
			Description:      "Python package and project manager (bootstrapper for markitdown)",
			BinName:          "uv",
			ArchiveName:      "uv-" + triple + ".tar.gz",
			BinPathInArchive: "uv-" + triple + "/uv",
			URLs: map[string]string{
				"darwin-amd64":  "https://github.com/astral-sh/uv/releases/download/0.7.0/uv-x86_64-apple-darwin.tar.gz",
				"darwin-arm64":  "https://github.com/astral-sh/uv/releases/download/0.7.0/uv-aarch64-apple-darwin.tar.gz",
				"linux-amd64":   "https://github.com/astral-sh/uv/releases/download/0.7.0/uv-x86_64-unknown-linux-musl.tar.gz",
				"linux-arm64":   "https://github.com/astral-sh/uv/releases/download/0.7.0/uv-aarch64-unknown-linux-gnu.tar.gz",
				"windows-amd64": "https://github.com/astral-sh/uv/releases/download/0.7.0/uv-x86_64-pc-windows-msvc.zip",
			},
			Checksums: map[string]string{
				"darwin-amd64":  "dc5037f3ffbf8074b3ee63de7a73aa57421b0da0837a478e26317424dbab16f3",
				"darwin-arm64":  "964ebe641b563920e0650a60bf5ac21e6c8c56557704e5ecfaaad7ff62c3a73c",
				"linux-amd64":   "08e1bb8fdea2c6d5edbe40ab1651de097b884020056c0925a9973582ff669d04",
				"linux-arm64":   "540fcb8f2f972c82260a8063a6a4b496d7ff858edc42aa0e2c733a7b55ef8dd8",
				"windows-amd64": "62836c9d6e3f346d06c45fee4109be21ca9d1df8d087472dcc8d51815f182332",
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
			Name:             "rtk",
			Version:          "0.28.2",
			Type:             StaticBinary,
			Description:      "CLI proxy that reduces LLM token consumption on common dev commands (infrastructure-only: installed but not yet consumed by the agent)",
			BinName:          "rtk",
			ArchiveName:      "rtk-" + triple + ".tar.gz",
			BinPathInArchive: "rtk",
			URLs: map[string]string{
				"darwin-amd64":  "https://github.com/rtk-ai/rtk/releases/download/v0.28.2/rtk-x86_64-apple-darwin.tar.gz",
				"darwin-arm64":  "https://github.com/rtk-ai/rtk/releases/download/v0.28.2/rtk-aarch64-apple-darwin.tar.gz",
				"linux-amd64":   "https://github.com/rtk-ai/rtk/releases/download/v0.28.2/rtk-x86_64-unknown-linux-musl.tar.gz",
				"linux-arm64":   "https://github.com/rtk-ai/rtk/releases/download/v0.28.2/rtk-aarch64-unknown-linux-gnu.tar.gz",
				"windows-amd64": "https://github.com/rtk-ai/rtk/releases/download/v0.28.2/rtk-x86_64-pc-windows-msvc.zip",
			},
			Checksums: map[string]string{
				"darwin-amd64":  "5ce5dab3b744a6ecce7ff9deea9fd4606f72c6490c9ee447d74883d9393dcbc7",
				"darwin-arm64":  "5dede8ac36648960a3ad52611856b9047a7817b755750d2bdbda8d4e9931db4d",
				"linux-amd64":   "c7b61e87b8430e42b04ab84fbe1b3b41b563454b0181247fd04844b8e9194371",
				"linux-arm64":   "9dbf6dd22cfdf8b85b916505a5e96e1721d7af4cbe2f3dc90b87c9d677d01636",
				"windows-amd64": "8bd4ae58b8657f9afd82c76f28e06232b0e8f994e949176206425dcc6005936a",
			},
		},
		{
			Name:          "markitdown",
			Version:       "0.1.1",
			Type:          PythonPackage,
			Description:   "Convert various file formats to Markdown (infrastructure-only: installed via uv but not yet consumed by the agent)",
			BinName:       "markitdown",
			PipSpec:       "markitdown[all]",
			PythonVersion: "3.12",
			// PythonPackage tools use uv for bootstrap; URLs/Checksums are nil.
			URLs:      nil,
			Checksums: nil,
		},
	}

	// Apply per-tool overrides for tools whose upstream naming convention
	// differs from PlatformTriple for specific platforms.
	platform := Platform()
	for i := range tools {
		tools[i].ArchiveName = archiveNameForPlatform(tools[i], platform)
		if tools[i].BinPathInArchiveOverride != "" {
			tools[i].BinPathInArchive = tools[i].BinPathInArchiveOverride
		}
	}

	return tools, nil
}

// archiveNameForPlatform resolves the local cache filename for a tool on the
// given platform. The download URL is the source of truth for the archive
// format — upstream ships ".zip" on Windows but ".tar.gz" elsewhere — so
// deriving the name from the URL basename keeps the on-disk extension
// consistent with the actual bytes and lets the installer pick the correct
// extractor. ArchiveNameOverride wins when set; the triple-derived
// ArchiveName is the fallback when there is no URL for the platform.
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
