# Configuration

## Purpose

Owns the application's single operator-authored YAML document — `~/.c0wrk/config.yaml` — from path resolution and load-time migration through defaults, per-section normalization, fail-closed validation, and atomic persistence, and publishes the sanitized in-memory view the frontend reads. Every tunable in c0wrk enters the system through this domain; no other component parses `config.yaml`.

## Key Files

- `backend/config/config.go` — the `Config` tree (all top-level sections), `Load`/`LoadWithResult`/`Save`, `ExpandEnvVars`, the fail-closed `validate` funnel plus the section validators the save path and the load path share (`ValidateVectorIndexExcludePatterns`, `ValidateProviderTimeoutClass`, `ValidateSilentMode`, `ValidateSearchBaseURL`, …), provider/default-model resolution (`ResolveDefaultModelProvider`), and the embedded-provider reconciliation `SyncEmbeddedLLMProvider`
- `backend/config/resolve.go` — `ResolveAndLoad`, the startup entry: agent-directory creation, missing-file bootstrap (`CreateDefault`), parse/validation failure policy, and model-profile catalog warnings
- `backend/config/defaults.go` — `ApplyDefaults`, the zero-value seeder for every section
- `backend/config/paths.go` — the single source of truth for every fixed `~/.c0wrk` path (`AgentDir`, `ConfigPath`, `DatabasePath`, `LogsDir`, `ProjectsDir`, `ToolsDir`, `UpdateStagingDir`, `UpdateStatePath`, `SingleInstanceLockPath`, `WindowStatePath`, `WebviewStatePath`, `DialogStatePath`, `ModelProfilesPath`, …) and the managed-worktree path family
- `backend/config/shell_env.go` — `LoadShellEnvironment`, the macOS login-shell environment recovery that runs before any other startup init
- `backend/config/blacklist_migration.go` — the one-time `security.groups.<group>.blacklist` → `blocklist` migration (legacy default list recognized and dropped, a customized list carried over)
- `backend/config/mcp_mode.go` — the shared capability-mode enum (`ValidMCPServerMode`/`NormalizeMCPServerMode`), the legacy `mcp.servers.<name>.mode` fold into `capabilities.mcp`, and the strict `ValidateCapabilities` save gate
- `backend/config/mcp_timeout.go`, `session_artifacts.go`, `service_models.go`, `shell_exec.go`, `web_access.go`, `web_access_cert.go`, `model_profiles.go`, `model_profiles_store.go`, `sdd_store.go` (`SDDPath`/`ProjectSDDPath`), `search.go`, `deref.go`, `modelid.go` — per-section structs, load normalization, and save gates (each domain spec owns the semantics; this file group owns the config-layer enforcement)
- `backend/configadapter.go` — the single `config.Config → core.BuilderConfig` conversion (including the `ExpandEnvVars` seam)
- `backend/frontend_api_config.go` — `GetConfig` (sanitized, pure in-memory read) and the `Update*` RPCs with the save discipline
- `backend/events.go` — `EventConfigUpdated` (`config:updated`)

## Core Types

