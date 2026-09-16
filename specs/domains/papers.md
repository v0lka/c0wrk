# Papers (Literature Library)

## Purpose

The paper ("literature") library is a per-project, workspace-contained store of studied-paper artifacts produced by the bundled `study-paper` skill: a per-paper card (`paper.md`), an evidence note (`note.md`), a critical-appraisal sheet (`appraisal.md`), a spaced-repetition deck (`flashcards.md`), and a literature-neighbourhood graph (`literature.json`). `core/papers` parses them into an in-memory model, the backend exposes read/pin/review/lookup RPCs, and the frontend renders the Papers panel and the per-paper workspace. The library is a global sibling of the research projects and is maintained independently of the RESEARCH toggle (hybrid mode).

## Key Files

- `core/papers/model.go` - domain types plus pure derived logic (ID/slug normalization, library lookup/indexing). No I/O.
- `core/papers/parser.go` - front-matter and Markdown-table parsing; the thin filesystem orchestrators `ParsePaperDir`/`ParseLibraryDir`. Parsing is best-effort (every artifact is optional).
- `core/papers/writer.go` - atomic (temp file + rename) persistence of a paper's artifacts, containment-checked against the library root (`resolveTargetWithinRoot` + `pathutil.IsWithinPath`).
- `core/papers/flashcards.go` - the `flashcards.md` model, parser, renderer, and the fixed-interval scheduler. Pure, no I/O.
- `core/papers/skillpack.go` - the embedded `study-paper` pack and the non-destructive, versioned GLOBAL seeding used at startup (hybrid).
- `core/papers/seedstaging.go` - the crash-safe staging core; a deliberate mirror of `core/research/seedstaging.go` (the two copies must be kept in sync).
- `core/papers/skills/study-paper/` - the embedded skill: `SKILL.md` plus `references/`, `assets/` (`flashcards.md`, `comparison-matrix.md`, `note-template.md`, `appraisal-template.md`, `explainer-outline.md`), and `scripts/` (`literature.py`, `fetch_paper.py`).
- `backend/frontend_api_papers.go` - `GetPapers`/`GetPaper`/`SetPaperPinned`/`RecordFlashcardReview`, DTO mapping/normalization, the paper-library watcher callback, and pin/unpin.
- `backend/frontend_api_papers_literature.go` - `RunPaperLiterature` and its testable `runLiteratureHelper` branch (exit-code → status mapping).
- `backend/frontend_api_skills.go` - `seedPapersSkillPack(agentDir)` (the startup global seed; a no-op on an empty agent dir) and the skill cache.
- `backend/frontend_api.go` - calls `seedPapersSkillPack(cfg.AgentDir)` before the global skill watchers start.
- `backend/frontend_api_project.go` - the workspace-watcher integration that emits `papers:changed` for the paper library.
- `backend/config/paths.go` - the path helpers `PaperLibraryPathIn`/`PaperLibraryPath` and `ComparisonDirName`/`ComparisonsPathIn`/`ComparisonsPath`.
- `frontend/src/api/papers.ts` - the RPC wrappers plus the boundary normalizers (`normalizePaperLibrary`/`normalizePaperRecord`/`normalizePaperLiteratureResult`).
- `frontend/src/stores/paperStore.ts` - the paper-library state and its sync functions (see [frontend/stores.md](frontend/stores.md)).
- `frontend/src/hooks/usePapersEvents.ts` - loads the active project's library and refetches it on `papers:changed`.
- `frontend/src/components/papers/` - `PapersView` (the panel segment), `PaperWorkspace` (the per-paper viewer tab) and its sections (`PaperOverview`, `PaperMarkdownSection`, `PaperSourceView`, `PaperAnchorList`, `CriticalLayerWidgets`, `CompareMatrix`, `FlashcardsReview`, `PaperLiterature`/`LiteratureGraph`), `paperSections.ts` (the section catalog), `paperActions.ts` (the dispatch-prompt audit module), `useComparisons`/`usePaperArtifacts`/`usePaperLiterature`.
- `frontend/src/components/research/PriorArtRow.tsx` - the research dashboard's prior-art row (Open + Deep read).
- `frontend/src/lib/flashcards.ts`, `spacedRepetition.ts` - the frontend flashcards parser and schedule, mirroring `core/papers`.
- `frontend/src/lib/paperComparison.ts`, `paperAnchors.ts`, `paperWidgets.ts`, `literatureGraph.ts` - pure frontend parsing/render helpers.

## Artifact Layout

