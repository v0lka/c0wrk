# Code Review — branch `ui/viewer-position` vs `main` (commit 25fda526)

**Scope:** 10 files, +609/−8 — per-file reading-position memory for the file viewer
(`fileViewerStore.scrollPositions` + save/restore in `CodeMirrorFileViewer`/`MarkdownPreviewPane`
+ `restoreScrollTop` util + tests + spec/AGENTS.md updates). Review-only change set; nothing
outside this report was modified.

**Method:** multi-pass review per the code-review skill. Pass 1 covered bugs/logic and security,
pass 2 covered structure, performance, and behavior changes; both passes read the full diff plus
the complete changed files and their load-bearing context (`FileViewerContent`'s loader gate,
`chatStore`/`ChatScrollManager` as the claimed twin, `tabEngine` snapshot fields,
`markdownConfig` root structure, `useProjectSwitchState`, store `partialize`). Findings from all
passes are aggregated below, numbered sequentially. A final confirming pass re-derives the review
and must surface no new MUST FIX / SHOULD FIX beyond this report (its outcome is recorded at the
end).

**Context facts the findings rely on (all verified during the passes):**

- `FileViewerContent` renders a spinner while `fileData.loading` is true, so the viewer components
  mount only after content has arrived — a restore can never run against an empty document.
- `tabEngine` snapshots only `{pinned, collapsed, width}` from `fileViewerStore`; `scrollPositions`
  cannot leak into workspace-tab snapshots or localStorage (the store's `partialize` omits it, and
  a test enforces that).
- `Markdown` renders a single root `<div>`, so `MarkdownPreviewPane`'s
  `wrapper.firstElementChild` scroll-listener binding is sound.
- The chat twin (`ChatScrollManager` + `chatStore.scrollPositions`) uses the same
  save-on-unmount/restore-on-mount contract; the viewer adds tab re-target saves and proportional
  scaling on top.

---

## Findings

### 1. SHOULD FIX — Closing the active tab resurrects its reading position, breaking the documented invariant

The save-on-unmount / save-on-re-target paths re-create `scrollPositions` entries that the close
actions have just removed. Trigger: viewer content is mounted, the active tab A is scrolled (so
the listener-fed `scrollStateRef` holds a live position), and the user closes A — the common
paths: the tab's ✕ button or middle-click, "Close Others" with A active, or closing the last /
all tabs.

