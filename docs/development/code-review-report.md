# Comprehensive Code Review Report

**Date:** 2026-06-05  
**Scope:** Full codebase audit — `sdk/`, `core/`, `backend/`, `desktop/`, `frontend/src/`  
**Methodology:** 12 independent review passes across 4 layers, 3 perspectives each (correctness, architecture/design, impact/maintainability)  
**Verification:** Each issue verified against actual codebase on 2026-06-05.

---

## Executive Summary

| Severity              | Count |
| --------------------- | ----- |
| Critical (MUST FIX)   | 5     |
| Warning (SHOULD FIX)  | 34    |
| Suggestion (CONSIDER) | 37    |

The codebase is architecturally sound with correct layering, good separation of concerns, and careful concurrency practices overall. The critical issues concentrate in **security** (prompt injection, tool policy bypass, unsafe confirmation fallback) and **correctness** (cross-session policy leak, incomplete execution signaling). The warning-level issues are predominantly about **maintainability** (oversized files/functions, missing tests for critical paths) and **correctness edge cases** (race conditions, UTF-8 truncation, policy leakage across sessions).

---

## Critical Issues (MUST FIX)

### C-1. AGENTS.md prompt injection vector

**Layer:** Core  
**Files:** [`core/systemprompt.go#L123-L140`](/Users/vkochetkov/Repositories/c0wrk/core/systemprompt.go), [`core/orchestrator.go#L442-L505`](/Users/vkochetkov/Repositories/c0wrk/core/orchestrator.go)

**Problem:** `formatAgentsMD` injects AGENTS.md with strong trust language: "you MUST strictly follow these instructions". `injectVectorSearchHints` reads the file without size limits via `os.ReadFile`. Content is wrapped in `<agents-md>` tags but no injection-defense mechanism (like the untrusted-content tagging used for tool outputs) is applied. Any compromised or malicious AGENTS.md in a workspace can override tool policies and system instructions.

**Fix:**

- Downgrade AGENTS.md to advisory (not authoritative) — replace "MUST strictly follow" with "consider these project-specific guidelines"
- Wrap content inside the untrusted-content mechanism (`InjectionDefenseKey` context) used for tool outputs
- Add a configurable size cap (e.g. 64KB) with line-boundary truncation and informational hint insertion before injection

---

### C-2. Tool policy `user_confirm` bypassed for workspace/temp paths

**Layer:** Core  
**File:** [`core/tools/registry.go#L228-L240`](/Users/vkochetkov/Repositories/c0wrk/core/tools/registry.go)

**Problem:** Auto-approval logic at lines 231-240 executes tools without consulting `PolicyUserConfirm` or the ToolJudge when all input paths are inside the workspace or temp directory. The comment explicitly states "regardless of policy (except AlwaysDeny)". This applies to `bash_exec` — a command like `./scripts/exfiltrate.sh` with working_directory inside workspace will auto-execute despite `user_confirm` policy.

**Fix:**

- Never weaken an explicit `user_confirm` policy via auto-approval

---

### C-3. Skill-derived tool policies leak across sessions via shared ToolRegistry

**Layer:** Core  
**Files:** [`core/orchestrator.go#L664-L667`](/Users/vkochetkov/Repositories/c0wrk/core/orchestrator.go), [`core/tools/registry.go#L174-L180`](/Users/vkochetkov/Repositories/c0wrk/core/tools/registry.go), [`core/builder.go#L348`](/Users/vkochetkov/Repositories/c0wrk/core/builder.go)

**Problem:** `Build()` passes the shared `b.registry` as `CoreToolRegistry` (line 348). Each `HandleMessage` calls `o.coreToolRegistry.SetSkillPolicyOverrides(skillOverrides)` (line 666), writing task-specific skill policies to the process-wide `ToolRegistry`. These overrides persist until the next request that happens to set new ones — creating a cross-session security and information leak. The code comment at line 660-663 acknowledges "the tool registry is shared across the task" but doesn't address cross-session impact.

**Fix (recommended — context-based):**

- Clone a per-session tool registry in `Build()` 

---

### C-4. Tool confirmation falls back to "allow once" when UI context is missing

**Layer:** Desktop  
**File:** [`desktop/startup.go#L237-L246`](/Users/vkochetkov/Repositories/c0wrk/desktop/startup.go)

