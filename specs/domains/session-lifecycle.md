# Session Lifecycle

## Purpose

Manages the lifecycle of user sessions: creation, message handling, task execution, persistence, and resumption after failure.

## Key Files

- `backend/session/manager.go` — SessionManager (session CRUD, message routing, plan review state)
- `backend/session/manager_execution.go` — SendMessage (incl. the live-send branch + `ErrPausePending` pausing-window rejection), CancelTask execution flows
- `core/tools/declare_plan.go` — `declare_plan` tool (`present` / `await_approval` modes); under ADR-012 plan review is a Conductor-invoked tool, not a pipeline stage
- `backend/events.go` — `EventPlanApprovalResponse` constant (frontend → backend plan-approval decision event)
- `backend/config/paths.go` — centralized path functions (single source of truth for ~/.c0wrk/ directory structure)
- `github.com/v0lka/sp4rk/orchestration/step_dump_tracker.go` — StepDumpTracker (per-step LLM dump file management)
- `backend/session/file_coherence.go` — FileCoherenceTracker (cross-session conflict detection)
- `backend/session/persistence.go` — SessionStore (SQLite persistence including plan review state)
- `backend/session/persistence_fork.go` — `(*SQLiteSessionStore).ForkSession` deep-copy (messages, tasks+steps/facts/attachments/trajectory, terminal commands, work directories) with regenerated identifiers in a single atomic transaction
- `backend/session/events.go` — event data structs (session lifecycle + plan review)
- `backend/session/emitter.go` — EventEmitter (fans out to the Wails UI and persistence through the combined emitFunc built at Application init)
- `backend/session/event_persister.go` — EventPersister (persists events to SQLite)
- `backend/session/task_adapter.go` — task step/fact/attachment adapter for persistence
- `backend/session/manager_attachment.go` — file attachments: `AttachFiles`/`RemovePendingAttachment`/`GetSessionAttachments`, pending-attachment staging (documents + images), `attachments:changed` emission, image metadata persistence/restore
- `backend/session/image_processor.go` — image decode/resize/re-encode/thumbnail (png/jpeg/gif/webp via stdlib + `golang.org/x/image/webp`)
- `backend/session/clipboard.go` — cross-platform clipboard probe precedence (image → file URLs → text) and attachment staging
- `frontend/src/hooks/useFileDrop.ts` — native `files:dropped` subscription, drag overlay, and webview-navigation suppression
- `frontend/src/hooks/useStageAttachments.ts` — shared picker/drop attachment staging with vision filtering
- `backend/frontend_api_attachment.go` — FrontendAPI attachment RPC surface
- `core/markitdown/converter.go` — markdown conversion of attached files (shells out to the managed `markitdown` CLI, ADR-010); `core/markitdown/vision.go` + `driver.go` — optional vision-assisted conversion via the markitdown Python API (embedded stdlib-only OpenAI-compatible client; the CLI exposes no LLM flags), per-document `VisionOptions` resolved from the currently active model. markitdown 0.1.4 captions only pptx images internally, so the driver adds two passes: PDF-embedded-image extraction (pdfminer XObject walk + Pillow decode) appended as an `## Embedded images` section, and data-URI replacement for docx/html/epub (converts with `keep_data_uris=True`, captions each blob, strips the base64 from the output); `core/visionresolver.go` — model/provider → vision-params mapping (vision capability gate via `ModelRegistry.ResolveLocal`; anthropic_compatible proxies excluded — no OpenAI-compatible surface)
- `core/tools/read_file_doc.go` — read_file document wrapper; vision resolver attached to the task context by the Orchestrator, conversion cache key includes the vision identity
- `backend/session/title.go` — auto title generation via LLM
- `backend/frontend_api_session.go` — FrontendAPI session methods
- `backend/frontend_api_project.go` — project switch state persistence + destination session fallback
- `core/orchestrator.go` — Orchestrator.HandleMessage, Orchestrator.Resume, `installPauseSignal`/`PauseSession`/`newPauseChecker` (universal pause signal); live user-message queue (`QueueLiveUserMessage`/`DrainLiveUserMessages`/`TakeLiveUserMessages`/`DiscardLiveUserMessages`, wired into every conductor run via `ConductorConfig.UserMessageSource`)
- `backend/session/manager_execution.go` — `ErrPausePending`, `finishLiveLeftover` (follow-up task for undelivered live messages), `sendMessage(presented)` wrapper, `session.pausing` lifecycle
- `backend/session/manager_compaction.go` — manual context compaction flow (`CompactSessionContext`/`CancelSessionCompaction`, `ErrSessionCompacting`, `ErrCompactionInFlight`, compaction_started/compaction_finished events, context_compaction marker persistence + restore via `convertChatMessagesToLLM`) — see [memory/compaction.md](memory/compaction.md) § Manual Context Compaction
- `backend/session/manager_live_send_test.go` — live-send tests (queue-into-running-task, pausing-window rejection, goal/skill rejection, pausing-flag lifecycle)
- `frontend/src/lib/chatInputLock.ts` — pure input-lock matrix helpers (`computeChatInputDisabled`, `computeChatPlaceholder`) for the live-send affordance
- `core/orchestrator_goal.go` — goal mode (runGoalLoop, resumeGoalLoop, runGoalTurns, goalLoopResult mid-turn-pause mapping); see [goal-mode.md](goal-mode.md)
- `backend/session/manager_execution.go` — `PauseSession` (delegates to `Orchestrator.PauseSession`), `ResumeSession` (delegates to `ResumeTask`), `hasPausedUnfinishedTask` (nudge-resume router), `SessionRuntimeStatus.Paused`
- `backend/session/manager_goal.go` — SetGoalProposalResolver, ResolveGoalProposal
- `backend/frontend_api_session.go` — FrontendAPI.PauseSession/ResumeSession (session-level pause/resume RPC surface)
- `backend/frontend_api_goal.go` — FrontendAPI.ConfirmGoal/CancelGoal (goal RPC surface)
- `core/toolnames.go` — NoProjectDisabledTools, NoProjectShellBlacklist constants
- `backend/project/manager.go` — EnsureNoProject (pseudo-project lifecycle)

## Flow

### Session Creation

```
User clicks "New Chat" (or first message in empty state)
  → Frontend: CreateSession()
  → Backend: FrontendAPI.CreateSession()
      ├─ Read active project ID + workspace path
      ├─ Call SessionManager.CreateSession(projectID, workspacePath)
      │   ├─ No Project (__no_project__): creates per-session workspace
      │   │   under ~/.c0wrk/projects/__no_project__/<id>/workspace
      │   ├─ Creates orchestrator via factory
      │   └─ No Project: calls orchestrator.SetNoProjectMode() to
      │       disable code tools and add bash command blacklist
      ├─ Persist to SQLite (sessions table; best-effort when store wired)
      └─ Return SessionInfo {id, name, projectId, createdAt}
  → Frontend: sessionStore.addSession()
```

### Project Switch Session Restoration

```
User switches project
  → Frontend hook: useProjectSwitchState(nextProjectId)
      ├─ Save source project UI state (best-effort):
      │   SaveProjectSwitchState({project_id, saved_session_id, open_tabs, active_file})
      ├─ SwitchProject(nextProjectId)
      ├─ Reset session store for destination project
      ├─ GetProjectSwitchState(nextProjectId)
      ├─ Restore open tabs + active file in fileViewerStore
      └─ Resolve active session deterministically:
          1) saved_session_id when it belongs to destination project
          2) latest destination session by activity timestamp
          3) create new session for empty destination project

Backend SwitchProject path
  → persistCurrentProjectSwitchState(previousProjectID) (normalize/validate persisted source state)
  → applySavedProjectSwitchState(destinationProjectID)
      ├─ resolveSavedSessionForProject(projectID, savedSessionID)
      ├─ fallback to resolveLatestSessionForProject(projectID)
      └─ fallback to createSessionForProject(projectID)
  → Persist resolved saved_session_id in project_ui_state
```

### Message Handling

