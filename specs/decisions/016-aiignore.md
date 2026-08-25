# ADR-016: .gitignore + .aiignore as the ignore source of truth

## Status

Accepted

> **Drift note (2026-08-25, vibespec-check):**
> - The documented ripgrep limitation ("nested .aiignore not honoured by rg; root-level only via --ignore-file") has since been lifted — ripgrep results are now post-filtered per entry through the ignore checker (sp4rk/tools/builtins/ripgrep.go), honouring root AND nested .aiignore uniformly; no --ignore-file flag is passed.
> - The Multi construction described as `ignore.NewMulti(workspace + workDirectories...)` is now per-root Resolver construction (fault-isolated, symlink-resolved, deduplicated) combined via NewMultiFromResolvers (backend/session/manager_execution.go).

## Context

c0wrk historically controlled which files and directories were excluded from the
file tree and the vector index through a `workspace` block in `config.yaml`:

```yaml
workspace:
  ignore_dirs: [vendor, node_modules, dist, build, ...]
  ignore_extensions: [.exe, .png, .lock, ...]
  ignore_file_names: []
```

The corresponding Go types (`WorkspaceConfig`) and a large hardcoded default
list (`cfg.Workspace.IgnoreDirs` / `IgnoreExtensions` / `IgnoreFileNames` in
`backend/config/defaults.go`) supplied ~45 extension entries and ~9 directory
names whenever the user left them unset.

This design had three problems:

1. **Divergence from reality.** The hardcoded list drifted from what users
   actually want ignored. Two of the most useful ignores — `node_modules` and
   `vendor` — were present, but lock files, vendored snapshots, and AI-generated
   artefacts were only partially covered. Every project had to fight the same
   defaults.
2. **Duplication of git's own knowledge.** Git already maintains a precise,
   per-project ignore set in `.gitignore`. Ignoring it meant c0wrk re-derived a
   worse approximation and then layered its own defaults on top.
3. **No "AI-only" channel.** A file can be tracked by git (so it belongs in the
   repo) yet be noise that the agent and indexer should never see — generated
   reports, large vendored snapshots, scratch dumps. There was no place to put
   "hide from the AI but keep in git" rules.

## Decision

Replace the config-driven ignore list with **repository ignore files** as the
single source of truth. c0wrk now consults, in every applicable root:

- **`.gitignore`** — standard git ignore rules, honoured for every project.
- **`.aiignore`** — an optional, AI-specific ignore file layered on top of
  `.gitignore`. Use it for files that git tracks but the agent/indexer should
  not see.

Concretely:

- **Removed:** the `WorkspaceConfig` struct (`backend/config/config.go`), the
  `workspace` field from `Config`, and the hardcoded default maps in
  `backend/config/defaults.go` (`IgnoreDirs`, `IgnoreExtensions`,
  `IgnoreFileNames`). The matching `workspace:` block in `config.example.yaml`
  is replaced with a usage note (see *Consequences*).
- **Added (shared engine primitive):** `github.com/v0lka/sp4rk/ignore` — a
  multi-root ignore resolver. It walks each root once at construction,
  collecting every `.gitignore` and `.aiignore` (root plus nested directories),
  compiles the patterns into doublestar globs anchored relative to the root,
  and answers whether an arbitrary path is ignored. A `Multi` holds one
  `Resolver` per root and delegates each query to whichever root contains the
  path (`RootFor`, symlink-aware via `pathutil.IsWithinPath`). Both `Resolver`
  and `Multi` satisfy the `ignore.IgnoreChecker` interface.
- **Wired through the tool-execution context:** `backend/session/manager_execution.go`
  builds an `ignore.NewMulti(workspace + workDirectories...)` (roots symlink-
  resolved and deduplicated) and attaches it to the task context via
  `tools.WithIgnoreChecker`. `glob` and `ripgrep` read it back through
  `tools.IgnoreCheckerFrom` and filter per the containing root's rules. The
  vector indexer (`core/vectorindex`) and the file tree
  (`backend/frontend_api_workspace.go`) build their own single/multi-root
  resolvers over the workspace.

