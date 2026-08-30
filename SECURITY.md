# Security Policy

> **This is a security *policy*, not an audit report.** It defines the project's threat model and the secure coding rules every contributor and coding agent must follow. It does **not** record whether the current codebase complies with these rules — compliance verification belongs in code review, audits, or the issue tracker, never in this file.

## Supported Versions

c0wrk is pre-1.0 software under active development. Only the latest release line receives security updates.

| Version | Supported          |
| ------- | ------------------ |
| 0.5.x   | :white_check_mark: |
| < 0.5   | :x:                |

The current release is tracked in [`CHANGELOG.md`](./CHANGELOG.md). Breaking changes may land in any 0.x release until 1.0 stabilizes the public surface.

## Reporting a Vulnerability

**Do NOT open public GitHub issues for security vulnerabilities.**

Report suspected vulnerabilities privately so they can be triaged before public disclosure.

- **Preferred channel:** open a private security advisory via GitHub's "Report a vulnerability" feature on the repository's **Security** tab. If that is unavailable, contact the maintainer directly.
- **Include:** a description of the issue, the attack path, affected versions/files, and a reproduction if possible.
- **Response SLA (target):**
  - Acknowledgment: within 72 hours
  - Triage & severity assessment: within 7 business days
  - Fix timeline: Critical — 14 days, High — 45 days, Medium — 90 days
- **Disclosure policy:** coordinated disclosure. We request a 90-day embargo before public disclosure, extendable on request. Reporters are credited in release notes unless they prefer anonymity.
- **Bug bounty:** none at this time.

---

## Threat Model

c0wrk is a **desktop AI coding-agent** (Go backend + React/TypeScript frontend, packaged with Wails v2). The agent reads a user's codebase, reasons, calls tools (filesystem, shell, web), manages subagents, persists memory, and integrates external MCP servers — all on the user's local machine, running under the user's own operating-system credentials. The agentic threat model (OWASP Top 10 for Agentic Applications, ASI01–ASI10) is therefore **required** and is documented below.

### Assets

| Asset                                   | Sensitivity   | Description                                                                                            |
| --------------------------------------- | ------------- | ----------------------------------------------------------------------------------------------------- |
| LLM provider API keys                   | Critical      | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `TAVILY_API_KEY`, proxy credentials — billed, scope = blast radius |
| SQLite database                         | High          | `~/.c0wrk/database.db` — sessions, chat messages (`session_messages`), projects, reviews, work dirs   |
| Configuration file                      | High          | `~/.c0wrk/config.yaml` — provider keys (env-expanded), tool-group policies (`security.groups`), execute blacklist, injection-defense toggles |
| System & role prompts                   | High          | `core/prompts/*.md` embedded into the system prompt — extraction reveals guardrails & business logic  |
| Agent conversation history / context    | High          | Multi-turn message logs persisted in SQLite; vectorized codebase index                                |
| Vector index (embeddings)               | Medium/High   | `chromem-go` index of the user's source code; file-hash sidecars — persistent searchable memory       |
| Blackboard facts                        | Medium        | Cross-step/cross-agent facts persisted in `core/persistent_blackboard.go`                            |
| Tool & MCP definitions                  | Medium/High   | Tool schemas, MCP server configs (command/args/env, URL/headers), permission mappings                 |
| Agent-generated artifacts               | Medium        | Code, file edits, plans, shell commands produced by the agent at runtime                              |
| Crash-capture log                       | Medium        | `~/.c0wrk/logs/stderr.log` — always-on raw fd 1/2 mirror (4 MiB rotation, content never truncated); persists unfiltered output of the whole process tree incl. native libs and child processes; `C0WRK_DISABLE_CRASH_CAPTURE=1` opt-out |
| Downloaded tool binaries                | Medium        | `~/.c0wrk/tools/` — `rg`, `uv`, `markitdown` (SHA256-verified)                                        |
| ONNX Runtime + embedding model          | Medium        | Fetched into `.cache/` and bundled for the vector index                                              |
| User's filesystem & shell               | Critical      | The agent operates directly on the host under the user's credentials (read/write/exec)               |
| Project source code                     | High          | The workspace the agent reads and modifies — integrity & confidentiality                             |

### Threat Actors

- **Opportunistic attacker** — automated scanners, credential stuffing against leaked API keys.
- **Prompt-injection adversary (ASI01/ASI06)** — plants hostile instructions in files, web pages, tool outputs, dependencies, or `AGENTS.md` sources that the agent reads and may follow.
- **Malicious dependency / tool author (ASI04)** — publishes a compromised Go/npm dependency, MCP server, or bundled tool with deceptive schemas.
- **Compromised agent identity (ASI03/ASI07)** — forges or inherits agent/subagent authority to act with delegated privileges.
- **Trust-exploitation target (ASI09)** — the operator who over-trusts confident agent output and rubber-stamps confirmation prompts (approval fatigue).
- **Compromised host process** — any local process that writes machine-wide files (`~/.agents/AGENTS.md`) or tampers with `~/.c0wrk/` data.
- **Motivated external attacker** — targeted exploitation of the supply chain, SSRF via `web_fetch`, or shell-command injection.

### Attack Surface

Entry points where untrusted input or adversarial content reaches the system:

