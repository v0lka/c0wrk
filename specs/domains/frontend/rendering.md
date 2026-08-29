# Frontend Rendering

## Role

Transforms flat message arrays into a structured display tree, rendering each item type with appropriate visual treatment. Handles message grouping, tool call correlation, and special display modes.

## Key Files

- `frontend/src/lib/chatUtils.ts` — groupMessages() transform
- `frontend/src/lib/chatGroupingHandlers.ts` — tool call, plan step, and reflection grouping handlers
- `frontend/src/components/chat/ChatArea.tsx` — chat container (hosts the root message list + new-activity banner and enables sticky user turns)
- `frontend/src/components/chat/ChatMessageRenderer.tsx` — item type dispatch; at the root chat level, groups history into user-turn containers so each user message can stick within its own turn
- `frontend/src/components/chat/toolCards/` — specialized tool card system (registry-driven per-tool rendering)
- `frontend/src/components/chat/toolCards/toolCardRegistry.ts` — per-tool card config lookup, `(cached)` / `(batched)` suffix stripping
- `frontend/src/components/chat/toolCards/ToolCard.tsx` — tool card component (renders cached/batched badges, suffix-aware title extraction)
- `frontend/src/components/chat/ChecklistCard.tsx` — checklist card (the visual reference style for pending-action cards)
- `frontend/src/components/chat/UserMessage.tsx` — user message component with an optional sticky ("floating") mode: the message collapses to a single truncated line by default and expands to the full bubble on click (collapse again with another click); the collapsed line shows compact goal/attachment/image icons so the metadata is visible at a glance without expanding; an opaque background row plus a gradient fade strip below it erase chat content that scrolls underneath
- `frontend/src/components/chat/UserMessageContent.tsx` — renders user message content with skill chips and clickable file links (falls back to Markdown for messages without references)
- `frontend/src/components/chat/userMessageSegments.ts` — pure parser for `@file`/`/skill`/free-text segments in user input; accepts GitHub-canonical line anchors (`@file#L20-L36`) and legacy bare-number forms (`@file#20-36`)
- `frontend/src/lib/markdownConfig.tsx` — `Markdown` wrapper component with remark/rehype plugins and custom element handlers: local file links open the File Viewer; external URLs (http/https/mailto/ftp/data) are dispatched to the system browser via `openExternalURL` (`runtime.BrowserOpenURL`) since the webview ignores `<a target="_blank">`; local image `src` values are resolved to base64 `data:` URLs (see `markdownImageResolve.ts`). Accepts optional `baseFilePath` + `workspaceRoot` props for relative-image resolution (file viewer passes both; chat rendering omits them, so local-image embedding is a file-viewer feature)
- `frontend/src/lib/markdownImageResolve.ts` — pure helpers for resolving markdown image `src` values to local disk paths: `EXTERNAL_SRC_RE` (pass-through for external/data URLs), `normalizeAbsolutePath`, and `candidateImagePaths` (ordered list: absolute `src` → single candidate; relative `src` → markdown-file directory first, then workspace root). Pure so it can be unit-tested in isolation and the component file stays component-only (React Fast Refresh)
- `frontend/src/lib/localFileLink.ts` — pure utility functions for markdown link hrefs: `isLocalFileHref`, `isExternalUrl` (scheme-based detection for external URLs routed to the system browser), `parseLocalFileHref`, `normalizePath`; extracts line anchors in GitHub-canonical forms (`#L42`, `#L5-10`, `#L5-L10`, `#L20-L36`) and resolves both workspace-relative and absolute (out-of-workspace) paths
- `frontend/src/hooks/useWorkspacePath.ts` — resolves the active workspace root path for the current view (project `workspace_path` for regular projects; per-session workspace fetched via `GetSessionWorkspace` for No Project, with the project path as a loading fallback). Used by the file viewer for relative-path display and as the `workspaceRoot` for markdown image resolution
- `frontend/src/components/chat/ChatScrollManager.tsx` — scroll lock / auto-scroll coordination
- `frontend/src/components/chat/ChatNewActivityBanner.tsx` — “new activity” pill

## Behavior

### Message Grouping Pipeline

```
Backend persists: ChatMessage[] (flat, role-based)
         │
         ▼
Frontend converts: ChatMessageUI[] (semantic type, metadata)
         │
         ▼
groupMessages(): GroupedMessages { items: DisplayItem[] } (tree structure, 21 kinds)
         │
         ▼
ChatMessageRenderer: renders each DisplayItem by type
```

