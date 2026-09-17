# Code Review Report — Papers Library (`feat/study-paper`)

Scope: the local uncommitted change set on `feat/study-paper` — 36 modified tracked files plus 83 untracked new files (the `study-paper` / literature-library feature; the report itself is the 84th untracked entry, so `git ls-files --others` reports 84 while the directory-collapsed `git status` count of "35" understates the expanded set). Reviewed via `git diff HEAD` plus a full read of every untracked file. Severities in scope: **MUST FIX** and **SHOULD FIX** only (CONSIDER / nits excluded). Each finding carries exactly one severity tag and letter-labeled fix options.

Scope note: the finding set below was produced by the primary pass and then re-checked by a second, independent read of every changed and new file (Go backend, `core/papers`, the frontend components/store/libs, the vendored skill scripts, and the spec diffs). The second pass surfaced no new in-scope item beyond Issues 1–40 except four, which were recorded as **Issues 41–44** (`paperAnchors.ts`, `paperWidgets.ts`, `paperComparison.ts`). A later, fully independent pass over the same change set (`git diff HEAD` plus a full read of every untracked file) surfaced a further set of in-scope items the earlier passes missed; they are recorded as **Issues 45–58** (the vendored skill script `fetch_paper.py`, `core/papers/flashcards.go`/`writer.go`/`parser.go`, the `paperComparison.ts` / `paperWidgets.ts` section resolution, `backend/frontend_api_{papers,project,research}.go`, `SKILL.md`, `FileTreeContextMenu.tsx`, `PaperWorkspace.tsx`, `usePaperArtifacts.ts`). The previously-noted `literatureGraph.ts` cycle was re-examined and confirmed out of scope (CONSIDER) because `literatureGraph.test.ts` pins the node reuse as intended and `layoutDag` guards cycles with a `visited` set. Further independent passes (this revision) re-read the whole change set and surfaced additional in-scope items: the dead single-paper refresh chain (`refreshPaper`/`getPaper`/`normalizePaperRecord`), now folded into **Issue 12**'s enumeration; **Issues 69–70** (the `PapersView` dispatch input-loss and the uncapped OpenAlex `per-page` in `literature.py`); **Issues 71–75** (five `literature.py` script defects: the arXiv-DOI resolution gap, the contradicting exit-code docstring, the documented-but-unsupported URL seed form, the unescaped `title.search` filter value, and the arXiv request bypassing the retrying HTTP helper); and **Issues 76–92** (the frontend reading per-paper artifact filenames no producer writes, and vendored-helper defects: the `0600` output mode, the unreachable rate-limit exit path, the unanchored OpenAlex-id classifier, the under- and over-capturing `DOI_RE`, the substring `CONTRADICTION_MARKERS`, the string `year` from `crossref_references`, the dead `fetch_paper.py` "API error" arm, the exit-2 "network unavailable" mislabel on mixed failures, the quote-unaware `fetch_paper.py` tag/attr regexes, the missing Crossref politeness contact, the `arXiv:`-prefix vs old-style-id misclassification, and seven `fetch_paper.py` defects — four in the URL-branch/merge order (dropped page DOI, an unreachable title fallback, a mis-ranked abstract, a title-search guess overriding verified OA/counts data) and three more (the `strip_markup` unescape-before-strip corrupting escaped abstracts, an unresolved relative PDF link, and `_pick` treating a real `0` as unknown). A still later, seventh fully independent pass — over seven disjoint read-only slices (`core/papers` Go, backend Go, the vendored skill scripts, the frontend libs, the frontend store/api/hooks, the frontend UI components, and the specs + skill docs), each deduplicated against the 95 recorded issues — surfaced nine further in-scope items, recorded as **Issues 96–104** (a `core/papers/flashcards.go` reader/writer card-row asymmetry and a review-row column-collision that drops the grade; the two vendored-script stdout / `<meta charset>` encoding defects; the `paperAnchors` `algorithm` kind; the `paperStore.lastSyncAt` watchdog claim; the `fileViewerStore.test.ts` persistence-assertion gap; the Research segment control's ARIA roles; and the `note-template.md` Skim mapping). The backend Go slice was re-verified clean (no new item). Two further independent verification waves (the same seven slices, fresh readers) then surfaced eight more in-scope items, recorded as **Issues 105–112** (the Go↔TS flashcard column-matching divergence; the `paperAnchors` sub-number false jump; the backend hybrid-watch test that never drives production wiring; the paper-workspace section switcher's missing selected state; further dead documented frontend surface; and three `literature.py`/`fetch_paper.py` defects — an S2 merge without dedup, a no-metadata URL reported resolved, and an `is_oa` sourced only from Unpaywall). The `specs`/skill-docs slice and the frontend store/api/hooks slice were re-verified clean on those waves. All other candidates the fresh passes raised were already recorded.

Environment: Go 1.27.1, Node 26, React 19 / Zustand 5 / Vitest. `go build ./core/papers/... ./backend/...`, `go vet ./core/papers/...`, `go test ./core/papers/...`, and the in-scope `vitest` suites pass, so the findings below are behavioral, not compile-time.

---

## MUST FIX

### Issue 1 — MUST FIX — Grading a flashcard resets the in-progress review to card 1 (self-inflicted via the review's own write-back)

The review-reset effect keys on the raw artifact content and the component also writes that same content back to disk, so the write-back retriggers the reset.

**Location:** `frontend/src/components/papers/FlashcardsReview.tsx:82-87` (reset effect `useEffect(..., [artifact.content])`), interacting with the write-back at the `rate` callback (`void commitFlashcardReview(paperId, cardId, grade)`), and the wiring in `frontend/src/components/papers/PaperWorkspace.tsx` (`<FlashcardsReview … paperId={paper.id} />`).

**Why it is a problem:** The effect resets `index`/`flipped`/`results`/`finished` whenever `artifact.content` changes. The review writes its own state back: `commitFlashcardReview` → `RecordFlashcardReview` appends a row to `flashcards.md`, which lands inside the watched library, so the backend emits `papers:changed` (see `backend/events.go` `EventPapersChanged` and `backend/frontend_api_papers.go` `emitPapersChanged`). `usePapersEvents` refetches → `paperStore.loadLibrary` bumps `lastSyncAt` → `PaperWorkspace` passes that as the `refreshKey` to `usePaperArtifacts(dir, syncAt)` → `flashcards.md` is re-read with the new review-log row → `artifact.content` changes → this effect fires. Realistic trigger: open a paper's Flashcards section and rate one card of a multi-card deck; ~50–300 ms later the UI jumps back to "Card 1 / N" with `results`/`finished` cleared, so a persisted review never completes (even a single-card deck never durably shows "Review complete"). The surrounding comment ("A different artifact in the same tab starts a clean session") misdescribes the case: it is the **same** artifact being rewritten by the review. (The file header also states the review is "READ-ONLY here … nothing is written back to disk", which contradicts the `paperId` write-back path.)

**Suggested fix:**
a) Key the reset on paper identity, not content — pass a `resetKey` (e.g. `paper.id`/`paper.slug`) and make the effect depend on it (`useEffect(..., [resetKey])`), letting `deck` still re-derive from `artifact.content`.
b) Remount the section per paper from the parent (`key={paper.id}` / `key={paper.slug}` on `<FlashcardsReview>`) and drop the content-keyed effect entirely.

### Issue 2 — MUST FIX — In hybrid mode the papers writers serialize on a different mutex than `EnableResearch`, so a projects-row lost update is possible

The paper RPCs lock the per-research-root mutation mutex keyed by the **effective** root, but `EnableResearch` locks it keyed by the **raw** `ResearchRoot`, which is `""` while RESEARCH is off — so the two writers take different mutexes.

**Location:** `backend/frontend_api_papers.go:194` (`SetPaperPinned`), `backend/frontend_api_papers.go:265` (`RecordFlashcardReview`), `backend/frontend_api_papers_literature.go:121` (`RunPaperLiterature`) vs `backend/frontend_api_research.go:310` (`EnableResearch`), `backend/frontend_api_research.go:396` (`DisableResearch`). Helper: `backend/frontend_api_papers.go:331` `effectiveResearchRoot`.

**Why it is a problem:** `papersReadContextFor` sets `ctx.researchRoot = effectiveResearchRoot(proj)`, which is the default `<workspace>/.research` when RESEARCH is off, and each papers RPC locks `f.researchMutationMu(ctx.researchRoot)`. `EnableResearch` locks `f.researchMutationMu(proj.ResearchRoot)` — `""` when RESEARCH is off (its own comment says the key is "the root the row carries BEFORE this mutation"). `""` ≠ `"<ws>/.research"`, so the two writers run concurrently, and both call `SQLiteProjectStore.SaveProject`, which is a full-row upsert (`backend/project/persistence.go:143` rewrites `research_root` **and** `research_pins` from the passed snapshot). A papers save whose `fresh` snapshot still carries `ResearchRoot == ""` can land after `EnableResearch` persisted the new root, silently reverting the enable (RESEARCH off again after restart), or `EnableResearch`'s save can drop a concurrently committed pin. The `fresh.ResearchRoot != ctx.project.ResearchRoot` sentinel only catches a root change that landed **before** the reload, not this interleaving. Trigger: RESEARCH is off and the user pins a paper / grades a flashcard concurrently with enabling RESEARCH in the same window — exactly the hybrid scenario this feature exists for. The doc comment on `SetPaperPinned` explicitly claims this cannot happen ("serializes on the same per-research-root mutex as … Enable/DisableResearch … so a concurrent row save cannot clobber it").

**Suggested fix:**
a) Key the papers mutex on the pre-mutation raw root so it matches `Enable`/`DisableResearch`: `mu := f.researchMutationMu(ctx.project.ResearchRoot)` (the mutex + the existing sentinel then fully protect the row).
b) Or change `EnableResearch`/`DisableResearch` to key their mutex on the **effective** research root, so every writer of the projects row agrees; keep the sentinel check.

### Issue 13 — MUST FIX — The front-matter splitter never recognizes the `---` closing fence, so every card's body (and the documented title fallback) is lost

`fenceKind` classifies `---` as an opener only and `...` as the sole closer, but `frontMatter`'s scan loop accepts only the closer — so a `---`-delimited block never terminator-matches and the body is always empty.

**Location:** `core/papers/parser.go:44-58` (`frontMatter`) and `core/papers/parser.go:61-71` (`fenceKind`); the consumer `core/papers/parser.go:418-421` (`ParsePaperMD`: `rec.Title = firstHeading(body)`); the inverse writer `core/papers/writer.go:73-76` (`RenderPaperMD`).

**Why it is a problem:** `fenceKind` returns `fenceOpen` for a line that is `---` and `fenceClose` **only** for `...`. `frontMatter` opens on `fenceOpen` (line 47) but its loop accepts only `k == fenceClose` (line 51), so a `---` line is classified as `fenceOpen` and never terminates the block. Execution falls through to the unterminated-fence branch `return strings.Join(lines[1:], "\n"), "", true`, i.e. `body == ""` and `ok == true`. The function's own doc comment contradicts the code ("closer ("---" or "...")"). Verified empirically: `frontMatter("---\nid: P-001\ntitle: T\n---\n# Heading\nrest\n")` → `fm="id: P-001\ntitle: T\n---\n# Heading\nrest\n"`, `body=""`; `ParsePaperMD("---\nid: P-001\n---\n# Heading From Body\n").Title == ""`. The format that is actually used is `---`-delimited everywhere: the package writer emits exactly `---\n` + YAML + `---\n` (`writer.go:73-76`), the shipped skill's `SKILL.md` front matter uses it (lines 1/8), and `SKILL.md` documents `paper.md` as that shape. So `ParsePaperMD`'s documented body-heading fallback is unreachable for every front-matter card the writer produces; additionally, because `fm` folds the closing fence **and the entire body** into the "front matter" text, the folded text (closing fence + body) is handed to the YAML decode in `parseFrontMatter`, where `gopkg.in/yaml.v3` silently decodes only the first document — the fence and the entire body are ignored with no error (verified: `yaml.Unmarshal("id: P-002\n---\n# Body H\ntitle: body-title\n", &meta)` yields `Title == ""` and a nil error) — and the body-shaped lines actually reach the `parseFrontMatterFallback` line scan only when that first document is not valid YAML. The tests miss it — the only no-title fallback test (`parser_test.go`) uses a document with **no** front matter, and the truncated-fence test carries its title inside the front matter.

**Suggested fix:**
a) Make either delimiter terminate the block: in `frontMatter` accept any fence line, e.g. `if k := fenceKind(lines[i]); k != fenceNone { ... }` (keep `fenceOpen`/`fenceClose` for documentation), or compare against `fenceOpen || k == fenceClose`.
b) Or make the classification stateful — a `---` on a line after the opener closes the block — and keep `...` as an accepted closer.
c) Add a regression test: a `---`-delimited card with no `title:` and a body `# Heading` must yield `Title == "Heading"`, plus a `RenderPaperMD` → `ParsePaperMD` round-trip for a title-less record.

---

## SHOULD FIX

### Issue 3 — SHOULD FIX — `RunPaperLiterature` writes into a pre-lock–resolved directory without re-checking the research root under the mutex

**Location:** `backend/frontend_api_papers_literature.go:96-125` (`RunPaperLiterature`).

**Why it is a problem:** Unlike `SetPaperPinned`/`RecordFlashcardReview`, it resolves the record from a pre-lock `papersReadContextFor` and, after acquiring the mutex, writes directly into `rec.Dir` — it never re-loads the project row or checks `errResearchRootChanged`. If `EnableResearch`/`DisableResearch` commits a root change while this RPC waits on the mutex, the effective library moves (`<ws>/.research/papers` ↔ `<custom>/papers`) and the helper writes `literature.json` into the now-inactive directory, which the frontend (reading the active library) never surfaces. Trigger: click "Refresh" on a paper, then toggle RESEARCH before the helper starts. It is also an internal inconsistency — the other two writers both re-load under the lock precisely to avoid this.

**Suggested fix:**
a) Mirror the other two RPCs: after `mu.Lock()`, `fresh, err := f.loadProjectForResearch(projectID)`; return `errResearchRootChanged` if `fresh.ResearchRoot != rctx.project.ResearchRoot`; otherwise re-resolve the record from `rctx.libraryRoot` before writing.
b) Or re-resolve `rec` (and re-check containment) from `rctx.libraryRoot` under the lock after confirming the root is unchanged.

### Issue 4 — SHOULD FIX — The appraisal confidence key set does not match the bundled appraisal template, so a template-authored confidence is silently dropped

**Location:** `core/papers/parser.go:881` (`ParseAppraisal`, `case "confidence", "confidence level", "certainty":`) vs the bundled template `core/papers/skills/study-paper/assets/appraisal-template.md:100` (`- **Confidence in this verdict** (low / medium / high) — and why:`).

**Why it is a problem:** `ParseAppraisal` matches keys by exact equality after normalization. The template's label normalizes to `"confidence in this verdict"`, which matches none of the accepted keys, so `Confidence` stays `""` — even when the line is filled as `**Confidence in this verdict:** high`. The adjacent `**Verdict:**` line **does** match, so the asymmetry is easy to miss. Trigger: an `appraisal.md` written from the bundled template that records confidence only on that line (with `paper.md`'s `confidence:` omitted, or differing) parses with an empty Confidence, contradicting the spec ("the appraisal sheet records Verdict and Confidence", `specs/contracts/desktop-frontend.md` § Papers).

**Suggested fix:**
a) Extend the case to include the template's label: `case "confidence", "confidence level", "certainty", "confidence in this verdict":`.
b) Or change the bundled `appraisal-template.md` to the bare `**Confidence:**` label the parser already accepts (keeping the parser unchanged).

### Issue 5 — SHOULD FIX — A pin cannot be removed once the paper's directory is fully deleted

**Location:** `backend/frontend_api_papers.go:196-236` (`SetPaperPinned`; `rec := lib.Get(paperID)` at ~212-214 precedes the pin toggle).

**Why it is a problem:** `lib.Get` returns `nil` when the entire `papers/<slug>/` directory is gone (`ParseLibraryDir` skips directories carrying none of the three artifacts — `core/papers/parser.go:1037`), so `SetPaperPinned(…, false)` returns `paper %q not found in the library` before it can toggle the pin. There is no other RPC that removes a paper pin, so the persisted `ResearchPins.Papers` entry is orphaned forever. The doc comment covers only the card-deleted-but-directory-present case ("unpinning deliberately works for a paper whose card has since been deleted, so stale pins stay removable"). Trigger: delete a pinned paper's folder (e.g. via the file explorer), then try to unpin it.

**Suggested fix:**
a) For `pinned == false` with `rec == nil`, derive the candidate card path from the key (`path.Join("papers", papers.Slugify(paperID), papers.PaperFileName)`) and drop any matching pin instead of erroring.
b) Or make unpin tolerant of an unknown paper (remove any pin entry whose path segment matches the normalized key) and return `nil`.

### Issue 6 — SHOULD FIX — Every library sync blanks all paper sections and re-enters a full "Loading…" state, contradicting the hook's stated contract

**Location:** `frontend/src/components/papers/usePaperArtifacts.ts:95` (`setArtifacts(allOf(pending))` inside the effect at lines 89–129; `pending()` sets `content: ''`).

**Why it is a problem:** On every `refreshKey` change (any `papers:changed` — a paper write, a deepen, a flashcard grade) the effect sets **every** section to `pending()` before re-listing/reading, so Note/Appraisal/Compare/Source/Literature/Flashcards blank out. Two concrete symptoms: (1) the workspace flashes "Loading…" after any paper write; (2) during that window `artifacts.source.content === ''`, so clicking a resolved anchor in the Overview yields a false "не найдено" (`PaperWorkspace.onAnchorSelect` → `resolveAnchor('') === null`), and `FlashcardsReview` bounces to its markdown fallback. The hook's own test comment states the contract as "re-lists … while never dropping the sections already loaded" — the implementation does the opposite (the sibling `fileViewerStore.openVirtualFile` deliberately keeps existing content "to avoid a flash").

**Suggested fix:**
a) Keep the currently loaded artifacts in state and mark loading only on the **first** load for a given `dir`; on a `refreshKey` re-run keep the previous content and swap it in when the async read resolves.
b) If a refresh indicator is wanted, keep the last `PaperArtifacts` as-is and expose a separate `refreshing` boolean rather than overwriting each `content` with `''`.

### Issue 7 — SHOULD FIX — The "Compare selected" multi-select is not scoped to the project

**Location:** `frontend/src/components/papers/PapersView.tsx:250` (`const [selectedIds, setSelectedIds] = useState<string[]>([])`) and the cleanup effect at `:257-265`.

**Why it is a problem:** `selectedIds` is component state and the only cleanup drops ids no longer present in `papers`. Paper ids are per-project `P-NNN` and collide across projects — `paperStore.loadLibrary` itself resets its own project-scoped state because "paper ids/slugs collide across projects" (`frontend/src/stores/paperStore.ts:130-133`). `ResearchPanel` (and thus `PapersView`) is not keyed/remounted on project change (`frontend/src/components/layout/WorkspacePanel.tsx:152` renders `<ResearchPanel />`), so a selection made in project A survives a switch to project B. Trigger: tick two papers in project A, switch to project B whose library also has `P-001`/`P-002`; the count still shows "2 selected", Compare-selected stays enabled, and `compareSelected` builds a comparison prompt over project B's papers the user never selected.

**Suggested fix:**
a) Subscribe to the loaded library's project (`usePaperStore(selectPapersProjectId)`) and clear `selectedIds` in the existing effect whenever it differs from the previously seen project (track the last project in a ref).
b) Or reset selection on `activeProjectId` change (or give the panel `key={activeProjectId}`) so a project switch starts from a clean selection.

### Issue 8 — SHOULD FIX — A `GetPapers` in flight when the panel is reset still applies, repopulating the store with the previous project's papers

**Location:** `frontend/src/stores/paperStore.ts:356` (`let latestFetch`) and `:363-374` (`fetchPaperLibrary`); `reset` at `:214`; called from `frontend/src/hooks/usePapersEvents.ts:29-37`.

**Why it is a problem:** The only staleness guard is the `latestFetch` ticket, and it is bumped **only** inside `fetchPaperLibrary`. The No-Project branch of the hook calls `usePaperStore.getState().reset()`, which does **not** bump the ticket, so a slow fetch for the departed project resolves with `ticket === latestFetch` and calls `loadLibrary`, writing `projectId = <old project>` back into the store that was just cleared. Trigger: on project P1 (large library) the fetch starts; the user switches to "No Project" before it resolves → the global store holds P1's papers while the app is on No Project, and `papersByHypothesis` folds those foreign cards against whatever graph is loaded. Note `refreshPaper`/`togglePaperPin` do compare the live project (`after.projectId !== projectId`); `fetchPaperLibrary` does not.

**Suggested fix:**
a) Capture `projectId` and, after the `await`, bail unless the store still belongs to that project (add the same live-project check used by `refreshPaper`).
b) Make `reset()` invalidate pending fetches by bumping the ticket (expose an `invalidate()`/ticket bump) so a reset makes every in-flight fetch stale.

### Issue 9 — SHOULD FIX — The documented 50 ms debounce never applies to real `papers:changed` events

**Location:** `frontend/src/hooks/usePapersEvents.ts:44-61` (well-formed branch at `:56-59` calls `applyPapersChanged(data)` immediately; only the malformed fallback reaches `debouncedRefresh`).

**Why it is a problem:** The header claims the hook invalidates "on every `papers:changed` event (50ms debounce, mirroring `useResearchStatusEvents`)", and `applyPapersChanged`'s own doc says "The caller (an events hook) may debounce rapid bursts" — but the well-formed branch refetches immediately and `return`s. Trigger: the study-paper skill writes `paper.md`/`note.md`/`appraisal.md`/`flashcards.md` in a burst; the watcher emits one event per callback → N full `GetPapers` calls (each re-parsing every card on the backend) instead of one coalesced refetch. The ticket keeps it correct, but the coalescing the design promises is absent.

**Suggested fix:**
a) Route both branches through `debouncedRefresh()` (drop the immediate `applyPapersChanged`, or debounce it) so a burst collapses to one refetch.
b) Keep a leading-edge path but wrap it in a short throttle, and update the header to state the chosen behavior.

### Issue 10 — SHOULD FIX — `togglePaperPin` writes back a pre-await record snapshot, which can revert a newer record

**Location:** `frontend/src/stores/paperStore.ts:414` (`const record = state.records[paperId]`), `:418` (`await setPaperPinned(...)`), `:429` (`after.loadPaper({ ...record, pinned })`).

**Why it is a problem:** The record object is captured **before** the RPC and then spread into `loadPaper` after it, and `loadPaper` replaces the whole record (`records: { ...state.records, [record.id]: record }`). Any field refreshed meanwhile is lost. Trigger: pin a paper while the study-paper skill is appending to its card — a `papers:changed` refetch lands between the read and the write-back, and the fold-back reverts the card's fresh content until the next change.

**Suggested fix:**
a) Re-read after the await: `const current = usePaperStore.getState().records[paperId] ?? record; after.loadPaper({ ...current, pinned })`.
b) Or add a dedicated `setPinned(paperId, pinned)` action that mutates only the `pinned` flag instead of replacing the record.

### Issue 11 — SHOULD FIX — `togglePaperPin` leaves `isMutating` stuck `true` when the project changes mid-flight

**Location:** `frontend/src/stores/paperStore.ts:411-431` — early `return` at `:428` (`if (after.projectId !== projectId) return`) precedes the only `setMutating(false)` at `:430`.

**Why it is a problem:** `setMutating(true)` runs before the `await`; on success the function returns at `:428` when the active project changed, so `isMutating` is never cleared. The switched-to project's `loadLibrary` does not clear `isMutating` (only `setError`/`reset` do). Trigger: pin a paper, switch to another project before the RPC resolves → the flag stays `true`. It is currently latent (the consumer `selectPapersMutating` has no production callers — see Issue 12), but any future wiring of that flag would surface a permanently "mutating" panel.

**Suggested fix:**
a) Move `setMutating(false)` into a `finally` (or a `try/finally`) so it always clears.
b) Additionally clear `isMutating` inside `loadLibrary` (project-scoped state), so a project switch resets it.

### Issue 12 — SHOULD FIX — A large slice of `paperStore`'s exported surface has no production consumer (dead state + doc drift)

**Location:** `frontend/src/stores/paperStore.ts` — `paperTabPath` (`:39`), state `selectedPaperId` (`:69`), `mode` (`:71`), `isMutating` (`:75`), actions `selectPaper`/`setMode` (`:204-206`), selectors `selectSelectedPaperId` (`:235`), `selectPaperViewMode` (`:240`), `selectPapersMutating` (`:250`), `selectPinnedPaperPaths`, `selectPapersProjectId`, and hooks `usePaperViewMode` (`:290`), `useSelectedPaper` (`:303`), `usePinnedPapers` (`:321`), `useFilteredPapers` (`:334`), plus the whole single-paper refresh chain — `refreshPaper` (`:387`, the only caller of `getPaper`) → `frontend/src/api/papers.ts` `getPaper` (`:347`) → `normalizePaperRecord` (`:316`) — none of which has a production caller (only `paperStore.test.ts`/`papers.test.ts`), so the backend `GetPaper` RPC has zero frontend call sites and `loadPaper`'s append branch (`:196`) is unreachable in production.

**Why it is a problem:** A `grep` over `frontend/src` (excluding `*.test.*` and `paperStore.ts` itself) finds **no** production consumers for these symbols; the actual reader surface is the file-viewer tab (`openPaper`/`PAPER_TAB_PREFIX` → `PapersView`/`PaperWorkspace`/`FileViewerContent`). Because `mode`/`selectedPaperId`/`isMutating` are still updated on every load/reset and covered by tests, they look load-bearing — bugs in them (Issue 11) stay invisible, and `specs/domains/frontend/stores.md` documents them as the authoritative reader state, so the spec and the implementation diverge. This is dead, must-be-maintained state with an accompanying documentation drift. The refresh chain is the same class and is doubly misleading, because several findings below (Issues 8, 19, 20, 28, 62) cite `refreshPaper` as the live exemplar of a correct project-switch guard, and the `getPaper`/`refreshPaper` doc comments plus `stores.md` describe a "per-paper incremental refresh path" (and a pin whose "resolved promise is the refresh signal") that never runs.

**Suggested fix:**
a) Remove the unconsumed state/actions/selectors/hooks (and their tests), keeping only what `PapersView`/`PaperWorkspace`/`FileViewerContent` use, and trim the stores.md description accordingly.
b) Or, if they are intentionally reserved for a future reader, wire at least one consumer (or add an explicit TODO/issue reference) and align the stores.md wording with the shipped reader.

### Issue 14 — SHOULD FIX — The `P-`/`H-` id regexes are unanchored, so arbitrary slugs mint spurious canonical ids

`NormalizePaperID`/`NormalizeResearchID` match `<p|P>` + optional `-` + digits **anywhere** in the input, so a slug or directory base containing `p-<n>` is canonicalized into a real-looking id.

**Location:** `core/papers/model.go:219` (`paperIDRe = (?i)P-?(\d+)`), `core/papers/model.go:224` (`researchIDRe = (?i)H-?(\d+)`), used by `NormalizePaperID` (`:231-245`), `NormalizeResearchID` (`:247-259`), `paperIDNumber` (`:261-272`), and `parsePaperDir`'s fallback `rec.ID = NormalizePaperID(base)` (`core/papers/parser.go:1026`).

**Why it is a problem:** Both patterns are unanchored and applied with `FindStringSubmatch`, so a substring match suffices; the doc comments claim the functions accept whole-identifier spellings (`P001`, `P-001`, `P1`, `P-1`). Verified empirically: `NormalizePaperID("deep-2") == "P-002"`, `NormalizePaperID("group-2") == "P-002"`, `NormalizePaperID("clip-2") == "P-002"`, `NormalizePaperID("setup-1") == "P-001"`. A paper directory named `group-2` therefore becomes `P-002` (via the `parser.go:1026` fallback) and collides with a genuine `P-002` in the library map; `PaperLibrary.Get(key)` then resolves the wrong record through its normalized-id pass (`model.go:508`). Likewise a `research_ids` entry such as `path-1` normalizes to `H-001`, and `PaperLibrary.ByResearchID("path-1")` links unrelated papers. `model_test.go` pins only the clean spellings (`"P-001"`, `"nope"`, `"title-x"`), so the false-positive class is untested.

