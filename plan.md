# Fix Plan: Code Review Remediations

## Context

Code review of 5 commits (d3fb2d6, bcbc559, b23b590, 82664f8, 2cbabbd) identified 8 issues. User issued specific fix instructions for each. The plan covers all 6 fix tasks across symlink security, injection defense config wiring, tool trust classification, MCP-first line restoration, and spec updates.

**Key finding from git archaeology**: Commit `3d350ff` was only a roadmap doc change — irrelevant to rollbacks. Commit `d11d4d9` restored "Pre-Finish Verification" and "When finished, call finish tool" to model prompts, but did NOT restore the MCP-first line in `planner.go` (d11d4d9 didn't touch `core/planner.go`). Only that one planner line needs rollback.

---

## Task 1: Fix `isPathOutside` with `filepath.Rel`

**File**: `core/tools/symlink.go` (lines 328-352)

**Problem**: Uses `strings.HasPrefix` with trailing separator for workspace boundary check. While this handles sibling directories correctly (`/home/user/proj/` vs `/home/user/proj-other/`), `filepath.Rel` is the idiomatic Go approach and handles edge cases (case-insensitive filesystems, UNC paths, etc.) more robustly.

**Change**: Replace the `strings.HasPrefix`-based check with `filepath.Rel`:
```go
func isPathOutside(absPath, workspace string) bool {
    pathAbs := filepath.Clean(absPath)
    workspaceAbs := filepath.Clean(workspace)
    pathEvaled, err := filepath.EvalSymlinks(pathAbs)
    if err == nil {
        pathAbs = pathEvaled
    }
    workspaceEvaled, err := filepath.EvalSymlinks(workspaceAbs)
    if err == nil {
        workspaceAbs = workspaceEvaled
    }
    rel, err := filepath.Rel(workspaceAbs, pathAbs)
    if err != nil {
        return true // different volume roots → outside
    }
    // If rel starts with "..", the path escapes the workspace
    return strings.HasPrefix(rel, "..")
}
```

---

## Task 2: Fix `walkSymlinkComponents` to walk component-by-component

**File**: `core/tools/symlink.go` (lines 235-307)

**Problem**: The function returns immediately upon finding the first non-OS-level symlink. If a path has chained symlinks (e.g., `/ws/symlink1/real/symlink2/target`), only the first is detected. The second symlink's indirection is invisible because `filepath.Join(resolvedTarget, tail)` manually reconstructs the path without checking remaining components for additional symlinks.

**Change**: After detecting a symlink and resolving its target:
1. Construct the full resolved path: `resolvedTarget + tail`
2. Call `filepath.EvalSymlinks` on that full path
3. Compare the `EvalSymlinks` result with `workspaceAbs` to determine if it escapes the workspace
4. Return the fully-resolved traversal info

```go
// After os.Readlink(current) resolves a symlink target:
tail := filepath.Join(parts[i+1:]...)
fullResolved := filepath.Join(resolvedTarget, tail)
// Resolve any remaining symlinks in the chain
if evaled, err := filepath.EvalSymlinks(fullResolved); err == nil {
    fullResolved = evaled
}
return &SymlinkTraversal{
    Path:         originalAbsPath,
    SymlinkChain: components[:i+1], // path components up to and including the symlink
    Target:       target,
    FullResolved: fullResolved,
}
```

---

## Task 3: Wire `injection_defense.enabled` runtime gating

**Files**: 7 files in the config pipeline

### 3a. `backend/config/config.go` — Add config struct
Add `InjectionDefenseConfig` struct and field in `SecurityConfig`:
```go
type InjectionDefenseConfig struct {
    Enabled *bool `yaml:"enabled"` // *bool for nil-vs-false distinction
}

type SecurityConfig struct {
    Judge            JudgeConfig               `yaml:"judge"`
    InjectionDefense InjectionDefenseConfig    `yaml:"injection_defense"`
    ToolPolicies     map[string]ToolPolicyConfig `yaml:"tool_policies"`
    DefaultPolicy    string                    `yaml:"default_policy"`
}
```

### 3b. `backend/config/defaults.go` — Default to true
Add after existing security defaults:
```go
if cfg.Security.InjectionDefense.Enabled == nil {
    t := true
    cfg.Security.InjectionDefense.Enabled = &t
}
```

### 3c. `backend/configadapter.go` — Wire to builder config
Add deref helper and wire:
```go
func derefBool(b *bool) bool {
    if b == nil { return true }
    return *b
}
// In adapter function:
Security: core.BuilderSecurityConfig{
    JudgeModel:              cfg.Security.Judge.Model,
    InjectionDefenseEnabled: derefBool(cfg.Security.InjectionDefense.Enabled),
    ToolPolicies:            toolPolicies,
    DefaultPolicy:           cfg.Security.DefaultPolicy,
},
```

### 3d. `core/builderconfig.go` — Add field
```go
type BuilderSecurityConfig struct {
    JudgeModel              string
    InjectionDefenseEnabled bool
    ToolPolicies            map[string]BuilderToolPolicy
    DefaultPolicy           string
}
```

### 3e. `core/builder.go` — Thread flag to ContextWindow
In `buildContextFactory`, pass `cfg.Security.InjectionDefenseEnabled` to `NewContextWindow`:
```go
cw := sdkmemory.NewContextWindow(systemPrompt, modelMeta, tracker, thresholds, strategy,
    cfg.Executor.Compaction.SafetyMarginPercent, cfg.Security.InjectionDefenseEnabled, pruning...)
```

### 3f. `sdk/memory/context.go` — Gate wrapping calls
Add `injectionDefenseEnabled bool` field to `ContextWindow`. Update `NewContextWindow` signature. Gate the two `WrapUntrustedContent` call sites:
```go
if cw.injectionDefenseEnabled && cw.steps[idx].IsUntrusted {
    observation = security.WrapUntrustedContent(observation, gs.Action.Name, nil)
}
```

### 3g. `core/systemprompt.go` — Gate prompt injection
Gate the `Core(prompts.InjectionDefense)` line. Extract the flag from context (set upstream in builder):
```go
b := prompt.NewBuilder().
    Core(prompts.OrchestratorSystem).
    Core(prompts.FamilyPrompt("orchestrator", family)).
    Core(prompts.VerificationMandate)
if injectionDefenseEnabled { // from context or function param
    b.Core(prompts.InjectionDefense)
}
result := b.CacheBreak()...
```

For threading the flag to `buildSystemPrompt`: add a context key in `core/types.go`, set it in `builder.go`'s `Build()` method, read it in `buildSystemPrompt`.

---

## Task 4: Replace static `IsUntrustedTool` map with dynamic `Tool.IsUntrusted()`

**Files**: `sdk/tools/tool.go`, `sdk/tools/registry.go`, `sdk/agent/types.go`, `sdk/agent/executor.go`, 4 mock files

### 4a. `sdk/agent/types.go` — Extend `ToolExecutor` interface
Add method:
```go
type ToolExecutor interface {
    Execute(ctx context.Context, name string, input json.RawMessage) (result tools.ToolResult, err error)
    GetToolSource(name string) string
    IsToolUntrusted(name string) bool  // NEW
}
```

### 4b. `sdk/tools/registry.go` — Implement `IsToolUntrusted`
```go
func (r *ToolRegistry) IsToolUntrusted(name string) bool {
    r.mu.RLock()
    defer r.mu.RUnlock()
    tool, ok := r.tools[name]
    if !ok {
        return false
    }
    // Use the Tool interface's IsUntrusted() method
    if tool.IsUntrusted() {
        return true
    }
    // MCP-sourced tools are always untrusted
    if source, ok := r.toolSources[name]; ok && strings.HasPrefix(source, "mcp") {
        return true
    }
    return false
}
```

### 4c. `core/tools/registry.go` — Inherit via embedding
The core `ToolRegistry` embeds `*sdktools.ToolRegistry`, so `IsToolUntrusted` is inherited automatically. No change needed (same pattern as `GetToolSource`).

### 4d. `sdk/agent/executor.go` — Replace static call
Replace:
```go
isUntrusted := strings.HasPrefix(e.tools.GetToolSource(action.Name), "mcp") ||
    tools.IsUntrustedTool(action.Name)
```
With:
```go
isUntrusted := e.tools.IsToolUntrusted(action.Name)
```

### 4e. `sdk/tools/tool.go` — Remove static map
Remove `untrustedBuiltinTools` map and `IsUntrustedTool()` function (lines 193-209).

### 4f. Mock implementations — Add `IsToolUntrusted` method
Files: `sdk/agent/testhelpers_test.go` (2 mocks), `core/testhelpers_test.go` (1 mock), `sdk/orchestration/orchestrator_test.go` (1 mock). All default to `return false` for built-in mock tools.

### 4g. Verify built-in tools set `Untrusted: true`
Check that the 6 tools in the old static map (`web_search`, `web_fetch`, `bash_exec`, `ripgrep`, `glob`, `read_file`) have `Untrusted: true` set on their `BaseTool` construction in `sdk/tools/builtins/`. If not, add it.

---

## Task 5: Restore MCP-first line in `planner.go`

**File**: `core/planner.go` (insert after line 206)

**Commit `b23b590` removed** from `planModePreamble`:
```
-Step executors follow MCP-first tool priority: prefer MCP tools over built-in equivalents. Built-in tools are fallback. `bash_exec` is last resort. When writing step descriptions, direct executors to use project-specific MCP tools first, then built-in code exploration, then targeted search, then file operations.
```

**Verified**: d11d4d9 did NOT restore this (didn't touch planner.go). Only this line needs rollback.

**Change**: Insert the removed line after line 206 (the `Omit this field` bullet) and before line 207 (closing backtick):
```
  * Omit this field only when the step genuinely needs every available tool (rare; only for unbounded exploration tasks)
Step executors follow MCP-first tool priority: prefer MCP tools over built-in equivalents. Built-in tools are fallback. ` + "`bash_exec`" + ` is last resort. When writing step descriptions, direct executors to use project-specific MCP tools first, then built-in code exploration, then targeted search, then file operations.
`
```

---

## Task 6: Update specifications

### 6a. `specs/contracts/core-sdk.md` (line 60)
Replace reference to static `IsUntrustedTool()` with `ToolExecutor.IsToolUntrusted()` (which delegates to `Tool.IsUntrusted()` plus MCP source check).

### 6b. `specs/architecture/security-model.md` (lines 142-175)
Remove mention of `untrustedBuiltinTools` static map. Document that trust is determined by `Tool.IsUntrusted()` method on each tool instance, and all MCP-sourced tools are implicitly untrusted.

### 6c. `specs/domains/tool-system/README.md` (lines 115-116)
Update invariants to reference `Tool.IsUntrusted()` instead of the static list. Note that the `ToolExecutor` interface now exposes `IsToolUntrusted(name)` for consumers.

---

## Verification

1. **Unit tests**: Run `go test ./core/tools/... -v` — verify symlink boundary and chained-symlink tests pass
2. **Unit tests**: Run `go test ./sdk/agent/... -v` — verify executor tests with `IsToolUntrusted` pass
3. **Unit tests**: Run `go test ./sdk/memory/... -v` — verify context window wrapping gating
4. **Full test suite**: `make test` — all tests must pass
5. **Lint**: `make lint` — no new lint errors
6. **Config parsing**: Start the app, verify `injection_defense.enabled: false` in config suppresses `<untrusted-content>` wrapping and removes the injection defense prompt text
7. **Planner prompt**: Verify the restored MCP-first line appears in planner system prompt output
