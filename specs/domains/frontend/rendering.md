# Frontend Rendering

## Role

Transforms flat message arrays into a structured display tree, rendering each item type with appropriate visual treatment. Handles message grouping, tool call correlation, and special display modes.

## Key Files

- `frontend/src/lib/chatUtils.ts` — groupMessages() transform
- `frontend/src/lib/chatGroupingHandlers.ts` — tool call, plan step, and reflection grouping handlers
- `frontend/src/components/chat/ChatArea.tsx` — chat container (hosts pinned last user message + message list + new-activity banner)
- `frontend/src/components/chat/ChatMessageRenderer.tsx` — item type dispatch
- `frontend/src/components/chat/toolCards/` — specialized tool card system (registry-driven per-tool rendering)
- `frontend/src/components/chat/toolCards/toolCardRegistry.ts` — per-tool card config lookup, `(cached)` / `(batched)` suffix stripping
- `frontend/src/components/chat/toolCards/ToolCard.tsx` — tool card component (renders cached/batched badges, suffix-aware title extraction)
- `frontend/src/components/chat/ChecklistCard.tsx` — checklist card (the visual reference style for pending-action cards)
- `frontend/src/components/chat/UserMessage.tsx` — user message component (supports `isPinned` mode for sticky rendering inside ChatArea)
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
| `user`               | User message              | Right-aligned bubble; `/skill` refs as chips, `@file` refs as clickable links |
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
| `goal_proposal`      | Goal sign-off prompt      | Checklist-style card (info accent); two **pre-filled editable** textareas (condition + verify) seeded from the proposal, Approve (submits edited values) / Cancel UI. Needs-clarification mode renders the clarification question prominently and focuses the verify field. Sinks while unresolved; settles at stream position when resolved (collapses to a settled "Goal approved"/"Goal cancelled" card). Only emitted in goal mode. |
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
7. **Checklist sinking**: `step_todo_update` messages → `checklist` DisplayItem; latest per key (stepId || standalone) supersedes previous; active (unchecked items) checklists are moved to the end of their container (root items for standalone, step children for step-associated) so they stay visible at the bottom while new content streams in above; settled (all-checked) checklists remain at their stream position

### Smart Auto-Scroll

- Scroll locked to bottom when user is within 50px of end
- "New activity" pill shown when messages arrive while scrolled up
- ScrollContext (React context) coordinates between components
- Auto-scroll temporarily suspended during user scroll-up

### Pinned Last User Message

All user messages always render in the chat history. The most recent user message is additionally rendered as a sticky element at the top of the chat area, but **only when it is not visible** in the scroll viewport:

- `ChatArea.tsx` finds the last user item and renders `UserMessage` with `isPinned` inside a sticky wrapper (`sticky top-0 z-10`)
- Visibility is tracked via an `IntersectionObserver` (root: scroll container, threshold: 0) watching the original message element in the chat history (located by `data-message-id`)
- The pin appears only when the original message is fully scrolled out of view; disappears when any part becomes visible again
- Collapsible when content exceeds `containerHeight / 7` (maxPinnedHeight) — click to expand full text
- Provides context for what the agent is working on when the original message is off-screen
- Does not scroll with message list (sticky positioning)

## Markdown Element Handling

The `Markdown` component (`markdownConfig.tsx`) supplies custom `react-markdown` element handlers on top of its remark/rehype plugin chain:

- **Local file links** — `href` values that are not external URLs are treated as workspace paths. Clicking resolves the path against the workspace root (`localFileLink.normalizePath`) and opens it in the File Viewer (`openFile` / `openFileAtLine` when a `#L<n>` anchor is present). Rendered as a keyboard-accessible `<span role="link">`.
- **External URLs** — `http`/`https`/`mailto`/`ftp`/`data:` hrefs (`localFileLink.isExternalUrl`) are dispatched to the system browser via `openExternalURL` → `runtime.BrowserOpenURL` (`open` / `xdg-open` / Windows shell handler). The Wails webview has no default browser, so `<a target="_blank">` is either ignored or opens inside the webview, which cannot render arbitrary pages; clicks are intercepted (`preventDefault`) and routed through the native runtime instead.
- **Local images** — a local `src` (relative or absolute disk path, not an external/data URL) is resolved to a base64 `data:` URL via the `ReadFileAsDataURL` RPC, because the webview cannot load `file://` or project-root-relative URLs. Candidates are tried in order (`markdownImageResolve.candidateImagePaths`): absolute `src` → single candidate; relative `src` → the markdown document's directory first, then the workspace root. A 1×1 transparent-PNG placeholder shows while loading or on failure (avoids the broken-image flicker). External/data `src` values pass through unchanged. Image embedding is a **file-viewer** feature: the file viewer passes `baseFilePath` + `workspaceRoot` to `Markdown`, chat rendering does not.

## Invariants

- groupMessages() is a pure function (testable without rendering)
- Display items are keyed by stable IDs (not array index)
- Streaming text renders in real-time without layout shift
- Tool call and result are always rendered together (never orphaned)
- Pending actions are always visible: unresolved action cards sink to the bottom of the chat stream (never scrolled out of reach while the user is at the bottom); resolved action cards settle at their stream position
- No component file exceeds 300 lines (extract into sub-components — see `ChatInput.tsx` for a component using sub-hooks to manage complexity)

## Error Handling

- **Missing event data**: `groupMessages()` skips items with unrecognized types rather than throwing; malformed payloads are silently dropped
- **Markdown rendering**: `react-markdown` wraps parse failures in a `<pre>` block with the error message; invalid markdown does not crash the component
- **Syntax highlighting**: `highlight.js` falls back to plain text rendering when the language is not recognized (no error boundary needed)
- **Mermaid diagrams**: rendering failures are caught by the Mermaid error callback and displayed as an error snippet instead of a blank diagram; lazy loading failures produce a "Diagram unavailable" placeholder
- **Streaming interruption**: streaming text lives per-session in `chatStore.streamingText[sessionId]`; it is flushed to a permanent message on `assistant_done` (`addMessage` + `clearStreamingText`), and any leftover streaming state is cleared on task lifecycle events (`task_complete`, `task_cancelled`)
- **File viewer errors**: binary detection (null byte) returns a "binary file" notice; read failures from backend display the error message in the viewer pane
- **Local image resolution**: each candidate path is tried in order; a failed `ReadFileAsDataURL` falls through to the next candidate, and if all fail the original `src` is used as-is (which cannot load in the webview, matching prior behavior). Resolution is cancelled on unmount/src-change to avoid setState-after-unmount
- **Missing message types**: `ChatMessageRenderer` renders unknown display item types as a muted "Unsupported message type" fallback
- **Scroll lock resilience**: `ChatScrollManager` handles edge cases where the scroll target element is unmounted during a transition (no-op, no exception)

## Related Specs

- [stores.md](stores.md) — chatStore provides raw message data
- [events.md](events.md) — how messages arrive via events
- [README.md](README.md) — overall frontend architecture
