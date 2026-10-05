# Button Tooltips

## Role

The convention that guarantees every button in the app exposes its purpose to a user who cannot read its visible label — via a native `title=` attribute, a Radix `<TooltipTrigger>` wrapper, or its own always-visible label — and the source-scan guards that enforce it, including the exclusivity rule: the two hover-tooltip channels never combine on one button. Buttons whose markup may hide the label (responsive collapse into an icon-only state) always carry a `title`; buttons whose label always shows need none; picker triggers follow the heading rule below.

## Key Files

- `frontend/src/components/ui/tooltip.tsx` — the Radix tooltip primitive: `TooltipProvider` / `Tooltip` / `TooltipTrigger` / `TooltipContent` and `TOOLTIP_DELAY_MS`, the single open-delay constant (1000 ms) shared by the whole UI
- `frontend/src/App.tsx` — mounts one `TooltipProvider` at the app root, so every tooltip in the tree shares the app-wide delay; individual surfaces never re-provide it
- `frontend/src/test/buttonTitleInvariant.test.ts` — the guard: an AST-based scan of the whole non-test source tree that fails when a `<button>` / `<Button>` has neither a `title` attribute, a `<TooltipTrigger>` ancestor, nor a statically visible label — or when it illegally combines a `title` with a `<TooltipTrigger>` ancestor (the channels never combine)
- `frontend/src/components/layout/ItemAction.tsx` — a native-title-only row-action button (see [row-actions.md](row-actions.md)): the `title` alone satisfies the guard; its disabled reason rides the wrapper span's `title`
- `frontend/src/components/ui/segmented-control.tsx` — the pinned label-hiding surface: segments may hide their string label responsively (`labelClassName="hidden …:inline"`), so the control derives each item's `title` from its string label automatically (`item.title` overrides). Items also carry an `itemProps` passthrough spread onto the rendered button — the ARIA-tabs extension point (`id`/`aria-controls`, used by the Research panel); it never touches the title derivation or the control's own props, which are applied after the spread.

## Behavior

### The invariant

Every native `<button>` and shadcn `<Button>` element (including the last segment of a dotted name, `<Tooltip.Button>`) must satisfy **exactly one** of:

1. an explicit `title` attribute — any value counts, the attribute's mere presence is the contract (`title={updatePending ? '…' : '…'}` is fine); or
2. a `<TooltipTrigger>` JSX ancestor — the Radix wrapper whose whole purpose is to attach the tooltip text. The check walks the JSX parent chain, so conditional children count too: `<TooltipTrigger>{cond && <Button>x</Button>}</TooltipTrigger>`; or
3. a **statically visible label** — JSX text inside the element, or an expression proven to render a non-empty string (`{saving ? 'Saving…' : 'Save'}`, nested ternaries included). The label IS the explanation, so a tooltip over always-labeled text can only echo it — an **echo `title` on a button whose label always shows is the anti-pattern this channel removes**.

The channels are **mutually exclusive**: a button under a `<TooltipTrigger>` carries **no `title`** — hovering would raise the Radix popup AND the native tooltip on top of each other. This combination is a guard violation in its own right: both `frontend/src/test/buttonTitleInvariant.test.ts` and `frontend/src/test/chatButtonTitles.test.ts` flag `title` under a trigger. For icon-only buttons under a trigger (the R1∩R4 corner) the Radix tooltip alone satisfies the may-hide obligation — it is present at the moment the label hides, and it works on disabled buttons where the native title does not render. A native `title` outside any trigger stays the way to hover-explain an enabled button; a button that must stay hover-explained while DISABLED uses the `ItemAction` wrapper-span pattern (see below), not the title-plus-trigger combination.

The three channels answer different visibility regimes:

- **The label may hide** (responsive collapse `hidden @min-[272px]:inline`, overflow truncation, a later redesign into icon-only) — the `title` is REQUIRED regardless of what the source shows, because at the moment it hides, the tooltip becomes the only name the button has. The guard cannot see CSS, so this obligation is enforced at review time: markup that hides its label and relies on the static-label channel is a review defect even though the guard accepts it.
- **The label always shows** — the `title` is NOT needed; write one only when it adds information the label lacks (the four-rule title policy below), never as an echo.

`title=""` technically passes the guard but is still an empty tooltip, so the text should stay meaningful.

### The context-menu exception (`role="menuitem"`)

Context-menu entries are the one deliberate gap in the invariant: a `<button role="menuitem">` carries **no `title`** — the action is named by the entry's visible text. While the menu is open the pointer already sits on that rendered text, so a hover tooltip could only repeat the label verbatim; the label *is* the explanation. The exemption covers the hand-rolled context menus (`FileViewerContextMenu`, `FileViewerTabContextMenu`, `GitFileContextMenu`, `GitHistoryContextMenu`, `FileTreeContextMenu`), which render their entries as native `<button role="menuitem">`.

