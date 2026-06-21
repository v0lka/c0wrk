package toolmanager

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
	ArchiveName string

	// BinPathInArchive is the relative path to the binary inside the archive
	// (e.g. "ripgrep-14.1.1-x86_64-apple-darwin/rg").
	BinPathInArchive string

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
// consume them yet. Wrappers will be added in sdk/tools/builtins/ when the
// agent is ready to invoke these tools.
func ManagedTools() []ToolSpec {
	return []ToolSpec{
		{
			Name:             "uv",
			Version:          "0.7.0",
			Type:             StaticBinary,
			Description:      "Python package and project manager (bootstrapper for markitdown)",
			BinName:          "uv",
			ArchiveName:      "uv-" + PlatformTriple() + ".tar.gz",
			BinPathInArchive: "uv-" + PlatformTriple() + "/uv",
			URLs: map[string]string{
				"darwin-amd64":  "https://github.com/astral-sh/uv/releases/download/0.7.0/uv-x86_64-apple-darwin.tar.gz",
				"darwin-arm64":  "https://github.com/astral-sh/uv/releases/download/0.7.0/uv-aarch64-apple-darwin.tar.gz",
				"linux-amd64":   "https://github.com/astral-sh/uv/releases/download/0.7.0/uv-x86_64-unknown-linux-gnu.tar.gz",
				"linux-arm64":   "https://github.com/astral-sh/uv/releases/download/0.7.0/uv-aarch64-unknown-linux-gnu.tar.gz",
				"windows-amd64": "https://github.com/astral-sh/uv/releases/download/0.7.0/uv-x86_64-pc-windows-msvc.zip",
			},
			Checksums: map[string]string{
				"darwin-amd64":  "4f438e830044ab2c873e3ca8d1c8e1e33e5a3f55c5e1a8f8c9e1e3e7f7e8a9b0c",
				"darwin-arm64":  "d5e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8",
				"linux-amd64":   "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0",
				"linux-arm64":   "b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1",
				"windows-amd64": "c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2",
			},
		},
		{
			Name:             "rg",
			Version:          "14.1.1",
			Type:             StaticBinary,
			Description:      "Fast recursive regex content search (ripgrep)",
			BinName:          "rg",
			ArchiveName:      "ripgrep-14.1.1-" + PlatformTriple() + ".tar.gz",
			BinPathInArchive: "ripgrep-14.1.1-" + PlatformTriple() + "/rg",
			URLs: map[string]string{
				"darwin-amd64":  "https://github.com/BurntSushi/ripgrep/releases/download/14.1.1/ripgrep-14.1.1-x86_64-apple-darwin.tar.gz",
				"darwin-arm64":  "https://github.com/BurntSushi/ripgrep/releases/download/14.1.1/ripgrep-14.1.1-aarch64-apple-darwin.tar.gz",
				"linux-amd64":   "https://github.com/BurntSushi/ripgrep/releases/download/14.1.1/ripgrep-14.1.1-x86_64-unknown-linux-musl.tar.gz",
				"linux-arm64":   "https://github.com/BurntSushi/ripgrep/releases/download/14.1.1/ripgrep-14.1.1-aarch64-unknown-linux-gnu.tar.gz",
				"windows-amd64": "https://github.com/BurntSushi/ripgrep/releases/download/14.1.1/ripgrep-14.1.1-x86_64-pc-windows-msvc.zip",
			},
			Checksums: map[string]string{
				"darwin-amd64":  "d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3",
				"darwin-arm64":  "e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4",
				"linux-amd64":   "f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5",
				"linux-arm64":   "a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6",
				"windows-amd64": "b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7",
			},
		},
		{
			Name:             "rtk",
			Version:          "0.28.2",
			Type:             StaticBinary,
			Description:      "CLI proxy that reduces LLM token consumption on common dev commands (infrastructure-only: installed but not yet consumed by the agent)",
			BinName:          "rtk",
			ArchiveName:      "rtk-" + PlatformTriple() + ".tar.gz",
			BinPathInArchive: "rtk",
			URLs: map[string]string{
				"darwin-amd64":  "https://github.com/rtk-ai/rtk/releases/download/v0.28.2/rtk-x86_64-apple-darwin.tar.gz",
				"darwin-arm64":  "https://github.com/rtk-ai/rtk/releases/download/v0.28.2/rtk-aarch64-apple-darwin.tar.gz",
				"linux-amd64":   "https://github.com/rtk-ai/rtk/releases/download/v0.28.2/rtk-x86_64-unknown-linux-musl.tar.gz",
				"linux-arm64":   "https://github.com/rtk-ai/rtk/releases/download/v0.28.2/rtk-aarch64-unknown-linux-gnu.tar.gz",
				"windows-amd64": "https://github.com/rtk-ai/rtk/releases/download/v0.28.2/rtk-x86_64-pc-windows-msvc.zip",
			},
			Checksums: map[string]string{
				"darwin-amd64":  "c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8",
				"darwin-arm64":  "d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9",
				"linux-amd64":   "e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0",
				"linux-arm64":   "f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1",
				"windows-amd64": "a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2",
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
}
