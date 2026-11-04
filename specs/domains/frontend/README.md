# Frontend

## Purpose

React 19 application providing the user interface for c0wrk: chat interaction, plan visualization, file viewing, and workspace management. Communicates with the Go backend through the `src/api/runtime.ts` transport seam: Wails IPC in the desktop webview; in a browser tab served by the web-access sidecar, `POST /api/rpc` + a WebSocket carrying the same event stream ([ADR-101](../../decisions/101-browser-web-access.md)) — identical behavior in both transports (see [loading-states.md](loading-states.md)).

## Key Files

- `frontend/src/App.tsx` — root component
- `frontend/src/stores/` — Zustand state management (37 stores)
- `frontend/src/hooks/` — custom React hooks (event handlers, data loading)
- `frontend/src/api/` — backend RPC wrapper layer
- `frontend/src/lib/` — utilities (fuzzyMatch, parseReferences, markdown config + local image resolution, local file link detection, CodeMirror extensions, the UI-scale geometry helpers `layoutSpace` + `cursorMenuPosition`, and the `@floating-ui/dom` zoom compensation `floatingUiZoom`)
- `frontend/src/components/` — UI component tree
- `frontend/src/types/` — TypeScript type definitions
- `frontend/src/index.css` — design tokens (Tailwind v4 @theme), the `height: 100%` base-sizing chain, and the zoom-corrected `--ui-vh` primitive

## Core Types

```typescript
// SessionInfo — session metadata from backend
interface SessionInfo {
  id: string
  project_id: string
  name: string
  created_at: string
  last_active_at: string
  archived: boolean
  pinned: boolean
  active: boolean
  total_input_tokens: number
  total_output_tokens: number
  model: string
  family: string
  has_unfinished_task: boolean
}

// ProjectInfo — project metadata from backend
interface ProjectInfo {
  id: string
  name: string
  workspace_path: string
  is_external: boolean
  is_no_project: boolean
  research_pins: { research: string[] | null; hypotheses: Record<string, string[]> | null }
  created_at: string
  last_active_at: string
}

// ProjectSwitchState — persisted per-project UI switch state
interface ProjectSwitchState {
  project_id: string
  saved_session_id: string
  open_tabs: string[]
  active_file: string
  updated_at: string
}

// FileEntry — file tree entry from backend
interface FileEntry {
  name: string
  path: string
  is_dir: boolean
  icon?: string
  icon_color?: string
  hidden?: boolean
  gitignored?: boolean
}
```

Note: Wails auto-generates TypeScript types from Go structs using snake_case JSON field names. Git status is stored separately in `fileTreeStore.gitStatus` (a `Record<string, GitStatusEntry>` keyed by absolute path), not as a field on each file entry.

## Stack

- React 19 + TypeScript ~5.7
- Vite 6 (build tool)
- Tailwind CSS v4 (utility-first styling)
- Zustand 5 (state management)
- shadcn/ui + Radix UI (component primitives)
- lucide-react (icons)
- CodeMirror 6 (file viewer + chat input editor, with Markdown mode and custom extensions)
- react-markdown 10 + remark/rehype plugins (markdown rendering)
- highlight.js 11 (syntax highlighting in rendered messages)
- Mermaid 11 (lazy-loaded diagrams)

## Layout

Three-column panel layout (no router, single-page app). The file viewer can be pinned as the classic in-flow third column or unpinned as an overlay above the chat; unpinned floating mode is the default.

```
┌──────────────────────────────────────────────────────────────────┐
│  Sidebar (persisted) │  Main Chat Area  │  File Viewer           │
│  collapsible→40px    │                  │  pinned: docked column │
│                      │                  │  unpinned: chat overlay│
│                      │                  │  (collapses→40px rail) │
│  ┌─────────────┐  │  ┌────────────┐  │  ┌─────────────────────┐ │
│  │ Project sel │  │  │ Pinned msg │  │  │ Tab bar             │ │
│  │ Session sel │  │  │ Message    │  │  │ File content        │ │
│  │ File tree   │  │  │ list       │  │  │ (syntax highlight   │ │
│  │             │  │  │            │  │  │  or diff view)      │ │
│  │             │  │  │ Activity   │  │  │                     │ │
│  │             │  │  │ indicator  │  │  │                     │ │
│  │             │  │  ├────────────┤  │  │                     │ │
│  │             │  │  │ Chat input │  │  │                     │ │
│  └─────────────┘  │  └────────────┘  │  └─────────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
```