**Suggested fix:**
a) Anchor both patterns at both ends: `^(?i)P-?(\d+)$` / `^(?i)H-?(\d+)$` (`FindStringSubmatch` still works), so only a complete identifier normalizes.
b) Or require a word boundary (`(?i)\bP-?(\d+)\b`) and reject a match whose digit run is embedded inside a longer alphanumeric token.
c) Add tests pinning `NormalizePaperID("group-2") == ""`, `NormalizeResearchID("path-1") == ""`, and that `parsePaperDir` does not synthesize an id from a non-id directory base.

### Issue 15 — SHOULD FIX — The bundled appraisal template's placeholder `Verdict` line overwrites the card's valid verdict with a non-canonical value

`ParseAppraisal` takes the value after `Verdict:` verbatim, and the template's verdict bullet is the whole options list, which `NormalizeVerdict` passes through unchanged — so a template-authored `appraisal.md` wins over the card with garbage.

**Location:** `core/papers/parser.go:873-887` (`ParseAppraisal`), `core/papers/parser.go:955-976` (`ParsePaper`: "the appraisal sheet … wins over a conflicting value in the front matter"), and the trigger text `core/papers/skills/study-paper/assets/appraisal-template.md:98`.

**Why it is a problem:** The template's verdict bullet is `- **Verdict:** accept / weak accept / borderline / weak reject / reject — or, for`. `parseLineKeyValue`/`scanKeyValues` recover the value after the colon verbatim, so the parsed value is the entire option list. That string matches none of `NormalizeVerdict`'s synonyms (`model.go:177-197`), so it is returned unchanged (`default: return Verdict(s)`). Verified empirically: `ParseAppraisal("- **Verdict:** accept / weak accept / borderline / weak reject / reject — or, for\n- **Confidence in this verdict** (low / medium / high) — and why:\n")` → `verdict="accept / weak accept / borderline / weak reject / reject — or, for"`. `ParsePaper` then does `if v != "" { rec.Verdict = v }`, overwriting the card's correct `verdict:` value, and `RenderPaperMD` round-trips the garbage. `SKILL.md` instructs the agent to build `appraisal.md` from this very template and states the verdict is "kept in sync with `paper.md`", so the defect is reachable through the prescribed authoring path. (This is distinct from Issue 4, which is about the confidence **key** not matching; here the **key** matches but the **value** is the placeholder.)

**Suggested fix:**
a) Make the placeholder non-parseable as a value — render the options as prose/bullets with the `**Verdict:**` value cell left empty, or as a `Field | Value` table row.
b) Harden `NormalizeVerdict` to take the first recognizable token when the value contains a delimiter (`/`, `|`, `—`) instead of passing the whole phrase through.
c) In `ParsePaper`, only let the appraisal override the card when `NormalizeVerdict(appraisalValue)` resolves to a canonical constant; otherwise keep the card's verdict.

### Issue 16 — SHOULD FIX — `ParseNote` reads the Claim and Evidence columns from the same cell for the bundled note template, silently dropping the evidence

`parseClaims` resolves its columns with the non-exclusive `columnIndex`, and the template's §5 header contains both `claim` and `evidence`/`support` in one cell, so `Claim` and `Evidence` bind to the same column.

**Location:** `core/papers/parser.go:794-825` (`parseClaims`; `ic`/`ie` at `:799-800`), the greedy `columnIndex`/`colOr` (`parser.go:681-705`), and the trigger header `core/papers/skills/study-paper/assets/note-template.md:58`.

**Why it is a problem:** The template's §5 header is `| Contribution | Claim the evidence is meant to support | Evidence offered (experiment / result / proof) | **Anchor** … | … |`. `pickTable(tables, ["claim"], …)` selects it (cell 1 contains `claim`). Then `ic = colOr(header, 0, "claim","assertion","finding","statement")` → **cell 1**, and `ie = colOr(header, 1, "evidence","support","supporting")` → **also cell 1** (that same cell contains `evidence` and `support`), so the real evidence column (index 2) is never read. Verified empirically with the template header: a row `| C1 | the claim text | the ACTUAL evidence text | 3.2 | present | yes |` parses to `Claim="the claim text"`, `Evidence="the claim text"`, `Location="3.2"`, `Stance=""` — the evidence is lost. It compounds a contract mismatch: `SKILL.md:252` documents `note.md`'s claims table as `Claim | Evidence | Location | Stance`, but the bundled `note-template.md` (§5, which `SKILL.md:326` names as the note template) carries the richer matrix, which the parser then mis-reads. Unlike `parseCards`, `parseClaims` does not use `columnIndexExcept`.

**Suggested fix:**
a) Resolve claim columns with `columnIndexExcept`, excluding earlier picks (mirroring `parseCards`), so `Evidence` cannot re-select the `Claim` cell.
b) Or match tokens against the cell prefix / with word boundaries and skip cells already claimed by another field.
c) Align assets and parser: add the documented `Claim | Evidence | Location | Stance` table to `note-template.md` (or update `SKILL.md` to describe §5), and add a parser test using the template header asserting `Claim != Evidence`.

### Issue 17 — SHOULD FIX — `RunPaperLiterature` holds the per-research-root row-mutation mutex across the entire network-bound helper run (up to 90 s)

The RPC takes the coarse mutex that serializes every projects-row writer and holds it for the whole external subprocess run, although it writes no row.

**Location:** `backend/frontend_api_papers_literature.go:113-125` (lock at `:121-123`); timeout `literatureRunTimeout = 90 * time.Second` (`:58`); the subprocess runs in `runLiteratureHelper` (`:174-236`).

**Why it is a problem:** The mutex is the same one `SetPaperPinned`, `RecordFlashcardReview`, and (with RESEARCH on) the research pin/mutation RPCs and `Enable/DisableResearch` acquire. Holding it for a network-bound `exec.CommandContext(...).Run()` (several HTTP requests; up to 90 s, longer if the helper wedges) head-of-line-blocks any pin/unpin or flashcard grade on the same project — and any research mutation — surfacing as an unresponsive Papers/Research panel. The only shared resource is a single file (`<paper-dir>/literature.json`) inside one paper directory, and the write already lands inside the watched library (the `papers:changed` watcher is the refresh signal). The RPC also does not re-load the row / re-check the root under the lock, so the sibling writers' `errResearchRootChanged` guard has no analogue here (see Issue 3).

**Suggested fix:**
a) Do not take the mutation mutex at all — the helper touches no projects row; rely on the watcher. If mutual exclusion is genuinely needed, use a per-paper-directory lock so lookups on different papers stay concurrent.
b) Or narrow the critical section: run the helper outside the lock, then take the lock only to re-resolve the containment-checked paper directory and perform/verify the atomic write.
c) If the lock must span the run, add the same under-lock `fresh.ResearchRoot != rctx.project.ResearchRoot` re-check the siblings use, and document the hold can last up to `literatureRunTimeout`.

### Issue 18 — SHOULD FIX — `paperCardRelPath` uses the path-containment idiom the project forbids, diverging from the sibling pin-path builder

`paperCardRelPath` guards with `filepath.Rel` + `strings.HasPrefix(rel, "..")`, which AGENTS.md explicitly forbids for path construction/containment.

**Location:** `backend/frontend_api_papers.go:492-498` (the guard at `:495`); the sibling `researchDocRelPath` at `backend/frontend_api_research.go:1103-1109` carries no such guard.

**Why it is a problem:** AGENTS.md's path-centralization rule states: "NEVER inline … `filepath.Rel`+`HasPrefix(rel,\"..\")` … Always use `pathutil.IsWithinPath`, `config.IsWithinPath`, … or the relevant constant." This is a new file, and the two root-relative-path builders now disagree on the idiom. The guarded branch is unreachable today (both operands are validated upstream by `papersReadContextFor`/`parsePaperLibrary`), so it gives a false sense of a local containment check while blocking reuse of the centralized helper.

**Suggested fix:**
a) Replace the ad-hoc test with the centralized API, e.g. `if ok, _ := config.IsWithinPath(researchRoot, dir); !ok { rel = filepath.Base(dir) }` — or drop the branch entirely since the inputs are guaranteed contained.
b) Add a `config`/`pathutil` helper (e.g. `RootRelativeSlashPath(root, path)`) encapsulating `filepath.Rel`+`ToSlash`, and use it from both `paperCardRelPath` and `researchDocRelPath` so pin-path construction lives in the path layer.

### Issue 19 — SHOULD FIX — `togglePaperPin`'s catch path writes the previous project's error into the now-active store

The failure path calls `setError` unconditionally, unlike the success path and `refreshPaper`, which both re-check the live project.

**Location:** `frontend/src/stores/paperStore.ts:419-423` (catch `setError`), contrasted with the success path guard at `:428` (`if (after.projectId !== projectId) return`) and `refreshPaper`'s guarded paths (`:396`, `:400`).

**Why it is a problem:** `togglePaperPin` reads `projectId` once, then awaits `setPaperPinned`. On success it re-reads the live store and bails when the project changed; on failure it calls `usePaperStore.getState().setError(...)` with no guard. `setError` also forces `isLoading:false, isMutating:false` (`:212`). Trigger: pin on project A, switch to B while the write is in flight, and the write rejects (card deleted, disk/permission error) — B's store receives A's error message (rendered by `PapersView`, which consumes `usePapersError()`), and B's in-flight library spinner is dismissed early. It is an inconsistency, not a deliberate contract.

**Suggested fix:**
a) Mirror the success path: re-read the live store and bail before calling `setError` when `after.projectId !== projectId`.
b) Or add a guarded `setErrorFor(projectId, msg)` in the store so every async writer shares one live-project guard.

### Issue 20 — SHOULD FIX — `PapersView`'s dispatch-failure catch mutates the paper store with no live-project guard

The component-level `send()` rejection handler writes `setError` blindly, unlike the store's own guarded async writers.

**Location:** `frontend/src/components/papers/PapersView.tsx:278-290` (catch `setError` at `:285`).

**Why it is a problem:** `send()` rethrows only when the auto-created session fails (the documented splash race), and that rejection can land after the user switched projects; the failure string is then written into the **new** project's error slot (the panel shows "Failed to dispatch study-paper…" for a project that never dispatched), and the same call clears `isLoading`/`isMutating` of an in-flight library fetch. The store's own async writers (`refreshPaper`, `togglePaperPin` success path) guard for exactly this. The omission is not unique to `PapersView` — the identical dispatch helper in `PaperWorkspace` (and the research `[22]a` pattern it cites) has the same gap — so the fix belongs in a shared guarded helper rather than one call site.

**Suggested fix:**
a) Snapshot `usePaperStore.getState().projectId` before `send` and only `setError` in the catch when it still matches.
b) Or route dispatch errors through a store action with a guarded setter (mirroring `refreshPaper`) so the decision lives in the store.

### Issue 21 — SHOULD FIX — The Literature graph is the only paper section not keyed to the library sync, so a `literature.json` written by the skill never refreshes it

`usePaperLiterature(dir)` re-probes only on `[dir, nonce]`, while the sibling loaders take a `refreshKey` threaded from the store's `lastSyncAt`.

**Location:** `frontend/src/components/papers/usePaperLiterature.ts:47-88` (effect deps `[dir, nonce]`); `PaperWorkspace.tsx:115/118` passes `syncAt` to `usePaperArtifacts`/`useComparisons` but passes no key to `PaperLiterature` (`:114`, `:326`).

**Why it is a problem:** `<paper-dir>/literature.json` is written by the study-paper `literature.py` helper, which `SKILL.md:286` instructs the agent to run in a chat session — exactly the path that emits `papers:changed`. `PaperWorkspace` then rebuilds the other sections (they take `syncAt`) and the literature.md note, but the Literature **DAG** keeps rendering the previous file until the user manually hits Refresh or reopens the tab. The three sibling loaders thus have inconsistent refresh contracts.

**Suggested fix:**
a) Give `usePaperLiterature` a `refreshKey` parameter mirroring `usePaperArtifacts`, thread `syncAt` from `PaperWorkspace`, and add it to the effect deps.
b) Or have `PaperLiterature` accept a `refreshKey` prop that drives `reload`.

### Issue 22 — SHOULD FIX — Every library sync blanks all comparisons and re-enters "Loading…", unmounting the rendered matrix

The reload clears `items` on every `refreshKey` change (which fires on any `papers:changed`), not only on a directory change.

**Location:** `frontend/src/components/papers/useComparisons.ts:75` (`setState({ dir, loading: true, items: [] })` inside the effect at `:69-99`).

**Why it is a problem:** The effect deps are `[dir, refreshKey]` and `PaperWorkspace.tsx:118` passes `syncAt`, which bumps on *every* library sync (a pin/unpin, a background watcher tick, the skill appending any section). With the Compare section open, any such sync flips `comparisons.loading` to `true` and empties `items` → `PaperWorkspace` renders `paper-compare-loading` and unmounts the `CompareMatrix`, so the matrix visibly flashes "Loading…" and re-reads every comparison file each sync. The listing-failure catch (`:96-98`) likewise replaces the rendered set with `items: []`, so a transient list error on refresh downgrades an open matrix to the empty/fallback state instead of keeping what was shown. This is the same class as Issue 6 (`usePaperArtifacts`), in a sibling file.

**Suggested fix:**
a) Keep the previous `items` while reloading — `setState((s) => ({ dir, loading: true, items: s.items }))` — and only clear on a `dir` change.
b) Or refetch only when the directory listing actually changed (compare names/mtimes), preserving `items` otherwise.

### Issue 23 — SHOULD FIX — A comparison read failure is silently swallowed (`ComparisonArtifact.error` has no consumer)

The per-file catch records an `error`, but nothing renders it, so a failed read degrades to the "no comparison" empty state.

**Location:** `frontend/src/components/papers/useComparisons.ts:83-92` (per-file catch returns `{ content: '', error }`); consumer `PaperWorkspace.tsx` Compare branch filters `relevantComparisons` on `comparisonMentionsPaper(item.content, paper)` and passes only `content` to `CompareMatrix`.

**Why it is a problem:** If a comparison file exists but cannot be read (permissions, removed between the directory listing and the read, truncated write), the item has `content: ''`, so `comparisonMentionsPaper('')` is false and the section silently falls back to the per-paper artifact or shows "No comparison recorded for this paper yet." — a misleading empty state that hides the read failure (unlike `PaperMarkdownSection`, which renders `artifact.error`).

**Suggested fix:**
a) In the Compare branch, surface items whose `error !== null` as a small "could not read <file>" notice instead of letting them fall through to the empty state.
b) Or exclude errored items from the mention filter but return a parallel error list the section renders.

### Issue 24 — SHOULD FIX — The pin action can issue duplicate RPCs (no in-flight guard), unlike the auto-pin path

`togglePaperPin` has no re-entry guard and the pin control is never disabled, so a rapid double-click fires two identical RPCs.

**Location:** `frontend/src/components/papers/PapersView.tsx:347-349` (`pinPaper` → `togglePaperPin(paper.id, !paper.pinned)`); store `frontend/src/stores/paperStore.ts:411-431` (`togglePaperPin`), contrasted with `ensurePaperPinned` (`:437-450`, guarded by `pinEnsuresInFlight`).

**Why it is a problem:** the record is not updated until the RPC resolves, so two fast clicks compute the same `!paper.pinned` target and issue two identical `SetPaperPinned` RPCs; the row button stays enabled throughout. The target is idempotent so there is no corruption, but it is an avoidable duplicate write and a maintainability inconsistency with the guarded auto-pin path (`isMutating` is set but never consumed to disable the button).

**Suggested fix:**
a) Disable the pin button while a mutation is in flight (consume `selectPapersMutating`).
b) Or add a re-entry guard inside `togglePaperPin`, symmetric with `ensurePaperPinned`'s in-flight set.

### Issue 25 — SHOULD FIX — `ResearchStatusDTO.PinnedPapers` is a populated, documented wire field with no consumer (duplicate pin channel)

The backend fills `pinned_papers` on every research status, the spec documents it, and the generated binding carries it — but no frontend code reads it.

**Location:** `backend/frontend_api_research.go:63-68` (the field) and `:1333` (`applyResearchPins` fills it); the contract `specs/contracts/desktop-frontend.md:342` ("`ResearchStatusDTO` also carries `pinned_papers` (non-nil)…"); generated `frontend/wailsjs/go/models.ts:1371`.

**Why it is a problem:** a repo-wide search of `frontend/src` finds no reader of `pinned_papers` — the Papers UI gets pin state from `GetPapers`'s `Pinned`/`card_path` (`paperStore.pinned`). So the same pin list travels on two channels, one of which is dead payload that must be kept in sync; the spec and the generated model claim a consumer that does not exist (documentation/contract drift).

**Suggested fix:**
a) Drop `PinnedPapers` from `ResearchStatusDTO`, `applyResearchPins`, and the spec line (single source of truth: `GetPapers`).
b) Or, if it is intended for a future consumer, wire one (or add an explicit TODO/issue reference) and keep the spec/source in agreement.

### Issue 26 — SHOULD FIX — A test that claims to pin a guard asserts nothing, so it cannot detect the regression it documents

`TestSeedPapersSkillPack_EmptyAgentDirIsNoop` has no assertion; it passes as long as the call does not panic.

**Location:** `backend/frontend_api_papers_test.go:68-74`.

**Why it is a problem:** The body is `f := &FrontendAPI{}; f.seedPapersSkillPack("")` — there is no assertion, while the doc comment claims it "pins the guard: an empty agentDir (the test/default case) must be a silent no-op, never a write to the real user home." If the guard `if agentDir == "" { return }` (`backend/frontend_api_skills.go:72`) were deleted, `config.SkillsDir("")` would resolve to a relative `.agents/skills` under the test's working directory and the test would still pass — the no-write behavior it claims to verify is never observed.

**Suggested fix:**
a) Assert the no-write outcome: run from a temp CWD and assert `.agents`/`config.SkillsDir("")` does not exist afterwards.
b) Or add a seeder seam (`seedPapersSkillPack(agentDir, seedFn)`) and assert the injected function is not called with `""`.
c) Or drop the guard claim from the test's comment so it does not assert coverage it lacks.

### Issue 27 — SHOULD FIX — `panelPersistence.test.ts` states the persisted version is 5 while asserting 6

The test name documents a schema version the assertion contradicts.

**Location:** `frontend/src/stores/panelPersistence.test.ts:203-205`.

**Why it is a problem:** `it('persist version is 5', () => { expect(useUIStore.persist.getOptions().version).toBe(6) })`. The assertion is correct (`uiStore.ts:257` is `version: 6`) but the name is stale, so anyone auditing the persistence-version contract through test names is told the store is at 5 while it is at 6; the same file's other cases correctly treat 6 as current and 5/4 as older payloads.

**Suggested fix:**
a) Rename the case to reflect the current version, or make it version-agnostic (e.g. `'persist version matches the store'`).
b) Or export a `UI_STORE_VERSION` constant from `uiStore` and assert `version` equals it, so the name can never drift.

### Issue 28 — SHOULD FIX — The literature run applies its result with no paper-identity guard (a likely-future bug, currently masked by the section reset)

`PaperLiterature`'s `onRun` writes `setRun`/`setRunning(false)` unconditionally when the network-bound helper RPC resolves; every sibling async path in this change carries a cancellation/identity guard.

**Location:** `frontend/src/components/papers/PaperLiterature.tsx:184-198` (`setRun(result)` at `:188`, `setRunning(false)` at `:198`); contrast `usePaperLiterature.ts`'s own `cancelled` flag, `paperStore.ts` `refreshPaper`'s `projectId` re-check (`:396`, `:400`), and `fetchPaperLibrary`'s ticket (`:357`, `:371`).

**Why it is a problem:** `onRun` captures `paper.id`/`projectId` at click time and applies the result with no staleness check. Today the only ways the displayed paper changes (a tab switch or a project switch) also change `slug`, and `PaperWorkspace`'s `[slug]` effect resets `section` to `'overview'` (`PaperWorkspace.tsx:126-130`), which unmounts `PaperLiterature` before the ~90 s-bounded RPC can resolve — so the late `setRun` lands on an unmounted component and is discarded, and the race is **not currently observable**. It remains a latent defect (the "likely future bug" band): the guard is missing exactly where all three sibling loaders have one, so any later change that keeps the section mounted across a paper change (removing the section reset, or rendering the workspace with a stable per-tab `key`) would let paper A's run status (`ok`/`offline`/…) render under paper B. The `reload()` call is harmless (its callback deps are `[]`).

**Suggested fix:**
a) Capture the paper identity at call time and drop the result on mismatch (mirror `usePaperLiterature`'s `cancelled` flag, or compare against the live paper id / a ref before `setRun`).
b) Or key the run/`running` state by slug (a per-slug ref/store) so a paper switch cannot cross-contaminate.

### Issue 29 — SHOULD FIX — The bundled `note-template.md` omits the Red Flags / Uncertainty tables the parser and spec require, so `ParseNote` returns nil and the "gaps" gesture loses the paper's open questions

**Location:** `core/papers/parser.go:820` (`parseRedFlags`) and `:844` (`parseUncertainties`); the template `core/papers/skills/study-paper/assets/note-template.md:78-86` (§7 prose bullets); the consumer `frontend/src/components/papers/paperActions.ts:209` (`paperGaps` → `paper.uncertainties`).

**Why it is a problem:** `parseRedFlags`/`parseUncertainties` select a **table** whose header carries `flag`/`uncertain` (or a matching heading), but §7 of the shipped template writes them as bold prose bullets (`- **Red flags (≤3, ranked by severity):**`, `- **Uncertainty flags** — …`) and carries no such table. A note authored from the template (the path `SKILL.md:326` prescribes) therefore parses to `RedFlags == nil, Uncertainties == nil`. `ParsePaper` folds those into `PaperRecord` (`parser.go:964`), the DTO carries `[]` (`frontend_api_papers.go:477-478`), and `paperGaps` — which reads the **DTO's** `uncertainties` — returns `[]`. Trigger: record red flags / open questions in a note built from the template, then use "Suggest hypotheses from gaps" — the dispatch prompt (`buildProposeHypothesisPrompt`) silently falls back to the generic proposal instead of grounding on the recorded gaps. (`SKILL.md:252-255` documents the expected `Flag | Detail | Severity` / `Item | Detail` tables and `RenderNoteMD` emits them, so writer↔parser agree; the template is the outlier. The chips *display* still works only because the frontend re-parses the raw note with a list fallback — the structured / `paperGaps` path does not.)

**Suggested fix:**
a) Add `## Red Flags` (`Flag | Detail | Severity`) and `## Uncertainty` (`Item | Detail`) tables to `note-template.md` (matching `RenderNoteMD`, `writer.go:90-107`), keeping the §7 prose as authoring guidance.
b) Or extend the Go `parseRedFlags`/`parseUncertainties` to also read the §7 bulleted lists (mirroring the frontend's `parseRedFlagList`/`parseUncertaintyList`).
c) Add a parser test that runs `note-template.md`'s §7 shape and asserts non-empty red flags / uncertainty.

### Issue 30 — SHOULD FIX — `commitFlashcardReview` swallows a failed grade write-back with no UI consumer

**Location:** `frontend/src/stores/paperStore.ts:466-477` (`commitFlashcardReview`; `catch (err) { logger.warn(...) }`), called fire-and-forget from `frontend/src/components/papers/FlashcardsReview.tsx:95-99` (`void commitFlashcardReview(...)`).

**Why it is a problem:** The grade is applied optimistically to the local `results` and the RPC is fired without awaiting its outcome or surfacing a failure; `commitFlashcardReview` catches every error and only logs a warning (its own doc: "Never throws: a failure is a log warning"). If `RecordFlashcardReview` fails (card removed/renamed by a concurrent skill edit, backend/disk error), the user still sees "good → 2026-09-20" while nothing was persisted, and the next `papers:changed` refetch silently reverts the card. No `paperStore.error`, toast, or per-card "not saved" state consumes the failure — unlike the dispatch paths (`PapersView`/`PaperWorkspace`) that surface on `paperStore.error`. Same class as Issue 23 (a swallowed error with no consumer).

**Suggested fix:**
a) Surface it: `usePaperStore.getState().setError(...)`, or return the promise and have `FlashcardsReview` render a per-card retry state.
b) Or mark optimistic grades and flag any that vanish on the next refetch.
c) At minimum report the failure through the existing `runtime_error` event channel rather than only `logger.warn`.

### Issue 31 — SHOULD FIX — The `usePaperArtifacts` "never drops loaded sections" test asserts only the settled state, so it cannot fail on the regression it documents

**Location:** `frontend/src/components/papers/usePaperArtifacts.test.tsx:100-119` (the test, via `settle()` at `:45-49`), file header at `:1-6`.

**Why it is a problem:** The header claims the hook "re-lists the directory and rebuilds the section set, **while never dropping the sections already loaded**", and the test asserts `text('note') === 'Note v1'` after `await rerender(1)`. `rerender` awaits `settle()` (a `setTimeout(0)` flush), so every assertion observes the post-resolve state; the implementation's transient blank (`setArtifacts(allOf(pending))` at `usePaperArtifacts.ts:95`, where `pending()` sets `content: ''`) is invisible. The assertion therefore passes whether or not the hook blanks the sections mid-refresh — it cannot detect Issue 6. A future reader auditing the contract through this test is misled.

**Suggested fix:**
a) Assert the intermediate frame: gate `readFileMock` on a deferred promise and assert `text('note') === 'Note v1'` *between* `rerender(1)` and the resolve (a blank-then-reload implementation then fails).
b) Or rewrite the header/test to state the real contract (rebuild-after-read) so it no longer claims coverage it lacks.

### Issue 32 — SHOULD FIX — `TestWriteFlashcardsRoundTrip` makes a decorative, unfalsifiable `ParsePaperDir` call (`_ = got`)

**Location:** `core/papers/writer_flashcards_test.go:83-90` (`got, err := ParsePaperDir(...)`; `_ = got` at `:90`).

**Why it is a problem:** The call's result is discarded and only `err` is checked; a directory just created by `WriteFlashcards` always parses, and a deck-only directory yields no card artifacts, so the call can never fail on a regression and asserts nothing about the written card. The test's real assertion (the deck round-trip via `ParseFlashcards`) is separate. It reads as a round-trip through the card parser but provides none — the same "asserts nothing" pattern as Issue 26.

**Suggested fix:**
a) Drop the `ParsePaperDir` call (the deck assertion is the test).
b) Or make it meaningful: also seed a `paper.md` and assert the parsed `PaperRecord` id/slug (plus a lookup), so the call can fail.

### Issue 33 — SHOULD FIX — `flexibleInt` documents `"c. 2017"` support but only recovers a leading digit run, so `year: c. 2017` silently becomes 0

**Location:** `core/papers/parser.go:258-261` (doc) with `:217-224` (`leadingDigits`) and `:266-281` (`flexibleInt.UnmarshalYAML`); the `year:` fallback case reuses the same helper.

**Why it is a problem:** The doc says `flexibleInt` recovers the year from `"2017"`, `"2017-06"`, and `"c. 2017"`. But `leadingDigits` returns only the *leading* run of ASCII digits, so `"c. 2017"` (or `"circa 2017"`) yields `""` → `Atoi("")` errors → `Year` stays `0` with no warning. Trigger: a hand-authored card with `year: c. 2017` — expected 2017, actual 0.

**Suggested fix:**
a) Implement the promise: when `leadingDigits` is empty, take the first 4-digit run anywhere (`` regexp.MustCompile(`\d{4}`).FindString(v) ``).
b) Or correct the doc comment to state that only a leading number is recovered and drop the `"c. 2017"` claim.

### Issue 34 — SHOULD FIX — `RunPaperLiterature`'s exit-code → status mapping conflates the helper's own codes with Python's / argparse's generic ones and with the write-failure case

**Location:** `backend/frontend_api_papers_literature.go:206-213` (the `switch exitErr.ExitCode()` mapping); the helper invocation `backend/frontend_api_papers_literature.go:181-190` (`exec.CommandContext(ctx, pythonPath, scriptPath, seed, "--format", "json", …)` — the `seed` is a bare positional with no `--` terminator, and `literatureSeed` falls back to `rec.Title`, so a title/identifier beginning with `-` is parsed by argparse as an option — error exit 2 → surfaced as `offline`, or the following flag swallowed as that option's value); helper exit codes `core/papers/skills/study-paper/scripts/literature.py:673-700` (1 = `could not resolve the seed`, 2 = network, 3 = rate-limited **or** output-write failure).

**Why it is a problem:** The mapping assumes exit 1/2/3 can only come from the helper's `main()`, but CPython also exits **1** on an uncaught exception and `argparse` exits **2** on a usage error (the seed is passed as a bare positional ahead of `--format`); and the helper returns **3** for an output-write failure, not only rate limiting. So an unexpected crash in `literature.py` is surfaced as "the seed could not be resolved", and a failed write as "rate limited" — mislabeled user-facing statuses (`specs/domains/papers.md` treats these as messages). The `Message` carries the real stderr tail, which partly mitigates it.

**Suggested fix:**
a) Reserve a non-1/2/3 exit code (or an stderr sentinel) in `literature.py` for unexpected faults, pass `scriptPath, "--", seed, …` to avoid argparse option confusion, and map only the documented codes.
b) Or tighten the Go mapping: treat exit 1 without the `could not resolve the seed:` marker, and exit 3 without `rate limited:`, as `litStatusError`.