### Display Item Types (21 kinds)

| Type                 | Description               | Visual Treatment                                                              |
| -------------------- | ------------------------- | ----------------------------------------------------------------------------- |
| `user`               | User message              | Right-aligned bubble; `/skill` refs as chips, `@file` refs as clickable links. A message sent into a **paused** session carries an `is_nudge` marker and renders with a Zap "Nudge" badge (`bg-info/10 text-info`) — it is a normal user message that resumes the paused task (nudge-resume). A message sent while a task is **running** (live interjection) carries the same marker and badge — it interjects into the running task's next LLM request |
| `assistant`          | Assistant response        | Left-aligned, markdown rendered; local file links clickable (open File Viewer)|
| `thought`            | Single reasoning block    | Collapsed by default, muted                                                   |
| `thought_group`      | Multiple thoughts grouped | Collapsible container                                                         |
| `tool`               | Tool call + result pair   | Specialized card per tool (icon + verb + title + type-specific body). Batched sub-calls arrive with `" (batched)"` suffix — `ToolCard.tsx` strips the suffix, renders a "batched" badge, and looks up the original tool's card config. Cached tool reads arrive with `" (cached)"` suffix and follow the same pattern. |
| `tool_confirm`       | Pending confirmation      | Checklist-style card (warning accent); Allow/Deny/Ask-Agent buttons. **Sinks** to the bottom of the chat while unresolved; settles at stream position when resolved (success/destructive accent). Always rendered in the root stream, never nested in a plan step or subagent. |
| `ask_user`           | Agent question to user    | Checklist-style card (info accent); option list + custom-text input + Submit. Sinks while unresolved; settles at stream position when answered. |
| `step_limit`         | Budget decision           | Checklist-style card (warning accent); Allow Once/Always/Deny buttons. Sinks while unresolved; settles at stream position when decided. |
| `resume_action`      | Resume after failure      | Checklist-style card (destructive accent); Resume/Cancel buttons. Sinks while unresolved; settles at stream position when resolved (records `resumed`/`cancelled` decision). |
| `error`              | Error message             | Red accent, error icon                                                        |
| `service`            | System/status message     | Muted, small text (routing, retry, status events are grouped here)            |
| `plan_step`          | Plan step indicator       | Step badge with status                                                        |
| `subagent`           | Delegated subagent block  | Collapsible container (status badge, title, optional duration/error); nested children (plan steps, tools, thoughts) grouped inside. Rendered by `SubAgentBlock`. |
| `plan_review`        | Plan review message       | Checklist-style card (info accent); Approve/Request-Changes/Abandon UI. Sinks while unresolved; settles at stream position when decided. Only the last unresolved `plan_review` is kept (replan cycle supersedes earlier unresolved ones); resolved plan_reviews remain at their stream position. |
| `review_prompt`      | Code-review prompt        | Checklist-style card (info accent); Enter/Decline UI. "Enter" opens the review page (`ReviewPage` via the `c0wrk:review` tab) and enters the review loop; "Decline" dismisses. Sinks while unresolved; settles at stream position when decided (enter → success accent, decline → muted). Rendered by `ReviewPromptBlock`. |
| `goal_proposal`      | Goal sign-off prompt      | Checklist-style card (info accent); two **pre-filled editable** textareas (condition + verify) seeded from the proposal, Approve (submits edited values) / Cancel UI. Sinks while unresolved; settles at stream position when resolved (collapses to a settled "Goal approved"/"Goal cancelled" card). Only emitted in goal mode. |
| `reflection`         | Reflector analysis        | Warning accent, collapsible                                                   |
| `step_finish`        | Step completion marker    | Success/fail indicator (emitted by `finish` tool call)                        |
| `memory_read`        | Memory read notice        | Info badge (agent read from persistent memory)                                |
| `context_compaction` | Compaction notice         | Info badge (shows before/after fill %)                                        |
| `checklist`          | Checklist card            | Checkbox list with progress count; **sinks** to end of parent container while active (unchecked items), settles at stream position when all items checked. Renders standalone (no plan step) or nested in a `plan_step` block. Pending-action cards (`tool_confirm`/`ask_user`/`step_limit`/`plan_review`/`resume_action`) follow the same sinking semantics but always render in the root stream and use the same card chrome as this component. |

### Grouping Logic

Key transformations in `groupMessages()`:

