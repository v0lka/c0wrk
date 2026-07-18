# Frontend Events

## Role

Manages real-time event subscription, validation, and store updates. Events flow from backend during task execution, enabling live UI updates.

## Key Files

- `frontend/src/hooks/useSessionEvents.ts` — master event subscription hook
- `frontend/src/hooks/events/useChatEvents.ts` — streaming, thoughts, errors, task lifecycle (task_complete, task_cancelled, task_failed_resumable)
- `frontend/src/hooks/events/usePlanEvents.ts` — plan generation, step lifecycle
- `frontend/src/hooks/events/useToolEvents.ts` — tool call/result correlation
- `frontend/src/hooks/events/useActionEvents.ts` — confirmations, ask_user, step limits
- `frontend/src/hooks/events/useContextEvents.ts` — context fill, compaction
- `frontend/src/hooks/events/useLifecycleEvents.ts` — routing, step_start, step_complete, retry, step_retry
- `frontend/src/hooks/events/useSubagentEvents.ts` — subagent lifecycle
- `frontend/src/hooks/events/useBlackboardEvents.ts` — blackboard state updates
- `frontend/src/hooks/events/useAttachmentEvents.ts` — attachment list updates
- `frontend/src/hooks/events/usePlanReviewEvents.ts` — plan review lifecycle (plan_review_ready, plan_validation_failed, etc.)
- `frontend/src/hooks/events/useGoalEvents.ts` — goal-mode events (`goal_proposal` pending action + `goal_status`/`goal_progress` service-phase events → goalStore + chat message)
- `frontend/src/hooks/events/useTerminalEvents.ts` — terminal output events
- `frontend/src/hooks/events/useToolJudgeEvents.ts` — LLM judge response events
- `frontend/src/types/events.ts` — event payload type definitions
- `frontend/src/types/guards.ts` — type guard functions

## Behavior

### Subscription Composition

```
useSessionEvents(sessionId)
  ├─ On mount/sessionId change:
  │   ├─ Subscribe to session:${sessionId}:* events
  │   └─ Set up dispatch to type-specific handlers
  │
  ├─ Delegates to focused hooks:
  │   ├─ useChatEvents → chatStore updates (streaming, thoughts, errors, task lifecycle)
  │   ├─ usePlanEvents → planStore updates
  │   ├─ useToolEvents → chatStore (tool messages)
  │   ├─ useActionEvents → chatStore (pending actions)
  │   ├─ useContextEvents → chatStore (context fill)
  │   ├─ useLifecycleEvents → chatStore (routing, step_start/complete, retry)
  │   ├─ useSubagentEvents → planStore
  │   ├─ useBlackboardEvents → blackboardStore
  │   ├─ useAttachmentEvents → attachmentsStore
  │   ├─ usePlanReviewEvents → planStore + planReviewStore
  │   ├─ useGoalEvents → goalStore (goal status/progress) + chatStore (goal_proposal message)
  │   ├─ useTerminalEvents → terminal state
  │   └─ useToolJudgeEvents → tool confirmation state
  │
  └─ On unmount/sessionId change:
      └─ Unsubscribe all listeners
```

### Handler Pattern

Each event handler follows the same pattern:

```typescript
function handleAssistantChunk(data: unknown, sessionId: string) {
  // 1. Type guard validates payload
  if (!isAssistantChunkData(data)) return;

  // 2. Update activity status
  chatStore.setActivityStatus("Generating response...");

  // 3. Update domain-specific store
  chatStore.setStreamingText(data.accumulated_content, sessionId);

  // 4. Auto-scroll handled by ChatScrollManager component (not by event handlers)
}
```

### Streaming Flow

```
Backend emits assistant_chunk events (rapid, during LLM streaming):
  → handler accumulates text in chatStore.streamingText[sessionId]
  → component renders streaming text in real-time

Backend emits assistant_done (once, when LLM finishes):
  → handler calls chatStore.addMessage(sessionId, message) + chatStore.clearStreamingText()
  → streaming state cleared
  → permanent ChatMessageUI added to message list
```

### Pending Actions

Certain events create "pending actions" that require user response:

| Event          | Action Type          | UI                 | Response Event          |
| -------------- | -------------------- | ------------------ | ----------------------- |
| `tool_confirm` | Tool confirmation    | Allow/Deny buttons | `tool_confirm_response` |
| `ask_user`     | Multi-question form  | Form with inputs   | `ask_user_response`     |
| `step_limit`   | Step budget decision | Allow/Deny/Always  | `step_limit_response`   |
| `goal_proposal` | Goal sign-off | Approve (with editable condition/verify textareas) / Cancel | `goal_proposal_response` (event) or `ConfirmGoal`/`CancelGoal` RPC — both funnel through one resolver |
| `plan_review_ready` | Plan review     | Approve/Reject + feedback | `ApprovePlan` / `RejectPlan` RPC |

Pending actions are stored in chatStore and rendered by the PendingActionsBar component.

## Error Handling

- Invalid payload (type guard fails) → event silently dropped, logged in dev mode
- Event for wrong session → ignored (sessionId mismatch)
- Store update throws → caught by error boundary, logged

## Invariants

- Event handlers are pure functions (testable without React rendering)
- Type guards validate at ingestion point — downstream code is fully typed
- Only one subscription per session at a time (previous unsubscribed on change)
- Streaming state is per-session (multiple sessions don't interfere)
- Pending actions expire on task completion/cancellation

## Related Specs

- [stores.md](stores.md) — store structure for event-driven updates
- [rendering.md](rendering.md) — how updated store state renders
- [../../contracts/event-catalog.md](../../contracts/event-catalog.md) — complete event reference
