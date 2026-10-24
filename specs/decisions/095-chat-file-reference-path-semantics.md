# ADR-095: Chat File Reference Path Semantics

## Status

Accepted. Documents the cross-platform path semantics that the chat
file-reference pipeline (the `fileref://` URI emission, the markdown link
classification, the tool-card link resolution, and the read-path RPC
resolvers) now implements; the domain and contract specs listed under
References were updated in the same change. Amended in the same cycle: the
chat workspace root that relative user-facing paths resolve against follows
the ACTIVE SESSION (execution workspace; checkout fallback) — Decision 6
and the Consequences; [ADR-098](098-research-root-follows-active-session.md)
records the same session-following rule for the research/papers roots and
the workspace watcher.

## Context

File paths reach the chat in four places, and all four were written
POSIX-first:

1. `@file` references in user messages — `PreprocessMessageText`
   (`core/message_preprocess.go`) converts them into `fileref://` URIs that
   are persisted in the session transcript and shipped to the model.
2. Markdown links and local images in assistant messages and file-viewer
   markdown — classified by `frontend/src/lib/localFileLink.ts`
   (`isLocalFileHref` / `isExternalUrl`) and resolved by `normalizePath`.
3. Tool-call card titles — the path argument of the eight file tools
   (`read_file`, `write_file`, `edit_file`, `read_skill_resource`,
   `create_directory`, `delete_file`, `delete_directory`, `list_directory`),
   rendered as a clickable `FileLink`.
4. The read-path RPCs behind every click — `ReadFile`, `GetFileIcon`,
   `ReadImageAsDataURL`, `GetFileDiff`, all resolving through
   `resolveReadablePath` in `backend/frontend_api_workspace.go`.

On a POSIX build the historical classification logic mis-handled every
Windows-dialect path (`Go`'s `filepath.IsAbs` and the TS
`path.startsWith('/')` are both GOOS-conditional in effect): `C:\Doc\f.md`
was "relative", so it was joined onto the workspace root
(`/ws/C:\Doc\f.md`), markdown classifiers parsed `C:` as a URL scheme and a
drive-path link could reach `BrowserOpenURL`, and the read-path resolver
rooted drive paths under the process CWD (`/C:\…`). c0wrk is a
cross-platform desktop app and the model cites the host's native dialect,
so these paths are first-class input everywhere, on every build.

Two further questions needed deciding. First, `C:/x` parses as a URI with
scheme `C` under RFC 3986 tooling — the classifier needs a rule that keeps
it a file path without maintaining a scheme whitelist. Second, a
`read_skill_resource` card's path argument is relative to the *skill's*
directory, not the workspace, and skill discovery precedence
(project-local `.agents/skills` first, then the shared catalog
directories, first name wins) lives in the backend — the frontend cannot
correctly resolve such a click on its own. Finally, `fileref://` URIs and
raw paths are persisted in `session_messages`; any behavior change must
keep old rows readable without a schema migration.

## Decision

1. **Absolute-path detection is semantic and GOOS-independent.** One
   lexical classifier on each side — `core.IsAbsolutePath`
   (`core/pathabs.go`) and its declared mirror `isAbsolutePath`
   (`frontend/src/lib/localFileLink.ts`): POSIX leading `/`; Windows
   drive-rooted `^[A-Za-z]:[\\/]` (the required separator keeps the
   drive-relative `C:x` relative); Windows UNC `^[\\/]{2}` (mixed
   separators included). There is no `runtime.GOOS` switch anywhere in the
   pipeline: the same text classifies the same way on every platform,
   because a `fileref://` URI is a logical transcript identifier, not an
   OS path. `~`-prefixed paths stay their own category and resolve exactly
   as before.
2. **Canonical `fileref://` URIs carry forward slashes.** The emitter
   (`resolveFileRefPath` in `core/message_preprocess.go`) canonicalizes
   drive/UNC inputs through `core.NormalizeAbsPath`: backslashes become
   forward slashes, dot segments are removed, and `..` is clamped at the
   path's anchored root — the filesystem root, the drive root, or the UNC
   server/share pair — so it can never rise above the anchor. Canonical
   output forms: POSIX `/a/b`, drive `C:/a/b` (no leading slash), UNC
   `//server/share/a/b` (the double-slash prefix is pinned; the full URI
   literal therefore reads `fileref:////server/share/f.md`). POSIX-absolute
   and `~` inputs pass verbatim, and relative inputs join the session
   workspace exactly as before — canonicalization is emit-time; the
   frontend mirrors it (`normalizePath`, `normalizeAbsolutePath` in
   `markdownImageResolve.ts`). The clamp is deliberate: a plain
   `path.Clean` would pop `..` across the anchor (`C:\..\x` → `../x`,
   erasing the drive) and collapse the UNC `//` marker.
