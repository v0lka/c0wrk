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
- `frontend/src/components/chat/PendingActionsBar.tsx` — action bar (confirm/ask/limit)
- `frontend/src/components/chat/UserMessage.tsx` — user message component (supports `isPinned` mode for sticky rendering inside ChatArea)
- `frontend/src/components/chat/UserMessageContent.tsx` — renders user message content with skill chips and clickable file links (falls back to Markdown for messages without references)
- `frontend/src/lib/markdownConfig.tsx` — Markdown wrapper component with remark/rehype plugins and custom link handler for local file navigation
- `frontend/src/lib/localFileLink.ts` — pure utility functions for detecting, parsing, and validating local file link hrefs in markdown output
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
groupMessages(): DisplayItem[] (tree structure, 16 kinds)
         │
         ▼
ChatMessageRenderer: renders each DisplayItem by type
```

### Display Item Types (16 kinds)

| Type                 | Description               | Visual Treatment                                                              |
| -------------------- | ------------------------- | ----------------------------------------------------------------------------- |
| `user`               | User message              | Right-aligned bubble; `/skill` refs as chips, `@file` refs as clickable links |
| `assistant`          | Assistant response        | Left-aligned, markdown rendered; local file links clickable (open File Viewer)|
| `thought`            | Single reasoning block    | Collapsed by default, muted                                                   |
| `thought_group`      | Multiple thoughts grouped | Collapsible container                                                         |
| `tool`               | Tool call + result pair   | Specialized card per tool (icon + verb + title + type-specific body). Batched sub-calls arrive with `" (batched)"` suffix — `ToolCard.tsx` strips the suffix, renders a "batched" badge, and looks up the original tool's card config. Cached tool reads arrive with `" (cached)"` suffix and follow the same pattern. |
| `tool_confirm`       | Pending confirmation      | Action buttons (Allow/Deny)                                                   |
| `ask_user`           | Agent question to user    | Form with inputs                                                              |
| `step_limit`         | Budget decision           | Action buttons                                                                |
| `resume_action`      | Resume after failure      | Resume button                                                                 |
| `error`              | Error message             | Red accent, error icon                                                        |
| `service`            | System/status message     | Muted, small text                                                             |
| `plan_step`          | Plan step indicator       | Step badge with status                                                        |
| `reflection`         | Reflector analysis        | Warning accent, collapsible                                                   |
| `step_finish`        | Step completion marker    | Success/fail indicator                                                        |
| `action_placeholder` | Pending action indicator  | Placeholder with label                                                        |
| `context_compaction` | Compaction notice         | Info badge                                                                    |

### Grouping Logic

Key transformations in `groupMessages()`:

1. **Tool call correlation**: matches `tool_call` with its `tool_result` by `tool_call_id` or composite key
2. **Thought collapsing**: consecutive thought messages grouped into `thought_group`
3. **Plan step nesting**: messages between `plan_step_start` and `plan_step_complete` nested under step
4. **Pending action extraction**: unresolved confirmations/questions extracted to PendingActionsBar
5. **Subagent handling**: subagent messages optionally collapsed

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

## Invariants

- groupMessages() is a pure function (testable without rendering)
- Display items are keyed by stable IDs (not array index)
- Streaming text renders in real-time without layout shift
- Tool call and result are always rendered together (never orphaned)
- Pending actions are always visible (sticky bar, never scrolled off-screen)
- No component file exceeds 300 lines (extract into sub-components — see `ChatInput.tsx` for a component using sub-hooks to manage complexity)

## Error Handling

- **Missing event data**: `groupMessages()` skips items with unrecognized types rather than throwing; malformed payloads are silently dropped
- **Markdown rendering**: `react-markdown` wraps parse failures in a `<pre>` block with the error message; invalid markdown does not crash the component
- **Syntax highlighting**: `highlight.js` falls back to plain text rendering when the language is not recognized (no error boundary needed)
- **Mermaid diagrams**: rendering failures are caught by the Mermaid error callback and displayed as an error snippet instead of a blank diagram; lazy loading failures produce a "Diagram unavailable" placeholder
- **Streaming interruption**: if the WebSocket/event stream disconnects mid-stream, `chatStore.flushStreamingToMessage()` preserves the partial content as a permanent message
- **File viewer errors**: binary detection (null byte) returns a "binary file" notice; read failures from backend display the error message in the viewer pane
- **Missing message types**: `ChatMessageRenderer` renders unknown display item types as a muted "Unsupported message type" fallback
- **Scroll lock resilience**: `ChatScrollManager` handles edge cases where the scroll target element is unmounted during a transition (no-op, no exception)

## Related Specs

- [stores.md](stores.md) — chatStore provides raw message data
- [events.md](events.md) — how messages arrive via events
- [README.md](README.md) — overall frontend architecture