**Problem:** If `a.ctx == nil` (lines 239-241) or `sessionID == ""` (lines 244-245), the confirmation callback silently auto-approves with `ConfirmAllowOnce`. This degrades the safety mechanism to permissive in exactly the cases where the UI cannot review the request (e.g., during startup race conditions or background tasks without session context).

**Fix:**

- Change fallback to `ConfirmDenyAndStop` (conservative: deny when no UI present)
- Log a warning at `slog.Warn` level when this condition is hit to surface the issue
- Consider returning an error explaining the unavailability instead of a silent approval

---

### C-5. Incomplete plan execution reported as success (nil error)

**Layer:** SDK  
**File:** [`sdk/orchestration/orchestrator.go#L196-L221`](/Users/vkochetkov/Repositories/c0wrk/sdk/orchestration/orchestrator.go)

**Problem:** When `executePlanWithSteps` fails and not all steps are executed (line 210: `!allExecuted`), the function returns `ExecutionResult` with **nil error** (line 220). While the output string contains `[Execution incomplete: ...]`, callers cannot programmatically distinguish success from partial execution without parsing the output string. This violates Go's error-handling contract.

**Fix:** Return both the best-effort result and a non-nil sentinel error:

```go
var ErrExecutionIncomplete = errors.New("plan execution incomplete")
// ...
return result, fmt.Errorf("%w: %v", ErrExecutionIncomplete, execErr)
```

Callers must then use `errors.Is(err, ErrExecutionIncomplete)` to detect partial execution while still accessing the result.

---

## Warnings (SHOULD FIX)

### SDK Layer

#### W-1. MapBlackboard.SetFacts breaks thread-safety by aliasing caller-owned slice

[`sdk/orchestration/blackboard.go#L322-L327`](/Users/vkochetkov/Repositories/c0wrk/sdk/orchestration/blackboard.go)

**Problem:** `SetFacts` stores the caller's slice directly (`b.facts = facts`) without copying, violating the documented thread-safety contract. If callers mutate the slice after calling SetFacts, shared mutable state exists outside the mutex. Note: `GetFacts` (lines 310-320) correctly copies on read — `SetFacts` should do the same on write.

**Fix:** Defensively copy the slice and nested `Keywords` slices:

```go
func (b *MapBlackboard) SetFacts(facts []Fact) {
    b.mu.Lock()
    defer b.mu.Unlock()
    b.facts = make([]Fact, len(facts))
    for i, f := range facts {
        kwCopy := make([]string, len(f.Keywords))
        copy(kwCopy, f.Keywords)
        b.facts[i] = Fact{Keywords: kwCopy, Content: f.Content, Author: f.Author}
    }
}
```

---

#### W-2. ModelRegistry keeps reference to external overrides map

[`sdk/llm/modelregistry.go#L52-L66`](/Users/vkochetkov/Repositories/c0wrk/sdk/llm/modelregistry.go)

**Problem:** `NewModelRegistry` stores the `overrides` map directly (line 56). The comment "no lock needed for read-only map" assumes callers never mutate it — a fragile invariant that breaks if config updates modify overrides concurrently.

**Fix:** Copy the overrides map at construction time:

```go
copied := make(map[string]ModelMetadata, len(overrides))
for k, v := range overrides { copied[k] = v }
registry.overrides = copied
```

---

#### W-3. Monolithic `Executor.Run` (~830 LOC)

[`sdk/agent/executor.go#L350-L1178`](/Users/vkochetkov/Repositories/c0wrk/sdk/agent/executor.go)

**Problem:** Single function (~830 lines) mixes LLM request assembly, multiple circuit breakers (repeat/truncation/fruitless/same-tool/parse-error), tool execution, compaction orchestration, and step-limit negotiation. Makes isolated testing and safe modification very difficult.

**Fix:** Extract into smaller helpers: `handleStepLimit`, `callLLMWithRetry`, `handleImplicitFinish`, `processToolCalls`, `handleCompaction`, `checkCircuitBreakers`.

---

#### W-4. Import-time `panic` in netcheck init

[`sdk/tools/builtins/netcheck.go#L13-L38`](/Users/vkochetkov/Repositories/c0wrk/sdk/tools/builtins/netcheck.go)

**Problem:** A typo in any CIDR literal will panic the entire binary at startup. While all current CIDRs are valid constants (verified), the pattern is risky for maintainability — any future edit could crash the app at init.