3. **A single-letter scheme guard classifies drive hrefs — not a scheme
   whitelist.** `WINDOWS_DRIVE_RE = /^[A-Za-z]:/` in `localFileLink.ts`:
   RFC 3986's grammar would accept a one-letter scheme (`C:` parses as URI
   scheme `c`), but no real-world scheme is a single letter, so any such
   href is in practice a local Windows path —
   `isExternalUrl` is false for the whole `^[A-Za-z]:` set (nothing
   reaches `openExternalURL` / `BrowserOpenURL`) and `isLocalFileHref`
   answers true before the protocol test runs. `EXTERNAL_SRC_RE` in
   `markdownImageResolve.ts` carries the same ≥2-character scheme rule so
   image `src` values with a drive prefix resolve as files, not external
   URLs. Multi-letter schemes (`https:`, `mailto:`, `data:`, and even a
   two-letter pseudo-scheme) keep their existing behavior.
4. **No database migration.** The rules are additive: POSIX-absolute, `~`,
   and relative inputs follow byte-for-byte historical paths, so persisted
   rows render and resolve exactly as they always did. Only Windows-dialect
   inputs change behavior — and they were broken before. A legacy rooted
   spelling `/C:/x/f.md` (which an old resolver could persist) is un-rooted
   by the read-path resolver whenever the remainder is itself a
   Windows-dialect absolute path; a remainder starting with `/` is a
   canonical forward-slash UNC and stays intact.
5. **Skill-resource resolution is backend-owned, via one RPC.** The
   frontend never replicates skill discovery. `ResolveSkillResourcePath`
   (`backend/frontend_api_skills.go`) delegates to
   `OrchestratorBuilder.ResolveSkillResourcePath` (`core/builder.go`),
   which performs the same discovery set and the same
   `skills.SafeResolvePath` traversal guard as the `read_skill_resource`
   tool itself — one source of truth for "which file does this relative
   resource path mean". The RPC answers an absolute path to open; any
   failure (backend not ready, unknown skill, traversal outside the skill
   directory) reaches the frontend as a fail-soft `null`
   (`frontend/src/api/skills.ts`) and the card degrades to plain text
   instead of navigating.
6. **Tool-card links resolve against the argument's true base.**
   `FileLink` accepts an optional `resolve` callback; `ToolCard` supplies
   one only for the eight file tools: `read_skill_resource` paths resolve
   through the skill-resource RPC keyed by the call's skill argument (no
   skill name → no resolution), while every other file tool resolves
   `normalizePath(root, arg)` against the chat workspace root. That root is
   resolved by `resolveChatWorkspaceRoot()`
   (`frontend/src/lib/chatWorkspaceRoot.ts`) and follows the ACTIVE
   SESSION: a `managed_worktree` session of the active project resolves
   against its own tree (the binding's `workspace_path`, or the
   backend-authoritative `GetSessionWorkspace` RPC while the binding row
   has not landed), and local/unbound sessions fall back to the project
   checkout — the session's `project_id` must equal the active project, so
   a foreign session's tree never leaks into another project's window (the
   same session-following rule the research/papers roots and the CODE
   watcher follow, [ADR-098](098-research-root-follows-active-session.md)).
   With no workspace root available (No-Project / transient window) the
   raw path passes through unchanged. Markdown links and `@file` user-message links
   resolve through `resolveFileRefPath` against the workspace root —
   absolute inputs (POSIX, drive, UNC) are recognized by decision 1's
   classifier and normalized *without* joining, with or without a root (a
   No-Project window still opens a markdown absolute link); relative
   inputs need the root — a markdown link without one stays inert rather
   than open a wrong file. A drive-prefixed markdown link destination
   (`C:\…`) is additionally cleared upstream by react-markdown's default
   URL transform (its safe-scheme list has no `c`), so the MarkdownLink
   classifier only ever sees POSIX-absolute and relative hrefs — the
   drive/UNC classification guards the tool-card, `@file`, and read-path
   surfaces. Markdown images try the document's directory
   first, then the workspace root.