- **Agent prompt inputs (ASI01)** — user messages, clipboard text/files/images, native file drops, retrieved files, `web_search`/`web_fetch` results, `bash_exec` stdout, `ripgrep`/`glob`/`read_file` output, and **all MCP tool output** entering the model context (indirect-injection vectors).
- **Multi-source `AGENTS.md` (ASI01)** — `~/.agents/AGENTS.md` (machine-wide writable), `~/.c0wrk/.agents/AGENTS.md`, and `<workspace>/AGENTS.md`, all injected into the system prompt.
- **Agent tool / function-calling surface (ASI02)** — every tool the agent may invoke, especially side-effecting ones: `bash_exec`/`posh_exec`, `write_file`, `edit_file`, `delete_*`, `web_fetch`, `create_directory`.
- **Agent-generated shell / code execution (ASI05)** — `bash_exec` (Unix) / `posh_exec` (Windows) running model-generated commands; agent-authored file edits executed by the user's shell.
- **External MCP registries (ASI04)** — dynamically integrated stdio/HTTP MCP servers whose tool descriptions, schemas, and permissions may be forged; configured commands executed as child processes.
- **Downloaded tool binaries (ASI04)** — `rg`, `uv`, `markitdown` fetched from public URLs by the tool-manager.
- **In-app self-update / auto-update (ASI04)** — the self-update pipeline (`core/updater/`) downloads a release archive from GitHub Releases, SHA256-verifies it against `SHA256SUMS`, and atomically swaps the install tree via a two-process re-exec (`--self-update`). This is a **supply-chain delivery vector**: a compromised release (archive *and* checksum) or a MITM that breaks TLS could replace the running binary the user executes. Integrity is SHA256-only, fail-closed; artifacts are **unsigned** (no app-pinned signature key). See [ADR-023](./specs/decisions/023-auto-update.md) for the full threat model and accepted trade-offs.
- **Agent memory stores (ASI06)** — SQLite `session_messages`, the project-scoped vector index, persisted pause/subagent trajectories, RESEARCH artifacts under a workspace-contained root, and persistent blackboard facts.
- **Prompt-discovered work directories (ASI02/ASI05)** — existing directory paths mentioned by the user may be added to the session's auxiliary roots. Adding a root expands the agent's filesystem reach for that task; containment/symlink/group-policy gates still apply to individual tool calls.
- **Vector/attachment ingestion (ASI06/ASI10)** — pasted/dropped/selected files and workspace files are parsed, converted, chunked, or embedded. File-size, image-size, conversion-time, chunk-count, and output limits are resource-exhaustion boundaries, not optional tuning conveniences.
- **RESEARCH mode (ASI06/ASI07)** — experimental methodology skills and recursively watched Markdown artifacts influence routing and future turns. Explicit research roots must remain inside the project workspace; disabling the mode preserves artifacts and seeded skills, which remain untrusted persisted data.
- **Inter-agent channels (ASI07)** — the subagent delegation protocol (`delegate`, `execute_plan`) and the shared blackboard.
- **Human-approval / HITL gates (ASI09)** — `tool_confirm`, `ask_user`, plan/goal review prompts.
- **Outbound HTTP (SSRF)** — `web_fetch`, `web_search`, LLM API calls, MCP HTTP servers.
- **LLM provider endpoints** — outbound calls to Anthropic/OpenAI/local servers carrying API keys.
- **Dependency supply chain** — Go modules (`go.sum`) and npm packages (`package-lock.json`).
- **Frontend rendering** — `react-markdown`, `rehype-sanitize`, Mermaid, highlight.js rendering tool/agent output (XSS surface).
- **CI/CD pipeline** — `.github/workflows/ci.yml`, `.github/workflows/release.yml`.

### Trust Boundaries

```
┌─────────────────────────────────────────────────────────┐
│  User / Operator zone (trusted)                         │
│  OS credentials, config.yaml, approved workspace        │
└──────────────────────────┬──────────────────────────────┘
                           │ system-prompt assembly
┌──────────────────────────▼──────────────────────────────┐
│  Developer-instructions zone (trusted)                  │
│  core/prompts/*.md, tool definitions, guardrails        │
└──────────────────────────┬──────────────────────────────┘
        untrusted content   │   instruction/data separation
        injected here ──────┘   (<untrusted-content> spotlighting)
┌──────────────────────────▼──────────────────────────────┐
│  Agent reasoning zone (semi-trusted, bounded scope)     │
│  Model context, plans, tool-call decisions              │
└─────────────────┬─┬─────────────────────────────────────┘
   untrusted in ──┘ └──→ least-agency + policy enforcement
┌──────────────────────────▼──────────────────────────────┐
│  Untrusted content sources (ASI01 vectors)              │
│  Files, web pages, tool/MCP output, AGENTS.md, deps     │
└──────────────────────────┬──────────────────────────────┘
                           │ group policy → judge → confirmation
┌──────────────────────────▼──────────────────────────────┐
│  Execution / Action zone                                │
│  Filesystem, shell, web, DB, vector store, MCP procs    │
│  Inter-agent channels, memory stores (ASI06/ASI07)      │
└─────────────────────────────────────────────────────────┘
```

The most critical boundary is the one between **trusted developer instructions** (`core/prompts/`, tool definitions) and **untrusted content the agent reads at runtime** (files, web, tool/MCP output, `AGENTS.md`). Both arrive as natural language the model treats identically — the root cause of ASI01. The hard enforcement boundary is the **tool-policy pipeline** (policy → judge → confirmation); prompt-level framing is defense in depth on top of it.

### Known Risks & Accepted Trade-offs

These are inherent architectural trade-offs the design knowingly accepts — not compliance gaps.

