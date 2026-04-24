# State Management

<cite>
**Referenced Files in This Document**
- [chatStore.ts](file://frontend/src/stores/chatStore.ts)
- [sessionStore.ts](file://frontend/src/stores/sessionStore.ts)
- [projectStore.ts](file://frontend/src/stores/projectStore.ts)
- [settingsStore.ts](file://frontend/src/stores/settingsStore.ts)
- [planStore.ts](file://frontend/src/stores/planStore.ts)
- [events.go](file://desktop/events.go)
- [wails.ts](file://frontend/src/lib/wails.ts)
- [useWails.ts](file://frontend/src/hooks/useWails.ts)
- [useSessionEvents.ts](file://frontend/src/hooks/useSessionEvents.ts)
- [usePlanEvents.ts](file://frontend/src/hooks/events/usePlanEvents.ts)
- [useProjectLoader.ts](file://frontend/src/hooks/useProjectLoader.ts)
- [useSessionLoader.ts](file://frontend/src/hooks/useSessionLoader.ts)
- [emitter.go](file://backend/session/emitter.go)
- [app.go](file://desktop/app.go)
- [App.tsx](file://frontend/src/App.tsx)
- [AppLayout.tsx](file://frontend/src/components/layout/AppLayout.tsx)
- [ChatArea.tsx](file://frontend/src/components/chat/ChatArea.tsx)
- [ChatScrollManager.tsx](file://frontend/src/components/chat/ChatScrollManager.tsx)
- [ScrollContext.tsx](file://frontend/src/components/chat/ScrollContext.tsx)
- [Sidebar.tsx](file://frontend/src/components/layout/Sidebar.tsx)
- [ChatInput.tsx](file://frontend/src/components/chat/ChatInput.tsx)
- [chatStore.test.ts](file://frontend/src/stores/chatStore.test.ts)
- [models.ts](file://frontend/src/types/models.ts)
</cite>

## Update Summary
**Changes Made**
- Added new planStore.ts with dedicated plan management system for execution plans and step statuses
- Replaced panelStore.ts with plan-based architecture using planStore for session statistics
- Removed scrollStore.ts and replaced with ScrollContext.tsx provider pattern
- Updated hooks useProjectLoader.ts and useSessionLoader.ts for initialization and lifecycle management
- Enhanced useSessionEvents.ts to integrate plan store alongside chat store
- Updated ChatArea.tsx and ChatScrollManager.tsx to use ScrollProvider pattern

## Table of Contents
1. [Introduction](#introduction)
2. [Project Structure](#project-structure)
3. [Core Components](#core-components)
4. [Architecture Overview](#architecture-overview)
5. [Detailed Component Analysis](#detailed-component-analysis)
6. [Dependency Analysis](#dependency-analysis)
7. [Performance Considerations](#performance-considerations)
8. [Troubleshooting Guide](#troubleshooting-guide)
9. [Conclusion](#conclusion)

## Introduction
This document explains C0WRK's state management architecture built around custom Zustand stores. It covers:
- Chat store for conversation and execution timeline state
- Session store for workspace and project session lifecycle
- Project store for project lifecycle management
- Settings store for modal and UI preferences
- **New** Plan store for execution plan management with step statuses and session statistics
- Store initialization and update patterns
- Inter-store communication and cross-component usage
- Integration with the Wails backend for real-time event-driven updates and persistence
- Examples of store usage, selectors, and async updates
- Best practices for desktop application state management

## Project Structure
C0WRK organizes state in the frontend under a dedicated stores directory, with hooks bridging to the Wails backend and UI components subscribing to store slices. The backend emits structured events consumed by the frontend to keep UI state synchronized. **Updated** with new plan management architecture and scroll context provider.

```mermaid
graph TB
subgraph "Frontend"
UI["React Components<br/>Sidebar, ChatInput, App"]
Hooks["Hooks<br/>useWails, useSessionEvents, useProjectLoader, useSessionLoader"]
Stores["Zustand Stores<br/>chatStore, sessionStore, projectStore, settingsStore, planStore"]
Types["Type Definitions<br/>wails.ts, models.ts"]
Scroll["Scroll Context<br/>ScrollProvider, ScrollContext"]
end
subgraph "Backend"
Emitter["EventEmitter<br/>emitter.go"]
Desktop["Desktop App<br/>app.go"]
Events["Event Names<br/>events.go"]
end
UI --> Hooks
Hooks --> Stores
Hooks --> Types
Hooks --> Scroll
Desktop --> Emitter
Emitter --> Events
Events --> Hooks
Hooks --> UI
```

**Diagram sources**
- [useWails.ts:1-61](file://frontend/src/hooks/useWails.ts#L1-L61)
- [useSessionEvents.ts:1-48](file://frontend/src/hooks/useSessionEvents.ts#L1-L48)
- [useProjectLoader.ts:1-76](file://frontend/src/hooks/useProjectLoader.ts#L1-L76)
- [useSessionLoader.ts:1-51](file://frontend/src/hooks/useSessionLoader.ts#L1-L51)
- [chatStore.ts:1-249](file://frontend/src/stores/chatStore.ts#L1-L249)
- [sessionStore.ts:1-52](file://frontend/src/stores/sessionStore.ts#L1-L52)
- [projectStore.ts:1-44](file://frontend/src/stores/projectStore.ts#L1-L44)
- [settingsStore.ts:1-20](file://frontend/src/stores/settingsStore.ts#L1-L20)
- [planStore.ts:1-100](file://frontend/src/stores/planStore.ts#L1-L100)
- [ScrollContext.tsx:1-37](file://frontend/src/components/chat/ScrollContext.tsx#L1-L37)
- [wails.ts:1-205](file://frontend/src/lib/wails.ts#L1-L205)
- [models.ts:66-82](file://frontend/src/types/models.ts#L66-L82)
- [emitter.go:1-668](file://backend/session/emitter.go#L1-L668)
- [events.go:1-46](file://desktop/events.go#L1-L46)
- [app.go:1-73](file://desktop/app.go#L1-L73)

**Section sources**
- [useWails.ts:1-61](file://frontend/src/hooks/useWails.ts#L1-L61)
- [useSessionEvents.ts:1-48](file://frontend/src/hooks/useSessionEvents.ts#L1-L48)
- [useProjectLoader.ts:1-76](file://frontend/src/hooks/useProjectLoader.ts#L1-L76)
- [useSessionLoader.ts:1-51](file://frontend/src/hooks/useSessionLoader.ts#L1-L51)
- [chatStore.ts:1-249](file://frontend/src/stores/chatStore.ts#L1-L249)
- [sessionStore.ts:1-52](file://frontend/src/stores/sessionStore.ts#L1-L52)
- [projectStore.ts:1-44](file://frontend/src/stores/projectStore.ts#L1-L44)
- [settingsStore.ts:1-20](file://frontend/src/stores/settingsStore.ts#L1-L20)
- [planStore.ts:1-100](file://frontend/src/stores/planStore.ts#L1-L100)
- [ScrollContext.tsx:1-37](file://frontend/src/components/chat/ScrollContext.tsx#L1-L37)
- [wails.ts:1-205](file://frontend/src/lib/wails.ts#L1-L205)
- [models.ts:66-82](file://frontend/src/types/models.ts#L66-L82)
- [emitter.go:1-668](file://backend/session/emitter.go#L1-L668)
- [events.go:1-46](file://desktop/events.go#L1-L46)
- [app.go:1-73](file://desktop/app.go#L1-L73)

## Core Components
- Chat store: Manages per-session messages, streaming assistant text, thinking/task flags, plan grouping, and context fill metrics. Provides helpers to group raw events into display-friendly items and pending actions.
- Session store: Tracks available sessions, active session, and supports add/update/remove with activity-aware sorting.
- Project store: Tracks available projects, active project, and supports add/update/remove with activity-aware sorting.
- Settings store: Controls the settings modal open state and active tab.
- **New** Plan store: Manages execution plans with step statuses, completion tracking, and session statistics. Provides selectors for plan progress and step management.

Key patterns:
- Each store is a Zustand slice with a typed state interface and action methods.
- Stores expose selectors for efficient component subscriptions.
- Stores are used directly by UI components and indirectly via hooks.
- **Updated** Plan store integrates with chat store for plan visualization and execution tracking.

**Section sources**
- [chatStore.ts:104-249](file://frontend/src/stores/chatStore.ts#L104-L249)
- [sessionStore.ts:15-52](file://frontend/src/stores/sessionStore.ts#L15-L52)
- [projectStore.ts:15-44](file://frontend/src/stores/projectStore.ts#L15-L44)
- [settingsStore.ts:3-20](file://frontend/src/stores/settingsStore.ts#L3-L20)
- [planStore.ts:13-99](file://frontend/src/stores/planStore.ts#L13-L99)

## Architecture Overview
The architecture is event-driven and desktop-native with enhanced plan management:
- Backend emits structured events via an emitter that injects plan-scoped and retry-scoped metadata.
- Wails runtime forwards events to the frontend with typed payloads.
- Frontend hooks subscribe to session-scoped and global events, updating Zustand stores.
- **Updated** useSessionEvents now coordinates both chat store and plan store for comprehensive session state management.
- **Updated** ScrollProvider pattern replaces scrollStore for scroll management.

```mermaid
sequenceDiagram
participant Backend as "Backend Emitter<br/>emitter.go"
participant Runtime as "Wails Runtime"
participant Hooks as "useSessionEvents<br/>useSessionEvents.ts"
participant Chat as "Chat Store<br/>chatStore.ts"
participant Plan as "Plan Store<br/>planStore.ts"
Backend->>Runtime : "session : {sessionId} : plan_generated"<br/>with steps array
Runtime-->>Hooks : Event payload
Hooks->>Chat : setActivityStatus("Executing plan...")
Hooks->>Plan : setPlan(planGroup)
Hooks->>Chat : addMessage(sessionId, plan)
Backend->>Runtime : "session : {sessionId} : plan_step_start/complete"
Runtime-->>Hooks : Event payload
Hooks->>Plan : updateStepStatus(stepId, status, duration)
Hooks->>Chat : addMessage(plan_step_start/complete)
Backend->>Runtime : "session : {sessionId} : assistant_chunk/done"
Runtime-->>Hooks : Event payload
Hooks->>Chat : setStreaming / appendStreamToken<br/>or addMessage(assistant)
```

**Diagram sources**
- [emitter.go:307-369](file://backend/session/emitter.go#L307-L369)
- [emitter.go:473-530](file://backend/session/emitter.go#L473-L530)
- [useSessionEvents.ts:15-47](file://frontend/src/hooks/useSessionEvents.ts#L15-L47)
- [usePlanEvents.ts:23-103](file://frontend/src/hooks/events/usePlanEvents.ts#L23-L103)
- [useSessionEvents.ts:22-28](file://frontend/src/hooks/useSessionEvents.ts#L22-L28)
- [chatStore.ts:104-249](file://frontend/src/stores/chatStore.ts#L104-L249)
- [planStore.ts:57-99](file://frontend/src/stores/planStore.ts#L57-L99)

**Section sources**
- [emitter.go:1-668](file://backend/session/emitter.go#L1-L668)
- [useSessionEvents.ts:1-48](file://frontend/src/hooks/useSessionEvents.ts#L1-L48)
- [usePlanEvents.ts:1-104](file://frontend/src/hooks/events/usePlanEvents.ts#L1-L104)
- [events.go:1-46](file://desktop/events.go#L1-L46)

## Detailed Component Analysis

### Chat Store
Purpose:
- Centralized per-session conversation and execution timeline state.
- Streaming assistant text handling.
- Plan grouping and pending action detection.
- Context fill metrics per step and session totals.

Key capabilities:
- Message lifecycle: add, update, replace, clear.
- Streaming: setStreaming and appendStreamToken.
- Thinking/task flags and activity status.
- Pending actions: tool_confirm, ask_user, step_limit, task_failed_resumable.
- Grouping: transforms raw events into display items and pending actions.

```mermaid
flowchart TD
Start(["Incoming Message"]) --> Type{"Message Type"}
Type --> |tool_call| ToolCall["Create tool_call message<br/>with tool_call_id or composite key"]
Type --> |tool_result| ToolResult["Find matching tool_call by tool_call_id<br/>or composite key<br/>Update metadata.completed/result"]
Type --> |plan_generated| PlanGen["Set activity status<br/>Create planGroup<br/>Add plan message"]
Type --> |plan_step_start| PlanStart["Add plan_step_start message<br/>Update activity status"]
Type --> |plan_step_complete| PlanComplete["Add plan_step_complete message<br/>Update step status"]
Type --> |assistant_chunk| Stream["setStreaming or appendStreamToken"]
Type --> |assistant_done| Done["Add assistant message<br/>clear streaming"]
Type --> |error| Error["Add error message"]
Type --> |routing/retry/step_retry/status| Service["Add service message"]
ToolCall --> Group["Group into DisplayItem[]"]
ToolResult --> Group
PlanGen --> Group
PlanStart --> Group
PlanComplete --> Group
Stream --> Group
Done --> Group
Error --> Group
Service --> Group
Group --> Pending["Build pendingActions[]"]
Pending --> End(["Render Timeline"])
```

**Diagram sources**
- [chatStore.ts:114-249](file://frontend/src/stores/chatStore.ts#L114-L249)
- [usePlanEvents.ts:30-58](file://frontend/src/hooks/events/usePlanEvents.ts#L30-L58)
- [usePlanEvents.ts:62-74](file://frontend/src/hooks/events/usePlanEvents.ts#L62-L74)
- [usePlanEvents.ts:78-98](file://frontend/src/hooks/events/usePlanEvents.ts#L78-L98)

Usage examples:
- UI components subscribe to isThinking, streamingText, and messages via selectors.
- Pending actions are extracted with extractPendingActions for actionable panels.
- **Updated** Plan messages are integrated into the chat timeline for execution visibility.

Best practices:
- Always correlate tool_result with tool_call using tool_call_id when available.
- Keep plan step containers separate from top-level items.
- Clear streaming state on assistant_done.
- **Updated** Handle plan-generated messages alongside regular chat messages.

**Section sources**
- [chatStore.ts:1-249](file://frontend/src/stores/chatStore.ts#L1-L249)
- [usePlanEvents.ts:1-104](file://frontend/src/hooks/events/usePlanEvents.ts#L1-L104)
- [chatStore.test.ts:1-403](file://frontend/src/stores/chatStore.test.ts#L1-L403)

### Plan Store
**New** Purpose:
- Dedicated management of execution plans with step statuses and completion tracking.
- Session statistics tracking for routing domain, complexity, and attempt counts.
- Real-time plan progression monitoring and step status updates.

Key capabilities:
- Plan lifecycle: setPlan, clearPlan, clearAll.
- Step management: updateStepStatus, addStepToCurrentPlan.
- Statistics tracking: setSessionStats with partial updates.
- Progress calculation: selectors for completed/total step counts.

```mermaid
flowchart TD
Start(["Plan Event"]) --> Type{"Event Type"}
Type --> |plan_generated| CreatePlan["setPlan(planGroup)<br/>Create new planGroup<br/>Calculate completedCount"]
Type --> |plan_step_start| StartStep["updateStepStatus(stepId, 'running')"]
Type --> |plan_step_complete| CompleteStep["updateStepStatus(stepId, status, duration)"]
Type --> |add_step| AddStep["addStepToCurrentPlan(step)<br/>Append to items array"]
Type --> |session_stats| Stats["setSessionStats(sessionId, stats)<br/>Merge partial stats"]
CreatePlan --> Progress["Calculate progress<br/>Update completedCount"]
StartStep --> Running["Mark step as running"]
CompleteStep --> Status["Update step status<br/>Add duration"]
AddStep --> Items["Append step to items"]
Stats --> Session["Update session statistics"]
Progress --> Render["Render plan view"]
Running --> Render
Status --> Render
Items --> Render
Session --> Render
```

**Diagram sources**
- [planStore.ts:57-99](file://frontend/src/stores/planStore.ts#L57-L99)
- [planStore.ts:29-53](file://frontend/src/stores/planStore.ts#L29-L53)

Usage examples:
- **New** usePlanCompleted() and usePlanTotal() selectors for progress tracking.
- **New** Integration with usePlanEvents for real-time plan updates.
- **New** Session statistics stored per sessionId for routing and complexity analysis.

Best practices:
- Use updateStepStatus for atomic step state changes with optional duration tracking.
- Leverage selectors for efficient progress calculations without scanning all items.
- Maintain planGroups in newest-first order for easy access to current plan.

**Section sources**
- [planStore.ts:1-100](file://frontend/src/stores/planStore.ts#L1-L100)
- [models.ts:66-82](file://frontend/src/types/models.ts#L66-L82)

### Session Store
Purpose:
- Manage workspace sessions lifecycle and selection.
- Maintain sorted order by last_active_at with helper sorting.

Key capabilities:
- CRUD operations with activity-aware sorting.
- Active session tracking.
- Touch operation to refresh last_active_at.

Usage examples:
- Sidebar lists sessions and allows create/rename/archive/delete.
- ChatInput disables input until a session is active.

**Section sources**
- [sessionStore.ts:1-52](file://frontend/src/stores/sessionStore.ts#L1-L52)
- [Sidebar.tsx:64-98](file://frontend/src/components/layout/Sidebar.tsx#L64-L98)
- [ChatInput.tsx:13-32](file://frontend/src/components/chat/ChatInput.tsx#L13-L32)

### Project Store
Purpose:
- Manage project lifecycle and selection.
- Maintain sorted order by last_active_at.

Key capabilities:
- CRUD operations with activity-aware sorting.
- Active project tracking.

Usage examples:
- Sidebar lists projects and allows create/rename/delete.
- Session operations are gated by active project.

**Section sources**
- [projectStore.ts:1-44](file://frontend/src/stores/projectStore.ts#L1-L44)
- [Sidebar.tsx:64-98](file://frontend/src/components/layout/Sidebar.tsx#L64-L98)

### Settings Store
Purpose:
- Control settings modal open state and active tab.

Usage examples:
- Settings modal toggles open/close and switches tabs.

**Section sources**
- [settingsStore.ts:1-20](file://frontend/src/stores/settingsStore.ts#L1-L20)

### Scroll Context System
**New** Purpose:
- Replace scrollStore.ts with React Context provider pattern for scroll management.
- Centralized scroll control with scrollToStep functionality.

Key capabilities:
- ScrollToStep callback registration and invocation.
- Provider pattern for component tree integration.
- Context-based scroll state management.

Usage examples:
- **Updated** ChatScrollManager uses ScrollProvider for scroll-to-step functionality.
- Components register scroll callbacks via setScrollToStep.
- Smooth scrolling to specific step elements.

**Section sources**
- [ScrollContext.tsx:1-37](file://frontend/src/components/chat/ScrollContext.tsx#L1-L37)
- [ChatScrollManager.tsx:1-109](file://frontend/src/components/chat/ChatScrollManager.tsx#L1-L109)
- [ChatArea.tsx:124-144](file://frontend/src/components/chat/ChatArea.tsx#L124-L144)

### Loader Hooks
**New** Purpose:
- Handle initialization and lifecycle management for project and session data.
- Manage backend readiness and event subscriptions.

Key capabilities:
- useProjectLoader: Initial project loading and project lifecycle events.
- useSessionLoader: Session loading when active project changes.
- Backend event subscriptions for real-time updates.

Usage examples:
- **Updated** App.tsx integrates both loader hooks for comprehensive initialization.
- Automatic project activation and session selection.
- Real-time project and session updates via backend events.

**Section sources**
- [useProjectLoader.ts:1-76](file://frontend/src/hooks/useProjectLoader.ts#L1-L76)
- [useSessionLoader.ts:1-51](file://frontend/src/hooks/useSessionLoader.ts#L1-L51)
- [App.tsx:16-31](file://frontend/src/App.tsx#L16-L31)

### Inter-Store Communication and Cross-Component Usage
- UI components subscribe to multiple stores via selectors (e.g., Sidebar consumes projectStore, sessionStore, settingsStore).
- **Updated** useSessionEvents coordinates both chat store and plan store for comprehensive session state management.
- **Updated** ScrollProvider enables scroll-to-step functionality across components.
- Global non-session events (e.g., vector index status) update specialized stores via App-level listeners.

**Section sources**
- [Sidebar.tsx:64-98](file://frontend/src/components/layout/Sidebar.tsx#L64-L98)
- [App.tsx:21-91](file://frontend/src/App.tsx#L21-L91)
- [useSessionEvents.ts:1-48](file://frontend/src/hooks/useSessionEvents.ts#L1-L48)
- [ChatArea.tsx:124-144](file://frontend/src/components/chat/ChatArea.tsx#L124-L144)

### Backend Integration and Event-Driven Updates
- Backend emitter injects plan_step_id and retry_attempt into event data for scoping.
- tool_call_id is generated globally and used to correlate tool_call and tool_result.
- Wails runtime exposes typed payloads for frontend consumption.
- Frontend hooks subscribe to session-scoped channels and update stores accordingly.
- **Updated** usePlanEvents handles plan-specific events alongside other event types.

```mermaid
classDiagram
class EventEmitter {
+WithPlanStepID(id)
+WithRetryAttempt(attempt)
+ToolCall(step, callIdx, tool, args, source)
+ToolResult(step, callIdx, resultLen, preview)
+AssistantChunk(content)
+AssistantDone(fullContent, inputTokens, outputTokens)
+ContextFill(fillPercent, usedTokens, maxTokens, status, stepID)
+PlanGenerated(steps, progress, completedCount, totalCount)
+PlanStepStart(step_id, description, summary, depends_on)
+PlanStepComplete(step_id, success, duration, error)
+EmitSessionTokens(totalIn, totalOut, model, family)
}
class EventNames {
+EventProjectsLoaded
+EventProjectCreated
+EventProjectDeleted
+EventProjectSwitched
+EventSessionsLoaded
+EventSessionEvent
}
EventEmitter --> EventNames : "emits"
```

**Diagram sources**
- [emitter.go:107-135](file://backend/session/emitter.go#L107-L135)
- [emitter.go:307-369](file://backend/session/emitter.go#L307-L369)
- [emitter.go:473-530](file://backend/session/emitter.go#L473-L530)
- [emitter.go:563-594](file://backend/session/emitter.go#L563-L594)
- [events.go:7-46](file://desktop/events.go#L7-L46)

**Section sources**
- [emitter.go:1-668](file://backend/session/emitter.go#L1-L668)
- [events.go:1-46](file://desktop/events.go#L1-L46)

## Dependency Analysis
- UI components depend on hooks and stores for state.
- Hooks depend on Wails runtime and typed payloads.
- **Updated** Stores coordinate through useSessionEvents for comprehensive session state management.
- **Updated** ScrollProvider enables scroll functionality without centralized scroll store.
- Backend depends on emitter and persists token totals via callbacks.

```mermaid
graph LR
UI["UI Components"] --> Hooks["useWails, useSessionEvents, useProjectLoader, useSessionLoader"]
Hooks --> Stores["Stores"]
Hooks --> Types["wails.ts, models.ts"]
Hooks --> Scroll["ScrollProvider"]
Backend["Backend App"] --> Emitter["EventEmitter"]
Emitter --> Events["events.go"]
Events --> Hooks
```

**Diagram sources**
- [useWails.ts:1-61](file://frontend/src/hooks/useWails.ts#L1-L61)
- [useSessionEvents.ts:1-48](file://frontend/src/hooks/useSessionEvents.ts#L1-L48)
- [useProjectLoader.ts:1-76](file://frontend/src/hooks/useProjectLoader.ts#L1-L76)
- [useSessionLoader.ts:1-51](file://frontend/src/hooks/useSessionLoader.ts#L1-L51)
- [chatStore.ts:1-249](file://frontend/src/stores/chatStore.ts#L1-L249)
- [sessionStore.ts:1-52](file://frontend/src/stores/sessionStore.ts#L1-L52)
- [projectStore.ts:1-44](file://frontend/src/stores/projectStore.ts#L1-L44)
- [settingsStore.ts:1-20](file://frontend/src/stores/settingsStore.ts#L1-L20)
- [planStore.ts:1-100](file://frontend/src/stores/planStore.ts#L1-L100)
- [ScrollContext.tsx:1-37](file://frontend/src/components/chat/ScrollContext.tsx#L1-L37)
- [wails.ts:1-205](file://frontend/src/lib/wails.ts#L1-L205)
- [models.ts:66-82](file://frontend/src/types/models.ts#L66-L82)
- [emitter.go:1-668](file://backend/session/emitter.go#L1-L668)
- [events.go:1-46](file://desktop/events.go#L1-L46)
- [app.go:1-73](file://desktop/app.go#L1-L73)

**Section sources**
- [useWails.ts:1-61](file://frontend/src/hooks/useWails.ts#L1-L61)
- [useSessionEvents.ts:1-48](file://frontend/src/hooks/useSessionEvents.ts#L1-L48)
- [useProjectLoader.ts:1-76](file://frontend/src/hooks/useProjectLoader.ts#L1-L76)
- [useSessionLoader.ts:1-51](file://frontend/src/hooks/useSessionLoader.ts#L1-L51)
- [chatStore.ts:1-249](file://frontend/src/stores/chatStore.ts#L1-L249)
- [sessionStore.ts:1-52](file://frontend/src/stores/sessionStore.ts#L1-L52)
- [projectStore.ts:1-44](file://frontend/src/stores/projectStore.ts#L1-L44)
- [settingsStore.ts:1-20](file://frontend/src/stores/settingsStore.ts#L1-L20)
- [planStore.ts:1-100](file://frontend/src/stores/planStore.ts#L1-L100)
- [ScrollContext.tsx:1-37](file://frontend/src/components/chat/ScrollContext.tsx#L1-L37)
- [wails.ts:1-205](file://frontend/src/lib/wails.ts#L1-L205)
- [models.ts:66-82](file://frontend/src/types/models.ts#L66-L82)
- [emitter.go:1-668](file://backend/session/emitter.go#L1-L668)
- [events.go:1-46](file://desktop/events.go#L1-L46)
- [app.go:1-73](file://desktop/app.go#L1-L73)

## Performance Considerations
- Prefer stable selectors to avoid unnecessary re-renders.
- Use targeted updates (e.g., setStreaming vs appendStreamToken) to minimize churn.
- Keep plan grouping logic efficient; avoid repeated scans by leveraging indices (e.g., plan step map).
- Debounce or batch UI updates for frequent events (e.g., assistant_chunk).
- Persist token totals via backend callbacks to avoid UI thrash.
- **Updated** Use planStore selectors for efficient progress calculations instead of manual item scanning.
- **Updated** ScrollProvider pattern reduces memory overhead compared to centralized scroll store.

## Troubleshooting Guide
Common issues and resolutions:
- Missing tool_call_id: Backend generates tool_call_id globally; frontend falls back to composite keys for backward compatibility.
- Out-of-order tool_result arrival: chatStore buffers tool_result until tool_call arrives; ensure both are correlated by the same key.
- Session not active: useSessionEvents checks active session before updating UI state; ensure session is active before expecting updates.
- Startup errors: App listens for startup_error and displays a banner; inspect error payload for diagnostics.
- Vector index status: Non-session events update vector index store; ensure runtime subscription is active.
- **Updated** Plan not displaying: Ensure plan_generated events are received and usePlanEvents is subscribed to the active session.
- **Updated** Scroll not working: Verify ScrollProvider is wrapping ChatArea and scrollToStep callback is registered.

**Section sources**
- [useSessionEvents.ts:15-37](file://frontend/src/hooks/useSessionEvents.ts#L15-L37)
- [useSessionEvents.ts:177-264](file://frontend/src/hooks/useSessionEvents.ts#L177-L264)
- [App.tsx:21-91](file://frontend/src/App.tsx#L21-L91)
- [usePlanEvents.ts:23-103](file://frontend/src/hooks/events/usePlanEvents.ts#L23-L103)
- [ChatArea.tsx:124-144](file://frontend/src/components/chat/ChatArea.tsx#L124-L144)

## Conclusion
C0WRK's state management leverages lightweight, focused Zustand stores coordinated by Wails-backed event streams. The chat store centralizes complex rendering logic, while session and project stores manage lifecycle and selection. **Updated** The new plan store provides dedicated execution plan management with step status tracking and session statistics. The architecture emphasizes:
- Event-driven updates with strong typing
- Correlation of asynchronous events (tool_call/tool_result)
- Efficient UI subscriptions via selectors
- Persistence callbacks for token totals
- **Updated** Comprehensive plan management with real-time execution tracking
- **Updated** Modern React patterns with Context providers instead of centralized stores
- Clear separation of concerns across stores and hooks

This foundation enables robust, real-time collaboration between the desktop backend and the React frontend with enhanced plan execution visualization and scroll management.