1. **Tool call correlation**: matches `tool_call` with its `tool_result` by `tool_call_id` or composite key
2. **Thought collapsing**: consecutive thought messages grouped into `thought_group`
3. **Plan step nesting**: messages between `plan_step_start` and `plan_step_complete` nested under step
4. **Pending action sinking**: unresolved confirmations/questions (`tool_confirm`, `ask_user`, `step_limit`, `plan_review`, `goal_proposal`, `resume_action`) are pushed into the **root** items (never nested in a plan step or subagent, regardless of `plan_step_id`) and moved to the very end of the chat so they stay visible at the bottom while new content streams in above. Resolved actions remain at their stream position (like settled checklists). Only the last unresolved `plan_review` is kept (replan cycle).
5. **Subagent handling**: a `subagent_launch` message creates a `subagent` DisplayItem (collapsible container); subsequent messages are nested as its children until the matching `subagent_complete` closes the container. `subagent_complete` finalizes status/duration. Rendered by `SubAgentBlock`.
6. **Special tool handling**: `finish` tool call → `step_finish` item; `memory_read` event → `memory_read` item; `plan_review_*` events → `plan_review` item; `context_compaction` event → `context_compaction` item (with before/after fill %); `routing`/`retry`/`status` events → `service` item
7. **Checklist sinking**: `step_todo_update` messages → `checklist` DisplayItem; each update supersedes the previous one for the same **level** — a checklist nested inside an open `plan_step`/`subagent` is keyed by that block's `step_id`, while every root-level checklist (standalone `step_id=""` or any ad-hoc `step_id` whose block is suppressed/closed) shares a single root key, so only one active checklist renders per chat level. Active (unchecked items) checklists are moved to the end of their container (root items for standalone, step children for step-associated) so they stay visible at the bottom while new content streams in above; settled (all-checked) checklists remain at their stream position.

### Smart Auto-Scroll

- Scroll locked to bottom when user is within 50px of end
- "New activity" pill shown when messages arrive while scrolled up
- ScrollContext (React context) coordinates between components
- Auto-scroll temporarily suspended during user scroll-up

### Sticky User Turns

Every user message renders exactly once in the normal root chat stream. `ChatMessageRenderer` groups root items into user turns: a turn starts at a user message and includes all following items up to (but not including) the next user message.

- The user message at the start of each turn renders with `sticky top-0`; the enclosing turn is its natural sticky boundary, so the next user turn displaces it without a duplicate overlay.
- `ChatArea.tsx` enables sticky turns only for the root history. Nested renderers inside plan-step and subagent blocks retain ordinary, non-sticky user-message rendering.
- Streaming assistant output and the activity indicator are trailing content inside the final turn, so the latest user message remains sticky while generation is active.
- Sticky messages use one DOM branch and one `data-message-id` instance. There is no separately rendered last-user copy and no visibility state synchronized between copies.
- A sticky message **collapses to a single truncated line** by default and **expands to the full bubble on click** (collapse again with another click). The collapsed state shows a plain-text preview (whitespace collapsed, `truncate`) instead of the rich rendering; the expanded state restores metadata badges, file/skill chips, Markdown, and the footer. Clicks on interactive descendants while expanded (file refs, Markdown links, the copy button) do not collapse the message — the toggle handler inspects the event target's closest interactive ancestor and bails out.
  - In the collapsed state, **compact metadata indicators** precede the text: a Target icon (goal), a FileText icon with count (document attachments), and an ImageIcon icon with count (images). These come from `parseUserMessageMeta`; plain-text messages render no indicators.
- A sticky message draws a **full-width opaque background row** behind the bubble and a **gradient fade strip** below it (`bg-gradient-to-b from-background to-transparent`, `pointer-events-none`, fixed `h-6`). Content scrolling up toward the floating message dissolves smoothly instead of being hard-clipped — the same solid-to-transparent gradient technique used for the action buttons in the session list.
- Positioning is CSS-only. The sticky contract does not depend on `IntersectionObserver`, `ResizeObserver`, container-height measurement, or right-panel width. The collapse/expand state is local React state on the single sticky instance (no measurement-derived toggling).

## Markdown Element Handling

The `Markdown` component (`markdownConfig.tsx`) supplies custom `react-markdown` element handlers on top of its remark/rehype plugin chain:

