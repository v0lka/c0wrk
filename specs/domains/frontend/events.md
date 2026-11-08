# Frontend Events

## Role

Manages real-time event subscription, validation, and store updates. Events flow from backend during task execution, enabling live UI updates.

## Key Files

- `frontend/src/api/runtime.ts` — the event transport seam every subscription travels: `onSessionEvent(sessionId, type, cb)` builds the name `session:${sessionId}:${type}` and `onGlobalEvent(type, cb)` covers the global names; both go through `subscribe`, which registers the callback in a per-event-name fan-out registry and (desktop transport only) also registers it directly with `window.runtime.EventsOn`, so an individually-emitted event reaches the callback exactly once and a batched one arrives through the single envelope listener (`EVENT_BATCH_NAME` = `c0wrk:events:batch`, installed lazily by `installBatchListener`) that `dispatchEnvelope` unwraps into the same registry. The web-access websocket router normalizes its frames into that one dispatch path, so per-event order, payload and null-filtering are transport-agnostic; a payload the guard rejects is surfaced through `reportDroppedEvent`
- `frontend/src/hooks/useSessionEvents.ts` — master event subscription hook
- `frontend/src/hooks/events/useChatEvents.ts` — streaming, thoughts, errors, task lifecycle (task_complete, task_cancelled), `memory_read` (the compact "memory read" card — persisted as role `memory_read`, so the restored row renders the identical card), `session_paused` (sets the per-session `paused` flag + clears `taskActive`, unlocking the input — SUPPRESSED while `chatStore.compacting[session]` is set, the manual-compaction flow's own pause), and `session_resumed` (clears the `paused` flag + sets `taskActive`, re-locking the input)
- `frontend/src/hooks/events/usePlanEvents.ts` — plan generation, step lifecycle
- `frontend/src/hooks/events/useToolEvents.ts` — tool call/result correlation and tool confirmation (`tool_confirm` via shared handlers), the judge-phase pair (`tool_judge_started`/`tool_judge_finished`), and the non-blocking `autonomy_decision` audit notice
- `frontend/src/hooks/events/useActionEvents.ts` — `ask_user`, step limits, resume actions (`task_failed_resumable`/`task_resumed`), and plan review (`plan_review_ready`) via shared handlers
- `frontend/src/hooks/events/useContextEvents.ts` — context fill (session-level **and** step-scoped), compaction. Step-scoped `context_fill` (a `plan_step_id` is set — subagent/executor steps) writes the step fill into `chatStore.stepContextFill`, the step's own context-window totals into `chatStore.stepContextTokens` (a subagent/executor step reports its own window, separate from the conductor's), and still refreshes the session token sums — a subagent's own fill never clobbers the conductor's session-level fill the status bar renders. A step-scoped event carrying `session_root_mirror` (the root conductor emitter's dynamic inline-step scope — the Go side sets the flag exactly on `isSessionRoot && stepID != ""`) is additionally mirrored into the session-level fill/tokens immediately: without the mirror the mid-step status bar rests on the `session_tokens` re-broadcast alone, which fires **before** `ContextFill` within one sp4rk executor iteration and therefore carries the previous iteration's fill (a one-iteration lag; sp4rk's emission order is canonical upstream). Session-root events (no `plan_step_id` — conductor emissions plus the lazy-local-probe window re-broadcast) update the session-level fill/tokens. Also owns the manual-compaction lifecycle: `compaction_started` sets `chatStore.compacting` + the "Compacting" activity, `compaction_finished` releases it (failure → "Compaction failed" label; a failed auto-resume → `paused_without_resume` re-applies the paused state, since `session_paused` was suppressed while compacting; any other outcome with nothing to resume — idle session including an idle no-op (`nothing_compacted`, no card follows), cancelled flow, plain success — clears the activity; a successful auto-resume or a deferral (`deferred_to_resume`) leaves the label to `task_resumed`); pure handlers `handleCompactionStarted`/`handleCompactionFinished` are shared-testable
- `frontend/src/hooks/events/useLifecycleEvents.ts` — routing, step_start, step_complete, retry, step_retry
- `frontend/src/hooks/events/useSubagentEvents.ts` — subagent lifecycle
- `frontend/src/hooks/events/useWorkUnitEvents.ts` — the LIVE half of the durable work-unit overlay: `work_unit_settled` records a unit the resume funnel explicitly settled (it will not relaunch it) through `lib/sessionRuntime.applyWorkUnitSettled`, and a fresh `subagent_launch`/`plan_step_start` carrying the same `step_id` is the resume proof that clears the stale entry (`chatStore.clearWorkUnitStep`) so the block stops rendering paused/interrupted. The durable snapshot half rides `GetSessionRuntimeStatus.work_units` on restart/session load
- `frontend/src/hooks/events/useBlackboardEvents.ts` — blackboard state updates
- `frontend/src/hooks/events/useAttachmentEvents.ts` — attachment list updates
- `frontend/src/hooks/events/hitlHandlers.ts` — shared HITL event handlers (`handleToolConfirmEvent`, `handleAskUserEvent`, `handleStepLimitEvent`, `handlePlanReviewEvent`, `handleAutonomyDecisionEvent`, the judge-phase pair `handleToolJudgeStartedEvent`/`handleToolJudgeFinishedEvent`, `handleHitlResolvedEvent`, plus the `assertTaskActiveFromPrompt`/`resolveConfirmForToolResult` correlators) used by `useToolEvents` (`tool_confirm`/`tool_judge_*`/`autonomy_decision`) and `useActionEvents` (`ask_user`/`step_limit`/`plan_review`/`hitl_resolved`), plus the background-session watcher; `handleHitlResolvedEvent` settles the chat card AND the goal-store mirror, and re-queries `GetPendingActions` for any session holding a `true` pending-override bit in `activeSessionsStore` (authoritative apply, runs before the chat-card early return) so the live-sessions badge retires with the card
- `frontend/src/hooks/events/useReviewRestore.ts` — restores code-review buffer state on session activation (reloads comments, reopens review page if mid-loop, reconciles stale loop flags)
- `frontend/src/hooks/events/useGoalEvents.ts` — goal-mode events (`goal_proposal` and `goal_suggestion` pending actions + `goal_status`/`goal_progress` service-phase events → goalStore + chat message)
- `frontend/src/hooks/events/useE2SStateEvents.ts` — E2S execution-state events (`e2s_state` Σ snapshots → e2sStore via `e2sHandlers`; the outgoing session's snapshot is dropped on switch)
- `frontend/src/hooks/usePromptSuggestionsSync.ts` — prompt-suggestions refetch wiring (ADR-088): loads the active session's set on session/project change (session creation rides the session-change trigger — a create selects the new session) and refetches on the session-scoped `prompt_suggestions` event; **mounted by the chat-input surface** with the active `(sessionId, projectId)` (not delegated by `useSessionEvents`, which carries no project identity) — the store, triggers and selector contract are specified in [stores.md](stores.md) § promptSuggestionsStore
- `frontend/src/hooks/events/useSoundEvents.ts` — sound-notification events (plays Web Audio tones on task lifecycle milestones when `soundStore.enabled`); composed by `useSessionEvents`
- `frontend/src/hooks/useFileDrop.ts` — global `files:dropped` subscription, HTML5 drag overlay, and navigation suppression
- `frontend/src/hooks/useExitGuard.ts` — global `app:exit_requested` subscription (mounted once at the app root, above the per-phase renders): validates the intercepted-quit payload and writes `exitGuardStore`; a malformed payload is reported via `reportDroppedEvent` and still opens the generic list-less modal — the backend has already prevented the quit, so an unanswered dialog would leave the app unclosable. The user's decision travels back through the `ConfirmExit` RPC from `ExitConfirmDialog` (a pure view over the store; `update_pending` in the payload switches it to restart context)
- `frontend/src/hooks/useStageAttachments.ts` — shared picker/drop attachment staging and model-vision filtering
- `frontend/src/hooks/events/goalHandlers.ts` — shared goal event handlers (`handleGoalProposalEvent`, `handleGoalSuggestionEvent`, `handleGoalStatusEvent`, `handleGoalProgressEvent`) used by `useGoalEvents` (foreground) and the background-session watcher (mirrors the `hitlHandlers.ts` pattern)
- `frontend/src/hooks/events/e2sHandlers.ts` — shared E2S state handler (`handleE2SStateEvent`, applies a full `e2s_state` Σ snapshot to the e2sStore) used by `useE2SStateEvents` (mirrors the `goalHandlers.ts` pattern)
- `frontend/src/hooks/events/sessionLifecycleHandlers.ts` — shared session pause/resume handlers (`handleSessionPausedEvent`, `handleSessionResumedEvent`) used by `useChatEvents` (foreground) and the background-session watcher (mirrors the `hitlHandlers.ts` pattern)
- `frontend/src/hooks/events/useTerminalEvents.ts` — terminal output events; **component-mounted** by `terminal/Terminal.tsx` (not delegated by `useSessionEvents`)
- `frontend/src/hooks/events/useToolJudgeEvents.ts` — LLM judge response events; **component-mounted** by `chat/ToolConfirmation.tsx` (not delegated by `useSessionEvents`)
- `frontend/src/types/events.ts` — event payload type definitions
- `frontend/src/types/guards.ts` — type guard functions

## Behavior

### Subscription Composition

```
useSessionEvents(sessionId)
  ├─ On mount/sessionId change (its own effect):
  │   ├─ Reset planStore.planGroups for the incoming session (taskActive and
  │   │   the per-session maps are deliberately preserved — the runtime
  │   │   reconcile and useTaskFlagRestore own them)
  │   └─ Load the persisted session token totals (getSessionTokens →
  │       chatStore.setSessionTokens; a failure keeps the previous values)
  │
  ├─ Delegates to focused hooks (the 14 composed by useSessionEvents); each
  │   subscribes per event name through api/runtime.onSessionEvent, which
  │   registers `session:${sessionId}:${type}` and dispatches to that hook's
  │   type-specific handler:
  │   ├─ useChatEvents → chatStore updates (streaming, thoughts, errors, task lifecycle)
  │   ├─ usePlanEvents → planStore updates
  │   ├─ useToolEvents → chatStore (tool messages, tool_confirm / tool_judge_* /
  │   │   autonomy_decision via hitlHandlers)
  │   ├─ useActionEvents → chatStore (pending actions: ask_user/step_limit/resume; plan_review via hitlHandlers)
  │   ├─ useContextEvents → chatStore (context fill — session-level + step-scoped maps —, manual-compaction lifecycle)
  │   ├─ useLifecycleEvents → chatStore (routing, step_start/complete, retry)
  │   ├─ useSubagentEvents → planStore
  │   ├─ useWorkUnitEvents → chatStore work-unit overlay (work_unit_settled +
  │   │   the subagent_launch/plan_step_start resume proof)
  │   ├─ useBlackboardEvents → blackboardStore
  │   ├─ useAttachmentEvents → attachmentsStore
  │   ├─ useReviewRestore → reviewStore (restores code-review buffer on session activation)
  │   ├─ useGoalEvents → goalStore (goal status/progress) + chatStore (goal_proposal and goal_suggestion messages, via goalHandlers)
  │   ├─ useE2SStateEvents → e2sStore (E2S execution-state Σ snapshots, via e2sHandlers)
  │   └─ useSoundEvents → sound playback (task lifecycle milestones, gated by soundStore.enabled)
  │
  │  (useTerminalEvents and useToolJudgeEvents are component-mounted, not
  │   orchestrator-delegated: Terminal.tsx and ToolConfirmation.tsx subscribe
  │   to their own session-scoped events directly, as does
  │   usePromptSuggestionsSync — the chat-input surface mounts it with the
  │   active (sessionId, projectId) pair, which the orchestrator hook does
  │   not carry)
  │
  └─ On unmount/sessionId change:
      └─ Unsubscribe all listeners
```

### Global Event Subscriptions

Global (non-session-scoped) events travel the same seam — `onGlobalEvent` for a typed name, `subscribe` for a raw one — and are owned by the surface that reacts; `useSessionEvents` is not involved. The authoritative name/payload catalog is [../../contracts/event-catalog.md](../../contracts/event-catalog.md).

| Event group | Subscriber | Effect |
| ----------- | ---------- | ------ |
| `backend:ready`, `config:updated` | `useProjectLoader`, `useVectorIndexStatus`, `useUpdateChecker`, `AppLayout`, `EmbeddedModelStatus`, and the config-mirror loaders (`useExperimentalFeatures`, `useIndexingSettings`, `useSidebarPacks`, `useAutonomyLoader`, `useModelProfilesGate`; `useSystemFonts` on `backend:ready` only) | one-shot hydration plus re-hydration of every config-mirroring store after a persisted config mutation |
| `startup_error`, `runtime_error`, `tool_manager:start`/`progress`/`done` | `App.tsx` | boot-phase error surface and the tool-manager progress toasts |
| `project:created`/`deleted`/`renamed`/`switched` | `useProjectLoader`; `GitConfigRiskToast` on `project:switched` | project list and active-project refresh |
| `sessions:loaded`/`sessions:changed`, `session:renamed` | `useSessionLoader`; `useActiveSessionsRefresh` (in `activeSessionsStore.ts`) on `sessions:changed` | foreground session list refresh plus the cross-project live-sessions snapshot |
| `workspace:tree_changed` | `FileTreePanel`, `useFileViewerData`, `useFileSearch`, `useProjectGitRepo`, `useGitStatusEvents`, `useResearchStatusEvents`, `SpecsPanel`, `ReviewPage`, `lib/cmChatAutocomplete` | tree/viewer/specs invalidation and the `@file` completion cache drop |
| `git:status_changed` | `useGitStatusEvents`, `ReviewPage` | git panel and review diff refresh |
| `vector_index:status` | `useVectorIndexStatus`, `useVectorSearch`, `useVectorReindex` | index status/progress and search availability |
| `research:changed`, `research:file_changed`, `papers:changed` | `useResearchStatusEvents`, `useResearchFileWatcher`, `usePapersEvents` | research panel and paper library refresh |
| `skills:changed`, `agents:changed`, `commands:changed`, `mcp:ready` | `lib/refCatalogs`, `lib/cmChatAutocomplete`, `MCPSettings` | `/`-mention catalog invalidation and the MCP settings reload |
| `update:available`/`none`/`progress`/`downloaded`/`error` | `api/updater` subscribers → `useUpdateChecker` → `updateStore` | self-update UI state machine |
| `embedded_llm:state`/`install_progress`/`notice` | `api/embedded` subscribers → `subscribeEmbeddedLLMEvents` (`stores/embeddedLLMSync.ts`) | embedded-model status invalidation and install progress |
| `chatgpt_auth:state`, `kimi_auth:state` | `api/auth` subscribers | subscription-provider auth-flow state |
| `project:git_config_risk` | `api/gitConfigRisk` → `GitConfigRiskToast` | risky-git-config toast |
| `notification_clicked` | `api/notifications` | system-notification banner click routing |
| `files:dropped` | `useFileDrop` | attachment staging (§ Native File Drop) |
| `app:exit_requested` | `useExitGuard` | intercepted-quit confirmation (§ Close Guard) |

### Handler Pattern

Each event handler follows the same pattern:

```typescript
function handleAssistantChunk(data: unknown, sessionId: string) {
  // 1. Type guard validates payload — a rejection is reported, never silent
  if (!isAssistantChunkData(data)) { reportDroppedEvent("assistant_chunk", data); return; }

  // 2. Update activity status
  chatStore.setActivityStatus(sessionId, "Generating response...");

  // 3. Update domain-specific store (a plan-step/subagent-scoped chunk commits
  //    inside its own block and never enters the session-global stream buffer)
  if (data.plan_step_id) return;
  chatStore.setStreamingText(sessionId, data.accumulated_content);

  // 4. Auto-scroll handled by ChatScrollManager component (not by event handlers)
}
```

### Streaming Flow

```
Backend emits assistant_chunk events (rapid, during LLM streaming):
  → handler accumulates text in chatStore.streamingText[sessionId]
  → component renders streaming text in real-time

Backend emits assistant_done (once, when LLM finishes):
  → handler calls chatStore.addMessage(sessionId, message) + chatStore.clearStreamingText(sessionId)
  → streaming state cleared
  → permanent ChatMessageUI added to message list
```

### Native File Drop

`files:dropped` is a global desktop event, not part of `useSessionEvents`. In chat input mode, `useFileDrop` validates `{paths, x, y}` and forwards a defensive copy of `paths` to `useStageAttachments` for the active session. That shared hook applies the same image/vision filtering and backend `AttachFiles` call as the native picker.

Document-level HTML5 `dragenter`/`dragover`/`dragleave`/`drop` listeners maintain a reference-counted overlay and suppress the webview's default open-file navigation. They do not read filesystem paths from `dataTransfer`; Wails native drop is the sole path source. Leaving chat mode unsubscribes the global event and clears drag state.

### Close Guard

`app:exit_requested` is a global desktop event: the Wails `OnBeforeClose` hook intercepted a quit because sessions have live work (running task or in-flight manual compaction; paused/unfinished tasks are persisted and don't count). `useExitGuard` — mounted once at the app root, above the per-phase renders — validates `{sessions, update_pending}` (each session carries `id`, `name`, `compacting`, and `hung` — the last true only when a **Stop** request has gone unanswered past the backend threshold, so the dialog labels that session "not responding"; a cooperative pause never sets it) and opens `ExitConfirmDialog` over `exitGuardStore`; store-backed state survives the app-phase remounts of the dialog component. A malformed payload is reported via `reportDroppedEvent` and degrades to the generic list-less modal, because the backend has already prevented the quit and an unanswered dialog would leave the app unclosable. The user's answer travels back through the `ConfirmExit` RPC — never a response event, since the decision must reach the process that owns the exit-confirmed bypass flag. `update_pending: true` (the intercepted quit belongs to `ApplyUpdate`) switches the dialog to restart context; the backend marker self-expires with the staged updater's parent-wait window, so a cancelled-then-retried quit after it degrades to a plain quit.

### Pending Actions

Certain events create "pending actions" that require user response:

| Event          | Action Type          | UI                 | Response Event          |
| -------------- | -------------------- | ------------------ | ----------------------- |
| `tool_confirm` | Tool confirmation    | Allow/Deny buttons | `tool_confirm_response` |
| `ask_user`     | Multi-question form  | Form with inputs   | `ask_user_response`     |
| `step_limit`   | Step budget decision | Allow/Deny/Always  | `step_limit_response`   |
| `goal_proposal` | Goal sign-off | Approve (with editable condition/verify textareas) / Cancel | `goal_proposal_response` (event) or `ConfirmGoal`/`CancelGoal` RPC — both funnel through one resolver |
| `goal_suggestion` | Goal-mode offer — the post-routing gate judged a fresh task obviously goal-suited and blocks before any work starts | Accept / Decline card (`GoalSuggestionPanel`) | `goal_suggestion_response` (event) or `AcceptGoalSuggestion`/`DeclineGoalSuggestion` RPC — both funnel through one resolver; the UI answers through the RPC path (`api/goal.ts`) |
| `plan_review_ready` | Plan review     | Approve/Request-Changes/Abandon + feedback | `plan_approval_response` event |

Pending actions are stored in chatStore and rendered by the PendingActionsBar component.

### Prompt Suggestions Sync

`prompt_suggestions` is consumed by `usePromptSuggestionsSync`, mounted by the chat-input surface with the active `(sessionId, projectId)` — not by `useSessionEvents`. It owns every refetch trigger of `promptSuggestionsStore`:

| Trigger | Mechanism |
| ------- | --------- |
| Session switch / session creation | `load` effect refires on the `sessionId` change (a creating flow selects the new session, so creation needs no separate trigger) |
| Project switch | `load` effect refires on the `projectId` change (a null id is the No-Project/CHAT window, normalized to `''`) |
| `prompt_suggestions` event | The scoped subscription validates the payload (`isPromptSuggestionsData`; malformed → `reportDroppedEvent`, no action) and calls `store.refetch()` |

The event handler **refetches rather than applying the payload**: the event carries the session's FULL active set (both kinds), while `GetPromptSuggestions` re-routes by the session's current content and returns exactly the kind the context serves — the backend stays the single routing authority. The refetch reuses the `(sessionId, projectId)` context the load effect published, so a stale event from a just-left session can never load the wrong context. Store semantics (switch skeleton, sequence guard, fail-open degradation, selection clamping): [stores.md](stores.md) § promptSuggestionsStore.

## Error Handling

- Invalid payload (type guard fails) → the event is dropped and reported through `reportDroppedEvent` (`api/runtime.ts`), an unconditional `logger.warn` naming the event and carrying the payload — a malformed event never disappears silently
- Event for wrong session → ignored (sessionId mismatch)
- Store update throws → caught by error boundary, logged

## Invariants

- Event handlers are pure functions (testable without React rendering)
- Type guards validate at ingestion point — downstream code is fully typed
- Only one subscription per session at a time (previous unsubscribed on change)
- Streaming state is per-session (multiple sessions don't interfere)
- Pending actions expire on task completion/cancellation
- Native file-drop paths originate only from the validated Wails `files:dropped` event; HTML5 drag events control overlay state and suppress navigation
- Picker and native drop share one attachment-staging/vision-filtering hook
- The per-session `paused` flag is set by `session_paused` and cleared by `session_resumed`/`task_resumed`/`task_cancelled`; it is also restored from the backend's persisted paused status on session load (the switch-time fast restore in `useTaskFlagRestore` plus ChatArea's `reconcileRuntimeStatus`), so a persisted-paused session shows Resume + Stop right after a switch/reload. The chat toolbar additionally resolves the paused state from the session's EFFECTIVE unfinished-task status (the same DB-snapshot fallback the sidebar dot / radar use), so it agrees with them even before/without the live flag. A paused session unlocks the input and shows Resume + Stop (see [rendering.md](rendering.md))

## Related Specs

- [stores.md](stores.md) — store structure for event-driven updates
- [rendering.md](rendering.md) — how updated store state renders
- [../../contracts/event-catalog.md](../../contracts/event-catalog.md) — complete event reference
