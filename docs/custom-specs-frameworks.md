# Custom Specs Frameworks

A user guide to extending c0wrk's **Specs** sidebar with your own
specification-driven development (SDD) framework.

You do not need to modify or rebuild c0wrk. Install your framework's Agent
Skills, then describe its files, checks, actions, and sidebar sections in YAML.
The same DSL describes the three bundled integrations: VibeSpec
(`vibespec`), OpenSpec (`openspec`), and GitHub Spec Kit (`spec-kit`).

This guide describes the current `sdd-framework/v1` implementation, including
its limitations. A schema-valid declaration is not necessarily an effective
check or a working action: the distinctions below matter.

## Contents

1. [Mental model and prerequisites](#1-mental-model-and-prerequisites)
2. [Quick start](#2-quick-start)
3. [Definition locations, precedence, and updates](#3-definition-locations-precedence-and-updates)
4. [Recognition and framework selection](#4-recognition-and-framework-selection)
5. [Complete YAML reference](#5-complete-yaml-reference)
6. [Markdown extraction reference](#6-markdown-extraction-reference)
7. [Integrity checks: all eight kinds](#7-integrity-checks-all-eight-kinds)
8. [Actions and skill dispatch](#8-actions-and-skill-dispatch)
9. [Views and tree presentation](#9-views-and-tree-presentation)
10. [Worked example: Acme SDD](#10-worked-example-acme-sdd)
11. [Testing and troubleshooting](#11-testing-and-troubleshooting)
12. [Security, boundaries, and current limitations](#12-security-boundaries-and-current-limitations)
13. [Implementation and further reading](#13-implementation-and-further-reading)

## 1. Mental model and prerequisites

There are three separate pieces:

| Piece | What it does | What it does **not** do |
| --- | --- | --- |
| Installed Agent Skills | Supply instructions for the agent's lifecycle tasks: initialize, review, implement, repair, etc. | Do not define the sidebar's tree or its deterministic checks. |
| Framework YAML | Declares recognition rules, roots, document concepts, checks, actions, and views. | Does not install skills, execute commands, or modify specifications. |
| Specification tree | Holds the project's actual documents. | Does not, by itself, make Specs visible. |

The panel is a **read-only view** of files. An action click sends a prompt to
chat with a declared skill activated. The agent, not the parser or panel,
performs any requested changes through the normal tool/security pipeline.

Before starting:

- Open a real project. Specs is not a No Project / CHAT-mode surface.
- Enable Specs in **Settings → General** if it has been disabled
  (`specs.enabled`; enabled by default). The experimental-features switch is
  not a prerequisite.
- Install valid Agent Skills for the workflow you want to expose. A slash
  command or a loose Markdown file is not an Agent Skill.
- Decide which directory will contain your specifications. Keep the root narrow:
  the parser walks declared roots, not just the files a glob eventually selects.

c0wrk does **not** seed an SDD skillset, create an operator `specs.yaml`, or
initialize your spec tree merely because you open the panel.

For the full Agent Skill format and activation model, see
[Custom Skills and Subagents](custom-skills-and-subagents.md).

## 2. Quick start

This small integration recognizes an `acme-init` skill and shows Markdown files
under `acme-specs/`.

### Step 1: install a real skill

Create `.agents/skills/acme-init/SKILL.md` in the project workspace:

````markdown
---
name: acme-init
description: Initialize the Acme specification tree for the current workspace.
---

When asked to initialize Acme SDD, clarify the project's scope first.
Then create acme-specs/INDEX.md with a title and navigation links.
Do not overwrite existing specifications without asking the user.
````

The skill directory name and frontmatter `name` must agree, and the frontmatter
must start at the beginning of the file.
Alternatively, install the same `acme-init/` directory under
`~/.c0wrk/.agents/skills/` (c0wrk-wide) or `~/.agents/skills/` (user-wide), provided
those global directories remain in your `skills.dirs` configuration.

### Step 2: describe the integration

Create **`<project-checkout>/.c0wrk/specs.yaml`**:

```yaml
schema: sdd-framework/v1
id: acme
label: Acme SDD
description: A small Markdown-based specification workflow.
priority: 40

detect:
  markers: [acme-specs/INDEX.md]
  skills:
    dirs: [.agents/skills]
    all_of: [acme-init]

roots:
  - id: main
    path: acme-specs
    label: Acme specifications

concepts:
  - kind: page
    match:
      root: main
      glob: '**/*.md'
    extract: [title, headings, cross_refs]
    required: [title, non_empty]

checks:
  - kind: crossrefs
    finding: BROKEN_LINK
    message: A relative specification link or heading anchor does not resolve.

actions:
  - kind: init
    label: Initialize
    skill: acme-init
    prompt: Initialize Acme SDD for this workspace without overwriting existing specs.

views:
  sections:
    - id: documents
      label: Specifications
      kind: tree
      concepts: [page]
    - id: findings
      label: Findings
      kind: findings
```

**`roots` is a sequence of objects**, not a mapping such as
`roots: {main: acme-specs}`. `match.root` references a root's **ID**, not its path.

### Step 3: inspect and initialize

Reopen the project if Specs is not yet visible. If it is already visible,
activate the Specs tab or use its header refresh control.

- If the installed skill matches, **Specs appears even before `acme-specs/`
  exists**.
- With no root directory, the panel displays an empty state and an **Initialize**
  action. Clicking it starts an ordinary skill-backed task; it is not an
  immediate filesystem operation.
- After the root and documents exist, the tree displays matched files. Clicking
  a document opens it in the file viewer.
- A broken relative Markdown link produces a finding on the next parse.
- If another supported skillset is also installed, choose **Acme SDD** in the
  header selector. That selection is remembered for this project.

The quick-start `required: [title, non_empty]` records missing content in the
parsed document model. It does **not** create a finding. Add explicit checks
when you want violations to appear in the findings list (see §7 and §10).

## 3. Definition locations, precedence, and updates

### 3.1 Where to put definitions

| Layer, lowest to highest precedence | Location | Typical use |
| --- | --- | --- |
| Bundled | Embedded `core/sdd/frameworks/*.yaml` | The three integrations shipped with c0wrk. No user file is needed. |
| Global operator definitions | `~/.c0wrk/specs.yaml` | Add a framework to every project, or override a bundled definition for yourself. |
| Project operator definitions | `<project-checkout>/.c0wrk/specs.yaml` | Share an integration or project-specific layout with a repository. |

The global path is actually `<c0wrk-agent-directory>/specs.yaml`; `~/.c0wrk`
is the default agent directory. These are **framework definition files**, not
`config.yaml`, a skill manifest, or documents inside the specification tree.
They are optional and user-owned. c0wrk reads them but never materializes them.

**Worktree distinction:** the project definition file always comes from the
registered project **checkout**. The specification tree and project-local
skill evidence follow the active session's **workspace**, which may be a
separate worktree. Placing an override only in a session worktree does not
replace the checkout's definition file.

### 3.2 Whole-document replacement, not inheritance

Definitions merge by `id`:

1. Bundled definitions load first.
2. The global file adds new IDs or replaces matching IDs.
3. The project file adds new IDs or replaces matching IDs again.

A later definition replaces the **entire** earlier document. Lists do not
append, mappings do not deep-merge, and omitted actions/checks/views are not
inherited. To relocate OpenSpec, for example, copy its complete bundled YAML,
keep `id: openspec`, edit the root path and related markers, and place that
complete document in an operator file.

Use a new ID if you want a new, separately selectable framework. Use the same
ID if you mean to replace an existing integration. Keep IDs stable: persisted
project selection refers to the ID, not the label.

There is no `extends`, `include`, deletion directive, or `enabled` field in this
DSL. An empty or invalid override does not remove the earlier definition.
Removing your project override reveals the global or bundled one again;
removing a custom-only definition removes that framework from the catalog.

### 3.3 Multiple frameworks in one file

Use a YAML **document stream**, not a `frameworks:` wrapper or a top-level list.
Every `---`-separated document is a complete framework:

```text
# ~/.c0wrk/specs.yaml
<complete framework document for acme>
---
<complete framework document for another framework>
```

Within one file, a later valid document with the same ID replaces the earlier
one and emits a duplicate-ID warning. Cross-layer replacement is intentional
and does not emit that warning.

### 3.4 Loading errors and refresh

- Missing file: no definitions, no warning.
- Schema/type/validation error: that document is skipped; other decodable
  documents can still load.
- YAML syntax error: decoding stops at that point. Definitions decoded before
  the fault are retained; later documents may not be read.
- A decoded document with no keys at all (empty, or a trailing `---`) is blank
  and skipped silently. A document that carries keys but omits `schema`/`id` is
  NOT blank: it is validated like any other definition and reports its faults,
  so a fragment copied without the header never disappears silently.
- Catalog ordering is `priority` ascending, then `id` lexicographically.

The catalog is reloaded on a Specs status fetch; a rebuild is unnecessary.
The panel fetches on tab activation, active-session change, header refresh,
and relevant workspace-change events. Do **not** depend on an immediate live
reload of an operator file outside the active spec root, particularly the
global file. Refresh explicitly. If there is no Specs tab to refresh, reopening
the project or restarting the app is the reliable discovery retry.

Warnings appear in the panel's diagnostic strip when the panel is visible;
hover it for the combined messages. If your only custom definition is invalid
and nothing else is recognized, the panel can be hidden, so there may be no
visible warning strip. Use the validation procedure in §11 in that case.

## 4. Recognition and framework selection

### 4.1 Skill discovery is separate from YAML lookup

By default the relevant skill locations, highest priority first, are:

1. `<workspace>/.agents/skills/` (always considered).
2. Framework-declared project directories, such as `.claude/skills/`.
3. `~/.c0wrk/.agents/skills/` (c0wrk-global).
4. `~/.agents/skills/` (portable user-global).

The shared directories come from `skills.dirs` in c0wrk configuration. Changing
that setting changes what is available; listing `.agents/skills` in the DSL
does not disable the shared tiers.

For **panel recognition**, each immediate child directory carrying a
`SKILL.md` file supplies its validated frontmatter `name` as evidence.
Recognition runs the same validating parser as the callable skill catalog, so
a malformed, description-less, or name-mismatched `SKILL.md` supplies no
evidence. What recognition does not check is whether the skill is enabled by
Capabilities settings: only a capability-disabled skill can be recognized
without being usable in a task. Always verify the actual skill through the
chat skill catalog as well.

`detect.skills.dirs` adds discovery directories; it is **not** an exclusive
per-framework filter. Recognition uses the assembled evidence set. Required
skills may come from different tiers or declared directories. Duplicate names
prefer the strongest tier; the same-tier first occurrence wins.

For custom workflows, use the standard `.agents/skills` locations, or declare
an extra directory in the DSL. A declared directory is not panel-only:
framework-declared skill directories join the session's executable skill
discovery, so a skill is callable wherever its framework is detected. See §12
before relying on a new location such as `.acme/skills`.

### 4.2 The recognition expression

```text
recognized =
  at least one skill name is declared in all_of or any_of
  AND every all_of name is installed
  AND (any_of is empty OR at least one any_of name is installed)
```

Thus:

- `all_of: [acme-init, acme-review]`: both must exist.
- `any_of: [acme-review, acme-update]`: either is enough.
- Both lists: **AND** the two conditions, not either/or.
- Neither list: the definition is rejected at load (`detect: at least one all_of
  or any_of skill is required`), because it could never be recognized even if
  markers exist — markers are an ordering signal, never a visibility one.

When either skill-name list is set, `detect.skills.dirs` must be nonempty for
schema validation. The names are exact, case-sensitive slugs, not regexes or
globs, and detection compares them exactly — there are no aliases, built-in or
declared, so an installation must carry the declared spelling. For an
alternative spelling, list both names in `any_of`.

Markers and roots are **tree evidence only**. They cannot make the Specs tab
appear without the matching installed skills. Marker paths are existence
probes (file or directory); roots must exist as directories to be present.
No contents of marker files are matched against expressions.

### 4.3 Visibility, default selection, and persistence

Specs is visible for a real project when:

```text
specs.enabled AND at least one framework is recognized
```

If there is no still-recognized saved selection, the default is ordered by:

1. Any declared root or marker exists.
2. Strongest matched skill tier: project → c0wrk-global → user → unattributed.
3. Catalog order: lower `priority`, then `id`.

`priority` is nonnegative, defaults to `0`, and only breaks later ties. It does
not override tree evidence, stronger skill scope, or an explicit saved choice.

When more than one framework is recognized, the header selector lets you
choose the active framework. The choice is saved **per project in c0wrk's
project UI state**, survives restart, and is not written to `specs.yaml`.
Only the selected framework's tree, checks, and actions are used, not a union
of all recognized frameworks. If its skills disappear, detection falls back
to the next default; a saved choice is honored only while still recognized.
Switching keeps the previous tree visible until the new parse finishes.

### 4.4 Bundled recognition rules

These are the shipped DSL rules, not promises about every upstream installer
or older upstream version:

| Framework ID | Required installed skill names | Roots | Priority |
| --- | --- | --- | --- |
| `vibespec` | Any of `vibespec-init`, `vibespec-explore`, `vibespec-consult`, `vibespec-update`, `vibespec-create`, `vibespec-check` | `specs/` | 10 |
| `openspec` | `openspec-propose` **and** any of `openspec-apply-change`, `openspec-sync-specs`, `openspec-archive-change` | `openspec/` | 20 |
| `spec-kit` | Any of `speckit-constitution`, `speckit-specify`, `speckit-plan`, `speckit-tasks` | `specs/`, `.specify/` | 30 |

All three currently declare `.agents/skills` and `.claude/skills` as project
skill directories. Each also declares the tree markers its `init` creates —
VibeSpec probes `specs/META.md`, `specs/INDEX.md`, `specs/WORKFLOW.md`,
`specs/decisions/_template.md` and the `specs/architecture`, `specs/domains`,
`specs/contracts` and `specs/decisions` directories — but markers are the tree
signal only: they order a recognised framework as tree-present and never make
one visible on their own. Installing only command templates, or differently named
skills, does not satisfy these definitions. Override the definition if your
installation uses a different set.

## 5. Complete YAML reference

### 5.1 Common syntax and validation

- The only supported schema is exactly **`sdd-framework/v1`**.
- Unknown YAML fields are rejected, including nested fields.
- IDs and slugs match `^[a-z][a-z0-9-]*$`: start with a lowercase letter,
  followed by lowercase letters, digits, or hyphens. Use this form for framework
  IDs, root IDs, concept kinds, action kinds/skills, and view IDs.
- Finding keys match `^[A-Z][A-Z0-9_]*$` and must be unique within a framework.
- Root IDs, concept kinds, action kinds, and section IDs must each be unique
  within their own collection. Reusing a concept kind across different
  frameworks is fine.
- Paths and globs use forward slashes, including on Windows. Use clean,
  workspace-relative paths such as `.acme/specs`; avoid `/absolute`, `..`,
  `./prefix`, duplicate separators, trailing slashes, and backslashes.
- Use workspace-relative roots and markers. Do not rely on home expansion,
  environment interpolation, or platform-specific absolute path spellings in
  DSL paths. Shared skill locations belong in `skills.dirs` configuration.
- Quote globs starting with `*` and prefer single-quoted regexes so YAML does
  not consume backslashes: `'^[0-9]{3}-.*\.md$'`.
- Vocabulary values are case-sensitive. There is no custom executable
  expression language or arbitrary user-defined check implementation.

### 5.2 Top-level fields

| Field | Required / default | Meaning |
| --- | --- | --- |
| `schema` | Required | `sdd-framework/v1`. |
| `id` | Required | Stable framework slug used for merging and selection. |
| `label` | Required, nonblank | Human-readable framework name. |
| `description` | Optional | Explanation, including in the empty state and selector tooltip. |
| `docs_url` | Optional | Absolute HTTP(S) URL. Metadata, not an import or download. |
| `priority` | Optional, `0` | Nonnegative integer; lower comes first in catalog ordering. |
| `detect` | Required in a useful integration | At least one `all_of`/`any_of` skill name is required by validation — markers alone are a LOAD ERROR, not a framework that loads but never recognizes; markers and roots only order the frameworks whose skill evidence already matched. |
| `roots` | Required, nonempty sequence | Root objects. |
| `concepts` | Required, nonempty sequence | File/directory classification rules. |
| `checks` | Optional, empty | Deterministic integrity checks. |
| `actions` | Optional, empty | User-invoked skill-backed gestures. |
| `views` | Optional | Display configuration. Explicit sections are strongly recommended. |

### 5.3 `detect`

| Field | Type | Meaning |
| --- | --- | --- |
| `markers` | List of paths | Existence-only tree signal for default selection. Not visibility. |
| `skills.dirs` | List of directory paths | Additional project skill discovery roots. Required when either name list is nonempty. |
| `skills.all_of` | List of skill slugs | Every name must be installed. Duplicate literal entries are rejected. |
| `skills.any_of` | List of skill slugs | At least one name must be installed if this list is nonempty. Duplicate literal entries are rejected. |

### 5.4 `roots[]`

| Field | Required / default | Meaning |
| --- | --- | --- |
| `id` | Required | Slug referenced by concept matches. |
| `path` | Required | Root directory relative to the parsed workspace. |
| `label` | Optional | Root metadata. It does not replace all folder labels in the tree. |
| `required` | Optional, `false` | Presence gate: the parsed tree is present only when this root exists too. A missing required root is a warning and the not-present state. |

The parsed tree is present if at least one declared root exists as a directory
**and** every root marked `required` exists. A missing required root is a
warning (the empty state names it), not a rendered partial tree. The `root`
action prerequisite tests that whole presence predicate. A plain missing root
is neutral; no missing-root finding is automatically generated. Checks are not
run when the tree is not present.

### 5.5 `concepts[]`

| Field | Required / default | Meaning |
| --- | --- | --- |
| `kind` | Required | Concept slug; arbitrary names are allowed. This is not a closed list of built-in framework concepts. |
| `label` | Optional | The concept's own human name — DOCUMENT metadata (`Document.Label`), used for lists and search. It is never the tree row text: a row is named by the instance itself (its file name, or its document title when the concept extracts `title`). |
| `match.root` | Required | Existing root ID. |
| `match.glob` | Required | Pattern relative to that root, not the workspace. Every segment must be a valid glob (`[` must balance) — a malformed pattern is a load error, not a silently empty concept. |
| `match.dir` | Optional, `false` | Match directories instead of files. |
| `match.exclude` | Optional list | Globs a matched path must NOT match to belong to the concept — how an actual-documents concept sheds its `_`-prefixed templates (a more specific template concept claims them instead). |
| `files` | Optional list | Companion paths relative to each matched directory. Legal only with `match.dir: true`. |
| `extract` | Optional list | Which metadata is retained in parsed file documents; all 16 directives are listed in §6. A load error on a `match.dir` concept, which has no document to extract from. |
| `required` | Optional list | Assertions recorded as missing document metadata; see below. Evaluated per document, so also a load error on a `match.dir` concept. |

A directory concept does **not** parse or concatenate its companion files.
Declare separate file concepts for `proposal.md`, `tasks.md`, etc. Merely
listing a file under `files` does not make it a tree document.

#### Glob behavior and overlap

- `*.md`: files immediately inside the root.
- `domains/**/*.md`: files under `domains/`, at any depth, including directly
  inside `domains/`.
- `changes/*` with `dir: true`: immediate change directories.
- `changes/*/tasks.md`: task files one directory down.
- Segments use Go-style path matching (`*`, `?`, character classes); a segment
  exactly `**` spans zero or more path segments. No brace expansion or regex
  alternation is provided. A malformed glob (an unbalanced `[`) is a LOAD
  error naming the field — it can no longer silently match nothing.
- `match.exclude` removes a matched candidate from the concept: declare
  `exclude: ['decisions/**/_*.md']` on the actual-documents concept and a
  `decisions/**/_*.md` template concept claims the templates.
- A path receives **one** concept: deepest fixed, wildcard-free leading prefix
  wins, then longest pattern, then earlier declaration. This is not a union.
- File and directory rules compete separately. A directory concept can coexist
  with its nested file concepts.
- Unmatched files are absent from the model. Traversal still descends through
  unmatched directories; a broad root can therefore be expensive (the whole
  parse is additionally budgeted — see §12).
- Overlapping physical roots (equal or nested paths) are a LOAD error naming
  both ids. Forest nodes use workspace paths as IDs: one file, one owner.

Example: `decisions/_*.md` can classify templates separately from
`decisions/*.md`. For an archived subtree, a fixed prefix such as
`changes/archive/*` beats `changes/*` where both would otherwise compete.

#### `required` assertions

| Token | Satisfied by |
| --- | --- |
| `non_empty` | Non-whitespace file content. |
| `title` | At least one parsed ATX heading. |
| `status` | A nonempty status parsed from the Status section. |
| `purpose` | A nonempty section whose heading contains `purpose`. |
| `requirements` | At least one parsed requirement entry. |
| `scenarios` | At least one parsed scenario entry. |
| `tasks` | At least one Markdown checkbox task; it need not be completed. |
| `principles` | At least one parsed principle entry. |
| `user_stories` | At least one parsed user-story entry. |

These assertions inspect content independently of `extract`. They populate a
file document's `missing` metadata, **not the findings list**. The current tree
UI shows directory missing-companion warnings, not a complete editor for these
file assertions. Use explicit `content_regex` or other checks for visible
violations. On a directory, use `files` and a `required_files` check instead of
content assertions.

### 5.6 `checks[]`

Every check has these common fields:

| Field | Required / default | Meaning |
| --- | --- | --- |
| `kind` | Required | One of the eight kinds in §7. |
| `finding` | Required | Unique uppercase finding key. |
| `message` | Optional | User-facing explanation. The UI otherwise humanizes `finding`. |
| `severity` | Optional, `error` | `error` or `warning`. Both contribute findings and satisfy an action's `findings` prerequisite. |

Only the parameters belonging to the chosen kind are meaningful; unknown
keys and nonempty parameters from other kinds are rejected. A check may emit
multiple findings. Identical findings are deduplicated and sorted
by kind, file, and detail. Findings are recomputed, not persisted.

### 5.7 `actions[]` and `views`

These are fully described in §8–9. Their complete field sets are:

```text
actions[]: kind, label, skill, prompt, concept, requires
views.sections[]: id, label, kind, concepts
```

There are no DSL fields for custom icons, colors, shell commands, models,
subagent profiles, per-node variables beyond `{path}`/`{name}`, or arbitrary
widgets. Unknown action kinds are valid slugs and get generic presentation;
unknown check/extraction/view kinds are not valid.

## 6. Markdown extraction reference

Extraction directives are fixed parser operations, not configurable selectors
for arbitrary frontmatter keys or custom heading names.

### 6.1 All 16 directives

| Directive | What the parser retains |
| --- | --- |
| `title` | First ATX heading outside fenced code blocks (not necessarily H1). Also a display title with a numeric identity prefix removed when applicable. |
| `headings` | All ATX headings outside fenced code, with backticks removed. |
| `key_files` | First backticked path from each `-` / `*` bullet in the first section named exactly `Key Files`, case-insensitively. |
| `cross_refs` | Inline relative Markdown link targets outside fenced code and inline-code spans. |
| `frontmatter` | Leading `---` frontmatter parsed as real YAML and flattened into string key/value pairs (nested keys, arrays, and block scalars are stringified with their YAML shape). |
| `number` | Leading digits in a filename of the form `NNN-slug.ext`. Name-derived number and slug are actually populated even without this directive. |
| `status` | First word of the first nonempty line in a section named exactly `Status`, lowercased. It does not read a frontmatter `status` key. |
| `superseded_by` | Successor token from that status line, only when its status word is `superseded`. Recognizes `[042]`, `[ADR-042]`, or `ADR-042` with one to six digits; a 1900–2099 token resolving to no record is a calendar year, not a broken reference (see §7.8); an `ADR-`-prefixed longer run truncates to six digits, while a bare bracketed run longer than six digits names no successor token. |
| `purpose` | Trimmed body of the first section whose heading contains `purpose`, case-insensitively. |
| `requirements` | Headings starting with `requirement` (excluding the bare container heading), plus direct bullets of the first matching section. |
| `scenarios` | The same topic extraction using `scenario`. |
| `deltas` | Exact heading forms `ADDED Requirements`, `MODIFIED Requirements`, `REMOVED Requirements`, `RENAMED Requirements`; stores the uppercase operation. |
| `task_items` | Checkbox list items `- [ ]`, `- [x]`, `* [X]`, etc., outside code fences, with text and done state. |
| `principles` | Topic headings/bullets using `principle`. |
| `user_stories` | Topic headings/bullets using the prefix `user stor`, covering User Story and User Stories. |
| `index_tasks` | Pipe-table rows in the first section whose heading contains `task`; column 1 is the task, column 2 must contain relative links. |

For topic extraction, bullets under a deeper subsection are not treated as
direct bullets of the parent topic. Sections end at the next heading of the
same or a higher level; nested sections remain within the section body.

### 6.2 Markdown shapes that work well

````markdown
# ADR-001: Choose a storage engine

## Status

Accepted

## Purpose

Keep project state durable.

## Key Files

- `src/storage.go` - storage implementation

## Requirements

- The storage API must support atomic updates.

### Scenario: a write fails

- The previous value remains readable.

## Tasks

- [ ] T001 Implement the storage API
- [x] T002 Define the interface
````

For `index_tasks`, use a navigation table such as:

````markdown
# Acme index

## Navigation by Task

| Task | Specs |
| --- | --- |
| Change storage | [Storage](decisions/001-storage.md) |
````

### 6.3 Parsing boundaries

- Headings are ATX (`# Heading` through `###### Heading`), not Setext underline
  headings. Fenced examples are ignored by heading/topic/task scans.
- Frontmatter is parsed as real YAML and flattened into the flat string model:
  nested mappings, arrays, and block scalars are stringified with their YAML
  shape, so their keys still count as present. Use simple scalar lines when you
  need the value itself. `frontmatter_keys` tests key presence, not whether its
  value is nonempty or of a particular type.
- Relative links use the inline `[label](target)` shape. Link targets containing
  spaces or parentheses do not match. Reference-style links and full Markdown
  AST behavior are not provided.
- Anchor slugs are lowercased, punctuation is removed except `_`/`-`, and spaces
  become hyphens. A repeated heading resolves under its plain slug and its
  GitHub-style `-1`/`-2` suffixes; anchor URL-decoding is not modeled
  (percent-escapes in a link's path are decoded before the target is
  resolved). Use simple, unique heading text for predictable checks.
- Extracted metadata is available in the parsed model; declaring `extract`
  does not create a new sidebar widget. The present UI primarily renders the
  tree, actions, findings, companion warnings, counts, and optional dates.

## 7. Integrity checks: all eight kinds

Checks are deterministic structural/content checks. They do not compare prose
against source-code behavior or run your framework's CLI. Put semantic review
and framework-native validation in skills invoked by actions.

Most file checks parse their input directly and do not require the corresponding
`extract` directive. The important exception is a **concept-less `crossrefs`**
check: it selects concepts declaring `cross_refs`.

### 7.1 `required_files`

Three useful modes:

```yaml
# Fragment: every PRESENT root must contain INDEX.md.
- kind: required_files
  finding: ROOT_INDEX_MISSING
  files: [INDEX.md]

# Fragment: every matched change directory must carry its declared companions.
- kind: required_files
  finding: CHANGE_ARTIFACT_MISSING
  concept: change
  from: files

# Fragment: check paths listed in Key Files sections across matched documents.
- kind: required_files
  finding: SOURCE_PATH_MISSING
  from: key_files
```

Parameters: optional `concept`, and **either** a nonempty `files` list **or**
`from` (mutually exclusive). `from: files` requires `concept` and uses that
directory concept's companion files.

**Current execution details:**

- Without a `concept`, literal `files` are checked relative to **each present
  root**. With a `concept`, the same `files` resolve next to EACH instance of
  it — inside a matched directory, beside a matched document — so a concept
  turns one literal list into a per-instance companion assertion.
- With `from: files`, only matched directory instances of that concept count,
  and the concept must BE a directory concept (a file concept is a load
  error — it has no companions).
- With another `from`, the check scans the DECLARED concept's matched file
  instances (all of them when no concept is set) and treats extracted entries
  as **workspace-root-relative**, not document-relative.
- Missing roots are skipped. A DECLARED path must be a regular FILE: a
  directory standing in for `INDEX.md`, or as a document's companion, is a
  finding. An EXTRACTED path (any `from` except `files`) is asserted for
  EXISTENCE only — of any kind — because a document's own `## Key Files` list
  legitimately names a package directory; demanding a regular file there
  would turn a correct tree into permanent findings.
- Every check/concept pair must be shape-compatible: the content-bearing
  checks (`crossrefs`, `frontmatter_keys`, `content_regex`, `unique`,
  `references`, and `index_completeness`'s index) require a FILE concept —
  naming a directory concept is a load error, because such a check could
  never see an instance.
- `from` must be `files` or one of the list directives `key_files`, `cross_refs`,
  `headings`, `requirements`, `scenarios`, `principles`, `user_stories`, and
  `deltas`; any other directive is a load error, not a silent no-op. Most are
  not sensible paths. Prefer `key_files`; use `crossrefs` for links.
- Extracted candidates beginning with `/`, `~`, `<`, or containing glob syntax
  are skipped. For a path with a directory component, its first component must
  already be an existing workspace directory. Thus this is not a complete
  missing-path validator when the whole top-level directory is absent.

### 7.2 `name_pattern`

```yaml
# Fragment
- kind: name_pattern
  finding: DECISION_NAME_INVALID
  concept: decision
  pattern: '^[0-9]{3}-[a-z0-9]+(?:-[a-z0-9]+)*\.md$'
  allow: [_template.md]
```

Required: `concept`, `pattern`. Optional: `allow` (bare filenames/directory
names, no paths). Checks the instance's **base name**, including extension for
files. Works for file and directory concepts. Use `^`/`$` if the entire name
must conform; otherwise a regex substring match suffices.

### 7.3 `crossrefs`

```yaml
# Fragment
- kind: crossrefs
  finding: CROSS_REFERENCE_BROKEN
  concept: decision
```

`concept` is optional. With it, all matched files of that concept are checked;
without it, only file concepts extracting `cross_refs` are selected.

Relative links resolve from the source document's directory. Targets may be
outside the specification root **but must remain inside the workspace** to be
inspected. External URLs are ignored. Missing target files and missing heading
anchors are findings. Outside-workspace targets are skipped, and an unreadable
target's anchors are not reported as broken. This is not an external-link
checker or a proof that every reference is valid.

### 7.4 `index_completeness`

```yaml
# Fragment
- kind: index_completeness
  finding: INDEX_INCOMPLETE
  concept: index
  covers: [decision, guide]
```

Required: index `concept`, nonempty `covers` list of known concept kinds.
Each matched index file must mention every instance of the covered concepts
under its own directory subtree — file and directory instances alike, checked
on equal footing. A mention is an exact relative path, a bare name, or a raw
path-shaped token (so a directory listed without a `.md` suffix still counts;
a trailing-slash spelling names a directory too). Mentions are recognized in
links, inline-code fields, and tree-like fenced listings. It is not a semantic
navigation audit: a bare name may satisfy several same-named instances.

### 7.5 `frontmatter_keys`

```yaml
# Fragment
- kind: frontmatter_keys
  finding: OWNER_MISSING
  concept: decision
  keys: [owner]
```

Required: file `concept`, nonempty `keys` list. Uses the real YAML frontmatter
parser from §6. Missing keys create a finding; empty values still count as
present. It does not require `extract: [frontmatter]` and tests key presence
only, not value shape or schema.

### 7.6 `content_regex`

```yaml
# Fragment
- kind: content_regex
  finding: DECISION_HEADING_INVALID
  concept: decision
  in: body
  regex: '^# ADR-(?P<number>[0-9]{3}): \S'
  capture: number
  equals: filename_number
```

Required: `concept`, `regex`. Optional:

| Parameter | Meaning |
| --- | --- |
| `in: body` | Default. The markdown BODY (everything after a leading frontmatter block), raw: code fences still count, so a format sample can satisfy the pattern. |
| `in: prose` | The body minus fenced code blocks — a `### Requirement: Example` inside a fenced sample stops satisfying a structural check. |
| `in: first_line` | The body's first line (frontmatter skipped), not first nonempty line or first heading. |
| `in: first_heading` | The body's first ATX heading TEXT (no leading `#`), so a pattern like `^ADR-` matches the heading itself. |
| `in: frontmatter` | Leading frontmatter block's inner text, or empty text if absent. |
| `optional: true` | A non-match is NOT a finding, and an empty capture is not compared: the check asserts agreement only when the pattern matched something. A heading that spells no ADR number cannot mismatch one. Requires `equals: filename_number` — it relaxes that comparison, so without it the check could never report anything and the definition is rejected at load. |
| `capture` | Name of a named group present in `regex`. Alone it does not assert a value. |
| `equals: filename_number` | Compare that capture with the leading filename number NUMERICALLY (`043` = `43`). Requires `capture`. This is the only supported comparison. |

Patterns use Go's RE2-style regular expressions (no lookaround or
backreferences). `content_regex` is evaluated in multiline mode: `^`/`$` match
line boundaries. Add `(?s)` if you need dot to span newlines. The first regex
match is used. Numeric capture comparisons ignore zero padding. A filename
without a number skips the comparison, so pair it with `name_pattern`.

Document text is normalized at read (a UTF-8 BOM is stripped, CRLF and lone
CR fold to LF), so a Windows checkout cannot defeat a `$`-anchored pattern or
hide a BOM-led frontmatter block. Frontmatter never enters the body, prose or
heading inputs — a YAML block scalar's contents are metadata, not document
prose.

### 7.7 `unique`

```yaml
# Fragment
- kind: unique
  finding: DECISION_NUMBER_DUPLICATE
  concept: decision
  field: number
```

Required: file `concept`, `field`. The field must be one of the scalar
directives **`number`, `title`, `purpose`, `status`, `superseded_by`**; any
other extraction directive is a load error, not a silent no-comparison. There
is no `frontmatter.owner`, grouping, compound key, or per-directory uniqueness
syntax.

Empty values are skipped. Numeric values are normalized (`001` equals `1`);
other values compare literally. One finding is produced per duplicate group.
The concept need not retain that field in `extract` for the check to work.

### 7.8 `references`

```yaml
# ONE check, two findings: resolution failures under `finding`, loops under
# `cycle_finding`.
- kind: references
  finding: SUCCESSOR_MISSING
  cycle_finding: SUCCESSOR_CYCLE
  concept: decision
  field: superseded_by
  of: decision
  acyclic: true
```

Required: source file `concept`, scalar `field`, target concept `of`.
Optional: `acyclic` (default `false`) and `cycle_finding` (legal only with
`acyclic`, must differ from `finding`). The useful scalar field set is the
same as `unique`; empty references are skipped.

Targets are indexed using path/name/slug identities and normalized leading
numbers — each path identity in BOTH spellings (with and without the `.md`),
so a reference to `records/001-a` resolves the same file as
`records/001-a.md`. An ADR successor token such as `042` is the most
straightforward use; successor numbers carry up to six digits (a 1024th ADR
works), and a token in 1900–2099 that resolves to no record is treated as a
calendar year, not a broken reference. An `ADR-`-prefixed longer digit run
is truncated to its first six digits and then judged like any other token; a
bare bracketed run longer than six digits (`[1234567]`) is not a successor
token at all and is silently skipped — keep successor references to six
digits or fewer. Do not use ambiguous duplicate
identities: path-order first wins. `acyclic: true` ADDS the cycle report
(under `cycle_finding`); it never silences the missing-target report — a
lone `references {acyclic: true}` still checks both. The DSL cannot express
an arbitrary relationship extracted from a custom frontmatter field.

## 8. Actions and skill dispatch

### 8.1 Action fields and prerequisites

| Field | Required / default | Meaning |
| --- | --- | --- |
| `kind` | Required | Unique action slug. Most names are arbitrary; `fix-findings` is reserved for findings gestures. |
| `label` | Required, nonblank | Button text. |
| `skill` | Optional | Agent Skill slug to activate. No skill means no dispatch. Use the actual installed name. |
| `prompt` | Optional | Message text sent into the chat when the user confirms the panel's dispatch dialog — the dialog previews the prompt verbatim first, so a repo-authored prompt never enters the chat unseen. If omitted/empty on the wire, the UI falls back to `label`. |
| `concept` | Optional | Attach action to existing nodes of this concept instead of the toolbar. |
| `requires` | Optional list, empty | All listed tree prerequisites must hold. |

Supported prerequisite tokens:

| Token | Meaning |
| --- | --- |
| `root` | The tree is present: at least one declared root exists **and** every root marked `required` exists. |
| `findings` | There is at least one finding, including warnings. |
| `concept:<kind>` | At least one matched file **or directory** instance of that concept exists anywhere in the parsed tree. |
| `companion:<kind>` | At least one instance of that concept exists somewhere in the tree — the coarse, tree-wide half of "the artifact this step builds on is in place". |
| `absent:<kind>` | The action's holder concept has MORE instances than `<kind>` does — some holder still lacks its artifact. This is what creation steps gate on: a Plan action attached to the feature concept with `absent: plan` stays live on the features that have no `plan.md` yet and renders DISABLED on the ones that do (disabled, never hidden — the next workflow step stays visible with its reason). |

Unknown or duplicate prerequisites are rejected, and so is `absent:<kind>` on an
action that declares no `concept` — there would be no holder to compare the
count against. These are **tree-wide** tests, not per-node tests: they decide
whether the gesture is usable at all. On top of that the backend refines
`companion:`/`absent:` per holder node — a node's artifacts are the instances
beside it in its own directory, or below it when the holder is a directory
instance — and lists the nodes where the refinement fails in the action's
`unavailable_at` field. A Plan action on the feature concept with `absent: plan`
therefore stays live on the features that still lack `plan.md` and renders
disabled on the ones that already have it. There is no `not`, `or`, numeric
threshold, task-completion test, or arbitrary expression in `requires`.

The backend checks each action's `skill` against the workspace's callable
skill catalog by exact name (a declared `vibespec-check` needs an installed
`vibespec-check`) and reports an unresolvable skill as `available: false` —
the panel then renders the action inert with the reason instead of dispatching
into a missing skill. Recognition may need only one skill while actions
reference several: install all skills used by your integration and keep them
enabled in CODE-mode Capabilities. A manual skill can be explicitly activated
by the action; a disabled skill is not made available merely by being named
in YAML.

### 8.2 Toolbar and node actions

No `concept` means a toolbar action. An unavailable toolbar action remains
visible but disabled, with the generic tooltip **Not available for this tree
yet**. An action without a skill is also disabled.

A `concept` means an action on existing tree nodes of that concept. The UI
renders node actions that carry a skill and are not `fix-findings`; a node
action whose requirements are unmet renders **disabled with the reason** — the
next workflow step stays visible — and an action with no skill builds no
dispatch at all.

```yaml
# Fragment
- kind: implement
  label: Implement
  skill: acme-implement
  concept: change
  requires: [root, concept:change]
  prompt: Implement the approved change at {path} (name {name}).
```

Only two substitutions exist in normal action prompts:

- `{path}` → node path relative to the **workspace**, such as
  `acme-specs/changes/001-storage`.
- `{name}` → node base name, such as `001-storage`.

No absolute path, content, frontmatter, or extra variables are substituted.
Unknown placeholders remain literal. On toolbar/empty-state dispatches,
`{path}` and `{name}` become empty strings. Labels are not templated.

Attach a **create** action to the toolbar or an existing parent, not to the
not-yet-created file's concept: no node means no node action to click.

### 8.3 Bootstrap action

The empty state chooses the **first toolbar action with an empty `requires`
list and a nonempty skill**, excluding `fix-findings`. It does not inspect
whether its kind is named `init` or `initialize`.

Put your intended bootstrap first, leave it without prerequisites, and give
ordinary workflow actions appropriate prerequisites. Bootstrap also remains
a toolbar action after initialization; there is no `root-absent` prerequisite.
If you declare no eligible bootstrap, the empty state has no call to action.

### 8.4 Findings repair: `fix-findings`

Declaring this exact action kind opts into the header finding badge and the
individual finding repair gestures:

```yaml
# Fragment
- kind: fix-findings
  label: Repair findings
  skill: acme-review
  requires: [root, findings]
  prompt: Repair the listed integrity findings, then verify the tree again.
```

It is excluded from the normal toolbar and node-action lists. Without it,
findings are still displayed but their fix affordances are inert. The UI
appends current findings verbatim to the action's prompt:

```text
Repair the listed integrity findings, then verify the tree again.

Findings:
- [BROKEN_LINK] acme-specs/INDEX.md: link does not resolve: missing.md
```

The header sends all findings; a finding card sends only that finding. The
copy control copies the same bullet format. Use a self-contained repair prompt:
this specialized dispatch does not perform the normal node placeholder
substitution. Do not use `requires` on this action as a security control; it
gates presentation, not authority. The dispatch still refuses an unavailable
gesture like every other surface — an unavailable badge or finding card builds
no dispatch — but the real boundary is that a click starts an ordinary agent
task under the full policy pipeline (§8.5).

### 8.5 What clicking actually does

All action surfaces use the same message-sending path with the skill in the
explicit active-skills slot. A normal click uses the active session;
**Shift-click requests a new session**. Sending/session errors surface in chat
or the panel's error banner. Subsequent work is an ordinary agent task:
YAML neither grants tools nor bypasses policy, confirmations, or skill
activation rules.

Action dispatch uses the same exact-name rule as detection: when the status is
built, a declared `skill` that the workspace does not carry under that exact
name is reported not-callable, and the panel renders the action inert. A
differently spelled installation (for example the retired `spec-check`) does
not satisfy a declared `vibespec-check`.

## 9. Views and tree presentation

Declare explicit sections, for example:

```yaml
# Fragment
views:
  sections:
    - id: changes
      label: Changes
      kind: tree
      concepts: [change, proposal, tasks]
    - id: decisions
      label: Decisions
      kind: tree
      concepts: [decision]
    - id: findings
      label: Integrity findings
      kind: findings
```

| Section field | Rule |
| --- | --- |
| `id` | Required unique slug. |
| `label` | Required nonblank heading. |
| `kind` | `tree`, `findings`, or `checks` — at most ONE non-tree section per framework (a second is a load error: both would render the same findings under two headings). |
| `concepts` | Optional list of existing concept kinds; for a useful tree, list the kinds explicitly in desired top-entry order. |

Tree sections select instances of the listed concepts, build their ancestor
folders, and display recursive children. Instances and child paths are
path-sorted. Concept order can influence top entries; it is not a custom sort
expression.

Each tree section carries its own node MEMBERSHIP (its concepts' instances
plus their ancestor folders): a section renders only its own subtree, so two
sections sharing a top-level folder show their own concepts under it and stop
bleeding each other's entries. The forest nodes themselves remain shared —
separate top-level subtrees are still the cleanest shape.

`findings` and `checks` both render the one findings list — which is exactly
why a second non-tree section is rejected. Empty tree sections are not
rendered; empty finding lists do not create a useful pass report.

`views` is schema-optional: a framework that declares no sections gets ONE
implicit tree over every concept (recognized documents are always visible),
plus the panel's implicit findings rendering.

Node labels are each instance's OWN identity — the document's display title
(its first heading with the record's own `NNN:` prefix stripped) falling back
to the file name, and the directory name for a directory instance. Eight ADRs
read as eight different rows. The concept's static `label` is no longer the
row text.

Directory companion warnings come from `files`; companion file contents need
separate concepts. Optional git-derived dates are backend enrichment, not a
DSL date extractor. There is no DSL field for date format or arbitrary columns.

## 10. Worked example: Acme SDD

This example has one root, a navigation index, numbered ADR-like decisions,
and change bundles. It exercises all eight check kinds without relying on
custom discovery directories or implicit views.

### 10.1 Install the workflow skills

Install valid `SKILL.md` directories for:

- `acme-init`: initialize the tree without overwriting existing documents.
- `acme-review`: review or repair findings and check specifications against code.
- `acme-implement`: implement an approved change identified by its workspace path.

Use `<workspace>/.agents/skills/`, or either configured shared tier. The
quick-start skill format is sufficient; give each skill its own real procedure,
not just a placeholder description. The complete definition below requires
both init and review for recognition, and references implement for node actions.

### 10.2 Complete definition

Save this as either operator `specs.yaml` (replace the quick-start `acme`
document rather than keeping two copies of the same ID):

```yaml
schema: sdd-framework/v1
id: acme
label: Acme SDD
description: Numbered decisions and task-oriented change bundles.
priority: 40

detect:
  markers: [acme-specs/INDEX.md]
  skills:
    dirs: [.agents/skills]
    all_of: [acme-init, acme-review]

roots:
  - id: main
    path: acme-specs
    label: Acme specifications
    required: true

concepts:
  - kind: index
    match: {root: main, glob: INDEX.md}
    extract: [title, cross_refs, index_tasks]
    required: [title, non_empty]
  - kind: decision
    match: {root: main, glob: 'decisions/*.md'}
    extract: [title, number, status, superseded_by, frontmatter, cross_refs]
    required: [title, status]
  - kind: change
    match: {root: main, glob: 'changes/*', dir: true}
    files: [proposal.md, tasks.md]
  - kind: proposal
    match: {root: main, glob: 'changes/*/proposal.md'}
    extract: [title, purpose, cross_refs]
    required: [title, purpose]
  - kind: tasks
    match: {root: main, glob: 'changes/*/tasks.md'}
    extract: [title, task_items]
    required: [title, tasks]

checks:
  - kind: required_files
    finding: ROOT_INDEX_MISSING
    message: Every initialized Acme tree must have INDEX.md.
    files: [INDEX.md]
  - kind: required_files
    finding: CHANGE_ARTIFACT_MISSING
    message: Every change must carry proposal.md and tasks.md.
    concept: change
    from: files
  - kind: name_pattern
    finding: DECISION_NAME_INVALID
    concept: decision
    pattern: '^[0-9]{3}-[a-z0-9]+(?:-[a-z0-9]+)*\.md$'
  - kind: crossrefs
    finding: BROKEN_LINK
    message: A relative document link or anchor does not resolve.
  - kind: index_completeness
    finding: INDEX_INCOMPLETE
    concept: index
    covers: [decision]
  - kind: frontmatter_keys
    finding: OWNER_MISSING
    concept: decision
    keys: [owner]
  - kind: content_regex
    finding: DECISION_HEADING_INVALID
    concept: decision
    in: body
    regex: '^# ADR-(?P<number>[0-9]{3}): \S'
    capture: number
    equals: filename_number
  - kind: content_regex
    finding: TASK_LIST_EMPTY
    concept: tasks
    regex: '^[-*] \[[ xX]\] \S'
  - kind: unique
    finding: DECISION_NUMBER_DUPLICATE
    concept: decision
    field: number
  - kind: references
    finding: SUCCESSOR_MISSING
    cycle_finding: SUCCESSOR_CYCLE
    concept: decision
    field: superseded_by
    of: decision
    acyclic: true

actions:
  - kind: init
    label: Initialize
    skill: acme-init
    prompt: Initialize the Acme tree without overwriting existing documents.
  - kind: review
    label: Review
    skill: acme-review
    requires: [root]
    prompt: Review Acme specifications against the implementation and report drift.
  - kind: implement
    label: Implement
    skill: acme-implement
    concept: change
    requires: [root, concept:change]
    prompt: Implement the approved change at {path} (name {name}); keep its specs current.
  - kind: fix-findings
    label: Repair findings
    skill: acme-review
    requires: [root, findings]
    prompt: Repair the listed integrity findings and verify the Acme tree again.

views:
  sections:
    - id: navigation
      label: Navigation
      kind: tree
      concepts: [index]
    - id: decisions
      label: Decisions
      kind: tree
      concepts: [decision]
    - id: changes
      label: Changes
      kind: tree
      concepts: [change, proposal, tasks]
    - id: findings
      label: Integrity findings
      kind: findings
```

### 10.3 Sample tree and documents

```text
acme-specs/
├── INDEX.md
├── decisions/
│   └── 001-storage.md
└── changes/
    └── 001-storage/
        ├── proposal.md
        └── tasks.md
```

`acme-specs/INDEX.md`:

````markdown
# Acme specifications

## Navigation by Task

| Task | Specs |
| --- | --- |
| Change storage | [Storage decision](decisions/001-storage.md) |
````

`acme-specs/decisions/001-storage.md` (the actual file starts with `---`):

````markdown
---
owner: platform
---
# ADR-001: Use durable storage

## Status

Accepted

## Purpose

Keep user data durable across restarts.
````

`acme-specs/changes/001-storage/proposal.md`:

````markdown
# Durable storage

## Purpose

Implement the [storage decision](../../decisions/001-storage.md).
````

`acme-specs/changes/001-storage/tasks.md`:

````markdown
# Storage tasks

- [ ] T001 Implement the storage adapter
- [ ] T002 Verify recovery after restart
````

### 10.4 Expected results and deliberate breakages

With the skills installed and the sample files above:

- Acme is recognized; its root is present.
- The parsed model has **four file documents** and one `change` directory
  instance. Ancestor folder nodes are additional presentation nodes.
- There are no integrity findings.
- The change node offers **Implement**, with
  `{path} = acme-specs/changes/001-storage` and `{name} = 001-storage`.
- Review is enabled; Initialize remains available as explained in §8.3.

Try these independently, restoring the sample after each experiment:

| Change | Expected result |
| --- | --- |
| Delete the change's `tasks.md` | `CHANGE_ARTIFACT_MISSING` plus a missing companion warning on the change node. |
| Empty `tasks.md` | `TASK_LIST_EMPTY`; the file still exists, so no missing-companion finding. |
| Remove the index link | `INDEX_INCOMPLETE`. |
| Change the decision heading to `# ADR-002: ...` | `DECISION_HEADING_INVALID`. |
| Remove `owner` frontmatter | `OWNER_MISSING`. |
| Add `decisions/001-other.md` with the same number | `DECISION_NUMBER_DUPLICATE`; also index coverage unless the new file is listed. |
| Set Status to `Superseded by ADR-099` with no such decision | `SUCCESSOR_MISSING`. |
| Set decision 001's Status to `Superseded by ADR-001` | `SUCCESSOR_CYCLE`. |
| Replace the proposal link with `[missing](no-such-file.md)` | `BROKEN_LINK`. |
| Remove the entire `acme-specs/` directory | Detected-but-empty state, no integrity findings, bootstrap still offered. |

## 11. Testing and troubleshooting

### 11.1 User acceptance checklist

1. Confirm that your skills are usable through the chat `/` catalog, not merely
   present on disk. Check CODE-mode capability settings and skill frontmatter.
2. Start with one small complete definition and explicit tree sections.
3. Select the right project and session/worktree. Confirm the operator file is
   in the registered checkout while documents are in the active workspace.
4. Force a fresh status fetch; select your framework if several are detected.
5. Test **no tree**, **empty root**, and **populated tree** separately.
6. Trigger at least one violation for each declared check, then fix it and
   refresh. A permanently silent check may have the wrong scope or field.
7. Test every action, including its prompt and activated skill, on a disposable
   project. Test node substitutions and Shift-click if you expose those flows.
8. Restart c0wrk and confirm the project's selected framework is remembered.
9. Test your global definition without its project override to ensure the
   fallback behaves as intended.

### 11.2 Validate with the implementation (optional, source checkout)

The integration does not require a source build. If you have a c0wrk source
checkout and its Go development environment, the following Go program validates
an operator file using the **actual loader**, not a generic YAML linter.
Save it as a temporary `.go` file outside the repository's root package (for
example, `/tmp/validate-sdd.go` on Unix), then run it from the source checkout:

```go
package main

import (
    "fmt"
    "os"

    "github.com/v0lka/c0wrk/core/sdd"
)

func main() {
    if len(os.Args) != 2 {
        fmt.Fprintln(os.Stderr, "usage: validate-sdd <specs.yaml>")
        os.Exit(2)
    }
    definitions, warnings := sdd.LoadDefinitions(sdd.OriginProject, os.Args[1])
    for _, warning := range warnings {
        fmt.Fprintln(os.Stderr, warning)
    }
    for _, definition := range definitions {
        fmt.Println("valid:", definition.ID)
    }
    if len(warnings) > 0 || len(definitions) == 0 {
        os.Exit(1)
    }
}
```

```sh
go run /tmp/validate-sdd.go /path/to/project/.c0wrk/specs.yaml
```

This validates documents, not whether installed skills match or actions run.
`LoadDefinitions` alone does not merge bundled/global definitions. For deeper
experiments, `sdd.Load(globalPath, projectPath)` assembles the catalog;
`sdd.DetectInstalled` accepts explicit skill/path evidence; and
`sdd.ParseTree(workspace, framework)` reads a fixture tree. There is no dedicated
end-user `c0wrk validate-specs` CLI command in this integration.

For contributors, the module's regression tests can be run from the checkout:

```sh
go test -count=1 -v ./core/sdd
```

### 11.3 Symptom → diagnosis

| Symptom | Things to check |
| --- | --- |
| Specs is absent | Real project? Specs gate on? At least one valid definition with matching skill names? Correct shared directories configured? A marker/tree alone is insufficient. Retry discovery by reopening the project. |
| YAML appears ignored | Exact file path and checkout, not just the session worktree; complete documents; `roots` sequence; schema; unknown fields; duplicate IDs; syntax fault before your document. |
| Global edit has no effect | A project document with the same ID replaces it completely. Also force refresh. |
| Definition loads but never recognizes | Loaded definition missing an `all_of` member; `any_of` not satisfied; directory-name mismatch; skills installed only as commands. A marker-only `detect` never gets this far — it is a load error, see the panel's warnings. |
| Recognized but empty state | Root absent/non-directory in the active workspace; escaping symlink; root path points at the wrong tree. |
| Documents counted but no tree | Explicit tree sections missing, empty `concepts`, or concepts not included in any tree section. |
| Files absent from the model | Wrong root ID, file-vs-directory match, glob depth/case, a more-specific concept won, no file concept for a companion, unreadable or oversized file. |
| Every row has the same label | The rows are named by the instances themselves: a directory-based concept shows the directory names, and a concept that extracts `title` shows each document's own heading. A concept `label` never overrides row text (it is document metadata), so identical rows mean identical file names or identical document titles — check the documents' first headings, and the `match.glob` depth that grouped them. |
| Two sections look alike | Shared ancestor forest or duplicate `findings`/`checks` sections; see §9. |
| Missing content does not produce findings | `required` only records missing metadata. Add an explicit check. |
| Check is always silent | No source instances; all roots absent; scalar field unsupported by `unique`/`references`; `required_files.from` supplies no values; concept-less crossrefs need `cross_refs`; missing target is not checked by `acyclic: true`. |
| Content regex unexpectedly fails | Actual first line vs heading after frontmatter; YAML escaping; RE2 syntax; exact capitalization/heading shape. |
| Button disabled | Unmet tree-wide requirements or no `skill`; tooltip is generic. `root` means the whole tree-present predicate (see §8.1). |
| Node action missing | No existing target node; action unavailable; missing skill field; concept omitted from tree views. |
| Button sends a task without the expected skill | Skill malformed, disabled, undiscovered by the execution catalog, or wrong action skill name; detection success is weaker than activation success. |
| Custom skill directory recognizes but will not activate | The skill fails the validating parser, is disabled in CODE-mode Capabilities, or its framework is not present to the skill-dir probe; framework-declared directories do extend session discovery (see §12). |
| No Initialize CTA | No toolbar action with a skill and empty `requires`, or at least one root directory already exists, so the panel shows the tree branch. |
| Findings cannot be fixed by clicking | No `fix-findings` action, or it has no skill. Findings still report correctly. |
| Framework choice seems to ignore priority | A still-recognized persisted choice wins. Otherwise tree presence and skill scope outrank catalog priority. |
| Change in another root is not refreshed immediately | Manual refresh; path-bearing workspace events currently scope against the first present root. |

## 12. Security, boundaries, and current limitations

Treat a repository's `.c0wrk/specs.yaml` and its skills as code-review material.
YAML may supply prompts that reach an agent **after you click**. Read actions
and skill bodies before trusting unfamiliar integrations.

### What the parser does and does not authorize

- Tree reads are workspace-contained, including real-path containment checks.
  Escaping roots/entries are skipped; per-document reads are capped at **8 MiB**
  and the whole parse at **64 MiB of content / 2000 documents** (documents past
  the budget are excluded whole and the skip is named in the panel's warnings).
  The model is bounded too: retained findings are capped at **500** plus one
  counted "further findings suppressed" line, and cross-reference resolution at
  **20 000** references (each one costs a stat), so a link-dense document cannot
  grow the finding list or the resolution work linearly. Past either cap the
  remainder is named in the panel's warnings instead of being silently dropped.
  Missing/unreadable files can produce a partial model or warnings rather than
  a fatal parse error. Home-relative (`~`) declared paths are rejected at load.
- The DSL does not execute scripts, install packages/skills, run framework CLIs,
  or write files. Actions are user-originated agent tasks, not direct YAML
  execution hooks.
- Recognition only checks the installed skill shape/name; it does not certify
  the skill's content or grant permissions. `requires` controls workflow
  presentation, not security authority.
- The tree walker prunes exactly two directory names — `.git` (the VCS store)
  and `node_modules` (dependencies) — because neither can hold a project's own
  specifications and a lone `path: .` root would otherwise classify npm READMEs
  as documents. There is no general ignore list and no `.gitignore` filtering
  (`match.exclude` shapes concept membership, not the walk). Avoid `path: .`
  and unnecessarily broad recursive roots — the budgets below stop the worst
  case, but a tight concept set stays the right design.

### Current implementation limits to design around

| Area | Supported approach / limitation |
| --- | --- |
| Custom skill discovery | The merged framework catalog (built-ins + global + checkout DSL) drives detection, listing AND session activation, so a custom `detect.skills.dirs` is callable wherever it is detected. `skills.dirs` in c0wrk config shapes the shared base set for both sides identically. |
| Skill-name aliases | None. Detection and action dispatch both compare exact names, so the declared spelling is the installed spelling. To accept several spellings, list them all in `any_of`. |
| Root completeness | `roots[].required` is honored: `Present` requires every required root to exist, and a missing one is a warning (the empty state names it). `root` (the action token) tests that same presence predicate. |
| Required content | File `required` metadata does not become findings or comprehensive missing-content UI. Add checks. |
| Arbitrary metadata | No JSONPath/YAMLPath, arbitrary frontmatter scalar extraction, or custom user-defined extractor/check kind. Frontmatter is parsed as real YAML (quoted/nested keys, block scalars). |
| Scalar checks | Only `number`, `title`, `purpose`, `status`, `superseded_by` currently produce scalar comparison values. |
| Literal required files | Without a `concept` they are checked at all present roots; WITH a `concept` they resolve next to each instance. Existence demands a regular file — a directory of the required name is a finding. |
| Tree views | One implicit tree exists when no sections are declared; explicit sections render their own membership-filtered subtrees (at most one non-tree section). |
| Checks section | Shows findings, not all checks with pass states; at most one such section. |
| Workflow conditions | Tree-wide conjunction, plus per-node `absent:`/`companion:` refinement computed by the backend and reported as `unavailable_at` — not per-change readiness, negation, or task completion. |
| Bootstrap | First eligible no-requirements toolbar action; not a reserved `init` behavior and not hidden after initialization. |
| Refresh | No dedicated operator-file reload event. Explicit refresh/reopen may be necessary; multi-root path-filtering currently follows the first present root. |
| Framework semantics | Matching/extraction/check vocabularies are fixed. Anything outside them needs a skill-based workflow or a c0wrk implementation change, not invented YAML keys. |

These distinctions are important when adapting a sophisticated upstream
framework: the panel is an integration layer, not that framework's complete
native validation engine.

## 13. Implementation and further reading

This guide was checked against the implementation, not just the intended DSL.
The following are useful when developing or debugging an integration:

| Topic | Source |
| --- | --- |
| Fields, vocabularies, validation | [`core/sdd/framework.go`](../core/sdd/framework.go) |
| Operator loading, warnings, full replacement | [`core/sdd/dsl.go`](../core/sdd/dsl.go) |
| Skill recognition, exact name matching, default ordering | [`core/sdd/detect.go`](../core/sdd/detect.go) |
| Markdown parsing, glob classification, checks, nodes | [`core/sdd/parse.go`](../core/sdd/parse.go) |
| Parsed document/tree model | [`core/sdd/model.go`](../core/sdd/model.go) |
| Bundled integration examples | [`vibespec.yaml`](../core/sdd/frameworks/vibespec.yaml), [`openspec.yaml`](../core/sdd/frameworks/openspec.yaml), [`spec-kit.yaml`](../core/sdd/frameworks/spec-kit.yaml) |
| Definition-file locations | [`backend/config/sdd_store.go`](../backend/config/sdd_store.go) |
| Detection probes, selection, action prerequisites | [`backend/frontend_api_sdd.go`](../backend/frontend_api_sdd.go) |
| Checkout vs session workspace | [`backend/frontend_api_specs.go`](../backend/frontend_api_specs.go) |
| Session skill discovery | [`core/skilldirs.go`](../core/skilldirs.go) |
| Action placement, templates, bootstrap, repair | [`specsActions.ts`](../frontend/src/components/specs/specsActions.ts) |
| Toolbar vs node action behavior | [`SpecsQuickActions.tsx`](../frontend/src/components/specs/SpecsQuickActions.tsx), [`SpecsSectionTree.tsx`](../frontend/src/components/specs/SpecsSectionTree.tsx) |
| Section rendering and refresh | [`SpecsPanel.tsx`](../frontend/src/components/specs/SpecsPanel.tsx), [`specsPaths.ts`](../frontend/src/components/specs/specsPaths.ts) |
| Deterministic parser tests and fixture trees | [`core/sdd/parse_test.go`](../core/sdd/parse_test.go), [`core/sdd/tree_test.go`](../core/sdd/tree_test.go), [`core/sdd/testdata/`](../core/sdd/testdata/) |
| Loader and detection regression tests | [`dsl_test.go`](../core/sdd/dsl_test.go), [`detect_test.go`](../core/sdd/detect_test.go) |

Related specifications:

- [SDD Framework DSL](../specs/domains/sdd-dsl.md)
- [Specs Subsystem](../specs/domains/specs.md)
- [ADR-126: SDD Framework DSL](../specs/decisions/126-sdd-framework-dsl.md)
- [Security policy](../SECURITY.md)

If a broad specification statement and a detail in this guide differ, consult
the linked implementation and regression tests for the current behavior;
the limitations above deliberately call out places where the implementation
is narrower than the general DSL description.
