# Fix All Code Review Issues for `cd6bd15`

## Context

The commit `cd6bd15` introduced centralized tool result caching and per-tool truncation. Code review found 17 issues across all severity levels. The user requires all of them fixed, plus enforcement of one critical architectural rule:

> **NO tool output must be truncated BEFORE reaching the cache. All truncation must happen exclusively in the centralized truncation pipeline.**

This means: tools return full output → full output goes into cache → then truncation pipeline applies Stage 1 (line/byte) → Stage 2 (token budget).

---

## Task 1: Fix `sdk/agent/executor.go` — Core pipeline fixes

Modify: [/Users/vkochetkov/Repositories/c0wrk/sdk/agent/executor.go](/Users/vkochetkov/Repositories/c0wrk/sdk/agent/executor.go)

### 1a. UTF-8 safe byte truncation (issue #1)
In `applyPerToolTruncation`, replace `content = content[:cfg.MaxBytes]` with a loop that walks back to the last valid UTF-8 boundary using `utf8.ValidString`. Add `"unicode/utf8"` import.

### 1b. Guard Stage 1 nudge from Stage 2 token-budget truncation (issue #7)
In the Stage 1 → Stage 2 flow (~lines 990-1006): when Stage 1 appends a fragmentation nudge, extract the nudge, apply Stage 2 truncation only to the body, then re-append the nudge. This prevents the SHA256 hash from being stripped.

### 1c. Workspace boundary validation in `buildCacheMeta` (issue #13)
Before calling `os.Stat(params.Path)`, validate that `params.Path` is within the workspace boundary. Extract the workspace path from context via `tools.WorkspacePathFrom(ctx)` (available at executor level) and use `filepath.HasPrefix` with a resolved absolute path check.

### 1d. Pass page-size to `formatFragmentationNudge` (issue #5)
Change `formatFragmentationNudge` to accept the per-tool `MaxLines` value and include it in the message instead of the generic "default page size". The nudge should say: `num_lines must not exceed %d`.

---

## Task 2: Fix `sdk/agent/tool_cache.go` — Cache correctness

Modify: [/Users/vkochetkov/Repositories/c0wrk/sdk/agent/tool_cache.go](/Users/vkochetkov/Repositories/c0wrk/sdk/agent/tool_cache.go)

### 2a. Include tool name in hash key (issue #6)
Change `sha256hex(content)` to `sha256hex(toolName + "\x00" + content)` so that identical content from different tools gets different hashes.

### 2b. Delete expired entries inline in `Get` with write-lock (issues #9, #17)
Convert `Get` from using `RLock` to using `Lock`. When an entry is found to be expired (TTL check), delete it from the map and return `nil, false`. Also call `EvictExpired` periodically — add a call every 100th `Store` call.

### 2c. Add TTL check in `CheckCoherence` (issue #15)
Before checking file coherence, verify TTL expiry for MCP entries. If expired, return `false, "cache entry expired"`.

---

## Task 3: Fix `sdk/tools/builtins/tool_result_read.go` — Enforce num_lines upper bound (issue #5)

Modify: [/Users/vkochetkov/Repositories/c0wrk/sdk/tools/builtins/tool_result_read.go](/Users/vkochetkov/Repositories/c0wrk/sdk/tools/builtins/tool_result_read.go)

- Add a `perToolTruncation` field to `ToolResultReadTool` or pass via context
- After reading `params.NumLines`, cap it at per-tool `MaxLines` for that entry's tool
- If per-tool config is unavailable, enforce a hard cap of `defaultResultReadLines` (500)

---

## Task 4: Remove `max_results` from ripgrep/glob (issue #4)

### 4a. `sdk/tools/builtins/ripgrep.go`
- Remove `max_results` from JSON schema
- Remove `MaxResults` field from `RipgrepInput` struct
- Remove `params.MaxResults` parsing/defaulting block
- Update `toolRipgrepDescription` — remove "Returns up to 200 matches by default"

### 4b. `sdk/tools/builtins/glob.go`
- Remove `max_results` from JSON schema
- Remove `MaxResults` field from `GlobInput` struct
- Remove `params.MaxResults` parsing/defaulting block
- Update `toolGlobDescription` — remove "Returns up to 200 results by default"

---

## Task 5: Fix inaccurate tool descriptions/schemas (issue #8)

### 5a. `sdk/tools/builtins/file_read.go`
- Update `end_line` schema description — remove "Individual lines exceeding the per-line character limit are truncated with a notice."
- Update `toolReadFileDescription` — remove "When more content remains beyond the returned range, a continuation hint is appended." Replace with accurate description of current behavior.

