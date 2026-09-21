# ADR-054: Symlink Gate Is a Literal-Path Extractor

## Status

Accepted

## Context

The symlink gate (sp4rk's `DetectSymlinksInToolInput` consumed by the host registry's `symlinkHardReason`) carried a second responsibility besides symlink detection: flagging shell commands containing variable expansions (`$var`, `$(cmd)`, backticks, `$env:...`, process substitution) as **suspicious** — a hard `symlink_suspicious` escalation routed to the strict LLM judge and the confirmation card.

That check duplicated coverage the deterministic flowsh analysis ([ADR-052](./052-flowsh-command-analysis.md)) already provides on the same call: constructs the static token walk cannot see through land on the non-canonical C6 (`command_unbounded_analysis`) criterion, and out-of-root effects on C4/C9 — both assessed more precisely from the effect IR than from token walking. The built-in shell tools' `Judge` had already dropped its static unresolvable-token and containment stages for exactly this reason; the symlink gate was the last consumer.

The check also carried a large maintenance tail: the validated command-substitution machinery ([ADR-038](./038-validated-command-substitution.md)) — `shellEnvBindings.go` (~2000 lines), the shell-path resolvers in `shellpaths.go` (~1350 lines), and the PowerShell static-binding tokenizer — existed mostly to decide when an expansion was benign enough NOT to fire the flag. And operationally the escalation cluttered the LLM judge's work: every `PKGS=$(...)`-style command produced a hard reason the strict judge had to assess, spending tokens per session on noise the deterministic layer had already covered.

## Decision

The symlink gate is a **pure literal-path extractor**. All variable-expansion checks (and their escalation) are removed from the bash and posh judges' symlink path:

- `DetectSymlinksInToolInput` returns traversals only (`inside`, `outside`); the `suspicious` flag no longer exists. Shell commands contribute their **literal** paths (unquoted, single-quoted, and double-quoted literals); dynamic parts contribute nothing and never escalate.
- The host registry's `symlinkHardReason` escalates **escapes only**. `ReasonCodeSymlinkSuspicious` is retained in the sp4rk reason-code contract for wire stability (the precedent of `unresolvable_path_token`) but is no longer fired by any built-in path.
- The orphaned ADR-038 machinery is deleted from sp4rk (`shellEnvBindings.go`, the shell-path resolvers, their tests). ADR-038 is superseded.
- Dynamic constructs (`$var`, `$(...)`, unparseable input) are the flowsh analysis's domain — unchanged.

## Consequences

- Fewer false-positive escalations for ordinary shell idioms; the strict LLM judge no longer spends tokens assessing expansion noise.
- A symlink reachable only *through* a variable (`X=$(cat link/secret); cat $X`) is no longer surfaced by the symlink walk — accepted: flowsh assesses the command holistically, and the deterministic floor (C1–C9) does not depend on symlink-walk coverage of dynamic constructs.
- **Accepted residual — a statically-resolvable in-root symlink.** The gate extracts literal paths only, so a symlink whose target *escapes the roots* but which is reached through a **dynamically composed** path that statically resolves to an in-root path is escalated by neither layer. Concretely, with `X=./sub; cat "$X/link"` where `<ws>/sub/link` is a symlink out of the roots, flowsh resolves the read to the textual in-root path (no criterion fires) and the literal-path symlink walk never sees the dynamic `$X` — so under an `allow` execute policy the out-of-root read executes with no card and no hard reason. This is the same accepted-risk class ADR-054 takes for the fully-⊤ case, here reached without ⊤; the shape is pinned by a test in `core/tools/symlink_test.go`.
- The sp4rk `tools` package shrinks by ~3300 lines plus their tests; the three live helpers (`isASCIILetter`, `isPathComponentChar`, `isPureSeparatorRunToken`) were relocated to their consumers (`shellanalysis.go`, `judge.go`).
- An escape through a **literal** path is still a canonical hard reason, backstopped beyond a strict ALLOW (interactive scope).

## Alternatives Considered

- **Keep the checks and widen ADR-038.** Rejected: every widening of the exception machinery added more code to avoid firing a flag whose information flowsh already provides — pure negative ROI.
- **Downgrade `symlink_suspicious` to soft.** Rejected: a soft reason still consults the strict judge (same token spend, weaker posture); the redundancy argument removes the check, not its severity.