The guard applies the exemption before the title check: `hasMenuitemRole` in `frontend/src/test/buttonTitleInvariant.test.ts` reads the element's **own** attribute list and matches only the exact string literal `role="menuitem"`. Keying off the ARIA role — not the file or the component — keeps the gap narrow: a plain button cannot smuggle itself out of the scan by living in a menu file. Everything else still flags:

- `role="menu"` — the container role is not an entry;
- `role={menuItemRole}` — a non-literal role is not provably exempt;
- `data-role="menuitem"` — a different attribute entirely.

A `title` on a menu entry remains accepted (harmless redundancy), but it is not required, and the convention is to omit it.

### The four-rule title policy (pickers, headings, tooltips)

**R1 — may-hide label ⇒ `title` required.** A button that is icon-only — or whose label may hide (responsive collapse into an icon-only state, truncation to the point of loss) — carries a `title`, echo or otherwise: at the moment the label disappears, the tooltip is the only name the control has. `SegmentedControl` derives each item's `title` from its string label automatically and is the pinned compliant surface. This obligation predates the policy and stays the review-time rule where the guard's static-label channel would otherwise accept hide-capable markup.

**R2 — picker with an external heading ⇒ `title` optional.** A picker that sits under a visible `<label>` naming it (`Search provider`, `API type`, the embedded-LLM tuning knobs, the judge-mode selects) needs no trigger `title` — the heading names the control and the trigger shows the selection. When a `title` is present anyway, it follows the content rule below. The generic `ui/Combobox` renders `title={displayLabel}` — the selected option's label (placeholder, then raw value, when nothing matches). The trigger truncates (`truncate` class; consumers run `w-44` to `min-w-[180px]`), so this is the R1 truncation-restore echo: legal because the hover reveals what the truncation hid, and harmless under R2 because the title is optional where a heading already names the control. Every current generic-Combobox call site is headed; an unheaded picker must never be built on this primitive — there the value echo is the R3 anti-pattern.

**R3 — picker without an external heading ⇒ non-echo `title` required.** A picker whose trigger shows only the value and that has no visible heading — the sidebar project/session switchers (`Switch project`, `Switch session`), the chat-toolbar model picker (`Provider: Model` over the bare model label) — MUST carry a `title` that names the action or enriches the value: the tooltip replaces the missing heading as the control's name. A value echo (`title={activeProject?.name}`) is the anti-pattern here — it adds nothing over the on-screen text and names no action.

**R4 — Radix tooltip ⇒ no native `title`.** An element wrapped in a `<Tooltip>`/`<TooltipTrigger>` carries no `title` attribute — hovering would raise the Radix popup AND the native tooltip on top of each other. Both guards enforce this as a violation in its own right. R4 beats R1 for an icon-only button under a trigger: the Radix tooltip already provides the hover explanation, including on disabled buttons where the native title does not render.