### 5b. `sdk/tools/builtins/webfetch.go`
- Update `toolWebfetchDescription` — remove "Markdown output is limited to 2MB". Replace with: "Output is truncated by the centralized truncation layer."
- Remove `rawHTMLSafetyCapMultiplier` constant (issue #16)
- In `fetchPage`, remove the `io.LimitReader` safety cap — read the full HTTP body. This enforces the "no caps outside truncation pipeline" rule.

---

## Task 6: Fix WebFetch default (issue #10)

Modify: [/Users/vkochetkov/Repositories/c0wrk/backend/config/defaults.go](/Users/vkochetkov/Repositories/c0wrk/backend/config/defaults.go)

Change `"web_fetch": {MaxLines: 0, MaxBytes: 204800}` to `{MaxLines: 0, MaxBytes: 2097152}` matching the old 2MB default.

---

## Task 7: Clean up dead config/limit parameters (issue #12)

### 7a. `sdk/tools/builtins/limits.go`
- Remove `ReadMaxLineLength`, `ReadMaxBytes` from `FileLimits` (keep `ReadDefaultLines` — still used for end_line default)
- Remove `MaxResults`, `MaxLineLength` from `RipgrepLimits` (keep `Timeout`)
- Remove `MaxResults` from `GlobLimits`
- Remove `MaxBodySize` from `WebFetchLimits` (keep `Timeout`)
- Update `Default*Limits()` functions accordingly

### 7b. `core/tools/builtin_registration.go`
- Simplify type re-exports — removed fields no longer exist
- Update `BuiltinToolsConfig` to remove dead fields
- Update `RegisterBuiltinTools` — simplify tool constructor calls (no more dead limit params)

### 7c. `core/builderconfig.go`
- Remove dead fields from `BuilderToolLimitsConfig`: `ReadMaxLineLength`, `ReadMaxBytes`, `RipgrepMaxResults`, `RipgrepMaxLineLength`, `GlobMaxResults`, `WebFetchMaxBodySize`
- Keep `ReadDefaultLines` (still used)

### 7d. `backend/configadapter.go`
- Remove mappings for dead fields from `ToBuilderConfig`

### 7e. `backend/config/config.go`
- Keep config struct fields for backward compatibility (they'll be silently ignored but won't error on YAML parse)
- Add deprecation comments

### 7f. `backend/config/defaults.go`
- Remove defaults for dead fields (they're still set in config struct but no longer mapped)

### 7g. `core/builder.go`
- Update `configToBuiltinToolsConfig` to only use remaining live fields
- Update `UpdateWebTools` to remove `MaxBodySize` usage

### 7h. `config.example.yaml`
- Remove example entries for dead toolLimits fields

---

## Task 8: Audit ENTIRE codebase — no truncation before cache (issue #16 + general rule)

Use `SearchCodebase` + `Grep` to search for any truncation/capping patterns outside the truncation pipeline:

Search patterns:
- `truncat`, `limit`, `[:`, `cap`, `max.*bytes`, `max.*len`, `io.LimitReader`, `io.CopyN`
- In `sdk/tools/builtins/` — verify no tool truncates its own output
- In `sdk/agent/` — verify only the executor's truncation pipeline truncates

Critical findings to verify:
1. ✅ `webfetch.go` `fetchPage` — remove `io.LimitReader` (Task 5b)
2. Check `file_read.go` — already returns full content (no truncation) ✅
3. Check `ripgrep.go` — scan loop no longer has early exit ✅
4. Check `glob.go` — walk no longer has early exit ✅
5. Check `bash_exec.go` — verify no output capping
6. Check `list_directory.go` — verify no output capping
7. Check any other built-in tools

---

## Task 9: Update tests (issues #4, #5, #6, #8, #10, #12)

### 9a. `sdk/tools/builtins/ripgrep_test.go`
- `TestRipgrepTool_MaxResults` — test now expects full output (no truncation). The test was already updated in the commit but verify it still passes.
- `TestRipgrepTool_LongLineTruncation` — test should verify long lines are returned in full (already updated).

### 9b. `sdk/tools/builtins/glob_test.go`
- `TestGlobTool_MaxResults` and `TestGlobTool_DefaultMaxResults` — already updated, verify

### 9c. `sdk/tools/builtins/file_tools_test.go`
- Verify file read tests still pass without truncation assertions

### 9d. `sdk/tools/builtins/webfetch_test.go`
- `TestWebFetchTool_BodySizeLimit` — update to expect full content
- `TestWebFetchTool_TruncationShowsLineCount` — update to expect no truncation
- `TestWebFetchTool_LineRangeWithTruncation` — update to expect no truncation

### 9e. Add `sdk/agent/executor_test.go` — executor-level truncation tests (issue #14)
- Test that `applyPerToolTruncation` correctly truncates by lines
- Test that `applyPerToolTruncation` correctly truncates by bytes (UTF-8 safe)
- Test that `formatFragmentationNudge` includes the hash
- Test that Stage 2 preserves the Stage 1 nudge
- Test that cache store happens BEFORE truncation
- Test that non-cacheable tools bypass the cache
- Test `buildCacheMeta` extracts correct metadata

### 9f. `sdk/agent/tool_cache_test.go` — cache tests
- Test hash includes tool name (different tools, same content → different hashes)
- Test expired entries are deleted inline in Get
- Test CheckCoherence checks TTL

---

## Task 10: Update specs

Modify spec files that reference old limits/truncation behavior:
- `specs/domains/tool-system/builtins.md` — remove references to per-tool truncation limits
- `specs/contracts/core-sdk.md` — update if it references old limit fields
- `specs/domains/orchestration/executor.md` — update truncation pipeline description

---

## Verification

1. `cd /Users/vkochetkov/Repositories/c0wrk && make lint` — no new lint errors
2. `make test` — all tests pass (Go + frontend)
3. Specifically run: `go test ./sdk/agent/... -v -run "Cache|Truncation"` 
4. `go test ./sdk/tools/builtins/... -v`
5. `go build ./...` — compiles cleanly
