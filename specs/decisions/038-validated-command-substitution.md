# ADR-038: Validated Command Substitution

## Status

**Superseded by [ADR-054](./054-symlink-gate-literal-paths-only.md)** — the variable-expansion checks this ADR refined were removed entirely from the symlink gate: the walk is a pure literal-path extractor, the `suspicious` flag no longer exists, and the assessability machinery (binding promotion, fail-closed union, inner-path surfacing — `shellEnvBindings.go`, the shell-path resolvers, the PowerShell static-binding tokenizer) was deleted from sp4rk. (Earlier amendment by [ADR-052](./052-flowsh-command-analysis.md) had already removed the static unresolvable-token and containment stages from the shell tools' `Judge`.)

## Context

The shell safety layer extracts path-like tokens from `bash_exec` / `posh_exec`
commands to feed two independent gates:

1. **Path containment** — `tools.PathsOutsideRoots` (behind the shell tools'
   `ToolJudger.Judge`) reports tokens that resolve outside the [session
   roots](../architecture/security-model.md#session-roots-workspace-temp-directory-and-auxiliary-directories).
2. **Symlink detection** — `tools.DetectSymlinksInToolInput` walks each extracted
   path for symlink traversals (see [Symlink
   Confirmation](../architecture/security-model.md#symlink-confirmation)).

Neither gate is a shell interpreter: both reason about the command's **static**
path tokens. A token the extractor cannot resolve statically — a bare `$VAR`
reference, a `$(cmd)` / `` `cmd` `` substitution, a `<(...)` process substitution
— historically marks the **entire call suspicious**. The whole command is then
treated as potentially path-masking and escalates to confirmation, because "if we
cannot see the path, we must not auto-approve."

That fail-closed default is correct for genuinely dynamic input, but it produces a
false positive on an extremely common, entirely static pattern: **capture a
command's output into a variable, then use the variable**.

```bash
PKGS=$(go list ./... | grep -v node_modules)
go test $PKGS ./... 2>&1 | tail -50
```

Here the substitution's inner command is fully literal — no dynamic reference, no
opaque construct (`eval`, `source`, a rebinding builtin), no unresolvable path
token — so there is nothing the substitution can smuggle in that no pass could
see. Yet because the reference is a substitution, the call escalated on every
run.

### Forces

- **Keep the fail-closed default.** The safety value of the gate comes from
  escalating anything it cannot assess. The exception must be narrow and must
  fall back to escalation the moment the inner command is anything less than
  fully static.
- **Reuse one assessment pipeline.** The bash extractor already builds an
  `envBindings` summary (`collectShellEnvBindings`) that models literal
  assignments, dynamic bindings, opaque constructs, and unresolvable path tokens.
  The inner command's assessment must reuse that same machinery, recursively, not
  a parallel notion of "safe".
- **Detect paths hidden behind a substitution.** If a substitution is declared
  assessable, its inner command's literal paths must still reach the symlink
  walker — otherwise an approved substitution would become a blind spot for the
  very traversal the gate exists to catch.
- **Preserve dialect parity.** `posh_exec` uses a char-level heuristic tokenizer
  (no PowerShell AST is available in this module). It must offer the same
  treatment for its own assignment form, or the two dialects would diverge.
- **Own the implementation in the engine.** The extractors live in the SDK
  (`github.com/v0lka/sp4rk/tools`). During the current dual-repo cycle c0wrk
  consumes the change through the repo-root `go.work`
  ([ADR-031](031-gowork-repo-root.md)); the c0wrk side adds only tests that pin
  the observable behavior.

## Decision

Recognize a **validated command substitution**: an assignment-form `VAR=$(...)`
whose inner command is fully assessable no longer escalates, and its name is
treated as *assessable* (not hostily dynamic) on later reference. Six decisions
define the behavior.

### D1 — Assignment form only

Only a **pure command substitution in assignment position** is eligible: the RHS
of `VAR=$(...)`, or the double-quoted form `VAR="$(...)"` wrapping exactly one
substitution. A **bare `$(...)` in argument position** — `cat $(echo x)` — is
**not** eligible and stays suspicious, because its value is used directly, with
no assignment to reason about. Backquote substitutions and the mksh
`${ …;}` / `${|…;}` forms are also rejected as non-pure (fail-closed).

### D2 — Same full pipeline, applied recursively

The inner text is assessed by the **same** pipeline that governs the outer
command, recursively: it must parse; a fresh `collectShellEnvBindings` pass must
report **no** opaque construct and **no** dynamic binding; every inner word must
be statically assessable; and `UnresolvablePathTokens(inner, ShellBash)` must be
empty. Inside the inner command, a nested pure substitution is assessable iff it
is itself assessable (recursion), and a plain `$NAME` / `${NAME}` reference is
assessable only when `NAME` is itself bound solely by an assessable substitution.
Parameter-expansion modifiers, arithmetic, process substitution, globbing, ANSI-C
/ locale quotes, backquotes, and any composition with such a part all fail closed.

### D3 — Full suppression of the escalation

When a name is promoted, the binding stops producing a safety signal:

- A later reference to the name (a `ParamExp`) contributes nothing to the literal
  and does **not** set the unexpandable flag — the name is deliberately **not**
  unassessable, so `go test $PKGS` does not escalate.
- The approved substitution node itself does **not** set the unexpandable flag.
- The name is **not resolvable**: its value is the substitution's unknown output,
  so it is never expanded to a literal (assessable ≠ resolvable).
- The **inner command's literal paths are surfaced**: `extractBashPaths` recurses
  into an approved substitution and merges the inner command's literal paths into
  the same path set the symlink walker consumes. A target reachable through a
  symlink only via such a substitution is therefore still detected.

### D4 — Implemented in the engine (sp4rk), consumed via `go.work`

The extractors and the binding model live in the SDK
(`github.com/v0lka/sp4rk/tools`: `shellEnvBindings.go` for the bash binding
summary and resolution, `symlink.go` for the bash/posh path extractors). c0wrk
consumes the unpublished change through the repo-root `go.work`
([ADR-031](031-gowork-repo-root.md)) and contributes only user-visible drift
tests (`core/tools/registry_reported_command_guard_unix_test.go`) that drive the
**real** sp4rk bash judge and the registry symlink gate.

### D5 — Fail-closed union

A name is promoted **only** when *every* recorded binding of it is a pure,
assessable substitution and the name carries **no** literal value, no other
dynamic binding, and the command is not opaque. Any single literal binding, any
non-assessable inner, any differently-shaped binding (append/index/array/`read`),
or an opaque construct **poisons the whole name back to dynamic** — reverting to
the pre-existing escalation. The promotion is a union check over all bindings,
never a last-writer-wins.

### D6 — PowerShell parity

`posh_exec` recognizes the analogous **static binding** `$NAME = <literal RHS>`.
The tokenizer emits statement/pipeline terminator markers so the RHS can be
bounded; the RHS is static only when it runs to the next terminator and contains
**no** expandable token (`$`-expansion, backtick escape, or expandable
double-quoted content). A bare `$NAME` reference is then treated as literal iff
`NAME` is bound **exactly once** with a static RHS. Recognition is fail-closed for
the same cases as bash: a suffixed reference (`$X/secret`), a name bound more than
once, an unbound name, and `$(...)` / `(...)` all keep every reference suspicious.

## Accepted Residual Risk

**The substitution's stdout is not analyzed.** The gate assesses the structure of
the *inner command* — its static assessability and its literal paths — never the
runtime value the variable will hold. A substitution whose inner command reads
data (`X=$(cat cfg); cat "$X"`) can therefore produce a value that is never
inspected; if that value is itself an out-of-root path string, the later reference
is treated as assessable-but-unknown and does not escalate on the substitution
alone.

This is accepted, not overlooked:

- The exception fires only when the inner command is **fully static** (D2) and
  has **no unresolved path tokens**; anything the pipeline cannot assess falls
  back to the historical escalation (D5).
- The inner command's **literal paths are still surfaced** to the containment and
  symlink gates (D3), so the substitution cannot hide a literal path.
- The narrow, static pattern it unblocks (`PKGS=$(go list ./...)`) is pervasive in
  agent-authored commands, and escalating it on every run made the gate unusable
  there.

A host that needs the stronger guarantee — analyzing substitution output — must
keep every substitution suspicious; that policy is out of scope here (see below).

## Out of Scope

- **Bare `$(...)` in argument position.** Deliberately still suspicious (D1):
  there is no assignment to quantify over, and the value feeds the command
  directly. `cat $(echo x)` continues to escalate.
- **Unbound environment variables.** A reference to a name **not** bound by an
  assessable substitution — including well-known environment variables such as
  `$HOME`, `$PATH`, and `$env:HOME` — stays **suspicious by design**. The feature
  narrows only the *statically-bound assignment* case; it does not introduce
  knowledge of the process/user environment.
- **Output-content analysis** of any substitution or variable (see Accepted
  Residual Risk).

## Consequences

**Positive:**

- The common "capture output, then use the variable" pattern no longer forces a
  confirmation when the substitution is fully static — with no widening of the
  assessable surface beyond that exact shape.
- Fail-closed throughout: the promotion is a union over all bindings (D5), the
  inner command is assessed by the same pipeline recursively (D2), and bare
  substitutions and unbound variables are untouched (D1, Out of Scope).
- No new blind spot for symlink traversal: an approved substitution's inner
  literal paths are walked (D3).
- Dialect parity (D6): bash and PowerShell get the equivalent exception, so the
  two shells do not diverge on a pattern an agent may emit either way.

**Negative / trade-offs:**

- **New assessability surface.** A name bound by an approved substitution is now
  `assessable` (a `$NAME` reference to it does not escalate). The blast radius is
  bounded by D2/D5, but it is a genuine reduction in conservatism — see Accepted
  Residual Risk.
- **Two prompt dialects, two tokenizers.** The PowerShell exception is a heuristic
  (char-level, no AST), so it is narrower and more shape-sensitive than the bash
  AST-based check; both are pinned by tests, but they are not literally the same
  algorithm.
- **Mid-cycle dependency.** Until the sp4rk change is released and the pin
  advanced, c0wrk builds only with the repo-root `go.work` in place; `GOWORK=off`
  builds fail ([ADR-031](031-gowork-repo-root.md)).

## Alternatives Considered

- **Keep every substitution suspicious (no exception).** Rejected: it forces a
  confirmation on the pervasive static capture pattern, which is exactly the false
  positive that motivated the change. It also contradicts the observation that a
  fully static inner command cannot hide a path no pass can see.
- **Analyze the substitution's output.** Rejected as out of scope: it would
  require executing the command or modeling file contents, defeats the "no shell
  interpreter" design, and is the accepted residual risk instead. Kept available
  to hosts as a stricter policy.
- **Approach substitutions in argument position (`cat $(echo x)`).** Rejected:
  there is no assignment binding to promote, and the value is consumed directly by
  the command, so the argument form stays suspicious (D1).
- **Last-writer-wins or per-substitution promotion.** Rejected: a name reassigned
  by a literal, a rebinding builtin (`read`), or a second non-assessable
  substitution must poison the whole name. The union check (D5) is what keeps the
  exception fail-closed.
- **Bash-only, leaving `posh_exec` unchanged.** Rejected: it would create a
  dialect gap where an identical pattern escalates under one shell and not the
  other (D6).

## Related Specs

- [architecture/security-model.md](../architecture/security-model.md) — the shell
  path-containment and symlink-detection gates this decision narrows (assignment-
  form exception and validated-inner rule).
- [decisions/024-group-policies.md](024-group-policies.md) — capability-group
  policies; the shell tools' `execute` group and the judge/confirmation funnel.
- [decisions/026-smart-approve-unified-funnel.md](026-smart-approve-unified-funnel.md)
  — the escalation funnel a flagged shell call routes through.
- [decisions/031-gowork-repo-root.md](031-gowork-repo-root.md) — how c0wrk
  consumes the unpublished sp4rk change mid-cycle (D4).
- [decisions/007-shell-parser-dependency.md](007-shell-parser-dependency.md) — the
  `mvdan.cc/sh` parser underlying the bash assessment.