### Issue 35 — SHOULD FIX — A card without the documented-optional `id:` gets `PaperDTO.id == ""`, and the whole frontend is keyed on that id (row collisions, shared selection, rejected pin/review RPCs)

**Location:** `core/papers/parser.go:1023-1026` (`rec.ID = NormalizePaperID(base)`) → `core/papers/model.go:219` (`paperIDRe = (?i)P-?(\d+)`) → `backend/frontend_api_papers.go:463` (`ID: rec.ID`, no fallback) → `frontend/src/api/papers.ts:246` (`normalizePaper` accepts any string id) → `frontend/src/components/papers/PapersView.tsx:470` (`key={paper.id}`), `:326`/`:472` (`selectedSet.has(paper.id)`), `:348` (`togglePaperPin(paper.id, …)`), `:341` (`ensurePaperPinned(paper.id)`), `frontend/src/stores/paperStore.ts:173` (`records[next.id]`), `:200` (`records[record.id]`).

**Why it is a problem:** `SKILL.md:226` declares `id: P-001 … (optional)` and "every field is optional", so an id-less card is a documented-valid input. `parsePaperDir` derives an id only via `NormalizePaperID(base)`, which requires a literal `p`+digits, so a slug like `vaswani-2017-attention` yields `ID == ""`. `toPaperDTO` passes it through and `normalizePaper` accepts `""`, so the UI keys every id-less paper on `""`: `key=""` collides (React duplicate-key warning, unstable reconciliation), `records[""]` collapses to one record, `selectedSet.has("")` shares one checkbox across all id-less rows (Compare-selected then builds a prompt over papers never chosen), `togglePaperPin("")` → `SetPaperPinned(…, "")` → rejected ("paper id or slug is required") so pinning is broken, and `commitFlashcardReview("")` → `RecordFlashcardReview` rejected → grades silently lost (per Issue 30). The rejection is fail-closed (no wrong paper is written).

**Suggested fix:**
a) Guarantee a non-empty, unique DTO id: in `toPaperDTO` fall back to `rec.Slug`, and/or in `parsePaperDir` fall back to the slug when `NormalizePaperID(base) == ""`.
b) Or key/select/dispatch on `paper.slug` everywhere in the frontend (`PaperLibrary.Get` already accepts an id or a slug).
c) Add a regression test: parse a library with an id-less card in a non-`P-NNN` directory and assert the DTO id is non-empty and unique.

### Issue 36 — SHOULD FIX — The flashcard due date is computed from the UTC date in the UI but the backend records the LOCAL date, so the shown "next due" can be off by one day from the persisted value

**Location:** `frontend/src/components/papers/FlashcardsReview.tsx:52-54` (`todayISO()` = `new Date().toISOString().slice(0,10)`) and `frontend/src/lib/spacedRepetition.ts:113` (`toISODate` = `date.toISOString()`), vs `backend/frontend_api_papers.go:287` (`date := time.Now().Format("2006-01-02")`).

**Why it is a problem:** The frontend derives "today" from the UTC calendar date (and UTC throughout `spacedRepetition`), while `RecordCardReview` stamps both the appended review row's date cell and the computed schedule with `time.Now()` in the machine's **local** zone. For any non-UTC user the two can differ for part of the day (e.g. MSK at 01:00 local, or US users all evening), so the optimistic banner shows a due date one day off from what `flashcards.md` records for the same grade.

**Suggested fix:**
a) Make the frontend use the local date consistently (a shared `todayLocalISO()` in `lib/spacedRepetition.ts`) to match the backend.
b) Or return the written `date`/`next_due` from `RecordFlashcardReview` and render the authoritative values instead of computing them optimistically.
c) Add a test with a clock offset from UTC asserting the UI-computed and RPC-requested dates agree.

### Issue 37 — SHOULD FIX — A genuinely unreadable library root is silently shown as an empty library

**Location:** `backend/frontend_api_papers.go:443-454` (`parsePaperLibrary`: `if err != nil { f.log().Debug(...); return &papers.PaperLibrary{Root: libraryRoot} }`) → `core/papers/parser.go:1036-1049` (`ParseLibraryDir` propagates `os.Stat`/`os.ReadDir` errors).

**Why it is a problem:** Every `ParseLibraryDir` error is collapsed into the same empty-library value the backend uses for the legitimately-not-yet-created case, logged only at `Debug` (invisible at the default level). Trigger: make `<research-root>/papers` unreadable (permissions, I/O error, stale mount) — the entire library silently disappears from the Papers panel ("No papers studied yet"), indistinguishable from having no papers, with no toast or error. This is the sibling of Issue 23 (a swallowed comparison read error); the doc comment states the conflation is intentional, but the user-visible consequence is a misleading empty state.

**Suggested fix:**
a) Distinguish not-found from unreadable: treat `errors.Is(err, fs.ErrNotExist)` as the intended empty library and surface anything else (return an error, or add a `degraded`/`error` field the panel renders as a banner).
b) At minimum raise the log to `Warn` with the root so the failure is diagnosable.
c) Add a "root exists but is unreadable" test asserting the error is not converted to an empty list.

### Issue 38 — SHOULD FIX — Dead exported API surface across the new packages (no production consumer)

**Location:** `core/papers/model.go:474` (`PaperRecord.IsAppraised` — no callers at all), `core/papers/model.go:525` (`PaperLibrary.ByResearchID` — tests only; the backend links via its own helper and the frontend folds its own), `backend/config/paths.go:392` (`config.PaperDir` — tests only), `core/papers/writer.go:157` (`WriteFlashcards`) with `core/papers/flashcards.go:225` (`RenderFlashcards`) — reached only through the test-only `WriteFlashcards`, and `frontend/src/lib/spacedRepetition.ts:127` (`isDue`) / `:157` (`lastDueDate`) — tests only.

**Why it is a problem:** These helpers are documented in `specs/domains/papers.md` / the package docs as if they drive behaviour, but no production call site uses them; `ByResearchID` in particular is documented as *the* H-NNN selector while the linking is implemented elsewhere, so spec and code disagree about the source of truth. A maintainer trusting the docs may extend or rely on code that is never exercised — the same drift class as Issue 12 for the store and Issue 25 for the DTO field, here in the Go package and `lib/spacedRepetition.ts`.

**Suggested fix:**
a) Remove the unused helpers (and their tests) and drop/adjust the corresponding spec sentences.
b) Or wire them up (e.g. use `ByResearchID` for the research→paper linking) and mark the kept ones explicitly as public/spec-owned API.

### Issue 39 — SHOULD FIX — The `PaperWorkspace` "keeps every previous section on a deepen" test stubs the loader, so it cannot fail on the regression it names

**Location:** `frontend/src/components/papers/PaperWorkspace.test.tsx:24-28` (`vi.mock('./usePaperArtifacts', …)` returning a pre-built `artifactsHolder.current`) with the test at `:380-412`.

**Why it is a problem:** The test injects a fully built `PaperArtifacts` object through the mocked hook and then only asserts the sections render. Because the hook is stubbed, the component never experiences the real `refreshKey` re-run (`pending()` blanking) that Issue 6 describes, so the assertion is a tautology over the mock, not a round-trip; it cannot catch a regression that drops prior sections during a rebuild. It is the component-level twin of Issue 31 (hook-level) and the same class as Issue 26.

**Suggested fix:**
a) Drive the real hook (mock `@/api/workspace`, not `./usePaperArtifacts`) and bump the refresh key between renders, asserting previously parsed sections survive.
b) Or retitle the test to what it actually proves ("renders each section the loader supplies") and drop the round-trip claim.

### Issue 40 — SHOULD FIX — `SKILL.md` documents a `fetch_paper.py --arxiv <id>` invocation the shipped script does not accept, so following the docs fails with a usage error

**Location:** `core/papers/skills/study-paper/SKILL.md:288` (documented example) vs `core/papers/skills/study-paper/scripts/fetch_paper.py:562-575` (actual CLI).

**Why it is a problem:** The skill's "Optional lookups" section instructs: `` run `python3 <skill-dir>/scripts/fetch_paper.py --arxiv 1706.03762` `` (SKILL.md:286-289). The vendored `fetch_paper.py` declares only a **positional** `reference` argument (`:562`) plus `--email` (`:563`), `--timeout` (`:569`), `--out` (`:573`), `--compact` (`:574`) — there is **no `--arxiv` option**, and the script's own usage block shows the bare-positional form (`python3 fetch_paper.py arXiv:1706.03762`). `argparse.parse_args` (`:575`) therefore rejects the documented command (unrecognized `--arxiv`, and the required `reference` missing) with a usage error (exit 2), so the helper never runs. The mismatch is compounded by the backend mapping: a usage error's exit 2 is mapped to `litStatusOffline` ("network unavailable") by `RunPaperLiterature` (see Issue 34), so following the shipped docs surfaces a misleading network-outage status rather than a usage error.

**Suggested fix:**
a) Correct the example to the supported positional form — `` `python3 <skill-dir>/scripts/fetch_paper.py 1706.03762` `` (or `arXiv:1706.03762`), matching the script's own usage block.
b) Or add an `--arxiv`/`--doi` flag to `fetch_paper.py` that populates `reference`, if the flag-style invocation is the intended interface.

### Issue 41 — SHOULD FIX — `classifyAnchor` drops a figure/table reference's sub-label suffix, so `Fig. 2a` jumps to a *different* figure and `Table 3b` reports "not found"

`classifyAnchor` captures only the digit run from a figure/table reference, and `numberedLineRe` then anchors that bare token with a trailing `\b`, which cannot match a sub-labelled caption (`Figure 2a`).

**Location:** `frontend/src/lib/paperAnchors.ts:57` (`/^fig(?:ure)?\.?\s*(\d+)/i`) and `:59` (`/^tab(?:le)?\.?\s*(\d+)/i`) capturing only `(\d+)`, with `numberedLineRe` at `:84-86` (`` `\\b${word}\\.?\\s*\\(?${escapeRe(token)}\\)?\\b` ``).

**Why it is a problem:** `Fig. 2a` / `Table 3b` classify to token `"2"` / `"3"`. `numberedLineRe` builds `\bfig(?:ure)?\.?\s*\(?2\)?\b`; in the source line `Figure 2a: Overview…` the character after `2` is `a` (a word character), so the trailing `\b` fails at that position, the scan continues, and a *plain* `Figure 2` caption later in the document is returned instead — a wrong jump, exactly what the module's header says it avoids ("only … reports a hit on a confident structural match … no false jumps"). When the source has no un-suffixed caption it returns `null` and the UI shows an honest-looking but wrong "not found". Sub-labelled floats are a common convention, and the `anchors` `ref` values are authored by the skill/model (e.g. `Fig. 2a`). Verified by executing the module: `resolveAnchor(source, {ref: "Fig. 2a"})` on a document whose line 1 is `Figure 2a: …` and line 2 is `Figure 2: …` returns line 2 (the wrong caption); `{ref: "Table 3b"}` returns `null` against `Table 3b: Results.`. No test covers a suffixed token (`frontend/src/lib/paperAnchors.test.ts` contains no `2a`/`3b` case).

**Suggested fix:**
a) Capture the optional suffix — `/^fig(?:ure)?\.?\s*(\d+[a-z]?)/i` (same for `table`) — and match the suffix in `numberedLineRe` (drop the trailing `\b` for a token that may end in a letter).
b) Keep the digits-only token but replace the trailing `\b` with `(?!\d)`, so `2` matches `2`, `2.`, `2a`, `2:` but not `20`/`21`.
c) When the reference carries a suffix, require it (`Fig. 2a` ⇒ `2[a-z]?`) so `Figure 2` and `Figure 2a` cannot be confused, and add `Fig. 2a`/`Table 3b` regression tests.

### Issue 42 — SHOULD FIX — `normalizeStrength` classifies `not present` / `no evidence present` as `present`, inverting the recorded verdict

The verdict is chosen by plain `String.includes` on `absent`/`weak`/`present`; a negated phrase contains the positive token, so it is classified as the strongest verdict.

**Location:** `frontend/src/lib/paperWidgets.ts:161-172` (`normalizeStrength`); the multi-token ambiguity guard at `:168` only folds a cell naming *two* verdicts, not a negated one.

**Why it is a problem:** The note's §5 "Evidence strength (present / weak / absent)" column is rendered by this fold, so a negated value silently becomes its opposite. Verified by executing the module: `normalizeStrength('not present') === 'present'`, `normalizeStrength('no evidence present') === 'present'`, and (substring collision) `normalizeStrength('unrepresented') === 'present'`. Trigger: a note authored from the template whose strength cell reads `not present` / `no evidence present` (natural wording for "absent") → the Critical-Layer evidence matrix paints a claim with no evidence as well-evidenced. No test covers a negated or word-embedded cell.

**Suggested fix:**
a) Detect negation first: when the cell matches `/\b(not|no|none|without)\b/` and names exactly one positive verdict, fold to the complementary verdict (`not present` / `no evidence present` → `absent`).
b) Match whole-word tokens (`/\bpresent\b/`, `/\bweak\b/`, `/\babsent\b/`) so `unrepresented`/`presence` no longer collide, keeping the multi-token → `unknown` rule.
c) Accept only the template vocabulary — anything not exactly `present`/`weak`/`absent` folds to `unknown` — preferring an honest unknown over a wrong strong verdict.

### Issue 43 — SHOULD FIX — `listItemsOfSection` treats a bold-led list ITEM as a new section label, truncating the red-flag / uncertainty chips

In `listItemsOfSection` the bold-label branch runs before the list-item branch and, for any `- **…**` line whose bold text is not the section marker, unconditionally ends the current section — so a bullet whose lead-in is bolded terminates the list.

**Location:** `frontend/src/lib/paperWidgets.ts:287-330` (`listItemsOfSection`), specifically the bold match at `:306` and `if (inSection) inSection = false` at `:315`.

**Why it is a problem:** For a line such as `- **Data leakage** is not ruled out.` the regex `/^[-*+]?\s*\*\*(.+?)\*\*/` matches with `bold[1] = "Data leakage"`, which does not satisfy the uncertainty marker (`/uncertain|unknown|open question|limitation/`), so the section is ended and that item — and every item after it — is dropped. Verified by executing the module on the note template's §7 shape: `parseUncertainties` returns `[]` when the child item is bolded and the item when it is not; `parseRedFlagList` behaves the same. The module's own `stripInlineMd` strips `**` from items, so bold items were clearly meant to be accepted — the section scanner contradicts that intent. Trigger: the skill records §7 (as the template directs) with bolded lead-ins, a common LLM formatting habit; the Critical-Layer widget then renders no uncertainties/red flags, and `parseCriticalLayer` falls back to the appraisal, which uses the same style. (This is the frontend list-fallback twin of Issue 29, which is about the Go parser.)

**Suggested fix:**
a) Test the list-item shape first: when `inSection` and the line matches a bullet/numbered item, push it (stripping the bold) instead of ending the section.
b) Only treat a line as a label when it is a *pure* bold label — require the bold to span the whole trimmed line (e.g. `/^\*\*(.+?)\*\*\s*:?$/`) — so a bolded list item is not mistaken for a heading.
c) Peek at the next non-blank line before terminating on a non-marker bold line; stay in-section when it is itself a list item.

### Issue 44 — SHOULD FIX — `comparisonMentionsPaper` searches the whole artifact text, so a paper "takes part in" comparisons it is only mentioned in (or whose text merely contains a token of its name)

The docstring says a comparison names its participants "in its 'Papers under comparison' table", but the implementation never reads that table: it lowercases the entire artifact and does a raw substring search over the paper's slug/id/card path/identifiers/title.

**Location:** `frontend/src/lib/paperComparison.ts:283-297` (`comparisonMentionsPaper`; the `haystack.includes(needle)` test at `:292` and the title test at `:296`); consumer `frontend/src/components/papers/PaperWorkspace.tsx:137-141` (`relevantComparisons = comparisons.items.filter((item) => comparisonMentionsPaper(item.content, paper))`).

**Why it is a problem:** Every needle (slug, id, `scheme:value`, and the trimmed title) is tested with `String.includes` against the whole document — the participants table, matrix cells, fairness notes, gaps and verdict prose — with no table scoping and no word boundary. So any textual occurrence attributes the comparison to a paper that is not a participant, and the Compare section then renders that matrix *instead of* the paper's own `comparison.md`. Verified by executing the module: for a comparison whose participants are RoBERTa + GPT but whose fairness note reads "RoBERTa builds on BERT but is compared only with GPT here", `comparisonMentionsPaper(content, {id:"P-002", slug:"bidirectional-encoders", title:"BERT", …})` returns `true`, so the BERT paper's Compare section shows a RoBERTa-vs-GPT comparison (a paper with slug `bert`/title `BERT` collides on the 4-char title needle; a short common title such as `Attention` collides on almost any transformer text). Identifier substrings collide too (`arxiv:1706.0376` ⊂ `arxiv:1706.03762`). No test covers a non-participant mentioned in prose.

**Suggested fix:**
a) Scope the search to the parsed participants table: use `parseComparison(content).papers` and compare each row's id / short name / citation to the paper's id, slug and `scheme:value` identifiers with normalised **equality**, not `includes`.
b) Or keep the content-wide search but make every needle a bounded token (`new RegExp('\\b' + escapeRegExp(needle) + '\\b', 'i')`) and stop using the bare `title` as a content-wide needle.
c) Or restrict needles to the unambiguous identity forms only (`id`, `slug`, `scheme:value`) and drop title/substring matching, so an unmatched comparison simply does not appear in the paper's Compare section (fail-closed), matching the documented intent.

---

## Notes on items examined and deliberately **not** raised