**Fix:** Store init error in a package-level variable; gate `isPrivateIP` behavior on that error. Alternatively, add a `_test.go` init test that verifies all CIDRs parse successfully (defense against regressions).

---

### Core Layer

#### W-5. Byte-based string truncation corrupts UTF-8

[`core/orchestrator.go#L463`](/Users/vkochetkov/Repositories/c0wrk/core/orchestrator.go), [`core/tools/judge.go#L173`](/Users/vkochetkov/Repositories/c0wrk/core/tools/judge.go), [`core/planner.go`](/Users/vkochetkov/Repositories/c0wrk/core/planner.go)

**Problem:** Multiple places truncate strings using byte indices (`summary[:100]`, `abbrevReasoning[:120]`), which can split multi-byte UTF-8 characters, producing invalid output sent to LLMs and frontends.

**Fix:** Introduce a shared `truncateUTF8(s string, maxBytes int) string` helper in a `core/internal/strutil` package (or similar) that respects rune boundaries:

```go
func TruncateUTF8(s string, maxBytes int) string {
    if len(s) <= maxBytes { return s }
    for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) { maxBytes-- }
    return s[:maxBytes]
}
```

---

#### W-6. Data race on `OrchestratorBuilder.vectorSearchFunc`

[`core/builder.go#L597-L606`](/Users/vkochetkov/Repositories/c0wrk/core/builder.go)

**Problem:** `RegisterVectorSearch` writes `b.vectorSearchFunc` (line 601) without holding the mutex, while `OptimizePrompt` (line 712) reads it under `b.mu.RLock()`. This is a data race detectable by `-race`.

**Fix:** Protect the write with `b.mu.Lock()` in `RegisterVectorSearch`:

```go
func (b *OrchestratorBuilder) RegisterVectorSearch(...) {
    if searchFunc == nil { return }
    b.mu.Lock()
    b.vectorSearchFunc = searchFunc
    b.mu.Unlock()
    b.registry.Register(builtins.NewVectorSearchTool(searchFunc, waitFunc))
    // ...
}
```

---

#### W-7. MCP Gateway config comparison mixes raw and expanded values

[`core/tools/mcp/gateway.go#L283-L327`](/Users/vkochetkov/Repositories/c0wrk/core/tools/mcp/gateway.go)

**Problem:** `configChanged` compares stored `ServerEntry` (from `g.config.Servers[name]`, containing raw `${VAR}` placeholders in Env/Headers) against newly-built `ServerConfig` (with expanded values). Comment at line 307 claims "we compare the expanded values" but `oldEntry.Env` is raw. This causes false positives and unnecessary server reconnect cycles every time `Reconfigure` is called.

**Fix:** Store the expanded `ServerConfig` (post-env-expansion) alongside the raw config, or expand the stored raw entry before comparison. Simplest: change `g.config` to store a parallel `map[string]ServerConfig` of expanded values used only for comparison.

---

#### W-8. Orchestrator concurrency contract is implicit

[`core/orchestrator.go#L80-L114`](/Users/vkochetkov/Repositories/c0wrk/core/orchestrator.go)

**Problem:** `conversationHistory` and other mutable fields have no synchronization. The concurrency contract IS documented in a comment (lines 103-107: "One active request per *Orchestrator"), but there's no enforcement mechanism. A concurrent `HandleMessage` call would cause silent data races.

**Fix:** Add a `sync.Mutex` guarding `HandleMessage` entry with a `TryLock` pattern that returns an error if a request is already in-flight. This makes the contract enforceable rather than advisory.

---

#### W-9. `Orchestrator.HandleMessage` too large (~310 LOC)

[`core/orchestrator.go#L540-L849`](/Users/vkochetkov/Repositories/c0wrk/core/orchestrator.go)

**Problem:** Mixes blackboard lifecycle, routing, skill activation, clarification handling, plan generation, and SDK delegation in a single method.

**Fix:** Factor into private methods: `prepareBlackboard`, `routeAndActivateSkills`, `handleClarification`, `executeFirstMessage`, `executeContinuation`.

---

#### W-10. Stringly-typed configuration for domains, execution modes, and tool names