```
<research-root>/
  papers/
    <slug>/
      paper.md          front-matter card (id, title, authors, year, venue,
                        identifiers, mode, reading, verdict, confidence, research_ids, anchors)
      note.md           claims / red flags / uncertainties tables
      appraisal.md      verdict + confidence sheet
      flashcards.md     card table + append-only review log   (study-paper Teach mode)
      source.md         (optional) extracted source text       (PDF → Markdown extraction)
      literature.md     (optional) literature-context note     (predecessors / citing / contradictions)
      comparison.md     (optional) per-paper comparison note   (single-paper form; comparison-matrix.md also accepted)
      literature.json   predecessor / citing / contradiction graph (literature.py output)
  comparisons/
    <slug>.md           multi-paper comparison (study-paper Compare intent)
```

`<research-root>` is the persisted `ProjectInfo.ResearchRoot` when RESEARCH is enabled, otherwise the default `<workspace>/.research`. `papers/` and `comparisons/` are direct children of the root — global siblings of the `R-NNN-<slug>/` research projects, owned by no single project. `paper.md`, `note.md`, and `appraisal.md` are the `PaperArtifacts` set (the front matter card and the two Markdown sheets the writer owns); `flashcards.md`, `literature.json`, and the OPTIONAL per-paper artifacts `source.md` (the extracted source text), `literature.md` (the literature-context note) and `comparison.md` (a single-paper comparison) are additionally produced by the skill/helper and are NOT part of the backend `PaperArtifacts`.

## Core Types

```go
// Model (core/papers/model.go)
type Identifier struct { Scheme, Value string }
type Anchor     struct { Label, Ref, Note string }
type Claim      struct { Claim, Evidence, Location, Stance string }
type RedFlag    struct { Flag, Detail, Severity string }
type Uncertainty struct { Item, Detail string }

type Mode       string // skim | deep | survey | review | implement | teach
type Reading    string // full | selective | skip (the reading DECISION)
type Verdict    string // accepted | rejected | uncertain (the SOUNDNESS judgement)
type Confidence string // low | medium | high

type PaperRecord struct {
    ID, Slug, Title string
    Authors []string
    Year    int
    Venue   string
    Identifiers []Identifier
    Mode        Mode
    Reading     Reading
    Verdict     Verdict
    Confidence  Confidence
    ResearchIDs []string // H-NNN hypotheses this paper informs
    Anchors     []Anchor
    Claims        []Claim
    RedFlags      []RedFlag
    Uncertainties []Uncertainty
    Dir string // on-disk dir (json:"-")
}

type PaperLibrary struct {
    Root   string
    Papers []*PaperRecord
}

// Flashcards (core/papers/flashcards.go)
type FlashcardStage string // new | learning | review
type Grade          string // again | hard | good | easy
type Card        struct { ID, Front, Back, Anchor, Tag string; Stage FlashcardStage }
type ReviewEntry struct { ID, Date string; Grade Grade; NextDue string }
type Deck        struct { Cards []Card; Reviews []ReviewEntry }
```

`NormalizePaperID` canonicalizes a paper id to `P-NNN` (accepting `P001`/`P-1`/`p1` spellings); `NormalizeResearchID` does the same for `H-NNN`. `Slugify` folds a title/id into a filesystem-safe slug, and `ValidSlug` is the fail-closed security check (rejects `.`/`..`, separators, hidden names, control/space characters, and requires an alphanumeric). `PaperRecord.ResolvedSlug` prefers an explicit valid slug, else a title-derived one, else an id-derived one. `PaperLibrary.Get` resolves a key by exact id → exact slug → normalized `P-NNN` → slugified key, so an exact id wins over a slug collision. `PaperLibrary.ByResearchID` selects the papers linked to an `H-NNN` (normalized, so `h1` and `H-001` agree). `NormalizeMode`/`NormalizeReading`/`NormalizeVerdict` canonicalize their tokens and pass an unrecognized value through verbatim (never dropping it): `NormalizeReading` folds the prose forms ("read in full", "read selectively") onto `full`/`selective`/`skip` and the "no decision" spellings (`n/a`/`none`/`-`/empty) to `""`, while `NormalizeVerdict` folds the appraisal-template synonyms (`weak accept` → `accepted`, `weak reject` → `rejected`, `borderline` → `uncertain`).

## Flow

