# ADR-057: Flow-Based Network Verdicts (Cradle and Ingest)

## Status

Accepted — amends [ADR-052](./052-flowsh-command-analysis.md) (criteria C5–C7 and the shell-analysis digest version). Engine side in sp4rk (`tools/shellanalysis.go`), over the flow model contributed to flowsh.

Amended by [ADR-061](./061-package-runner-resolution-exec-scope.md): a new hard **non-canonical** criterion **C10** (`command_exec_outside_roots`) is appended, the criteria set extends to **C1–C10**, the digest is bumped to **`sp4rk-shell-analysis/v4`** (over flowsh v0.4.0's binder-level runner/bin-path resolution), and winner selection is now by **severity first** (ties broken by the fixed priority order). Every "C1–C9" reference below reads **C1–C10**, and every digest "v3" reference below reads **v4**, under this amendment.

## Context

[ADR-052](./052-flowsh-command-analysis.md) made the deterministic download-cradle criterion (**C5**, `command_download_cradle`, hard **canonical**) fire on the **co-occurrence** of analyzer ⊤/conservative **and** `NetEgress`, and then required **"cradle evidence"** to keep that canonical verdict honest — the **C5 consistency rule**: the egress had to be pinned to a concrete host/URL destination, or paired with a real secret→sink exfil pairing, else the verdict degraded to the non-canonical C6.

That evidence rule was a **proxy**, not the fact it stood for. It inferred *"fetched content reaches execution"* from two things that are not that fact:

1. the mere co-presence of an unbounded command and a network egress, and
2. the egress having resolved to a concrete host.

Condition (2) made C5 canonicality hinge on the identity/authority of the **destination host** — a **host-reputation trigger**: an ad-hoc, agent-chosen host fired a canonical deny even when nothing executed, and the trigger could not distinguish *"downloaded and executed"* (a true cradle) from *"downloaded to a file"* (a persistence, not an execution) at all. Its known failure modes were structural:

- **Co-occurrence is not a flow.** `curl -o out.pdf URL` (a plain download) and `curl URL | sh` (a cradle) both carried ⊤ ∧ `NetEgress`; only the evidence guard kept the former from manufacturing a canonical deny, and the guard itself turned on host resolvability.
- **Phantom destinations.** The evidence rule existed largely because unbounded pipelines misparsed subcommand names and commit SHAs as egress hosts (the silent-mode audit's `git`/`awk` cases); those had to *degrade to C6* by an explicit consistency rule rather than never firing.
- **Host authority is a judgment.** Whether a destination is "authoritative for this artifact class" (a paper from its publisher vs. an arbitrary user-content bucket) is not a structural fact the effect IR can assert — it is exactly the kind of contextual call an LLM judge makes and a deterministic engine cannot.

flowsh meanwhile gained a genuine **data-flow** model: `Effect.NetFlow` (`FlowNone`/`FlowCradle`/`FlowIngest`), populated by the analyzers and aggregated into `Score.CradleFlows` / `Score.IngestFlows` (`DetectCradleFlows` / `DetectIngestFlows`). A flow is asserted only where the analysis proved the network content **reached** a sink — a pipe to a shell/interpreter, a `source`/`.` of a fetched path, a sink invoked on a command substitution, or the exec of a path a download wrote (cradle); a download client writing the fetched body to a file (ingest). A `NetEgress` alone, a sink alone, or an untagged sink yields **no** flow.

## Decision

### D1. The canonical cradle is keyed on the cradle **flow**

C5 (`command_download_cradle`, hard **canonical**) now fires iff `Score.CradleFlows` is non-empty — the analysis **proved** fetched network content reaches a shell/interpreter. The ⊤/conservative condition and the host-resolution evidence condition are both **gone**: a canonical cradle verdict is now backed by the very evidence it ships (`cradleFlows`), so it can never contradict its own digest. A fetch that is never executed establishes no cradle flow; a fetch whose bytes land only on stdout/stderr establishes nothing. The reason-code **string** `command_download_cradle` is unchanged — it remains the cross-repo contract (ADR-031).

### D2. The host-authority judgment moves to the judge (new non-canonical C7 ingest)

The former host-reputation trigger is replaced by a new hard **non-canonical** criterion **C7 `command_external_content_ingest`**, fired by the **ingest flow** (`Score.IngestFlows` non-empty): a download client wrote the content it fetched to a file (`curl -o`/`-O`, `wget -O`/default; a `-`/stdout target is excluded, and VCS sync — `git clone/fetch/pull` — is never an ingest). It is deliberately non-canonical because the decision it encodes — *is this destination host authoritative for this artifact class, and what happens to the saved file?* — is a judgment about the destination, not a structural fact. The strict judge is handed the flow (and the destination) and applies the **fail-closed-on-arbitrary-host** doctrine: a canonical publisher/preprint server, a code host's own API, or an official registry is benign, while an ad-hoc CDN or user-content bucket keeps the ingest concern in force.

This is a deliberate **trust-boundary shift**: the deterministic engine now decides only the **structure** (was externally-fetched content persisted to disk, or executed?), while the **authority/trust** of the host becomes an LLM-judge decision. The move is consistent with ADR-052's ownership split — the engine owns the structural floor, the extension (and the contextual judgment) does not ship as compiled-in policy — and it is *fail-closed*: the shape is not silent (C7 is hard), only its final resolution is delegated.

### D3. C6's unbounded boundary

C6 (`command_unbounded_analysis`, hard **non-canonical**) fires on `unboundedWrite || (unbounded && !hasCradleFlow)` — an irreversible write whose target the analyzer could not resolve, **or** an analyzer-unbounded (⊤/conservative) command with **no** established cradle flow. Keying the unbounded disjunct on `!hasCradleFlow` (rather than on a *resolved egress target*, the former evidence rule) keeps a genuinely unbounded egress escalating — an unresolved download, or a dialect whose pipe the analysis could not follow into a cradle — instead of passing silently; the established-cradle shape belongs to C5. The `unboundedWrite` disjunct is deliberately independent of egress: an unresolved irreversible write is destructive wherever it lands.

### D4. Digest v3 and the effect-IR schema bump

The digest version constant `ShellDigestSchemaVersion` is bumped to **`sp4rk-shell-analysis/v3`**; `ShellAnalysisDigest` gains two top-level arrays — `cradleFlows` and `ingestFlows` — each a list of `ShellFlowPairDigest` (`{source, sink}` effect keys, `Effect.Key()` form), placed right after `exfilPairs` and populated from `report.Score.CradleFlows` / `report.Score.IngestFlows`. On the flowsh side the additive flow field bumps `engine.SchemaVersion` **`effect-ir/v1` → `effect-ir/v2`** (optional `netFlow`; flowless effects serialise byte-for-byte as before) and `analysis.ToolVersion` **`flowsh/v2` → `flowsh/v3`**. The digest reaches every judge as before (tool `Judge`, strict `AnalysisContext` behind the `shell_analysis` untrusted envelope, advisory Ask-Agent block).

**The canonical set is unchanged.** C5 is still the only canonical network criterion; the new C7 ingest is non-canonical, so the canonical hard set, and therefore `ExecuteUnattended`'s block set and the interactive backstop, are unaffected.

### D5. Criteria renumbering

The credential-access criterion moves **C7 → C8**, and out-of-root scope **C8 → C9**; the ingest criterion occupies **C7**. C1–C4 are unchanged. The full fixed-priority set is now **C1–C9**:

| # | Condition | Reason code | Severity | Canonical |
| --- | --- | --- | --- | --- |
| C1 | exfiltration pair: secret read → tainted network egress | `command_exfil_flow` | hard | yes |
| C2 | privilege-escalation effect (e.g. SUID install) | `command_privilege_escalation` | hard | yes |
| C3 | direct write/metadata on a system path or non-harmless raw device | `command_system_write` | hard | yes |
| C4 | irreversible destructive command (KB class D/E) writing outside the session roots | `command_destructive_outside_roots` | hard | yes |
| C5 | an established **network→code-execution cradle flow** (`cradleFlows` non-empty) — fetched content reaching a shell/interpreter | `command_download_cradle` | hard | yes |
| C6 | analyzer ⊤/conservative **without** a cradle flow, **or** an irreversible write whose target is ⊤ | `command_unbounded_analysis` | hard | **no** |
| C7 | an established **network→filesystem ingest flow** (`ingestFlows` non-empty) — a download client wrote fetched content to a file (stdout fetch excluded) | `command_external_content_ingest` | hard | **no** |
| C8 | credential access without an exfil pair | `credential_access` | soft | — |
| C9 | direct FS effect outside the session roots (system writes/metadata are C3's; raw-device reads exempt) | `outside_session_roots` (reused) | soft | — |

### D6. PowerShell cradle gap

The cradle producer is implemented only in the bash front (`flowsh/front/bash/exec.go` `sinkFlowRole`, fed by the pipeline value flow and by the provenance of a sink's command substitution); the PowerShell front marks **no** cradle flow. `Invoke-WebRequest … | Invoke-Expression` therefore no longer establishes a cradle flow and now escalates as the non-canonical **C6** instead of the canonical C5. It still **escalates** (never a silent allow), and the judge is handed the same command text it always saw; restoring canonical PowerShell cradles is a flowsh-side follow-up (a PS value-flow producer), tracked as a known gap.

## Consequences

**Positive**

- The canonical cradle verdict is now **structural evidence**, not a host proxy: ADR-052's invariant — *"a canonical reason never contradicts the digest it ships in"* — holds by construction, and it now holds for the **read-only/stdout fetch** shape too. A fetch that neither executes nor persists establishes neither flow: it fires neither C5 nor C7, so it can no longer manufacture a canonical deny from an unresolved host the digest cannot point at. The explicit C5-consistency degrade rule is no longer needed.
- The **TD 8/8 invariant** is preserved. The silent-mode live audit's hard requirement — all eight TRUE_DENY corpus events denied, `FALSE_ALLOW 0`, deny-precision ≥ 0.80 (ADR-052 §6.4) — is met by the flow model for the same reason it was met before: every true-deny cradle shape in the corpus (`curl | sh`, `wget | bash`, `bash <(curl …)`) still establishes a cradle flow → canonical C5, and the benign classes the audit separated (a stdout fetch, a plain document download) establish no cradle flow, reaching the judge only through the non-canonical C6/C7. The deterministic floor below the funnel is untouched (ADR-053 D2).
- The host-authority judgment is where it belongs — in the LLM judge, with the digest carrying the destination so the call is informed and auditable (ADR-053 D6) rather than a compiled-in reputation list.

**Negative / risks**

- The decision "is this host authoritative?" left the deterministic layer for the judge. Mitigations: C7 is **hard** (never a silent pass), the strict judge applies the fail-closed-on-arbitrary-host doctrine, the digest carries `ingestFlows` (so the judge sees the destination), and every decision is audited (ADR-053 D6). A wrong/lenient judge ALLOW is the same accepted cost ADR-053 D4 already names for the silent `judge` terminal.
- PowerShell cradles lost canonicality until flowsh marks them (D6). They still escalate (non-canonical C6); the gap is a flowsh-side producer, not a relaxation of a gate.

## Alternatives Considered

- **Keep C5 as the ⊤ ∧ `NetEgress` co-occurrence + evidence rule and add ingest alongside.** Rejected: two overlapping proxies whose canonical/clearable split still turns on host resolvability, and a co-occurrence that cannot tell execution from persistence. The flow model states the fact directly.
- **Make the ingest criterion canonical too.** Rejected: "is this host authoritative for this artifact class?" is a judgment, not a structural fact; canonicalizing it would force a human card on every routine document/archive fetch on the interactive paths. The `command_download_cradle` precedent is precise because *execution* is a structural fact — ingest is not.
- **Keep the host-reputation trigger as a deterministic allowlist of authoritative hosts.** Rejected: a shipped authoritative-host list is the same stale-by-construction, ownership-confused policy the blocklist removal rejected (ADR-052); it would also need hand maintenance per artifact class and would fail closed on every host a legitimate workflow uses.
- **Key C6's unbounded egress disjunct on a resolved destination (keep the evidence rule) instead of `!hasCradleFlow`.** Rejected: it is the rule this ADR removes — an unresolved download would silently pass. Keying on `!hasCradleFlow` keeps the ambiguous shape escalating.

## References

- [ADR-052](./052-flowsh-command-analysis.md) — the deterministic shell floor this amends (criteria, digest, canonical set)
- [ADR-053](./053-silent-mode.md) — the silent `judge` terminal whose precision the digest evidence serves
- [SECURITY.md](../../SECURITY.md) — Shell Command Analysis (criteria table, canonical set, ⊤ semantics)
- [specs/architecture/security-model.md](../architecture/security-model.md) — Flowsh criteria, canonical set and ⊤ semantics
- flowsh: `engine/effect.go` (`FlowRole`, `Effect.NetFlow`), `engine/taint.go` (`CradleFlow`/`IngestFlow`, `DetectCradleFlows`/`DetectIngestFlows`), `engine/score.go` (`Score.CradleFlows`/`IngestFlows`), `bind/bind.go` (ingest producer), `front/bash/exec.go` (cradle producer)
- sp4rk: `tools/shellanalysis.go` (`evaluateShellReport`, `ShellAnalysisDigest`, `ShellFlowPairDigest`, `ShellDigestSchemaVersion`), `tools/safety.go` (`ReasonCodeCommandExternalContentIngest`)