[`core/orchestrator.go`](/Users/vkochetkov/Repositories/c0wrk/core/orchestrator.go), [`core/router.go`](/Users/vkochetkov/Repositories/c0wrk/core/router.go), [`core/stepconfig.go`](/Users/vkochetkov/Repositories/c0wrk/core/stepconfig.go), [`core/toolprofiles.go`](/Users/vkochetkov/Repositories/c0wrk/core/toolprofiles.go)

**Problem:** Many contracts use bare strings (`"normal"`, `"code"`, `"read_file"`) spread across files with no single source of truth. Renaming requires multi-site updates.

**Fix:** Introduce shared constants and typed enums in a central location (e.g. `core/domains.go`, `core/toolnames.go`).

---

#### W-11. Planner family resolution ignores configured model

[`core/planner.go#L428-L438`](/Users/vkochetkov/Repositories/c0wrk/core/planner.go)

**Problem:** `getFamily` calls `p.modelRegistry.Resolve(ctx, "")` (line 433) instead of using `p.model` (field at line 348, settable via `SetModel`). This potentially selects wrong family-specific prompts when a non-default model is configured.

**Fix:** Pass `p.model` to `Resolve`:

```go
meta, _ := p.modelRegistry.Resolve(ctx, p.model)
```

---

#### W-12. Global proxy and HTTP transport side effects

[`core/builder.go#L81-L96`](/Users/vkochetkov/Repositories/c0wrk/core/builder.go), [`core/proxy.go#L79-L112`](/Users/vkochetkov/Repositories/c0wrk/core/proxy.go)

**Problem:** `NewOrchestratorBuilder` mutates `http.DefaultTransport` (line 92) and environment variables via `SetProxyEnvVars` (line 88), affecting all HTTP clients in the process including third-party libraries.

**Fix:** Prefer passing explicit `*http.Client` to all subsystems that need proxy support rather than mutating globals.

---

#### W-13. Path and symlink detection is POSIX-centric

[`core/tools/symlink.go#L79-L95`](/Users/vkochetkov/Repositories/c0wrk/core/tools/symlink.go), [`core/tools/judge.go#L20-L21`](/Users/vkochetkov/Repositories/c0wrk/core/tools/judge.go)

**Problem:** `looksLikePath` (line 85) checks only for `/` and `pathRegex` (judge.go line 21: `/[a-zA-Z0-9/_.\-~]+`) only matches `/`-based paths. Windows-style paths (`C:\...`) are never recognized, making safety heuristics ineffective on Windows.

**Fix:** Add `filepath.Separator` check and drive-letter pattern (`[A-Za-z]:\\`) matching. 

---

#### W-14. Hidden nil contract on `BuilderConfig.ExpandEnvVars`

[`core/builderconfig.go#L20-L23`](/Users/vkochetkov/Repositories/c0wrk/core/builderconfig.go)

**Problem:** Core code calls `cfg.ExpandEnvVars(...)` without nil checks. A caller constructing `BuilderConfig` without this field gets a nil-pointer panic.

**Fix:** Validate non-nil at `NewOrchestratorBuilder` entry, or provide a neutral default:

```go
if cfg.ExpandEnvVars == nil {
    cfg.ExpandEnvVars = func(s string) string { return s }
}
```

---

#### W-15. Compaction strategy ignores routing complexity (always 3)

[`core/stepconfig.go#L179`](/Users/vkochetkov/Repositories/c0wrk/core/stepconfig.go)

**Problem:** `applyCompactionStrategy(profile.Domain, 3)` hardcodes complexity=3, making hierarchical compaction unreachable for "general"/"mixed" domains even for high-complexity tasks.

**Fix:** Thread the actual routing complexity score (from `WithComplexity` context value) into the step configurator and pass it to `applyCompactionStrategy`.

---

#### W-16. `OrchestratorBuilder` is a god object (~1180 LOC)

[`core/builder.go`](/Users/vkochetkov/Repositories/c0wrk/core/builder.go)

**Problem:** ~1180 lines owning tool registry, security policies, MCP gateway, LLM router, proxy config, vector search, skills, and prompt optimization. Violates Single Responsibility.

**Fix:** Split into focused collaborators: `ToolRegistryManager`, `LLMRouterManager`, `MCPGatewayManager`, `ProxyManager`, `PromptOptimizationService`. Builder becomes a thin coordinator.

---

#### W-17. Critical configuration paths lack dedicated tests