7. **Drive-relative and rooted Windows paths are out of scope.** `C:x\f.md`
   (drive letter + relative remainder) stays "relative" under both
   classifiers; with a workspace root it joins the root (the historical
   behavior for relative input), which resolves incorrectly on Windows. The
   single-backslash rooted spelling `\x\f.md` (relative to the current
   drive's root) falls into the same class: one leading separator is below
   the UNC threshold, so both classifiers read it as relative and it joins
   the workspace root the same way (pinned by the classifier tables on both
   sides — `C:x`, `\x` stay relative). Both dialects are near-extinct and
   their correct resolution needs a per-drive CWD concept nothing else in
   the app has; the limitation is documented rather than solved.
8. **Read-path RPC resolution is lexical and workspace-independent.**
   `resolveReadablePath` (`ReadFile`, `GetFileIcon`, `ReadImageAsDataURL`,
   `GetFileDiff`): strip the trailing line anchor; un-root the legacy
   `/C:…` spelling per decision 4; return absolute inputs verbatim — no
   CWD-joining, no re-cleaning (dot-segment canonicalization is the
   emitter's job, decisions 1–2); otherwise the historical `filepath.Abs`
   against the process CWD. `ReadFileAsDataURL` deliberately stays on the
   workspace-contained `resolveWorkspacePath` (see Alternatives).

## Consequences

- Windows drive/UNC paths work end-to-end in chat on every platform: they
  are never joined onto the workspace, never dispatched to the system
  browser, and every read-path RPC resolves them.
- Relative user-facing paths resolve against the ACTIVE SESSION's
  execution workspace, not a fixed project checkout: a click in a session
  bound to a managed worktree opens the file in that session's tree, and
  the checkout is only the fallback for local/unbound sessions
  (`resolveChatWorkspaceRoot()` for links and tool cards; the viewer's
  `useWorkspacePath` mirrors the rule for markdown preview).
- Classification is deterministic and testable: the Go and TS classifiers
  are declared mirrors and the test suites are GOOS-independent tables over
  the same inputs, so a transcript renders identically after moving between
  platforms.
- A `read_skill_resource` card click opens the true file even when the
  skill lives in a shared/global catalog directory, and a failed resolution
  visibly degrades to text instead of silently opening the wrong file.
- No persisted-data migration and no transcript rewrite: old sessions
  render unchanged, and the transcript keeps preserving the user's
  original text.
- Absolute inputs pass through the read-path RPCs un-cleaned: consumers
  that display a raw absolute path are responsible for dot-segment
  normalization (the frontend `normalizePath` /
  `core.NormalizeAbsPath` do this at emission/display time).
- The UNC URI literal carries four slashes (`fileref:////server/...`) —
  visually odd, contractually pinned: the path component keeps the UNC
  identity.
- Drive-relative and rooted Windows paths (`C:x`, `\x`) remain misresolved
  when a workspace root exists (decision 7) — accepted, documented
  limitation.
- The `ResolveSkillResourcePath` RPC requires Wails binding regeneration
  (`wails build` / `wails dev`) before the runtime proxy exposes it; until
  then the fail-soft wrapper degrades clicks to plain text.

## Alternatives Considered

- **GOOS-conditional detection** (`runtime.GOOS == "windows"` per build) —
  rejected: the chat transcript is cross-platform data; a session viewed on
  a different OS than it was recorded on must classify identically, and
  per-build branching would make the two classifier mirrors diverge by
  construction.
- **A whitelist of allowed URL schemes for markdown hrefs** — rejected: the
  classifier must also answer "is this a local file" (not merely "not
  external"), which a whitelist does not cover, and the list grows with
  every legitimate scheme (including custom app schemes); the one-letter
  guard is total, future-proof, and matches how the drive prefix is
  actually parsed.
- **Migrating persisted rows to the canonical form** — rejected: the
  transcript deliberately persists the user's original text alongside the
  preprocessed form; rewriting history corrupts the record, and the
  additive rules keep every legacy spelling readable, so a migration would
  buy nothing.
- **Frontend-side skill resolution (listing the catalog and guessing the
  directory)** — rejected: discovery precedence, cache generation, and the
  traversal guard are backend state; a frontend copy would be a second
  source of truth that drifts. The RPC keeps `read_skill_resource`'s own
  resolution as the single authority.
- **Plain `path.Clean` for URI canonicalization** — rejected: it pops `..`
  across the drive/UNC anchor and collapses the UNC `//` marker; the
  anchored clamp mirrors the TS consumer exactly.
- **Moving `ReadFileAsDataURL` onto `resolveReadablePath`** — rejected:
  its workspace containment is a deliberate security boundary (markdown
  auto-render runs without an explicit user action and must not let a
  document read arbitrary files into the DOM); its drive/UNC handling
  follows the contained resolver and is a separate concern.

## References

- [specs/domains/session-lifecycle.md](../domains/session-lifecycle.md)
  § File Reference (`fileref://`) Path Semantics — the URI emission rules.
- [specs/domains/frontend/rendering.md](../domains/frontend/rendering.md)
  § Markdown Element Handling — link classification, `normalizePath`, and
  the tool-card resolution bases.
- [specs/decisions/098-research-root-follows-active-session.md](098-research-root-follows-active-session.md)
  — the session-following workspace-root rule Decision 6 builds on (the
  research/papers roots and the workspace watcher).
- [specs/contracts/desktop-frontend.md](../contracts/desktop-frontend.md)
  § Workspace and § Skills — the read-path RPC dialect contract and the
  `ResolveSkillResourcePath` RPC.