- **Path containment / atomic writes** in `core/papers/writer.go` are sound: every target is symlink-resolved and re-checked via `pathutil.IsWithinPath` before staging; staging failure leaves targets untouched; `ParseLibraryDir` skips symlinked dirs. `ValidSlug` correctly rejects `/`, `\`, `..`, leading `.`, and control/space characters.
- **Enum normalization / DTO shape**: `PaperDTO` collections are non-nil (`nonNilSlice`), `pinned_papers` is non-nil (`applyResearchPins`), and `frontend/src/api/papers.ts` mirrors the Go enums and folds out-of-set values to `''`.
- **`papers:changed` wiring**: the event constant, catalog entry, event map, payload type guard, and both watcher emit sites are consistent.
- **Zustand selector stability** (React #185): every `useStore` selector in the in-scope files returns a primitive or a direct store reference; derived collections use `useMemo`. **Zoom-safety**: no raw viewport units / `*-screen` utilities; the one floating panel routes through `useCursorMenuPosition`.
- **`switchProjectSetupWatcher`** pre-creating `<workspace>/.research/papers` and `/comparisons` on every project switch is intentional per the path-helper docs ("created lazily by the writer layer … and by the watcher setup"); empty directories are invisible to `git status`, so this was not raised.
- **Vendored Python** (`core/papers/skills/study-paper/scripts/*.py`): no `eval`/`exec`/`subprocess`/`shell=True`; all HTTP calls carry timeouts; `_test_literature.py` is underscore-prefixed and therefore excluded from the `//go:embed` bundle (a repo-only dev helper).
- **Backend path containment** (other than Issue 18): the new paper RPCs route every read/write through `config.IsWithinPath`, `config.PaperLibraryPathIn`, and `config.ComparisonsPathIn`; no other inline prefix/`Rel` containment was introduced. Watcher roots (`activePapersRoot`/`activeComparisonsRoot`) are all read/written under `activeProjectMu`.
- **Go error wrapping / logging**: the new backend and `core/papers` files wrap with `%w`/`%q`, use `errors.New` only for static sentinels, and log via `log/slog`; no violations found.

### Explicitly examined and excluded as CONSIDER (out of scope)

- `ensurePaperPinned`'s module-level `pinEnsuresInFlight` set is keyed by paper id only (`paperStore.ts:435`); ids are per-project (`P-001` recurs), so a theoretical cross-project race could skip an auto-pin. Trigger is very narrow and no incorrect data results.
- `lib/literatureGraph.ts`'s `literatureToGraph` can emit a self-loop/2-cycle when a work's identity equals the seed's or appears in both predecessor and citing sets; `literatureGraph.test.ts` documents the same-node reuse as intended.
- `specs/domains/papers.md` labels the Literature control "Run lookup" while the shipped button reads "Refresh" (`PaperLiterature.tsx`, `data-testid="literature-run"`); the sibling `desktop-frontend.md` uses "Refresh" correctly — a wording nit.
- `core/papers/model.go:147-155` `NormalizeMode`'s vacuous `switch` was examined; because two independent passes raised it as a misleading-contract (dead-logic) issue it is recorded as **Issue 67** rather than excluded here.
- `frontend/src/components/papers/paperSections.ts:17-23` mixes languages: `'Обзор'`/`'Заметка'`/`'Источник'` sit beside `'Appraisal'`/`'Compare'`/`'Flashcards'`/`'Literature'`, while the rest of the new UI (badges, buttons, empty states) is English. A consistency nit (not a behavioral defect), so it is not raised; if RU is the product language, the whole surface should route through one localization layer.
- `core/papers/writer.go:243-253` `writeFilesAtomic` is documented "atomic-ish": the resolve and staging phases do leave every target untouched on failure (verified), but a failure on the 2nd/3rd `os.Rename` of a multi-file group would leave a mixed new/old set. Every current caller writes a single file per call (`WritePaperMD`/`WriteFlashcards`/`RecordCardReview`), so the partial-write window is not reachable today — a nit.
- `backend/frontend_api_project.go:585-592` (+ `backend/frontend_api_papers.go:361`) — with RESEARCH on, `<research-root>/papers/` is nested inside the watched research tree, so a leaf paper edit emits both `research:file_changed` and `papers:changed`; the extra event only triggers a redundant research-graph refetch (no incorrect data).
- `backend/frontend_api_papers.go:492-501` (`paperCardRelPath`) and the analogous research helper — when `filepath.Rel` fails or returns `.`/`..`, the fallback is the bare base name, yielding a pin path without the `papers/` prefix; unreachable while both operands are validated upstream (a defensive branch).
- `backend/frontend_api_research.go:461-487` vs `:236-243` — a RESEARCH toggle with a root change can re-watch the default `papers/`/`comparisons/` leaves and then re-add the tree watch, leaving possibly-duplicate leaf watches (spurious `workspace:tree_changed` only; sibling of Issue 54).
- `frontend/src/lib/papersByHypothesis.ts:130-138` — during the initial research-status fetch (`status?.root` undefined) every `research_ids` entry folds to `dangling`, so `DanglingPaperLinks` can flash a false "unknown hypothesis" banner until the status arrives — a transient display nit.
- `frontend/src/components/papers/paperActions.ts:144` (`COMPARISONS_SUBDIR`) vs `frontend/src/components/papers/useComparisons.ts:20` (`COMPARISONS_DIRNAME`) — the `comparisons` subdirectory name is declared twice; a single-source-of-truth/drift risk versus the backend's `ComparisonDirName`.
- `frontend/src/components/papers/paperActions.ts:100-113` (`cardDepthRank`) — folds the legacy `survey` mode onto the shallowest rung (rank 0), so a `survey` card "deepens" into `review`; documented as intentional in the comment/test.
- `frontend/src/components/research/index.tsx:150-152` — the RESEARCH `error` banner is also rendered in the Papers segment, so an unrelated research error can appear inside the independent paper-library view.
- `frontend/wailsjs/go/models.ts` (diff) — the regenerated file also reorders `backend.VectorIndexSettingsResponse` / `ConfigResponse.vector_index` relative to `ModelProfilesSettingsResponse`, which this change set does not touch (`backend/frontend_api_config.go` is unchanged). Harmless at runtime (JSON tags drive the wire shape) but it suggests generator churn; worth a `wails build` drift check.
- `core/papers/skills/study-paper/references/genres.md:24,248` — uses "Survey / review" as a paper *genre* name, which reads as the (non-existent) `survey` mode token and feeds the Issue 59 vocabulary confusion; the genre sense is correct in context.

---

## Issues 45–57 (third, independent pass)

### Issue 45 — MUST FIX — `fetch_paper.py`'s `_parse_meta(html)` parameter shadows the module-level `import html`, so every URL lookup dies with an uncaught `AttributeError`

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:349` (`def _parse_meta(html):`), `:356` (`values.setdefault(key, html.unescape(content).strip())`), `:43` (`import html`); the caller chain `:146` (`classify`) → `:411` (`resolve`) → `:368-378` (`lookup_url` → `meta = _parse_meta(html)`); the guard that should absorb it, `:424` (`except (ResolveError, ValueError, KeyError, TypeError)`), and `:558-` (`main`, which catches only `NetError`/`ResolveError`).

**Why it is a problem:** The function's parameter is named `html`, which shadows the module imported at `:43` inside the function body, so `html.unescape(...)` at `:356` calls `.unescape` on a **string** → `AttributeError: 'str' object has no attribute 'unescape'`. `classify()` returns `"url"` for anything starting with `http(s)://` (a bare `https://arxiv.org/abs/1706.03762` also lands here — the arXiv regexes require the id at the end / no `.org`), `resolve()` then runs it through `attempt("landing page", …)` at `:446`, and `attempt` does **not** catch `AttributeError`; `main`'s handlers don't either. Net effect: the script's own documented usage — `python3 fetch_paper.py https://www.nature.com/articles/nature12373` (docstring `USAGE`) — crashes with an uncaught traceback instead of the documented exit codes, for any real landing page carrying a `<meta … content="…">` tag. No test covers the URL path. Verified by importing the shipped module: `_parse_meta('<meta name="citation_title" content="A &amp; B">')` raises exactly `AttributeError: 'str' object has no attribute 'unescape'`.

**Suggested fix:**
a) Rename the parameter (`def _parse_meta(page_html):`) and keep `html.unescape(...)`.
b) Keep the name and alias the import (`import html as html_mod`; `html_mod.unescape(content)`), or reference it via `importlib`/a module handle.
c) Additionally widen `attempt`'s tuple at `:424` to include `AttributeError` (or `Exception`) so a helper regression degrades to a note rather than a traceback.

### Issue 46 — SHOULD FIX — `parseComparison` resolves its sections by matching *any* heading title, so the model-authored H1 topic can hijack the lookup and silently drop a section (and the whole matrix)

**Location:** `frontend/src/lib/paperComparison.ts:88` (`findSection`), `:246-249` (`fairnessSection`/`agreementsSection`/`gapsSection`/`verdictSection`), `:237-238` (the `papersSection` fallback).

**Why it is a problem:** `findSection` returns the first section (document order) whose *title* matches an **unanchored** regex. The H1 is `# Paper Comparison Matrix — <model-authored topic>`, and only the matrix lookup is anchored (`:243`); the agreements/fairness/gaps/verdict lookups are not, so a topic containing `agree`/`disagree`/`fairness`/`gaps`/`verdict` makes the **H1** win. The H1's body is the intro/blockquote, not the section's table, so the section parses to empty and its table is dropped. The same hazard makes `# Comparison Matrix — <topic>` (the template title with `Paper ` dropped) resolve `matrixSection` to the H1, so `columns`/`matrix` come out empty and `hasMatrix` is false. Verified by executing the module (`npx tsx`): topic `Do the papers agree on the mechanism?` → `agreements: 0`; topic `Closing the gaps in long-context modeling` → `gaps: 0`; `# Comparison Matrix — Sparse vs dense` → `columns: 0, matrix: 0, hasMatrix: false` — versus the neutral topic which yields `agreements: 1, gaps: 1` and a populated matrix. No test uses a topic containing a section keyword.

**Suggested fix:**
a) Restrict section resolution to non-title headings (`section.level > 1`).
b) Anchor the section regexes to the template's numbering (e.g. `/^(?:\d+[.)]\s*)?agree/i`), as the matrix lookup already does.
c) Resolve sections from an explicit ordered list of the numbered headings rather than a title-wide regex search.

### Issue 47 — SHOULD FIX — `NormalizeStage`/`NormalizeGrade` do not strip Markdown emphasis, so the backend and the UI disagree on the same deck (contradicting the file's stated mirror contract and the spec)

**Location:** `core/papers/flashcards.go:79-101` (`NormalizeStage` at `:79-88`, `NormalizeGrade` at `:92-101`, both keyed on `strings.ToLower(cleanLine(raw))`); `core/papers/parser.go:585` (`cleanLine` trims whitespace/CR only) versus `frontend/src/lib/flashcards.ts:63-68` (`norm()` = `toLowerCase().replace(/[*_`]/g, '').trim()`); the parity claim in `specs/domains/papers.md:144`.

**Why it is a problem:** The frontend applies `norm()` to **cell values** as well as headers; the Go copy strips emphasis only from *headers* (`columnIndex` does `strings.Trim(h, "*_` ")`), not from values. The flashcards.go header asserts "Everything here mirrors the frontend's lib/flashcards.ts … so the review a reader sees and the review the backend records agree", and `papers.md` repeats the parity claim. A deck whose Stage/Grade cells carry emphasis therefore diverges: verified with a Go test, `NormalizeStage("**review**") == "new"` and `NormalizeGrade("`good`") == ""` while the UI reads `review` / `good` for the same cells. The drift is not cosmetic: `ApplyReview` replays the log through `StateFromReviews` (which skips empty grades), so the appended `Next due` and the rewritten `Stage` are computed from the wrong rung and persist the divergence into the artifact. `NormalizeVerdict`/`NormalizeReading`/`NormalizeConfidence` in the same package *do* strip `"*_` ."`, so this is also internally inconsistent. The same gap applies to `NormalizeMode` (`core/papers/model.go:147-159`), which lower-cases/trims only (no emphasis strip): a hand-authored `mode: **review**` is not folded onto `ModeReview` (so the badge renders blank), even though the sibling `reading`/`verdict`/`confidence` normalizers would fold it.

**Suggested fix:**
a) Strip the same set in both normalizers: `strings.ToLower(strings.Trim(cleanLine(raw), "*_` "))`.
b) Extract one shared value-normalizer in `parser.go` and call it from every value fold (as already done for verdict/reading/confidence) so header and value folding cannot drift again.

### Issue 48 — SHOULD FIX — `RecordCardReview` creates the library root and the paper directory before checking the deck exists, so a rejected review still mutates the filesystem

**Location:** `core/papers/writer.go:183` (`ensurePaperDir(libraryRoot, slug)`), which precedes `:187` (`os.ReadFile(target)`), inside `RecordCardReview` (`:179-`); `ensurePaperDir` at `:215-` (`os.MkdirAll(libraryRoot, …)` + `os.MkdirAll(<root>/<slug>, …)`).

**Why it is a problem:** The function documents the opposite ordering — "An unknown card id (no row), an unknown grade, or a deck with no review-log table is rejected **before any write** — leaving the file byte-for-byte unchanged". Because `ensurePaperDir` (which `MkdirAll`s both the root and the paper dir) runs first, a review of a slug whose deck is missing (a mistyped/renamed/stale slug) returns the "no deck to review" error but leaves a **stray empty `<root>/<slug>/` directory** (and may create the library root). The sibling validators run in the opposite order — `ApplyReview` checks everything before mutating — and the writer header promises "a failure leaves the prior content byte-for-byte unchanged". The existing `TestRecordCardReviewRejectsMissingDeck` asserts only the returned error, so the side effect is unpinned. Reachability is limited today (the production caller at `backend/frontend_api_papers.go:288` passes `filepath.Base(rec.Dir)` for an already-parsed record, so `MkdirAll` no-ops), i.e. a contract defect at the API boundary rather than end-user data loss.

**Suggested fix:**
a) Read the deck first and create the directory only after → move the `os.ReadFile(target)` above `ensurePaperDir` (use the directory `ensurePaperDir` returns for the write).
b) Split `ensurePaperDir` into a pure `resolvePaperDir` (no `MkdirAll`) plus explicit creation, and have `RecordCardReview` call only the resolver + the read before any creation.
c) Keep the order but add a test asserting that a rejected review does **not** create the directory (and document the side effect).

### Issue 49 — SHOULD FIX — `listItemsOfSection` tests its marker against the H1 (which embeds the paper title), so a title containing a marker keyword harvests unrelated bullets

**Location:** `frontend/src/lib/paperWidgets.ts:287-` (`listItemsOfSection`; the heading-marker branch at `:297-303`, `stopLevel = level` at `:300`, the termination test `if (inSection && level <= stopLevel) inSection = false` at `:303`).

**Why it is a problem:** The note/appraisal H1 is `# Reading Note — <paper title>` / `# Critical Appraisal — <paper title>`. `listItemsOfSection` runs its marker (`/uncertain|unknown|open question|limitation/` for uncertainties, `/red flag|concern|weakness/` for red flags) against **every** heading's text, including that H1. When the paper title contains a marker word (e.g. an ML paper titled "Uncertainty Quantification in Deep Learning"), the H1 matches → `inSection = true, stopLevel = 1`; because no later heading satisfies `level <= 1`, the section never terminates and **every subsequent bullet up to the first bold-led line** is collected as an uncertainty/red-flag. Verified by executing the module (`npx tsx`): for such a note, `parseUncertainties` returns four items including three spurious §2/§6 bullets. This is distinct from Issue 43 (which is about a bold-led list *item* truncating the section) — here the *entry* marker fires on the title.

**Suggested fix:**
a) Only enter a section on a numbered or bold **label**, not a bare level-1 heading (require `level >= 2`).
b) Terminate on any heading with `level <= stopLevel || level <= 2`.
c) Bound the section with the numbering the templates already carry (`/^##\s*\d+\./`).

### Issue 50 — SHOULD FIX — `_parse_meta` uses `setdefault`, collapsing repeated `citation_author` metas to a single author

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:356` (`values.setdefault(key, html.unescape(content).strip())`); the consumer at `:381-383` in `lookup_url` (`record["authors"] = [value for key, value in meta.items() if key in ("citation_author", "dc.creator")]`).

**Why it is a problem:** `setdefault` keeps only the **first** occurrence of each meta key, so a page that emits one `citation_author` meta per author (the standard citation-meta convention) yields a `meta` dict with a single `citation_author` entry, and the `authors` comprehension can never return more than one author. Resolving a multi-author landing page by URL therefore records `metadata.authors == [<first author only>]` — silently wrong provenance for the card the agent then writes. This is latent behind Issue 45 (the `:356` crash fires first), which is exactly why it should be fixed in the same change: once the shadowing is corrected, the crash is replaced by silent author truncation.

**Suggested fix:**
a) Accumulate repeats — `values.setdefault(key, []).append(html.unescape(content).strip())` — and flatten the author lookup, keeping first-wins for the singular keys (`citation_title`, `citation_doi`, …).
b) Special-case only the known repeatable keys (`citation_author`, `dc.creator`) into lists and leave the rest as scalars.

### Issue 51 — SHOULD FIX — `SKILL.md` documents the per-paper directory as holding "three files", omitting `flashcards.md`, which the app reads and edits at `<paper-dir>/flashcards.md`

**Location:** `core/papers/skills/study-paper/SKILL.md:198-210` ("a per-paper slug directory holding three files" + the tree listing only `paper.md`, `note.md`, `appraisal.md`, then "These three files are c0wrk's paper-library format and are read back verbatim by the app"); the Teach-mode step `:185`; the file-map entry `:329`.

**Why it is a problem:** `flashcards.md` is a first-class per-paper artifact of this feature — `core/papers/flashcards.go:25` (`FlashcardFileName = "flashcards.md"`), read/edited by `RecordCardReview` (`writer.go:179-`), and read by the UI (`frontend/src/components/papers/usePaperArtifacts.ts:32` lists `flashcards: ['flashcards.md']`). Its only backend producer is the **append-only** review RPC, which requires an existing deck. The skill that is the designated author of the deck never names its path (it mentions flashcards only as an asset *format*), so an agent following SKILL.md end-to-end for Teach mode can produce the cards in its answer but never write `<research-root>/papers/<slug>/flashcards.md` — the Flashcards panel then reports the artifact missing and the first review RPC fails with "no deck to review". (`literature.json`, by contrast, has a backend producer, so it is unaffected.)

**Suggested fix:**
a) Add `flashcards.md` to the artifact-layout tree and the "read back verbatim" sentence, with an explicit instruction that Teach mode writes the deck to `<research-root>/papers/<slug>/flashcards.md` using the `assets/flashcards.md` shape.
b) Also state the exact `<paper-dir>/flashcards.md` path next to the Teach-mode flashcards step (`:185`).
c) Extend the same block to name the **comparisons** artifact location as well — the app also watches and reads `<research-root>/comparisons/<slug>.md` (`backend/config/paths.go:404,406-410` `ComparisonDirName`/`ComparisonsPathIn`; the comparisons watcher in `backend/frontend_api_project.go:670-684`), yet SKILL.md's "Where artifacts live" names only the per-paper `papers/<slug>/` tree and never the `comparisons/` sibling. An agent running a Compare intent has no documented target path (the multi-select "Compare selected" prompt supplies it at `frontend/src/components/papers/paperActions.ts:190-197`, but the generic `buildComparePrompt` — "Compare X with the other papers in the library." — does not), so the artifact can land outside the watched tree and never surface. The same gap appears in the mode table (`SKILL.md:36-45`), which lists only Skim/Review/Implement/Teach even though the specs (`specs/domains/papers.md`, `specs/contracts/desktop-frontend.md:352`) and `assets/comparison-matrix.md` treat "the study-paper Compare intent" as artifact-producing; add a Compare row/entry (or an explicit note that Compare is dispatched via the matrix template).

### Issue 52 — SHOULD FIX — Every CODE project switch unconditionally creates `<workspace>/.research/papers` and `<workspace>/.research/comparisons`

**Location:** `backend/frontend_api_project.go:660-684` (`switchProjectSetupWatcher`: `os.MkdirAll(papersRoot, 0o755)` and `os.MkdirAll(comparisonsRoot, 0o755)`); the same pattern on the disable path at `backend/frontend_api_research.go:470-490`.

**Why it is a problem:** The watcher setup `MkdirAll`s both leaf directories for **every** real project on **every** switch, before `WatchTree`, regardless of whether RESEARCH/papers are used. Before this change, `.research` materialized only when RESEARCH was explicitly enabled. Selecting any project now creates two new directories in the user's repository; if `.research` is not ignored they show up as untracked entries in `git status` and the file tree (this change's own `.gitignore` addition covers only `__pycache__/`, not `.research/`), and the `MkdirAll` itself generates Create events that surface as `workspace:tree_changed`. The code comment justifies the creation as needed to watch a not-yet-used library, but the same goal is met by watching the nearest existing ancestor.

**Suggested fix:**
a) `os.MkdirAll(<ws>/.research, …)` once (or watch the workspace root) and let the recursive watcher auto-add `papers/` / `comparisons/` when they are first written.
b) Gate the leaf-directory creation on the feature actually being engaged rather than on every project switch.

### Issue 53 — SHOULD FIX — Unpinning a paper silently no-ops when its directory was renamed while the card kept its `id`

**Location:** `backend/frontend_api_papers.go:212-227` (`rec := lib.Get(paperID)`; `cardPath := paperCardRelPath(ctx.researchRoot, rec)`; `next, changed := togglePinnedPath(fresh.ResearchPins.Papers, cardPath, pinned); if !changed { return nil }`).

**Why it is a problem:** A pin key is the card's directory-derived relative path (`papers/<slug>/paper.md`). On unpin the function resolves `lib.Get(paperID)`; when the paper's directory was renamed (slug changed) but the card kept `id: P-001`, `lib.Get` resolves to the **new** directory, so `cardPath` is the new path, the stored pin (old path) does not match, `togglePinnedPath` returns `changed == false`, and the function returns `nil` as "already in the requested state" — a **silent success that leaves the stale pin**. The user cannot then remove the pin through any other RPC. (Issue 5 covers the different case where the whole directory is deleted, which errors instead.) Trigger: rename a pinned paper's folder (slug change) with its `id:` preserved, then unpin.

**Suggested fix:**
a) Match on the pin's path *segment* (the normalized slug) rather than the full current card path, so a rename still matches the stale entry.
b) Make unpin tolerant of an unknown/renamed paper: remove any `ResearchPins.Papers` entry whose path is under `papers/` and matches the normalized id/slug, and return `nil`.

### Issue 54 — SHOULD FIX — `EnableResearch` does not unwatch the default-root papers/comparisons watches created at switch time, leaking watches and emitting spurious `workspace:tree_changed`

**Location:** `backend/frontend_api_research.go:236-256` (`EnableResearch` sets `activePapersRoot`/`activeComparisonsRoot` to the new root and calls only `watcher.WatchTree(researchRoot)`); contrast the leaf watches added at `backend/frontend_api_project.go:660-684` and the symmetric unwatch/re-watch in `DisableResearch` (`backend/frontend_api_research.go:442-490`).

**Why it is a problem:** `fsnotify` watches persist until `Remove`/`Close`. When RESEARCH is enabled with a **custom** root while a project is active, the library moves to `<custom>/papers`, but the `<ws>/.research/papers` and `<ws>/.research/comparisons` watches added at switch time remain registered. A change under the now-inactive default tree still fires the callback, and the callback emits `EventWorkspaceTreeChanged` unconditionally (`research_scoped: false` for such a path), forcing a spurious full status refetch; the stale watches also leak for the app's lifetime. The asymmetry is notable because `DisableResearch` is careful to unwatch the tree and re-watch the default leaves. Practical impact is limited (nothing normally writes into the inactive default tree), so this is a leak/latent-spurious-event defect rather than data loss.

**Suggested fix:**
a) In `EnableResearch`, snapshot the previous `activePapersRoot`/`activeComparisonsRoot` and `UnwatchTree` them before re-watching the new root.
b) Stop watching the leaf roots entirely and watch only the research root, deriving the papers/comparisons scoping purely in `emitPapersChanged`.

### Issue 55 — SHOULD FIX — The file-tree context menu's "Study this paper…" depth picker is not re-measured, so it can open partly outside the viewport

**Location:** `frontend/src/components/layout/FileTreeContextMenu.tsx:68` (`useCursorMenuPosition(position, menuRef)`) and the picker markup at `:243-266`; `frontend/src/lib/cursorMenuPosition.ts:141-169` (the placement `useLayoutEffect` depends on `[anchor, menuRef, margin]` and reads `menu.offsetHeight` once).

**Why it is a problem:** The placement is recomputed only when the anchor's identity changes or on window `resize`. Flipping `studyOpen` swaps the menu's content for the **taller** depth picker (header + five mode rows + separator + "Back") without changing the anchor, so the flip/clamp decision is made against the stale (shorter) height. If the menu was placed low (flipped up to fit the short item list), the taller picker grows past the window bottom; the element is `position: fixed` with a pinned `top`, and `overflow-hidden` clips its own content, not the viewport — so the lower rows (including "Back") become unreachable. Trigger: in a non-git workspace, right-click a `.pdf` near the bottom of the tree, then click "Study this paper…".

**Suggested fix:**
a) Feed the content-height change back into the hook (include a `studyOpen`/`measureKey` in the effect deps, or observe the element with a `ResizeObserver`).
b) Render the depth picker as a separately measured anchored panel so the main menu keeps its measured size.

### Issue 56 — SHOULD FIX — A paper-tab dispatch failure is stored but never rendered in the surface that raised it

**Location:** `frontend/src/components/papers/PaperWorkspace.tsx:163-172` (`dispatch` → `usePaperStore.getState().setError(...)` on failure); the workspace is mounted by `frontend/src/components/fileViewer/FileViewerContent.tsx:64`; the only renderer of `paperStore.error` is `frontend/src/components/papers/PapersView.tsx:242,409`.

**Why it is a problem:** `PaperWorkspace` (the paper tab shown in the file viewer) subscribes to no error selector. Its "Go deeper" and "Hypotheses from gaps" buttons route failures (e.g. the auto-session-create rejection the code explicitly handles) into `paperStore.error`, which is rendered only by the Research panel's `PapersView` segment — a different surface. While the user stays in the paper tab, the failure is invisible; it only appears after switching to the Papers segment. Trigger: open a paper tab, click "Go deeper" while a session cannot be auto-created → nothing happens visibly.

**Suggested fix:**
a) Subscribe to `usePapersError()` in `PaperWorkspace` and render a small inline alert (mirroring `PapersView`'s `papers-error`), cleared on the next successful action.
b) Or surface dispatch failures as a `runtime_error` toast, as `AttachmentChips`/`FileTreeContextMenu` already do for the same failure class.

### Issue 57 — SHOULD FIX — `usePaperArtifacts` maps any directory-listing failure to "every section absent", so a real error looks like a paper with no artifacts

**Location:** `frontend/src/components/papers/usePaperArtifacts.ts:118-127` (the outer `catch` → `setArtifacts(allOf(absent))`).

**Why it is a problem:** A transient or permission-caused `listDirectory` failure degrades **every** section to the "missing" empty state and only logs a warning — the user sees a blank paper (no note/appraisal/flashcards/source/literature) rather than a failure, with no way to tell "this paper has no artifacts" from "the directory could not be read". Individual `readFile` failures *are* distinguished (they populate `error`), so the ambiguity is specific to the listing step. This is the artifact-level analogue of Issue 37 (unreadable library root shown as empty). Trigger: the paper directory is momentarily unlistable (I/O or permission) at load/refresh time.

**Suggested fix:**
a) Expose the listing failure as a distinct error state and render it (rather than swallowing it into `missing`).
b) Distinguish "directory does not exist" (clean empty state) from "listing failed" (error state) and surface the latter.

---

### Issue 58 — SHOULD FIX — `parser.go`'s `readFile` folds *every* read error (not just "not found") into "artifact absent", so an unreadable paper silently vanishes

**Location:** `core/papers/parser.go:981-989` (`readFile` returns `("", false)` for any error), consumed at `:1002-1014` (`parsePaperDir`: `hasPaper || hasNote || hasAppraisal`) and `:1036-` (`ParseLibraryDir`, which skips a directory carrying none of the three artifacts).

**Why it is a problem:** `readFile` collapses **any** `os.ReadFile` error — `EACCES`, an I/O error, a broken symlink, or a path that is itself a directory (`EISDIR`) — into the same `("", false)` it uses for "not written yet". `parsePaperDir` folds that into the presence flag, and `ParseLibraryDir` skips a directory whose artifacts all read as "absent". A paper whose artifacts exist but are momentarily unreadable therefore disappears from the library silently, with no log or signal, indistinguishable from a never-written paper. Its doc comment defends the conflation only for the *missing* case ("a missing optional artifact is a normal partial state"); it does not justify masking genuine I/O errors. This is the per-artifact sibling of Issue 37 (unreadable library root → empty library) and the same class as Issue 23 (a swallowed comparison read error). Trigger: an artifact file that exists but cannot be read (permissions, a corrupt/dangling symlink, or a path that is a directory) at load time.

**Suggested fix:**
a) Treat only `errors.Is(err, fs.ErrNotExist)` as "optional artifact absent" and propagate or log any other error (e.g. return it from `parsePaperDir`, which `ParseLibraryDir` already handles by skipping-with-evidence).
b) Keep the best-effort parse but emit a `Warn` (path + error) before returning `false`, so an unreadable artifact is visible rather than silent.

---

## Issues 59–61 (fourth, independent pass)

### Issue 59 — SHOULD FIX — The `mode` vocabulary documented in code, spec, and frontend (six tokens, incl. `deep`/`survey`) is partly unproducible by the skill that authors it, so two of the six tokens are unreachable

The Go comment, both specs, and the frontend union describe a six-token `mode` vocabulary that "spans both vocabularies the skill writes", but the skill's own mode menu and `paper.md` template only ever write four of them.

**Location:** `core/papers/skills/study-paper/SKILL.md:38-45` (the mode menu — Skim/Review/Implement/Teach) and `SKILL.md:237` (the `paper.md` template line `mode: skim | review | implement | teach`) vs `core/papers/model.go:140-146` (the `NormalizeMode` doc comment: "the known set spans both vocabularies the skill writes — the engagement depths (skim/deep/survey) and the skill's own modes (review/implement/teach)"), `core/papers/model.go:60-75` (`ModeDeep`/`ModeSurvey` constants), `specs/domains/papers.md:139` ("`Mode` spans both the engagement depths (`skim`/`deep`/`survey`) and the study-paper skill's own depths (`review`/`implement`/`teach`)"), `specs/contracts/desktop-frontend.md:348` (`mode` (`skim`/`deep`/`survey`/`review`/`implement`/`teach`)), and `frontend/src/api/papers.ts:65` (`PaperMode` union incl. `'deep'`/`'survey'`, with `PAPER_MODES` at `:152-153`).

**Why it is a problem:** The bundled skill is the designated author of `paper.md`, and it writes exactly four mode tokens — its mode menu defines Skim/Review/Implement/Teach and its `paper.md` template declares `mode: skim | review | implement | teach`. It contains **zero** whole-word occurrences of `deep` or `survey` (`grep -ciE '\b(deep|survey)\b' core/papers/skills/study-paper/SKILL.md` → `0`; the only matches are the substrings `deeply`/`deeper`). Yet `model.go`'s comment asserts the skill writes the engagement depths `skim`/`deep`/`survey`, and both specs plus the frontend union publish all six as the contract, with the spec pinning the "go deeper" ladder and the mode badge to "the same vocabulary". So `ModeDeep`/`ModeSurvey` (and the frontend `'deep'`/`'survey'` union members) are reachable only via hand-authored or out-of-pack cards, never via the shipped skill — a future reader (or the spec-driven "go deeper" ladder) reasoning from the comment/spec will expect the skill to emit tokens it cannot. Trigger: any attempt to exercise the documented `skim → deep → survey → …` progression from the bundled skill; the two middle tokens never appear. This is the same class of skill-vs-doc drift the report already rates SHOULD FIX (`SKILL.md`'s artifact list omitting `flashcards.md` — Issue 51; `SKILL.md`'s `--arxiv` flag the script rejects — Issue 40), and every file involved is introduced or modified by this change set.

**Suggested fix:**
a) Reconcile the docs to the skill: correct the `model.go` comment and both specs to the four tokens the skill writes (`skim`/`review`/`implement`/`teach`), keeping `ModeDeep`/`ModeSurvey` (if retained for hand-authored cards) explicitly described as non-skill-authorable tokens rather than "the skill writes".
b) Or make the claim true by adding the engagement depths to the skill: extend `SKILL.md`'s mode menu and its `paper.md` template to `skim | deep | survey | review | implement | teach` with a one-line definition each, so all six documented tokens are producible.
c) Or, minimally, drop `'deep'`/`'survey'` from the frontend `PaperMode` union + `PAPER_MODES` and the `ModeDeep`/`ModeSurvey` constants and treat an out-of-set card value through the existing `NormalizeMode` passthrough/`""` fold, removing the contradiction without changing the skill.

### Issue 60 — SHOULD FIX — ADR-047 attributes `comparison-matrix.md` parsing to the backend, but the comparison parser is frontend-only and the sibling contract says comparisons have no RPC

The ADR's Context lists the `comparison-matrix.md` asset among the additions "that the backend parses", which contradicts where the parser actually lives and what the linked contract states.

**Location:** `specs/decisions/047-papers-library.md:13` ("It also needs c0wrk-specific additions: the `flashcards.md` and `comparison-matrix.md` assets that the backend parses, and the `literature.py` helper that the backend invokes.") vs `frontend/src/lib/paperComparison.ts` (`parseComparison`, the sole comparison-matrix parser), `specs/contracts/desktop-frontend.md:352` ("Comparisons have no dedicated RPC: the frontend reads them through the workspace file RPCs (`ListDirectory`/`ReadFile`) under the same research root."), and `core/papers/skills/study-paper/assets/comparison-matrix.md`.

**Why it is a problem:** Only `flashcards.md` is parsed by the backend (`core/papers/flashcards.go` — `ParseFlashcards`); the comparison-matrix parser is the frontend's `frontend/src/lib/paperComparison.ts`, and the backend has no comparison parser at all (a search for `comparison-matrix` / `ComparisonMatrix` across `*.go` hits only the embedded-asset assertion in `core/papers/skillpack_test.go:132` and a path-helper comment in `backend/config/paths.go:409`; no `func ...Comparison` parser exists in `core/` or `backend/`). The linked contract explicitly documents the opposite arrangement (no comparison RPC; the frontend reads/parses). A reader trusting the ADR will look for — or add — a backend comparison parser that the design deliberately does not have, mis-scoping future work to the wrong layer. ADR line 19 is fine ("the seeded skill can carry the c0wrk-specific assets …"), so the defect is confined to the parse attribution on line 13. This is documentation-vs-implementation drift of the same kind as Issue 40/51, and both the ADR and the parsing code are introduced by this change set, so it is in scope.

**Suggested fix:**
a) Reword line 13 to attribute parsing correctly: "the `flashcards.md` asset that the backend parses (`core/papers/flashcards.go`) and the `comparison-matrix.md` asset that the frontend parses (`frontend/src/lib/paperComparison.ts`)", leaving the `literature.py` clause intact.
b) Or drop the "that the backend parses" attribution entirely and defer to `specs/domains/papers.md` / `desktop-frontend.md` for which layer parses which artifact, so the ADR's Context no longer makes a layer claim it does not own.

### Issue 61 — SHOULD FIX — `applyPapersChanged` guards an event against the *loaded* library's project, which lags the *active* project during a switch, so a late old-project event starts a fetch that supersedes the new project's load and leaves the wrong library on screen

`refresh` scopes itself to the active project (`projectStore`), but the event path scopes itself to the loaded library's `projectId` (`paperStore`), and the two diverge for the whole duration of a project switch — long enough for an old-project event to win.

**Location:** `frontend/src/stores/paperStore.ts:489-494` (`applyPapersChanged`: `const { projectId } = usePaperStore.getState(); if (projectId === null || event.project_id !== projectId) return false; await fetchPaperLibrary(projectId)`), reaching `frontend/src/stores/paperStore.ts:363-375` (`fetchPaperLibrary` ticket `++latestFetch`) and `:152-183` (`loadLibrary`, which is what sets `projectId: library.project_id`); vs `frontend/src/hooks/usePapersEvents.ts:21-37` (`refresh` reads `activeProjectId` from `projectStore` and awaits `fetchPaperLibrary(activeProjectId)`) and `:48-59` (well-formed payloads go straight to `applyPapersChanged(data)`).

**Why it is a problem:** On a project switch p1→p2 the hook's `refresh` effect fires immediately and starts `fetchPaperLibrary('p2')` (ticket 2), but the store's `projectId` is only updated when that fetch's `loadLibrary` resolves (`paperStore.ts:177`), so until then the store still says `'p1'`. A `papers:changed` event for **p1** that arrives inside that window (a queued fsnotify event from the previous project's still-registered watcher, or a write the agent is still making there) passes the `event.project_id !== projectId` guard — because `projectId` is still `'p1'` — and calls `fetchPaperLibrary('p1')` with the **newer** ticket (3). p2's fetch then resolves with `ticket(2) !== latestFetch(3)` and is dropped (`:371`), p1's resolves and applies (`projectId = 'p1'`, p1's papers), and the hook's effect does not re-run (its deps `activeProjectId`/`isNoProject` are unchanged), so the Papers panel shows p1's library while the app is on p2 until some later event or another switch. This is a distinct defect from Issue 8: that one is the `reset()`/No-Project path not bumping the ticket (so an in-flight fetch applies after a reset); here no reset happens, the ticket **is** bumped (by p2's fetch), yet the wrong project still wins because the event guard reads the stale loaded project instead of the active one. The `fetchPaperLibrary` doc's "last-write-wins by initiation order" assumes every initiation targets the same project, which is exactly what fails here. Trigger: switch projects while the previous project's watcher is still delivering events (agent mid-write / queued fsnotify), or any `papers:changed` that lands during the switch RPC window.

**Suggested fix:**
a) Guard the event against the **active** project (the source `refresh` already uses): read `useProjectStore.getState().activeProjectId` in `applyPapersChanged` and compare `event.project_id` to it (falling back to the loaded `projectId` only when no project is active), so a departed project's event is ignored.
b) Or invalidate in-flight fetches on switch: bump the ticket in the hook's `refresh`/project-change path (or expose a store `invalidate()`) so the switch immediately makes every pre-switch fetch — including one started by a late event for the old project — stale, and make the event path fetch the active project rather than the loaded one.
c) Or record the active project in the store on switch (set `projectId` from `projectStore` synchronously) so the loaded/active identities cannot diverge during the load window; then the existing guard is correct.

---

## Issues 62–66 (fifth, independent pass)

### Issue 62 — SHOULD FIX — `PapersView` renders the previous project's papers for the whole switch window (the list is gated only on loading-and-empty, not on the loaded project matching the active one)

The panel falls through to the list whenever `papers.length > 0`, so while a project switch's `GetPapers` is in flight it shows the departed project's cards under the new project.

**Location:** `frontend/src/components/papers/PapersView.tsx:239-247` (reads `usePapers()`), `:351` (the `isNoProject || activeProjectId === null` guard — No-Project only), and `:460-472` (the render: `isLoading && papers.length === 0 ? Loading : papers.length === 0 ? Empty : <ul>{papers.map(...)}`); store side `frontend/src/stores/paperStore.ts:152-183` (`loadLibrary` is what replaces `papers`, i.e. only when the new fetch resolves).

**Why it is a problem:** On a project→project switch, `activeProjectId` changes immediately (so `refresh` starts `fetchPaperLibrary(newId)`), but the store's `papers` array still holds the **old** project's cards until that fetch resolves. Because the render condition is `isLoading && papers.length === 0`, a non-empty stale array skips the "Loading…" branch and renders the previous project's papers — for the whole duration of the `GetPapers` RPC — together with any selection ids that happen to collide across projects (`selectedSet.has(paper.id)`). The `activeProjectId === null`/`isNoProject` guard covers only the No-Project case. This is distinct from Issue 8 (an in-flight fetch surviving a `reset()`) and Issue 61 (the event-path fetch race): here nothing is racing — the view simply renders the store's stale cross-project array. Trigger: switch from a project that has papers to another that has papers; stale cards are visible until the RPC lands. Because the stale rows remain **interactive**, an action taken in that window runs against the store's still-previous `projectId` (e.g. `pinPaper` → `togglePaperPin` → `SetPaperPinned(<old project>, …)`), committing the click to the departed project; the pin/compare handlers have no live-project check.

**Suggested fix:**
a) Gate the list on the loaded project matching the active one: read `selectPapersProjectId` and render Loading/Empty until `loadedProjectId === activeProjectId` (the list — and the empty state — then never reflects a foreign library).
b) Or clear/replace `papers` optimistically when the requested project differs from the loaded one (e.g. `reset()`/a "pendingProjectId" marker in the hook before the fetch), so the stale array cannot be rendered.
c) Also reset `selectedIds` when the loaded project id changes, so a colliding id cannot stay selected across the switch.

### Issue 63 — SHOULD FIX — The frontend's parsed `Flashcard.stage` (the deck's Stage column) is never consumed, so the deck's recorded Stage and the UI's derived badge can silently disagree

`parseCardTable` binds the Stage column to `card.stage`, but the only renderer derives the badge from the review log and ignores it.

**Location:** `frontend/src/lib/flashcards.ts:40` (`stage: FlashcardStage`) and `:143`/`:158` (populated from the Stage column, and asserted by `frontend/src/lib/flashcards.test.ts`); the sole consumer `frontend/src/components/papers/FlashcardsReview.tsx:115` (`const state = stateFromReviews(cardReviews)`) and `:169-170` (renders `state.stage`).

**Why it is a problem:** No production code reads `card.stage` — a `grep` for `.stage` across `frontend/src` (excluding tests) finds only `state.stage` (the derived value), `progress.stage`, and an event type guard; the parsed field is dead surface that the test suite keeps alive. `specs/domains/papers.md:144` declares the stage DERIVED from the interval index and "never stored independently, so the two never drift", yet the deck file does carry a Stage column that the parser binds to a field nothing reads. A deck whose Stage column disagrees with its log (a partially written deck, or a hand-authored/hand-edited deck that sets Stage without a matching log row) renders the derived value with no indication that the file says otherwise. This is the same class as Issue 38 (dead surface) and Issue 12 (dead state with doc drift), here in `lib/flashcards.ts`.

**Suggested fix:**
a) Reconcile in the UI: when the log yields `new`, fall back to `card.stage` (or take the deeper of the two) so a recorded-but-unlogged Stage is reflected.
b) Or, if the review log is the single source of truth (as the spec says), drop `stage` from `Flashcard`/`parseCardTable` and its test assertion, and note the deck's Stage column is write-only.
c) Or render the recorded Stage as a separate "recorded" badge beside the derived one so the two are never conflated.

### Issue 64 — SHOULD FIX — `fetch_paper.py`'s documented exit codes contradict its behavior: the documented "3 unexpected problem" is unreachable and the real exit 3 (an `--out` write failure) is undocumented

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:29-33` (doc block `0 resolved successfully / 1 input unrecognised, or it could not be resolved / 2 network unavailable / 3 unexpected problem`) vs `:587` (`return 2`), `:589`/`:592` (`return 1`), `:601` (`return 3`, reached **only** from the `except OSError` around writing `--out`), `:605` (`return 0`).

**Why it is a problem:** Nothing in the script returns 3 for a generic "unexpected problem" — an uncaught exception makes CPython exit 1, which the doc reserves for "input unrecognised, or it could not be resolved" — while the write-failure path that actually returns 3 is documented nowhere. An agent/operator scripting against the documented contract (SKILL.md directs the agent to run this helper) will misread a `--out` write failure as an undocumented code and an internal crash as "could not be resolved", and will never observe the documented "unexpected problem" signal. Same doc-vs-code drift class as Issue 40 (`--arxiv`) and Issue 45, and the sibling of Issue 34 (the backend's `literature.py` exit-code mapping).

**Suggested fix:**
a) Correct the doc block: document 3 as "could not write the `--out` file" and state that an unexpected error exits 1.
b) Or add a top-level `except Exception` that returns 3 so the documented contract is true.
c) Or align the block with `literature.py`'s exit-code table (Issue 34) so the two helpers cannot drift.

### Issue 65 — SHOULD FIX — The Go and frontend flashcard parsers disagree on a deck whose cards table has no `id` column, so the same deck yields different card identities across the boundary

**Location:** `core/papers/flashcards.go:149` (`idIdx := colOr(t.Header, 0, "id")` — positional fallback to column 0) and `:177`/`:420` (same fallback in `parseReviews`) vs `frontend/src/lib/flashcards.ts:142` (`const idIdx = pickColumn(t.header, ID_COL)` — no fallback, so an absent id column resolves to `-1` and `cell(row, -1)` yields `''`).

**Why it is a problem:** For a cards table whose header carries no `id`-matching cell, the Go parser binds `Card.ID` to cell 0 (via `colOr`'s fallback — for a `| Front | Back |` deck that is the Front text), while the frontend's `pickColumn` returns `-1` and the id becomes `''`. The package header ("Everything here mirrors the frontend's lib/flashcards.ts … so the review a reader sees and the review the backend records agree") and `specs/domains/papers.md:144` assert parity, yet the two sides derive different card identities from the same input, so the backend's review-row matching (keyed on the card id) and the UI's card keys address different cards. Reachable with a hand-authored/rollup deck that omits the id column (the shipped asset carries one, but SKILL.md lets the author "copy the block per card, or add rows to the table"). Same "backend and UI disagree on the same deck" class as Issue 47.

**Suggested fix:**
a) Drop the positional fallback on the Go side — `idIdx := columnIndex(t.Header, "id")` (or `colOr(t.Header, -1, "id")`) — so an absent id column is treated as absent, matching the frontend.
b) Or give the frontend the same fallback (`pickColumn(t.header, ID_COL, ...)` defaulting to 0) so both alias cell 0 consistently.
c) Add a Go+TS parity test over a deck whose cards table has no `id` column, asserting equal card ids.

### Issue 66 — SHOULD FIX — `RenderPaperMD` silently swallows a `yaml.Marshal` failure and emits a card with an empty front matter instead of surfacing the error

**Location:** `core/papers/writer.go:67-72` (`data, err := yaml.Marshal(out); if err != nil { /* comment */ data = nil }`), then `:73-76` (`b.WriteString("---\n"); b.Write(data); b.WriteString("---\n")`).

**Why it is a problem:** The marshal error is neither returned nor logged; on failure the writer still returns `"---\n---\n"` and reports success, so a future field that cannot marshal (an unsupported type, a `MarshalYAML` that errors) would silently emit a card whose entire front matter is missing — every identity field dropped, no error, no log. The adjacent comment documents the guard but not the swallow, so the failure is invisible until a card fails to re-parse. This is the same "error silently folded into a plausible-looking artifact" pattern the report rates SHOULD FIX for `readFile` (Issue 58) and `commitFlashcardReview` (Issue 30).

**Suggested fix:**
a) Return the error: change `RenderPaperMD` to `(string, error)` and propagate it through `WritePaper`/its caller.
b) Or keep the string signature but log a `Warn` before returning and return an explicit sentinel the writer rejects on.
c) Or delete the dead guard (the comment asserts the shape cannot fail) so there is no silent path at all.

---

## Issues 67–68 (sixth, independent pass)

### Issue 67 — SHOULD FIX — `NormalizeMode` is a vacuous `switch` whose shape implies a normalization it does not perform

**Location:** `core/papers/model.go:147-155` (`NormalizeMode`).

**Why it is a problem:** The `switch Mode(s) { case ModeSkim, ModeDeep, ModeSurvey, ModeReview, ModeImplement, ModeTeach: return Mode(s); default: return Mode(s) }` returns the identical value from every arm, so the whole construct is equivalent to `return Mode(s)`. It reads as if it validates or canonicalizes the six "known" tokens, but it neither maps nor rejects anything, so a maintainer will "fix" or extend the case list expecting a behavior change that cannot occur — the function's shape (an enumerated known set) contradicts its actual behavior (a pure passthrough). Zero behavioral impact today (unknown tokens are passed through anyway), which is why it is the same "dead logic that implies a contract" class as Issue 12 and Issue 38; `TestNormalizeModeAndReading` (`model_test.go`) passes with or without the case list, so the dead arm is unpinned.

**Suggested fix:**
a) Collapse the switch to `return Mode(s)` and let the doc comment carry the "unknown values pass verbatim" contract.
b) Or make it meaningful: map the known tokens through a lookup table with `default: return Mode(s)` for foreign values.
c) Or delete the case list together with the comment's "known set" claim (pairs with Issue 59).

### Issue 68 — SHOULD FIX — `SKILL.md`'s "no research root" fallback writes to `<workspace>/papers/<slug>/`, a path no RPC, watcher, or panel ever reads

**Location:** `core/papers/skills/study-paper/SKILL.md:208-212` ("If no research root is available, ask the user where to write, or fall back to `papers/<slug>/` inside the current workspace") vs `backend/frontend_api_papers.go:331-336` (`effectiveResearchRoot`) + `:298-308` (`papersRootForProject`) + `backend/config/paths.go:378-390` (`PaperLibraryPathIn`/`PaperLibraryPath`).

**Why it is a problem:** The app's library root is always `<effective research root>/papers`, where the effective root is the persisted `ProjectInfo.ResearchRoot` when set, else the default `<workspace>/.research` (`effectiveResearchRoot`) — so the RPCs, the watcher (`emitPapersChanged` keys off the effective root's `papers/`), and the panel read `<workspace>/.research/papers/<slug>/`, never a workspace-relative `<workspace>/papers/<slug>/` (no code path reads a bare `papers/` beside the workspace root). The doc's premise is also off: `effectiveResearchRoot` never returns "no root" for a real workspace project — it defaults to `<workspace>/.research`. Trigger: an author follows the documented fallback and writes the card to `<workspace>/papers/<slug>/`; the study then silently never appears in the library (no RPC resolves it, the watcher never fires for it). This is the same "SKILL.md names a path the app does not read" class as Issue 51.

**Suggested fix:**
a) Remove the fallback and state that the library is always `<research-root>/papers/`, with `<research-root>` = the persisted root or, when unset, `<workspace>/.research`.
b) Or point the fallback at the path the app actually reads (`<workspace>/.research/papers/<slug>/`) and note that RESEARCH need not be enabled for the library (hybrid mode).

---

## Issues 69–70 (sixth, independent pass)

### Issue 69 — SHOULD FIX — The Papers "Study" field and the "Compare selected" multi-select discard the user's input when the dispatch fails

`PapersView` clears its local input synchronously, before the fire-and-forget `send()` settles, so a rejected dispatch loses the pasted reference / ticked selection — unlike the app's own send path, which restores text on failure.

**Location:** `frontend/src/components/papers/PapersView.tsx:295-303` (`study`: `dispatch(buildStudyPrompt(ref, mode), …)` at `:299` followed by `setReference('')` at `:300`); the sibling `compareSelected` at `:326-334` (`setSelectedIds([])` at `:332` after the `dispatch` at `:331`); the failure handler `dispatch` at `:278-293` (only `usePaperStore.getState().setError(...)` in the `catch`). Contract honored elsewhere: `frontend/src/hooks/useMessageSender.ts:73` (`throw error // let caller restore text` inside the `createSession()` `catch`) and `frontend/src/hooks/useChatInputController.ts` `handleSend`, whose `catch` calls `writeTextToSession(originSessionId, messageText)` with the comment "the catch restores text on failure".