[`core/builder.go`](/Users/vkochetkov/Repositories/c0wrk/core/builder.go), [`core/proxy.go`](/Users/vkochetkov/Repositories/c0wrk/core/proxy.go), [`core/systemprompt.go`](/Users/vkochetkov/Repositories/c0wrk/core/systemprompt.go), [`core/context_wrapper.go`](/Users/vkochetkov/Repositories/c0wrk/core/context_wrapper.go)

**Problem:** These critical files have no dedicated test files despite governing security posture, reasoning correctness, connectivity, and layering.

**Fix:** Add focused tests for builder wiring, proxy behavior, system prompt construction, and context helpers.

---

#### W-18. Prompt text partly in code, partly in templates

[`core/planner.go#L44-L325`](/Users/vkochetkov/Repositories/c0wrk/core/planner.go)

**Problem:** Large raw strings (planModePreamble, ToT sections, guidance) are inline in `planner.go` while other prompts use embedded `.md` templates. Splits the source of truth.

**Fix:** Move mode-specific preambles into embedded markdown templates matching existing patterns in `core/prompts/`.

---

### Backend/Desktop Layer

#### W-19. Missing tests for desktop startup orchestration

[`desktop/startup.go`](/Users/vkochetkov/Repositories/c0wrk/desktop/startup.go)

**Problem:** `Startup` is a large function (~500 LOC) orchestrating config, DB, stores, callbacks, and events with no functional tests (only a benchmark in `startup_bench_test.go`).

**Fix:** Add integration tests using a fake Wails runtime, temp AgentDir, and in-memory SQLite.

---

#### W-20. Incomplete test coverage for FrontendAPI config/MCP methods

[`backend/frontend_api_config.go`](/Users/vkochetkov/Repositories/c0wrk/backend/frontend_api_config.go), [`backend/frontend_api_mcp.go`](/Users/vkochetkov/Repositories/c0wrk/backend/frontend_api_mcp.go)

**Problem:** Config mutation methods (`UpdateLLMSettings`, `UpdateProxySettings`, etc.) that persist to disk and reconfigure the builder have no direct tests.

**Fix:** Test using a mock builder and temporary config file, verifying mutations and builder method invocations.

---

#### W-21. Session manager concentrates too many responsibilities (~1330 LOC)

[`backend/session/manager.go`](/Users/vkochetkov/Repositories/c0wrk/backend/session/manager.go)

**Problem:** Single file owns session lifecycle, orchestration construction, token persistence, task persistence, lazy restoration, blackboard retrieval, and shutdown.

**Fix:** Split into focused files: `manager_core.go`, `manager_restore.go`, `manager_lifecycle.go`, `manager_execution.go`.

---

#### W-22. Limited tests for `backend.Application` orchestration

[`backend/application.go#L64-L218`](/Users/vkochetkov/Repositories/c0wrk/backend/application.go)

**Problem:** Central hub between desktop and core has no direct tests verifying composition of emit functions, session store wiring, or judge/MCP status behavior.

**Fix:** Add tests with fake builder and manager verifying wiring contracts.

---

#### W-23. No tests for Wails event listener wiring

[`desktop/startup.go#L616-L905`](/Users/vkochetkov/Repositories/c0wrk/desktop/startup.go)

**Problem:** Event listeners parsing untyped `map[string]any` payloads and dispatching to channels have no tests.

**Fix:** Abstract `wailsRuntime.EventsOn` behind an interface; test valid/invalid payloads.

---

#### W-24. Monolithic `Startup` function

[`desktop/startup.go`](/Users/vkochetkov/Repositories/c0wrk/desktop/startup.go)

**Problem:** ~500 lines with nested goroutines and multiple phases in a single function.

**Fix:** Factor into cohesive internal helpers per phase: `initConfig`, `initDatabase`, `initStores`, `wireCallbacks`, `startAsyncServices`.

---

#### W-25. FileCoherenceTracker mutex map never shrinks

[`backend/session/file_coherence.go#L25-L45`](/Users/vkochetkov/Repositories/c0wrk/backend/session/file_coherence.go)

**Problem:** `fileMutexes` map grows monotonically with unique paths but is never pruned, even after sessions are deleted.

**Fix:** Clear `fileMutexes` when all sessions for a project are gone, or add a periodic sweep based on inactivity.

---