```go
// The single in-memory configuration tree. One field per top-level YAML
// section; pointer fields are sentinels (see the pointer doctrine below).
type Config struct {
    LogLevel          string                    `yaml:"log_level"`
    LLM               LLMConfig                 `yaml:"llm"`
    EmbeddedLLM       EmbeddedLLMConfig         `yaml:"embedded_llm"`
    MCP               MCPConfig                 `yaml:"mcp"`
    Capabilities      CapabilitiesConfig        `yaml:"capabilities,omitempty"`
    Router            RouterConfig              `yaml:"router"`
    Executor          ExecutorConfig            `yaml:"executor"`
    Security          SecurityConfig            `yaml:"security"`
    Skills            SkillsConfig              `yaml:"skills"`
    Agents            AgentsConfig              `yaml:"agents"`
    Commands          CommandsConfig            `yaml:"commands"`
    Search            SearchConfig              `yaml:"search"`
    ToolLimits        ToolLimitsConfig          `yaml:"toolLimits"`
    Timeouts          TimeoutsConfig            `yaml:"timeouts"`
    Shutdown          ShutdownConfig            `yaml:"shutdown"`
    ShellExec         ShellExecConfig           `yaml:"shell_exec"`
    Orchestration     OrchestrationConfig       `yaml:"orchestration"`
    GoalLoop          GoalLoopConfig            `yaml:"goal_loop"`
    PromptSuggestions PromptSuggestionsConfig   `yaml:"prompt_suggestions"`
    VectorIndex       VectorIndexConfig         `yaml:"vector_index"`
    Proxy             ProxyConfig               `yaml:"proxy"`
    Terminal          TerminalConfig            `yaml:"terminal"`
    Git               GitConfig                 `yaml:"git"`
    Runtime           RuntimeConfig             `yaml:"runtime"`
    Notifications     NotificationsConfig       `yaml:"notifications"`
    Research          SidebarPackConfig         `yaml:"research"`
    Specs             SidebarPackConfig         `yaml:"specs"`
    ModelProfiles     ModelProfilesPersistConfig `yaml:"model_profiles"`
    E2S               E2SConfig                 `yaml:"e2s"`
    Experimental      ExperimentalConfig        `yaml:"experimental"`
    Updates           UpdatesConfig             `yaml:"updates"`
    WebAccess         WebAccessConfig           `yaml:"web_access"`
    SessionArtifacts  SessionArtifactsConfig    `yaml:"session_artifacts"`
}

// Result of one load: the config plus every non-fatal problem, in a
// deterministic order. The strings reach the UI as GetConfig.config_errors.
type LoadResult struct {
    Config     *Config
    LoadErrors []string
}

// Startup result: the loaded config, the resolved paths, and the warnings.
type ResolvedConfig struct {
    Config     *Config
    ConfigPath string
    AgentDir   string
    LoadErrors []string
}
```

**Pointer-sentinel doctrine.** A knob whose contract distinguishes "absent" from an explicit zero/false is a pointer (`*bool`, `*int`, `*float64`, `*int64`): `nil` means "apply the default", a non-nil value is applied verbatim (`0` disables, `false` disables, `""` means infer). Plain booleans and zero-defaulted integers cannot express that distinction and are only used where "zero = default" is the whole contract. The same doctrine governs settings RPC payloads — a nil field keeps the persisted value, a non-nil field applies verbatim.

**Nil-vs-empty slice doctrine.** A discovery list (`skills.dirs`, `agents.dirs`, `commands.dirs`) seeds its defaults only when the YAML key is absent (`nil`); an explicit empty list is an opt-out and stays empty.

## Flow

```
startup
  main.go → desktop.App.Startup
    → config.LoadShellEnvironment()            (macOS only, before any other init)
    → config.ResolveAndLoad(logger)
        ├─ AgentDir()                          ($HOME/.c0wrk, "./.c0wrk" without a home)
        ├─ safeio.MkdirAllReal(agentDir, 0755) (refuses a dangling/swapped link)
        ├─ stat ConfigPath(agentDir)
        │    ├─ missing     → CreateDefault: ApplyDefaults + Save
        │    └─ present     → LoadWithResult → the load pipeline below
        └─ LoadModelProfilesCatalog + ResolveModelProfilesConfig → warnings
    → backend.FrontendAPI receives Config + ConfigPath + LoadErrors
    → configadapter.ToBuilderConfig → core.BuilderConfig → core.Builder

mutation (a settings RPC)
  Update* RPC
    → saveMu.Lock()                        (whole sequence: mutate → persist → propagate)
    → configMu.Lock()
        ├─ validate the candidate          (section gate; reject without mutating)
        ├─ mutate the in-memory Config
        ├─ config.Save(config, configPath) (atomic write; on failure restore the prior value)
        └─ snapshot for propagation
    → configMu.Unlock()
    → propagate / rebuild OUTSIDE configMu (router, gateway, live sessions)
    → emit config:updated  → frontend re-reads GetConfig
```

## Load Pipeline

`LoadWithResult` runs a fixed, order-sensitive sequence. Stages before `validate` are **fail-soft**: a malformed value is replaced with the documented safe value and reported in `LoadErrors`. `validate` is **fail-closed**.