### Two universal guards kept (caller-side)

The `ignore` package is a **pure algorithmic building block**: it performs no
hidden-dotfile or binary-file filtering. Those two universal guards remain
caller-side concerns that layer on top of the resolver:

- **Hidden-dot guard** — entries whose name starts with `.` are always excluded
  from the file tree (`core/workspace/filetree.go`) and the vector index
  (`core/vectorindex/indexer.go`), regardless of ignore files.
- **Binary-content guard** — binary files are excluded from search/indexing
  (`rg` skips them natively; the indexer detects null bytes).

### Tools respect ignores per applicable root

Because the resolver is a `Multi` over the workspace **and** every auxiliary
work directory, `glob`/`ripgrep` honour each root's own `.gitignore` +
`.aiignore`. A glob against a work directory excludes that work dir's ignore
files; a glob against the workspace excludes the workspace's.

### ripgrep nested-`.aiignore` limitation

`rg` honours `.gitignore` natively (including nested files) but does **not**
understand `.aiignore`. The `ripgrep` tool therefore passes the **root-level**
`.aiignore` (located at the resolved search root) to `rg` via `--ignore-file`
— but only when an ignore checker is plumbed through the context, preserving
"no checker ⇒ today's behaviour". Nested `.aiignore` files are **not** honoured
by `rg`; this is a documented, accepted limitation. (`glob`, which uses the
resolver directly, honours nested `.aiignore` files fully.)

### `read_file` intentionally unrestricted

`read_file` deliberately does **not** consult the ignore checker. Targeted,
explicit reads of any file — including ignored ones — remain permitted. Ignore
filtering governs *discovery* (`glob`, `ripgrep`, tree, index), not *access*.

## Consequences

**Positive:**

- One source of truth: edit `.gitignore`/`.aiignore` at the project root and
  the tree, index, and discovery tools all update. No more per-project fights
  with hardcoded extension lists.
- A clean "AI-only" channel (`.aiignore`) for files that are tracked but should
  be hidden from the agent.
- The ignore engine lives in sp4rk (`ignore`), so it is reusable, tested in
  isolation, and shared across all tool surfaces via the `IgnoreChecker`
  interface — no re-export layers.
- Work directories get correct, independent ignore handling.

**Negative / trade-offs:**

- `.aiignore` is a new convention users must learn. Mitigated by the usage note
  in `config.example.yaml` and `specs/domains/workspace.md`.
- `ripgrep` honours only root-level `.aiignore` (see limitation above).
- First-use cost: building a resolver walks the root once. Negligible for
  typical repos; non-fatal (ignore filtering falls back to "off") on failure.
- Users who relied on the old `workspace.*` config keys must migrate to
  `.aiignore`. The removed keys are simply ignored by YAML decoding.

## Alternatives Considered

- **Keep the config keys, add `.aiignore` alongside.** Rejected: two ignore
  sources that must be reconciled, and the hardcoded defaults were the original
  problem (drift, duplication). One source of truth is simpler and correct.
- **Make `.aiignore` a global, config-supplied pattern list.** Rejected: that
  re-introduces a global config ignore list — the very thing being removed —
  and is not per-project.
- **Have `ignore` also do hidden-dot + binary filtering.** Rejected: those are
  universal, unconditional guards that apply even with no ignore files present.
  Mixing them into the resolver would conflate "rule-based ignore" with
  "always-hidden", and would force every caller to build a resolver even when
  it only wants the universal guards. Keeping them caller-side preserves a
  clean, optional resolver.
- **Honour nested `.aiignore` in `rg` by post-filtering.** Rejected as
  out-of-scope complexity; `glob`'s resolver-based path already covers nested
  `.aiignore` for discovery, and `rg`'s root-level `--ignore-file` covers the
  common case.
