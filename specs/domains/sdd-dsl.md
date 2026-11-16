# SDD Framework DSL

## Purpose

The declarative description of an SDD (specification-driven development) framework: one YAML document says how a workspace is recognised as that framework, which directories it owns, what document concepts live in them, which integrity checks apply, which lifecycle actions it offers and how it renders. c0wrk renders WHATEVER framework a workspace has installed through this DSL — there is no built-in layout, no native grammar and no code path per framework.

## Key Files

- `core/sdd/framework.go` - the document model (`Framework`, `Root`, `Detect`, `Skills`, `Concept`, `Match`, `Check`, `Action`, `Views`, `Section`), the strict single-document parser `Parse`, its validation (per-kind parameter sets, vocabularies, regex compilation, slug/path/glob rules) and the `Error` type naming document + field path
- `core/sdd/dsl.go` - the operator layer: `Origin` (`builtin` | `global` | `project`), `Definition` (framework + provenance), `Load(globalPath, projectPath)`, `LoadDefinitions`, `ParseDefinitions` (a `---`-separated stream, fail-soft per document) and `Merge` (by ID, whole-document replace, `priority` then `id` order)
- `core/sdd/builtin.go` - the embedded catalog (`core/sdd/frameworks/*.yaml` via `//go:embed`): `BuiltinNames`, `Builtins`, `BuiltinDefinitions`, `Builtin(id)`; built-ins are ordinary DSL documents
- `core/sdd/frameworks/sdd.yaml` - c0wrk's own tree (`specs/META.md`, `INDEX.md`, `architecture/**`, `domains/**`, `contracts/**`, `decisions/**`, each excluding its `_`-prefixed templates), its eight checks (one `references` check carrying both the resolution and the cycle finding) and its `spec-*` lifecycle actions including the `fix-findings` gesture
- `core/sdd/frameworks/openspec.yaml` - OpenSpec: `openspec/specs/*` capability folders + `spec.md`, `openspec/changes/*` change folders with delta specs and task lists
- `core/sdd/frameworks/spec-kit.yaml` - GitHub Spec Kit: `.specify/` (constitution, templates, active-feature pointer) + `specs/<feature>/` artifact sets
- `backend/config/sdd_store.go` - `SDDPath` (`<agentDir>/specs.yaml`, i.e. `~/.c0wrk/specs.yaml`) and `ProjectSDDPath` (`<checkout>/.c0wrk/specs.yaml`, `ProjectMetaDirName`); both are read-only path derivations that never materialize a file
- `core/sdd/detect.go` - the I/O-free detector (`DetectInstalled`, `Evidence`, `Detection`, `DetectResult`, `SkillScope`, `SkillNameMatches`)
- `core/sdd/parse.go` - `ParseTree`, the one filesystem entry point: root walking, concept classification, declared extraction, declared checks, containment and size caps
- `core/sdd/model.go` - the parsed model (`Tree`, `RootState`, `Document`, `Node`, `View`, `Finding`, `Counts`, `TaskItem`, `IndexTask`); no I/O
- `specs/domains/specs.md` - the subsystem that consumes this DSL: detection, selection, panel, gate

## Core Types

```go
type Framework struct {                    // one complete framework document
    Schema      string    // "sdd-framework/v1" — the only accepted schema
    ID          string    // stable slug, unique across the merged catalog
    Label       string    // human name
    Description string    // one-paragraph summary
    DocsURL     string    // absolute http(s) URL
    Priority    int       // catalog order (lower first), default 0
    Detect      Detect    // presence signal
    Roots       []Root    // directories the framework owns
    Concepts    []Concept // document/directory kinds inside those roots
    Checks      []Check   // integrity primitives
    Actions     []Action  // lifecycle gestures
    Views       Views     // rendering sections
}

type Root struct { ID, Path, Label string; Required bool }   // Path is clean, workspace-relative, slash separated

type Detect struct {
    Markers []string          // tree signal only — never visibility
    Skills  Skills            // the visibility signal
}
type Skills struct { Dirs, AllOf, AnyOf []string }           // Dirs are workspace-relative skill directories

type Concept struct {
    Kind     string   // slug; referenced by checks, actions and views
    Match    Match    // which paths are instances
    Files    []string // companion files per instance directory (requires Match.Dir)
    Extract  []string // extraction directives
    Required []string // per-instance assertions
}
type Match struct {
    Root, Glob string; Dir bool            // "*" within a segment, "**" across segments
    Exclude []string                       // globs a matched path must NOT match
}                                           // (how an actual-docs concept sheds its `_` templates)

type Check struct {                       // the parameter set is per-kind and enforced
    Kind, Finding, Message, Severity string
    Concept, From, Pattern, In, Capture, Equals, Field, Of string
    Files, Allow, Keys, Covers []string
    Acyclic bool
    CycleFinding string                     // references+acyclic: the finding a LOOP reports under
    Optional bool                           // content_regex: a non-match is not a finding
}

type Definition struct { Framework; Origin Origin; Document string }  // provenance for the surface
```