- **Local file links** — `href` values that are not external URLs are treated as workspace paths. Clicking resolves the path against the workspace root (`localFileLink.normalizePath`) and opens it in the File Viewer (`openFile` / `openFileAtLine` when a `#L<n>` anchor is present). Rendered as a keyboard-accessible `<span role="link">`.
- **External URLs** — `http`/`https`/`mailto`/`ftp`/`data:` hrefs (`localFileLink.isExternalUrl`) are dispatched to the system browser via `openExternalURL` → `runtime.BrowserOpenURL` (`open` / `xdg-open` / Windows shell handler). The Wails webview has no default browser, so `<a target="_blank">` is either ignored or opens inside the webview, which cannot render arbitrary pages; clicks are intercepted (`preventDefault`) and routed through the native runtime instead.
- **Local images** — a local `src` (relative or absolute disk path, not an external/data URL) is resolved to a base64 `data:` URL via the `ReadFileAsDataURL` RPC, because the webview cannot load `file://` or project-root-relative URLs. Candidates are tried in order (`markdownImageResolve.candidateImagePaths`): absolute `src` → single candidate; relative `src` → the markdown document's directory first, then the workspace root. A 1×1 transparent-PNG placeholder shows while loading or on failure (avoids the broken-image flicker). External/data `src` values pass through unchanged. Image embedding is a **file-viewer** feature: the file viewer passes `baseFilePath` + `workspaceRoot` to `Markdown`, chat rendering does not.

### Mermaid Diagrams

`MermaidBlock` lazy-loads Mermaid and renders assistant-controlled diagram source with `securityLevel: 'strict'` and SVG text labels (`htmlLabels: false`). Before insertion through `dangerouslySetInnerHTML`, DOMPurify applies the SVG/SVG-filter profiles, removing scripts, event handlers, foreign HTML, and any upstream sanitizer regressions. Theme changes rerender the diagram; render/import failures show the source in an error card rather than a blank canvas.

The diagram canvas supports pointer-drag panning, cursor-centered wheel zoom, explicit zoom-in/zoom-out controls, a live percentage, and reset-to-fit. Fit never upscales above natural size; zoom remains clamped, and horizontal-dominant trackpad gestures are left to the browser. Temporary Mermaid render nodes and listeners are removed on source/theme change or unmount.

### Plan Review Feedback

A pending `plan_review` card offers Approve, Request Changes, Abandon, and Open in Viewer. Request Changes reveals a feedback textarea where Enter submits non-empty trimmed feedback and Shift+Enter inserts a newline, matching chat-input keyboard semantics. The decision and optional feedback travel through `plan_approval_response`; the card immediately records its settled decision locally and displays an error banner if emission fails.

### Chat Input Controls (Pause / Resume / Stop)

The chat input toolbar (`ChatInputToolbar`) adapts its controls to the session's execution state, driven from `chatStore.taskActive` + `chatStore.paused`:

| State | Controls | Behavior |
| ----- | -------- | -------- |
| **Running** (`taskActive`) | Pause + Stop | Pause → `pauseSession` RPC (sets ONLY the `pausing` in-flight flag; the input locks for the window); Stop → `cancelTask`. The input stays **open** (live-send): a message sent now interjects into the running task's next LLM request |
| **Pausing** (`pausing`) | Stop | Pause renders as a non-clickable spinner; activity label reads "Pausing". Input **locked** (sends in this window race the pause→paused transition and are rejected server-side with `ErrPausePending`). Cleared by `session_paused` or any terminal event |
| **Paused** (`paused`) | Resume + Stop | Resume → `resumeSession` RPC (forwards model/reasoning overrides; optimistic flip back to active); Stop → `cancelTask`. The input is **unlocked** (a paused task is not "active"), so the user can type |
| **Idle** | Optimize + Send | Normal send |

The input-lock matrix is a pure helper (`lib/chatInputLock.ts` `computeChatInputDisabled`): the input is disabled iff `pausing || isNoProject`. The placeholder advertises the affordance per state (running: "Working — your message joins the next request to the model"; pausing: "Pausing — the input unlocks once the pause lands").

**Per-message selector lock.** The selector cluster in the chat toolbar (`ModelCombobox`, `ReasoningCombobox`, `GoalToggle`, and — when goal mode is armed — `BudgetCombobox`) locks while the session is mid-task: `selectorsLocked = taskActive || pausing || compacting`. The controls unlock when the task finished, failed, or is cooperatively paused (`paused` sets `taskActive=false`; the resume path honors a freshly picked model/reasoning override). Each control receives a `disabled` prop (native disabled buttons — a11y/keyboard semantics) and shows the "Locked while the session is running" title; the wrapper dims the cluster. This is the frontend half of the session-pinning invariant: everything inside a running session — strict-judge evaluations included — stays on the provider/model the session runs on, so per-message model picks cannot move it mid-task (see [ADR-028](../../decisions/028-session-pinned-judge.md)). Picking a model while idle also persists the global `default_model` (partial `UpdateLLMConfig`) — that changes the default for FUTURE sessions only and never re-binds a live session's judge.