#### W-26. `GetSessionWorkspace` ignores sessionID parameter

[`backend/frontend_api_workspace.go#L290-L300`](/Users/vkochetkov/Repositories/c0wrk/backend/frontend_api_workspace.go)

**Problem:** Function takes `sessionID` but always returns `f.activeProjectPath` (lines 292-299). Misleading API that hardcodes single-active-project assumption.

**Fix:** honor `sessionID` via session manager lookup.

---

#### W-27. Default DEBUG log level + LLM dump for all sessions

[`backend/config/defaults.go#L24-L27`](/Users/vkochetkov/Repositories/c0wrk/backend/config/defaults.go)

**Problem:** Default install sets `cfg.LogLevel = "DEBUG"` (line 26), creating per-session LLM dump files and verbose debug logs without explicit opt-in, causing rapidly growing log directories.

**Fix:** Default to `"INFO"`; make `"DEBUG"` an explicit configuration choice in `config.example.yaml`.

---

### Frontend Layer

#### W-28. ChatInput component oversized (~291 LOC, multi-responsibility)

[`frontend/src/components/chat/ChatInput.tsx`](/Users/vkochetkov/Repositories/c0wrk/frontend/src/components/chat/ChatInput.tsx)

**Problem:** Mixes editor lifecycle, mode switching, resize logic, prompt optimization, and send controls.

**Fix:** Extract `useChatInputController` hook, `ChatInputToolbar`, and `ChatEditorPane` subcomponents.

---

#### W-29. FileViewerContent oversized (~345 LOC)

[`frontend/src/components/fileViewer/FileViewerContent.tsx`](/Users/vkochetkov/Repositories/c0wrk/frontend/src/components/fileViewer/FileViewerContent.tsx)

**Problem:** Combines data loading, Wails integration, and full CodeMirror editor implementation.

**Fix:** Extract `useFileViewerData` hook and `CodeMirrorFileViewer` component.

---

#### W-30. VectorStorePanel oversized (~329 LOC)

[`frontend/src/components/layout/VectorStorePanel.tsx`](/Users/vkochetkov/Repositories/c0wrk/frontend/src/components/layout/VectorStorePanel.tsx)

**Problem:** Mixes search form state, network orchestration, global event handling, and results rendering.

**Fix:** Extract `useVectorSearch` hook; split UI into `VectorSearchFilters`, `VectorSearchStatus`, `VectorSearchResults`.

---

#### W-31. VectorStorePanel/Terminal event handlers bypass available type guards

[`frontend/src/components/layout/VectorStorePanel.tsx#L86-L96`](/Users/vkochetkov/Repositories/c0wrk/frontend/src/components/layout/VectorStorePanel.tsx), [`frontend/src/components/terminal/Terminal.tsx#L51-L56`](/Users/vkochetkov/Repositories/c0wrk/frontend/src/components/terminal/Terminal.tsx)

**Problem:** VectorStorePanel uses ad-hoc shape checks (`data && typeof data === 'object' && 'state' in data` → `data as Record<string, unknown>`) instead of a proper type guard. Terminal accesses `data.data` directly without runtime validation.

**Fix:** Create and use shared type guards (e.g. `isVectorIndexStatusEvent`, `isTerminalOutputEvent`) consistently at all event boundaries.

---

#### W-32. Global double-click suppression partially hurts accessibility

[`frontend/src/main.tsx#L11-L29`](/Users/vkochetkov/Repositories/c0wrk/frontend/src/main.tsx)

**Problem:** Document-wide `preventDefault()` on double/triple clicks prevents word/paragraph selection everywhere except form elements (input, textarea, contentEditable). Read-only content like code blocks and markdown output cannot be word-selected via double-click.

**Fix:** Narrow scope to specific interactive containers via a `[data-no-select]` attribute, or add `[data-allow-selection]` opt-in to read-only content areas (code blocks, markdown viewer, file viewer).

---

#### W-33. File search re-scans entire workspace tree on each query

[`frontend/src/hooks/useFileSearch.ts#L36-L55`](/Users/vkochetkov/Repositories/c0wrk/frontend/src/hooks/useFileSearch.ts)

**Problem:** `listDirectory(rootPath, true)` walks the entire workspace recursively on each debounced keystroke (line 40). Performance degrades on large repos.