```
User sends message
  → Frontend: SendMessage(sessionId, text, activeSkills, activeAgents, modelOverride, reasoningEffort, goal, goalBudget, reviewMode)
  → Backend: FrontendAPI.SendMessage()
      ├─ Live-send gate: validate pause window / goal / skill-agent refs
      │   BEFORE persisting (a rejected send never reaches the store)
      ├─ Preprocess text for orchestrator:
      │   ├─ Strip /skill references from text
      │   └─ Convert @file references to fileref:// URIs (relative paths resolved to absolute against the session workspace)
      ├─ Get or create Orchestrator for session (via factory)
      ├─ Create emitter (EventEmitter over the combined emitFunc: UI + EventPersister)
      ├─ Enrich task context:
      │   ├─ WithWorkspacePath (project workspace)
      │   ├─ WithTempDir (session-specific temp directory)
      │   └─ WithCoherence (FileCoherenceTracker for cross-session conflict detection)
      ├─ Determine opts: {TaskID, UserSkills, UserAgents, ModelOverride, ReasoningEffort, Goal, GoalBudgetOverride, ReviewMode}
      │   ├─ First message: TaskID=""
      │   └─ Continuation: TaskID=lastCompletedTaskID
      ├─ Call orchestrator.HandleMessage(ctx, preprocessedText, sessionId, opts)
      │   (executes asynchronously, events stream to frontend;
      │    prepareRequestContext — and Resume — attach the markitdown vision
      │    resolver to the task context, inherited by subagent delegations,
      │    so document conversions inside the task caption embedded images
      │    with the model active at conversion time)
      ├─ After dispatch: persist original text to DB (preserves /skill and
      │   @file refs) with the authoritative classification — is_nudge: true
      │   for live interjections and nudge-resumes, absent for fresh tasks
      ├─ On success: persist result, emit task_complete
      └─ On failure: emit task_failed_resumable or error
```

> **Per-continuation parameters.** `goal`, `modelOverride`, `reasoningEffort`,
> and `pendingAttachments` are **per-message** inputs: they are taken from the
> *current* send, not persisted from the prior task. A continuation message can
> therefore flip goal mode on (`/goal` prefix or the goal toggle), switch the
> model or reasoning effort, and stage a fresh set of attachments — all are
> applied to the continuation pass and override whatever the prior task used.
> `TaskID`, `UserSkills`, `SessionPlansDir`, `goalBudget`, and `reviewMode`
> behave the same way (per-message). Only the restored blackboard state (facts,
> plan/trajectory, conversation history, routing decision) is *inherited* from
> the prior task; the per-message parameters above are applied on top of it.