Vocabularies are closed and validated — an unknown member is a load error, never a silently ignored value:

| Vocabulary | Members |
| ---------- | ------- |
| `extract` directives | `title`, `headings`, `key_files`, `cross_refs`, `frontmatter`, `number`, `status`, `superseded_by`, `purpose`, `requirements`, `scenarios`, `deltas`, `task_items`, `principles`, `user_stories`, `index_tasks` |
| `required` assertions | `non_empty`, `title`, `status`, `purpose`, `requirements`, `scenarios`, `tasks`, `principles`, `user_stories` |
| check kinds | `required_files`, `name_pattern`, `crossrefs`, `index_completeness`, `frontmatter_keys`, `content_regex`, `unique`, `references` |
| `severity` | `error` (default), `warning` |
| view section kinds | `tree`, `findings`, `checks` |
| action `requires` | `root`, `findings`, `concept:<kind>`, `companion:<kind>`, `absent:<kind>` |

## Behavior

### Document → catalog

```
  core/sdd/frameworks/*.yaml        (embedded, Origin=builtin)
        │  Parse → strict per-document validation
        ▼
  ~/.c0wrk/specs.yaml               (operator global, Origin=global)
        │  ParseDefinitions: every --- document validated independently;
        │  an invalid one is a WARNING naming it, the rest still apply
        ▼
  <checkout>/.c0wrk/specs.yaml      (operator project, Origin=project)
        │
        ▼
  Merge(sets...)  ── by ID: a later definition REPLACES an earlier one in
                     full (never field-by-field); nothing is ever removed,
                     so a built-in survives every operator file;
                     ordered by priority, then id
        ▼
  sdd.LoadResult{Frameworks []Definition, Warnings []string}
```

Load never returns an error. An absent operator file is the neutral state (no definitions, no warning); an unreadable file, a YAML syntax fault, or a document this schema cannot decode is a warning while the definitions that did parse still apply. Inside ONE file a repeated ID replaces the earlier definition WITH a warning (an in-file duplicate is a typo; the cross-source merge is deliberate and silent). Blankness is decided by CONTENT, not by the two header keys: a document carrying no keys at all (an empty file, a trailing `---`, an explicit `null`) is skipped silently, while a document carrying any key without `schema`/`id` — the likeliest operator mistake, a fragment copied out of the guide — is validated and named in the warnings like every other invalid definition. Neither operator file is ever created — `SDDPath`/`ProjectSDDPath` are pure path derivations.

### Match globs, excludes and specificity

`*` matches within one path segment, `**` matches zero or more segments (`domains/**/*.md` covers `domains/x.md` and `domains/frontend/x.md`). Every pattern segment must parse as a glob (a malformed segment — `[` unbalanced — is a LOAD error naming `concepts[i].match.glob`, never a silently empty concept). `match.exclude` lists globs a matched candidate must NOT match to belong to the concept: this is how an actual-documents concept sheds its section's `_`-prefixed templates while a more specific template concept claims them. When several concepts match one path the MOST SPECIFIC glob wins — deepest fixed prefix, then longest pattern — which is what lets a container concept (`changes/*`, `dir: true`) coexist with a nested one (`changes/archive/*`). A file no concept matches never enters the parsed model. Root PATHS must not overlap (equal or nested roots are a load error naming both ids): a file is addressed by its workspace path but owned through its root, and two roots over one subtree would walk one file twice and show one document under two owners.