**Fix:** Cache the directory listing per project (e.g. in `fileTreeStore`), invalidated on `workspace:tree_changed` events. The search filter then operates on the cached list.

---

#### W-34. Prompt optimization errors are silent to the user

[`frontend/src/components/chat/ChatInput.tsx#L94-L96`](/Users/vkochetkov/Repositories/c0wrk/frontend/src/components/chat/ChatInput.tsx)

**Problem:** Failed `optimizePrompt` only calls `logger.error` (line 95) with no user-visible feedback. Users click the sparkles button and nothing happens.

**Fix:** Surface a transient toast notification or inline error near the optimize button (e.g. "Optimization failed — try again").

---

## Suggestions (CONSIDER)

### SDK Layer

| #   | Issue                                                                                                                      | File                                                   |
| --- | -------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------ |
| S-1 | Built-in model name helpers rebuild full registry on every call                                                            | `sdk/llm/modelregistry.go#L594-L617`                   |
| S-2 | `bash_exec` allows arbitrary `working_directory` without tool-level workspace containment (registry policy serves as gate) | `sdk/tools/builtins/bash.go#L133-L139`                 |
| S-3 | Embedder/ONNX runtime lifecycle is global, non-reference-counted                                                           | `sdk/embedding/embedder.go#L84-L193`                   |
| S-4 | Tight coupling to specific tool names via switch statements                                                                | `sdk/agent/executor.go#L231-L330`                      |
| S-5 | Duplication of "effective context window" logic between router and memory                                                  | `sdk/llm/router.go#L170` / `sdk/memory/context.go#L84` |
| S-6 | Global `privateNetworks` slice could be made explicitly immutable                                                          | `sdk/tools/builtins/netcheck.go#L9-L37`                |
| S-7 | Step-history token estimation doesn't match actual message layout                                                          | `sdk/memory/context.go#L160-L168`                      |
| S-8 | Central behaviors in large inline system strings with tool references                                                      | `sdk/agent/executor.go#L20-L63`                        |
| S-9 | Router/Executor don't explicitly state concurrency guarantees                                                              | `sdk/llm/router.go#L33` / `sdk/agent/executor.go#L65`  |

### Core Layer

| #    | Issue                                                                                  | File                                                      |
| ---- | -------------------------------------------------------------------------------------- | --------------------------------------------------------- |
| S-10 | Symlink/path heuristics Unix-centric (Windows unsupported)                             | `core/tools/symlink.go#L79`                               |
| S-11 | AGENTS.md size is unbounded in prompts (partially overlaps C-1)                        | `core/orchestrator.go#L475-L495`                          |
| S-12 | Explicitly document orchestrator lifecycle and session scope                           | `core/types.go#L309`                                      |
| S-13 | Centralize domain→compaction-strategy mapping                                          | `core/router.go#L204` / `core/stepconfig.go#L179`         |
| S-14 | Use composition instead of embedding sdk.ToolRegistry                                  | `core/tools/registry.go#L41-L69`                          |
| S-15 | Separate prompt content from planner logic further                                     | `core/planner.go#L31-L216`                                |
| S-16 | Review whether skills/MCP belong in sdk for reuse                                      | `core/skills/`, `core/tools/mcp/`                         |
| S-17 | MCP Gateway schema sanitizer implemented and tested but not wired from production code | `core/tools/mcp/gateway.go#L47-L53`                       |
| S-18 | Codify layering invariant (`core` must not import `backend`) in tooling                | `core/`                                                   |
| S-19 | SkillManager path containment helper duplication                                       | `core/skills/manager.go#L131` / `core/skills/tool.go#L97` |
| S-20 | Reflector's `modelRegistry` field is set but never read                                | `core/reflector.go#L26-L38`                               |
| S-21 | Router/planner JSON extraction is fragile and duplicated                               | `core/router.go#L129` / `core/planner.go#L1057`           |

### Backend/Desktop Layer

