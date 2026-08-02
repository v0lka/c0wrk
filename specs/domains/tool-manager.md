# Tool Manager

## Purpose

The tool-manager downloads, installs, and version-tracks the external CLI binaries c0wrk needs at runtime — `ripgrep` (`rg`), `uv`, and `markitdown` — into `~/.c0wrk/tools/`. It is the supply-chain boundary for runtime binary dependencies: every managed version is a compile-time pin in the registry, reconciled against a local `.versions` file on startup. It deliberately does not check upstream for newer releases or auto-update installed binaries — this is a defense against supply-chain attacks (a compromised or maliciously rotated upstream release cannot reach a user's machine without a developer explicitly raising the pin and shipping a new build).

## Key Files

- `core/toolmanager/registry.go` — `ManagedTools()` registry: per-tool pinned `Version`, per-platform download `URLs`, SHA256 `Checksums`, archive-layout metadata (`ArchiveName`, `BinPathInArchive`), and `PipSpec` for Python packages
- `core/toolmanager/manager.go` — `Manager.EnsureCriticalTools` reconciliation loop, version-mismatch handling, `removeStalePythonEnv`, `PrependToPATH`
- `core/toolmanager/install.go` — `FSInstaller`: archive extraction (`StaticBinary`), Python bootstrap (`uv python install` → `uv venv` → `uv pip install`), wrapper-script creation, zip-bomb guard
- `core/toolmanager/download.go` — HTTP downloader with retry and post-download SHA256 verification
- `core/toolmanager/versions.go` — `ToolVersions` map and `.versions` JSON read/write
- `core/toolmanager/platform.go` — `Platform()` / `PlatformTriple()` canonical naming
- `core/toolmanager/manager_diskcheck_unix.go` / `manager_diskcheck_windows.go` — best-effort pre-download disk-space guard
- `desktop/startup_phases.go` `initTools()` — wiring: `NeedsInstall()` early detection → splash → `EnsureCriticalTools` → fatal modal on failure → `PrependToPATH()` (Phase 2)

## Core Types

```go
type ToolType string
const (
	StaticBinary  ToolType = "static_binary" // self-contained archive (rg, uv)
	PythonPackage ToolType = "python_package" // pip-installable via uv (markitdown)
)

type ToolSpec struct {
	Name        string          // "rg", "uv", "markitdown"
	Version     string          // pinned upstream version (source of truth for reconciliation)
	Type        ToolType
	BinName     string
	URLs        map[string]string  // platform key -> download URL
	Checksums   map[string]string  // platform key -> SHA256 hex (empty disables verification)
	PipSpec     string          // pip install spec, pinned with == for determinism
	PythonVersion string
}

type ToolVersions map[string]string  // tool name -> installed version, persisted to .versions
```

## Flow

```
startup Phase 2: initTools()
│
├─ Manager.EnsureCriticalTools(ctx)
│   │
│   ├─ create ~/.c0wrk/tools/ dirs
│   ├─ checkDiskSpace(toolsDir, 200 MiB)        [best-effort; no-op on Windows]
│   ├─ ReadVersions(.versions)
│   │
│   └─ for each tool in ManagedTools() (dependency order: uv, rg, markitdown):
│         installed := .versions[tool]
│         mismatch  := installed != tool.Version
│         │
│         ├─ version matches AND binary present → skip
│         ├─ version matches BUT binary missing  → reinstall
│         ├─ version mismatch AND PythonPackage  → removeStalePythonEnv()
│         │      (delete wrapper + venv so the new version is actually installed)
│         │
│         ├─ installOne:
│         │    StaticBinary  → download + SHA256 verify + extract to bin/
│         │    PythonPackage → uv python install → uv venv → uv pip install <PipSpec>
│         │
│         └─ WriteVersions(.versions)  [persisted after each tool]
│
└─ return mgr.PrependToPATH() → tools/bin/ prepended to PATH (Phase 3)
```

## Invariants

**Supply-chain integrity:**

- Managed tool versions are compile-time pins in `ManagedTools()`; the reconciliation source of truth is source code, not the network.
- Installed tool versions advance only when a developer raises a pin in the registry and ships a new build. The tool-manager performs no upstream version queries and no automatic updates.
- A static-binary download is SHA256-verified against the registry checksum before extraction. An empty checksum disables verification and is reserved for staged rollouts.
- The Python package spec (`PipSpec`) carries an explicit `==<version>` pin so every install resolves to a deterministic, reviewable version rather than the PyPI "latest".

**Reconciliation correctness:**

- A pinned-version change always forces a reinstall. For `StaticBinary` tools the binary-presence check triggers re-download; for `PythonPackage` tools `removeStalePythonEnv` rebuilds the wrapper and virtual environment, because the wrapper-existence short-circuit in `InstallPythonPackage` would otherwise leave the previous package in place.
- The `.versions` file is rewritten after each successful tool install, so a partially completed bootstrap resumes on the next launch.

**Operational:**

- All `exec.CommandContext("rg", ...)` call sites resolve to the managed binary transparently, because `tools/bin/` is prepended to `PATH` at startup before Phase 3.
- The first run requires network access; every run after the managed tools are installed operates fully offline.

## Configuration

The tool registry is an embedded Go struct (`core/toolmanager/registry.go`), not a `config.yaml` surface — there are no user-tunable version knobs by design (tunability would undermine the supply-chain guarantee). Fixed operational thresholds:

| Parameter | Value | Location |
| --- | --- | --- |
| Minimum free disk space before download | 200 MiB | `manager.go` `checkDiskSpace` (best-effort; no-op on Windows) |
| Max decompressed size per archive entry (zip-bomb guard) | 512 MiB | `install.go` `maxExtractEntryBytes` |
| Download retry on transient failure | 1 retry | `manager.go` `installStatic` |

To remove all managed tools, delete `~/.c0wrk/tools/`.

## Extension Points

**Add a new managed tool:** append a `ToolSpec` to `ManagedTools()` in dependency order, provide per-platform `URLs` and SHA256 `Checksums` (verified against the upstream `sha256.sum` asset), set `BinPathInArchive` from `PlatformTriple()`, and add `archiveNameForPlatform`/override handling if upstream naming differs. Static binaries are then resolvable by name with no call-site changes (PATH prepend).

**Raise a pinned version (security/maintenance bump):** update `Version`, the version segment in every `URLs` entry, all five platform `Checksums`, and — for `PythonPackage` tools — the `==<version>` segment of `PipSpec`. The reconciliation loop forces the upgrade automatically for existing installs.

**CVE review before any bump (required):** raising a pin is the only mechanism that introduces new external code onto users' machines. Before bumping, query the current and target versions against a vulnerability database (e.g. OSV.dev / GHSA) for the exact affected ranges, and record the decision. Bumping to fix a CVE is safe; bumping to "latest" without review reopens the supply-chain surface the no-auto-update invariant exists to close.

## Related Specs

- [ADR-010: Tool Manager for External Binary Dependencies](../decisions/010-tool-manager.md) — decision record (accepted, immutable): why tools are downloaded rather than bundled, and why `git` stays a lazy system dependency
- [Security Model](../architecture/security-model.md) — defense-in-depth for *agent-invoked* tool execution; the tool-manager's supply-chain guarantee is the software-delivery counterpart
- [Tool System README](tool-system/README.md) — the agent's `ToolRegistry` consumes the managed `rg` binary transparently via PATH
- [Event Catalog](../contracts/event-catalog.md) — `tool_manager:start` / `tool_manager:progress` / `tool_manager:done` lifecycle events emitted during bootstrap