```
App startup
  -> seedPapersSkillPack(cfg.AgentDir) writes the embedded study-paper pack into
     the GLOBAL skills dir (~/.c0wrk/.agents/skills) — hybrid, independent of
     RESEARCH; content-hash classified, staged + atomically swapped, non-destructive
  -> global skill watchers start, so the seeded skill enters the ListSkills catalog

User opens the Papers panel (or the paper workspace tab)
  -> usePapersEvents loads the active project's library (GetPapers)
  -> paperStore publishes the normalized library + an id-keyed record index

Paper artifact change (card/note/appraisal/flashcards written or edited)
  -> workspace watcher callback (frontend_api_project.go) batches changed paths
  -> emit papers:changed {project_id, paths}
     (fires INDEPENDENTLY of the RESEARCH toggle; BOTH the papers/ library and the
      sibling comparisons/ dir are watched in hybrid mode — a comparison write
      bumps the same event, refreshing the Compare section)
  -> paperStore.applyPapersChanged refetches the loaded project's library

Flashcard review (the workspace Flashcards section)
  -> RecordFlashcardReview(projectID, paperID, cardID, grade)
  -> core/papers appends the self-grade to flashcards.md and advances the card's
     stage (atomic, containment-checked) under the per-research-root mutation mutex
  -> the write lands in the watched library, so the watcher emits papers:changed

Literature lookup ("Run lookup" in the Literature section)
  -> RunPaperLiterature(projectID, paperID) resolves the seed from the card's
     identifiers (DOI -> arXiv -> any id -> title) and runs the seeded
     literature.py through c0wrk's managed Python
  -> writes <paper-dir>/literature.json and returns an explicit status
  -> the frontend reads literature.json and projects it into the research DAG
     canvas (predecessors -> parents, citing -> children, contradictions retagged)
```

## Invariants