**Why it is a problem:** `send()` rethrows only when the auto-created session fails (the documented splash race), and `dispatch` is deliberately fire-and-forget — it surfaces the error string but never restores the caller's state. Because `study`/`compareSelected` clear the field/selection *before* the RPC settles, that rejection leaves the UI with the user's input gone. Trigger: the user pastes an arXiv URL / DOI / citation into the Study field and clicks **Study** (or presses Enter) before the runtime/session is ready; `send()` rejects at `createSession()`, `dispatch` renders "Failed to dispatch study-paper: …", but `setReference('')` already ran, so the pasted reference is silently lost and must be retyped. Same shape for **Compare selected**: if that dispatch rejects, the ticked papers are already deselected and the author must re-select every one. This is distinct from Issue 20 (which is the *store* mutation lacking a live-project guard) and Issue 56 (a workspace dispatch failure not being rendered) — here the harm is the discarded local input on the failure path the code itself names.

**Suggested fix:**
a) Restore on failure, mirroring the chat controller: make `dispatch` return its promise and do `void dispatch(...).catch(() => setReference(ref))` in `study` and restore the previous `selectedIds` in the same `.catch` in `compareSelected`.
b) Clear only on success (`dispatch(...).then(() => { setReference(''); setSelectedIds([]) })`), leaving the state untouched when the send rejects.
c) Centralize the contract: have the shared dispatch helper accept an `onFailure`/`restore` callback so every paper gesture (rows, workspace, prior-art row, attachments) uses one failure path instead of hand-rolled clears.

### Issue 70 — SHOULD FIX — `literature.py`'s OpenAlex predecessor fetch sends an uncapped `per-page`, so a `--limit > 200` run silently loses the predecessor set (its sibling caps at 200)

The predecessor lookup sets OpenAlex's `per-page` to the raw number of requested ids with no cap, while the citing lookup in the same module deliberately clamps it — so the two sibling calls disagree about the API's page-size limit.

**Location:** `core/papers/skills/study-paper/scripts/literature.py:318` (`openalex_predecessors`: `params = {"filter": "ids.openalex:" + "|".join(ids), "per-page": str(len(ids))}`, where `ids = [reference.rsplit("/", 1)[-1] …][:limit]` at `:317`) vs `:353` (`openalex_citing`: `"per-page": str(min(limit, 200))`); CLI `--limit` at `:653` (`type=int, default=25`); call sites `:496-498` / `:513-515`.

**Why it is a problem:** `per-page` is `len(ids)`, which is `min(limit, #references)` — uncapped (the citing sibling clamps to 200). Two OpenAlex limits bite here: (i) the `ids.openalex` **filter accepts at most 100 values** — a 101-value filter returns HTTP 400 ("Maximum number of values exceeded for ids.openalex…", independent of `per-page`) — and (ii) `per-page` is capped at 200. Either way the HTTP error is swallowed by `call(...)` into a stderr note (`:496`) and `predecessors` comes back empty (or reduced to the Crossref fallback when the seed has a DOI), with no error surfaced beyond a Notes line — while `citing` still returns up to 200. Verified live: `python3 literature.py <doi> --limit 100` → 97 predecessors; `--limit 101` → 0 predecessors with the note `OpenAlex predecessors: HTTP 400 …`; the raw query `filter=ids.openalex:<101 ids>&per-page=100` → HTTP 400 while 100 ids → HTTP 200. So `--limit` in the 101–200 range is broken by the **value** cap even though it is under the 200 `per-page` limit — a per-page cap alone does not fix it. The app path (`RunPaperLiterature`, default 25) is unaffected; the documented direct-CLI/agent path (SKILL.md directs the agent to run this helper) is what breaks. The two sibling calls in the same module also disagree on the cap.

**Suggested fix:**
a) Cap both dimensions — set `"per-page": str(min(len(ids), 200))` **and** chunk the `ids.openalex` filter into batches of ≤100 values (merging results), mirroring `openalex_citing`; if a wider `--limit` must be honored, paginate with `&page=N` until `limit` items or `meta.count` is reached.
b) Extract one shared `openalex_works_by_ids(ids, …)` helper called by both `openalex_predecessors` and `openalex_citing`, so the 200-`per-page`/100-value caps live in one place and cannot drift again.
c) Or clamp `--limit` at argument-parse time to 100 (the filter-value limit) and document that max in the `--limit` help and the module docstring, so the API limits are explicit rather than discovered at runtime.

### Issue 71 — SHOULD FIX — A `literature.py` arXiv seed that carries a DOI OpenAlex cannot resolve by identifier aborts instead of falling back to the title search

In the arXiv branch the title-search fallback is an `elif`, so once the record supplies a DOI it can never be reached — an identifier-lookup failure on that DOI ends the run instead of retrying by title, as the sibling vendored helper does.

**Location:** `core/papers/skills/study-paper/scripts/literature.py:459-466` (the `elif kind == "arxiv":` branch — `if arxiv_meta and arxiv_meta.get("doi"):` at `:460` … `seed_work = call("OpenAlex", lambda: openalex_work_by_doi(...))` at `:462`, then `elif arxiv_meta and arxiv_meta.get("title"):` at `:464`); the helper `openalex_title_search` and the sibling's fallback `fetch_paper.py:440` (`if not openalex and (arxiv or {}).get("title"): openalex = attempt("OpenAlex", search_openalex(...))`).

**Why it is a problem:** `call(...)` swallows a failed OpenAlex lookup into a stderr Notes line and returns `None`, so a DOI that OpenAlex does not index by identifier (HTTP 404 / a DataCite-style or not-yet-indexed DOI) leaves `seed_work is None`; because the title path is an `elif` it is skipped, `collect` raises `LitError`, and `main` returns 1 ("could not resolve the seed") — even though `arxiv_meta["title"]` is present and `openalex_title_search` is implemented and used one branch away. Trigger: `python3 literature.py arXiv:1706.03762` (a record whose `doi` OpenAlex 404s on), verified by importing the shipped modules with the network functions stubbed: `collect()` raised `LitError: could not resolve the seed 'arXiv:1706.03762'` and `openalex_title_search` was called 0 times, whereas `fetch_paper.py` under the same stub resolved via the title search (`sources_used=['arXiv','OpenAlex']`, `title_search` called once). Reachable from the app, not just the CLI: `literatureSeed` (`backend/frontend_api_papers_literature.go:145-169`) returns `"arXiv:"+arxiv` for any card carrying an arXiv id and no DOI, so the Literature "Refresh" RPC feeds this branch; `SKILL.md` also directs direct helper runs. Not covered by Issue 34 (the Go exit-code→status *mapping*), Issue 70 (the `per-page` cap), or Issues 40/45/50/64 (`fetch_paper.py`'s flag / `_parse_meta` / `setdefault` / exit-code issues) — the resolution order is a distinct defect.

**Suggested fix:**
a) Mirror the sibling — drop the `elif` and fall back on any identifier-lookup failure (retry by title whenever `seed_work is None and arxiv_meta.get("title")`), recording `match = "title-search"` so the "verify it is the right paper" note fires.
b) Or keep the branch structure but distinguish "no DOI" from "by-DOI lookup failed" and retry by title only on failure.
c) Or have `call()` return a sentinel on failure so attempts can be chained explicitly (`if not seed_work and …`), and add a regression test pinning arXiv + DOI + 404 → title-search fallback.

### Issue 72 — SHOULD FIX — `literature.py`'s documented exit-code table contradicts the script: the documented "3 unexpected problem" is unreachable, and the real exit 3 (rate-limited or `--out` write failure) is undocumented

The `EXIT CODES` docstring publishes a code the script never returns and omits what its exit 3 actually means — the same doc-vs-code defect the report records for the sibling `fetch_paper.py` (Issue 64), here in the other vendored helper.

**Location:** `core/papers/skills/study-paper/scripts/literature.py:36-40` (the `EXIT CODES` block — `0 seed resolved and results produced`, `1 the seed could not be resolved`, `2 network unavailable`, `3 unexpected problem`) vs `main` at `:676-705` (`:680` → 2 on a network error, `:683` → 3 on `RateLimitError`, `:686` → 1 on `LitError`, `:698` → 3 on the `--out` write `OSError`, `:705` → 0).

**Why it is a problem:** No path returns 3 for a generic "unexpected problem" — the only `return 3`s are the rate-limit stall (`:683`, which is itself unreachable — see Issue 78) and the output-write failure (`:698`) — while a genuine internal fault exits CPython's default 1, which the table reserves for "the seed could not be resolved". An operator/agent scripting against the published table (SKILL.md directs the agent to run this helper) therefore misreads a rate-limit or write failure as an "unexpected problem" and an internal crash as "seed not resolved". This is distinct from Issue 64 (which documents the identical defect for the sibling file `fetch_paper.py:29-33`) and from Issue 34 (which cites `literature.py`'s real codes only as *input* to the backend's exit-code→status mapping): the `literature.py` docstring (file:line) itself is not recorded.

**Suggested fix:**
a) Correct the block to the real codes, e.g. `1  the seed could not be resolved` / `2  network unavailable` / `3  rate limited, or the output could not be written`.
b) Or reserve a dedicated code (or stderr sentinel) for generic faults — one the backend does not map to a user-facing status (pairs with Issue 34 fix option a) — and document 3 as rate-limit/write only.
c) Or lift both helpers' exit-code tables into one shared documented block so the two siblings cannot drift again.

### Issue 73 — SHOULD FIX — `literature.py` documents a URL seed form its classifier does not recognize, so an arXiv or publisher URL is treated as a title and the run fails

Both the module docstring and the CLI help advertise "a URL" among the accepted seeds, but `classify_seed` has no URL branch — an arXiv/publisher URL falls through to the title search and the helper aborts with a "could not resolve the seed" error.

**Location:** `core/papers/skills/study-paper/scripts/literature.py:8` (module docstring: "Given a seed paper (a DOI, an arXiv id, an OpenAlex id, a title, **or a URL**)") and `:647` (`parser.add_argument("seed", help="DOI, arXiv id, OpenAlex id, title, or URL")`) vs `classify_seed` at `:227-250` (own docstring `:228` "kind in openalex|arxiv|doi|title"; the final fallback `return "title", value` at `:250`); the sibling `fetch_paper.py`'s `classify` (which does have a URL kind).

**Why it is a problem:** `classify_seed` recognizes only an OpenAlex id, an arXiv id, a DOI, and *(everything else)* as a **title** — there is no URL branch and no `url` kind is ever returned. The arXiv regex `arxiv[:\s/]*(\d{4}\.\d{4,5})(v\d+)?` (`:237`) cannot match `arxiv.org/abs/…` because the `.org/abs/` run breaks the `[:\s/]*` character class, and a generic publisher landing page carries no bare DOI, so both fall through to `return "title", value` and are handed to `openalex_title_search` as a literal URL string. Trigger (verified by executing the shipped module): `classify_seed("https://arxiv.org/abs/1706.03762")` → `("title", "https://arxiv.org/abs/1706.03762")` and `classify_seed("https://www.nature.com/articles/nature12373")` → `("title", "https://…")`; with the network functions stubbed, `collect()` raised `LitError: could not resolve the seed 'https://arxiv.org/abs/1706.03762'. Notes: OpenAlex: OpenAlex title search found nothing …` (→ `main` exit 1). So a documented input silently degrades to a confusing failure instead of resolving the paper. This is distinct from Issue 71 (the arXiv branch skipping the title fallback when a *DOI* lookup fails), Issues 40/45/50/64 (`fetch_paper.py`) and Issues 70/72 (`per-page` cap, exit-code docstring) — no recorded issue mentions `classify_seed` or the "or a URL" claim.

**Suggested fix:**
a) Add the missing branch: detect `http(s)://` in `classify_seed` (an arXiv-URL form → `("arxiv", id)`, a DOI-in-URL form → `("doi", doi)`, and a generic `("url", …)` kind handled in `collect`), mirroring `fetch_paper.py`.
b) Or correct the docs to the implementation: drop "or a URL" from the module docstring (`:8`) and the argparse help (`:647`), stating the accepted forms as DOI / arXiv id / OpenAlex id / title, matching `classify_seed`'s own kind list.
c) Add a regression test pinning `classify_seed("https://arxiv.org/abs/1706.03762") == ("arxiv", "1706.03762")` (and a rejection case), so the documented contract and the classifier cannot drift again.

### Issue 74 — SHOULD FIX — `literature.py`'s OpenAlex title search concatenates the raw seed title into the `filter` grammar, so a title containing a comma returns HTTP 400 and the run aborts

The title search builds `filter=title.search:<title>` by raw string concatenation, and OpenAlex's filter grammar treats `,` as the AND separator and `|` as OR — so a title containing a comma yields a malformed filter (HTTP 400) and a `|` silently changes the query.

**Location:** `core/papers/skills/study-paper/scripts/literature.py:277` (`openalex_title_search`: `params = {"filter": "title.search:" + title, "per-page": "1"}`); call sites in `collect` at `:465` (the `arxiv`-branch title fallback) and `:468` (the `title`-kind seed); app path `backend/frontend_api_papers_literature.go:144-170` (`literatureSeed`). Contrast the sibling `fetch_paper.py`'s `search_openalex`, which uses the dedicated `search=` parameter.

**Why it is a problem:** `urlencode` percent-encodes the title for transport, but OpenAlex decodes the filter value and re-parses it, so the reserved characters survive into the grammar. A comma in the title therefore splits the filter into two clauses → HTTP 400 → `call(...)` swallows it into a Notes line; for a title-kind seed the whole run then aborts with "could not resolve the seed", and for the arxiv-branch fallback the fallback is lost. Trigger (verified live against the shipped module): `python3 literature.py "Attention, Is All You Need"` → exit 1 with `Notes: OpenAlex: HTTP 400 …`, while the comma-free title resolves (exit 0); `filter=title.search:zzzqqqnotatitle | Attention` returns the CBAM paper while the literal `zzzqqqnotatitle` alone returns 0, confirming `|` ORs the query (a silent wrong match). Comma-bearing titles are common (e.g. "The Elements of Statistical Learning: Data Mining, Inference, and Prediction"). Reachable from the app, not just the CLI: `literatureSeed` falls back to `rec.Title` when a card carries no DOI/arXiv/identifier, so the Literature "Refresh" RPC fails for any such paper whose title contains a comma. Not covered by any recorded issue — the report never mentions `title.search:` (Issues 70/71/72/73 have different root causes, and `fetch_paper.py` avoids the problem via `search=`).

**Suggested fix:**
a) Use the dedicated search parameter, mirroring the sibling: `params = {"search": title, "per-page": "1"}` (comma-tolerant).
b) Or keep `title.search` but escape the reserved characters (`,`, `|`, `:`) per OpenAlex filter syntax before concatenation.
c) Add a regression test that stubs the request for a comma-containing title and asserts the constructed query is safe (no 400).

### Issue 75 — SHOULD FIX — `literature.py`'s arXiv lookup bypasses the single retrying HTTP helper, contradicting the module's "every request goes through one helper" rate-limit contract

The `RATE LIMITS` docstring promises Retry-After backoff for *every* request, but `arxiv_lookup` opens its URL directly and never retries on 429/503.