Resize handles (4px) sit between in-flow panels and on the floating viewer's left edge. Sidebar width/collapse, viewer width/collapse, and viewer pin preference persist through Zustand `persist`/localStorage. The unpinned expanded viewer is a right-aligned absolute overlay over the chat, auto-collapses when the pointer lands outside it or when keyboard focus leaves it (an outside `focusin` collapses only after the viewer itself held focus — Radix portal menus restore focus to their trigger a tick after an item's `onSelect` opened a file, and that focus-restore must not dismiss the freshly opened viewer), and leaves a 40px in-flow rail for reopening; pinning keeps it as a permanently docked, resizable column. Collapsing unmounts the viewer content (the slim rail replaces it), so the reading position does not live in the DOM: `CodeMirrorFileViewer`/`MarkdownPreviewPane` save a per-file session-scope reading position into `fileViewerStore.scrollPositions` on unmount/tab re-target and restore it on the next mount/activation (`restoreScrollTop` scales the offset when the file changed in between; `highlightLine`'s open-at-line scroll runs after and wins), mirroring the chat's `ChatScrollManager`/`chatStore.scrollPositions` reading positions — the same file re-opened from the rail continues where reading stopped instead of jumping to the top. A middle-click (`auxclick`, `button === 1`) on a viewer tab closes that tab without activating it — browser-style semantics via the same `closeFile` path as the per-tab close button; the event is `preventDefault`-ed so the webview's autoscroll/selection does not kick in.

Above the main row sits the optional **workspace tab bar** (`components/layout/TabBar.tsx`): the outermost context layer of the shell, rendered only while `uiStore.tabsEnabled` is on (Settings → Appearance, default off; the bar carries only tab-scoped affordances — «+» and close — while the sidebar header's Settings gear is unconditional in both flag states, [ADR-093](../../decisions/093-sidebar-gear-unconditional.md)). The layer itself always lives — with the flag off it runs its "exactly one tab" special case as an invisible live mirror of the workspace, and the shell is byte-identical to the pre-tab UI. Every tab activation travels the single engine path (`lib/tabEngine`, snapshot/restore over three validated UI slices); the model, invariants, and edge handling live in [tabs.md](tabs.md).

The native desktop window separately persists validated width, height, and maximized state in `~/.c0wrk/window_state.json`. Frontend resize events debounce `PersistWindowBounds`; desktop shutdown performs a final best-effort save, and the next process launch uses valid stored dimensions (falling back to defaults for missing, malformed, or below-minimum values).

### Sidebar header

Between the collapse and settings buttons sits a CHAT/CODE segmented toggle:
- **CHAT**: switches to No Project (pseudo-project with semantic_search disabled, per-session workspaces)
- **CODE**: switches to `lastRealProjectId` (the most recent non-No-Project project) or the first available real project
- The toggle is hidden when no projects are loaded yet

Chat input uses a CodeMirror 6 editor in Markdown mode (`@codemirror/lang-markdown`), providing syntax highlighting for Markdown constructs (headings, bold, italic, code, links) and custom token decorations for `/skill` references (warning color) and `@file` references (info color) via a `StateField`. Autocomplete is powered by `@codemirror/autocomplete` with two custom `CompletionSource` functions: typing `/` at a word boundary triggers fuzzy-filtered skill suggestions, and `@` triggers workspace file/directory suggestions. Open file viewer tabs are boosted to the top of file completions. On send, skill refs are extracted as `activeSkills[]` and file refs are converted to `fileref://` URIs by the core preprocessor (see `core/message_preprocess.go`). An `@file` ref accepts an optional line anchor in GitHub-canonical forms — `#L20`, `#L20-36`, `#L20-L36`, or legacy bare-number `#20-36`; the anchor survives into the `fileref://` URI and positions the viewer on open. Paths containing spaces use the single-quoted form `@'my file.go'` — the canonical insert of the `@`-autocomplete and of the file viewer's "Add to chat" action (anchor allowed after the closing quote or inside it); the legacy backslash-escaped form `@my\ file.go` remains recognized. The `/` completion is session-kind-aware: it filters all three sections (Subagents, MCP Servers, Skills) through the active session's chat/code capability context — a capability hidden in that context is not offered, and a manual-in-context entry carries the "mention to enable" detail (see [capabilities.md](../capabilities.md)).

The `/`-ref grammar is one contract implemented twice: the frontend extraction (`REF_BOUNDARY_SPACE_SOURCE`/`SLASH_REF_SOURCE` in `frontend/src/lib/parseReferences.ts`, shared with display parsing) and the Go preprocessor scanner (`isRefBoundarySpace` and the mention scanner in `core/message_preprocess.go`) implement the same grammar — the Go ASCII boundary set `[ \t\n\r\f]` rather than JavaScript's wider `\s`; names of ASCII word characters/hyphens; a collision-qualified `agent:`/`skill:`/`mcp:` marker tolerating zero or one space/tab after its colon; and a complete name that ends at a boundary or EOF with no backtracking into a shorter name or a plain marker (the TS side asserts EOF with `(?![\s\S])` because `$` also matches before a final Unicode line separator, which Go does not treat as a boundary). Grammar tests pin both sides — `parseReferences.test.ts` asserts the Go-compatible boundary set, whole qualified names, and no-backtracking finds; `message_preprocess_test.go` asserts the same boundary and qualified cases on the strip path — and a grammar change to one side lands together with the other.

Undo/redo in the chat input is layout-independent: `Ctrl+Z` (undo), `Ctrl+Shift+Z` / `Ctrl+Y` (redo) — `Cmd` replacing `Ctrl` on macOS, where the fallback additionally provides `Cmd+Y` = redo (the named history redo chord there is `Cmd+Shift+Z`) — always drive the editor's history regardless of the active keyboard layout. CodeMirror resolves a keydown in three layers: key-name bindings for `historyKeymap`'s `Mod-z` / `Mod-y` derived from `event.key`, which follows the ACTIVE layout (with a non-Latin layout active, Cyrillic `Ctrl+Z` arrives as `key='я'` and no key-name binding fires); an else-if physical fallback mapping `event.keyCode` onto a QWERTY key name and re-resolving the chord (how mac `Cmd+Shift+Z` still redoes on a Cyrillic layout); and, for every keydown no binding consumed, each keymap's `any` fallback. `createChatKeymap` (`frontend/src/lib/cmChatKeymap.ts`, the only `any`-carrying keymap of the chat extension set built in `frontend/src/lib/cmChatExtensions.ts`) recognizes the history chords in that last layer: it matches the lowercased `event.key` character FIRST — the same layout-following signal the key-name layer resolves bindings from, so a chord recomputes into the same action (on QWERTZ the printed-`y`-on-physical-`KeyZ` chord `Ctrl+Y` is redo and never undo) — and falls back to the layout-independent physical key (`event.code`) for layouts that print neither Z nor Y at all (Cyrillic `я`/`н`). A recognized chord is consumed exactly once — `any` returns true even when the history command no-ops on an empty stack, mirroring `historyKeymap`'s own bindings whose `preventDefault` defaults to true — so the physical-keyCode layer never re-interprets the same keydown as the opposite chord. Only the history chords are intercepted: AltGr (`Ctrl+Alt`) and IME composition never match, and an unrecognized keydown returns false so the remaining bindings and native behavior proceed unchanged.
### Sidebar session selector

The CODE-mode session dropdown (`components/layout/SessionSelector.tsx`) has two display modes switched by the List⇄FolderTree toggle on the search-input line, positioned BEFORE the input (the FolderTree glyph is the git panel's tree-view icon; the toggle is a registered `DropdownMenuItem` whose `onSelect` preventDefaults, so switching never dismisses the menu, it renders even when the search input itself is hidden below the 5-session threshold, carries a hover affordance, and its `h-7 w-7` hit area with a `size-4` glyph matches the input's height; the icon shows the mode you switch to):

- **flat** (default) — the plain ordered session rows;
- **grouped** — `uiStore.sessionListGrouped` (persisted display-only flag, default off) renders Worktrees-section branch groups over the session grouping model `lib/sessionGroups.ts` (`buildSessionGroups`): each worktree branch renders a header row (chevron + GitBranch icon + branch name + the group's freshest member's relative time) whose selection (click or Enter) collapses/expands only that group's member sessions; groups themselves order **GROUP-pinned** branches first, then the group containing the **active session**, then by their freshest member — the options fed to `buildSessionGroups` are `pinnedBranches` (from the persisted `pinnedGroupsStore` — `c0wrk-pinned-worktree-groups`, keyed per project — the same per-project pin key orders the Archived section's branch groups) and `activeSessionId` (the session the user is currently viewing, read from `sessionStore`); member rows inside a group order **pinned → active → freshest** — the active session surfaces right below its group's pinned members, ahead of fresher siblings — and the Local section applies the same pinned → active → freshest order to its bucket, so an active local session leads Local. The active float is **live-sections-only** and pin-first: GROUP-pinned branches keep priority over the active-containing group, per-session pins keep priority over the active row, a null/undefined/unknown `activeSessionId` or a search that hides the active session degrades to the exact pre-active ordering, and nothing else moves — the Archived section (branch groups and local rows) keeps its plain pin → freshness rules, flat mode keeps pure store order, and the section order is untouched (only the visible grouped model carries the option; the unfiltered model behind group actions/prune stays order-unaware, so group actions keep keying on membership, not order); member rows inside a group carry extra left padding (`SessionItem`'s additive optional `indented` prop — `variant='dropdown'` only, the CHAT-mode flat list is untouched). The branch row also carries the group's **radar-style activity dot** (`BranchActivityDot`): ONE dot summarizing the members' live statuses through the radar's exact aggregation chain — `SessionGroupedList` derives the per-session flags ONCE per list render (`useLiveSessionFlags`: `deriveLiveSessionFlags` → `mergePendingOverride`, lib/activeSessions) and each row's dot narrows the shared map to its members via `aggregateBadgeFlags` — with the radar's precedence (failed → pending → active → paused), painted with the shared `SESSION_DOT_CLASSES` color and `SESSION_DOT_TITLES` wording — so a COLLAPSED group containing a live session stays visible; idle groups render no dot, and archived groups need none (archived sessions always derive idle).