| # | Stage | Kind | Effect of a bad value |
| - | ----- | ---- | --------------------- |
| 1 | YAML parse (`yaml.Unmarshal`, **non-strict**) | fail-closed | unparsable document aborts the load; unknown keys are ignored and disappear at the next save |
| 2 | `migrateLegacyGroupBlacklists` | migration | a still-default legacy list is dropped; a customized one becomes `blocklist` |
| 3 | `migrateLegacyAutonomyMode` | migration (warn) | the pre-enum `smart_approve` / `silent_mode.enabled` booleans fold into `security.autonomy_mode` (silent wins); an unknown enum value falls back to `standard` |
| 4 | `ApplyDefaults` | fail-soft | zero-value fields are seeded; pointer knobs and nil slices keep their documented meaning |
| 5 | `SyncEmbeddedLLMProvider` | derived state | `llm.openai_compatible.embedded` is regenerated from `embedded_llm.installed` (generated, never operator-edited) |
| 6 | `normalizeShellExec` | fail-soft (warn + reset) | an invalid launch-shape override resets to the built-in shape |
| 7 | `normalizeWebAccess` | fail-soft (warn + reset) | an invalid enabled section is disabled; a valid non-loopback bind stays enabled with an exposure warning |
| 8 | `normalizeMCPTimeouts` | fail-soft (warn, value left) | an unparseable per-server duration is warned here and resolved at build time |
| 9 | `migrateLegacyMCPServerModes` | migration (fail-soft) | `mcp.servers.<name>.mode` folds into `capabilities.mcp.<name>` for both contexts and is erased |
| 10 | `normalizeCapabilities` | fail-soft (warn + reset) | an unrecognized `chat_mode`/`code_mode` becomes `auto`; stale `capabilities.mcp` names and both-empty entries are dropped |
| 11 | `normalizeSessionArtifacts` | fail-soft (warn + reset) | an unknown codec or out-of-range zstd window resets to the default |
| 12 | `normalizeServiceModels` | fail-soft (warn + drop) | an unknown service key or non-composite value is dropped in place |
| 13 | `validate` | **fail-closed** | the load returns the parsed config plus an error; `ResolveAndLoad` keeps that config so operator settings are never reset |

Warnings are appended in stage order, so `config_errors` is deterministic.

## Save Discipline

`config.Save` is the only writer of `config.yaml`:

1. **Fail-closed section gates run first** — `validateWebAccessForSave` and `validateSessionArtifactsForSave` refuse to persist an enabled-but-invalid section (the load path heals such a section, so reaching the gate means an in-memory mutation tried to persist an unusable value).
2. **Marshal** the whole tree with `yaml.Marshal`.
3. **Atomic write** through `safeio.WriteFileAtomic`: a randomized staging sibling plus rename, with an unconditional mode `0o600` (not subject to the ambient umask), so a planted FIFO/symlink at the fixed path can neither block the open nor receive the write, and the API-key-bearing file is never world-readable.

Locking is two-tier by contract: `saveMu` serializes whole save sequences (mutation → persist → propagate never interleave between two callers), while `configMu` covers only candidate validation, the in-memory mutation, the atomic write, and snapshotting. Rebuilds and other fallible propagation run outside `configMu` on a fresh snapshot, because a queued writer would otherwise freeze every `GetConfig`/settings read for the rebuild's duration.

## Invariants

- `~/.c0wrk/config.yaml` is the only configuration document the process loads; the process working directory is never a configuration source (a directory-planted file would otherwise contribute MCP commands, security policies, and provider endpoints).
- A load never destroys operator state: a parse failure falls back to defaults with an error, while a validation failure keeps the parsed configuration and reports the error.
- Every section binds its own semantics to its own spec; this domain fixes only the cross-section rules — load is fail-soft per section, save is fail-closed per section, and `validate` is the single fail-closed gate.
- Every configuration write is atomic (staged sibling plus rename) and mode `0o600`.
- `${VAR}` references are stored verbatim and expanded only at consumption (`config.ExpandEnvVars`, or the `BuilderConfig.ExpandEnvVars` seam); an undefined variable expands to the empty string.
- `GetConfig` is a pure in-memory read: no disk, no network, and API keys leave as `***configured***` (or empty).
- A committed mutation is announced by exactly one `config:updated` (payload-less) after the in-memory state is final; rollback writers emit only after a successful write, so a failed update is indistinguishable from a rejected request.
- Every fixed path under `~/.c0wrk` is produced by a helper in `backend/config/paths.go`; no caller joins the agent directory inline.
- The unknown-key posture is non-strict: a misspelled or retired key is silently ignored on load and erased by the next save.

## Configuration

The document's own surface, by top-level section. Each section's parameters and defaults are specified by the owning domain.