When the user **sends a message while paused**, it is a **nudge-resume**: the optimistic user message is marked `is_nudge: true`, the `paused` flag is cleared, `taskActive` is set, and `sendMessage` routes to `resumeSession` (the backend detects the paused task and injects the text as a trailing user-nudge turn). The placeholder reads "Paused — send a message to nudge-resume, or press Resume".

When the user **sends a message while a task is running**, it is a **live interjection**: the optimistic user message is marked `is_nudge: true` (same Zap badge as a nudge-resume) but the UI state is untouched — the task keeps running. The backend queues the text into the running request; it lands as the final user message of the next LLM request (see [session-lifecycle.md](../session-lifecycle.md) "Live-send"). A failed live send (e.g. attachments staged) reverts nothing but the optimistic message: the still-running task's state flags are preserved.

The goal status indicator (`GoalStatusIndicator`) is a **read-only** badge (icon + turn + budget); it has no Pause/Resume/Clear buttons — pause/resume is session-level.

## Invariants

- groupMessages() is a pure function (testable without rendering)
- Display items are keyed by stable IDs (not array index)
- Streaming text renders in real-time without layout shift
- Tool call and result are always rendered together (never orphaned)
- Pending actions are always visible: unresolved action cards sink to the bottom of the chat stream (never scrolled out of reach while the user is at the bottom); resolved action cards settle at their stream position
- Mermaid SVG always passes through strict Mermaid rendering and an SVG-only DOMPurify sink before DOM insertion; interactive pan/zoom transforms only the sanitized SVG container
- Plan-review feedback submits on Enter only when non-empty; Shift+Enter always remains a newline
- Component files are kept small as a target (~300 lines); complexity is managed by extracting sub-hooks/sub-components (see `ChatInput.tsx`). There is no enforced size cap — larger components (e.g. context menus, settings dialogs, modals) are accepted where splitting would reduce cohesion

## Error Handling

- **Missing event data**: `groupMessages()` skips items with unrecognized types rather than throwing; malformed payloads are silently dropped
- **Markdown rendering**: `react-markdown` wraps parse failures in a `<pre>` block with the error message; invalid markdown does not crash the component
- **Syntax highlighting**: `highlight.js` falls back to plain text rendering when the language is not recognized (no error boundary needed)
- **Mermaid diagrams**: rendering failures are caught by the Mermaid error callback and displayed as an error snippet instead of a blank diagram; lazy loading failures produce a "Diagram unavailable" placeholder
- **Streaming interruption**: streaming text lives per-session in `chatStore.streamingText[sessionId]`; it is flushed to a permanent message on `assistant_done` (`addMessage` + `clearStreamingText`), and any leftover streaming state is cleared on task lifecycle events (`task_complete`, `task_cancelled`)
- **File viewer errors**: binary detection (null byte) returns a "binary file" notice; read failures from backend display the error message in the viewer pane
- **Prompt optimization errors**: the editor keeps the user's original prompt, the active session's `optimizeError` in `chatInputStore` (keyed per session) drives a dismissible warning banner, and a later Optimize action starts a fresh pipeline after backend rewrite/call retries are exhausted
- **Local image resolution**: each candidate path is tried in order; a failed `ReadFileAsDataURL` falls through to the next candidate, and if all fail the original `src` is used as-is (which cannot load in the webview, matching prior behavior). Resolution is cancelled on unmount/src-change to avoid setState-after-unmount
- **Missing message types**: `ChatMessageRenderer` renders unknown display item kinds as nothing (`if (!Component) return null` — no fallback UI); render failures inside a known component are caught by the React error boundary, whose compact fallback reads "Failed to render message" (a separate mechanism)
- **Scroll lock resilience**: `ChatScrollManager` handles edge cases where the scroll target element is unmounted during a transition (no-op, no exception)

## Related Specs

- [stores.md](stores.md) — chatStore provides raw message data
- [events.md](events.md) — how messages arrive via events
- [README.md](README.md) — overall frontend architecture