> If the session has an **unfinished (interrupted) task** *and the message is
> not a goal request*, the execution goroutine takes the
> `tryContinueInterruptedTask` branch *before* `HandleMessage` and resumes the
> prior ReAct cycle instead of routing a new task. A goal request (`/goal`
> prefix or the goal toggle) supersedes this branch — the interrupted task is
> abandoned and the goal loop runs instead (see [Continuing an interrupted task with a new message](#continuing-an-interrupted-task-with-a-new-message)).

### File Attachments

The user attaches files which are made available to the agent. There are two attachment **kinds**, routed by file extension and kept on separate staging lists:

- **Documents** (pdf, docx, pptx, xlsx, odt, html, htm, md, txt, …) — converted to markdown via `core/markitdown`, staged on `session.pendingAttachments`, and flushed into the blackboard as read-only context (the `read_attachment` tool reads their content).
- **Images** (png, jpg, jpeg, gif, webp) — decoded (stdlib png/jpeg/gif + `golang.org/x/image/webp`), optionally downscaled to a 1568px long edge and re-encoded as JPEG (quality 90) when they exceed 5 MB or 8000×8000px, and staged on `session.pendingImageAttachments`. They are passed to the LLM as image content blocks, **not** through the blackboard (the blackboard is markdown/text-only). A 64px JPEG thumbnail (quality 70, data URI) is generated for UI chips.

Both kinds have the same two-phase lifecycle (pending → committed); see [memory/blackboard.md](memory/blackboard.md) for the document/blackboard side.

```
User clicks Attach (Paperclip) in the chat input toolbar
  → Frontend: pickAttachmentFiles() → native multi-select picker (App.PickAttachmentFiles,
      Wails context, two filters: "Supported documents" + "Images" png/jpg/jpeg/gif/webp)
  → Frontend vision gating (useAttachmentsInput): resolve the effective model's
      capability (ModelInfo.vision). When the model lacks vision, image files are
      filtered out before staging and an error banner is shown (attachmentsStore.imageError);
      documents stage normally regardless of model capability.
  → Frontend: attachFiles(sessionId, paths) → RPC AttachFiles
  → Backend: Manager.AttachFiles(sessionID, paths) (manager_attachment.go)
      ├─ getOrRestoreSession (lazy restore)
      ├─ per file (routed by extension):
      │   ├─ IMAGE (png/jpg/jpeg/gif/webp):
      │   │     ├─ processImage: decode → optional resize/re-encode → base64 + thumbnail data URI
      │   │     ├─ write processed copy to config.SessionImagesDir(...)/<uuid>.<ext> (on-disk source of truth)
      │   │     └─ append ImageAttachment to session.pendingImageAttachments (guarded by mu)
      │   │         emit attachments:changed (incremental — chips appear one-by-one)
      │   └─ DOCUMENT:
      │       ├─ markitdown.IsSupported? no → record AttachmentFailure, skip
      │       ├─ converterOrInit() — lazily create core/markitdown.Converter
      │       │     (2min/file timeout; a single ≥5min budget covers the vision attempt
      │       │      AND its plain fallback; managed venv python path
      │       │      enables vision-assisted conversion)
      │       ├─ resolveSessionVision(session) — PER DOCUMENT: vision params for the
      │       │     model active on the session's router RIGHT NOW, or nil
      │       │     (non-vision model / unsupported endpoint / no orchestrator)
      │       ├─ os.Stat; converter.ConvertWithVision(ctx, path, vision) → markdown
      │       │     (nil/incomplete vision or driver failure degrades to plain CLI)
      │       └─ append orchestration.Attachment to session.pendingAttachments (guarded by mu)
      │           emit attachments:changed (incremental — chips appear one-by-one)
      └─ if any failures: emit attachments:changed {failed: [...]} (UI toasts names)

User removes a chip:
  → Frontend: removeAttachment(sessionId, id) → RPC RemoveAttachment
  → Backend: Manager.RemovePendingAttachment — removes from pending (document or image) only
      (committed attachments untouched). Removing a pending image also deletes its on-disk copy.

SendMessage flushes pending attachments:
  → Manager.SendMessage snapshots session.pendingAttachments + session.pendingImageAttachments,
      clears both (flushed exactly once)
  → emits attachments:changed {attachments: []} so chips clear
  → passes PendingAttachments via HandleOptions → Orchestrator.setupBlackboard flushes the documents
      into the blackboard (bb.AddAttachment) in both fresh and restored paths
  → passes PendingImages via HandleOptions (as []llm.ContentBlock image blocks) → injected into the
      context window as image content (NOT the blackboard)
```

`AttachFiles` returns a non-nil `error` only for system-level failures (session not found). File-level failures (unsupported format, conversion/decode error, inaccessible file) are reported via the `attachments:changed` event payload's `failed` field — not as an error — so a partial success never discards the successfully attached files or triggers a generic error toast. Converted markdown content never reaches the UI: `AttachmentInfo` is metadata-only (image entries additionally carry `is_image: true` and a `thumbnail` data URI); the agent reads document content via the `read_attachment` tool, and image content reaches the LLM only as a content block.

Image attachments survive a backend restart. Before `SendMessage` snapshots and clears the pending image list, the frontend API persists a compact metadata blob (`StoredImagesMetadata`, thumbnail data URI + on-disk path — never the full base64) into `ChatMessage.Metadata`. On lazy session restore the image files are read from disk and re-encoded into `ContentBlock`s, matching what the live session saw.

#### Vision-assisted Document Conversion — Data Egress

When vision assistance is active (managed venv installed + the currently active model is vision-capable + provider credentials present), document conversion does not just produce local markdown: images **embedded in the document** are base64-encoded and sent to the active LLM provider's endpoint for captioning. Coverage per format (markitdown 0.1.4 captions only pptx internally, so the driver adds two post-processing passes):

- **pptx** — pictures captioned by markitdown's own `llm_caption` integration.
- **pdf** — markitdown's PDF converter is pure pdfminer text extraction and drops images entirely; the driver walks page XObjects itself (pdfminer + Pillow, both venv-resident; DCTDecode/JPXDecode pass-through, FlateDecode raw-sample reconstruction for DeviceGray/RGB/CMYK and ICCBased at 8bpc) and appends an `## Embedded images` section with one caption per unique image (content-hash deduplication).
- **docx / html / epub** — the conversion runs with `keep_data_uris=True` so embedded images survive as markdown data-URI images; the driver replaces each base64 blob in place with `![caption](embedded-image-N)`, so the captioned description reaches the context AND the raw base64 payload never does (without vision, markitdown truncates data URIs to inert stubs).

Two things to note about this egress:

- It applies to **both** conversion paths: user-staged attachments (`AttachFiles`) and the agent-driven `read_file` document wrapper. In the `read_file` case the user attached nothing — an agent merely reading a sensitive PDF implicitly sends that document's embedded images to the provider.
- With a **cloud** provider this is third-party egress of image content the user may not think of as "uploaded"; with a **local** model (LM Studio etc.) the captioning traffic stays on the machine. The connection honors the configured proxy. There is deliberately **no separate config toggle**: gating follows the active model's vision capability, exactly like explicit image attachments (which the user does choose). Disabling vision assistance wholesale is possible by pointing the session at a non-vision model.

Cost and abuse bounds, shared across markitdown's internal captioning and both driver passes: at most **12 captioning LLM calls per document** (images beyond the budget are listed without descriptions; a note is appended when the limit is hit), images smaller than 32px in either dimension are ignored as decorations, and every image is normalized (RGB JPEG, longest side ≤ 2048px) before upload.

Failure containment: captioning failures are swallowed per image (markitdown internally, and by the driver's per-image guards — diagnostics go to stderr and the Go-side debug log), each driver pass is individually exception-guarded (a pass failure leaves the base markdown intact), a wholesale driver failure degrades to plain CLI conversion, and the vision attempt plus its fallback share one elevated per-file deadline — a hung vision endpoint can never make conversion worse (or slower) than the plain path bounded by a single budget.

#### Clipboard and Native File Drop

Clipboard paste is resolved by `PasteFromClipboard(sessionID, supportsVision)` in strict precedence order: image, copied file URLs, then plain text. A failed platform probe is logged and treated as absent so the next representation can be tried. An image present at the highest-priority representation returns an image result (or the explicit non-vision error) rather than falling through to lower-priority file/text representations. Copied files route through the same `AttachFiles` processing as the picker; plain text is returned to the editor without creating an attachment.

Native file drop is the sole path-delivery mechanism for drag-and-drop. Wails emits the global `files:dropped {paths, x, y}` event while webview-native file navigation is disabled. In chat input mode, `useFileDrop` validates the event and sends its paths through `useStageAttachments`, the same staging/vision-filtering pipeline used by the picker. Document files stage regardless of model capability; image paths are rejected before backend staging when the effective model lacks vision. HTML5 drag events only drive the overlay and call `preventDefault`; their `dataTransfer` is never trusted as the filesystem-path source.

### Plan Review

Plan review is **not** a pipeline stage — under ADR-012 (Conductor pipeline) it is a tool call (`declare_plan`, `core/tools/declare_plan.go`) invoked by the Conductor at a point of its own choosing. The tool has two modes:

- `mode: "present"` (default) — the plan is published to the plan panel/blackboard and execution continues immediately.
- `mode: "await_approval"` — the tool **blocks** until the user approves, requests changes, or abandons. The Conductor decides when (and whether) to gate on approval; it is not driven by a `HandleOptions` flag.

```
Conductor calls declare_plan(tasks, mode="await_approval")
  → core/tools/declare_plan.go Execute()
      ├─ Publish the plan via the context's PlanPublisher
      ├─ mode != await_approval? → return immediately (present)
      └─ ApprovalFunc(ctx, planPath, planMarkdown)  (cfg.PlanApprovalFunc, wired in desktop/startup.go)
          ├─ Register a pending plan-approval entry (request_id → response channel)
          ├─ Emit plan_review_ready {request_id, plan_path, plan_content}
          │   (session-scoped event; persisted via EmitSessionEvent so it reappears on reload)
          └─ BLOCK on the channel until the user responds

User responds (frontend emits plan_approval_response):
  → desktop/event_handlers.go handlePlanApprovalResponse
      ├─ decision: "approve"     → tool returns; Conductor continues to implementation
      ├─ decision: "request_changes" → tool returns the feedback; Conductor revises
      │   the plan and calls declare_plan again (loop until approve/abandon)
      └─ decision: "abandon"     → tool returns; Conductor abandons the plan
```

There is **no dedicated plan-review RPC** (no `ApprovePlan`/`RejectPlan`): the decision flows back through the `plan_approval_response` event (`backend/events.go` `EventPlanApprovalResponse`) into the pending-approval resolver on the desktop `App`. Plan files are written to the session-scoped plans dir (`~/.c0wrk/projects/<pid>/<sid>/plans/`); previous plan files are not deleted on a revise/abandon — they remain as history.

### Task Resumption

Resume reconstructs the full prior ReAct trajectory from the task store and
re-enters the Conductor at that checkpoint. **A plan and a routing decision are
no longer required** — a persisted routing decision is reused if available
(the task is never re-routed), and when none was persisted the Conductor runs
in the default `general` domain. A plan-less task is handled by the Conductor's
standalone checklist.

```
User clicks "Resume" (after task_failed_resumable)
  → Frontend: ResumeTask(sessionId, modelOverride, reasoningEffort)
  → Backend: FrontendAPI.ResumeTask(id, modelOverride, reasoningEffort)
      ├─ Resolve unfinished task via TaskStoreAdapter.GetUnfinishedTaskID
      ├─ Restore Blackboard from SQLite (RestoreBlackboard: facts + step results)
      ├─ Load persisted trajectory via LoadTrajectory(taskID) → resumeSteps
      ├─ Load persisted goal state via LoadGoalState(taskID) → goalState (nil for non-goal tasks)
      ├─ Resolve routing decision (OPTIONAL — may be nil; defaults to "general")
      ├─ Emit task_resumed (resolves the resumable banner; sets UI active)
      └─ Call orchestrator.Resume(ctx, bb, routing, plansDir, resumeSteps, goalState)
          ├─ IF goalState != nil && !goalState.Status.IsTerminal():
          │   └─ resumeGoalLoop — re-activates a paused goal to `active`, seeds
          │       resumeSteps into the first resumed turn, and continues the
          │       multi-turn goal loop (turn counter continues from goalState.TurnCount).
          │       See [goal-mode.md](#goal-resume) below.
          ├─ Seeds resumeSteps into the ContextManager (StepSeedable.SeedSteps)
          │   so they render as assistant+tool messages in BuildPrompt
          ├─ Seeds resumeSteps into the Executor (WithResumeSteps) so the step
          │   counter continues from len(resumeSteps)+1 and the full trajectory
          │   syncs to the TrajectoryStore (persisted on every Sync)
          └─ Conductor continues toward completion (no plan required)
```

#### Goal resume

A task that was running a goal loop when it was paused (or interrupted) is
re-entered into the goal loop rather than the plain Conductor path. This
applies to the user-driven Pause/Resume flow (`FrontendAPI.ResumeSession` →
`Manager.ResumeSession` → `ResumeTask`) and to the app-restart recovery path (the
persisted non-terminal `GoalState` is loaded and passed to
`orchestrator.Resume`).

- `Orchestrator.Resume` guards on `goalState != nil && !goalState.Status.IsTerminal()`: a terminal goal (`met`/`exhausted`/`cancelled`) falls through to the normal resume path and is never re-entered.
- A cooperative session-pause leaves the goal `active` (pause is task-level), so on resume the turn loop's `for gs.Status == active` guard enters directly; a `blocked_idle` goal is re-activated to `active` first. The prior trajectory is seeded into the first resumed turn only (subsequent turns rely on the Conductor's accumulated trajectory).
- The universal pause signal is installed fresh for the resumed request (`installPauseSignal`) and cleared on exit, so a stale signal from the prior run cannot affect a future request.

See [goal-mode.md](goal-mode.md) for the full goal-mode lifecycle, budgets, anti-spin, and the [Pause is Session-Level](goal-mode.md#pause-is-session-level-universal-pause-signal) section.

`recordResumeOutcome` appends **only the assistant side** of the resumed
execution to the in-memory conversation history — no user/assistant pair is
recorded (the user message that spawned the task was already recorded when the
task first ran, or restored from the message store after a restart).

Under ADR-012 the router's `needs_clarification` flag is ignored
(`Router.NeedsClarification` is read only for logging in
`core/orchestrator_handle.go`): the Conductor handles clarification itself via
the `ask_user` tool during execution. A clarification never short-circuits the
pipeline, so there is no router-driven clarification branch to resume.

#### Continuing an interrupted task with a new message

Sending a message to a session that has an **unfinished (interrupted) task**
does NOT start a new task: `Manager.tryContinueInterruptedTask` appends the
user message as a final **user-nudge** turn to the prior trajectory and resumes
the same ReAct cycle via `orchestrator.Resume` — **no routing, no new task, no
new conversation-history pair**. The nudge renders as a `{role:user}` message
positioned after the prior steps, so the agent sees the new instruction
immediately. Idle sessions (no unfinished task) behave exactly as before
(route → plan → execute).

> **Per-continuation parameters are applied on this path too.** Because the
> resume path bypasses `HandleMessage`, the model/reasoning override and
> staged attachments from the current send are applied **explicitly** inside
> `tryContinueInterruptedTask`: after the blackboard is restored it calls
> `orchestrator.ApplyRequestOverrides(ctx, modelOverride, reasoningEffort)`
> (the same step 0 `HandleMessage` runs) and flushes the snapshot of
> `pendingAttachments` into the restored blackboard via `bb.AddAttachment`.
> Both calls are no-ops when their arguments are empty. The `pendingAttachments`
> snapshot is taken (and `session.pendingAttachments` cleared) **before** the
> continue-check so both the resume path and the fresh-task path consume the
> staged attachments exactly once.

> **Goal mode supersedes the resume path.** The goal flag is detected
> (`/goal` prefix OR the goal toggle) **before** the resume check. When goal
> mode is requested, the resume path is **skipped** and `abandonUnfinishedTaskForGoal`
> cancels the interrupted task (persisting the cancellation, resolving the
> `task_failed_resumable` banner, and emitting a service event) so it does not
> linger as resumable WIP; the goal loop then runs on the last **completed**
> task's blackboard (`lastTaskID`) — or fresh when there is none. In short: a
> goal request always wins over an interrupted task, discarding the WIP rather
> than silently resuming it as a non-goal task.

```
User sends a NON-GOAL message to session with unfinished task
  (a goal request would abandon the task via abandonUnfinishedTaskForGoal
   and run the goal loop instead — see "Goal mode supersedes the resume path" above)
  → Frontend: SendMessage(...) — optimistically sets task active
  → Backend: FrontendAPI.SendMessage() (goroutine)
      └─ tryContinueInterruptedTask(ctx, id, session, message, modelOverride, reasoningEffort, pendingAttachments)
          ├─ Snapshot pendingAttachments; clear session.pendingAttachments; emit attachments:changed
          ├─ Resolve unfinished task via TaskStoreAdapter.GetUnfinishedTaskID
          ├─ Restore Blackboard (facts + step results)
          ├─ ApplyRequestOverrides(ctx, modelOverride, reasoningEffort)
          ├─ for each staged attachment: bb.AddAttachment (persists to store)
          ├─ Load trajectory → append agent.Step{UserNudge: message}
          ├─ Resolve routing decision (OPTIONAL)
          ├─ Resolve the resumable banner (so it does not linger)
          └─ orchestrator.Resume(ctx, bb, routing, plansDir, resumeSteps)
              (task ID preserved; lifecycle events stream normally)
```

### Discarding an Unfinished Task

```
User clicks "Cancel" on the resume prompt
  → Frontend: CancelUnfinishedTask(sessionId)
  → Backend: FrontendAPI.CancelUnfinishedTask()
      └─ TaskStoreAdapter.PersistCancellation(taskID)
          (marks the unfinished task as cancelled; no further resume prompt)
```

### Task Cancellation

```
User clicks "Cancel"
  → Frontend: CancelTask(sessionId)
  → Backend: FrontendAPI.CancelTask()
      └─ Cancel context → executor stops at next iteration
          → emit task_cancelled
```

### Session Pause / Resume / Nudge

Pause/resume is a **session-level** control that applies uniformly to **all** tasks — goal and non-goal alike. There is no goal-specific pause; the universal pause signal (`Orchestrator.activePause`) is read by every conductor run's pause-checker at each step boundary. See [goal-mode.md § Pause is Session-Level](goal-mode.md#pause-is-session-level-universal-pause-signal).

**Pause (cooperative, mid-turn):**

```
User clicks "Pause"
  → Frontend: pauseSession(sessionId); sets ONLY the `pausing` in-flight flag
      (input locks for the window, activity label reads "Pausing")
  → Backend: FrontendAPI.PauseSession()
      └─ Manager.PauseSession() — sets session.pausing (under session.mu,
          only when a task is active) + Orchestrator.PauseSession() flips
          the active pause signal
      In-flight conductor run observes the signal at the next step boundary
        → executor returns ErrPaused → Conductor maps to ExecutionStatusPaused
        → persistTaskOutcome: pbb.PauseTask() (task persisted as "paused")
        → emit session_paused (UI unlocks input; shows Resume + Stop)
        → request epilogue clears session.pausing; the input re-opens —
          sends now become nudge-resumes
        → request exits, releasing the single-flight lock
```

A cooperative pause is a **clean checkpoint, not a degraded completion** — the trajectory was already flushed, so the checkpoint is live. For a goal task, `runGoalTurns` breaks out of the loop and `goalLoopResult` maps the paused turn to `ExecutionStatusPaused` (the **goal stays `active`** — the pause is task-level).

**Resume:**

```
User clicks "Resume" (optionally with model/reasoning-effort overrides)
  → Frontend: resumeSession(sessionId, modelOverride, reasoningEffort, nudge="")
  → Backend: FrontendAPI.ResumeSession()
      └─ Manager.ResumeSession() → ResumeTask()
          └─ loads unfinished task + persisted state (trajectory, goal state)
          └─ emit task_resumed + session_resumed (UI re-locks input; shows Pause + Stop)
          └─ orchestrator.Resume → resumeGoalLoop (goal task) or plain Conductor
```

`ResumeSession(sessionID, modelOverride, reasoningEffort, nudge)` delegates to `ResumeTask`. The optional `nudge` is injected as a trailing user message into the first resumed turn (one-shot). An empty nudge resumes silently from the checkpoint. `session_resumed` clears the UI's paused state (complementary to `session_paused`).

**Nudge-resume (sending a message into a paused session):**

```
User types a message while paused → Send
  → Frontend: marks the optimistic user message is_nudge=true; clears paused
  → Backend: SendMessage detects hasPausedUnfinishedTask(sessionID) == true
      (and not a goal message) → routes to ResumeSession(text, ...)
        → the user's text becomes the nudge, injected as a trailing user message
          into the first resumed turn
```

The nudge-resume path is how a user "steers" a paused agent: rather than starting a fresh task, the message resumes the paused one with the user's new input appended next to the pending tool result in the very first resumed LLM call. The nudge renders as a normal user message with a "Nudge" badge (see [frontend/rendering.md](frontend/rendering.md)).

**Live-send (sending a message while a task is running):**

A message sent while a task is already executing does NOT pause the task and does NOT wait for completion — it is queued into the running request and delivered to the LLM in the very next request, exactly as a resume-with-nudge would land it.

```
User types a message while a task runs → Send
  → Frontend: optimistic user message with is_nudge=true; the input stays
      open while a task runs (see the input-lock matrix below)
  → Backend: FrontendAPI.SendMessage — live checks before persisting:
      ├─ HasPendingAttachments → reject (attachments are task-start-only)
      └─ persists the message with is_nudge (interjection semantics)
  → Manager.SendMessage — live branch under session.mu (session.active):
      ├─ session.pausing → ErrPausePending (the pausing window)
      ├─ goal flag → reject (goal supersedes running work; needs idle)
      ├─ skills/agents refs → reject (they reshape task context at start)
      └─ otherwise → orchestrator.QueueLiveUserMessage(text);
          emit message_received; return nil (no task started)
  → Delivery: the running request's executor polls the queue at every step
      boundary (right after the pause check, before the LLM call):
        ├─ message → appended to the trajectory as a nudge-only step +
        │   pushed to the ContextManager → renders as the FINAL {role:user}
        │   message of the next LLM request, next to the pending tool result
        └─ one message per boundary (FIFO); the rest stay queued
```

Invariants and edge cases:

- **Pausing window**: between `PauseSession` (sets `session.pausing` + flips the signal under `session.mu`) and the request epilogue (clears `pausing`), live sends are rejected with `ErrPausePending`. The UI locks the input for the window (`pausing` flag); once `session_paused` lands, sends become nudge-resumes instead.
- **Cancel (Stop)**: the epilogue discards queued-but-undelivered messages (`DiscardLiveUserMessages`) — an undelivered message in a cancelled exchange does not leak into a future request.
- **Completion with leftovers**: when the task finishes successfully without delivering a queued message, the epilogue takes the leftovers atomically (`TakeLiveUserMessages` under the same lock that flipped `active=false`) and launches a follow-up continuation task carrying them (joined with `\n\n`). The message was already persisted/rendered at send time, so the follow-up re-enters the send path in "presented" mode (no duplicate `message_received`, no title regen).
- **Pause/resumable outcome**: leftovers stay queued; the resumed request drains them at its first step boundary.
- **Race-freedom**: queueing happens under `session.mu` while `active=true`; the epilogue's take happens under the same mutex as the `active=false` flip — a send racing the completion either joins the follow-up (queued before the flip) or starts a normal task (observed `active=false`), never both.
- **Scope**: live delivery applies to the session's main Conductor run only (normal path, resume, every goal-loop turn). Subagent executors never receive live messages.
- **Text-only**: attachments, `/goal` requests, and `/skill`/`#agent` references are rejected on the live path (they are task-start concerns); the user is asked to wait for pause/completion.

**Input-lock matrix (frontend)**: `computeChatInputDisabled` (pure helper, `lib/chatInputLock.ts`): input is disabled iff `compacting || pausing || isNoProject`. A running (`taskActive`) or paused session keeps the input open — running sends interject live; paused sends nudge-resume. While compacting the whole input area (editor, toolbar buttons, selector cluster, send/pause/resume) locks — with one exception: **Stop stays available** (CancelTask carries no compacting guard, and terminating the in-flight request is the one user action that helps the flow's pause-wait land; the Pause/Resume flank is hidden for the window because the flow owns the pause signal). The placeholder advertises the affordance ("your message joins the next request to the model").

**Runtime status after restart / session switch:** `GetSessionRuntimeStatus` reports `Paused: true` when the resumable unfinished task is in the `"paused"` status. The frontend reconciles this on session activation (`reconcileRuntimeStatus`): it sets the `paused` flag and clears `taskActive`, and crucially does **not** inject a `task_failed_resumable` banner — a paused task resumes via the Resume button or a nudge, not a "did not finish" banner.

### Manual Context Compaction

```
User picks a strategy in the status-bar compact menu (left of the fill indicator)
  → Frontend: compactSessionContext(sessionId, strategy) → RPC CompactSessionContext
  → Backend: Manager.CompactSessionContext
      ├─ Validate strategy (fail fast) / reject ErrCompactionInFlight
      ├─ Set session.compacting (sends/resumes now fail with ErrSessionCompacting)
      ├─ Emit compaction_started {strategy}
      ├─ If a task runs: pausing window + orch.PauseSession() (identical to PauseSession)
      │    then wait on session.done for the cooperative checkpoint
      ├─ orch.CompactConversationHistory(strategy) — rewrites o.conversationHistory
      │    (summarization runs through the session's tracking caller; last message kept)
      ├─ No-op (ErrNothingCompacted — dialogue already fits the limits): a SUCCESS
      │    with nothing_compacted=true and zero %; NO marker row. When a paused
      │    unfinished task waits for the auto-resume below, arm
      │    orch.RequestResumeCompaction(strategy) BEFORE resuming
      │    (deferred_to_resume=true) → the resumed run force-compacts the
      │    merged trajectory up front (CompactOnStart, ignores fill thresholds);
      │    the real numbers arrive as the executor's context_compaction card
      ├─ Persist marker row (compacted outcome only): role "context_compaction",
      │    metadata {strategy, before/after %, messages: compacted history snapshot}
      ├─ Clear compacting, then auto-resume the task this flow paused
      │    (ResumeTask — only when a paused checkpoint remains; a FAILED
      │    resume sets paused_without_resume so the UI re-applies the paused
      │    state — session_paused was suppressed while compacting)
      └─ Emit compaction_finished {strategy, success|cancelled|error, resumed,
                                  paused_without_resume?, nothing_compacted?,
                                  deferred_to_resume?}

Cancel (CancelSessionCompaction):
  during pause-wait  → still waits for the checkpoint (unflipping the pause signal
                       mid-flight would race the executor), then skips the
                       compaction and auto-resumes
  during compaction  → aborts the summarize calls; history stays untouched
```

- The UI chat history is untouched — only the LLM-visible conversation history shrinks; the marker row renders as the existing compaction card on reload.
- A no-op outcome never writes a marker (nothing was compacted to snapshot). With a paused task waiting, the no-op defers to the resume instead: the one-shot resume-compaction request is armed strictly before the auto-resume, so the resumed run force-compacts the merged trajectory (checkpoint steps + the resumed run) up front with the user-selected strategy, regardless of fill thresholds — see [memory/compaction.md](memory/compaction.md) § Resume-side Forced Trajectory Compaction. In the UI, `compaction_finished` with `nothing_compacted` and nothing to resume clears the "Compacting" activity at once (no card follows); `deferred_to_resume` keeps the label for the subsequent `task_resumed`, like `resumed`.
- History restore (`convertChatMessagesToLLM`): the LAST marker's `messages` snapshot seeds the restored history (conversational rows before it are dropped; later exchanges append on top).
- While compacting the UI shows the "Compacting" activity (where "Thinking" renders), locks the input area, and suppresses the `session_paused` paused affordances (the flow's own pause). `SessionRuntimeStatus.Compacting` reconciles this on session switch/restart.
- See [memory/compaction.md](memory/compaction.md) § Manual Context Compaction for the strategy semantics and the orchestrator-level behavior.

### Session Forking

A session can be deep-copied into an independent fork. The fork keeps the full
conversation and task history but starts with fresh runtime accounting; it
shares no rows with the original.

```
User clicks Fork (GitFork icon) in SessionSelector on a session item
  → Frontend: forkSession(id) → RPC ForkSession
      (button disabled while task active or when has_unfinished_task)
  → Backend: FrontendAPI.ForkSession(id)
      ├─ Guard: store.GetUnfinishedTask(id)
      │   └─ non-nil → return error "cannot fork a session with an unfinished task"
      ├─ Build optional ForkReviewCloner = reviewStore.CloneReviewTx
      │   (nil when no review store is wired)
      └─ store.ForkSession(ctx, id, cloneReview)  (single transaction)
          ├─ INSERT new sessions row: same project, name "<src> (fork N)"
          │   (N = highest existing fork number + 1), runtime counters reset
          ├─ Copy session_messages (autoincrement id; JSON tool_call correlation
          │   preserved verbatim — no id rewriting)
          ├─ Copy terminal_commands (autoincrement id)
          ├─ Copy session_work_directories (regenerated UUID PK)
          ├─ For each task: new task id, copy tasks (NULL completed_at preserved)
          │   + task_steps + task_facts + task_attachments + task_trajectory
          │   + task_goal_state (preserves the task's goal history)
          ├─ cloneReview(src, new) on the same tx (review_state + review_comments)
          └─ Commit (any error rolls back the whole fork; source untouched)
  → Frontend: sessionStore.addSession(forked) + setActiveSessionId(forked.id)
```

Forking is rejected when the source session has an unfinished
(`in_progress` or `failed`) task — copying would duplicate a half-completed
execution state. The guard runs on the backend (authoritative); the frontend
also disables the fork button preemptively via the session's
`has_unfinished_task` flag and the live active status.

### Per-Session Terminal Lifetime

The terminal manager owns at most one PTY per session ID. Switching the active session or project does not stop that PTY: the frontend keeps one xterm instance per session, and `StartTerminal(sessionID)` treats an already-active PTY as a successful reattach. Input, resize, output events, and command history remain session-keyed, so concurrent terminal sessions do not cross streams.

`StartTerminalInDir` is the explicit restart path: the requested working directory must be contained in the session workspace, then any existing PTY for that session is stopped and replaced. `StopTerminal` ends only the named session's PTY. Application shutdown calls the terminal manager's global stop through backend cleanup; terminal processes do not outlive the app.

### Session Persistence

Persisted in SQLite (`~/.c0wrk/database.db`) — schema defined in `backend/session/persistence.go` and `backend/project/persistence.go`:

- `projects` — project roster (in `backend/project/persistence.go`)
- `project_ui_state` — project_id, saved_session_id, open_tabs (JSON), active_file, updated_at; stores per-project switch UI restoration state
- `sessions` — id, project_id, name, created_at, last_active_at, archived, pinned, total_input_tokens, total_output_tokens, model, family, fill_percent
- `session_messages` — id, session_id, role, content, reasoning_content, tool_calls, metadata (JSON), created_at
- `tasks` — id, session_id, original_request, routing_decision (JSON), plan (JSON), reflections (JSON), final_output, attempt_count, status, created_at, completed_at
- `task_steps` — step_id, task_id, summary, full_output, error_text, steps (JSON), created_at (PRIMARY KEY (task_id, step_id))
- `task_facts` — task_id, facts (JSON), updated_at
- `task_attachments` — task_id, attachments (JSON-marshaled `[]orchestration.Attachment`), updated_at
- `task_trajectory` — task_id, steps (JSON), updated_at (persisted ReAct trajectory for resume)
- `task_goal_state` — task_id, goal_state (JSON), updated_at (goal-mode state for resume; see [goal-mode.md](goal-mode.md))
- `terminal_commands` — id, session_id, command, created_at
- `session_work_directories` — id, session_id, path, description, created_at (session-scoped additional work dirs; UNIQUE(session_id, path))

`SessionInfo.HasUnfinishedTask` is not a stored column — it is derived at query
time (`LoadSession`, `ListSessions`, `ListSessionsByProject`) via a correlated
`EXISTS` subquery over `tasks` with status `in_progress` or `failed`. The fork
guard (`ForkSession`) and the frontend fork-button disabled state consume it.

Blackboard state is reconstructed from `tasks` + `task_steps` + `task_facts` + `task_attachments` on resume (no dedicated `blackboard` column). Events are streamed via the Wails runtime and are NOT persisted to a standalone table — any event state that must survive restart is folded into `session_messages` or `tasks`/`task_steps` via `backend/session/event_persister.go`.

### SessionStore Interface

The `backend/session/persistence.go` defines the `SessionStore` interface:

| Method                                                       | Description                                                      |
| ------------------------------------------------------------ | ---------------------------------------------------------------- |
| `SaveSession(ctx, info)`                                     | Upsert session (INSERT OR REPLACE)                               |
| `LoadSession(ctx, id)`                                       | Load session by ID (returns nil if not found)                    |
| `ListSessions(ctx)`                                          | List all sessions ordered by last activity                       |
| `ListSessionsByProject(ctx, projectID)`                      | List sessions for a specific project                             |
| `DeleteSession(ctx, id)`                                     | Delete session and cascade messages                              |
| `ArchiveSession(ctx, id, archived)`                          | Set archived flag on session                                     |
| `PinSession(ctx, id, pinned)`                                | Set pinned flag on session                                       |
| `RenameSession(ctx, id, name)`                               | Update session name                                              |
| `UpdateSessionTokens(ctx, id, input, output, model, family, fillPercent)` | Update accumulated token counts, model info, and context-fill %  |
| `UpdateSessionActivity(ctx, id)`                             | Update last_active_at timestamp to now                           |
| `SaveMessage(ctx, msg)`                                      | Insert a new chat message                                        |
| `LoadMessages(ctx, sessionID)`                               | Load all messages for session (ordered by created_at)            |
| `DeleteMessages(ctx, sessionID)`                             | Delete all messages for session                                  |
| `ResolvePendingMessage(ctx, sessionID, role, matchField, matchValue, extra)` | Patch metadata of the most recent matching HITL message (tool_confirm/ask_user/step_limit/plan_review) as resolved so it doesn't reappear as pending on reload |
| `UpsertStepTodoUpdate(ctx, sessionID, stepID, msg)` | Replace/insert the persisted `step_todo_update` message for `stepID` (preserving id and created_at so the checklist keeps its stream position on reload); the Conductor emits one after every tool call, so upserting bounds `session_messages` growth |
| `SaveTerminalCommand(ctx, sessionID, command)`               | Save terminal command to history                                 |
| `LoadTerminalCommands(ctx, sessionID, limit)`                | Load most recent terminal commands                               |
| `SaveSessionWorkDir(ctx, sessionID, rec)`                    | Insert a session-scoped work directory record                    |
| `ListSessionWorkDirs(ctx, sessionID)`                        | List session-scoped work directories                             |
| `UpdateSessionWorkDirDescription(ctx, sessionID, id, description)` | Update a work directory's description                      |
| `DeleteSessionWorkDir(ctx, sessionID, id)`                   | Delete a session-scoped work directory                           |
| `Close()`                                                    | Close the store (no-op for SQLite, lifecycle managed externally) |

### Conversation History

The orchestrator maintains an in-memory conversation history (`conversationHistory`)
that accumulates one user/assistant pair per exchange, without truncation:

- Updated centrally for EVERY terminal outcome of `HandleMessage` (via a
  `recordConversationOutcome` defer): success, partial success, clarification,
  failure (`[Task failed before completion: …]` note), and cancellation
  (`[Task was cancelled before completion]` note). `Orchestrator.Resume`
  (interrupted-task resume) appends the assistant-side outcome the same way.
- When the assistant output contains tool-call syntax printed as text (failure-mode
  detected by `agent.DetectToolCallSyntaxInContent` — e.g. `` ```bash_exec ``
  typed as prose instead of a `tool_use` block), the history records a
  `[Task failed before completion: task ended in failure-mode: model printed
  tool-call syntax as text instead of using tool_use blocks]` note instead of
  the hallucinated text. This ensures future routing/planning sees an honest
  failure, not the stuck-model artifact.
- A failed attempt retried with the same message (continuation fallback)
  replaces the failed pair instead of duplicating the user message.
- History sent to Router for context-aware classification (last
  `router.history_window` messages, default 10).
- History sent to the planner: `PlanContinuation` and first-message `Plan`
  both receive the history (compacted to `PlannerHistoryBudgetTokens`).
- History sent to the Conductor: `HandleMessage` injects the last
  `ConductorHistoryWindow` messages (default 20) into the Conductor's
  ContextManager as prior conversation, so the LLM sees the dialogue context
  leading up to the current message. Without this, a follow-up like
  "implement variant a" has no referent. `Resume` does NOT inject history —
  the Conductor continues the same task and the original request is already
  the task message.

On lazy session restore (`getOrRestoreSession`), the history is reconstructed
from the message store via `convertChatMessagesToLLM` to match what the live
session saw: user rows are re-preprocessed (`@file` → `fileref://`),
consecutive assistant rows (per-step `assistant_done` + final `task_complete`)
collapse to the most recent one, and `error`/`task_cancelled` rows become the
same assistant notes the orchestrator records live. The continuation anchor
(`lastCompletedTaskID`) is restored from the task store (`GetLatestTaskID`) so
the next message takes the continuation path after a backend restart.

### Auto Title Generation

When the first message is received for a session with the default auto-generated name:

- Backend calls LLM to generate session title from user message
- Emits `session_renamed` event
- Frontend updates session list

### LLM Dump Files (DEBUG mode)

When the global log level is set to `DEBUG`, the session manager creates LLM dump
files for all LLM calls within the session:

```
~/.c0wrk/projects/<projectID>/<sessionID>/dumps/
├── session_<id>_llm_dump.jsonl       ← router, planner, reflector, title gen, ToolJudge
└── steps/
    ├── step_<stepID>.jsonl           ← executor step (initial + retries append to same file)
    └── step_planner-exploration.jsonl ← planner exploration sub-agent
```

Each `.jsonl` file contains full, untruncated request/response pairs for every LLM
call, serialized as `dumpEntry` records (`ts`, `direction`, `data`, `error`). No
sampling or truncation is applied.

The session-level file (`session_<id>_llm_dump.jsonl`) is written via `DumpCaller`
wrapping the main `llm.Router`. Per-step files are created lazily by
`StepDumpTracker` which manages file handles through the orchestrator lifecycle and
closes them all on session deletion or application shutdown.

Dump file creation is controlled by `Manager.logLevel`. When not `DEBUG`:
`dumpFile` and `stepDumpTracker` are nil, and no files are created.

Cross-cutting LLM calls that don't pass through the per-step `CallerForStep`
pipeline (title generation, ToolJudge) receive the dump writer via
`context.Context` using `agent.WithDumpWriter(ctx, w)` (backed by an
unexported key type in `sp4rk/agent/context.go`; read back with
`agent.DumpWriterFromContext`). The writers are injected at
the call sites: `SendMessage` for title generation and
`desktop/event_handlers.go` for ToolJudge.

## Core Types

```go
// SessionInfo — session metadata returned to frontend
type SessionInfo struct {
    ID               string
    ProjectID        string
    Name             string
    CreatedAt        string // RFC 3339
    LastActiveAt     string // RFC 3339
    Archived         bool
    Pinned           bool
    Active           bool
    TotalInputTokens  int
    TotalOutputTokens int
    Model            string
    Family           string
    FillPercent      float64 // context-fill percentage (stored in sessions.fill_percent)
    HasUnfinishedTask bool  // derived (not stored): EXISTS unfinished in_progress/failed task; gates the fork button
}

// HandleOptions — user-specified skill overrides + per-message model/reasoning/goal/review toggles
type HandleOptions struct {
    TaskID             string                     // non-empty = continuation of existing task
    UserSkills         []string                   // explicitly requested by user via /skill refs (bypass router)
    UserAgents         []string                   // explicitly requested by user via #agent-name mentions (drives the "Requested Subagents" prompt directive)
    ModelOverride      string                     // non-empty → use this model for all LLM calls; empty → router default
    ReasoningEffort    string                     // non-empty → native reasoning value for all LLM calls; empty → use family default
    SessionPlansDir    string                     // directory for session-scoped plan files (used by declare_plan tool)
    PendingAttachments []orchestration.Attachment // staged document attachments flushed into the blackboard before execution
    PendingImages      []llm.ContentBlock         // staged image attachments (base64 image blocks) injected into the context window (not the blackboard)
    ReviewMode         bool                       // true = message carries code-review feedback the agent must act on (sets ReviewModeKey)
    Goal               bool                       // true = dispatch to runGoalLoop (goal mode)
    GoalBudgetOverride *goal.GoalBudget           // optional per-message turn cap; MaxTurns>0 caps turns, nil/0 = unlimited (no config-level defaults)
}

// HandleResult — orchestration output
type HandleResult struct {
    Output          string                        `json:"output"`
    RoutingDecision *router.RoutingDecision       `json:"routing_decision"`
    Plan            *orchestration.Plan           `json:"plan,omitempty"`
    Blackboard      orchestration.Blackboard      `json:"-"`
    Reflections     []orchestration.Reflection    `json:"reflections,omitempty"`
    Status          orchestration.ExecutionStatus `json:"status,omitempty"` // typed outcome: success | partial | failed | aborted | cancelled
}
```

## Extension Points

- **SessionStore interface**: replace SQLite with a different backend by implementing all methods in `backend/session/persistence.go`
- **Auto-title generation**: customize the LLM prompt or model used for title generation in `backend/session/title.go`
- **Preprocessing pipeline**: add custom message transforms (e.g., additional filter types) before orchestrator invocation in `FrontendAPI.SendMessage()`
- **Event persistence**: implement `EventPersister` interface for alternative storage backends
- **Session metadata enrichment**: add custom fields to `SessionInfo` and populate them in `SessionManager.Create()`
- **File coherence strategy**: replace `FileCoherenceTracker` in `backend/session/file_coherence.go` with an alternative conflict detection implementation (must satisfy `FileCoherenceChecker` interface from `github.com/v0lka/sp4rk/tools/coherence.go`)

## Invariants

- One Orchestrator per session: `CreateSession` builds it eagerly via the factory; lazy creation applies only to the restart/restore path (`getOrRestoreSession`, on first access after a restart)
- `DeleteSession` cancels any running task, removes the in-memory session and cleans up the entire per-session
  directory (`~/.c0wrk/projects/__no_project__/<id>/`) for No Project
  sessions. The session temp directory is always cleaned up regardless.
- Session state survives app restart (SQLite persistence)
- Project switch session restore order is deterministic: valid saved session for destination project, otherwise latest destination session, otherwise new destination session
- Destination project switch state always persists the resolved `saved_session_id` in `project_ui_state` when project persistence is wired
- User messages are persisted after the authoritative dispatch, not on receive: the `is_nudge` flag is written only once the live-send/fresh classification is known, and a rejected send (pause window, goal gate, attachment gate) never reaches the store
- Archived sessions are read-only history: the session-manager choke point rejects both new `SendMessage` execution and failed/paused task resume until the session is unarchived
- Archiving a session that is running or has an unfinished task first cancels the running task and discards the unfinished task, so an archived session is a clean read-only snapshot. The session temp directory is removed once the task goroutine settles; if cancellation does not settle within `stopTimeout`, the archived flag still flips and the temp directory removal is deferred until the goroutine actually finishes (never racing a still-running tool call).
- Task state is checkpointed on each step completion (enables resume)
- Cancellation is cooperative (executor checks context at each iteration)
- An unfinished task (`in_progress` or `failed`) is continued by the next user
  message via `tryContinueInterruptedTask`: the message is appended as a
  user-nudge turn to the prior trajectory and `orchestrator.Resume` is invoked
  with **no routing, no new task, and no new conversation-history pair** (task
  ID preserved). Explicit `CancelUnfinishedTask` is the only path that closes
  it and lets the following message start fresh.
- `orchestrator.Resume` reconstructs the full prior trajectory (`resumeSteps`)
  and seeds it into both the ContextManager (`StepSeedable.SeedSteps`) and the
  Executor (`WithResumeSteps`): the step counter continues from
  `len(resumeSteps)+1` and the full trajectory syncs to the (persisted)
  TrajectoryStore on every step. A routing decision and a plan are **optional**
  — routing is reused if persisted (otherwise `general` domain), and a plan-less
  task runs the Conductor's standalone checklist.
- Under ADR-012 the router's `needs_clarification` flag is ignored — the
  Conductor handles clarification itself via the `ask_user` tool, so a
  router clarification decision never short-circuits the pipeline or closes
  a task on its own.
- The Cancel button on the resume prompt is a hard discard: it persists
  cancellation on the unfinished task without launching the orchestrator.
- Every cancellation path (CancelTask, mid-task ctx-cancel, resume-cancel)
  persists the task status as `cancelled` — never `completed` — so the
  persisted status always reflects the real outcome.
- Task outcome persistence follows the typed execution status
  (`orchestration.ExecutionStatus` → `core.persistTaskOutcome`):
  `success` → `completed`; `partial` → left `in_progress` (resumable);
  `failed`/`aborted` → `failed` (resumable); `cancelled` → handled by the
  session manager's cancellation paths.
- **Continuation reactivation is a commit-point side effect.** A continuation
  (`HandleMessage` with a `TaskID`) flips its anchor task back to
  `in_progress` (`ReactivateTask`) only once the continuation has actually
  committed to executing — after routing succeeded (normal path) or the goal
  loop is entered (goal path; `core.reactivateContinuationTask`). A failure
  BEFORE that point (blackboard restore, routing error) leaves the anchor's
  prior terminal status intact, so the manager's fresh-workflow fallback
  (`Manager.shouldRetryContinuationFresh`) cannot orphan a reactivated row.
  The guard classifies the failed attempt via a pre-send snapshot of the
  anchor's status: a terminal anchor that turns up unfinished after the
  attempt was reactivated by this send's own execution and failed mid-flight
  (no fresh retry — the resumable banner covers it), while an anchor that
  was already unfinished when the send began (a `lastCompletedTaskID`
  restored without a status check, pointing at a failed task whose resume
  path fell back on a restore error) retries fresh — the banner would
  dead-end on the same restore error, and the stale sweep below cancels the
  leftover row once the fresh run succeeds. This fixes the bug where a
  routing failure on a continuation reactivated the anchor and the fallback's
  fresh task left the anchor `in_progress` forever, pinning
  `has_unfinished_task=true` and re-injecting the "Task failed / Resume"
  banner after every restart over an otherwise completed session.
- **A successful completion sweeps stale unfinished rows.**
  `Manager.emitTaskComplete` on `success=true` cancels any leftover
  unfinished (`in_progress`/`paused`/`failed`) task rows in the session other
  than the just-completed task and resolves their persisted
  `task_failed_resumable` banners (`Manager.sweepStaleUnfinishedTasks`).
  Such rows are stale by construction — a session runs one task at a time
  and a fresh task starts only when nothing is unfinished — so this heals
  legacy databases already carrying orphaned rows. The sweep never runs on
  degraded completions, where the resumable banner is legitimate, and is
  skipped when the result carries no persistable blackboard (no completed
  task ID to shield from cancellation — an unfinished row may belong to the
  just-completed task, whose completion write raced). It is best-effort: the
  lookup returns one unfinished row at a time, so an older orphan sitting
  behind the just-completed task's own unfinished row stays for the next
  successful completion to cancel.
- Session forking deep-copies all dependent rows (messages, tasks and their
  steps/facts/attachments/trajectory/goal state, terminal commands, work directories,
  and review data — `task_goal_state` preserves the forked task's goal history)
  with freshly generated identifiers in a single atomic
  transaction, so the fork shares no rows with the original. A fork with any
  unfinished (`in_progress` or `failed`) task is rejected by
  `FrontendAPI.ForkSession` (backend guard) and the frontend fork button is
  disabled preemptively via `has_unfinished_task` plus the live active status.
- `task_complete` carries the typed success contract (`success`,
  `completion`, `failed_steps`). A degraded completion (`success=false`) is
  always followed by `task_failed_resumable` or a `service` warning — never
  delivered as a silent visual success (`Manager.emitTaskComplete`).
- `GetSessionRuntimeStatus(sessionID)` exposes `{active,
  has_unfinished_task, unfinished_task_id, paused, activity, streaming}`; the frontend calls it after
  every history load to reconcile UI state (running/paused flags, resume banner,
  stale step_limit prompts) instead of defaulting to idle. The `activity` /
  `streaming` fields are the backend-tracked live snapshot (emitter
  `activityState`): `activity` is the last user-facing phase label
  ("Thinking...", "Routing request...", ...) and `streaming` reports an open
  assistant stream. The reconcile uses them to replace a session's frozen
  activity label and clear stale streaming text after a session/project switch
  — events emitted while no frontend listener existed must not leave the UI
  showing "Routing request..." over a ReAct loop that long moved past it, nor a
  frozen partial answer from a stream that already finished in the background.
  A stale-snapshot guard bounds the reverse race: when a live event updates the
  activity label or streaming text after the frontend read the status snapshot
  (the event subscription mounts before the RPC resolves), the reconcile keeps
  the live label/stream and skips only that application; the running/paused
  flags and prompt resolution still come from the snapshot, which stays
  authoritative for them.
- `GetSessionTokens(sessionID)` overlays the live emitter token snapshot
  (used/max context-window tokens, fresh fill percent, model) over the
  persisted session row when the session is in memory, so the status bar's
  context-fill badge survives a switch back to a running session.
- Per-step context-fill badges (`stepContextFill`) are keyed by session then
  step id and survive session switches (A→B→A); `plan_generated` invalidates
  the session's fills because plan step ids are reused by every new plan in
  the same session.
- Clipboard paste resolves image → copied file URLs → plain text; platform probe failures fall through, while a present image remains authoritative even when vision support rejects it.
- Picker and native-drop paths share `useStageAttachments`; native `files:dropped` supplies filesystem paths while HTML5 drag/drop is limited to overlay state and navigation suppression.
- One PTY is active per session ID; session/project switches preserve it, `StartTerminal` reattaches, and only explicit stop/restart or application shutdown terminates it.
- A `plan_review_ready` event (emitted by `declare_plan` await_approval) is persisted to `session_messages` (role `plan_review`) so it reappears in history after a restart; the pending plan approval is also surfaced via `GetPendingActions` (`desktop/pending_actions.go` `PlanApprovals`)
- Plan files live at `~/.c0wrk/projects/<pid>/<sid>/plans/` (written by `declare_plan`); previous plan files are not deleted when the Conductor revises (request_changes) or abandons
- `DeleteSession` closes all per-step dump files via `Orchestrator.Cleanup()` before closing the session-level log and dump files
- `Shutdown` marks shutdown before cancellation, waits for active task goroutines, then converts every still-`in_progress` active task to a persisted `paused` checkpoint; already failed/completed tasks retain their terminal state. It also closes all per-step dump files for every session via `Orchestrator.Cleanup()` before closing session resources.
- Pending attachments are flushed into the blackboard exactly once on `SendMessage`: the session manager snapshots and clears `session.pendingAttachments`, then passes them via `HandleOptions.PendingAttachments` (both HandleMessage calls in `SendMessage` receive the same snapshot)

## Configuration

| Parameter                          | Default                | Description                 |
| ---------------------------------- | ---------------------- | --------------------------- |
| `ConductorHistoryWindow` (internal default) | 20                     | Conversation history window injected into the Conductor context (`OrchestratorConfig`, applied when 0) |
| Database path                      | `~/.c0wrk/database.db` | SQLite file location        |

## Related Specs

- [orchestration/README.md](orchestration/README.md) — orchestration cycle
- [memory/blackboard.md](memory/blackboard.md) — blackboard persistence
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — session RPC methods
- [../contracts/event-catalog.md](../contracts/event-catalog.md) — task lifecycle events