**Location:** `core/papers/skills/study-paper/scripts/literature.py:29-33` (docstring: "Every request goes through one helper that honours the Retry-After header and otherwise backs off exponentially (capped), retrying a bounded number of times.") vs `arxiv_lookup` at `:287-297` — its own `urllib.request.urlopen(request, timeout=timeout)` at `:292`, the only request in the module that does not go through `http_get_json` (`grep urlopen` → `:148` inside `http_get_json`, `:292` in `arxiv_lookup`).

**Why it is a problem:** `http_get_json` (`:139`) is the single retrying helper (429/503 + `Retry-After` + capped exponential backoff), but `arxiv_lookup` is a second, unaided request path: it turns any error status into `ApiError("arXiv returned HTTP %s")` with no backoff. So a transient 429 from `export.arxiv.org` aborts the arXiv metadata lookup instead of retrying, contrary to the documented contract; for an arXiv seed (`collect:459-466`) that leaves `seed_work is None` → `LitError` → exit 1. Trigger: an arXiv seed resolved while `export.arxiv.org` is rate-limiting (a realistic burst scenario the docstring claims to handle). Not covered: no recorded issue mentions `arxiv_lookup` or `Retry-After` (Issue 72 concerns the exit-code table, not the rate-limit claim).

**Suggested fix:**
a) Route `arxiv_lookup` through a shared retry helper (a raw-body sibling of `http_get_json`) so the 429/`Retry-After` backoff applies to arXiv too.
b) Or narrow the docstring to state that only the JSON sources (OpenAlex/Crossref/Semantic Scholar) are retried and that an arXiv error status surfaces immediately.

### Issue 76 — SHOULD FIX — The paper workspace reads per-paper artifact filenames that no producer ever writes, so the Source section and the anchor-jump feature are permanently inert (and the Literature note pane and the per-paper Compare fallback can never render)

`usePaperArtifacts` lists candidate filenames per section, but three of the four sections' candidates are never created by any producer — only `paper.md`/`note.md`/`appraisal.md`/`flashcards.md`/`literature.json` have writers — so the sections that depend on the unproduced names stay empty forever.

**Location:** `frontend/src/components/papers/usePaperArtifacts.ts:31-34` (`PAPER_SECTION_FILES`: `compare: ['comparison.md','comparison-matrix.md']`, `source: ['source.md']`, `literature: ['literature.md','literature-context.md']`) plus the file header at `:5-8` ("optional artifacts the study-paper skill may add — the extracted source text (`source.md`) …"); consumers `frontend/src/components/papers/PaperWorkspace.tsx:112-131` (the Source section and `onAnchorSelect` → `resolveAnchor(artifacts.source.content, …)`), `:294-302` (the per-paper Compare fallback), and `frontend/src/components/papers/PaperLiterature.tsx:276-286` (the Literature markdown pane). The "other half" of the contradicting contract: `core/papers/skills/study-paper/SKILL.md:198-210` ("a per-paper slug directory holding **three** files: `paper.md`, `note.md`, `appraisal.md`"), `SKILL.md:280-283` (PDF→Markdown extraction written "to a **scratch file** (not one of the three artifacts)"), and the authoritative `specs/domains/papers.md` § Artifact Layout (lists only `paper.md`, `note.md`, `appraisal.md`, `flashcards.md`, `literature.json`).

**Why it is a problem:** A repo-wide, test-excluded search shows `source.md`, `literature.md` and `comparison.md` appear **only** under `frontend/src/**` (code + tests); the only non-frontend hits for `literature-context.md`/`comparison-matrix.md` are the skill's own reference/asset **basenames** (a different namespace from paper-directory artifacts). No Go RPC, watcher, dispatch prompt (`paperActions.ts`), or SKILL.md instruction ever writes these files into `<research-root>/papers/<slug>/`. Trigger: study a PDF end-to-end via the file-tree "Study this paper…" gesture (the model follows SKILL.md and writes only `paper.md`/`note.md`/`appraisal.md`, extraction going to a scratch file), then open the paper tab — the **Source** section always reports "No source text captured for this paper yet", and because `onAnchorSelect` resolves against that empty `artifacts.source.content`, every anchor click yields the false "не найдено" (`resolveAnchor('') === null`); the **Literature** context-note pane never appears; the per-paper **Compare** fallback can never render. The frontend tests mock these artifacts as fixtures, so the suite never notices (the same failure mode the report records for `flashcards.md` in Issue 51). This is distinct from Issue 6 (which treats the empty-`source` state as a transient loading artifact) and Issue 21 (which treats `literature.md` as an existing, merely un-refreshed artifact) — neither records that these filenames have no producer; Issue 51(c) covers only the `comparisons/` **directory** omission, not the per-paper `comparison.md`/`comparison-matrix.md` candidates.

**Suggested fix:**
a) Make the contract true in the skill/spec: add the extracted source and the literature note to `SKILL.md`'s "Where artifacts live" (name `<paper-dir>/source.md` and `<paper-dir>/literature.md`), drop the "scratch file / three files" wording, and list them (plus the per-paper comparison filename) in `specs/domains/papers.md`§ Artifact Layout so spec, skill and `PAPER_SECTION_FILES` agree.
b) Or make it true in the app: remove the unproduced candidates from `PAPER_SECTION_FILES` (`source`, `literature`, and the per-paper `compare` fallback) and delete the now-dead Source pane / anchor-jump surface, so the UI stops advertising sections that can never populate.
c) Or have the study flow persist them explicitly — e.g. a backend post-step that writes the extracted text to `<paper-dir>/source.md` (and the note to `literature.md`) after the skill run — keeping the current readers and the E1 anchor feature functional.

### Issue 77 — SHOULD FIX — `literature.py`'s new `write_atomic` leaves the output at mode `0600` (no `chmod` after `mkstemp`), diverging from the sibling helper and the Go writer

The staging file `mkstemp` creates is mode `0600`, and `os.replace` carries that mode onto the destination, so every `--out` run writes/lowers `literature.json` to `0600` — unlike every other artifact of the feature.

**Location:** `core/papers/skills/study-paper/scripts/literature.py:580-604` (`write_atomic`; `tempfile.mkstemp(...)` at `:590`, `os.replace(tmp_path, path)` at `:596`); caller `:695` (`write_atomic(args.out, output)`). Siblings that do it right: `core/papers/skills/study-paper/scripts/fetch_paper.py`'s `--out` (a plain `open()` → `0644`) and the Go writer `core/papers/writer.go:301` (`os.Chmod(tmpName, 0o644)` right after `os.CreateTemp`, exactly because `CreateTemp`/`mkstemp` yield `0600`).

**Why it is a problem:** Verified empirically: `write_atomic(fresh_path, …)` → mode `0o600`; `write_atomic` over a pre-existing `0644` file → `0o600`; `literature.py <doi> --out <pre-existing 0644 file>` → exit 0, mode `0o600`, while a plain `open()` beside it yields `0644`. So the artifact becomes unreadable to group/other, inconsistent with the sibling helper and with the `paper.md`/`note.md`/`appraisal.md` written into the same paper directory by the Go writer, and a **behavior change** from the pre-change code (which used a plain `open()`); `write_atomic` is new code introduced by this change set. The existing `_test_literature.py` never checks the output mode, so the regression is unpinned. Impact is low (single-user desktop) but the divergence is real and easy to fix.

**Suggested fix:**
a) Mirror `writer.go:301`: `os.chmod(tmp_path, 0o644)` (or the destination's prior mode) after `fsync` and before `os.replace`.
b) Or create the staging file with a umask-respecting mode (`os.open` + `os.fchmod` to `0o666 & ~umask`) so `write_atomic` matches a normal `open()`.
c) Add a `_test_literature.py` case asserting the written file's mode, so the regression cannot recur.

### Issue 78 — SHOULD FIX — `literature.py`'s `main` rate-limit handler is unreachable: `collect()`'s `call()` swallows `RateLimitError` (a `LitError` subclass), so a real rate limit is reported as "could not resolve the seed" (exit 1) and the backend's rate-limited status can never be produced

`RateLimitError` subclasses `LitError`, and every HTTP call inside `collect()` goes through `call()`, whose `except LitError` handler catches it first — so the dedicated `except RateLimitError: return 3` in `main` never runs.

**Location:** `core/papers/skills/study-paper/scripts/literature.py:681-683` (`main`'s `except RateLimitError as exc: … return 3`) vs `collect()`'s `call()` at `:426-440` (`except NetworkError …` then `except LitError …`, no re-raise) and the class hierarchy `:114-121` (`class RateLimitError(LitError)`); backend consumer `backend/frontend_api_papers_literature.go:206-213` (exit 3 → `litStatusRateLimited`).

**Why it is a problem:** Because `RateLimitError` is a `LitError`, the `except LitError` arm of `call()` catches it (and `ApiError`) and only appends a note; nothing re-raises it, so it can never reach `main`'s `:681` handler. Verified by importing the shipped module with the OpenAlex work lookup stubbed to raise `RateLimitError`: `collect()` raised a plain `LitError` and `main([...])` returned **1**, printing `could not resolve the seed … Notes: OpenAlex: HTTP 429 from api.openalex.org after 3 retries`. So a rate-limited lookup is surfaced to the agent/operator — and to the backend, which maps exit 3 to `litStatusRateLimited` (a status `specs/domains/papers.md` lists) — as "the seed could not be resolved", and the rate-limited status is **unreachable via rate limiting** (exit 3 can now only arise from the `--out` write failure at `:698`). This is a control-flow defect, distinct from Issue 72 (the docstring table, whose premise that the `:683` branch produces 3 this disproves — hence the cross-reference added there) and from Issue 34 (the Go mapping).

**Suggested fix:**
a) Re-raise it where it matters: in `call()` (or the seed-resolution path) add `except RateLimitError: raise` **before** `except LitError`, so `main`'s `:681-683` handler becomes live and the rate-limited outcome is produced.
b) Or delete the dead `:681-683` handler and align the exit-code table, the Go mapping, and `specs/domains/papers.md` with the actual behavior (a 429 surfaces as exit 1 with an `HTTP 429` note).
c) Add a regression test that stubs a source to raise `RateLimitError` and pins the intended outcome.

### Issue 79 — SHOULD FIX — `literature.py`'s `OPENALEX_ID_RE` is unanchored, so a title or DOI containing a `W`+digits token is misclassified as an OpenAlex id and the run aborts

`classify_seed` tests the OpenAlex id pattern first, and that pattern is an unanchored `search` for `W` + four-or-more digits — so a real title such as `GW170817: …` or a DOI such as `10.1093/nar/gkw1092` is read as an OpenAlex id.

**Location:** `core/papers/skills/study-paper/scripts/literature.py:68` (`OPENALEX_ID_RE = re.compile(r"(?:openalex\.org/)?(W\d{4,})", re.IGNORECASE)`) and `:233-235` (`classify_seed`: `match = OPENALEX_ID_RE.search(value)` → `return "openalex", match.group(1)`, evaluated **before** the arXiv/DOI branches at `:236-250`); consequence path `:450-452` (`kind == "openalex"` → `openalex_work_by_identifier`) → `:264-270` (`GET https://api.openalex.org/works/<id>`) → swallowed by `call()` `:426-440` → `main` `:686` (exit 1); app path `backend/frontend_api_papers_literature.go:145-169` (`literatureSeed`).

**Why it is a problem:** `OPENALEX_ID_RE.search` matches a `W` followed by ≥4 digits *inside a larger token*, and the branch runs before the DOI/arXiv checks, so it captures substrings of ordinary text. Verified by importing the shipped module: `classify_seed("GW170817: Observation of Gravitational Waves from a Binary Neutron Star Inspiral")` → `("openalex", "W170817")` and `classify_seed("10.1093/nar/gkw1092")` → `("openalex", "w1092")` (both real, resolvable inputs), while `classify_seed("Attention Is All You Need")` correctly yields `("title", …)`. The helper then requests `GET https://api.openalex.org/works/W170817`, which 404s; the `ApiError` is swallowed into a Notes line, `seed_work` stays `None`, `collect` raises `LitError`, and `main` returns 1 — so a perfectly resolvable paper fails with a misleading "could not resolve the seed". Every gravitational-wave event title of the form `GW\d{6}` (`GW150914`, `GW170817`, `GW190521`, …) and any DOI containing a `…w####` run are affected. Secondary hazard: when the matched substring happens to be a *real* OpenAlex id, the run silently builds the graph around the **wrong** seed (`seed_match: "identifier"`, no title-search warning). Reachable from the app (`literatureSeed` at `:159-168`) and from the documented direct run (`SKILL.md:286`; the helper advertises a *title* seed at `literature.py:8`). Not covered by Issue 73 (the missing `http(s)://` branch of the same function — a different root cause) or Issue 14 (the analogous unanchored regex, but in `core/papers/model.go`); the report already rates the "unanchored regex mints a spurious id" class as SHOULD FIX.

**Suggested fix:**
a) Anchor the id to a standalone token — e.g. `re.compile(r"^(?:https?://(?:www\.)?openalex\.org/)?(W\d{4,})$", re.IGNORECASE)` used with `fullmatch`, or keep `.search` with boundaries `(?<![A-Za-z0-9])W\d{4,}(?![0-9])`.
b) Or keep the permissive URL form but evaluate the OpenAlex branch **after** the DOI/arXiv checks and require a word boundary, so a real DOI/title wins first.
c) Add regression tests pinning `classify_seed("GW170817: …") == ("title", …)` and `classify_seed("10.1093/nar/gkw1092") == ("doi", "10.1093/nar/gkw1092")`.

### Issue 80 — SHOULD FIX — `DOI_RE`'s trailing character class over-captures wrapping markdown/format characters, so a formatted-but-valid DOI fails to resolve (both vendored scripts)

The DOI pattern accepts any non-space character after the slash, so it swallows a DOI's trailing emphasis, brackets or backticks, and the cleanup `rstrip(").,;")` does not remove them — the contaminated identifier then 404s against every source.

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:58` (`DOI_RE = re.compile(r"10\.\d{4,9}/[^\s\"'<>]+", re.IGNORECASE)`), used at `:161-163` (`classify`: `match.group(0).rstrip(").,;")`); the identical pattern and use in `core/papers/skills/study-paper/scripts/literature.py:67` / `:246-248` (`classify_seed`).

**Why it is a problem:** The negated class `[^\s"'<>]` permits `*`, `` ` ``, `]`, `}`, etc., and the `rstrip(").,;")` cleanup removes only `)`, `.`, `,`, `;`. Verified by importing the shipped modules: `fetch_paper.classify("**10.1038/nature12373**")` → `('doi', '10.1038/nature12373**')`; `classify("[10.1038/nature12373]")` → `('doi', '10.1038/nature12373]')`; `classify("`10.1038/nature12373`")` → `('doi', '10.1038/nature12373`')`; `literature.classify_seed(...)` returns the same contaminated values. The contaminated identifier then builds `https://api.crossref.org/works/10.1038%2Fnature12373%2A%2A` and `https://doi.org/10.1038/nature12373**` → HTTP 404 → every source is swallowed into a Notes line → `ResolveError`/`LitError` → exit 1. Trigger: paste a DOI copied from a markdown bibliography (emphasis, brackets, or a code span) into the Study field / a direct helper run — a perfectly resolvable DOI fails end-to-end. Not covered by any recorded issue: no entry mentions `DOI_RE` or its character class; Issue 79 is `OPENALEX_ID_RE` (a mid-token substring, not a trailing capture), Issue 73 is `classify_seed`'s missing URL branch, and Issue 41 is a frontend TS word-boundary regex.

**Suggested fix:**
a) Tighten the class to the DOI charset and exclude delimiters — `r"10\.\d{4,9}/[^\s\"'<>()[\]{}*`]+"` — keeping the `rstrip` as a safety net.
b) Or keep the class but strip wrappers from both ends: `match.group(0).strip("*`[](){}.,;:'\"")`.
c) Or bound the match on a terminal alphanumeric — `r"10\.\d{4,9}/[^\s\"'<>]*[A-Za-z0-9]"` — and add regression tests for `**DOI**`, `[DOI]` and `` `DOI` ``.

### Issue 81 — SHOULD FIX — `literature.py`'s `CONTRADICTION_MARKERS` are matched as bare substrings, so ordinary papers are misclassified as contradictions and rendered to the user as "refuted"

The contradiction shortlist tests each marker with a plain `in` against the lowered title+abstract, and several markers are bare ambiguous tokens, so common titles are flagged as refutations of the seed.

**Location:** `core/papers/skills/study-paper/scripts/literature.py:73-98` (`CONTRADICTION_MARKERS`) and `:410` (`find_contradictions`: `reasons = sorted({label for marker, label in CONTRADICTION_MARKERS if marker in haystack})`); consumer `frontend/src/lib/literatureGraph.ts:96` (`contradiction: 'refuted'`) and `:301-309` (re-tags every flagged work as `contradiction`).

**Why it is a problem:** The markers include bare ambiguous substrings (`challenge`, `correction`, `retract`, `rebut`, `disagree`) tested with substring `in` and no word boundary. Verified by importing the shipped module: `find_contradictions([{'title': 'Retractable Bioadhesives for Wound Closure'}], False)` → reason `retraction` (`retract` ⊂ "Retractable"); `[{'title': 'Error correction in quantum memories'}]` → `correction`; `[{'title': 'Grand Challenges in Machine Learning'}]` and `[…'Challenges and Opportunities for Deep Learning']` → `direct challenge`. The frontend then re-tags each flagged work as `contradiction` (role `refuted`, destructive colour), so a materials/bio paper, any coding-theory paper, or any "Challenges in …" survey is presented to the user as a refutation of the seed. Not covered: no recorded issue concerns `CONTRADICTION_MARKERS`/`find_contradictions`. It is the `literature.py` counterpart of the report's own Issue 42 (the frontend `normalizeStrength` substring fold) — the same false-positive class, different file/function/input.

**Suggested fix:**
a) Require word boundaries and prefer unambiguous phrases — drop bare `challenge`/`correction` in favour of `a challenge to`/`correction to`/`retraction of`, and match `\bretract(?:ion|ed)?\b` so "retractable" no longer fires.
b) Or keep the list but add a stop-context guard for the ambiguous markers (skip when the haystack contains `error correction`/`error-correcting`/`challenges in`/`challenges and`/`retractable`).
c) Or split markers into a high-precision phrase tier and a low-precision word tier, and surface the matched span + tier in `reasons` so low-tier hits are advisory rather than rendered as `refuted`.

### Issue 82 — SHOULD FIX — `literature.py`'s `crossref_references` emits `year` as a JSON string, so Crossref-derived predecessors lose their year at the frontend boundary

The Crossref producer copies `reference.year` verbatim (a string), while the OpenAlex/S2 producers emit an int — so the frontend's numeric coercion drops the string years.

**Location:** `core/papers/skills/study-paper/scripts/literature.py:339` (`crossref_references`: `"year": reference.get("year")`) vs the sibling producers `_work_summary` (`:213`) and `_s2_summary` (`:367`) which emit an int; consumer `frontend/src/lib/literatureGraph.ts:158` (`year: asNumberOrNull(r['year'])`, with `asNumberOrNull` at `:149` requiring `typeof v === 'number'`), rendered at `frontend/src/components/papers/PaperLiterature.tsx:128` and `literatureGraph.ts:222`.

**Why it is a problem:** Crossref types `reference.year` as a **string**, and `:339` copies it as-is, so `predecessors[]`/`citing[]` mix `"year": 2012` (int, from the OpenAlex/S2 producers) with `"year": "2012"` (str, from Crossref). `asNumberOrNull` returns `null` for a string, and the UI renders the year only when it is `!== null` — so those nodes show no year in the DAG detail card / tick tooltip. Verified live: the raw Crossref API for `10.1038/nature12373` returns `reference[1].year` as `('str','2005')` (29 of 30 refs are strings); `crossref_references("10.1038/nature12373", 30, 25)` yields `year` types `{'NoneType': 1, 'str': 29}`; and an end-to-end default-limit run (`literature.py 10.1038/nature14539 --direction predecessors --format json`) included a Crossref-derived predecessor with a string year (`"2012"`). Impact is bounded (a missing year, no crash or wrong value) — hence SHOULD FIX, not MUST FIX. Not covered: the report never mentions `crossref_references` or `asNumberOrNull` (its only "year" entry, Issue 33, is `core/papers/parser.go` card fields).

**Suggested fix:**
a) Normalize at the producer, matching the siblings — `"year": _to_int_or_none(reference.get("year"))` (e.g. `int(y) if str(y).strip().isdigit() else None`) — so every producer emits `int | None`.
b) Or coerce at the boundary: widen `asNumberOrNull` (or add an `asYear`) to accept a numeric string, keeping `year: number | null` intact for all producers.
c) Do (a) and add a `_test_literature.py` regression asserting `crossref_references(...)[i]["year"]` is `int | None` for a stubbed response, so the two producers cannot drift again.

### Issue 83 — SHOULD FIX — `fetch_paper.py`'s `main` "API error" arm is dead code: `attempt()` swallows every `NetError`, so only `kind="network"` can reach the handler

`http_get`/`get_json`/`lookup_arxiv` raise `NetError(kind="http")`, but they are only ever called from inside `resolve()`'s `attempt()` closure, which catches every `NetError` — so the `kind != "network"` arm of `main`'s handler can never execute.

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:588-589` (`main`'s `sys.stderr.write("API error: %s\n" % exc); return 1` arm of the `except NetError` handler), enabled by `:415-429` (`resolve.attempt` catches all `NetError`s) and `:530-536` (`resolve`'s only escaping `NetError`, `kind="network"`).

**Why it is a problem:** Every `http_get(`/`get_json(` call site is inside an `attempt(...)` lambda, and `attempt` converts each caught `NetError` into a note (never re-raising), so the sole `NetError` that escapes `resolve()` is the explicit `raise NetError(..., kind="network")` at `:532-536`. Therefore `main`'s `if exc.kind == "network": … return 2` always fires and the `"API error:"`/`return 1` arm at `:588-589` is unreachable: an HTTP-level source failure (e.g. Crossref HTTP 500) is never distinguished at the exit boundary, and the `kind` "http"/"network" distinction documented at `:70-71` is half-used. Verified by importing the shipped module and driving `resolve(...)` with every source raising `NetError(kind="http")` → it raises `ResolveError`, not `NetError`. This is the same dead-handler class the report already rates SHOULD FIX for the sibling file (Issue 78, `literature.py`'s unreachable `except RateLimitError`); the analogous `fetch_paper.py` handler is not recorded (Issue 64 is the docstring's exit-3 wording; Issue 45 is the `_parse_meta` shadow crash).

**Suggested fix:**
a) Delete the dead arm (`:588-589`) and fold it into the `ResolveError` arm, so `main` has one honest "could not resolve" path and `kind` is used only for the note text.
b) Or make `kind="http"` actually reach the boundary — have `attempt` re-raise a fatal HTTP error when there is no fallback (or introduce an `ApiError` exception for HTTP statuses), so the "API error" arm becomes live and the docstring's taxonomy is honoured.
c) Or collapse the two `NetError` arms into one and add a regression test pinning which exit code each `kind` produces, so the deadness cannot silently return.

### Issue 84 — SHOULD FIX — `fetch_paper.py` reports "network unavailable / no source could be reached" (exit 2) whenever *any* source had a network error, even when another source was reached and answered

The final classification asserts total unreachability on any network-kind failure, but a source that answered with an HTTP error counts as "not a source", so a mixed outcome is mislabeled as an outage.

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:530-536` (`resolve`'s final classification: `if not state["sources"]: if state["network_failures"]: raise NetError("network unavailable; no source could be reached. Last error: %s" …, kind="network")`), fed by `:420-422` (`attempt`'s `state["network_failures"] += 1`) and surfaced by `:579-587` (`main` → exit 2).

**Why it is a problem:** `sources` is appended only when a call returns a value, so it is empty both when nothing was reachable *and* when reachable sources answered with HTTP errors (swallowed to `None`); the presence of any `network_failures` then forces the stronger, false claim "no source could be reached" and exit 2. Verified by importing the module and driving `main(["10.1038/nature12373"])` with Crossref raising `NetError(kind="network")` and OpenAlex raising `NetError(kind="http")` (404): the process prints `network unavailable: network unavailable; no source could be reached …` and exits **2**, even though OpenAlex was reached and answered — whereas the same input with both sources returning HTTP errors correctly exits 1. The message ("Retry when the network is back") misdirects the agent/operator when the relevant source simply does not index the paper. Not covered by Issue 64 (docstring exit-3 wording), Issue 34 (the Go exit-code→status mapping) or Issue 78 (the dead rate-limit handler).

**Suggested fix:**
a) Track whether *every* attempted source failed at the network level (e.g. an `attempted`/`reached` counter) and emit the "network unavailable"/exit-2 classification only when no source was reachable; otherwise raise `ResolveError` (exit 1) with the per-source notes.
b) Or keep the heuristic but stop asserting the false fact — set the message to "could not resolve; at least one source was unreachable" and map it to exit 1, reserving exit 2 for the all-sources-unreachable case.
c) Distinguish the two in `state` and add a regression test: one network failure + one HTTP failure → exit 1; two network failures → exit 2.

### Issue 85 — SHOULD FIX — `DOI_RE` excludes `<`/`>`, so a legacy SICI DOI is truncated and fails to resolve (both vendored scripts)

The DOI pattern stops at the first `<` or `>`, which are legal DOI characters, so a pasted SICI DOI is cut short and the truncated id 404s against every source.

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:58` (`DOI_RE = re.compile(r"10\.\d{4,9}/[^\s\"'<>]+", re.IGNORECASE)`), used at `:161` (`classify`); the identical pattern at `core/papers/skills/study-paper/scripts/literature.py:67`, used at `:246` (`classify_seed`).

**Why it is a problem:** `<` and `>` are part of the real DOI charset (the legacy SICI form), but the negated class excludes them and the `rstrip(").,;")` cleanup cannot repair a mid-string truncation. Verified: `classify_seed("10.1002/(sici)1099-0518(20000601)38:11<1951::aid-pola40>3.0.co;2-c")` → `("doi", "10.1002/(sici)1099-0518(20000601)38:11")` (cut at the `<`), and the *full* DOI resolves (HTTP 200) while the truncated value 404s. Such DOIs are real and common (a Crossref scan of four journal prefixes for 1998–2001 found 638 DOIs containing `<`/`>`). Crucially `DOI_RE` is applied **only to user input** — `grep` shows its sole uses are `fetch_paper.py:161` and `literature.py:246`; HTML markup is parsed by the separate `META_TAG_RE`/`ATTR_RE` — so excluding `<`/`>` protects nothing here. This is the inverse of Issue 80 (which is about the class *over*-capturing trailing emphasis/brackets); a fix must reconcile both (Issue 80's suggestions all keep `<>` excluded, so they do not address this).

**Suggested fix:**
a) Admit `<`/`>` in the DOI match and strip only a *balanced* wrapping pair in the cleanup, so a bare SICI DOI is captured whole while `<https://doi.org/10.1038/nature12373>` still reduces to the bare DOI.
b) Or split the pattern: keep the strict `[^\s"'<>]` form for any HTML-context use and give `classify`/`classify_seed` a permissive DOI pattern that admits `<>`.
c) Add regression tests pinning the SICI DOI (and the `<https://doi.org/…>` wrapper) to the correct DOI in both helpers.

### Issue 86 — SHOULD FIX — `fetch_paper.py`'s tag/attr regexes treat any `>` as the end of a tag, so a `<meta>`/`<link>` whose quoted value contains `>` is truncated (and unquoted attributes are unmatched)

The tag and attribute patterns are not quote-aware, so a legal `>` inside a quoted attribute value ends the tag early and the attribute silently disappears.

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:60` (`META_TAG_RE = re.compile(r"<meta\b[^>]*>", re.IGNORECASE)`), `:61` (`LINK_TAG_RE = re.compile(r"<link\b[^>]*>", re.IGNORECASE)`), consumed by `_parse_meta` at `:349-357` and `_find_pdf_link` at `:360-365`; the sibling `ATTR_RE` at `:62` (`([a-zA-Z:_-]+)\s*=\s*(\"[^\"]*\"|'[^']*')`) used by `_attrs` at `:135-139`.

**Why it is a problem:** A raw `>` is valid and unescaped inside a quoted HTML attribute value. Verified by importing the module: `META_TAG_RE.findall('<meta name="description" content="Convert HTML > Markdown">')` → `['<meta name="description" content="Convert HTML >']`, and `_attrs(...)` on that → `{'name': 'description'}` — the `content` is dropped, so `_parse_meta` loses the meta (same for `citation_abstract`/`og:description`). The adjacent `ATTR_RE` fails from the other side: `_attrs('<meta name=description content="Hello World">')` → `{'content': 'Hello World'}` (the *unquoted* `name` is unmatched, so the meta is dropped). This is latent behind Issue 45 (the `_parse_meta(html)` shadow crash fires first), exactly like Issue 50, but it is not recorded by 45 or 50 (which concern the parameter shadow and the `setdefault` author collapse, not the regexes).

**Suggested fix:**
a) Make the tag patterns quote-aware — `<meta\b(?:[^>"']|"[^"]*"|'[^']*')*>` (and the same for `<link`) — so a `>` inside a quoted value cannot end the tag prematurely.
b) Or replace the hand-rolled extraction with the stdlib `html.parser.HTMLParser` and read `<meta>`/`<link>` attributes structurally (which also fixes `_find_pdf_link`'s un-unescaped `href`).
c) At minimum widen `ATTR_RE` to admit unquoted values (`…=("[^"]*"|'[^']*'|[^\s>]+)`) and add regression tests for a `content` containing `>` and for an unquoted attribute.