### Detection (pure)

The caller collects the evidence — the installed skill catalog with its discovery tier, and the declared marker/root paths that exist — and `DetectInstalled` answers two questions:

```
visibility  := every all_of skill installed ∧ (any_of absent ∨ ≥1 any_of installed)
default     := the first recognised framework, ordered by
                 1. tree present (a declared root or marker exists)
                 2. strongest skill tier (project > c0wrk global > user)
                 3. catalog order (priority, then id)
```

Markers and roots are the TREE signal only: they never make a framework visible, because an installed framework with no tree yet is exactly the state the empty state and the bootstrap action exist for, and a foreign repository's tree would otherwise summon a panel for a framework nobody installed. Installed names are compared through the alias prefixes (`spec-` ↔ `vibespec-`), so an upstream copy of c0wrk's skills names the same skill. An unattributed skill scope ranks last.

### Checks

| Kind | Declares | Asserts |
| ---- | -------- | ------- |
| `required_files` | `files:` (relative to the root), optionally scoped by `concept`, or `from: key_files` / a directive scoped to `concept` when set, or `from: files` + a DIRECTORY `concept` | declared `files` exist as regular FILES (a directory standing in for `index.md` is a finding), while extracted `from:` entries are asserted for EXISTENCE only — a document's own `## Key Files` list legitimately names a directory; with a `concept`, the files resolve next to each instance (in it for a directory, beside it for a document) and extracted paths are checked only for that concept's documents |
| `name_pattern` | `concept`, `pattern` (RE2), `allow` (bare names) | each instance's base name matches the pattern, minus the exemptions |
| `crossrefs` | optional `concept` | every relative markdown link (and anchor) extracted from the covered instances resolves inside the tree |
| `index_completeness` | `concept` (the index), `covers` (concept kinds) | the index document names every instance of each covered kind |
| `frontmatter_keys` | `concept`, `keys` | each instance's frontmatter carries every key |
| `content_regex` | `concept`, `regex`, `in` (`first_line`/`first_heading`/`body`/`frontmatter`/`prose`), optional `capture` + `equals: filename_number`, optional `optional: true` (requires `equals`) | the regex matches the selected text (`prose` = the markdown body minus fenced examples, so a format sample never satisfies a structural check); `optional` turns a non-match into a non-finding and compares the capture only when present (an ADR heading that spells no number cannot mismatch one) — it relaxes THAT comparison, so it is a load error without `equals`; a capture may be compared with the instance's own file-name number NUMERICALLY (043 = 43) |
| `unique` | `concept`, `field` (one of the scalar directives `number`, `title`, `purpose`, `status`, `superseded_by`) | the extracted value is unique across the concept's instances |
| `references` | `concept`, `field` (one of the scalar directives `number`, `title`, `purpose`, `status`, `superseded_by`), `of` (a concept kind), optional `acyclic`, optional `cycle_finding` | each extracted reference resolves to an instance of `of`, and — with `acyclic` — the relation forms no cycle, reported under `cycle_finding` (defaulting to `finding`). `acyclic` ADDS the cycle report; it never silences the missing-target one. Identities resolve by path with or without the `.md`, root-relative path, bare name, slug and unpadded number |

Every check declares a stable upper-case `finding` key (unique within the document), an optional operator-facing `message`, and a `severity`. Parameters belonging to another kind are a load error, so a document cannot smuggle a meaningless field past the loader. The loader also enforces the check/concept SHAPE: every content-bearing check runs over a concept's FILE instances, so naming a directory concept for `crossrefs`, `index_completeness`'s index, `frontmatter_keys`, `content_regex`, `unique` or `references` is a load error (it could never see an instance), while `required_files {from: files}` demands the opposite — a directory concept whose companion files it asserts. Findings are recomputed on every parse and never persisted; a finding exists ONLY because a declared check failed — the parser invents none, with one declared exception: the counted «`N` further findings suppressed» summary that the retention cap appends when it drops findings. The concept-level shape is enforced too: `extract` and `required` are per-DOCUMENT work (a directory instance never becomes one), so declaring either on a `match.dir` concept is a load error rather than a permanently inert declaration.