The content rule for any `title` that IS written (beyond the guards' reach — they verify presence, not wording): it adds information the on-screen label does not carry — an action name or an enrichment — and never echoes the visible label verbatim. The one carve-out is the R1 may-hide case, where the echo IS the point: `ui/Combobox`'s `title={displayLabel}` restores the full selection at hover over a truncate-clipped trigger (the pinned example). State-dependent title branches on a picker trigger stay allowed when each branch either names an action, adds information, or gives a real hover hint (`isLoading ? 'Loading models…' : disabled ? 'Locked while the session is running' : effectiveEntry ? \`${providerLabel}: ${model}\` : displayLabel` in `ModelPickerMenu`) — the rule bans echoing the *value*, not conditionality. The pinned example is the `ModelPickerMenu` test asserting the trigger title is exactly `Embedded: Bonsai 2 27B` for the embedded selection whose visible label is the bare `Bonsai 2 27B` — the enrich case, not an echo.

The picker-trigger `title` names the action or restores a truncate-hidden value, never decorates — but R2 makes clear the action name is only obligatory where no heading carries it (R3).

### Disabled buttons and hover explanations

The native `title` tooltip is rendered by the OS/webview on any element — but **not on a disabled button** in most engines. The Radix tooltip does not have that limitation. The channels never combine (the invariant above), so a button that can render disabled while hover-explaining itself picks ONE shape: the `ItemAction` pattern — the `disabledReason` mirrored onto the focusable wrapper span's `title`, so hovering the (inert) button area still shows the reason with no Radix involved (see [row-actions.md](row-actions.md)) — or a Radix tooltip alone, which keeps working through the disabled state. A native `title` alone suffices when the button never renders disabled.

### The guard test

`buttonTitleInvariant.test.ts` scans every non-test `.ts`/`.tsx` file under `frontend/src/` (same recursive walk pattern as `zoomViewportInvariant.test.ts`, which requires >50 files as a vacuity guard) and parses each with the TypeScript compiler API (`ts.createSourceFile`), so the scan:

- sees only real JSX start/self-closing tags — `<button` in comments, string literals or JSX text can never flag (immunity is by construction, not by comment-stripping heuristics);
- parses `.ts` files as TS (not TSX), so an unparenthesised generic arrow (`<T>(x: T)`) cannot invent a phantom JSX tag;
- walks the JSX parent chain for the `<TooltipTrigger>` ancestor check — and flags a `title` on a button that HAS such an ancestor (the channels never combine);
- accepts the static-label channel: a paired element's own JSX text and provable expressions (`isStaticString` — string literals, parenthesized literals, and nested ternaries with all-literal leaves). Only the element's OWN children count — a sibling element's text never labels a self-closing button;
- exempts a button whose own attribute list carries the exact string literal `role="menuitem"` — the context-menu exception documented above, applied before the title check.

The static-label proof is deliberately conservative: an identifier — even a lookup in a `const` table (`{NAME_LABELS[kind].action}`) — is not provable and keeps the title requirement. A label that needs the guard's acceptance must be written as literal text or an all-literal ternary in the JSX; when a static table is the cleaner source of truth for the data, render the string through a literal ternary at the use site.

Violations are reported as `file:line: snippet`, and the tree-wide assertion fails fast with the full offender list.

The `{...spread}` case is **trusted, fail-open**: a button carrying `{...rest}` passes, because props-forwarding wrappers (`<button {...props}>`) are exactly how a title set at the call site reaches the DOM. Equally fail-open by design: wrapper *components* are out of scope — only intrinsic `button` tags and components named `Button` / `*.Button` are matched (`IconButton`, `DropdownMenuTrigger`, … are beyond the invariant's reach, stopped where a component's own contract begins), a tooltip from a differently-named wrapper is not recognized, and a CSS-hidden label fools the static-label acceptance (the review-time rule above is the counterweight).

### Canonical shapes (how the guards read them)

```tsx
// 1 — icon-only: native title REQUIRED (the label cannot explain anything)
<Button title="Cancel and stay in c0wrk"><X /></Button>

// 2 — static visible label: NO title — the label IS the explanation
<Button variant="outline" onClick={close}>Cancel</Button>
<Button onClick={save} disabled={isSaving}>{isSaving ? 'Saving...' : 'Save'}</Button>

// 3 — dynamic text: title REQUIRED (the guard cannot prove the label)
<Button title={dynamicTitle}>ok</Button>

// 4 — Radix tooltip: the trigger carries NO title (rule 4; works on
//     disabled buttons too)
<Tooltip>
  <TooltipTrigger asChild>
    <button type="button" onClick={f}><Icon /></button>
  </TooltipTrigger>
  <TooltipContent side="left">Copy path</TooltipContent>
</Tooltip>

// 5 — VIOLATION (rule 4): a title under a TooltipTrigger — hovering raises
//     the Radix popup AND the native tooltip on top of each other; both
//     guards flag this
<Tooltip>
  <TooltipTrigger asChild>
    <button title={label} disabled={disabled} onClick={onClick}><Icon /></button>
  </TooltipTrigger>
  <TooltipContent side="left">{label}</TooltipContent>
</Tooltip>

// 6 — native title with the reason on a focusable wrapper (the ItemAction
//     pattern; see row-actions.md — no Radix needed)
<span tabIndex={disabled ? 0 : undefined} title={disabled ? reason : undefined}>
  <button title={disabled ? reason : label} disabled={disabled} onClick={onClick}><Icon /></button>
</span>

// 7 — conditional child inside the trigger still counts
<TooltipTrigger>{ok && <Button>x</Button>}</TooltipTrigger>

// 8 — context-menu entry: the role="menuitem" exemption (no title needed)
<button role="menuitem" onClick={handleClose} className={menuItemClass}>
  <X className="size-4" />
  Close
</button>

// 9 — MAY-HIDE label: title REQUIRED even though the label is in the source
//     (responsive collapse to icon-only — the SegmentedControl pattern; the
//     control derives this title from the string label automatically)
<SegmentedControl
  items={[{ value: 'files', icon: <FolderTree />, label: 'Files' }]}
  labelClassName="hidden @min-[272px]:inline"
/>

// 10 — may-hide label kept in a data table: render it through a literal
//      ternary so the guard proves the label without an echo title
<Button>{busy ? 'Saving…' : kind === 'duplicate' ? 'Duplicate' : 'Rename'}</Button>
```

## Error Handling

The guard is a test-time gate, not runtime code: it fails `npm test` (and therefore CI) with an actionable `file:line: snippet` list. A red run means one of two things. Either a newly landed button has none of the three channels — give an icon-only button a `title`, let an always-labeled button's text speak for itself (write it as JSX text or an all-literal ternary), or rely on one of the documented fail-open passes: the `{...spread}` pass-through (only for a genuine wrapper that forwards `title` from its caller) or the `role="menuitem"` context-menu exemption (only for a real menu entry). Or a button combines the tooltip channels — a `title` under a `<TooltipTrigger>` — in which case drop the `title` and let the Radix tooltip speak. There is no runtime fallback: a button that bypasses the test and can hide its label simply shows no tooltip in its icon-only state.

## Invariants

- Every `<button>` / `<Button>` in non-test frontend sources exposes its purpose via exactly one of the three channels — a `title` attribute, a `<TooltipTrigger>` ancestor, or a statically visible label — and the channels never combine: a `title` under a `<TooltipTrigger>` is a violation flagged by BOTH `frontend/src/test/buttonTitleInvariant.test.ts` (project-wide) and `frontend/src/test/chatButtonTitles.test.ts` (chat-scoped) on every test run. The chat-scoped guard additionally holds `CollapsibleTrigger` to the same rule and deliberately does NOT honor the `role="menuitem"` exemption: chat menus are built exclusively through Radix primitives (`DropdownMenuItem` renders `div[role="menuitem"]`, invisible to the `<button>` scan), so a native menuitem button in chat is a hand-rolled one-off that must still carry a `title` or a static visible label.
- A button whose markup may hide its label (responsive collapse into an icon-only state, truncation to the point of loss) carries a `title` regardless of the label's presence in the source — at the moment the label hides, the tooltip is the only name the button has. The guard cannot verify visibility, so this obligation is enforced at review time; `SegmentedControl` is the pinned compliant surface (it derives each item's `title` from its string label automatically).
- A button whose label always shows carries no echo `title` — its label is the explanation, and a tooltip repeating it verbatim adds nothing. The static-label channel accepts the button; writing `title="Cancel"` over the label `Cancel` is the anti-pattern this convention removed from `CreateProjectDialog`, `MCPServerForm`, `ModelConfigDialog`, `ModelProfileDialog`, `MCPSettings`, `MCPServerCard`, `UpdateSettings`, `UpdateToast`, `SessionActionConfirmDialog`, `UserConfirmDangerDialog`, `ModelProfilesSettings`, `SecuritySettings`, `FileTreePanel`, `EmbeddedLLM*`, `ui/dialog`, and `LLMSettings` (text Cancel). `ui/dialog`'s footer Close is the pinned echo example.
- Picker triggers follow the heading rule: with a visible external heading the `title` is optional (the heading names the control; the generic `ui/Combobox` renders `title={displayLabel}` — the R1 truncation-restore echo, legal only because every call site is headed); without one the trigger MUST carry a non-echo `title` naming the action or enriching the value (`Switch project`, `Provider: Model` over the bare model label) — the tooltip replaces the missing heading as the control's name. A `title` echoing the value verbatim (`title={activeProject?.name}`) is the anti-pattern. The guard cannot verify wording — this rule is enforced at review time, with `ModelPickerMenu`'s "the title is exactly `Embedded: Bonsai 2 27B` over the bare `Bonsai 2 27B` label" test as the pinned enrich example.
- The static-label proof is deliberately conservative: only JSX text and all-literal (nested) ternaries are provable; identifiers and expressions keep the title requirement — a label meant for the static channel is written as literal text or a literal ternary at the use site. The check reads a paired element's OWN children only: a sibling element's text never labels a self-closing button.
- The `role="menuitem"` exemption keys off the exact string literal in the element's own attribute list: `role="menu"`, a non-literal `role={…}` and `data-role="menuitem"` still flag, and a `title` on a menu entry stays accepted — all pinned by the guard's self-tests.
- The app mounts exactly one `TooltipProvider`, at the root in `frontend/src/App.tsx`; tooltip open delay is the single constant `TOOLTIP_DELAY_MS` (1000 ms) exported from `frontend/src/components/ui/tooltip.tsx`.
- The guard's comment/string immunity is structural (AST-based), so prose mentioning `<button` never produces false positives.
- `TooltipContent` is always portaled and always carries an `Arrow`; styling stays in the primitive (`text-xs`, token colors) — call sites pass placement (`side`) and content, not chrome.
- A button that can render disabled while hover-explaining itself picks ONE tooltip shape — the `ItemAction` wrapper-span pattern (the reason on a focusable span's `title`, see [row-actions.md](row-actions.md)) or a Radix tooltip alone, which keeps working through the disabled state where the native title does not render — never the title-plus-trigger combination.

## Related Specs

- [README.md](README.md) — frontend architecture overview (this convention's Invariants section)
- [ui-scale.md](ui-scale.md) — the sibling source-scan guard pattern (`zoomViewportInvariant.test.ts`, regex + comment-stripping variant) and zoom-safe geometry rules