- Papers are available only for real projects: `loadProjectForResearch` rejects the No Project pseudo-project, and every RPC enforces workspace containment on the research root and on the paper library (`config.IsWithinPath`, defense in depth).
- The library location is `<research-root>/papers` with `<research-root>` = the persisted `ProjectInfo.ResearchRoot` when RESEARCH is enabled, else `<workspace>/.research`. The library is a global sibling of the `R-NNN-<slug>/` projects and belongs to no single one.
- The library is maintained and watched independently of the RESEARCH toggle (hybrid): it is readable and watched even when RESEARCH is off, and a paper edit emits `papers:changed` regardless of any active `R-NNN`.
- Parsing is best-effort and total: every artifact is optional, so a partially written or hand-edited paper yields a usable record; an empty library and a zero-value `PaperRecord` are valid partial states. Missing optional artifacts never invalidate other parseable papers.
- Records and libraries are deterministically ordered: papers by numeric id then slug (`P-2` sorts before `P-10`), so a parse is stable regardless of directory-read order.
- The card carries two orthogonal signals that MUST NOT be conflated: the **reading decision** (`Reading` — `full`/`selective`/`skip`, written as `reading:` on `paper.md`, and the ONLY source for it) and the **soundness verdict** (`Verdict` — `accepted`/`rejected`/`uncertain`, where the appraisal sheet wins over the front matter and the appraisal-template synonyms fold). `Mode` spans both the engagement depths (`skim`/`deep`/`survey`) and the study-paper skill's own depths (`review`/`implement`/`teach`) on one shallow → deep axis; the frontend `PaperMode` union and the "go deeper" ladder key off the same vocabulary, so a card written with either token renders its badge and advances the ladder.
- Every DTO collection is non-nil (empty slice/map rather than `null`), so the frontend boundary guard is a pure pass-through.
- Pins reuse the `ProjectInfo.ResearchPins.Papers` container and store research-root-relative, forward-slash document paths (`papers/<slug>/paper.md`). Pinning requires the card file to exist; unpinning tolerates a deleted card, so stale pins stay removable. `SetPaperPinned` is idempotent in both directions and emits no event (the caller's resolved promise is its refresh signal). `SetPaperPinned` serializes on the per-research-root mutation mutex shared with the research pin RPCs and `Enable`/`DisableResearch`.
- Paper writes are atomic (temp file + rename) and containment-checked `core/papers`: every target is symlink-resolved and re-checked against the library root before anything is staged, so a rejected write (a symlinked paper directory, an escaped target) leaves every file byte-for-byte unchanged and leaks no temp file.
- `RecordFlashcardReview` is fail-closed: an unknown paper/card, an unknown grade, or a deck with no review-log table is rejected and leaves the deck byte-for-byte unchanged; the resolve→read→mutate→write chain runs under the per-research-root mutation mutex, so concurrent reviews cannot lose a row.
- The flashcards schedule is fixed and shared: the interval ladder is 1 → 3 → 7 → 16 → 35 days, `again` resets to the first interval, `hard` repeats at the same interval, and `good`/`easy` both promote. A card's stage is DERIVED from its interval index (`new` = -1 → `learning` = 0 → `review` ≥ 1), never stored independently, so the two never drift. `core/papers` and `frontend/src/lib/flashcards.ts` + `spacedRepetition.ts` implement the same rules.
- `RunPaperLiterature` degrades explicitly: every non-success outcome is a status (`ok` | `offline` | `unresolved` | `rate_limited` | `no_python` | `no_script` | `no_seed` | `error`) that the UI renders as a message, never an empty graph. Only an unknown paper or a containment violation is a Go `error`. `literature.json` is helper OUTPUT and is not produced by the backend writer.
- Global skill-pack seeding is hybrid, idempotent, crash-safe, and non-destructive to user-authored or user-edited skills: each destination is classified by CONTENT HASH against the embedded pack (never mtime/size, never the marker alone) — a pack-equal tree is Current, a pack-marked truncated subset is repaired, a diverging pack-marked tree is preserved as Modified, and a marker-less diverging directory is user-owned and preserved. Writes are staged in a hidden sibling temp dir and swapped in with a single rename. Seeding never runs for an empty agent dir, and a seeding failure is logged but never fatal to startup.
- The paper pack's seed version (`papers.CurrentSeedVersion`) is independent of the research pack's (`research.CurrentSeedVersion`), so the two packs bump separately.

## Configuration

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| Library location | `<research-root>/papers` | Global sibling of the `R-NNN-*` projects; `<research-root>` is the persisted `ProjectInfo.ResearchRoot` or `<workspace>/.research` |
| Comparisons location | `<research-root>/comparisons` | One `<slug>.md` per comparison set (`config.ComparisonDirName`) |
| Global skill seed dir | `~/.c0wrk/.agents/skills` (`config.SkillsDir(agentDir)`) | Destination of the startup paper-pack seed; one of `config.defaultSkillDirs` |
| `papers.CurrentSeedVersion` | `3` | Pack version stamped into each seeded skill's `.seed-version` marker; bump to refresh marked copies |
| Literature run timeout | `90s` wall clock; `20s` per HTTP request | Bounds `RunPaperLiterature`; the helper owns its per-request timeout |
| Literature message cap | `600` runes | Truncation of the helper's stderr tail in `PaperLiteratureDTO.Message` |
| Flashcards interval ladder | `1, 3, 7, 16, 35` days | `papers.IntervalScheduleDays` (mirrored in `spacedRepetition.ts`) |

## Extension Points

- Add a paper field by extending `PaperRecord`, the front-matter parser/renderer, the DTO mapping (`toPaperDTO`) + `PaperDTO`, the frontend type guard, and the paper workspace together.
- Add a new artifact (e.g. a new Markdown sheet) by extending `ParsePaperDir`/`ParseLibraryDir` and the writer; preserve the best-effort partial-state contract, and add it to `PaperArtifacts` only if the backend writer owns it.
- Update the bundled `study-paper` skill by editing `core/papers/skills/study-paper/` and incrementing `CurrentSeedVersion` when existing marked copies must refresh.
- Add a flashcards grade or interval by changing the shared ladder/rules in `core/papers/flashcards.go` AND `frontend/src/lib/spacedRepetition.ts` together (the two must agree), then extend the DTO/type guards.
- Add a literature-neighbourhood kind by extending `literature.py` output plus `frontend/src/lib/literatureGraph.ts`.
- Change RPC DTOs, watcher payloads, or events only with matching updates to the desktop/frontend and event contracts.

## Related Specs

- [research.md](research.md) - RESEARCH mode: the research root, the project-local research pack, and the panel whose Papers segment renders this library
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) - the Papers RPC surface and DTO boundary (incl. `RunPaperLiterature`/`PaperLiteratureDTO`)
- [../contracts/event-catalog.md](../contracts/event-catalog.md) - the `papers:changed` event
- [frontend/stores.md](frontend/stores.md) - `paperStore` and the `uiStore` Research-panel segment map
- [tool-manager.md](tool-manager.md) - the managed Python interpreter `RunPaperLiterature` uses
- [architecture/security-model.md](../architecture/security-model.md) - workspace containment and untrusted persisted artifacts
- [../decisions/050-papers-library.md](../decisions/050-papers-library.md) - why the study-paper skill is vendored, globally seeded (hybrid), and where the library lives