### Issue 87 — SHOULD FIX — Neither helper ever sends the contact address to Crossref, so the source every predecessor run depends on is never served from the polite pool (contradicting the docstrings and the bundled reference)

The contact address is threaded into every OpenAlex and Unpaywall request but not into the Crossref builders, and the `User-Agent` carries no `(mailto:…)` contact — so neither politeness mechanism applies to Crossref.

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:239-240` (`lookup_crossref(doi, timeout)` — no `email` parameter; `url = "https://api.crossref.org/works/" + quote(doi, safe="/")`) against its own contract at `:14` ("SOURCES (all keyless; **queried politely with a contact address when known**)"); `core/papers/skills/study-paper/scripts/literature.py:329-330` (`crossref_references(doi, limit, timeout)` — the same bare URL) against `:17` ("SOURCES (keyless first; **queried politely**)"). Sibling plumbing that *does* satisfy it: `literature.py:257-261` (`_with_mailto`), `:320`/`:357`, `fetch_paper.py:306-307`, `:315-325` (Unpaywall `?email=`). Corroborating bundled guidance: `core/papers/skills/study-paper/references/literature-context.md:135`, `:194`, `:271` ("include `mailto=` on every request to join the polite pool").

**Why it is a problem:** `--email` (or `UNPAYWALL_EMAIL`/`OPENALEX_MAILTO`) reaches every OpenAlex/Unpaywall request, but the Crossref builders take no email and the `User-Agent` (`study-paper-fetch/1.0` / `study-paper-literature/1.0`) has no contact, so Crossref requests leave the polite pool — the shared pool is throttled harder, and the bundled reference doc explicitly treats the polite pool as the intended budget. Verified by importing the shipped modules and capturing the built URL: even with an address configured, `fetch_paper.lookup_crossref("10.1038/nature12373", 20)` and `literature.crossref_references("10.1038/nature12373", 25, 25)` produce the bare URL, while `openalex_work_by_doi(…, "me@x.org")` appends `mailto=`. Trigger: any `--email` run — every `--direction predecessors`/`both` run calls `crossref_references` (`literature.py:502-505`) and the `fetch_paper.py` DOI path calls `lookup_crossref` (`:443`, `:448`). Same docstring-vs-code class as Issue 75 (the "every request is retried" claim), but a different claim, mechanism and source.

**Suggested fix:**
a) Thread the address through both Crossref builders, mirroring `_with_mailto`: add `email` to `lookup_crossref(doi, timeout, email)` / `crossref_references(doi, limit, timeout, email)` and append `?mailto=`/`&mailto=` from `args.email`.
b) Or satisfy politeness via the header the reference doc accepts — build the `User-Agent` as `study-paper-<role>/1.0 (mailto:<email>)` when an address is known.
c) Or, if Crossref politeness is deliberately out of scope, narrow the docstrings (`fetch_paper.py:14`, `literature.py:17`) and the reference note so the contract matches the code (the remedy Issue 75 lists for the retry claim).

### Issue 88 — SHOULD FIX — The backend always prefixes `arXiv:`, but the helpers recognize old-style arXiv ids only in the bare anchored form, so an old-style id is misclassified as a title and the lookup fails

`literatureSeed` hands the helper `"arXiv:" + <arxiv id>`, but `classify_seed` matches an old-style id only when the whole seed is the bare id — the prefix defeats the anchored regex, so the seed degrades to a title search and the run fails.

**Location:** `backend/frontend_api_papers_literature.go:161` (`literatureSeed`: `if arxiv := byScheme("arxiv"); arxiv != "" { return "arXiv:" + arxiv }`) vs `core/papers/skills/study-paper/scripts/literature.py:243` (`if ARXIV_OLD_RE.match(value):`, with the **anchored, lowercase-only** regex at `:69` `^[a-z\-]+(?:\.[A-Z]{2})?/\d{7}(v\d+)?$`) inside `classify_seed` (`:227-250`); the identical logic in `fetch_paper.py:162-163` (regex `:59`).

**Why it is a problem:** Verified by importing the shipped module: `classify_seed("arXiv:hep-th/9901001")` → `("title", "arXiv:hep-th/9901001")` while `classify_seed("hep-th/9901001")` → `("arxiv", "hep-th/9901001")`. The anchored old-style regex rejects the `arXiv:` prefix (and the new-style regex requires the `dddd.dddd` form), so a card whose identifier is an old-style arXiv id (e.g. `hep-th/9901001`, or any pre-2007 `archive/YYMMNNN` id) gets `"arXiv:hep-th/9901001"` from `literatureSeed`, is classified as a **title**, and is sent to `openalex_title_search` as that literal string → nothing found → the Literature "Refresh" RPC fails. Reachable from the app (the RPC's own seed builder) and from `SKILL.md`-directed helper runs. Not covered by Issue 71 (the arXiv+DOI title fallback) or Issue 73 (the missing URL branch) — this is the prefix/anchoring interaction.

**Suggested fix:**
a) Strip a leading `arXiv:`/`arxiv:` (case-insensitively) in `classify`/`classify_seed` before matching, so the prefixed and bare forms agree.
b) Or make `ARXIV_OLD_RE` (and its `fetch_paper.py` twin) tolerate the `arXiv:` prefix — e.g. `^(?:arxiv[:\s]*)?[a-z\-]+(?:\.[A-Z]{2})?/\d{7}(v\d+)?$`, `re.IGNORECASE` for the prefix only.
c) Or have the backend stop prefixing when the value already carries a slash-form id; and add a regression test pinning `classify_seed("arXiv:hep-th/9901001") == ("arxiv", "hep-th/9901001")`.

### Issue 89 — SHOULD FIX — `fetch_paper.py`'s URL branch drops the landing page's own DOI from the resolved record

In the `kind == "url"` branch the page's `citation_doi` is read and used to drive the Crossref/OpenAlex lookups, but it is never added to the final DOI merge chain, so when those lookups fail the DOI that was already in hand is discarded.

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:445-452` (URL branch: `page = attempt(...); if page and page.get("kind") == "url": doi = page.get("doi")`) and the merged `doi = _pick(...)` chain at `:468-473` (`value(crossref,"doi")`, `value(oa_confident,"doi")`, `value(arxiv,"doi")`, `identifier if kind == "doi" else None`) — which omits `value(page,"doi")`.

**Why it is a problem:** For a URL input `identifier` is the URL, so the `kind == "doi"` guard is `False`; if Crossref and OpenAlex both fail (a realistic DataCite/Zenodo/figshare landing page where Crossref 404s on the DOI, or an OpenAlex hiccup), the final `doi` is `None` even though `page["doi"]` held a valid DOI — and, because `doi` is then falsy, the Unpaywall pass is skipped and `crossref_api`/`unpaywall_api` are `None`. Verified by driving `resolve()` with a stubbed page carrying `citation_doi="10.1234/real"` and both Crossref/OpenAlex raising: `resolve(...)["identifiers"]["doi"] is None`. Not covered: no recorded issue mentions `identifiers.doi`, `page.get("doi")`, or the URL-branch merge chain.

**Suggested fix:**
a) Add `value(page, "doi")` to the chain: `_pick(value(crossref,"doi"), value(oa_confident,"doi"), value(arxiv,"doi"), value(page,"doi"), identifier if kind == "doi" else None)`.
b) Or bind `page_doi = page.get("doi")` once and seed it into the `_pick(...)` so the discovered DOI can never be lost.
c) Add a regression test: landing page with `citation_doi`, Crossref + OpenAlex failing → `identifiers.doi` equals the page DOI.

### Issue 90 — SHOULD FIX — `fetch_paper.py`'s URL branch skips the title-search fallback whenever the page declares a DOI

The URL branch's OpenAlex-by-title fallback is an `elif`, so once the page supplies a DOI the fallback is unreachable — an identifier-lookup failure on that DOI ends OpenAlex resolution instead of retrying by title.

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:445-452` (URL branch: `if doi: … openalex = attempt("OpenAlex", lambda: lookup_openalex("https://doi.org/" + doi, …))` then `elif page.get("title"): openalex = attempt("OpenAlex", lambda: search_openalex(page["title"], …))`). Contrast the arXiv branch in the same function (`:437-440`), which falls back on **any** identifier failure (`if not openalex and (arxiv or {}).get("title"): openalex = attempt(... search_openalex ...)`), and `literature.py`'s arXiv branch (Issue 71).

**Why it is a problem:** Verified by driving `resolve()` with a URL page carrying both `citation_doi` and a title, and Crossref + OpenAlex-by-DOI both raising `NetError(kind="http")`: `search_openalex` is invoked **0 times**, whereas the identical page without a DOI resolves via the title search. So a resolvable landing page fails to resolve OpenAlex metadata purely because it declared a DOI the DOI-indexed APIs did not know. Not covered: Issue 71 is scoped to `literature.py` and explicitly lists the `fetch_paper.py` items it does not cover; the URL-branch `elif` is not recorded anywhere.

**Suggested fix:**
a) Drop the `elif` and mirror the arXiv branch — attempt Crossref/OpenAlex by DOI, then `if not openalex and page.get("title"): openalex = attempt("OpenAlex", lambda: search_openalex(page["title"], …))`.
b) Or restructure as `if doi: <by-doi attempts>` followed by an unconditional `if not openalex and page.get("title"): <title search>`.
c) Add a regression test: URL + DOI + identifier 404 → title search attempted.

### Issue 91 — SHOULD FIX — `fetch_paper.py`'s `metadata.abstract` ranks the unverified title-search record above the landing page's own abstract

The abstract merge tuple places the OpenAlex *title-search guess* before the landing page's own `citation_abstract`, unlike every sibling field — contradicting the function's own comment that a title-search match "must not silently override better metadata".

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:495` (`metadata.abstract = _pick(value(arxiv,"abstract"), value(crossref,"abstract"), value(oa_confident,"abstract"), value(oa_search,"abstract"), value(page,"abstract"))`) vs the sibling orders at `:486-488` (`title_order`/`author_order`/`year_order`) and `:494` (`venue`), which all place `page` **before** `oa_search`; the policy comment at `:456-462` ("one resolved by a title search is a guess and must not silently override better metadata").

**Why it is a problem:** `oa_search` (the guess, flagged in a note) outranks `page` only for `abstract`. Verified by driving `resolve()` with a URL input whose page emits `citation_abstract`/`og:description` and whose title OpenAlex matches only by title search to a different paper: `metadata.abstract` becomes the *other* paper's abstract, silently replacing the page's genuine one. Not covered by any recorded issue.

**Suggested fix:**
a) Move `value(page, "abstract")` ahead of `value(oa_search, "abstract")`, matching the title/author/year/venue order.
b) Or never let `oa_search` feed `metadata.*` (use `oa_confident` only), leaving the guess strictly for `notes`.
c) Add a regression test: URL + no DOI + title-search match → `metadata.abstract` comes from the page.

### Issue 92 — SHOULD FIX — `fetch_paper.py`'s open-access fields and counts use the conflated `openalex` binding, so a title-search guess overrides verified data

`resolve` splits `oa_confident`/`oa_search` for `metadata`, but `candidates`/`best_pdf`, `oa_status`, and `counts` read the conflated `openalex` variable, so a title-search guess is treated as authoritative for those fields.

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:500-509` (`candidates` includes `(openalex or {}).get("pdf")`; `best_pdf` at `:519`), `:521` (`open_access.oa_status = _pick((openalex or {}).get("oa_status"), (unpaywall or {}).get("oa_status"))`), `:526-527` (`counts.referenced_works`/`cited_by` = `_pick(value(oa_confident, …), value(openalex, …), value(crossref, …))`); the guess/`oa_confident` split and policy comment at `:456-462`.

**Why it is a problem:** When the DOI-keyed OpenAlex lookup fails but the title search returns a *different* paper, `oa_search`'s PDF enters `candidates` and can become `best_pdf`, its `oa_status` is preferred over the DOI-keyed Unpaywall status, and (when `oa_confident` is `None`) its `reference_count`/`cited_by_count` are preferred over Crossref's verified counts. Verified by driving `resolve()` with a stubbed title-search match carrying `reference_count=7`/`oa_status="gold"`/a `pdf`, Crossref returning verified counts, and Unpaywall returning `oa_status="closed"`/`is_oa=False`: the record ends up with the guess's counts and `oa_status == "gold"` beside `is_oa == False` — two fields of the same record contradicting each other. This contradicts the `:459-460` "must not silently override better metadata" policy that `metadata` honours. Not covered by any recorded issue.

**Suggested fix:**
a) Use `oa_confident` (not `openalex`) for `candidates`, `oa_status`, and `counts`, so the guess surfaces only via `notes`/`identifiers.openalex_match`.
b) Or, if a guess may fill gaps, apply it strictly last — `counts` = `_pick(oa_confident.…, crossref.…, oa_search.…)`, `oa_status` = `_pick(unpaywall.oa_status, oa_confident.oa_status, oa_search.oa_status)` — and exclude `oa_search.pdf` from `candidates`/`best_pdf`.
c) Add a regression test: DOI + verified Crossref counts + Unpaywall + a title-search guess → the record keeps the verified counts/OA status and the guess's PDF is absent from `candidates`.

### Issue 93 — SHOULD FIX — `fetch_paper.py`'s `strip_markup` unescapes HTML entities *before* stripping tags, so escaped literal angle brackets in an abstract are silently deleted

`strip_markup` runs `html.unescape` first and then strips `<…>` runs, so an escaped literal (`&lt;a,b&gt;`) becomes `<a,b>`, which the strip then treats as markup and deletes together with the text between the brackets.

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:116-120` (`strip_markup` = `collapse(re.sub(r"<[^>]+>", " ", html.unescape(text)))`), consumed by `:253` (`lookup_crossref`: `record["abstract"] = strip_markup(message.get("abstract"))`) and `:390-392` (`lookup_url`: `record["abstract"] = strip_markup(meta.get(...) or …)`); surfaced as `resolve`'s `metadata["abstract"]`.

**Why it is a problem:** Verified by direct calls and through `resolve()` with a stubbed Crossref record: `strip_markup("The set &lt;a,b&gt; is closed.")` → `"The set is closed."` and `strip_markup("prove d &lt; 0.2 and r &gt; 0.5 here")` → `"prove d 0.5 here"` — the bracketed spans are dropped. This is reachable **today** on the DOI/arXiv paths (Crossref abstracts are JATS-XML with exactly these escapes), not latent behind Issue 45, so an abstract the agent then records in a note is silently corrupted (the skill is instructed never to fabricate, yet the truncated text is written verbatim). Not covered: the report never mentions `strip_markup`, the unescape ordering, or abstract corruption (Issue 86 is the tag/attr regexes, Issue 45 the parameter shadow, Issue 50 the `setdefault` author collapse).

**Suggested fix:**
a) Strip tags first, then unescape — `collapse(html.unescape(re.sub(r"<[^>]+>", " ", text)))` — so `<jats:p>a &lt; b</jats:p>` → `a < b`.
b) Or replace the regex strip with the stdlib `html.parser.HTMLParser` (collect text nodes), which never mistakes an escaped literal for a tag.
c) For the URL-page path, unescape exactly once at the end — keep `_parse_meta`'s raw `content` and apply `html.unescape` only after `strip_markup`.

### Issue 94 — SHOULD FIX — `fetch_paper.py`'s URL branch returns the page's PDF link verbatim, so a relative `citation_pdf_url`/`<link href>` becomes a non-navigable `best_pdf`

The PDF extraction returns the raw `href`/`content` and never resolves it against the page URL, so a relative reference produces a link the agent/user cannot fetch.

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:360-366` (`_find_pdf_link` returns `attrs["href"]` unmodified) and `:389` (`record["pdf"] = meta.get("citation_pdf_url") or _find_pdf_link(html)`); consumed at `resolve:499-519` (`candidates` → `open_access["best_pdf"] = candidates[0]`); `urllib.parse.urljoin` is never used.

**Why it is a problem:** Relative `citation_pdf_url`/`href` values are legal and common. Verified by direct call: `_find_pdf_link('<link rel="alternate" type="application/pdf" href="/download/x.pdf">')` → `"/download/x.pdf"`, and a stubbed page with `citation_pdf_url="/pdf/rel.pdf"` yields `record["pdf"] == "/pdf/rel.pdf"`, so `resolve` returns `open_access.best_pdf == "/pdf/rel.pdf"` — a link the helper's contract ("the best open-access PDF link found") implies is fetchable. Not covered: no recorded issue mentions `urljoin`, relative links, or base-URL resolution (Issue 86 concerns a `>` inside a quoted attribute and the un-escaped `href`, not a relative one).

**Suggested fix:**
a) Resolve against the page URL — `record["pdf"] = urllib.parse.urljoin(url, meta.get("citation_pdf_url") or _find_pdf_link(html) or "") or None` — and normalize anything appended to `candidates`.
b) Or have `_find_pdf_link(html, base)` / `lookup_url` join every extracted `href`/`content` against `url`.
c) Add a regression test: a page whose `citation_pdf_url`/`<link>` is root-relative must yield an absolute `best_pdf`.

### Issue 95 — SHOULD FIX — `_pick` cannot tell a real `0` from "unknown", so an OpenAlex zero silently overrides the other sources' non-zero counts

`_pick`'s presence test accepts `0`, and OpenAlex reports `0` whenever its `referenced_works` list is absent — so a confident OpenAlex zero masks Crossref's verified reference/citation counts.

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:400-406` (`_pick`: `if value not in (None, "", [], {})`), fed by `_openalex_record:285-286` (`reference_count = len(work.get("referenced_works") or [])`, `cited_by_count = work.get("cited_by_count")`) into `resolve:526-527` (`counts = _pick(value(oa_confident, …), value(openalex, …), value(crossref, …))`).

**Why it is a problem:** `0` passes `_pick`'s test, so a verified Crossref count is discarded whenever OpenAlex reports `0`. Verified: `_pick(0, 30) == 0`, and driving `resolve()` with a confident (identifier-matched) OpenAlex record plus a Crossref record carrying `reference-count: 30` / `is-referenced-by-count: 7` yields `counts == {"referenced_works": 0, "cited_by": 0}` with no note flagging the disagreement. This is distinct from Issue 92: 92's root cause is the conflated `openalex` binding letting a *title-search guess* override verified data, and its fix keeps `oa_confident` first — so a **confident** OpenAlex `0` would still mask Crossref after applying 92.

**Suggested fix:**
a) Omit OpenAlex counts when the underlying list is empty — set `reference_count` in `_openalex_record` only when `referenced_works` is present/non-empty — so `_pick` falls through to Crossref.
b) Or decide counts by explicit source preference (prefer Crossref's non-zero `reference-count`/`is-referenced-by-count` over an OpenAlex `0`) using `is not None`/`> 0` logic for this field.
c) Record a note when the merged counts disagree across sources, so a genuine zero is distinguishable from a missing list, and add a regression test (confident OpenAlex `0` + Crossref non-zero → the non-zero value wins).

---

## Issues 96–104 (seventh, independent pass)

A further fully independent read-only pass over the whole change set — seven disjoint slices (`core/papers` Go, backend Go, the vendored skill scripts, the frontend libs, the frontend store/api/hooks, the frontend UI components, and the specs + skill docs), each deduplicated against the 95 already-recorded issues — surfaced the nine additional in-scope items below. The backend Go slice was re-verified clean (no new MUST FIX / SHOULD FIX item).

### Issue 96 — SHOULD FIX — `ApplyReview` and `ParseFlashcards` disagree on what a "card row" is, so a review of an ID-only placeholder row succeeds yet every reader still reports zero cards

**Location:** `core/papers/flashcards.go:423-430` (`ApplyReview` locates the card row by matching any raw table row's `id` cell: `if cellAt(splitCells(cleanLine(lines[i])), idIdx) == cardID { … }`) versus `core/papers/flashcards.go:161` (`parseCards` drops a row whose Front and Back are both empty: `if front == "" && back == "" { continue }` — comment "the template's placeholder").

**Why it is a problem:** The writer's notion of "this card exists" is strictly broader than the reader's, so the same file can be written and read back as empty. The bundled default deck `core/papers/skills/study-paper/assets/flashcards.md` ships exactly four ID-only placeholder rows (`| P1-01 | | | | | new |`), i.e. the artifact's own default state is a deck where the writer accepts ids the reader does not recognise. Verified: `ApplyReview(<assets/flashcards.md>, "P1-02", GradeGood, "2024-05-01")` returns `err=<nil>` and rewrites the row to `| P1-02 | | | | | review |` plus appends a log row, while `ParseFlashcards` on that output yields `cards=0 reviews=1`. Trigger: any caller that reaches `RecordCardReview` with a placeholder id. (The frontend's `FlashcardsReview` validates against the reader's card set and so cannot surface it today — which masks the defect rather than removing it.)

**Suggested fix:**
a) Make `ApplyReview`'s row scan skip empty-Front/Back rows exactly as `parseCards` does, so "card row" has a single definition.
b) Extract a shared predicate (e.g. `rowIsCard(cells, idIdx, frontIdx, backIdx)`) and call it from both `parseCards` and `ApplyReview`.
c) Or, if placeholders are meant to be ratable, keep them in `parseCards` too so reader and writer agree (and surface them as unfilled in the UI).

### Issue 97 — SHOULD FIX — `ApplyReview`'s review-row builder resolves the `grade` and `next/due` columns to the same cell on a grade-less log header, silently discarding the recorded grade

**Location:** `core/papers/flashcards.go:449-458` — `set([]string{"id"},0,cardID)`, `set([]string{"date","when"},1,date)`, `set([]string{"grade","rating","result"},2,string(grade))`, `set([]string{"next","due"},3,nextDue)`, each routed through `colOr(rHeader, fallback, names...)` (`core/papers/parser.go:698-707`), which matches by case-insensitive substring (`columnIndex`, `core/papers/parser.go:683-696`) and otherwise returns the positional fallback.

**Why it is a problem:** For a review-log header that carries a `Next due` column but no grade-like column — e.g. `| ID | Date | Next due |`, which `isReviewHeader` (`core/papers/flashcards.go:122-124`) deliberately accepts as a valid review log — the `grade` set finds no name match and lands on the positional fallback column 2, then the `next/due` set *matches* that same column 2 (`"next due"` contains `"next"`) and overwrites it. Verified: `ApplyReview(doc, "A1", GradeGood, "2024-03-01")` returns `err=<nil>` and appends `| A1 | 2024-03-01 | 2024-03-02 |`; `ParseFlashcards` reads it back as `ReviewEntry{ID:A1, Date:2024-03-01, Grade:"", NextDue:2024-03-02}`. The grade is lost and the frontend's `stateFromReviews` (which skips empty grades) never advances the card, so the user's rating silently no-ops. Trigger: an agent-authored deck whose review-log header omits the Grade column (the shipped template's log *does* carry `Grade`, so this needs a non-standard header, but `isReviewHeader` accepts it).

**Suggested fix:**
a) Resolve all four review columns in one pass with `columnIndexExcept` (excluding already-claimed indices, as `parseCards`/`parseReviews` do), so `next`/`due` cannot reclaim the grade cell.
b) Fail closed when a required field resolves only through the positional fallback (require an explicit header match for `id` and `grade` before writing), leaving the file untouched.
c) Tighten `isReviewHeader` to require a grade/rating/result column, so a log that cannot record a grade is not treated as reviewable.

### Issue 98 — SHOULD FIX — Both helpers serialize JSON with `ensure_ascii=False` and write it to an un-reconfigured `sys.stdout`, so on a non-UTF-8 stdout a non-Latin-1 metadata character raises an uncaught `UnicodeEncodeError` (traceback, exit 1)

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:594` + `:604` (`text = json.dumps(record, …, ensure_ascii=False)` … `sys.stdout.write(text + "\n")`) and `core/papers/skills/study-paper/scripts/literature.py:691` + `:701` (`output = json.dumps(record, …, ensure_ascii=False) + "\n"` … `sys.stdout.write(output)`). Neither script calls `sys.stdout.reconfigure(...)` or otherwise forces the stream encoding.

**Why it is a problem:** `ensure_ascii=False` emits raw Unicode, but `sys.stdout`'s charset is the process locale's, and any character outside that charset is unencodable under the default `strict` handler. Verified (driving each `main()` with a realistic non-ASCII record under `PYTHONIOENCODING=ascii`): `fetch_paper.py` dies at `sys.stdout.write(text + "\n")` with `UnicodeEncodeError: 'ascii' codec can't encode character '\xfc' in position 67` (process exit 1); `literature.py` dies at `sys.stdout.write(output)` with `… '\xdc' …` (exit 1). Trigger: a **direct helper run** (exactly what `SKILL.md:286-288` instructs) on a non-UTF-8 stdout — a Windows console (cp1252/cp437), a POSIX C/ASCII locale, or `PYTHONIOENCODING=ascii` — for any paper whose title/author/abstract carries `Müller`, CJK, Cyrillic, etc. The app RPC path is unaffected (it passes `--out` and `cmd.Stdout = io.Discard`), so the harm lands on the documented direct/agent path (`fetch_paper.py` is never invoked by the backend at all).

**Suggested fix:**
a) Force UTF-8 on the streams before writing: `sys.stdout.reconfigure(encoding="utf-8")` (and stderr) at the top of `main` (Python 3.7+).
b) Or write bytes explicitly: `sys.stdout.buffer.write(text.encode("utf-8") + b"\n")`, so the destination encoding is not inherited.
c) Or make only the stdout branch ASCII-safe (`ensure_ascii=True` for stdout, keeping `ensure_ascii=False` for the `--out` file) and add a smoke test that drives `main([...])` with a non-ASCII record under `PYTHONIOENCODING=ascii`.