Every one of the dropdown's controls — the mode toggle, the section headers, and every control of a worktree branch row (the collapse toggle plus the four actions of its hover overlay) — is a **registered Radix menu item** (never a plain button): Radix prevents Tab inside menu content and arrow-key navigation only visits the roving-focus collection, so an unregistered control would be unreachable for keyboard-only users (a section collapsed with the mouse could never be reopened from the keyboard). Arrow keys traverse them in DOM order, Enter/Space activate them through `onSelect`, and clicks run through the same `onSelect` — toggle logic lives in exactly one place.

The popup is a fixed wide `w-[28rem]`. Section headers ("Worktrees" / "Local", plus "Archived") are themselves collapsible menu items in the branch combo's section styling (`text-xs font-semibold uppercase tracking-wide` + leading chevron + hover), and grouped mode separates the Worktrees and Local sections with the same hairline divider (`DropdownMenuSeparator`) the branch combo draws between its Local/Remote/Worktrees sections — the divider belongs to the headers: it renders whenever both do (even with a section collapsed) and disappears with them in the lone-section flat view. The Worktrees/Local headers render only when BOTH sections are non-empty — a lone Local section IS the flat view, and a lone Worktrees section speaks through its branch rows alone — and a section's content renders unconditionally whenever its header is hidden. Collapse state is one ephemeral component-level set holding the stable `sessionGroups` keys (the Worktrees section's key VALUE stays `managed` after the section's rename — persisted collapse state survives — plus `local` / `archived`, archived starts collapsed), the live groups' `branch:<name>` keys, and the archived branch groups' namespaced `archived:<branch>` keys — a branch may hold both live and archived sessions, and the two toggle independently. An active search force-expands every section AND group (matches inside collapsed ones stay reachable) and groups left without members are hidden. The **Archived** section renders the grouping model's archived buckets in BOTH display modes: archived managed-worktree sessions as collapsible branch rows mirroring the live ones (chevron + GitBranch + branch name + freshest-member time, selection toggles the group, members indented) but with their own THREE-action hover overlay — **Pin/Unpin** ("Pin group (branch)" — the same GROUP-level pin as the live rows, toggled in `pinnedGroupsStore` under the ACTIVE project's id by the branch name; preventDefaults so the menu stays open), **Restore** ("Restore sessions (branch)" — unarchives EVERY member through `handleUnarchiveGroup`, non-destructive so it never asks) and **Delete** ("Delete sessions (branch)" — `handleDeleteGroup`, always arms the branch-named (count-aware) `SessionActionConfirmDialog`; the backend completes/cancels any residual task per member) — then archived local sessions as flat rows. Its header count comes from the same search-filtered buckets (`archivedWorktrees` members + `archivedLocal` length), so count and rows can never disagree, and the section hides whole when that count reaches 0. Branch-header, section-header, and toggle selection never closes the dropdown (their `onSelect` preventDefaults — `CollapsibleSectionHeader` is shared with the branch combo's Local/Remote/Worktrees headers); selecting a session row still does.

Each worktree branch row carries a hover **action overlay** (`ItemActions`, see [row-actions.md](row-actions.md)) holding FOUR nested registered menu items — the row's actions, revealed on row hover/focus: **"+"** ("New session in this worktree (branch)"), **Pin/Unpin** ("Pin group (branch)" / "Unpin group (branch)" — the GROUP-level pin: toggling the branch name in the persisted `pinnedGroupsStore` floats the whole group above the unpinned ones in its section; member sessions' own pins are untouched), **Archive** ("Archive sessions (branch)") and **Delete** ("Delete sessions (branch)"). The "+" arms the ADR-091 per-branch gesture via `useSessionActions.handleNewSessionOnBranch`: it eagerly creates a plain LOCAL session and presets its fresh-window workspace choice with `createBranch=false` — the branch exists, so the session's FIRST send **adopts** the branch's existing managed worktree (ADR-082 shared trees) and the new session executes in the same tree; provisioning still happens only at that send (SendMessage's `workspace` argument). The gesture selects the new session (click or Enter equivalent), resets the search and closes the dropdown so the chat area's armed workspace picker becomes visible — the one overlay item whose `onSelect` does NOT preventDefault, so Radix's default select-close fires. Pin toggles `pinnedGroupsStore.togglePinnedBranch` (keyed by the active project via `useGroupPins`) and preventDefaults (a state flip keeps the menu open); Archive/Delete go through `handleArchiveGroup`/`handleDeleteGroup` and target EVERY member of the group — the branch's UNFILTERED membership (`fullBranchIds`, looked up from the no-search grouping model), so an active search cannot shrink what a group action destroys and the branch-group confirmation counts the true total (archived rows read their membership from the same unfiltered model) — Archive asks first whenever the group holds more than one member (one click would archive N sessions, busy or not) or the lone member is busy, Delete always asks, both through the branch-named `SessionActionConfirmDialog` copy — count-aware, so a single-member group reads "groups 1 session" rather than a branch name posing as a session name. The overlay stops click and Enter/Space key bubbles, so activating an action never toggles the row's collapse state; arrow keys still traverse toggle → actions (Radix registers the nested items in the same roving-focus collection). The overlay works on a collapsed row too. Live Worktrees branch rows and Archived branch rows are the only rows carrying an overlay — Local rows and flat mode have none (their sessions act per row).

### Settings → Capabilities

The former MCP settings tab is the **Capabilities** tab: three subsections in the completion-list order — **Subagents → MCP Servers → Skills** — each rendering every configured capability with **two chat/code mode selectors** (`auto` | `manual` | `disabled` per context, [ADR-084](../../decisions/084-per-capability-chat-code-activation-modes.md)):

- **MCP Servers** keeps add/edit/delete/reload on `updateMCPServers` (the wire format carries no mode key); mode edits go through `updateCapabilityModes('mcp', …)` with the **full replacement section**. A card renders its live gateway status except when **both** slots are disabled — the only never-dialed combination renders neutrally (a "disabled" badge, no red state); a partially-disabled server keeps its live status.
- **Subagents** and **Skills** are view-only (existence is owned by `.agents/` profiles and the skills directories): name, description, and the two selectors — no add/edit/delete. The read-only card carries no per-card icon (the Bot/Sparkles catalog icons live on the section headers only; a both-disabled entry communicates through its "disabled" badge, the neutral `CircleOff` glyph is gone with the icon slot) and no per-context explanation captions (`Chat sessions:…` / `Code sessions:…` stay MCP-only); the two selectors stack vertically (CHAT above CODE, uppercase captions) so the description keeps the card's full width.
- Each of the two catalog sections carries an MCP-style reload button in its header (`Reload subagents` / `Reload skills`) that forces a **re-discovery** through the `RefreshAgents` / `RefreshSkills` RPCs (`backend/frontend_api_agents.go` / `frontend_api_skills.go`): the RPC invalidates the backend's catalog cache and re-scans the skill/agent directories — plain `ListAgents`/`ListSkills` are cache-served, and directories created after startup are never watched, so this is the only manual catch-up path. A refresh swaps only that section's rendered list (the capabilities modes map is untouched, so a stale mode entry for a dropped name survives the next mode write — server-side unknown-name trimming is MCP-only; a stale skills/agents entry is inert and its stored preference revives if the name returns), disables only its own button while in flight, and surfaces a failure in the section's error banner without touching the catalogs.
- Mode state loads through `getCapabilityModes()`; a failed read leaves the selectors disabled rather than letting the UI write a half-known map (failing lists render, only mode editing is blocked).
- The `/`-completion and send-path filters read the same modes keyed on the active session's context (`lib/capabilityModes.ts` + `lib/sessionKind.ts`): a capability is mentionable unless `disabled` in that context; an unknown session falls open to the active project's context.

### Settings → General → Prompt Suggestions

The **Prompt Suggestions** section sits directly after **System Notifications** and carries the ADR-088 master gate (`components/settings/PromptSuggestionsSettings.tsx`): when it is off, the backend never spawns a suggestion pass, so the chat input's ↑/↓/→ suggestion mode stays silent (the subsystem is fail-open — absence is unobservable on the chat path).

- The value lives in backend config, not a frontend store: the section reads the effective gate through `getConfig().prompt_suggestions?.enabled` once on mount (a payload without the block reads as **on** — the backend's nil default = on; the backend reports `false` only for an explicit `enabled: false` or an unloaded config). The toggle stays disabled until that read answers.
- Writes go through `setPromptSuggestionsEnabled` from `@/api/suggestions` (the subsystem's RPC module — the toggle rides the same bridge as the chat-input read path). The backend persists AND applies in one call (the session manager's outcome gate flips without a restart), so the UI updates optimistically and rolls back to the previous value when the RPC rejects — a rejected write persists nothing and flips nothing, leaving the previous value authoritative everywhere.

## Design System

One Dark theme. All colors as Tailwind v4 `@theme` custom properties:

| Token                 | Value   | Usage          |
| --------------------- | ------- | -------------- |
| `--color-background`  | #282c34 | App background |
| `--color-foreground`  | #abb2bf | Default text   |
| `--color-primary`     | #abb2bf | Actions, links (dark theme; `--color-primary-rgb: 82,139,255` / `#528bff` is a separate rgba-helper token) |
| `--color-destructive` | #e06c75 | Errors, delete |
| `--color-success`     | #98c379 | Success states |
| `--color-warning`     | #d19a66 | Warnings       |
| `--color-info`        | #61afef | Information    |
| `--color-highlight`   | #e5c07b | Highlights     |

### Typography

Font sizes are relative: text is sized with the named Tailwind scale (`text-xs`, `text-sm`, `text-base`, `text-lg`, …), whose rem values resolve against the single absolute anchor — `html { font-size: 14px }` in `frontend/src/index.css` (deliberately not 16px — rem-based spacing and radii are tuned for it). Absolute (px) font sizes are forbidden everywhere else.

The scale is tokenized in the `@theme` block of `frontend/src/index.css` — values mirror the Tailwind defaults exactly (rem, like every other rem length behind the 14px root), so the `text-*` utilities render unchanged; component CSS sizes text through `var(--text-*)` instead of raw rem, so retuning the scale never means touching component rules.

| Token            | Value      | Line-height        | Utility      |
| ---------------- | ---------- | ------------------ | ------------ |
| `--text-xs`      | `0.75rem`  | `calc(1 / 0.75)`   | `text-xs`    |
| `--text-sm`      | `0.875rem` | `calc(1.25 / 0.875)` | `text-sm`  |
| `--text-base`    | `1rem`     | `calc(1.5 / 1)`    | `text-base`  |
| `--text-lg`      | `1.125rem` | `calc(1.75 / 1.125)` | `text-lg`  |

The guard pins these values to the Tailwind defaults; a deliberate scale retune is a two-place edit (`@theme` values + this table) that re-renders utilities and component CSS together.

Invariants:

- Text sizes come from the named scale only; arbitrary `text-[Npx]`/`text-[Nrem]` font sizes are forbidden — only color arbitrary values are legal in the `text-[…]` slot (enforced by `frontend/src/test/typeScaleInvariant.test.ts`).
- CSS carries no `font-size: Npx` outside the `html` root rule — every other CSS font size is rem (enforced by the same guard via brace-matched location of the root rule). Component CSS references the `--text-*` tokens instead of raw rem; the one non-token rem size left is `.cm-completion-nerd-icon`'s `0.8125rem` (icon optical sizing with no slot on the xs..lg scale).
- Inline TS/TSX font sizes are relative too: a bare number or a number/px string (`fontSize: 10`, `fontSize: '13px'`) is forbidden; rem strings (CodeMirror themes) stay legal (enforced by the same guard).
- Deliberate exceptions, bound to an external API or canvas geometry: the xterm constructor option in `Terminal.tsx` (a px number by API) and the SVG labels on the research DAG canvas (`ResearchDagCanvas.tsx`, fontSize 9/11).

Font families are tokenized the same way: `--font-sans`, `--font-mono` and `--font-icon` live in the `@theme` block of `frontend/src/index.css`, with a TS mirror in `frontend/src/lib/fonts.ts` for the non-CSS consumers (CodeMirror themes and the xterm constructor need a literal stack string, not a `var()` reference; CSS is the source of truth — keep both in sync).

- Every text surface rides one of the two text stacks: proportional UI text is `--font-sans` (the `html` base via Tailwind preflight; the Settings → Appearance font pickers override exactly this chain through inline `--font-sans`/`--font-mono` properties on `<html>`, see ADR-078), monospaced text is `--font-mono`. The same mono stack applies everywhere — file viewer, terminal, SHAs, IDs — no per-surface variants. The chat input's autocomplete tooltip is UI text (sans), not mono, despite being CodeMirror-rendered (see `specs/domains/frontend/fonts.md`).
- The icon font `--font-icon` (SauceCodePro NF, embedded via `@font-face`) carries Nerd Font glyphs ONLY — file-tree icons (`.nerd-font-icon`) and completion icons (`.cm-completion-nerd-icon`). Never for text: a text surface that needs a monospaced look uses `--font-mono`.
- The terminal runs on the plain `--font-mono` stack with no icon font — no Nerd Font glyphs in terminal output.
- Hardcoded font-family stacks are forbidden: CSS may only reference `var(--font-*)` (plus the `@font-face` declaration itself); TS may only use the `FONT_*_STACK` constants from `lib/fonts.ts` (or a `var(--font-…)` string) — enforced by `frontend/src/test/typeScaleInvariant.test.ts`.

Dark color-scheme. Focus outlines globally suppressed. Custom scrollbar (8px, semi-transparent thumb).

## Communication Pattern

```
┌──────────────────────────────────────────────────────────────┐
│                      Frontend                                │
│                                                              │
│  src/api/*.ts ──RPC──→ src/api/runtime.ts  (transport seam)  │
│       ↑                        │                             │
│  src/stores/* ←──Events──      ├─ desktop webview (Wails):   │
│       ↑                        │    window.go.desktop.App.*  │
│  src/hooks/* ←──handlers──     │    window.runtime.EventsOn()│
│       ↑                        └─ browser tab (ADR-101):     │
│  src/components/* ←── React renders ←── store selectors      │
│       (the seam picks the transport per environment:         │
│        POST /api/rpc/<Method> + /ws via the web-access       │
│        sidecar, or the Wails bridge in the webview)          │
└──────────────────────────────────────────────────────────────┘
```

Project switching is orchestrated by `useProjectSwitchState`: it saves source-project UI state through project RPC wrappers (`saveProjectSwitchState`), calls `switchProject`, restores destination tabs/files from `getProjectSwitchState`, and then applies session fallback (saved session → latest session → create session). The saved-session pointer is PROJECT-scoped (one value per project; the No Project pseudo-project is one bucket for all CHAT sessions) — when the switch is driven by the workspace tab engine, the engine re-selects the activating tab's own remembered session on top of whatever the pointer landed on (see [tabs.md](tabs.md) § Activation sequence).

## Invariants

- No direct imports from `wailsjs/go/desktop/App` in components — all through `@/api/*`
- No object/array allocation inside Zustand selectors (causes infinite re-render)
- No module-level side effects in store files
- Every selector returns referentially stable value (primitive or direct store property)
- Derived values computed with `useMemo` in custom hooks, not in selectors
- All backend calls are async (never blocking UI thread)
- Project-switch persistence and restoration is hook-driven (`useProjectSwitchState`) and uses best-effort source-state save plus deterministic destination session fallback
- Project switch flow preserves order: save source UI state before `switchProject`, then restore destination file/session state after switch
- Session restore fallback during project switch is deterministic: saved session for destination project, otherwise latest destination session, otherwise a new session
- `lastRealProjectId` always tracks the most recent non-No-Project project activated (updated in `setActiveProjectId` when switching to a real project; preserved when switching to No Project)
- CHAT/CODE toggle switches projects via `switchProject()`; CHAT selects No Project, CODE selects `lastRealProjectId` (or first real project if the last one was deleted)
- All projects created through the CreateProjectDialog always require an external workspace directory; internal workspaces are reserved for No Project auto-creation
- An unpinned expanded file viewer overlays the chat and auto-collapses on an outside pointerdown, or on focus leaving it once it held focus (a freshly expanded viewer ignores the Radix focus-restore that follows opening a file from a portal menu — e.g. the Research panel's "View artifacts" dropdown); a pinned viewer remains an in-flow resizable column
- Collapsing an unpinned viewer preserves the unpinned preference and renders a 40px in-flow reopen rail
- Persisted desktop window dimensions are accepted only at or above the minimum usable size; invalid state falls back to defaults
- The workspace tab layer always holds at least one tab, its ACTIVE tab always mirrors the live workspace context, and every activation travels the single engine path — the `tabsEnabled` flag gates only visibility (tab bar + tab-cycling hotkeys) and notification-click routing, never the layer itself; the sidebar Settings gear is unconditional in both flag states, [ADR-093](../../decisions/093-sidebar-gear-unconditional.md) (see [tabs.md](tabs.md))
- Tab arrangements are app-lifetime (`tabStore` never persists); only the `tabsEnabled` flag persists across restarts (`c0wrk-sidebar-collapsed` v7, default off)
- The frontend is **zoom-safe** under the app-wide UI Scale (`zoom` on `<html>`, see [ui-scale.md](ui-scale.md)): the shell and full-height containers size with percentages, viewport-derived sizes use the `--ui-vh` primitive, and pointer-anchored floating panels open at the cursor and fully inside the visible window at any scale — enforced by `frontend/src/test/zoomViewportInvariant.test.ts` plus the per-primitive guards
- Every enabled interactive element shows the pointer cursor and every disabled one shows `not-allowed` — a base-layer cursor policy in `frontend/src/index.css` covers native `button`/`input[type=…]`/`select`/`label`/`summary` and ARIA widget roles (`button`, `menuitem*`, `option`, `tab`, `checkbox`, `radio`, `switch`, `combobox`, `link`, `treeitem`); utility classes (e.g. `cursor-grab` on drag canvases) still override it for intentional exceptions, and `cursor-default` on clickable elements is forbidden outside the allowlist in `frontend/src/test/clickableCursorInvariant.test.ts` (Radix disabled menu items keep `pointer-events-none`, so their cursor stays the UA default)
- Typography is relative-scale-governed: text sizes come from the named Tailwind scale anchored at the 14px `html` root (see Design System); arbitrary `text-[Npx]`/`text-[Nrem]` utilities, px CSS font sizes outside the root rule, and absolute inline `fontSize` values fail the source-scan guard in `frontend/src/test/typeScaleInvariant.test.ts` (API-bound exceptions: the xterm constructor in `Terminal.tsx` and the research DAG canvas SVG labels)

## Configuration

Frontend configuration is derived from backend (no separate frontend config file):

| Source                          | Parameter          | Purpose                    |
| ------------------------------- | ------------------ | -------------------------- |
| `GetConfig()` RPC              | `default_model`    | Default model display      |
| `GetConfig()` RPC              | provider `.models` | Per-provider enabled models|
| `GetLogLevel()` RPC            | log level          | Console/log verbosity      |
| `GetSecuritySettings()` RPC    | `security.groups`  | Tool-group policy UI state (per-group policy + execute blacklist, ADR-024) |
| `GetCapabilityModes()` RPC     | `capabilities.*`   | Capability chat/code activation modes UI (ADR-084) |
| `ListSkills()` RPC             | skills             | `/skill` autocomplete      |
| `localStorage`                 | sidebar width + collapsed state | Persistent left-panel layout |
| `localStorage`                 | file-viewer width + collapsed + pinned state | Persistent docked/floating right-panel layout |
| `~/.c0wrk/window_state.json`   | window width, height, maximized | Native desktop geometry across process restarts |
| `localStorage`                 | execution mode     | Normal/advanced toggle     |
| `localStorage`                 | selected model     | Per-message model override |

## Extension Points

- **New RPC wrapper**: add module in `frontend/src/api/` when backend exposes a new method
- **Project-switch persistence wrapper updates**: when project switch-state RPC names or payload fields change, update method probing (`Save/GetProjectSwitchState`, `Save/GetProjectUIState`) and guards in `frontend/src/api/projects.ts` + `frontend/src/types/guards.ts`
- **New store**: create in `frontend/src/stores/`, register in the store initialization sequence
- **New tab snapshot slice**: extend `TabUIState` in `frontend/src/lib/tabSnapshot.ts` with fail-closed sanitization (`sanitizeTabUI`) and `isSameTabUI` content equality — a deliberate v2 composition change per [tabs.md](tabs.md)
- **New event handler hook**: add to `frontend/src/hooks/` with type guard and store update logic
- **Project-switch orchestration changes**: extend `frontend/src/hooks/useProjectSwitchState.ts` to keep save-before-switch and restore-after-switch ordering stable
- **New display item type**: extend `groupMessages()` in `frontend/src/lib/chatUtils.ts` and add renderer in `ChatMessageRenderer.tsx`
- **New tool card**: add a `CardConfig` entry in `frontend/src/components/chat/toolCards/toolCardRegistry.ts` and (optionally) a body component in `toolCards/bodies/`
- **Custom autocomplete**: add a new `CompletionSource` in `frontend/src/lib/cmChatAutocomplete.ts` and register it in the `autocompletion({ override: [...] })` array

## Related Specs

- [ui-scale.md](ui-scale.md) — UI scale feature and the zoom-safety invariant
- [../capabilities.md](../capabilities.md) — capability activation-mode semantics the Capabilities settings surface and the mention filters consume
- [fonts.md](fonts.md) — user font selection: UI/mono family pickers (free text + detected + installed options) plus per-scope select-only smoothing pickers over the persisted `fontStore`, once-per-launch dual system-font detection, and the inline `--font-sans`/`--font-mono` `@theme`-token + `--font-smoothing-*` delivery on `<html>` ([ADR-078](../../decisions/078-user-font-selection.md), [ADR-079](../../decisions/079-font-smoothing-selection.md))
- [button-tooltips.md](button-tooltips.md) — the button tooltip convention: a `title=`, a Radix `TooltipTrigger` wrapper, or a statically visible label on every button — exactly one, the channels never combine (both AST guards flag a `title` under a `TooltipTrigger`); buttons that may hide their label always carry a `title`, always-labeled buttons never carry an echo, picker triggers follow the heading rule
- [stores.md](stores.md) — Zustand store catalog
- [../specs.md](../specs.md) — the Specs subsystem: the sidebar's fifth workspace tab (`specs`, after `research`) rendering `SpecsPanel` over the read-only `<workspace>/specs` tree — task→spec navigation, ADR timeline, tier-1 integrity findings, and `spec-*` skill dispatch ([ADR-105](../../decisions/105-vendor-spec-skillset.md), [ADR-106](../../decisions/106-specs-sidebar-panel.md))
- [loading-states.md](loading-states.md) — the three loading states (pre-React boot loader in `index.html`, chat-history spinner over `chatStore.historyLoaded`, session-list skeleton/loading row over the `sessions === null` sentinel), their markers and single-writer rules, and the cross-cutting invariants: CSP legality (no inline scripts, inline `<style>` allowed), zoom-safe overlay sizing, honest `null`-vs-`[]`/cancelled semantics, identical behavior in both transports (wails webview + web-access sidecar)
- [tabs.md](tabs.md) — the workspace tab layer: always-live model, `tabsEnabled` flag scope, single activation engine, snapshot slices ([ADR-085](../../decisions/085-workspace-tabs-single-engine.md))
- [git-operation-console.md](git-operation-console.md) — the footer log of the last git mutation result (button tint, anchored popover, per-project scope, acknowledge semantics)
- [git-changes-list.md](git-changes-list.md) — the Changes tab file list: porcelain-axis sections, flat name-first rows, tree basename rows, full-path tooltips
- [events.md](events.md) — event handling architecture
- [rendering.md](rendering.md) — message display pipeline
- [../../contracts/desktop-frontend.md](../../contracts/desktop-frontend.md) — RPC surface
- [../../contracts/event-catalog.md](../../contracts/event-catalog.md) — event types