| Section | Owns |
| ------- | ---- |
| `log_level` | process log level (`INFO` default) |
| `llm`, `timeouts` | [llm-providers.md](llm-providers.md) |
| `search` | [tool-system/builtins.md](tool-system/builtins.md) (web search providers) |
| `embedded_llm` | [embedded-llm.md](embedded-llm.md) (app-written install state; only `engine`, `auto_unload`, `tuning` are operator-owned) |
| `mcp`, `capabilities`, `skills`, `agents`, `commands` | [capabilities.md](capabilities.md), [tool-system/mcp-gateway.md](tool-system/mcp-gateway.md), [commands.md](commands.md) |
| `router`, `executor`, `orchestration`, `goal_loop`, `prompt_suggestions` | [orchestration/](orchestration/README.md), [memory/compaction.md](memory/compaction.md), [goal-mode.md](goal-mode.md), [prompt-suggestions.md](prompt-suggestions.md) |
| `security` | [../architecture/security-model.md](../architecture/security-model.md) |
| `shell_exec` | [tool-system/builtins.md](tool-system/builtins.md) (Shell Invocation Override) |
| `toolLimits` | [tool-system/builtins.md](tool-system/builtins.md) |
| `vector_index` | [workspace.md](workspace.md) (Vector Index) |
| `proxy` | [networking.md](networking.md) |
| `git` | [git-auto-fetch.md](git-auto-fetch.md), [session-worktrees.md](session-worktrees.md) |
| `runtime` | [workspace.md](workspace.md) (GC memory hygiene) |
| `terminal` | [terminal.md](terminal.md) |
| `notifications` | [frontend/system-notifications.md](frontend/system-notifications.md) |
| `research`, `specs` | [research.md](research.md), [specs.md](specs.md) (sidebar surface gates) |
| `model_profiles` | [model-profiles.md](model-profiles.md) |
| `e2s`, `experimental` | [e2s.md](e2s.md), [web-access.md](web-access.md) |
| `updates` | [updates.md](updates.md) |
| `web_access` | [web-access.md](web-access.md) |
| `session_artifacts` | [session-lifecycle.md](session-lifecycle.md) (Compressed Session Artifacts) |
| `shutdown` | [crash-logging.md](crash-logging.md), [webview-gpu-policy.md](webview-gpu-policy.md) |

Auxiliary operator stores live beside the document, each owned by its domain: `~/.c0wrk/model-profiles.yaml` ([model-profiles.md](model-profiles.md)), `~/.c0wrk/specs.yaml` plus `<workspace>/.c0wrk/specs.yaml` ([sdd-dsl.md](sdd-dsl.md)), `~/.c0wrk/update_state.json` ([updates.md](updates.md)), and the desktop state files (`window_state.json`, `dialog_state.json`, `webview_state.json`) owned by their desktop specs.

`config.example.yaml` at the repository root is the operator-facing reference for every tunable.

## Extension Points

Adding a tunable is a fixed sequence; skipping a step leaves the knob unvalidated or unreachable:

1. Add the field to its section struct in `backend/config/config.go` (pointer type when absent ≠ zero).
2. Seed its default in `backend/config/defaults.go` (`ApplyDefaults`).
3. Add a load normalizer when the value has a safe fallback, or a rule in `validate` when a bad value must fail the load.
4. Add a save gate in `config.Save` (or the section's `validate*ForSave`) when the section must never be persisted invalid.
5. Map it in `backend/configadapter.go` — the single conversion point, whose logger turns residual inconsistencies a load-bypassing config can carry into warnings instead of silence.
6. Document it in the owning domain's `## Configuration` section and in `config.example.yaml`.
7. When it is editable from the UI, add the RPC field, the rollback-on-failure write, and the `config:updated` announcement.

A new top-level section additionally needs a `Core Types` entry here and a row in the section table above.

## Related Specs

- [../architecture/data-flow.md](../architecture/data-flow.md) — the request, event, configuration and startup flows this domain feeds
- [../contracts/backend-core.md](../contracts/backend-core.md) — the config-adapter contract and the "add a config field" checklist
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — `GetConfig` / `Update*` RPC shapes and the `ConfigResponse` DTO
- [../contracts/event-catalog.md](../contracts/event-catalog.md) — `config:updated` and the rollback-writer rule
- [../architecture/security-model.md](../architecture/security-model.md) — fixed-path state hardening (`safeio`) that every write here follows
- [llm-providers.md](llm-providers.md) — the largest section (`llm.*`) and the shared config-save discipline
- [model-profiles.md](model-profiles.md), [sdd-dsl.md](sdd-dsl.md), [updates.md](updates.md), [networking.md](networking.md) — the auxiliary stores and sections introduced here
