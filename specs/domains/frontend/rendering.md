# Frontend Rendering

## Role

Transforms flat message arrays into a structured display tree, rendering each item type with appropriate visual treatment. Handles message grouping, tool call correlation, and special display modes.

## Key Files

- `frontend/src/lib/chatUtils.ts` — groupMessages() transform
- `frontend/src/components/chat/ChatArea.tsx` — chat container (hosts pinned last user message + message list + new-activity banner)
- `frontend/src/components/chat/ChatMessageRenderer.tsx` — item type dispatch
- `frontend/src/components/chat/PendingActionsBar.tsx` — action bar (confirm/ask/limit)
- `frontend/src/components/chat/UserMessage.tsx` — user message component (supports `isPinned` mode for sticky rendering inside ChatArea)
- `frontend/src/components/chat/UserMessageContent.tsx` — renders user message content with skill chips and clickable file links (falls back to Markdown for messages without references)
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
groupMessages(): DisplayItem[] (tree structure, 17 kinds)
         │
         ▼
ChatMessageRenderer: renders each DisplayItem by type
```

### Display Item Types (17 kinds)

| Type                 | Description               | Visual Treatment                                                              |
| -------------------- | ------------------------- | ----------------------------------------------------------------------------- |
| `user`               | User message              | Right-aligned bubble; `/skill` refs as chips, `@file` refs as clickable links |
| `assistant`          | Assistant response        | Left-aligned, markdown rendered                                               |
| `thought`            | Single reasoning block    | Collapsed by default, muted                                                   |
| `thought_group`      | Multiple thoughts grouped | Collapsible container                                                         |
| `tool`               | Tool call + result pair   | Code block with status icon                                                   |
| `tool_confirm`       | Pending confirmation      | Action buttons (Allow/Deny)                                                   |
| `ask_user`           | Agent question to user    | Form with inputs                                                              |
| `step_limit`         | Budget decision           | Action buttons                                                                |
| `resume_action`      | Resume after failure      | Resume button                                                                 |
| `error`              | Error message             | Red accent, error icon                                                        |
| `service`            | System/status message     | Muted, small text                                                             |
| `plan_step`          | Plan step indicator       | Step badge with status                                                        |
| `reflection`         | Reflector analysis        | Warning accent, collapsible                                                   |
| `step_finish`        | Step completion marker    | Success/fail indicator                                                        |
| `memory_read`        | Fact retrieval indicator  | Info accent                                                                   |
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

The most recent user message is rendered as a sticky element at the top of the chat area. It is NOT a separate component — `ChatArea.tsx` finds the last user item, filters it out of the main list, and renders `UserMessage` with `isPinned` inside a sticky wrapper (`sticky top-0 z-10`):

- Collapsible when content exceeds `containerHeight / 7` (maxPinnedHeight) — click to expand full text
- Provides context for what the agent is working on
- Does not scroll with message list (sticky positioning, not a separate render tree)

## Invariants

- groupMessages() is a pure function (testable without rendering)
- Display items are keyed by stable IDs (not array index)
- Streaming text renders in real-time without layout shift
- Tool call and result are always rendered together (never orphaned)
- Pending actions are always visible (sticky bar, never scrolled off-screen)
- No component file exceeds 200 lines (extract into sub-components)

## Related Specs

- [stores.md](stores.md) — chatStore provides raw message data
- [events.md](events.md) — how messages arrive via events
- [README.md](README.md) — overall frontend architecture