| Risk                                                                  | Severity | Mitigation / Rationale                                                                                                        |
| --------------------------------------------------------------------- | -------- | ----------------------------------------------------------------------------------------------------------------------------- |
| Agent runs under the user's own OS credentials (no sandbox/tenant isolation) | High     | Accepted: c0wrk is a single-user local desktop tool. The tool-policy pipeline + confirmation gates bound the agent's *latitude*, not the OS-level *reach* (least agency, not least privilege). |
| Local LLM servers may use an empty API key                            | Medium   | Accepted for local-dev convenience; only `localhost`/named local providers. Production cloud providers require keys.          |
| `~/.agents/AGENTS.md` is machine-wide writable and feeds every prompt | Medium   | Accepted trade-off (ADR-020) for global-instructions utility; mitigated by untrusted framing + tool-policy gating; not eliminated. |
| LLM-based safety judge is advisory/on-demand by default; Smart Approve makes it an automatic gate for every escalated call (opt-in, default off)   | Medium   | When Smart Approve is off, the judge remains advisory only (invoked via the "Ask Agent" button). When Smart Approve is on, a strict OWASP ASI judge automatically evaluates every escalated call — an effective `user_confirm` call, or a hard/soft safety reason surfaced by an `allow`-group tool (the unified confirmation funnel) — after all deterministic gates and workspace auto-approval. Only a strict ALLOW skips UI and every other outcome (CONFIRM, timeout, error, unparseable) falls back to manual confirmation. The group-policy/judge-blacklist/symlink/confirmation pipeline remains the hard gate; Smart Approve never weakens a `deny` group or the fail-closed nil-ConfirmFunc invariant, and the deterministic backstop forces confirmation for canonical hard reasons — fired controls (blacklist, SSRF, symlink escape) and unassessable inputs (degraded SSRF protection, an undeterminable URL/path) — even when the strict judge returns ALLOW. Canonicality is keyed off typed reason codes (`JudgeOutcome.ReasonCode`, a stable sp4rk contract), never off prose. |
| No LLM-based output-content judging for injection detection           | Medium   | Accepted; injection defense is prompt-level spotlighting + tag-escape, judged externally/by firewall.                         |
| No session-level wall-clock time-box (ASI10)                          | Low      | Accepted trade-off: agent autonomy is bounded by step/loop caps, the 50-turn goal ceiling, and circuit breakers, but there is no hard wall-clock deadline. A long-running session can in principle run indefinitely until those caps hit. |
| Pre-1.0 breaking-change surface                                       | Low      | Tracked via CHANGELOG semver discipline until 1.0.                                                                            |
| Self-update ships **unsigned** artifacts verified by SHA256 only (ASI04) | High     | Accepted trade-off (ADR-023): integrity is SHA256-only, fail-closed, with no app-pinned signature key. SHA256 pins the archive to the released bytes but does **not** prove release authorship — a compromised GitHub release (archive *and* matching checksum) would be accepted. Bounded by release-publishing perms + GitHub account security (2FA); mitigated by HTTPS-only/proxy-aware transport, fail-closed verification, `.old` rollback, and `ErrNonStandardLocation` location safety. A signature verifier can be added later via the `Verifier` interface. |

---

## Security Architecture

### Authentication & Authorization

c0wrk is a local single-user desktop application; there is no multi-tenant authentication surface. Authorization is **tool-level**, enforced by the policy pipeline in `core/tools/registry.go`:

- **Tool-group policies** — `allow` / `user_confirm` / `deny` (default `user_confirm`, the safest), configured per **capability group** in `security.groups` (ADR-024). Every tool declares exactly one of the 8 groups (`execute`, `local_read`, `local_write`, `remote_read`, `remote_write`, `local_mcp`, `remote_mcp`, `system`); the registry resolves the call's policy from its group alone — there is no per-tool override layer. An unconfigured group resolves fail-safe to `user_confirm`.
- **The `system` group** (agent infrastructure: `ask_user`, `finish`, `delegate`, `store_fact`, `batch`, `declare_plan`, `execute_plan`, `update_checklist`, etc.) bypasses policy — it is a reserved group that cannot be configured and must not be extended carelessly. A tool with an undeclared group matches no allow-list and fails closed.
- **LLM-provider authentication** — API keys are supplied per provider via `${ENV_VAR}` expansion; local servers may omit keys.

**Rules:** Every new tool MUST declare a capability group (`ToolGroup` on `BaseTool`); side-effecting tools belong to a mutating group whose default policy is `user_confirm`. Tagging a tool `GroupSystem` requires explicit security review — it bypasses every gate. Never ship a tool in an `allow` group without a `ToolJudger` for its dangerous operations.

### Data Protection

- **At rest:** the SQLite database (`~/.c0wrk/database.db`) and config (`~/.c0wrk/config.yaml`) live in the user's home directory under default OS file permissions. Secrets are env-expanded at load, not stored as plaintext literals in committed config.
- **In transit:** outbound calls to LLM providers and MCP HTTP servers use TLS; an optional HTTP/HTTPS proxy (`proxy` config) with a bypass list and custom CA-cert directory (for corporate MITM) is supported.
- **PII / secrets in memory:** API keys are threaded to clients; conversation history and the vector index contain whatever the user's workspace contains — treat workspace contents as potentially sensitive.

**Rules:** Never log secrets, full API keys, or session tokens (`log/slog`). Never write API keys into committed files; always reference `${ENV_VAR}`. Never persist raw credentials into chat messages, blackboard facts, or error results.

### Secret Management

- Secrets enter via environment variables expanded in `config.yaml` (`${ANTHROPIC_API_KEY}`, `${OPENAI_API_KEY}`, `${TAVILY_API_KEY}`, proxy credentials). `config.LoadShellEnvironment()` runs before any other init so Finder-launched apps inherit shell env — this ordering is an invariant.
- Secrets MUST NEVER appear in: source code, log output, error messages returned to the LLM, CI logs, the vector index, blackboard facts, or tool results.

**Rules:** Treat every tool result and agent message as potentially entering the model context — never embed a secret in a value the agent can read or echo. On macOS, preserve the `LoadShellEnvironment()`-before-init ordering in `desktop/startup.go`.

### Dependency Management

The dependency graph is sizeable: ~368 Go module entries (`go.sum`) and ~701 npm packages (`frontend/package-lock.json`). Key security-relevant dependencies include the LLM SDKs (`openai-go`, `go-anthropic`), the MCP client (`mark3labs/mcp-go`), the vector store (`chromem-go`), the SQLite driver (`modernc.org/sqlite`), and the agent SDK (`github.com/v0lka/sp4rk`).