- `closeFile` removes `scrollPositions[A]` and switches `activeFile` to the next tab
  ([fileViewerStore.ts:244-246](frontend/src/stores/fileViewerStore.ts#L244-L246),
  [fileViewerStore.ts:237-241](frontend/src/stores/fileViewerStore.ts#L237-L241)).
- The still-mounted `CodeMirrorEditor` then runs its `[activeFile]` re-target effect, whose
  outgoing-save writes A's position straight back
  ([CodeMirrorFileViewer.tsx:222-226](frontend/src/components/fileViewer/CodeMirrorFileViewer.tsx#L222-L226)).
- For the close-last-tab / close-all case the viewer content unmounts instead, and the unmount
  cleanup performs the same re-save
  ([CodeMirrorFileViewer.tsx:172-176](frontend/src/components/fileViewer/CodeMirrorFileViewer.tsx#L172-L176)).
- `MarkdownPreviewPane` has the identical pair of save sites
  ([CodeMirrorFileViewer.tsx:384-386](frontend/src/components/fileViewer/CodeMirrorFileViewer.tsx#L384-L386),
  [CodeMirrorFileViewer.tsx:404-409](frontend/src/components/fileViewer/CodeMirrorFileViewer.tsx#L404-L409)).

Because the component effects run after the store update has committed, the resurrected entry
always wins.

**Why it is a problem:** the behavior contradicts the contract this same diff documents in four
places — `closeFile`'s own comment "A closed tab's reading position dies with it — re-opening the
file is a fresh read starting at the top" and the `closeOthersFiles` invariant comment
"scrollPositions ⊆ open tabs" ([fileViewerStore.ts:264](frontend/src/stores/fileViewerStore.ts#L264)),
the spec row "an entry dies with `closeFile`/`closeOthersFiles`/`closeAllFiles`"
([specs/domains/frontend/stores.md:62](specs/domains/frontend/stores.md#L62)), and the new store
test header "entries die with their tab"
([fileViewerStore.test.ts:93](frontend/src/stores/fileViewerStore.test.ts#L93)). The store-level
tests pass because they exercise the actions directly, but the mounted component undoes them.
Observable consequences: re-opening a just-closed file resumes from the old position (inconsistent
with closing a non-active tab, which does drop the entry), and entries for closed tabs accumulate
in the map until the next `restoreProjectFiles` prune.

**Suggested fix:**

a) Enforce the invariant centrally — make `saveScrollPosition` ignore paths that are not in
   `openTabs` (one guard covers the unmount save, the re-target save, the preview cleanup, and any
   future caller; document the rule on the action).
b) Guard at the save sites — before saving, check
   `useFileViewerStore.getState().openTabs.includes(outgoing)` in both component save paths
   (and the preview cleanup).

### 2. SHOULD FIX — `MarkdownPreviewPane` docstring promises two independent source/preview positions, but both panes share one store slot

The new docstring states the source/preview toggle "swaps between two independently remembered
positions for the same file"
([CodeMirrorFileViewer.tsx:361-367](frontend/src/components/fileViewer/CodeMirrorFileViewer.tsx#L361-L367)).
In the implementation both panes key the store by the same path: the source branch reads and saves
`scrollPositions[activeFile]`
([CodeMirrorFileViewer.tsx:231](frontend/src/components/fileViewer/CodeMirrorFileViewer.tsx#L231)),
the preview reads and saves `scrollPositions[path]`
([CodeMirrorFileViewer.tsx:392](frontend/src/components/fileViewer/CodeMirrorFileViewer.tsx#L392),
[CodeMirrorFileViewer.tsx:386](frontend/src/components/fileViewer/CodeMirrorFileViewer.tsx#L386))
— the same string. There is exactly one entry per file; each pane's unmount overwrites whatever
the other saved last.

**Why it is a problem:** the runtime behavior is coherent (the position is carried across the
toggle proportionally), but the documented contract is false, and a maintainer or a future test
written from that comment will mispredict the behavior. Additionally, toggling back and forth
applies proportional scaling twice, so the offset drifts a little on every toggle — fine in
practice, but not what "independently remembered" describes.

**Suggested fix:**

a) Correct the docstring to describe the shared-slot reality (one position per file; the
   source/preview toggle carries it across via proportional scaling).
b) If two independent positions are actually wanted, namespace the preview key
   (e.g. `` `${path}::preview` ``) — then `closeFile`/`closeOthersFiles`/`restoreProjectFiles`
   pruning must cover both keys for markdown files.

### 3. CONSIDER — `clearScrollPosition` has no production caller

[fileViewerStore.ts:373-378](frontend/src/stores/fileViewerStore.ts#L373-L378) adds the action,
but nothing outside its own test calls it — the chat twin's `clearScrollPosition` is wired up in
`useSessionActions` on session deletion, while the viewer has no equivalent path. Dead API invites
confusion about who is responsible for clearing entries.

**Suggested fix:**

a) Drop the action until a consumer exists.
b) Or wire it where the chat clears positions (session/task deletion paths) if viewer symmetry is
   intended.

### 4. CONSIDER — the highlightLine-ordering test cannot falsify "open-at-line still wins"

The test "applies the restore before the highlightLine scroll (open-at-line still wins)"
([CodeMirrorFileViewer.test.tsx:216-224](frontend/src/components/fileViewer/CodeMirrorFileViewer.test.tsx#L216-L224))
asserts the final `scrollTop === 500` — the restored value. In jsdom, CodeMirror's
`scrollIntoView` is a geometry no-op, so the assertion passes whether or not the later-defined
highlight effect re-scrolls; in a real browser that effect would move the scroll away from 500,
and the assertion would contradict its own title. The ordering guarantee currently rests solely on
React's effect-definition order
([CodeMirrorFileViewer.tsx:156-158](frontend/src/components/fileViewer/CodeMirrorFileViewer.tsx#L156-L158)),
which the test does not exercise.

**Suggested fix:**

a) Assert the ordering directly (e.g. spy on the view dispatch / `scrollIntoView` input and assert
   the restore happened first), or
b) Rename the test to what it actually pins ("restores the saved position on mount even when a
   highlightLine is set") so the title matches the assertion's strength.

### 5. CONSIDER — the save/restore reading-position contract is implemented twice

`CodeMirrorEditor` (listener-fed ref, mount restore, re-target save/restore, unmount save) and
`MarkdownPreviewPane` (the same four pieces with the element acquired via
`wrapper.firstElementChild`) duplicate the contract
([CodeMirrorFileViewer.tsx:119-236](frontend/src/components/fileViewer/CodeMirrorFileViewer.tsx#L119-L236) vs
[CodeMirrorFileViewer.tsx:369-437](frontend/src/components/fileViewer/CodeMirrorFileViewer.tsx#L369-L437)).
The comments cross-reference each other, but the two implementations can drift — finding 1, for
example, needs the same fix applied in both places. Acceptable as-is given the different
element-acquisition strategies; noted for maintainability.

**Suggested fix:**

a) Extract a small shared hook (e.g. `useReadingPosition(path, getScrollElement)`) owning the ref,
   the listener, and the save/restore effects; both components keep only their element lookup.

---

## Verified non-issues (investigated, no finding)

- **Security:** no new IPC, persistence, or parsing surface. Positions are in-memory, session
  scope, excluded from `partialize` (test-enforced), and never leave the renderer. The Markdown
  rendering pipeline is unchanged (same sanitized component, only wrapped). No OWASP-relevant
  change.
- **Performance:** scroll listeners are passive and write to a ref, not the store; store writes
  happen only on unmount/tab re-target; restore is O(1); `restoreProjectFiles` pruning is
  O(tabs²) with a handful of tabs. No hot-path impact.
- **Restore against empty content:** impossible — the `FileViewerContent` loading gate unmounts
  the viewer while content is loading, so restores always see real geometry.
- **Tab-snapshot leakage:** `tabEngine` copies only `{pinned, collapsed, width}`; the `tabs.md`
  claim that `scrollPositions` never enters a snapshot is correct.
- **Reference stability (React #185):** `saveScrollPosition`/`clearScrollPosition`/
  `restoreProjectFiles` keep the map reference on no-op writes; nothing selects `scrollPositions`
  reactively in any component, so there is no render-loop exposure at all.
- **Proportional scaling math:** `restoreScrollTop`
  ([fileViewerUtils.ts:66-77](frontend/src/lib/fileViewerUtils.ts#L66-L77)) is correct, handles
  degenerate geometries, and matches its unit tests, including the deterministic rounding case.
- **Layout:** the new wrapper `<div className="flex flex-1 min-h-0">` around `Markdown` is
  layout-equivalent to the previous direct-child arrangement (single-root child keeps
  `flex-1 overflow-auto`); `min-h-0` is a strict flex-overflow improvement.
- **Spec/AGENTS.md updates:** consistent with the implementation (subject to finding 1 — the
  `stores.md` row describes the intended invariant the component currently breaks).

---

## Pass log

- **Pass 1 — bugs/logic/security:** full diff + full reads of all 10 changed files. Produced
  findings 1 and 2; one bug candidate (restore running against a not-yet-loaded document) was
  investigated and dropped after the `FileViewerContent` loader gate proved it unreachable.
- **Pass 2 — structure/performance/behavior changes:** same context plus the cross-references
  listed above. Produced findings 3–5 and the verified-non-issues list.
- **Confirming pass — fresh re-derivation, outcome: SATURATED (zero new MUST FIX / SHOULD FIX).**
  Re-read `git diff main` in full and re-read all 10 changed files completely and independently of
  the earlier passes, then re-derived every skill category from scratch: bugs/logic (all four
  component save sites re-confirmed at the cited lines; restore-vs-highlight ordering, the null
  `path` preview guards, mount-time double-restore idempotence, `restoreProjectFiles`' pruned
  no-op reference handling); security (`partialize` re-verified to omit `scrollPositions`
  ([fileViewerStore.ts:462-475](frontend/src/stores/fileViewerStore.ts#L462-L475)); no new IPC,
  persistence, parsing, or Markdown-rendering surface); structure and comment conventions
  (load-bearing why-comments, cross-references, zoom-safe wrapper classes); performance (passive
  ref-writing listeners, O(1) restore, bounded prune); behavior changes (the finding-1
  resurrection and the finding-2 docstring mismatch remain the only ones, both already recorded).
  Every prior finding was re-confirmed against source at its cited location; no new MUST FIX or
  SHOULD FIX issue surfaced, so the five findings above are the complete set. CONSIDER-level notes
  (3–5) do not gate this review.
