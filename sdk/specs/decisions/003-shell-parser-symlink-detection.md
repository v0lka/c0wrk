# ADR-003: mvdan.cc/sh/v3/syntax for Symlink-Traversal Detection

## Status

Accepted

## Context

A shell-execution tool is among the most powerful capabilities an agent can be given. To prevent an agent from breaking out of its workspace by following symlinks (e.g. reading a target outside the workspace via a symlink created inside it), the tool needs to inspect bash commands and resolve the file-path arguments they reference.

The original detection approach used simple string matching, which was fragile: it could not distinguish `echo "../etc/passwd"` (safe — a string literal) from `cat ../etc/passwd` (dangerous — a relative path argument). A proper AST-based shell parser was needed to resolve argument paths, understand command structure, and correctly identify symlink targets.

Go's standard library does not include a shell parser. Writing one is non-trivial — shell syntax includes quoting, variable expansion, command substitution, redirections, and heredocs. A buggy custom parser would create security false positives (blocking legitimate tool use) or false negatives (allowing escape).

## Decision

Add `mvdan.cc/sh/v3/syntax` as a dependency of `github.com/v0lka/sp4rk/tools`. This library:

- Is the de-facto standard shell parser for Go (used by staticcheck, shfmt, and gopls).
- Parses POSIX Shell and Bash syntax per the POSIX/IEEE Std 1003.1 specification.
- Provides an AST that enables reliable resolution of argument paths, distinguishing string literals from actual file arguments.
- Is used exclusively in `github.com/v0lka/sp4rk/tools/symlink.go` for bash command traversal detection.

The dependency is confined to `github.com/v0lka/sp4rk/tools/symlink.go` — no other package in the module imports it.

## Consequences

**Positive:**

- Symlink-traversal detection is correct: the parser distinguishes between actual path arguments and string literals, heredocs, and variable expansions.
- No false positives from path-like strings in safe contexts (e.g. `echo "/some/path"`).
- A well-tested, widely-reviewed library reduces the risk of parser bugs versus a bespoke implementation.
- The dependency is small and focused: a single Go package with no transitive dependencies outside the `mvdan.cc/sh/v3` module.

**Negative:**

- Adds a compile-time dependency from `github.com/v0lka/sp4rk/tools` on a third-party module. Breaking changes in `mvdan.cc/sh/v3` would require code updates in `symlink.go`.
- `go.mod` gains a `require` directive. The module is otherwise dependency-light, so the supply-chain impact is bounded.
- The parser adds CPU cost to every shell-tool invocation with symlink detection enabled. This cost is negligible relative to process startup time (~1ms parse vs ~10ms `exec`).

## Alternatives Considered

**Write a custom shell lexer that only tokenizes paths.** Rejected: even "just paths" requires understanding quoting rules, escape sequences, and command boundaries. A partial parser would inevitably miss edge cases, creating security gaps.

**Use regex-based path detection.** Rejected: the original implementation used this approach and was replaced because it produced false positives on string literals and false negatives on multi-quoted paths (e.g. `cat "/"etc"/"passwd`).

**Reject any command containing path-like strings without parsing.** Rejected: would block legitimate commands and harm usability. The point of symlink detection is to allow normal tool use while catching actual escape attempts.

**Pre-execution filesystem check (resolve all paths before exec).** Rejected: resolving all paths before execution cannot handle commands that create symlinks as part of their operation (e.g. `ln -s /tmp target && cat target` — the symlink only exists after the first command). AST-based detection handles this by identifying the command arguments that are path arguments, then resolving those specific paths.

## Related

- [001-separate-module.md](001-separate-module.md) — this dependency is part of `github.com/v0lka/sp4rk`'s explicit, auditable `go.mod`.
- [../contracts/tools.md](../contracts/tools.md) — the tool surface this security control protects.