### Actions and views

An action is a gesture: `kind` (unique slug), `label`, optional `skill` (slug), optional `prompt` (may carry `{path}`/`{name}`), optional `concept` (renders on that concept's instances) and optional `requires`. Beyond `root`/`findings`/`concept:<kind>`, a creation step gates on `companion:<kind>` (the target directory already holds an instance of the kind — a feature with its `plan.md` in place) and `absent:<kind>` (it does not yet): the backend evaluates them TREE-level (an output artifact is missing from at least one holder), and refines them PER NODE for the panel — a node's artifacts are the instances beside it in its own directory, or below it when the holder is a directory instance — reporting the nodes where the refinement fails as the action's `unavailable_at`, so a Plan button stays live on the feature directories that still lack a plan and renders disabled on the ones that have one. `absent:<kind>` compares the `<kind>` count against the action's holder, so that action MUST declare `concept` — declaring `absent:` without it is a load error, because the executor would fail the gate closed and the gesture could never become available. The backend evaluates every declared requirement against the parsed tree AND resolves the declared `skill` against the installed catalog: `available` is the conjunction of both — the one flag a surface dispatches on — while `skill_available` reports the skill half on its own. The two are separate fields because a framework's declared skill set may be installed only partially (detection matches `all_of` plus one name of `any_of`), so a gesture can be blocked by a missing skill while its tree is complete, and the reason a surface shows must name THAT cause instead of the tree: `available: false` with `skill_available: false` means "install the skill"; `available: false` with `skill_available: true` means "the tree's requirements are unmet". The frontend renders an unmatched action inert with that reason rather than hiding it, and an action with no `skill` is never turned into a dispatch — every surface (badge, finding card, quick row, node row) shares one dispatchable predicate, so no surface can look live while another correctly refuses. A view section is a `label` + `kind` + an ordered `concepts` list; at most ONE non-tree section is accepted (two would print the same findings twice with no attribution), and each tree section additionally carries its own node MEMBERSHIP (its concepts' instances plus their ancestor folders) so two sections sharing an ancestor folder render their own subtrees, not each other's concepts. A framework that declares no sections gets one implicit tree over every concept.

### Parsing a tree (the only filesystem entry point)

`sdd.ParseTree(workspaceRoot, fw)`:

1. resolves each declared root inside the workspace — a root that escapes (absolute, `..`, `~`, or a symlink out) is skipped with a warning and reported `Present=false`; a workspace-INTERNAL symlink root is walked through its physical target (WalkDir would otherwise skip the whole root) while instance paths keep the declared namespace; an empty workspace root is a hard error, never the process CWD;
2. walks each present root, classifies entries against the concepts (honoring `exclude`), and skips anything matching no concept; the walk is bounded by a WHOLE-TREE budget (64 MiB of content / 2000 documents) — a document past it is excluded whole and the skip is named in warnings once;
3. parses each instance for exactly the metadata its concept declares (a concept that extracts no headings keeps no headings even in a heading-heavy file); document text is normalized once at read (BOM stripped, CRLF folded to LF, so line-endings never defeat a `$`-anchored regex or hide frontmatter), frontmatter is parsed as YAML and never enters the prose a heading/link scan sees, and fenced blocks follow CommonMark (a ``` inside a ```` example stays inside the example);
4. builds the flat forest the tree views index into (folders, documents, directory instances — labeled by each document's own display title, not the concept's generic label) and renders the declared views;
5. runs the declared checks; cross-reference anchors are judged only where the model can decide (a target with no headings at all, a `#L12` line anchor and a bare `#` fragment are never findings; setext headings and duplicate-suffix slugs resolve);
6. returns the model with `Present`, per-root state, documents, nodes, views, findings, counts and warnings.

`Present` means at least one declared root exists AND every root the framework marked `required` exists — a missing required root is a warning and the not-present state (the empty state's reason), not a rendered empty tree. Reading is tolerant and strictly read-only: a missing root is the neutral state, an unreadable or oversized (8 MiB cap) document is skipped, an escaping entry is excluded — none of these is an error. Parsing content is pure string work, so every extraction is unit-testable without a filesystem, and the model carries no environment metadata (git-derived dates are added by the backend DTO layer, never by this package).

## Flow

```
  Load(builtin, global, project YAML) ── ParseDefinitions + Merge (by ID,
        whole-document replace, fail-soft per document) ──► catalog []Definition
        │
        ▼
  DetectInstalled(evidence)      I/O-free: the caller supplies installed-skill
        │                        and existing-marker/root evidence; skills alone
        │                        decide VISIBILITY, tree evidence orders the
        │                        DEFAULT selection and feeds the empty state
        ▼
  selected Definition ── ParseTree(workspaceRoot, fw)   (the ONLY filesystem entry)
        │                  resolve roots → walk/classify → extract declared
        │                  metadata → nodes + views → declared checks → warnings
        ▼
  Tree{Present, Roots, Documents, Nodes, Views, Findings, Counts, Warnings}
        │  backend DTO layer: per-action `available` / `skill_available` + resolved skill names,
        │  git-derived dates; the panel renders sections/actions/findings
        ▼
  Specs panel
```

Everything before `ParseTree` is pure catalog and evidence work; `ParseTree` is the single filesystem entry point, and every stage after it is pure string work over what it read. The [Behavior](#behavior) subsections describe each stage in detail.

## Error Handling

- **Document faults are data.** `Parse` returns a `*sdd.Error` carrying the document label, the field path (`checks[2].pattern`) and the problem; a document with several faults reports them together. `ParseDefinitions` converts each fault into a warning and keeps the documents around it, so one broken operator document never blinds the surface.
- **A broken embedded document degrades loudly.** A built-in that fails to parse empties the built-in set behind one warning while the operator files still apply; `core/sdd/builtin_test.go` pins the embedded catalog, so the defect is caught at build time rather than by an operator.
- **Unreadable/absent files are neutral.** `LoadDefinitions` maps `os.ErrNotExist` to "no definitions, no warning" and every other read error to a warning; the load never fails.
- **Tree reads never error.** `ParseTree` fails only when the workspace path itself cannot be resolved; every other fault (missing root, unreadable or oversized document, escaping symlink) degrades to warnings or a neutral state.
- **Unknown requirements fail closed.** A `requires` token the backend cannot evaluate makes the action unavailable, and an unknown vocabulary member fails the load — a declaration c0wrk cannot honour is never reported as satisfied.
- **Encoding failures are named, not guessed.** A detection or parse diagnostic travels to the panel as an operator-facing string (the status payload's `warnings`), never as a thrown RPC error.

## Security Model

The project-scoped operator file (`<checkout>/.c0wrk/specs.yaml`) arrives with a clone, so it is UNTRUSTED-adjacent input: it is repository data under someone else's control. Its authority is bounded:

- **Strict validation.** Unknown keys, schemas, kinds, directives, parameters and malformed regexes are load errors — the DSL cannot carry anything the code does not model, and no field is executed.
- **No read outside the workspace.** Every root must be a clean workspace-relative path (absolute paths, `..` and `~` are rejected at load), every read is containment-checked against the workspace, and an entry whose real path escapes (a symlink out of the tree) is excluded. Detection markers are clean workspace-relative paths under the same rule (rejected at load otherwise), and they are only stat-ed — existence, never content.
- **No execution.** A document contributes labels, globs, regexes and prompts; nothing in it is run. A declared action dispatches its declared skill into the chat on a user click, which becomes an ordinary user-originated task subject to the full policy/judge/confirmation pipeline; an agent-authored DSL file therefore cannot arm a gesture that fires without the operator clicking it.
- **Bounded work.** Reads are capped per document (8 MiB) AND per parse (64 MiB of content / 2000 documents — a hostile clone cannot turn a panel refresh into a multi-hundred-MiB retention spike or an unbounded walk); the walk itself is separately capped at 50 000 visited entries, so directories, unreadable files and non-matching siblings — work that never reaches the content budget — are bounded too, and the walker prunes `.git` and `node_modules`, which never hold a project's own specifications; retained diagnostics are capped at 20 lines plus one counted summary, and RETAINED FINDINGS at 500 plus one counted summary (a reference-dense document packs a failed check per few dozen bytes, so the findings are the collection most able to outgrow every other budget); cross-reference RESOLUTION is separately capped at 20 000 references, because each one costs a stat before any finding is recorded; glob matching is memoized (repeated `**` cannot backtrack combinatorially); only declared concepts, checks and views are evaluated.
- **Fail-soft, never fail-open.** A document that fails validation is skipped with a visible warning; the built-ins still apply and the panel renders the reduced catalog rather than silently trusting the broken one.

## Invariants

- A framework is expressible entirely in `sdd-framework/v1`; the built-ins are embedded documents parsed by the same code, so no framework has a privileged representation.
- Every vocabulary (extraction directives, requirement tokens, check kinds, severities, section kinds, `requires` tokens) is closed; an unknown member is a load error and a parameter of another check kind is never accepted.
- The merged catalog is ordered by `priority` then `id`; merging is by ID and whole-document, adds or replaces only, and never removes a built-in.
- Loading is fail-soft per document and strictly read-only: no operator file is created, and a broken document degrades to a warning beside the definitions that parsed.
- Visibility is decided by installed-skill evidence alone; markers and roots order the default selection and feed the empty state, never visibility. A definition declaring neither `all_of` nor `any_of` is therefore a load error — it could never be recognised, so accepting it would be a trap.
- `ParseTree` reads only declared paths inside the workspace, extracts only declared metadata, and emits findings only for declared checks.
- The parsed model carries no environment metadata; git-derived dates are a backend enrichment of the wire payload.

## Configuration

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| Global definitions file | `~/.c0wrk/specs.yaml` (`config.SDDPath`) | Operator-owned YAML stream, optional, never created; merged after the built-ins |
| Project definitions file | `<checkout>/.c0wrk/specs.yaml` (`config.ProjectSDDPath`, `config.ProjectMetaDirName`) | Operator-owned YAML stream travelling with the repository, optional, never created; merged last, so its IDs win |
| Embedded catalog | `core/sdd/frameworks/{sdd,openspec,spec-kit}.yaml` | Compile-time pins; an added `.yaml` here is a build-time catalog change |
| Document size cap | 8 MiB (`maxDocumentBytes`) | An oversized document is skipped with a warning |

There is no config.yaml key for the DSL: the framework definitions ARE the configuration surface, and the only settings-side switch on this path is the sidebar gate `specs.enabled` (`specs/domains/specs.md`).

## Extension Points

- **Add or override a framework** by dropping a complete `sdd-framework/v1` document into `~/.c0wrk/specs.yaml` (all projects) or `<checkout>/.c0wrk/specs.yaml` (one repository). To override a built-in, copy its embedded document and edit it out of the box — the ID decides which definition wins.
- **Add a built-in framework** by adding `core/sdd/frameworks/<id>.yaml`; `BuiltinNames` picks it up through the embed glob, and `core/sdd/builtin_test.go` pins the catalog's validity.
- **Add a check kind** by extending `CheckKinds`, `checkKindFields`, the validator and `parse.go`'s runner in one change — a new kind is a reviewed code change (the reason the vocabulary is closed).
- **Add an extraction directive** the same way (a directive, its parser, and the concept/check validators that name it).
- **Add a gesture** by declaring an action with a `skill` — no frontend change: the panel renders whatever the active framework declares.

## Related Specs

- [specs.md](specs.md) - the Specs subsystem that consumes this DSL: detection tiers, active-framework selection, the panel, the sidebar gate.
- [../decisions/126-sdd-framework-dsl.md](../decisions/126-sdd-framework-dsl.md) - why the surface became framework-aware and why c0wrk stopped shipping a skillset.
- [../architecture/security-model.md](../architecture/security-model.md) - the untrusted-input and policy model a declared action's dispatch lands in.