| #    | Issue                                                                               | File                                              |
| ---- | ----------------------------------------------------------------------------------- | ------------------------------------------------- |
| S-22 | FrontendAPI central façade is large but cohesive — consider sub-façades             | `backend/frontend_api.go#L16-L57`                 |
| S-23 | Event contract types spread across multiple packages                                | `backend/events.go` / `backend/session/events.go` |
| S-24 | Narrow dependencies exposed from vectorindex.Manager                                | `backend/vectorindex/manager.go#L133`             |
| S-25 | Vector index Manager.SwitchProject is a god method                                  | `backend/vectorindex/manager.go#L143-L283`        |
| S-26 | Workspace API mixes git/fs/meta concerns in one file                                | `backend/frontend_api_workspace.go`               |
| S-27 | Terminal Manager could tie lifetime to app root context                             | `backend/terminal/manager.go#L71-L115`            |
| S-28 | Document single-active-project assumption explicitly                                | `backend/frontend_api_project.go#L142`            |
| S-29 | `OpenDatabase` assumes non-nil logger                                               | `backend/database.go#L11-L31`                     |
| S-30 | Repeated dynamic regexp compilation in `preprocessMessageText`                      | `backend/frontend_api_session.go#L303,L333`       |
| S-31 | Test coverage for small but important helpers (snapToTheme, gitIgnored, verifyDeps) | Various                                           |

### Frontend Layer

| #    | Issue                                                               | File                                                   |
| ---- | ------------------------------------------------------------------- | ------------------------------------------------------ |
| S-32 | Loosely-typed Wails App bindings (`any`) reduce compile-time safety | `api/runtime.ts#L6-L12`                                |
| S-33 | Some RPC wrappers skip runtime shape validation                     | `api/workspace.ts`, `api/config.ts`, `api/terminal.ts` |
| S-34 | `dangerouslySetInnerHTML` in BashBody — document safety contract    | `components/chat/toolCards/bodies/BashBody.tsx`        |
| S-35 | Routing/plan messages rely on implicit metadata contracts           | `lib/chatUtils.ts#L12-L24`                             |
| S-36 | User-facing strings have no localization boundaries                 | `components/chat/ServiceMessage.tsx`                   |
| S-37 | Plain string event names for global Wails events (not type-safe)    | `api/runtime.ts#L47-L58`                               |
| S-38 | XTerm/CodeMirror themes don't model multi-theme evolution           | `hooks/useXTermTheme.ts`, `lib/cmTheme.ts`             |
| S-39 | A11y: Markdown toggle relies on `title` only (no `aria-label`)      | `components/MarkdownViewer.tsx#L18-L37`                |
| S-40 | Centralize shared debounce/threshold constants                      | Various hooks                                          |
| S-41 | Extract shared "border alignment with sidebars" pattern             | `ChatInput`, `ExecutionPanels`, `BlackboardPanel`      |
| S-42 | Reuse shared "latest-only" async pattern across hooks               | `hooks/useLatestAsync.ts`                              |
| S-43 | Vector index store reset behavior on project switch is implicit     | `stores/vectorIndexStore.ts`                           |
| S-44 | Consider typed global event wrapper (`onGlobalEvent<K>`)            | `api/runtime.ts`                                       |

---

## Summary of Changes Needed (Priority Order)

### Immediate (Security/Correctness)

1. Fix AGENTS.md injection vector (C-1)
2. Fix tool policy bypass for workspace paths (C-2)
3. Fix skill policy cross-session leak (C-3)
4. Fix confirmation fallback to "allow" (C-4)
5. Fix ExecutionResult error swallowing (C-5)

### Short-term (Correctness & Safety)

6. Add UTF-8-safe truncation helper (W-5)
7. Fix vectorSearchFunc race condition (W-6)
8. Fix planner family resolution (W-11)
9. Fix MapBlackboard.SetFacts thread-safety (W-1)
10. Fix ModelRegistry overrides map aliasing (W-2)
11. Fix MCP config comparison (W-7)
12. Use type guards at all event boundaries (W-31)

### Medium-term (Maintainability & Testing)

13. Add tests for critical untested paths (W-17, W-19, W-20, W-22, W-23)
14. Refactor oversized functions/components (W-3, W-9, W-16, W-21, W-24, W-28-W-30)
15. Introduce shared constants for stringly-typed config (W-10)
16. Default log level to INFO (W-27)
17. Fix file search performance (W-33)
18. Add ExpandEnvVars nil guard (W-14)

### Long-term (Architecture Evolution)

19. Refactor OrchestratorBuilder into focused collaborators
20. Move prompt text to templates
21. Add Windows path support
22. Extract frontend data-loading into reusable hooks
23. Wire MCP schema sanitizer in production
24. Remove unused Reflector.modelRegistry field