### Issue 99 — SHOULD FIX — `fetch_paper.py`'s `lookup_url` decodes the landing page as UTF-8 with `"replace"` regardless of the response/`<meta charset>`, so a non-UTF-8 page yields U+FFFD mojibake in the very `citation_*` fields the helper extracts

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:377` (`html = raw[:400000].decode("utf-8", "replace")`), consumed by `_parse_meta`/`lookup_url` (`:349-383`). `http_get` (`:88-106`) returns only the body bytes, so the `Content-Type` charset is discarded and the page's `<meta charset>` / `<meta http-equiv="content-type">` is never consulted.

**Why it is a problem:** A hardcoded UTF-8 decode with the `replace` handler silently maps every non-UTF-8 byte to U+FFFD instead of using the declared charset. Verified on Windows-1252 page bytes: the current expression yields `content="Universit\ufffd de Gen\ufffdve"` / `content="M\ufffdller"`, whereas a `cp1252` decode yields `Université de Genève` / `Müller`. Trigger: a legacy publisher/landing page served as ISO-8859-1/Windows-1252 (common for the pre-2010 corpus Issue 85 also targets) whose `citation_*` metas carry accented names — the helper then reports mojibake that an agent following the study-paper flow may transcribe into the paper's note/appraisal, undermining the skill's own "never fabricate citations" guardrail. Currently latent behind Issue 45 (the `_parse_meta(html)` shadow crash fires first for any page carrying meta tags), exactly as Issues 50/86 are masked.

**Suggested fix:**
a) Decode with the declared charset: have `http_get` surface the `Content-Type` charset (or sniff the leading `<meta charset>`/`<meta http-equiv>`) and decode as `raw.decode(charset or "utf-8", "replace")`.
b) Or parse the HTML structurally (stdlib `html.parser.HTMLParser`) so charset detection and quote-aware attribute extraction come for free (the same remedy recorded for Issue 86 option b).
c) At minimum, fall back through a candidate list — try `utf-8`, then `cp1252`/`latin-1`, and only then `"replace"` — with a regression test decoding a Windows-1252 `citation_author` page to `Müller`.

### Issue 100 — SHOULD FIX — `classifyAnchor` has no `algorithm` anchor kind, so the skill's documented `algorithm` anchor is degraded to a verbatim quote: `Alg. N` resolves to `null` ("not found") while a bare `Algorithm` produces a false jump

**Location:** `frontend/src/lib/paperAnchors.ts:19` (`export type AnchorKind = 'section' | 'figure' | 'table' | 'equation' | 'text'`), `:55-61` (the `sec`/`fig`/`tab`/`eq` classifiers), `:65` (the bare-keyword refusal list `/^(?:fig(?:ure)?|tab(?:le)?|eq(?:uation)?|sec(?:tion)?|§)\b/i`), `:67` (`return { kind: 'text', token: s }`).

**Why it is a problem:** The bundled skill that authors the `anchors` tells the model there are six anchor kinds — "section, figure, table, equation, **algorithm**, or page" (`core/papers/skills/study-paper/SKILL.md:77-78`; repeated in `assets/note-template.md:14,54`) — but `classifyAnchor` recognises only four structural kinds (plus `§`). Verified (throwaway vitest suite importing the real module; source `# Paper\n\nAlgorithm 2: Training loop\n\nAlgorithm 1: Inference\n\nFigure 3: Results\n`): `classifyAnchor('Alg. 2')` and `('Alg 2')` return `{kind:'text', …}` and their resolution is `null` — an anchor pointing at an existing line is reported "not found"; `classifyAnchor('Algorithm')` (a structural keyword without a number) is **not** refused and resolves to the first line containing `algorithm` — a false jump that the module header explicitly promises never to make ("a structural keyword without a number … resolves to `null` … no false jumps", `:11-13`). Trigger: any card whose anchor points at an algorithm, in the same abbreviated form used for every other kind (`Fig.`/`Sec.`/`Eq.`/`Tab.`).

**Suggested fix:**
a) Add the kind symmetrically: `AnchorKind += 'algorithm'`; add `^alg(?:orithm)?\.?\s*(\d+)` → `{kind:'algorithm', token:m[1]}`; add `algo(?:rithm)?` to the bare-keyword guard at `:65`; and map the kind in the numbered-line regex.
b) Or, if algorithm anchors are intentionally treated as quotes, add `algo(?:rithm)?` to the refusal guard so a bare `Algorithm` resolves to `null`, and document in the module header that `algorithm` is deliberately not a structural kind (today the header and the skill disagree).
c) Add regression coverage (none exists today): `classifyAnchor('Alg. 2')` → `{kind:'algorithm',token:'2'}`, `resolveAnchor('…Algorithm 2: …', {ref:'Alg. 2'})` → line 2, and `{ref:'Algorithm'}` → `null`.

### Issue 101 — SHOULD FIX — `paperStore.lastSyncAt` documents a convergence "status-events watchdog" consumer that does not exist, and the papers sync path has no recovery for a lost/coalesced `papers:changed`

**Location:** `frontend/src/stores/paperStore.ts:78-80` (field doc: "Lets a consumer (e.g. a status-events watchdog) verify an event-driven refresh landed") and `:263-265` (`selectPapersSyncAt`); `frontend/src/hooks/usePapersEvents.ts:39-70` (no watchdog); contrast the sibling `frontend/src/hooks/useResearchStatusEvents.ts:100-124` (`RESEARCH_WATCHDOG_DELAY_MS = 1500` + `scheduleResearchWatchdog`).

**Why it is a problem:** The papers library is refreshed only on project switch and on a *received* `papers:changed` (`usePapersEvents.refresh` / `applyPapersChanged`); there is no convergence fallback for a dropped/coalesced watcher event or a single failed `GetPapers`, so the panel can stay stale indefinitely. The only code consumer of `lastSyncAt` is `PaperWorkspace.tsx:112` (a refresh key), not the watchdog its doc names — verified: a `lastSyncAt`/`selectPapersSyncAt` search finds only `PaperWorkspace.tsx:112` and a comment in `useComparisons.ts:58`, and a `watchdog` search finds no implementation in either the hook or the store. Additionally, `applyPapersChanged` (`paperStore.ts:485-493`) returns `true` unconditionally after `await fetchPaperLibrary(projectId)`, even when that fetch errored or was superseded, so its documented return ("true when the loaded library was invalidated") cannot distinguish applied from dropped and a caller cannot retry. Trigger: the skill appends a section to `paper.md` and the fsnotify event is coalesced/lost (or `GetPapers` fails transiently) — the user sees the paper without the new section until an unrelated event fires. (Distinct from Issue 9, which is about the debounce never applying to well-formed payloads; distinct from Issues 6/22, which assume a refresh that *did* land.)

**Suggested fix:**
a) Port the sibling watchdog into `usePapersEvents`: on each `papers:changed`, schedule a delayed refresh that runs only if `lastSyncAt` did not advance past the scheduling timestamp (mirror `scheduleResearchWatchdog` and its cleanup).
b) Or drop the watchdog claim from the `lastSyncAt` doc and document it purely as `PaperWorkspace`'s refresh key, making the absence of a convergence guarantee explicit.
c) Or make convergence caller-driven: have `fetchPaperLibrary` return whether its payload applied (drop when `ticket !== latestFetch`, `false` on error) and have `applyPapersChanged` propagate it, so a failed/superseded invalidation can be re-scheduled instead of silently returning `true`.

### Issue 102 — SHOULD FIX — The new `fileViewerStore.test.ts` documents the "paper tab is never persisted" contract but asserts only the `virtual` flag, so no test detects a regression of the `partialize` exclusion it names

**Location:** `frontend/src/stores/fileViewerStore.test.ts:1-3` (header: "…routes it to the PaperWorkspace and never persists it") and `:13-22` (asserts only `state.files[path]?.virtual === true`, `openTabs`, `activeFile`); the guard that actually implements "never persists" is `frontend/src/stores/fileViewerStore.ts:379-380` (`openTabs: state.openTabs.filter(p => !state.files[p]?.virtual)`, `activeFile: … ? null : …`).

**Why it is a problem:** The test pins the *mechanism* (the `virtual` flag) but never observes the *outcome* it advertises. Deleting the `!state.files[p]?.virtual` filter (or the `activeFile` null-ing) would persist the `c0wrk:paper:<slug>` pseudo-path; on restart the tab rehydrates with no `files[…]` entry and the data loader treats it as an on-disk file — and nothing fails. This is the same "test that cannot fail on the regression it documents" class as Issues 26, 31, 32, 39. Trigger: any future edit to `partialize`; the suite stays green.

**Suggested fix:**
a) Add a test that runs the snapshot through the store's `partialize` (or a jsdom `localStorage` probe) with a virtual paper tab open and asserts the paper pseudo-path is absent from the persisted `openTabs`/`activeFile`.
b) Or assert the same outcome via `useFileViewerStore.persist.getOptions().partialize(...)` directly, so the filter is pinned without depending on storage availability.
c) Or narrow the header's claim to "opens a virtual tab" so the test no longer advertises persistence coverage it lacks.

### Issue 103 — SHOULD FIX — The Research panel's `[Dashboard | Papers]` control uses `role="tab"`/`role="tablist"` with no `tabpanel` relationship and no tab keyboard interaction, an incorrect ARIA pattern that also diverges from the app's toggle convention

**Location:** `frontend/src/components/research/index.tsx:44` (`role="tablist"`) and `:55-57` (`role="tab"` + `aria-selected`). The panels it swaps (the dashboard body and `<PapersView/>`) are plain `<div>`s with no `role="tabpanel"`.

**Why it is a problem:** `role="tab"` obliges the ARIA tabs pattern — each tab needs `aria-controls` pointing at a `role="tabpanel"` container (`aria-labelledby` back to the tab), and the tablist needs roving `tabIndex` + Left/Right (Home/End) arrow navigation. None is present: `role="tabpanel"` appears nowhere in `frontend/src`, the control carries no `aria-controls`, and there is no `tabIndex`/`onKeyDown`/arrow handling (every tab stays in the tab order). Verified: `role="tab(list)?"` occurs only at `index.tsx:44,55`, and every other view toggle in the app is a `<button aria-pressed=…>` (`ResearchToggle.tsx:102`, `GoalToggle.tsx:55`, `BookmarkStar.tsx:75`, `E2SToggle.tsx:45`). Trigger: operate the Research segment control with a screen reader or keyboard — AT announces tabs whose panels are not programmatically associated, and keyboard users must Tab through each tab instead of arrowing (WCAG 4.1.2 / ARIA tabs-pattern non-conformance). `frontend/eslint.config.js` has no `jsx-a11y` plugin and `researchSegment.test.tsx` asserts only `aria-selected`, so nothing detects it.

**Suggested fix:**
a) Drop the tab roles and use the project convention: `<button type="button" aria-pressed={selected} …>` (as `ResearchToggle`/`E2SToggle` do) — no tabpanel semantics required.
b) Or implement the full ARIA tabs pattern: a stable `id` per tab + `aria-controls` referencing a `role="tabpanel"` wrapper (with `aria-labelledby`) around the dashboard body and `<PapersView/>`, plus roving `tabIndex` and Left/Right/Home/End handling.
c) Or, minimally, keep the roles but add the `aria-controls`/`role="tabpanel"` wiring, the roving-tabindex arrow-key behaviour, and `aria-orientation="horizontal"`.

### Issue 104 — SHOULD FIX — The bundled `note-template.md` omits §4 (Contributions) from its Skim mapping, contradicting `SKILL.md`, so a doc-following Skim produces a note missing a mode-required output

**Location:** `core/papers/skills/study-paper/assets/note-template.md:5` ("**Skim** → §1–3, §7, §8, §9") vs `core/papers/skills/study-paper/SKILL.md:38` (Skim output = "map + contributions + TL;DR + ≤3 red flags + reading decision") and `SKILL.md:114` (Mode 1 step 3: "**List the contributions** — the contributions as the authors frame them, anchored").

**Why it is a problem:** The template's usage block is the authoritative "which sections each mode fills" instruction, and the section it skips — `## 4. Contributions (as the authors frame them — do not upgrade them)` (`note-template.md:43`) — is not subsumed by §3 (whose rows are only Motivation / Problem statement / Core idea / Headline result, `:34-41`). So a reader who runs a Skim exactly as the bundled template prescribes produces a note with no contributions table even though `SKILL.md` makes contributions a required Skim output. This is the same skill-internal doc-drift class as Issues 29 and 59. (Secondary, same mapping: `note-template.md:7` lists `§4, §5` for Implement, which `SKILL.md` Mode 3 does not produce — harmless extra, but the Skim omission is the impactful half.)

**Suggested fix:**
a) Add §4 to the Skim mapping: "**Skim** → §1–4, §7, §8, §9 (keep it to about one screen)".
b) Or, if a triage Skim deliberately omits contributions, drop `contributions` from `SKILL.md:38` and remove Mode 1 step 3 (`SKILL.md:114`) so both docs agree.
c) Or move the contributions table under §3 (the structured map) and drop the standalone §4, so the Skim mapping needs no change.

---

## Issues 105–112 (eighth, independent pass)

Two further independent verification waves re-read the whole change set (the same seven slices, fresh readers, deduplicated against Issues 1–104) and surfaced eight additional in-scope items. The `specs`/skill-docs slice and the frontend store/api/hooks slice were re-verified clean on this wave.

### Issue 105 — SHOULD FIX — The Go `flashcards.go` parser resolves columns by *substring* while the frontend twin resolves them by *anchored/word-boundary* regex, so a non-canonical header yields different card ids / anchors / tags across the boundary

**Location:** `core/papers/parser.go:683-696` (`columnIndex`, the `strings.Contains(hn, n)` at `:687`), consumed via `colOr`/`columnIndexExcept` at `core/papers/flashcards.go:149` (`idIdx`), `:152` (`anchorIdx`), `:156` (`tagIdx`), `:177` (review `idIdx`) and `:420-421` (`ApplyReview`) — versus `frontend/src/lib/flashcards.ts:73-91` (`pickColumn` with `ID_COL = /^id\b/`, `ANCHOR_COL = /anchor|source|location|\bref\b|where|page/`, `TAG_COL = /\btag\b|topic|concept|\blabel\b/`, `NEXT_DUE_COL = /next|\bdue\b/`), applied at `:138/:141/:142/:165`.

**Why it is a problem:** The Go resolver matches every column name as a case-insensitive **substring**; the frontend matches the same names as **anchored/word-boundary** regexes. Go is strictly more permissive, so a header cell that merely *contains* the keyword (but is not the keyword) is claimed by Go and ignored by the frontend — while the file header and `specs/domains/papers.md:144` assert the two parsers "mirror … so the review a reader sees and the review the backend records agree". Trigger: a deck whose header deviates from the shipped `| ID | Front (question) | Back (answer) | Anchor | Tag | Stage |` (SKILL.md/§2 invites copying the block or adding rows) — e.g. `Card ID`, `Reference`, `Tags`. For a `Card ID` deck the backend resolves the id cell while the UI resolves `''`, so `commitFlashcardReview`/`RecordCardReview` receives `cardId:''` and the review silently no-ops; anchor/tag columns diverge the same way. This is a distinct root cause from Issue 65 (the *absent* id column + Go's positional fallback; Issue 65's own fix `columnIndex(t.Header,"id")` still substring-matches `Card ID`) and from Issue 47 (emphasis in cell values).

**Suggested fix:**
a) Align Go to the frontend's token semantics: add a word-boundary `columnIndex` variant and use it for `id`, `ref`, `tag`, `label`, `due`, so `Card ID`/`Reference`/`Tags` resolve as absent — matching `pickColumn`.
b) Or align the frontend to Go by relaxing the TS constants (`ID_COL=/id/`, drop the `\b` from `ref`/`tag`/`label`/`due`), accepting the looser matching on both sides.
c) Add a Go+TS parity test over one deck whose header uses `Card ID` / `Reference` / `Tags` and assert the two sides yield identical card ids, anchors and tags (no test covers a non-`ID` header today).

### Issue 106 — SHOULD FIX — `paperAnchors.numberedLineRe` matches a sub-numbered float, so `Fig. 2` jumps to `Figure 2.1` (a different caption), violating the module's "no false jumps" contract

**Location:** `frontend/src/lib/paperAnchors.ts:84-86` (`numberedLineRe` = `` new RegExp(`\\b${word}\\.?\\s*\\(?${escapeRe(token)}\\)?\\b`, 'i') ``), reached from `resolveNeedle` (`:103-108`, first matching line wins); contrast `sectionHeadingMatches` (`:79-82`), which guards the same risk with `\.(?!\d)`.

**Why it is a problem:** The trailing `\b` only rejects a following *word* character; for token `2` the character after `2` in `Figure 2.1` is `.`, which is a word boundary, so a plain `Fig. 2` anchor matches the sub-numbered caption. `resolveNeedle` returns the first matching line, so the anchor jumps to `Figure 2.1` even when a `Figure 2` line exists. Verified by executing the module: `resolveAnchor('Figure 2.1: sub one\n\nFigure 2: main', {ref:'Fig. 2'})` → `{line:0, …}` (the `Figure 2.1` line, not line 2), and `resolveAnchor('Table 3.1: a\nTable 3: b', {ref:'Tab. 3'})` → line 0. Trigger: any source numbered by section (`Figure 2.1`, `Table 3.1`, `Equation (1.2)`) with an anchor `Fig. 2` / `Tab. 3` / `Eq. 1`. Distinct from Issue 41 (the sub-*label* false negative `Fig. 2a`): Issue 41's fix (b) replaces `\b` with `(?!\d)`, under which `2` still matches `2.`, so it does not fix this.

**Suggested fix:**
a) Guard the trailing boundary like `sectionHeadingMatches`: `…\)?(?![.\d])` (so `2` still matches `2`, `2:`, `2)` but not `2.1`).
b) Capture the whole number (`(\d+(?:\.\d+)*)`) in the figure/table/equation classifiers and anchor with `(?![.\d])`, so `Tab. 3` never aliases `Table 3.1`.
c) Add regression tests: `{ref:'Fig. 2'}` over `[Figure 2.1, Figure 2]` → the `Figure 2` line; likewise `Tab. 3`/`Eq. 1` over `3.1`/`(1.2)`.

### Issue 107 — SHOULD FIX — The two `papers:changed` "acceptance" tests build their own watcher, so the production watch wiring in `switchProjectSetupWatcher` has no test coverage and can regress silently

**Location:** `backend/frontend_api_papers_library_test.go:470,490` (`TestPapersFileChanged_EmitsWithoutResearch`) and `:548,565` (`TestComparisonsFileChanged_EmitsWithoutResearch`); the production wiring they claim to cover is `backend/frontend_api_project.go:660-668` (`papersRoot` `MkdirAll` + `watcher.WatchTree(papersRoot)`) and `:677-685` (`comparisonsRoot` likewise).

**Why it is a problem:** Both tests are documented as "the acceptance test for the hybrid path", but each constructs its own `workspace.NewWatcher(ws, <inline copy of the callback>)` and then performs the very action under test itself (`watcher.WatchTree(papersRoot)` / `WatchTree(comparisonsRoot)`), so the production `WatchTree` calls in `switchProjectSetupWatcher` are never exercised — the only test that calls that function (`frontend_api_watcher_test.go:241`) takes the No-Project early-return and never reaches lines 660-685. Verified: `grep -rn "switchProjectSetupWatcher(" backend/*_test.go` returns only the No-Project case, and `grep -rn "WatchTree(papersRoot)\|WatchTree(comparisonsRoot)" backend/*_test.go` returns only the two self-wiring assertions. Deleting the `papersRoot` block — the exact behaviour this feature exists to provide (a paper edit must emit `papers:changed` with RESEARCH off) — leaves `go test ./backend/...`, `go vet` and `golangci-lint` green while the Papers panel silently stops refreshing in hybrid mode. Same class as Issues 26, 31, 39, 102.

**Suggested fix:**
a) Add a test that drives the real `f.switchProjectSetupWatcher(p)` for a CODE project (a `project.ProjectInfo` with a workspace, no vector manager) and asserts that writing under `<effective-research-root>/papers/…` (and `…/comparisons/…`) emits `papers:changed` while RESEARCH is off.
b) Or extract the papers/comparisons watch setup into a small helper (e.g. `watchPapersRoots(watcher, p)`) called from `switchProjectSetupWatcher`, and point these two tests at that helper against a real `workspace.Watcher`.

### Issue 108 — SHOULD FIX — The paper workspace's section switcher exposes no selected state to assistive tech (only a `data-active` attribute), so an AT user cannot tell which section is active

**Location:** `frontend/src/components/papers/PaperWorkspace.tsx:236-257` — the `<nav data-testid="paper-sections">` and its buttons (`data-active={section === s.id}` at `:245`; active styling only at `:248-251`). Contrast the app's toggle convention (`aria-pressed` in `ResearchToggle.tsx:102`, `GoalToggle.tsx:55`, `E2SToggle.tsx:45`, `BookmarkStar.tsx:75`).

**Why it is a problem:** The control swaps Overview/Note/Appraisal/Compare/Flashcards/Source/Literature, but the only marker of the current item is the non-semantic `data-active` attribute plus a CSS class — there is no `aria-current`/`aria-pressed`/`aria-selected` and no `role`. Verified: `grep -nE "aria-|role=" frontend/src/components/papers/PaperWorkspace.tsx` returns nothing, and `grep -rn "aria-current|aria-pressed|aria-selected" frontend/src/components/papers` (excluding tests) returns nothing. `PaperWorkspace.test.tsx` asserts the active section via `[data-active="true"]` only, so nothing detects the missing semantics. Trigger: navigate the workspace with a screen reader — AT announces interchangeable buttons and never indicates the selected section (WCAG 4.1.2). Same class as Issue 103 (the sibling Research segment control).

**Suggested fix:**
a) Use the project toggle convention: add `aria-pressed={section === s.id}` (or `aria-current={section === s.id ? 'true' : undefined}`) to each section button — no tabpanel wiring required.
b) Or implement the correct ARIA tabs pattern: `role="tablist"` on the `<nav>`, `role="tab"` + `aria-selected` + `id`/`aria-controls` per button, a `role="tabpanel"` (with `aria-labelledby`) around the rendered section, and roving `tabIndex` + Left/Right/Home/End handling.
c) At minimum add `aria-current` so the active section is programmatically determinable, keeping `data-active` for the tests.

### Issue 109 — SHOULD FIX — Additional documented-but-unconsumed frontend surface: `useComparisons`' `dir`/`fileName`/`comparisonsDirFor` export and `PaperArtifact.missing` are dead state with docs stating a consumer that does not exist

**Location:** `frontend/src/components/papers/useComparisons.ts:26` (`ComparisonArtifact.fileName`, written at `:88,90`), `:31-34` (`ComparisonsState.dir`) and `:46-48` (`comparisonsDirFor`, doc: "Exported so the Compare section can render the path it looked at"); `frontend/src/components/papers/usePaperArtifacts.ts:46` (`PaperArtifact.missing`, doc "True when none of the section's candidate files exist on disk", written at `:54,58,108,114`).

**Why it is a problem:** The Compare section renders no path (its empty/fallback states use the generic `PaperMarkdownSection` text; `CompareMatrix` takes only `{slug, content}` and `PaperWorkspace.tsx:298-301` passes only `key`/`slug`/`content`), so `comparisons.dir`, `ComparisonArtifact.fileName` and the exported `comparisonsDirFor` have no production reader — verified: `grep -rn "comparisonsDirFor" frontend/src` returns only its definition (`:48`) and the internal call (`:62`), and `fileName` is read nowhere outside `useComparisons` itself. Likewise `PaperArtifact.missing` is read only by `usePaperArtifacts.test.tsx` (the empty state is driven purely by `content === ''`), so "no candidate file exists" and "the file exists but is empty" are conflated despite the field/contract implying they are distinguished (verified: `grep -rn "missing" PaperMarkdownSection.tsx PaperSourceView.tsx FlashcardsReview.tsx` → nothing). This is the same "dead state + doc drift / documented field with no consumer" class as Issues 12, 25 and 38; a maintainer trusting the docs may wire a consumer to a field nothing needs.

**Suggested fix:**
a) Remove the unconsumed surface (`ComparisonsState.dir`, the `comparisonsDirFor` export, `ComparisonArtifact.fileName`, `PaperArtifact.missing`) and their tests, and trim the docs/header lines accordingly.
b) Or make them real: render the looked-at path (`comparisons.dir`) in the Compare empty state, and render a distinct `missing` empty state ("No note artifact exists on disk" vs "The note is empty") so the documented distinction is actually surfaced.
c) At minimum correct the `comparisonsDirFor` rationale and the `missing`/`dir` docs to state they are informational and currently unconsumed.

### Issue 110 — SHOULD FIX — `literature.py` appends Semantic Scholar results without the identity-dedup pass the Crossref results get, so one work is listed twice

**Location:** `core/papers/skills/study-paper/scripts/literature.py:534` and `:540` (`predecessors.extend(item for item in extra if (item.get("title") or item.get("doi")))` / the same for `citing`), versus the Crossref merge three blocks above at `:501-509`, which builds a `present` identity set and skips `key in present`.

**Why it is a problem:** The Crossref branch dedups extras against the already-collected works; the S2 branch only filters *empty* items and never consults an identity set, so a work already returned by OpenAlex/Crossref is appended again when S2 is enabled (`--semantic-scholar` or an ambient `S2_API_KEY`, which turns `use_s2` on at `:518-519`). Verified (shipped `collect()` with the network layer stubbed): a shared Crossref item yields `predecessors=1`, the same item via S2 yields `predecessors=2` including a `origin='semantic-scholar'` duplicate, and `citing` shows the same duplication. Reachable from the app, not just the CLI: `runLiteratureHelper` (`backend/frontend_api_papers_literature.go:181-190`) spawns the helper with an inherited environment, so a shell `S2_API_KEY` silently enables the S2 leg of every app-triggered lookup. No recorded issue mentions the `semantic_scholar`/`_s2_summary` merge path (grep: 0 hits).

**Suggested fix:**
a) Route the S2 extras through the same identity set: guard `predecessors.extend(...)`/`citing.extend(...)` with the same `key not in present` test.
b) Extract one shared `merge_unique(items, present)` helper and call it from the Crossref, S2-references and S2-citations sites so the three legs cannot drift again.
c) While doing (a)/(b), normalise the identity key (lowercase the DOI) — `find_contradictions:409` and the frontend's `workIdentity` lowercase, this merge does not.

### Issue 111 — SHOULD FIX — `fetch_paper.py` reports a URL that yielded no metadata as *resolved* (exit 0) with an all-null record and `sources_used: ["landing page"]`

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:379` (`lookup_url` returns a record for *any* 200 response), `:415-431` (`attempt` appends the label to `state["sources"]` for any non-`None` value), the guard at `:530-538` (`if not state["sources"]: … raise`), `:605` (`return 0`).

**Why it is a problem:** Because `lookup_url` always returns a dict, `attempt` always records the page as a reached source, so an extraction that found nothing is indistinguishable from a successful resolve: the only failure test is "was any source reached", never "did the reached source yield anything". The documented contract (`0 resolved successfully` / `1 … could not be resolved`) is therefore violated. Verified live: `python3 fetch_paper.py https://www.ietf.org/rfc/rfc2119.txt` exits 0 with `sources_used: ['landing page']`, empty `notes`, and every metadata field `null`. Trigger: any URL whose response carries no usable `<meta name|property|http-equiv=… content=…>` (a non-HTML 200, or an HTML page whose metadata is only in `<title>`/JSON-LD). Not covered by Issues 89-92 (which presuppose the page *did* yield `doi`/`title`/`abstract`) nor by Issue 45 (the `_parse_meta` crash for pages that *do* carry a meta tag).

**Suggested fix:**
a) Treat an empty page as no result: only count the page as a source when it carries something — `if page and (page.get("title") or page.get("doi") or page.get("pdf"))` — else fall through to the existing `ResolveError` (exit 1).
b) Or make `lookup_url` raise `ResolveError("no citation metadata found on %s")` when the extracted record has no title/doi/pdf, so `attempt` records a note and no source.
c) Add a regression test: a 200 HTML body with no `citation_*`/`og:*` tags → non-zero exit (and no `sources_used` entry).

### Issue 112 — SHOULD FIX — `fetch_paper.py` sources `open_access.is_oa` only from Unpaywall, so the default run emits `oa_status: "bronze"` beside `is_oa: null` although OpenAlex supplied `is_oa`

**Location:** `core/papers/skills/study-paper/scripts/fetch_paper.py:289-291` (`_openalex_record`: `record["oa_status"] = open_access.get("oa_status")` — `is_oa` is never read) and `:518-523` (`"is_oa": (unpaywall or {}).get("is_oa")`), with Unpaywall skipped whenever no `--email`/env address is present (note at `:474-479`).

**Why it is a problem:** The same OpenAlex `open_access` object that supplies `oa_status` also supplies `is_oa`; the code reads only the former and takes `is_oa` from the email-gated Unpaywall leg, so a default invocation emits the OA status while asserting nothing about OA-ness. Verified live: OpenAlex returns `open_access: {"is_oa": true, "oa_status": "bronze", …}` for `10.1038/nature12373`; `python3 fetch_paper.py 10.1038/nature12373` yields `open_access: {"best_pdf": …, "oa_status": "bronze", "is_oa": null}` plus a "Unpaywall: skipped" note, whereas `--email test@example.org` yields `is_oa: true`. So one record carries OpenAlex's `oa_status` and a `null` OA flag derived from a different, absent source — the same "two fields of one record disagree" class the report rates SHOULD FIX for Issue 92, but for the `is_oa` field, which no recorded issue mentions. Trigger: the helper's own documented default invocation (`SKILL.md:286-289`).

**Suggested fix:**
a) Read the field in `_openalex_record` (`record["is_oa"] = open_access.get("is_oa")`) and merge it — `"is_oa": _pick((oa_confident or {}).get("is_oa"), (openalex or {}).get("is_oa"), (unpaywall or {}).get("is_oa"))`.
b) Or, if `is_oa` is deliberately Unpaywall-only, document that and stop emitting an OpenAlex-sourced `oa_status` alongside it, keeping the record self-consistent.
c) Add a regression test: a DOI resolving via OpenAlex with `open_access.is_oa=true` and no email → `open_access.is_oa is True`.

---

_End of report._