**Rules:** Pin via lockfiles (`go.sum`, `package-lock.json`) and verify checksums. Evaluate new dependencies for maintenance, license, and transitive risk before adding. External binaries downloaded at runtime MUST be SHA256-verified (the tool-manager's `verifyChecksum` pattern). MCP servers integrated dynamically MUST be treated as untrusted code (see ASI04).

### Logging, Monitoring & Incident Response

- Logging uses `log/slog` throughout; a `*slog.Logger` is threaded through constructors (no global `slog` in new code except the top boundary).
- Security-relevant events (tool confirmations, denials, policy escalations) surface to the operator via Wails events (`tool_confirm`, etc.).

**Rules:** Log security-relevant decisions (confirmations, denials, policy escalations, symlink traversals, blacklist matches) with the verified principal/tool context. **Never** log secrets, full API keys, passwords, or session tokens. Do not log full tool outputs that may contain workspace secrets.

---

## Agentic Application Security

c0wrk builds AI agents that plan, act, call tools, persist memory, manage subagents, and integrate external tool servers on behalf of users. This section follows the [OWASP Top 10 for Agentic Applications (2026)](https://genai.owasp.org/resource/owasp-top-10-for-agentic-applications-for-2026/), identifiers ASI01–ASI10. Every applicable category states **what the rules require**.

The unifying principle is **least agency** — grant an agent only the minimum autonomy required for a safe, bounded task. Ask, for every agent: what is its *reach* (least privilege) AND its *latitude* within that reach (least agency)?

### ASI01 — Agent Goal Hijacking

**Relevant.** The agent ingests untrusted content at runtime: files, `web_fetch`/`web_search` results, `bash_exec`/`ripgrep`/`glob`/`read_file` output, **all MCP tool output**, and multi-source `AGENTS.md`. A trusted user instruction and a hostile instruction hidden in a retrieved file look identical to the model.

**Rules:**
- **Instruction/data separation is mandatory.** All untrusted tool output MUST be wrapped in `<untrusted-content source="...">` tags (spotlighting) at the last point before the model context (`sp4rk` `buildStepMessages`). Any new tool that returns external/untrusted data MUST be marked `Untrusted: true` (or be an MCP tool, which is always untrusted).
- **Tag-breakout protection is mandatory.** Literal `<untrusted-content` sequences in output MUST be escaped (`StripUntrustedTags`) before wrapping so an attacker cannot close the delimiter early.
- **Treat retrieved/tool-produced text as data, never commands.** The injection-defense system prompt (`core/prompts/injection_defense.md`) MUST remain wired into the system prompt when `security.injection_defense.enabled` is true (default).
- **`AGENTS.md` sources are untrusted advisory.** All three sources (global, c0wrk-specific, project) MUST be wrapped in `<untrusted-content>`, framed as non-authoritative, and MUST NOT bypass the tool-policy pipeline (ADR-020).
- **Scope error-recovery tightly.** Tool diagnostics inside `<untrusted-content>` may be used only to repair the *same* failed operation; anything beyond that (new actions, following links, passing secrets) is treated as injection.

### ASI02 — Tool Misuse and Exploitation

**Relevant.** The agent can invoke many side-effecting tools; individually-allowed actions can be chained or looped into harm.

**Rules:**
- **Group-based least privilege.** Each tool declares a capability group; mutating groups default to `user_confirm`. Dangerous operations (delete, shell exec, web write) are programmatically restricted regardless of agent request.
- **Parameter schemas validated before execution** (type, range, allowlists) via the registry's param manager. A centralized required-field check (`validateRequiredFields`) runs in the registry for every tool as defense-in-depth, so a tool whose author forgot per-tool validation still rejects inputs missing a JSON Schema `required` parameter.
- **Shell-exec blacklist is mandatory** for `bash_exec`/`posh_exec`. The blacklist runs via `ToolJudger.Judge()` BEFORE workspace auto-approval so a blacklisted command with in-workspace paths (e.g. `rm -rf /workspace/.git`) still escalates to confirmation. Patterns MUST cover recursive deletion, privilege escalation (`sudo`), destructive disk ops (`mkfs`, `dd`), device writes, and pipe-to-shell (`curl | sh`). A blacklist match is a **canonical** hard reason, so it is deterministically backstopped to confirmation even if Smart Approve's strict judge returns ALLOW.
- **Tool-call allowlist, not denylist.** The registry exposes only registered tools; never expose arbitrary shell access beyond `bash_exec`/`posh_exec`.
- **Budget / loop-depth caps** must remain enforced (executor loop caps, tool limits in `config.yaml`).
- **Irreversible actions require a human-approval gate.** Confirmation MUST carry a human-readable `JudgeReasoning` so the operator understands *why* before deciding.
- **A `deny` group policy is never bypassed** by auto-approval, judge, or symlink check.

### ASI03 — Agent Identity and Privilege Abuse

**Relevant.** The agent (and delegated subagents) act under the user's own OS credentials and LLM API keys; their combined scope is the blast radius.

**Rules:**
- **Subagents operate with the verified principal inherited from the conductor** — delegation must not silently escalate privileges or assume a new identity.
- **Delegated credentials must be scoped to the minimum required.** Never give a subagent or MCP server credentials broader than the task needs.
- **Confused-deputy prevention.** A low-privilege input (e.g. untrusted file content suggesting a tool call) MUST NOT cause a high-privilege action without the normal policy/confirmation gates — the tool-policy pipeline is identity-blind and applies to every call.
- **Audit-log with the verified principal.** Security-relevant actions must record which agent/subagent/tool initiated them.
- **Delegation chains preserve and record the original caller** (delegation depth is capped; re-delegation requires explicit `allow_redelegate`).

### ASI04 — Agentic Supply Chain Compromise

**Relevant.** c0wrk integrates third-party tools, MCP servers, downloaded binaries, and Go/npm dependencies whose schemas and code may be forged or compromised.

**Rules:**
- **All MCP tools are untrusted by default** (`IsUntrusted()` true for every MCP tool) — their output is spotlighted, and their calls pass the full policy pipeline.
- **MCP server configs are executable.** Stdio MCP servers run an arbitrary `command`/`args`/`env` as a child process; HTTP MCP servers send `headers`/URL. Treat new MCP integrations as executing untrusted code — review the command, scope the `WorkDir`, and never forward secrets into MCP server `Env` unless required. Stdio child processes inherit only an allowlisted subset of the host environment (`PATH`, `HOME`, `USER`, `SHELL`, `LANG`, `LC_*`); anything else the server needs must be declared explicitly in its config `env`.
- **Downloaded binaries MUST be SHA256-verified** (`toolmanager` `verifyChecksum`); verification is **fail-closed** — a missing per-platform checksum refuses the binary rather than skipping verification, and checksum mismatches trigger re-download, never silent acceptance.
- **Pin and vet dependencies.** Lockfiles are the source of truth; review new direct dependencies for maintenance, license, and transitive risk before adding.
- **Review tool/MCP descriptions for deceptive language** before exposing them to the agent; a malicious description can steer tool selection.

### ASI05 — Unexpected Code Execution

**Relevant.** The agent generates and runs shell commands (`bash_exec`/`posh_exec`) and authors file edits executed by the user's shell/compiler.

**Rules:**
- **Shell execution is policy-gated, never auto-trusted.** `bash_exec`/`posh_exec` default to `user_confirm`; the blacklist Judge runs before auto-approval.
- **Path containment is enforced.** Operations outside session roots (workspace + temp + auxiliary dirs) ALWAYS require user confirmation, regardless of policy. Relative paths escaping via `..` are rejected by `resolvePath`. The host OS temp tree (`/tmp`, `os.TempDir()`, `%SystemRoot%\Temp`) is injected as an implicit session root on every task — see [Implicit Temp Roots](./specs/architecture/security-model.md#implicit-temp-roots) for the per-platform set and the accepted risk.
- **Symlink traversal is always detected** before policy resolution and forces confirmation — agents must not follow symlinks out of session roots silently.
- **Argument/command scanning is mandatory** for shell tools; suspicious shell expansions (`$var`, `$(cmd)`, backticks) that can mask paths MUST be flagged in the confirmation dialog.
- **Ephemeral / scoped execution.** Session temp directories are per-session (`~/.c0wrk/projects/<id>/<session>/temp/`); scratch artifacts are isolated, not global.

### ASI06 — Memory and Context Poisoning

**Relevant.** The agent persists memory across sessions: SQLite `session_messages` (conversation history), the `chromem-go` vector index (source-code embeddings + file-hash sidecars), and persistent blackboard facts. Poisoned memory can steer future sessions.

**Rules:**
- **Validate before write/read.** Memory stores are populated from tool output that is already spotlighted as untrusted; never treat persisted memory as trusted instructions.
- **Session isolation.** Temp directories and per-session workspaces are isolated; vector indexes are project-scoped.
- **Retention / TTL awareness.** Conversation history and facts persist indefinitely by default — contributors must not store secrets or injection payloads in facts (`store_fact`) or messages. `store_fact` enforces this at runtime: a heuristic secret-pattern scanner refuses to persist content matching common credential shapes (API keys, tokens, bearer strings), preventing durable secret disclosure via long-term memory.
- **Poisoning-pattern awareness.** Treat agent-authored `store_fact` content and vectorized documents as potentially adversarial when read back into context.
- **Never persist secrets** into the database, vector index, or blackboard.
- **Raw stdout/stderr is a persisted output channel.** The crash-capture subsystem (see [specs/domains/crash-logging.md](./specs/domains/crash-logging.md)) dup2-redirects fd 1/2 into `~/.c0wrk/logs/stderr.log` (rotated at 4 MiB, content never truncated) and wires `slog.Default()` through it, so output from native libraries, dependencies, and child processes that bypass `slog` entirely is captured verbatim and persists across restarts. The no-secrets rule therefore covers EVERY output channel, not just structured logs: never print credentials (API keys, tokens, bearer strings, credential-bearing URLs) to stdout/stderr anywhere in the process tree. Capture is always-on; `C0WRK_DISABLE_CRASH_CAPTURE=1` is the documented opt-out.

### ASI07 — Insecure Inter-Agent Communication

**Relevant.** Subagents are launched via the delegation protocol (`delegate`, `execute_plan`) and communicate over the shared blackboard.

**Rules:**
- **Delegation depth is limited and explicit.** Re-delegation requires `allow_redelegate` (capped by config); infinite agent chains are forbidden.
- **Inter-agent content is untrusted by default.** Output from a delegated subagent re-entering the conductor's context MUST be treated with the same instruction/data separation as any tool output.
- **Fail-closed delegation.** A failed/cancelled subagent must not silently corrupt the parent's plan; delegation progress is tracked and cancellable (`cancel_delegation`).
- **Defined cross-agent permissions.** A subagent's tool set and budget are scoped at launch (Subagent Profiles, `tools`/`max-steps`/`model`); a delegated agent MUST NOT exceed its granted tool set.
- **Impersonation resistance.** Agent identity is assigned by the conductor, not self-declared by the agent.

### ASI08 — Cascading Agent Failures

**Relevant.** The orchestrator → planner → executor → tools chain, plus subagent delegation, means one failure can propagate.

**Rules:**
- **Fail-closed by default.** Tool errors return error `ToolResult` to the LLM (it can adapt); unrecoverable failures cancel the context (`ConfirmDenyAndStop`, cancel).
- **Loop / budget caps are mandatory.** Executor loop caps, tool limits, and step caps bound runaway chains; the reflector intervenes when the trajectory looks wrong.
- **Per-dependency isolation.** MCP server failures, tool-manager download failures, and vector-index unavailability must degrade gracefully (e.g. vector RPCs return empty until ready) without crashing the agent loop.
- **Circuit-breaker behavior.** Repeated denials or reflector-flagged trajectories must escalate (reflection, stop), not loop silently.

### ASI09 — Human-Agent Trust Exploitation

**Relevant.** Confirmation prompts (`tool_confirm`), `ask_user` forms, and plan/goal review gates exist — and are vulnerable to approval fatigue and injection disguised as benign intent.

**Rules:**
- **Show raw intent, not agent summaries.** Confirmation requests MUST carry the actual tool input plus a human-readable `JudgeReasoning` (the *why*), so the operator can verify the real action — never just an agent-authored justification.
- **Never auto-approve irreversible actions.** Operations outside session roots, symlink traversals, and blacklist matches ALWAYS force confirmation.
- **Confirmation blocks until the user responds** (no timeout) — this is intentional; do not implement confirmation timeouts.
- **Distinguish denial outcomes.** `Deny` returns an error the agent adapts to; `Deny & Stop` cancels the whole task — the UI must make these distinct.
- **Resist approval fatigue.** The default policy is `user_confirm`; `auto_approve_workspace_writes` defaults to `false`. Convenience overrides that weaken confirmation must remain opt-in and documented.
- **Verify-on-edit runs confirmation-free but config-only.** After file edits, the executor may run the verification command (`executor.verify_on_edit.command`) via `registry.ExecuteUnattended`, skipping HITL confirmation — the command originates exclusively from user config (the model cannot set, change, or suggest it) and the feature is opt-in (disabled by default). `ExecuteUnattended` keeps every hard gate, fail-closed: required-field validation, disabled-tool checks, per-session shell blacklist, execute-group `deny`, and hard safety reasons (judge + symlink detection) — a hard reason blocks outright because there is no confirmation flow to escalate to. Skipped deliberately (input is fixed config, not model output): pre/post-execute hooks, advisory judging of soft reasons, HITL confirmation. `ExecuteUnattended` must never be wired into a model-facing tool-execution path. See [specs/domains/verify-on-edit.md](./specs/domains/verify-on-edit.md).
- **Smart Approve is opt-in and conservative.** When `security.smart_approve` is enabled, a strict OWASP ASI judge evaluates **every escalated call** — an effective `user_confirm` call, or a hard/soft safety reason surfaced by an `allow`-group tool — through the unified confirmation funnel (`smartApproveOrConfirm`); there is no separate bypass path for hard reasons. Only a strict ALLOW (clearly safe, narrowly scoped, reversible/read-only, no material ASI risk) skips the UI; CONFIRM, judge errors, timeouts, and unparseable responses ALWAYS fall back to manual confirmation. The strict verdict reasoning is shown to the user (never an agent-authored justification); the advisory "Ask Agent" button is hidden because the call was already evaluated. Workspace auto-approval retains priority. Smart Approve never weakens a `deny` group or the fail-closed nil-ConfirmFunc invariant, and the deterministic backstop forces confirmation for **canonical** hard reasons — fired controls (blacklist, SSRF, symlink escape) and unassessable inputs (degraded SSRF protection, an undeterminable URL/path), matched by typed `JudgeOutcome.ReasonCode` — even when the strict judge returns ALLOW.

### ASI10 — Rogue Agents

**Relevant.** Agents operate with autonomy (planning, tool use, delegation); drift, runaway loops, or goal corruption are possible.

**Rules:**
- **Autonomy bounds are mandatory.** Step caps, loop caps, token budgets, tool-output limits, service/main-call timeouts, and circuit breakers bound individual operations and trajectories. c0wrk has no session-wide wall-clock deadline; do not claim one exists unless it is implemented end to end.
- **Auditable receipt chain.** Plan steps, tool calls, delegations, and reflections are emitted as events and persisted — the trajectory must be reconstructable.
- **Kill-switch / circuit-breaker.** The operator can cancel or cooperatively pause a running task. Cancel remains terminal for the current task; pause persists a resumable checkpoint. `ConfirmDenyAndStop` and `cancel_delegation` must remain effective and unrecoverable for the current task.
- **Checkpoint integrity.** A paused conductor/subagent trajectory is persisted as paused, seeded into both executor and context manager on resume, and never reported as completed or failed. Graceful shutdown preserves active work as paused; explicit user cancellation remains cancelled. Archived sessions are read-only at the backend Send/Resume choke point.
- **Behavioral-drift monitoring.** The reflector (`reflect`) reviews the trajectory and may recommend replan/abort when direction is wrong.
- **Periodic alignment checks.** Goal proposals (`propose_goal`) and self-evaluation verdicts (`declare_goal_status`) keep the agent aligned with user intent; the orchestrator must not silently abandon or mutate the stated goal.

---

## Secure Coding Guidelines

### Go (backend / core)

- **Path containment via the centralized path API.** NEVER inline `strings.HasPrefix(path, root+"/")`, `filepath.Rel`+`HasPrefix(rel,"..")`, or hand-rolled `filepath.Join(ws, ".agents", ...)`. Use `pathutil.IsWithinPath`, `config.IsWithinPath`, `config.ProjectSkillsPath`, and the constants in `core/pathsegments.go`.
- **Shell/command construction.** Never build shell commands by string-concatenating untrusted input; pass arguments as discrete argv elements. The `bash_exec` blacklist and `mvdan.cc/sh` AST parsing must remain the parsing path for symlink detection.
- **SQL.** Parameterized queries only (`modernc.org/sqlite`); no string-interpolated SQL. `sqlclosecheck`/`bodyclose`/`noctx` linters are enabled.
- **Errors.** `errorlint` + `perfsprint` are on. Use `%w` for wrapping, `errors.Is/As`; never `fmt.Errorf` where `errors.New` suffices; never `fmt.Sprintf("%s", s)`.
- **TOCTOU in file ops.** Resolve and validate paths atomically; re-check after `os.Lstat`/`os.Stat` when the result authorizes an action (symlink detection already does per-component `Lstat`).
- **Goroutine lifecycle.** Avoid goroutine leaks; context cancellation must propagate to MCP server processes, downloads, and the executor loop.
- **Integer/loop safety.** Bound untrusted sizes (read windows, token budgets, `agents_md_max_bytes`, attachment conversion/image dimensions, vector `max_file_size`/`max_chunk_size`/`max_chunks_per_file`, embedding sub-batches); avoid unbounded allocation from tool output or workspace files. Oversized/pathological files are skipped as a whole rather than partially indexed.
- **`unsafe` / cgo.** The SQLite driver is pure Go, but the desktop application is not globally CGO-free: Wails/native platform integration and ONNX-related builds require native toolchains on supported platforms. Do not add `unsafe`, cgo, or new native-library dependencies without explicit justification, platform build coverage, lifecycle cleanup, and a security review.

### React / TypeScript (frontend)

- **XSS.** Never render untrusted HTML directly. Markdown stays behind `react-markdown` + `rehype-sanitize`; Mermaid output stays behind strict Mermaid security settings plus DOMPurify before insertion. Any exceptional `dangerouslySetInnerHTML` use requires sanitized, non-user-controlled output and a focused security test — raw tool/agent/workspace HTML is forbidden.
- **Clipboard and native drop boundaries.** Clipboard image → file URLs → text precedence is intentional. File URLs and `files:dropped` paths must enter through the shared attachment staging/validation pipeline; never navigate the webview to a dropped path or bypass vision/format/size checks.
- **Stable Zustand selectors.** Never allocate arrays/objects inside `useStore` selectors (React 19 `useSyncExternalStore` reference-equality → infinite re-render, error #185). Return direct references; derive with `useMemo`.
- **Token storage.** Do not store secrets in `localStorage`; persist only UI state via Zustand middleware.
- **Event-data validation.** Validate Wails event payloads with type guards at the ingestion point; everything downstream is typed.
- **One import path for backend calls.** All RPC through `@/api/*`; never import `wailsjs/go/desktop/App` directly.

### Framework / integration specific

- **Wails bindings** (`frontend/wailsjs/...`) are generated — do not hand-edit; rebuild to regenerate. New frontend-callable methods go in the matching `backend/frontend_api_*.go`.
- **Tool registry pattern.** Reusable built-in tools live in `sp4rk/tools/builtins/`; c0wrk-specific tools (e.g. `ask_user`) in `core/tools/`; wire via `core/tools/RegisterBuiltinTools`. Every tool declares a capability group (`ToolGroup` on `BaseTool`, ADR-024); the group drives both policy and tool budgets.
- **MCP integration.** New MCP tools are always untrusted; their output is spotlighted and their calls pass the policy pipeline. Do not mark MCP tools trusted.
- **Auxiliary work directories.** Prompt discovery may add only existing directories and must deduplicate through the workdir subsystem. Treat every added directory as a deliberate expansion of session roots; do not expose implicit OS temp roots or security-only roots in the prompt/UI.
- **RESEARCH artifacts.** Explicit roots are accepted only inside the project workspace through the centralized containment API. Seed skills non-destructively: user-modified skills and artifacts are preserved and remain untrusted. Recursive watcher add/remove must follow enable/disable lifecycle and must not outlive app shutdown.
- **Prompts are data.** `core/prompts/*.md` are `go:embed`ded; tests verify every `.md` is referenced — update both when adding/removing prompts. Orchestration uses the unified Conductor prompt path; do not reintroduce planner-family prompt assets without a new architectural decision.

---

## Rules for AI Coding Agents

Any AI coding agent (c0wrk itself, Copilot, Cursor, etc.) working on this repository MUST:

1. **Read and follow this SECURITY.md** before making changes, plus [`specs/architecture/security-model.md`](./specs/architecture/security-model.md), [`specs/decisions/020-multi-source-agents-md-threat-model.md`](./specs/decisions/020-multi-source-agents-md-threat-model.md), and (for any self-update work) [`specs/decisions/023-auto-update.md`](./specs/decisions/023-auto-update.md).
2. **Never weaken the tool-policy pipeline.** Do not change a mutating group's policy away from `user_confirm` in defaults, remove blacklist entries, or bypass symlink/SSRF/path-containment checks. A **canonical** hard safety reason (blacklist, SSRF, symlink escape, or an unassessable input: degraded SSRF protection, an undeterminable URL/path) must never become Smart Approve-passable — keep the deterministic backstop that overrides a strict ALLOW verdict to confirmation, keep it keyed off the typed `JudgeOutcome.ReasonCode` contract, and keep the real-judge drift-guard test (`core/tools/registry_canonical_reasons_test.go`) green.
3. **Never mark untrusted output as trusted.** External/MCP tool output stays wrapped in `<untrusted-content>`; new external tools stay `Untrusted: true`.
4. **Never hard-code or log secrets.** Use `${ENV_VAR}`; never write API keys, tokens, or passwords into source, logs, tool results, facts, or messages.
5. **Use the centralized path API** for all filesystem containment — no inline `HasPrefix`/`filepath.Rel` checks.
6. **Never tag a tool `GroupSystem`** (the reserved group that bypasses all policy) without explicit security review, and never leave a tool's group undeclared — undeclared groups match no allow-list (fail closed).
7. **Never `go install` the ONNX runtime differently** — always via `make fetch-onnx`.
8. **Never commit** secrets, `.cache/`, `build/bin/`, `config.local.yaml`, coverage outputs, or anything in `.gitignore`.
9. **Run `make build` → `make lint` → `make test`** before declaring done; all three must be clean.
10. **Never weaken the self-update integrity gate.** The `core/updater/` pipeline is a supply-chain delivery vector (ADR-023): do not make SHA256 verification optional, skip the fail-closed archive removal on mismatch, downgrade HTTPS-only asset/checksum URLs, bypass `ErrNonStandardLocation` validation, or remove the zip-slip/tar-slip traversal guards in extraction.
11. **Surface contradictions** between instructions and security policy via `ask_user` rather than resolving them silently.

---

## Security-Related Configuration Files

| File | Purpose |
| --- | --- |
| [config.example.yaml](./config.example.yaml) | Authoritative reference for every security tunable: `security.groups` (per-group `allow`/`user_confirm`/`deny` policies + the `execute` blacklist), `security.injection_defense.enabled`, `security.auto_approve_workspace_writes`, `security.smart_approve`, `security.judge`, `toolLimits`, `proxy`, LLM provider keys (`${ENV_VAR}`). |
| [.golangci.yml](./.golangci.yml) | Linter config enforcing `errcheck`, `govet`, `staticcheck`, `errorlint`, `bodyclose`, `noctx`, `sqlclosecheck`, `perfsprint`, `unconvert`, `depguard`, etc. |
| [.gitignore](./.gitignore) | Excludes `.cache/`, `build/bin/`, `config.local.yaml`, coverage outputs. |
| [.github/workflows/ci.yml](./.github/workflows/ci.yml) | CI gate: build / lint / test on Linux, macOS, and Windows. |
| [.github/workflows/release.yml](./.github/workflows/release.yml) | Release pipeline. |
| [specs/architecture/security-model.md](./specs/architecture/security-model.md) | Canonical c0wrk security model (tool policies, session roots, injection defense, symlink/bash-blacklist). |
| [specs/decisions/020-multi-source-agents-md-threat-model.md](./specs/decisions/020-multi-source-agents-md-threat-model.md) | Threat model for multi-source `AGENTS.md` prompt injection. |
| [specs/decisions/023-auto-update.md](./specs/decisions/023-auto-update.md) | Threat model for the self-update supply-chain delivery vector (single-binary re-exec, SHA256-only, unsigned). |

---

## Revision History

| Date       | Version | Notes                                                                                                  |
| ---------- | ------- | ------------------------------------------------------------------------------------------------------ |
| 2026-08-07 | 1.0     | Initial SECURITY.md: threat model + ASI01–ASI10.                                                       |
| 2026-08-07 | 1.1     | Security hardening: untrusted-flagging for 5 external-output tools (ASI01/ASI07); stdio MCP env allowlist isolation (ASI04); pipe-to-shell blacklist pattern; `max_redelegationDepth` exposed in config (ASI07); security-event logging at policy denials/blocks (ASI03/ASI09); fail-closed checksum for missing per-platform hashes (ASI04); `store_fact` secret-pattern scanner (ASI06); centralized required-field validation (ASI02); eslint `react/no-danger` (FE-R1); npm audit fix. Rules updated to reflect now-enforced controls; wall-clock time-box documented as accepted trade-off. |
| 2026-08-09 | 1.2     | Self-update attack surface: documented the in-app auto-update pipeline (`core/updater/`) as an ASI04 supply-chain delivery vector; added the unsigned/SHA256-only trade-off to Known Risks (ADR-023); added rule 10 forbidding weakening the self-update integrity gate (fail-closed SHA256, HTTPS-only, location validation, traversal guards). |
| 2026-08-11 | 1.3     | Smart Approve: strict OWASP ASI (ASI01–ASI10) LLM judge can now automatically resolve effective `user_confirm` calls (opt-in, `security.smart_approve`, default off). Only a strict ALLOW skips UI; CONFIRM/timeout/error/unparseable always fall back to manual confirmation with the verdict reasoning shown and "Ask Agent" hidden. Workspace auto-approval retains priority; `always_allow`/`always_deny`/symlink-forced confirmation unchanged. Also fixes fail-closed behavior: nil `ConfirmFunc` now denies `user_confirm` execution instead of executing silently (aligns with the sp4rk engine invariant). Updated Known Risks to reflect the judge is no longer purely advisory when Smart Approve is enabled. |
| 2026-08-15 | 1.4     | Tool-capability group policies (ADR-024): per-tool `security.tool_policies`/`security.default_policy` replaced by `security.groups.<group>.{policy, blacklist?}` over 8 declared groups (reserved `system` bypasses policy); skill-policy overrides and per-tool registry overrides removed (group policy is the single resolution layer); the legacy per-tool schema is removed outright — stale keys have no effect; judge reasons classified hard (blacklist/SSRF/symlink escape — never Smart Approve-passable) vs soft (path containment); subagent/verifier tool budgets keyed by group tokens. Rules 2/6 and the Authorization section updated. |
| 2026-08-17 | 1.5     | Verify-on-edit (ASI09): documented the confirmation-free `registry.ExecuteUnattended` shell path used by the post-edit verification runner — opt-in (`executor.verify_on_edit.*`, default off), command sourced exclusively from user config, hard gates retained (blacklist, execute-group `deny`, hard safety reasons, required-field validation), HITL/advisory-judge steps deliberately skipped; the path must never serve model-initiated tool calls. |
| 2026-08-20 | 1.6     | Expanded the threat model and contributor rules for clipboard/native-drop attachments, bounded vector ingestion, prompt-discovered workdirs, implicit temp roots, resumable pause/subagent trajectories, and experimental RESEARCH artifacts/watchers; recorded the unified Conductor prompt-path invariant. |
| 2026-08-30 | 1.7     | Crash-capture subsystem: added `logs/stderr.log` (always-on raw fd 1/2 mirror) to the Assets table and an ASI06 rule that the no-secrets rule covers raw stdout/stderr of the whole process tree (native libraries and child processes included), with `C0WRK_DISABLE_CRASH_CAPTURE=1` as the documented opt-out. |
